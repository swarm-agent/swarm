package audiogen

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Purpose: large accepted speech must use Files, not exceed Gemini's inline
// ceiling, and delete the temporary file even when transcription fails. The
// fake HTTP boundary also proves unsafe upload targets cannot receive audio.
func TestSpeechLargeAlignment(t *testing.T) {
	for _, scenario := range []string{"success", "transcription-failure", "unsafe-target"} {
		t.Run(scenario, func(t *testing.T) {
			uploaded, generated, deleted := 0, 0, 0
			audio := make([]byte, 16384*48000/25+44)
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/upload/v1beta/files":
					if r.Header.Get("X-Goog-Upload-Protocol") != "resumable" {
						t.Error("missing resumable protocol")
					}
					target := server.URL + "/upload-target"
					if scenario == "unsafe-target" {
						target = "https://invalid.example/upload"
					}
					w.Header().Set("X-Goog-Upload-URL", target)
				case "/upload-target":
					n, err := io.Copy(io.Discard, r.Body)
					if err != nil || n != int64(len(audio)) {
						t.Errorf("upload bytes %d: %v", n, err)
					}
					uploaded++
					json.NewEncoder(w).Encode(map[string]any{"file": map[string]string{"name": "files/speech-test", "uri": server.URL + "/v1beta/files/speech-test", "state": "ACTIVE"}})
				case "/v1beta/models/transcriber:generateContent":
					generated++
					data, err := io.ReadAll(r.Body)
					if err != nil || len(data) >= 20<<20 {
						t.Fatal("oversized inline request")
					}
					var body map[string]any
					json.Unmarshal(data, &body)
					part := body["contents"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)
					if part["inlineData"] != nil || part["fileData"] == nil {
						t.Error("missing uploaded file reference")
					}
					if scenario == "transcription-failure" {
						w.WriteHeader(400)
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]string{"text": `{"duration_ms":1000,"segments":[{"start_ms":0,"end_ms":1000,"speech":"Hello"}]}`}}}}}})
				case "/v1beta/files/speech-test":
					if r.Method != http.MethodDelete {
						t.Error("expected deletion")
					}
					deleted++
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			s := NewService(nil, nil, nil)
			s.SetBaseURL(server.URL)
			result, err := s.alignGoogleSpeech(context.Background(), "test-key", "transcriber", SpeechResult{Bytes: audio, MediaType: "audio/wav"})
			if (err == nil) != (scenario == "success") {
				t.Fatalf("error %v", err)
			}
			if scenario == "unsafe-target" {
				if uploaded+generated+deleted != 0 {
					t.Fatal("unsafe target dispatched")
				}
				return
			}
			if uploaded != 1 || generated != 1 || deleted != 1 {
				t.Fatalf("upload=%d generate=%d delete=%d", uploaded, generated, deleted)
			}
			if scenario == "success" && result.Timings.Granularity != "segment" {
				t.Fatal("missing timings")
			}
		})
	}
}
