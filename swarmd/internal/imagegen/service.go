package imagegen

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/appstorage"
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
	provider := strings.TrimSpace(req.Provider)
	if provider == "" || provider == "openai" || provider == "codex" {
		provider = ProviderCodexOpenAI
	}
	if req.Count == 0 {
		req.Count = 1
	}
	if req.Count != 1 {
		return GenerateResult{}, errors.New("only single-image generation is supported")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return GenerateResult{}, errors.New("prompt is required")
	}

	switch provider {
	case ProviderCodexOpenAI:
		return s.generateCodexOpenAI(ctx, req)
	case ProviderGoogleImagen:
		return GenerateResult{}, errors.New("Google Imagen generation is not implemented yet")
	default:
		return GenerateResult{}, fmt.Errorf("unsupported image provider %q", provider)
	}
}

func (s *Service) generateCodexOpenAI(ctx context.Context, req GenerateRequest) (GenerateResult, error) {
	if s.codexClient == nil {
		return GenerateResult{}, errors.New("codex image provider is not configured")
	}
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		modelID = defaultCodexImageModel
	}
	generated, err := s.codexClient.GenerateImage(ctx, codex.ImageGenerationRequest{
		Model:         modelID,
		Prompt:        req.Prompt,
		Size:          firstNonEmpty(req.Size, settingString(req.Settings, "size")),
		PartialImages: req.PartialImages,
		OnEvent:       codexImageGenerationEventCallback(req.OnEvent),
	})
	if err != nil {
		return GenerateResult{}, err
	}
	partials := mapCodexPartialImages(generated.PartialImages)
	if len(generated.DecodedPNG) == 0 {
		thread, _, resolveErr := s.resolveWorkspaceImageStorage(req.Target.ThreadID)
		if resolveErr != nil {
			return GenerateResult{}, resolveErr
		}
		return GenerateResult{
			Assets:           []GeneratedAsset{},
			Partials:         partials,
			Target:           &WorkspaceImageSessionTargetInfo{Kind: TargetWorkspaceImage, Thread: thread},
			ProviderResponse: generated.ProviderResponse,
		}, nil
	}
	asset, thread, err := s.storeGeneratedAsset(req.Target, generated, ProviderCodexOpenAI, modelID)
	if err != nil {
		return GenerateResult{}, err
	}
	return GenerateResult{
		Assets:           []GeneratedAsset{asset},
		Partials:         partials,
		Target:           &WorkspaceImageSessionTargetInfo{Kind: TargetWorkspaceImage, Thread: thread},
		ProviderResponse: generated.ProviderResponse,
	}, nil
}

func codexImageGenerationEventCallback(onEvent func(GenerateStreamEvent)) func(codex.ImageGenerationStreamEvent) {
	if onEvent == nil {
		return nil
	}
	return func(event codex.ImageGenerationStreamEvent) {
		onEvent(GenerateStreamEvent{
			Type:              string(event.Type),
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
	}
}

func mapCodexPartialImages(partials []codex.ImageGenerationPartialImage) []PartialImage {
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
		out = append(out, PartialImage{
			ItemID:            partial.ItemID,
			OutputIndex:       partial.OutputIndex,
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

func (s *Service) storeGeneratedAsset(target GenerationTarget, generated codex.ImageGenerationResult, provider, modelID string) (GeneratedAsset, pebblestore.ImageThreadSnapshot, error) {
	if strings.TrimSpace(target.Kind) != TargetWorkspaceImage {
		return GeneratedAsset{}, pebblestore.ImageThreadSnapshot{}, errors.New("target.kind must be workspace_image_session")
	}
	thread, storagePath, err := s.resolveWorkspaceImageStorage(target.ThreadID)
	if err != nil {
		return GeneratedAsset{}, pebblestore.ImageThreadSnapshot{}, err
	}
	assetID := newAssetID()
	baseName := sanitizeFilename(firstNonEmpty(generated.CallID, assetID))
	if baseName == "" {
		baseName = assetID
	}
	fileName := uniqueAssetFilename(storagePath, baseName, generatedImageExtension)
	targetPath := filepath.Join(storagePath, fileName)
	if !pathWithinRoot(storagePath, targetPath) {
		return GeneratedAsset{}, pebblestore.ImageThreadSnapshot{}, errors.New("generated image path escapes managed session storage")
	}
	if len(generated.DecodedPNG) == 0 {
		return GeneratedAsset{}, pebblestore.ImageThreadSnapshot{}, errors.New("generated image payload is empty")
	}
	if !looksLikePNG(generated.DecodedPNG) {
		return GeneratedAsset{}, pebblestore.ImageThreadSnapshot{}, errors.New("generated image payload is not a PNG image")
	}
	if err := appstorage.WritePrivateFile(targetPath, generated.DecodedPNG); err != nil {
		return GeneratedAsset{}, pebblestore.ImageThreadSnapshot{}, fmt.Errorf("write generated image: %w", err)
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		return GeneratedAsset{}, pebblestore.ImageThreadSnapshot{}, fmt.Errorf("stat generated image: %w", err)
	}
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
		RevisedPrompt: generated.RevisedPrompt,
		Provider:      provider,
		Model:         modelID,
	}
	thread.ImageAssets = append(thread.ImageAssets, asset.ImageAssetSnapshot)
	thread.ImageAssetOrder = appendUniqueString(thread.ImageAssetOrder, asset.ID)
	updated, err := s.imageThreads.Update(thread)
	if err != nil {
		return GeneratedAsset{}, pebblestore.ImageThreadSnapshot{}, fmt.Errorf("update image thread asset metadata: %w", err)
	}
	return asset, updated, nil
}

func (s *Service) ResolveAssetPath(threadID, assetID string) (string, pebblestore.ImageAssetSnapshot, error) {
	thread, storagePath, err := s.resolveWorkspaceImageStorage(threadID)
	if err != nil {
		return "", pebblestore.ImageAssetSnapshot{}, err
	}
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return "", pebblestore.ImageAssetSnapshot{}, errors.New("asset id is required")
	}
	for _, asset := range thread.ImageAssets {
		if asset.ID != assetID {
			continue
		}
		assetPath := filepath.Clean(strings.TrimSpace(asset.Path))
		if assetPath == "." || assetPath == "" || !pathWithinRoot(storagePath, assetPath) {
			return "", pebblestore.ImageAssetSnapshot{}, errors.New("image asset path is outside managed session storage")
		}
		return assetPath, asset, nil
	}
	return "", pebblestore.ImageAssetSnapshot{}, errors.New("image asset not found")
}

func (s *Service) resolveWorkspaceImageStorage(threadID string) (pebblestore.ImageThreadSnapshot, string, error) {
	if s.imageThreads == nil {
		return pebblestore.ImageThreadSnapshot{}, "", errors.New("image thread store is not configured")
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return pebblestore.ImageThreadSnapshot{}, "", errors.New("thread_id is required")
	}
	thread, ok, err := s.imageThreads.Get(threadID)
	if err != nil {
		return pebblestore.ImageThreadSnapshot{}, "", err
	}
	if !ok {
		return pebblestore.ImageThreadSnapshot{}, "", errors.New("image thread not found")
	}
	if thread.Metadata == nil {
		return pebblestore.ImageThreadSnapshot{}, "", errors.New("image thread storage metadata is missing")
	}
	storagePath, ok := thread.Metadata[assetPathMetadataKey].(string)
	storagePath = filepath.Clean(strings.TrimSpace(storagePath))
	if !ok || storagePath == "" || storagePath == "." {
		return pebblestore.ImageThreadSnapshot{}, "", errors.New("image thread managed storage path is missing")
	}
	if isLegacyWorkspaceStoragePath(thread.WorkspacePath, storagePath) {
		return pebblestore.ImageThreadSnapshot{}, "", errors.New("legacy workspace .swarm tool storage is not allowed")
	}
	expected, err := appstorage.WorkspaceDataDir(thread.WorkspacePath, "tools", "image", "sessions", thread.ID)
	if err != nil {
		return pebblestore.ImageThreadSnapshot{}, "", fmt.Errorf("resolve expected workspace image storage: %w", err)
	}
	expected = filepath.Clean(expected)
	if storagePath != expected {
		return pebblestore.ImageThreadSnapshot{}, "", errors.New("image thread managed storage path does not match app-managed workspace session folder")
	}
	if !pathWithinRoot(expected, storagePath) {
		return pebblestore.ImageThreadSnapshot{}, "", errors.New("image thread managed storage path is invalid")
	}
	return thread, storagePath, nil
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

func isLegacyWorkspaceStoragePath(workspacePath, candidate string) bool {
	workspacePath = strings.TrimSpace(workspacePath)
	candidate = strings.TrimSpace(candidate)
	if workspacePath == "" || candidate == "" {
		return false
	}
	workspacePath, err := filepath.Abs(workspacePath)
	if err != nil {
		workspacePath = filepath.Clean(workspacePath)
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(workspacePath, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		candidate = filepath.Clean(candidate)
	}
	return pathWithinRoot(filepath.Join(workspacePath, ".swarm", "tools"), candidate)
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
