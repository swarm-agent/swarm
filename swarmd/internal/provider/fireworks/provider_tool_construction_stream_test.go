package fireworks

import (
	"reflect"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func TestEmitFireworksToolCallConstructionEventsFromStreamingDeltas(t *testing.T) {
	state := newFireworksToolCallConstructionState()
	var events []provideriface.StreamEvent

	emitFireworksToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{{
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
	}}}, func(event provideriface.StreamEvent) { events = append(events, event) })
	emitFireworksToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{{
		Index: 0,
		Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{{
			Index:    0,
			Function: &chatCompletionToolFunctionDelta{Arguments: `:"file.txt"}`},
		}}},
		FinishReason: "tool_calls",
	}}}, func(event provideriface.StreamEvent) { events = append(events, event) })

	gotTypes := make([]provideriface.StreamEventType, 0, len(events))
	for _, event := range events {
		gotTypes = append(gotTypes, event.Type)
	}
	wantTypes := []provideriface.StreamEventType{
		provideriface.StreamEventToolCallStarted,
		provideriface.StreamEventToolCallArgumentsDelta,
		provideriface.StreamEventToolCallArgumentsDelta,
		provideriface.StreamEventToolCallCompleted,
	}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("event types = %#v, want %#v; events=%#v", gotTypes, wantTypes, events)
	}
	if events[0].ToolCallIndex == nil || *events[0].ToolCallIndex != 0 || events[0].ToolCallID != "call_1" || events[0].ToolName != "read" {
		t.Fatalf("started event identity = %#v", events[0])
	}
	if events[1].ArgumentsDelta != `{"path"` || events[2].ArgumentsDelta != `:"file.txt"}` {
		t.Fatalf("argument deltas = %#v %#v", events[1].ArgumentsDelta, events[2].ArgumentsDelta)
	}
	if events[3].Arguments != `{"path":"file.txt"}` {
		t.Fatalf("completed arguments = %q", events[3].Arguments)
	}
}

func TestEmitFireworksToolCallConstructionEventsCompletesOnFinishOnlyChunk(t *testing.T) {
	state := newFireworksToolCallConstructionState()
	var events []provideriface.StreamEvent
	emitFireworksToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{{
		Index: 0,
		Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{{
			Index:    0,
			Function: &chatCompletionToolFunctionDelta{Name: "plan_manage", Arguments: `{"action":"complete`},
		}}},
	}}}, func(event provideriface.StreamEvent) { events = append(events, event) })
	emitFireworksToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{{
		Index:        0,
		FinishReason: "tool_calls",
	}}}, func(event provideriface.StreamEvent) { events = append(events, event) })

	gotTypes := make([]provideriface.StreamEventType, 0, len(events))
	for _, event := range events {
		gotTypes = append(gotTypes, event.Type)
	}
	wantTypes := []provideriface.StreamEventType{
		provideriface.StreamEventToolCallStarted,
		provideriface.StreamEventToolCallArgumentsDelta,
		provideriface.StreamEventToolCallCompleted,
	}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("event types = %#v, want %#v; events=%#v", gotTypes, wantTypes, events)
	}
	if events[2].ToolName != "plan_manage" || events[2].Arguments != `{"action":"complete` {
		t.Fatalf("completed event = %#v", events[2])
	}
}

func TestEmitFireworksToolCallConstructionEventsAbsentWhenNoToolDeltas(t *testing.T) {
	state := newFireworksToolCallConstructionState()
	called := false
	emitFireworksToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{{
		Index: 0,
		Delta: &chatCompletionMessageDelta{Content: "hello"},
	}}}, func(event provideriface.StreamEvent) { called = true })
	if called {
		t.Fatal("tool-call construction event emitted for content-only chunk")
	}
}
