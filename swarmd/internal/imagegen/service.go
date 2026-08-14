package imagegen

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegenlog"
	"swarm/packages/swarmd/internal/provider/codex"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	ProviderCodexOpenAI             = "codex_openai"
	ProviderGoogleGemini            = "google_gemini"
	TargetWorkspaceImage            = "workspace_image_session"
	defaultCodexImageModel          = "gpt-5.5"
	generatedImageExtension         = ".png"
	assetPathMetadataKey            = "tool_storage_path"
	assetURLBase                    = "/v1/image/assets"
	geminiMaxParallelRequests       = 10
	managedImageMaxParallelRequests = 4
	managedImageMaxBytes            = 16 << 20
)

type CodexImageClient interface {
	GenerateImage(ctx context.Context, req codex.ImageGenerationRequest) (codex.ImageGenerationResult, error)
}

type GeminiImageClient interface {
	GenerateImage(ctx context.Context, req GeminiImageGenerationRequest) (GeminiImageGenerationResult, error)
}

type ModelCatalog interface {
	ListCatalog(providerID string, limit int) ([]pebblestore.ModelCatalogRecord, error)
}

type Service struct {
	codexClient       CodexImageClient
	geminiImageClient GeminiImageClient
	authStore         *pebblestore.AuthStore
	imageThreads      *pebblestore.ImageThreadStore
	modelCatalog      ModelCatalog
	managedSlots      chan struct{}
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
	Principal     identity.Principal        `json:"-"`
}

type GenerateStreamEvent struct {
	Type              string `json:"type"`
	ItemID            string `json:"item_id,omitempty"`
	OutputIndex       int    `json:"output_index,omitempty"`
	SequenceNumber    int    `json:"sequence_number,omitempty"`
	PartialImageIndex int    `json:"partial_image_index,omitempty"`
	PartialImageB64   string `json:"partial_image_b64,omitempty"`
	OutputFormat      string `json:"output_format,omitempty"`
	MIMEType          string `json:"mime_type,omitempty"`
	Size              string `json:"size,omitempty"`
	Quality           string `json:"quality,omitempty"`
	Background        string `json:"background,omitempty"`
	Text              string `json:"text,omitempty"`
	Thinking          string `json:"thinking,omitempty"`
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

// ManagedGenerateRequest is the provider-neutral, single-image contract used by
// V3 managed artifacts. Provider and model are resolved from the account's
// canonical image setting before this boundary and are never model-authored tool
// arguments.
type ManagedGenerateRequest struct {
	SelectionID string
	Prompt      string
	Size        string
	Settings    map[string]any
	Principal   identity.Principal
}

// ManagedImage is an in-memory provider result ready for direct publication by
// the artifact authority. It intentionally contains no workspace or storage path.
type ManagedImage struct {
	Bytes         []byte
	MediaType     string
	RevisedPrompt string
}

type ModelSelection struct {
	ID          string
	Provider    string
	Model       string
	DisplayName string
}

const DefaultModelSelectionID = "codex-image-gen"

func HardcodedModelSelections() []ModelSelection {
	return []ModelSelection{{ID: DefaultModelSelectionID, Provider: ProviderCodexOpenAI, Model: defaultCodexImageModel, DisplayName: "Codex Image Gen"}}
}

// ResolveModelSelection resolves only the dedicated hardcoded Codex contract.
// Snapshot-backed provider models are resolved by Service.ResolveModelSelection.
func ResolveModelSelection(selectionID string) (ModelSelection, error) {
	selectionID = strings.TrimSpace(selectionID)
	if selectionID == "" {
		selectionID = DefaultModelSelectionID
	}
	if selectionID == DefaultModelSelectionID || selectionID == defaultCodexImageModel {
		return HardcodedModelSelections()[0], nil
	}
	return ModelSelection{}, fmt.Errorf("configured image model %q is not a hardcoded image model", selectionID)
}

func (s *Service) GoogleImageModelSelections() ([]ModelSelection, error) {
	if s == nil || s.modelCatalog == nil {
		return nil, nil
	}
	records, err := s.modelCatalog.ListCatalog("google", 2000)
	if err != nil {
		return nil, fmt.Errorf("list Google image models: %w", err)
	}
	selections := make([]ModelSelection, 0, len(records))
	for _, record := range records {
		if !isSnapshotGoogleGenerateContentImageModel(record) {
			continue
		}
		selections = append(selections, ModelSelection{
			ID: strings.TrimSpace(record.Model), Provider: ProviderGoogleGemini, Model: strings.TrimSpace(record.Model),
			DisplayName: firstNonEmpty(strings.TrimSpace(record.DisplayName), strings.TrimSpace(record.Model)),
		})
	}
	sort.Slice(selections, func(i, j int) bool {
		if selections[i].DisplayName == selections[j].DisplayName {
			return selections[i].Model < selections[j].Model
		}
		return selections[i].DisplayName < selections[j].DisplayName
	})
	return selections, nil
}

func (s *Service) ResolveModelSelection(selectionID string) (ModelSelection, error) {
	selectionID = strings.TrimSpace(selectionID)
	if selectionID == "" || selectionID == DefaultModelSelectionID || selectionID == defaultCodexImageModel {
		return ResolveModelSelection(selectionID)
	}
	selections, err := s.GoogleImageModelSelections()
	if err != nil {
		return ModelSelection{}, err
	}
	for _, selection := range selections {
		if selection.ID == selectionID || selection.Model == selectionID || legacyGoogleSelectionID(selection.DisplayName) == selectionID {
			return selection, nil
		}
	}
	return ModelSelection{}, fmt.Errorf("configured image model %q is not in the current Google image catalog", selectionID)
}

func legacyGoogleSelectionID(displayName string) string {
	fields := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(displayName)), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	if len(fields) == 0 {
		return ""
	}
	return "gemini-" + strings.Join(fields, "-")
}

func isSnapshotGoogleGenerateContentImageModel(record pebblestore.ModelCatalogRecord) bool {
	if !strings.EqualFold(strings.TrimSpace(record.Provider), "google") || !containsStringFold(record.CatalogModalities.Outputs, "image") {
		return false
	}
	if record.Media == nil || record.Media.State != pebblestore.ModelCatalogMediaStateSupported || record.Media.ProviderSurface != provideriface.MediaProviderSurfaceGoogleGenerateContent {
		return false
	}
	var providerSpecific map[string]struct {
		ModelAPISurface string `json:"model_api_surface"`
	}
	if err := json.Unmarshal(record.ProviderSpecific, &providerSpecific); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(providerSpecific["google"].ModelAPISurface), "generate_content")
}

func containsStringFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), wanted) {
			return true
		}
	}
	return false
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

func NewService(codexClient CodexImageClient, authStore *pebblestore.AuthStore, imageThreads *pebblestore.ImageThreadStore, modelCatalog ...ModelCatalog) *Service {
	service := &Service{
		codexClient:       codexClient,
		geminiImageClient: googleGeminiImageClient{},
		authStore:         authStore,
		imageThreads:      imageThreads,
		managedSlots:      make(chan struct{}, managedImageMaxParallelRequests),
	}
	if len(modelCatalog) > 0 {
		service.modelCatalog = modelCatalog[0]
	}
	return service
}

func (s *Service) SetModelCatalog(catalog ModelCatalog) {
	if s == nil {
		return
	}
	s.modelCatalog = catalog
}

func (s *Service) SetGeminiImageClient(client GeminiImageClient) {
	if s == nil {
		return
	}
	s.geminiImageClient = client
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

func (s *Service) Capabilities(ctx context.Context) (Capabilities, error) {
	codexStatus := ProviderStatus{
		ID:            ProviderCodexOpenAI,
		Label:         "Codex Image Gen (OAuth only)",
		DefaultModel:  defaultCodexImageModel,
		Models:        []string{defaultCodexImageModel},
		RequiresOAuth: true,
	}
	if s == nil || s.codexClient == nil || s.authStore == nil {
		codexStatus.Ready = false
		codexStatus.Reason = "codex image provider is not configured"
	} else if principal, ok := identity.PrincipalFromContext(ctx); !ok || !principal.Valid() {
		codexStatus.Ready = false
		codexStatus.Reason = "product identity is required"
	} else if record, ok, err := s.authStore.GetCodexAuthRecordForAccount(principal.AccountScopeID); err != nil {
		return Capabilities{}, fmt.Errorf("read codex image auth: %w", err)
	} else if !ok || strings.TrimSpace(record.AccessToken) == "" || strings.TrimSpace(record.RefreshToken) == "" {
		codexStatus.Ready = false
		codexStatus.Reason = "connect Codex with OAuth to enable image generation"
	} else {
		codexStatus.Ready = true
	}
	googleSelections, err := s.GoogleImageModelSelections()
	if err != nil {
		return Capabilities{}, err
	}
	geminiStatus := ProviderStatus{ID: ProviderGoogleGemini, Label: "Google Gemini"}
	for _, selection := range googleSelections {
		geminiStatus.Models = append(geminiStatus.Models, selection.Model)
	}
	if len(geminiStatus.Models) > 0 {
		geminiStatus.DefaultModel = geminiStatus.Models[0]
	} else {
		geminiStatus.Reason = "no snapshot-backed Google image models are available"
	}
	if s == nil || s.geminiImageClient == nil || s.authStore == nil {
		geminiStatus.Ready = false
		geminiStatus.Reason = "google gemini image provider is not configured"
	} else if principal, ok := identity.PrincipalFromContext(ctx); !ok || !principal.Valid() {
		geminiStatus.Ready = false
		geminiStatus.Reason = "product identity is required"
	} else if record, ok, err := s.authStore.GetActiveCredentialForAccount(principal.AccountScopeID, "google"); err != nil {
		return Capabilities{}, fmt.Errorf("read google image auth: %w", err)
	} else if !ok || strings.TrimSpace(record.APIKey) == "" {
		geminiStatus.Ready = false
		geminiStatus.Reason = "connect a Google API key to enable Gemini image generation"
	} else if len(geminiStatus.Models) > 0 {
		geminiStatus.Ready = true
	}
	return Capabilities{Providers: []ProviderStatus{codexStatus, geminiStatus}}, nil
}

// GenerateManagedImage performs exactly one billed provider call and returns
// exactly one verified image without writing image-tool session storage.
func (s *Service) GenerateManagedImage(ctx context.Context, req ManagedGenerateRequest) (ManagedImage, error) {
	if s == nil {
		return ManagedImage{}, errors.New("image generation service is not configured")
	}
	if !req.Principal.Valid() {
		return ManagedImage{}, identity.ErrPrincipalRequired
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return ManagedImage{}, errors.New("prompt is required")
	}
	if s.managedSlots != nil {
		select {
		case s.managedSlots <- struct{}{}:
			defer func() { <-s.managedSlots }()
		case <-ctx.Done():
			return ManagedImage{}, ctx.Err()
		}
	}
	selection, err := s.ResolveModelSelection(req.SelectionID)
	if err != nil {
		return ManagedImage{}, err
	}
	switch selection.Provider {
	case ProviderCodexOpenAI:
		if s.codexClient == nil || s.authStore == nil {
			return ManagedImage{}, errors.New("codex image provider is not configured")
		}
		record, ok, err := s.authStore.GetCodexAuthRecordForAccount(req.Principal.AccountScopeID)
		if err != nil {
			return ManagedImage{}, fmt.Errorf("read codex image auth: %w", err)
		}
		if !ok || strings.TrimSpace(record.AccessToken) == "" || strings.TrimSpace(record.RefreshToken) == "" {
			return ManagedImage{}, errors.New("connect Codex with OAuth to enable image generation")
		}
		generated, err := s.codexClient.GenerateImage(identity.ContextWithPrincipal(ctx, req.Principal), codex.ImageGenerationRequest{
			Model: selection.Model, Prompt: prompt, Size: managedCodexImageSize(req), Count: 1,
		})
		if err != nil {
			return ManagedImage{}, err
		}
		completed, err := completedCodexImages(generated, 1, nil)
		if err != nil {
			return ManagedImage{}, err
		}
		return managedImageFromCodex(completed[0])
	case ProviderGoogleGemini:
		if s.geminiImageClient == nil || s.authStore == nil {
			return ManagedImage{}, errors.New("Gemini image provider is not configured")
		}
		record, ok, err := s.authStore.GetActiveCredentialForAccount(req.Principal.AccountScopeID, "google")
		if err != nil {
			return ManagedImage{}, fmt.Errorf("read google auth: %w", err)
		}
		if !ok || strings.TrimSpace(record.APIKey) == "" {
			return ManagedImage{}, errors.New("connect a Google API key to enable Gemini image generation")
		}
		imageSize, err := normalizeGeminiImageSize(selection.Model, managedGeminiImageSize(req))
		if err != nil {
			return ManagedImage{}, err
		}
		generated, err := s.geminiImageClient.GenerateImage(identity.ContextWithPrincipal(ctx, req.Principal), GeminiImageGenerationRequest{
			APIKey: record.APIKey, Model: selection.Model, Prompt: prompt,
			AspectRatio: managedGeminiAspectRatio(req), ImageSize: imageSize,
		})
		if err != nil {
			return ManagedImage{}, err
		}
		completed, err := completedGeminiImages(generated, 0, "")
		if err != nil {
			return ManagedImage{}, err
		}
		return managedImageFromCodex(completed[0])
	default:
		return ManagedImage{}, fmt.Errorf("unsupported image provider %q", selection.Provider)
	}
}

func managedCodexImageSize(req ManagedGenerateRequest) string {
	if value := strings.TrimSpace(firstNonEmpty(req.Size, settingString(req.Settings, "size"))); value != "" {
		return value
	}
	switch strings.ToUpper(strings.TrimSpace(settingString(req.Settings, "image_size"))) {
	case "512", "512X512":
		return "512x512"
	case "1K", "1024", "1024X1024":
		return "1024x1024"
	case "2K", "2048", "2048X2048":
		return "2048x2048"
	case "4K", "4096", "4096X4096":
		return "4096x4096"
	default:
		return ""
	}
}

func managedGeminiImageSize(req ManagedGenerateRequest) string {
	if value := strings.TrimSpace(settingString(req.Settings, "image_size")); value != "" {
		return value
	}
	if _, imageSize := splitPortableImageDimensions(firstNonEmpty(req.Size, settingString(req.Settings, "size"))); imageSize != "" {
		return imageSize
	}
	return ""
}

func managedGeminiAspectRatio(req ManagedGenerateRequest) string {
	if value := strings.TrimSpace(settingString(req.Settings, "aspect_ratio")); value != "" {
		return normalizeGeminiAspectRatio(value)
	}
	aspectRatio, _ := splitPortableImageDimensions(firstNonEmpty(req.Size, settingString(req.Settings, "size")))
	return normalizeGeminiAspectRatio(aspectRatio)
}

func splitPortableImageDimensions(value string) (aspectRatio, imageSize string) {
	value = strings.ToUpper(strings.TrimSpace(value))
	parts := strings.Split(value, "X")
	if len(parts) != 2 {
		return "", ""
	}
	var width, height int
	if _, err := fmt.Sscanf(parts[0], "%d", &width); err != nil || width < 1 {
		return "", ""
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &height); err != nil || height < 1 {
		return "", ""
	}
	switch {
	case width == height:
		aspectRatio = "1:1"
	case width*2 == height*3:
		aspectRatio = "3:2"
	case width*3 == height*2:
		aspectRatio = "2:3"
	case width*9 == height*16:
		aspectRatio = "16:9"
	case width*16 == height*9:
		aspectRatio = "9:16"
	}
	maxDimension := width
	if height > maxDimension {
		maxDimension = height
	}
	switch {
	case maxDimension <= 512:
		imageSize = "512"
	case maxDimension <= 1024:
		imageSize = "1K"
	case maxDimension <= 2048:
		imageSize = "2K"
	default:
		imageSize = "4K"
	}
	return aspectRatio, imageSize
}

func managedImageFromCodex(image codex.ImageGenerationResult) (ManagedImage, error) {
	mediaType := finalImageMIME(image)
	if extensionFromMIME(mediaType) == "" || len(image.DecodedPNG) == 0 {
		return ManagedImage{}, errors.New("image generation returned unsupported or empty image data")
	}
	if len(image.DecodedPNG) > managedImageMaxBytes {
		return ManagedImage{}, fmt.Errorf("generated image exceeds %d bytes", managedImageMaxBytes)
	}
	return ManagedImage{Bytes: append([]byte(nil), image.DecodedPNG...), MediaType: mediaType, RevisedPrompt: strings.TrimSpace(image.RevisedPrompt)}, nil
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
	case ProviderGoogleGemini:
		return s.generateGoogleGemini(ctx, req, count, prompt)
	default:
		return GenerateResult{}, fmt.Errorf("unsupported image provider %q", provider)
	}
}

func normalizeGenerateRequest(req GenerateRequest) (provider string, count int, partialImages int, prompt string, err error) {
	provider = strings.TrimSpace(req.Provider)
	if provider == "" || provider == "openai" || provider == "codex" {
		provider = ProviderCodexOpenAI
	}
	if provider == "google" || provider == "gemini" || provider == "nano_banana" {
		provider = ProviderGoogleGemini
	}
	count = req.Count
	if count == 0 {
		count = 1
	}
	maxCount := 3
	if provider == ProviderGoogleGemini {
		maxCount = geminiMaxParallelRequests
	}
	if count < 1 || count > maxCount {
		return "", 0, 0, "", fmt.Errorf("count must be between 1 and %d", maxCount)
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
	if s.authStore == nil {
		return GenerateResult{}, errors.New("codex auth store is not configured")
	}
	if !req.Principal.Valid() {
		return GenerateResult{}, identity.ErrPrincipalRequired
	}
	record, ok, err := s.authStore.GetCodexAuthRecordForAccount(req.Principal.AccountScopeID)
	if err != nil {
		return GenerateResult{}, fmt.Errorf("read codex image auth: %w", err)
	}
	if !ok || strings.TrimSpace(record.AccessToken) == "" || strings.TrimSpace(record.RefreshToken) == "" {
		return GenerateResult{}, errors.New("connect Codex with OAuth to enable image generation")
	}
	session, err := s.openImageSession(req.Principal, req.Target)
	if err != nil {
		imageGenerationLogf("stage=open_session thread_id=%q reason=%q will_save=false", strings.TrimSpace(req.Target.ThreadID), err.Error())
		return GenerateResult{}, err
	}
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		modelID = defaultCodexImageModel
	}
	imageGenerationLogf("stage=start provider=%q model=%q thread_id=%q storage_path=%q requested_count=%d partial_images=%d", ProviderCodexOpenAI, modelID, session.thread.ID, session.storagePath, count, partialImages)

	streamCapture := newImageStreamFrameCapture(0)
	imageGenerationLogf("stage=provider_call_start thread_id=%q requested_count=%d storage_path=%q", session.thread.ID, count, session.storagePath)
	generated, err := s.codexClient.GenerateImage(identity.ContextWithPrincipal(ctx, req.Principal), codex.ImageGenerationRequest{
		Model:         modelID,
		Prompt:        prompt,
		Size:          firstNonEmpty(req.Size, settingString(req.Settings, "size")),
		Count:         count,
		PartialImages: partialImages,
		OnEvent:       codexImageGenerationEventCallback(req.OnEvent, 0, streamCapture),
	})
	if err != nil {
		imageGenerationLogf("stage=provider_call_error thread_id=%q reason=%q captured_frames=%d latest_stream_png_bytes=%d will_save=false", session.thread.ID, err.Error(), streamCapture.FrameCount(), len(streamCapture.LatestPNG()))
		return GenerateResult{}, err
	}
	partials := mapCodexPartialImages(generated.PartialImages, 0)
	imageGenerationLogf("stage=provider_call_done thread_id=%q provider_response=%s result_count=%d decoded_png_bytes=%d partials_from_result=%d partials_forwarded=%d captured_frames=%d latest_stream_png_bytes=%d provider_response_keys=%q", session.thread.ID, providerResponseShape(generated.ProviderResponse), len(generated.Results), len(generated.DecodedPNG), len(generated.PartialImages), len(partials), streamCapture.FrameCount(), len(streamCapture.LatestPNG()), strings.Join(sortedAnyMapKeys(generated.ProviderResponse), ","))
	completed, err := completedCodexImages(generated, count, streamCapture.LatestPNG())
	if err != nil {
		imageGenerationLogf("stage=final_validation_failed thread_id=%q reason=%q result_count=%d decoded_png_bytes=%d partials_from_result=%d captured_frames=%d latest_stream_png_bytes=%d provider_response=%s provider_response_keys=%q will_save=false", session.thread.ID, err.Error(), len(generated.Results), len(generated.DecodedPNG), len(generated.PartialImages), streamCapture.FrameCount(), len(streamCapture.LatestPNG()), providerResponseShape(generated.ProviderResponse), strings.Join(sortedAnyMapKeys(generated.ProviderResponse), ","))
		return GenerateResult{}, err
	}
	for index := range completed {
		completed[index].OutputIndex = index
	}
	assets, updatedThread, err := s.saveGeneratedImages(session, completed, ProviderCodexOpenAI, modelID)
	if err != nil {
		imageGenerationLogf("stage=save_failed thread_id=%q reason=%q final_images=%d storage_path=%q will_save=false", session.thread.ID, err.Error(), len(completed), session.storagePath)
		return GenerateResult{}, err
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
		ProviderResponse: generated.ProviderResponse,
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

func (s *Service) openImageSession(principal identity.Principal, target GenerationTarget) (imageSession, error) {
	if !principal.Valid() {
		return imageSession{}, identity.ErrPrincipalRequired
	}
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
	thread, ok, err := s.imageThreads.GetForAccount(principal.AccountScopeID, threadID)
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
	thread.AccountScopeID = principal.AccountScopeID
	thread.UserID = firstNonEmpty(thread.UserID, principal.UserID)
	thread, err = s.imageThreads.UpdateForAccount(principal.AccountScopeID, thread)
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
		mimeType := finalImageMIME(finalImage)
		ext := extensionFromMIME(mimeType)
		if ext == "" {
			return nil, pebblestore.ImageThreadSnapshot{}, fmt.Errorf("unsupported generated image MIME type %q", mimeType)
		}
		baseName := imageAssetBaseName(index, finalImage, assetID)
		fileName := uniqueAssetFilename(storagePath, baseName, ext)
		targetPath := filepath.Join(storagePath, fileName)
		imageGenerationLogf("stage=file_write_attempt thread_id=%q image_index=%d asset_id=%q output_index=%d call_id=%q mime_type=%q detected_mime=%q image_bytes=%d target_path=%q", thread.ID, index+1, assetID, finalImage.OutputIndex, finalImage.CallID, mimeType, detectImageMIME(finalImage.DecodedPNG), len(finalImage.DecodedPNG), targetPath)
		if !pathWithinRoot(storagePath, targetPath) {
			imageGenerationLogf("stage=file_write_rejected thread_id=%q image_index=%d reason=path_escapes_storage storage_path=%q target_path=%q will_update_db=false", thread.ID, index+1, storagePath, targetPath)
			return nil, pebblestore.ImageThreadSnapshot{}, errors.New("generated image path escapes managed session storage")
		}
		info, err := writePrivateFileAtomic(targetPath, finalImage.DecodedPNG)
		if err != nil {
			imageGenerationLogf("stage=file_write_failed thread_id=%q image_index=%d reason=%q mime_type=%q image_bytes=%d target_path=%q will_update_db=false", thread.ID, index+1, err.Error(), mimeType, len(finalImage.DecodedPNG), targetPath)
			return nil, pebblestore.ImageThreadSnapshot{}, fmt.Errorf("save generated image %d: %w", index+1, err)
		}
		imageGenerationLogf("stage=file_write_done thread_id=%q image_index=%d asset_id=%q size_bytes=%d target_path=%q", thread.ID, index+1, assetID, info.Size(), targetPath)
		asset := GeneratedAsset{
			ImageAssetSnapshot: pebblestore.ImageAssetSnapshot{
				ID:         assetID,
				Name:       fileName,
				Path:       targetPath,
				Extension:  strings.TrimPrefix(ext, "."),
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
	updated, err := s.imageThreads.UpdateForAccount(thread.AccountScopeID, thread)
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
	return s.ResolveAssetPathForPrincipal(identity.Principal{}, threadID, assetID)
}

func (s *Service) ResolveAssetPathForPrincipal(principal identity.Principal, threadID, assetID string) (string, pebblestore.ImageAssetSnapshot, error) {
	session, err := s.openImageSession(principal, GenerationTarget{Kind: TargetWorkspaceImage, ThreadID: threadID})
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
	return s.ResolveSessionStoragePathForPrincipal(identity.Principal{}, threadID)
}

func (s *Service) ResolveSessionStoragePathForPrincipal(principal identity.Principal, threadID string) (string, pebblestore.ImageThreadSnapshot, error) {
	session, err := s.openImageSession(principal, GenerationTarget{Kind: TargetWorkspaceImage, ThreadID: threadID})
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

func looksLikeJPEG(data []byte) bool {
	return len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff
}

func looksLikeWebP(data []byte) bool {
	return len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP"
}

func detectImageMIME(data []byte) string {
	switch {
	case looksLikePNG(data):
		return "image/png"
	case looksLikeJPEG(data):
		return "image/jpeg"
	case looksLikeWebP(data):
		return "image/webp"
	default:
		return "unknown"
	}
}

func imageMagicHex(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	limit := len(data)
	if limit > 16 {
		limit = 16
	}
	return hex.EncodeToString(data[:limit])
}

func dumpInvalidGeneratedImage(storagePath, provider string, outputIndex, imageIndex int, data []byte) (string, error) {
	storagePath = filepath.Clean(strings.TrimSpace(storagePath))
	if storagePath == "." || storagePath == "" {
		return "", errors.New("image session storage path is required")
	}
	if len(data) == 0 {
		return "", errors.New("image data is empty")
	}
	debugDir := filepath.Join(storagePath, "debug")
	if !pathWithinRoot(storagePath, debugDir) {
		return "", errors.New("debug image path escapes managed session storage")
	}
	if err := os.MkdirAll(debugDir, appstorage.PrivateDirPerm); err != nil {
		return "", err
	}
	if err := os.Chmod(debugDir, appstorage.PrivateDirPerm); err != nil {
		return "", err
	}
	ext := extensionFromMIME(detectImageMIME(data))
	if ext == "" {
		ext = ".bin"
	}
	baseName := fmt.Sprintf("invalid-%s-output-%02d-image-%02d-%d", sanitizeFilename(provider), outputIndex+1, imageIndex+1, time.Now().UnixMilli())
	fileName := uniqueAssetFilename(debugDir, baseName, ext)
	targetPath := filepath.Join(debugDir, fileName)
	if !pathWithinRoot(storagePath, targetPath) {
		return "", errors.New("debug image dump path escapes managed session storage")
	}
	if _, err := writePrivateFileAtomic(targetPath, data); err != nil {
		return "", err
	}
	return targetPath, nil
}

func supportedGeneratedImageMIME(declaredMIME, detectedMIME string) string {
	detectedMIME = strings.ToLower(strings.TrimSpace(detectedMIME))
	declaredMIME = strings.ToLower(strings.TrimSpace(declaredMIME))
	if extensionFromMIME(detectedMIME) != "" {
		return detectedMIME
	}
	if extensionFromMIME(declaredMIME) != "" && declaredMIME == detectedMIME {
		return declaredMIME
	}
	return ""
}

func finalImageMIME(finalImage codex.ImageGenerationResult) string {
	declaredMIME := strings.TrimSpace(finalImage.MIMEType)
	detectedMIME := detectImageMIME(finalImage.DecodedPNG)
	if mimeType := supportedGeneratedImageMIME(declaredMIME, detectedMIME); mimeType != "" {
		return mimeType
	}
	if declaredMIME != "" {
		return declaredMIME
	}
	return detectedMIME
}

func extensionFromMIME(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
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
