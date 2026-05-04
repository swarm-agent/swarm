package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"swarm/packages/swarmd/internal/provider/codex"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type googleGeminiImageClient struct {
	httpClient *http.Client
	baseURL    string
}

type GeminiImageGenerationRequest struct {
	APIKey      string
	Model       string
	Prompt      string
	AspectRatio string
	ImageSize   string
	OutputIndex int
	OnEvent     func(GenerateStreamEvent)
}

type GeminiImageGenerationResult struct {
	ResponseID        string
	ModelVersion      string
	Text              []string
	Thinking          []string
	Images            []GeminiGeneratedImage
	Usage             map[string]any
	Cost              map[string]any
	ProviderResponse  map[string]any
	ChunkCount        int
	RealImageChunkIDs []string
}

type GeminiGeneratedImage struct {
	Base64Image string
	DecodedPNG  []byte
	MIMEType    string
}

type geminiGenerateContentRequest struct {
	Contents         []geminiRESTContent        `json:"contents"`
	GenerationConfig geminiRESTGenerationConfig `json:"generationConfig"`
}

type geminiRESTContent struct {
	Role  string           `json:"role,omitempty"`
	Parts []geminiRESTPart `json:"parts,omitempty"`
}

type geminiRESTPart struct {
	Text       string                `json:"text,omitempty"`
	Thought    bool                  `json:"thought,omitempty"`
	InlineData *geminiRESTInlineData `json:"inlineData,omitempty"`
}

type geminiRESTInlineData struct {
	MIMEType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
}

type geminiRESTGenerationConfig struct {
	ResponseModalities []string              `json:"responseModalities,omitempty"`
	ThinkingConfig     map[string]any        `json:"thinkingConfig,omitempty"`
	ImageConfig        geminiRESTImageConfig `json:"imageConfig,omitempty"`
}

type geminiRESTImageConfig struct {
	AspectRatio string `json:"aspectRatio,omitempty"`
	ImageSize   string `json:"imageSize,omitempty"`
}

type geminiGenerateContentResponse struct {
	ResponseID    string                `json:"responseId,omitempty"`
	ModelVersion  string                `json:"modelVersion,omitempty"`
	Candidates    []geminiRESTCandidate `json:"candidates,omitempty"`
	UsageMetadata map[string]any        `json:"usageMetadata,omitempty"`
}

type geminiRESTCandidate struct {
	Content *geminiRESTContent `json:"content,omitempty"`
}

type geminiRESTErrorResponse struct {
	Error *struct {
		Code    int             `json:"code,omitempty"`
		Message string          `json:"message,omitempty"`
		Status  string          `json:"status,omitempty"`
		Details json.RawMessage `json:"details,omitempty"`
	} `json:"error,omitempty"`
}

func (c googleGeminiImageClient) GenerateImage(ctx context.Context, req GeminiImageGenerationRequest) (GeminiImageGenerationResult, error) {
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		return GeminiImageGenerationResult{}, errors.New("Google API key is required for Gemini image generation")
	}
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		modelID = defaultGeminiImageModel
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return GeminiImageGenerationResult{}, errors.New("prompt is required")
	}

	result := GeminiImageGenerationResult{ProviderResponse: map[string]any{
		"provider":      ProviderGoogleGemini,
		"model":         modelID,
		"aspect_ratio":  req.AspectRatio,
		"image_size":    req.ImageSize,
		"stream_method": "REST generateContent non-stream",
		"transport":     "rest",
		"cost":          geminiCostUnavailable(),
	}}
	emitGenerateEvent(req.OnEvent, GenerateStreamEvent{Type: "generating", OutputIndex: req.OutputIndex, SequenceNumber: 1})

	requestBody := geminiGenerateContentRequest{
		Contents: []geminiRESTContent{{
			Role:  "user",
			Parts: []geminiRESTPart{{Text: prompt}},
		}},
		GenerationConfig: geminiRESTGenerationConfig{
			ResponseModalities: []string{"IMAGE"},
			ThinkingConfig:     map[string]any{"includeThoughts": true},
			ImageConfig: geminiRESTImageConfig{
				AspectRatio: strings.TrimSpace(req.AspectRatio),
				ImageSize:   strings.TrimSpace(req.ImageSize),
			},
		},
	}
	encodedBody, err := json.Marshal(requestBody)
	if err != nil {
		return GeminiImageGenerationResult{}, fmt.Errorf("marshal Gemini REST request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiGenerateContentURL(c.baseURL, modelID, apiKey), bytes.NewReader(encodedBody))
	if err != nil {
		return GeminiImageGenerationResult{}, fmt.Errorf("create Gemini REST request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Content-Type", "application/json")

	httpClient := c.httpClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(httpReq)
	if err != nil {
		return GeminiImageGenerationResult{}, fmt.Errorf("Gemini REST generateContent request failed: %w", err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return GeminiImageGenerationResult{}, fmt.Errorf("read Gemini REST generateContent response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return GeminiImageGenerationResult{}, geminiRESTHTTPError(response.StatusCode, responseBody)
	}

	var decoded geminiGenerateContentResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return GeminiImageGenerationResult{}, fmt.Errorf("decode Gemini REST generateContent response: %w", err)
	}
	mergeGeminiImageResponse(&result, decoded, req.OutputIndex, 1, req.OnEvent)
	if result.ChunkCount == 0 {
		return GeminiImageGenerationResult{}, errors.New("Gemini REST generateContent returned no response candidates")
	}
	finalizeGeminiProviderResponse(&result)
	return result, nil
}

func geminiGenerateContentURL(baseURL, modelID, apiKey string) string {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	base = strings.TrimRight(base, "/")
	modelPath := strings.TrimSpace(modelID)
	if !strings.HasPrefix(modelPath, "models/") && !strings.HasPrefix(modelPath, "tunedModels/") {
		modelPath = "models/" + modelPath
	}
	parts := strings.Split(modelPath, "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return base + "/" + strings.Join(parts, "/") + ":generateContent?key=" + url.QueryEscape(apiKey)
}

func geminiRESTHTTPError(statusCode int, responseBody []byte) error {
	var decoded geminiRESTErrorResponse
	if err := json.Unmarshal(responseBody, &decoded); err == nil && decoded.Error != nil {
		message := strings.TrimSpace(decoded.Error.Message)
		if message == "" {
			message = strings.TrimSpace(decoded.Error.Status)
		}
		if message != "" {
			return fmt.Errorf("Gemini REST generateContent failed: http_status=%d api_status=%q message=%q", statusCode, decoded.Error.Status, message)
		}
	}
	body := strings.TrimSpace(string(responseBody))
	if len(body) > 1000 {
		body = body[:1000] + "…"
	}
	if body == "" {
		return fmt.Errorf("Gemini REST generateContent failed: http_status=%d", statusCode)
	}
	return fmt.Errorf("Gemini REST generateContent failed: http_status=%d body=%q", statusCode, body)
}
func (s *Service) generateGoogleGemini(ctx context.Context, req GenerateRequest, count int, prompt string) (GenerateResult, error) {
	if s.geminiImageClient == nil {
		imageGenerationLogf("stage=preflight provider=%q reason=gemini_client_nil will_save=false", ProviderGoogleGemini)
		return GenerateResult{}, errors.New("Gemini image provider is not configured")
	}
	if s.authStore == nil {
		return GenerateResult{}, errors.New("Google auth store is not configured")
	}
	record, ok, err := s.authStore.GetActiveCredential("google")
	if err != nil {
		return GenerateResult{}, fmt.Errorf("read google auth: %w", err)
	}
	if !ok || strings.TrimSpace(record.APIKey) == "" {
		return GenerateResult{}, errors.New("connect a Google API key to enable Gemini image generation")
	}
	session, err := s.openImageSession(req.Target)
	if err != nil {
		imageGenerationLogf("stage=open_session provider=%q thread_id=%q reason=%q will_save=false", ProviderGoogleGemini, strings.TrimSpace(req.Target.ThreadID), err.Error())
		return GenerateResult{}, err
	}
	modelID := normalizeGeminiImageModel(req.Model)
	aspectRatio := normalizeGeminiAspectRatio(firstNonEmpty(settingString(req.Settings, "aspect_ratio"), req.Size))
	imageSize, err := normalizeGeminiImageSize(modelID, firstNonEmpty(settingString(req.Settings, "image_size"), settingString(req.Settings, "size")))
	if err != nil {
		return GenerateResult{}, err
	}
	imageGenerationLogf("stage=start provider=%q model=%q thread_id=%q storage_path=%q requested_count=%d aspect_ratio=%q image_size=%q parallel_limit=%d", ProviderGoogleGemini, modelID, session.thread.ID, session.storagePath, count, aspectRatio, imageSize, geminiMaxParallelRequests)

	results, err := s.generateGeminiSlotsParallel(ctx, record.APIKey, modelID, prompt, aspectRatio, imageSize, count, req.OnEvent)
	if err != nil {
		return GenerateResult{}, err
	}

	assets := make([]GeneratedAsset, 0, count)
	updatedThread := session.thread
	providerResponses := make([]map[string]any, 0, count)
	for index, generated := range results {
		providerResponses = append(providerResponses, generated.ProviderResponse)
		finals, err := completedGeminiImages(generated, index, session.storagePath)
		if err != nil {
			imageGenerationLogf("stage=final_validation_failed provider=%q thread_id=%q slot=%d reason=%q response_id=%q image_count=%d chunk_count=%d provider_response_keys=%q will_save=false", ProviderGoogleGemini, session.thread.ID, index+1, err.Error(), generated.ResponseID, len(generated.Images), generated.ChunkCount, strings.Join(sortedAnyMapKeys(generated.ProviderResponse), ","))
			return GenerateResult{}, err
		}
		slotAssets, slotThread, err := s.saveGeneratedImages(session, finals, ProviderGoogleGemini, modelID)
		if err != nil {
			imageGenerationLogf("stage=slot_save_failed provider=%q thread_id=%q slot=%d reason=%q storage_path=%q will_save=false", ProviderGoogleGemini, session.thread.ID, index+1, err.Error(), session.storagePath)
			return GenerateResult{}, err
		}
		assets = append(assets, slotAssets...)
		updatedThread = slotThread
		session.thread = slotThread
		emitGenerateEvent(req.OnEvent, GenerateStreamEvent{Type: "completed", OutputIndex: index})
	}
	if len(assets) != count {
		return GenerateResult{}, fmt.Errorf("backend saved %d Gemini image assets, expected exactly %d", len(assets), count)
	}
	providerResponse := summarizeGeminiProviderResponses(providerResponses)
	updatedThread, err = s.recordImageGenerationMetadata(updatedThread, providerResponse, assets)
	if err != nil {
		return GenerateResult{}, err
	}
	imageGenerationLogf("stage=success provider=%q thread_id=%q saved_assets=%d expected=%d storage_path=%q", ProviderGoogleGemini, session.thread.ID, len(assets), count, session.storagePath)
	return GenerateResult{Assets: assets, Target: &WorkspaceImageSessionTargetInfo{Kind: TargetWorkspaceImage, Thread: updatedThread}, ProviderResponse: providerResponse}, nil
}

type geminiSlotResult struct {
	index  int
	result GeminiImageGenerationResult
	err    error
}

func (s *Service) generateGeminiSlotsParallel(ctx context.Context, apiKey, modelID, prompt, aspectRatio, imageSize string, count int, onEvent func(GenerateStreamEvent)) ([]GeminiImageGenerationResult, error) {
	results := make([]GeminiImageGenerationResult, count)
	out := make(chan geminiSlotResult, count)
	var wg sync.WaitGroup
	for index := 0; index < count; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			emitGenerateEvent(onEvent, GenerateStreamEvent{Type: "started", OutputIndex: index})
			imageGenerationLogf("stage=provider_call_start provider=%q thread_id_slot=%d requested_count=%d", ProviderGoogleGemini, index+1, count)
			generated, err := s.geminiImageClient.GenerateImage(ctx, GeminiImageGenerationRequest{
				APIKey:      apiKey,
				Model:       modelID,
				Prompt:      prompt,
				AspectRatio: aspectRatio,
				ImageSize:   imageSize,
				OutputIndex: index,
				OnEvent:     onEvent,
			})
			if err != nil {
				imageGenerationLogf("stage=provider_call_error provider=%q slot=%d reason=%q will_save=false", ProviderGoogleGemini, index+1, err.Error())
			} else {
				imageGenerationLogf("stage=provider_call_done provider=%q slot=%d response_id=%q chunk_count=%d image_count=%d text_chunks=%d thinking_chunks=%d real_image_chunks=%d usage_present=%t provider_response_keys=%q", ProviderGoogleGemini, index+1, generated.ResponseID, generated.ChunkCount, len(generated.Images), len(generated.Text), len(generated.Thinking), len(generated.RealImageChunkIDs), generated.Usage != nil, strings.Join(sortedAnyMapKeys(generated.ProviderResponse), ","))
			}
			out <- geminiSlotResult{index: index, result: generated, err: err}
		}()
	}
	wg.Wait()
	close(out)
	var firstErr error
	for slot := range out {
		if slot.err != nil && firstErr == nil {
			firstErr = slot.err
		}
		results[slot.index] = slot.result
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

func mergeGeminiImageResponse(result *GeminiImageGenerationResult, response geminiGenerateContentResponse, outputIndex, sequence int, onEvent func(GenerateStreamEvent)) {
	if result == nil {
		return
	}
	if len(response.Candidates) == 0 {
		return
	}
	result.ChunkCount++
	if strings.TrimSpace(response.ResponseID) != "" {
		result.ResponseID = response.ResponseID
		result.ProviderResponse["response_id"] = response.ResponseID
	}
	if strings.TrimSpace(response.ModelVersion) != "" {
		result.ModelVersion = response.ModelVersion
		result.ProviderResponse["model_version"] = response.ModelVersion
	}
	if response.UsageMetadata != nil {
		result.Usage = cloneStringAnyMap(response.UsageMetadata)
		result.Usage["available"] = true
		result.ProviderResponse["usage_metadata"] = result.Usage
		result.ProviderResponse["cost"] = geminiCostUnavailable()
	}
	for _, candidate := range response.Candidates {
		if candidate.Content == nil {
			continue
		}
		for partIndex, part := range candidate.Content.Parts {
			if text := strings.TrimSpace(part.Text); text != "" {
				if part.Thought {
					result.Thinking = append(result.Thinking, text)
					emitGenerateEvent(onEvent, GenerateStreamEvent{Type: "thinking", OutputIndex: outputIndex, SequenceNumber: sequence, PartialImageIndex: partIndex, Thinking: text})
				} else {
					result.Text = append(result.Text, text)
					emitGenerateEvent(onEvent, GenerateStreamEvent{Type: "text", OutputIndex: outputIndex, SequenceNumber: sequence, PartialImageIndex: partIndex, Text: text})
				}
			}
			if part.InlineData == nil || strings.TrimSpace(part.InlineData.Data) == "" || part.Thought {
				continue
			}
			mimeType := strings.TrimSpace(part.InlineData.MIMEType)
			if mimeType == "" {
				mimeType = "image/png"
			}
			if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
				continue
			}
			decoded, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
			if err != nil {
				imageGenerationLogf("stage=rest_image_part_decode_failed provider=%q output_index=%d sequence=%d part_index=%d mime_type=%q reason=%q", ProviderGoogleGemini, outputIndex, sequence, partIndex, mimeType, err.Error())
				continue
			}
			imageGenerationLogf("stage=rest_image_part_received provider=%q output_index=%d sequence=%d part_index=%d mime_type=%q detected_mime=%q bytes=%d magic_hex=%q thought=%t", ProviderGoogleGemini, outputIndex, sequence, partIndex, mimeType, detectImageMIME(decoded), len(decoded), imageMagicHex(decoded), part.Thought)
			image := GeminiGeneratedImage{Base64Image: part.InlineData.Data, DecodedPNG: decoded, MIMEType: mimeType}
			result.Images = append(result.Images, image)
			chunkID := fmt.Sprintf("rest-%d-part-%d", sequence, partIndex)
			result.RealImageChunkIDs = append(result.RealImageChunkIDs, chunkID)
			emitGenerateEvent(onEvent, GenerateStreamEvent{Type: "image", ItemID: chunkID, OutputIndex: outputIndex, SequenceNumber: sequence, PartialImageIndex: len(result.Images) - 1, OutputFormat: imageFormatFromMIME(mimeType), MIMEType: mimeType})
		}
	}
	emitGenerateEvent(onEvent, GenerateStreamEvent{Type: "generating", OutputIndex: outputIndex, SequenceNumber: sequence})
}

func cloneStringAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
func finalizeGeminiProviderResponse(result *GeminiImageGenerationResult) {
	if result == nil {
		return
	}
	result.ProviderResponse["chunk_count"] = result.ChunkCount
	result.ProviderResponse["image_count"] = len(result.Images)
	if len(result.RealImageChunkIDs) > 0 {
		result.ProviderResponse["real_image_chunk_ids"] = append([]string(nil), result.RealImageChunkIDs...)
	}
	if len(result.Text) > 0 {
		result.ProviderResponse["text"] = append([]string(nil), result.Text...)
	}
	if len(result.Thinking) > 0 {
		result.ProviderResponse["thinking"] = append([]string(nil), result.Thinking...)
	}
	if result.Usage == nil {
		result.ProviderResponse["usage_metadata"] = map[string]any{"available": false}
	}
	if result.Cost == nil {
		result.Cost = geminiCostUnavailable()
	}
	result.ProviderResponse["cost"] = result.Cost
}

func completedGeminiImages(generated GeminiImageGenerationResult, outputIndex int, storagePath string) ([]codex.ImageGenerationResult, error) {
	if len(generated.Images) == 0 {
		imageGenerationLogf("stage=final_validation_no_images provider=%q output_index=%d response_id=%q chunk_count=%d text_chunks=%d thinking_chunks=%d provider_response_keys=%q will_save=false", ProviderGoogleGemini, outputIndex, generated.ResponseID, generated.ChunkCount, len(generated.Text), len(generated.Thinking), strings.Join(sortedAnyMapKeys(generated.ProviderResponse), ","))
		return nil, errors.New("Gemini image generation returned no final image data to save")
	}
	if len(generated.Images) != 1 {
		imageGenerationLogf("stage=final_validation_count_mismatch provider=%q output_index=%d response_id=%q image_count=%d expected=1 chunk_count=%d will_save=false", ProviderGoogleGemini, outputIndex, generated.ResponseID, len(generated.Images), generated.ChunkCount)
		return nil, fmt.Errorf("Gemini image generation returned %d final images for one slot, expected exactly 1", len(generated.Images))
	}
	finals := make([]codex.ImageGenerationResult, 0, len(generated.Images))
	for index, image := range generated.Images {
		detectedMIME := detectImageMIME(image.DecodedPNG)
		magicHex := imageMagicHex(image.DecodedPNG)
		imageGenerationLogf("stage=final_validation_image_check provider=%q output_index=%d image_index=%d response_id=%q declared_mime=%q detected_mime=%q bytes=%d magic_hex=%q looks_png=%t", ProviderGoogleGemini, outputIndex, index+1, generated.ResponseID, image.MIMEType, detectedMIME, len(image.DecodedPNG), magicHex, looksLikePNG(image.DecodedPNG))
		if len(image.DecodedPNG) == 0 {
			return nil, fmt.Errorf("Gemini final image %d has no bytes", index+1)
		}
		mimeType := supportedGeneratedImageMIME(image.MIMEType, detectedMIME)
		if mimeType == "" {
			dumpPath, dumpErr := dumpInvalidGeneratedImage(storagePath, ProviderGoogleGemini, outputIndex, index, image.DecodedPNG)
			if dumpErr != nil {
				imageGenerationLogf("stage=invalid_image_dump_failed provider=%q output_index=%d image_index=%d reason=%q storage_path=%q bytes=%d magic_hex=%q declared_mime=%q detected_mime=%q", ProviderGoogleGemini, outputIndex, index+1, dumpErr.Error(), storagePath, len(image.DecodedPNG), magicHex, image.MIMEType, detectedMIME)
				return nil, fmt.Errorf("Gemini final image %d has unsupported image data (declared_mime=%q detected_mime=%q bytes=%d magic_hex=%q; debug dump failed: %w)", index+1, image.MIMEType, detectedMIME, len(image.DecodedPNG), magicHex, dumpErr)
			}
			imageGenerationLogf("stage=invalid_image_dumped provider=%q output_index=%d image_index=%d dump_path=%q bytes=%d magic_hex=%q declared_mime=%q detected_mime=%q", ProviderGoogleGemini, outputIndex, index+1, dumpPath, len(image.DecodedPNG), magicHex, image.MIMEType, detectedMIME)
			return nil, fmt.Errorf("Gemini final image %d has unsupported image data (declared_mime=%q detected_mime=%q bytes=%d magic_hex=%q debug_dump=%q)", index+1, image.MIMEType, detectedMIME, len(image.DecodedPNG), magicHex, dumpPath)
		}
		finals = append(finals, codex.ImageGenerationResult{
			ResponseID:       generated.ResponseID,
			Model:            generated.ModelVersion,
			CallID:           firstNonEmpty(generated.ResponseID, fmt.Sprintf("gemini-image-%d", index+1)),
			OutputIndex:      outputIndex,
			RevisedPrompt:    strings.Join(generated.Text, "\n\n"),
			Base64Image:      image.Base64Image,
			DecodedPNG:       image.DecodedPNG,
			MIMEType:         mimeType,
			ProviderResponse: generated.ProviderResponse,
		})
	}
	return finals, nil
}

func summarizeGeminiProviderResponses(responses []map[string]any) map[string]any {
	summary := map[string]any{
		"provider": ProviderGoogleGemini,
		"results":  responses,
		"cost":     summarizeGeminiCosts(responses),
	}
	usage := make([]any, 0, len(responses))
	for _, response := range responses {
		if response == nil {
			continue
		}
		if value, ok := response["usage_metadata"]; ok {
			usage = append(usage, value)
		}
	}
	if len(usage) > 0 {
		summary["usage_metadata"] = usage
	}
	return summary
}

func summarizeGeminiCosts(responses []map[string]any) map[string]any {
	return map[string]any{
		"available": false,
		"reason":    "Gemini REST generateContent returned usage metadata but no exact dollar charge field; dollar cost was not estimated.",
	}
}

func geminiCostUnavailable() map[string]any {
	return map[string]any{
		"available": false,
		"reason":    "exact dollar charge not returned by Google GenAI response",
	}
}
func (s *Service) recordImageGenerationMetadata(thread pebblestore.ImageThreadSnapshot, providerResponse map[string]any, assets []GeneratedAsset) (pebblestore.ImageThreadSnapshot, error) {
	if s == nil || s.imageThreads == nil || len(assets) == 0 {
		return thread, nil
	}
	metadata := make(map[string]any, len(thread.Metadata)+2)
	for key, value := range thread.Metadata {
		metadata[key] = value
	}
	assetIDs := make([]string, 0, len(assets))
	assetEntries := make([]map[string]any, 0, len(assets))
	for _, asset := range assets {
		assetIDs = append(assetIDs, asset.ID)
		assetEntries = append(assetEntries, map[string]any{
			"id":       asset.ID,
			"name":     asset.Name,
			"provider": asset.Provider,
			"model":    asset.Model,
		})
	}
	entry := map[string]any{
		"provider":          ProviderGoogleGemini,
		"asset_ids":         assetIDs,
		"assets":            assetEntries,
		"provider_response": providerResponse,
	}
	history := metadataSlice(metadata["image_generation_history"])
	history = append(history, entry)
	metadata["image_generation_history"] = history
	metadata["last_image_generation"] = entry
	thread.Metadata = metadata
	updated, err := s.imageThreads.Update(thread)
	if err != nil {
		return pebblestore.ImageThreadSnapshot{}, fmt.Errorf("record image generation metadata: %w", err)
	}
	return updated, nil
}

func normalizeGeminiImageModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return defaultGeminiImageModel
	}
	for _, allowed := range geminiImageModels {
		if model == allowed {
			return model
		}
	}
	return defaultGeminiImageModel
}

func normalizeGeminiAspectRatio(value string) string {
	switch strings.TrimSpace(value) {
	case "1:1", "2:3", "3:2", "3:4", "4:3", "9:16", "16:9", "21:9":
		return strings.TrimSpace(value)
	default:
		return "1:1"
	}
}

func normalizeGeminiImageSize(model, value string) (string, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" || value == "AUTO" {
		return "1K", nil
	}
	if value == "512" {
		if strings.TrimSpace(model) == "gemini-2.5-flash-image" {
			return value, nil
		}
		return "", fmt.Errorf("image_size 512 is only supported for gemini-2.5-flash-image")
	}
	if value == "1K" || value == "2K" || value == "4K" {
		return value, nil
	}
	return "", fmt.Errorf("unsupported Gemini image_size %q", value)
}

func imageFormatFromMIME(mimeType string) string {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if after, ok := strings.CutPrefix(mimeType, "image/"); ok && after != "" {
		return after
	}
	return "png"
}

func metadataSlice(value any) []any {
	if existing, ok := value.([]any); ok {
		return append([]any(nil), existing...)
	}
	return nil
}

func emitGenerateEvent(onEvent func(GenerateStreamEvent), event GenerateStreamEvent) {
	if onEvent != nil {
		onEvent(event)
	}
}
