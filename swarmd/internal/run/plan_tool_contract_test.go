package run

import (
	"encoding/json"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestPlanDocumentFromArgsAcceptsObjectAndJSONString(t *testing.T) {
	objectDoc, err := planDocumentFromArgsForTool(map[string]any{"document": map[string]any{
		"id":    "plan-object",
		"title": "Object Plan",
		"info": map[string]any{
			"goal":                "object document",
			"scope":               []any{"backend", "plan tooling"},
			"decisions":           "use canonical plan lifecycle",
			"success_criteria":    []any{"persist criteria"},
			"validation_strategy": []any{"go test ./swarmd/internal/run", "go test ./swarmd/internal/session"},
		},
		"checkpoints": []any{map[string]any{"id": "cp-1", "title": "Object checkpoint"}},
	}}, "exit_plan_mode")
	if err != nil {
		t.Fatalf("parse object document: %v", err)
	}
	if objectDoc == nil || objectDoc.ID != "plan-object" || objectDoc.Info.Scope != "backend; plan tooling" || len(objectDoc.Info.Decisions) != 1 || objectDoc.Info.Decisions[0] != "use canonical plan lifecycle" || objectDoc.Info.SuccessCriteria[0] != "persist criteria" || len(objectDoc.Checkpoints) != 1 {
		t.Fatalf("object document = %#v", objectDoc)
	}
	if objectDoc.Info.ValidationStrategy != "go test ./swarmd/internal/run; go test ./swarmd/internal/session" {
		t.Fatalf("object validation_strategy = %q", objectDoc.Info.ValidationStrategy)
	}

	jsonDoc := `{"id":"plan-json","title":"JSON Plan","info":{"goal":"json document","success_criteria":["json criteria"],"validation":["json validation"]},"checkpoints":[{"id":"cp-1","title":"JSON checkpoint"}]}`
	stringDoc, err := planDocumentFromArgsForTool(map[string]any{"document": jsonDoc}, "exit_plan_mode")
	if err != nil {
		t.Fatalf("parse JSON string document: %v", err)
	}
	if stringDoc == nil || stringDoc.ID != "plan-json" || stringDoc.Info.SuccessCriteria[0] != "json criteria" || len(stringDoc.Checkpoints) != 1 {
		t.Fatalf("JSON string document = %#v", stringDoc)
	}
	if stringDoc.Info.ValidationStrategy != "json validation" {
		t.Fatalf("JSON validation alias = %q", stringDoc.Info.ValidationStrategy)
	}
}

func TestPlanDocumentPatchFromArgsRejectsInvalidDocumentPatchOperation(t *testing.T) {
	patch, err := planDocumentPatchFromArgs(map[string]any{"document_operation": "definitely_not_supported", "info": map[string]any{"goal": "update"}})
	if err != nil {
		t.Fatalf("planDocumentPatchFromArgs: %v", err)
	}
	if patch == nil || patch.Operation != "definitely_not_supported" {
		t.Fatalf("patch = %#v", patch)
	}
	if _, err := sessionruntime.ApplyPlanDocumentPatch("plan-one", "One Plan", &pebblestore.SessionPlanDocument{Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "One"}}}, *patch); err == nil || !strings.Contains(err.Error(), "unsupported plan document patch operation") {
		t.Fatalf("ApplyPlanDocumentPatch invalid operation err=%v", err)
	}
}

func TestPlanDocumentPatchFromArgsPreservesPartialInfoFieldPresence(t *testing.T) {
	patch, err := planDocumentPatchFromArgs(map[string]any{
		"action":             "update_info",
		"document_operation": "update_info",
		"info": map[string]any{
			"scope":          "only this scope",
			"relevant_files": []any{"swarmd/internal/session/plan_document.go"},
		},
	})
	if err != nil {
		t.Fatalf("parse document patch: %v", err)
	}
	if patch == nil || patch.Info == nil || patch.Info.Scope != "only this scope" || len(patch.InfoFields) != 2 {
		t.Fatalf("patch info presence = %#v fields=%#v", patch, patch.InfoFields)
	}
}

func TestPlanDocumentFromArgsRejectsInvalidJSONString(t *testing.T) {
	_, err := planDocumentFromArgsForTool(map[string]any{"document": "not json"}, "exit_plan_mode")
	if err == nil || !strings.Contains(err.Error(), "exit_plan_mode document invalid") {
		t.Fatalf("invalid JSON string error = %v", err)
	}
}

func TestExecuteExitPlanModeRejectsAutoModeBeforeSaving(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	initial, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-auto", "Auto Plan", "# Auto", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Info:        pebblestore.SessionPlanInfo{Goal: "initial"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Initial", Status: "pending"}},
	}})
	if err != nil {
		t.Fatalf("save initial plan: %v", err)
	}
	args := map[string]any{
		"plan_id": "plan-auto",
		"title":   "Should Not Save",
		"plan":    "# Should not save",
		"document": map[string]any{
			"id":          "plan-auto",
			"title":       "Should Not Save",
			"info":        map[string]any{"goal": "mutated"},
			"checkpoints": []any{map[string]any{"id": "cp-2", "title": "Mutated"}},
		},
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	raw, err := runSvc.executeExitPlanModeTool(sessionID, sessionruntime.ModeAuto, pebblestore.AgentProfile{Name: "swarm"}, string(rawArgs), "", nil)
	if err != nil {
		t.Fatalf("auto exit_plan_mode: %v output=%s", err, raw)
	}
	var payload struct {
		Status        string `json:"status"`
		ApprovalState string `json:"approval_state"`
		Summary       string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode payload: %v raw=%s", err, raw)
	}
	if payload.Status != "rejected" || payload.ApprovalState != "not_in_plan_mode" || !strings.Contains(payload.Summary, "rejected") {
		t.Fatalf("auto rejection payload = %#v raw=%s", payload, raw)
	}
	stored, ok, err := sessionSvc.GetPlan(sessionID, "plan-auto")
	if err != nil || !ok {
		t.Fatalf("get stored plan: ok=%v err=%v", ok, err)
	}
	if stored.Version != initial.Version || stored.Title != initial.Title || stored.Plan != initial.Plan || stored.Document.Info.Goal != "initial" || len(stored.Document.Checkpoints) != 1 || stored.Document.Checkpoints[0].ID != "cp-1" {
		t.Fatalf("auto exit_plan_mode mutated stored plan: %#v", stored)
	}
	revisions, err := sessionSvc.ListPlanRevisions(sessionID, "plan-auto", 10)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revisions) != 1 {
		t.Fatalf("revision count after rejected auto exit = %d, want 1", len(revisions))
	}
}

func TestExecutePlanManageSaveApprovedPayloadPreservesDocumentArguments(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	initial, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-approved", "Approved Plan", "# Approved", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Info:        pebblestore.SessionPlanInfo{Goal: "initial"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Initial", Status: "pending"}},
	}})
	if err != nil {
		t.Fatalf("save initial plan: %v", err)
	}
	approvedPayload := map[string]any{
		"action":         "save",
		"plan_id":        "plan-approved",
		"title":          "Approved Plan Updated",
		"approval_state": "approved",
		"document": map[string]any{
			"id":    "plan-approved",
			"title": "Approved Plan Updated",
			"info": map[string]any{
				"goal":             "updated through approval",
				"relevant_files":   []any{"swarmd/internal/run/service_tools.go"},
				"success_criteria": []any{"approved args survive"},
			},
			"checkpoints": []any{
				map[string]any{"id": "cp-1", "title": "Initial", "status": "done"},
				map[string]any{"id": "cp-2", "title": "Next", "status": "pending"},
			},
			"active_checkpoint_id": "cp-2",
		},
	}
	feedbackRaw, err := json.Marshal(approvedPayload)
	if err != nil {
		t.Fatalf("marshal feedback: %v", err)
	}
	raw, err := runSvc.executePlanManageTool(sessionID, `{}`, string(feedbackRaw))
	if err != nil {
		t.Fatalf("approved plan_manage save: %v output=%s", err, raw)
	}
	var payload struct {
		Plan pebblestore.SessionPlanSnapshot `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode payload: %v raw=%s", err, raw)
	}
	if payload.Plan.Version != initial.Version+1 || payload.Plan.Document == nil {
		t.Fatalf("saved revision/document = version %d document %#v", payload.Plan.Version, payload.Plan.Document)
	}
	if payload.Plan.Document.Info.Goal != "updated through approval" || payload.Plan.Document.Info.RelevantFiles[0] != "swarmd/internal/run/service_tools.go" || payload.Plan.Document.Info.SuccessCriteria[0] != "approved args survive" {
		t.Fatalf("saved info = %#v", payload.Plan.Document.Info)
	}
	if payload.Plan.Document.ActiveCheckpointID != "cp-2" || len(payload.Plan.Document.Checkpoints) != 2 || payload.Plan.Document.Checkpoints[1].ID != "cp-2" {
		t.Fatalf("saved checkpoints = %#v", payload.Plan.Document)
	}
}

func TestPlanManageApprovalArgumentsPreservesApprovedArguments(t *testing.T) {
	payload := map[string]any{
		"action": "save",
		"title":  "Preview title",
		"document": map[string]any{
			"id": "preview",
		},
		"approved_arguments": map[string]any{
			"action":  "save",
			"plan_id": "plan-original",
			"document": map[string]any{
				"id":          "plan-original",
				"title":       "Approved title",
				"checkpoints": []any{map[string]any{"id": "cp-approved"}},
			},
		},
	}
	args := planManageApprovalArguments(payload)
	if args["plan_id"] != "plan-original" {
		t.Fatalf("plan_id = %v, want approved original", args["plan_id"])
	}
	doc, ok := args["document"].(map[string]any)
	if !ok || doc["title"] != "Approved title" {
		t.Fatalf("approved document not preserved: %#v", args["document"])
	}
}
