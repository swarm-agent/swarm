package anthropic

import (
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func TestAnthropicStreamStateMapsParallelToolUseConstructionLifecycle(t *testing.T) {
	state := newAnthropicStreamState()
	state.SetContext("anthropic", "claude-test")
	var events []provideriface.StreamEvent
	emit := func(event provideriface.StreamEvent) { events = append(events, event) }

	for _, raw := range []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_read","name":"read","input":{}}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_edit","name":"edit","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"note.txt\"}"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"AGENTS.md\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_stop","index":0}`,
	} {
		state.HandleEvent(mustAnthropicStreamEvent(t, raw), emit)
	}

	wantTypes := []provideriface.StreamEventType{
		provideriface.StreamEventToolCallStarted,
		provideriface.StreamEventToolCallStarted,
		provideriface.StreamEventToolCallArgumentsDelta,
		provideriface.StreamEventToolCallArgumentsDelta,
		provideriface.StreamEventToolCallArgumentsDelta,
		provideriface.StreamEventToolCallCompleted,
		provideriface.StreamEventToolCallCompleted,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("events len = %d, want %d: %#v", len(events), len(wantTypes), events)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Fatalf("event[%d].Type = %q, want %q: %#v", i, events[i].Type, want, events)
		}
		if events[i].ProviderID != "anthropic" || events[i].Model != "claude-test" {
			t.Fatalf("event[%d] context = %#v", i, events[i])
		}
		if events[i].ToolCallIndex == nil {
			t.Fatalf("event[%d] missing content-block index: %#v", i, events[i])
		}
		if events[i].Metadata["provider_content_block_index"] == nil || events[i].Metadata["provider_event_type"] == nil {
			t.Fatalf("event[%d] native metadata = %#v", i, events[i].Metadata)
		}
	}

	assertAnthropicConstructionIdentity(t, events[0], "toolu_read", "read", 0)
	assertAnthropicConstructionIdentity(t, events[1], "toolu_edit", "edit", 1)
	assertAnthropicConstructionIdentity(t, events[5], "toolu_edit", "edit", 1)
	assertAnthropicConstructionIdentity(t, events[6], "toolu_read", "read", 0)
	if events[2].ArgumentsDelta != `{"path":` || events[3].ArgumentsDelta != `{"path":"note.txt"}` || events[4].ArgumentsDelta != `"AGENTS.md"}` {
		t.Fatalf("argument deltas = %q, %q, %q", events[2].ArgumentsDelta, events[3].ArgumentsDelta, events[4].ArgumentsDelta)
	}
	if events[5].Arguments != `{"path":"note.txt"}` || events[6].Arguments != `{"path":"AGENTS.md"}` {
		t.Fatalf("completed arguments = %q, %q", events[5].Arguments, events[6].Arguments)
	}

	startedAt := map[int]int64{}
	lastRecorded := map[int]int64{}
	for i, event := range events {
		index := *event.ToolCallIndex
		if event.Type == provideriface.StreamEventToolCallStarted {
			startedAt[index] = event.StartedAtUnixMs
		}
		if event.StartedAtUnixMs == 0 || event.StartedAtUnixMs != startedAt[index] {
			t.Fatalf("event[%d] unstable start time = %#v", i, event)
		}
		if event.RecordedAtUnixMs <= lastRecorded[index] {
			t.Fatalf("event[%d] non-monotonic record time = %#v", i, event)
		}
		lastRecorded[index] = event.RecordedAtUnixMs
	}
	if events[0].Status != "started" || events[2].Status != "building" || events[5].Status != "completed" {
		t.Fatalf("construction statuses = %#v", events)
	}
}

func TestAnthropicStreamStateCompletesEmptyToolInputOnce(t *testing.T) {
	state := newAnthropicStreamState()
	var events []provideriface.StreamEvent
	emit := func(event provideriface.StreamEvent) { events = append(events, event) }

	for _, raw := range []string{
		`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_empty","name":"list","input":{}}}`,
		`{"type":"content_block_stop","index":3}`,
		`{"type":"content_block_stop","index":3}`,
		`{"type":"message_stop"}`,
	} {
		state.HandleEvent(mustAnthropicStreamEvent(t, raw), emit)
	}

	if len(events) != 2 || events[0].Type != provideriface.StreamEventToolCallStarted || events[1].Type != provideriface.StreamEventToolCallCompleted {
		t.Fatalf("events = %#v", events)
	}
	assertAnthropicConstructionIdentity(t, events[0], "toolu_empty", "list", 3)
	assertAnthropicConstructionIdentity(t, events[1], "toolu_empty", "list", 3)
	if events[1].Arguments != `{}` {
		t.Fatalf("completed empty arguments = %q, want {}", events[1].Arguments)
	}
}

func assertAnthropicConstructionIdentity(t *testing.T, event provideriface.StreamEvent, callID, toolName string, index int) {
	t.Helper()
	if event.ToolCallID != callID || event.ToolName != toolName || event.ToolCallIndex == nil || *event.ToolCallIndex != index {
		t.Fatalf("construction identity = %#v, want id=%q name=%q index=%d", event, callID, toolName, index)
	}
}
