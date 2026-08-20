package google

import (
	"strings"
	"testing"
)

func TestParseGoogleEventStreamAllowsAggregateResponseBeyondUnaryBodyLimit(t *testing.T) {
	const event = "data: {}\n\n"
	fragments := maxResponseBytes/len(event) + 1
	stream := strings.Repeat(event, fragments)

	accumulator := newGoogleStreamAccumulator("gemini-test")
	seen := 0
	err := parseGoogleEventStream(strings.NewReader(stream), func(payload string) error {
		seen++
		return accumulator.applyPayload(payload, nil)
	})
	if err != nil {
		t.Fatalf("parse response larger than unary body limit: %v", err)
	}
	if seen != fragments {
		t.Fatalf("fragments seen = %d, want %d", seen, fragments)
	}
}

func TestParseGoogleEventStreamRejectsOversizedSingleEvent(t *testing.T) {
	stream := "data: " + strings.Repeat("x", maxStreamEventBytes+1) + "\n\n"
	err := parseGoogleEventStream(strings.NewReader(stream), func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "event byte limit") {
		t.Fatalf("error = %v, want single-event byte limit", err)
	}
}

func TestGoogleStreamAccumulatorRequiresFinishReason(t *testing.T) {
	accumulator := newGoogleStreamAccumulator("gemini-test")
	if err := accumulator.applyPayload(`{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`, nil); err != nil {
		t.Fatalf("apply payload: %v", err)
	}
	if accumulator.finished {
		t.Fatal("stream marked finished without finishReason")
	}
}

func TestGoogleStreamAccumulatorAllowsOutputBeyondFormerAggregateLimit(t *testing.T) {
	accumulator := newGoogleStreamAccumulator("gemini-test")
	payload := `{"candidates":[{"content":{"parts":[{"text":"` + strings.Repeat("x", (4<<20)+1) + `"}]}}]}`
	if err := accumulator.applyPayload(payload, nil); err != nil {
		t.Fatalf("apply output beyond former aggregate limit: %v", err)
	}
}

func TestGoogleStreamAccumulatorAllowsToolArgumentsBeyondFormerAggregateLimit(t *testing.T) {
	accumulator := newGoogleStreamAccumulator("gemini-test")
	payload := `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"write","args":{"content":"` + strings.Repeat("x", (1<<20)+1) + `"}}}]}}]}`
	if err := accumulator.applyPayload(payload, nil); err != nil {
		t.Fatalf("apply tool arguments beyond former aggregate limit: %v", err)
	}
}
