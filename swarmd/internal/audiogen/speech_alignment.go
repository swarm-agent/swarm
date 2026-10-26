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
	"os"
	"sort"
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

// ErrNoSpeechTranscriptionModel means synthesis can proceed without timings.
var ErrNoSpeechTranscriptionModel = errors.New("timings unavailable: no transcription model")

// SpeechAlignmentModel resolves the account selection or the first eligible
// catalog model in display-name order, matching the media catalog ordering.
// Google is currently the only speech/alignment adapter and is preferred over
// introducing a dispatch to a provider without a supported alignment contract.
func (s *Service) SpeechAlignmentModel(account string) (string, error) {
	if s == nil || s.uiSettings == nil || s.modelCatalog == nil {
		return "", errors.New("speech timing transcription is not configured")
	}
	settings, err := s.uiSettings.GetForAccount(account)
	if err != nil {
		return "", err
	}
	model, err := videotranscription.NormalizeTranscriptionModel(settings.Media.TranscriptionModel)
	if err != nil {
		return "", err
	}
	records, err := s.modelCatalog.ListCatalog("google", 2000)
	if err != nil {
		return "", err
	}
	if model != "" {
		record, err := videotranscription.ResolveTranscriptionModel(model, records, "audio")
		return record.Model, err
	}
	records = append([]pebblestore.ModelCatalogRecord(nil), records...)
	sort.Slice(records, func(i, j int) bool {
		name := func(r pebblestore.ModelCatalogRecord) string {
			if strings.TrimSpace(r.DisplayName) != "" {
				return strings.TrimSpace(r.DisplayName)
			}
			return r.Model
		}
		if name(records[i]) == name(records[j]) {
			return records[i].Model < records[j].Model
		}
		return name(records[i]) < name(records[j])
	})
	for _, record := range records {
		if !speechTranscriptionCatalogRecord(record) {
			continue
		}
		if _, err := videotranscription.ResolveTranscriptionModel(record.Model, []pebblestore.ModelCatalogRecord{record}, "audio"); err != nil {
			continue
		}
		if _, err := s.getGoogleAPIKey(account); err != nil {
			return "", err
		}
		return record.Model, nil
	}
	return "", ErrNoSpeechTranscriptionModel
}

// speechTranscriptionCatalogRecord identifies models usable by the Google
// generateContent alignment adapter, excluding specialized/realtime endpoints.
func speechTranscriptionCatalogRecord(record pebblestore.ModelCatalogRecord) bool {
	if !strings.EqualFold(strings.TrimSpace(record.Provider), "google") {
		return false
	}
	joined := strings.ToLower(strings.Join([]string{record.Model, record.DisplayName, record.CatalogID, strings.Join(record.CatalogModalities.Categories, " ")}, " "))
	for _, excluded := range []string{"embedding", "veo", "robot", "research", "live", "imagen"} {
		if strings.Contains(joined, excluded) {
			return false
		}
	}
	return true
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
	result := SpeechAlignmentResult{Model: model}
	// Known short synthesis retains its inline path. Long inputs and retained
	// audio with no duration use trusted preparation to probe before chunking.
	request := videotranscription.AudioAnalysisRequest{DurationMs: int64(audio.DurationSeconds * 1000)}
	if audio.DurationSeconds <= 0 || request.DurationMs > 45000 {
		prepared, err := videotranscription.PrepareRegisteredAudio(ctx, bytes.NewReader(audio.Bytes), int64(len(audio.Bytes)))
		if err != nil {
			return result, err
		}
		defer prepared.Close()
		request.PrivatePath, request.DurationMs = prepared.FLACPath, prepared.DurationMs
	}
	transcript, err := videotranscription.AnalyzeAudioChunks(ctx, request, func(ctx context.Context, chunk videotranscription.AudioAnalysisRequest) (videotranscription.GeneratedTranscript, error) {
		input := audio
		if chunk.PrivatePath != "" {
			data, err := os.ReadFile(chunk.PrivatePath)
			if err != nil {
				return videotranscription.GeneratedTranscript{}, err
			}
			input.Bytes, input.MediaType = data, "audio/flac"
		}
		answer, usage, err := s.alignGoogleSpeechChunk(ctx, key, model, input)
		result.PromptTokens += usage.PromptTokens
		result.AudioInputTokens += usage.AudioInputTokens
		result.OutputTokens += usage.OutputTokens
		return answer, err
	})
	if err != nil {
		return result, err
	}
	result.Timings, err = TimingsFromTranscript(transcript)
	return result, err
}

func (s *Service) alignGoogleSpeechChunk(ctx context.Context, key, model string, audio SpeechResult) (answer videotranscription.GeneratedTranscript, usage SpeechAlignmentResult, err error) {
	var audioPart map[string]any
	if len(audio.Bytes) > speechInlineAudioLimit {
		file, cleanup, err := s.uploadSpeechAudio(ctx, key, audio)
		if err != nil {
			return answer, SpeechAlignmentResult{}, err
		}
		defer cleanup()
		audioPart = map[string]any{"fileData": map[string]string{"mimeType": audio.MediaType, "fileUri": file.URI}}
	} else {
		audioPart = map[string]any{"inlineData": map[string]string{"mimeType": audio.MediaType, "data": base64.StdEncoding.EncodeToString(audio.Bytes)}}
	}
	schema := videotranscription.StructuredTranscriptJSONSchema()
	schema["properties"].(map[string]any)["words"] = map[string]any{"type": "array", "items": map[string]any{"type": "object", "required": []string{"text", "start_ms", "end_ms"}, "properties": map[string]any{"text": map[string]string{"type": "string"}, "start_ms": map[string]string{"type": "integer"}, "end_ms": map[string]string{"type": "integer"}}}}
	body := map[string]any{
		"contents": []any{map[string]any{"parts": []any{
			audioPart,
			map[string]string{"text": "Transcribe the supplied audio accurately. Return chronological speech segments and individual words with text, start_ms and end_ms measured from the audio. If word timestamps cannot be determined, omit words rather than inventing them. Use empty strings for visual and on_screen_text. Do not infer words from any external script."},
		}}},
		"generationConfig": map[string]any{"responseMimeType": "application/json", "responseJsonSchema": schema, "temperature": 0},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return answer, SpeechAlignmentResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.googleURL()+"/v1beta/models/"+model+":generateContent", bytes.NewReader(payload))
	if err != nil {
		return answer, SpeechAlignmentResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key)
	client := *s.client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return answer, SpeechAlignmentResult{}, errors.New("speech timing provider request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return answer, SpeechAlignmentResult{}, fmt.Errorf("speech timing provider returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return answer, SpeechAlignmentResult{}, errors.New("speech timing response exceeds limit or could not be read")
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
		return answer, SpeechAlignmentResult{}, errors.New("invalid timing response")
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
		return answer, result, errors.New("speech timing generation incomplete")
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
		return answer, result, videotranscription.ErrInvalidTranscript
	}
	return videotranscription.GeneratedTranscript{Summary: transcript.Summary, Language: transcript.Language, DurationMs: transcript.DurationMs, ContentEmpty: transcript.ContentEmpty, Segments: transcript.Segments, Words: transcript.Words}, result, nil
}
