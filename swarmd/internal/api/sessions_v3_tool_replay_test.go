package api

import (
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func TestSessionsV3ProviderToolRecordInputItemsBoundsReplayedOutput(t *testing.T) {
	rawOutput := strings.Repeat("A", 120*1024)
	items := sessionsV3ProviderToolRecordInputItems(sessionV3ProviderToolResultRecord{
		ToolName:  "bash",
		CallID:    "call-large",
		Arguments: `{"command":"generate large log"}`,
		Output:    rawOutput,
	})
	if len(items) != 2 {
		t.Fatalf("item count = %d, want 2", len(items))
	}
	output := strings.TrimSpace(sessionsV3MapString(items[1], "output"))
	if output == "" {
		t.Fatal("replayed tool output is empty")
	}
	if output == rawOutput || len(output) >= len(rawOutput) {
		t.Fatalf("replayed tool output was not bounded: got %d bytes, raw %d bytes", len(output), len(rawOutput))
	}
	if !strings.Contains(output, `"truncated_for_model":true`) {
		t.Fatalf("replayed output missing truncation marker: %s", output)
	}
	if !strings.Contains(output, `"original_bytes":122880`) {
		t.Fatalf("replayed output missing original byte count: %s", output)
	}
}

func TestSessionsV3ProviderToolResultInputItemsBoundsFallbackOutput(t *testing.T) {
	rawOutput := strings.Repeat("B", 120*1024)
	items := sessionsV3ProviderToolResultInputItems(
		[]provideriface.FunctionCall{{CallID: "call-large", Name: "bash", Arguments: `{"command":"generate large log"}`}},
		[]provideriface.ToolExecutionResult{{CallID: "call-large", Name: "bash", Output: rawOutput}},
	)
	if len(items) != 2 {
		t.Fatalf("item count = %d, want 2", len(items))
	}
	output := strings.TrimSpace(sessionsV3MapString(items[1], "output"))
	if output == rawOutput || len(output) >= len(rawOutput) {
		t.Fatalf("fallback tool output was not bounded: got %d bytes, raw %d bytes", len(output), len(rawOutput))
	}
	if !strings.Contains(output, `"truncated_for_model":true`) {
		t.Fatalf("fallback output missing truncation marker: %s", output)
	}
}
