package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestSessionsV2LifecycleDoesNotReferenceLegacyHandlers(t *testing.T) {
	body, err := os.ReadFile("sessions_v2_lifecycle.go")
	if err != nil {
		t.Fatalf("read lifecycle file: %v", err)
	}
	for _, forbidden := range []string{
		"handleSessionByID",
		"handleSessions(",
		"createSessionFromRequest",
		"proxyRoutedSessionRequest",
		"localCanonicalSessionForRoutedFetch",
		"handleManagedHostSession",
		"sessionWorkspaceBindingForAccess",
		"enforceSessionBindingWriteAccess",
	} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("sessions_v2_lifecycle.go contains forbidden legacy/routed symbol %q", forbidden)
		}
	}
}

func TestSessionsV2LifecycleGetAndAppendUseExecutionAuthority(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

	getReq := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+sessionID, nil)
	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, withTestPrincipal(getReq))
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d, body=%s", getRec.Code, http.StatusOK, getRec.Body.String())
	}
	if !strings.Contains(getRec.Body.String(), `"ok":true`) || !strings.Contains(getRec.Body.String(), sessionID) {
		t.Fatalf("get body = %s", getRec.Body.String())
	}

	postReq := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/messages", bytes.NewBufferString(`{"role":"user","content":"hello v2"}`))
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(postRec, withTestPrincipal(postReq))
	if postRec.Code != http.StatusOK {
		t.Fatalf("append status = %d, want %d, body=%s", postRec.Code, http.StatusOK, postRec.Body.String())
	}
	if !strings.Contains(postRec.Body.String(), `"message"`) || !strings.Contains(postRec.Body.String(), "hello v2") {
		t.Fatalf("append body = %s", postRec.Body.String())
	}
}

func TestSessionsV2LifecycleRejectsMissingExecution(t *testing.T) {
	server, sessionSvc := newSessionAccessModeTestServer(t, pebblestore.TopologyWorkspaceBindingAccessModeReadWrite)
	pref := pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "medium"}
	session, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Title: "legacy", WorkspacePath: "/tmp/workspace", WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto, Preference: &pref})
	if err != nil {
		t.Fatalf("create legacy session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+session.ID, nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "session_v2_authority_not_found") {
		t.Fatalf("body = %s, want authority not found", rec.Body.String())
	}
}

func TestSessionsV2LifecycleRejectsStalePlacementGeneration(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)
	placement, ok, err := server.topology.GetRuntimePlacementForAccount(testPrincipal().AccountScopeID, "host-swarm-id")
	if err != nil || !ok {
		t.Fatalf("get placement ok=%t err=%v", ok, err)
	}
	placement.PlacementGeneration = 2
	if _, err := server.topology.PutRuntimePlacementForAccount(testPrincipal().AccountScopeID, placement); err != nil {
		t.Fatalf("put stale placement: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+sessionID+"/messages", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "placement generation mismatch") {
		t.Fatalf("body = %s, want generation mismatch", rec.Body.String())
	}
}

func TestSessionsV2LifecycleRejectsBindingAttestationMismatch(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)
	binding, ok, err := server.topology.GetWorkspaceBindingForAccount(testPrincipal().AccountScopeID, "binding-primary-v2")
	if err != nil || !ok {
		t.Fatalf("get binding ok=%t err=%v", ok, err)
	}
	binding.AttestedByHostSwarmID = "other-host-swarm-id"
	binding.LegacyTargetKind = "attestation-mismatch-v2-test"
	if _, err := server.topology.UpsertWorkspaceBinding(binding); err != nil {
		t.Fatalf("update binding attestation: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+sessionID+"/messages", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "attesting host does not match authority host") {
		t.Fatalf("body = %s, want attestation mismatch", rec.Body.String())
	}
}

func TestSessionsV2LifecycleRejectsIncompleteMatchingExecutionAndBindingAuthority(t *testing.T) {
	for _, tc := range []struct {
		name          string
		mutateExec    func(*pebblestore.SessionExecutionV2Record)
		mutateBinding func(*pebblestore.TopologyWorkspaceBindingRecord)
	}{
		{
			name: "source workspace id empty",
			mutateExec: func(execution *pebblestore.SessionExecutionV2Record) {
				execution.SourceWorkspaceID = ""
			},
			mutateBinding: func(binding *pebblestore.TopologyWorkspaceBindingRecord) {
				binding.SourceWorkspaceID = ""
			},
		},
		{
			name: "source workspace path empty",
			mutateExec: func(execution *pebblestore.SessionExecutionV2Record) {
				execution.SourceWorkspacePath = ""
			},
			mutateBinding: func(binding *pebblestore.TopologyWorkspaceBindingRecord) {
				binding.SourceWorkspacePath = ""
			},
		},
		{
			name: "runtime workspace path empty",
			mutateExec: func(execution *pebblestore.SessionExecutionV2Record) {
				execution.RuntimeWorkspacePath = ""
			},
			mutateBinding: func(binding *pebblestore.TopologyWorkspaceBindingRecord) {
				binding.DestinationWorkspacePath = ""
			},
		},
		{
			name: "source workspace generation zero",
			mutateExec: func(execution *pebblestore.SessionExecutionV2Record) {
				execution.SourceWorkspaceGeneration = 0
			},
			mutateBinding: func(binding *pebblestore.TopologyWorkspaceBindingRecord) {
				binding.SourceWorkspaceGeneration = 0
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, sessionSvc, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
			sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

			execution, ok, err := sessionSvc.Store().GetSessionExecutionV2(sessionID)
			if err != nil || !ok {
				t.Fatalf("get execution ok=%t err=%v", ok, err)
			}
			tc.mutateExec(&execution)
			session, ok, err := sessionSvc.GetSession(sessionID)
			if err != nil || !ok {
				t.Fatalf("get session ok=%t err=%v", ok, err)
			}
			if err := sessionSvc.Store().CreateSessionWithExecutionV2(session, execution); err != nil {
				t.Fatalf("corrupt execution: %v", err)
			}

			binding, ok, err := server.topology.GetWorkspaceBindingForAccount(testPrincipal().AccountScopeID, "binding-primary-v2")
			if err != nil || !ok {
				t.Fatalf("get binding ok=%t err=%v", ok, err)
			}
			tc.mutateBinding(&binding)
			binding.LegacyTargetKind = "incomplete-authority-v2-test"
			snapshot, err := server.topology.SnapshotForAccount(testPrincipal().AccountScopeID)
			if err != nil {
				t.Fatalf("snapshot topology: %v", err)
			}
			replaced := false
			for i := range snapshot.WorkspaceBindings {
				if snapshot.WorkspaceBindings[i].BindingID == binding.BindingID {
					snapshot.WorkspaceBindings[i] = binding
					replaced = true
					break
				}
			}
			if !replaced {
				t.Fatalf("workspace binding %q missing from snapshot", binding.BindingID)
			}
			if err := server.topology.ReplaceSnapshotForAccount(testPrincipal().AccountScopeID, snapshot); err != nil {
				t.Fatalf("corrupt binding: %v", err)
			}

			req := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+sessionID+"/messages", nil)
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "authority identity is incomplete") {
				t.Fatalf("body = %s, want incomplete authority rejection", rec.Body.String())
			}
		})
	}
}

func TestSessionsV2LifecycleMetadataUpdateRejectsAuthorityKeys(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "backend_url", body: `{"metadata":{"backend_url":"https://example.invalid/backend"}}`},
		{name: "workspace_path", body: `{"metadata":{"workspace_path":"workspace-path"}}`},
		{name: "target_swarm_id", body: `{"metadata":{"target_swarm_id":"target-swarm"}}`},
		{name: "swarm_v2_runtime_swarm_id", body: `{"metadata":{"swarm_v2_runtime_swarm_id":"runtime-swarm"}}`},
		{name: "local_workspace_binding_id", body: `{"metadata":{"local_workspace_binding_id":"binding-primary-v2"}}`},
		{name: "nested", body: `{"metadata":{"safe":{"workspace_path":"/workspace"}}}`},
		{name: "nested_slice", body: `{"metadata":{"safe":[{"backend_url":"https://example.invalid/backend"}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/metadata", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("metadata status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "routing authority key") {
				t.Fatalf("body = %s, want routing authority rejection", rec.Body.String())
			}
		})
	}
}

func TestSessionsV2LifecycleMetadataUpdateAcceptsSafeMetadata(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/metadata", bytes.NewBufferString(`{"metadata":{"ticket":"abc-123","labels":["safe"],"nested":{"note":"ok"}}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("metadata status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ticket":"abc-123"`) || !strings.Contains(rec.Body.String(), `"note":"ok"`) {
		t.Fatalf("metadata body = %s, want safe metadata", rec.Body.String())
	}
}

func TestSessionsV2LifecycleRunRejectsRequestTimeAuthorityOverrides(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "target_kind", body: `{"prompt":"hello","target_kind":"subagent"}`, want: "target_kind"},
		{name: "target_name", body: `{"prompt":"hello","target_name":"memory"}`, want: "target_name"},
		{name: "execution_context_workspace_path", body: `{"prompt":"hello","execution_context":{"workspace_path":"override-workspace"}}`, want: "execution_context.workspace_path"},
		{name: "execution_context_cwd", body: `{"prompt":"hello","execution_context":{"cwd":"override-cwd"}}`, want: "execution_context.cwd"},
		{name: "execution_context_worktree_root_path", body: `{"prompt":"hello","execution_context":{"worktree_root_path":"override-worktree"}}`, want: "execution_context.worktree_root_path"},
		{name: "execution_context_worktree_mode", body: `{"prompt":"hello","execution_context":{"worktree_mode":"off"}}`, want: "execution_context.worktree_mode"},
		{name: "execution_context_worktree_branch", body: `{"prompt":"hello","execution_context":{"worktree_branch":"feature"}}`, want: "execution_context.worktree_branch"},
		{name: "execution_context_worktree_base_branch", body: `{"prompt":"hello","execution_context":{"worktree_base_branch":"main"}}`, want: "execution_context.worktree_base_branch"},
		{name: "tool_scope", body: `{"prompt":"hello","tool_scope":{}}`, want: "tool_scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
			server.runner = &primaryV2RunRequestRecordingRunner{}
			sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

			req := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/run", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("run status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "session_v2_bad_request") || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("body = %s, want bad request mentioning %s", rec.Body.String(), tc.want)
			}
		})
	}
}

func TestSessionsV2LifecycleRunStreamControlRejectsRequestTimeAuthorityOverrides(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "target_kind", body: `{"type":"run.start","prompt":"hello","target_kind":"subagent"}`, want: "target_kind"},
		{name: "target_name", body: `{"type":"run.start","prompt":"hello","target_name":"memory"}`, want: "target_name"},
		{name: "execution_context_workspace_path", body: `{"type":"run.start","prompt":"hello","execution_context":{"workspace_path":"override-workspace"}}`, want: "execution_context.workspace_path"},
		{name: "execution_context_cwd", body: `{"type":"run.start","prompt":"hello","execution_context":{"cwd":"override-cwd"}}`, want: "execution_context.cwd"},
		{name: "execution_context_worktree_root_path", body: `{"type":"run.start","prompt":"hello","execution_context":{"worktree_root_path":"override-worktree"}}`, want: "execution_context.worktree_root_path"},
		{name: "execution_context_worktree_mode", body: `{"type":"run.start","prompt":"hello","execution_context":{"worktree_mode":"off"}}`, want: "execution_context.worktree_mode"},
		{name: "execution_context_worktree_branch", body: `{"type":"run.start","prompt":"hello","execution_context":{"worktree_branch":"feature"}}`, want: "execution_context.worktree_branch"},
		{name: "execution_context_worktree_base_branch", body: `{"type":"run.start","prompt":"hello","execution_context":{"worktree_base_branch":"main"}}`, want: "execution_context.worktree_base_branch"},
		{name: "tool_scope", body: `{"type":"run.start","prompt":"hello","tool_scope":{}}`, want: "tool_scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
			server.runner = &primaryV2RunRequestRecordingRunner{}
			sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

			req := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/run/stream", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("run stream status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "session_v2_bad_request") || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("body = %s, want bad request mentioning %s", rec.Body.String(), tc.want)
			}
		})
	}
}

func TestSessionsV2LifecycleRunAllowsSafeInstructionsAndBackgroundOwnership(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &primaryV2RunRequestRecordingRunner{}
	server.runner = runner
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/run", bytes.NewBufferString(`{"prompt":"hello","instructions":"safe user instructions","agent_name":"swarm","background":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("run status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	calls, recordedSessionID, recordedRequest, _ := runner.snapshot()
	if calls != 1 || recordedSessionID != sessionID {
		t.Fatalf("runner calls=%d session_id=%q, want one call for %q", calls, recordedSessionID, sessionID)
	}
	if recordedRequest.Prompt != "hello" || recordedRequest.Instructions != "safe user instructions" || !recordedRequest.Background {
		t.Fatalf("runner request = %+v", recordedRequest)
	}
	if recordedRequest.TargetKind != "" || recordedRequest.TargetName != "" || recordedRequest.ExecutionContext != nil || recordedRequest.ToolScope != nil {
		t.Fatalf("runner request carried authority override: %+v", recordedRequest)
	}
}

func TestSessionsV2LifecycleRunStreamControlAllowsSafeBackgroundOwnership(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &primaryV2RunRequestRecordingRunner{emitLifecycle: true}
	server.runner = runner
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/run/stream", bytes.NewBufferString(`{"type":"run.start","prompt":"hello","instructions":"safe user instructions","background":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("run stream status = %d, want %d, body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	calls, recordedSessionID, recordedRequest, recordedMeta := runner.snapshot()
	if calls != 1 || recordedSessionID != sessionID {
		t.Fatalf("runner calls=%d session_id=%q, want one call for %q", calls, recordedSessionID, sessionID)
	}
	if recordedRequest.Prompt != "hello" || recordedRequest.Instructions != "safe user instructions" || !recordedRequest.Background {
		t.Fatalf("runner request = %+v", recordedRequest)
	}
	if recordedRequest.TargetKind != "" || recordedRequest.TargetName != "" || recordedRequest.ExecutionContext != nil || recordedRequest.ToolScope != nil {
		t.Fatalf("runner request carried authority override: %+v", recordedRequest)
	}
	if recordedMeta.OwnerTransport != "background_api" {
		t.Fatalf("owner transport = %q, want background_api", recordedMeta.OwnerTransport)
	}
}

func TestSessionsV2LifecycleReadOnlyBindingAllowsReadBlocksMutation(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-readonly-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadOnly, false)

	getReq := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+sessionID+"/messages", nil)
	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, withTestPrincipal(getReq))
	if getRec.Code != http.StatusOK {
		t.Fatalf("read status = %d, want %d, body=%s", getRec.Code, http.StatusOK, getRec.Body.String())
	}

	postReq := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/messages", bytes.NewBufferString(`{"role":"user","content":"blocked"}`))
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(postRec, withTestPrincipal(postReq))
	if postRec.Code != http.StatusForbidden {
		t.Fatalf("write status = %d, want %d, body=%s", postRec.Code, http.StatusForbidden, postRec.Body.String())
	}
	if !strings.Contains(postRec.Body.String(), "read-only") {
		t.Fatalf("body = %s, want read-only rejection", postRec.Body.String())
	}
}

type primaryV2RunRequestRecordingRunner struct {
	mu            sync.Mutex
	calls         int
	sessionID     string
	request       runruntime.RunRequest
	meta          runruntime.RunStartMeta
	emitLifecycle bool
}

func (r *primaryV2RunRequestRecordingRunner) RunTurn(_ context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta) (runruntime.RunResult, error) {
	r.mu.Lock()
	r.calls++
	r.sessionID = sessionID
	r.request = request
	r.meta = meta
	r.mu.Unlock()
	return runruntime.RunResult{SessionID: sessionID, Background: request.Background, TargetKind: request.TargetKind, TargetName: request.TargetName}, nil
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
	return runruntime.RunResult{SessionID: sessionID, Background: request.Background, TargetKind: request.TargetKind, TargetName: request.TargetName}, nil
}

func (r *primaryV2RunRequestRecordingRunner) snapshot() (int, string, runruntime.RunRequest, runruntime.RunStartMeta) {
	r.mu.Lock()
	defer r.mu.Unlock()
	request := r.request
	if request.ToolScope != nil {
		scope := *request.ToolScope
		request.ToolScope = &scope
	}
	if request.ExecutionContext != nil {
		ctx := *request.ExecutionContext
		request.ExecutionContext = &ctx
	}
	return r.calls, r.sessionID, request, r.meta
}

func (r *primaryV2RunRequestRecordingRunner) StopSessionRun(sessionID, runID, reason string) error {
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
		t.Fatalf("create status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if strings.TrimSpace(payload.Session.ID) == "" {
		t.Fatalf("missing created session id: %s", rec.Body.String())
	}
	if accessMode != pebblestore.TopologyWorkspaceBindingAccessModeReadWrite || !writable {
		binding, ok, err := server.topology.GetWorkspaceBindingForAccount(testPrincipal().AccountScopeID, bindingID)
		if err != nil || !ok {
			t.Fatalf("get binding ok=%t err=%v", ok, err)
		}
		binding.AccessMode = accessMode
		binding.Writable = writable
		if _, err := server.topology.UpsertWorkspaceBinding(binding); err != nil {
			t.Fatalf("update binding access mode: %v", err)
		}
	}
	return payload.Session.ID
}
