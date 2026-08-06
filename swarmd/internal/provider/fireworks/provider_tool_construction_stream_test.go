package fireworks

import (
	"reflect"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func TestEmitFireworksToolCallConstructionEventsFromStreamingDeltas(t *testing.T) {
	state := newFireworksToolCallConstructionState("accounts/fireworks/models/test")
	var events []provideriface.StreamEvent

	emitFireworksToolCallConstructionEvents(state, chatCompletionChunk{
		ID:    "chatcmpl_1",
		Model: "accounts/fireworks/models/provider-test",
		Choices: []chatCompletionChoice{{
			Index: 0,
			Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{{
				Index: 0,
				ID:    "call_1",
				Type:  "function",
				Function: &chatCompletionToolFunctionDelta{
					Name:      "read",
					Arguments: `{"path"`,
				},
			}}},
		}}}, collectFireworksConstructionEvents(&events))
	emitFireworksToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{{
		Index: 0,
		Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{{
			Index:    0,
			Function: &chatCompletionToolFunctionDelta{Arguments: `:"file.txt"}`},
		}}},
		FinishReason: "tool_calls",
	}}}, collectFireworksConstructionEvents(&events))

	assertFireworksConstructionTypes(t, events, []provideriface.StreamEventType{
		provideriface.StreamEventToolCallStarted,
		provideriface.StreamEventToolCallArgumentsDelta,
		provideriface.StreamEventToolCallArgumentsDelta,
		provideriface.StreamEventToolCallCompleted,
	})
	for i, event := range events {
		if event.ToolCallIndex == nil || *event.ToolCallIndex != 0 || event.ToolCallID != "call_1" || event.ToolName != "read" {
			t.Fatalf("event[%d] identity = %#v", i, event)
		}
		if event.ProviderID != "fireworks" || event.Model != "accounts/fireworks/models/test" {
			t.Fatalf("event[%d] context = %#v", i, event)
		}
		if event.StartedAtUnixMs != events[0].StartedAtUnixMs || event.RecordedAtUnixMs < events[0].RecordedAtUnixMs {
			t.Fatalf("event[%d] timing = %#v; start=%#v", i, event, events[0])
		}
	}
	if events[0].Status != "started" || events[1].Status != "building" || events[3].Status != "completed" {
		t.Fatalf("statuses = %q, %q, %q", events[0].Status, events[1].Status, events[3].Status)
	}
	if events[1].ArgumentsDelta != `{"path"` || events[2].ArgumentsDelta != `:"file.txt"}` {
		t.Fatalf("argument deltas = %#v %#v", events[1].ArgumentsDelta, events[2].ArgumentsDelta)
	}
	if events[3].Arguments != `{"path":"file.txt"}` {
		t.Fatalf("completed arguments = %q", events[3].Arguments)
	}
	if events[3].Metadata["response_id"] != "chatcmpl_1" || events[3].Metadata["provider_model"] != "accounts/fireworks/models/provider-test" || events[3].Metadata["finish_reason"] != "tool_calls" {
		t.Fatalf("completed metadata = %#v", events[3].Metadata)
	}
}

func TestEmitFireworksToolCallConstructionEventsRepairsLateIdentityAndName(t *testing.T) {
	state := newFireworksToolCallConstructionState("model-requested")
	var events []provideriface.StreamEvent
	collect := collectFireworksConstructionEvents(&events)

	emitFireworksToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{{
		Index: 4,
		Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{{
			Index:    2,
			Function: &chatCompletionToolFunctionDelta{Arguments: `{"action"`},
		}}},
	}}}, collect)
	emitFireworksToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{{
		Index: 4,
		Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{{
			Index: 2,
			ID:    "call_late",
			Type:  "function",
			Function: &chatCompletionToolFunctionDelta{
				Name:      "plan_manage",
				Arguments: `:"complete"}`,
			},
		}}},
		FinishReason: "tool_calls",
	}}}, collect)

	assertFireworksConstructionTypes(t, events, []provideriface.StreamEventType{
		provideriface.StreamEventToolCallStarted,
		provideriface.StreamEventToolCallArgumentsDelta,
		provideriface.StreamEventToolCallArgumentsDelta,
		provideriface.StreamEventToolCallCompleted,
	})
	for i := range events {
		if events[i].ToolCallID != "call_late" || events[i].ToolName != "plan_manage" {
			t.Fatalf("event[%d] repaired identity = %#v", i, events[i])
		}
	}
	if events[3].Arguments != `{"action":"complete"}` {
		t.Fatalf("completed arguments = %q", events[3].Arguments)
	}
	if events[3].Metadata["choice_index"] != 4 || events[3].Metadata["native_tool_call_index"] != 2 {
		t.Fatalf("native identity metadata = %#v", events[3].Metadata)
	}
}

func TestEmitFireworksToolCallConstructionEventsKeepsParallelChoicesDistinct(t *testing.T) {
	state := newFireworksToolCallConstructionState("model")
	var events []provideriface.StreamEvent
	collect := collectFireworksConstructionEvents(&events)

	emitFireworksToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{
		{
			Index: 0,
			Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{{
				Index: 0, ID: "call_choice_0", Function: &chatCompletionToolFunctionDelta{Name: "read", Arguments: `{}`},
			}}},
		},
		{
			Index: 1,
			Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{{
				Index: 0, ID: "call_choice_1", Function: &chatCompletionToolFunctionDelta{Name: "write", Arguments: `{"path":"a"}`},
			}}},
		},
	}}, collect)
	emitFireworksToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{
		{Index: 1, FinishReason: "tool_calls"},
		{Index: 0, FinishReason: "tool_calls"},
	}}, collect)

	if len(events) != 6 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].ToolCallIndex == nil || events[2].ToolCallIndex == nil || *events[0].ToolCallIndex == *events[2].ToolCallIndex {
		t.Fatalf("choice-local native indexes collided: %#v %#v", events[0], events[2])
	}
	if events[4].ToolCallID != "call_choice_1" || events[4].Type != provideriface.StreamEventToolCallCompleted || events[5].ToolCallID != "call_choice_0" || events[5].Type != provideriface.StreamEventToolCallCompleted {
		t.Fatalf("choice-scoped completion = %#v %#v", events[4], events[5])
	}
	if events[4].Metadata["choice_index"] != 1 || events[5].Metadata["choice_index"] != 0 {
		t.Fatalf("choice metadata = %#v %#v", events[4].Metadata, events[5].Metadata)
	}
}

func TestEmitFireworksToolCallConstructionEventsCompletesInNativeIndexOrder(t *testing.T) {
	state := newFireworksToolCallConstructionState("model")
	var events []provideriface.StreamEvent
	collect := collectFireworksConstructionEvents(&events)

	emitFireworksToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{{
		Index: 3,
		Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{
			{Index: 2, ID: "call_2", Function: &chatCompletionToolFunctionDelta{Name: "third"}},
			{Index: 0, ID: "call_0", Function: &chatCompletionToolFunctionDelta{Name: "first"}},
			{Index: 1, ID: "call_1", Function: &chatCompletionToolFunctionDelta{Name: "second"}},
		}},
	}}}, collect)
	emitFireworksToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{{Index: 3, FinishReason: "tool_calls"}}}, collect)

	var completed []provideriface.StreamEvent
	for _, event := range events {
		if event.Type == provideriface.StreamEventToolCallCompleted {
			completed = append(completed, event)
		}
	}
	if len(completed) != 3 {
		t.Fatalf("completed = %#v", completed)
	}
	gotNativeIndexes := []any{
		completed[0].Metadata["native_tool_call_index"],
		completed[1].Metadata["native_tool_call_index"],
		completed[2].Metadata["native_tool_call_index"],
	}
	if !reflect.DeepEqual(gotNativeIndexes, []any{0, 1, 2}) {
		t.Fatalf("completion native indexes = %#v", gotNativeIndexes)
	}
}

func TestEmitFireworksToolCallConstructionEventsDeduplicatesTerminalChunk(t *testing.T) {
	state := newFireworksToolCallConstructionState("model")
	var events []provideriface.StreamEvent
	collect := collectFireworksConstructionEvents(&events)

	emitFireworksToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{{
		Index: 0,
		Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{{
			Index: 0, ID: "call_once", Function: &chatCompletionToolFunctionDelta{Name: "task", Arguments: `{}`},
		}}},
	}}}, collect)
	terminal := chatCompletionChunk{Choices: []chatCompletionChoice{{Index: 0, FinishReason: "tool_calls"}}}
	emitFireworksToolCallConstructionEvents(state, terminal, collect)
	emitFireworksToolCallConstructionEvents(state, terminal, collect)

	completed := 0
	for _, event := range events {
		if event.Type == provideriface.StreamEventToolCallCompleted {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("completed event count = %d; events=%#v", completed, events)
	}
}

func TestEmitFireworksToolCallConstructionEventsDoesNotCompleteOnNonToolTerminal(t *testing.T) {
	state := newFireworksToolCallConstructionState("model")
	var events []provideriface.StreamEvent
	collect := collectFireworksConstructionEvents(&events)

	emitFireworksToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{{
		Index: 0,
		Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{{
			Index: 0, ID: "call_incomplete", Function: &chatCompletionToolFunctionDelta{Name: "read", Arguments: `{"path"`},
		}}},
	}}}, collect)
	emitFireworksToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{{Index: 0, FinishReason: "length"}}}, collect)

	for _, event := range events {
		if event.Type == provideriface.StreamEventToolCallCompleted {
			t.Fatalf("length-truncated call was marked complete: %#v", events)
		}
	}
}

func TestEmitFireworksToolCallConstructionEventsAbsentWhenNoToolDeltas(t *testing.T) {
	state := newFireworksToolCallConstructionState("model")
	called := false
	emitFireworksToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{{
		Index: 0,
		Delta: &chatCompletionMessageDelta{Content: "hello"},
	}}}, func(provideriface.StreamEvent) { called = true })
	if called {
		t.Fatal("tool-call construction event emitted for content-only chunk")
	}
}

func collectFireworksConstructionEvents(events *[]provideriface.StreamEvent) func(provideriface.StreamEvent) {
	return func(event provideriface.StreamEvent) { *events = append(*events, event) }
}

func assertFireworksConstructionTypes(t *testing.T, events []provideriface.StreamEvent, want []provideriface.StreamEventType) {
	t.Helper()
	got := make([]provideriface.StreamEventType, 0, len(events))
	for _, event := range events {
		got = append(got, event.Type)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event types = %#v, want %#v; events=%#v", got, want, events)
	}
}
