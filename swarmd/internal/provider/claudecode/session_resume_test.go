package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// fakeCLICall is one recorded invocation of the fixture CLI.
type fakeCLICall struct {
	Args      []string `json:"args"`
	Cwd       string   `json:"cwd"`
	Resume    string   `json:"resume"`
	SID       string   `json:"sid"`
	Input     string   `json:"input"`
	Queued    string   `json:"queued"`
	Tool      string   `json:"tool"`
	PromptDir string   `json:"prompt_dir"`
}

func (c fakeCLICall) has(flag string) bool {
	for _, arg := range c.Args {
		if arg == flag {
			return true
		}
	}
	return false
}

// cliFixture is a process-level stand-in for the claude CLI. It records argv,
// working directory and stdin, reports a session id in system/init, and calls
// the configured MCP tool only when it was given an --mcp-config. Python is a
// fixture prerequisite only; no live CLI/provider behavior is claimed.
type cliFixture struct {
	t        *testing.T
	root     string
	state    string
	dbPath   string
	store    *pebblestore.Store
	auth     *pebblestore.AuthStore
	sec      *security.Service
	endpoint *SessionMCP
	server   *httptest.Server
	addr     string
	cli      *CLI
	runner   *Runner
	p        identity.Principal
	ctx      context.Context
}

func newCLIFixture(t *testing.T) *cliFixture {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("fake CLI requires python3:", err)
	}
	f := &cliFixture{t: t, root: t.TempDir()}
	f.state = filepath.Join(f.root, "state")
	f.dbPath = filepath.Join(f.root, "db")
	if err = os.MkdirAll(f.state, 0o700); err != nil {
		t.Fatal(err)
	}
	f.p = testPrincipal()
	f.p.Type = identity.PrincipalTypeUser
	ctx, cancel := context.WithTimeout(identity.ContextWithPrincipal(context.Background(), f.p), 60*time.Second)
	t.Cleanup(cancel)
	f.ctx = ctx
	f.openStore()
	if _, err = f.auth.UpsertCredential(pebblestore.AuthCredentialInput{Provider: ProviderID, AccountScopeID: f.p.AccountScopeID, Type: pebblestore.AuthTypeCLI, SetActive: true}); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(f.root, "fake-claude")
	source := `#!` + python + `
import sys,json,urllib.request,pathlib,os
STATE=pathlib.Path(` + fmt.Sprintf("%q", f.state) + `)
args=sys.argv[1:]
def arg(name): return args[args.index(name)+1] if name in args else ''
config=arg('--mcp-config'); resume=arg('--resume'); prompt=arg('--system-prompt-file')
if '--strict-mcp-config' not in args: raise RuntimeError('missing CLI isolation flag')
if (STATE/'provider-error').exists():
 with open(STATE/'attempts','a') as out: out.write('attempt\n')
 print('provider rate limit',file=sys.stderr); sys.exit(1)
if resume and (STATE/'missing').exists():
 (STATE/'missing').unlink()
 print('No conversation found with session ID: '+resume,file=sys.stderr); sys.exit(1)
line=sys.stdin.readline()
calls=STATE/'calls'
n=len(calls.read_text().splitlines()) if calls.exists() else 0
sid=resume or ('cli-%d'%n)
print(json.dumps({'type':'system','subtype':'init','session_id':sid,'model':'fixture'}),flush=True)
tool=''
if config:
 c=json.load(open(config))['mcpServers']['swarm']
 req=urllib.request.Request(c['url'],data=json.dumps({'jsonrpc':'2.0','id':1,'method':'tools/call','params':{'name':'bash','arguments':{},'_meta':{'claudecode/toolUseId':'fixture-%d'%n}}}).encode(),headers={**c['headers'],'Content-Type':'application/json'})
 tool=json.load(urllib.request.urlopen(req,timeout=5))['result']['content'][0]['text']
queued=sys.stdin.readline() if (STATE/'queue').exists() else ''
with open(calls,'a') as out: out.write(json.dumps({'args':args,'cwd':os.getcwd(),'resume':resume,'sid':sid,'input':line,'queued':queued,'tool':tool,'prompt_dir':os.path.dirname(prompt)})+'\n')
print(json.dumps({'type':'result','subtype':'success','result':'done'}),flush=True)
sys.stdin.read()
`
	if err = os.WriteFile(script, []byte(source), 0o700); err != nil {
		t.Fatal(err)
	}
	f.cli = NewCLI()
	f.cli.lookupEnv = func(key string) (string, bool) {
		if key == CLIPathEnv {
			return script, true
		}
		return os.LookupEnv(key)
	}
	f.endpoint = newTestSessionMCP(f.sec, &testLocation{root: filepath.Join(f.root, "claudecode")})
	f.startServer("127.0.0.1:0")
	f.runner = NewRunner(f.auth, f.cli)
	f.runner.SetSessionMCP(f.endpoint)
	t.Cleanup(func() { f.server.Close(); f.store.Close() })
	return f
}

func (f *cliFixture) openStore() {
	store, err := pebblestore.Open(f.dbPath)
	if err != nil {
		f.t.Fatal(err)
	}
	f.store = store
	f.auth = pebblestore.NewAuthStore(store)
	f.sec = security.NewService(pebblestore.NewClientAuthStore(store), nil)
}

func (f *cliFixture) startServer(addr string) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		f.t.Fatal(err)
	}
	f.server = httptest.NewUnstartedServer(f.endpoint)
	f.server.Listener = listener
	f.server.Start()
	f.addr = listener.Addr().String()
	if err = setTestAddress(f.endpoint, f.addr); err != nil {
		f.t.Fatal(err)
	}
}

// restart closes the endpoint and store and reopens both at the same address.
func (f *cliFixture) restart() {
	f.server.Close()
	if err := f.store.Close(); err != nil {
		f.t.Fatal(err)
	}
	f.openStore()
	f.endpoint = reopenTestEndpoint(f.endpoint, f.sec)
	f.startServer(f.addr)
	f.runner = NewRunner(f.auth, f.cli)
	f.runner.SetSessionMCP(f.endpoint)
}

func (f *cliFixture) calls() []fakeCLICall {
	raw, err := os.ReadFile(filepath.Join(f.state, "calls"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	var calls []fakeCLICall
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var call fakeCLICall
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			f.t.Fatal(err)
		}
		calls = append(calls, call)
	}
	return calls
}

func (f *cliFixture) last() fakeCLICall {
	calls := f.calls()
	if len(calls) == 0 {
		f.t.Fatal("the fixture CLI never ran")
	}
	return calls[len(calls)-1]
}

func (f *cliFixture) sessionDir(sessionID string) string {
	config, err := f.endpoint.prepare(f.p, sessionID)
	if err != nil {
		f.t.Fatal(err)
	}
	return config.dir
}

func (f *cliFixture) continuation(sessionID string) cliContinuation {
	record, found, err := loadContinuation(f.sessionDir(sessionID))
	if err != nil || !found {
		f.t.Fatalf("continuation: found=%v %v", found, err)
	}
	return record
}

func userItem(text string) map[string]any { return map[string]any{"role": "user", "content": text} }
func assistantItem(text string) map[string]any {
	return map[string]any{"role": "assistant", "content": text}
}

// conversationRequest is a V3 executor turn of one conversation.
func conversationRequest(sessionID, epoch, lineage string, input ...map[string]any) provideriface.Request {
	return provideriface.Request{SessionID: sessionID, ExecutionEpochID: epoch, ProviderLineageID: lineage, BoundaryReason: "session_turn", Model: "fixture", Instructions: "fixture system prompt", Input: input, Tools: []provideriface.ToolDefinition{{Name: "bash"}}, ToolInvoker: fixedToolInvoker{output: "tool-result"}}
}

// Purpose: Runner.run must reuse one private config, credential and CLI session
// across turns and a store/endpoint reopen; only explicit pre-init resume
// refusal may seed durable history again, and a general provider failure must
// not. A session's config must recover on its next turn when the API address
// changes or its credential is revoked (finding: a config could never be
// repaired). The process-level fake CLI exercises real loopback HTTP MCP, the
// canonical credential store and provider-owned continuation files.
func TestSessionMCPResumeAcrossRestartAndMissingCLIHistory(t *testing.T) {
	f := newCLIFixture(t)
	req := conversationRequest("conversation", "epoch-1", "lineage-1", userItem("old message"))
	if _, err := f.runner.CreateResponse(f.ctx, req); err != nil {
		t.Fatal(err)
	}
	first := f.last()
	dir := f.sessionDir(req.SessionID)
	path := filepath.Join(dir, "mcp.json")
	original := mustReadFile(t, path)
	if stat, _ := os.Stat(path); stat.Mode().Perm() != 0o600 {
		t.Fatal("config not private")
	}
	if first.Resume != "" || first.has("--no-session-persistence") || first.Cwd != dir || first.Tool != "tool-result" {
		t.Fatalf("first turn=%+v", first)
	}
	if first.PromptDir == dir || first.PromptDir == "" {
		t.Fatalf("system prompt written to the shared session directory: %q", first.PromptDir)
	}

	f.restart()
	req.Input = append(req.Input, assistantItem("old answer"), userItem("new message"))
	if _, err := f.runner.CreateResponse(f.ctx, req); err != nil {
		t.Fatal(err)
	}
	if after := mustReadFile(t, path); string(after) != string(original) {
		t.Fatal("config/token changed after restart")
	}
	second := f.last()
	if second.SID != first.SID || second.Resume != first.SID || second.Cwd != dir || strings.Contains(second.Input, "old message") || strings.Contains(second.Input, "old answer") || !strings.Contains(second.Input, "new message") {
		t.Fatalf("second turn=%+v", second)
	}

	// Explicit pre-init refusal: one fresh attempt seeded with full history.
	req.Input = append(req.Input, assistantItem("second answer"), userItem("third message"))
	if err := os.WriteFile(filepath.Join(f.state, "missing"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.CreateResponse(f.ctx, req); err != nil {
		t.Fatal(err)
	}
	reseeded := f.last()
	if reseeded.Resume != "" || !strings.Contains(reseeded.Input, "old message") || !strings.Contains(reseeded.Input, "third message") {
		t.Fatalf("history was not seeded once: %+v", reseeded)
	}
	if record := f.continuation(req.SessionID); record.CLISessionID != reseeded.SID || record.InputItems != len(req.Input) {
		t.Fatalf("replacement continuation=%+v", record)
	}

	// A general provider failure must not cause a fresh seeded attempt.
	before := f.continuation(req.SessionID)
	req.Input = append(req.Input, assistantItem("third answer"), userItem("fourth message"))
	if err := os.WriteFile(filepath.Join(f.state, "provider-error"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.CreateResponse(f.ctx, req); err == nil {
		t.Fatal("provider failure hidden")
	}
	if attempts := mustReadFile(t, filepath.Join(f.state, "attempts")); string(attempts) != "attempt\n" {
		t.Fatalf("general error retried: %q", attempts)
	}
	if record := f.continuation(req.SessionID); record != before {
		t.Fatalf("failed attempt changed continuation: %+v", record)
	}
	if err := os.Remove(filepath.Join(f.state, "provider-error")); err != nil {
		t.Fatal(err)
	}

	// A tampered (non-private) config is refused, never reused.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.endpoint.prepare(f.p, req.SessionID); err == nil {
		t.Fatal("public config reused")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	// The API address changes (another configured port): the next turn rewrites
	// the URL and keeps the same credential.
	token := configToken(original)
	f.server.Close()
	f.startServer("127.0.0.1:0")
	if _, err := f.runner.CreateResponse(f.ctx, req); err != nil {
		t.Fatalf("turn after an address change: %v", err)
	}
	moved := mustReadFile(t, path)
	if configToken(moved) != token || !strings.Contains(string(moved), "http://"+f.addr+"/mcp") || f.last().Tool != "tool-result" {
		t.Fatalf("config not repaired for the new address: %s", moved)
	}

	// A revoked credential is replaced on the next turn; the old one stays refused.
	rec, err := f.sec.ValidateScopedToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sec.RevokeScopedToken(f.p.AccountScopeID, rec.ID); err != nil {
		t.Fatal(err)
	}
	req.Input = append(req.Input, assistantItem("fifth answer"), userItem("sixth message"))
	if _, err = f.runner.CreateResponse(f.ctx, req); err != nil {
		t.Fatalf("turn after credential revocation: %v", err)
	}
	reissued := configToken(mustReadFile(t, path))
	if reissued == "" || reissued == token || f.last().Tool != "tool-result" {
		t.Fatal("revoked credential was not replaced")
	}
	probe := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	probe.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.endpoint.ServeHTTP(w, probe)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked credential accepted: %d", w.Code)
	}
}

// Purpose: requests that are not the conversation's own turns must run
// stateless. Session-less callers (Router, compaction, run titles, AI task
// metadata, memory) must work without a durable session or even a configured
// endpoint, and side requests that reuse the conversation's SessionID (V3
// titles, the task swarm Router, design jobs) must neither collide with the
// active turn nor resume, record or rewrite the conversation's CLI state.
func TestClaudeCodeOneShotRequestsRunStateless(t *testing.T) {
	f := newCLIFixture(t)
	// No standing endpoint at all: a session-less one-shot still runs.
	bare := NewRunner(f.auth, f.cli)
	for _, reason := range []string{"", "compact_context", "ai_task_metadata"} {
		if _, err := bare.CreateResponse(f.ctx, provideriface.Request{Model: "fixture", Instructions: "router prompt", BoundaryReason: reason, ToolChoice: "none", Input: []map[string]any{userItem("route this")}}); err != nil {
			t.Fatalf("session-less %q request: %v", reason, err)
		}
		call := f.last()
		if !call.has("--no-session-persistence") || call.has("--mcp-config") || call.Resume != "" {
			t.Fatalf("session-less request was not stateless: %+v", call)
		}
	}
	if _, err := bare.CreateResponse(f.ctx, provideriface.Request{Model: "fixture", Input: []map[string]any{userItem("x")}, Tools: []provideriface.ToolDefinition{{Name: "bash"}}, ToolInvoker: fixedToolInvoker{}}); err == nil {
		t.Fatal("session-less tool request accepted without a session endpoint")
	}

	conversation := conversationRequest("conversation", "epoch-1", "lineage-1", userItem("hello"))
	if _, err := f.runner.CreateResponse(f.ctx, conversation); err != nil {
		t.Fatal(err)
	}
	main := f.last()
	dir := f.sessionDir(conversation.SessionID)
	continuation := mustReadFile(t, continuationPath(dir))

	// The conversation's turn is active while side requests run.
	detach, err := f.endpoint.WithToolExecutor(f.ctx, f.p, provideriface.Request{SessionID: conversation.SessionID, Tools: []provideriface.ToolDefinition{{Name: "bash"}}, ToolInvoker: fixedToolInvoker{output: "main"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{"session_title", "task_swarm_router_one_shot", "design_job_one_shot"} {
		side := provideriface.Request{SessionID: conversation.SessionID, ProviderLineageID: "side-" + reason, BoundaryReason: reason, NativeContinuationAllowed: false, ForceFreshProviderContext: true, Model: "fixture", Instructions: "side prompt " + reason, ToolChoice: "none", Input: []map[string]any{userItem("side input")}}
		if _, err := f.runner.CreateResponse(f.ctx, side); err != nil {
			t.Fatalf("side request %s collided with the active turn: %v", reason, err)
		}
		call := f.last()
		if call.Resume != "" || !call.has("--no-session-persistence") || call.has("--mcp-config") || call.Cwd == dir || call.PromptDir == dir {
			t.Fatalf("side request %s touched the conversation's CLI session: %+v", reason, call)
		}
	}
	detach()
	if after := mustReadFile(t, continuationPath(dir)); string(after) != string(continuation) {
		t.Fatalf("side request rewrote the continuation: %s", after)
	}
	if _, err := os.Stat(filepath.Join(dir, "system-prompt.txt")); !os.IsNotExist(err) {
		t.Fatalf("system prompt in the shared session directory: %v", err)
	}
	conversation.Input = append(conversation.Input, assistantItem("hi"), userItem("next"))
	if _, err := f.runner.CreateResponse(f.ctx, conversation); err != nil {
		t.Fatal(err)
	}
	if next := f.last(); next.Resume != main.SID || strings.Contains(next.Input, "side input") {
		t.Fatalf("next conversation turn did not resume its own CLI session: %+v", next)
	}
}

// Purpose: resume must never skip Swarm context boundaries. At a new epoch
// (compaction), a fresh-context boundary reason (checkpoint, plan guard,
// restart after a tool, provider/model handoff), a return from another
// provider's lineage, or an input whose recorded prefix changed (bounded
// history, compacted summary), the runner must start a fresh CLI session seeded
// with the request's complete input instead of resuming the old one.
func TestClaudeCodeResumeRespectsContextBoundaries(t *testing.T) {
	f := newCLIFixture(t)
	type boundary struct {
		name   string
		mutate func(req *provideriface.Request)
	}
	cases := []boundary{
		{"compaction epoch", func(req *provideriface.Request) {
			req.ExecutionEpochID = "epoch-2"
			req.Input = []map[string]any{userItem("compacted summary of old message"), userItem("follow-up")}
		}},
		{"checkpoint fresh context", func(req *provideriface.Request) { req.BoundaryReason = "checkpoint_fresh_context" }},
		{"plan guard", func(req *provideriface.Request) { req.BoundaryReason = "context_compaction_plan_guard" }},
		{"restart after tool", func(req *provideriface.Request) { req.BoundaryReason = "session_turn+restart_after_tool" }},
		{"provider handoff", func(req *provideriface.Request) { req.BoundaryReason = "provider_model_runtime_handoff" }},
		{"back from another provider", func(req *provideriface.Request) { req.PreviousProviderLineageID = "other-provider-lineage" }},
		{"other lineage", func(req *provideriface.Request) { req.ProviderLineageID = "lineage-2" }},
		{"bounded history", func(req *provideriface.Request) { req.Input = req.Input[1:] }},
	}
	for i, tc := range cases {
		session := fmt.Sprintf("boundary-%d", i)
		base := conversationRequest(session, "epoch-1", "lineage-1", userItem("old message"))
		if _, err := f.runner.CreateResponse(f.ctx, base); err != nil {
			t.Fatal(err)
		}
		seeded := f.last()
		next := base
		next.Input = append(append([]map[string]any(nil), base.Input...), assistantItem("other provider answer"), userItem("follow-up"))
		tc.mutate(&next)
		if _, err := f.runner.CreateResponse(f.ctx, next); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		call := f.last()
		if call.Resume != "" || call.SID == seeded.SID || call.has("--resume") {
			t.Fatalf("%s resumed the old CLI session: %+v", tc.name, call)
		}
		for _, item := range next.Input {
			if text, _ := item["content"].(string); !strings.Contains(call.Input, text) {
				t.Fatalf("%s: fresh session missing %q: %s", tc.name, text, call.Input)
			}
		}
		if record := f.continuation(session); record.CLISessionID != call.SID || record.ExecutionEpochID != next.ExecutionEpochID || record.ProviderLineageID != next.ProviderLineageID {
			t.Fatalf("%s: continuation not moved to the fresh session: %+v", tc.name, record)
		}
	}
	// Control: the same ordinary turn without a boundary resumes.
	base := conversationRequest("control", "epoch-1", "lineage-1", userItem("old message"))
	if _, err := f.runner.CreateResponse(f.ctx, base); err != nil {
		t.Fatal(err)
	}
	seeded := f.last()
	base.Input = append(base.Input, assistantItem("answer"), userItem("follow-up"))
	if _, err := f.runner.CreateResponse(f.ctx, base); err != nil {
		t.Fatal(err)
	}
	if call := f.last(); call.Resume != seeded.SID || strings.Contains(call.Input, "old message") {
		t.Fatalf("ordinary turn did not resume: %+v", call)
	}
}

// Purpose: Runner.run/resumedInput must not replay durable user notes already
// delivered by queuedInputToolInvoker on CLI stdin. A process fixture plus real
// MCP call is the narrowest layer exercising delivery, continuation, and resume.
// Assert exactly one note, retained resume identity, and the next new user input.
func TestClaudeCodeResumeDoesNotReplayDeliveredQueuedNote(t *testing.T) {
	f := newCLIFixture(t)
	const note = "queued-note-once"
	marker := filepath.Join(f.state, "queue")
	if err := os.WriteFile(marker, []byte("queue"), 0600); err != nil {
		t.Fatal(err)
	}
	req := conversationRequest("queued-resume", "epoch", "lineage", userItem("begin"))
	req.QueuedUserInput = &queuedInputStub{messages: []string{note}}
	if _, err := f.runner.CreateResponse(f.ctx, req); err != nil {
		t.Fatal(err)
	}
	first := f.last()
	if strings.Count(first.Queued, note) != 1 {
		t.Fatalf("queued stdin=%q", first.Queued)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	req.QueuedUserInput = nil
	req.Input = append(req.Input, userItem(note), assistantItem("done"), userItem("next-user-input"))
	if _, err := f.runner.CreateResponse(f.ctx, req); err != nil {
		t.Fatal(err)
	}
	second := f.last()
	if second.Resume != first.SID || strings.Contains(second.Input, note) || !strings.Contains(second.Input, "next-user-input") {
		t.Fatalf("resumed input=%+v", second)
	}
	calls := f.calls()
	if len(calls) != 2 {
		t.Fatalf("CLI calls=%d", len(calls))
	}
	if strings.Count(first.Input+first.Queued+second.Input+second.Queued, note) != 1 {
		t.Fatal("queued note duplicated across turns")
	}
}

// Purpose: a recovered conversation reaches Runner.run before the daemon binds
// its loopback listener. It must wait without dispatching or changing durable
// continuation, then resume the same CLI session after SetAddress. This real
// runner/process/store-reopen fixture isolates the readiness boundary; cancelled
// waits must return without starting a CLI process or creating credentials.
func TestSessionMCPRecoveredTurnWaitsForLoopbackAddress(t *testing.T) {
	f := newCLIFixture(t)
	req := conversationRequest("pending-recovery", "epoch", "lineage", userItem("begin"))
	if _, err := f.runner.CreateResponse(f.ctx, req); err != nil {
		t.Fatal(err)
	}
	first := f.last()
	f.server.Close()
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.openStore()
	f.endpoint = newTestSessionMCP(f.sec, &testLocation{root: filepath.Join(f.root, "claudecode")})
	f.runner = NewRunner(f.auth, f.cli)
	f.runner.SetSessionMCP(f.endpoint)
	req.Input = append(req.Input, assistantItem("done"), userItem("recovered-next"))
	done := make(chan error, 1)
	go func() { _, err := f.runner.CreateResponse(f.ctx, req); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("turn failed before bind: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if len(f.calls()) != 1 {
		t.Fatal("CLI dispatched before listener readiness")
	}
	f.startServer(f.addr)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	second := f.last()
	if second.Resume != first.SID || len(f.calls()) != 2 || !strings.Contains(second.Input, "recovered-next") {
		t.Fatalf("recovered call=%+v", second)
	}

	endpoint := NewSessionMCP(f.sec, t.TempDir())
	runner := NewRunner(f.auth, f.cli)
	runner.SetSessionMCP(endpoint)
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, err := runner.CreateResponse(ctx, req); err == nil {
		t.Fatal("cancelled wait succeeded")
	}
	if len(f.calls()) != 2 {
		t.Fatal("cancelled turn dispatched")
	}
	if entries, err := os.ReadDir(endpoint.root); err != nil || len(entries) != 0 {
		t.Fatalf("cancelled wait mutated state: %v %v", entries, err)
	}
}
