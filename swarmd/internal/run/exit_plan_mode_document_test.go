package run

import (
	"encoding/json"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestExecuteExitPlanModePersistsStructuredDocument(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	initial, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-exit", "Initial Plan", "# Initial", "draft", "draft", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Info: pebblestore.SessionPlanInfo{Goal: "initial goal"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "First", Status: "pending"},
		},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save initial plan: %v", err)
	}

	args := map[string]any{
		"plan_id":                "plan-exit",
		"title":                  "Exit Structured Plan",
		"plan":                   "# Display only",
		"continue_automatically": true,
		"document": pebblestore.SessionPlanDocument{
			Info: pebblestore.SessionPlanInfo{
				Goal:               "ship structured exit plan",
				Context:            "exit_plan_mode must carry info and checkpoints",
				Decisions:          []string{"document is canonical"},
				RelevantFiles:      []string{"swarmd/internal/run/service_tools.go"},
				SuccessCriteria:    []string{"structured info fields persist"},
				ValidationStrategy: "go test ./swarmd/internal/run -run TestExecuteExitPlanModePersistsStructuredDocument",
			},
			Checkpoints: []pebblestore.SessionPlanCheckpoint{
				{ID: "cp-2", Title: "Second", Status: "pending", Objective: "preserve requested order"},
				{ID: "cp-1", Title: "First", Status: sessionruntime.PlanCheckpointStatusCompleted, Objective: "preserve stable id"},
			},
			ActiveCheckpointID: "cp-2",
		},
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	raw, err := runSvc.executeExitPlanModeTool(sessionID, sessionruntime.ModePlan, pebblestore.AgentProfile{Name: "swarm"}, string(rawArgs), "", nil)
	if err != nil {
		t.Fatalf("exit plan mode: %v output=%s", err, raw)
	}

	var payload struct {
		Status         string                           `json:"status"`
		PlanID         string                           `json:"plan_id"`
		Title          string                           `json:"title"`
		Plan           string                           `json:"plan"`
		ModeChanged    bool                             `json:"mode_changed"`
		NextAction     string                           `json:"next_action"`
		CheckpointID   string                           `json:"checkpoint_id"`
		Version        int                              `json:"version"`
		ParentRevision int                              `json:"parent_revision"`
		Document       *pebblestore.SessionPlanDocument `json:"document"`
		RunRequest     struct {
			PlanCheckpointContext struct {
				PlanID       string `json:"plan_id"`
				CheckpointID string `json:"checkpoint_id"`
			} `json:"plan_checkpoint_context"`
		} `json:"run_request"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode payload: %v raw=%s", err, raw)
	}
	if payload.Status != "approved" || !payload.ModeChanged {
		t.Fatalf("exit payload status/mode = %q/%v: %s", payload.Status, payload.ModeChanged, raw)
	}
	if payload.PlanID != "plan-exit" || payload.Title != "Exit Structured Plan" || payload.Plan != "# Display only" {
		t.Fatalf("exit payload identity/body = %#v", payload)
	}
	if payload.NextAction != "run_checkpoint_with_current_context" || payload.CheckpointID != "cp-2" || payload.RunRequest.PlanCheckpointContext.PlanID != "plan-exit" || payload.RunRequest.PlanCheckpointContext.CheckpointID != "cp-2" || payload.Document.ExecutionPolicy.Mode != sessionruntime.PlanExecutionPolicyModeAutomatic || payload.Document.ExecutionPolicy.Shape != sessionruntime.PlanExecutionShapeCheckpointed {
		t.Fatalf("exit payload run request = next %q checkpoint %q request %#v raw=%s", payload.NextAction, payload.CheckpointID, payload.RunRequest.PlanCheckpointContext, raw)
	}
	if payload.Version != initial.Version+1 || payload.ParentRevision != initial.Version {
		t.Fatalf("payload revision = version %d parent %d, want %d/%d", payload.Version, payload.ParentRevision, initial.Version+1, initial.Version)
	}
	assertExitPlanDocument(t, payload.Document)

	stored, ok, err := sessionSvc.GetPlan(sessionID, "plan-exit")
	if err != nil || !ok {
		t.Fatalf("get stored plan: ok=%v err=%v", ok, err)
	}
	if stored.Status != "approved" || stored.ApprovalState != "approved" || !stored.Active {
		t.Fatalf("stored status/approval/active = %q/%q/%v", stored.Status, stored.ApprovalState, stored.Active)
	}
	if stored.Version != initial.Version+1 || stored.ParentRevision != initial.Version {
		t.Fatalf("stored revision = version %d parent %d", stored.Version, stored.ParentRevision)
	}
	assertExitPlanDocument(t, stored.Document)
	if stored.Document.RevisionID != "plan-exit:v2" {
		t.Fatalf("document revision id = %q, want plan-exit:v2", stored.Document.RevisionID)
	}

	revisions, err := sessionSvc.ListPlanRevisions(sessionID, "plan-exit", 10)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revisions) != 2 {
		t.Fatalf("revision count = %d, want 2", len(revisions))
	}
	if revisions[0].Version != 2 || revisions[1].Version != 1 {
		t.Fatalf("revision ordering/versions = %#v", revisions)
	}
}

func TestExecuteExitPlanModeFailsBeforeMutationWhenAutoPolicyCannotResolve(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()
	sessionID := createPlanManageTestSession(t, sessionSvc)
	runSvc.model = nil
	mutationCalls := 0
	apply := func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		mutationCalls++
		return sessionruntime.SessionMutationResult{}, nil
	}
	args := mustMarshalPlanToolTestArgs(t, map[string]any{
		"title": "Fail closed",
		"document": pebblestore.SessionPlanDocument{
			Title:       "Fail closed",
			Info:        pebblestore.SessionPlanInfo{Goal: "do not half-transition"},
			Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Only", Status: "pending", Tasks: []string{"work"}, AcceptanceCriteria: []string{"done"}}},
		},
	})
	profile := pebblestore.AgentProfile{Name: "custom", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), Provider: "codex", Model: "action-model"}
	if _, err := runSvc.executeExitPlanModeTool(sessionID, sessionruntime.ModePlan, profile, args, "", apply); err == nil {
		t.Fatal("exit_plan_mode unexpectedly succeeded without model policy resolver")
	}
	if mutationCalls != 0 {
		t.Fatalf("mutation calls = %d, want zero", mutationCalls)
	}
	stored, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok || stored.Mode != sessionruntime.ModePlan {
		t.Fatalf("session after failed exit: ok=%t err=%v session=%+v", ok, err, stored)
	}
	if _, ok, err := sessionSvc.GetActivePlan(sessionID); err != nil || ok {
		t.Fatalf("plan persisted during failed exit: ok=%t err=%v", ok, err)
	}
}

func mustMarshalPlanToolTestArgs(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestExitPlanModePermissionPayloadIncludesStructuredDocument(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-exit", "Initial Plan", "# Initial", "draft", "draft", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Info:        pebblestore.SessionPlanInfo{Goal: "initial goal"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Initial", Status: "pending", Order: 1, Tasks: []string{"initial work"}, AcceptanceCriteria: []string{"initial work completes"}}},
	}})
	if err != nil {
		t.Fatalf("save initial plan: %v", err)
	}

	args := map[string]any{
		"plan_id":                "plan-exit",
		"title":                  "Exit Structured Plan",
		"execution_granularity":  "checkpointed",
		"continuation_policy":    "automatic",
		"continue_automatically": true,
		"document": pebblestore.SessionPlanDocument{
			Info:               pebblestore.SessionPlanInfo{Goal: "approval sees structured goal"},
			Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Initial", Status: sessionruntime.PlanCheckpointStatusCompleted, Order: 1, Tasks: []string{"initial work"}, AcceptanceCriteria: []string{"initial work completed"}}, {ID: "cp-2", Title: "Next", Status: "pending", Order: 2, Tasks: []string{"next work"}, AcceptanceCriteria: []string{"next work completes"}}},
			ActiveCheckpointID: "cp-2",
		},
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	permissionPayload, err := runSvc.buildExitPlanModePermissionPayload(sessionID, toolCallForExitPlanMode(string(rawArgs)))
	if err != nil {
		t.Fatalf("build permission payload: %v", err)
	}
	document, ok := permissionPayload["document"].(*pebblestore.SessionPlanDocument)
	if !ok || document == nil {
		t.Fatalf("permission document missing: %#v", permissionPayload["document"])
	}
	if document.ID != "plan-exit" || document.Title != "Exit Structured Plan" || document.Info.Goal != "approval sees structured goal" || len(document.Checkpoints) != 2 || document.Checkpoints[1].ID != "cp-2" || document.ActiveCheckpointID != "cp-2" {
		t.Fatalf("permission document = %#v", document)
	}
	if permissionPayload["execution_granularity"] != sessionruntime.PlanAcceptanceGranularityCheckpointed || permissionPayload["continuation_policy"] != sessionruntime.PlanAcceptanceContinuationAutomatic || permissionPayload["continue_automatically"] != true {
		t.Fatalf("permission execution recommendation = %#v", permissionPayload)
	}
	approved, ok := permissionPayload["approved_arguments"].(map[string]any)
	if !ok {
		t.Fatalf("approved arguments missing: %#v", permissionPayload["approved_arguments"])
	}
	approvedRaw, err := json.Marshal(approved["document"])
	if err != nil {
		t.Fatalf("marshal approved document: %v", err)
	}
	var decodedApprovedDoc pebblestore.SessionPlanDocument
	if err := json.Unmarshal(approvedRaw, &decodedApprovedDoc); err != nil {
		t.Fatalf("decode approved document: %v", err)
	}
	if decodedApprovedDoc.ID != "plan-exit" || decodedApprovedDoc.Title != "Exit Structured Plan" || decodedApprovedDoc.Info.Goal != document.Info.Goal {
		t.Fatalf("approved structured document missing: %#v", approved["document"])
	}
	if approved["execution_granularity"] != sessionruntime.PlanAcceptanceGranularityCheckpointed || approved["continuation_policy"] != sessionruntime.PlanAcceptanceContinuationAutomatic || approved["continue_automatically"] != true {
		t.Fatalf("approved execution recommendation missing: %#v", approved)
	}
	if _, ok := permissionPayload["prior_document"]; !ok {
		t.Fatalf("permission payload missing prior_document: %#v", permissionPayload)
	}
}

func assertExitPlanDocument(t *testing.T, document *pebblestore.SessionPlanDocument) {
	t.Helper()
	if document == nil {
		t.Fatal("document missing")
	}
	if document.ID != "plan-exit" || document.Title != "Exit Structured Plan" || document.Status != "approved" {
		t.Fatalf("document identity/status = %q/%q/%q", document.ID, document.Title, document.Status)
	}
	if document.Info.Goal != "ship structured exit plan" || document.Info.Context == "" || len(document.Info.Decisions) != 1 || document.Info.RelevantFiles[0] != "swarmd/internal/run/service_tools.go" || document.Info.SuccessCriteria[0] != "structured info fields persist" {
		t.Fatalf("document info = %#v", document.Info)
	}
	if document.ExecutionPolicy.Mode != sessionruntime.PlanExecutionPolicyModeAutomatic || document.ExecutionPolicy.Shape != sessionruntime.PlanExecutionShapeCheckpointed || document.ExecutionState != nil {
		t.Fatalf("document execution policy/state = %#v/%#v", document.ExecutionPolicy, document.ExecutionState)
	}
	if document.ActiveCheckpointID != "cp-2" {
		t.Fatalf("active checkpoint = %q", document.ActiveCheckpointID)
	}
	if len(document.Checkpoints) != 2 {
		t.Fatalf("checkpoint count = %d", len(document.Checkpoints))
	}
	if document.Checkpoints[0].ID != "cp-2" || document.Checkpoints[0].Order != 1 || document.Checkpoints[0].Status != "pending" || document.Checkpoints[0].Objective != "preserve requested order" {
		t.Fatalf("checkpoint[0] = %#v", document.Checkpoints[0])
	}
	if document.Checkpoints[1].ID != "cp-1" || document.Checkpoints[1].Order != 2 || document.Checkpoints[1].Status != sessionruntime.PlanCheckpointStatusPending || document.Checkpoints[1].Objective != "preserve stable id" {
		t.Fatalf("checkpoint[1] = %#v", document.Checkpoints[1])
	}
}

func toolCallForExitPlanMode(arguments string) tool.Call {
	return tool.Call{Name: "exit_plan_mode", Arguments: arguments}
}
