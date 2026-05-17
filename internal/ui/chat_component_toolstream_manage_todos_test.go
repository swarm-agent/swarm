package ui

import (
	"strings"
	"testing"
)

func TestFormatUnifiedToolEntry_ManageTodosAgentListPrioritizesActiveItems(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "manage_todos",
		Output: `{
			"tool":"manage_todos",
			"action":"list",
			"owner_kind":"agent",
			"session_id":"session-1",
			"items":[
				{"id":"todo_done_1","text":"Completed one","done":true,"session_id":"session-1"},
				{"id":"todo_done_2","text":"Completed two","done":true,"session_id":"session-1"},
				{"id":"todo_done_3","text":"Completed three","done":true,"session_id":"session-1"},
				{"id":"todo_active","text":"Actually active item","done":false,"in_progress":true,"session_id":"session-1"},
				{"id":"todo_open","text":"Open next item","done":false,"session_id":"session-1"}
			]
		}`,
		State: "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if !strings.Contains(rendered, "> [ ] Actually active item") {
		t.Fatalf("rendered entry missing active agent todo: %q", rendered)
	}
	if !strings.Contains(rendered, "[ ] Open next item") {
		t.Fatalf("rendered entry missing open agent todo: %q", rendered)
	}
	if strings.Contains(rendered, "Completed one") || strings.Contains(rendered, "Completed two") || strings.Contains(rendered, "Completed three") {
		t.Fatalf("rendered entry should not let completed todos crowd out active todos: %q", rendered)
	}
}
