package ui

import (
	"strings"
	"testing"
)

func TestStructuredPlanDocumentTextFromValueRendersInfoAndCheckpoints(t *testing.T) {
	document := map[string]any{
		"id":     "plan_123",
		"title":  "Structured Plan",
		"status": "approved",
		"info": map[string]any{
			"goal":                "Ship structured rendering",
			"decisions":           []any{"document is canonical"},
			"success_criteria":    []any{"criteria survives"},
			"validation_strategy": "targeted tests",
		},
		"checkpoints": []any{
			map[string]any{"id": "cp-2", "title": "UI", "status": "pending", "objective": "render checkpoints", "tasks": []any{"show info", "show checkpoint"}, "order": float64(2)},
			map[string]any{"id": "cp-1", "title": "Model", "status": "done", "order": float64(1)},
		},
		"active_checkpoint_id": "cp-2",
	}

	got := StructuredPlanDocumentTextFromValue(document)
	for _, want := range []string{
		"Structured plan: Structured Plan",
		"Goal: Ship structured rendering",
		"Decisions:",
		"- document is canonical",
		"Success criteria:",
		"- criteria survives",
		"Validation strategy: targeted tests",
		"Active checkpoint: cp-2",
		"1. Model [done]",
		"2. UI [pending]",
		"   Tasks:",
		"   - show checkpoint",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("structured plan text missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "1. Model") > strings.Index(got, "2. UI") {
		t.Fatalf("checkpoints not rendered by order:\n%s", got)
	}
}

func TestExitPlanPermissionPayloadIncludesStructuredDocumentAndApprovedArgs(t *testing.T) {
	record := ChatPermissionRecord{
		ToolName:      "exit_plan_mode",
		ToolArguments: `{"title":"Exit Structured Plan","plan_id":"plan_exit","plan":"# fallback","document":{"id":"plan_exit","title":"Exit Structured Plan","info":{"goal":"Approve structured exit"},"checkpoints":[{"id":"cp-1","title":"Exit","status":"pending","order":1}]},"approved_arguments":{"plan_id":"plan_exit","document":{"id":"plan_exit","title":"Exit Structured Plan"}}}`,
	}

	title, body, planID, documentText, approvedArguments := exitPlanPermissionPayload(record)
	if title != "Exit Structured Plan" || planID != "plan_exit" || body != "# fallback" {
		t.Fatalf("identity/body = %q/%q/%q", title, planID, body)
	}
	if !strings.Contains(documentText, "Goal: Approve structured exit") || !strings.Contains(documentText, "1. Exit [pending]") {
		t.Fatalf("documentText = %q", documentText)
	}
	if !strings.Contains(approvedArguments, "\"document\"") || !strings.Contains(approvedArguments, "plan_exit") {
		t.Fatalf("approvedArguments = %q", approvedArguments)
	}
}
