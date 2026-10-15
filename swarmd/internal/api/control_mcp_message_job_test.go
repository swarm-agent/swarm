package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// settledWorkerJobForMCP dispatches a direct worker job through the real
// worker route and settles it the way the executor and the reconcile loop do
// (its run intent completes, ReconcileWorker records the receipt).
func settledWorkerJobForMCP(t *testing.T, s *Server, h http.Handler, workspaceID, key string) (pebblestore.WorkerRecord, pebblestore.WorkerRunRecord) {
	t.Helper()
	w, err := s.sessions.Store().WorkerStore().CreateWorker("acct-test", "user-test", pebblestore.CreateWorkerRequest{Name: "Coder " + key, Role: pebblestore.WorkerRoleCoder, WorkspaceRequirements: []pebblestore.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w = activateWorkerAPIFixture(t, s, w, workspaceID)
	if response := executeWorkerAPI(h, http.MethodPost, "/"+w.ID+"/direct", `{"prompt":"Add a script","idempotency_key":"`+key+`"}`, workerAPICallOptions{scopes: []string{"automations:write"}}); response.Code != http.StatusCreated {
		t.Fatalf("dispatch: %d %s", response.Code, response.Body.String())
	}
	runs, _, err := s.sessions.ListWorkerRuns("acct-test", w.ID, 10, "")
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	r := runs[0]
	intent, _, err := s.sessions.GetSessionRunIntent(r.SessionID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	intent.Status = sessionruntime.RunIntentCompleted
	if _, err = s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: r.SessionID, UserID: "user-test", AccountScopeID: "acct-test", Kind: sessionruntime.SessionMutationRecordRunIntent, ClientRequestID: "done-" + key, IdempotencyKey: "done-" + key, PayloadHash: "done-" + key, RequestHash: "done-" + key, EventType: "session.run_intent.updated", RunIntent: &intent, NowUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	execution, err := s.workerExecutionService()
	if err != nil {
		t.Fatal(err)
	}
	if err = execution.ReconcileWorker(context.Background(), "acct-test", w.ID); err != nil {
		t.Fatal(err)
	}
	settled, _, err := s.sessions.Store().WorkerStore().GetWorkerRun("acct-test", w.ID, r.ID)
	if err != nil || settled.Status != "succeeded" {
		t.Fatalf("job did not settle: %+v %v", settled, err)
	}
	return w, settled
}

func messageJobForMCP(h http.Handler, jobID, message, requestID string) (any, error) {
	return callJobForMCP(h, "swarm_message_job", map[string]any{"job_id": jobID, "message": message, "client_request_id": requestID})
}

func callJobForMCP(h http.Handler, name string, args map[string]any) (any, error) {
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r = r.WithContext(context.WithValue(context.WithValue(r.Context(), productPrincipalRequestContextKey, identity.Principal{Type: "user", UserID: "user-test", AccountScopeID: "acct-test"}),
		productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: "acct-test", UserID: "user-test", Scopes: []string{"sessions:read", "sessions:write", "automations:read", "automations:write", "projects:read"}}))
	for _, tool := range controlMCPJobTools() {
		if tool.Name == name {
			if err := controlMCPValidateArgs(tool.InputSchema, args); err != nil {
				return nil, err
			}
			return tool.call(&controlMCPCall{request: r, next: h}, args)
		}
	}
	panic("unknown job tool")
}

// Purpose: swarm_message_job sends a reviewing AI's follow-up into a settled
// worker job, and returns an error, never {"accepted": true}, when the
// follow-up cannot start; a second follow-up while one is pending is refused
// clearly and appends nothing. Threat: the tool reported every follow-up as
// accepted while the run then failed at start ("worker revision or state
// conflict"), so the AI believed work had been sent back. Owners:
// controlMCPMessageJob, the V3 session message route (acceptSessionsV3Message)
// and WorkerExecutionService.PrepareFollowup. The real API mux, worker
// execution service, Git worktree and Pebble store are the narrowest layer
// joining the tool to the route that refuses; no provider runs, so the run
// gate itself is proven in the run package. Also require a 6,000-character
// follow-up to reach the durable session intact and oversized input to fail
// without appending a message, through both MCP and the underlying API.
func TestControlMCPMessageJobStartsOrFails(t *testing.T) {
	s, db, h := setupWorkerAPITestServer(t)
	workspaceID := setupWorkerAPIExecution(t, s, db)
	// A follow-up that cannot start (its worker was archived) is an error.
	other, settled := settledWorkerJobForMCP(t, s, h, workspaceID, "archived")
	execution, err := s.workerExecutionService()
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := s.sessions.Store().WorkerStore().GetWorker("acct-test", other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = execution.Stop(context.Background(), "acct-test", "user-test", other.ID, current.Revision, pebblestore.WorkerLifecycleStateArchived); err != nil {
		t.Fatal(err)
	}
	raw, err := messageJobForMCP(h, controlMCPJobID(controlMCPJobWorker, other.ID, settled.ID), "Try again", "archived")
	if err == nil || !strings.Contains(err.Error(), "archived") {
		t.Fatalf("follow-up that cannot start reported: %+v %v", raw, err)
	}
	if _, ok, err := s.sessions.GetSessionActiveRunIntent(settled.SessionID); err != nil || ok {
		t.Fatalf("refused follow-up left a run: %v %v", ok, err)
	}
	w, r := settledWorkerJobForMCP(t, s, h, workspaceID, "followup")
	jobID := controlMCPJobID(controlMCPJobWorker, w.ID, r.ID)

	beforeLong, err := s.sessions.ListSessionMessages(r.SessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	oversized := strings.Repeat("x", 100001)
	if _, err := messageJobForMCP(h, jobID, oversized, "too-long"); err == nil || !strings.Contains(err.Error(), "100000") {
		t.Fatalf("oversized MCP follow-up: %v", err)
	}
	if _, _, err := s.acceptSessionsV3Message(identity.Principal{Type: "user", UserID: "user-test", AccountScopeID: "acct-test"}, r.SessionID, sessionsV3MessageRequest{Role: "user", Content: oversized, ClientRequestID: "too-long-api"}); err == nil || !strings.Contains(err.Error(), "100000") {
		t.Fatalf("oversized API follow-up: %v", err)
	}
	afterLong, err := s.sessions.ListSessionMessages(r.SessionID, 0, 100)
	if err != nil || len(afterLong) != len(beforeLong) {
		t.Fatalf("oversized follow-up changed messages: %v", err)
	}
	message := strings.Repeat("界", 6000)
	raw, err = messageJobForMCP(h, jobID, message, "first")
	if err != nil {
		t.Fatalf("follow-up on a settled job refused: %v", err)
	}
	sent := controlMCPMap(controlMCPMap(raw)["sent"])
	runID, _ := sent["run_id"].(string)
	if sent["accepted"] != true || runID == "" {
		t.Fatalf("follow-up not accepted: %+v", raw)
	}
	active, ok, err := s.sessions.GetSessionActiveRunIntent(r.SessionID)
	if err != nil || !ok || active.RunID != runID || active.Status != sessionruntime.RunIntentPendingExecutor {
		t.Fatalf("follow-up run not pending in the job's session: %+v %v %v", active, ok, err)
	}
	messages := func() int {
		list, err := s.sessions.ListSessionMessages(r.SessionID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		return len(list)
	}
	stored, err := s.sessions.ListSessionMessages(r.SessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	foundMessage := false
	for _, item := range stored {
		foundMessage = foundMessage || item.Content == message
	}
	if !foundMessage {
		t.Fatal("worker session did not receive the complete 6000-character follow-up")
	}
	before := messages()
	if raw, err = messageJobForMCP(h, jobID, "And update the docs", "second"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second follow-up while one is pending: %+v %v", raw, err)
	}
	if got := messages(); got != before {
		t.Fatalf("refused follow-up appended a message: %d, want %d", got, before)
	}

}

// Purpose: swarm_get_job must not tell the AI a job "committed no changes,
// nothing to merge" while its worktree holds uncommitted or conflicted
// changes (staged files, an unfinished cherry-pick): the delivery assessment
// reports "empty" (no commits) with source_dirty set. It names the
// uncommitted changes and says to have them committed instead; a clean empty
// job keeps the report-only hint. Threat: an AI archives or ignores work the
// worker left uncommitted. Owners: controlMCPTaskResult and jobView; a stub
// task route carrying the assessment shape is the narrowest layer.
func TestControlMCPGetJobReportsUncommittedChanges(t *testing.T) {
	for _, dirty := range []bool{true, false} {
		t.Run(map[bool]string{true: "dirty", false: "clean"}[dirty], func(t *testing.T) {
			assessment := map[string]any{"state": "empty", "reason_code": "no_commits", "source_dirty": dirty}
			if dirty {
				assessment["source_dirty_files"] = []string{"src/app.go (both modified)", "src/new.go (added)"}
			}
			task := map[string]any{"id": "t1", "project_id": "p1", "status": "needs_review", "delivery_assessment": assessment}
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v3/workers/w1/runs/r1":
					_ = json.NewEncoder(w).Encode(map[string]any{"run": map[string]any{"status": "succeeded", "review": map[string]any{"project_id": "p1", "task_id": "t1"}}})
				case r.Method == http.MethodGet && r.URL.Path == "/v3/projects/p1/tasks/t1":
					_ = json.NewEncoder(w).Encode(map[string]any{"task": task})
				default:
					w.WriteHeader(http.StatusNotFound)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": "unexpected " + r.Method + " " + r.URL.Path})
				}
			})
			r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			r = r.WithContext(context.WithValue(r.Context(), productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: "acct", UserID: "user", Scopes: []string{"sessions:read"}}))
			raw, err := controlMCPGetJob(&controlMCPCall{request: r, next: h}, map[string]any{"job_id": "worker/w1/r1"})
			if err != nil {
				t.Fatal(err)
			}
			out := controlMCPMap(raw)
			next, _ := out["next"].(string)
			result := controlMCPMap(out["result"])
			if !dirty {
				if !strings.Contains(next, "nothing to merge") || result["uncommitted_changes"] != nil {
					t.Fatalf("clean empty job lost the report-only hint: %+v", out)
				}
				return
			}
			if strings.Contains(next, "committed no changes") || strings.Contains(next, "nothing to merge") {
				t.Fatalf("dirty worktree reported as nothing to merge: %q", next)
			}
			if !strings.Contains(next, "uncommitted changes") || !strings.Contains(next, "swarm_message_job") {
				t.Fatalf("next does not say there are uncommitted changes: %q", next)
			}
			files, _ := result["uncommitted_changes"].([]string)
			if len(files) != 2 || files[0] != "src/app.go (both modified)" {
				t.Fatalf("uncommitted changes not named: %+v", result)
			}
		})
	}
}

// Purpose: answers must survive the MCP job schema, permission API and durable
// receipt whole. Oversized answers must leave the question pending. Owners:
// controlMCPAnswerJob and permission.Service.resolveLocked; real API/store
// fixtures prove this boundary without running an external provider.
func TestControlMCPAnswerJobLongReply(t *testing.T) {
	s, db, h := setupWorkerAPITestServer(t)
	s.perm = permission.NewService(pebblestore.NewPermissionStore(db), nil, nil)
	workspaceID := setupWorkerAPIExecution(t, s, db)
	w, run := settledWorkerJobForMCP(t, s, h, workspaceID, "long-answer")
	pending, err := s.perm.CreatePending(permission.CreateInput{SessionID: run.SessionID, RunID: run.ID, CallID: "question", ToolName: "ask_user", ToolArguments: `{"question":"What next?"}`, Requirement: "approval", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"job_id": controlMCPJobID(controlMCPJobWorker, w.ID, run.ID), "answer": strings.Repeat("x", 100001)}
	if _, err := callJobForMCP(h, "swarm_answer_job", args); err == nil || !strings.Contains(err.Error(), "100000") {
		t.Fatalf("oversized MCP answer: %v", err)
	}
	if _, err := s.perm.Resolve(run.SessionID, pending.ID, "allow_once", args["answer"].(string)); err == nil || !strings.Contains(err.Error(), "100000") {
		t.Fatalf("oversized service answer: %v", err)
	}
	if open, err := s.perm.ListPending(run.SessionID, 10); err != nil || len(open) != 1 {
		t.Fatalf("rejection changed pending question: %v", err)
	}
	answer := strings.Repeat("界", 100000)
	args["answer"] = answer
	if _, err := callJobForMCP(h, "swarm_answer_job", args); err != nil {
		t.Fatal(err)
	}
	records, err := s.perm.ListPermissions(run.SessionID, 10)
	if err != nil || len(records) != 1 || records[0].Reason != answer || records[0].Status != "approved" {
		t.Fatalf("answer not retained whole: %v", err)
	}
}

// Purpose: MCP replies must reach the actual provider adapter after executor
// assembly, including a model handoff. The executor and permission tool runner
// own this contract. A recording adapter and real MCP/API/store/tool execution
// exercise it without external credentials; bulk autonomous history must remain
// bounded while the current user instruction and accepted answer stay whole.
func TestControlMCPRepliesReachProvider(t *testing.T) {
	t.Setenv("SWARM_V3_HANDOFF_TOTAL_CHARS", "2000")
	s, db, h := setupWorkerAPITestServer(t)
	workspaceID := setupWorkerAPIExecution(t, s, db)
	w, previous := settledWorkerJobForMCP(t, s, h, workspaceID, "provider-replies")
	s.perm = permission.NewService(pebblestore.NewPermissionStore(db), nil, nil)
	message, answer := strings.Repeat("界", 6000), strings.Repeat("答", 100000)
	if _, err := messageJobForMCP(h, controlMCPJobID(controlMCPJobWorker, w.ID, previous.ID), message, "provider-followup"); err != nil {
		t.Fatal(err)
	}
	intent, ok, err := s.sessions.GetSessionActiveRunIntent(previous.SessionID)
	if err != nil || !ok {
		t.Fatalf("active: %v", err)
	}
	snapshot, _, err := s.sessions.GetSession(previous.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	job := sessionV3ExecutorJob{SessionID: previous.SessionID, RunID: intent.RunID, EpochID: intent.EpochID, SourceMessageID: intent.SourceMessageID, Principal: identity.Principal{Type: "user", UserID: "user-test", AccountScopeID: "acct-test"}}
	exec := &sessionV3Executor{server: s}
	bulk := strings.Repeat("BULK-HISTORY-", 2000)
	if _, err := exec.recordPreToolAssistantSegment(job, sessionV3AssistantResponse{Content: bulk}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.sessions.PutExecutionProviderLifecycleState(pebblestore.ExecutionProviderLifecycleState{SessionID: job.SessionID, EpochID: job.EpochID, Provider: "test-provider", Model: "old-model", ConfigurationHash: "old-config", ContextBranchID: "old-branch", ProviderLineageID: "old-lineage", UpdatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	runner := &sessionsV3RecordingProviderRunner{responses: []provideriface.Response{
		{FunctionCalls: []provideriface.FunctionCall{{CallID: "question", Name: "ask-user", Arguments: `{"question":"What next?","options":["Continue","Stop"]}`}}},
		{Text: "done"},
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	s.providers = providers
	s.runner = runruntime.NewService(s.sessions, nil, providers, tool.NewRuntime(1), s.perm.(*permission.Service), nil, nil, nil)
	resolved := sessionV3ResolvedRuntime{Instructions: "Ask before proceeding.", ToolChoice: "auto", Session: snapshot, Preference: pebblestore.ModelPreference{Provider: "test-provider", Model: "new-model"}, Scope: tool.WorkspaceScope{PrimaryPath: snapshot.WorkspacePath}, Tools: []provideriface.ToolDefinition{{Type: "function", Name: "ask_user"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := exec.providerAssistantResponse(ctx, job, resolved, "", false, nil); done <- err }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
waiting:
	for {
		select {
		case err := <-done:
			t.Fatalf("executor exited before question: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
			pending, err := s.perm.ListPending(job.SessionID, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) > 0 {
				break waiting
			}
		}
	}
	if _, err := callJobForMCP(h, "swarm_answer_job", map[string]any{"job_id": controlMCPJobID(controlMCPJobWorker, w.ID, previous.ID), "answer": answer}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Re-enter the executor from durable history after another model switch.
	// The answer below is the actual persisted tool result, not a fabricated
	// permission-record projection.
	resolved.Preference.Model = "third-model"
	stopAfterCapture := errors.New("stop after recording handoff request")
	runner.err = stopAfterCapture
	if _, err := exec.providerAssistantResponse(ctx, job, resolved, "handoff", false, nil); !errors.Is(err, stopAfterCapture) {
		t.Fatalf("handoff capture: %v", err)
	}
	runner.mu.Lock()
	requests := append([]provideriface.Request(nil), runner.requests...)
	runner.mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("requests = %d", len(requests))
	}
	if requests[0].BoundaryReason != "provider_model_runtime_handoff" || requests[2].BoundaryReason != "provider_model_runtime_handoff" {
		t.Fatalf("no model handoff: %s", requests[0].BoundaryReason)
	}
	for i, req := range requests {
		input := fmt.Sprint(req.Input)
		if !strings.Contains(input, message) {
			t.Fatalf("request %d shortened user turn", i)
		}
		if strings.Contains(input, bulk) {
			t.Fatalf("request %d replayed bulk history", i)
		}
	}
	if !strings.Contains(fmt.Sprint(requests[1].Input), answer) || !strings.Contains(fmt.Sprint(requests[2].Input), answer) {
		t.Fatal("provider did not receive whole accepted answer")
	}
}
