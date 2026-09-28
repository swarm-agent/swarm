package videogen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
)

const (
	ProviderGoogleGemini = "google"
	ProviderOpenRouter   = "openrouter"

	DefaultVideoGenerationModel = "veo-3.1-generate-preview"
	DefaultVideoIterationModel  = "gemini-omni-1.1-flash"

	managedVideoMaxBytes            = 100 << 20
	managedVideoMaxParallelRequests = 4

	defaultGoogleBaseURL     = "https://generativelanguage.googleapis.com"
	defaultOpenRouterBaseURL = "https://openrouter.ai"

	defaultPollInterval  = 2 * time.Second
	defaultPollTimeout   = 8 * time.Minute
	default4kPollTimeout = 15 * time.Minute
)

type ModelCatalog interface {
	ListCatalog(providerID string, limit int) ([]pebblestore.ModelCatalogRecord, error)
}

type ManagedVideoRequest struct {
	Operation        string // "create" | "edit" | "extend"
	Model            string
	Prompt           string
	AspectRatio      string
	Resolution       string
	DurationSeconds  int
	Principal        identity.Principal
	Source           *ManagedVideoSource
	SourceProvenance *pebblestore.VideoProvenance
	Image            *ManagedVideoImage
	Prober           VideoProber
}

type ManagedVideoImage struct {
	Bytes     []byte
	MediaType string
}

// SVGRasterizer converts vector SVG images into raster PNG images.
type SVGRasterizer interface {
	RasterizeSVG(ctx context.Context, svgBytes []byte) ([]byte, error)
}

type ManagedVideoSource struct {
	Bytes         []byte
	MediaType     string
	InteractionID string
	Model         string
	URI           string // e.g. Veo video URI
	Provenance    *pebblestore.VideoProvenance
	SourceLink    *pebblestore.VideoSourceLink
}

type ManagedVideoResult struct {
	Bytes            []byte
	MediaType        string
	InteractionID    string
	ProviderResource string // e.g. Veo video URI
	Model            string
	Provider         string
	Transport        string
	Operation        string
	DurationMs       int
	Width            int
	Height           int
	Resolution       string
	DurationSeconds  int
	AspectRatio      string
	PriceStatus      string
	PricingSummary   string
	SnapshotID       string
	SnapshotVersion  string
	EstimatedCostUSD float64
	Provenance       *pebblestore.VideoProvenance
	IsCombinedOutput bool
	ExtensionCount   int
	AdvisoryWarnings []string
}

type Service struct {
	authStore         *pebblestore.AuthStore
	uiSettings        *uisettings.Service
	modelCatalog      ModelCatalog
	httpClient        *http.Client
	svgRasterizer     SVGRasterizer
	videoProber       VideoProber
	googleBaseURL     string
	openRouterBaseURL string
	pollInterval      time.Duration
	pollTimeout       time.Duration
	managedSlots      chan struct{}
	mu                sync.RWMutex
}

func NewService(authStore *pebblestore.AuthStore, uiSettings *uisettings.Service, modelCatalog ModelCatalog) *Service {
	return &Service{
		authStore:         authStore,
		uiSettings:        uiSettings,
		modelCatalog:      modelCatalog,
		httpClient:        &http.Client{Timeout: 90 * time.Second},
		videoProber:       FFprobeVideoProber{},
		googleBaseURL:     defaultGoogleBaseURL,
		openRouterBaseURL: defaultOpenRouterBaseURL,
		pollInterval:      defaultPollInterval,
		pollTimeout:       defaultPollTimeout,
		managedSlots:      make(chan struct{}, managedVideoMaxParallelRequests),
	}
}

func (s *Service) SetHTTPClient(client *http.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.httpClient = client
}

func (s *Service) SetSVGRasterizer(rasterizer SVGRasterizer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.svgRasterizer = rasterizer
}

func (s *Service) SVGRasterizer() SVGRasterizer {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.svgRasterizer
}

func (s *Service) SetVideoProber(prober VideoProber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.videoProber = prober
}

func (s *Service) VideoProber() VideoProber {
	if s == nil {
		return FFprobeVideoProber{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.videoProber != nil {
		return s.videoProber
	}
	return FFprobeVideoProber{}
}

func (s *Service) probeVideoBytes(ctx context.Context, videoBytes []byte) (VideoMetadata, error) {
	if len(videoBytes) == 0 {
		return VideoMetadata{}, errors.New("cannot probe empty video bytes")
	}
	prober := s.VideoProber()
	return prober.ProbeVideo(ctx, videoBytes)
}

func (s *Service) SetBaseURLs(googleURL, openRouterURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if googleURL != "" {
		s.googleBaseURL = strings.TrimRight(googleURL, "/")
	}
	if openRouterURL != "" {
		s.openRouterBaseURL = strings.TrimRight(openRouterURL, "/")
	}
}

func (s *Service) SetPollTiming(interval, timeout time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if interval > 0 {
		s.pollInterval = interval
	}
	if timeout > 0 {
		s.pollTimeout = timeout
	}
}

func (s *Service) client() *http.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.httpClient != nil {
		return s.httpClient
	}
	return http.DefaultClient
}

func (s *Service) googleURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.googleBaseURL != "" {
		return s.googleBaseURL
	}
	return defaultGoogleBaseURL
}

func (s *Service) openRouterURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.openRouterBaseURL != "" {
		return s.openRouterBaseURL
	}
	return defaultOpenRouterBaseURL
}

func (s *Service) pollingInterval() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.pollInterval > 0 {
		return s.pollInterval
	}
	return defaultPollInterval
}

func (s *Service) pollingTimeout(resolution string) time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.pollTimeout > 0 {
		return s.pollTimeout
	}
	if strings.EqualFold(strings.TrimSpace(resolution), "4k") {
		return default4kPollTimeout
	}
	return defaultPollTimeout
}

func (s *Service) GenerateManagedVideo(ctx context.Context, req ManagedVideoRequest) (ManagedVideoResult, error) {
	if s == nil {
		return ManagedVideoResult{}, errors.New("videogen service is not configured")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return ManagedVideoResult{}, errors.New("video generation requires prompt")
	}

	select {
	case s.managedSlots <- struct{}{}:
		defer func() { <-s.managedSlots }()
	case <-ctx.Done():
		return ManagedVideoResult{}, ctx.Err()
	}

	if req.Image != nil && len(req.Image.Bytes) > 0 {
		if err := s.ensureRasterImage(ctx, req.Image); err != nil {
			return ManagedVideoResult{}, err
		}
	}

	// Probe source video metadata if bytes are provided
	var srcDurationSec float64
	var srcWidth, srcHeight int
	if req.Source != nil && len(req.Source.Bytes) > 0 {
		srcMeta, err := s.probeVideoBytes(ctx, req.Source.Bytes)
		if err != nil {
			return ManagedVideoResult{}, fmt.Errorf("probe source video: %w", err)
		}
		if srcMeta.DurationSeconds <= 0 || math.IsNaN(srcMeta.DurationSeconds) || math.IsInf(srcMeta.DurationSeconds, 0) {
			return ManagedVideoResult{}, errors.New("source video duration must be positive and finite")
		}
		if srcMeta.Width <= 0 || srcMeta.Height <= 0 {
			return ManagedVideoResult{}, errors.New("source video dimensions must be positive")
		}
		srcDurationSec = srcMeta.DurationSeconds
		srcWidth = srcMeta.Width
		srcHeight = srcMeta.Height
	}
	srcProv := req.SourceProvenance
	if srcProv == nil && req.Source != nil {
		srcProv = req.Source.Provenance
	}
	if srcDurationSec <= 0 && srcProv != nil {
		if srcProv.ObservedDurationMs > 0 {
			srcDurationSec = float64(srcProv.ObservedDurationMs) / 1000.0
		}
		if srcWidth <= 0 {
			srcWidth = srcProv.ObservedWidth
		}
		if srcHeight <= 0 {
			srcHeight = srcProv.ObservedHeight
		}
	}
	if req.Source != nil && req.Source.Provenance == nil && srcProv != nil {
		req.Source.Provenance = srcProv
	}

	// Validate, resolve, and preflight operation using canonical evaluator
	preflightReq := VideoPreflightRequest{
		AccountScopeID:        req.Principal.AccountScopeID,
		Operation:             req.Operation,
		ExplicitModel:         req.Model,
		AspectRatio:           req.AspectRatio,
		Resolution:            req.Resolution,
		DurationSeconds:       req.DurationSeconds,
		Prompt:                req.Prompt,
		Principal:             req.Principal,
		Source:                req.Source,
		SourceProvenance:      req.SourceProvenance,
		Image:                 req.Image,
		SourceDurationSeconds: srcDurationSec,
		SourceWidth:           srcWidth,
		SourceHeight:          srcHeight,
	}

	preflight, err := s.PreflightVideoOperation(ctx, preflightReq)
	if err != nil {
		return ManagedVideoResult{}, err
	}

	modelID := preflight.ResolvedModel
	providerID := preflight.ResolvedProvider
	aspectRatio := preflight.AspectRatio
	resolution := preflight.Resolution
	durationSeconds := preflight.DurationSeconds
	operation := preflight.Operation

	var result ManagedVideoResult
	var genErr error

	switch providerID {
	case ProviderGoogleGemini:
		apiKey, err := s.getGoogleAPIKey(req.Principal.AccountScopeID, preflight.CredentialID, preflight.CredentialVersion)
		if err != nil {
			return ManagedVideoResult{}, err
		}
		if IsOmniModel(modelID) {
			result, genErr = s.generateGoogleOmni(ctx, apiKey, modelID, prompt, aspectRatio, resolution, operation, req.Source, req.Image)
		} else {
			result, genErr = s.generateGoogleVeo(ctx, apiKey, modelID, prompt, aspectRatio, resolution, durationSeconds, operation, req.Source, req.Image)
		}
	case ProviderOpenRouter:
		apiKey, err := s.getOpenRouterAPIKey(req.Principal.AccountScopeID, preflight.CredentialID, preflight.CredentialVersion)
		if err != nil {
			return ManagedVideoResult{}, err
		}
		result, genErr = s.generateOpenRouter(ctx, apiKey, modelID, prompt, aspectRatio, resolution, durationSeconds, req.Image)
	default:
		return ManagedVideoResult{}, fmt.Errorf("unsupported video provider %q", providerID)
	}

	if genErr != nil {
		return ManagedVideoResult{}, genErr
	}

	if len(result.Bytes) == 0 {
		return ManagedVideoResult{}, errors.New("provider generated empty video output")
	}

	// Probe output video bytes to measure actual duration and dimensions
	outMeta, probeErr := s.probeVideoBytes(ctx, result.Bytes)
	if probeErr != nil {
		return ManagedVideoResult{}, fmt.Errorf("probe generated video output: %w", probeErr)
	}
	if outMeta.DurationSeconds <= 0 || math.IsNaN(outMeta.DurationSeconds) || math.IsInf(outMeta.DurationSeconds, 0) {
		return ManagedVideoResult{}, errors.New("probed video output has invalid duration")
	}
	if outMeta.Width <= 0 || outMeta.Height <= 0 {
		return ManagedVideoResult{}, errors.New("probed video output has invalid dimensions")
	}
	result.Width = outMeta.Width
	result.Height = outMeta.Height
	result.DurationMs = int(outMeta.DurationSeconds * 1000)
	result.DurationSeconds = int(math.Round(outMeta.DurationSeconds))

	// Enforce output bounds and deltas for extensions
	if operation == pebblestore.VideoOperationExtend {
		if srcDurationSec <= 0 {
			return ManagedVideoResult{}, errors.New("source video duration is required to verify extension output delta")
		}
		if IsVeoModel(modelID) {
			if outMeta.DurationSeconds > 148.0 {
				return ManagedVideoResult{}, fmt.Errorf("extended Veo video duration (%.1fs) exceeds maximum allowed ceiling (148s)", outMeta.DurationSeconds)
			}
			delta := outMeta.DurationSeconds - srcDurationSec
			if delta < 5.0 || delta > 10.0 {
				return ManagedVideoResult{}, fmt.Errorf("extended Veo video output duration unexpected (source %.1fs, output %.1fs, delta %.1fs; expected ~7s)", srcDurationSec, outMeta.DurationSeconds, delta)
			}
		}
		if IsOmniModel(modelID) {
			if outMeta.DurationSeconds > 40.0 {
				return ManagedVideoResult{}, fmt.Errorf("extended Omni video duration (%.1fs) exceeds maximum allowed ceiling (40s)", outMeta.DurationSeconds)
			}
			delta := outMeta.DurationSeconds - srcDurationSec
			if delta < 3.0 || delta > 10.0 {
				return ManagedVideoResult{}, fmt.Errorf("extended Omni video output duration delta (%.1fs) outside allowed range 3-10s", delta)
			}
		}
	}

	result.Resolution = resolution
	result.AspectRatio = aspectRatio
	if result.DurationSeconds == 0 {
		result.DurationSeconds = durationSeconds
	}
	result.EstimatedCostUSD = preflight.EstimatedCostUSD
	result.PriceStatus = preflight.PriceStatus
	result.PricingSummary = preflight.PricingSummary
	result.SnapshotID = preflight.SnapshotID
	result.SnapshotVersion = preflight.SnapshotVersion
	result.AdvisoryWarnings = preflight.AdvisoryWarnings
	result.Operation = operation
	result.Transport = preflight.ResolvedTransport

	// Build typed server-authored VideoProvenance
	var srcLink *pebblestore.VideoSourceLink
	if req.Source != nil && req.Source.SourceLink != nil {
		srcLink = req.Source.SourceLink.Clone()
	} else if preflight.SourceLink != nil {
		srcLink = preflight.SourceLink.Clone()
	}

	h := sha256.Sum256(result.Bytes)
	outputDigest := hex.EncodeToString(h[:])

	now := time.Now().UnixMilli()
	var expiresAt int64
	if IsVeoModel(modelID) {
		// Veo references known validity: 2 days (48 hours)
		expiresAt = now + 48*3600*1000
	}

	extCount := 0
	extKnown := false
	isCombined := false
	switch operation {
	case pebblestore.VideoOperationCreate:
		extCount = 0
		extKnown = true
		isCombined = false
	case pebblestore.VideoOperationEdit:
		extCount = 0
		extKnown = true
		isCombined = false
	case pebblestore.VideoOperationExtend:
		extCount = result.ExtensionCount
		extKnown = true
		isCombined = true
	}

	prov := &pebblestore.VideoProvenance{
		AccountScopeID:      req.Principal.AccountScopeID,
		CredentialID:        preflight.CredentialID,
		CredentialVersion:   preflight.CredentialVersion,
		Provider:            providerID,
		Model:               modelID,
		Transport:           preflight.ResolvedTransport,
		Operation:           operation,
		SourceLink:          srcLink,
		OutputDigestSHA256:  outputDigest,
		InteractionID:       result.InteractionID,
		ProviderResource:    result.ProviderResource,
		CreatedAt:           now,
		ExpiresAt:           expiresAt,
		ObservedDurationMs:  int64(result.DurationMs),
		ObservedWidth:       result.Width,
		ObservedHeight:      result.Height,
		ExtensionCount:      extCount,
		ExtensionCountKnown: extKnown,
		IsCombinedOutput:    isCombined,
		AspectRatio:         preflight.AspectRatio,
		Resolution:          preflight.Resolution,
		DurationSeconds:     preflight.DurationSeconds,
	}
	result.Provenance = prov
	result.ExtensionCount = extCount
	result.IsCombinedOutput = isCombined
	return result, nil
}

func (s *Service) ensureRasterImage(ctx context.Context, img *ManagedVideoImage) error {
	if img == nil || len(img.Bytes) == 0 {
		return nil
	}
	mimeType := strings.ToLower(strings.TrimSpace(img.MediaType))
	if mimeType == "" {
		mimeType = http.DetectContentType(img.Bytes)
	}
	isSVG := mimeType == "image/svg+xml" || (len(img.Bytes) > 4 && strings.Contains(string(img.Bytes[:min(len(img.Bytes), 256)]), "<svg"))
	if !isSVG {
		return nil
	}
	rasterizer := s.SVGRasterizer()
	if rasterizer == nil {
		return errors.New("vector SVG images must be rasterized to PNG or JPEG before passing to video generation")
	}
	pngBytes, err := rasterizer.RasterizeSVG(ctx, img.Bytes)
	if err != nil {
		return fmt.Errorf("rasterize SVG image to PNG: %w", err)
	}
	if len(pngBytes) == 0 {
		return errors.New("rasterized SVG image is empty")
	}
	img.Bytes = pngBytes
	img.MediaType = "image/png"
	return nil
}

func (s *Service) inferProvider(modelID string) string {
	if strings.HasPrefix(modelID, "google/") || strings.Contains(modelID, "/") {
		return ProviderOpenRouter
	}
	return ProviderGoogleGemini
}

func (s *Service) getGoogleAPIKey(accountScopeID, expectedCredID, expectedCredVersion string) (string, error) {
	if s.authStore == nil {
		return "", errors.New("auth store is not configured")
	}
	record, ok, err := s.authStore.GetActiveCredentialForAccount(accountScopeID, "google")
	if err != nil {
		return "", fmt.Errorf("read google credentials: %w", err)
	}
	if !ok || strings.TrimSpace(record.APIKey) == "" {
		return "", errors.New("google api key is not configured; add a Google API key in Settings -> Providers")
	}
	if expectedCredID != "" && record.ID != expectedCredID {
		return "", fmt.Errorf("active credential changed since preflight (expected %q, got %q)", expectedCredID, record.ID)
	}
	if expectedCredVersion != "" {
		actualVersion := ""
		if record.UpdatedAt > 0 {
			actualVersion = fmt.Sprintf("v%d", record.UpdatedAt)
		}
		if actualVersion != expectedCredVersion {
			return "", fmt.Errorf("credential version changed since preflight (expected %q, got %q)", expectedCredVersion, actualVersion)
		}
	}
	return strings.TrimSpace(record.APIKey), nil
}

func (s *Service) getOpenRouterAPIKey(accountScopeID, expectedCredID, expectedCredVersion string) (string, error) {
	if s.authStore == nil {
		return "", errors.New("auth store is not configured")
	}
	record, ok, err := s.authStore.GetActiveCredentialForAccount(accountScopeID, "openrouter")
	if err != nil {
		return "", fmt.Errorf("read openrouter credentials: %w", err)
	}
	if !ok || strings.TrimSpace(record.APIKey) == "" {
		return "", errors.New("openrouter api key is not configured; add an OpenRouter API key in Settings -> Providers")
	}
	if expectedCredID != "" && record.ID != expectedCredID {
		return "", fmt.Errorf("active credential changed since preflight (expected %q, got %q)", expectedCredID, record.ID)
	}
	if expectedCredVersion != "" {
		actualVersion := ""
		if record.UpdatedAt > 0 {
			actualVersion = fmt.Sprintf("v%d", record.UpdatedAt)
		}
		if actualVersion != expectedCredVersion {
			return "", fmt.Errorf("credential version changed since preflight (expected %q, got %q)", expectedCredVersion, actualVersion)
		}
	}
	return strings.TrimSpace(record.APIKey), nil
}

func (s *Service) resolveModelRecord(providerID, modelID string) (pebblestore.ModelCatalogRecord, bool) {
	if s == nil || s.modelCatalog == nil {
		return pebblestore.ModelCatalogRecord{}, false
	}
	records, err := s.modelCatalog.ListCatalog(providerID, 100)
	if err != nil {
		return pebblestore.ModelCatalogRecord{}, false
	}
	for _, rec := range records {
		if strings.EqualFold(rec.Model, modelID) {
			return rec, true
		}
	}
	cleanModel := strings.TrimPrefix(strings.TrimPrefix(modelID, providerID+"/"), "google/")
	for _, rec := range records {
		cleanRec := strings.TrimPrefix(strings.TrimPrefix(rec.Model, providerID+"/"), "google/")
		if strings.EqualFold(cleanRec, cleanModel) {
			return rec, true
		}
	}
	return pebblestore.ModelCatalogRecord{}, false
}

func (s *Service) resolveModelPricing(providerID, modelID string) []byte {
	if rec, found := s.resolveModelRecord(providerID, modelID); found && len(rec.Pricing) > 0 {
		return rec.Pricing
	}
	return nil
}

func EstimateVideoCost(providerID, modelID string, durationSeconds int, isIteration bool, catalogPricing []byte) (float64, string) {
	return EstimateVideoCostWithResolution(providerID, modelID, durationSeconds, "", isIteration, catalogPricing)
}

func EstimateVideoCostWithResolution(providerID, modelID string, durationSeconds int, resolution string, isIteration bool, catalogPricing []byte) (float64, string) {
	rec := pebblestore.ModelCatalogRecord{
		Provider: providerID,
		Model:    modelID,
		Pricing:  catalogPricing,
	}
	estimate := pebblestore.EstimateMediaCostFromRecord(rec, pebblestore.MediaCostEstimateOptions{
		Provider:        providerID,
		Model:           modelID,
		Kind:            "video",
		Count:           1,
		DurationSeconds: durationSeconds,
		Resolution:      resolution,
		AspectRatio:     aspectRatioOrDefault(resolution),
		IncludesAudio:   true,
		IsIteration:     isIteration,
		ServiceTier:     "standard",
	})
	return estimate.CostUSD, estimate.PricingSummary
}

func aspectRatioOrDefault(res string) string {
	return "16:9"
}
