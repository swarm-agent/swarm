package google

import (
	"encoding/json"
	"reflect"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func TestGoogleGenerateContentStreamMapsCompleteFunctionCallSnapshot(t *testing.T) {
	acc := newGoogleStreamAccumulator("gemini-3-flash-preview")
	events := collectGoogleConstructionEvents(t, acc,
		`{"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"id":"call_weather","name":"weather","args":{"city":"Paris"}},"thoughtSignature":"sig-1"}]}}]}`,
		`{"candidates":[{"index":0,"finishReason":"STOP","content":{"parts":[]}}]}`,
	)
	assertGoogleConstructionTypes(t, events, []provideriface.StreamEventType{
		provideriface.StreamEventToolCallStarted,
		provideriface.StreamEventToolCallArgumentsSnapshot,
		provideriface.StreamEventToolCallCompleted,
	})
	for i, event := range events {
		if event.ToolCallID != "call_weather" || event.ToolName != "weather" || event.ToolCallIndex == nil || *event.ToolCallIndex != 0 {
			t.Fatalf("event[%d] identity = %+v", i, event)
		}
		if event.ProviderID != "google" || event.Model != "gemini-3-flash-preview" {
			t.Fatalf("event[%d] context = %+v", i, event)
		}
		if event.StartedAtUnixMs != events[0].StartedAtUnixMs || event.RecordedAtUnixMs < events[0].RecordedAtUnixMs {
			t.Fatalf("event[%d] timing = %+v, start=%+v", i, event, events[0])
		}
	}
	if events[0].Status != "started" || events[1].Status != "building" || events[2].Status != "completed" {
		t.Fatalf("statuses = %q %q %q", events[0].Status, events[1].Status, events[2].Status)
	}
	if events[1].ArgumentsSnapshot != `{"city":"Paris"}` || events[2].Arguments != `{"city":"Paris"}` {
		t.Fatalf("arguments events = %+v", events)
	}
	google := googleEventMetadata(t, events[2])
	if google["thought_signature"] != "sig-1" || google["finish_reason"] != "STOP" || google["candidate_index"] != float64(0) || google["part_index"] != float64(0) {
		t.Fatalf("completed Google metadata = %#v", google)
	}
}

func TestGoogleGenerateContentStreamPreservesParallelCandidateAndPartIdentity(t *testing.T) {
	acc := newGoogleStreamAccumulator("gemini-test")
	events := collectGoogleConstructionEvents(t, acc,
		`{"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"id":"call_a","name":"lookup","args":{"key":"a"}},"thoughtSignature":"sig-parallel"},{"functionCall":{"id":"call_b","name":"lookup","args":{"key":"b"}}}},{"index":1,"content":{"parts":[{"functionCall":{"id":"call_c","name":"lookup","args":{"key":"c"}}}]}}]}`,
		`{"candidates":[{"index":0,"finishReason":"STOP","content":{"parts":[]}},{"index":1,"finishReason":"STOP","content":{"parts":[]}}]}`,
	)
	assertGoogleConstructionTypes(t, events, []provideriface.StreamEventType{
		provideriface.StreamEventToolCallStarted, provideriface.StreamEventToolCallArgumentsSnapshot,
		provideriface.StreamEventToolCallStarted, provideriface.StreamEventToolCallArgumentsSnapshot,
		provideriface.StreamEventToolCallStarted, provideriface.StreamEventToolCallArgumentsSnapshot,
		provideriface.StreamEventToolCallCompleted, provideriface.StreamEventToolCallCompleted, provideriface.StreamEventToolCallCompleted,
	})
	wantIDs := []string{"call_a", "call_a", "call_b", "call_b", "call_c", "call_c", "call_a", "call_b", "call_c"}
	for i, want := range wantIDs {
		if events[i].ToolCallID != want {
			t.Fatalf("event[%d].ToolCallID=%q, want %q", i, events[i].ToolCallID, want)
		}
	}
	if *events[0].ToolCallIndex != 0 || *events[2].ToolCallIndex != 1 || *events[4].ToolCallIndex != 2 {
		t.Fatalf("parallel indexes = %d %d %d", *events[0].ToolCallIndex, *events[2].ToolCallIndex, *events[4].ToolCallIndex)
	}
	if googleEventMetadata(t, events[0])["thought_signature"] != "sig-parallel" {
		t.Fatalf("first parallel call lost thought signature: %+v", events[0])
	}
	if _, ok := googleEventMetadata(t, events[2])["thought_signature"]; ok {
		t.Fatalf("second parallel call unexpectedly inherited thought signature: %+v", events[2])
	}
}

func TestGoogleGenerateContentStreamRepairsMissingIDAndDeduplicatesRepeatedChunk(t *testing.T) {
	acc := newGoogleStreamAccumulator("gemini-2.5-flash")
	chunk := `{"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"name":"search","args":{"query":"swarm"}}}]}}]}`
	events := collectGoogleConstructionEvents(t, acc,
		chunk,
		chunk,
		`{"candidates":[{"index":0,"finishReason":"STOP","content":{"parts":[]}}]}`,
		`{"candidates":[{"index":0,"finishReason":"STOP","content":{"parts":[]}}]}`,
	)
	assertGoogleConstructionTypes(t, events, []provideriface.StreamEventType{
		provideriface.StreamEventToolCallStarted,
		provideriface.StreamEventToolCallArgumentsSnapshot,
		provideriface.StreamEventToolCallCompleted,
	})
	for i, event := range events {
		if event.ToolCallID != "" || event.ToolCallIndex == nil || *event.ToolCallIndex != 0 || event.ToolName != "search" {
			t.Fatalf("event[%d] provisional identity = %+v", i, event)
		}
	}
	if synthetic, _ := googleEventMetadata(t, events[2])["synthetic_call_id"].(bool); !synthetic {
		t.Fatalf("completed metadata did not retain synthetic ID marker: %+v", events[2].Metadata)
	}
	if len(acc.response().FunctionCalls) != 1 {
		t.Fatalf("final function calls = %+v, want one deduplicated call", acc.response().FunctionCalls)
	}
}

func TestGoogleGenerateContentStreamRepairsNativeIDWhenItArrivesAfterProvisionalCall(t *testing.T) {
	acc := newGoogleStreamAccumulator("gemini-test")
	events := collectGoogleConstructionEvents(t, acc,
		`{"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"name":"read","args":{"path":"a"}}}]}}]}`,
		`{"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"id":"call_read","name":"read","args":{"path":"a"}}}]}}]}`,
		`{"candidates":[{"index":0,"finishReason":"STOP","content":{"parts":[]}}]}`,
	)
	assertGoogleConstructionTypes(t, events, []provideriface.StreamEventType{
		provideriface.StreamEventToolCallStarted,
		provideriface.StreamEventToolCallArgumentsSnapshot,
		provideriface.StreamEventToolCallCompleted,
	})
	if events[0].ToolCallID != "" || events[2].ToolCallID != "call_read" || events[2].ToolName != "read" {
		t.Fatalf("late identity repair events = %+v", events)
	}
	if len(acc.response().FunctionCalls) != 1 || acc.response().FunctionCalls[0].CallID != "call_read" {
		t.Fatalf("late identity repair final response = %+v", acc.response().FunctionCalls)
	}
}

func TestGoogleGenerateContentStreamDoesNotMergeDistinctNativeIDsWithSamePayload(t *testing.T) {
	acc := newGoogleStreamAccumulator("gemini-test")
	events := collectGoogleConstructionEvents(t, acc,
		`{"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"id":"call_a","name":"read","args":{"path":"same"} }},{"functionCall":{"id":"call_b","name":"read","args":{"path":"same"}}}]},"finishReason":"STOP"}]}`,
	)
	assertGoogleConstructionTypes(t, events, []provideriface.StreamEventType{
		provideriface.StreamEventToolCallStarted, provideriface.StreamEventToolCallArgumentsSnapshot,
		provideriface.StreamEventToolCallStarted, provideriface.StreamEventToolCallArgumentsSnapshot,
		provideriface.StreamEventToolCallCompleted, provideriface.StreamEventToolCallCompleted,
	})
	if events[0].ToolCallID != "call_a" || events[2].ToolCallID != "call_b" {
		t.Fatalf("same-payload parallel calls merged: %+v", events)
	}
}

func TestGoogleGenerateContentStreamWaitsForEveryObservedCandidateFinishReason(t *testing.T) {
	acc := newGoogleStreamAccumulator("gemini-test")
	events := collectGoogleConstructionEvents(t, acc,
		`{"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"id":"call_a","name":"read","args":{"path":"a"}}}]}},{"index":1,"content":{"parts":[{"functionCall":{"id":"call_b","name":"read","args":{"path":"b"}}}]}}]}`,
		`{"candidates":[{"index":0,"finishReason":"STOP","content":{"parts":[]}}]}`,
	)
	if acc.finished {
		t.Fatal("stream marked finished while observed candidate 1 had no finishReason")
	}
	events = append(events, collectGoogleConstructionEvents(t, acc,
		`{"candidates":[{"index":1,"finishReason":"STOP","content":{"parts":[]}}]}`,
	)...)
	if !acc.finished {
		t.Fatal("stream did not finish after every observed candidate reported finishReason")
	}
	completed := 0
	for _, event := range events {
		if event.Type == provideriface.StreamEventToolCallCompleted {
			completed++
		}
	}
	if completed != 2 {
		t.Fatalf("completed events=%d, want 2; events=%+v", completed, events)
	}
}

func TestGoogleGenerateContentStreamDoesNotCompleteBeforeFinishReason(t *testing.T) {
	acc := newGoogleStreamAccumulator("gemini-test")
	events := collectGoogleConstructionEvents(t, acc,
		`{"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"id":"call_1","name":"read","args":{"path":"a"}}}]}}]}`,
		`{"candidates":[{"index":0,"content":{"parts":[]}}]}`,
	)
	assertGoogleConstructionTypes(t, events, []provideriface.StreamEventType{
		provideriface.StreamEventToolCallStarted,
		provideriface.StreamEventToolCallArgumentsSnapshot,
	})
	if acc.finished {
		t.Fatal("stream marked finished before candidate finishReason")
	}
}

func collectGoogleConstructionEvents(t *testing.T, acc *googleStreamAccumulator, payloads ...string) []provideriface.StreamEvent {
	t.Helper()
	var events []provideriface.StreamEvent
	for _, payload := range payloads {
		if err := acc.applyPayload(payload, func(event provideriface.StreamEvent) {
			if event.Type == provideriface.StreamEventToolCallStarted || event.Type == provideriface.StreamEventToolCallArgumentsSnapshot || event.Type == provideriface.StreamEventToolCallCompleted {
				events = append(events, event)
			}
		}); err != nil {
			t.Fatalf("apply payload: %v", err)
		}
	}
	return events
}

func assertGoogleConstructionTypes(t *testing.T, events []provideriface.StreamEvent, want []provideriface.StreamEventType) {
	t.Helper()
	got := make([]provideriface.StreamEventType, len(events))
	for i := range events {
		got[i] = events[i].Type
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("construction event types = %v, want %v; events=%+v", got, want, events)
	}
}

func googleEventMetadata(t *testing.T, event provideriface.StreamEvent) map[string]any {
	t.Helper()
	raw, err := json.Marshal(event.Metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	google, ok := decoded["google"].(map[string]any)
	if !ok {
		t.Fatalf("Google metadata missing: %#v", decoded)
	}
	return google
}
