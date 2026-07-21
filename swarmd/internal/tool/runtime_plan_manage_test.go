package tool

import (
	"context"
	"strings"
	"testing"
)

func TestEditPendingPlanDefinitionRequiresNativeStructuredDocument(t *testing.T) {
	definition := mustFindDefinition(t, "edit_pending_plan")
	if !containsAll(definition.Description, "native structured JSON object", "never as serialized/quoted JSON text", "authoritative attached document", "preserve its current title") {
		t.Fatalf("edit_pending_plan description does not reject stringified documents: %q", definition.Description)
	}
	params, ok := definition.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("edit_pending_plan properties type = %T", definition.Parameters["properties"])
	}
	expectedRevision := params["expected_revision"].(map[string]any)
	if expectedRevision["type"] != "integer" || !containsAll(expectedRevision["description"].(string), "integer", "not a quoted string") {
		t.Fatalf("expected_revision contract = %#v", expectedRevision)
	}
	document := params["document"].(map[string]any)
	if document["type"] != "object" || !containsAll(document["description"].(string), "native JSON object", "do not pass JSON text", "quoted/stringified JSON", "current title", "unless the user explicitly requests a rename") {
		t.Fatalf("document contract = %#v", document)
	}
}

func TestPlanManageContractMakesNoPlanCheckpointAtomic(t *testing.T) {
	definition := mustFindDefinition(t, "plan_manage")
	if !containsAll(definition.Description,
		"atomically creates an approved one-checkpoint active plan and starts that checkpoint in the current run",
		"Never call request_followup_checkpoint when no active plan exists",
		"never call start_checkpoint after start_session_checkpoint",
	) {
		t.Fatalf("plan_manage description does not enforce atomic no-plan checkpoint routing: %s", definition.Description)
	}
}

func TestPlanManageDefinitionIncludesExplicitNewOverride(t *testing.T) {
	definition := mustFindDefinition(t, "plan_manage")
	if definition.Description == "" {
		t.Fatal("plan_manage description is empty")
	}
	if !containsAll(definition.Description, "new", "active plan", "override=true") {
		t.Fatalf("description %q does not make override requirement obvious", definition.Description)
	}
	if !containsAll(definition.Description, "patch", "update_section", "targeted partial edits") {
		t.Fatalf("description %q does not advertise partial plan updates", definition.Description)
	}

	params, ok := definition.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T", definition.Parameters["properties"])
	}
	override, ok := params["override"].(map[string]any)
	if !ok {
		t.Fatalf("override property missing or wrong type: %T", params["override"])
	}
	if override["type"] != "boolean" {
		t.Fatalf("override type = %v, want boolean", override["type"])
	}
	description, _ := override["description"].(string)
	if !containsAll(description, "action=new", "active plan", "replacement") {
		t.Fatalf("override description %q does not explain intentional replacement", description)
	}
	for _, name := range []string{"patch", "operation", "section", "old_text", "new_text", "text", "checklist_item", "checked", "replace_all", "document", "document_patch", "info", "checkpoint_order", "active_checkpoint_id"} {
		if _, ok := params[name].(map[string]any); !ok {
			t.Fatalf("%s property missing or wrong type: %T", name, params[name])
		}
	}
}

func TestPlanToolDefinitionsExposeCanonicalInfoFieldTypes(t *testing.T) {
	for _, toolName := range []string{"plan_manage", "exit_plan_mode"} {
		definition := mustFindDefinition(t, toolName)
		params, ok := definition.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s properties type = %T", toolName, definition.Parameters["properties"])
		}
		document, ok := params["document"].(map[string]any)
		if !ok {
			t.Fatalf("%s document property missing", toolName)
		}
		variants, _ := document["anyOf"].([]any)
		if len(variants) == 0 {
			t.Fatalf("%s document schema has no variants: %#v", toolName, document)
		}
		objectSchema, _ := variants[0].(map[string]any)
		documentProperties, _ := objectSchema["properties"].(map[string]any)
		infoSchema, _ := documentProperties["info"].(map[string]any)
		infoProperties, _ := infoSchema["properties"].(map[string]any)
		if infoProperties["scope"].(map[string]any)["type"] != "string" {
			t.Fatalf("%s info.scope schema = %#v", toolName, infoProperties["scope"])
		}
		decisions := infoProperties["decisions"].(map[string]any)
		if decisions["type"] != "array" || decisions["items"].(map[string]any)["type"] != "string" {
			t.Fatalf("%s info.decisions schema = %#v", toolName, decisions)
		}
	}
}

func TestPlanManageDefinitionRequiresRequirementAwareRestartSelection(t *testing.T) {
	definition := mustFindDefinition(t, "plan_manage")
	if !containsAll(definition.Description, "restart_checkpoint retries the same deliverable", "complete replacement title/tasks/acceptance_criteria/notes", "atomically", "independent deliverable", "request_followup_checkpoint") {
		t.Fatalf("plan_manage description does not define requirement-aware restart selection: %q", definition.Description)
	}
	params, ok := definition.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("plan_manage properties type = %T", definition.Parameters["properties"])
	}
	actionDescription, _ := params["action"].(map[string]any)["description"].(string)
	if !containsAll(actionDescription, "Before restarting after user feedback", "same deliverable", "changed that checkpoint", "independent deliverable") {
		t.Fatalf("plan_manage action description does not classify redirected work: %q", actionDescription)
	}
	changeRequestDescription, _ := params["change_request"].(map[string]any)["description"].(string)
	if !containsAll(changeRequestDescription, "restart_checkpoint", "atomically replaces the checkpoint definition", "do not restart an unchanged stale definition") {
		t.Fatalf("plan_manage change_request description does not require replacement on redirect: %q", changeRequestDescription)
	}
}

func TestPlanManageDefinitionDirectsBlockedFollowupsToAtomicRecovery(t *testing.T) {
	definition := mustFindDefinition(t, "plan_manage")
	if !containsAll(definition.Description, "blocked plan", "request_followup_checkpoint", "do not call resolve_blocked_checkpoint first", "failed checkpoints remain stopped") {
		t.Fatalf("plan_manage description does not advertise atomic blocked follow-up recovery: %q", definition.Description)
	}
	params, ok := definition.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("plan_manage properties type = %T", definition.Parameters["properties"])
	}
	actionDescription, _ := params["action"].(map[string]any)["description"].(string)
	if !containsAll(actionDescription, "active approved/running/blocked/review plan", "atomically supersedes and resolves", "do not call resolve_blocked_checkpoint first") {
		t.Fatalf("plan_manage action description does not direct atomic blocked recovery: %q", actionDescription)
	}
}

func TestToolDefinitionsRouteAgentProgressToPlanManageNotManageTodos(t *testing.T) {
	planDefinition := mustFindDefinition(t, "plan_manage")
	if !containsAll(planDefinition.Description, "agent execution progress", "canonical agent checklist/progress surface", "Do not use manage_todos") {
		t.Fatalf("plan_manage description %q does not advertise canonical agent progress tracking", planDefinition.Description)
	}
	planParams, ok := planDefinition.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("plan_manage properties type = %T", planDefinition.Parameters["properties"])
	}
	planActionDescription, _ := planParams["action"].(map[string]any)["description"].(string)
	if !containsAll(planActionDescription, "pass subtask_ids to batch every task completed since the last update", "discovery-only work", "single-step checkpoints", "set complete_checkpoint=true", "instead of making a second call", "not routine agent progress/checklist transitions") {
		t.Fatalf("plan_manage action description %q does not define smart batched progress transitions", planActionDescription)
	}
	for _, name := range []string{"subtask_id", "subtask_ids", "complete_checkpoint"} {
		if _, ok := planParams[name].(map[string]any); !ok {
			t.Fatalf("plan_manage %s property missing or wrong type: %T", name, planParams[name])
		}
	}
	checkpointDescription, _ := planParams["checkpoint"].(map[string]any)["description"].(string)
	if !containsAll(checkpointDescription, "tasks", "notes", "agent progress/checklist") {
		t.Fatalf("plan_manage checkpoint description %q does not advertise checkpoint progress fields", checkpointDescription)
	}
	for _, unwanted := range []string{"do not call complete_subtask repeatedly first", "so do not call complete_subtask repeatedly first"} {
		if strings.Contains(strings.ToLower(planDefinition.Description), unwanted) || strings.Contains(strings.ToLower(planActionDescription), unwanted) {
			t.Fatalf("plan_manage contract retained contradictory subtask suppression %q", unwanted)
		}
	}

	todoDefinition := mustFindDefinition(t, "manage_todos")
	if !containsAll(todoDefinition.Description, "user-owned", "Do not use this for agent self-tracking", "use plan_manage for agent progress") {
		t.Fatalf("manage_todos description %q does not restrict agent progress tracking", todoDefinition.Description)
	}
	todoParams, ok := todoDefinition.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("manage_todos properties type = %T", todoDefinition.Parameters["properties"])
	}
	ownerDescription, _ := todoParams["owner_kind"].(map[string]any)["description"].(string)
	if !containsAll(ownerDescription, "user", "agent self-tracking belongs in plan_manage") {
		t.Fatalf("manage_todos owner_kind description %q does not steer agents away", ownerDescription)
	}
}

func TestManageTodosRejectsAgentOwnerKindForSelfTracking(t *testing.T) {
	rt := NewRuntime(1)
	_, err := rt.ExecuteForWorkspaceScopeWithRuntime(context.Background(), WorkspaceScope{PrimaryPath: "."}, Call{
		CallID:    "call-manage-todos-agent",
		Name:      "manage_todos",
		Arguments: `{"action":"create","owner_kind":"agent","text":"agent checklist"}`,
	})
	if err == nil || !strings.Contains(err.Error(), "manage_todos is user-owned only") || !strings.Contains(err.Error(), "use plan_manage") {
		t.Fatalf("manage_todos owner_kind=agent error = %v, want plan_manage guardrail", err)
	}
}

func TestExitPlanModeDefinitionAcceptsStructuredDocument(t *testing.T) {
	definition := mustFindDefinition(t, "exit_plan_mode")
	if !containsAll(definition.Description, "structured", "SessionPlanDocument", "document") {
		t.Fatalf("exit_plan_mode description %q does not advertise structured document", definition.Description)
	}
	params, ok := definition.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T", definition.Parameters["properties"])
	}
	for _, name := range []string{"title", "plan", "document", "plan_id", "id", "execution_granularity", "continuation_policy", "continue_automatically"} {
		if _, ok := params[name].(map[string]any); !ok {
			t.Fatalf("%s property missing or wrong type: %T", name, params[name])
		}
	}
	if required, ok := definition.Parameters["required"]; ok {
		t.Fatalf("exit_plan_mode should not require markdown-only title/plan fields, got required=%#v", required)
	}
}

func mustFindDefinition(t *testing.T, name string) Definition {
	t.Helper()
	rt := NewRuntime(1)
	for _, candidate := range rt.Definitions() {
		if candidate.Name == name {
			return candidate
		}
	}
	t.Fatalf("%s definition not found", name)
	return Definition{}
}

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
