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
)

type lyriaInteractionRequest struct {
	Model                 string                `json:"model"`
	Input                 any                   `json:"input"`
	PreviousInteractionID string                `json:"previous_interaction_id,omitempty"`
	ResponseFormat        *lyriaResponseFormat  `json:"response_format,omitempty"`
}

type lyriaResponseFormat struct {
	Type string `json:"type,omitempty"`
}

type lyriaInteractionResponse struct {
	ID     string           `json:"id"`
	Status string           `json:"status"`
	Model  string           `json:"model"`
	Steps  []lyriaStep      `json:"steps"`
	Error  *googleRPCStatus `json:"error,omitempty"`
}

type lyriaStep struct {
	Type    string         `json:"type"`
	Content []lyriaContent `json:"content"`
}

type lyriaContent struct {
	Type     string `json:"type"`
	MIMEType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"`
	URI      string `json:"uri,omitempty"`
	Text     string `json:"text,omitempty"`
}

type googleRPCStatus struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Status  string `json:"status,omitempty"`
}

func (s *Service) generateGoogleLyria(
	ctx context.Context,
	apiKey string,
	modelID string,
	shapedPrompt string,
	durationSeconds int,
	req ManagedAudioRequest,
) (ManagedAudioResult, error) {
	reqBody := lyriaInteractionRequest{
		Model: modelID,
		ResponseFormat: &lyriaResponseFormat{
			Type: "audio",
		},
	}

	// Multimodal image inspiration support (Image-to-Music)
	var imageContent map[string]any
	if req.Image != nil && len(req.Image.Bytes) > 0 {
		mimeType := strings.ToLower(strings.TrimSpace(req.Image.MediaType))
		if mimeType == "" {
			mimeType = http.DetectContentType(req.Image.Bytes)
		}
		imageContent = map[string]any{
			"type":      "image",
			"data":      base64.StdEncoding.EncodeToString(req.Image.Bytes),
			"mime_type": mimeType,
		}
	}

	// Multi-turn conversational iteration support via previous_interaction_id
	if req.Source != nil && strings.TrimSpace(req.Source.InteractionID) != "" {
		reqBody.PreviousInteractionID = strings.TrimSpace(req.Source.InteractionID)
	}

	if imageContent != nil {
		reqBody.Input = []map[string]any{
			imageContent,
			{"type": "text", "text": shapedPrompt},
		}
	} else {
		reqBody.Input = shapedPrompt
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return ManagedAudioResult{}, fmt.Errorf("marshal lyria request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v1beta/interactions?key=%s", s.googleURL(), apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ManagedAudioResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", apiKey)

	resp, err := s.client().Do(httpReq)
	if err != nil {
		return ManagedAudioResult{}, fmt.Errorf("call google interactions api: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, managedAudioMaxBytes))
	if err != nil {
		return ManagedAudioResult{}, fmt.Errorf("read google interactions response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp struct {
			Error googleRPCStatus `json:"error"`
		}
		errMsg := string(bodyBytes)
		if err := json.Unmarshal(bodyBytes, &errResp); err == nil && errResp.Error.Message != "" {
			errMsg = errResp.Error.Message
		}
		return ManagedAudioResult{}, fmt.Errorf("google lyria api error (%d): %s", resp.StatusCode, errMsg)
	}

	var lyriaResp lyriaInteractionResponse
	if err := json.Unmarshal(bodyBytes, &lyriaResp); err != nil {
		return ManagedAudioResult{}, fmt.Errorf("decode google interactions response: %w", err)
	}
	if lyriaResp.Error != nil && lyriaResp.Error.Message != "" {
		return ManagedAudioResult{}, fmt.Errorf("google lyria error: %s", lyriaResp.Error.Message)
	}

	audioBytes, lyrics, err := s.extractLyriaOutput(ctx, apiKey, lyriaResp)
	if err != nil {
		return ManagedAudioResult{}, err
	}

	// Calculate initial expected duration based on model
	durationMs := 30000
	if strings.Contains(strings.ToLower(modelID), "3.5") || strings.Contains(strings.ToLower(modelID), "song") {
		if durationSeconds > 0 {
			durationMs = durationSeconds * 1000
		} else {
			durationMs = DefaultSongDurationSeconds * 1000
		}
	}

	// Check if trimming / exact duration normalization is requested
	targetSec := req.TargetDurationSeconds
	if targetSec <= 0 && req.DurationSeconds > 0 && req.DurationSeconds < ClipDurationLimitSeconds && strings.Contains(strings.ToLower(modelID), "clip") {
		targetSec = float64(req.DurationSeconds)
	}

	var trimmed bool
	if targetSec > 0 {
		trimmedBytes, didTrim, err := TrimAudioToDuration(ctx, s.CommandRunner(), audioBytes, targetSec, req.FadeOutSeconds)
		if err != nil {
			return ManagedAudioResult{}, fmt.Errorf("normalize audio duration: %w", err)
		}
		if didTrim {
			audioBytes = trimmedBytes
			durationMs = int(targetSec * 1000)
			trimmed = true
		}
	}

	return ManagedAudioResult{
		Bytes:         audioBytes,
		MediaType:     DefaultAudioMIMEType,
		InteractionID: lyriaResp.ID,
		Model:         modelID,
		Provider:      ProviderGoogleGemini,
		DurationMs:    durationMs,
		Lyrics:        lyrics,
		Metadata: AudioMetadata{
			Prompt:                req.Prompt,
			ArrangementPrompt:     shapedPrompt,
			TargetDurationSeconds: targetSec,
			ActualDurationMs:      durationMs,
			Model:                 modelID,
			Provider:              ProviderGoogleGemini,
			InteractionID:         lyriaResp.ID,
			PreviousInteractionID: reqBody.PreviousInteractionID,
			Lyrics:                lyrics,
			HasImageInspiration:   req.Image != nil && len(req.Image.Bytes) > 0,
			Trimmed:               trimmed,
		},
	}, nil
}

func (s *Service) extractLyriaOutput(ctx context.Context, apiKey string, resp lyriaInteractionResponse) ([]byte, string, error) {
	var audioBytes []byte
	var lyrics string

	for _, step := range resp.Steps {
		if step.Type != "model_output" {
			continue
		}
		for _, item := range step.Content {
			if strings.EqualFold(item.Type, "audio") || strings.HasPrefix(item.MIMEType, "audio/") {
				if item.Data != "" {
					decoded, err := base64.StdEncoding.DecodeString(item.Data)
					if err != nil {
						return nil, "", fmt.Errorf("decode lyria audio base64: %w", err)
					}
					audioBytes = decoded
				} else if item.URI != "" {
					downloaded, err := s.downloadGoogleFile(ctx, apiKey, item.URI)
					if err != nil {
						return nil, "", err
					}
					audioBytes = downloaded
				}
			} else if strings.EqualFold(item.Type, "text") || (item.Text != "" && item.Type != "audio") {
				lyrics = strings.TrimSpace(item.Text)
			}
		}
	}

	if len(audioBytes) == 0 {
		return nil, "", errors.New("google lyria response contained no audio output")
	}

	return audioBytes, lyrics, nil
}

func (s *Service) downloadGoogleFile(ctx context.Context, apiKey string, fileURI string) ([]byte, error) {
	if !strings.HasPrefix(fileURI, "http://") && !strings.HasPrefix(fileURI, "https://") {
		fileURI = fmt.Sprintf("%s/%s", s.googleURL(), strings.TrimPrefix(fileURI, "/"))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", apiKey)

	resp, err := s.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("download google audio: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download google audio failed (%d)", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, managedAudioMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("read google audio bytes: %w", err)
	}
	return data, nil
}
