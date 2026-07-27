package codex

import (
	"strings"
	"testing"
)

func TestTranscriptionErrorsDoNotExposeUnexpectedPayload(t *testing.T) {
	secret := "customer-transcript-secret"
	_, err := extractTranscriptionText(map[string]any{"unexpected": secret})
	if err == nil {
		t.Fatal("expected missing text error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposed response content: %q", err)
	}
	if got := transcriptionFailureDetail(map[string]any{"error": map[string]any{"message": secret}}); strings.Contains(got, secret) {
		t.Fatalf("failure detail exposed response content: %q", got)
	}
}

func TestTranscriptionDetailTruncatesByRune(t *testing.T) {
	got := truncateTranscriptionDetail("ééé", 2)
	if got != "éé...[truncated]" {
		t.Fatalf("truncate detail = %q", got)
	}
}
