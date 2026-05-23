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
		ToolArguments: `{"goal":"Inspect repo","description":"Inspect repo","prompt":"Map files and summarize findings. Include architecture, risks, and relevant filepaths.","launch_count":2,"allow_bash":true,"resolved_agent_name":"explorer","resolved_tools":{"preset":"read_only","runtime_mode":"auto","effective_execution_mode":"read","allowed_tools":["read","search"]},"launches":[{"launch_index":1,"requested_subagent_type":"explorer","resolved_agent_name":"explorer","assignment_label":"Backend","meta_prompt":"backend/core service architecture","subagent_provider":"anthropic","subagent_model":"claude-sonnet","resolved_tools":{"preset":"read_only","runtime_mode":"auto","effective_execution_mode":"read","allowed_tools":["read","search"],"disabled_tools":["write"],"launch_disabled_tools":["edit"],"bash_prefixes":["git status"]}},{"launch_index":2,"requested_subagent_type":"parallel","resolved_agent_name":"parallel","meta_prompt":"desktop permissions UI"}]}`,
		Status:        "pending",
	})

	lines := page.taskLaunchModalLines(page.pendingPerms[0], 72)
	text := renderLinesText(lines)
	for _, want := range []string{"Task", "Agent roles", "Meta", "Prompt", "11 words", "Map files and summarize findings.", "Press p to show the full prompt", "Backend", "backend/core service architecture", "desktop permissions UI", "anthropic/claude-sonnet", "router explorer", "preset read_only", "effective read", "allowed read, search", "tools: preset read_only", "launch disabled edit", "bash prefixes git status"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected task launch layout to contain %q, got:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"Readable prompt preview", "Permission:", "Requirement:", "Tool:", "bash yes"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("expected compact task launch layout without %q, got:\n%s", unwanted, text)
		}
	}
}

func TestTaskLaunchPermissionModalPromptToggleShowsFullPrompt(t *testing.T) {
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
		ToolArguments: `{"goal":"Inspect repo","description":"Inspect repo","prompt":"Map files and summarize findings. Include architecture, risks, and relevant filepaths.","launch_count":1,"resolved_agent_name":"explorer","launches":[{"launch_index":1,"requested_subagent_type":"explorer","resolved_agent_name":"explorer","meta_prompt":"backend/core service architecture"}]}`,
		Status:        "pending",
	})

	collapsed := renderLinesText(page.taskLaunchModalLines(page.pendingPerms[0], 72))
	if !strings.Contains(collapsed, "Press p to show the full prompt") {
		t.Fatalf("expected collapsed prompt hint, got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "Include architecture, risks, and relevant filepaths") {
		t.Fatalf("expected collapsed prompt to hide full prompt body, got:\n%s", collapsed)
	}

	if !page.handleTaskLaunchModalKey(tcell.NewEventKey(tcell.KeyRune, 'p', tcell.ModNone)) {
		t.Fatalf("expected p to toggle task launch prompt")
	}
	expanded := renderLinesText(page.taskLaunchModalLines(page.pendingPerms[0], 72))
	for _, want := range []string{"Press p to hide the full prompt", "Include architecture", "relevant filepaths"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expected expanded prompt to contain %q, got:\n%s", want, expanded)
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
