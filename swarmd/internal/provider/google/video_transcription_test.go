package google

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGenerateUsesPortableJSONModeForVideo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-video:generateContent" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		config, _ := body["generationConfig"].(map[string]any)
		if config["responseMimeType"] != "application/json" {
			t.Fatalf("generationConfig = %#v", config)
		}
		if _, present := config["responseJsonSchema"]; present {
			t.Fatalf("video generation must not send a model-specific JSON schema: %#v", config)
		}
		contents, _ := body["contents"].([]any)
		content, _ := contents[0].(map[string]any)
		parts, _ := content["parts"].([]any)
		filePart, _ := parts[0].(map[string]any)
		fileData, _ := filePart["file_data"].(map[string]any)
		if fileData["mime_type"] != "video/mp4" || fileData["file_uri"] != "https://example.invalid/file" {
			t.Fatalf("file_data = %#v", fileData)
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{}"}]}}]}`))
	}))
	defer server.Close()

	adapter := &VideoTranscriptionAdapter{baseURL: server.URL, httpClient: server.Client()}
	if _, err := adapter.generate(context.Background(), "test-key", "gemini-video", "video/mp4", "https://example.invalid/file", "transcribe"); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateAudioUsesStructuredResponseSchema(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-audio:generateContent" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		config, _ := body["generationConfig"].(map[string]any)
		if config["responseMimeType"] != "application/json" {
			t.Fatalf("generationConfig = %#v", config)
		}
		schema, _ := config["responseSchema"].(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		if schema["type"] != "OBJECT" || properties["segments"] == nil || properties["words"] == nil {
			t.Fatalf("responseSchema = %#v", schema)
		}
		contents, _ := body["contents"].([]any)
		content, _ := contents[0].(map[string]any)
		parts, _ := content["parts"].([]any)
		fileData, _ := parts[0].(map[string]any)["file_data"].(map[string]any)
		if fileData["mime_type"] != "audio/flac" || fileData["file_uri"] != "https://example.invalid/audio" {
			t.Fatalf("file_data = %#v", fileData)
		}
		_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"{}"}]}}]}`))
	}))
	defer server.Close()

	adapter := &VideoTranscriptionAdapter{baseURL: server.URL, httpClient: server.Client()}
	if _, err := adapter.generateAudio(context.Background(), "test-key", "gemini-audio", "audio/flac", "https://example.invalid/audio", "transcribe"); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratePartsUsesGeminiInlineImageShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		contents, _ := body["contents"].([]any)
		content, _ := contents[0].(map[string]any)
		parts, _ := content["parts"].([]any)
		inline, _ := parts[0].(map[string]any)["inlineData"].(map[string]any)
		if inline["mimeType"] != "image/jpeg" || inline["data"] != "aW1hZ2U=" {
			t.Fatalf("parts = %#v", parts)
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{}"}]}}]}`))
	}))
	defer server.Close()
	adapter := &VideoTranscriptionAdapter{baseURL: server.URL, httpClient: server.Client()}
	_, err := adapter.generateParts(context.Background(), "test-key", "gemini-video", []any{map[string]any{"inlineData": map[string]string{"mimeType": "image/jpeg", "data": "aW1hZ2U="}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWaitUntilActiveRetriesTransientNotFoundAndDecodesDirectFile(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(googleAPIKeyHeader); got != "test-key" {
			t.Fatalf("api key header = %q", got)
		}
		switch requests.Add(1) {
		case 1, 2:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"File is not visible yet","status":"NOT_FOUND"}}`))
		default:
			_, _ = w.Write([]byte(`{"name":"files/test-video","uri":"https://example.invalid/file","state":"ACTIVE"}`))
		}
	}))
	defer server.Close()

	var logs bytes.Buffer
	adapter := &VideoTranscriptionAdapter{
		baseURL: server.URL, httpClient: server.Client(), pollInterval: time.Millisecond,
		logger: slog.New(slog.NewTextHandler(&logs, nil)),
	}
	file, err := adapter.waitUntilActive(context.Background(), "test-key", googleUploadedFile{Name: "files/test-video", URI: "https://example.invalid/file", State: "PROCESSING"})
	if err != nil {
		t.Fatal(err)
	}
	if file.State != "ACTIVE" || requests.Load() != 3 {
		t.Fatalf("file = %#v, requests = %d", file, requests.Load())
	}
	if text := logs.String(); !strings.Contains(text, "http_status=404") || !strings.Contains(text, "provider_status=NOT_FOUND") || !strings.Contains(text, "state=ACTIVE") {
		t.Fatalf("logs = %q", text)
	}
}

func TestWaitUntilActiveStopsAfterBoundedNotFoundWindowAndKeepsSafeDetails(t *testing.T) {
	var requests atomic.Int32
	secret := "super-secret-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"Requested entity was not found?key=` + secret + `","status":"NOT_FOUND"}}`))
	}))
	defer server.Close()

	adapter := &VideoTranscriptionAdapter{baseURL: server.URL, httpClient: server.Client(), pollInterval: time.Millisecond, logger: slog.New(slog.NewTextHandler(ioDiscard{}, nil))}
	_, err := adapter.waitUntilActive(context.Background(), "test-key", googleUploadedFile{Name: "files/test-video", State: "PROCESSING"})
	if err == nil {
		t.Fatal("expected bounded not-found failure")
	}
	if requests.Load() != googleFileNotFoundMaxAttempts {
		t.Fatalf("requests = %d", requests.Load())
	}
	if got := err.Error(); !strings.Contains(got, "status=404") || !strings.Contains(got, "provider_status=NOT_FOUND") || !strings.Contains(got, "provider_message=Requested entity was not found?key=[REDACTED]") || strings.Contains(got, secret) {
		t.Fatalf("error = %q", got)
	}
}

func TestGoogleHTTPStatusErrorSanitizesCredentialLikeProviderMessage(t *testing.T) {
	secret := "synthetic-test-credential"
	err := googleHTTPStatusError("google request failed", http.StatusBadRequest, googleRPCStatus{
		Code: http.StatusBadRequest, Status: "INVALID_ARGUMENT", Message: "authorization: Bearer " + secret,
	})
	if got := err.Error(); strings.Contains(got, secret) || !strings.Contains(got, "Bearer [redacted]") {
		t.Fatalf("error = %q", got)
	}
}

func TestWaitUntilActiveReportsFailedFileDetails(t *testing.T) {
	adapter := &VideoTranscriptionAdapter{pollInterval: time.Millisecond}
	_, err := adapter.waitUntilActive(context.Background(), "test-key", googleUploadedFile{
		Name: "files/test-video", State: "FAILED", Error: googleRPCStatus{Code: 13, Status: "INTERNAL", Message: "Video processing failed"},
	})
	if err == nil || !strings.Contains(err.Error(), "provider_code=13 provider_status=INTERNAL provider_message=Video processing failed") {
		t.Fatalf("error = %v", err)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func TestParseGoogleFrameObservationsUsesExactFrameIDs(t *testing.T) {
	payload := []byte(`{"candidates":[{"content":{"parts":[{"text":"{\"frames\":[{\"frame_id\":\"frame_000000000000\",\"visual\":\"Settings opens\",\"on_screen_text\":\"Media\"}]}"}]}}]}`)
	frames, err := parseGoogleFrameObservations(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || frames[0].FrameID != "frame_000000000000" || frames[0].OnScreenText != "Media" {
		t.Fatalf("frames = %#v", frames)
	}
}

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

func TestParseGoogleTranscriptNormalizesWordMilliseconds(t *testing.T) {
	payload := []byte(`{"candidates":[{"content":{"parts":[{"text":"{\"summary\":\"Greeting\",\"language\":\"en\",\"duration_ms\":1000,\"content_empty\":false,\"segments\":[{\"start_ms\":0,\"end_ms\":1000,\"speech\":\"hello world\",\"audio\":\"\",\"visual\":\"\",\"on_screen_text\":\"\"}],\"words\":[{\"text\":\"hello\",\"start_ms\":25,\"end_ms\":400,\"confidence\":0.9},{\"text\":\"world\",\"start_ms\":450,\"end_ms\":900}]}"}]}}]}`)
	transcript, err := parseGoogleAudioTranscript(payload)
	if err != nil {
		t.Fatal(err)
	}
	if transcript.Partial || len(transcript.Words) != 2 || transcript.Words[0].StartMs != 25 || transcript.Words[1].EndMs != 900 || transcript.Words[0].Provenance != "google_audio_semantic.v1" {
		t.Fatalf("transcript = %#v", transcript)
	}
}

func TestDeterministicAudioPromptSeparatesSemanticAndDSPTiming(t *testing.T) {
	prompt := deterministicAudioPrompt(2000, "")
	for _, required := range []string{"every spoken word", "start_ms", "end_ms", "deterministic local DSP owns those timelines", "empty words array"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q: %s", required, prompt)
		}
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

func TestParseGoogleCandidateTextReportsSanitizedPromptBlock(t *testing.T) {
	secret := "synthetic-secret"
	_, err := parseGoogleCandidateText([]byte(`{"promptFeedback":{"blockReason":"SAFETY","blockReasonMessage":"authorization: Bearer ` + secret + `"}}`))
	if err == nil || !strings.Contains(err.Error(), "prompt_block_reason=SAFETY") || !strings.Contains(err.Error(), "Bearer [redacted]") || strings.Contains(err.Error(), secret) {
		t.Fatalf("error = %v", err)
	}
}

func TestParseGoogleCandidateTextReportsSanitizedFinishDetails(t *testing.T) {
	secret := "synthetic-secret"
	_, err := parseGoogleCandidateText([]byte(`{"candidates":[{"finishReason":"MAX_TOKENS","finishMessage":"authorization: Bearer ` + secret + `","content":{"parts":[]}}]}`))
	if err == nil || !strings.Contains(err.Error(), "finish_reason=MAX_TOKENS") || !strings.Contains(err.Error(), "Bearer [redacted]") || strings.Contains(err.Error(), secret) {
		t.Fatalf("error = %v", err)
	}
}

func TestParseGoogleCandidateTextUsesLaterTextCandidate(t *testing.T) {
	text, err := parseGoogleCandidateText([]byte(`{"candidates":[{"finishReason":"SAFETY","content":{"parts":[]}},{"finishReason":"STOP","content":{"parts":[{"text":"{\"ok\":true}"}]}}]}`))
	if err != nil || text != `{"ok":true}` {
		t.Fatalf("text=%q err=%v", text, err)
	}
}
