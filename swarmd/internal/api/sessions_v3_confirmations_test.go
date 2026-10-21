package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

var placeOrderSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"order_id": map[string]any{"type": "string", "pattern": "^[A-Z0-9]{6}$"},
		"quantity": map[string]any{"type": "integer", "minimum": 1},
	},
	"required":             []any{"order_id", "quantity"},
	"additionalProperties": false,
}

// confirmationModel plays the model: a message mentioning "lookup" calls the
// read tool; any other message calls the write tool (plus a second write and a
// write without a summary, which must both be refused); once a confirmed
// call's outcome is in its input it replies with that outcome.
func confirmationModel(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	for i := len(req.Input) - 1; i >= 0; i-- {
		item := req.Input[i]
		if stringMapValue(item, "type") == "function_call_output" && strings.HasPrefix(stringMapValue(item, "call_id"), "confirmed_") {
			return provideriface.Response{Text: "outcome:" + stringMapValue(item, "output"), StopReason: "stop"}, nil
		}
	}
	if hasFunctionCallOutput(req.Input) {
		return provideriface.Response{Text: "done:" + lastFunctionCallOutput(req.Input), StopReason: "stop"}, nil
	}
	if strings.Contains(lastConfirmationTestUserText(req.Input), "lookup") {
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "read1", Name: "lookup_order", Arguments: `{"order_id":"AB12CD"}`}}}, nil
	}
	if strings.Contains(lastConfirmationTestUserText(req.Input), "plain") {
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "plain1", Name: "place_order", Arguments: `{"order_id":"AB12CD","quantity":2}`}}}, nil
	}
	return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{
		{CallID: "nosum", Name: "place_order", Arguments: `{"order_id":"AB12CD","quantity":2}`},
		{CallID: "write1", Name: "place_order", Arguments: `{"order_id":"AB12CD","quantity":2,"summary":"Order 2 of AB12CD"}`},
		{CallID: "write2", Name: "place_order", Arguments: `{"order_id":"ZZ99ZZ","quantity":1,"summary":"Order 1 of ZZ99ZZ"}`},
	}}, nil
}

func lastConfirmationTestUserText(input []map[string]any) string {
	for i := len(input) - 1; i >= 0; i-- {
		if input[i]["role"] != "user" {
			continue
		}
		raw, _ := json.Marshal(input[i]["content"])
		return string(raw)
	}
	return ""
}

type confirmationFixture struct {
	t           *testing.T
	server      *Server
	sessions    *sessionruntime.Service
	permissions *permission.Service
	runner      *sessionsV3RecordingProviderRunner
	close       func()
	path        string
}

// newConfirmationFixture opens a daemon over the store at path with a sealed
// agent "frontdesk": place_order (write, so confirmed by default) and
// lookup_order (read). writeTool overrides the place_order definition.
func newConfirmationFixture(t *testing.T, path string, writeTool *pebblestore.AgentCustomToolDefinition) *confirmationFixture {
	t.Helper()
	server, sessions, permissions, closeStore, _ := newRoutedSessionTestServerAt(t, path, false)
	runner := &sessionsV3RecordingProviderRunner{handler: confirmationModel}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	server.runner = runruntime.NewService(sessions, server.model, providers, tool.NewRuntime(1), permissions, server.agents, nil, nil)
	account := testPrincipal().AccountScopeID
	write := pebblestore.AgentCustomToolDefinition{Name: "place_order", Kind: pebblestore.AgentCustomToolKindClient, Description: "Place an order.", InputSchema: placeOrderSchema, Effect: "write"}
	if writeTool != nil {
		write = *writeTool
	}
	for _, definition := range []pebblestore.AgentCustomToolDefinition{
		write,
		{Name: "lookup_order", Kind: pebblestore.AgentCustomToolKindClient, Description: "Look up an order.", InputSchema: lookupOrderSchema, Effect: "read"},
	} {
		if _, err := server.agents.PutCustomToolForAccount(account, definition); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := server.agents.UpsertForAccount(account, agentruntime.UpsertInput{
		Name: "frontdesk", Mode: agentruntime.ModeSubagent, Provider: "test-provider", Model: "test-model",
		Enabled: pebblestore.BoolPtr(true), Prompt: "Take orders.",
		ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{
			"place_order": {Enabled: pebblestore.BoolPtr(true)}, "lookup_order": {Enabled: pebblestore.BoolPtr(true)},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec
	return &confirmationFixture{t: t, server: server, sessions: sessions, permissions: permissions, runner: runner, close: closeStore.(func()), path: path}
}

func (f *confirmationFixture) createSession(title string) string {
	f.t.Helper()
	workspace := f.t.TempDir()
	bindingID := seedSessionsV3PrimaryAuthority(f.t, f.server, workspace)
	raw, _ := json.Marshal(map[string]any{
		"client_request_id": "create-" + title, "workspace_path": workspace, "workspace_name": filepath.Base(workspace),
		"swarm_id": "host-swarm-id", "workspace_binding_id": bindingID, "target_kind": "host", "target_relationship": "self",
		"title": title, "mode": sessionruntime.ModeAuto, "agent_name": "frontdesk",
		"preference": pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model"},
	})
	code, body := f.do(http.MethodPost, "/v3/sessions", string(raw))
	if code != http.StatusOK {
		f.t.Fatalf("create session: %d %s", code, body)
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		f.t.Fatal(err)
	}
	return created.Session.ID
}

func (f *confirmationFixture) do(method, path, body string) (int, string) {
	f.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	return rec.Code, rec.Body.String()
}

// say posts a visitor message and waits for its run to finish.
func (f *confirmationFixture) say(sessionID, requestID, content string) {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{"client_request_id": requestID, "role": "user", "content": content})
	if code, out := f.do(http.MethodPost, "/v3/sessions/"+sessionID+"/messages", string(body)); code != http.StatusOK {
		f.t.Fatalf("message: %d %s", code, out)
	}
	f.waitRun(sessionID, stableSessionsV3PrimaryRunID(sessionID, requestID), sessionruntime.RunIntentCompleted)
}

func (f *confirmationFixture) waitRun(sessionID, runID, status string) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if intent, ok, err := f.sessions.GetSessionRunIntent(sessionID, runID); err == nil && ok && intent.Status == status {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	intent, ok, err := f.sessions.GetSessionRunIntent(sessionID, runID)
	f.t.Fatalf("run %s never reached %s: %+v ok=%v err=%v", runID, status, intent, ok, err)
}

func (f *confirmationFixture) confirmation(sessionID string) runruntime.ClientToolConfirmation {
	f.t.Helper()
	session, ok, err := f.sessions.GetSession(sessionID)
	if err != nil || !ok {
		f.t.Fatalf("session: %v %v", ok, err)
	}
	c, _ := runruntime.ClientToolConfirmationFromSession(session)
	return c
}

func (f *confirmationFixture) decide(sessionID, confirmationID, body string) (int, runruntime.ClientToolConfirmation, string) {
	f.t.Helper()
	code, out := f.do(http.MethodPost, "/v3/sessions/"+sessionID+"/confirmations/"+confirmationID, body)
	var decoded struct {
		Confirmation runruntime.ClientToolConfirmation `json:"confirmation"`
	}
	_ = json.Unmarshal([]byte(out), &decoded)
	return code, decoded.Confirmation, out
}

func (f *confirmationFixture) callCount() int {
	f.runner.mu.Lock()
	defer f.runner.mu.Unlock()
	return f.runner.callCount
}

func (f *confirmationFixture) lastReply(sessionID string) string {
	f.t.Helper()
	messages, err := f.sessions.ListSessionMessages(sessionID, 0, 200)
	if err != nil {
		f.t.Fatal(err)
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" {
			continue
		}
		// The scripted model echoes the model-facing tool output, which wraps
		// the client tool's own output: unwrap it for readable assertions.
		var wrapped map[string]any
		if rest, ok := strings.CutPrefix(messages[i].Content, "outcome:"); ok && json.Unmarshal([]byte(rest), &wrapped) == nil {
			if inner, ok := wrapped["output"].(string); ok {
				return "outcome:" + inner
			}
		}
		return messages[i].Content
	}
	return ""
}

func (f *confirmationFixture) toolOutput(sessionID, callID string) string {
	f.t.Helper()
	messages, err := f.sessions.ListSessionMessages(sessionID, 0, 200)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, message := range messages {
		if record, ok := sessionsV3DecodeProviderToolResultRecord(message.Content); ok && message.Role == "tool" && record.CallID == callID {
			return record.Output
		}
	}
	return ""
}

func (f *confirmationFixture) pendingClientCalls(sessionID string) []pebblestore.PermissionRecord {
	f.t.Helper()
	pending, err := f.permissions.ListPending(sessionID, 100)
	if err != nil {
		f.t.Fatal(err)
	}
	return pending
}

// Purpose: a write client tool's call never reaches the gateway as an
// executable call when the model makes it. Swarm records one pending
// confirmation (tool, the arguments without the summary, their hash, the
// model's summary), ends the run without another model call, and refuses a
// second confirmation and a call without a summary. An approval needs the
// shown arguments' hash; it then becomes an ordinary pending client tool call
// with byte-identical arguments, and the gateway's answer reaches the model.
// Threat: a model executing a side effect the person never approved, or an
// approval running different arguments than the card showed.
// Boundary: requestClientToolConfirmation (run), handleSessionV3Confirmations,
// advanceClientToolConfirmationLocked, the executor's AwaitingConfirmation stop.
// Narrowest layer: the real V3 executor and HTTP handlers with a scripted
// model, since the contract spans the run loop, metadata and both routes.
func TestConfirmedClientToolPausesUntilApprovedAndRunsTheSameArguments(t *testing.T) {
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "confirm.pebble"), nil)
	sessionID := f.createSession("approve")
	f.say(sessionID, "m1", "order 2 of AB12CD please")

	if got := f.callCount(); got != 1 {
		t.Fatalf("model calls while waiting for confirmation = %d, want 1 (the run must end)", got)
	}
	var offered map[string]any
	for _, definition := range f.runner.requests[0].Tools {
		if definition.Name == "place_order" {
			offered = definition.Parameters
		}
		if definition.Name == "lookup_order" {
			if raw, _ := json.Marshal(definition.Parameters); strings.Contains(string(raw), "summary") {
				t.Fatalf("read tool schema gained a summary: %s", raw)
			}
		}
	}
	if raw, _ := json.Marshal(offered); !strings.Contains(string(raw), `"summary"`) || !strings.Contains(string(raw), `"required":["order_id","quantity","summary"]`) {
		t.Fatalf("write tool was offered without a required summary: %s", raw)
	}
	if pending := f.pendingClientCalls(sessionID); len(pending) != 0 {
		t.Fatalf("the gateway saw an executable call before approval: %+v", pending)
	}
	c := f.confirmation(sessionID)
	wantArgs := `{"order_id":"AB12CD","quantity":2}`
	gotArgs, _ := json.Marshal(c.Arguments)
	if c.Status != runruntime.ClientToolConfirmationPending || c.Tool != "place_order" || c.CallID != "write1" || string(gotArgs) != wantArgs || c.Summary != "Order 2 of AB12CD" || !strings.HasPrefix(c.ArgumentsHash, "sha256:") {
		t.Fatalf("confirmation = %+v args=%s", c, gotArgs)
	}
	if _, err := c.VerifyArguments(); err != nil {
		t.Fatal(err)
	}
	if out := f.toolOutput(sessionID, "write1"); !strings.Contains(out, "awaiting_confirmation") {
		t.Fatalf("model's call result = %q", out)
	}
	if out := f.toolOutput(sessionID, "write2"); !strings.Contains(out, "waiting for the person's confirmation") {
		t.Fatalf("a second confirmation was not refused: %q", out)
	}
	if out := f.toolOutput(sessionID, "nosum"); !strings.Contains(out, "summary") {
		t.Fatalf("a call without a summary was not refused: %q", out)
	}

	code, body := f.do(http.MethodGet, "/v3/sessions/"+sessionID+"/confirmations", "")
	if code != http.StatusOK || !strings.Contains(body, c.ID) || !strings.Contains(body, `"kind":"confirmation"`) {
		t.Fatalf("confirmations list: %d %s", code, body)
	}
	if code, body := f.do(http.MethodGet, "/v3/sessions/"+sessionID, ""); code != http.StatusOK || !strings.Contains(body, `"pending_confirmation"`) || !strings.Contains(body, c.ID) {
		t.Fatalf("session read lacks pending_confirmation: %d %.300s", code, body)
	}

	if code, _, body := f.decide(sessionID, c.ID, `{"decision":"approve"}`); code != http.StatusBadRequest {
		t.Fatalf("approve without arguments_hash: %d %s", code, body)
	}
	if code, _, body := f.decide(sessionID, c.ID, `{"decision":"approve","arguments_hash":"sha256:00"}`); code != http.StatusConflict {
		t.Fatalf("approve with another hash: %d %s", code, body)
	}
	if code, _, body := f.decide(sessionID, c.ID, `{"decision":"maybe"}`); code != http.StatusBadRequest {
		t.Fatalf("unknown decision: %d %s", code, body)
	}
	if f.confirmation(sessionID).Status != runruntime.ClientToolConfirmationPending || len(f.pendingClientCalls(sessionID)) != 0 {
		t.Fatal("a refused decision changed state")
	}

	approve := fmt.Sprintf(`{"decision":"approve","arguments_hash":%q}`, c.ArgumentsHash)
	code, approved, body := f.decide(sessionID, c.ID, approve)
	if code != http.StatusOK || approved.Status != runruntime.ClientToolConfirmationApproved || approved.ExecutionPermissionID == "" {
		t.Fatalf("approve: %d %s", code, body)
	}
	pending := f.pendingClientCalls(sessionID)
	if len(pending) != 1 || pending[0].ToolName != "place_order" || pending[0].ToolArguments != wantArgs || pending[0].ToolCallArguments != wantArgs || pending[0].Requirement != permission.RequirementClientTool || pending[0].CallID != c.ExecutionCallID {
		t.Fatalf("approved call = %+v", pending)
	}
	if got := f.callCount(); got != 1 {
		t.Fatalf("the model ran before the gateway answered: %d calls", got)
	}
	if code, _, body := f.decide(sessionID, c.ID, approve); code != http.StatusOK {
		t.Fatalf("repeated approve: %d %s", code, body)
	}
	if len(f.pendingClientCalls(sessionID)) != 1 {
		t.Fatal("a repeated approve created another call")
	}
	if code, _, body := f.decide(sessionID, c.ID, `{"decision":"reject"}`); code != http.StatusConflict {
		t.Fatalf("reject after approve: %d %s", code, body)
	}

	if code, body := f.do(http.MethodPost, "/v3/sessions/"+sessionID+"/permissions/"+pending[0].ID+"/resolve", `{"action":"allow_once","approved_arguments":{"result":{"status":"placed","ref":"P-1"}}}`); code != http.StatusOK {
		t.Fatalf("gateway answer: %d %s", code, body)
	}
	delivered := f.confirmation(sessionID)
	if !delivered.Delivered || delivered.ContinuationRunID == "" {
		t.Fatalf("outcome not delivered: %+v", delivered)
	}
	f.waitRun(sessionID, delivered.ContinuationRunID, sessionruntime.RunIntentCompleted)
	reply := f.lastReply(sessionID)
	if !strings.HasPrefix(reply, "outcome:") || !strings.Contains(reply, `"status":"placed"`) || !strings.Contains(reply, `"confirmation":"approved"`) || !strings.Contains(reply, `"untrusted_content":true`) {
		t.Fatalf("model reply = %q", reply)
	}
	if code, body := f.do(http.MethodGet, "/v3/sessions/"+sessionID+"/confirmations", ""); code != http.StatusOK || strings.Contains(body, c.ID) {
		t.Fatalf("a delivered confirmation is still listed as pending: %s", body)
	}
}

// Purpose: a person's decline reaches the model as a tool error carrying
// their note, and nothing is ever offered to the gateway to execute.
// Threat: a declined action running anyway, or the model not learning of it.
// Boundary: decideClientToolConfirmation, ClientToolConfirmationOutcome.
// Narrowest layer: the real executor and routes (see the approve test).
func TestConfirmedClientToolRejectTellsTheModel(t *testing.T) {
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "reject.pebble"), nil)
	sessionID := f.createSession("reject")
	f.say(sessionID, "m1", "order please")
	c := f.confirmation(sessionID)
	code, rejected, body := f.decide(sessionID, c.ID, `{"decision":"reject","note":"too expensive"}`)
	if code != http.StatusOK || rejected.Status != runruntime.ClientToolConfirmationRejected || !rejected.Delivered {
		t.Fatalf("reject: %d %s", code, body)
	}
	f.waitRun(sessionID, rejected.ContinuationRunID, sessionruntime.RunIntentCompleted)
	if reply := f.lastReply(sessionID); !strings.Contains(reply, "the person declined: too expensive") || !strings.Contains(reply, `"status":"error"`) {
		t.Fatalf("model reply = %q", reply)
	}
	records, err := f.permissions.ListPermissions(sessionID, 100)
	if err != nil || len(records) != 0 {
		t.Fatalf("a declined call reached the gateway: %+v %v", records, err)
	}
	if code, _, _ := f.decide(sessionID, c.ID, fmt.Sprintf(`{"decision":"approve","arguments_hash":%q}`, c.ArgumentsHash)); code != http.StatusConflict {
		t.Fatalf("approve after reject: %d", code)
	}
}

// Purpose: an unanswered confirmation expires after confirm_timeout_ms; the
// model is told nobody answered and a late approval is refused.
// Threat: a stale approval executing long after the conversation moved on, or
// a conversation stuck forever on an unanswered card.
// Boundary: advanceClientToolConfirmationLocked expiry, the expiry timer.
// Narrowest layer: the real executor with a short configured expiry.
func TestConfirmedClientToolExpires(t *testing.T) {
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "expire.pebble"), &pebblestore.AgentCustomToolDefinition{
		Name: "place_order", Kind: pebblestore.AgentCustomToolKindClient, Description: "Place an order.", InputSchema: placeOrderSchema, Effect: "write", ConfirmTimeoutMS: 300,
	})
	sessionID := f.createSession("expire")
	f.say(sessionID, "m1", "order please")
	c := f.confirmation(sessionID)
	if c.ExpiresAt-c.CreatedAt != 300 {
		t.Fatalf("expiry window = %d ms, want 300", c.ExpiresAt-c.CreatedAt)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !f.confirmation(sessionID).Delivered && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	expired := f.confirmation(sessionID)
	if expired.Status != runruntime.ClientToolConfirmationExpired || !expired.Delivered {
		t.Fatalf("not expired by the timer: %+v", expired)
	}
	f.waitRun(sessionID, expired.ContinuationRunID, sessionruntime.RunIntentCompleted)
	if reply := f.lastReply(sessionID); !strings.Contains(reply, "no answer") {
		t.Fatalf("model reply = %q", reply)
	}
	if code, _, _ := f.decide(sessionID, c.ID, fmt.Sprintf(`{"decision":"approve","arguments_hash":%q}`, c.ArgumentsHash)); code != http.StatusConflict {
		t.Fatalf("late approve: %d", code)
	}
	if records, _ := f.permissions.ListPermissions(sessionID, 100); len(records) != 0 {
		t.Fatalf("an expired confirmation reached the gateway: %+v", records)
	}
}

// Purpose: read tools, and write tools declared confirm:false, keep the
// existing contract: the call is a pending client tool call at once and no
// confirmation is recorded.
// Threat: confirmation leaking onto tools that did not ask for it (breaking
// lookups) or confirm:false being ignored.
// Boundary: pebblestore.ClientToolRequiresConfirmation, the invoker intercept.
// Narrowest layer: the real executor; the gateway answers via the resolve route.
func TestReadAndUnconfirmedClientToolsAreUnaffected(t *testing.T) {
	for _, tc := range []struct{ name, message string }{{"read", "lookup AB12CD"}, {"write_confirm_false", "plain order please"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConfirmationFixture(t, filepath.Join(t.TempDir(), tc.name+".pebble"), &pebblestore.AgentCustomToolDefinition{
				Name: "place_order", Kind: pebblestore.AgentCustomToolKindClient, Description: "Place an order.", InputSchema: placeOrderSchema, Effect: "write", Confirm: pebblestore.BoolPtr(false),
			})
			stored, _, _ := f.server.agents.GetCustomToolForAccount(testPrincipal().AccountScopeID, "place_order")
			if stored.Confirm == nil || *stored.Confirm {
				t.Fatalf("confirm:false not stored: %+v", stored)
			}
			sessionID := f.createSession(tc.name)
			body, _ := json.Marshal(map[string]any{"client_request_id": "m1", "role": "user", "content": tc.message})
			if code, out := f.do(http.MethodPost, "/v3/sessions/"+sessionID+"/messages", string(body)); code != http.StatusOK {
				t.Fatalf("message: %d %s", code, out)
			}
			wantTool := map[string]string{"read": "lookup_order", "write_confirm_false": "place_order"}[tc.name]
			record := waitForPendingConfirmationTestCall(t, f.permissions, sessionID)
			if record.ToolName != wantTool || record.RunID == "" {
				t.Fatalf("call = %+v, want an immediate %s call inside the run", record, wantTool)
			}
			if code, out := f.do(http.MethodPost, "/v3/sessions/"+sessionID+"/permissions/"+record.ID+"/resolve", `{"action":"allow_once","approved_arguments":{"result":{"status":"shipped"}}}`); code != http.StatusOK {
				t.Fatalf("answer: %d %s", code, out)
			}
			f.waitRun(sessionID, stableSessionsV3PrimaryRunID(sessionID, "m1"), sessionruntime.RunIntentCompleted)
			if c := f.confirmation(sessionID); c.ID != "" {
				t.Fatalf("a confirmation was recorded: %+v", c)
			}
			if !strings.Contains(f.lastReply(sessionID), "shipped") {
				t.Fatalf("reply = %q", f.lastReply(sessionID))
			}
		})
	}
}

func waitForPendingConfirmationTestCall(t *testing.T, permissions *permission.Service, sessionID string) pebblestore.PermissionRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pending, err := permissions.ListPending(sessionID, 10); err == nil && len(pending) > 0 {
			return pending[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no pending client tool call")
	return pebblestore.PermissionRecord{}
}

// Purpose: a confirmation is session state: a decision names it through its
// own session only, and a pending confirmation and an approved-but-unanswered
// call both survive a daemon restart (the run-wait reconciliation at startup
// must not cancel the approved call).
// Threat: approving another visitor's action by its ID, or a restart silently
// dropping an approval so the conversation hangs.
// Boundary: handleSessionV3Confirmations (session lookup),
// permission.ReconcilePendingRuns, clientToolConfirmationExecution (no run ID).
// Narrowest layer: two daemons opened in turn over the same store.
func TestConfirmationIsSessionScopedAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.pebble")
	f := newConfirmationFixture(t, path, nil)
	sessionA := f.createSession("visitor-a")
	sessionB := f.createSession("visitor-b")
	f.say(sessionA, "m1", "order please")
	c := f.confirmation(sessionA)
	approve := fmt.Sprintf(`{"decision":"approve","arguments_hash":%q}`, c.ArgumentsHash)
	if code, _, body := f.decide(sessionB, c.ID, approve); code != http.StatusNotFound {
		t.Fatalf("approved session A's confirmation through session B: %d %s", code, body)
	}
	if code, body := f.do(http.MethodGet, "/v3/sessions/"+sessionB+"/confirmations/"+c.ID, ""); code != http.StatusNotFound {
		t.Fatalf("read session A's confirmation through session B: %d %s", code, body)
	}

	restart := func() {
		f.close()
		f = newConfirmationFixture(t, path, nil)
		if err := f.permissions.ReconcilePendingRuns("daemon restarted"); err != nil {
			t.Fatal(err)
		}
	}
	restart()
	if got := f.confirmation(sessionA); got.ID != c.ID || got.Status != runruntime.ClientToolConfirmationPending {
		t.Fatalf("pending confirmation after restart: %+v", got)
	}
	code, approved, body := f.decide(sessionA, c.ID, approve)
	if code != http.StatusOK || approved.Status != runruntime.ClientToolConfirmationApproved {
		t.Fatalf("approve after restart: %d %s", code, body)
	}
	restart()
	pending := f.pendingClientCalls(sessionA)
	if len(pending) != 1 || pending[0].ID != approved.ExecutionPermissionID {
		t.Fatalf("approved call after restart: %+v", pending)
	}
	if code, body := f.do(http.MethodPost, "/v3/sessions/"+sessionA+"/permissions/"+pending[0].ID+"/resolve", `{"action":"deny","reason":"out of stock"}`); code != http.StatusOK {
		t.Fatalf("gateway failure: %d %s", code, body)
	}
	delivered := f.confirmation(sessionA)
	f.waitRun(sessionA, delivered.ContinuationRunID, sessionruntime.RunIntentCompleted)
	if reply := f.lastReply(sessionA); !strings.Contains(reply, "out of stock") || !strings.Contains(reply, `"confirmation":"approved"`) {
		t.Fatalf("model reply = %q", reply)
	}
	if got := f.confirmation(sessionB); got.ID != "" {
		t.Fatalf("session B gained a confirmation: %+v", got)
	}
}

// Purpose: expiry does not depend on the in-process timer. After a restart
// (no timer), an expired confirmation is closed when the gateway next reads
// the session's confirmations (outcome plus a run), or when a visitor posts a
// message (outcome first, carried by that message's run, no extra run).
// Threat: a restart leaving a card approvable forever, or the model never
// told that nobody answered.
// Boundary: advanceClientToolConfirmationLocked via handleSessionV3Confirmations
// and handleSessionV3PrimaryMessages. Narrowest layer: two daemons in turn
// over one store, the first stopped before its timer fires.
func TestExpiredConfirmationClosesWithoutTheTimer(t *testing.T) {
	for _, trigger := range []string{"read", "message"} {
		t.Run(trigger, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), trigger+".pebble")
			short := &pebblestore.AgentCustomToolDefinition{Name: "place_order", Kind: pebblestore.AgentCustomToolKindClient, Description: "Place an order.", InputSchema: placeOrderSchema, Effect: "write", ConfirmTimeoutMS: 200}
			f := newConfirmationFixture(t, path, short)
			sessionID := f.createSession(trigger)
			f.say(sessionID, "m1", "order please")
			f.server.stopClientToolConfirmationTimer(sessionID)
			f.close()
			time.Sleep(300 * time.Millisecond)
			f = newConfirmationFixture(t, path, short)
			if c := f.confirmation(sessionID); c.Status != runruntime.ClientToolConfirmationPending || c.Delivered {
				t.Fatalf("closed before anything touched it: %+v", c)
			}
			if trigger == "read" {
				if code, body := f.do(http.MethodGet, "/v3/sessions/"+sessionID+"/confirmations", ""); code != http.StatusOK || !strings.Contains(body, `"confirmations":[]`) {
					t.Fatalf("read: %d %s", code, body)
				}
				c := f.confirmation(sessionID)
				if c.Status != runruntime.ClientToolConfirmationExpired || !c.Delivered || c.ContinuationRunID == "" {
					t.Fatalf("after read: %+v", c)
				}
				f.waitRun(sessionID, c.ContinuationRunID, sessionruntime.RunIntentCompleted)
			} else {
				f.say(sessionID, "m2", "hello again")
				c := f.confirmation(sessionID)
				if c.Status != runruntime.ClientToolConfirmationExpired || !c.Delivered || c.ContinuationRunID != "" {
					t.Fatalf("after message: %+v", c)
				}
			}
			if reply := f.lastReply(sessionID); !strings.Contains(reply, "no answer") {
				t.Fatalf("model reply = %q", reply)
			}
		})
	}
}

// Purpose: the gateway token reaches the confirmation routes of its own
// agent's sessions only; the operator token and other sessions are refused.
// Threat: a leaked hub key approving visitors' actions, or a gateway key
// deciding confirmations in the owner's sessions.
// Boundary: agentBoundRequestError. Narrowest layer: the gate with real minted
// tokens and seeded sessions; handlers are not run.
func TestConfirmationRoutesAreLimitedToTheGatewayToken(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "confirm-gate"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	s.perm = permission.NewService(pebblestore.NewPermissionStore(db), nil, nil)
	s.agents = agentruntime.NewService(pebblestore.NewAgentStore(db), events)
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	account := actor.AccountScopeID
	if _, err := s.agents.PutCustomToolForAccount(account, pebblestore.AgentCustomToolDefinition{Name: "place_order", Kind: pebblestore.AgentCustomToolKindClient, Description: "x", InputSchema: placeOrderSchema, Effect: "write"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.agents.UpsertForAccount(account, agentruntime.UpsertInput{Name: "frontdesk", Mode: agentruntime.ModeSubagent, Enabled: pebblestore.BoolPtr(true), Prompt: "x",
		ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"place_order": {Enabled: pebblestore.BoolPtr(true)}}}}); err != nil {
		t.Fatal(err)
	}
	for _, seed := range []struct{ id, agent string }{{"desk", "frontdesk"}, {"owner", "swarm"}} {
		snapshot := pebblestore.SessionSnapshot{ID: seed.id, UserID: actor.UserID, AccountScopeID: account, Title: seed.id, Mode: "auto", Metadata: map[string]any{"agent_name": seed.agent}}
		if _, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: seed.id, UserID: actor.UserID, AccountScopeID: account, Session: &snapshot, IdempotencyKey: seed.id, PayloadHash: seed.id, NowUnixMs: 1000}); err != nil {
			t.Fatal(err)
		}
	}
	mint := func(body string) pebblestore.ScopedTokenRecord {
		w := httptest.NewRecorder()
		s.handleAuthTokens(w, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v3/auth/tokens", strings.NewReader(body)), actor.UserID, account))
		var out struct {
			Record pebblestore.ScopedTokenRecord `json:"record"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
			t.Fatalf("mint %s: %d %s", body, w.Code, w.Body.String())
		}
		return out.Record
	}
	gateway := mint(`{"name":"gw","agent_name":"frontdesk","expires_in_seconds":3600}`)
	operator := mint(`{"name":"hub","agent_name":"frontdesk","operator":true,"expires_in_seconds":3600}`)
	allowed := func(rec pebblestore.ScopedTokenRecord, method, path string) bool {
		return s.agentBoundRequestError(httptest.NewRequest(method, path, strings.NewReader(`{"decision":"reject"}`)), &rec) == nil
	}
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v3/sessions/desk/confirmations"},
		{"GET", "/v3/sessions/desk/confirmations/conf_1"},
		{"POST", "/v3/sessions/desk/confirmations/conf_1"},
	} {
		if !allowed(gateway, tc.method, tc.path) {
			t.Fatalf("gateway token refused %s %s", tc.method, tc.path)
		}
		if allowed(operator, tc.method, tc.path) {
			t.Fatalf("operator token allowed %s %s", tc.method, tc.path)
		}
		if owner := strings.Replace(tc.path, "/desk/", "/owner/", 1); allowed(gateway, tc.method, owner) {
			t.Fatalf("gateway token reached another agent's session: %s %s", tc.method, owner)
		}
	}
	for _, tc := range []struct{ method, path string }{
		{"POST", "/v3/sessions/desk/confirmations"},
		{"DELETE", "/v3/sessions/desk/confirmations/conf_1"},
		{"POST", "/v3/sessions/desk/confirmations/conf_1/extra"},
	} {
		if allowed(gateway, tc.method, tc.path) {
			t.Fatalf("gateway token allowed %s %s", tc.method, tc.path)
		}
	}
}

// Purpose: a confirmed tool's schema may not define the argument Swarm adds;
// the stored definition makes confirm explicit and bounds the expiry.
// Boundary: agent.Service.PutCustomToolForAccount, NormalizeAgentCustomToolDefinition.
// Narrowest layer: the custom tool PUT route.
func TestConfirmedClientToolDefinitionIsValidated(t *testing.T) {
	server, sessions, permissions, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	server.runner = runruntime.NewService(sessions, server.model, nil, tool.NewRuntime(1), permissions, server.agents, nil, nil)
	put := func(name, body string) (int, string) {
		req := withTestPrincipal(httptest.NewRequest(http.MethodPut, "/v2/custom-tools/"+name, bytes.NewBufferString(body)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	clash := `{"kind":"client","effect":"write","input_schema":{"type":"object","properties":{"summary":{"type":"string"}}}}`
	if code, body := put("send_note", clash); code != http.StatusBadRequest || !strings.Contains(body, "summary") {
		t.Fatalf("write tool defining summary: %d %s", code, body)
	}
	if code, body := put("send_note", strings.Replace(clash, `"effect":"write"`, `"effect":"write","confirm":false`, 1)); code != http.StatusOK {
		t.Fatalf("unconfirmed tool may define summary: %d %s", code, body)
	}
	schema, _ := json.Marshal(placeOrderSchema)
	if code, body := put("place_order", `{"kind":"client","effect":"write","confirm_timeout_ms":999999999,"input_schema":`+string(schema)+`}`); code != http.StatusOK {
		t.Fatalf("write tool: %d %s", code, body)
	}
	stored, ok, err := server.agents.GetCustomToolForAccount(testPrincipal().AccountScopeID, "place_order")
	if err != nil || !ok || stored.Confirm == nil || !*stored.Confirm || stored.ConfirmTimeoutMS != pebblestore.AgentClientToolMaxConfirmTimeoutMS {
		t.Fatalf("stored = %+v", stored)
	}
	if code, body := put("lookup_order", `{"kind":"client","input_schema":`+string(schema)+`}`); code != http.StatusOK {
		t.Fatalf("read tool: %d %s", code, body)
	}
	read, _, _ := server.agents.GetCustomToolForAccount(testPrincipal().AccountScopeID, "lookup_order")
	if read.Confirm == nil || *read.Confirm || read.ConfirmTimeoutMS != 0 {
		t.Fatalf("read tool stored = %+v", read)
	}
	// A definition saved before confirm existed: write means confirmed.
	if !pebblestore.ClientToolRequiresConfirmation(pebblestore.AgentCustomToolDefinition{Kind: "client", Effect: "write"}) || pebblestore.ClientToolRequiresConfirmation(pebblestore.AgentCustomToolDefinition{Kind: "client", Effect: "read"}) {
		t.Fatal("legacy definitions are not confirmed by effect")
	}
}

// gatewayRecord is a gateway (agent-bound) token for the fixture's agent.
func (f *confirmationFixture) gatewayRecord() *pebblestore.ScopedTokenRecord {
	return &pebblestore.ScopedTokenRecord{ID: "tok_gw", AgentName: "frontdesk", AccountScopeID: testPrincipal().AccountScopeID, Scopes: []string{"sessions:read", "sessions:write"}}
}

// asGateway sends a request as the gateway token: through the agent-bound
// gate (route allow-list, limits), then the routed handler.
func (f *confirmationFixture) asGateway(method, path, body string) (int, map[string]any) {
	f.t.Helper()
	gateway := f.gatewayRecord()
	r := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(method, path, bytes.NewBufferString(body))), gateway)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	if f.server.gateAgentBoundToken(w, r, gateway) {
		f.server.Handler().ServeHTTP(w, r)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// createEndUserSession creates a session the way a gateway does, so the
// create handler stores the end-user binding (alice, plan=pro).
func (f *confirmationFixture) createEndUserSession(title string) string {
	f.t.Helper()
	workspace := f.t.TempDir()
	bindingID := seedSessionsV3PrimaryAuthority(f.t, f.server, workspace)
	raw, _ := json.Marshal(map[string]any{
		"client_request_id": "create-" + title, "workspace_path": workspace, "workspace_name": filepath.Base(workspace),
		"swarm_id": "host-swarm-id", "workspace_binding_id": bindingID, "target_kind": "host", "target_relationship": "self",
		"title": title, "mode": sessionruntime.ModeAuto, "agent_name": "frontdesk", "end_user": "alice", "labels": map[string]string{"plan": "pro"},
		"preference": pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model"},
	})
	create := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewReader(raw))), f.gatewayRecord())
	create.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(w, create)
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
		EndUser string                      `json:"end_user"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || w.Code != http.StatusOK || created.EndUser != "alice" {
		f.t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	return created.Session.ID
}

// endUserOf returns a response's (or item's) end_user and labels.plan.
func endUserOf(out map[string]any) (string, string) {
	labels, _ := out["labels"].(map[string]any)
	endUser, _ := out["end_user"].(string)
	plan, _ := labels["plan"].(string)
	return endUser, plan
}

// Purpose: a confirmation on a gateway session with an end user reaches that
// gateway token with the session's stored end user and labels, and the call
// the approval creates is delivered with the same end user, so the gateway
// runs it for the visitor who owns the session. Threat: an approved write
// running for the wrong visitor because the confirmation lost the binding.
// Boundary: gateAgentBoundToken, handleSessionV3Confirmations,
// writeConfirmationResponse, handleSessionV3PrimaryPermissions.
// Narrowest layer: the real executor and routes with the gate in front.
func TestConfirmationOnEndUserSessionCarriesTheEndUser(t *testing.T) {
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "confirm-end-user.pebble"), nil)
	sessionID := f.createEndUserSession("alice")
	f.say(sessionID, "m1", "order 2 of AB12CD please")

	code, listed := f.asGateway(http.MethodGet, "/v3/sessions/"+sessionID+"/confirmations", "")
	confirmations, _ := listed["confirmations"].([]any)
	if code != http.StatusOK || len(confirmations) != 1 {
		t.Fatalf("gateway confirmations: %d %v", code, listed)
	}
	if endUser, plan := endUserOf(listed); endUser != "alice" || plan != "pro" {
		t.Fatalf("confirmations end_user/labels = %q/%q, want the stored alice/pro", endUser, plan)
	}
	c := f.confirmation(sessionID)
	code, decided := f.asGateway(http.MethodPost, "/v3/sessions/"+sessionID+"/confirmations/"+c.ID, fmt.Sprintf(`{"decision":"approve","arguments_hash":%q}`, c.ArgumentsHash))
	if endUser, _ := endUserOf(decided); code != http.StatusOK || endUser != "alice" {
		t.Fatalf("gateway approve: %d %v", code, decided)
	}
	code, delivery := f.asGateway(http.MethodGet, "/v3/sessions/"+sessionID+"/permissions?status=pending&limit=100", "")
	calls, _ := delivery["permissions"].([]any)
	if code != http.StatusOK || len(calls) != 1 || calls[0].(map[string]any)["tool_name"] != "place_order" {
		t.Fatalf("approved call delivery: %d %v", code, delivery)
	}
	if endUser, plan := endUserOf(delivery); endUser != "alice" || plan != "pro" {
		t.Fatalf("approved call end_user/labels = %q/%q, want alice/pro", endUser, plan)
	}
}

// waitReasons follows a session's wait from cursor (wait_seconds=0, so each
// read answers at once) until it is caught up, and returns every reason seen,
// the last answer and its cursor.
func (f *confirmationFixture) waitReasons(sessionID string, cursor uint64) (map[string]bool, map[string]any, uint64) {
	f.t.Helper()
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		code, out := f.asGateway(http.MethodGet, fmt.Sprintf("/v3/sessions/%s/wait?after=%d&wait_seconds=0", sessionID, cursor), "")
		if code != http.StatusOK {
			f.t.Fatalf("wait: %d %v", code, out)
		}
		reasons, _ := out["reasons"].([]any)
		for _, reason := range reasons {
			seen[reason.(string)] = true
		}
		next := uint64(out["cursor"].(float64))
		if next == cursor && len(reasons) == 0 {
			return seen, out, cursor
		}
		cursor = next
	}
	f.t.Fatal("wait never caught up")
	return nil, nil, 0
}

// Purpose: the gateway's wait, event feed, session list and session end cover
// confirmations and end users together. A confirmation wakes the wait
// (reason "confirmation") and comes back in it with the stored end user; the
// approval wakes it again with the approved call (reason "tool_call"). The
// feed records confirmation.requested and confirmation.decided, and every
// entry and listed session carries end_user and labels. Ending the
// conversation closes its confirmation without starting a run (a pending one
// expires, an approved call is failed) and a later decision is refused (409).
// Threat: a gateway missing an approval card, running an approved write after
// the conversation ended, or a console attributing events to the wrong visitor.
// Boundary: handleSessionV3Wait, sessionWaitReasons, agentEventFromSessionEvent,
// handleSessionsV3AgentSessions, handleSessionV3End,
// closeClientToolConfirmationForEndLocked, decideClientToolConfirmation.
// Narrowest layer: the real executor and routes behind the agent-bound gate.
func TestGatewayWaitFeedAndEndCoverConfirmationsAndEndUsers(t *testing.T) {
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "confirm-wait.pebble"), nil)

	// A pending confirmation wakes the wait and is in it, with the end user.
	asked := f.createEndUserSession("asked")
	_, first := f.asGateway(http.MethodGet, "/v3/sessions/"+asked+"/wait", "")
	start := uint64(first["cursor"].(float64))
	f.say(asked, "m1", "order 2 of AB12CD please")
	seen, state, cursor := f.waitReasons(asked, start)
	pending, _ := state["pending_confirmations"].([]any)
	if !seen["confirmation"] || len(pending) != 1 {
		t.Fatalf("wait reasons %v, pending_confirmations %v", seen, state["pending_confirmations"])
	}
	if endUser, plan := endUserOf(state); endUser != "alice" || plan != "pro" {
		t.Fatalf("wait end_user/labels = %q/%q", endUser, plan)
	}
	c := f.confirmation(asked)
	if pending[0].(map[string]any)["id"] != c.ID {
		t.Fatalf("wait listed %v, want %s", pending[0], c.ID)
	}

	// Approving wakes the wait with the approved call.
	if code, out := f.asGateway(http.MethodPost, "/v3/sessions/"+asked+"/confirmations/"+c.ID, fmt.Sprintf(`{"decision":"approve","arguments_hash":%q}`, c.ArgumentsHash)); code != http.StatusOK {
		t.Fatalf("approve: %d %v", code, out)
	}
	seen, state, _ = f.waitReasons(asked, cursor)
	calls, _ := state["pending_calls"].([]any)
	if !seen["tool_call"] || len(calls) != 1 || calls[0].(map[string]any)["tool_name"] != "place_order" {
		t.Fatalf("after approval: reasons %v, pending_calls %v", seen, state["pending_calls"])
	}
	if pending, _ := state["pending_confirmations"].([]any); len(pending) != 0 {
		t.Fatalf("an approved confirmation is still pending in the wait: %v", pending)
	}

	// Ending fails the approved call; the gateway can no longer run it.
	if code, out := f.asGateway(http.MethodPost, "/v3/sessions/"+asked+"/end", `{"client_request_id":"end-asked"}`); code != http.StatusOK {
		t.Fatalf("end: %d %v", code, out)
	}
	if left := f.pendingClientCalls(asked); len(left) != 0 {
		t.Fatalf("the approved call survived the end: %+v", left)
	}
	if closed := f.confirmation(asked); !closed.Delivered || closed.ContinuationRunID != "" {
		t.Fatalf("closing after end: %+v (no run may start)", closed)
	}

	// A pending confirmation expires when the conversation ends; deciding it
	// afterwards is refused.
	open := f.createEndUserSession("open")
	f.say(open, "m1", "order 2 of AB12CD please")
	pendingOne := f.confirmation(open)
	if code, out := f.asGateway(http.MethodPost, "/v3/sessions/"+open+"/end", `{"client_request_id":"end-open"}`); code != http.StatusOK {
		t.Fatalf("end: %d %v", code, out)
	}
	if closed := f.confirmation(open); closed.Status != runruntime.ClientToolConfirmationExpired || !closed.Delivered || closed.ContinuationRunID != "" {
		t.Fatalf("pending confirmation after end: %+v", closed)
	}
	if code, out := f.asGateway(http.MethodPost, "/v3/sessions/"+open+"/confirmations/"+pendingOne.ID, fmt.Sprintf(`{"decision":"approve","arguments_hash":%q}`, pendingOne.ArgumentsHash)); code != http.StatusConflict {
		t.Fatalf("decision on an ended conversation: %d %v", code, out)
	}
	if got := f.callCount(); got != 2 {
		t.Fatalf("model calls = %d, want 2 (ending started no run)", got)
	}

	// The feed and the listing carry the end user and the confirmation steps.
	code, feed := f.asGateway(http.MethodGet, "/v3/sessions:agent-events?limit=500", "")
	events, _ := feed["events"].([]any)
	if code != http.StatusOK || len(events) == 0 {
		t.Fatalf("feed: %d %v", code, feed)
	}
	kinds := map[string][]map[string]any{}
	for _, raw := range events {
		event := raw.(map[string]any)
		if endUser, plan := endUserOf(event); endUser != "alice" || plan != "pro" {
			t.Fatalf("feed entry without the end user: %v", event)
		}
		kinds[event["kind"].(string)] = append(kinds[event["kind"].(string)], event)
	}
	if len(kinds["session.created"]) != 2 || len(kinds["confirmation.requested"]) != 2 || len(kinds["session.ended"]) != 2 {
		t.Fatalf("feed kinds: %v", feedKindCounts(kinds))
	}
	requested := kinds["confirmation.requested"][0]["data"].(map[string]any)
	if requested["tool"] != "place_order" || requested["summary"] != "Order 2 of AB12CD" || requested["arguments"] != `{"order_id":"AB12CD","quantity":2}` {
		t.Fatalf("confirmation.requested data: %v", requested)
	}
	decisions := map[string]bool{}
	for _, event := range kinds["confirmation.decided"] {
		decisions[event["data"].(map[string]any)["decision"].(string)] = true
	}
	if !decisions["approved"] || !decisions["expired"] {
		t.Fatalf("confirmation.decided decisions: %v", decisions)
	}
	code, listing := f.asGateway(http.MethodGet, "/v3/sessions:agent", "")
	sessions, _ := listing["sessions"].([]any)
	if code != http.StatusOK || len(sessions) != 2 {
		t.Fatalf("listing: %d %v", code, listing)
	}
	for _, raw := range sessions {
		if endUser, plan := endUserOf(raw.(map[string]any)); endUser != "alice" || plan != "pro" {
			t.Fatalf("listed session without the end user: %v", raw)
		}
	}
}

func feedKindCounts(kinds map[string][]map[string]any) map[string]int {
	counts := map[string]int{}
	for kind, events := range kinds {
		counts[kind] = len(events)
	}
	return counts
}
