package audiogen

import (
	"context"
	"crypto/sha256"
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

	caps, capsErr := s.ManagedAudioCapabilities(modelID)
	if capsErr == nil && caps.Available {
		if req.CapabilityToken != "" && caps.CapabilityToken != "" && req.CapabilityToken != caps.CapabilityToken {
			return ManagedAudioResult{}, errors.New("audio generation capability_token does not match current model capabilities; call manage_artifact action='audio_capabilities' first")
		}
		if req.DurationSeconds > 0 {
			if caps.DurationSeconds.MaxSeconds > 0 && req.DurationSeconds > caps.DurationSeconds.MaxSeconds {
				return ManagedAudioResult{}, fmt.Errorf("requested duration %ds exceeds maximum duration of %ds for audio model %q; inspect audio_capabilities or choose a full-song model in Settings -> Media", req.DurationSeconds, caps.DurationSeconds.MaxSeconds, modelID)
			}
			if caps.DurationSeconds.MinSeconds > 0 && req.DurationSeconds < caps.DurationSeconds.MinSeconds {
				return ManagedAudioResult{}, fmt.Errorf("requested duration %ds is below minimum duration of %ds for audio model %q", req.DurationSeconds, caps.DurationSeconds.MinSeconds, modelID)
			}
		}
	}

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
	if s != nil && s.uiSettings != nil && strings.TrimSpace(principal.AccountScopeID) != "" {
		if ui, err := s.uiSettings.GetForAccount(principal.AccountScopeID); err == nil {
			if configured := strings.TrimSpace(ui.Tools.Audio.DefaultModel); configured != "" {
				return RouteModel(durationSeconds, configured)
			}
		}
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

func (s *Service) googleAudioCatalogRecord(modelID string) (pebblestore.ModelCatalogRecord, bool, error) {
	if s == nil || s.modelCatalog == nil {
		return pebblestore.ModelCatalogRecord{}, false, nil
	}
	records, err := s.modelCatalog.ListCatalog("google", 2000)
	if err != nil {
		return pebblestore.ModelCatalogRecord{}, false, fmt.Errorf("list Google audio models: %w", err)
	}
	clean := strings.TrimPrefix(strings.TrimSpace(modelID), "google/")
	for _, record := range records {
		if strings.EqualFold(record.Model, modelID) || strings.EqualFold(record.CatalogID, modelID) ||
			strings.EqualFold(record.Model, clean) || strings.EqualFold(record.CatalogID, clean) {
			return record, true, nil
		}
	}
	return pebblestore.ModelCatalogRecord{}, false, nil
}

type googleMusicGenerationProfile struct {
	Status                     string `json:"status"`
	APISurface                 string `json:"api_surface"`
	Kind                       string `json:"kind"`
	DocumentedGenerationMethod string `json:"documented_generation_method"`
	Settings                   struct {
		DurationSeconds struct {
			Status          string `json:"status"`
			ClientParameter string `json:"client_parameter"`
			DefaultValue    int    `json:"default_value"`
			MinSeconds      int    `json:"min_seconds"`
			MaxSeconds      int    `json:"max_seconds"`
			SupportedValues []int  `json:"supported_values"`
			Notes           string `json:"notes"`
		} `json:"duration_seconds"`
		Format struct {
			Status       string `json:"status"`
			DefaultValue string `json:"default_value"`
			Notes        string `json:"notes"`
		} `json:"format"`
	} `json:"settings"`
	Features map[string]any `json:"features"`
	Billing  struct {
		RateUSD float64 `json:"rate_usd"`
		Unit    string  `json:"unit"`
		Notes   string  `json:"notes"`
	} `json:"billing"`
	Output struct {
		SampleRateHz       int    `json:"sample_rate_hz"`
		Channels           int    `json:"channels"`
		DefaultFormat      string `json:"default_format"`
		IncludesLyricsText bool   `json:"includes_lyrics_text"`
	} `json:"output"`
	Notes string `json:"notes"`
}

func decodeGoogleMusicGenerationProfile(raw json.RawMessage) (googleMusicGenerationProfile, string, error) {
	var providers map[string]struct {
		ModelAPISurface string                       `json:"model_api_surface"`
		MusicGeneration googleMusicGenerationProfile `json:"music_generation"`
	}
	if err := json.Unmarshal(raw, &providers); err != nil {
		return googleMusicGenerationProfile{}, "", fmt.Errorf("decode Google music-generation profile: %w", err)
	}
	for providerID, provider := range providers {
		if strings.EqualFold(strings.TrimSpace(providerID), "google") {
			return provider.MusicGeneration, strings.TrimSpace(provider.ModelAPISurface), nil
		}
	}
	return googleMusicGenerationProfile{}, "", errors.New("Google music-generation profile is missing")
}

// ManagedAudioCapabilities resolves and returns structured audio model capabilities and constraints.
func (s *Service) ManagedAudioCapabilities(selectionID string) (ManagedAudioCapabilities, error) {
	selectionID = strings.TrimSpace(selectionID)
	if selectionID == "" {
		selectionID = DefaultAudioSongModel
	}
	clean := strings.TrimPrefix(selectionID, "google/")
	isClip := strings.Contains(strings.ToLower(clean), "clip")

	record, found, err := s.googleAudioCatalogRecord(selectionID)
	if err != nil {
		return ManagedAudioCapabilities{}, err
	}

	if found && len(record.ProviderSpecific) > 0 {
		profile, _, err := decodeGoogleMusicGenerationProfile(record.ProviderSpecific)
		if err == nil && profile.Status != "" {
			minSec := profile.Settings.DurationSeconds.MinSeconds
			if minSec <= 0 {
				minSec = 1
			}
			maxSec := profile.Settings.DurationSeconds.MaxSeconds
			if maxSec <= 0 {
				if profile.Kind == "clip" || isClip {
					maxSec = ClipDurationLimitSeconds
				} else {
					maxSec = 300
				}
			}
			defaultVal := profile.Settings.DurationSeconds.DefaultValue
			if defaultVal <= 0 {
				if profile.Kind == "clip" || isClip {
					defaultVal = DefaultClipDurationSeconds
				} else {
					defaultVal = DefaultSongDurationSeconds
				}
			}
			durationCap := ManagedAudioDurationCapability{
				DefaultValue:    defaultVal,
				MinSeconds:      minSec,
				MaxSeconds:      maxSec,
				SupportedValues: append([]int(nil), profile.Settings.DurationSeconds.SupportedValues...),
				Notes:           profile.Settings.DurationSeconds.Notes,
			}
			kind := profile.Kind
			if kind == "" {
				if isClip {
					kind = "clip"
				} else {
					kind = "full_song"
				}
			}
			tokenPayload := strings.Join([]string{record.SourceSnapshotID, record.SourceSnapshotVersion, record.Model, string(record.ProviderSpecific)}, "\x00")
			token := fmt.Sprintf("%x", sha256.Sum256([]byte(tokenPayload)))

			var billingMap map[string]any
			if profile.Billing.RateUSD > 0 {
				billingMap = map[string]any{
					"rate_usd": profile.Billing.RateUSD,
					"unit":     profile.Billing.Unit,
					"notes":    profile.Billing.Notes,
				}
			}

			return ManagedAudioCapabilities{
				Available:       true,
				Model:           record.Model,
				Provider:        ProviderGoogleGemini,
				DisplayName:     firstNonEmpty(record.DisplayName, record.Model),
				Kind:            kind,
				DurationSeconds: durationCap,
				Features:        profile.Features,
				Billing:         billingMap,
				CapabilityToken: token,
			}, nil
		}
	}

	// Fallback when catalog record is absent (e.g. unit tests or unhydrated catalog)
	durationCap := ManagedAudioDurationCapability{}
	kind := "full_song"
	if isClip {
		kind = "clip"
		durationCap = ManagedAudioDurationCapability{
			DefaultValue:    DefaultClipDurationSeconds,
			MinSeconds:      1,
			MaxSeconds:      ClipDurationLimitSeconds,
			SupportedValues: []int{5, 10, 15, 20, 25, 30},
			Notes:           "Lyria 3 Clip Preview generates short musical clips up to 30 seconds.",
		}
	} else {
		durationCap = ManagedAudioDurationCapability{
			DefaultValue:    DefaultSongDurationSeconds,
			MinSeconds:      1,
			MaxSeconds:      300,
			SupportedValues: []int{30, 60, 90, 120, 180, 240, 300},
			Notes:           "Full-length songs last from 30 to 300 seconds.",
		}
	}
	tokenPayload := strings.Join([]string{"fallback", selectionID, kind}, "\x00")
	token := fmt.Sprintf("%x", sha256.Sum256([]byte(tokenPayload)))
	return ManagedAudioCapabilities{
		Available:       true,
		Model:           clean,
		Provider:        ProviderGoogleGemini,
		DisplayName:     clean,
		Kind:            kind,
		DurationSeconds: durationCap,
		CapabilityToken: token,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
