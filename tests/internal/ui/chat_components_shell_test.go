package ui

import "testing"

func TestFormatAgentTodoBadge_InProgress(t *testing.T) {
	got := formatAgentTodoBadge(ChatSessionMeta{
		AgentTodoTaskCount:  6,
		AgentTodoOpenCount:  2,
		AgentTodoInProgress: 1,
	})
	want := "4/6 complete • 1 active"
	if got != want {
		t.Fatalf("formatAgentTodoBadge() = %q, want %q", got, want)
	}
}

func TestFormatAgentTodoBadge_Complete(t *testing.T) {
	got := formatAgentTodoBadge(ChatSessionMeta{
		AgentTodoTaskCount:  6,
		AgentTodoOpenCount:  0,
		AgentTodoInProgress: 0,
	})
	want := "Complete · 6/6"
	if got != want {
		t.Fatalf("formatAgentTodoBadge() = %q, want %q", got, want)
	}
}

func TestFormatAgentTodoBadge_EmptyWhenNoTasks(t *testing.T) {
	got := formatAgentTodoBadge(ChatSessionMeta{})
	if got != "" {
		t.Fatalf("formatAgentTodoBadge() = %q, want empty", got)
	}
}
