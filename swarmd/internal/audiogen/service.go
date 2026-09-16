package audiogen

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

// Service provides managed audio and music generation and iteration via Google Lyria.
type Service struct {
	authStore     *pebblestore.AuthStore
	uiSettings    *uisettings.Service
	modelCatalog  ModelCatalog
	httpClient    *http.Client
	commandRunner CommandRunner
	googleBaseURL string
	managedSlots  chan struct{}
	mu            sync.RWMutex
}

// NewService constructs a new audiogen Service.
func NewService(authStore *pebblestore.AuthStore, uiSettings *uisettings.Service, modelCatalog ModelCatalog) *Service {
	return &Service{
		authStore:     authStore,
		uiSettings:    uiSettings,
		modelCatalog:  modelCatalog,
		httpClient:    &http.Client{Timeout: 90 * time.Second},
		commandRunner: &osCommandRunner{},
		googleBaseURL: defaultGoogleBaseURL,
		managedSlots:  make(chan struct{}, managedAudioMaxParallelRequests),
	}
}

// SetHTTPClient configures a custom HTTP client for testing or custom transport.
func (s *Service) SetHTTPClient(client *http.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.httpClient = client
}

// SetCommandRunner configures a custom CommandRunner for testing external commands like ffmpeg.
func (s *Service) SetCommandRunner(runner CommandRunner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commandRunner = runner
}

// CommandRunner returns the active CommandRunner or nil.
func (s *Service) CommandRunner() CommandRunner {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.commandRunner
}

// SetBaseURL overrides the default Google API base URL (useful in tests).
func (s *Service) SetBaseURL(googleURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if googleURL != "" {
		s.googleBaseURL = strings.TrimRight(googleURL, "/")
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

// GenerateManagedAudio executes audio generation or iteration against Google Lyria.
func (s *Service) GenerateManagedAudio(ctx context.Context, req ManagedAudioRequest) (ManagedAudioResult, error) {
	if s == nil {
		return ManagedAudioResult{}, errors.New("audiogen service is not configured")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return ManagedAudioResult{}, errors.New("audio generation requires prompt")
	}

	select {
	case s.managedSlots <- struct{}{}:
		defer func() { <-s.managedSlots }()
	case <-ctx.Done():
		return ManagedAudioResult{}, ctx.Err()
	}

	isIteration := req.Source != nil && (len(req.Source.Bytes) > 0 || strings.TrimSpace(req.Source.InteractionID) != "")
	modelID, providerID := s.resolveTargetModel(ctx, req.Principal, req.DurationSeconds, req.Model, isIteration)

	durationSeconds := NormalizeDuration(req.DurationSeconds, modelID)
	shapedPrompt := ShapePromptWithDuration(prompt, durationSeconds)

	var result ManagedAudioResult
	var genErr error

	switch providerID {
	case ProviderGoogleGemini:
		apiKey, err := s.getGoogleAPIKey(req.Principal.AccountScopeID)
		if err != nil {
			return ManagedAudioResult{}, err
		}
		result, genErr = s.generateGoogleLyria(ctx, apiKey, modelID, shapedPrompt, durationSeconds, req)
	default:
		return ManagedAudioResult{}, fmt.Errorf("unsupported audio provider %q", providerID)
	}

	if genErr != nil {
		return ManagedAudioResult{}, genErr
	}

	catalogPricing := s.resolveModelPricing(providerID, modelID)
	cost, summary := EstimateAudioCost(providerID, modelID, isIteration, catalogPricing)
	result.EstimatedCostUSD = cost
	result.PricingSummary = summary

	return result, nil
}

func (s *Service) resolveTargetModel(
	ctx context.Context,
	principal identity.Principal,
	durationSeconds int,
	requestedModel string,
	isIteration bool,
) (string, string) {
	if strings.TrimSpace(requestedModel) != "" {
		return RouteModel(durationSeconds, requestedModel)
	}
	return RouteModel(durationSeconds, "")
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
