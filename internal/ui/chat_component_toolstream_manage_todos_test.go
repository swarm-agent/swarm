package ui

import (
	"strconv"
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

func TestRenderPlanManageToolUsesCollapsedPlanCardAndKeepsDocumentForModal(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	page.SetPlanExecutionState(ChatSessionPlan{ID: "plan_123", Title: "Implementation Plan", Document: map[string]any{"checkpoints": []any{map[string]any{"id": "cp-1"}, map[string]any{"id": "cp-2"}}}}, nil, "", "")
	message := chatMessageItem{
		Role:      "tool",
		ToolState: "done",
		Metadata: map[string]any{
			chatToolTimelineObjectMetadataKey:   true,
			chatToolTimelineToolNameMetadataKey: "plan_manage",
			chatToolTimelinePayloadMetadataKey:  `{"tool":"plan_manage","action":"save","status":"ok","plan":{"id":"plan_123","title":"Implementation Plan","update_summary":"Ready for review","document":{"title":"Implementation Plan","status":"approved","info":{"goal":"Ship a readable TUI plan"},"checkpoints":[{"id":"cp-1","title":"Inspect","status":"done","order":1},{"id":"cp-2","title":"Render","status":"pending","order":2}]}}}`,
		},
	}

	lines := page.renderToolMessageLines(message, 72)
	var rendered []string
	for _, line := range lines {
		rendered = append(rendered, chatRenderLineText(line))
	}
	joined := strings.Join(rendered, "\n")
	for _, want := range []string{"┌", "PLAN  ·  save  ·  2 checkpoints  ·  Approved", "Implementation Plan", "Ship a readable TUI plan", "/plan  Open full plan", "└"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("collapsed plan card missing %q:\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{"Inspect", "Render", "acceptance_criteria", "plan_manage"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("collapsed plan card exposed document detail %q:\n%s", unwanted, joined)
		}
	}
	if !page.OpenCurrentPlanModal(page.planExecutionPlan) || !page.planEditorVisible {
		t.Fatal("full structured plan should remain available through the plan modal")
	}
}

func TestRenderRequestNewPlanPermissionPayloadUsesPlanCardAndRetainsModalDocument(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	message := chatMessageItem{
		Role:      "tool",
		ToolState: "done",
		Metadata: map[string]any{
			chatToolTimelineObjectMetadataKey:   true,
			chatToolTimelineToolNameMetadataKey: "tool",
			chatToolTimelinePayloadMetadataKey: `{
				"path_id":"tool.plan-new-request.v1",
				"document_operation":"request_new_plan",
				"plan_id":"plan_perm_123",
				"proposal_revision":1,
				"title":"Two-step completion plan",
				"update_kind":"request_new_plan",
				"update_type":"new_plan",
				"document":{
					"id":"plan_perm_123",
					"title":"Two-step completion plan",
					"info":{"goal":"Finish the target work end-to-end.","scope":"Identify and implement the work."},
					"checkpoints":[
						{"id":"cp-1","title":"Verify the work","status":"pending","order":1,"tasks":["Inspect the target"],"acceptance_criteria":["Scope is explicit"]},
						{"id":"cp-2","title":"Finish the work","status":"pending","order":2,"tasks":["Implement the target"],"acceptance_criteria":["Work is complete"]}
					]
				}
			}`,
		},
	}

	lines := page.renderToolMessageLines(message, 72)
	var rendered []string
	for _, line := range lines {
		rendered = append(rendered, chatRenderLineText(line))
	}
	joined := strings.Join(rendered, "\n")
	for _, want := range []string{"PLAN  ·  request new plan  ·  2 checkpoints", "Two-step completion plan", "Finish the target work end-to-end.", "/plan  Open full plan"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("request_new_plan card missing %q:\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{"path_id", "proposal_revision", "acceptance_criteria", "Verify the work"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("request_new_plan card exposed raw payload detail %q:\n%s", unwanted, joined)
		}
	}
	if page.planExecutionPlan.ID != "plan_perm_123" {
		t.Fatalf("retained plan id = %q", page.planExecutionPlan.ID)
	}
	if !page.OpenCurrentPlanModal(page.planExecutionPlan) || !page.planEditorVisible {
		t.Fatal("request_new_plan document should open in the full-plan modal")
	}
	detail := planEditorDisplayText(page.planEditorPlan)
	for _, want := range []string{"Goal: Finish the target work end-to-end.", "1. Verify the work [pending]", "Acceptance:", "- Work is complete"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("retained modal document missing %q:\n%s", want, detail)
		}
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

func TestRenderTaskToolTableLines_StackedShowsCriticalSubagentFields(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	message := chatMessageItem{
		Role:      "tool",
		ToolState: "running",
		Metadata: map[string]any{
			chatToolTimelineObjectMetadataKey:    true,
			chatToolTimelinePayloadMetadataKey:   `{"tool":"task","status":"running","launches":[{"launch_index":1,"subagent":"explorer","assignment_label":"Backend architecture mapper","subagent_provider":"anthropic","subagent_model":"claude-sonnet","status":"running","current_tool":"search","current_tool_ms":1500}]}`,
			chatToolTimelineStartedAtMetadataKey: int64(100),
		},
	}
	payload, ok := toolTimelinePayload(message)
	if !ok {
		t.Fatalf("expected managed tool payload")
	}
	lines := page.renderTaskToolTableLines(message, payload, 120)
	joinedParts := make([]string, 0, len(lines))
	for _, line := range lines {
		joinedParts = append(joinedParts, line.Text)
	}
	joined := strings.Join(joinedParts, "\n")
	for _, want := range []string{"Subagents · 1 running", "Backend architecture mapper", "@explorer", "anthropic/claude-sonnet", "search", "1.5s"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("stacked task view missing %q:\n%s", want, joined)
		}
	}
}

func TestRenderTaskToolTableLines_SwarmCompactOverTenPreservesEveryLaunch(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	payload := `{"tool":"task","status":"running","launches":[`
	for i := 1; i <= 11; i++ {
		if i > 1 {
			payload += `,`
		}
		payload += `{"launch_index":` + strconv.Itoa(i) + `,"subagent":"parallel","assignment_label":"Full title for launch ` + strconv.Itoa(i) + `","subagent_provider":"openai","subagent_model":"gpt-5-mini","status":"running","current_tool":"read","elapsed_ms":` + strconv.Itoa(i*1000) + `}`
	}
	payload += `]}`
	message := chatMessageItem{
		Role:      "tool",
		ToolState: "running",
		Metadata: map[string]any{
			chatToolTimelineObjectMetadataKey:  true,
			chatToolTimelinePayloadMetadataKey: payload,
		},
	}
	parsed, ok := toolTimelinePayload(message)
	if !ok {
		t.Fatalf("expected managed tool payload")
	}
	lines := page.renderTaskToolTableLines(message, parsed, 140)
	joinedParts := make([]string, 0, len(lines))
	for _, line := range lines {
		joinedParts = append(joinedParts, line.Text)
	}
	joined := strings.Join(joinedParts, "\n")
	if !strings.Contains(joined, "Swarm mode · 11 subagents") {
		t.Fatalf("expected swarm compact header:\n%s", joined)
	}
	for _, want := range []string{"Full title for launch 1", "Full title for launch 11", "openai/gpt-5-mini", "read", "11.0s"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("swarm compact view missing %q:\n%s", want, joined)
		}
	}
}
