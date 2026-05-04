package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/imagegenlog"
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
	Count         int
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
	OutputIndex      int
	RevisedPrompt    string
	Base64Image      string
	DecodedPNG       []byte
	MIMEType         string
	PartialImages    []ImageGenerationPartialImage
	ProviderResponse map[string]any
	Results          []ImageGenerationResult
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
		codexImageGenerationLogf("stage=preflight reason=client_nil will_parse=false")
		return ImageGenerationResult{}, errors.New("codex client is not configured")
	}
	record, err := c.ensureAuth(ctx)
	if err != nil {
		codexImageGenerationLogf("stage=auth_failed reason=%q will_parse=false", err.Error())
		return ImageGenerationResult{}, err
	}
	if record.Type != pebblestore.CodexAuthTypeOAuth {
		codexImageGenerationLogf("stage=auth_failed reason=not_oauth auth_type=%q will_parse=false", record.Type)
		return ImageGenerationResult{}, errors.New("codex image generation requires Codex OAuth auth")
	}

	payload, err := buildImageGenerationPayload(req)
	if err != nil {
		codexImageGenerationLogf("stage=payload_failed reason=%q will_send=false", err.Error())
		return ImageGenerationResult{}, err
	}
	onEvent := imageGenerationStreamCallback(req.OnEvent)
	emitImageGenerationStreamEvent(req.OnEvent, ImageGenerationStreamEvent{Type: ImageGenerationStreamEventStarted})
	codexImageGenerationLogf("stage=send_start model=%q count=%d partial_images=%d payload_bytes=%d", strings.TrimSpace(req.Model), normalizedImageRequestCount(req.Count), normalizedPartialImages(req.PartialImages), len(payload))
	decoded, statusCode, err := c.send(ctx, record, payload, onEvent)
	if err != nil {
		codexImageGenerationLogf("stage=send_failed reason=%q will_parse=false", err.Error())
		return ImageGenerationResult{}, err
	}
	codexImageGenerationLogf("stage=send_done status=%d response_keys=%q raw_events=%d output_items=%d partials=%d", statusCode, strings.Join(sortedMapKeys(decoded), ","), len(asSlice(decoded["raw_events"])), len(asSlice(decoded["output"])), len(asSlice(decoded["image_generation_partials"])))
	if statusCode == http.StatusUnauthorized {
		codexImageGenerationLogf("stage=refresh_start status=%d", statusCode)
		refreshed, refreshErr := c.refreshOAuth(ctx, record.RefreshToken)
		if refreshErr != nil {
			codexImageGenerationLogf("stage=refresh_failed reason=%q will_parse=false", refreshErr.Error())
			return ImageGenerationResult{}, fmt.Errorf("codex image generation unauthorized and refresh failed: %w", refreshErr)
		}
		accountID := extractAccountIDFromToken(refreshed.AccessToken)
		record, err = c.authStore.UpdateOAuthCredential(record.Provider, record.ID, refreshed.AccessToken, refreshed.RefreshToken, refreshed.ExpiresAt, accountID)
		if err != nil {
			codexImageGenerationLogf("stage=refresh_persist_failed reason=%q will_parse=false", err.Error())
			return ImageGenerationResult{}, fmt.Errorf("persist refreshed codex oauth: %w", err)
		}
		decoded, statusCode, err = c.send(ctx, record, payload, onEvent)
		if err != nil {
			codexImageGenerationLogf("stage=send_failed_after_refresh reason=%q will_parse=false", err.Error())
			return ImageGenerationResult{}, err
		}
		codexImageGenerationLogf("stage=send_done_after_refresh status=%d response_keys=%q raw_events=%d output_items=%d partials=%d", statusCode, strings.Join(sortedMapKeys(decoded), ","), len(asSlice(decoded["raw_events"])), len(asSlice(decoded["output"])), len(asSlice(decoded["image_generation_partials"])))
	}
	if statusCode >= 400 {
		codexImageGenerationLogf("stage=provider_status_error status=%d response_keys=%q body_summary=%q will_parse=false", statusCode, strings.Join(sortedMapKeys(decoded), ","), compactBody(decoded))
		if transport, _ := extractCodexTransportMetadata(decoded); transport != "" {
			return ImageGenerationResult{}, &ProviderResponseError{Err: fmt.Errorf("codex image generation failed status=%d transport=%s body=%s", statusCode, transport, compactBody(decoded)), Response: decoded}
		}
		return ImageGenerationResult{}, &ProviderResponseError{Err: fmt.Errorf("codex image generation failed status=%d body=%s", statusCode, compactBody(decoded)), Response: decoded}
	}
	result, err := parseImageGenerationResult(decoded)
	if err != nil {
		codexImageGenerationLogf("stage=parse_failed reason=%q response_keys=%q raw_events=%d output_items=%d partials=%d finals=%d", err.Error(), strings.Join(sortedMapKeys(decoded), ","), len(asSlice(decoded["raw_events"])), len(asSlice(decoded["output"])), len(asSlice(decoded["image_generation_partials"])), len(asSlice(decoded["image_generation_finals"])))
		return ImageGenerationResult{}, &ProviderResponseError{Err: err, Response: decoded}
	}
	result.ProviderResponse = decoded
	for i := range result.Results {
		result.Results[i].ProviderResponse = decoded
	}
	codexImageGenerationLogf("stage=parse_done results=%d decoded_png_bytes=%d partials=%d response_id=%q model=%q", len(result.Results), len(result.DecodedPNG), len(result.PartialImages), result.ResponseID, result.Model)
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
	count := normalizedImageRequestCount(req.Count)
	if count < 1 || count > 3 {
		return nil, errors.New("count must be between 1 and 3")
	}
	if count > 1 {
		prompt = fmt.Sprintf("Create %d distinct final images for this request.\n\n%s", count, prompt)
	}
	partialImages := normalizedPartialImages(req.PartialImages)
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
		"parallel_tool_calls": count > 1,
		"instructions":        fmt.Sprintf("Generate exactly %d completed image%s that satisfy the user's prompt. Use the image_generation tool and return exactly %d completed image_generation_call result%s. Do not answer with text only.", count, pluralSuffix(count), count, pluralSuffix(count)),
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode image generation request: %w", err)
	}
	return encoded, nil
}

func normalizedImageRequestCount(count int) int {
	if count == 0 {
		return 1
	}
	return count
}

func normalizedPartialImages(partialImages int) int {
	if partialImages < 0 {
		return 0
	}
	if partialImages > 3 {
		return 3
	}
	return partialImages
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
	codexImageGenerationLogf("stage=parse_start response_keys=%q raw_events=%d output_items=%d top_level_partials=%d", strings.Join(sortedMapKeys(decoded), ","), len(asSlice(decoded["raw_events"])), len(asSlice(decoded["output"])), len(asSlice(decoded["image_generation_partials"])))
	decoded = normalizeImageGenerationRawEvents(decoded)
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
	responseID := strings.TrimSpace(asString(responseObj["id"]))
	model := strings.TrimSpace(asString(responseObj["model"]))
	codexImageGenerationLogf("stage=parse_shape response_id=%q model=%q used_nested_response=%t output_items=%d partials=%d response_keys=%q", responseID, model, usedNestedResponse, len(items), len(partialImages), strings.Join(sortedMapKeys(responseObj), ","))
	foundImageGenerationCall := false
	var pendingStatuses []string
	results := make([]ImageGenerationResult, 0, 3)
	seenResultKeys := make(map[string]struct{}, 4)
	appendResult := func(parsed ImageGenerationResult) {
		key := imageGenerationParsedResultKey(parsed, len(results))
		if _, exists := seenResultKeys[key]; exists {
			return
		}
		seenResultKeys[key] = struct{}{}
		results = append(results, parsed)
	}
	for outputIndex, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || !strings.EqualFold(strings.TrimSpace(asString(item["type"])), "image_generation_call") {
			continue
		}
		foundImageGenerationCall = true
		status := strings.TrimSpace(asString(item["status"]))
		result := strings.TrimSpace(asString(item["result"]))
		codexImageGenerationLogf("stage=parse_output_item output_index=%d id=%q call_id=%q status=%q has_result=%t result_chars=%d keys=%q", outputIndex, strings.TrimSpace(asString(item["id"])), strings.TrimSpace(asString(item["call_id"])), status, result != "", len(result), strings.Join(sortedMapKeys(item), ","))
		if result == "" {
			if status == "" {
				pendingStatuses = append(pendingStatuses, "missing status")
			} else {
				pendingStatuses = append(pendingStatuses, status+" without result")
			}
			codexImageGenerationLogf("stage=parse_output_item_missing_result output_index=%d status=%q keys=%q", outputIndex, status, strings.Join(sortedMapKeys(item), ","))
			continue
		}
		if status != "" && !strings.EqualFold(status, "completed") {
			codexImageGenerationLogf("stage=parse_output_item_result_status_accepted output_index=%d status=%q result_chars=%d", outputIndex, status, len(result))
		}
		parsed, err := parseCompletedImageGenerationCall(responseID, model, outputIndex, item, result, partialImages, decoded)
		if err != nil {
			return ImageGenerationResult{}, err
		}
		appendResult(parsed)
	}
	for _, rawEvent := range collectImageGenerationEvents(responseObj, true) {
		event, ok := rawEvent.(map[string]any)
		if !ok {
			continue
		}
		eventResult := strings.TrimSpace(extractImageGenerationResultFromFinal(event))
		codexImageGenerationLogf("stage=parse_completed_event event_type=%q item_id=%q output_index=%d has_result=%t result_chars=%d keys=%q", strings.TrimSpace(asString(event["type"])), strings.TrimSpace(firstNonEmpty(asString(event["item_id"]), asString(event["id"]))), intFromAny(event["output_index"], -1), eventResult != "", len(eventResult), strings.Join(sortedMapKeys(event), ","))
		if eventResult == "" {
			continue
		}
		parsed, err := parseCompletedImageGenerationCall(responseID, model, intFromAny(event["output_index"], len(results)), event, eventResult, partialImages, decoded)
		if err != nil {
			return ImageGenerationResult{}, err
		}
		appendResult(parsed)
	}
	if len(results) > 0 {
		primary := results[0]
		primary.Results = results
		if responseObj != nil {
			responseObj["image_generation_results"] = imageGenerationResultsSummary(results)
		}
		primary.ProviderResponse = decoded
		for i := range primary.Results {
			primary.Results[i].ProviderResponse = decoded
		}
		return primary, nil
	}
	if len(partialImages) > 0 {
		return ImageGenerationResult{
			ResponseID:       responseID,
			Model:            model,
			CallID:           firstNonEmpty(partialImages[0].ItemID, "image_generation"),
			OutputIndex:      partialImages[0].OutputIndex,
			PartialImages:    partialImages,
			ProviderResponse: decoded,
			Results:          []ImageGenerationResult{},
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

func parseCompletedImageGenerationCall(responseID, model string, outputIndex int, item map[string]any, result string, partialImages []ImageGenerationPartialImage, providerResponse map[string]any) (ImageGenerationResult, error) {
	result = strings.TrimSpace(result)
	if result == "" {
		result = extractImageGenerationResultFromFinal(item)
	}
	codexImageGenerationLogf("stage=parse_final_candidate output_index=%d result_chars=%d item_keys=%q", outputIndex, len(result), strings.Join(sortedMapKeys(item), ","))
	if strings.Contains(result, "://") || strings.Contains(strings.ToLower(result), "base64,") {
		codexImageGenerationLogf("stage=parse_final_rejected output_index=%d reason=not_raw_base64 result_chars=%d", outputIndex, len(result))
		return ImageGenerationResult{}, errors.New("image generation result must be raw base64 data")
	}
	decodedImage, err := base64.StdEncoding.Strict().DecodeString(result)
	if err != nil {
		codexImageGenerationLogf("stage=parse_final_rejected output_index=%d reason=base64_decode_failed error=%q result_chars=%d", outputIndex, err.Error(), len(result))
		return ImageGenerationResult{}, fmt.Errorf("decode image generation result: %w", err)
	}
	if !looksLikePNG(decodedImage) {
		codexImageGenerationLogf("stage=parse_final_rejected output_index=%d reason=decoded_not_png decoded_bytes=%d", outputIndex, len(decodedImage))
		return ImageGenerationResult{}, errors.New("image generation result decoded but is not a PNG image")
	}
	callID := strings.TrimSpace(asString(item["id"]))
	if callID == "" {
		callID = strings.TrimSpace(asString(item["item_id"]))
	}
	if callID == "" {
		callID = strings.TrimSpace(asString(item["call_id"]))
	}
	if callID == "" {
		callID = fmt.Sprintf("image_generation_%d", outputIndex+1)
	}
	if itemOutputIndex := intFromAny(item["output_index"], -1); itemOutputIndex >= 0 {
		outputIndex = itemOutputIndex
	}
	return ImageGenerationResult{
		ResponseID:       responseID,
		Model:            model,
		CallID:           callID,
		OutputIndex:      outputIndex,
		RevisedPrompt:    strings.TrimSpace(asString(item["revised_prompt"])),
		Base64Image:      result,
		DecodedPNG:       decodedImage,
		PartialImages:    partialImages,
		ProviderResponse: providerResponse,
	}, nil
}

func normalizeImageGenerationRawEvents(decoded map[string]any) map[string]any {
	if decoded == nil {
		return nil
	}
	if nested, ok := decoded["response"].(map[string]any); ok {
		normalizeImageGenerationRawEvents(nested)
		return decoded
	}
	if len(asSlice(decoded["output"])) > 0 {
		return decoded
	}
	events := collectImageGenerationEvents(decoded, true)
	if len(events) == 0 {
		return decoded
	}
	output := make([]any, 0, len(events))
	for index, raw := range events {
		event, ok := raw.(map[string]any)
		if !ok || strings.TrimSpace(extractImageGenerationResultFromFinal(event)) == "" {
			continue
		}
		item := map[string]any{
			"type":         "image_generation_call",
			"status":       "completed",
			"result":       extractImageGenerationResultFromFinal(event),
			"output_index": intFromAny(event["output_index"], index),
		}
		if id := strings.TrimSpace(firstNonEmpty(asString(event["item_id"]), asString(event["id"]))); id != "" {
			item["id"] = id
		}
		for _, key := range []string{"revised_prompt", "output_format", "size", "quality", "background"} {
			if value := strings.TrimSpace(asString(event[key])); value != "" {
				item[key] = value
			}
		}
		output = append(output, item)
	}
	if len(output) > 0 {
		decoded["output"] = output
	}
	return decoded
}

func extractImageGenerationResultFromFinal(item map[string]any) string {
	if len(item) == 0 {
		return ""
	}
	for _, key := range []string{"result", "b64_json", "image_b64", "base64_image"} {
		if result := strings.TrimSpace(asString(item[key])); result != "" {
			return result
		}
	}
	if nested, ok := item["item"].(map[string]any); ok {
		return extractImageGenerationResultFromFinal(nested)
	}
	return ""
}

func imageGenerationParsedResultKey(result ImageGenerationResult, fallbackIndex int) string {
	if result.OutputIndex >= 0 {
		return fmt.Sprintf("output_index:%d", result.OutputIndex)
	}
	if result.CallID != "" {
		return "call_id:" + result.CallID
	}
	if result.Base64Image != "" {
		return "base64:" + result.Base64Image
	}
	return fmt.Sprintf("idx:%d", fallbackIndex)
}

func imageGenerationResultsSummary(results []ImageGenerationResult) []any {
	if len(results) == 0 {
		return nil
	}
	out := make([]any, 0, len(results))
	for _, result := range results {
		entry := map[string]any{
			"call_id":      result.CallID,
			"output_index": result.OutputIndex,
		}
		if result.RevisedPrompt != "" {
			entry["revised_prompt"] = result.RevisedPrompt
		}
		out = append(out, entry)
	}
	return out
}

func parseImageGenerationPartials(responseObj map[string]any) []ImageGenerationPartialImage {
	partialsRaw := asSlice(responseObj["image_generation_partials"])
	if len(partialsRaw) == 0 {
		partialsRaw = collectImageGenerationEvents(responseObj, false)
	}
	if len(partialsRaw) == 0 {
		return nil
	}
	partials := make([]ImageGenerationPartialImage, 0, len(partialsRaw))
	for _, raw := range partialsRaw {
		partial, ok := raw.(map[string]any)
		if !ok || len(partial) == 0 {
			continue
		}
		base64Image := strings.TrimSpace(firstNonEmpty(asString(partial["partial_image_b64"]), asString(partial["b64_json"]), asString(partial["image_b64"]), asString(partial["base64_image"])))
		if base64Image == "" {
			continue
		}
		partials = append(partials, ImageGenerationPartialImage{
			ItemID:            strings.TrimSpace(firstNonEmpty(asString(partial["item_id"]), asString(partial["id"]))),
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

func collectImageGenerationEvents(responseObj map[string]any, completed bool) []any {
	rawEvents := asSlice(responseObj["raw_events"])
	if len(rawEvents) == 0 {
		return nil
	}
	events := make([]any, 0, len(rawEvents))
	for _, raw := range rawEvents {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		data, ok := entry["data"].(map[string]any)
		if !ok || len(data) == 0 {
			continue
		}
		eventType := strings.TrimSpace(asString(data["type"]))
		if completed {
			if strings.EqualFold(eventType, "image_generation.completed") || strings.EqualFold(eventType, "response.image_generation_call.completed") {
				events = append(events, data)
			}
			continue
		}
		if strings.EqualFold(eventType, "image_generation.partial_image") || strings.EqualFold(eventType, "response.image_generation_call.partial_image") {
			events = append(events, data)
		}
	}
	return events
}

func codexImageGenerationLogf(format string, args ...any) {
	imagegenlog.Printf("codex", format, args...)
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func looksLikePNG(data []byte) bool {
	return len(data) >= 8 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' && data[4] == '\r' && data[5] == '\n' && data[6] == 0x1a && data[7] == '\n'
}
