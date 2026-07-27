package run

import (
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func TestProviderToolConstructionStreamEventKeepsConstructionSeparateFromExecution(t *testing.T) {
	index := 2
	event := providerToolConstructionStreamEvent(3, provideriface.StreamEvent{
		Type:              provideriface.StreamEventToolCallArgumentsSnapshot,
		ToolCallID:        "call-build",
		ToolCallIndex:     &index,
		ToolName:          "read",
		ArgumentsSnapshot: `{"path":"file.txt"}`,
		Metadata:          map[string]any{"provider_item_id": "item-1"},
	})

	if event.Type != StreamEventProviderToolCallArgumentsSnapshot {
		t.Fatalf("type = %q, want provider construction snapshot", event.Type)
	}
	if event.Type == StreamEventToolStarted || event.Type == StreamEventToolDelta || event.Type == StreamEventToolCompleted {
		t.Fatalf("provider construction event reused execution event type %q", event.Type)
	}
	if event.Step != 3 || event.CallID != "call-build" || event.ToolCallID != "call-build" || event.ToolName != "read" {
		t.Fatalf("unexpected identity fields: %+v", event)
	}
	if event.ToolCallIndex == nil || *event.ToolCallIndex != 2 {
		t.Fatalf("tool_call_index = %#v, want 2", event.ToolCallIndex)
	}
	if event.ArgumentsSnapshot != `{"path":"file.txt"}` {
		t.Fatalf("arguments_snapshot = %q", event.ArgumentsSnapshot)
	}
	if event.Metadata["provider_item_id"] != "item-1" {
		t.Fatalf("metadata = %#v", event.Metadata)
	}
}

func TestProviderToolConstructionStreamEventUsesDeltaFallback(t *testing.T) {
	event := providerToolConstructionStreamEvent(1, provideriface.StreamEvent{
		Type:       provideriface.StreamEventToolCallArgumentsDelta,
		ToolName:   "bash",
		ToolCallID: "call-delta",
		Delta:      `{"command"`,
	})

	if event.Type != StreamEventProviderToolCallArgumentsDelta {
		t.Fatalf("type = %q", event.Type)
	}
	if event.ArgumentsDelta != `{"command"` {
		t.Fatalf("arguments_delta = %q", event.ArgumentsDelta)
	}
}
