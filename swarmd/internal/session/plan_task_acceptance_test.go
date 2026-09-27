package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Invariant: Plan acceptance must enforce definition revision and receipt compare at the store
// transaction boundary. Stale or mismatched acceptances must be rejected with no mutation.
// Threat: Concurrent or stale plan acceptance applies outdated definitions, corrupting session mode and plan state.
// Authority: CommitV3PlanAcceptance in plan_acceptance_commit.go and applyV3PlanAcceptanceMutation in session_plan_acceptance.go.
func TestPlanAcceptanceStaleAndConcurrentRejectionNoMutation(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	session, ok, err := svc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("get session: %v", err)
	}

	doc := &pebblestore.SessionPlanDocument{
		ID:    "plan-stale-test",
		Title: "Stale Test Plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Verify stale acceptance rejection"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Checkpoint 1", Status: PlanCheckpointStatusPending, Order: 1},
		},
	}

	apply := func(input SessionMutationInput) (SessionMutationResult, error) {
		return svc.ApplySessionMutation(input)
	}

	// First commit the plan initially (version 1)
	first, err := svc.CommitV3PlanAcceptance(PlanAcceptanceCommitInput{
		Session:              session,
		PlanID:               doc.ID,
		Title:                doc.Title,
		Plan:                 "# Stale Test Plan",
		Document:             doc,
		ApplySessionMutation: apply,
	})
	if err != nil {
		t.Fatalf("initial acceptance failed: %v", err)
	}
	if first.Plan.Version != 1 {
		t.Fatalf("expected version 1, got %d", first.Plan.Version)
	}

	// Switch session back to plan mode to test a second acceptance attempt
	if _, _, err := svc.SetMode(sessionID, ModePlan); err != nil {
		t.Fatalf("set mode to plan: %v", err)
	}
	session, _, _ = svc.GetSession(sessionID)

	// Update the plan in store to version 2 (simulating a concurrent revision)
	updatedDoc := clonePlanLifecycleDocument(doc)
	updatedDoc.Title = "Stale Test Plan Rev 2"

	planV2 := first.Plan
	planV2.Version = 2
	planV2.Title = updatedDoc.Title
	planV2.Document = updatedDoc
	planV2.Status = "pending_approval"
	planV2.ApprovalState = "pending"
	if err := svc.Store().PutPlan(planV2); err != nil {
		t.Fatalf("put plan v2: %v", err)
	}

	// Test 1: Stale ExpectedBindingRevision rejection
	// Caller expects revision 1, but plan in store is currently revision 2
	_, staleErr := svc.CommitV3PlanAcceptance(PlanAcceptanceCommitInput{
		Session:                 session,
		PlanID:                  doc.ID,
		Title:                   doc.Title,
		Plan:                    "# Stale Test Plan",
		Document:                doc,
		ApplySessionMutation:    apply,
		ExpectedBindingRevision: 1,
	})
	if staleErr == nil {
		t.Fatal("expected error on stale definition revision, got nil")
	}
	if !strings.Contains(staleErr.Error(), "stale") {
		t.Fatalf("expected error message to mention stale, got: %v", staleErr)
	}

	// Verify NO MUTATION occurred:
	// Session must still be in ModePlan
	sessAfterStale, _, _ := svc.GetSession(sessionID)
	if NormalizeMode(sessAfterStale.Mode) != ModePlan {
		t.Fatalf("session mode mutated on stale rejection: got %q, want %q", sessAfterStale.Mode, ModePlan)
	}
	// Plan in store must still be version 2 and pending_approval
	planAfterStale, found, err := svc.Store().GetPlan(sessionID, doc.ID)
	if err != nil || !found {
		t.Fatalf("get plan after stale: %v", err)
	}
	if planAfterStale.Version != 2 || planAfterStale.ApprovalState != "pending" {
		t.Fatalf("plan mutated on stale rejection: version=%d approval_state=%q", planAfterStale.Version, planAfterStale.ApprovalState)
	}

	// Test 2: Receipt mismatch rejection
	_, receiptErr := svc.CommitV3PlanAcceptance(PlanAcceptanceCommitInput{
		Session:                 session,
		PlanID:                  doc.ID,
		Title:                   doc.Title,
		Plan:                    "# Stale Test Plan",
		Document:                doc,
		ApplySessionMutation:    apply,
		ExpectedBindingRevision: 2,
		ExpectedReceipt:         "mismatched-receipt-value",
	})
	if receiptErr == nil {
		t.Fatal("expected error on receipt mismatch, got nil")
	}
	if !strings.Contains(receiptErr.Error(), "receipt mismatch") {
		t.Fatalf("expected receipt mismatch error, got: %v", receiptErr)
	}

	// Verify NO MUTATION occurred after receipt mismatch
	sessAfterReceipt, _, _ := svc.GetSession(sessionID)
	if NormalizeMode(sessAfterReceipt.Mode) != ModePlan {
		t.Fatalf("session mode mutated on receipt mismatch: got %q, want %q", sessAfterReceipt.Mode, ModePlan)
	}
}

// Invariant: Submitting an identical document to a task card must detect duplicate via exact document
// hash and preserve version/linkage. Accepting the plan records durable accepted-definition receipt.
// Retrying acceptance after session mode changed to auto and execution versions advanced must recover cleanly.
// Threat: Accidental duplicate submission increments revisions; retry fails after partial accept or reconnect.
// Authority: SubmitProjectTaskStructuredPlan in plan_lifecycle_service.go, CommitV3PlanAcceptance in plan_acceptance_commit.go.
func TestSubmitProjectTaskStructuredPlanResubmitDoubleAcceptanceRecovery(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	accountID := "acc-resubmit-test"
	projectID := "proj-resubmit-test"
	taskID := "task-resubmit-test"
	wsPath := t.TempDir()

	proj := &pebblestore.ProjectRecord{
		ID:        projectID,
		AccountID: accountID,
		Name:      "Test Project",
		Workspaces: []pebblestore.ProjectWorkspaceRef{
			{Path: wsPath, Role: "primary_code"},
		},
	}
	if err := svc.Store().PutProject(accountID, proj); err != nil {
		t.Fatalf("put project: %v", err)
	}

	task := &pebblestore.ProjectTaskRecord{
		ID:            taskID,
		ProjectID:     projectID,
		AccountID:     accountID,
		Title:         "Task Resubmit Test",
		Status:        "planning",
		WorkspacePath: wsPath,
	}
	if err := svc.Store().PutProjectTask(accountID, task); err != nil {
		t.Fatalf("put task: %v", err)
	}

	lifecycle := NewPlanLifecycleService(svc)

	doc := &pebblestore.SessionPlanDocument{
		ID:    "plan-resubmit-01",
		Title: "Resubmit Test Plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Verify resubmit and recovery"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Checkpoint 1", Status: PlanCheckpointStatusPending, Order: 1},
		},
	}

	// 1. First submission
	res1, err := lifecycle.SubmitProjectTaskStructuredPlan(ProjectTaskPlanSubmissionInput{
		AccountScopeID: accountID,
		UserID:         "user-test",
		ProjectID:      projectID,
		TaskID:         taskID,
		Document:       doc,
		Title:          doc.Title,
		WorkspacePath:  wsPath,
	})
	if err != nil {
		t.Fatalf("submit 1 failed: %v", err)
	}
	if res1.Plan.Version != 1 {
		t.Fatalf("expected version 1, got %d", res1.Plan.Version)
	}
	if res1.Task.PlanBinding == nil || res1.Task.PlanBinding.DefinitionRevision != 1 {
		t.Fatalf("expected plan binding revision 1, got %#v", res1.Task.PlanBinding)
	}
	firstReceipt := res1.Receipt
	if firstReceipt == "" {
		t.Fatal("expected non-empty receipt")
	}

	// 2. Duplicate submission with EXACT same document
	res2, err := lifecycle.SubmitProjectTaskStructuredPlan(ProjectTaskPlanSubmissionInput{
		AccountScopeID: accountID,
		UserID:         "user-test",
		ProjectID:      projectID,
		TaskID:         taskID,
		Document:       doc,
		Title:          doc.Title,
		WorkspacePath:  wsPath,
	})
	if err != nil {
		t.Fatalf("submit 2 (duplicate) failed: %v", err)
	}
	// Version must NOT increment on duplicate
	if res2.Plan.Version != 1 {
		t.Fatalf("expected version 1 on duplicate submission, got %d", res2.Plan.Version)
	}
	if res2.Receipt != firstReceipt {
		t.Fatalf("expected identical receipt %q, got %q", firstReceipt, res2.Receipt)
	}

	// 3. Plan Acceptance
	commitResult, err := svc.CommitV3PlanAcceptance(PlanAcceptanceCommitInput{
		Session:                   res1.Session,
		PlanID:                    res1.Plan.ID,
		Title:                     res1.Plan.Title,
		Plan:                      res1.Plan.Plan,
		Document:                  res1.Plan.Document,
		ApplySessionMutation:      svc.ApplySessionMutation,
		ExpectedBindingRevision:   1,
		ExpectedReceipt:           firstReceipt,
		AcceptedDefinitionReceipt: firstReceipt,
		ModePreference: pebblestore.ModelPreference{
			Provider: "codex",
			Model:    "gpt-5.4",
		},
	})
	if err != nil {
		t.Fatalf("commit plan acceptance failed: %v", err)
	}
	if commitResult.Plan.Status != "approved" {
		t.Fatalf("expected plan status approved, got %q", commitResult.Plan.Status)
	}
	if commitResult.Session.Mode != ModeAuto {
		t.Fatalf("expected session mode auto, got %q", commitResult.Session.Mode)
	}
	if commitResult.Plan.AcceptedDefinitionReceipt != firstReceipt {
		t.Fatalf("expected accepted receipt %q, got %q", firstReceipt, commitResult.Plan.AcceptedDefinitionReceipt)
	}
	// Verify explicit task acceptance preference was applied even without ModeEventFields
	if commitResult.Session.Preference.Provider != "codex" || commitResult.Session.Preference.Model != "gpt-5.4" {
		t.Fatalf("explicit mode preference not applied: %#v", commitResult.Session.Preference)
	}

	// 4. Simulate version execution revisions advanced during execution (e.g. revision advanced to 3)
	advancedPlan := commitResult.Plan
	advancedPlan.Version = 3
	advancedPlan.ParentRevision = 2
	if err := svc.Store().PutPlan(advancedPlan); err != nil {
		t.Fatalf("advance plan version in store: %v", err)
	}

	// 5. Double acceptance / Retry after session mode changed to auto and version advanced
	retryResult, err := svc.CommitV3PlanAcceptance(PlanAcceptanceCommitInput{
		Session:                   commitResult.Session, // Mode is Auto!
		PlanID:                    res1.Plan.ID,
		Title:                     res1.Plan.Title,
		Plan:                      res1.Plan.Plan,
		Document:                  res1.Plan.Document,
		ApplySessionMutation:      svc.ApplySessionMutation,
		ExpectedBindingRevision:   1,
		ExpectedReceipt:           firstReceipt,
		AcceptedDefinitionReceipt: firstReceipt,
	})
	if err != nil {
		t.Fatalf("retry acceptance failed: %v", err)
	}
	if retryResult.Plan.Status != "approved" {
		t.Fatalf("retry result plan status = %q, want approved", retryResult.Plan.Status)
	}
	if retryResult.Session.Mode != ModeAuto {
		t.Fatalf("retry result session mode = %q, want auto", retryResult.Session.Mode)
	}
}

// Invariant: Direct plan submission must verify session ownership and project/task metadata,
// rejecting foreign, cross-account, cross-user, or unlinked sessions.
// Threat: Session hijacking across tasks or accounts via supplied session ID.
// Authority: SubmitProjectTaskStructuredPlan in plan_lifecycle_service.go.
func TestSubmitProjectTaskStructuredPlanUnauthorizedExistingSession(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	accountID := "acc-auth-test"
	projectID := "proj-auth-test"
	taskID := "task-auth-test"
	wsPath := t.TempDir()

	proj := &pebblestore.ProjectRecord{
		ID:        projectID,
		AccountID: accountID,
		Name:      "Auth Test Project",
		Workspaces: []pebblestore.ProjectWorkspaceRef{
			{Path: wsPath, Role: "primary_code"},
		},
	}
	if err := svc.Store().PutProject(accountID, proj); err != nil {
		t.Fatalf("put project: %v", err)
	}

	task := &pebblestore.ProjectTaskRecord{
		ID:            taskID,
		ProjectID:     projectID,
		AccountID:     accountID,
		Title:         "Auth Task",
		Status:        "planning",
		WorkspacePath: wsPath,
	}
	if err := svc.Store().PutProjectTask(accountID, task); err != nil {
		t.Fatalf("put task: %v", err)
	}

	lifecycle := NewPlanLifecycleService(svc)

	doc := &pebblestore.SessionPlanDocument{
		ID:    "plan-auth-01",
		Title: "Auth Test Plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Verify session isolation"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Checkpoint 1", Status: PlanCheckpointStatusPending, Order: 1},
		},
	}

	// 1. Create an existing unrelated session belonging to a DIFFERENT project/task
	unrelatedSessionID := "sess-unrelated-foreign"
	unrelatedSession := pebblestore.SessionSnapshot{
		ID:             unrelatedSessionID,
		AccountScopeID: accountID,
		UserID:         "user-test",
		WorkspacePath:  wsPath,
		Mode:           ModePlan,
		Metadata: map[string]any{
			"project_id": "other-project",
			"task_id":    "other-task",
		},
	}
	if err := svc.Store().PutSession(unrelatedSession); err != nil {
		t.Fatalf("put unrelated session: %v", err)
	}

	// Attempt submission with the foreign session ID
	_, err := lifecycle.SubmitProjectTaskStructuredPlan(ProjectTaskPlanSubmissionInput{
		AccountScopeID: accountID,
		UserID:         "user-test",
		ProjectID:      projectID,
		TaskID:         taskID,
		SessionID:      unrelatedSessionID,
		Document:       doc,
		Title:          doc.Title,
		WorkspacePath:  wsPath,
	})
	if err == nil {
		t.Fatal("expected error on foreign session ID, got nil")
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("expected error to mention unauthorized, got: %v", err)
	}

	// 2. Create an existing session without any project metadata
	noMetaSessionID := "sess-no-metadata"
	noMetaSession := pebblestore.SessionSnapshot{
		ID:             noMetaSessionID,
		AccountScopeID: accountID,
		UserID:         "user-test",
		WorkspacePath:  wsPath,
		Mode:           ModePlan,
		Metadata:       nil,
	}
	if err := svc.Store().PutSession(noMetaSession); err != nil {
		t.Fatalf("put no-meta session: %v", err)
	}

	_, err = lifecycle.SubmitProjectTaskStructuredPlan(ProjectTaskPlanSubmissionInput{
		AccountScopeID: accountID,
		UserID:         "user-test",
		ProjectID:      projectID,
		TaskID:         taskID,
		SessionID:      noMetaSessionID,
		Document:       doc,
		Title:          doc.Title,
		WorkspacePath:  wsPath,
	})
	if err == nil {
		t.Fatal("expected error on session without project metadata, got nil")
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("expected error to mention unauthorized, got: %v", err)
	}

	// 3. Create an existing session belonging to a DIFFERENT account
	crossAccountSessionID := "sess-cross-account"
	crossAccountSession := pebblestore.SessionSnapshot{
		ID:             crossAccountSessionID,
		AccountScopeID: "other-account",
		UserID:         "user-test",
		WorkspacePath:  wsPath,
		Mode:           ModePlan,
		Metadata: map[string]any{
			"project_id": projectID,
			"task_id":    taskID,
		},
	}
	if err := svc.Store().PutSession(crossAccountSession); err != nil {
		t.Fatalf("put cross-account session: %v", err)
	}

	_, err = lifecycle.SubmitProjectTaskStructuredPlan(ProjectTaskPlanSubmissionInput{
		AccountScopeID: accountID,
		UserID:         "user-test",
		ProjectID:      projectID,
		TaskID:         taskID,
		SessionID:      crossAccountSessionID,
		Document:       doc,
		Title:          doc.Title,
		WorkspacePath:  wsPath,
	})
	if err == nil {
		t.Fatal("expected error on cross-account session, got nil")
	}
	if !strings.Contains(err.Error(), "cross-account") {
		t.Fatalf("expected cross-account error, got: %v", err)
	}
}

// Invariant: Submitting a structured plan directly to a project task card transitions the task
// to pending_approval with PlanBinding, keeps session in plan mode, projects plan_document without
// persisting it as a second authority in Pebble, and starts NO implementation run.
// Threat: Plan submission prematurely runs implementation or persists duplicate divergent authorities.
// Authority: SubmitProjectTaskStructuredPlan in plan_lifecycle_service.go, project_store.go.
func TestSubmitProjectTaskStructuredPlanPendingNoImplementation(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	accountID := "acc-pending-test"
	projectID := "proj-pending-test"
	taskID := "task-pending-test"
	wsPath := t.TempDir()

	proj := &pebblestore.ProjectRecord{
		ID:        projectID,
		AccountID: accountID,
		Name:      "Pending Test Project",
		Workspaces: []pebblestore.ProjectWorkspaceRef{
			{Path: wsPath, Role: "primary_code"},
		},
	}
	if err := svc.Store().PutProject(accountID, proj); err != nil {
		t.Fatalf("put project: %v", err)
	}

	task := &pebblestore.ProjectTaskRecord{
		ID:            taskID,
		ProjectID:     projectID,
		AccountID:     accountID,
		Title:         "Pending Task",
		Status:        "planning",
		WorkspacePath: wsPath,
	}
	if err := svc.Store().PutProjectTask(accountID, task); err != nil {
		t.Fatalf("put task: %v", err)
	}

	lifecycle := NewPlanLifecycleService(svc)

	doc := &pebblestore.SessionPlanDocument{
		ID:    "plan-pending-01",
		Title: "Pending Test Plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Verify pending state without implementation"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Checkpoint 1", Status: PlanCheckpointStatusPending, Order: 1},
		},
	}

	res, err := lifecycle.SubmitProjectTaskStructuredPlan(ProjectTaskPlanSubmissionInput{
		AccountScopeID: accountID,
		UserID:         "user-test",
		ProjectID:      projectID,
		TaskID:         taskID,
		Document:       doc,
		Title:          doc.Title,
		WorkspacePath:  wsPath,
	})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}

	// 1. Task status must be pending_approval
	if res.Task.Status != "pending_approval" {
		t.Fatalf("task status = %q, want pending_approval", res.Task.Status)
	}

	// 2. Plan status must be pending_approval, approval_state pending
	if res.Plan.Status != "pending_approval" || res.Plan.ApprovalState != "pending" {
		t.Fatalf("plan status = %q approval_state = %q, want pending_approval / pending", res.Plan.Status, res.Plan.ApprovalState)
	}

	// 3. Session must remain in ModePlan (no auto transition before approval)
	if NormalizeMode(res.Session.Mode) != ModePlan {
		t.Fatalf("session mode = %q, want %q", res.Session.Mode, ModePlan)
	}

	// 4. Task PlanBinding must be set
	if res.Task.PlanBinding == nil {
		t.Fatal("task PlanBinding is nil")
	}
	if res.Task.PlanBinding.PlanID != "plan-pending-01" {
		t.Fatalf("task PlanBinding.PlanID = %q, want plan-pending-01", res.Task.PlanBinding.PlanID)
	}
	if res.Task.PlanBinding.DefinitionRevision != 1 {
		t.Fatalf("task PlanBinding.DefinitionRevision = %d, want 1", res.Task.PlanBinding.DefinitionRevision)
	}

	// 5. Task PlanDocument is projected (hydrated)
	if res.Task.PlanDocument == nil {
		t.Fatal("task PlanDocument was not projected")
	}

	// 6. IN PEBBLE: The raw stored task bytes must NOT contain "plan_document"
	taskKey := pebblestore.KeyProjectTask(accountID, projectID, taskID)
	rawBytes, ok, err := svc.Store().Underlying().GetBytes(taskKey)
	if err != nil || !ok {
		t.Fatalf("get raw task bytes from pebble: ok=%v err=%v", ok, err)
	}
	var rawMap map[string]any
	if err := json.Unmarshal(rawBytes, &rawMap); err != nil {
		t.Fatalf("unmarshal raw task bytes: %v", err)
	}
	if _, exists := rawMap["plan_document"]; exists {
		t.Fatalf("plan_document persisted in Pebble task record as a second authority: %s", string(rawBytes))
	}

	// 7. Verify GetProjectTask hydrates plan_document
	loadedTask, found, err := svc.Store().GetProjectTask(accountID, projectID, taskID)
	if err != nil || !found {
		t.Fatalf("get project task: %v", err)
	}
	if loadedTask.PlanDocument == nil {
		t.Fatal("GetProjectTask did not hydrate PlanDocument from canonical plan")
	}
	if loadedTask.PlanDocument.ID != "plan-pending-01" {
		t.Fatalf("hydrated PlanDocument ID = %q, want plan-pending-01", loadedTask.PlanDocument.ID)
	}

	// 8. Verify NO implementation run intent was started
	intents, err := svc.ListSessionRunIntents(res.Session.ID, 0, 10)
	if err != nil {
		t.Fatalf("list run intents: %v", err)
	}
	if len(intents) != 0 {
		t.Fatalf("expected 0 run intents for pending plan, got %d", len(intents))
	}
}
