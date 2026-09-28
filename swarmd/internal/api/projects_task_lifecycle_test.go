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

func TestProjectTask_V3RunAuthority_LifecycleAbsentSuccessfulRun(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: An in-progress task must reflect canonical V3 run authority:
	//   when a V3 agent run finishes (completed), the task must transition to needs_review,
	//   never directly to completed, even when sess.Lifecycle is absent on the session record.
	// - Boundary/authority: syncTaskSessionState in projects.go, db.GetV3SessionRunState in session_event_store.go.
	// - Threat/regression: Stalled in_progress tasks where agent finished and committed work but
	//   UI/SDK getTask remains in_progress because sess.Lifecycle is absent on V3 sessions.
	// - Narrowest layer: Point tests on session store + syncTaskSessionState and HTTP GET handler.

	server, _, dbStore := newWorkspaceOverviewTopologyTestServer(t)
	server.ConfigureProjectRealtime(dbStore)
	sessionStore := pebblestore.NewSessionStore(dbStore)
	accountID := testPrincipal().AccountScopeID

	proj := &pebblestore.ProjectRecord{Name: "V3 Sync Project"}
	if err := sessionStore.PutProject(accountID, proj); err != nil {
		t.Fatal(err)
	}

	sessID := "sess_v3_success"
	now := time.Now().UnixMilli()
	sessSnap := pebblestore.SessionSnapshot{
		ID:             sessID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		Title:          "V3 Coder Worker",
		Mode:           "auto",
		CreatedAt:      now,
		UpdatedAt:      now,
		MessageCount:   10,
		Lifecycle:      nil, // Canonical V3 session: Lifecycle is ABSENT
	}
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
		SessionID:       sessID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  accountID,
		ClientRequestID: "create:" + sessID,
		IdempotencyKey:  "create:" + sessID,
		PayloadHash:     "create:" + sessID,
		RequestHash:     "create:" + sessID,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &sessSnap,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatal(err)
	}

	// Record completed run intent on the session via canonical V3 mutation
	runID := "desktop-v3-run:task_1"
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
		SessionID:       sessID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  accountID,
		ClientRequestID: "run-intent:" + runID,
		IdempotencyKey:  "run-intent:" + runID,
		PayloadHash:     "run-intent:" + runID,
		RequestHash:     "run-intent:" + runID,
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		RunIntent: &pebblestore.V3SessionRunIntent{
			SessionID:      sessID,
			RunID:          runID,
			Status:         pebblestore.V3RunIntentCompleted,
			UserID:         testPrincipal().UserID,
			AccountScopeID: accountID,
			CreatedAt:      now,
			UpdatedAt:      now + 12000,
		},
		NowUnixMs: now + 12000,
	}); err != nil {
		t.Fatal(err)
	}

	task := &pebblestore.ProjectTaskRecord{
		ProjectID:           proj.ID,
		AccountID:           accountID,
		Title:               "Implement feature",
		Agent:               "coder",
		Status:              "in_progress",
		SessionID:           sessID,
		UnintegratedCommits: 1,
		BaseBranch:          "dev",
	}
	if err := sessionStore.PutProjectTask(accountID, task); err != nil {
		t.Fatal(err)
	}

	// 1. Direct syncTaskSessionState invocation:
	syncTaskSessionState(task, sessionStore)
	if task.Status != "needs_review" {
		t.Fatalf("expected status 'needs_review', got %q", task.Status)
	}
	if !strings.Contains(task.ActionNeeded, "Review changes") {
		t.Fatalf("expected ActionNeeded to prompt review, got %q", task.ActionNeeded)
	}

	// A completed explicit retry must recover a persisted failed card even when
	// nobody read the task during the brief intermediate running state.
	task.Status, task.LastError = "failed", "prior cancellation"
	syncTaskSessionState(task, sessionStore)
	if task.Status != "needs_review" || task.LastError != "" {
		t.Fatalf("retry stayed failed: %#v", task)
	}

	// 2. HTTP GET /v3/projects/{id}/tasks/{taskId} handler invocation:
	t.Setenv("SWARM_API_NO_AUTH", "1")
	h := server.apiMux()
	r := httptest.NewRequest(http.MethodGet, ProjectsPath+"/"+proj.ID+"/tasks/"+task.ID, nil)
	p := testPrincipal()
	ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
	tokenRec := &pebblestore.ScopedTokenRecord{
		AccountScopeID: p.AccountScopeID,
		UserID:         p.UserID,
		Scopes:         []string{"projects:read", "sessions:read"},
	}
	ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Task pebblestore.ProjectTaskRecord `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Task.Status != "needs_review" {
		t.Fatalf("expected HTTP getTask status 'needs_review', got %q", resp.Task.Status)
	}
	// A completed provider turn cannot finish an unfinished approved plan.
	task.Status = "in_progress"
	task.PlanBinding = &pebblestore.ProjectTaskPlanBinding{PlanID: "bound-plan", SessionID: sessID}
	plan := pebblestore.SessionPlanSnapshot{ID: "bound-plan", SessionID: sessID, Document: &pebblestore.SessionPlanDocument{Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: "in_progress"}}}}
	if err := sessionStore.PutPlan(plan); err != nil {
		t.Fatal(err)
	}
	syncTaskSessionState(task, sessionStore)
	if task.Status != "in_progress" {
		t.Fatalf("unfinished plan prematurely reviewed: %s", task.Status)
	}
	plan.Document.Checkpoints[0].Status = "completed"
	if err := sessionStore.PutPlan(plan); err != nil {
		t.Fatal(err)
	}
	syncTaskSessionState(task, sessionStore)
	if task.Status != "needs_review" {
		t.Fatalf("completed plan not reviewable: %s", task.Status)
	}
}

func TestProjectTask_V3RunAuthority_ActiveRun(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: When a V3 session has an active run (running or pending_executor),
	//   the task status must be in_progress and must not be marked as needs_review prematurely.
	// - Boundary/authority: syncTaskSessionState in projects.go, db.GetV3SessionRunState in session_event_store.go.
	// - Threat/regression: False early transition to needs_review while an agent is actively executing.
	// - Narrowest layer: Focused unit test with session store and syncTaskSessionState.

	server, _, dbStore := newWorkspaceOverviewTopologyTestServer(t)
	sessionStore := pebblestore.NewSessionStore(dbStore)
	accountID := testPrincipal().AccountScopeID

	sessID := "sess_v3_active"
	now := time.Now().UnixMilli()
	sessSnap := pebblestore.SessionSnapshot{
		ID:             sessID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		Title:          "V3 Active Worker",
		Mode:           "auto",
		CreatedAt:      now,
		UpdatedAt:      now,
		MessageCount:   3,
		Lifecycle:      nil,
	}
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
		SessionID:       sessID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  accountID,
		ClientRequestID: "create:" + sessID,
		IdempotencyKey:  "create:" + sessID,
		PayloadHash:     "create:" + sessID,
		RequestHash:     "create:" + sessID,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &sessSnap,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Running state
	runID := "desktop-v3-run:active_1"
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
		SessionID:       sessID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  accountID,
		ClientRequestID: "run-intent:" + runID,
		IdempotencyKey:  "run-intent:" + runID,
		PayloadHash:     "run-intent:" + runID,
		RequestHash:     "run-intent:" + runID,
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		RunIntent: &pebblestore.V3SessionRunIntent{
			SessionID:      sessID,
			RunID:          runID,
			Status:         pebblestore.V3RunIntentRunning,
			UserID:         testPrincipal().UserID,
			AccountScopeID: accountID,
			CreatedAt:      now,
			UpdatedAt:      now + 1000,
		},
		NowUnixMs: now + 1000,
	}); err != nil {
		t.Fatal(err)
	}

	task := &pebblestore.ProjectTaskRecord{
		ProjectID: "proj_active",
		AccountID: accountID,
		Title:     "Active task",
		Agent:     "coder",
		Status:    "in_progress",
		SessionID: sessID,
	}
	syncTaskSessionState(task, sessionStore)
	if task.Status != "in_progress" {
		t.Fatalf("expected running task to remain 'in_progress', got %q", task.Status)
	}

	// Even if task was previously 'needs_review', active run must reset it to 'in_progress'
	task.Status = "needs_review"
	syncTaskSessionState(task, sessionStore)
	if task.Status != "in_progress" {
		t.Fatalf("expected reopened task with active run to transition to 'in_progress', got %q", task.Status)
	}
}

func TestProjectTask_V3RunAuthority_FailedAndCancelled(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: When a V3 agent run fails or is cancelled, the task must
	//   transition to failed (not needs_review or completed), preserving the error reason
	//   in LastError and updating ActionNeeded.
	// - Boundary/authority: syncTaskSessionState in projects.go, db.GetV3SessionRunState in session_event_store.go.
	// - Threat/regression: Concealing run failures/cancellations behind needs_review or completed status.
	// - Narrowest layer: Focused unit test with session store and syncTaskSessionState.

	server, _, dbStore := newWorkspaceOverviewTopologyTestServer(t)
	sessionStore := pebblestore.NewSessionStore(dbStore)
	accountID := testPrincipal().AccountScopeID
	now := time.Now().UnixMilli()

	// Sub-case A: Failed run
	sessFailedID := "sess_v3_failed"
	sessSnap := pebblestore.SessionSnapshot{
		ID:             sessFailedID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		Title:          "V3 Failed Worker",
		Mode:           "auto",
		CreatedAt:      now,
		UpdatedAt:      now,
		Lifecycle:      nil,
	}
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
		SessionID:       sessFailedID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  accountID,
		ClientRequestID: "create:" + sessFailedID,
		IdempotencyKey:  "create:" + sessFailedID,
		PayloadHash:     "create:" + sessFailedID,
		RequestHash:     "create:" + sessFailedID,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &sessSnap,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
		SessionID:       sessFailedID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  accountID,
		ClientRequestID: "run-failed",
		IdempotencyKey:  "run-failed",
		PayloadHash:     "run-failed",
		RequestHash:     "run-failed",
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		RunIntent: &pebblestore.V3SessionRunIntent{
			SessionID:      sessFailedID,
			RunID:          "run-fail-1",
			Status:         pebblestore.V3RunIntentFailed,
			BlockedReason:  "provider rate limit exceeded",
			UserID:         testPrincipal().UserID,
			AccountScopeID: accountID,
			CreatedAt:      now,
			UpdatedAt:      now + 5000,
		},
		NowUnixMs: now + 5000,
	}); err != nil {
		t.Fatal(err)
	}
	taskFailed := &pebblestore.ProjectTaskRecord{
		ProjectID: "proj_fail",
		AccountID: accountID,
		Title:     "Failing task",
		Agent:     "coder",
		Status:    "in_progress",
		SessionID: sessFailedID,
	}
	syncTaskSessionState(taskFailed, sessionStore)
	if taskFailed.Status != "failed" {
		t.Fatalf("expected status 'failed', got %q", taskFailed.Status)
	}
	if taskFailed.LastError != "provider rate limit exceeded" {
		t.Fatalf("expected LastError 'provider rate limit exceeded', got %q", taskFailed.LastError)
	}

	// Sub-case B: Cancelled run
	sessCancelID := "sess_v3_cancelled"
	sessSnapCancel := pebblestore.SessionSnapshot{
		ID:             sessCancelID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		Title:          "V3 Cancelled Worker",
		Mode:           "auto",
		CreatedAt:      now,
		UpdatedAt:      now,
		Lifecycle:      nil,
	}
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
		SessionID:       sessCancelID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  accountID,
		ClientRequestID: "create:" + sessCancelID,
		IdempotencyKey:  "create:" + sessCancelID,
		PayloadHash:     "create:" + sessCancelID,
		RequestHash:     "create:" + sessCancelID,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &sessSnapCancel,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
		SessionID:       sessCancelID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  accountID,
		ClientRequestID: "run-cancel",
		IdempotencyKey:  "run-cancel",
		PayloadHash:     "run-cancel",
		RequestHash:     "run-cancel",
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		RunIntent: &pebblestore.V3SessionRunIntent{
			SessionID:      sessCancelID,
			RunID:          "run-cancel-1",
			Status:         pebblestore.V3RunIntentCancelled,
			UserID:         testPrincipal().UserID,
			AccountScopeID: accountID,
			CreatedAt:      now,
			UpdatedAt:      now + 2000,
		},
		NowUnixMs: now + 2000,
	}); err != nil {
		t.Fatal(err)
	}
	taskCancel := &pebblestore.ProjectTaskRecord{
		ProjectID: "proj_fail",
		AccountID: accountID,
		Title:     "Cancelled task",
		Agent:     "coder",
		Status:    "in_progress",
		SessionID: sessCancelID,
	}
	syncTaskSessionState(taskCancel, sessionStore)
	if taskCancel.Status != "failed" {
		t.Fatalf("expected status 'failed' on cancellation, got %q", taskCancel.Status)
	}
}

func TestProjectTask_V3RunAuthority_NoExecution(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: When a session has messages (e.g. system seed, context) but no
	//   run has executed, the task must remain in_progress and must not guess completion from MessageCount > 1.
	// - Boundary/authority: syncTaskSessionState in projects.go.
	// - Threat/regression: Prematurely marking new tasks as needs_review simply because seed/prompt messages exist.
	// - Narrowest layer: Focused unit test with session store and syncTaskSessionState.

	server, _, dbStore := newWorkspaceOverviewTopologyTestServer(t)
	sessionStore := pebblestore.NewSessionStore(dbStore)
	accountID := testPrincipal().AccountScopeID
	now := time.Now().UnixMilli()

	sessID := "sess_v3_no_exec"
	sessSnap := pebblestore.SessionSnapshot{
		ID:             sessID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		Title:          "V3 Fresh Worker",
		Mode:           "auto",
		CreatedAt:      now,
		UpdatedAt:      now,
		MessageCount:   10,  // Many seed messages exist!
		Lifecycle:      nil, // Lifecycle is absent
	}
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
		SessionID:       sessID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  accountID,
		ClientRequestID: "create:" + sessID,
		IdempotencyKey:  "create:" + sessID,
		PayloadHash:     "create:" + sessID,
		RequestHash:     "create:" + sessID,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &sessSnap,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatal(err)
	}
	// Note: NO run intent or run state recorded.

	task := &pebblestore.ProjectTaskRecord{
		ProjectID: "proj_no_exec",
		AccountID: accountID,
		Title:     "Unexecuted task",
		Agent:     "coder",
		Status:    "in_progress",
		SessionID: sessID,
	}
	syncTaskSessionState(task, sessionStore)
	if task.Status != "in_progress" {
		t.Fatalf("expected task without execution to remain 'in_progress', got %q", task.Status)
	}
}

func TestProjectTask_V3RunAuthority_AccountBoundaryIsolation(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: Realtime task state sync must strictly enforce account boundaries.
	//   A task in account A referencing a session in account B must never read or apply that foreign session's run state.
	// - Boundary/authority: syncTaskSessionState in projects.go.
	// - Threat/regression: Cross-account data leakage or foreign session manipulation of project tasks.
	// - Narrowest layer: Focused unit test with session store and syncTaskSessionState across accounts.

	server, _, dbStore := newWorkspaceOverviewTopologyTestServer(t)
	sessionStore := pebblestore.NewSessionStore(dbStore)
	now := time.Now().UnixMilli()

	// Foreign session in account_foreign with completed run
	foreignSessID := "sess_foreign_account"
	foreignAccount := "acct_foreign"
	sessSnap := pebblestore.SessionSnapshot{
		ID:             foreignSessID,
		UserID:         "foreign_user",
		AccountScopeID: foreignAccount,
		Title:          "Foreign Session",
		Mode:           "auto",
		CreatedAt:      now,
		UpdatedAt:      now,
		Lifecycle:      nil,
	}
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
		SessionID:       foreignSessID,
		UserID:          "foreign_user",
		AccountScopeID:  foreignAccount,
		ClientRequestID: "create:" + foreignSessID,
		IdempotencyKey:  "create:" + foreignSessID,
		PayloadHash:     "create:" + foreignSessID,
		RequestHash:     "create:" + foreignSessID,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &sessSnap,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
		SessionID:       foreignSessID,
		UserID:          "foreign_user",
		AccountScopeID:  foreignAccount,
		ClientRequestID: "run-foreign-completed",
		IdempotencyKey:  "run-foreign-completed",
		PayloadHash:     "run-foreign-completed",
		RequestHash:     "run-foreign-completed",
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		RunIntent: &pebblestore.V3SessionRunIntent{
			SessionID:      foreignSessID,
			RunID:          "run-foreign-1",
			Status:         pebblestore.V3RunIntentCompleted,
			UserID:         "foreign_user",
			AccountScopeID: foreignAccount,
			CreatedAt:      now,
			UpdatedAt:      now + 1000,
		},
		NowUnixMs: now + 1000,
	}); err != nil {
		t.Fatal(err)
	}

	// Task belonging to primary account referencing foreign session
	task := &pebblestore.ProjectTaskRecord{
		ProjectID: "proj_local",
		AccountID: testPrincipal().AccountScopeID,
		Title:     "Local task with foreign session pointer",
		Agent:     "coder",
		Status:    "in_progress",
		SessionID: foreignSessID,
	}
	syncTaskSessionState(task, sessionStore)
	if task.Status != "in_progress" {
		t.Fatalf("cross-account session run state applied! expected 'in_progress', got %q", task.Status)
	}
}

func TestProjectTask_V3RunAuthority_ReadPurity(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: GET operations on project tasks must be pure reads. In-memory
	//   hydration via syncTaskSessionState must not mutate the Pebble database or advance outbox revision.
	// - Boundary/authority: handleProjectTasks in projects.go, syncTaskSessionState in projects.go.
	// - Threat/regression: Write-on-read side-effects, outbox revision churn, and database lock contention on reads.
	// - Narrowest layer: Full HTTP GET with store assertions before and after.

	t.Setenv("SWARM_API_NO_AUTH", "1")
	server, _, dbStore := newWorkspaceOverviewTopologyTestServer(t)
	server.ConfigureProjectRealtime(dbStore)
	sessionStore := pebblestore.NewSessionStore(dbStore)
	accountID := testPrincipal().AccountScopeID
	now := time.Now().UnixMilli()

	proj := &pebblestore.ProjectRecord{Name: "Read Purity Project"}
	if err := sessionStore.PutProject(accountID, proj); err != nil {
		t.Fatal(err)
	}

	sessID := "sess_purity_v3"
	sessSnap := pebblestore.SessionSnapshot{
		ID:             sessID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		Title:          "Purity Worker",
		Mode:           "auto",
		CreatedAt:      now,
		UpdatedAt:      now,
		Lifecycle:      nil,
	}
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
		SessionID:       sessID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  accountID,
		ClientRequestID: "create:" + sessID,
		IdempotencyKey:  "create:" + sessID,
		PayloadHash:     "create:" + sessID,
		RequestHash:     "create:" + sessID,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &sessSnap,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
		SessionID:       sessID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  accountID,
		ClientRequestID: "run-purity",
		IdempotencyKey:  "run-purity",
		PayloadHash:     "run-purity",
		RequestHash:     "run-purity",
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		RunIntent: &pebblestore.V3SessionRunIntent{
			SessionID:      sessID,
			RunID:          "run-purity-1",
			Status:         pebblestore.V3RunIntentCompleted,
			UserID:         testPrincipal().UserID,
			AccountScopeID: accountID,
			CreatedAt:      now,
			UpdatedAt:      now + 2000,
		},
		NowUnixMs: now + 2000,
	}); err != nil {
		t.Fatal(err)
	}

	task := &pebblestore.ProjectTaskRecord{
		ProjectID: proj.ID,
		Title:     "Purity Task",
		Agent:     "coder",
		Status:    "in_progress",
		SessionID: sessID,
	}
	if err := sessionStore.PutProjectTask(accountID, task); err != nil {
		t.Fatal(err)
	}

	revBefore, err := server.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		t.Fatal(err)
	}

	h := server.apiMux()
	p := testPrincipal()
	tokenRec := &pebblestore.ScopedTokenRecord{
		AccountScopeID: p.AccountScopeID,
		UserID:         p.UserID,
		Scopes:         []string{"projects:read", "sessions:read"},
	}

	call := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, ProjectsPath+path, nil)
		ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
		ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// 1. Collection GET:
	w1 := call(http.MethodGet, "/"+proj.ID+"/tasks")
	if w1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w1.Code)
	}
	var colResp struct {
		Tasks []pebblestore.ProjectTaskRecord `json:"tasks"`
	}
	if err := json.Unmarshal(w1.Body.Bytes(), &colResp); err != nil {
		t.Fatal(err)
	}
	if len(colResp.Tasks) != 1 || colResp.Tasks[0].Status != "needs_review" {
		t.Fatalf("expected hydrated status 'needs_review', got %q", colResp.Tasks[0].Status)
	}

	// Assert store status in Pebble remains in_progress (no write-on-read):
	storedTask, found, err := sessionStore.GetProjectTask(accountID, proj.ID, task.ID)
	if err != nil || !found {
		t.Fatalf("task not found: %v", err)
	}
	if storedTask.Status != "in_progress" {
		t.Fatalf("write-on-read detected: store status mutated to %q", storedTask.Status)
	}

	revAfter, err := server.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		t.Fatal(err)
	}
	if revAfter != revBefore {
		t.Fatalf("outbox advanced on read: before=%d after=%d", revBefore, revAfter)
	}
}

func TestProjectTask_ReconcileProjectTaskRunLifecycle_RealtimeInvalidation(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: When a managed V3 run completes, reconcileProjectTaskRunLifecycle
	//   must update the task record in Pebble and emit a project.updated invalidation event to the realtime outbox.
	// - Boundary/authority: reconcileProjectTaskRunLifecycle in projects_realtime.go, UpdateProjectTask in project_store.go.
	// - Threat/regression: Clients missing live task completion notifications, leaving UI desynchronized from agent execution.
	// - Narrowest layer: Server reconciliation method with realtime outbox listener.

	server, _, dbStore := newWorkspaceOverviewTopologyTestServer(t)
	accountID := testPrincipal().AccountScopeID
	now := time.Now().UnixMilli()

	outboxChan := make(chan pebblestore.V3RealtimeOutboxRecord, 10)
	dbStore.SetProjectPublisher(func(record pebblestore.V3RealtimeOutboxRecord) {
		outboxChan <- record
	})
	sessionStore := pebblestore.NewSessionStore(dbStore)

	proj := &pebblestore.ProjectRecord{Name: "Realtime Invalidation Project"}
	if err := sessionStore.PutProject(accountID, proj); err != nil {
		t.Fatal(err)
	}

	sessID := "sess_reconcile_v3"
	taskID := "task_reconcile_v3"
	sessSnap := pebblestore.SessionSnapshot{
		ID:             sessID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		Title:          "Reconcile Worker",
		Mode:           "auto",
		CreatedAt:      now,
		UpdatedAt:      now,
		Metadata: map[string]any{
			"project_id": proj.ID,
			"task_id":    taskID,
		},
		Lifecycle: nil,
	}
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
		SessionID:       sessID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  accountID,
		ClientRequestID: "create:" + sessID,
		IdempotencyKey:  "create:" + sessID,
		PayloadHash:     "create:" + sessID,
		RequestHash:     "create:" + sessID,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &sessSnap,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatal(err)
	}

	task := &pebblestore.ProjectTaskRecord{
		ID:        taskID,
		ProjectID: proj.ID,
		AccountID: accountID,
		Title:     "Reconciled Task",
		Agent:     "coder",
		Status:    "in_progress",
		SessionID: sessID,
	}
	if err := sessionStore.PutProjectTask(accountID, task); err != nil {
		t.Fatal(err)
	}
	// Drain any setup outbox notifications
	for len(outboxChan) > 0 {
		<-outboxChan
	}

	// 1. Terminal completed run reconciliation
	job := sessionV3ExecutorJob{
		Principal: testPrincipal(),
		SessionID: sessID,
		RunID:     "run-reconcile-1",
	}
	if err := server.reconcileProjectTaskRunLifecycle(job, sessionruntime.RunIntentCompleted, ""); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Assert task in Pebble store was mutated to needs_review
	updatedTask, found, err := sessionStore.GetProjectTask(accountID, proj.ID, taskID)
	if err != nil || !found {
		t.Fatalf("get updated task failed: %v", err)
	}
	if updatedTask.Status != "needs_review" {
		t.Fatalf("expected stored task status 'needs_review', got %q", updatedTask.Status)
	}

	// Assert realtime invalidation event was emitted
	select {
	case rec := <-outboxChan:
		if rec.Event.EventType != pebblestore.ProjectUpdatedEventType {
			t.Fatalf("expected event type %q, got %q", pebblestore.ProjectUpdatedEventType, rec.Event.EventType)
		}
		var payload map[string]any
		if err := json.Unmarshal(rec.Event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["action"] != "task_updated" || payload["task_id"] != taskID {
			t.Fatalf("unexpected invalidation payload: %+v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for project.updated realtime invalidation")
	}

	// 2. Negative test: Foreign principal cannot reconcile task across account boundaries
	foreignJob := sessionV3ExecutorJob{
		Principal: identity.Principal{Type: "user", UserID: "attacker", AccountScopeID: "foreign_scope"},
		SessionID: sessID,
		RunID:     "run-reconcile-2",
	}
	if err := server.reconcileProjectTaskRunLifecycle(foreignJob, sessionruntime.RunIntentFailed, "malicious"); err != nil {
		t.Fatalf("expected silent reject, got err: %v", err)
	}
	// Task status must NOT have changed to failed
	currentTask, _, _ := sessionStore.GetProjectTask(accountID, proj.ID, taskID)
	if currentTask.Status != "needs_review" {
		t.Fatalf("cross-account mutation allowed! status=%q", currentTask.Status)
	}
}

func TestProjectTask_WorktreeHydrationFromSession(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: When a project task does not have worktree fields populated directly,
	//   they must be hydrated from the linked session's worktree configuration.
	// - Boundary/authority: syncTaskSessionState and inspectTaskGitState in projects.go.
	// - Threat/regression: Smoke tests and UI failing to locate the task worktree path, branch, or base branch.
	// - Narrowest layer: Focused unit test with session store and syncTaskSessionState.

	server, _, dbStore := newWorkspaceOverviewTopologyTestServer(t)
	sessionStore := pebblestore.NewSessionStore(dbStore)
	accountID := testPrincipal().AccountScopeID
	now := time.Now().UnixMilli()

	sessID := "sess_worktree_hydr"
	sessSnap := pebblestore.SessionSnapshot{
		ID:                 sessID,
		UserID:             testPrincipal().UserID,
		AccountScopeID:     accountID,
		Title:              "Worktree Worker",
		Mode:               "auto",
		WorktreeEnabled:    true,
		WorktreeRootPath:   "/tmp/worktrees/agent-feature-1",
		WorktreeBranch:     "agent/feature-branch",
		WorktreeBaseBranch: "dev",
		Metadata: map[string]any{
			"base_commit": "c3f659bdb5ef50929af78568fb66c04a89097368",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
		SessionID:       sessID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  accountID,
		ClientRequestID: "create:" + sessID,
		IdempotencyKey:  "create:" + sessID,
		PayloadHash:     "create:" + sessID,
		RequestHash:     "create:" + sessID,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &sessSnap,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatal(err)
	}

	task := &pebblestore.ProjectTaskRecord{
		ProjectID:     "proj_worktree",
		AccountID:     accountID,
		Title:         "Worktree task",
		Agent:         "coder",
		Status:        "in_progress",
		SessionID:     sessID,
		WorkspacePath: ".", // Empty/dot workspace path
	}
	syncTaskSessionState(task, sessionStore)

	if task.WorkspacePath != "/tmp/worktrees/agent-feature-1" {
		t.Fatalf("expected WorkspacePath hydrated to %q, got %q", "/tmp/worktrees/agent-feature-1", task.WorkspacePath)
	}
	if task.WorktreeBranch != "agent/feature-branch" {
		t.Fatalf("expected WorktreeBranch hydrated to %q, got %q", "agent/feature-branch", task.WorktreeBranch)
	}
	if task.WorktreeName != "feature-branch" {
		t.Fatalf("expected WorktreeName hydrated to %q, got %q", "feature-branch", task.WorktreeName)
	}
	if task.BaseBranch != "dev" {
		t.Fatalf("expected BaseBranch hydrated to %q, got %q", "dev", task.BaseBranch)
	}
	if task.BaseCommit != "c3f659bdb5ef50929af78568fb66c04a89097368" {
		t.Fatalf("expected BaseCommit hydrated to %q, got %q", "c3f659bdb5ef50929af78568fb66c04a89097368", task.BaseCommit)
	}
}

// Seed the canonical pending intent before simulating executor transitions.
func applyProjectLifecycleFixture(server *Server, input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
	if input.Kind == sessionruntime.SessionMutationRecordRunIntent && input.RunIntent != nil && input.RunIntent.Status != pebblestore.V3RunIntentPendingExecutor {
		pending := input
		intent := *input.RunIntent
		intent.Status = pebblestore.V3RunIntentPendingExecutor
		pending.RunIntent = &intent
		pending.ClientRequestID += ":pending"
		pending.IdempotencyKey += ":pending"
		pending.PayloadHash += ":pending"
		pending.RequestHash += ":pending"
		if _, err := server.applySessionV3PrimaryMutation(pending); err != nil {
			return sessionruntime.SessionMutationResult{}, err
		}
	}
	return server.applySessionV3PrimaryMutation(input)
}

func TestProjectTask_BigSwarmUsesReadOnlyPlanning(t *testing.T) {
	// Requirement: a big Swarm task must not execute writes before a plan is approved.
	// Regresses the live case where task status was planning but session mode was auto.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	project := f.createProject(t)
	if err := f.server.sessions.Store().CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	w := f.callAPI(http.MethodPost, "/"+project+"/tasks", map[string]any{"title": "Plan a change", "prompt": "Create a reviewed plan", "agent": "swarm", "feature_size": "big"}, p)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Task pebblestore.ProjectTaskRecord `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	sess, ok, err := f.server.sessions.Store().GetSession(response.Task.SessionID)
	if err != nil || !ok || sess.Mode != sessionruntime.ModePlan {
		t.Fatalf("big task requires plan mode, got %s err=%v", sess.Mode, err)
	}
	if !sess.WorktreeEnabled || sess.WorktreeRootPath == "" {
		t.Fatal("planner needs a session-owned lane for approved Task Program integration")
	}
}

func TestProjectTask_ReconcilePlanningRun_PlanAuthored_TransitionsToPendingApproval(t *testing.T) {
	// Requirement: When a plan agent session completes with an active plan,
	// reconcileProjectTaskRunLifecycle transitions the task from 'planning'
	// to 'pending_approval' with PlanBinding and PlanDocument, emitting project.updated.
	server, _, dbStore := newWorkspaceOverviewTopologyTestServer(t)
	accountID := testPrincipal().AccountScopeID
	now := time.Now().UnixMilli()

	outboxChan := make(chan pebblestore.V3RealtimeOutboxRecord, 10)
	dbStore.SetProjectPublisher(func(record pebblestore.V3RealtimeOutboxRecord) {
		outboxChan <- record
	})
	sessionStore := pebblestore.NewSessionStore(dbStore)

	proj := &pebblestore.ProjectRecord{Name: "Planning Reconcile Project"}
	if err := sessionStore.PutProject(accountID, proj); err != nil {
		t.Fatal(err)
	}

	sessID := "sess_plan_recon"
	taskID := "task_plan_recon"
	sessSnap := pebblestore.SessionSnapshot{
		ID:             sessID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		Title:          "Plan Agent Worker",
		Mode:           "plan",
		CreatedAt:      now,
		UpdatedAt:      now,
		Metadata: map[string]any{
			"project_id": proj.ID,
			"task_id":    taskID,
		},
	}
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
		SessionID:       sessID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  accountID,
		ClientRequestID: "create:" + sessID,
		IdempotencyKey:  "create:" + sessID,
		PayloadHash:     "create:" + sessID,
		RequestHash:     "create:" + sessID,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &sessSnap,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatal(err)
	}

	task := &pebblestore.ProjectTaskRecord{
		ID:        taskID,
		ProjectID: proj.ID,
		AccountID: accountID,
		Title:     "Plan Big Feature",
		Agent:     "plan",
		Status:    "planning",
		SessionID: sessID,
	}
	if err := sessionStore.PutProjectTask(accountID, task); err != nil {
		t.Fatal(err)
	}

	planDoc := &pebblestore.SessionPlanDocument{
		ID:    "plan-v1",
		Title: "Engine Architecture Plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Architect engine"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Order: 1, Title: "Core Engine", Tasks: []string{"Build core"}, AcceptanceCriteria: []string{"Tests pass"}},
		},
	}
	if err := sessionStore.PutPlan(pebblestore.SessionPlanSnapshot{
		ID:             "plan-v1",
		SessionID:      sessID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		Version:        1,
		Document:       planDoc,
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := sessionStore.SetActivePlan(sessID, "plan-v1", now); err != nil {
		t.Fatal(err)
	}

	for len(outboxChan) > 0 {
		<-outboxChan
	}

	job := sessionV3ExecutorJob{
		Principal: testPrincipal(),
		SessionID: sessID,
		RunID:     "run-plan-1",
	}

	if err := server.reconcileProjectTaskRunLifecycle(job, sessionruntime.RunIntentCompleted, ""); err != nil {
		t.Fatalf("reconcile planning completed failed: %v", err)
	}

	updatedTask, found, err := sessionStore.GetProjectTask(accountID, proj.ID, taskID)
	if err != nil || !found {
		t.Fatalf("get task failed: %v", err)
	}
	if updatedTask.Status != "pending_approval" {
		t.Fatalf("expected status 'pending_approval', got %q", updatedTask.Status)
	}
	if updatedTask.PlanBinding == nil || updatedTask.PlanBinding.PlanID != "plan-v1" {
		t.Fatalf("expected PlanBinding for plan-v1, got %#v", updatedTask.PlanBinding)
	}
	if updatedTask.PlanDocument == nil || updatedTask.PlanDocument.Title != "Engine Architecture Plan" {
		t.Fatalf("expected PlanDocument populated, got %#v", updatedTask.PlanDocument)
	}

	select {
	case rec := <-outboxChan:
		if rec.Event.EventType != pebblestore.ProjectUpdatedEventType {
			t.Fatalf("expected event type %q, got %q", pebblestore.ProjectUpdatedEventType, rec.Event.EventType)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for project.updated outbox invalidation")
	}
}

func TestProjectTask_ReconcilePlanningRun_Failure_TransitionsToFailed(t *testing.T) {
	// Requirement: When a plan agent session fails or cancels, reconcileProjectTaskRunLifecycle
	// transitions the task from 'planning' to 'failed' with LastError.
	server, _, dbStore := newWorkspaceOverviewTopologyTestServer(t)
	accountID := testPrincipal().AccountScopeID
	now := time.Now().UnixMilli()

	sessionStore := pebblestore.NewSessionStore(dbStore)

	proj := &pebblestore.ProjectRecord{Name: "Planning Fail Project"}
	if err := sessionStore.PutProject(accountID, proj); err != nil {
		t.Fatal(err)
	}

	sessID := "sess_plan_fail"
	taskID := "task_plan_fail"
	sessSnap := pebblestore.SessionSnapshot{
		ID:             sessID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		Title:          "Plan Agent Worker Fail",
		Mode:           "plan",
		CreatedAt:      now,
		UpdatedAt:      now,
		Metadata: map[string]any{
			"project_id": proj.ID,
			"task_id":    taskID,
		},
	}
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
		SessionID:       sessID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  accountID,
		ClientRequestID: "create:" + sessID,
		IdempotencyKey:  "create:" + sessID,
		PayloadHash:     "create:" + sessID,
		RequestHash:     "create:" + sessID,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &sessSnap,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatal(err)
	}

	task := &pebblestore.ProjectTaskRecord{
		ID:        taskID,
		ProjectID: proj.ID,
		AccountID: accountID,
		Title:     "Plan That Fails",
		Agent:     "plan",
		Status:    "planning",
		SessionID: sessID,
	}
	if err := sessionStore.PutProjectTask(accountID, task); err != nil {
		t.Fatal(err)
	}

	job := sessionV3ExecutorJob{
		Principal: testPrincipal(),
		SessionID: sessID,
		RunID:     "run-plan-fail-1",
	}

	if err := server.reconcileProjectTaskRunLifecycle(job, sessionruntime.RunIntentFailed, "Model quota exceeded"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	updatedTask, found, err := sessionStore.GetProjectTask(accountID, proj.ID, taskID)
	if err != nil || !found {
		t.Fatalf("get task failed: %v", err)
	}
	if updatedTask.Status != "failed" {
		t.Fatalf("expected status 'failed', got %q", updatedTask.Status)
	}
	if updatedTask.LastError != "Model quota exceeded" {
		t.Fatalf("expected LastError 'Model quota exceeded', got %q", updatedTask.LastError)
	}
}

func TestProjectTask_ReconcileProjectTaskRunLifecycle_PreservesCompletedAndIntegratedAndRejected(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: reconcileProjectTaskRunLifecycle must NOT regress tasks that are already
	//   completed, integrated, or rejected, even when a concurrent or late task program run terminates.
	// - Boundary/authority: reconcileProjectTaskRunLifecycle in projects_realtime.go, UpdateProjectTask callback guards.
	// - Threat/regression: Completed or integrated tasks regressing to in_progress or needs_review.
	// - Narrowest layer: Server unit test exercising reconcileProjectTaskRunLifecycle against pre-terminal tasks.

	server, _, dbStore := newWorkspaceOverviewTopologyTestServer(t)
	accountID := testPrincipal().AccountScopeID
	now := time.Now().UnixMilli()
	sessionStore := pebblestore.NewSessionStore(dbStore)

	proj := &pebblestore.ProjectRecord{Name: "Terminal Preservation Project"}
	if err := sessionStore.PutProject(accountID, proj); err != nil {
		t.Fatal(err)
	}

	testCases := []struct {
		name         string
		taskID       string
		sessID       string
		initialState string
		isIntegrated bool
	}{
		{name: "CompletedTask", taskID: "task_term_completed", sessID: "sess_term_completed", initialState: "completed", isIntegrated: false},
		{name: "IntegratedTask", taskID: "task_term_integrated", sessID: "sess_term_integrated", initialState: "completed", isIntegrated: true},
		{name: "RejectedTask", taskID: "task_term_rejected", sessID: "sess_term_rejected", initialState: "rejected", isIntegrated: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sessSnap := pebblestore.SessionSnapshot{
				ID:             tc.sessID,
				UserID:         testPrincipal().UserID,
				AccountScopeID: accountID,
				Title:          "Terminal Worker",
				Mode:           "auto",
				CreatedAt:      now,
				UpdatedAt:      now,
				Metadata: map[string]any{
					"project_id": proj.ID,
					"task_id":    tc.taskID,
				},
			}
			if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
				SessionID:       tc.sessID,
				UserID:          testPrincipal().UserID,
				AccountScopeID:  accountID,
				ClientRequestID: "create:" + tc.sessID,
				IdempotencyKey:  "create:" + tc.sessID,
				PayloadHash:     "create:" + tc.sessID,
				RequestHash:     "create:" + tc.sessID,
				Kind:            sessionruntime.SessionMutationCreateSession,
				Session:         &sessSnap,
				NowUnixMs:       now,
			}); err != nil {
				t.Fatal(err)
			}

			task := &pebblestore.ProjectTaskRecord{
				ID:           tc.taskID,
				ProjectID:    proj.ID,
				AccountID:    accountID,
				Title:        "Terminal Guarded Task",
				Agent:        "coder",
				Status:       tc.initialState,
				IsIntegrated: tc.isIntegrated,
				SessionID:    tc.sessID,
			}
			if err := sessionStore.PutProjectTask(accountID, task); err != nil {
				t.Fatal(err)
			}

			job := sessionV3ExecutorJob{
				Principal: testPrincipal(),
				SessionID: tc.sessID,
				RunID:     "run-term-check",
			}
			// Reconcile terminal run
			if err := server.reconcileProjectTaskRunLifecycle(job, sessionruntime.RunIntentCompleted, ""); err != nil {
				t.Fatalf("reconcile failed: %v", err)
			}

			updated, found, err := sessionStore.GetProjectTask(accountID, proj.ID, tc.taskID)
			if err != nil || !found {
				t.Fatalf("get task failed: %v", err)
			}
			if updated.Status != tc.initialState {
				t.Fatalf("task status regressed from %q to %q", tc.initialState, updated.Status)
			}
			if updated.IsIntegrated != tc.isIntegrated {
				t.Fatalf("task isIntegrated altered from %v to %v", tc.isIntegrated, updated.IsIntegrated)
			}
		})
	}
}

func TestProjectTask_HydrateTaskPlanDocument_HonorsExactBinding(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: hydrateTaskPlanDocument must honor exact PlanBinding (SessionID, PlanID,
	//   and DefinitionRevision) and must NOT attach an unrelated active plan when binding is missing or mismatched.
	// - Boundary/authority: hydrateTaskPlanDocument in project_task_program.go.
	// - Threat/regression: Cross-session plan contamination or arbitrary active plans attached to tasks.
	// - Narrowest layer: Focused unit test with Pebble session store.

	_, _, dbStore := newWorkspaceOverviewTopologyTestServer(t)
	sessionStore := pebblestore.NewSessionStore(dbStore)
	accountID := testPrincipal().AccountScopeID
	now := time.Now().UnixMilli()

	sessID := "sess_plan_exact"
	sessSnap := pebblestore.SessionSnapshot{
		ID:             sessID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: accountID,
		Title:          "Plan Session",
		Mode:           "plan",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := sessionStore.PutSessionSnapshot(sessSnap); err != nil {
		t.Fatal(err)
	}

	// Seed active plan "active-plan"
	activeDoc := &pebblestore.SessionPlanDocument{
		ID:    "active-plan",
		Title: "Unrelated Active Plan",
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-active-1", Title: "Active Checkpoint"},
		},
	}
	activePlan := pebblestore.PlanRecord{
		SessionID:     sessID,
		ID:            "active-plan",
		Version:       1,
		ApprovalState: "approved",
		Document:      activeDoc,
	}
	if err := sessionStore.PutPlan(activePlan); err != nil {
		t.Fatal(err)
	}
	if err := sessionStore.SetActivePlan(sessID, "active-plan", 1); err != nil {
		t.Fatal(err)
	}

	// Seed bound plan "bound-plan-v2"
	boundDoc := &pebblestore.SessionPlanDocument{
		ID:    "bound-plan",
		Title: "Exact Bound Plan",
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-bound-1", Title: "Bound Checkpoint"},
		},
	}
	boundPlan := pebblestore.PlanRecord{
		SessionID:     sessID,
		ID:            "bound-plan",
		Version:       2,
		ApprovalState: "pending_approval",
		Document:      boundDoc,
	}
	if err := sessionStore.PutPlan(boundPlan); err != nil {
		t.Fatal(err)
	}

	// Case 1: Task with NO PlanBinding must NOT get the active plan attached
	taskNoBinding := &pebblestore.ProjectTaskRecord{
		ID:        "task_no_binding",
		SessionID: sessID,
		Status:    "in_progress",
	}
	hydrateTaskPlanDocument(taskNoBinding, sessionStore)
	if taskNoBinding.PlanDocument != nil {
		t.Fatalf("expected nil PlanDocument when PlanBinding is missing, got %+v", taskNoBinding.PlanDocument)
	}

	// Case 2: Task with exact PlanBinding for "bound-plan" gets the exact bound document
	taskWithBinding := &pebblestore.ProjectTaskRecord{
		ID:        "task_with_binding",
		SessionID: sessID,
		Status:    "pending_approval",
		PlanBinding: &pebblestore.ProjectTaskPlanBinding{
			PlanID:             "bound-plan",
			SessionID:          sessID,
			DefinitionRevision: 2,
		},
	}
	hydrateTaskPlanDocument(taskWithBinding, sessionStore)
	if taskWithBinding.PlanDocument == nil || taskWithBinding.PlanDocument.Title != "Exact Bound Plan" {
		t.Fatalf("expected exact bound plan document, got %+v", taskWithBinding.PlanDocument)
	}

	// Case 3: Task with mismatched revision must NOT hydrate stale document
	taskMismatchedRev := &pebblestore.ProjectTaskRecord{
		ID:        "task_mismatched_rev",
		SessionID: sessID,
		Status:    "pending_approval",
		PlanBinding: &pebblestore.ProjectTaskPlanBinding{
			PlanID:             "bound-plan",
			SessionID:          sessID,
			DefinitionRevision: 99, // Mismatched
		},
	}
	hydrateTaskPlanDocument(taskMismatchedRev, sessionStore)
	if taskMismatchedRev.PlanDocument != nil {
		t.Fatalf("expected nil PlanDocument on revision mismatch, got %+v", taskMismatchedRev.PlanDocument)
	}
}
