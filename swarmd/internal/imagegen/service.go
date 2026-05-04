package imagegen

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/imagegenlog"
	"swarm/packages/swarmd/internal/provider/codex"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	ProviderCodexOpenAI     = "codex_openai"
	ProviderGoogleImagen    = "google_imagen"
	TargetWorkspaceImage    = "workspace_image_session"
	defaultCodexImageModel  = "gpt-5.5"
	generatedImageExtension = ".png"
	assetPathMetadataKey    = "tool_storage_path"
	assetURLBase            = "/v1/image/assets"
)

type CodexImageClient interface {
	GenerateImage(ctx context.Context, req codex.ImageGenerationRequest) (codex.ImageGenerationResult, error)
}

type Service struct {
	codexClient  CodexImageClient
	authStore    *pebblestore.AuthStore
	imageThreads *pebblestore.ImageThreadStore
}

type GenerateRequest struct {
	Provider      string                    `json:"provider"`
	Model         string                    `json:"model"`
	Prompt        string                    `json:"prompt"`
	Count         int                       `json:"count"`
	Size          string                    `json:"size,omitempty"`
	PartialImages int                       `json:"partial_images,omitempty"`
	Settings      map[string]any            `json:"settings,omitempty"`
	Target        GenerationTarget          `json:"target"`
	OnEvent       func(GenerateStreamEvent) `json:"-"`
}

type GenerateStreamEvent struct {
	Type              string `json:"type"`
	ItemID            string `json:"item_id,omitempty"`
	OutputIndex       int    `json:"output_index,omitempty"`
	SequenceNumber    int    `json:"sequence_number,omitempty"`
	PartialImageIndex int    `json:"partial_image_index,omitempty"`
	PartialImageB64   string `json:"partial_image_b64,omitempty"`
	OutputFormat      string `json:"output_format,omitempty"`
	Size              string `json:"size,omitempty"`
	Quality           string `json:"quality,omitempty"`
	Background        string `json:"background,omitempty"`
}

type GenerationTarget struct {
	Kind     string `json:"kind"`
	ThreadID string `json:"thread_id"`
}

type GeneratedAsset struct {
	pebblestore.ImageAssetSnapshot
	URL           string `json:"url,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Model         string `json:"model,omitempty"`
}

type PartialImage struct {
	ItemID            string `json:"item_id,omitempty"`
	OutputIndex       int    `json:"output_index,omitempty"`
	SequenceNumber    int    `json:"sequence_number,omitempty"`
	PartialImageIndex int    `json:"partial_image_index,omitempty"`
	Base64Image       string `json:"base64_image"`
	DataURL           string `json:"data_url"`
	OutputFormat      string `json:"output_format,omitempty"`
	Size              string `json:"size,omitempty"`
	Quality           string `json:"quality,omitempty"`
	Background        string `json:"background,omitempty"`
}

type GenerateResult struct {
	Assets           []GeneratedAsset                 `json:"assets"`
	Partials         []PartialImage                   `json:"partials,omitempty"`
	Target           *WorkspaceImageSessionTargetInfo `json:"target,omitempty"`
	ProviderResponse map[string]any                   `json:"provider_response,omitempty"`
}

type WorkspaceImageSessionTargetInfo struct {
	Kind   string                          `json:"kind"`
	Thread pebblestore.ImageThreadSnapshot `json:"thread"`
}

type ProviderStatus struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Ready         bool     `json:"ready"`
	Reason        string   `json:"reason,omitempty"`
	DefaultModel  string   `json:"default_model"`
	Models        []string `json:"models"`
	RequiresOAuth bool     `json:"requires_oauth,omitempty"`
}

type Capabilities struct {
	Providers []ProviderStatus `json:"providers"`
}

type imageSession struct {
	thread      pebblestore.ImageThreadSnapshot
	storagePath string
}

func NewService(codexClient CodexImageClient, authStore *pebblestore.AuthStore, imageThreads *pebblestore.ImageThreadStore) *Service {
	return &Service{codexClient: codexClient, authStore: authStore, imageThreads: imageThreads}
}

func ProviderResponseFromError(err error) (map[string]any, bool) {
	return codex.ProviderResponseFromError(err)
}

func (s *Service) SetImageThreadStore(store *pebblestore.ImageThreadStore) {
	if s == nil {
		return
	}
	s.imageThreads = store
}

func (s *Service) Capabilities(context.Context) (Capabilities, error) {
	codexStatus := ProviderStatus{
		ID:            ProviderCodexOpenAI,
		Label:         "GPT Image 1.5 via Codex",
		DefaultModel:  defaultCodexImageModel,
		Models:        []string{defaultCodexImageModel},
		RequiresOAuth: true,
	}
	if s == nil || s.codexClient == nil || s.authStore == nil {
		codexStatus.Ready = false
		codexStatus.Reason = "codex image provider is not configured"
	} else {
		record, ok, err := s.authStore.GetCodexAuthRecord()
		if err != nil {
			return Capabilities{}, fmt.Errorf("read codex auth: %w", err)
		}
		if !ok || record.Type != pebblestore.CodexAuthTypeOAuth || strings.TrimSpace(record.AccessToken) == "" || strings.TrimSpace(record.RefreshToken) == "" {
			codexStatus.Ready = false
			codexStatus.Reason = "connect Codex with OAuth to enable image generation"
		} else {
			codexStatus.Ready = true
		}
	}
	return Capabilities{Providers: []ProviderStatus{codexStatus}}, nil
}

func (s *Service) Generate(ctx context.Context, req GenerateRequest) (GenerateResult, error) {
	if s == nil {
		return GenerateResult{}, errors.New("image generation service is not configured")
	}
	provider, count, partialImages, prompt, err := normalizeGenerateRequest(req)
	if err != nil {
		return GenerateResult{}, err
	}

	switch provider {
	case ProviderCodexOpenAI:
		return s.generateCodexOpenAI(ctx, req, count, partialImages, prompt)
	case ProviderGoogleImagen:
		return GenerateResult{}, errors.New("Google Imagen generation is not implemented yet")
	default:
		return GenerateResult{}, fmt.Errorf("unsupported image provider %q", provider)
	}
}

func normalizeGenerateRequest(req GenerateRequest) (provider string, count int, partialImages int, prompt string, err error) {
	provider = strings.TrimSpace(req.Provider)
	if provider == "" || provider == "openai" || provider == "codex" {
		provider = ProviderCodexOpenAI
	}
	count = req.Count
	if count == 0 {
		count = 1
	}
	if count < 1 || count > 3 {
		return "", 0, 0, "", errors.New("count must be between 1 and 3")
	}
	partialImages = req.PartialImages
	if partialImages < 0 {
		partialImages = 0
	}
	if partialImages > 3 {
		partialImages = 3
	}
	prompt = strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return "", 0, 0, "", errors.New("prompt is required")
	}
	return provider, count, partialImages, prompt, nil
}

func (s *Service) generateCodexOpenAI(ctx context.Context, req GenerateRequest, count int, partialImages int, prompt string) (GenerateResult, error) {
	if s.codexClient == nil {
		imageGenerationLogf("stage=preflight reason=codex_client_nil will_save=false")
		return GenerateResult{}, errors.New("codex image provider is not configured")
	}
	session, err := s.openImageSession(req.Target)
	if err != nil {
		imageGenerationLogf("stage=open_session thread_id=%q reason=%q will_save=false", strings.TrimSpace(req.Target.ThreadID), err.Error())
		return GenerateResult{}, err
	}
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		modelID = defaultCodexImageModel
	}
	imageGenerationLogf("stage=start provider=%q model=%q thread_id=%q storage_path=%q requested_count=%d partial_images=%d", ProviderCodexOpenAI, modelID, session.thread.ID, session.storagePath, count, partialImages)

	partials := make([]PartialImage, 0, partialImages*count)
	assets := make([]GeneratedAsset, 0, count)
	updatedThread := session.thread
	var providerResponse map[string]any
	for index := 0; index < count; index++ {
		streamCapture := newImageStreamFrameCapture(index)
		imageGenerationLogf("stage=provider_call_start thread_id=%q slot=%d requested_count=%d storage_path=%q", session.thread.ID, index+1, count, session.storagePath)
		generated, err := s.codexClient.GenerateImage(ctx, codex.ImageGenerationRequest{
			Model:         modelID,
			Prompt:        prompt,
			Size:          firstNonEmpty(req.Size, settingString(req.Settings, "size")),
			Count:         1,
			PartialImages: partialImages,
			OnEvent:       codexImageGenerationEventCallback(req.OnEvent, index, streamCapture),
		})
		if err != nil {
			imageGenerationLogf("stage=provider_call_error thread_id=%q slot=%d reason=%q captured_frames=%d latest_stream_png_bytes=%d will_save=false", session.thread.ID, index+1, err.Error(), streamCapture.FrameCount(), len(streamCapture.LatestPNG()))
			return GenerateResult{}, err
		}
		providerResponse = generated.ProviderResponse
		mappedPartials := mapCodexPartialImages(generated.PartialImages, index)
		partials = append(partials, mappedPartials...)
		imageGenerationLogf("stage=provider_call_done thread_id=%q slot=%d provider_response=%s result_count=%d decoded_png_bytes=%d partials_from_result=%d partials_forwarded=%d captured_frames=%d latest_stream_png_bytes=%d provider_response_keys=%q", session.thread.ID, index+1, providerResponseShape(generated.ProviderResponse), len(generated.Results), len(generated.DecodedPNG), len(generated.PartialImages), len(mappedPartials), streamCapture.FrameCount(), len(streamCapture.LatestPNG()), strings.Join(sortedAnyMapKeys(generated.ProviderResponse), ","))
		completed, err := completedCodexImages(generated, 1, streamCapture.LatestPNG())
		if err != nil {
			imageGenerationLogf("stage=final_validation_failed thread_id=%q slot=%d reason=%q result_count=%d decoded_png_bytes=%d partials_from_result=%d captured_frames=%d latest_stream_png_bytes=%d provider_response=%s provider_response_keys=%q will_save=false", session.thread.ID, index+1, err.Error(), len(generated.Results), len(generated.DecodedPNG), len(generated.PartialImages), streamCapture.FrameCount(), len(streamCapture.LatestPNG()), providerResponseShape(generated.ProviderResponse), strings.Join(sortedAnyMapKeys(generated.ProviderResponse), ","))
			return GenerateResult{}, err
		}
		completed[0].OutputIndex = index
		slotAssets, slotThread, err := s.saveGeneratedImages(session, completed, ProviderCodexOpenAI, modelID)
		if err != nil {
			imageGenerationLogf("stage=slot_save_failed thread_id=%q slot=%d reason=%q final_png_bytes=%d storage_path=%q will_save=false", session.thread.ID, index+1, err.Error(), len(completed[0].DecodedPNG), session.storagePath)
			return GenerateResult{}, err
		}
		assets = append(assets, slotAssets...)
		updatedThread = slotThread
		session.thread = slotThread
		imageGenerationLogf("stage=slot_save_done thread_id=%q slot=%d saved_assets=%d total_saved_assets=%d storage_path=%q image_thread_assets=%d", session.thread.ID, index+1, len(slotAssets), len(assets), session.storagePath, len(updatedThread.ImageAssets))
	}
	if len(assets) != count {
		err := fmt.Errorf("backend saved %d image assets, expected exactly %d", len(assets), count)
		imageGenerationLogf("stage=count_mismatch_after_save thread_id=%q reason=%q saved_assets=%d expected=%d storage_path=%q", session.thread.ID, err.Error(), len(assets), count, session.storagePath)
		return GenerateResult{}, err
	}
	imageGenerationLogf("stage=success thread_id=%q saved_assets=%d expected=%d storage_path=%q", session.thread.ID, len(assets), count, session.storagePath)
	return GenerateResult{
		Assets:           assets,
		Partials:         partials,
		Target:           &WorkspaceImageSessionTargetInfo{Kind: TargetWorkspaceImage, Thread: updatedThread},
		ProviderResponse: providerResponse,
	}, nil
}

func completedCodexImages(generated codex.ImageGenerationResult, expectedCount int, streamRecoveryPNG []byte) ([]codex.ImageGenerationResult, error) {
	finals := generated.Results
	if len(finals) == 0 && len(generated.DecodedPNG) > 0 {
		finals = []codex.ImageGenerationResult{generated}
	}
	if len(finals) == 0 && len(streamRecoveryPNG) > 0 {
		finals = []codex.ImageGenerationResult{{
			ResponseID:       generated.ResponseID,
			Model:            generated.Model,
			CallID:           firstNonEmpty(generated.CallID, "stream_frame_recovery"),
			OutputIndex:      generated.OutputIndex,
			RevisedPrompt:    generated.RevisedPrompt,
			DecodedPNG:       streamRecoveryPNG,
			PartialImages:    generated.PartialImages,
			ProviderResponse: generated.ProviderResponse,
		}}
		imageGenerationLogf("stage=final_validation_stream_recovery source=latest_stream_frame stream_recovery_png_bytes=%d result_count=0", len(streamRecoveryPNG))
	}
	if len(finals) == 0 {
		return nil, errors.New("codex image generation returned no final PNG image data to save")
	}
	if len(finals) != expectedCount {
		return nil, fmt.Errorf("codex image generation returned %d final image(s), expected exactly %d", len(finals), expectedCount)
	}
	for index, finalImage := range finals {
		if len(finalImage.DecodedPNG) == 0 {
			return nil, fmt.Errorf("codex final image %d has no PNG bytes", index+1)
		}
		if !looksLikePNG(finalImage.DecodedPNG) {
			return nil, fmt.Errorf("codex final image %d is not a PNG image", index+1)
		}
	}
	return finals, nil
}

func (s *Service) openImageSession(target GenerationTarget) (imageSession, error) {
	if strings.TrimSpace(target.Kind) != TargetWorkspaceImage {
		return imageSession{}, errors.New("target.kind must be workspace_image_session")
	}
	if s.imageThreads == nil {
		return imageSession{}, errors.New("image thread store is not configured")
	}
	threadID := strings.TrimSpace(target.ThreadID)
	if threadID == "" {
		return imageSession{}, errors.New("thread_id is required")
	}
	thread, ok, err := s.imageThreads.Get(threadID)
	if err != nil {
		return imageSession{}, err
	}
	if !ok {
		return imageSession{}, errors.New("image thread not found")
	}
	storagePath, err := appstorage.WorkspaceDataDir(thread.WorkspacePath, "tools", "image", "sessions", thread.ID)
	if err != nil {
		return imageSession{}, fmt.Errorf("resolve image session storage: %w", err)
	}
	storagePath = filepath.Clean(storagePath)
	if storagePath == "." || storagePath == "" {
		return imageSession{}, errors.New("image session storage path is invalid")
	}
	thread.Metadata = imageThreadStorageMetadata(thread.Metadata, storagePath)
	thread.ImageFolders = []string{storagePath}
	thread, err = s.imageThreads.Update(thread)
	if err != nil {
		return imageSession{}, fmt.Errorf("update image session storage metadata: %w", err)
	}
	return imageSession{thread: thread, storagePath: storagePath}, nil
}

func (s *Service) saveGeneratedImages(session imageSession, finalImages []codex.ImageGenerationResult, provider, modelID string) ([]GeneratedAsset, pebblestore.ImageThreadSnapshot, error) {
	if len(finalImages) == 0 {
		imageGenerationLogf("stage=save_preflight reason=no_final_images thread_id=%q storage_path=%q will_write_disk=false will_update_db=false", session.thread.ID, session.storagePath)
		return nil, pebblestore.ImageThreadSnapshot{}, errors.New("no final image data to save")
	}
	thread := session.thread
	storagePath := filepath.Clean(strings.TrimSpace(session.storagePath))
	if storagePath == "." || storagePath == "" {
		imageGenerationLogf("stage=save_preflight reason=missing_storage_path thread_id=%q storage_path=%q will_write_disk=false will_update_db=false", thread.ID, session.storagePath)
		return nil, pebblestore.ImageThreadSnapshot{}, errors.New("image session storage path is required")
	}
	imageGenerationLogf("stage=save_start thread_id=%q storage_path=%q final_images=%d existing_thread_assets=%d", thread.ID, storagePath, len(finalImages), len(thread.ImageAssets))
	if err := os.MkdirAll(storagePath, appstorage.PrivateDirPerm); err != nil {
		imageGenerationLogf("stage=save_storage_prepare_failed thread_id=%q storage_path=%q reason=%q will_write_disk=false will_update_db=false", thread.ID, storagePath, err.Error())
		return nil, pebblestore.ImageThreadSnapshot{}, fmt.Errorf("create image session storage: %w", err)
	}
	if err := os.Chmod(storagePath, appstorage.PrivateDirPerm); err != nil {
		imageGenerationLogf("stage=save_storage_protect_failed thread_id=%q storage_path=%q reason=%q will_write_disk=false will_update_db=false", thread.ID, storagePath, err.Error())
		return nil, pebblestore.ImageThreadSnapshot{}, fmt.Errorf("protect image session storage: %w", err)
	}

	assets := make([]GeneratedAsset, 0, len(finalImages))
	for index, finalImage := range finalImages {
		assetID := newAssetID()
		baseName := imageAssetBaseName(index, finalImage, assetID)
		fileName := uniqueAssetFilename(storagePath, baseName, generatedImageExtension)
		targetPath := filepath.Join(storagePath, fileName)
		imageGenerationLogf("stage=file_write_attempt thread_id=%q image_index=%d asset_id=%q output_index=%d call_id=%q png_bytes=%d target_path=%q", thread.ID, index+1, assetID, finalImage.OutputIndex, finalImage.CallID, len(finalImage.DecodedPNG), targetPath)
		if !pathWithinRoot(storagePath, targetPath) {
			imageGenerationLogf("stage=file_write_rejected thread_id=%q image_index=%d reason=path_escapes_storage storage_path=%q target_path=%q will_update_db=false", thread.ID, index+1, storagePath, targetPath)
			return nil, pebblestore.ImageThreadSnapshot{}, errors.New("generated image path escapes managed session storage")
		}
		info, err := writePrivateFileAtomic(targetPath, finalImage.DecodedPNG)
		if err != nil {
			imageGenerationLogf("stage=file_write_failed thread_id=%q image_index=%d reason=%q png_bytes=%d target_path=%q will_update_db=false", thread.ID, index+1, err.Error(), len(finalImage.DecodedPNG), targetPath)
			return nil, pebblestore.ImageThreadSnapshot{}, fmt.Errorf("save generated image %d: %w", index+1, err)
		}
		imageGenerationLogf("stage=file_write_done thread_id=%q image_index=%d asset_id=%q size_bytes=%d target_path=%q", thread.ID, index+1, assetID, info.Size(), targetPath)
		asset := GeneratedAsset{
			ImageAssetSnapshot: pebblestore.ImageAssetSnapshot{
				ID:         assetID,
				Name:       fileName,
				Path:       targetPath,
				Extension:  strings.TrimPrefix(generatedImageExtension, "."),
				SizeBytes:  info.Size(),
				ModifiedAt: info.ModTime().UnixMilli(),
			},
			URL:           AssetURL(thread.ID, assetID),
			RevisedPrompt: finalImage.RevisedPrompt,
			Provider:      provider,
			Model:         modelID,
		}
		assets = append(assets, asset)
	}

	thread.Metadata = imageThreadStorageMetadata(thread.Metadata, storagePath)
	thread.ImageFolders = []string{storagePath}
	for _, asset := range assets {
		thread.ImageAssets = append(thread.ImageAssets, asset.ImageAssetSnapshot)
		thread.ImageAssetOrder = appendUniqueString(thread.ImageAssetOrder, asset.ID)
	}
	imageGenerationLogf("stage=db_update_attempt thread_id=%q new_assets=%d total_thread_assets=%d storage_path=%q", thread.ID, len(assets), len(thread.ImageAssets), storagePath)
	updated, err := s.imageThreads.Update(thread)
	if err != nil {
		imageGenerationLogf("stage=db_update_failed thread_id=%q reason=%q new_assets=%d storage_path=%q", thread.ID, err.Error(), len(assets), storagePath)
		return nil, pebblestore.ImageThreadSnapshot{}, fmt.Errorf("update image thread asset metadata: %w", err)
	}
	imageGenerationLogf("stage=db_update_done thread_id=%q new_assets=%d total_thread_assets=%d total_asset_order=%d storage_path=%q", updated.ID, len(assets), len(updated.ImageAssets), len(updated.ImageAssetOrder), storagePath)
	return assets, updated, nil
}

func imageThreadStorageMetadata(existing map[string]any, storagePath string) map[string]any {
	metadata := make(map[string]any, len(existing)+4)
	for key, value := range existing {
		metadata[key] = value
	}
	metadata[assetPathMetadataKey] = storagePath
	metadata["tool_kind"] = "image"
	metadata["session_schema_version"] = 1
	metadata["storage_area"] = "app_managed_workspace_bucket/tools/image/sessions"
	return metadata
}

func imageAssetBaseName(index int, generated codex.ImageGenerationResult, assetID string) string {
	nameParts := []string{fmt.Sprintf("image-%02d", index+1)}
	if generated.OutputIndex >= 0 {
		nameParts = append(nameParts, fmt.Sprintf("output-%02d", generated.OutputIndex+1))
	}
	if callID := sanitizeFilename(generated.CallID); callID != "" {
		nameParts = append(nameParts, callID)
	}
	if assetID != "" {
		nameParts = append(nameParts, assetID)
	}
	return sanitizeFilename(strings.Join(nameParts, "-"))
}

func writePrivateFileAtomic(targetPath string, data []byte) (os.FileInfo, error) {
	targetPath = filepath.Clean(strings.TrimSpace(targetPath))
	if targetPath == "." || targetPath == "" {
		return nil, errors.New("target path is required")
	}
	if len(data) == 0 {
		return nil, errors.New("file data is empty")
	}
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, appstorage.PrivateDirPerm); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-image-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(appstorage.PrivateFilePerm); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return nil, err
	}
	cleanup = false
	if err := os.Chmod(targetPath, appstorage.PrivateFilePerm); err != nil {
		return nil, err
	}
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("saved image path is a directory")
	}
	if info.Size() <= 0 {
		return nil, errors.New("saved image file is empty")
	}
	return info, nil
}

func (s *Service) ResolveAssetPath(threadID, assetID string) (string, pebblestore.ImageAssetSnapshot, error) {
	session, err := s.openImageSession(GenerationTarget{Kind: TargetWorkspaceImage, ThreadID: threadID})
	if err != nil {
		return "", pebblestore.ImageAssetSnapshot{}, err
	}
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return "", pebblestore.ImageAssetSnapshot{}, errors.New("asset id is required")
	}
	for _, asset := range session.thread.ImageAssets {
		if asset.ID != assetID {
			continue
		}
		assetPath := filepath.Clean(strings.TrimSpace(asset.Path))
		if assetPath == "." || assetPath == "" || !pathWithinRoot(session.storagePath, assetPath) {
			return "", pebblestore.ImageAssetSnapshot{}, errors.New("image asset path is outside managed session storage")
		}
		return assetPath, asset, nil
	}
	return "", pebblestore.ImageAssetSnapshot{}, errors.New("image asset not found")
}

func (s *Service) ResolveSessionStoragePath(threadID string) (string, pebblestore.ImageThreadSnapshot, error) {
	session, err := s.openImageSession(GenerationTarget{Kind: TargetWorkspaceImage, ThreadID: threadID})
	if err != nil {
		return "", pebblestore.ImageThreadSnapshot{}, err
	}
	return session.storagePath, session.thread, nil
}

type imageStreamFrameCapture struct {
	mu                 sync.Mutex
	outputIndex        int
	frames             int
	invalidFrames      int
	latestPNG          []byte
	latestFrameIndex   int
	latestSequence     int
	latestItemID       string
	latestOutputFormat string
}

func newImageStreamFrameCapture(outputIndex int) *imageStreamFrameCapture {
	return &imageStreamFrameCapture{outputIndex: outputIndex, latestFrameIndex: -1, latestSequence: -1}
}

func (c *imageStreamFrameCapture) Record(event codex.ImageGenerationStreamEvent, effectiveOutputIndex int) {
	if c == nil || event.Type != codex.ImageGenerationStreamEventPartialImage {
		return
	}
	b64 := strings.TrimSpace(event.PartialImageB64)
	if b64 == "" {
		imageGenerationLogf("stage=stream_frame_skip reason=empty_base64 output_index=%d item_id=%q partial_index=%d sequence=%d", effectiveOutputIndex, event.ItemID, event.PartialImageIndex, event.SequenceNumber)
		return
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(b64)
	if err != nil || !looksLikePNG(decoded) {
		c.mu.Lock()
		c.frames++
		c.invalidFrames++
		frames := c.frames
		invalidFrames := c.invalidFrames
		c.mu.Unlock()
		reason := "not_png"
		if err != nil {
			reason = "base64_decode_failed: " + err.Error()
		}
		imageGenerationLogf("stage=stream_frame_invalid reason=%q output_index=%d item_id=%q partial_index=%d sequence=%d base64_chars=%d decoded_bytes=%d frames=%d invalid_frames=%d", reason, effectiveOutputIndex, event.ItemID, event.PartialImageIndex, event.SequenceNumber, len(b64), len(decoded), frames, invalidFrames)
		return
	}
	c.mu.Lock()
	c.frames++
	c.latestPNG = append(c.latestPNG[:0], decoded...)
	c.latestFrameIndex = event.PartialImageIndex
	c.latestSequence = event.SequenceNumber
	c.latestItemID = strings.TrimSpace(event.ItemID)
	c.latestOutputFormat = strings.TrimSpace(event.OutputFormat)
	frames := c.frames
	c.mu.Unlock()
	imageGenerationLogf("stage=stream_frame_captured output_index=%d item_id=%q partial_index=%d sequence=%d png_bytes=%d base64_chars=%d frames=%d output_format=%q", effectiveOutputIndex, event.ItemID, event.PartialImageIndex, event.SequenceNumber, len(decoded), len(b64), frames, event.OutputFormat)
}

func (c *imageStreamFrameCapture) LatestPNG() []byte {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.latestPNG) == 0 {
		return nil
	}
	return append([]byte(nil), c.latestPNG...)
}

func (c *imageStreamFrameCapture) FrameCount() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.frames
}

func codexImageGenerationEventCallback(onEvent func(GenerateStreamEvent), outputIndex int, capture *imageStreamFrameCapture) func(codex.ImageGenerationStreamEvent) {
	if onEvent == nil && capture == nil {
		return nil
	}
	return func(event codex.ImageGenerationStreamEvent) {
		effectiveOutputIndex := outputIndex
		if event.OutputIndex >= 0 && outputIndex == 0 {
			effectiveOutputIndex = event.OutputIndex
		}
		if capture != nil {
			capture.Record(event, effectiveOutputIndex)
		}
		if onEvent == nil {
			return
		}
		onEvent(GenerateStreamEvent{
			Type:              string(event.Type),
			ItemID:            event.ItemID,
			OutputIndex:       effectiveOutputIndex,
			SequenceNumber:    event.SequenceNumber,
			PartialImageIndex: event.PartialImageIndex,
			PartialImageB64:   event.PartialImageB64,
			OutputFormat:      event.OutputFormat,
			Size:              event.Size,
			Quality:           event.Quality,
			Background:        event.Background,
		})
	}
}

func mapCodexPartialImages(partials []codex.ImageGenerationPartialImage, outputIndex int) []PartialImage {
	if len(partials) == 0 {
		return nil
	}
	out := make([]PartialImage, 0, len(partials))
	for _, partial := range partials {
		base64Image := strings.TrimSpace(partial.Base64Image)
		if base64Image == "" {
			continue
		}
		format := strings.TrimSpace(partial.OutputFormat)
		if format == "" {
			format = "png"
		}
		effectiveOutputIndex := outputIndex
		if partial.OutputIndex >= 0 && outputIndex == 0 {
			effectiveOutputIndex = partial.OutputIndex
		}
		out = append(out, PartialImage{
			ItemID:            partial.ItemID,
			OutputIndex:       effectiveOutputIndex,
			SequenceNumber:    partial.SequenceNumber,
			PartialImageIndex: partial.PartialImageIndex,
			Base64Image:       base64Image,
			DataURL:           "data:image/" + format + ";base64," + base64Image,
			OutputFormat:      format,
			Size:              partial.Size,
			Quality:           partial.Quality,
			Background:        partial.Background,
		})
	}
	return out
}

func AssetURL(threadID, assetID string) string {
	return assetURLBase + "?thread_id=" + urlQueryEscape(threadID) + "&asset_id=" + urlQueryEscape(assetID)
}

func settingString(settings map[string]any, key string) string {
	if settings == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(settings[key]))
}

func imageGenerationLogf(format string, args ...any) {
	imagegenlog.Printf("", format, args...)
}

func providerResponseShape(response map[string]any) string {
	if response == nil {
		return "nil"
	}
	return fmt.Sprintf("keys=%s", strings.Join(sortedAnyMapKeys(response), ","))
}

func sortedAnyMapKeys(value map[string]any) []string {
	if len(value) == 0 {
		return nil
	}
	keys := make([]string, 0, len(value))
	for key := range value {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			keys = append(keys, trimmed)
		}
	}
	sort.Strings(keys)
	return keys
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func looksLikePNG(data []byte) bool {
	return len(data) >= 8 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' && data[4] == '\r' && data[5] == '\n' && data[6] == 0x1a && data[7] == '\n'
}

func uniqueAssetFilename(dir, base, ext string) string {
	base = sanitizeFilename(base)
	if base == "" {
		base = "generated-image"
	}
	ext = strings.TrimSpace(ext)
	if ext == "" {
		ext = generatedImageExtension
	}
	candidate := base + ext
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
}

func sanitizeFilename(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 80 {
		out = strings.Trim(out[:80], "-")
	}
	return out
}

func newAssetID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("img_%d", time.Now().UnixNano())
	}
	return "img_" + hex.EncodeToString(buf[:])
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func pathWithinRoot(root string, path string) bool {
	root = filepath.Clean(strings.TrimSpace(root))
	path = filepath.Clean(strings.TrimSpace(path))
	if root == "." || root == "" || path == "." || path == "" {
		return false
	}
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func urlQueryEscape(value string) string {
	replacer := strings.NewReplacer("%", "%25", " ", "%20", "&", "%26", "=", "%3D", "?", "%3F", "#", "%23")
	return replacer.Replace(strings.TrimSpace(value))
}
