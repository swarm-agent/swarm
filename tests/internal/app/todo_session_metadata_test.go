package app

import (
	"encoding/json"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestApplySessionMetadataUpdatedRefreshesChatTodoSummary(t *testing.T) {
	a := &App{
		home: ui.NewHomePage(model.EmptyHome()),
		homeModel: model.HomeModel{
			RecentSessions: []model.SessionSummary{{
				ID:       "session-1",
				Title:    "Session 1",
				Metadata: nil,
			}},
		},
		chat: ui.NewChatPage(ui.ChatPageOptions{
			SessionID:      "session-1",
			SessionTitle:   "Session 1",
			AuthConfigured: true,
			SessionMode:    "auto",
			ShowHeader:     true,
		}),
	}

	payload, err := json.Marshal(map[string]any{
		"session_id": "session-1",
		"metadata": map[string]any{
			"agent_todo_summary": map[string]any{
				"task_count":        2,
				"open_count":        2,
				"in_progress_count": 1,
				"agent": map[string]any{
					"task_count":        2,
					"open_count":        2,
					"in_progress_count": 1,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	changed := a.applySessionStreamEvent(client.StreamEventEnvelope{
		EventType: "session.metadata.updated",
		Payload:   payload,
	})
	if !changed {
		t.Fatal("applySessionStreamEvent() = false, want true")
	}

	if got := a.chat.Meta().AgentTodoTaskCount; got != 2 {
		t.Fatalf("chat task count = %d, want 2", got)
	}
	if got := a.chat.Meta().AgentTodoOpenCount; got != 2 {
		t.Fatalf("chat open count = %d, want 2", got)
	}
	if got := a.chat.Meta().AgentTodoInProgress; got != 1 {
		t.Fatalf("chat in-progress count = %d, want 1", got)
	}

	updated, ok := a.sessionSummaryByID("session-1")
	if !ok {
		t.Fatal("session summary missing after metadata update")
	}
	if updated.Metadata == nil {
		t.Fatal("updated metadata is nil")
	}
	text := renderPageText(t, a.chat)
	if !strings.Contains(text, "0/2 complete • 1 active") {
		t.Fatalf("chat header missing todo badge:\n%s", text)
	}
}
