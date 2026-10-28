package audiogen

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Purpose: alignGoogleSpeech must send bounded chunks through its real HTTP
// adapter, retain offset timings and gap notes, and sum usage including failed
// parses, including retained audio with no supplied duration after restart.
// Real local media tools and a stub model isolate this production path.
func TestAlignGoogleSpeechChunks(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s unavailable", tool)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "audio.wav")
	if out, err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=100", path).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	audio, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, duration := range []float64{100, 0} {
		t.Run(fmt.Sprintf("duration_%g", duration), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				text := `{"duration_ms":45000,"segments":[{"start_ms":1000,"end_ms":2000,"speech":"hello"}]}`
				if calls == 2 || calls == 3 {
					text = `invalid`
				}
				json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]string{"text": text}}}}}, "usageMetadata": map[string]any{"candidatesTokenCount": 3, "promptTokensDetails": []any{map[string]any{"modality": "AUDIO", "tokenCount": 5}, map[string]any{"modality": "TEXT", "tokenCount": 2}}}})
			}))
			defer server.Close()
			s := &Service{googleBaseURL: server.URL}
			got, err := s.alignGoogleSpeech(ctx, "test-key", "test-model", SpeechResult{Bytes: audio, MediaType: "audio/wav", DurationSeconds: duration})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 4 || !got.Timings.Partial || len(got.Timings.ChunkFailures) != 1 || len(got.Timings.Segments) != 2 || got.Timings.Segments[1].Start != 89 {
				t.Fatalf("calls=%d result=%+v", calls, got)
			}
			if got.PromptTokens != 8 || got.AudioInputTokens != 20 || got.OutputTokens != 12 {
				t.Fatalf("usage %+v", got)
			}
		})
	}
}

// Purpose: alignGoogleSpeech must fail before dispatch when an unknown-duration
// retained file cannot be probed, never send an unbounded input as a fallback.
// Invalid bytes and a local model server test this boundary without media tools.
func TestAlignGoogleSpeechUnknownDurationInvalidAudio(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusInternalServerError) }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := &Service{googleBaseURL: server.URL}
	_, err := s.alignGoogleSpeech(ctx, "test-key", "test-model", SpeechResult{Bytes: []byte("not audio"), MediaType: "audio/wav"})
	if err == nil || calls != 0 {
		t.Fatalf("error=%v model calls=%d", err, calls)
	}
}

// Purpose: a speech timing refusal names Google's own reason (bounded), so a
// failing job says what to change; a body without one keeps the plain status.
func TestAlignGoogleSpeechRefusalReason(t *testing.T) {
	for _, tc := range []struct {
		body, want string
	}{
		{`{"error":{"code":400,"message":"Invalid JSON payload received.\n Unknown name \"responseJsonSchema\"","status":"INVALID_ARGUMENT"}}`, `speech timing provider returned HTTP 400: Invalid JSON payload received. Unknown name "responseJsonSchema"`},
		{`{"error":{"code":400,"message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.BadRequest","fieldViolations":[{"field":"generation_config.response_json_schema","description":"too many states"}]}]}}`, "speech timing provider returned HTTP 400: Request contains an invalid argument. (generation_config.response_json_schema: too many states)"},
		{`not json`, "speech timing provider returned HTTP 400"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(tc.body))
		}))
		s := &Service{googleBaseURL: server.URL}
		_, _, err := s.alignGoogleSpeechChunk(context.Background(), "test-key", "test-model", SpeechResult{Bytes: []byte("RIFF"), MediaType: "audio/wav"})
		server.Close()
		if err == nil || err.Error() != tc.want {
			t.Fatalf("err = %v, want %q", err, tc.want)
		}
	}
}
