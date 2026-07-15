package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestSessionsV2LifecycleGetAndAppendUseLocalExecutionAuthority(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

	getReq := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+sessionID, nil)
	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, withTestPrincipal(getReq))
	if getRec.Code != http.StatusOK || !strings.Contains(getRec.Body.String(), sessionID) {
		t.Fatalf("get status=%d body=%s", getRec.Code, getRec.Body.String())
	}

	postReq := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/messages", bytes.NewBufferString(`{"role":"user","content":"hello v2"}`))
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(postRec, withTestPrincipal(postReq))
	if postRec.Code != http.StatusOK || !strings.Contains(postRec.Body.String(), "hello v2") {
		t.Fatalf("append status=%d body=%s", postRec.Code, postRec.Body.String())
	}
}

func TestSessionsV2LifecycleRejectsNonPrimaryExecution(t *testing.T) {
	server, sessionSvc, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)
	execution, ok, err := sessionSvc.Store().GetSessionExecutionV2(sessionID)
	if err != nil || !ok {
		t.Fatalf("get execution ok=%t err=%v", ok, err)
	}
	execution.ExecutionClass = sessionruntime.SessionExecutionClassLocalContainer
	session, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("get session ok=%t err=%v", ok, err)
	}
	if err := sessionSvc.Store().CreateSessionWithExecutionV2(session, execution); err != nil {
		t.Fatalf("replace execution: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+sessionID+"/messages", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "execution class") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSessionsV2LifecycleReadOnlyBindingAllowsReadBlocksMutation(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-readonly-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadOnly, false)

	getReq := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+sessionID+"/messages", nil)
	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, withTestPrincipal(getReq))
	if getRec.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", getRec.Code, getRec.Body.String())
	}
	postReq := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/messages", bytes.NewBufferString(`{"role":"user","content":"blocked"}`))
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(postRec, withTestPrincipal(postReq))
	if postRec.Code != http.StatusForbidden || !strings.Contains(postRec.Body.String(), "read-only") {
		t.Fatalf("write status=%d body=%s", postRec.Code, postRec.Body.String())
	}
}

func TestSessionsV2LifecycleRunUsesLocalRunner(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &primaryV2RunRequestRecordingRunner{}
	server.runner = runner
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)
	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/run", bytes.NewBufferString(`{"prompt":"hello","instructions":"safe","background":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	calls, gotSessionID, gotRequest, _ := runner.snapshot()
	if calls != 1 || gotSessionID != sessionID || gotRequest.Prompt != "hello" || gotRequest.TargetKind != "" || gotRequest.ExecutionContext != nil || gotRequest.ToolScope != nil {
		t.Fatalf("calls=%d session=%q request=%+v", calls, gotSessionID, gotRequest)
	}
}

func TestSessionsV2LifecycleMetadataRejectsAuthorityKeys(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)
	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/metadata", bytes.NewBufferString(`{"metadata":{"backend_url":"https://example.invalid"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "routing authority key") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type primaryV2RunRequestRecordingRunner struct {
	mu            sync.Mutex
	calls         int
	sessionID     string
	request       runruntime.RunRequest
	meta          runruntime.RunStartMeta
	emitLifecycle bool
	stopSessionID string
	stopRunID     string
	stopReason    string
}

func (r *primaryV2RunRequestRecordingRunner) RunTurn(_ context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta) (runruntime.RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.sessionID = sessionID
	r.request = request
	r.meta = meta
	return runruntime.RunResult{SessionID: sessionID, Background: request.Background}, nil
}

func (r *primaryV2RunRequestRecordingRunner) RunTurnStreaming(_ context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta, onEvent runruntime.StreamHandler) (runruntime.RunResult, error) {
	r.mu.Lock()
	r.calls++
	r.sessionID = sessionID
	r.request = request
	r.meta = meta
	emitLifecycle := r.emitLifecycle
	r.mu.Unlock()
	if emitLifecycle && onEvent != nil {
		onEvent(runruntime.StreamEvent{Type: runruntime.StreamEventSessionLifecycle, SessionID: sessionID, RunID: meta.RunID, Lifecycle: &pebblestore.SessionLifecycleSnapshot{SessionID: sessionID, RunID: meta.RunID, Active: true, OwnerTransport: meta.OwnerTransport}})
	}
	return runruntime.RunResult{SessionID: sessionID, Background: request.Background}, nil
}

func (r *primaryV2RunRequestRecordingRunner) snapshot() (int, string, runruntime.RunRequest, runruntime.RunStartMeta) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.sessionID, r.request, r.meta
}

func (r *primaryV2RunRequestRecordingRunner) StopSessionRun(sessionID, runID, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopSessionID, r.stopRunID, r.stopReason = sessionID, runID, reason
	return nil
}

func (r *primaryV2RunRequestRecordingRunner) ExecuteToolForSessionScope(context.Context, string, tool.Call) (string, error) {
	return "{}", nil
}
func (r *primaryV2RunRequestRecordingRunner) ListAgentToolDefinitions() []tool.Definition { return nil }
func (r *primaryV2RunRequestRecordingRunner) ListAgentToolDefinitionsForAccount(string) []tool.Definition {
	return nil
}
func (r *primaryV2RunRequestRecordingRunner) ResolveAgentToolContract(pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}
func (r *primaryV2RunRequestRecordingRunner) ResolveAgentToolContractForAccount(string, pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}

func createPrimarySessionV2ForLifecycleTest(t *testing.T, server *Server, swarmStore *pebblestore.SwarmStore, bindingID, accessMode string, writable bool) string {
	t.Helper()
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", bindingID, "/host/swarm-go")
	rec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","workspace_binding_id":"`+bindingID+`","title":"primary v2 lifecycle","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if accessMode != pebblestore.TopologyWorkspaceBindingAccessModeReadWrite || !writable {
		binding, ok, err := server.topology.GetWorkspaceBindingForAccount(testPrincipal().AccountScopeID, bindingID)
		if err != nil || !ok {
			t.Fatalf("get binding ok=%t err=%v", ok, err)
		}
		binding.AccessMode = accessMode
		binding.Writable = writable
		if _, err := server.topology.UpsertWorkspaceBinding(binding); err != nil {
			t.Fatalf("update binding: %v", err)
		}
	}
	return payload.Session.ID
}
