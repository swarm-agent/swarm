package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r = r.WithContext(context.WithValue(context.WithValue(r.Context(), productPrincipalRequestContextKey, identity.Principal{Type: "user", UserID: "user-test", AccountScopeID: "acct-test"}),
		productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: "acct-test", UserID: "user-test", Scopes: []string{"sessions:read", "sessions:write", "automations:read", "automations:write", "projects:read"}}))
	return controlMCPMessageJob(&controlMCPCall{request: r, next: h}, map[string]any{"job_id": jobID, "message": message, "client_request_id": requestID})
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
// gate itself is proven in the run package.
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

	raw, err = messageJobForMCP(h, jobID, "Also add a test", "first")
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
