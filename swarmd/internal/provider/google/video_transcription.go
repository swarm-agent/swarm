package google

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/privacy"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videotranscription"
)

const (
	googleFileAPIBase             = "https://generativelanguage.googleapis.com"
	maxGoogleTranscriptBytes      = 4 << 20
	maxGoogleProviderErrorBody    = 32 << 10
	googleFilePollInterval        = 5 * time.Second
	googleFilePollAttempts        = 80
	googleFileNotFoundMaxAttempts = 5
	maxGoogleFrameBatchBytes      = 40 << 20
)

type VideoTranscriptionAdapter struct {
	authStore    *pebblestore.AuthStore
	httpClient   *http.Client
	baseURL      string
	logger       *slog.Logger
	pollInterval time.Duration
}

func NewVideoTranscriptionAdapter(authStore *pebblestore.AuthStore) *VideoTranscriptionAdapter {
	return &VideoTranscriptionAdapter{
		authStore: authStore, baseURL: googleFileAPIBase, logger: slog.Default(), pollInterval: googleFilePollInterval,
		httpClient: &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

type googleRPCStatus struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Status  string `json:"status,omitempty"`
}

type googleUploadedFile struct {
	Name       string          `json:"name"`
	URI        string          `json:"uri"`
	State      string          `json:"state"`
	Expiration string          `json:"expiration_time"`
	Error      googleRPCStatus `json:"error,omitempty"`
}

type googleFileResponse struct {
	File googleUploadedFile `json:"file"`
}

type googleAPIErrorResponse struct {
	Error googleRPCStatus `json:"error"`
}

func (a *VideoTranscriptionAdapter) AnalyzeFrameBatch(ctx context.Context, request videotranscription.FrameBatchRequest) ([]videotranscription.FrameObservation, error) {
	apiKey, err := a.authorizedAPIKey(ctx, request.AccountScopeID)
	if err != nil {
		return nil, err
	}
	if len(request.Frames) == 0 || len(request.Frames) > videotranscription.DeterministicFrameBatchSize {
		return nil, fmt.Errorf("google frame analysis requires between 1 and %d frames", videotranscription.DeterministicFrameBatchSize)
	}
	parts := make([]any, 0, len(request.Frames)*2+1)
	frameIDs := make([]string, 0, len(request.Frames))
	totalBytes := int64(0)
	for _, frame := range request.Frames {
		if frame.SizeBytes <= 0 || frame.SizeBytes > 2<<20 || frame.MIMEType != "image/jpeg" || strings.TrimSpace(frame.ID) == "" {
			return nil, errors.New("google frame analysis received an invalid deterministic frame")
		}
		payload, readErr := os.ReadFile(frame.PrivatePath)
		if readErr != nil || int64(len(payload)) != frame.SizeBytes {
			return nil, errors.New("google frame analysis could not read a prepared frame")
		}
		totalBytes += int64(len(payload))
		if totalBytes > maxGoogleFrameBatchBytes {
			return nil, errors.New("google frame analysis batch exceeds the bounded request limit")
		}
		frameIDs = append(frameIDs, frame.ID)
		parts = append(parts,
			map[string]any{"inlineData": map[string]string{"mimeType": frame.MIMEType, "data": base64.StdEncoding.EncodeToString(payload)}},
			map[string]string{"text": fmt.Sprintf("frame_id=%s timestamp_ms=%d", frame.ID, frame.TimestampMs)},
		)
	}
	parts = append(parts, map[string]string{"text": deterministicFramePrompt(request.FocusNotes, frameIDs)})
	payload, err := a.generateParts(ctx, apiKey, request.Model, parts)
	if err != nil {
		return nil, err
	}
	return parseGoogleFrameObservations(payload)
}

func (a *VideoTranscriptionAdapter) AnalyzeAudio(ctx context.Context, request videotranscription.AudioAnalysisRequest) (videotranscription.GeneratedTranscript, error) {
	apiKey, err := a.authorizedAPIKey(ctx, request.AccountScopeID)
	if err != nil {
		return videotranscription.GeneratedTranscript{}, err
	}
	if request.SizeBytes <= 0 || request.SizeBytes > 512<<20 || request.MIMEType != "audio/flac" || request.DurationMs <= 0 {
		return videotranscription.GeneratedTranscript{}, errors.New("google audio analysis received an invalid deterministic audio track")
	}
	file, err := os.Open(request.PrivatePath)
	if err != nil {
		return videotranscription.GeneratedTranscript{}, errors.New("google audio analysis could not open the prepared audio track")
	}
	defer file.Close()
	uploaded, err := a.upload(ctx, apiKey, videotranscription.TranscribeRequest{MIMEType: request.MIMEType, SizeBytes: request.SizeBytes, Source: file})
	if err != nil {
		return videotranscription.GeneratedTranscript{}, err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		a.delete(cleanupCtx, apiKey, uploaded.Name)
	}()
	uploaded, err = a.waitUntilActive(ctx, apiKey, uploaded)
	if err != nil {
		return videotranscription.GeneratedTranscript{}, err
	}
	prompt := deterministicAudioPrompt(request.DurationMs, request.FocusNotes)
	payload, err := a.generate(ctx, apiKey, request.Model, request.MIMEType, uploaded.URI, prompt)
	if err != nil {
		return videotranscription.GeneratedTranscript{}, err
	}
	return parseGoogleAudioTranscript(payload)
}

func (a *VideoTranscriptionAdapter) authorizedAPIKey(ctx context.Context, accountScopeID string) (string, error) {
	if a == nil || a.authStore == nil {
		return "", errors.New("google video transcription adapter is not configured")
	}
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok || principal.AccountScopeID != strings.TrimSpace(accountScopeID) {
		return "", errors.New("google video transcription requires authenticated account authority")
	}
	credential, ok, err := a.authStore.GetActiveCredentialForAccount(principal.AccountScopeID, "google")
	if err != nil {
		return "", err
	}
	if !ok || strings.TrimSpace(credential.APIKey) == "" {
		return "", errors.New("google video transcription credential is unavailable")
	}
	return credential.APIKey, nil
}

func (a *VideoTranscriptionAdapter) Transcribe(ctx context.Context, request videotranscription.TranscribeRequest) (videotranscription.GeneratedTranscript, error) {
	if request.Source == nil {
		return videotranscription.GeneratedTranscript{}, errors.New("google video transcription adapter is not configured")
	}
	apiKey, err := a.authorizedAPIKey(ctx, request.AccountScopeID)
	if err != nil {
		return videotranscription.GeneratedTranscript{}, err
	}
	if request.SizeBytes <= 0 || request.SizeBytes > pebblestore.SessionVideoAttachmentMaxBytes || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(request.MIMEType)), "video/") {
		return videotranscription.GeneratedTranscript{}, errors.New("video source exceeds the supported upload contract")
	}
	a.log(ctx, slog.LevelInfo, "google video transcription started", "size_bytes", request.SizeBytes, "mime_type", request.MIMEType)
	file, err := a.upload(ctx, apiKey, request)
	if err != nil {
		a.log(ctx, slog.LevelWarn, "google temporary video upload failed", "error", err)
		return videotranscription.GeneratedTranscript{}, err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		a.delete(cleanupCtx, apiKey, file.Name)
	}()
	a.log(ctx, slog.LevelInfo, "google temporary video upload finalized", "initial_state", safeGoogleStatus(file.State))
	file, err = a.waitUntilActive(ctx, apiKey, file)
	if err != nil {
		a.log(ctx, slog.LevelWarn, "google temporary video processing failed", "error", err)
		return videotranscription.GeneratedTranscript{}, err
	}
	payload, err := a.generate(ctx, apiKey, request.Model, request.MIMEType, file.URI, request.Prompt)
	if err != nil {
		a.log(ctx, slog.LevelWarn, "google video transcription generation failed", "error", err)
		return videotranscription.GeneratedTranscript{}, err
	}
	transcript, err := parseGoogleTranscript(payload)
	if err != nil {
		a.log(ctx, slog.LevelWarn, "google video transcription response rejected", "error", err, "response_bytes", len(payload))
		return videotranscription.GeneratedTranscript{}, err
	}
	a.log(ctx, slog.LevelInfo, "google video transcription completed", "response_bytes", len(payload), "segments", len(transcript.Segments), "partial", transcript.Partial)
	return transcript, nil
}

func (a *VideoTranscriptionAdapter) upload(ctx context.Context, apiKey string, request videotranscription.TranscribeRequest) (googleUploadedFile, error) {
	metadata, _ := json.Marshal(map[string]any{"file": map[string]string{"display_name": "swarm-video-transcription"}})
	start, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.baseURL, "/")+"/upload/v1beta/files", bytes.NewReader(metadata))
	if err != nil {
		return googleUploadedFile{}, err
	}
	start.Header.Set(googleAPIKeyHeader, apiKey)
	start.Header.Set("Content-Type", "application/json")
	start.Header.Set("X-Goog-Upload-Protocol", "resumable")
	start.Header.Set("X-Goog-Upload-Command", "start")
	start.Header.Set("X-Goog-Upload-Header-Content-Length", fmt.Sprint(request.SizeBytes))
	start.Header.Set("X-Goog-Upload-Header-Content-Type", request.MIMEType)
	response, err := a.client().Do(start)
	if err != nil {
		return googleUploadedFile{}, errors.New("google temporary video upload could not start")
	}
	startPayload, _ := io.ReadAll(io.LimitReader(response.Body, maxGoogleProviderErrorBody))
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return googleUploadedFile{}, googleHTTPStatusError("google temporary video upload start failed", response.StatusCode, parseGoogleAPIError(startPayload))
	}
	uploadURL := strings.TrimSpace(response.Header.Get("X-Goog-Upload-URL"))
	if uploadURL == "" {
		return googleUploadedFile{}, errors.New("google temporary video upload did not return a resumable target")
	}
	if _, err := request.Source.Seek(0, io.SeekStart); err != nil {
		return googleUploadedFile{}, errors.New("video source could not be rewound for upload")
	}
	upload, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, io.LimitReader(request.Source, request.SizeBytes+1))
	if err != nil {
		return googleUploadedFile{}, err
	}
	upload.Header.Set("Content-Type", request.MIMEType)
	upload.Header.Set("Content-Length", fmt.Sprint(request.SizeBytes))
	upload.Header.Set("X-Goog-Upload-Offset", "0")
	upload.Header.Set("X-Goog-Upload-Command", "upload, finalize")
	response, err = a.client().Do(upload)
	if err != nil {
		return googleUploadedFile{}, errors.New("google temporary video upload failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, maxGoogleProviderErrorBody))
		return googleUploadedFile{}, googleHTTPStatusError("google temporary video upload failed", response.StatusCode, parseGoogleAPIError(payload))
	}
	var result googleFileResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxGoogleProviderErrorBody)).Decode(&result); err != nil || result.File.Name == "" || result.File.URI == "" {
		return googleUploadedFile{}, errors.New("google temporary video upload returned an invalid file reference")
	}
	return result.File, nil
}

func (a *VideoTranscriptionAdapter) waitUntilActive(ctx context.Context, apiKey string, file googleUploadedFile) (googleUploadedFile, error) {
	started := time.Now()
	lastState := strings.ToUpper(strings.TrimSpace(file.State))
	for attempt := 0; attempt < googleFilePollAttempts; attempt++ {
		switch lastState {
		case "ACTIVE":
			a.log(ctx, slog.LevelInfo, "google temporary video became active", "attempt", attempt, "elapsed_ms", time.Since(started).Milliseconds())
			return file, nil
		case "FAILED":
			return googleUploadedFile{}, googleFileProcessingError(file.Error)
		}
		if err := waitForGoogleFilePoll(ctx, a.filePollInterval()); err != nil {
			return googleUploadedFile{}, err
		}
		endpoint := strings.TrimRight(a.baseURL, "/") + "/v1beta/" + strings.TrimLeft(file.Name, "/")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return googleUploadedFile{}, err
		}
		req.Header.Set(googleAPIKeyHeader, apiKey)
		response, err := a.client().Do(req)
		if err != nil {
			a.log(ctx, slog.LevelWarn, "google temporary video status request failed", "attempt", attempt+1, "elapsed_ms", time.Since(started).Milliseconds())
			if attempt < 2 {
				continue
			}
			return googleUploadedFile{}, errors.New("google temporary video processing status is unavailable")
		}
		payload, readErr := io.ReadAll(io.LimitReader(response.Body, maxGoogleProviderErrorBody+1))
		response.Body.Close()
		if readErr != nil {
			return googleUploadedFile{}, errors.New("google temporary video status response could not be read")
		}
		if len(payload) > maxGoogleProviderErrorBody {
			return googleUploadedFile{}, errors.New("google temporary video status response exceeded the bounded limit")
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			providerErr := parseGoogleAPIError(payload)
			a.log(ctx, slog.LevelWarn, "google temporary video status returned an error", "attempt", attempt+1, "elapsed_ms", time.Since(started).Milliseconds(), "http_status", response.StatusCode, "provider_code", providerErr.Code, "provider_status", providerErr.Status)
			if googleFileStatusRetryable(response.StatusCode, attempt) {
				continue
			}
			return googleUploadedFile{}, googleHTTPStatusError("google temporary video status failed", response.StatusCode, providerErr)
		}
		var result googleUploadedFile
		if err := json.Unmarshal(payload, &result); err != nil || strings.TrimSpace(result.Name) == "" {
			return googleUploadedFile{}, errors.New("google temporary video status was malformed")
		}
		state := strings.ToUpper(strings.TrimSpace(result.State))
		if state != lastState || attempt == 0 {
			a.log(ctx, slog.LevelInfo, "google temporary video state observed", "attempt", attempt+1, "elapsed_ms", time.Since(started).Milliseconds(), "state", state)
		}
		file, lastState = result, state
	}
	return googleUploadedFile{}, fmt.Errorf("google temporary video processing exceeded the bounded polling window last_state=%s", safeGoogleStatus(lastState))
}

func waitForGoogleFilePoll(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = googleFilePollInterval
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func googleFileStatusRetryable(statusCode, attempt int) bool {
	if statusCode == http.StatusNotFound {
		return attempt+1 < googleFileNotFoundMaxAttempts
	}
	return attempt < 2 && (statusCode == http.StatusTooManyRequests || statusCode >= 500)
}

func parseGoogleAPIError(payload []byte) googleRPCStatus {
	var envelope googleAPIErrorResponse
	if len(payload) == 0 || json.Unmarshal(payload, &envelope) != nil {
		return googleRPCStatus{}
	}
	envelope.Error.Status = safeGoogleStatus(envelope.Error.Status)
	envelope.Error.Message = safeGoogleProviderMessage(envelope.Error.Message)
	return envelope.Error
}

func googleHTTPStatusError(prefix string, statusCode int, providerErr googleRPCStatus) error {
	providerErr.Status = safeGoogleStatus(providerErr.Status)
	providerErr.Message = safeGoogleProviderMessage(providerErr.Message)
	message := fmt.Sprintf("%s status=%d", prefix, statusCode)
	if providerErr.Code != 0 {
		message += fmt.Sprintf(" provider_code=%d", providerErr.Code)
	}
	if providerErr.Status != "" {
		message += " provider_status=" + providerErr.Status
	}
	if providerErr.Message != "" {
		message += " provider_message=" + providerErr.Message
	}
	return errors.New(message)
}

func googleFileProcessingError(providerErr googleRPCStatus) error {
	message := "google temporary video processing failed"
	if providerErr.Code != 0 {
		message += fmt.Sprintf(" provider_code=%d", providerErr.Code)
	}
	if status := safeGoogleStatus(providerErr.Status); status != "" {
		message += " provider_status=" + status
	}
	if providerMessage := safeGoogleProviderMessage(providerErr.Message); providerMessage != "" {
		message += " provider_message=" + providerMessage
	}
	return errors.New(message)
}

func safeGoogleStatus(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	var safe strings.Builder
	for _, r := range value {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			safe.WriteRune(r)
		}
		if safe.Len() >= 64 {
			break
		}
	}
	return safe.String()
}

func safeGoogleProviderMessage(value string) string {
	value = privacy.SanitizeText(strings.Join(strings.Fields(value), " "))
	value = googleAPIKeyQueryPattern.ReplaceAllString(value, "${1}[REDACTED]")
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}

func (a *VideoTranscriptionAdapter) filePollInterval() time.Duration {
	if a != nil && a.pollInterval > 0 {
		return a.pollInterval
	}
	return googleFilePollInterval
}

func (a *VideoTranscriptionAdapter) log(ctx context.Context, level slog.Level, message string, args ...any) {
	logger := slog.Default()
	if a != nil && a.logger != nil {
		logger = a.logger
	}
	logger.Log(ctx, level, message, args...)
}

func (a *VideoTranscriptionAdapter) generate(ctx context.Context, apiKey, model, mimeType, fileURI, prompt string) ([]byte, error) {
	model = strings.TrimSpace(strings.TrimPrefix(model, "models/"))
	if model == "" {
		return nil, errors.New("google video transcription model is required")
	}
	body := map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{
			map[string]any{"file_data": map[string]string{"mime_type": mimeType, "file_uri": fileURI}},
			map[string]string{"text": prompt},
		}}},
		"generationConfig": map[string]any{"responseMimeType": "application/json", "temperature": 0},
	}
	return a.generateRequest(ctx, apiKey, model, body)
}

func (a *VideoTranscriptionAdapter) generateParts(ctx context.Context, apiKey, model string, parts []any) ([]byte, error) {
	model = strings.TrimSpace(strings.TrimPrefix(model, "models/"))
	if model == "" {
		return nil, errors.New("google video transcription model is required")
	}
	if len(parts) == 0 {
		return nil, errors.New("google video transcription request has no media parts")
	}
	body := map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": parts}},
		"generationConfig": map[string]any{
			"responseMimeType": "application/json",
			"temperature":      0,
		},
	}
	return a.generateRequest(ctx, apiKey, model, body)
}

func (a *VideoTranscriptionAdapter) generateRequest(ctx context.Context, apiKey, model string, body map[string]any) ([]byte, error) {
	raw, _ := json.Marshal(body)
	endpoint := strings.TrimRight(a.baseURL, "/") + "/v1beta/models/" + url.PathEscape(model) + ":generateContent"
	var lastStatus int
	var lastProviderErr googleRPCStatus
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set(googleAPIKeyHeader, apiKey)
		req.Header.Set("Content-Type", "application/json")
		response, err := a.client().Do(req)
		if err != nil {
			if attempt < 2 {
				continue
			}
			return nil, errors.New("google video transcription generation failed")
		}
		lastStatus = response.StatusCode
		payload, readErr := io.ReadAll(io.LimitReader(response.Body, maxGoogleTranscriptBytes+1))
		response.Body.Close()
		if readErr != nil {
			return nil, errors.New("google video transcription response could not be read")
		}
		if len(payload) > maxGoogleTranscriptBytes {
			return nil, errors.New("google video transcription response exceeded the bounded limit")
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return payload, nil
		}
		lastProviderErr = parseGoogleAPIError(payload)
		if attempt < 2 && (response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 500 * time.Millisecond):
			}
			continue
		}
		break
	}
	return nil, googleHTTPStatusError("google video transcription generation failed", lastStatus, lastProviderErr)
}

func (a *VideoTranscriptionAdapter) delete(ctx context.Context, apiKey, name string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimRight(a.baseURL, "/")+"/v1beta/"+strings.TrimLeft(name, "/"), nil)
	if err != nil {
		return
	}
	req.Header.Set(googleAPIKeyHeader, apiKey)
	response, err := a.client().Do(req)
	if err == nil {
		io.Copy(io.Discard, io.LimitReader(response.Body, maxGoogleProviderErrorBody))
		response.Body.Close()
	}
}

func (a *VideoTranscriptionAdapter) client() *http.Client {
	if a.httpClient != nil {
		return a.httpClient
	}
	return &http.Client{Timeout: 90 * time.Second}
}

func deterministicFramePrompt(focusNotes string, frameIDs []string) string {
	prompt := `Return only JSON with this exact shape: {"frames":[{"frame_id":"exact supplied ID","visual":"factual visible action/state or empty","on_screen_text":"meaningful visible text or empty"}]}. Return exactly one item for every supplied frame ID, in supplied order. Do not merge frames, invent IDs, timestamps, clicks, keystrokes, intent, audio, or unobserved state. Inspect each image independently while using neighboring images only as temporal context.`
	if notes, err := videotranscription.NormalizeFocusNotes(focusNotes); err == nil && notes != "" {
		prompt += "\nSubordinate focus guidance: " + notes
	}
	prompt += "\nRequired frame IDs: " + strings.Join(frameIDs, ",")
	return prompt
}

func deterministicAudioPrompt(durationMs int64, focusNotes string) string {
	prompt := fmt.Sprintf(`Analyze only the supplied audio track. Return only JSON with this exact shape: {"summary":"brief factual audio overview or empty","language":"BCP-47 for speech or empty","duration_ms":%d,"content_empty":false,"segments":[{"start_ms":0,"end_ms":1000,"speech":"spoken dialogue or empty","audio":"meaningful non-speech audio or empty","visual":"","on_screen_text":""}],"words":[{"text":"exact spoken word","start_ms":0,"end_ms":250,"confidence":0.95}]}. Use integer milliseconds, chronological non-overlapping segments and words, empty visual and on_screen_text fields, and do not infer visual events. Include every spoken word with its exact observed start and end time. Word confidence is optional and must be from 0 to 1. Do not describe beats, onsets, tempo, or waveform timing; deterministic local DSP owns those timelines. If there is no speech return an empty words array. If no meaningful audio exists, return one duration-spanning segment with visual exactly %q, content_empty=true, and an empty words array.`, durationMs, pebblestore.ContentEmptyVideoDescription)
	if notes, err := videotranscription.NormalizeFocusNotes(focusNotes); err == nil && notes != "" {
		prompt += "\nSubordinate focus guidance: " + notes
	}
	return prompt
}

func parseGoogleCandidateText(payload []byte) (string, error) {
	var envelope struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || len(envelope.Candidates) == 0 {
		return "", errors.New("google video transcription response was malformed")
	}
	var text string
	for _, part := range envelope.Candidates[0].Content.Parts {
		text += part.Text
	}
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	return text, nil
}

func parseGoogleFrameObservations(payload []byte) ([]videotranscription.FrameObservation, error) {
	text, err := parseGoogleCandidateText(payload)
	if err != nil {
		return nil, err
	}
	var structured struct {
		Frames []struct {
			FrameID      string `json:"frame_id"`
			Visual       string `json:"visual"`
			OnScreenText string `json:"on_screen_text"`
		} `json:"frames"`
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&structured); err != nil {
		return nil, errors.New("google frame analysis output did not match the structured schema")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("google frame analysis output contained trailing structured data")
	}
	observations := make([]videotranscription.FrameObservation, len(structured.Frames))
	for index, frame := range structured.Frames {
		observations[index] = videotranscription.FrameObservation{FrameID: frame.FrameID, Visual: frame.Visual, OnScreenText: frame.OnScreenText}
	}
	return observations, nil
}

func parseGoogleTranscript(payload []byte) (videotranscription.GeneratedTranscript, error) {
	return parseGoogleTranscriptWithWords(payload, false)
}

func parseGoogleAudioTranscript(payload []byte) (videotranscription.GeneratedTranscript, error) {
	return parseGoogleTranscriptWithWords(payload, true)
}

func parseGoogleTranscriptWithWords(payload []byte, wordProvenance bool) (videotranscription.GeneratedTranscript, error) {
	text, err := parseGoogleCandidateText(payload)
	if err != nil {
		return videotranscription.GeneratedTranscript{}, err
	}
	var structured struct {
		Summary      string `json:"summary"`
		Language     string `json:"language"`
		DurationMs   int64  `json:"duration_ms"`
		ContentEmpty bool   `json:"content_empty"`
		Segments     []struct {
			StartMs      int64  `json:"start_ms"`
			EndMs        int64  `json:"end_ms"`
			Speech       string `json:"speech"`
			Audio        string `json:"audio"`
			Visual       string `json:"visual"`
			OnScreenText string `json:"on_screen_text"`
		} `json:"segments"`
		Words []struct {
			Text string `json:"text"`
			StartMs int64 `json:"start_ms"`
			EndMs int64 `json:"end_ms"`
			Confidence *float64 `json:"confidence,omitempty"`
		} `json:"words,omitempty"`
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&structured); err != nil {
		return videotranscription.GeneratedTranscript{}, errors.New("google video transcription output did not match the structured schema")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return videotranscription.GeneratedTranscript{}, errors.New("google video transcription output contained trailing structured data")
	}
	segments := make([]pebblestore.NormalizedTranscriptSegment, len(structured.Segments))
	for index, segment := range structured.Segments {
		segments[index] = pebblestore.NormalizedTranscriptSegment{
			StartMs: segment.StartMs, EndMs: segment.EndMs, Speech: segment.Speech,
			Audio: segment.Audio, Visual: segment.Visual, OnScreenText: segment.OnScreenText,
		}
	}
	words := make([]pebblestore.NormalizedTranscriptWord, len(structured.Words))
	for index, word := range structured.Words {
		provenance := ""
		if wordProvenance { provenance = "google_audio_semantic.v1" }
		words[index] = pebblestore.NormalizedTranscriptWord{Text: word.Text, StartMs: word.StartMs, EndMs: word.EndMs, Confidence: word.Confidence, Provenance: provenance}
	}
	return videotranscription.NormalizeGeneratedTranscript(videotranscription.GeneratedTranscript{
		Segments: segments, Words: words, Language: structured.Language, DurationMs: structured.DurationMs,
		Summary: structured.Summary, ContentEmpty: structured.ContentEmpty,
	})
}
