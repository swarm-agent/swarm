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

func TestFormatUnifiedToolEntry_PlanManageShowsPlanLabelAndAction(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "plan_manage",
		Output: `{
			"tool":"plan_manage",
			"action":"save",
			"status":"ok",
			"plan":{
				"id":"plan_123",
				"title":"Implementation Plan",
				"plan":"# Plan\n1. Patch tool stream\n2. Test",
				"update_summary":"polish tool stream"
			}
		}`,
		State: "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if !strings.Contains(rendered, "plan save") {
		t.Fatalf("rendered entry should use plan label/action: %q", rendered)
	}
	if strings.Contains(rendered, "plan_manage") {
		t.Fatalf("rendered entry should not expose raw plan_manage name: %q", rendered)
	}
	if !strings.Contains(rendered, "Implementation Plan") || !strings.Contains(rendered, "update: polish tool stream") {
		t.Fatalf("rendered entry missing plan details: %q", rendered)
	}
}

func TestFormatUnifiedToolEntry_ExitPlanModeUsesFlatPlanStyling(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "exit_plan_mode",
		Output: `{
			"tool":"exit_plan_mode",
			"status":"approved",
			"title":"Implementation Plan",
			"plan_id":"plan_123",
			"target_mode":"auto"
		}`,
		State: "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if !strings.Contains(rendered, "plan approved · Implementation Plan") {
		t.Fatalf("rendered entry should use flat plan label/action: %q", rendered)
	}
	if strings.Contains(rendered, "exit_plan_mode") || strings.Contains(rendered, "exit-plan-mode") {
		t.Fatalf("rendered entry should not expose raw exit_plan_mode name: %q", rendered)
	}
	if !strings.Contains(rendered, "plan: plan_123") || !strings.Contains(rendered, "next mode: auto") {
		t.Fatalf("rendered entry missing flat exit plan details: %q", rendered)
	}
}
