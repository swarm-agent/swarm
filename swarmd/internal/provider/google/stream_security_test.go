package google

import (
	"strings"
	"testing"
)

func TestParseGoogleEventStreamAllowsHighlyFragmentedResponse(t *testing.T) {
	const fragments = 16_385
	var stream strings.Builder
	for i := 0; i < fragments; i++ {
		stream.WriteString("data: {}\n\n")
	}

	accumulator := newGoogleStreamAccumulator("gemini-test")
	seen := 0
	err := parseGoogleEventStream(strings.NewReader(stream.String()), func(payload string) error {
		seen++
		return accumulator.applyPayload(payload, nil)
	})
	if err != nil {
		t.Fatalf("parse highly fragmented response: %v", err)
	}
	if seen != fragments {
		t.Fatalf("fragments seen = %d, want %d", seen, fragments)
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

func TestGoogleStreamAccumulatorRejectsOversizedOutput(t *testing.T) {
	accumulator := newGoogleStreamAccumulator("gemini-test")
	payload := `{"candidates":[{"content":{"parts":[{"text":"` + strings.Repeat("x", maxStreamOutputBytes+1) + `"}]}}]}`
	if err := accumulator.applyPayload(payload, nil); err == nil || !strings.Contains(err.Error(), "output limit") {
		t.Fatalf("error = %v, want output limit", err)
	}
}
