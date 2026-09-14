package videogen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type openRouterVideoRequest struct {
	Model       string `json:"model"`
	Prompt      string `json:"prompt"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
	Duration    int    `json:"duration,omitempty"`
	Resolution  string `json:"resolution,omitempty"`
}

type openRouterVideoJobResponse struct {
	ID           string   `json:"id"`
	Status       string   `json:"status"`
	PollingURL   string   `json:"polling_url,omitempty"`
	UnsignedURLs []string `json:"unsigned_urls,omitempty"`
	Error        *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error,omitempty"`
}

func (s *Service) generateOpenRouter(
	ctx context.Context,
	apiKey string,
	modelID string,
	prompt string,
	aspectRatio string,
	resolution string,
	durationSeconds int,
) (ManagedVideoResult, error) {
	reqBody := openRouterVideoRequest{
		Model:       modelID,
		Prompt:      prompt,
		AspectRatio: aspectRatio,
		Duration:    durationSeconds,
		Resolution:  resolution,
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return ManagedVideoResult{}, fmt.Errorf("marshal openrouter video request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/v1/videos", s.openRouterURL())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ManagedVideoResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := s.client().Do(httpReq)
	if err != nil {
		return ManagedVideoResult{}, fmt.Errorf("call openrouter video api: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, managedVideoMaxBytes))
	if err != nil {
		return ManagedVideoResult{}, fmt.Errorf("read openrouter video response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ManagedVideoResult{}, fmt.Errorf("openrouter video api error (%d): %s", resp.StatusCode, string(bodyBytes))
	}

	var jobResp openRouterVideoJobResponse
	if err := json.Unmarshal(bodyBytes, &jobResp); err != nil {
		return ManagedVideoResult{}, fmt.Errorf("decode openrouter video job: %w", err)
	}
	if jobResp.Error != nil && jobResp.Error.Message != "" {
		return ManagedVideoResult{}, fmt.Errorf("openrouter video error: %s", jobResp.Error.Message)
	}
	if jobResp.ID == "" {
		return ManagedVideoResult{}, errors.New("openrouter video did not return a job ID")
	}

	return s.pollOpenRouterJob(ctx, apiKey, modelID, jobResp.ID, jobResp.PollingURL)
}

func (s *Service) pollOpenRouterJob(ctx context.Context, apiKey, modelID, jobID, pollingURL string) (ManagedVideoResult, error) {
	deadline := time.Now().Add(s.pollingTimeout())
	pollEndpoint := fmt.Sprintf("%s/api/v1/videos/%s", s.openRouterURL(), jobID)
	if pollingURL != "" && strings.HasPrefix(pollingURL, "http") {
		pollEndpoint = pollingURL
	}

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ManagedVideoResult{}, ctx.Err()
		case <-time.After(s.pollingInterval()):
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollEndpoint, nil)
		if err != nil {
			return ManagedVideoResult{}, err
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)

		resp, err := s.client().Do(req)
		if err != nil {
			continue
		}
		var jobResp openRouterVideoJobResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&jobResp)
		resp.Body.Close()

		if decodeErr != nil {
			continue
		}
		if jobResp.Error != nil && jobResp.Error.Message != "" {
			return ManagedVideoResult{}, fmt.Errorf("openrouter video generation failed: %s", jobResp.Error.Message)
		}

		switch strings.ToLower(jobResp.Status) {
		case "completed":
			if len(jobResp.UnsignedURLs) == 0 || jobResp.UnsignedURLs[0] == "" {
				return ManagedVideoResult{}, errors.New("openrouter video completed but returned no video URL")
			}
			videoBytes, err := s.downloadURL(ctx, jobResp.UnsignedURLs[0])
			if err != nil {
				return ManagedVideoResult{}, fmt.Errorf("download openrouter video: %w", err)
			}
			return ManagedVideoResult{
				Bytes:     videoBytes,
				MediaType: "video/mp4",
				Model:     modelID,
				Provider:  ProviderOpenRouter,
			}, nil
		case "failed", "cancelled", "expired":
			return ManagedVideoResult{}, fmt.Errorf("openrouter video generation %s", jobResp.Status)
		}
	}
	return ManagedVideoResult{}, errors.New("openrouter video generation timed out")
}

func (s *Service) downloadURL(ctx context.Context, url string) ([]byte, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = fmt.Sprintf("%s/%s", s.openRouterURL(), strings.TrimPrefix(url, "/"))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download url returned %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, managedVideoMaxBytes))
}
