package videogen

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
	"time"
)

type omniInteractionRequest struct {
	Model                 string              `json:"model"`
	Input                 any                 `json:"input"`
	PreviousInteractionID string              `json:"previous_interaction_id,omitempty"`
	ResponseFormat        *omniResponseFormat `json:"response_format,omitempty"`
}

type omniResponseFormat struct {
	Type        string `json:"type,omitempty"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
	Resolution  string `json:"resolution,omitempty"`
}

type omniInteractionResponse struct {
	ID     string      `json:"id"`
	Status string      `json:"status"`
	Model  string      `json:"model"`
	Steps  []omniStep  `json:"steps"`
	Error  *googleRPCStatus `json:"error,omitempty"`
}

type omniStep struct {
	Type    string        `json:"type"`
	Content []omniContent `json:"content"`
}

type omniContent struct {
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

func (s *Service) generateGoogleOmni(
	ctx context.Context,
	apiKey string,
	modelID string,
	prompt string,
	aspectRatio string,
	resolution string,
	source *ManagedVideoSource,
	img *ManagedVideoImage,
) (ManagedVideoResult, error) {
	reqBody := omniInteractionRequest{
		Model: modelID,
		ResponseFormat: &omniResponseFormat{
			Type:        "video",
			AspectRatio: aspectRatio,
			Resolution:  resolution,
		},
	}

	var imageContent map[string]any
	if img != nil && len(img.Bytes) > 0 {
		mimeType := strings.ToLower(strings.TrimSpace(img.MediaType))
		if mimeType == "" {
			mimeType = http.DetectContentType(img.Bytes)
		}
		imageContent = map[string]any{
			"type":      "image",
			"data":      base64.StdEncoding.EncodeToString(img.Bytes),
			"mime_type": mimeType,
		}
	}

	if source != nil && strings.TrimSpace(source.InteractionID) != "" {
		// Multi-turn conversational edit using previous_interaction_id
		reqBody.PreviousInteractionID = strings.TrimSpace(source.InteractionID)
		if imageContent != nil {
			reqBody.Input = []map[string]any{
				imageContent,
				{"type": "text", "text": prompt},
			}
		} else {
			reqBody.Input = prompt
		}
	} else if source != nil && len(source.Bytes) > 0 {
		// External video input via Google Files API upload
		fileURI, err := s.uploadGoogleFile(ctx, apiKey, source.Bytes, "video/mp4")
		if err != nil {
			return ManagedVideoResult{}, fmt.Errorf("upload video to Google for editing: %w", err)
		}
		inputs := []map[string]any{
			{"type": "video", "uri": fileURI},
			{"type": "text", "text": prompt},
		}
		if imageContent != nil {
			inputs = append(inputs, imageContent)
		}
		reqBody.Input = inputs
	} else if imageContent != nil {
		// Image-to-video with Omni
		reqBody.Input = []map[string]any{
			imageContent,
			{"type": "text", "text": prompt},
		}
	} else {
		// Fresh generation
		reqBody.Input = prompt
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return ManagedVideoResult{}, fmt.Errorf("marshal omni request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v1beta/interactions?key=%s", s.googleURL(), apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ManagedVideoResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", apiKey)

	resp, err := s.client().Do(httpReq)
	if err != nil {
		return ManagedVideoResult{}, fmt.Errorf("call google interactions api: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, managedVideoMaxBytes))
	if err != nil {
		return ManagedVideoResult{}, fmt.Errorf("read google interactions response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp struct {
			Error googleRPCStatus `json:"error"`
		}
		errMsg := string(bodyBytes)
		if err := json.Unmarshal(bodyBytes, &errResp); err == nil && errResp.Error.Message != "" {
			errMsg = errResp.Error.Message
		}
		if resp.StatusCode == 400 && (strings.Contains(errMsg, "content_blocked") || strings.Contains(errMsg, "content_policy")) && source != nil && strings.TrimSpace(source.InteractionID) == "" {
			return ManagedVideoResult{}, fmt.Errorf("google omni api error (400): %s (editing uploaded external videos is restricted in the EU/EEA, UK, and Switzerland; generate the initial video with Gemini Omni Flash to enable multi-turn conversational editing in this region)", errMsg)
		}
		return ManagedVideoResult{}, fmt.Errorf("google omni api error (%d): %s", resp.StatusCode, errMsg)
	}

	var omniResp omniInteractionResponse
	if err := json.Unmarshal(bodyBytes, &omniResp); err != nil {
		return ManagedVideoResult{}, fmt.Errorf("decode google interactions response: %w", err)
	}
	if omniResp.Error != nil && omniResp.Error.Message != "" {
		return ManagedVideoResult{}, fmt.Errorf("google omni error: %s", omniResp.Error.Message)
	}

	videoBytes, err := s.extractOmniVideoBytes(ctx, apiKey, omniResp)
	if err != nil {
		return ManagedVideoResult{}, err
	}

	return ManagedVideoResult{
		Bytes:         videoBytes,
		MediaType:     "video/mp4",
		InteractionID: omniResp.ID,
		Model:         modelID,
		Provider:      ProviderGoogleGemini,
	}, nil
}

func (s *Service) extractOmniVideoBytes(ctx context.Context, apiKey string, resp omniInteractionResponse) ([]byte, error) {
	for _, step := range resp.Steps {
		if step.Type != "model_output" {
			continue
		}
		for _, item := range step.Content {
			if strings.EqualFold(item.Type, "video") || strings.HasPrefix(item.MIMEType, "video/") {
				if item.Data != "" {
					decoded, err := base64.StdEncoding.DecodeString(item.Data)
					if err != nil {
						return nil, fmt.Errorf("decode omni video base64: %w", err)
					}
					return decoded, nil
				}
				if item.URI != "" {
					return s.downloadGoogleFile(ctx, apiKey, item.URI)
				}
			}
		}
	}
	return nil, errors.New("google omni response contained no video output")
}

func (s *Service) uploadGoogleFile(ctx context.Context, apiKey string, videoData []byte, mimeType string) (string, error) {
	metadata, _ := json.Marshal(map[string]any{"file": map[string]string{"display_name": "swarm-video-edit-source"}})
	startURL := fmt.Sprintf("%s/upload/v1beta/files?key=%s", s.googleURL(), apiKey)
	startReq, err := http.NewRequestWithContext(ctx, http.MethodPost, startURL, bytes.NewReader(metadata))
	if err != nil {
		return "", err
	}
	startReq.Header.Set("x-goog-api-key", apiKey)
	startReq.Header.Set("Content-Type", "application/json")
	startReq.Header.Set("X-Goog-Upload-Protocol", "resumable")
	startReq.Header.Set("X-Goog-Upload-Command", "start")
	startReq.Header.Set("X-Goog-Upload-Header-Content-Length", fmt.Sprint(len(videoData)))
	startReq.Header.Set("X-Goog-Upload-Header-Content-Type", mimeType)

	startResp, err := s.client().Do(startReq)
	if err != nil {
		return "", fmt.Errorf("start google files upload: %w", err)
	}
	defer startResp.Body.Close()

	if startResp.StatusCode < 200 || startResp.StatusCode >= 300 {
		return "", fmt.Errorf("google files upload start failed (%d)", startResp.StatusCode)
	}

	uploadURL := strings.TrimSpace(startResp.Header.Get("X-Goog-Upload-URL"))
	if uploadURL == "" {
		return "", errors.New("google files upload did not return upload URL")
	}

	uploadReq, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(videoData))
	if err != nil {
		return "", err
	}
	uploadReq.Header.Set("Content-Type", mimeType)
	uploadReq.Header.Set("Content-Length", fmt.Sprint(len(videoData)))
	uploadReq.Header.Set("X-Goog-Upload-Offset", "0")
	uploadReq.Header.Set("X-Goog-Upload-Command", "upload, finalize")

	uploadResp, err := s.client().Do(uploadReq)
	if err != nil {
		return "", fmt.Errorf("finalize google files upload: %w", err)
	}
	defer uploadResp.Body.Close()

	if uploadResp.StatusCode < 200 || uploadResp.StatusCode >= 300 {
		return "", fmt.Errorf("google files upload failed (%d)", uploadResp.StatusCode)
	}

	var fileResp struct {
		File struct {
			Name  string `json:"name"`
			URI   string `json:"uri"`
			State string `json:"state"`
		} `json:"file"`
	}
	if err := json.NewDecoder(uploadResp.Body).Decode(&fileResp); err != nil || fileResp.File.URI == "" {
		return "", errors.New("google files upload returned invalid file metadata")
	}

	// Poll until ACTIVE if processing
	if strings.EqualFold(fileResp.File.State, "PROCESSING") {
		return s.pollGoogleFileActive(ctx, apiKey, fileResp.File.Name, fileResp.File.URI)
	}

	return fileResp.File.URI, nil
}

func (s *Service) pollGoogleFileActive(ctx context.Context, apiKey string, fileName, fallbackURI string) (string, error) {
	deadline := time.Now().Add(2 * time.Minute)
	pollURL := fmt.Sprintf("%s/v1beta/%s?key=%s", s.googleURL(), strings.TrimPrefix(fileName, "/"), apiKey)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(s.pollingInterval()):
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("x-goog-api-key", apiKey)

		resp, err := s.client().Do(req)
		if err != nil {
			continue
		}
		var statusResp struct {
			State string `json:"state"`
			URI   string `json:"uri"`
			Error *googleRPCStatus `json:"error,omitempty"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&statusResp)
		resp.Body.Close()

		if statusResp.Error != nil && statusResp.Error.Message != "" {
			return "", fmt.Errorf("google file processing failed: %s", statusResp.Error.Message)
		}
		if strings.EqualFold(statusResp.State, "ACTIVE") {
			if statusResp.URI != "" {
				return statusResp.URI, nil
			}
			return fallbackURI, nil
		}
	}
	return fallbackURI, nil
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
		return nil, fmt.Errorf("download google video: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download google video failed (%d)", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, managedVideoMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("read google video bytes: %w", err)
	}
	return data, nil
}
