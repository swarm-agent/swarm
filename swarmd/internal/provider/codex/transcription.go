package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	audioTranscriptionsURL    = "https://api.openai.com/v1/audio/transcriptions"
	defaultTranscriptionModel = "gpt-4o-transcribe"
)

type Transcription struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Text     string `json:"text"`
}

func (c *Client) TranscribeAudio(ctx context.Context, audio []byte, model, language string) (Transcription, error) {
	if c == nil {
		return Transcription{}, errors.New("codex client is not configured")
	}
	if len(audio) == 0 {
		return Transcription{}, errors.New("audio payload is required")
	}

	record, err := c.ensureAuth(ctx)
	if err != nil {
		return Transcription{}, err
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultTranscriptionModel
	}
	language = strings.TrimSpace(language)
	if strings.EqualFold(strings.TrimSpace(record.Type), pebblestore.CodexAuthTypeOAuth) {
		msg := "codex stt requires API key auth; configure Codex API key in /auth"
		codexVoiceDebugEvent("transcription.auth_unsupported", map[string]any{
			"auth_type": strings.TrimSpace(record.Type),
			"reason":    msg,
		})
		return Transcription{}, errors.New(msg)
	}
	endpoint := audioTranscriptionsURL

	codexVoiceDebugEvent("transcription.request", map[string]any{
		"endpoint":       endpoint,
		"auth_type":      strings.TrimSpace(record.Type),
		"has_account_id": strings.TrimSpace(record.AccountID) != "",
		"model":          model,
		"language":       language,
		"audio_bytes":    len(audio),
	})

	payload, statusCode, err := c.sendTranscription(ctx, record, audio, model, language)
	if err != nil {
		codexVoiceDebugEvent("transcription.transport_error", map[string]any{
			"endpoint":    endpoint,
			"status_code": statusCode,
			"error":       sanitizeDiagnosticText(err.Error()),
		})
		return Transcription{}, err
	}
	if statusCode == 401 && record.Type == pebblestore.CodexAuthTypeOAuth {
		codexVoiceDebugEvent("transcription.oauth_refresh_attempt", map[string]any{
			"endpoint": endpoint,
		})
		refreshed, refreshErr := c.refreshOAuth(ctx, record.RefreshToken)
		if refreshErr != nil {
			codexVoiceDebugEvent("transcription.oauth_refresh_failed", map[string]any{
				"endpoint": endpoint,
				"error":    sanitizeDiagnosticText(refreshErr.Error()),
			})
			return Transcription{}, fmt.Errorf("codex transcription unauthorized and refresh failed: %w", refreshErr)
		}
		accountID := extractAccountIDFromToken(refreshed.AccessToken)
		record, err = c.authStore.UpdateOAuthCredential(record.Provider, record.ID, refreshed.AccessToken, refreshed.RefreshToken, refreshed.ExpiresAt, accountID)
		if err != nil {
			return Transcription{}, fmt.Errorf("persist refreshed codex oauth: %w", err)
		}
		payload, statusCode, err = c.sendTranscription(ctx, record, audio, model, language)
		if err != nil {
			return Transcription{}, err
		}
	}
	if statusCode >= 400 {
		detail := transcriptionFailureDetail(payload)
		codexVoiceDebugEvent("transcription.failed", map[string]any{
			"endpoint":    endpoint,
			"status_code": statusCode,
			"detail":      detail,
		})
		return Transcription{}, fmt.Errorf("codex transcription failed status=%d: %s", statusCode, detail)
	}
	text, err := extractTranscriptionText(payload)
	if err != nil {
		return Transcription{}, err
	}
	return Transcription{
		Provider: "codex",
		Model:    model,
		Text:     text,
	}, nil
}

func (c *Client) sendTranscription(ctx context.Context, record pebblestore.CodexAuthRecord, audio []byte, model, language string) (map[string]any, int, error) {
	body := bytes.NewBuffer(nil)
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("model", model); err != nil {
		return nil, 0, err
	}
	if language != "" {
		if err := writer.WriteField("language", language); err != nil {
			return nil, 0, err
		}
	}
	// Keep consistent JSON outputs from the OpenAI transcription endpoint.
	if err := writer.WriteField("response_format", "json"); err != nil {
		return nil, 0, err
	}
	part, err := writer.CreateFormFile("file", "audio.wav")
	if err != nil {
		return nil, 0, err
	}
	if _, err := part.Write(audio); err != nil {
		return nil, 0, err
	}
	if err := writer.Close(); err != nil {
		return nil, 0, err
	}

	endpoint := audioTranscriptionsURL

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+bearerToken(record))
	if record.Type == pebblestore.CodexAuthTypeOAuth && strings.TrimSpace(record.AccountID) != "" {
		httpReq.Header.Set("ChatGPT-Account-Id", strings.TrimSpace(record.AccountID))
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		codexVoiceDebugEvent("transcription.http_error", map[string]any{
			"endpoint": endpoint,
			"error":    sanitizeDiagnosticText(err.Error()),
		})
		return nil, 0, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		codexVoiceDebugEvent("transcription.read_error", map[string]any{
			"endpoint":    endpoint,
			"status_code": resp.StatusCode,
			"error":       sanitizeDiagnosticText(err.Error()),
		})
		return nil, resp.StatusCode, err
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if len(bytes.TrimSpace(raw)) == 0 {
		codexVoiceDebugEvent("transcription.response", map[string]any{
			"endpoint":       endpoint,
			"status_code":    resp.StatusCode,
			"content_type":   contentType,
			"response_bytes": 0,
			"payload_kind":   "empty",
		})
		return map[string]any{}, resp.StatusCode, nil
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		decoded = map[string]any{"raw_body": sanitizeDiagnosticText(string(raw))}
		codexVoiceDebugResponse(endpoint, resp.StatusCode, contentType, len(raw), decoded)
		return decoded, resp.StatusCode, nil
	}
	codexVoiceDebugResponse(endpoint, resp.StatusCode, contentType, len(raw), decoded)
	return decoded, resp.StatusCode, nil
}

func extractTranscriptionText(payload map[string]any) (string, error) {
	if len(payload) == 0 {
		return "", errors.New("transcription response is empty")
	}
	if text := strings.TrimSpace(asString(payload["text"])); text != "" {
		return text, nil
	}
	if text := strings.TrimSpace(asString(payload["transcript"])); text != "" {
		return text, nil
	}
	if nested, ok := payload["data"].(map[string]any); ok {
		if text := strings.TrimSpace(asString(nested["text"])); text != "" {
			return text, nil
		}
		if text := strings.TrimSpace(asString(nested["transcript"])); text != "" {
			return text, nil
		}
	}
	if segments, ok := payload["segments"].([]any); ok && len(segments) > 0 {
		parts := make([]string, 0, len(segments))
		for _, raw := range segments {
			segment, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			text := strings.TrimSpace(asString(segment["text"]))
			if text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " "), nil
		}
	}
	return "", fmt.Errorf("transcription response missing text field: %s", compactBody(payload))
}

func transcriptionFailureDetail(payload map[string]any) string {
	if len(payload) == 0 {
		return "empty response body"
	}
	if errObj, ok := payload["error"].(map[string]any); ok {
		if message := strings.TrimSpace(asString(errObj["message"])); message != "" {
			return truncateTranscriptionDetail(sanitizeDiagnosticText(message), 220)
		}
		if typ := strings.TrimSpace(asString(errObj["type"])); typ != "" {
			return "upstream error type: " + truncateTranscriptionDetail(sanitizeDiagnosticText(typ), 80)
		}
	}
	if message := strings.TrimSpace(asString(payload["message"])); message != "" {
		return truncateTranscriptionDetail(sanitizeDiagnosticText(message), 220)
	}
	rawBody := strings.TrimSpace(asString(payload["raw_body"]))
	if rawBody != "" {
		lower := strings.ToLower(rawBody)
		if strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype") {
			return "upstream returned HTML (likely auth/session rejection)"
		}
		return "upstream returned non-JSON response"
	}
	keys := sortedMapKeys(payload)
	if len(keys) > 0 {
		return "unexpected payload keys=" + strings.Join(keys, ",")
	}
	return "unexpected transcription response"
}

func truncateTranscriptionDetail(value string, max int) string {
	if max <= 0 {
		max = 220
	}
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return strings.TrimSpace(value[:max]) + "...[truncated]"
}

func codexVoiceDebugEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("SWARMD_VOICE_DEBUG")))
	switch value {
	case "1", "true", "yes", "on", "debug":
		return true
	default:
		return false
	}
}

func codexVoiceDebugEvent(event string, data map[string]any) {
	if !codexVoiceDebugEnabled() {
		return
	}
	event = strings.TrimSpace(event)
	if event == "" {
		event = "event"
	}
	clean := map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"event": event,
		"data":  sanitizeDiagnosticValue(data),
	}
	encoded, err := json.Marshal(clean)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "[swarmd.voice] event=%s encode_error=true\n", event)
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "[swarmd.voice] %s\n", string(encoded))
}

func codexVoiceDebugResponse(endpoint string, statusCode int, contentType string, responseBytes int, payload map[string]any) {
	if !codexVoiceDebugEnabled() {
		return
	}
	event := "transcription.response"
	if statusCode >= 400 {
		event = "transcription.response_error"
	}

	rawBodyChars := 0
	if rawBody := strings.TrimSpace(asString(payload["raw_body"])); rawBody != "" {
		rawBodyChars = len(rawBody)
	}
	keys := sortedMapKeys(payload)
	data := map[string]any{
		"endpoint":       endpoint,
		"status_code":    statusCode,
		"content_type":   strings.TrimSpace(contentType),
		"response_bytes": responseBytes,
		"payload_keys":   strings.Join(keys, ","),
		"raw_body_chars": rawBodyChars,
	}
	if statusCode >= 400 {
		data["failure_detail"] = transcriptionFailureDetail(payload)
	}
	codexVoiceDebugEvent(event, data)
}
