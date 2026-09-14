package videogen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	defaultPollInterval = 2 * time.Second
	defaultPollTimeout  = 5 * time.Minute
)

type ModelCatalog interface {
	ListCatalog(providerID string, limit int) ([]pebblestore.ModelCatalogRecord, error)
}

type ManagedVideoRequest struct {
	Prompt          string
	AspectRatio     string
	Resolution      string
	DurationSeconds int
	Principal       identity.Principal
	Source          *ManagedVideoSource
	Image           *ManagedVideoImage
}

type ManagedVideoImage struct {
	Bytes     []byte
	MediaType string
}

type ManagedVideoSource struct {
	Bytes         []byte
	MediaType     string
	InteractionID string
	Model         string
}

type ManagedVideoResult struct {
	Bytes            []byte
	MediaType        string
	InteractionID    string
	Model            string
	Provider         string
	DurationMs       int
	Width            int
	Height           int
	EstimatedCostUSD float64
	PricingSummary   string
}

type Service struct {
	authStore         *pebblestore.AuthStore
	uiSettings        *uisettings.Service
	modelCatalog      ModelCatalog
	httpClient        *http.Client
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

func (s *Service) pollingTimeout() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.pollTimeout > 0 {
		return s.pollTimeout
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

	isIteration := req.Source != nil && len(req.Source.Bytes) > 0
	modelID, providerID, err := s.resolveTargetModel(ctx, req.Principal, isIteration)
	if err != nil {
		return ManagedVideoResult{}, err
	}

	aspectRatio := normalizeAspectRatio(req.AspectRatio)
	resolution := normalizeResolution(req.Resolution)
	durationSeconds := normalizeDuration(req.DurationSeconds, modelID, resolution)

	var result ManagedVideoResult
	var genErr error
	switch providerID {
	case ProviderGoogleGemini:
		apiKey, err := s.getGoogleAPIKey(req.Principal.AccountScopeID)
		if err != nil {
			return ManagedVideoResult{}, err
		}
		if isOmniModel(modelID) {
			result, genErr = s.generateGoogleOmni(ctx, apiKey, modelID, prompt, aspectRatio, resolution, req.Source, req.Image)
		} else {
			result, genErr = s.generateGoogleVeo(ctx, apiKey, modelID, prompt, aspectRatio, resolution, durationSeconds, req.Image)
		}
	case ProviderOpenRouter:
		apiKey, err := s.getOpenRouterAPIKey(req.Principal.AccountScopeID)
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

	catalogPricing := s.resolveModelPricing(providerID, modelID)
	cost, summary := EstimateVideoCost(providerID, modelID, durationSeconds, isIteration, catalogPricing)
	result.EstimatedCostUSD = cost
	result.PricingSummary = summary
	return result, nil
}

func (s *Service) resolveTargetModel(ctx context.Context, principal identity.Principal, isIteration bool) (string, string, error) {
	accountScopeID := strings.TrimSpace(principal.AccountScopeID)
	var defaultModel, iterationModel string

	if s.uiSettings != nil && accountScopeID != "" {
		ui, err := s.uiSettings.GetForAccount(accountScopeID)
		if err == nil {
			defaultModel = strings.TrimSpace(ui.Tools.Video.DefaultModel)
			iterationModel = strings.TrimSpace(ui.Tools.Video.IterationModel)
		}
	}

	if isIteration {
		target := iterationModel
		if target == "" {
			target = DefaultVideoIterationModel
		}
		provider := s.inferProvider(target)
		return target, provider, nil
	}

	target := defaultModel
	if target == "" {
		target = DefaultVideoGenerationModel
	}
	provider := s.inferProvider(target)
	return target, provider, nil
}

func (s *Service) inferProvider(modelID string) string {
	if strings.HasPrefix(modelID, "google/") || strings.Contains(modelID, "/") {
		return ProviderOpenRouter
	}
	return ProviderGoogleGemini
}

func (s *Service) getGoogleAPIKey(accountScopeID string) (string, error) {
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
	return strings.TrimSpace(record.APIKey), nil
}

func (s *Service) getOpenRouterAPIKey(accountScopeID string) (string, error) {
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
	return strings.TrimSpace(record.APIKey), nil
}

func isOmniModel(modelID string) bool {
	lower := strings.ToLower(modelID)
	return strings.Contains(lower, "omni")
}

func normalizeAspectRatio(aspectRatio string) string {
	switch strings.TrimSpace(aspectRatio) {
	case "9:16", "portrait":
		return "9:16"
	default:
		return "16:9"
	}
}

func normalizeResolution(resolution string) string {
	switch strings.ToLower(strings.TrimSpace(resolution)) {
	case "360p":
		return "360p"
	case "1080p":
		return "1080p"
	case "4k":
		return "4k"
	default:
		return "720p"
	}
}

func normalizeDuration(durationSeconds int, modelID, resolution string) int {
	if durationSeconds == 4 || durationSeconds == 6 || durationSeconds == 8 {
		if (resolution == "1080p" || resolution == "4k") && strings.Contains(modelID, "veo") {
			return 8
		}
		return durationSeconds
	}
	return 8
}

func (s *Service) resolveModelPricing(providerID, modelID string) []byte {
	if s == nil || s.modelCatalog == nil {
		return nil
	}
	records, err := s.modelCatalog.ListCatalog(providerID, 100)
	if err != nil {
		return nil
	}
	for _, rec := range records {
		if strings.EqualFold(rec.Model, modelID) && len(rec.Pricing) > 0 {
			return rec.Pricing
		}
	}
	return nil
}

func EstimateVideoCost(providerID, modelID string, durationSeconds int, isIteration bool, catalogPricing []byte) (float64, string) {
	if len(catalogPricing) > 0 {
		var p struct {
			VideoOutput float64 `json:"video_output"`
			Prompt      float64 `json:"prompt"`
		}
		if err := json.Unmarshal(catalogPricing, &p); err == nil {
			if p.VideoOutput > 0 {
				return p.VideoOutput, fmt.Sprintf("$%.2f per generation (catalog)", p.VideoOutput)
			}
			if p.Prompt > 0 {
				return p.Prompt, fmt.Sprintf("$%.2f per generation (catalog)", p.Prompt)
			}
		}
	}

	if !isIteration && (strings.Contains(strings.ToLower(modelID), "veo") || (providerID == ProviderGoogleGemini && !isOmniModel(modelID))) {
		if durationSeconds <= 0 {
			durationSeconds = 8
		}
		costPerSecond := 0.07
		total := float64(durationSeconds) * costPerSecond
		return total, fmt.Sprintf("$%.2f/sec ($%.2f for %ds) (Google Veo)", costPerSecond, total, durationSeconds)
	}

	if isIteration || isOmniModel(modelID) {
		cost := 0.05
		return cost, "$0.05 per conversational edit (Gemini Omni Flash)"
	}

	if providerID == ProviderOpenRouter {
		cost := 0.30
		return cost, "$0.30 per generation (OpenRouter estimated)"
	}

	return 0.10, "$0.10 per generation (estimated)"
}
