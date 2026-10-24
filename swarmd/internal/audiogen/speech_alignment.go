package audiogen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videotranscription"
)

// SpeechAlignment supports a future provider's native alignment without
// coupling generation to a particular transcription provider.
type SpeechAlignment interface {
	AlignSpeech(context.Context, SpeechRequest, SpeechResult) (SpeechAlignmentResult, error)
}
type SpeechAlignmentResult struct {
	Timings          SpeechTimings
	Model            string
	PromptTokens     int64
	AudioInputTokens int64
	OutputTokens     int64
}

// SpeechAlignmentModel resolves the existing account transcription selection.
func (s *Service) SpeechAlignmentModel(account string) (string, error) {
	if s == nil || s.uiSettings == nil || s.modelCatalog == nil {
		return "", errors.New("speech timing transcription is not configured")
	}
	settings, err := s.uiSettings.GetForAccount(account)
	if err != nil {
		return "", err
	}
	model := strings.TrimPrefix(settings.Media.TranscriptionModel, "google/")
	records, err := s.modelCatalog.ListCatalog("google", 2000)
	if err != nil {
		return "", err
	}
	for _, record := range records {
		if record.Model != model {
			continue
		}
		audio, text := false, false
		for _, input := range record.CatalogModalities.Inputs {
			audio = audio || input == "audio"
		}
		for _, output := range record.CatalogModalities.Outputs {
			text = text || output == "text"
		}
		if audio && text {
			return model, nil
		}
	}
	return "", errors.New("select a Google audio-input transcription model in Media settings for speech timings")
}

// AlignSpeech uses the existing structured-transcript schema and normalizer.
// Word timestamps are semantic estimates from the generated audio, not forced
// alignment. Missing words are explicitly represented as segment granularity.
func (s *Service) AlignSpeech(ctx context.Context, r SpeechRequest, audio SpeechResult) (SpeechAlignmentResult, error) {
	model, err := s.SpeechAlignmentModel(r.Principal.AccountScopeID)
	if err != nil {
		return SpeechAlignmentResult{}, err
	}
	key, err := s.getGoogleAPIKey(r.Principal.AccountScopeID)
	if err != nil {
		return SpeechAlignmentResult{}, err
	}
	if err := provideriface.CheckBillableDispatch(ctx); err != nil {
		return SpeechAlignmentResult{}, err
	}
	return s.alignGoogleSpeech(ctx, key, model, audio)
}

func (s *Service) alignGoogleSpeech(ctx context.Context, key, model string, audio SpeechResult) (SpeechAlignmentResult, error) {
	schema := videotranscription.StructuredTranscriptJSONSchema()
	schema["properties"].(map[string]any)["words"] = map[string]any{"type": "array", "items": map[string]any{"type": "object", "required": []string{"text", "start_ms", "end_ms"}, "properties": map[string]any{"text": map[string]string{"type": "string"}, "start_ms": map[string]string{"type": "integer"}, "end_ms": map[string]string{"type": "integer"}}}}
	body := map[string]any{
		"contents": []any{map[string]any{"parts": []any{
			map[string]any{"inlineData": map[string]string{"mimeType": audio.MediaType, "data": base64.StdEncoding.EncodeToString(audio.Bytes)}},
			map[string]string{"text": "Transcribe the supplied audio accurately. Return chronological speech segments and individual words with text, start_ms and end_ms measured from the audio. If word timestamps cannot be determined, omit words rather than inventing them. Use empty strings for visual and on_screen_text. Do not infer words from any external script."},
		}}},
		"generationConfig": map[string]any{"responseMimeType": "application/json", "responseJsonSchema": schema, "temperature": 0},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return SpeechAlignmentResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.googleURL()+"/v1beta/models/"+model+":generateContent", bytes.NewReader(payload))
	if err != nil {
		return SpeechAlignmentResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key)
	client := *s.client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return SpeechAlignmentResult{}, errors.New("speech timing provider request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return SpeechAlignmentResult{}, fmt.Errorf("speech timing provider returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return SpeechAlignmentResult{}, errors.New("speech timing response exceeds limit or could not be read")
	}
	var wire struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		Usage struct {
			Prompt  int64 `json:"promptTokenCount"`
			Details []struct {
				Modality string `json:"modality"`
				Count    int64  `json:"tokenCount"`
			} `json:"promptTokensDetails"`
			Output   int64 `json:"candidatesTokenCount"`
			Thoughts int64 `json:"thoughtsTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return SpeechAlignmentResult{}, errors.New("invalid timing response")
	}
	result := SpeechAlignmentResult{Model: model, OutputTokens: wire.Usage.Output + wire.Usage.Thoughts}
	for _, detail := range wire.Usage.Details {
		switch detail.Modality {
		case "AUDIO":
			result.AudioInputTokens += detail.Count
		case "TEXT":
			result.PromptTokens += detail.Count
		}
	}
	if len(wire.Candidates) != 1 || wire.Candidates[0].FinishReason != "STOP" {
		return result, errors.New("speech timing generation incomplete")
	}
	var text strings.Builder
	for _, part := range wire.Candidates[0].Content.Parts {
		text.WriteString(part.Text)
	}
	var transcript struct {
		Summary      string                                    `json:"summary"`
		Language     string                                    `json:"language"`
		DurationMs   int64                                     `json:"duration_ms"`
		ContentEmpty bool                                      `json:"content_empty"`
		Segments     []pebblestore.NormalizedTranscriptSegment `json:"segments"`
		Words        []pebblestore.NormalizedTranscriptWord    `json:"words"`
	}
	if err := json.Unmarshal([]byte(text.String()), &transcript); err != nil {
		return result, errors.New("invalid structured speech timings")
	}
	result.Timings, err = TimingsFromTranscript(videotranscription.GeneratedTranscript{Summary: transcript.Summary, Language: transcript.Language, DurationMs: transcript.DurationMs, ContentEmpty: transcript.ContentEmpty, Segments: transcript.Segments, Words: transcript.Words})
	return result, err
}
