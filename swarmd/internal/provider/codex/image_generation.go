package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// ImageGenerationRequest asks the Codex/ChatGPT backend to expose and invoke
// its built-in image_generation tool. This path intentionally requires Codex
// OAuth because API-key auth bypasses the Codex backend entitlement/tool
// normalization that makes image_generation available.
type ImageGenerationRequest struct {
	Model         string
	Prompt        string
	Size          string
	PartialImages int
	OnEvent       func(ImageGenerationStreamEvent)
}

type ImageGenerationStreamEventType string

const (
	ImageGenerationStreamEventStarted      ImageGenerationStreamEventType = "started"
	ImageGenerationStreamEventGenerating   ImageGenerationStreamEventType = "generating"
	ImageGenerationStreamEventPartialImage ImageGenerationStreamEventType = "partial_image"
	ImageGenerationStreamEventCompleted    ImageGenerationStreamEventType = "completed"
)

type ImageGenerationStreamEvent struct {
	Type              ImageGenerationStreamEventType `json:"type"`
	ItemID            string                         `json:"item_id,omitempty"`
	OutputIndex       int                            `json:"output_index,omitempty"`
	SequenceNumber    int                            `json:"sequence_number,omitempty"`
	PartialImageIndex int                            `json:"partial_image_index,omitempty"`
	PartialImageB64   string                         `json:"partial_image_b64,omitempty"`
	OutputFormat      string                         `json:"output_format,omitempty"`
	Size              string                         `json:"size,omitempty"`
	Quality           string                         `json:"quality,omitempty"`
	Background        string                         `json:"background,omitempty"`
}

type ImageGenerationPartialImage struct {
	ItemID            string `json:"item_id,omitempty"`
	OutputIndex       int    `json:"output_index,omitempty"`
	SequenceNumber    int    `json:"sequence_number,omitempty"`
	PartialImageIndex int    `json:"partial_image_index,omitempty"`
	Base64Image       string `json:"base64_image"`
	OutputFormat      string `json:"output_format,omitempty"`
	Size              string `json:"size,omitempty"`
	Quality           string `json:"quality,omitempty"`
	Background        string `json:"background,omitempty"`
}

type ImageGenerationResult struct {
	ResponseID       string
	Model            string
	CallID           string
	RevisedPrompt    string
	Base64Image      string
	DecodedPNG       []byte
	PartialImages    []ImageGenerationPartialImage
	ProviderResponse map[string]any
}

// ProviderResponseError preserves the decoded upstream provider response for API
// callers when image parsing fails. This is intentionally returned to the
// frontend so failed image-generation calls can be debugged from the HTTP
// response body instead of only server logs.
type ProviderResponseError struct {
	Err      error
	Response map[string]any
}

func (e *ProviderResponseError) Error() string {
	if e == nil || e.Err == nil {
		return "codex image generation failed"
	}
	return e.Err.Error()
}

func (e *ProviderResponseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *ProviderResponseError) ProviderResponseData() map[string]any {
	if e == nil {
		return nil
	}
	return e.Response
}

func ProviderResponseFromError(err error) (map[string]any, bool) {
	var providerErr interface{ ProviderResponseData() map[string]any }
	if errors.As(err, &providerErr) {
		response := providerErr.ProviderResponseData()
		return response, len(response) > 0
	}
	return nil, false
}

func (c *Client) GenerateImage(ctx context.Context, req ImageGenerationRequest) (ImageGenerationResult, error) {
	if c == nil {
		return ImageGenerationResult{}, errors.New("codex client is not configured")
	}
	record, err := c.ensureAuth(ctx)
	if err != nil {
		return ImageGenerationResult{}, err
	}
	if record.Type != pebblestore.CodexAuthTypeOAuth {
		return ImageGenerationResult{}, errors.New("codex image generation requires Codex OAuth auth")
	}

	payload, err := buildImageGenerationPayload(req)
	if err != nil {
		return ImageGenerationResult{}, err
	}
	onEvent := imageGenerationStreamCallback(req.OnEvent)
	emitImageGenerationStreamEvent(req.OnEvent, ImageGenerationStreamEvent{Type: ImageGenerationStreamEventStarted})
	decoded, statusCode, err := c.send(ctx, record, payload, onEvent)
	if err != nil {
		return ImageGenerationResult{}, err
	}
	if statusCode == http.StatusUnauthorized {
		refreshed, refreshErr := c.refreshOAuth(ctx, record.RefreshToken)
		if refreshErr != nil {
			return ImageGenerationResult{}, fmt.Errorf("codex image generation unauthorized and refresh failed: %w", refreshErr)
		}
		accountID := extractAccountIDFromToken(refreshed.AccessToken)
		record, err = c.authStore.UpdateOAuthCredential(record.Provider, record.ID, refreshed.AccessToken, refreshed.RefreshToken, refreshed.ExpiresAt, accountID)
		if err != nil {
			return ImageGenerationResult{}, fmt.Errorf("persist refreshed codex oauth: %w", err)
		}
		decoded, statusCode, err = c.send(ctx, record, payload, onEvent)
		if err != nil {
			return ImageGenerationResult{}, err
		}
	}
	if statusCode >= 400 {
		if transport, _ := extractCodexTransportMetadata(decoded); transport != "" {
			return ImageGenerationResult{}, &ProviderResponseError{Err: fmt.Errorf("codex image generation failed status=%d transport=%s body=%s", statusCode, transport, compactBody(decoded)), Response: decoded}
		}
		return ImageGenerationResult{}, &ProviderResponseError{Err: fmt.Errorf("codex image generation failed status=%d body=%s", statusCode, compactBody(decoded)), Response: decoded}
	}
	result, err := parseImageGenerationResult(decoded)
	if err != nil {
		return ImageGenerationResult{}, &ProviderResponseError{Err: err, Response: decoded}
	}
	result.ProviderResponse = decoded
	emitImageGenerationStreamEvent(req.OnEvent, ImageGenerationStreamEvent{Type: ImageGenerationStreamEventCompleted})
	return result, nil
}

func buildImageGenerationPayload(req ImageGenerationRequest) ([]byte, error) {
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		return nil, errors.New("model is required")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, errors.New("prompt is required")
	}
	if size := strings.TrimSpace(req.Size); size != "" && !strings.EqualFold(size, "auto") {
		prompt = prompt + "\n\nRequested output size: " + size + "."
	}
	partialImages := req.PartialImages
	if partialImages < 0 {
		partialImages = 0
	}
	if partialImages > 3 {
		partialImages = 3
	}
	imageTool := map[string]any{"type": "image_generation", "output_format": "png", "action": "generate", "partial_images": partialImages}
	body := map[string]any{
		"type":   "response.create",
		"model":  modelID,
		"stream": true,
		"store":  false,
		"input": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": prompt},
				},
			},
		},
		"tools":               []map[string]any{imageTool},
		"tool_choice":         map[string]any{"type": "image_generation"},
		"parallel_tool_calls": false,
		"instructions":        "Generate exactly one image that satisfies the user's prompt. Use the image_generation tool and return one completed image result. Do not answer with text only.",
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode image generation request: %w", err)
	}
	return encoded, nil
}

func imageGenerationStreamCallback(onEvent func(ImageGenerationStreamEvent)) func(StreamEvent) {
	if onEvent == nil {
		return nil
	}
	return func(event StreamEvent) {
		switch event.Type {
		case StreamEventImageGenerationPartialImage:
			emitImageGenerationStreamEvent(onEvent, ImageGenerationStreamEvent{
				Type:              ImageGenerationStreamEventPartialImage,
				ItemID:            event.ItemID,
				OutputIndex:       event.OutputIndex,
				SequenceNumber:    event.SequenceNumber,
				PartialImageIndex: event.PartialImageIndex,
				PartialImageB64:   event.PartialImageB64,
				OutputFormat:      event.OutputFormat,
				Size:              event.Size,
				Quality:           event.Quality,
				Background:        event.Background,
			})
		case StreamEventImageGenerationCallCompleted:
			emitImageGenerationStreamEvent(onEvent, ImageGenerationStreamEvent{
				Type:           ImageGenerationStreamEventGenerating,
				ItemID:         event.ItemID,
				OutputIndex:    event.OutputIndex,
				SequenceNumber: event.SequenceNumber,
			})
		}
	}
}

func emitImageGenerationStreamEvent(onEvent func(ImageGenerationStreamEvent), event ImageGenerationStreamEvent) {
	if onEvent != nil {
		onEvent(event)
	}
}

func parseImageGenerationResult(decoded map[string]any) (ImageGenerationResult, error) {
	responseObj := decoded
	usedNestedResponse := false
	if nested, ok := decoded["response"].(map[string]any); ok {
		responseObj = nested
		usedNestedResponse = true
	}
	items := asSlice(responseObj["output"])
	if len(items) == 0 {
		items = asSlice(decoded["output"])
	}
	partialImages := parseImageGenerationPartials(responseObj)
	if len(partialImages) == 0 && usedNestedResponse {
		partialImages = parseImageGenerationPartials(decoded)
	}
	foundImageGenerationCall := false
	var pendingStatuses []string
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || !strings.EqualFold(strings.TrimSpace(asString(item["type"])), "image_generation_call") {
			continue
		}
		foundImageGenerationCall = true
		status := strings.TrimSpace(asString(item["status"]))
		result := strings.TrimSpace(asString(item["result"]))
		if status != "" && !strings.EqualFold(status, "completed") {
			pendingStatuses = append(pendingStatuses, status)
			continue
		}
		if result == "" {
			if status == "" {
				pendingStatuses = append(pendingStatuses, "missing status")
			} else {
				pendingStatuses = append(pendingStatuses, status+" without result")
			}
			continue
		}
		if strings.Contains(result, "://") || strings.Contains(strings.ToLower(result), "base64,") {
			return ImageGenerationResult{}, errors.New("image generation result must be raw base64 data")
		}
		decodedImage, err := base64.StdEncoding.Strict().DecodeString(result)
		if err != nil {
			return ImageGenerationResult{}, fmt.Errorf("decode image generation result: %w", err)
		}
		if !looksLikePNG(decodedImage) {
			return ImageGenerationResult{}, errors.New("image generation result decoded but is not a PNG image")
		}
		callID := strings.TrimSpace(asString(item["id"]))
		if callID == "" {
			callID = strings.TrimSpace(asString(item["call_id"]))
		}
		if callID == "" {
			callID = "image_generation"
		}
		return ImageGenerationResult{
			ResponseID:       strings.TrimSpace(asString(responseObj["id"])),
			Model:            strings.TrimSpace(asString(responseObj["model"])),
			CallID:           callID,
			RevisedPrompt:    strings.TrimSpace(asString(item["revised_prompt"])),
			Base64Image:      result,
			DecodedPNG:       decodedImage,
			PartialImages:    partialImages,
			ProviderResponse: decoded,
		}, nil
	}
	if len(partialImages) > 0 {
		return ImageGenerationResult{
			ResponseID:       strings.TrimSpace(asString(responseObj["id"])),
			Model:            strings.TrimSpace(asString(responseObj["model"])),
			CallID:           firstNonEmpty(partialImages[0].ItemID, "image_generation"),
			PartialImages:    partialImages,
			ProviderResponse: decoded,
		}, nil
	}
	if foundImageGenerationCall {
		if len(pendingStatuses) > 0 {
			return ImageGenerationResult{}, fmt.Errorf("codex response did not include a completed image_generation_call with image data; observed statuses: %s", strings.Join(pendingStatuses, ", "))
		}
		return ImageGenerationResult{}, errors.New("codex response did not include a completed image_generation_call with image data")
	}
	return ImageGenerationResult{}, errors.New("codex response did not include an image_generation_call")
}

func parseImageGenerationPartials(responseObj map[string]any) []ImageGenerationPartialImage {
	partialsRaw := asSlice(responseObj["image_generation_partials"])
	if len(partialsRaw) == 0 {
		return nil
	}
	partials := make([]ImageGenerationPartialImage, 0, len(partialsRaw))
	for _, raw := range partialsRaw {
		partial, ok := raw.(map[string]any)
		if !ok || len(partial) == 0 {
			continue
		}
		base64Image := strings.TrimSpace(asString(partial["partial_image_b64"]))
		if base64Image == "" {
			base64Image = strings.TrimSpace(asString(partial["b64_json"]))
		}
		if base64Image == "" {
			continue
		}
		partials = append(partials, ImageGenerationPartialImage{
			ItemID:            strings.TrimSpace(asString(partial["item_id"])),
			OutputIndex:       intFromAny(partial["output_index"], -1),
			SequenceNumber:    intFromAny(partial["sequence_number"], -1),
			PartialImageIndex: intFromAny(partial["partial_image_index"], -1),
			Base64Image:       base64Image,
			OutputFormat:      strings.TrimSpace(asString(partial["output_format"])),
			Size:              strings.TrimSpace(asString(partial["size"])),
			Quality:           strings.TrimSpace(asString(partial["quality"])),
			Background:        strings.TrimSpace(asString(partial["background"])),
		})
	}
	return partials
}

func looksLikePNG(data []byte) bool {
	return len(data) >= 8 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' && data[4] == '\r' && data[5] == '\n' && data[6] == 0x1a && data[7] == '\n'
}
