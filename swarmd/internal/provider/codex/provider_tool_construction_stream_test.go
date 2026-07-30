package codex

import (
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func TestParseEventStreamEmitsFunctionCallConstructionLifecycle(t *testing.T) {
	stream := "event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"read\",\"arguments\":\"\"}}\n\n" +
		"event: response.function_call_arguments.delta\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"item_id\":\"fc_1\",\"delta\":\"{\\\"path\\\"\"}\n\n" +
		"event: response.function_call_arguments.delta\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"item_id\":\"fc_1\",\"delta\":\":\\\"file.txt\\\"}\"}\n\n" +
		"event: response.function_call_arguments.done\n" +
		"data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":0,\"item_id\":\"fc_1\",\"arguments\":\"{\\\"path\\\":\\\"file.txt\\\"}\"}\n\n" +
		"event: response.output_item.done\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"file.txt\\\"}\"}}\n\n" +
		completedResponseEvent()

	events := parseConstructionEvents(t, stream)
	wantTypes := []StreamEventType{StreamEventToolCallStarted, StreamEventToolCallArgumentsDelta, StreamEventToolCallArgumentsDelta, StreamEventToolCallCompleted}
	assertConstructionTypes(t, events, wantTypes)
	for i := range events {
		if events[i].ToolCallID != "call_1" || events[i].ToolName != "read" {
			t.Fatalf("event[%d] identity = %#v", i, events[i])
		}
		if events[i].ToolCallIndex == nil || *events[i].ToolCallIndex != 0 {
			t.Fatalf("event[%d].ToolCallIndex = %#v, want 0", i, events[i].ToolCallIndex)
		}
	}
	if events[1].ArgumentsDelta != `{"path"` || events[2].ArgumentsDelta != `:"file.txt"}` {
		t.Fatalf("unexpected deltas: %#v %#v", events[1].ArgumentsDelta, events[2].ArgumentsDelta)
	}
	if events[3].Arguments != `{"path":"file.txt"}` {
		t.Fatalf("completed arguments = %q", events[3].Arguments)
	}
}

func TestCodexProviderToolConstructionCallbackNormalizesContextAndStableTiming(t *testing.T) {
	index := 1
	var events []provideriface.StreamEvent
	callback := ToProviderStreamEventCallbackWithContext(func(event provideriface.StreamEvent) { events = append(events, event) }, "codex", "gpt-test")
	callback(StreamEvent{Type: StreamEventToolCallStarted, ToolCallID: "call-context", ToolCallIndex: &index, ToolName: "edit"})
	callback(StreamEvent{Type: StreamEventToolCallCompleted, ToolCallID: "call-context", ToolCallIndex: &index, ToolName: "edit", Arguments: `{}`})
	if len(events) != 2 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].ProviderID != "codex" || events[0].Model != "gpt-test" || events[0].Status != "started" {
		t.Fatalf("start context = %#v", events[0])
	}
	if events[1].Status != "completed" || events[1].StartedAtUnixMs != events[0].StartedAtUnixMs || events[1].RecordedAtUnixMs < events[0].RecordedAtUnixMs {
		t.Fatalf("completion timing = %#v start=%#v", events[1], events[0])
	}
}

func TestParseEventStreamRepairsArgumentDeltaFirstIdentityBeforeEmission(t *testing.T) {
	stream := "event: response.function_call_arguments.delta\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":2,\"item_id\":\"fc_pending\",\"delta\":\"{\\\"path\\\":\"}\n\n" +
		"event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"output_index\":2,\"item\":{\"type\":\"function_call\",\"id\":\"fc_pending\",\"call_id\":\"call_stable\",\"name\":\"read\",\"arguments\":\"\"}}\n\n" +
		"event: response.function_call_arguments.done\n" +
		"data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":2,\"item_id\":\"fc_pending\",\"arguments\":\"{\\\"path\\\":\"}\n\n" +
		completedResponseEvent()

	events := parseConstructionEvents(t, stream)
	assertConstructionTypes(t, events, []StreamEventType{StreamEventToolCallStarted, StreamEventToolCallArgumentsDelta, StreamEventToolCallCompleted})
	for i := range events {
		if events[i].ToolCallID != "call_stable" || events[i].ToolName != "read" {
			t.Fatalf("event[%d] identity not repaired: %#v", i, events[i])
		}
	}
	if events[1].Metadata["provider_item_id"] != "fc_pending" {
		t.Fatalf("delta metadata = %#v", events[1].Metadata)
	}
}

func TestParseEventStreamRepairsTerminalFirstAndDeduplicatesCompletion(t *testing.T) {
	stream := "event: response.function_call_arguments.done\n" +
		"data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":3,\"item_id\":\"fc_terminal\",\"arguments\":\"{\\\"command\\\":\\\"pwd\\\"}\"}\n\n" +
		"event: response.output_item.done\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":3,\"item\":{\"type\":\"function_call\",\"id\":\"fc_terminal\",\"call_id\":\"call_terminal\",\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"pwd\\\"}\"}}\n\n" +
		"event: response.output_item.done\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":3,\"item\":{\"type\":\"function_call\",\"id\":\"fc_terminal\",\"call_id\":\"call_terminal\",\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"pwd\\\"}\"}}\n\n" +
		completedResponseEvent()

	events := parseConstructionEvents(t, stream)
	assertConstructionTypes(t, events, []StreamEventType{StreamEventToolCallStarted, StreamEventToolCallArgumentsSnapshot, StreamEventToolCallCompleted})
	if events[2].ToolCallID != "call_terminal" || events[2].ToolName != "bash" || events[2].Arguments != `{"command":"pwd"}` {
		t.Fatalf("completion = %#v", events[2])
	}
}

func TestParseEventStreamDeduplicatesRepeatedOutputItemSnapshots(t *testing.T) {
	item := "{\"type\":\"function_call\",\"id\":\"fc_2\",\"call_id\":\"call_2\",\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"pwd\\\"}\"}"
	stream := "event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":" + item + "}\n\n" +
		"event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":" + item + "}\n\n" +
		completedResponseEvent()

	events := parseConstructionEvents(t, stream)
	assertConstructionTypes(t, events, []StreamEventType{StreamEventToolCallStarted, StreamEventToolCallArgumentsSnapshot})
}

func parseConstructionEvents(t *testing.T, stream string) []StreamEvent {
	t.Helper()
	var events []StreamEvent
	if _, err := parseEventStreamWithCallback([]byte(stream), func(event StreamEvent) { events = append(events, event) }); err != nil {
		t.Fatalf("parseEventStreamWithCallback returned error: %v", err)
	}
	return events
}

func assertConstructionTypes(t *testing.T, events []StreamEvent, want []StreamEventType) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("events len = %d, want %d: %#v", len(events), len(want), events)
	}
	for i := range want {
		if events[i].Type != want[i] {
			t.Fatalf("event[%d].Type = %q, want %q; events=%#v", i, events[i].Type, want[i], events)
		}
	}
}

func completedResponseEvent() string {
	return "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n"
}
