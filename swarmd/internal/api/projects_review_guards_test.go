package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func (f *matrixTestFixture) seedProjectTask(task *pebblestore.ProjectTaskRecord) {
	_ = f.server.sessions.Store().PutProjectTask(f.accountID, task)
	_, _ = f.server.sessions.Store().UpdateProject(f.accountID, task.ProjectID, func(pr *pebblestore.ProjectRecord) error {
		for _, tid := range pr.ActiveTaskIDs {
			if tid == task.ID {
				return nil
			}
		}
		pr.ActiveTaskIDs = append(pr.ActiveTaskIDs, task.ID)
		return nil
	})
	if task.SessionID != "" {
		if _, ok, _ := f.server.sessions.Store().GetActiveExecutionEpoch(task.SessionID); !ok {
			f.seedExecutionEpoch(task.SessionID)
		}
	}
}

// Requirement: Malformed JSON bodies sent to task approval must be rejected with 400 Bad Request,
// preventing silent fallback or execution with unintended/empty guards.
func TestProjectTaskApprove_MalformedJSONRejected(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: POST /v3/projects/{id}/tasks/{taskId}/approve must reject malformed JSON
	//   with HTTP 400 Bad Request. It must never silently ignore JSON decode errors or drop guards.
	// - Authority: handleProjects in swarmd/internal/api/projects.go.
	// - Threat/regression: Callers intending to pass safety guards (such as definition_revision) having
	//   malformed JSON silently ignored, leading to unguided approval of stale plan revisions.
	f := setupMatrixTestFixture(t)
	defer func() {
		f.server.BeginShutdown()
		f.server.CancelInFlightRuns()
		if !f.server.WaitForInFlightRuns(5 * time.Second) {
			t.Error("fixture runs did not stop before store cleanup")
			return
		}
		f.db.Close()
	}()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	taskID := "task-approve-malformed"
	sessID := "session-approve-malformed"
	sess := pebblestore.SessionSnapshot{
		ID:                 sessID,
		UserID:             f.userID,
		AccountScopeID:     f.accountID,
		WorkspacePath:      "/repo/root",
		WorktreeEnabled:    true,
		WorktreeRootPath:   "/mock/worktrees/agent-ws",
		WorktreeBranch:     "agent/test-task",
		WorktreeBaseBranch: "dev",
		CreatedAt:          time.Now().UnixMilli(),
		UpdatedAt:          time.Now().UnixMilli(),
	}
	if err := f.seedSession(sess); err != nil {
		t.Fatalf("create session %q: %v", sessID, err)
	}
	task := &pebblestore.ProjectTaskRecord{
		ID:             taskID,
		ProjectID:      projID,
		AccountID:      f.accountID,
		Title:          "Direct coder task",
		Description:    "Implement fix",
		Agent:          "coder",
		Status:         "pending_approval",
		SessionID:      sessID,
		WorkspacePath:  "/mock/worktrees/agent-ws",
		WorktreeBranch: "agent/test-task",
		BaseBranch:     "dev",
		BaseCommit:     "base-commit-sha-001",
		CreatedAt:      time.Now().UnixMilli(),
		UpdatedAt:      time.Now().UnixMilli(),
	}
	f.seedProjectTask(task)

	// Case 1: Malformed JSON syntax (truncated JSON)
	wBadSyntax := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", "{invalid-json-payload", p)
	if wBadSyntax.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on malformed JSON syntax, got %d: %s", wBadSyntax.Code, wBadSyntax.Body.String())
	}
	if !strings.Contains(wBadSyntax.Body.String(), "malformed JSON body") {
		t.Fatalf("expected 'malformed JSON body' error message, got: %s", wBadSyntax.Body.String())
	}

	// Case 2: Field type mismatch (e.g. definition_revision as string instead of int)
	wTypeMismatch := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", `{"definition_revision":"one"}`, p)
	if wTypeMismatch.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on JSON type mismatch, got %d: %s", wTypeMismatch.Code, wTypeMismatch.Body.String())
	}
	if !strings.Contains(wTypeMismatch.Body.String(), "malformed JSON body") {
		t.Fatalf("expected 'malformed JSON body' error message, got: %s", wTypeMismatch.Body.String())
	}

	// Case 3: Top-level null body rejected
	wNull := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", "null", p)
	if wNull.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on null body, got %d: %s", wNull.Code, wNull.Body.String())
	}
	if !strings.Contains(wNull.Body.String(), "malformed JSON body") {
		t.Fatalf("expected 'malformed JSON body' error message on null, got: %s", wNull.Body.String())
	}

	// Case 4: Trailing garbage after valid JSON object rejected
	wTrailing := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", `{"definition_revision":1} trailing_data`, p)
	if wTrailing.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on trailing garbage, got %d: %s", wTrailing.Code, wTrailing.Body.String())
	}
	if !strings.Contains(wTrailing.Body.String(), "malformed JSON body") {
		t.Fatalf("expected 'malformed JSON body' error message on trailing data, got: %s", wTrailing.Body.String())
	}

	// Case 5: Multiple JSON objects rejected
	wMultiple := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", `{"definition_revision":1}{"extra":2}`, p)
	if wMultiple.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on multiple JSON objects, got %d: %s", wMultiple.Code, wMultiple.Body.String())
	}
	if !strings.Contains(wMultiple.Body.String(), "malformed JSON body") {
		t.Fatalf("expected 'malformed JSON body' error message on multiple objects, got: %s", wMultiple.Body.String())
	}

	// Case 6: Oversized body with valid JSON prefix rejected (max+1 enforcement)
	oversizedBody := `{"definition_revision":1}` + strings.Repeat(" ", 1024*1024+32)
	wOversized := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", oversizedBody, p)
	if wOversized.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on oversized body with valid prefix, got %d: %s", wOversized.Code, wOversized.Body.String())
	}
	if !strings.Contains(wOversized.Body.String(), "exceeds maximum allowed size") {
		t.Fatalf("expected 'exceeds maximum allowed size' error message on oversized body, got: %s", wOversized.Body.String())
	}

	// Verify task remained pending_approval and was NOT approved
	taskAfterBad, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, projID, taskID)
	if err != nil || !found {
		t.Fatalf("task lookup failed: %v", err)
	}
	if taskAfterBad.Status != "pending_approval" {
		t.Fatalf("expected task status to remain 'pending_approval' after rejected malformed approval, got %q", taskAfterBad.Status)
	}

	// Case 7: Empty body or valid empty object succeeds for direct unguided task
	wGood := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", "", p)
	if wGood.Code != http.StatusOK {
		t.Fatalf("expected 200 on valid empty body approve, got %d: %s", wGood.Code, wGood.Body.String())
	}
}

// Requirement: Plan task approvals must support idempotent retry after execution lifecycle
// increments plan.Version. The approval guard must verify definition revision and accepted receipt,
// not plan.Version, preventing false-stale rejections of valid retries.
func TestProjectTaskApprove_IdempotentRetryAfterPlanLifecycleIncrements(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: POST /v3/projects/{id}/tasks/{taskId}/approve must support idempotent
	//   retries when plan execution has already begun and bumped plan.Version. It must compare
	//   guard.DefinitionRevision against the task's PlanBinding.DefinitionRevision and accepted receipt,
	//   not the volatile execution plan.Version.
	// - Authority: ApproveProjectTask in swarmd/internal/api/project_task_program.go.
	// - Threat/regression: Review guard regression where plan.Version advancement during checkpoint
	//   execution falsely blocks subsequent idempotent approval retries.
	f := setupMatrixTestFixture(t)
	defer func() {
		f.server.BeginShutdown()
		f.server.CancelInFlightRuns()
		if !f.server.WaitForInFlightRuns(5 * time.Second) {
			t.Error("fixture runs did not stop before store cleanup")
			return
		}
		f.db.Close()
	}()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	taskID := "task-plan-idempotent"
	sessID := "session-plan-idempotent"
	planID := "plan-idempotent-01"

	doc := &pebblestore.SessionPlanDocument{
		ID:    planID,
		Title: "Plan Idempotent Test",
		Info:  pebblestore.SessionPlanInfo{Goal: "Verify idempotent approval"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{
				ID:                 "cp-1",
				Title:              "Checkpoint 1",
				Order:              1,
				Tasks:              []string{"Implement idempotent test"},
				AcceptanceCriteria: []string{"Idempotency verified"},
			},
		},
	}
	sess := pebblestore.SessionSnapshot{
		ID:             sessID,
		UserID:         f.userID,
		AccountScopeID: f.accountID,
		WorkspacePath:  "/repo/root",
		Mode:           sessionruntime.ModePlan,
		Metadata: map[string]any{
			"project_id": projID,
			"task_id":    taskID,
		},
		CreatedAt: time.Now().UnixMilli(),
		UpdatedAt: time.Now().UnixMilli(),
	}
	if err := f.seedSession(sess); err != nil {
		t.Fatalf("create session %q: %v", sessID, err)
	}
	planSnap := pebblestore.SessionPlanSnapshot{
		ID:             planID,
		SessionID:      sessID,
		UserID:         f.userID,
		AccountScopeID: f.accountID,
		Title:          "Plan Idempotent Test",
		Plan:           "# Plan Idempotent Test\n\nVerify idempotent approval",
		Document:       doc,
		Status:         "pending_approval",
		ApprovalState:  "pending_approval",
		Version:        1,
		CreatedAt:      time.Now().UnixMilli(),
		UpdatedAt:      time.Now().UnixMilli(),
	}
	if err := f.server.sessions.Store().PutPlan(planSnap); err != nil {
		t.Fatalf("put plan %q: %v", planID, err)
	}
	task := &pebblestore.ProjectTaskRecord{
		ID:            taskID,
		ProjectID:     projID,
		AccountID:     f.accountID,
		Title:         "Idempotent plan task",
		Description:   "Idempotent prompt",
		Agent:         "swarm",
		Status:        "pending_approval",
		SessionID:     sessID,
		WorkspacePath: "/repo/root",
		PlanBinding: &pebblestore.ProjectTaskPlanBinding{
			SessionID:          sessID,
			PlanID:             planID,
			DefinitionRevision: 1,
		},
		CreatedAt: time.Now().UnixMilli(),
		UpdatedAt: time.Now().UnixMilli(),
	}
	f.seedProjectTask(task)

	// 1. Initial approval succeeds
	wApprove := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", tool.ProjectTaskApprovalGuards{
		SessionID:          sessID,
		PlanID:             "plan-idempotent-01",
		DefinitionRevision: 1,
	}, p)
	if wApprove.Code != http.StatusOK {
		t.Fatalf("initial approve failed %d: %s", wApprove.Code, wApprove.Body.String())
	}
	var appResp map[string]any
	_ = json.Unmarshal(wApprove.Body.Bytes(), &appResp)
	if appResp["status"] != "approved" {
		t.Fatalf("expected status 'approved', got %q", appResp["status"])
	}

	// Verify plan was approved and accepted definition receipt was recorded
	planAfterFirst, ok, err := f.server.sessions.Store().GetPlan(sessID, "plan-idempotent-01")
	if err != nil || !ok {
		t.Fatalf("lookup plan failed: %v", err)
	}
	if planAfterFirst.ApprovalState != "approved" {
		t.Fatalf("expected plan approval state 'approved', got %q", planAfterFirst.ApprovalState)
	}
	if planAfterFirst.AcceptedDefinitionReceipt == "" {
		t.Fatal("expected plan to have AcceptedDefinitionReceipt set")
	}

	// 2. Simulate execution lifecycle advancing plan.Version (e.g. checkpoint transition to version 3)
	planAdvanced := planAfterFirst
	planAdvanced.Version = 3
	planAdvanced.ParentRevision = 2
	if err := f.server.sessions.Store().PutPlan(planAdvanced); err != nil {
		t.Fatalf("put advanced plan: %v", err)
	}

	// 3. Idempotent retry with original definition guards (DefinitionRevision: 1) MUST SUCCEED!
	wRetry := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", tool.ProjectTaskApprovalGuards{
		SessionID:          sessID,
		PlanID:             "plan-idempotent-01",
		DefinitionRevision: 1,
	}, p)
	if wRetry.Code != http.StatusOK {
		t.Fatalf("idempotent retry failed %d: %s", wRetry.Code, wRetry.Body.String())
	}
	var retryResp map[string]any
	_ = json.Unmarshal(wRetry.Body.Bytes(), &retryResp)
	if retryResp["status"] != "already_approved" && retryResp["status"] != "approved" {
		t.Fatalf("expected status 'already_approved' or 'approved', got %q", retryResp["status"])
	}

	// 4. Stale retry with non-matching definition revision (e.g. 99) MUST be rejected with 400
	wStale := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", tool.ProjectTaskApprovalGuards{
		SessionID:          sessID,
		PlanID:             "plan-idempotent-01",
		DefinitionRevision: 99,
	}, p)
	if wStale.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on stale revision retry, got %d: %s", wStale.Code, wStale.Body.String())
	}
	if !strings.Contains(wStale.Body.String(), "stale") {
		t.Fatalf("expected 'stale' error message, got: %s", wStale.Body.String())
	}
}

// Requirement: Plan task approvals require exact canonical revision guards. Stale revisions,
// mismatched session/plan IDs, or unguided calls on plan tasks must be rejected with 400.
func TestProjectTaskApprove_GuardedCanonicalRevision(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: Structured plan tasks must strictly match canonical session plan revision.
	//   Approval guards (SessionID, PlanID, DefinitionRevision) must match the bound plan and task.
	// - Authority: ApproveProjectTask in swarmd/internal/api/project_task_program.go.
	// - Threat/regression: Approving stale or out-of-order plan revisions during concurrent editing.
	f := setupMatrixTestFixture(t)
	defer func() {
		f.server.BeginShutdown()
		f.server.CancelInFlightRuns()
		if !f.server.WaitForInFlightRuns(5 * time.Second) {
			t.Error("fixture runs did not stop before store cleanup")
			return
		}
		f.db.Close()
	}()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	taskID := "task-plan-guard"
	sessID := "session-plan-guard"
	planID := "plan-guard-01"

	doc1 := &pebblestore.SessionPlanDocument{
		ID:    planID,
		Title: "Plan Revision 1",
		Info:  pebblestore.SessionPlanInfo{Goal: "Build auth"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{
				ID:                 "cp-1",
				Title:              "Checkpoint 1",
				Order:              1,
				Tasks:              []string{"Build auth"},
				AcceptanceCriteria: []string{"Auth works"},
			},
		},
	}
	sess := pebblestore.SessionSnapshot{
		ID:             sessID,
		UserID:         f.userID,
		AccountScopeID: f.accountID,
		WorkspacePath:  "/repo/root",
		Mode:           sessionruntime.ModePlan,
		Metadata: map[string]any{
			"project_id": projID,
			"task_id":    taskID,
		},
		CreatedAt: time.Now().UnixMilli(),
		UpdatedAt: time.Now().UnixMilli(),
	}
	if err := f.seedSession(sess); err != nil {
		t.Fatalf("create session %q: %v", sessID, err)
	}
	planSnap1 := pebblestore.SessionPlanSnapshot{
		ID:             planID,
		SessionID:      sessID,
		UserID:         f.userID,
		AccountScopeID: f.accountID,
		Title:          "Plan Revision 1",
		Plan:           "# Plan Revision 1\n\nBuild auth",
		Document:       doc1,
		Status:         "pending_approval",
		ApprovalState:  "pending_approval",
		Version:        1,
		CreatedAt:      time.Now().UnixMilli(),
		UpdatedAt:      time.Now().UnixMilli(),
	}
	if err := f.server.sessions.Store().PutPlan(planSnap1); err != nil {
		t.Fatalf("put plan %q: %v", planID, err)
	}
	task := &pebblestore.ProjectTaskRecord{
		ID:            taskID,
		ProjectID:     projID,
		AccountID:     f.accountID,
		Title:         "Guarded plan task",
		Description:   "Auth plan",
		Agent:         "swarm",
		Status:        "pending_approval",
		SessionID:     sessID,
		WorkspacePath: "/repo/root",
		PlanBinding: &pebblestore.ProjectTaskPlanBinding{
			SessionID:          sessID,
			PlanID:             planID,
			DefinitionRevision: 1,
		},
		CreatedAt: time.Now().UnixMilli(),
		UpdatedAt: time.Now().UnixMilli(),
	}
	f.seedProjectTask(task)

	// 1. Submit Rev 2 via SubmitProjectTaskPlan to bump the canonical plan version to 2
	doc2 := &pebblestore.SessionPlanDocument{
		ID:    planID,
		Title: "Plan Revision 2",
		Info:  pebblestore.SessionPlanInfo{Goal: "Build auth with MFA"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{
				ID:                 "cp-1",
				Title:              "Checkpoint 1 with MFA",
				Order:              1,
				Tasks:              []string{"Build auth with MFA"},
				AcceptanceCriteria: []string{"MFA works"},
			},
		},
	}
	subResult2, err := f.server.SubmitProjectTaskPlan(context.Background(), sessionruntime.ProjectTaskPlanSubmissionInput{
		AccountScopeID: f.accountID,
		UserID:         f.userID,
		ProjectID:      projID,
		TaskID:         taskID,
		SessionID:      sessID,
		WorkspacePath:  "/repo/root",
		Document:       doc2,
		PlanText:       "# Plan Revision 2",
		Title:          "Plan Revision 2",
	})
	if err != nil {
		t.Fatalf("submit rev 2 failed: %v", err)
	}
	if subResult2.Plan.Version != 2 {
		t.Fatalf("expected plan version 2, got %d", subResult2.Plan.Version)
	}

	// 2. Unguided approval on plan task must fail
	wUnguided := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", "", p)
	if wUnguided.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on unguided plan task approval, got %d: %s", wUnguided.Code, wUnguided.Body.String())
	}
	if !strings.Contains(wUnguided.Body.String(), "guards are required") {
		t.Fatalf("expected guards required error, got: %s", wUnguided.Body.String())
	}

	// 3. Approval with stale definition revision (1 instead of 2) must fail
	wStale := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", tool.ProjectTaskApprovalGuards{
		SessionID:          sessID,
		PlanID:             "plan-guard-01",
		DefinitionRevision: 1,
	}, p)
	if wStale.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on stale revision, got %d: %s", wStale.Code, wStale.Body.String())
	}
	if !strings.Contains(wStale.Body.String(), "stale") {
		t.Fatalf("expected stale error, got: %s", wStale.Body.String())
	}

	// 4. Approval with mismatched session_id must fail
	wSessMismatch := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", tool.ProjectTaskApprovalGuards{
		SessionID:          "foreign-session-id",
		PlanID:             "plan-guard-01",
		DefinitionRevision: 2,
	}, p)
	if wSessMismatch.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on session mismatch, got %d: %s", wSessMismatch.Code, wSessMismatch.Body.String())
	}
	if !strings.Contains(wSessMismatch.Body.String(), "session ID mismatch") {
		t.Fatalf("expected session ID mismatch error, got: %s", wSessMismatch.Body.String())
	}

	// 5. Approval with mismatched plan_id must fail
	wPlanMismatch := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", tool.ProjectTaskApprovalGuards{
		SessionID:          sessID,
		PlanID:             "wrong-plan-id",
		DefinitionRevision: 2,
	}, p)
	if wPlanMismatch.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on plan mismatch, got %d: %s", wPlanMismatch.Code, wPlanMismatch.Body.String())
	}
	if !strings.Contains(wPlanMismatch.Body.String(), "plan ID mismatch") {
		t.Fatalf("expected plan ID mismatch error, got: %s", wPlanMismatch.Body.String())
	}

	// 6. Approval with exact matching guards succeeds
	wExact := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", tool.ProjectTaskApprovalGuards{
		SessionID:          sessID,
		PlanID:             "plan-guard-01",
		DefinitionRevision: 2,
	}, p)
	if wExact.Code != http.StatusOK {
		t.Fatalf("expected 200 on exact approval, got %d: %s", wExact.Code, wExact.Body.String())
	}
}

// Requirement: Task rejection must reject the bound plan first; if bound plan lookup or mutation
// fails, the task must NOT be partially updated to "rejected" in Pebble store.
func TestProjectTaskReject_NoPartialFailureOnBoundPlanError(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: POST /v3/projects/{id}/tasks/{taskId}/reject must be atomic across
	//   the task record and bound session plan. If the plan cannot be updated, the task must
	//   remain in its prior state without partial failure.
	// - Authority: handleProjects in swarmd/internal/api/projects.go.
	// - Threat/regression: Tasks marked as rejected while the bound plan remains pending_approval.
	f := setupMatrixTestFixture(t)
	defer func() {
		f.server.BeginShutdown()
		f.server.CancelInFlightRuns()
		if !f.server.WaitForInFlightRuns(5 * time.Second) {
			t.Error("fixture runs did not stop before store cleanup")
			return
		}
		f.db.Close()
	}()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	taskID := "task-reject-no-partial"
	sessID := "session-reject-no-partial"
	planID := "plan-reject-01"

	doc := &pebblestore.SessionPlanDocument{
		ID:    planID,
		Title: "Plan To Reject",
		Info:  pebblestore.SessionPlanInfo{Goal: "Verify atomic rejection"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{
				ID:                 "cp-1",
				Title:              "Checkpoint 1",
				Order:              1,
				Tasks:              []string{"Reject checkpoint"},
				AcceptanceCriteria: []string{"Reject verified"},
			},
		},
	}
	sess := pebblestore.SessionSnapshot{
		ID:             sessID,
		UserID:         f.userID,
		AccountScopeID: f.accountID,
		WorkspacePath:  "/repo/root",
		Mode:           sessionruntime.ModePlan,
		CreatedAt:      time.Now().UnixMilli(),
		UpdatedAt:      time.Now().UnixMilli(),
	}
	if err := f.seedSession(sess); err != nil {
		t.Fatalf("create session %q: %v", sessID, err)
	}
	planSnap := pebblestore.SessionPlanSnapshot{
		ID:             planID,
		SessionID:      sessID,
		UserID:         f.userID,
		AccountScopeID: f.accountID,
		Title:          "Plan To Reject",
		Plan:           "# Plan To Reject",
		Document:       doc,
		Status:         "pending_approval",
		ApprovalState:  "pending_approval",
		Version:        1,
		CreatedAt:      time.Now().UnixMilli(),
		UpdatedAt:      time.Now().UnixMilli(),
	}
	if err := f.server.sessions.Store().PutPlan(planSnap); err != nil {
		t.Fatalf("put plan %q: %v", planID, err)
	}
	task := &pebblestore.ProjectTaskRecord{
		ID:            taskID,
		ProjectID:     projID,
		AccountID:     f.accountID,
		Title:         "Atomic rejection task",
		Description:   "Verify atomic reject",
		Agent:         "swarm",
		Status:        "pending_approval",
		SessionID:     sessID,
		WorkspacePath: "/repo/root",
		PlanBinding: &pebblestore.ProjectTaskPlanBinding{
			SessionID:          sessID,
			PlanID:             planID,
			DefinitionRevision: 1,
		},
		CreatedAt: time.Now().UnixMilli(),
		UpdatedAt: time.Now().UnixMilli(),
	}
	f.seedProjectTask(task)

	// Simulate broken bound plan by pointing PlanBinding to a non-existent plan ID
	_, err := f.server.sessions.Store().UpdateProjectTask(f.accountID, projID, taskID, func(task *pebblestore.ProjectTaskRecord) error {
		task.PlanBinding.PlanID = "non-existent-plan-id"
		return nil
	})
	if err != nil {
		t.Fatalf("simulate plan mismatch failed: %v", err)
	}

	// Attempt rejection: bound plan is missing, so rejection must fail closed
	wReject := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/reject", nil, p)
	if wReject.Code != http.StatusConflict && wReject.Code != http.StatusNotFound {
		t.Fatalf("expected 409 or 404 when bound plan is missing, got %d: %s", wReject.Code, wReject.Body.String())
	}

	// CRITICAL INVARIANT: The task MUST NOT be left in status "rejected"!
	taskAfterFail, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, projID, taskID)
	if err != nil || !found {
		t.Fatalf("task lookup failed: %v", err)
	}
	if taskAfterFail.Status == "rejected" {
		t.Fatal("DEFECT DETECTED: task was partially updated to 'rejected' despite bound plan failure")
	}
	if taskAfterFail.Status != "pending_approval" {
		t.Fatalf("expected task status to remain 'pending_approval', got %q", taskAfterFail.Status)
	}
}

// Requirement: If updating the project task fails after marking the bound session plan rejected,
// the honest reconciliation boundary must restore the plan to its prior status.
func TestProjectTaskReject_TaskUpdateFailureReconcilesBoundPlan(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: POST /v3/projects/{id}/tasks/{taskId}/reject reconciles across
	//   canonical session mutations and project task store updates. When task store update fails
	//   after plan rejection, the reconciliation boundary must restore the bound plan so it does
	//   not remain rejected in an inconsistent split-brain state.
	// - Authority: handleProjects in swarmd/internal/api/projects.go.
	// - Threat/regression: Failed task updates leaving orphaned rejected plans in the session store.
	f := setupMatrixTestFixture(t)
	defer func() {
		f.server.BeginShutdown()
		f.server.CancelInFlightRuns()
		if !f.server.WaitForInFlightRuns(5 * time.Second) {
			t.Error("fixture runs did not stop before store cleanup")
			return
		}
		f.db.Close()
	}()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	taskID := "task-reject-reconcile"
	sessID := "session-reject-reconcile"
	planID := "plan-reconcile-01"

	doc := &pebblestore.SessionPlanDocument{
		ID:    planID,
		Title: "Plan To Reconcile",
		Info:  pebblestore.SessionPlanInfo{Goal: "Verify reconciliation on task failure"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{
				ID:                 "cp-1",
				Title:              "Checkpoint 1",
				Order:              1,
				Tasks:              []string{"Reconcile task"},
				AcceptanceCriteria: []string{"Reconcile verified"},
			},
		},
	}
	sess := pebblestore.SessionSnapshot{
		ID:             sessID,
		UserID:         f.userID,
		AccountScopeID: f.accountID,
		WorkspacePath:  "/repo/root",
		Mode:           sessionruntime.ModePlan,
		CreatedAt:      time.Now().UnixMilli(),
		UpdatedAt:      time.Now().UnixMilli(),
	}
	if err := f.seedSession(sess); err != nil {
		t.Fatalf("create session %q: %v", sessID, err)
	}
	planSnap := pebblestore.SessionPlanSnapshot{
		ID:             planID,
		SessionID:      sessID,
		UserID:         f.userID,
		AccountScopeID: f.accountID,
		Title:          "Plan To Reconcile",
		Plan:           "# Plan To Reconcile",
		Document:       doc,
		Status:         "pending_approval",
		ApprovalState:  "pending_approval",
		Version:        1,
		CreatedAt:      time.Now().UnixMilli(),
		UpdatedAt:      time.Now().UnixMilli(),
	}
	if err := f.server.sessions.Store().PutPlan(planSnap); err != nil {
		t.Fatalf("put plan %q: %v", planID, err)
	}
	task := &pebblestore.ProjectTaskRecord{
		ID:            taskID,
		ProjectID:     projID,
		AccountID:     f.accountID,
		Title:         "Reconcile rejection task",
		Description:   "Verify reconcile reject",
		Agent:         "swarm",
		Status:        "pending_approval",
		SessionID:     sessID,
		WorkspacePath: "/repo/root",
		PlanBinding: &pebblestore.ProjectTaskPlanBinding{
			SessionID:          sessID,
			PlanID:             planID,
			DefinitionRevision: 1,
		},
		CreatedAt: time.Now().UnixMilli(),
		UpdatedAt: time.Now().UnixMilli(),
	}
	f.seedProjectTask(task)

	// Verify initial plan state is pending_approval
	initialPlan, ok, err := f.server.sessions.Store().GetPlan(sessID, "plan-reconcile-01")
	if err != nil || !ok {
		t.Fatalf("lookup initial plan: %v", err)
	}
	if initialPlan.Status != "pending_approval" {
		t.Fatalf("expected initial plan status 'pending_approval', got %q", initialPlan.Status)
	}

	// Install failure seam on UpdateProjectTask
	restore := f.server.sessions.Store().SetProjectTaskUpdateHookForTest(func(tID string) error {
		if tID == taskID {
			return errors.New("injected project task update failure")
		}
		return nil
	})
	defer restore()

	// Attempt rejection: task update will fail, triggering reconciliation boundary
	wReject := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/reject", tool.ProjectTaskApprovalGuards{
		SessionID:          sessID,
		PlanID:             "plan-reconcile-01",
		DefinitionRevision: 1,
	}, p)
	if wReject.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when task update fails, got %d: %s", wReject.Code, wReject.Body.String())
	}

	// CRITICAL INVARIANT 1: Task status must NOT be 'rejected' (must remain pending_approval)
	taskAfterFail, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, projID, taskID)
	if err != nil || !found {
		t.Fatalf("task lookup failed: %v", err)
	}
	if taskAfterFail.Status == "rejected" {
		t.Fatal("task was partially marked rejected despite store failure")
	}
	if taskAfterFail.Status != "pending_approval" {
		t.Fatalf("expected task status 'pending_approval', got %q", taskAfterFail.Status)
	}

	// CRITICAL INVARIANT 2: Bound plan must NOT remain 'rejected' (reconciled back to prior state)
	planAfterReconcile, ok, err := f.server.sessions.Store().GetPlan(sessID, "plan-reconcile-01")
	if err != nil || !ok {
		t.Fatalf("lookup reconciled plan: %v", err)
	}
	if planAfterReconcile.Status == "rejected" || planAfterReconcile.ApprovalState == "rejected" {
		t.Fatalf("plan was left in 'rejected' state after task update failure; reconciliation failed: status=%q state=%q", planAfterReconcile.Status, planAfterReconcile.ApprovalState)
	}
	if planAfterReconcile.Status != initialPlan.Status {
		t.Fatalf("expected plan status %q, got %q", initialPlan.Status, planAfterReconcile.Status)
	}
}

// Requirement: Task rejection accepts optional revision guards, validates JSON body,
// and rejects malformed payloads and stale revisions with 400 Bad Request.
func TestProjectTaskReject_MalformedJSONAndRevisionGuards(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: POST /v3/projects/{id}/tasks/{taskId}/reject must reject malformed JSON
	//   and stale definition_revision guards without side effects.
	// - Authority: handleProjects in swarmd/internal/api/projects.go.
	// - Threat/regression: Rejecting the wrong plan revision or ignoring malformed client requests.
	f := setupMatrixTestFixture(t)
	defer func() {
		f.server.BeginShutdown()
		f.server.CancelInFlightRuns()
		if !f.server.WaitForInFlightRuns(5 * time.Second) {
			t.Error("fixture runs did not stop before store cleanup")
			return
		}
		f.db.Close()
	}()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	taskID := "task-reject-guards"
	sessID := "session-reject-guards"
	planID := "plan-reject-guard"

	doc := &pebblestore.SessionPlanDocument{
		ID:    planID,
		Title: "Guard Reject Plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Guard reject"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{
				ID:                 "cp-1",
				Title:              "Step 1",
				Order:              1,
				Tasks:              []string{"Guard step"},
				AcceptanceCriteria: []string{"Guard criterion"},
			},
		},
	}
	sess := pebblestore.SessionSnapshot{
		ID:             sessID,
		UserID:         f.userID,
		AccountScopeID: f.accountID,
		WorkspacePath:  "/repo/root",
		Mode:           sessionruntime.ModePlan,
		CreatedAt:      time.Now().UnixMilli(),
		UpdatedAt:      time.Now().UnixMilli(),
	}
	if err := f.seedSession(sess); err != nil {
		t.Fatalf("create session %q: %v", sessID, err)
	}
	planSnap := pebblestore.SessionPlanSnapshot{
		ID:             planID,
		SessionID:      sessID,
		UserID:         f.userID,
		AccountScopeID: f.accountID,
		Title:          "Guard Reject Plan",
		Plan:           "# Guard Reject Plan",
		Document:       doc,
		Status:         "pending_approval",
		ApprovalState:  "pending_approval",
		Version:        1,
		CreatedAt:      time.Now().UnixMilli(),
		UpdatedAt:      time.Now().UnixMilli(),
	}
	if err := f.server.sessions.Store().PutPlan(planSnap); err != nil {
		t.Fatalf("put plan %q: %v", planID, err)
	}
	task := &pebblestore.ProjectTaskRecord{
		ID:            taskID,
		ProjectID:     projID,
		AccountID:     f.accountID,
		Title:         "Guarded reject task",
		Description:   "Guard test",
		Agent:         "swarm",
		Status:        "pending_approval",
		SessionID:     sessID,
		WorkspacePath: "/repo/root",
		PlanBinding: &pebblestore.ProjectTaskPlanBinding{
			SessionID:          sessID,
			PlanID:             planID,
			DefinitionRevision: 1,
		},
		CreatedAt: time.Now().UnixMilli(),
		UpdatedAt: time.Now().UnixMilli(),
	}
	f.seedProjectTask(task)

	// 1. Malformed JSON body on reject returns 400
	wBadJSON := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/reject", "{invalid-json", p)
	if wBadJSON.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on malformed JSON reject, got %d: %s", wBadJSON.Code, wBadJSON.Body.String())
	}

	// 1b. Top-level null rejected
	wNull := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/reject", "null", p)
	if wNull.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on null reject body, got %d: %s", wNull.Code, wNull.Body.String())
	}
	if !strings.Contains(wNull.Body.String(), "malformed JSON body") {
		t.Fatalf("expected 'malformed JSON body' on null reject, got: %s", wNull.Body.String())
	}

	// 1c. Trailing data rejected
	wTrailing := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/reject", `{"definition_revision":1} trailing`, p)
	if wTrailing.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on trailing reject data, got %d: %s", wTrailing.Code, wTrailing.Body.String())
	}
	if !strings.Contains(wTrailing.Body.String(), "malformed JSON body") {
		t.Fatalf("expected 'malformed JSON body' on trailing reject, got: %s", wTrailing.Body.String())
	}

	// 1d. Oversized body with valid prefix rejected
	wOversized := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/reject", `{"definition_revision":1}`+strings.Repeat(" ", 1024*1024+32), p)
	if wOversized.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on oversized reject body, got %d: %s", wOversized.Code, wOversized.Body.String())
	}

	// 2. Stale definition revision on reject returns 400 (guarded 99 vs current 1)
	wStale := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/reject", tool.ProjectTaskApprovalGuards{
		SessionID:          sessID,
		PlanID:             "plan-reject-guard",
		DefinitionRevision: 99,
	}, p)
	if wStale.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on stale revision reject, got %d: %s", wStale.Code, wStale.Body.String())
	}
	if !strings.Contains(wStale.Body.String(), "stale") {
		t.Fatalf("expected stale error on reject, got: %s", wStale.Body.String())
	}

	// Verify task remains pending_approval
	taskRec, _, _ := f.server.sessions.Store().GetProjectTask(f.accountID, projID, taskID)
	if taskRec.Status != "pending_approval" {
		t.Fatalf("expected task to remain pending_approval, got %q", taskRec.Status)
	}

	// 3. Exact matching revision reject succeeds and rejects both task and plan
	wGood := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/reject", tool.ProjectTaskApprovalGuards{
		SessionID:          sessID,
		PlanID:             "plan-reject-guard",
		DefinitionRevision: 1,
	}, p)
	if wGood.Code != http.StatusOK {
		t.Fatalf("expected 200 on matching reject, got %d: %s", wGood.Code, wGood.Body.String())
	}

	taskAfterReject, _, _ := f.server.sessions.Store().GetProjectTask(f.accountID, projID, taskID)
	if taskAfterReject.Status != "rejected" {
		t.Fatalf("expected task status 'rejected', got %q", taskAfterReject.Status)
	}
	planAfterReject, _, _ := f.server.sessions.Store().GetPlan(sessID, "plan-reject-guard")
	if planAfterReject.Status != "rejected" || planAfterReject.ApprovalState != "rejected" {
		t.Fatalf("expected bound plan status/approval_state 'rejected', got status=%q, state=%q", planAfterReject.Status, planAfterReject.ApprovalState)
	}
}

// Requirement: Task reopen rejects malformed JSON and stale revision guards. Completed direct
// tasks can be reopened into in_progress with feedback; structured plan tasks require plan review.
func TestProjectTaskReopen_MalformedJSONAndRevisionGuards(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: POST /v3/projects/{id}/tasks/{taskId}/reopen must reject malformed JSON
	//   and stale revision guards. Direct execution tasks in completed status reopen cleanly;
	//   tasks requiring plan review cannot bypass approval via reopen.
	// - Authority: handleProjects in swarmd/internal/api/projects.go.
	// - Threat/regression: Corrupted reopen payloads, stale send-backs, or bypassing plan approval.
	f := setupMatrixTestFixture(t)
	defer func() {
		f.server.BeginShutdown()
		f.server.CancelInFlightRuns()
		if !f.server.WaitForInFlightRuns(5 * time.Second) {
			t.Error("fixture runs did not stop before store cleanup")
			return
		}
		f.db.Close()
	}()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	// Seed completed direct coder task
	taskID := "task-reopen-direct"
	sessID := "session-reopen-direct"
	sess := pebblestore.SessionSnapshot{
		ID:                 sessID,
		UserID:             f.userID,
		AccountScopeID:     f.accountID,
		WorkspacePath:      "/repo/root",
		WorktreeEnabled:    true,
		WorktreeRootPath:   "/mock/worktrees/agent-ws",
		WorktreeBranch:     "agent/test-task",
		WorktreeBaseBranch: "dev",
		CreatedAt:          time.Now().UnixMilli(),
		UpdatedAt:          time.Now().UnixMilli(),
	}
	if err := f.seedSession(sess); err != nil {
		t.Fatalf("create session %q: %v", sessID, err)
	}
	task := &pebblestore.ProjectTaskRecord{
		ID:             taskID,
		ProjectID:      projID,
		AccountID:      f.accountID,
		Title:          "Reopen Direct Task",
		Description:    "Direct coder task for reopen test",
		Agent:          "coder",
		Status:         "completed",
		Revision:       1,
		SessionID:      sessID,
		WorkspacePath:  "/mock/worktrees/agent-ws",
		WorktreeBranch: "agent/test-task",
		BaseBranch:     "dev",
		BaseCommit:     "base-commit-sha-001",
		CreatedAt:      time.Now().UnixMilli(),
		UpdatedAt:      time.Now().UnixMilli(),
	}
	f.seedProjectTask(task)

	// 1. Malformed JSON on reopen returns 400
	wBadJSON := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/reopen", "{invalid-json", p)
	if wBadJSON.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on malformed JSON reopen, got %d: %s", wBadJSON.Code, wBadJSON.Body.String())
	}

	// 1b. Top-level null rejected
	wNull := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/reopen", "null", p)
	if wNull.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on null reopen body, got %d: %s", wNull.Code, wNull.Body.String())
	}
	if !strings.Contains(wNull.Body.String(), "malformed JSON body") {
		t.Fatalf("expected 'malformed JSON body' on null reopen, got: %s", wNull.Body.String())
	}

	// 1c. Trailing data rejected
	wTrailing := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/reopen", `{"feedback":"fix"} trailing`, p)
	if wTrailing.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on trailing reopen data, got %d: %s", wTrailing.Code, wTrailing.Body.String())
	}
	if !strings.Contains(wTrailing.Body.String(), "malformed JSON body") {
		t.Fatalf("expected 'malformed JSON body' on trailing reopen, got: %s", wTrailing.Body.String())
	}

	// 1d. Oversized body with valid prefix rejected
	wOversized := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/reopen", `{"feedback":"fix"}`+strings.Repeat(" ", 1024*1024+32), p)
	if wOversized.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on oversized reopen body, got %d: %s", wOversized.Code, wOversized.Body.String())
	}

	// 2. Stale revision guard on reopen returns 400 (guarded 99 vs current revision 1)
	wStale := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/reopen", map[string]any{
		"feedback":            "Fix test assertions",
		"definition_revision": 99,
	}, p)
	if wStale.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on stale revision reopen, got %d: %s", wStale.Code, wStale.Body.String())
	}
	if !strings.Contains(wStale.Body.String(), "stale") {
		t.Fatalf("expected stale error on reopen, got: %s", wStale.Body.String())
	}

	// 3. Matching revision reopen succeeds
	wGood := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/reopen", map[string]any{
		"feedback":            "Fix test assertions",
		"definition_revision": 1,
	}, p)
	if wGood.Code != http.StatusOK {
		t.Fatalf("expected 200 on valid reopen, got %d: %s", wGood.Code, wGood.Body.String())
	}
	taskAfterReopen, _, _ := f.server.sessions.Store().GetProjectTask(f.accountID, projID, taskID)
	if taskAfterReopen.Status != "in_progress" {
		t.Fatalf("expected task status 'in_progress' after reopen, got %q", taskAfterReopen.Status)
	}

	// 4. Plan tasks in pending_approval or with PlanBinding cannot bypass approval via reopen
	pTaskID := "task-plan-reopen-block"
	pTask := &pebblestore.ProjectTaskRecord{
		ID:            pTaskID,
		ProjectID:     projID,
		AccountID:     f.accountID,
		Title:         "Plan task cannot reopen directly",
		Description:   "Plan task",
		Agent:         "swarm",
		Status:        "pending_approval",
		WorkspacePath: "/repo/root",
		PlanBinding: &pebblestore.ProjectTaskPlanBinding{
			PlanID:             "plan-reopen-block",
			DefinitionRevision: 1,
		},
		CreatedAt: time.Now().UnixMilli(),
		UpdatedAt: time.Now().UnixMilli(),
	}
	f.seedProjectTask(pTask)

	wPlanReopen := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+pTaskID+"/reopen", map[string]any{
		"feedback": "Skip approval and run",
	}, p)
	if wPlanReopen.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict when reopening plan task without approval, got %d: %s", wPlanReopen.Code, wPlanReopen.Body.String())
	}
}

// Requirement: Complete and Refine endpoints must reject malformed JSON, enforce cross-account checks,
// and reject stale revisions with 400 Bad Request.
func TestProjectTaskCompleteAndRefine_MalformedJSONAndRevisionGuards(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: Complete and Refine endpoints must validate request payloads, enforce
	//   account isolation, and reject stale revisions without mutating durable state.
	// - Authority: handleProjects in swarmd/internal/api/projects.go.
	// - Threat/regression: Corrupted complete/refine payloads, cross-account tampering, stale plan edits.
	f := setupMatrixTestFixture(t)
	defer func() {
		f.server.BeginShutdown()
		f.server.CancelInFlightRuns()
		if !f.server.WaitForInFlightRuns(5 * time.Second) {
			t.Error("fixture runs did not stop before store cleanup")
			return
		}
		f.db.Close()
	}()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	crossPrincipal := identity.Principal{Type: "user", UserID: "rogue", AccountScopeID: "rogue-acct"}

	taskID := "task-complete-refine"
	sessID := "session-complete-refine"
	task := &pebblestore.ProjectTaskRecord{
		ID:             taskID,
		ProjectID:      projID,
		AccountID:      f.accountID,
		Title:          "Complete and Refine Task",
		Description:    "Test complete and refine guards",
		Agent:          "coder",
		Status:         "in_progress",
		Revision:       1,
		SessionID:      sessID,
		WorkspacePath:  "/repo/root",
		WorktreeBranch: "agent/test-task",
		BaseBranch:     "dev",
		CreatedAt:      time.Now().UnixMilli(),
		UpdatedAt:      time.Now().UnixMilli(),
	}
	f.seedProjectTask(task)

	// Complete: Malformed JSON -> 400
	wCompBad := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/complete", "{invalid-json", p)
	if wCompBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on malformed complete body, got %d: %s", wCompBad.Code, wCompBad.Body.String())
	}

	// Complete: Null -> 400
	wCompNull := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/complete", "null", p)
	if wCompNull.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on null complete body, got %d: %s", wCompNull.Code, wCompNull.Body.String())
	}
	// Complete: Trailing -> 400
	wCompTrailing := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/complete", `{"definition_revision":1} trailing`, p)
	if wCompTrailing.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on trailing complete body, got %d: %s", wCompTrailing.Code, wCompTrailing.Body.String())
	}
	// Complete: Oversized with valid prefix -> 400
	wCompOversized := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/complete", `{"definition_revision":1}`+strings.Repeat(" ", 1024*1024+32), p)
	if wCompOversized.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on oversized complete body, got %d: %s", wCompOversized.Code, wCompOversized.Body.String())
	}

	// Complete: Stale revision guard -> 400
	wCompStale := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/complete", tool.ProjectTaskApprovalGuards{
		DefinitionRevision: 42,
	}, p)
	if wCompStale.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on stale complete revision, got %d: %s", wCompStale.Code, wCompStale.Body.String())
	}

	// Complete: Cross-account -> 403 or 404
	wCompCross := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/complete", nil, crossPrincipal)
	if wCompCross.Code != http.StatusForbidden && wCompCross.Code != http.StatusNotFound {
		t.Fatalf("expected 403 or 404 on cross-account complete, got %d: %s", wCompCross.Code, wCompCross.Body.String())
	}

	// Refine: Malformed JSON -> 400
	wRefBad := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/refine", "{invalid-json", p)
	if wRefBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on malformed refine body, got %d: %s", wRefBad.Code, wRefBad.Body.String())
	}

	// Refine: Null -> 400
	wRefNull := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/refine", "null", p)
	if wRefNull.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on null refine body, got %d: %s", wRefNull.Code, wRefNull.Body.String())
	}
	// Refine: Trailing -> 400
	wRefTrailing := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/refine", `{"feedback":"fix"} trailing`, p)
	if wRefTrailing.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on trailing refine body, got %d: %s", wRefTrailing.Code, wRefTrailing.Body.String())
	}
	// Refine: Oversized with valid prefix -> 400
	wRefOversized := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/refine", `{"feedback":"fix"}`+strings.Repeat(" ", 1024*1024+32), p)
	if wRefOversized.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on oversized refine body, got %d: %s", wRefOversized.Code, wRefOversized.Body.String())
	}

	// Refine: Stale revision guard -> 400
	wRefStale := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/refine", map[string]any{
		"feedback":            "Refine prompt",
		"definition_revision": 42,
	}, p)
	if wRefStale.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on stale refine revision, got %d: %s", wRefStale.Code, wRefStale.Body.String())
	}

	// Refine: Cross-account -> 403 or 404
	wRefCross := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/refine", map[string]any{
		"feedback": "Rogue refine",
	}, crossPrincipal)
	if wRefCross.Code != http.StatusForbidden && wRefCross.Code != http.StatusNotFound {
		t.Fatalf("expected 403 or 404 on cross-account refine, got %d: %s", wRefCross.Code, wRefCross.Body.String())
	}
}
