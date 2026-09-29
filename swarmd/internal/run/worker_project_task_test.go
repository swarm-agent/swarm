package run

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: Worker automation runs must integrate into ProjectTaskRecord with exact identity.
// Invariant: One generated task per automation run; exact WorkerID, WorkerName, WorkerRunID,
// AutomationID fields serialized as worker_id, worker_name, worker_run_id, automation_id.
// Boundary: WorkerExecutionService.startPlan, PutProjectTask in project_store.go.
func TestWorkerProjectTask_ExactIdentityAndSerialization(t *testing.T) {
	_, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	ws := ss.Store().WorkerStore()

	plan := store.SessionPlanDocument{
		Title: "Automation Task Title",
		Info:  store.SessionPlanInfo{Goal: "Inspect repo health"},
		Checkpoints: []store.SessionPlanCheckpoint{
			{ID: "cp-1", Order: 1, Title: "Inspect", Tasks: []string{"Inspect"}, AcceptanceCriteria: []string{"Healthy"}, Status: "pending"},
		},
	}
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{
		Name:                  "Health Monitor",
		Description:           "Monitors repo health",
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}},
		Automations: []store.WorkerAutomationDefinition{
			{Name: "daily-health", ActivationMode: "manual", Enabled: true, PlanDocument: plan},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	w, err = execution.Activate("account", "owner", w.ID, w.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}

	req := store.WorkerRunAdmission{
		WorkerID:       w.ID,
		AutomationID:   w.Automations[0].ID,
		RequestSource:  "test_run",
		Input:          map[string]any{"prompt": "Run health inspection"},
		IdempotencyKey: "exact-identity-1",
	}

	r, err := execution.Dispatch(context.Background(), "account", "owner", req)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Point lookup by deterministic task ID: task_{runID}
	taskID := "task_" + r.ID
	task, found, err := ss.Store().GetProjectTask("account", "proj_fixture", taskID)
	if err != nil {
		t.Fatalf("GetProjectTask: %v", err)
	}
	if !found || task == nil {
		t.Fatalf("expected project task %q to exist", taskID)
	}

	// 2. Exact field identity checks
	if task.WorkerID != w.ID {
		t.Errorf("task.WorkerID = %q, want %q", task.WorkerID, w.ID)
	}
	if task.WorkerName != w.Name {
		t.Errorf("task.WorkerName = %q, want %q", task.WorkerName, w.Name)
	}
	if task.WorkerRunID != r.ID {
		t.Errorf("task.WorkerRunID = %q, want %q", task.WorkerRunID, r.ID)
	}
	if task.AutomationID != w.Automations[0].ID {
		t.Errorf("task.AutomationID = %q, want %q", task.AutomationID, w.Automations[0].ID)
	}
	if task.Agent != "swarm" {
		t.Errorf("task.Agent = %q, want 'swarm'", task.Agent)
	}
	if task.OutcomeType != "general" {
		t.Errorf("task.OutcomeType = %q, want 'general'", task.OutcomeType)
	}
	if task.PlanBinding == nil || task.PlanBinding.PlanID != r.ID {
		t.Errorf("task.PlanBinding = %+v, want PlanID %q", task.PlanBinding, r.ID)
	}

	// 3. Exact JSON serialization tag validation: worker_id, worker_name, worker_run_id, automation_id
	rawBytes, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("json.Marshal(task): %v", err)
	}
	var rawMap map[string]any
	if err := json.Unmarshal(rawBytes, &rawMap); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if rawMap["worker_id"] != w.ID {
		t.Errorf("JSON worker_id = %v, want %q", rawMap["worker_id"], w.ID)
	}
	if rawMap["worker_name"] != w.Name {
		t.Errorf("JSON worker_name = %v, want %q", rawMap["worker_name"], w.Name)
	}
	if rawMap["worker_run_id"] != r.ID {
		t.Errorf("JSON worker_run_id = %v, want %q", rawMap["worker_run_id"], r.ID)
	}
	if rawMap["automation_id"] != w.Automations[0].ID {
		t.Errorf("JSON automation_id = %v, want %q", rawMap["automation_id"], w.Automations[0].ID)
	}

	// 4. Session metadata links
	sess, found, err := ss.GetSession(r.SessionID)
	if err != nil || !found {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Metadata["project_id"] != "proj_fixture" {
		t.Errorf("session project_id metadata = %v, want proj_fixture", sess.Metadata["project_id"])
	}
	if sess.Metadata["task_id"] != taskID {
		t.Errorf("session task_id metadata = %v, want %q", sess.Metadata["task_id"], taskID)
	}
	if sess.Metadata["worker_run_id"] != r.ID {
		t.Errorf("session worker_run_id metadata = %v, want %q", sess.Metadata["worker_run_id"], r.ID)
	}
}

// Requirement: Project association must trace authoritative provenance and relationships.
// Invariant: Never guess first project; detect and reject ambiguous or cross-account references.
// Boundary: WorkerExecutionService.resolveWorkerProject.
func TestWorkerProjectTask_ProjectAssociationHierarchyAndAmbiguity(t *testing.T) {
	_, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	db := ss.Store()
	ws := db.WorkerStore()

	// 1. Explicit metadata project_id overrides workspace binding
	customProj := &store.ProjectRecord{
		ID:        "proj_explicit",
		AccountID: "account",
		Name:      "Explicit Project",
	}
	if err := db.PutProject("account", customProj); err != nil {
		t.Fatal(err)
	}

	w1, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{
		Name:                  "Worker Explicit",
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}},
		Metadata:              map[string]any{"project_id": "proj_explicit"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w1, err = execution.Activate("account", "owner", w1.ID, w1.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	resolvedProj, err := execution.resolveWorkerProject(store.WorkerRunRecord{AccountScopeID: "account", WorkerID: w1.ID}, w1)
	if err != nil {
		t.Fatalf("resolveWorkerProject explicit: %v", err)
	}
	if resolvedProj.ID != "proj_explicit" {
		t.Fatalf("expected proj_explicit, got %q", resolvedProj.ID)
	}

	// 2. Automation plan project_id
	planWithProj := store.SessionPlanDocument{
		Title:    "Plan with project",
		WorkerV2: &store.AutomationV2Settings{ProjectID: "proj_explicit"},
	}
	w2, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{
		Name:                  "Worker Plan Proj",
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}},
		Automations: []store.WorkerAutomationDefinition{
			{Name: "auto-plan", ActivationMode: "manual", Enabled: true, PlanDocument: planWithProj},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w2, err = execution.Activate("account", "owner", w2.ID, w2.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	resolvedProj2, err := execution.resolveWorkerProject(store.WorkerRunRecord{AccountScopeID: "account", WorkerID: w2.ID, AutomationID: w2.Automations[0].ID}, w2)
	if err != nil {
		t.Fatalf("resolveWorkerProject plan: %v", err)
	}
	if resolvedProj2.ID != "proj_explicit" {
		t.Fatalf("expected proj_explicit from plan, got %q", resolvedProj2.ID)
	}

	// 3. Ambiguous workspace association: 2 projects share the same workspace
	ambigProj := &store.ProjectRecord{
		ID:        "proj_ambiguous_2",
		AccountID: "account",
		Name:      "Second Project sharing workspace",
		Workspaces: []store.ProjectWorkspaceRef{
			{WorkspaceID: workspaceID, Path: "/tmp/fake", Name: "shared"},
		},
	}
	if err := db.PutProject("account", ambigProj); err != nil {
		t.Fatal(err)
	}

	wAmbig, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{
		Name:                  "Worker Ambig",
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wAmbig, err = execution.Activate("account", "owner", wAmbig.ID, wAmbig.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}

	// Should reject with unambiguous error explaining that workspace belongs to multiple projects
	_, err = execution.resolveWorkerProject(store.WorkerRunRecord{AccountScopeID: "account", WorkerID: wAmbig.ID}, wAmbig)
	if err == nil || !strings.Contains(err.Error(), "ambiguous project association") {
		t.Fatalf("expected ambiguous project association error, got: %v", err)
	}

	// 4. Missing project association: workspace has no project in account
	wOrphan, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{
		Name:                  "Worker Orphan",
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}},
		LocalBindings:         map[string]string{"primary": "ws_nonexistent"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = execution.resolveWorkerProject(store.WorkerRunRecord{AccountScopeID: "account", WorkerID: wOrphan.ID}, wOrphan)
	if err == nil || !strings.Contains(err.Error(), "project association required") {
		t.Fatalf("expected project association required error, got: %v", err)
	}
}

// Requirement: Cross-account project association and provenance leaks must be rejected.
// Invariant: Account boundary isolation is strictly enforced; no cross-account project linking.
// Boundary: WorkerExecutionService.resolveWorkerProject.
func TestWorkerProjectTask_CrossAccountIsolation(t *testing.T) {
	_, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	db := ss.Store()
	ws := db.WorkerStore()

	// Create project in a foreign account
	foreignProj := &store.ProjectRecord{
		ID:        "proj_foreign",
		AccountID: "foreign_account",
		Name:      "Foreign Project",
	}
	if err := db.PutProject("foreign_account", foreignProj); err != nil {
		t.Fatal(err)
	}

	// 1. Worker in "account" referencing "proj_foreign"
	wForeign, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{
		Name:                  "Worker Cross-Account",
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}},
		Metadata:              map[string]any{"project_id": "proj_foreign"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wForeign, err = execution.Activate("account", "owner", wForeign.ID, wForeign.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}

	_, err = execution.resolveWorkerProject(store.WorkerRunRecord{AccountScopeID: "account", WorkerID: wForeign.ID}, wForeign)
	if err == nil {
		t.Fatal("expected cross-account project association rejection, got nil")
	}

	// 2. Provenance cross-account leak
	foreignSession := store.SessionSnapshot{
		ID:             "foreign_session",
		AccountScopeID: "foreign_account",
		UserID:         "foreign_user",
		Mode:           "auto",
		Metadata:       map[string]any{"project_id": "proj_foreign"},
	}
	if err := db.CreateSession(foreignSession); err != nil {
		t.Fatal(err)
	}

	wProv, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{
		Name:                  "Worker Provenance Leak",
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}},
		Metadata:              map[string]any{"source_session_id": "foreign_session"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wProv, err = execution.Activate("account", "owner", wProv.ID, wProv.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}

	_, err = execution.resolveWorkerProject(store.WorkerRunRecord{AccountScopeID: "account", WorkerID: wProv.ID}, wProv)
	if err == nil || !strings.Contains(err.Error(), "cross-account") {
		t.Fatalf("expected cross-account provenance error, got: %v", err)
	}
}

// Requirement: Duplicate retries and restart must be idempotent.
// Invariant: Exactly one ProjectTaskRecord exists per automation run; no duplicates in ActiveTaskIDs.
// Boundary: WorkerExecutionService.startPlan, PutProjectTask.
func TestWorkerProjectTask_DuplicateRetryIdempotent(t *testing.T) {
	_, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	ws := ss.Store().WorkerStore()

	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{
		Name:                  "Retry Worker",
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Activate("account", "owner", w.ID, w.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}

	req := store.WorkerRunAdmission{
		WorkerID:       w.ID,
		RequestSource:  "direct",
		Input:          map[string]any{"prompt": "Audit once"},
		IdempotencyKey: "idemp-dispatch-test",
	}

	// First dispatch
	r1, err := execution.Dispatch(context.Background(), "account", "owner", req)
	if err != nil {
		t.Fatalf("first dispatch: %v", err)
	}

	// Verify task created
	taskID := "task_" + r1.ID
	task1, found, err := ss.Store().GetProjectTask("account", "proj_fixture", taskID)
	if err != nil || !found {
		t.Fatalf("expected task %q after first dispatch: %v", taskID, err)
	}
	if task1.Status != "in_progress" {
		t.Errorf("task1.Status = %q, want 'in_progress'", task1.Status)
	}

	// Verify proj.ActiveTaskIDs has exactly 1 entry
	proj, found, err := ss.Store().GetProject("account", "proj_fixture")
	if err != nil || !found {
		t.Fatalf("GetProject: %v", err)
	}
	activeCount := 0
	for _, tid := range proj.ActiveTaskIDs {
		if tid == taskID {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Errorf("expected taskID %q in ActiveTaskIDs once, found %d times", taskID, activeCount)
	}

	// Second dispatch (replay/retry with same idempotency key)
	r2, err := execution.Dispatch(context.Background(), "account", "owner", req)
	if err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	if r2.ID != r1.ID {
		t.Fatalf("expected replay run ID %q, got %q", r1.ID, r2.ID)
	}

	// Re-verify proj.ActiveTaskIDs still has exactly 1 entry
	projAfter, _, _ := ss.Store().GetProject("account", "proj_fixture")
	activeCountAfter := 0
	for _, tid := range projAfter.ActiveTaskIDs {
		if tid == taskID {
			activeCountAfter++
		}
	}
	if activeCountAfter != 1 {
		t.Errorf("after replay: expected taskID %q in ActiveTaskIDs once, found %d times", taskID, activeCountAfter)
	}

	// List project tasks: verify count is 1
	taskList, err := ss.Store().ListProjectTasks("account", "proj_fixture", 50)
	if err != nil {
		t.Fatal(err)
	}
	runTasks := 0
	for _, tk := range taskList {
		if tk.WorkerRunID == r1.ID {
			runTasks++
		}
	}
	if runTasks != 1 {
		t.Errorf("expected exactly 1 task with WorkerRunID %q, found %d", r1.ID, runTasks)
	}
}

// Requirement: Project task status must accurately reflect execution lifecycle.
// Invariant: Queued before enqueue, in_progress while running, needs_review upon success
// (never fake success completed without true git integration), and failed on cancellation/failure.
// Boundary: WorkerExecutionService.startPlan, observeRun, cancelRun, ReconcileWorker.
func TestWorkerProjectTask_RunningAndTerminalLifecycle(t *testing.T) {
	runs, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	ws := ss.Store().WorkerStore()

	doc := store.SessionPlanDocument{
		Title: "Lifecycle Test Worker",
		Info:  store.SessionPlanInfo{Goal: "Lifecycle verification"},
		Checkpoints: []store.SessionPlanCheckpoint{
			{ID: "cp-1", Order: 1, Title: "Step 1", Tasks: []string{"Do work"}, AcceptanceCriteria: []string{"Done"}, Status: "pending"},
		},
	}
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{
		Name:                  "Lifecycle Worker",
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}},
		Automations: []store.WorkerAutomationDefinition{
			{Name: "run-step", ActivationMode: "manual", Enabled: true, PlanDocument: doc},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Activate("account", "owner", w.ID, w.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}

	// Case A: Succeeded run -> transitions to needs_review (not completed, because worktree has unintegrated commits)
	reqA := store.WorkerRunAdmission{
		WorkerID:       w.ID,
		AutomationID:   w.Automations[0].ID,
		RequestSource:  "test_run",
		Input:          map[string]any{"prompt": "Run lifecycle step"},
		IdempotencyKey: "lifecycle-a",
	}
	rA, err := execution.Dispatch(context.Background(), "account", "owner", reqA)
	if err != nil {
		t.Fatal(err)
	}
	taskIDA := "task_" + rA.ID
	taskA, found, err := ss.Store().GetProjectTask("account", "proj_fixture", taskIDA)
	if err != nil || !found {
		t.Fatalf("taskA: %v", err)
	}
	if taskA.Status != "in_progress" {
		t.Errorf("running task status = %q, want 'in_progress'", taskA.Status)
	}

	// Complete plan checkpoint
	planA, _, err := ss.GetActivePlan(rA.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runs.executePlanManageToolWithMutation(rA.SessionID, `{"action":"complete_checkpoint","checkpoint_id":"cp-1","run_id":"`+rA.ID+`","attempt_id":"`+planA.Document.Checkpoints[0].AttemptID+`","run_session_id":"`+rA.SessionID+`","parent_session_id":"`+rA.SessionID+`","report":"Done","result":"done"}`, "", ss.ApplySessionMutation); err != nil {
		t.Fatal(err)
	}
	// Complete run intent
	firstIntent, _, err := ss.GetSessionRunIntent(rA.SessionID, rA.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstIntent.Status = sessions.RunIntentCompleted
	_, err = ss.ApplySessionMutation(sessions.SessionMutationInput{
		SessionID:       rA.SessionID,
		UserID:          "owner",
		AccountScopeID:  "account",
		Kind:            sessions.SessionMutationRecordRunIntent,
		ClientRequestID: "first-complete",
		IdempotencyKey:  "first-complete",
		PayloadHash:     "first-complete",
		RequestHash:     "first-complete",
		EventType:       "session.run_intent.updated",
		RunIntent:       &firstIntent,
		NowUnixMs:       time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Observe run
	if err = execution.observeRun(rA); err != nil {
		t.Fatalf("observeRun: %v", err)
	}

	// Task must be in "needs_review", NEVER prematurely "completed" without true git integration
	taskAAfter, _, err := ss.Store().GetProjectTask("account", "proj_fixture", taskIDA)
	if err != nil {
		t.Fatal(err)
	}
	if taskAAfter.Status != "needs_review" {
		t.Errorf("completed unintegrated task status = %q, want 'needs_review'", taskAAfter.Status)
	}
	if !strings.Contains(taskAAfter.ActionNeeded, "Action Needed:") {
		t.Errorf("taskA.ActionNeeded = %q, want Action Needed prefix", taskAAfter.ActionNeeded)
	}

	// Case B: Cancelled run -> transitions to failed
	reqB := store.WorkerRunAdmission{
		WorkerID:       w.ID,
		AutomationID:   w.Automations[0].ID,
		RequestSource:  "test_run",
		Input:          map[string]any{"prompt": "Run to cancel"},
		IdempotencyKey: "lifecycle-b",
	}
	rB, err := execution.Dispatch(context.Background(), "account", "owner", reqB)
	if err != nil {
		t.Fatal(err)
	}
	taskIDB := "task_" + rB.ID
	cancelled, err := execution.CancelRun(context.Background(), "account", w.ID, rB.ID)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("CancelRun: %+v %v", cancelled, err)
	}

	taskBAfter, _, err := ss.Store().GetProjectTask("account", "proj_fixture", taskIDB)
	if err != nil {
		t.Fatal(err)
	}
	if taskBAfter.Status != "failed" {
		t.Errorf("cancelled task status = %q, want 'failed'", taskBAfter.Status)
	}
	if !strings.Contains(taskBAfter.ActionNeeded, "cancelled") {
		t.Errorf("taskB.ActionNeeded = %q, want cancellation message", taskBAfter.ActionNeeded)
	}
}

// Requirement: Existing task collision with conflicting identity must fail closed.
// Invariant: startPlan refuses to overwrite a project task if worker_id, worker_run_id, automation_id,
// project_id, or account_id mismatch.
// Boundary: WorkerExecutionService.startPlan.
func TestWorkerProjectTask_AdversarialExistingTaskCollision(t *testing.T) {
	_, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	ws := ss.Store().WorkerStore()
	db := ss.Store()

	doc := store.SessionPlanDocument{
		Title: "Collision Test Worker",
		Info:  store.SessionPlanInfo{Goal: "Collision test"},
		Checkpoints: []store.SessionPlanCheckpoint{
			{ID: "cp-1", Order: 1, Title: "Step 1", Tasks: []string{"Work"}, AcceptanceCriteria: []string{"Done"}, Status: "pending"},
		},
	}
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{
		Name:                  "Collision Worker",
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}},
		Automations: []store.WorkerAutomationDefinition{
			{Name: "collision-step", ActivationMode: "manual", Enabled: true, PlanDocument: doc},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Activate("account", "owner", w.ID, w.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}

	req := store.WorkerRunAdmission{
		WorkerID:       w.ID,
		AutomationID:   w.Automations[0].ID,
		RequestSource:  "test_run",
		Input:          map[string]any{"prompt": "Run collision"},
		IdempotencyKey: "collision-idemp-1",
	}

	admitted, err := ws.AdmitWorkerRun("account", req)
	if err != nil {
		t.Fatal(err)
	}

	// Pre-create conflicting task at task_{runID} with mismatched WorkerID
	taskID := "task_" + admitted.ID
	conflictingTask := &store.ProjectTaskRecord{
		ID:           taskID,
		ProjectID:    "proj_fixture",
		AccountID:    "account",
		Title:        "Adversarial pre-existing task",
		Status:       "queued",
		Agent:        "swarm",
		WorkerID:     "foreign_worker_id",
		WorkerName:   "Foreign Worker",
		WorkerRunID:  admitted.ID,
		AutomationID: "foreign_auto_id",
	}
	if err := db.PutProjectTask("account", conflictingTask); err != nil {
		t.Fatal(err)
	}

	// Calling startPlan directly with mismatched worker/task identity must return ErrWorkerConflict
	err = execution.startPlan(context.Background(), admitted, w, doc)
	if err == nil || !errors.Is(err, store.ErrWorkerConflict) {
		t.Fatalf("expected ErrWorkerConflict on adversarial task collision, got: %v", err)
	}

	// Verify the pre-existing task was NOT mutated or overwritten
	loadedTask, found, err := db.GetProjectTask("account", "proj_fixture", taskID)
	if err != nil || !found {
		t.Fatalf("failed to reload task: %v", err)
	}
	if loadedTask.WorkerID != "foreign_worker_id" || loadedTask.Title != "Adversarial pre-existing task" {
		t.Fatalf("conflicting task was mutated: %+v", loadedTask)
	}
}

// Requirement: cancelRun must not prematurely mark project task terminal before acknowledgement.
// Invariant: The project task remains in_progress until the executor acknowledges cancellation (observeRun).
// Boundary: WorkerExecutionService.cancelRun and observeRun.
func TestWorkerProjectTask_CancellationAckRequiredBeforeTaskTerminal(t *testing.T) {
	_, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	ws := ss.Store().WorkerStore()

	doc := store.SessionPlanDocument{
		Title: "Cancel Ack Worker",
		Info:  store.SessionPlanInfo{Goal: "Cancel ack"},
		Checkpoints: []store.SessionPlanCheckpoint{
			{ID: "cp-1", Order: 1, Title: "Step 1", Tasks: []string{"Work"}, AcceptanceCriteria: []string{"Done"}, Status: "pending"},
		},
	}
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{
		Name:                  "Cancel Ack Worker",
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}},
		Automations: []store.WorkerAutomationDefinition{
			{Name: "cancel-step", ActivationMode: "manual", Enabled: true, PlanDocument: doc},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Activate("account", "owner", w.ID, w.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}

	req := store.WorkerRunAdmission{
		WorkerID:       w.ID,
		AutomationID:   w.Automations[0].ID,
		RequestSource:  "test_run",
		Input:          map[string]any{"prompt": "Run to cancel"},
		IdempotencyKey: "cancel-ack-test-1",
	}
	r, err := execution.Dispatch(context.Background(), "account", "owner", req)
	if err != nil {
		t.Fatal(err)
	}
	taskID := "task_" + r.ID

	// Verify task is in_progress
	tk, found, err := ss.Store().GetProjectTask("account", "proj_fixture", taskID)
	if err != nil || !found {
		t.Fatalf("task not found: %v", err)
	}
	if tk.Status != "in_progress" {
		t.Fatalf("expected in_progress, got %q", tk.Status)
	}

	// Call cancelRun directly (cancellation requested but not yet acknowledged by observeRun)
	if err := execution.cancelRun(r, "testing unacknowledged cancel"); err != nil {
		t.Fatal(err)
	}

	// Verify task has NOT been marked terminal prematurely: it must still be in_progress
	tkAfterCancelRun, _, err := ss.Store().GetProjectTask("account", "proj_fixture", taskID)
	if err != nil {
		t.Fatal(err)
	}
	if tkAfterCancelRun.Status != "in_progress" {
		t.Fatalf("task was prematurely marked %q before cancellation acknowledgement", tkAfterCancelRun.Status)
	}

	// Now observe the run to acknowledge cancellation
	if err := execution.observeRun(r); err != nil {
		t.Fatalf("observeRun: %v", err)
	}

	// Now task must transition to terminal status (failed)
	tkTerminal, _, err := ss.Store().GetProjectTask("account", "proj_fixture", taskID)
	if err != nil {
		t.Fatal(err)
	}
	if tkTerminal.Status != "failed" {
		t.Fatalf("expected task status failed after acknowledged cancellation, got %q", tkTerminal.Status)
	}
}

// Requirement: Terminal receipts on restart must repair prior failed task projection updates.
// Invariant: ReconcileWorker inspects terminal runs and repairs tasks left in non-terminal states.
// Boundary: WorkerExecutionService.ReconcileWorker.
func TestWorkerProjectTask_TerminalReceiptRepairOnRestart(t *testing.T) {
	_, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	ws := ss.Store().WorkerStore()
	db := ss.Store()

	doc := store.SessionPlanDocument{
		Title: "Terminal Repair Worker",
		Info:  store.SessionPlanInfo{Goal: "Terminal repair"},
		Checkpoints: []store.SessionPlanCheckpoint{
			{ID: "cp-1", Order: 1, Title: "Step 1", Tasks: []string{"Work"}, AcceptanceCriteria: []string{"Done"}, Status: "pending"},
		},
	}
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{
		Name:                  "Terminal Repair Worker",
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}},
		Automations: []store.WorkerAutomationDefinition{
			{Name: "repair-step", ActivationMode: "manual", Enabled: true, PlanDocument: doc},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Activate("account", "owner", w.ID, w.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}

	req := store.WorkerRunAdmission{
		WorkerID:       w.ID,
		AutomationID:   w.Automations[0].ID,
		RequestSource:  "test_run",
		Input:          map[string]any{"prompt": "Run repair"},
		IdempotencyKey: "terminal-repair-1",
	}
	r, err := execution.Dispatch(context.Background(), "account", "owner", req)
	if err != nil {
		t.Fatal(err)
	}
	taskID := "task_" + r.ID

	// Simulate crash: WorkerRunRecord was recorded as succeeded, but task was left in_progress
	r.Status = "succeeded"
	r.CompletedAt = time.Now().UnixMilli()
	if _, err := ws.RecordWorkerRun("account", r); err != nil {
		t.Fatal(err)
	}

	// Task in Pebble is still in_progress
	tkBefore, _, err := db.GetProjectTask("account", "proj_fixture", taskID)
	if err != nil {
		t.Fatal(err)
	}
	if tkBefore.Status != "in_progress" {
		t.Fatalf("expected in_progress, got %q", tkBefore.Status)
	}

	// ReconcileWorker runs on daemon restart
	if err := execution.ReconcileWorker(context.Background(), "account", w.ID); err != nil {
		t.Fatalf("ReconcileWorker: %v", err)
	}

	// Task must now be repaired to needs_review
	tkAfter, _, err := db.GetProjectTask("account", "proj_fixture", taskID)
	if err != nil {
		t.Fatal(err)
	}
	if tkAfter.Status != "needs_review" {
		t.Fatalf("expected task repaired to needs_review, got %q", tkAfter.Status)
	}
}

// Requirement: resolveWorkerProject must reject dangling references, truncated project scans,
// and untrusted run input overrides.
// Invariant:
// 1. Untrusted run Input project_id cannot override accepted worker/project association.
// 2. Dangling explicit project or source session project fails explicitly rather than fallback.
// 3. Truncated project list (>=200) fails explicit rather than guessing.
// 4. Session pinned project takes precedence.
// Boundary: WorkerExecutionService.resolveWorkerProject.
func TestWorkerProjectTask_ProjectAssociationHardening(t *testing.T) {
	_, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	ws := ss.Store().WorkerStore()
	db := ss.Store()

	// 1. Untrusted run input project_id conflicting with accepted worker project
	wAccepted, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{
		Name:                  "Accepted Worker",
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}},
		Metadata:              map[string]any{"project_id": "proj_fixture"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wAccepted, err = execution.Activate("account", "owner", wAccepted.ID, wAccepted.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	conflictRun := store.WorkerRunRecord{
		AccountScopeID: "account",
		WorkerID:       wAccepted.ID,
		Input:          map[string]any{"project_id": "malicious_override_project"},
	}
	_, err = execution.resolveWorkerProject(conflictRun, wAccepted)
	if err == nil || !errors.Is(err, store.ErrWorkerConflict) {
		t.Fatalf("expected ErrWorkerConflict when run Input overrides accepted project, got: %v", err)
	}

	// 2. Dangling explicit project in metadata
	wDangling, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{
		Name:                  "Dangling Worker",
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}},
		Metadata:              map[string]any{"project_id": "proj_does_not_exist"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wDangling, err = execution.Activate("account", "owner", wDangling.ID, wDangling.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = execution.resolveWorkerProject(store.WorkerRunRecord{AccountScopeID: "account", WorkerID: wDangling.ID}, wDangling)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected explicit not found error for dangling project, got: %v", err)
	}

	// 3. Dangling source session reference
	wDanglingSess, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{
		Name:                  "Dangling Session Worker",
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}},
		Metadata:              map[string]any{"source_session_id": "sess_nonexistent"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = execution.resolveWorkerProject(store.WorkerRunRecord{AccountScopeID: "account", WorkerID: wDanglingSess.ID}, wDanglingSess)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected explicit not found error for dangling source session, got: %v", err)
	}

	// 4. Truncated project list (>=200 projects) fails explicitly
	for i := 0; i < 205; i++ {
		p := &store.ProjectRecord{
			ID:        "bulk_proj_" + string(rune('a'+i/26)) + string(rune('a'+i%26)),
			AccountID: "acct_large",
			Name:      "Bulk Project",
		}
		if err := db.PutProject("acct_large", p); err != nil {
			t.Fatal(err)
		}
	}
	wLarge, err := ws.CreateWorker("acct_large", "owner", store.CreateWorkerRequest{
		Name: "Large Acct Worker",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = execution.resolveWorkerProject(store.WorkerRunRecord{AccountScopeID: "acct_large", WorkerID: wLarge.ID}, wLarge)
	if err == nil || !strings.Contains(err.Error(), "truncated list") {
		t.Fatalf("expected truncated list error for >=200 projects, got: %v", err)
	}

	// 5. Existing execution session pinned project takes precedence
	pinnedProj := &store.ProjectRecord{
		ID:        "proj_pinned",
		AccountID: "account",
		Name:      "Pinned Project",
	}
	if err := db.PutProject("account", pinnedProj); err != nil {
		t.Fatal(err)
	}
	sessSnapshot := store.SessionSnapshot{
		ID:             "execution_session_pinned",
		AccountScopeID: "account",
		UserID:         "owner",
		Mode:           "auto",
		Metadata:       map[string]any{"project_id": "proj_pinned"},
	}
	if err := db.CreateSession(sessSnapshot); err != nil {
		t.Fatal(err)
	}
	runWithPinnedSess := store.WorkerRunRecord{
		AccountScopeID: "account",
		WorkerID:       wAccepted.ID,
		SessionID:      "execution_session_pinned",
	}
	resPinned, err := execution.resolveWorkerProject(runWithPinnedSess, wAccepted)
	if err != nil {
		t.Fatalf("resolveWorkerProject with pinned session: %v", err)
	}
	if resPinned.ID != "proj_pinned" {
		t.Fatalf("expected pinned project proj_pinned, got %q", resPinned.ID)
	}
}

