package google

import (
	"strings"
	"testing"
)

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
