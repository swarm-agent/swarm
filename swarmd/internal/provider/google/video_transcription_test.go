package google

import (
	"strings"
	"testing"
)

func TestParseGoogleTranscriptValidatesStructuredSegments(t *testing.T) {
	payload := []byte(`{"candidates":[{"content":{"parts":[{"text":"{\"text\":\"hello world\",\"language\":\"en\",\"duration_ms\":1000,\"segments\":[{\"start_ms\":0,\"end_ms\":1000,\"text\":\"hello world\"}]}"}]}}]}`)
	transcript, err := parseGoogleTranscript(payload)
	if err != nil {
		t.Fatal(err)
	}
	if transcript.Partial || transcript.Text != "hello world" || len(transcript.Segments) != 1 {
		t.Fatalf("transcript = %#v", transcript)
	}
}

func TestParseGoogleTranscriptLabelsIncompleteOutputPartial(t *testing.T) {
	payload := []byte(`{"candidates":[{"content":{"parts":[{"text":"{\"text\":\"hello\",\"segments\":[]}"}]}}]}`)
	transcript, err := parseGoogleTranscript(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !transcript.Partial {
		t.Fatal("incomplete structured transcript must be partial")
	}
}

func TestParseGoogleTranscriptRejectsMalformedProviderPayloadWithoutEcho(t *testing.T) {
	secret := "sensitive-provider-payload"
	_, err := parseGoogleTranscript([]byte(`{"unexpected":"` + secret + `"}`))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error = %v", err)
	}
}
