package tool

import (
	"context"
	"strings"
	"testing"
)

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
	if !containsAll(planDefinition.Description, "agent execution progress", "canonical agent checklist/progress surface", "do not use manage_todos") {
		t.Fatalf("plan_manage description %q does not advertise canonical agent progress tracking", planDefinition.Description)
	}
	planParams, ok := planDefinition.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("plan_manage properties type = %T", planDefinition.Parameters["properties"])
	}
	planActionDescription, _ := planParams["action"].(map[string]any)["description"].(string)
	if !containsAll(planActionDescription, "update_checkpoint", "agent progress/checklist", "mark_failed") {
		t.Fatalf("plan_manage action description %q does not point progress updates at update_checkpoint", planActionDescription)
	}
	checkpointDescription, _ := planParams["checkpoint"].(map[string]any)["description"].(string)
	if !containsAll(checkpointDescription, "tasks", "notes", "agent progress/checklist") {
		t.Fatalf("plan_manage checkpoint description %q does not advertise checkpoint progress fields", checkpointDescription)
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
