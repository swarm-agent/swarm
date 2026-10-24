package audiogen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Purpose: the alignment wire boundary must send JSON Schema in Gemini's JSON
// schema field and retain all billed modalities, including thinking, on parse
// failures. A fake server proves the adapter without spending or credentials.
func TestSpeechAlignmentWire(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "paid-invalid"}[invalid], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				config := body["generationConfig"].(map[string]any)
				if _, ok := config["responseSchema"]; ok {
					t.Error("JSON Schema sent as protobuf schema")
				}
				schema, ok := config["responseJsonSchema"].(map[string]any)
				if !ok || schema["properties"] == nil {
					t.Error("missing JSON schema")
				}
				text := `{"duration_ms":1000,"segments":[{"start_ms":0,"end_ms":1000,"speech":"Hello"}],"words":[{"text":"Hello","start_ms":0,"end_ms":1000}]}`
				if invalid {
					text = "invalid"
				}
				json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]string{"text": text}}}}}, "usageMetadata": map[string]any{"candidatesTokenCount": 30, "thoughtsTokenCount": 70, "promptTokensDetails": []any{map[string]any{"modality": "TEXT", "tokenCount": 10}, map[string]any{"modality": "AUDIO", "tokenCount": 20}}}})
			}))
			defer server.Close()
			s := NewService(nil, nil, nil)
			s.SetBaseURL(server.URL)
			got, err := s.alignGoogleSpeech(context.Background(), "test-key", "transcriber", SpeechResult{Bytes: []byte{0, 0}, MediaType: "audio/wav"})
			if (err != nil) != invalid {
				t.Fatalf("error %v", err)
			}
			if got.Model != "transcriber" || got.PromptTokens != 10 || got.AudioInputTokens != 20 || got.OutputTokens != 100 {
				t.Fatalf("lost usage: %+v", got)
			}
			if !invalid && got.Timings.Granularity != "word" {
				t.Fatalf("timings %+v", got)
			}
		})
	}
}

// Purpose: paid but unusable generation must retain usage, and the supported
// model maximum PCM output must fit the bounded read. The HTTP adapter is the
// narrowest layer proving both the wire limit and rejected-output accounting.
func TestSpeechPaidOutput(t *testing.T) {
	for _, scenario := range []string{"finish", "format", "base64", "maximum", "oversize"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "oversize" {
					_, _ = w.Write([]byte(strings.Repeat("x", (48<<20)+1)))
					return
				}
				finish, mime, data := "STOP", "audio/L16;codec=pcm;rate=24000", "AAA="
				switch scenario {
				case "finish":
					finish = "MAX_TOKENS"
				case "format":
					mime = "audio/unknown"
				case "base64":
					data = "!!!"
				case "maximum":
					data = base64.StdEncoding.EncodeToString(make([]byte, 16384*48000/25))
				}
				json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"finishReason": finish, "content": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]string{"mimeType": mime, "data": data}}}}}}, "usageMetadata": map[string]int{"promptTokenCount": 12, "candidatesTokenCount": 16384}})
			}))
			defer server.Close()
			s := NewService(nil, nil, nil)
			s.SetBaseURL(server.URL)
			got, err := s.generateGoogleSpeech(context.Background(), "test-key", SpeechRequest{Model: "gemini-2.5-flash-preview-tts", Text: "Hello", Voice: "Kore"})
			if (err == nil) != (scenario == "maximum") {
				t.Fatalf("error %v", err)
			}
			if got.Model == "" {
				t.Fatal("lost incurred-request marker")
			}
			if scenario != "oversize" && (got.PromptTokens != 12 || got.AudioTokens != 16384) {
				t.Fatalf("lost usage %+v", got)
			}
			if scenario == "oversize" && (got.PromptTokens != 0 || got.AudioTokens != 0) {
				t.Fatal("invented usage")
			}
		})
	}
}
