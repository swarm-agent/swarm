package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestTaskLaunchPermissionModalDrawsOnNarrowScreen(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	page.upsertPendingPermission(ChatPermissionRecord{
		ID:            "perm_task_1",
		SessionID:     "session-1",
		ToolName:      "task",
		Requirement:   "task_launch",
		ToolArguments: `{"goal":"Inspect repo","description":"Inspect repo","prompt":"Map files and summarize findings.","launch_count":1,"resolved_agent_name":"explorer","launches":[{"launch_index":1,"requested_subagent_type":"explorer","resolved_agent_name":"explorer","meta_prompt":"map repository structure"}]}`,
		Status:        "pending",
	})

	if !page.taskLaunchModalActive() {
		t.Fatalf("expected task launch modal to be visible")
	}

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	width, height := 44, 14
	screen.SetSize(width, height)
	page.Draw(screen)

	text := dumpScreenText(screen, width, height)
	if !strings.Contains(text, "Review Task Launch") {
		t.Fatalf("expected task launch modal header on narrow screen, got:\n%s", text)
	}
	if !strings.Contains(text, "Enter approve") && !strings.Contains(text, "Enter Approve") {
		t.Fatalf("expected task launch modal approval hint on narrow screen, got:\n%s", text)
	}
}
