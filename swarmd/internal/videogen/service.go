package videogen

import (
	"context"
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
}

type ManagedVideoSource struct {
	Bytes         []byte
	MediaType     string
	InteractionID string
	Model         string
}

type ManagedVideoResult struct {
	Bytes         []byte
	MediaType     string
	InteractionID string
	Model         string
	Provider      string
	DurationMs    int
	Width         int
	Height        int
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

	switch providerID {
	case ProviderGoogleGemini:
		apiKey, err := s.getGoogleAPIKey(req.Principal.AccountScopeID)
		if err != nil {
			return ManagedVideoResult{}, err
		}
		if isOmniModel(modelID) {
			return s.generateGoogleOmni(ctx, apiKey, modelID, prompt, aspectRatio, resolution, req.Source)
		}
		return s.generateGoogleVeo(ctx, apiKey, modelID, prompt, aspectRatio, resolution, durationSeconds)
	case ProviderOpenRouter:
		apiKey, err := s.getOpenRouterAPIKey(req.Principal.AccountScopeID)
		if err != nil {
			return ManagedVideoResult{}, err
		}
		return s.generateOpenRouter(ctx, apiKey, modelID, prompt, aspectRatio, resolution, durationSeconds)
	default:
		return ManagedVideoResult{}, fmt.Errorf("unsupported video provider %q", providerID)
	}
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
