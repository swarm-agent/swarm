package audiogen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videotranscription"
)

// Purpose: ValidateSpeech must reject unsupported or oversized inputs before
// any billable dispatch. Pure validation is the narrowest admission boundary.
func TestSpeechValidation(t *testing.T) {
	valid := SpeechRequest{Model: "gemini-2.5-flash-preview-tts", Text: "Hello", Voice: "Kore", Format: "wav"}
	if err := ValidateSpeech(valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*SpeechRequest){func(r *SpeechRequest) { r.Text = "" }, func(r *SpeechRequest) { r.Text = strings.Repeat("x", SpeechMaxPromptBytes+1) }, func(r *SpeechRequest) { r.Model = "lyria" }, func(r *SpeechRequest) { r.Voice = "clone" }, func(r *SpeechRequest) { r.Format = "mp3" }, func(r *SpeechRequest) { r.Style = strings.Repeat("x", SpeechMaxPromptBytes) }} {
		r := valid
		mutate(&r)
		if ValidateSpeech(r) == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
}

// Purpose: the Gemini adapter must send the requested voice/script, preserve
// provider usage and wrap real-format PCM, while rejecting incomplete output.
// A fake HTTP server tests wire behavior without credentials or paid calls.
func TestSpeechGoogleAdapter(t *testing.T) {
	finish := "STOP"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1beta/models/gemini-2.5-flash-preview-tts:generateContent" || r.Header.Get("x-goog-api-key") != "test-key" {
			t.Error("bad request")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		config := body["generationConfig"].(map[string]any)
		voice := config["speechConfig"].(map[string]any)["voiceConfig"].(map[string]any)["prebuiltVoiceConfig"].(map[string]any)["voiceName"]
		if voice != "Kore" {
			t.Error(voice)
		}
		json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"finishReason": finish, "content": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]string{"mimeType": "audio/L16;codec=pcm;rate=24000", "data": base64.StdEncoding.EncodeToString(make([]byte, 48000))}}}}}}, "usageMetadata": map[string]int{"promptTokenCount": 12, "candidatesTokenCount": 25}})
	}))
	defer server.Close()
	service := NewService(nil, nil, nil)
	service.SetBaseURL(server.URL)
	r := SpeechRequest{Model: "gemini-2.5-flash-preview-tts", Text: "Hello", Voice: "Kore"}
	got, err := service.generateGoogleSpeech(context.Background(), "test-key", r)
	if err != nil || len(got.Bytes) != 48044 || string(got.Bytes[:4]) != "RIFF" || got.PromptTokens != 12 || got.AudioTokens != 25 || got.DurationSeconds != 1 {
		t.Fatalf("result %+v %v", got, err)
	}
	finish = "MAX_TOKENS"
	if _, err = service.generateGoogleSpeech(context.Background(), "test-key", r); err == nil {
		t.Fatal("accepted truncated audio")
	}
	r.Voice = "unknown"
	if _, err = service.generateGoogleSpeech(context.Background(), "test-key", r); err == nil || calls != 2 {
		t.Fatal("invalid request dispatched")
	}
}

// Purpose: timing assembly must preserve measured word boundaries, explicitly
// label segment fallback, and reject malformed timelines rather than inventing
// alignment. The shared transcript normalizer is the production boundary.
func TestSpeechTimings(t *testing.T) {
	transcript := videotranscription.GeneratedTranscript{DurationMs: 1000, Segments: []pebblestore.NormalizedTranscriptSegment{{StartMs: 0, EndMs: 1000, Speech: "Hello"}}, Words: []pebblestore.NormalizedTranscriptWord{{Text: "Hello", StartMs: 100, EndMs: 800}}}
	got, err := TimingsFromTranscript(transcript)
	if err != nil || got.Granularity != "word" || got.Words[0].Start != .1 || got.Words[0].End != .8 {
		t.Fatalf("%+v %v", got, err)
	}
	transcript.Words = nil
	got, err = TimingsFromTranscript(transcript)
	if err != nil || got.Granularity != "segment" || len(got.Words) != 0 || len(got.Segments) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	transcript.Segments[0].StartMs = -1
	if _, err = TimingsFromTranscript(transcript); err == nil {
		t.Fatal("accepted invalid timeline")
	}
}

// Purpose: speech cost must multiply catalog token rates by provider usage,
// never by generation count alone, and must not fabricate missing pricing.
// EstimateMediaCostFromRecord is the exact receipt pricing boundary.
func TestSpeechCost(t *testing.T) {
	record := pebblestore.ModelCatalogRecord{Provider: "google", Model: "gemini-2.5-flash-preview-tts", Pricing: json.RawMessage(`{"billing":{"status":"verified","lines":[{"billable":"text_input","unit":"million_tokens","price_usd":0.5},{"billable":"audio_output","unit":"million_tokens","price_usd":10}]}}`)}
	opts := pebblestore.MediaCostEstimateOptions{Kind: "audio", Speech: true, PromptTokens: 100, AudioOutputTokens: 1000}
	got := pebblestore.EstimateMediaCostFromRecord(record, opts)
	if got.PriceStatus != "known" || math.Abs(got.CostUSD-.01005) > 1e-10 {
		t.Fatalf("%+v", got)
	}
	opts.AudioOutputTokens = 0
	got = pebblestore.EstimateMediaCostFromRecord(record, opts)
	if got.PriceStatus != "unknown" {
		t.Fatalf("%+v", got)
	}
}

// Purpose: transcription billing must distinguish text and audio inputs and
// refuse unverified prices. This pure catalog estimator test proves the exact
// receipt cost calculation without paid transcription or ambient settings.
func TestSpeechTranscriptionCost(t *testing.T) {
	rec := pebblestore.ModelCatalogRecord{Pricing: json.RawMessage(`{"billing":{"status":"verified","lines":[{"billable":"text_input","unit":"million_tokens","price_usd":1},{"billable":"audio_input","unit":"million_tokens","price_usd":2},{"billable":"text_output","unit":"million_tokens","price_usd":3}]}}`)}
	opts := pebblestore.MediaCostEstimateOptions{Kind: "audio", Transcription: true, PromptTokens: 100, AudioInputTokens: 200, TextOutputTokens: 300}
	got := pebblestore.EstimateMediaCostFromRecord(rec, opts)
	if got.PriceStatus != "known" || math.Abs(got.CostUSD-.0014) > 1e-10 {
		t.Fatalf("%+v", got)
	}
	rec.Pricing = json.RawMessage(strings.ReplaceAll(string(rec.Pricing), "verified", "unverified"))
	if got := pebblestore.EstimateMediaCostFromRecord(rec, opts); got.PriceStatus != "unknown" || got.CostUSD != 0 {
		t.Fatalf("%+v", got)
	}
}
