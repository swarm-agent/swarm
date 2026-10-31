package claudecode

import (
	"context"
	"encoding/json"
	"net"
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
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: Runner.run must reuse one private config, credential and CLI session
// across turns and store reopen; only explicit pre-init resume refusal may seed
// durable history again. Prevent transcript replay and silent tool loss. This
// process-level fake CLI exercises real HTTP MCP and Pebble/session mutations,
// without a provider account or network other than temporary loopback listeners.
func TestSessionMCPResumeAcrossRestartAndMissingCLIHistory(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db")
	mcpRoot := filepath.Join(root, "cli")
	store, err := pebblestore.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	p := testPrincipal()
	p.Type = identity.PrincipalTypeUser
	ctx, cancel := context.WithTimeout(identity.ContextWithPrincipal(context.Background(), p), 30*time.Second)
	defer cancel()
	sessions := session.NewService(pebblestore.NewSessionStore(store), nil)
	_, err = sessions.ApplySessionMutation(session.SessionMutationInput{Kind: session.SessionMutationCreateSession, SessionID: "conversation", AccountScopeID: p.AccountScopeID, UserID: p.UserID, ClientRequestID: "create", PayloadHash: "create", Session: &pebblestore.SessionSnapshot{ID: "conversation", AccountScopeID: p.AccountScopeID, UserID: p.UserID, Mode: "auto"}})
	if err != nil {
		t.Fatal(err)
	}
	auth := pebblestore.NewAuthStore(store)
	_, err = auth.UpsertCredential(pebblestore.AuthCredentialInput{Provider: ProviderID, AccountScopeID: p.AccountScopeID, Type: pebblestore.AuthTypeCLI, SetActive: true})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := NewSessionMCP(security.NewService(pebblestore.NewClientAuthStore(store), nil), auth, mcpRoot)
	server := httptest.NewServer(endpoint)
	addr := server.Listener.Addr().String()
	if err = endpoint.SetAddress(addr); err != nil {
		t.Fatal(err)
	}
	defer func() { server.Close() }()
	// Python is a fixture prerequisite only; no live CLI/provider behavior is claimed.
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("fake CLI requires python3:", err)
	}
	script := filepath.Join(root, "fake-claude")
	source := `#!` + python + `
import sys,json,urllib.request,pathlib
args=sys.argv[1:]
config=args[args.index('--mcp-config')+1]
resume=args[args.index('--resume')+1] if '--resume' in args else ''
base=pathlib.Path(config).parent
if '--no-session-persistence' in args or '--strict-mcp-config' not in args:
 raise RuntimeError('invalid CLI isolation/persistence flags')
if (base/'provider-error').exists():
 with open(base/'attempts','a') as out: out.write('attempt\n')
 print('provider rate limit',file=sys.stderr)
 sys.exit(1)
if resume and (base/'missing').exists():
 (base/'missing').unlink()
 print('No conversation found with session ID: '+resume,file=sys.stderr)
 sys.exit(1)
line=json.loads(sys.stdin.readline())
sid=resume or ('replacement-session' if (base/'turns').exists() else 'first-session')
print(json.dumps({'type':'system','subtype':'init','session_id':sid,'model':'fixture'}),flush=True)
c=json.load(open(config))['mcpServers']['swarm']
req=urllib.request.Request(c['url'],data=json.dumps({'jsonrpc':'2.0','id':1,'method':'tools/call','params':{'name':'bash','arguments':{},'_meta':{'claudecode/toolUseId':'fixture-call'}}}).encode(),headers={**c['headers'],'Content-Type':'application/json'})
result=json.load(urllib.request.urlopen(req,timeout=5))
assert result['result']['content'][0]['text']=='tool-result',result
with open(base/'turns','a') as out: out.write(json.dumps({'config':config,'resume':resume,'sid':sid,'input':line})+'\n')
print(json.dumps({'type':'result','subtype':'success','result':'done'}),flush=True)
sys.stdin.read()
`
	if err = os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	cli := NewCLI()
	cli.lookupEnv = func(key string) (string, bool) {
		if key == CLIPathEnv {
			return script, true
		}
		return os.LookupEnv(key)
	}
	runner := NewRunner(auth, cli)
	runner.SetSessionMCP(endpoint, sessions)
	req := provideriface.Request{SessionID: "conversation", Model: "fixture", Input: []map[string]any{{"role": "user", "content": "old message"}}, Tools: []provideriface.ToolDefinition{{Name: "bash"}}, ToolInvoker: fixedToolInvoker{output: "tool-result"}}
	if _, err = runner.CreateResponse(ctx, req); err != nil {
		t.Fatal(err)
	}
	dir, path, err := endpoint.prepare(p, req.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, _ := os.Stat(path)
	if stat.Mode().Perm() != 0600 {
		t.Fatal("config not private")
	}
	server.Close()
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = pebblestore.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	auth = pebblestore.NewAuthStore(store)
	sessions = session.NewService(pebblestore.NewSessionStore(store), nil)
	endpoint = NewSessionMCP(security.NewService(pebblestore.NewClientAuthStore(store), nil), auth, mcpRoot)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	server = httptest.NewUnstartedServer(endpoint)
	server.Listener = listener
	server.Start()
	if err = endpoint.SetAddress(addr); err != nil {
		t.Fatal(err)
	}
	runner = NewRunner(auth, cli)
	runner.SetSessionMCP(endpoint, sessions)
	req.Input = append(req.Input, map[string]any{"role": "assistant", "content": "old answer"}, map[string]any{"role": "user", "content": "new message"})
	if _, err = runner.CreateResponse(ctx, req); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Fatal("config/token changed after restart")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "turns"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("turns=%s", raw)
	}
	var second map[string]any
	json.Unmarshal([]byte(lines[1]), &second)
	input, _ := json.Marshal(second["input"])
	if second["sid"] != "first-session" || second["resume"] != "first-session" || second["config"] != path || strings.Contains(string(input), "old message") || !strings.Contains(string(input), "new message") {
		t.Fatalf("second turn=%s", lines[1])
	}
	if err = os.WriteFile(filepath.Join(dir, "missing"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.CreateResponse(ctx, req); err != nil {
		t.Fatal(err)
	}
	id, err := sessions.ClaudeCodeSession(p, req.SessionID)
	if err != nil || id != "replacement-session" {
		t.Fatalf("replacement=%q %v", id, err)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "turns"))
	lines = strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 || !strings.Contains(lines[2], "old message") {
		t.Fatalf("history was not seeded once: %s", raw)
	}
	// A general provider failure must not cause a fresh seeded attempt.
	if err = os.WriteFile(filepath.Join(dir, "provider-error"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.CreateResponse(ctx, req); err == nil {
		t.Fatal("provider failure hidden")
	}
	attempts, err := os.ReadFile(filepath.Join(dir, "attempts"))
	if err != nil {
		t.Fatal(err)
	}
	if string(attempts) != "attempt\n" {
		t.Fatalf("general error retried: %q", attempts)
	}
	id, err = sessions.ClaudeCodeSession(p, req.SessionID)
	if err != nil || id != "replacement-session" {
		t.Fatalf("failed attempt changed continuation: %q %v", id, err)
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err = endpoint.prepare(p, req.SessionID); err == nil {
		t.Fatal("public config reused")
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err = endpoint.SetAddress("127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = endpoint.prepare(p, req.SessionID); err == nil {
		t.Fatal("changed endpoint silently rewrote config")
	}
	after, _ = os.ReadFile(path)
	if string(after) != string(original) {
		t.Fatal("rejected configuration changed token or URL")
	}
}
