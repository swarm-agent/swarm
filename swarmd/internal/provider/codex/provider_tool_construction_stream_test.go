package codex

import "testing"

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
		"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"file.txt\\\"}\"}}\n\n"

	var events []StreamEvent
	if _, err := parseEventStreamWithCallback([]byte(stream), func(event StreamEvent) {
		events = append(events, event)
	}); err != nil {
		t.Fatalf("parseEventStreamWithCallback returned error: %v", err)
	}

	wantTypes := []StreamEventType{
		StreamEventToolCallStarted,
		StreamEventToolCallArgumentsDelta,
		StreamEventToolCallArgumentsDelta,
		StreamEventToolCallCompleted,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("events len = %d, want %d: %#v", len(events), len(wantTypes), events)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Fatalf("event[%d].Type = %q, want %q; events=%#v", i, events[i].Type, want, events)
		}
		if events[i].ToolCallID == "" {
			t.Fatalf("event[%d].ToolCallID is empty: %#v", i, events[i])
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

func TestParseEventStreamEmitsFunctionCallSnapshotFromOutputItem(t *testing.T) {
	stream := "event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"id\":\"fc_2\",\"call_id\":\"call_2\",\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"pwd\\\"}\"}}\n\n"

	var events []StreamEvent
	if _, err := parseEventStreamWithCallback([]byte(stream), func(event StreamEvent) {
		events = append(events, event)
	}); err != nil {
		t.Fatalf("parseEventStreamWithCallback returned error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2: %#v", len(events), events)
	}
	if events[0].Type != StreamEventToolCallStarted {
		t.Fatalf("events[0].Type = %q", events[0].Type)
	}
	if events[1].Type != StreamEventToolCallArgumentsSnapshot {
		t.Fatalf("events[1].Type = %q", events[1].Type)
	}
	if events[1].ToolCallID != "call_2" || events[1].ToolName != "bash" {
		t.Fatalf("unexpected identity: %#v", events[1])
	}
	if events[1].ArgumentsSnapshot != `{"command":"pwd"}` {
		t.Fatalf("snapshot = %q", events[1].ArgumentsSnapshot)
	}
}
