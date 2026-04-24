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
	if !strings.Contains(text, "PgUp/PgDn") {
		t.Fatalf("expected task launch modal to show PgUp/PgDn scroll hint, got:\n%s", text)
	}
}

func TestTaskLaunchPermissionModalUsesTaskRolesMetaLayout(t *testing.T) {
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
		ToolArguments: `{"goal":"Inspect repo","description":"Inspect repo","prompt":"Map files and summarize findings. Include architecture, risks, and relevant filepaths.","launch_count":2,"resolved_agent_name":"explorer","launches":[{"launch_index":1,"requested_subagent_type":"explorer","resolved_agent_name":"explorer","meta_prompt":"backend/core service architecture"},{"launch_index":2,"requested_subagent_type":"parallel","resolved_agent_name":"parallel","meta_prompt":"desktop permissions UI"}]}`,
		Status:        "pending",
	})

	lines := page.taskLaunchModalLines(page.pendingPerms[0], 72)
	text := renderLinesText(lines)
	for _, want := range []string{"Task", "Agent roles", "Meta", "backend/core service architecture", "desktop permissions UI"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected task launch layout to contain %q, got:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"Full prompt", "Readable prompt preview", "Permission:", "Requirement:", "Tool:"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("expected compact task launch layout without %q, got:\n%s", unwanted, text)
		}
	}
}

func renderLinesText(lines []chatRenderLine) string {
	var out strings.Builder
	for _, line := range lines {
		out.WriteString(chatRenderLineText(line))
		out.WriteByte('\n')
	}
	return out.String()
}
