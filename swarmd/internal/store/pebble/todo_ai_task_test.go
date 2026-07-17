package pebblestore

import "testing"

func TestNormalizeWorkspaceTodoAITaskKeepsUserManagedSessionLink(t *testing.T) {
	item := normalizeWorkspaceTodoItem(WorkspaceTodoItem{
		ID: "todo-1", WorkspacePath: "/workspace", OwnerKind: WorkspaceTodoOwnerKindUser, Text: "fix it",
		AIState: WorkspaceTodoAIStateInProgress, AIMode: "PLAN", AIWorktree: true,
		AIRequest: " fix it ", ManagedSessionID: " session-1 ",
	})
	if item.OwnerKind != WorkspaceTodoOwnerKindUser || item.AIState != WorkspaceTodoAIStateInProgress {
		t.Fatalf("unexpected user AI task: %#v", item)
	}
	if item.AIMode != "plan" || item.ManagedSessionID != "session-1" || !item.AIWorktree {
		t.Fatalf("AI launch linkage was not normalized: %#v", item)
	}
}

func TestNormalizeWorkspaceTodoAITaskStripsAgentAuthority(t *testing.T) {
	item := normalizeWorkspaceTodoItem(WorkspaceTodoItem{
		ID: "todo-1", WorkspacePath: "/workspace", OwnerKind: WorkspaceTodoOwnerKindAgent, Text: "fix it",
		AIState: WorkspaceTodoAIStatePreparing, AIMode: "auto", AIWorktree: true,
		AIRequest: "fix it", ManagedSessionID: "session-1",
	})
	if item.AIState != "" || item.AIMode != "" || item.ManagedSessionID != "" || item.AIWorktree {
		t.Fatalf("agent todo retained user AI-task authority: %#v", item)
	}
}
