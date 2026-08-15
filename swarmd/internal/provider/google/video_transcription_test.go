package google

import (
	"strings"
	"testing"
)

func TestParseGoogleTranscriptNormalizesMultimodalTimeline(t *testing.T) {
	payload := []byte(`{"candidates":[{"content":{"parts":[{"text":"{\"summary\":\"A short greeting.\",\"language\":\"en\",\"duration_ms\":1000,\"content_empty\":false,\"segments\":[{\"start_ms\":0,\"end_ms\":1000,\"speech\":\"hello world\",\"audio\":\"soft music\",\"visual\":\"A person waves\",\"on_screen_text\":\"Welcome\"}]}"}]}}]}`)
	transcript, err := parseGoogleTranscript(payload)
	if err != nil {
		t.Fatal(err)
	}
	if transcript.Partial || len(transcript.Segments) != 1 || !strings.Contains(transcript.Text, "Speech: hello world") || !strings.Contains(transcript.Text, "Visual: A person waves") {
		t.Fatalf("transcript = %#v", transcript)
	}
	if transcript.Segments[0].Text == "" || transcript.Summary != "A short greeting." {
		t.Fatalf("normalized transcript = %#v", transcript)
	}
}

func TestParseGoogleTranscriptAcceptsSilentVisualOnlyVideo(t *testing.T) {
	payload := []byte(`{"candidates":[{"content":{"parts":[{"text":"{\"summary\":\"A silent UI demo.\",\"language\":\"\",\"duration_ms\":2000,\"content_empty\":false,\"segments\":[{\"start_ms\":0,\"end_ms\":2000,\"speech\":\"\",\"audio\":\"\",\"visual\":\"A cursor opens Settings\",\"on_screen_text\":\"Media\"}]}"}]}}]}`)
	transcript, err := parseGoogleTranscript(payload)
	if err != nil {
		t.Fatal(err)
	}
	if transcript.Partial || transcript.Segments[0].Speech != "" || !strings.Contains(transcript.Text, "Visual: A cursor opens Settings") {
		t.Fatalf("transcript = %#v", transcript)
	}
}

func TestParseGoogleTranscriptRepresentsContentEmptyVideoWithoutSpeech(t *testing.T) {
	payload := []byte(`{"candidates":[{"content":{"parts":[{"text":"{\"summary\":\"\",\"language\":\"\",\"duration_ms\":1000,\"content_empty\":true,\"segments\":[{\"start_ms\":0,\"end_ms\":1000,\"speech\":\"\",\"audio\":\"\",\"visual\":\"No meaningful visual or auditory content was detected.\",\"on_screen_text\":\"\"}]}"}]}}]}`)
	transcript, err := parseGoogleTranscript(payload)
	if err != nil {
		t.Fatal(err)
	}
	if transcript.Partial || !transcript.ContentEmpty || transcript.Segments[0].Speech != "" || !strings.Contains(transcript.Text, "No meaningful visual or auditory content") {
		t.Fatalf("transcript = %#v", transcript)
	}
}

func TestParseGoogleTranscriptLabelsMissingTimelinePartial(t *testing.T) {
	payload := []byte(`{"candidates":[{"content":{"parts":[{"text":"{\"summary\":\"hello\",\"duration_ms\":1000,\"segments\":[]}"}]}}]}`)
	transcript, err := parseGoogleTranscript(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !transcript.Partial {
		t.Fatal("incomplete structured transcript must be partial")
	}
}

func TestParseGoogleTranscriptRejectsModelSuppliedDerivedText(t *testing.T) {
	payload := []byte(`{"candidates":[{"content":{"parts":[{"text":"{\"text\":\"forged\",\"summary\":\"\",\"language\":\"\",\"duration_ms\":1000,\"content_empty\":false,\"segments\":[{\"start_ms\":0,\"end_ms\":1000,\"visual\":\"A frame\"}]}"}]}}]}`)
	if _, err := parseGoogleTranscript(payload); err == nil {
		t.Fatal("provider full text must not be accepted by the v2 schema")
	}
}

func TestParseGoogleTranscriptRejectsUnknownSegmentFields(t *testing.T) {
	payload := []byte(`{"candidates":[{"content":{"parts":[{"text":"{\"summary\":\"\",\"language\":\"\",\"duration_ms\":1000,\"content_empty\":false,\"segments\":[{\"start_ms\":0,\"end_ms\":1000,\"visual\":\"A frame\",\"unexpected\":\"value\"}]}"}]}}]}`)
	if _, err := parseGoogleTranscript(payload); err == nil {
		t.Fatal("expected unknown segment field rejection")
	}
}

func TestParseGoogleTranscriptRejectsTrailingStructuredData(t *testing.T) {
	payload := []byte(`{"candidates":[{"content":{"parts":[{"text":"{\"summary\":\"\",\"language\":\"\",\"duration_ms\":1000,\"content_empty\":false,\"segments\":[{\"start_ms\":0,\"end_ms\":1000,\"visual\":\"A frame\"}]} {\"extra\":true}"}]}}]}`)
	if _, err := parseGoogleTranscript(payload); err == nil {
		t.Fatal("expected trailing structured data rejection")
	}
}

func TestParseGoogleTranscriptRejectsMalformedProviderPayloadWithoutEcho(t *testing.T) {
	secret := "sensitive-provider-payload"
	_, err := parseGoogleTranscript([]byte(`{"unexpected":"` + secret + `"}`))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error = %v", err)
	}
}
