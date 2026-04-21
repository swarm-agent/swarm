package todo

import (
	"path/filepath"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestListSummariesScopeAgentTodosPerSession(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "todo-service.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	sessionStore := pebblestore.NewSessionStore(store)
	sessionSvc := sessionruntime.NewService(sessionStore, eventLog)
	service := NewService(pebblestore.NewWorkspaceTodoStore(store), eventLog, nil, sessionSvc)
	workspace := "/workspace/demo"

	for _, sessionID := range []string{"session-a", "session-b"} {
		if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{
			ID:            sessionID,
			WorkspacePath: workspace,
			Title:         sessionID,
			Mode:          "auto",
		}); err != nil {
			t.Fatalf("create session %s: %v", sessionID, err)
		}
	}

	if _, _, _, err := service.Create(CreateInput{WorkspacePath: workspace, OwnerKind: pebblestore.WorkspaceTodoOwnerKindAgent, Text: "agent a1", SessionID: "session-a"}); err != nil {
		t.Fatalf("create agent a1: %v", err)
	}
	if _, _, _, err := service.Create(CreateInput{WorkspacePath: workspace, OwnerKind: pebblestore.WorkspaceTodoOwnerKindAgent, Text: "agent a2", SessionID: "session-a", InProgress: true}); err != nil {
		t.Fatalf("create agent a2: %v", err)
	}
	if _, _, _, err := service.Create(CreateInput{WorkspacePath: workspace, OwnerKind: pebblestore.WorkspaceTodoOwnerKindAgent, Text: "agent b1", SessionID: "session-b"}); err != nil {
		t.Fatalf("create agent b1: %v", err)
	}
	if _, _, _, err := service.Create(CreateInput{WorkspacePath: workspace, OwnerKind: pebblestore.WorkspaceTodoOwnerKindUser, Text: "user 1"}); err != nil {
		t.Fatalf("create user 1: %v", err)
	}

	items, summary, err := service.List(workspace, ListOptions{OwnerKind: pebblestore.WorkspaceTodoOwnerKindAgent, SessionID: "session-a"})
	if err != nil {
		t.Fatalf("list session-a agent todos: %v", err)
	}
	if got := len(items); got != 2 {
		t.Fatalf("session-a items = %d, want 2", got)
	}
	if summary.TaskCount != 2 || summary.OpenCount != 2 || summary.InProgressCount != 1 {
		t.Fatalf("session-a summary = %+v, want task=2 open=2 in_progress=1", summary)
	}
	if summary.Agent.TaskCount != 2 || summary.Agent.OpenCount != 2 || summary.Agent.InProgressCount != 1 {
		t.Fatalf("session-a agent summary = %+v, want task=2 open=2 in_progress=1", summary.Agent)
	}
	if summary.User.TaskCount != 0 || summary.User.OpenCount != 0 || summary.User.InProgressCount != 0 {
		t.Fatalf("session-a user summary = %+v, want zeroed", summary.User)
	}

	_, sessionAExists, err := sessionStore.GetSession("session-a")
	if err != nil {
		t.Fatalf("get session-a: %v", err)
	}
	if !sessionAExists {
		t.Fatal("session-a missing")
	}
	_, sessionBExists, err := sessionStore.GetSession("session-b")
	if err != nil {
		t.Fatalf("get session-b: %v", err)
	}
	if !sessionBExists {
		t.Fatal("session-b missing")
	}

	sessionA, _, err := sessionStore.GetSession("session-a")
	if err != nil {
		t.Fatalf("get session-a snapshot: %v", err)
	}
	sessionB, _, err := sessionStore.GetSession("session-b")
	if err != nil {
		t.Fatalf("get session-b snapshot: %v", err)
	}

	summaryA := metadataSummaryMap(t, sessionA.Metadata)
	if taskCount := metadataInt(summaryA, "task_count"); taskCount != 2 {
		t.Fatalf("session-a metadata task_count = %d, want 2", taskCount)
	}
	if openCount := metadataInt(summaryA, "open_count"); openCount != 2 {
		t.Fatalf("session-a metadata open_count = %d, want 2", openCount)
	}
	if inProgressCount := metadataInt(summaryA, "in_progress_count"); inProgressCount != 1 {
		t.Fatalf("session-a metadata in_progress_count = %d, want 1", inProgressCount)
	}
	if agentTaskCount := metadataInt(metadataObject(summaryA, "agent"), "task_count"); agentTaskCount != 2 {
		t.Fatalf("session-a metadata agent.task_count = %d, want 2", agentTaskCount)
	}

	summaryB := metadataSummaryMap(t, sessionB.Metadata)
	if taskCount := metadataInt(summaryB, "task_count"); taskCount != 1 {
		t.Fatalf("session-b metadata task_count = %d, want 1", taskCount)
	}
	if openCount := metadataInt(summaryB, "open_count"); openCount != 1 {
		t.Fatalf("session-b metadata open_count = %d, want 1", openCount)
	}
	if inProgressCount := metadataInt(summaryB, "in_progress_count"); inProgressCount != 0 {
		t.Fatalf("session-b metadata in_progress_count = %d, want 0", inProgressCount)
	}
}

func metadataSummaryMap(t *testing.T, metadata map[string]any) map[string]any {
	t.Helper()
	if metadata == nil {
		t.Fatal("metadata is nil")
	}
	value, ok := metadata[agentTodoSummaryMetadataKey]
	if !ok {
		t.Fatalf("metadata missing %q: %#v", agentTodoSummaryMetadataKey, metadata)
	}
	typed, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("metadata[%q] type = %T, want map[string]any", agentTodoSummaryMetadataKey, value)
	}
	return typed
}

func metadataObject(payload map[string]any, key string) map[string]any {
	value, ok := payload[key]
	if !ok || value == nil {
		return nil
	}
	typed, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return typed
}

func metadataInt(payload map[string]any, key string) int {
	if payload == nil {
		return 0
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}
