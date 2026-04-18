package codex

import (
	"strings"
	"testing"
)

func TestTranscriptionFailureDetail_ErrorMessage(t *testing.T) {
	payload := map[string]any{
		"error": map[string]any{
			"message": "forbidden: account is not allowed to call this endpoint",
		},
	}
	got := transcriptionFailureDetail(payload)
	if !strings.Contains(strings.ToLower(got), "forbidden") {
		t.Fatalf("transcriptionFailureDetail() = %q, want message to include forbidden", got)
	}
}

func TestTranscriptionFailureDetail_HTMLRawBody(t *testing.T) {
	payload := map[string]any{
		"raw_body": "<html><body>forbidden</body></html>",
	}
	got := transcriptionFailureDetail(payload)
	want := "upstream returned HTML (likely auth/session rejection)"
	if got != want {
		t.Fatalf("transcriptionFailureDetail() = %q, want %q", got, want)
	}
}

func TestTranscriptionFailureDetail_NonJSONRawBody(t *testing.T) {
	payload := map[string]any{
		"raw_body": "forbidden",
	}
	got := transcriptionFailureDetail(payload)
	want := "upstream returned non-JSON response"
	if got != want {
		t.Fatalf("transcriptionFailureDetail() = %q, want %q", got, want)
	}
}
