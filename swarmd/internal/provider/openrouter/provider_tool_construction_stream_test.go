package openrouter

import (
	"reflect"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func TestOpenRouterConstructionRepairsLateIdentityAndKeepsStableTiming(t *testing.T) {
	state := newOpenRouterToolCallConstructionState("openrouter", "requested/model")
	times := []int64{100, 90, 101, 102}
	state.now = func() int64 {
		value := times[0]
		times = times[1:]
		return value
	}
	var events []provideriface.StreamEvent
	emit := func(event provideriface.StreamEvent) { events = append(events, event) }

	emitOpenRouterToolCallConstructionEvents(state, openRouterToolChunk(0, 2, "", "", `{"path":`, ""), emit)
	emitOpenRouterToolCallConstructionEvents(state, openRouterToolChunk(0, 2, "call_read", "read", `"a.txt"}`, "tool_calls"), emit)

	assertOpenRouterConstructionTypes(t, events, []provideriface.StreamEventType{
		provideriface.StreamEventToolCallStarted,
		provideriface.StreamEventToolCallArgumentsDelta,
		provideriface.StreamEventToolCallArgumentsDelta,
		provideriface.StreamEventToolCallCompleted,
	})
	for i := range events {
		if events[i].ToolCallID != "call_read" || events[i].ToolName != "read" {
			t.Fatalf("event[%d] late identity not repaired: %#v", i, events[i])
		}
	}
	for i, event := range events {
		if event.ProviderID != "openrouter" || event.Model != "requested/model" {
			t.Fatalf("event[%d] context = %#v", i, event)
		}
		if event.StartedAtUnixMs != 100 || event.RecordedAtUnixMs < event.StartedAtUnixMs {
			t.Fatalf("event[%d] timing = %#v", i, event)
		}
	}
	if events[1].ArgumentsDelta != `{"path":` || events[2].ArgumentsDelta != `"a.txt"}` {
		t.Fatalf("repaired argument order = %#v", events)
	}
	if events[3].Arguments != `{"path":"a.txt"}` || events[3].Status != "completed" {
		t.Fatalf("completion = %#v", events[3])
	}
}

func TestOpenRouterConstructionSeparatesChoicesAndCompletesDeterministically(t *testing.T) {
	state := newOpenRouterToolCallConstructionState("openrouter", "model")
	var events []provideriface.StreamEvent
	emit := func(event provideriface.StreamEvent) { events = append(events, event) }

	emitOpenRouterToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{
		{Index: 1, Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{openRouterToolDelta(0, "call_choice_1", "task", `{}`)}}},
		{Index: 0, Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{openRouterToolDelta(0, "call_choice_0", "edit", `{}`)}}},
	}}, emit)
	emitOpenRouterToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{
		{Index: 0, FinishReason: "tool_calls"},
		{Index: 1, FinishReason: "tool_calls"},
	}}, emit)
	// Repeated normalized terminal chunks must not duplicate completions.
	emitOpenRouterToolCallConstructionEvents(state, chatCompletionChunk{Choices: []chatCompletionChoice{{Index: 0, FinishReason: "tool_calls"}}}, emit)

	var completed []provideriface.StreamEvent
	for _, event := range events {
		if event.Type == provideriface.StreamEventToolCallCompleted {
			completed = append(completed, event)
		}
	}
	if len(completed) != 2 || completed[0].ToolCallID != "call_choice_0" || completed[1].ToolCallID != "call_choice_1" {
		t.Fatalf("completion order = %#v", completed)
	}
	if completed[0].ToolCallIndex == nil || completed[1].ToolCallIndex == nil || *completed[0].ToolCallIndex == *completed[1].ToolCallIndex {
		t.Fatalf("choice/index collision was not separated: %#v", completed)
	}
	if completed[0].Metadata["native_tool_index"] != 0 || completed[1].Metadata["choice_index"] != 1 {
		t.Fatalf("native metadata = %#v %#v", completed[0].Metadata, completed[1].Metadata)
	}
}

func TestOpenRouterConstructionAcceptsFragmentedOrRepeatedIdentityFields(t *testing.T) {
	state := newOpenRouterToolCallConstructionState()
	var events []provideriface.StreamEvent
	emit := func(event provideriface.StreamEvent) { events = append(events, event) }
	emitOpenRouterToolCallConstructionEvents(state, openRouterToolChunk(0, 0, "call_", "get_", `{`, ""), emit)
	emitOpenRouterToolCallConstructionEvents(state, openRouterToolChunk(0, 0, "weather", "weather", `}`, "tool_calls"), emit)
	if got := events[len(events)-1]; got.ToolCallID != "call_weather" || got.ToolName != "get_weather" {
		t.Fatalf("fragmented identity = %#v", got)
	}

	repeated := newOpenRouterToolCallConstructionState()
	events = nil
	emitOpenRouterToolCallConstructionEvents(repeated, openRouterToolChunk(0, 0, "call_full", "read", `{`, ""), emit)
	emitOpenRouterToolCallConstructionEvents(repeated, openRouterToolChunk(0, 0, "call_full", "read", `}`, "tool_calls"), emit)
	if got := events[len(events)-1]; got.ToolCallID != "call_full" || got.ToolName != "read" {
		t.Fatalf("repeated identity = %#v", got)
	}
}

func TestOpenRouterConstructionCompletesTerminalIdentityWithoutDelta(t *testing.T) {
	state := newOpenRouterToolCallConstructionState()
	var events []provideriface.StreamEvent
	emit := func(event provideriface.StreamEvent) { events = append(events, event) }
	emitOpenRouterToolCallConstructionEvents(state, openRouterToolChunk(0, 0, "", "", `{}`, ""), emit)
	emitOpenRouterToolCallConstructionEvents(state, openRouterToolChunk(0, 0, "call_terminal", "read", "", "tool_calls"), emit)
	assertOpenRouterConstructionTypes(t, events, []provideriface.StreamEventType{
		provideriface.StreamEventToolCallStarted,
		provideriface.StreamEventToolCallArgumentsDelta,
		provideriface.StreamEventToolCallCompleted,
	})
	if events[2].ToolCallID != "call_terminal" || events[2].Arguments != `{}` {
		t.Fatalf("terminal repair = %#v", events)
	}
}

func TestOpenRouterConstructionDoesNotCompleteOnStopLengthOrError(t *testing.T) {
	for _, finishReason := range []string{"stop", "length", "error"} {
		t.Run(finishReason, func(t *testing.T) {
			state := newOpenRouterToolCallConstructionState()
			var events []provideriface.StreamEvent
			emit := func(event provideriface.StreamEvent) { events = append(events, event) }
			emitOpenRouterToolCallConstructionEvents(state, openRouterToolChunk(0, 0, "call_1", "read", `{}`, finishReason), emit)
			for _, event := range events {
				if event.Type == provideriface.StreamEventToolCallCompleted {
					t.Fatalf("finish_reason %q emitted construction completion: %#v", finishReason, events)
				}
			}
		})
	}
}

func TestOpenRouterStreamStateKeepsSameToolIndexFromDifferentChoicesDistinct(t *testing.T) {
	state := newOpenRouterStreamState()
	chunk := chatCompletionChunk{Choices: []chatCompletionChoice{
		{Index: 0, Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{openRouterToolDelta(0, "call_a", "edit", `{}`)}}},
		{Index: 1, Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{openRouterToolDelta(0, "call_b", "task", `{}`)}}},
	}}
	if err := state.apply(chunk); err != nil {
		t.Fatalf("apply chunk: %v", err)
	}
	got := state.response().Choices[0].Message.ToolCalls
	if len(got) != 2 || got[0].ID != "call_a" || got[1].ID != "call_b" {
		t.Fatalf("merged tool calls = %#v", got)
	}
}

func openRouterToolChunk(choiceIndex, toolIndex int, id, name, arguments, finishReason string) chatCompletionChunk {
	return chatCompletionChunk{
		Choices: []chatCompletionChoice{{
			Index:        choiceIndex,
			FinishReason: finishReason,
			Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{
				openRouterToolDelta(toolIndex, id, name, arguments),
			}},
		}},
	}
}

func openRouterToolDelta(index int, id, name, arguments string) chatCompletionToolCallDelta {
	return chatCompletionToolCallDelta{
		Index: index,
		ID:    id,
		Type:  "function",
		Function: &chatCompletionToolFunctionDelta{
			Name:      name,
			Arguments: arguments,
		},
	}
}

func assertOpenRouterConstructionTypes(t *testing.T, events []provideriface.StreamEvent, want []provideriface.StreamEventType) {
	t.Helper()
	got := make([]provideriface.StreamEventType, 0, len(events))
	for _, event := range events {
		got = append(got, event.Type)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("construction types = %#v, want %#v; events=%#v", got, want, events)
	}
}
