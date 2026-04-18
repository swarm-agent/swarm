package copilot

import (
	"context"
	"testing"

	sdk "github.com/github/copilot-sdk/go"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type stubToolInvoker struct {
	result provideriface.ToolExecutionResult
}

func (s stubToolInvoker) ExecuteTool(_ context.Context, _ provideriface.ToolInvocation) (provideriface.ToolExecutionResult, error) {
	return s.result, nil
}

func TestBuildToolWrappersSignalsRestartTurn(t *testing.T) {
	restarted := false
	tools, _, err := buildToolWrappers(nil, []provideriface.ToolDefinition{{
		Name:       "exit_plan_mode",
		Parameters: map[string]any{"type": "object"},
	}}, stubToolInvoker{result: provideriface.ToolExecutionResult{
		Output:       `{"tool":"exit_plan_mode","mode_changed":true,"target_mode":"auto"}`,
		TextForModel: `{"tool":"exit_plan_mode","mode_changed":true,"target_mode":"auto"}`,
		RestartTurn:  true,
	}}, func() { restarted = true })
	if err != nil {
		t.Fatalf("buildToolWrappers: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	_, err = tools[0].Handler(sdk.ToolInvocation{ToolCallID: "call_1", ToolName: tools[0].Name, Arguments: map[string]any{"title": "Plan", "plan": "# Plan"}})
	if err != nil {
		t.Fatalf("tool handler: %v", err)
	}
	if !restarted {
		t.Fatalf("expected restart turn callback")
	}
}
