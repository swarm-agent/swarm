package run

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestBuildPlanManagePermissionPayloadPreservesAmendPlanArguments(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-amend", "Plan Amend", "# Plan", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ID:    "plan-amend",
		Title: "Plan Amend",
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{
			Mode:  sessionruntime.PlanExecutionPolicyModeAutomatic,
			Shape: sessionruntime.PlanExecutionShapeCheckpointed,
		},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "First", Status: sessionruntime.PlanCheckpointStatusCompleted},
			{ID: "cp-2", Title: "Second", Status: sessionruntime.PlanCheckpointStatusPending},
		},
	}})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}

	args := `{"action":"amend_plan","plan_id":"plan-amend","base_revision":"1","override_stale":"true","replace_from_checkpoint_id":"cp-2","amend_future_checkpoints":true,"update_summary":"replace future work","plan":"# amended","title":"Plan Amend","document":{"id":"plan-amend","title":"Plan Amend","checkpoints":[{"id":"cp-2","title":"Second revised","status":"pending"}]}}`
	payload, ok, err := runSvc.buildPlanManagePermissionPayload(sessionID, tool.Call{Name: "plan_manage", Arguments: args})
	if err != nil {
		t.Fatalf("build permission payload: %v", err)
	}
	if !ok {
		t.Fatal("expected amend_plan permission payload")
	}
	if !payload.ApprovalRequired {
		t.Fatalf("amend_plan should require approval: %#v", payload)
	}
	if payload.PathID != "tool.plan-amendment.v1" || payload.UpdateKind != "plan_amendment" {
		t.Fatalf("unexpected amendment payload identity: %#v", payload)
	}
	if payload.PriorDocument == nil {
		t.Fatalf("amendment payload missing prior document: %#v", payload)
	}
	if payload.PlanAmendmentDelta == nil {
		t.Fatalf("amendment payload missing delta: %#v", payload)
	}
	if payload.PlanAmendmentDelta.ReplaceFromCheckpointID != "cp-2" || len(payload.PlanAmendmentDelta.PreservedCheckpoints) != 1 || payload.PlanAmendmentDelta.PreservedCheckpoints[0].ID != "cp-1" {
		t.Fatalf("unexpected amendment delta: %#v", payload.PlanAmendmentDelta)
	}
	if payload.PlanAmendmentDelta.NextCheckpoint == nil || payload.PlanAmendmentDelta.NextCheckpoint.Title != "Second revised" {
		t.Fatalf("amendment delta missing next checkpoint: %#v", payload.PlanAmendmentDelta)
	}
	if !strings.Contains(strings.Join(payload.PlanAmendmentDelta.Bullets, "\n"), "replace future work") {
		t.Fatalf("amendment delta missing reason bullet: %#v", payload.PlanAmendmentDelta.Bullets)
	}
	approved := payload.ApprovedArguments
	for _, key := range []string{"base_revision", "override_stale", "replace_from_checkpoint_id", "amend_future_checkpoints", "update_summary", "document", "plan", "title"} {
		if _, ok := approved[key]; !ok {
			t.Fatalf("approved arguments missing %q: %#v", key, approved)
		}
	}
	if got := mapInt(approved, "base_revision"); got != 1 {
		t.Fatalf("approved base_revision = %d, want 1 from string payload", got)
	}
	if !mapBool(approved, "override_stale") {
		t.Fatalf("approved override_stale did not survive string payload: %#v", approved["override_stale"])
	}
}

func TestExecutePlanManageAmendUsesApprovedArgumentsAndReportsCurrentRevision(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-amend", "Plan Amend", "# Plan", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ID:    "plan-amend",
		Title: "Plan Amend",
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{
			Mode:  sessionruntime.PlanExecutionPolicyModeAutomatic,
			Shape: sessionruntime.PlanExecutionShapeCheckpointed,
		},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "First", Status: sessionruntime.PlanCheckpointStatusCompleted},
			{ID: "cp-2", Title: "Second", Status: sessionruntime.PlanCheckpointStatusPending},
		},
	}})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}

	permissionPayload, ok, err := runSvc.buildPlanManagePermissionPayload(sessionID, tool.Call{Name: "plan_manage", Arguments: `{"action":"amend_plan","plan_id":"plan-amend","base_revision":1,"replace_from_checkpoint_id":"cp-2","update_summary":"replace future work","document":{"id":"plan-amend","title":"Plan Amend","checkpoints":[{"id":"cp-2","title":"Second revised","status":"pending"}]}}`})
	if err != nil || !ok {
		t.Fatalf("build permission payload ok=%v err=%v", ok, err)
	}
	feedback, err := json.Marshal(map[string]any{
		"action":             permissionPayload.Action,
		"approved_arguments": permissionPayload.ApprovedArguments,
	})
	if err != nil {
		t.Fatalf("marshal feedback: %v", err)
	}
	if _, err := runSvc.executePlanManageTool(sessionID, `{}`, string(feedback)); err != nil {
		t.Fatalf("execute approved amendment: %v", err)
	}
	amended, ok, err := sessionSvc.GetPlan(sessionID, "plan-amend")
	if err != nil || !ok {
		t.Fatalf("get amended plan ok=%v err=%v", ok, err)
	}
	if amended.Version != 2 || amended.ParentRevision != 1 {
		t.Fatalf("amended revision = version %d parent %d, want 2/1", amended.Version, amended.ParentRevision)
	}
	if len(amended.Document.Checkpoints) != 2 || amended.Document.Checkpoints[0].ID != "cp-1" || amended.Document.Checkpoints[1].Title != "Second revised" {
		t.Fatalf("amended checkpoints did not preserve prior and replace future: %#v", amended.Document.Checkpoints)
	}

	activeRaw, err := runSvc.executePlanManageTool(sessionID, `{"action":"get-active"}`, "")
	if err != nil {
		t.Fatalf("get active: %v", err)
	}
	var activePayload map[string]any
	if err := json.Unmarshal([]byte(activeRaw), &activePayload); err != nil {
		t.Fatalf("decode active payload: %v", err)
	}
	if got := mapInt(activePayload, "current_revision"); got != 2 {
		t.Fatalf("current_revision = %d, want 2 in payload %s", got, activeRaw)
	}
	if got := mapInt(activePayload, "base_revision"); got != 2 {
		t.Fatalf("base_revision = %d, want 2 in payload %s", got, activeRaw)
	}

	_, err = runSvc.executePlanManageTool(sessionID, `{"action":"amend_plan","plan_id":"plan-amend","base_revision":1,"replace_from_checkpoint_id":"cp-2","update_summary":"stale","document":{"id":"plan-amend","title":"Plan Amend","checkpoints":[{"id":"cp-2","title":"Again","status":"pending"}]}}`, "")
	if err == nil {
		t.Fatal("expected stale revision error")
	}
	if got := err.Error(); !strings.Contains(got, "base_revision 1 is stale") || !strings.Contains(got, "current revision is 2") {
		t.Fatalf("stale error = %q, want current revision detail", got)
	}
}

func TestExecutePlanManageApprovedAmendAppendsCompletedPlanAndReturnsCurrentContext(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	plan, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-amend-append", "Append", "# Append", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ID: "plan-amend-append", Title: "Append", Info: pebblestore.SessionPlanInfo{Goal: "append future work"},
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:     &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateWaitingReview, LastCheckpointID: "cp-1"},
		ActiveCheckpointID: "cp-1",
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Done", Status: sessionruntime.PlanCheckpointStatusCompleted, Order: 1, Tasks: []string{"done"}, AcceptanceCriteria: []string{"done"}, Report: "keep"}},
	}})
	if err != nil {
		t.Fatalf("save completed plan: %v", err)
	}
	args := fmt.Sprintf(`{"action":"amend_plan","plan_id":"plan-amend-append","base_revision":%d,"amend_future_checkpoints":true,"update_summary":"append future","document":{"title":"Append","info":{"goal":"append future work"},"execution_policy":{"mode":"automatic","shape":"checkpointed"},"checkpoints":[{"id":"cp-2","title":"Next","status":"pending","order":1,"tasks":["next"],"acceptance_criteria":["next done"]}]}}`, plan.Version)
	permissionPayload, ok, err := runSvc.buildPlanManagePermissionPayload(sessionID, tool.Call{Name: "plan_manage", Arguments: args})
	if err != nil || !ok || !permissionPayload.ApprovalRequired {
		t.Fatalf("build append approval ok=%v err=%v payload=%#v", ok, err, permissionPayload)
	}
	feedback, _ := json.Marshal(map[string]any{"action": permissionPayload.Action, "approved_arguments": permissionPayload.ApprovedArguments})
	raw, err := runSvc.executePlanManageTool(sessionID, args, string(feedback))
	if err != nil {
		t.Fatalf("execute approved append: %v raw=%s", err, raw)
	}
	var payload struct {
		Action       string `json:"action"`
		NextAction   string `json:"next_action"`
		CheckpointID string `json:"checkpoint_id"`
		Plan         struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode append payload: %v", err)
	}
	if payload.Action != "amend_plan" || payload.NextAction != "run_checkpoint_with_current_context" || payload.CheckpointID != "cp-2" {
		t.Fatalf("approved append did not auto-transition: %#v raw=%s", payload, raw)
	}
	if payload.Plan.Document == nil || len(payload.Plan.Document.Checkpoints) != 2 || payload.Plan.Document.Checkpoints[0].Report != "keep" || payload.Plan.Document.Checkpoints[1].Status != sessionruntime.PlanCheckpointStatusPending {
		t.Fatalf("approved append state = %#v", payload.Plan.Document)
	}
}

func TestExecutePlanManageRejectsRemovedRevisionAndNewOverridePaths(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()
	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-canonical", "Canonical", "# Canonical", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{Title: "Canonical", Info: pebblestore.SessionPlanInfo{Goal: "canonical"}, Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Done", Status: sessionruntime.PlanCheckpointStatusCompleted, Tasks: []string{"done"}, AcceptanceCriteria: []string{"done"}}}}})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if _, err := runSvc.executePlanManageTool(sessionID, `{"action":"request_plan_revision","document":{"title":"Bad"}}`, ""); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("removed revision path error = %v", err)
	}
	if _, err := runSvc.executePlanManageTool(sessionID, `{"action":"new","title":"Bad replacement","override":true}`, ""); err == nil || !strings.Contains(err.Error(), "request_new_plan") {
		t.Fatalf("legacy override path error = %v", err)
	}
	active, ok, err := sessionSvc.GetActivePlan(sessionID)
	if err != nil || !ok || active.ID != "plan-canonical" {
		t.Fatalf("rejected paths mutated active plan: ok=%v err=%v plan=%#v", ok, err, active)
	}
}

func TestExecutePlanManageAmendAllowsStringOverrideStaleWithoutBaseRevision(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-amend", "Plan Amend", "# Plan", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ID:    "plan-amend",
		Title: "Plan Amend",
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{
			Mode:  sessionruntime.PlanExecutionPolicyModeAutomatic,
			Shape: sessionruntime.PlanExecutionShapeCheckpointed,
		},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "First", Status: sessionruntime.PlanCheckpointStatusCompleted},
			{ID: "cp-2", Title: "Second", Status: sessionruntime.PlanCheckpointStatusPending},
		},
	}})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}

	_, err = runSvc.executePlanManageTool(sessionID, `{"action":"amend_plan","plan_id":"plan-amend","override_stale":"true","replace_from_checkpoint_id":"cp-2","update_summary":"override stale","document":{"id":"plan-amend","title":"Plan Amend","checkpoints":[{"id":"cp-2","title":"Second revised","status":"pending"}]}}`, "")
	if err != nil {
		t.Fatalf("execute override stale amendment: %v", err)
	}
}
