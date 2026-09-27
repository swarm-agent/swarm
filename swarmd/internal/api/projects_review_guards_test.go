package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

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
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	// Create a small coder task
	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":  "Direct coder task",
		"prompt": "Implement fix",
		"agent":  "coder",
	}, p)
	if w.Code != http.StatusCreated {
		t.Fatalf("create task failed %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskMap := resp["task"].(map[string]any)
	taskID := taskMap["id"].(string)

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

	// Verify task remained pending_approval and was NOT approved
	taskAfterBad, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, projID, taskID)
	if err != nil || !found {
		t.Fatalf("task lookup failed: %v", err)
	}
	if taskAfterBad.Status != "pending_approval" {
		t.Fatalf("expected task status to remain 'pending_approval' after rejected malformed approval, got %q", taskAfterBad.Status)
	}

	// Case 3: Empty body or valid empty object succeeds for direct unguided task
	wGood := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", "", p)
	if wGood.Code != http.StatusOK {
		t.Fatalf("expected 200 on valid empty body approve, got %d: %s", wGood.Code, wGood.Body.String())
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
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	doc1 := &pebblestore.SessionPlanDocument{
		ID:    "plan-guard-01",
		Title: "Plan Revision 1",
		Info:  pebblestore.SessionPlanInfo{Goal: "Build auth"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Checkpoint 1"},
		},
	}
	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":         "Guarded plan task",
		"prompt":        "Auth plan",
		"plan_document": doc1,
	}, p)
	if w.Code != http.StatusCreated {
		t.Fatalf("create task failed %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskMap := resp["task"].(map[string]any)
	taskID := taskMap["id"].(string)
	sessID := taskMap["session_id"].(string)

	// 1. Submit Rev 2 via SubmitProjectTaskPlan to bump the canonical plan version to 2
	doc2 := &pebblestore.SessionPlanDocument{
		ID:    "plan-guard-01",
		Title: "Plan Revision 2",
		Info:  pebblestore.SessionPlanInfo{Goal: "Build auth with MFA"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Checkpoint 1 with MFA"},
		},
	}
	subResult2, err := f.server.SubmitProjectTaskPlan(context.Background(), sessionruntime.ProjectTaskPlanSubmissionInput{
		AccountScopeID: f.accountID,
		UserID:         f.userID,
		ProjectID:      projID,
		TaskID:         taskID,
		SessionID:      sessID,
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
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	doc := &pebblestore.SessionPlanDocument{
		ID:    "plan-reject-01",
		Title: "Plan To Reject",
		Info:  pebblestore.SessionPlanInfo{Goal: "Verify atomic rejection"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Checkpoint 1"},
		},
	}
	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":         "Atomic rejection task",
		"prompt":        "Verify atomic reject",
		"plan_document": doc,
	}, p)
	if w.Code != http.StatusCreated {
		t.Fatalf("create task failed %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskMap := resp["task"].(map[string]any)
	taskID := taskMap["id"].(string)

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

// Requirement: Task rejection accepts optional revision guards, validates JSON body,
// and rejects malformed payloads and stale revisions with 400 Bad Request.
func TestProjectTaskReject_MalformedJSONAndRevisionGuards(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: POST /v3/projects/{id}/tasks/{taskId}/reject must reject malformed JSON
	//   and stale definition_revision guards without side effects.
	// - Authority: handleProjects in swarmd/internal/api/projects.go.
	// - Threat/regression: Rejecting the wrong plan revision or ignoring malformed client requests.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	doc := &pebblestore.SessionPlanDocument{
		ID:    "plan-reject-guard",
		Title: "Guard Reject Plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Guard reject"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Step 1"},
		},
	}
	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":         "Guarded reject task",
		"prompt":        "Guard test",
		"plan_document": doc,
	}, p)
	if w.Code != http.StatusCreated {
		t.Fatalf("create task failed %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskMap := resp["task"].(map[string]any)
	taskID := taskMap["id"].(string)
	sessID := taskMap["session_id"].(string)

	// 1. Malformed JSON body on reject returns 400
	wBadJSON := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/reject", "{invalid-json", p)
	if wBadJSON.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on malformed JSON reject, got %d: %s", wBadJSON.Code, wBadJSON.Body.String())
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
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}

	// Create direct coder task
	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":        "Reopen Direct Task",
		"prompt":       "Direct coder task for reopen test",
		"agent":        "coder",
		"auto_approve": true,
	}, p)
	if w.Code != http.StatusCreated {
		t.Fatalf("create task failed %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskMap := resp["task"].(map[string]any)
	taskID := taskMap["id"].(string)

	// Complete task explicitly
	wComp := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/complete", nil, p)
	if wComp.Code != http.StatusOK {
		t.Fatalf("complete task failed %d: %s", wComp.Code, wComp.Body.String())
	}

	// 1. Malformed JSON on reopen returns 400
	wBadJSON := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/reopen", "{invalid-json", p)
	if wBadJSON.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on malformed JSON reopen, got %d: %s", wBadJSON.Code, wBadJSON.Body.String())
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
	doc := &pebblestore.SessionPlanDocument{
		ID:    "plan-reopen-block",
		Title: "Plan Reopen Block",
		Info:  pebblestore.SessionPlanInfo{Goal: "Verify plan reopen block"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Checkpoint 1"},
		},
	}
	wPlan := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":         "Plan task cannot reopen directly",
		"prompt":        "Plan task",
		"plan_document": doc,
	}, p)
	var planResp map[string]any
	_ = json.Unmarshal(wPlan.Body.Bytes(), &planResp)
	pTaskMap := planResp["task"].(map[string]any)
	pTaskID := pTaskMap["id"].(string)

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
	defer f.db.Close()
	projID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	crossPrincipal := identity.Principal{Type: "user", UserID: "rogue", AccountScopeID: "rogue-acct"}

	w := f.callAPI(http.MethodPost, "/"+projID+"/tasks", map[string]any{
		"title":  "Complete and Refine Task",
		"prompt": "Test complete and refine guards",
		"agent":  "coder",
	}, p)
	if w.Code != http.StatusCreated {
		t.Fatalf("create task failed %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	taskMap := resp["task"].(map[string]any)
	taskID := taskMap["id"].(string)

	// Complete: Malformed JSON -> 400
	wCompBad := f.callAPI(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/complete", "{invalid-json", p)
	if wCompBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on malformed complete body, got %d: %s", wCompBad.Code, wCompBad.Body.String())
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
