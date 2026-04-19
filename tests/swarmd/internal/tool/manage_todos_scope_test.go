package tool

import (
	"context"
	"path/filepath"
	"testing"

	sessionruntime "swarm/swarmd/internal/session"
	pebblestore "swarm/swarmd/internal/store/pebble"
	todoruntime "swarm/swarmd/internal/todo"
)

func TestManageTodosListAllowsWorkspaceUserTodosOutsideSession(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-todos.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	todoSvc := todoruntime.NewService(pebblestore.NewWorkspaceTodoStore(store), eventLog, nil, sessionSvc)
	workspace := t.TempDir()

	if _, _, _, err := todoSvc.Create(todoruntime.CreateInput{
		WorkspacePath: workspace,
		OwnerKind:     pebblestore.WorkspaceTodoOwnerKindUser,
		Text:          "workspace user todo",
	}); err != nil {
		t.Fatalf("create user todo: %v", err)
	}
	if _, _, _, err := todoSvc.Create(todoruntime.CreateInput{
		WorkspacePath: workspace,
		OwnerKind:     pebblestore.WorkspaceTodoOwnerKindAgent,
		Text:          "session agent todo",
		SessionID:     "session-1",
	}); err != nil {
		t.Fatalf("create agent todo: %v", err)
	}

	rt := NewRuntime(2)
	rt.SetManageTodoService(todoSvc)

	output, err := rt.ExecuteForWorkspaceScopeWithRuntime(
		context.Background(),
		WorkspaceScope{PrimaryPath: workspace, Roots: []string{workspace}, SessionID: "session-1"},
		Call{
			CallID:    "manage-todos-user-list",
			Name:      "manage_todos",
			Arguments: mustArgsJSON(t, map[string]any{"action": "list", "workspace_path": workspace, "owner_kind": "user"}),
		},
	)
	if err != nil {
		t.Fatalf("execute manage_todos list: %v", err)
	}

	decoded := decodeResultJSON(t, output)
	items := mapArray(t, decoded, "items")
	if len(items) != 1 {
		t.Fatalf("user todo list length = %d, want 1; payload=%v", len(items), decoded)
	}
	summary, ok := decoded["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary type = %T, want map[string]any", decoded["summary"])
	}
	if got := mapIntValue(summary, "task_count"); got != 1 {
		t.Fatalf("summary task_count = %d, want 1", got)
	}
	if got := mapIntValue(summary, "open_count"); got != 1 {
		t.Fatalf("summary open_count = %d, want 1", got)
	}
}
