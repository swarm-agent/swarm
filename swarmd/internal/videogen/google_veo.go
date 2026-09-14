package videogen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"
	"time"
)

type veoPredictRequest struct {
	Instances  []veoInstance  `json:"instances"`
	Parameters *veoParameters `json:"parameters,omitempty"`
}

type veoImageInput struct {
	BytesBase64Encoded string `json:"bytesBase64Encoded,omitempty"`
	MIMEType           string `json:"mimeType,omitempty"`
}

type veoInstance struct {
	Prompt string         `json:"prompt"`
	Image  *veoImageInput `json:"image,omitempty"`
}

type veoParameters struct {
	AspectRatio     string `json:"aspectRatio,omitempty"`
	DurationSeconds int    `json:"durationSeconds,omitempty"`
	Resolution      string `json:"resolution,omitempty"`
}

type veoOperationResponse struct {
	Name     string           `json:"name"`
	Done     bool             `json:"done"`
	Error    *googleRPCStatus `json:"error,omitempty"`
	Response *veoResult       `json:"response,omitempty"`
}

type veoResult struct {
	GenerateVideoResponse *struct {
		GeneratedSamples []struct {
			Video struct {
				URI string `json:"uri"`
			} `json:"video"`
		} `json:"generatedSamples"`
	} `json:"generateVideoResponse,omitempty"`
	GeneratedVideos []struct {
		Video struct {
			URI        string `json:"uri"`
			VideoBytes string `json:"videoBytes"`
		} `json:"video"`
	} `json:"generatedVideos,omitempty"`
}

func (s *Service) generateGoogleVeo(
	ctx context.Context,
	apiKey string,
	modelID string,
	prompt string,
	aspectRatio string,
	resolution string,
	durationSeconds int,
	img *ManagedVideoImage,
) (ManagedVideoResult, error) {
	instance := veoInstance{Prompt: prompt}
	if img != nil && len(img.Bytes) > 0 {
		veoImg, err := prepareVeoImage(img)
		if err != nil {
			return ManagedVideoResult{}, err
		}
		instance.Image = veoImg
	}
	reqBody := veoPredictRequest{
		Instances: []veoInstance{instance},
		Parameters: &veoParameters{
			AspectRatio:     aspectRatio,
			DurationSeconds: durationSeconds,
			Resolution:      resolution,
		},
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return ManagedVideoResult{}, fmt.Errorf("marshal veo request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v1beta/models/%s:predictLongRunning?key=%s", s.googleURL(), modelID, apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ManagedVideoResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", apiKey)

	resp, err := s.client().Do(httpReq)
	if err != nil {
		return ManagedVideoResult{}, fmt.Errorf("call google veo api: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, managedVideoMaxBytes))
	if err != nil {
		return ManagedVideoResult{}, fmt.Errorf("read google veo response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp struct {
			Error googleRPCStatus `json:"error"`
		}
		if err := json.Unmarshal(bodyBytes, &errResp); err == nil && errResp.Error.Message != "" {
			return ManagedVideoResult{}, fmt.Errorf("google veo api error (%d): %s", resp.StatusCode, errResp.Error.Message)
		}
		return ManagedVideoResult{}, fmt.Errorf("google veo api error (%d): %s", resp.StatusCode, string(bodyBytes))
	}

	var opResp veoOperationResponse
	if err := json.Unmarshal(bodyBytes, &opResp); err != nil {
		return ManagedVideoResult{}, fmt.Errorf("decode google veo operation: %w", err)
	}
	if opResp.Error != nil && opResp.Error.Message != "" {
		return ManagedVideoResult{}, fmt.Errorf("google veo error: %s", opResp.Error.Message)
	}
	if opResp.Name == "" {
		return ManagedVideoResult{}, errors.New("google veo did not return an operation name")
	}

	return s.pollGoogleVeoOperation(ctx, apiKey, modelID, opResp.Name)
}

func (s *Service) pollGoogleVeoOperation(ctx context.Context, apiKey string, modelID, operationName string) (ManagedVideoResult, error) {
	deadline := time.Now().Add(s.pollingTimeout())
	pollURL := fmt.Sprintf("%s/v1beta/%s?key=%s", s.googleURL(), strings.TrimPrefix(operationName, "/"), apiKey)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ManagedVideoResult{}, ctx.Err()
		case <-time.After(s.pollingInterval()):
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if err != nil {
			return ManagedVideoResult{}, err
		}
		req.Header.Set("x-goog-api-key", apiKey)

		resp, err := s.client().Do(req)
		if err != nil {
			continue
		}
		var opResp veoOperationResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&opResp)
		resp.Body.Close()

		if decodeErr != nil {
			continue
		}
		if opResp.Error != nil && opResp.Error.Message != "" {
			return ManagedVideoResult{}, fmt.Errorf("google veo generation failed: %s", opResp.Error.Message)
		}
		if opResp.Done {
			videoURI := extractVeoVideoURI(opResp)
			if videoURI == "" {
				return ManagedVideoResult{}, errors.New("google veo operation finished but returned no video URI")
			}
			videoBytes, err := s.downloadGoogleFile(ctx, apiKey, videoURI)
			if err != nil {
				return ManagedVideoResult{}, fmt.Errorf("download veo video: %w", err)
			}
			return ManagedVideoResult{
				Bytes:     videoBytes,
				MediaType: "video/mp4",
				Model:     modelID,
				Provider:  ProviderGoogleGemini,
			}, nil
		}
	}
	return ManagedVideoResult{}, errors.New("google veo video generation timed out")
}

func extractVeoVideoURI(op veoOperationResponse) string {
	if op.Response == nil {
		return ""
	}
	if op.Response.GenerateVideoResponse != nil && len(op.Response.GenerateVideoResponse.GeneratedSamples) > 0 {
		return op.Response.GenerateVideoResponse.GeneratedSamples[0].Video.URI
	}
	if len(op.Response.GeneratedVideos) > 0 {
		return op.Response.GeneratedVideos[0].Video.URI
	}
	return ""
}

func prepareVeoImage(img *ManagedVideoImage) (*veoImageInput, error) {
	if img == nil || len(img.Bytes) == 0 {
		return nil, errors.New("image bytes are empty")
	}
	mimeType := strings.ToLower(strings.TrimSpace(img.MediaType))
	if mimeType == "image/jpeg" || mimeType == "image/jpg" {
		return &veoImageInput{
			BytesBase64Encoded: base64.StdEncoding.EncodeToString(img.Bytes),
			MIMEType:           "image/jpeg",
		}, nil
	}
	if mimeType == "image/png" {
		return &veoImageInput{
			BytesBase64Encoded: base64.StdEncoding.EncodeToString(img.Bytes),
			MIMEType:           "image/png",
		}, nil
	}
	decoded, _, err := image.Decode(bytes.NewReader(img.Bytes))
	if err != nil {
		detected := http.DetectContentType(img.Bytes)
		if detected == "image/jpeg" || detected == "image/png" {
			return &veoImageInput{
				BytesBase64Encoded: base64.StdEncoding.EncodeToString(img.Bytes),
				MIMEType:           detected,
			}, nil
		}
		if mimeType == "image/svg+xml" || (len(img.Bytes) > 4 && strings.Contains(string(img.Bytes[:min(len(img.Bytes), 256)]), "<svg")) {
			return nil, errors.New("vector SVG images must be rasterized to PNG or JPEG before passing to video generation")
		}
		return nil, fmt.Errorf("unsupported image format %q for Veo: %w", mimeType, err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, decoded); err != nil {
		return nil, fmt.Errorf("convert image to PNG for Veo: %w", err)
	}
	return &veoImageInput{
		BytesBase64Encoded: base64.StdEncoding.EncodeToString(buf.Bytes()),
		MIMEType:           "image/png",
	}, nil
}
