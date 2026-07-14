package run

import (
	"encoding/json"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestManageSessionsArchivePermissionHydratesFactsAndPreservesArguments(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	first, _, err := svc.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		AccountScopeID: "test-account",
		Title:          "Review session search",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "Swarm",
		Mode:           sessionruntime.ModeAuto,
		Preference:     &pebblestore.ModelPreference{Provider: "codex", Model: "test-model", Thinking: "medium"},
	})
	if err != nil {
		t.Fatalf("create first session: %v", err)
	}
	second, _, err := svc.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		AccountScopeID: "test-account",
		Title:          "Fix archive cards",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "Desktop",
		Mode:           sessionruntime.ModeAuto,
		Preference:     &pebblestore.ModelPreference{Provider: "codex", Model: "test-model", Thinking: "medium"},
	})
	if err != nil {
		t.Fatalf("create second session: %v", err)
	}

	original := map[string]any{
		"action":      "archive",
		"session_ids": []any{first.ID, second.ID},
		"expected_updated_at_by_id": map[string]any{
			first.ID:  first.UpdatedAt,
			second.ID: second.UpdatedAt,
		},
	}
	formatted, err := svc.permissionArgumentsForCall(parentSessionID, sessionruntime.ModeAuto, tool.Call{
		Name:      "manage-sessions",
		Arguments: mustJSON(t, original),
	})
	if err != nil {
		t.Fatalf("format permission arguments: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(formatted), &payload); err != nil {
		t.Fatalf("decode permission payload: %v", err)
	}
	facts, ok := payload["sessions"].([]any)
	if !ok || len(facts) != 2 {
		t.Fatalf("sessions = %#v", payload["sessions"])
	}
	firstFact, _ := facts[0].(map[string]any)
	if firstFact["title"] != first.Title || firstFact["workspace_name"] != first.WorkspaceName || firstFact["state"] != "idle" {
		t.Fatalf("first session fact = %#v", firstFact)
	}
	if _, exposed := firstFact["id"]; exposed {
		t.Fatalf("session facts should not expose opaque ids as primary details: %#v", firstFact)
	}

	approved, ok := payload["approved_arguments"].(map[string]any)
	if !ok {
		t.Fatalf("approved_arguments = %#v", payload["approved_arguments"])
	}
	approvedIDs, _ := approved["session_ids"].([]any)
	if len(approvedIDs) != 2 || approvedIDs[0] != first.ID || approvedIDs[1] != second.ID {
		t.Fatalf("approved session_ids changed: %#v", approvedIDs)
	}
	expected, _ := approved["expected_updated_at_by_id"].(map[string]any)
	if expected[first.ID] != float64(first.UpdatedAt) || expected[second.ID] != float64(second.UpdatedAt) {
		t.Fatalf("approved expected_updated_at_by_id changed: %#v", expected)
	}
}
