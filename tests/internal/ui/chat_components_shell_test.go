package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

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

func TestChatHeaderShowsBranchImmediatelyBeforeTimer(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		SessionTitle:   "Fix worktrees",
		ShowHeader:     true,
		AuthConfigured: true,
		Meta:           ChatSessionMeta{Branch: "agent/fix-worktrees", WorktreeEnabled: true},
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(100, 5)

	p.drawHeader(screen, Rect{X: 0, Y: 0, W: 100, H: 1})
	text := dumpScreenText(screen, 100, 1)
	if !strings.Contains(text, "branch agent/fix-worktrees  ·  idle") {
		t.Fatalf("header = %q, want branch immediately before timer/status", text)
	}
	if strings.Contains(text, "wt on") {
		t.Fatalf("header = %q, obsolete worktree mode should not render", text)
	}
}
