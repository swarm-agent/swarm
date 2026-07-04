package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	defaultCatalogURL          = "https://models.swarmagent.dev/v1/snapshot.json"
	defaultCatalogVersionURL   = "https://models.swarmagent.dev/v1/snapshot-version.json"
	defaultCatalogTTL          = time.Hour
	defaultCatalogFetchTimeout = 10 * time.Second

	catalogSourcePinned = "swarm_snapshot:pinned"
	catalogSourceLive   = "swarm_snapshot:live"

	pinnedCatalogSourceURL  = "embedded:swarm-models/v1/snapshot.json"
	pinnedCatalogVersionURL = "embedded:swarm-models/v1/snapshot-version.json"
)

var catalogProviderCanonicalIDs = map[string]string{
	"github-copilot": "copilot",
	"fireworks-ai":   "fireworks",
}

type CatalogService struct {
	store      *pebblestore.ModelCatalogStore
	client     *http.Client
	sourceURL  string
	versionURL string
	now        func() time.Time
	mu         sync.Mutex
}

type CatalogLookup struct {
	Record pebblestore.ModelCatalogRecord `json:"record"`
	Found  bool                           `json:"found"`
	Stale  bool                           `json:"stale"`
}

type CatalogRefreshResult struct {
	Source             string `json:"source,omitempty"`
	SourceURL          string `json:"source_url"`
	VersionURL         string `json:"version_url,omitempty"`
	SnapshotID         string `json:"snapshot_id,omitempty"`
	SnapshotVersion    string `json:"snapshot_version,omitempty"`
	GeneratedAt        string `json:"generated_at,omitempty"`
	ETag               string `json:"etag,omitempty"`
	VersionETag        string `json:"version_etag,omitempty"`
	FetchedAt          int64  `json:"fetched_at"`
	LastCheckedAt      int64  `json:"last_checked_at,omitempty"`
	ExpiresAt          int64  `json:"expires_at"`
	RecordCount        int    `json:"record_count"`
	ModelCount         int    `json:"model_count,omitempty"`
	NotModified        bool   `json:"not_modified"`
	UsedCache          bool   `json:"used_cache"`
	UsedPinned         bool   `json:"used_pinned"`
	UsingCacheFallback bool   `json:"using_cache_fallback,omitempty"`
	Manual             bool   `json:"manual,omitempty"`
	LastRefreshReason  string `json:"last_refresh_reason,omitempty"`
}

type swarmSnapshotVersion struct {
	SchemaVersion         string `json:"schema_version"`
	APIVersion            string `json:"api_version"`
	SnapshotSchemaVersion string `json:"snapshot_schema_version"`
	SnapshotID            string `json:"snapshot_id"`
	SnapshotVersion       string `json:"snapshot_version"`
	GeneratedAt           string `json:"generated_at"`
	ModelCount            int    `json:"model_count"`
	ProviderCount         int    `json:"provider_count"`
	HydratedProviderCount int    `json:"hydrated_provider_count"`
	SnapshotURL           string `json:"snapshot_url"`
}

type swarmSnapshot struct {
	SchemaVersion         string               `json:"schema_version"`
	APIVersion            string               `json:"api_version"`
	SnapshotSchemaVersion string               `json:"snapshot_schema_version"`
	SnapshotID            string               `json:"snapshot_id"`
	SnapshotVersion       string               `json:"snapshot_version"`
	GeneratedAt           string               `json:"generated_at"`
	ModelCount            int                  `json:"model_count"`
	ProviderCount         int                  `json:"provider_count"`
	HydratedProviderCount int                  `json:"hydrated_provider_count"`
	Models                []swarmSnapshotModel `json:"models"`
}

type swarmSnapshotModel struct {
	CatalogID           string `json:"catalog_id"`
	ProviderID          string `json:"provider_id"`
	ProviderDisplayName string `json:"provider_display_name"`
	ModelID             string `json:"model_id"`
	DisplayName         string `json:"display_name"`
	Capabilities        struct {
		SupportsReasoning *bool `json:"supports_reasoning"`
	} `json:"capabilities"`
	Limits struct {
		ContextWindowTokens *int `json:"context_window_tokens"`
		MaxOutputTokens     *int `json:"max_output_tokens"`
	} `json:"limits"`
	Pricing          json.RawMessage `json:"pricing"`
	Thinking         json.RawMessage `json:"thinking"`
	ProviderSpecific json.RawMessage `json:"provider_specific"`
	Routing          struct {
		TopProviderContextWindowTokens *int `json:"top_provider_context_window_tokens"`
		TopProviderMaxOutputTokens     *int `json:"top_provider_max_output_tokens"`
	} `json:"routing"`
}

type swarmSnapshotThinking struct {
	Supported              *bool                          `json:"supported"`
	SupportedSwarmSettings []string                       `json:"supported_swarm_settings"`
	DefaultSwarmSetting    string                         `json:"default_swarm_setting"`
	ProviderParameter      string                         `json:"provider_parameter"`
	SwarmSettingMappings   []swarmSnapshotThinkingMapping `json:"swarm_setting_mappings"`
}

type swarmSnapshotThinkingMapping struct {
	SwarmSetting           string `json:"swarm_setting"`
	ProviderValue          any    `json:"provider_value"`
	EffectiveProviderValue any    `json:"effective_provider_value"`
	Behavior               string `json:"behavior"`
}

type swarmSnapshotContextMode struct {
	Mode          string `json:"mode"`
	Label         string `json:"label"`
	ContextWindow int    `json:"context_window"`
	Default       bool   `json:"default"`
}

type swarmSnapshotProviderSpecific struct {
	ResourceName     string                     `json:"resource_name"`
	RequestModelPath string                     `json:"request_model_path"`
	ContextWindow    int                        `json:"context_window"`
	MaxContextWindow int                        `json:"max_context_window"`
	ContextModes     []swarmSnapshotContextMode `json:"context_modes"`
	Serving          struct {
		SupportedTiers []string                        `json:"supported_tiers"`
		DefaultTier    string                          `json:"default_tier"`
		Standard       *swarmSnapshotRawTier           `json:"standard"`
		Priority       *swarmSnapshotRawTier           `json:"priority"`
		Fast           *swarmSnapshotRawTier           `json:"fast"`
		Tiers          map[string]swarmSnapshotRawTier `json:"tiers"`
	} `json:"serving"`
}

type swarmSnapshotRawTier struct {
	Tier              string          `json:"tier"`
	SwarmSetting      string          `json:"swarm_setting"`
	ProviderParameter string          `json:"provider_parameter"`
	ProviderValue     string          `json:"provider_value"`
	RequestModelPath  string          `json:"request_model_path"`
	Pricing           json.RawMessage `json:"pricing,omitempty"`
}

type catalogVersionFetch struct {
	Version     swarmSnapshotVersion
	ETag        string
	NotModified bool
}

func NewCatalogService(store *pebblestore.ModelCatalogStore) *CatalogService {
	return &CatalogService{
		store:      store,
		sourceURL:  defaultCatalogURL,
		versionURL: defaultCatalogVersionURL,
		now:        time.Now,
		client: &http.Client{
			Timeout: defaultCatalogFetchTimeout,
		},
	}
}

func (s *CatalogService) EnsureBootDefaults() error {
	if err := s.seedPinnedSnapshotIfNeeded(); err != nil {
		return err
	}

	providerID := strings.ToLower(strings.TrimSpace(pebblestore.DefaultModelProvider))
	modelID := strings.TrimSpace(pebblestore.DefaultModelName)
	if providerID == "" || modelID == "" {
		return nil
	}
	if providerID == "copilot" {
		return nil
	}

	record, ok, err := s.store.GetRecord(providerID, modelID)
	if err != nil {
		return fmt.Errorf("read default model catalog record: %w", err)
	}
	if ok && record.ContextWindow > 0 {
		return nil
	}

	nowMs := s.now().UnixMilli()
	defaultContextWindow := 200000
	defaultRecord := pebblestore.ModelCatalogRecord{
		Provider:        providerID,
		Model:           modelID,
		ContextWindow:   defaultContextWindow,
		MaxOutputTokens: 32000,
		Reasoning:       true,
		Source:          "builtin",
		FetchedAt:       nowMs,
		ExpiresAt:       nowMs + int64((30*24*time.Hour)/time.Millisecond),
	}
	if err := s.store.SetRecord(defaultRecord); err != nil {
		return fmt.Errorf("write default model catalog record: %w", err)
	}
	return nil
}

func (s *CatalogService) seedPinnedSnapshotIfNeeded() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	meta, ok, err := s.store.GetMeta()
	if err != nil {
		return fmt.Errorf("read model catalog meta: %w", err)
	}
	if ok && !catalogMetaNeedsPinnedSeed(meta) {
		return nil
	}
	_, err = s.replaceSnapshotLocked(pinnedSwarmSnapshotJSON, catalogSourcePinned, pinnedCatalogSourceURL, pinnedCatalogVersionURL, "", "", "pinned_seed")
	return err
}

func (s *CatalogService) Get(providerID, modelID string) (CatalogLookup, error) {
	normalizedProvider := canonicalCatalogProviderID(providerID)
	normalizedModel := canonicalCatalogModelID(normalizedProvider, modelID)
	record, ok, err := s.store.GetRecord(normalizedProvider, normalizedModel)
	if err != nil {
		return CatalogLookup{}, err
	}
	if !ok && normalizedModel != strings.TrimSpace(modelID) {
		record, ok, err = s.store.GetRecord(normalizedProvider, strings.TrimSpace(modelID))
		if err != nil {
			return CatalogLookup{}, err
		}
	}
	if !ok {
		return CatalogLookup{Found: false}, nil
	}

	stale := record.ExpiresAt > 0 && record.ExpiresAt < s.now().UnixMilli()
	return CatalogLookup{
		Record: record,
		Found:  true,
		Stale:  stale,
	}, nil
}

func (s *CatalogService) List(providerID string, limit int) ([]pebblestore.ModelCatalogRecord, error) {
	providerID = canonicalCatalogProviderID(providerID)
	if providerID == "" {
		return nil, fmt.Errorf("provider is required")
	}
	if limit <= 0 {
		limit = 1000
	}
	return s.store.ListProvider(providerID, limit)
}

func (s *CatalogService) Meta() (pebblestore.ModelCatalogMeta, bool, error) {
	return s.store.GetMeta()
}

func (s *CatalogService) Refresh(ctx context.Context) (CatalogRefreshResult, error) {
	return s.refresh(ctx, false, "scheduled")
}

func (s *CatalogService) RefreshManual(ctx context.Context) (CatalogRefreshResult, error) {
	return s.refresh(ctx, true, "manual")
}

func (s *CatalogService) refresh(ctx context.Context, force bool, reason string) (CatalogRefreshResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(reason) == "" {
		reason = "scheduled"
	}
	meta, _, err := s.store.GetMeta()
	if err != nil {
		return CatalogRefreshResult{}, fmt.Errorf("read model catalog meta: %w", err)
	}
	if catalogMetaNeedsPinnedSeed(meta) {
		if _, err := s.replaceSnapshotLocked(pinnedSwarmSnapshotJSON, catalogSourcePinned, pinnedCatalogSourceURL, pinnedCatalogVersionURL, "", "", "pinned_seed"); err != nil {
			return CatalogRefreshResult{}, err
		}
		meta, _, err = s.store.GetMeta()
		if err != nil {
			return CatalogRefreshResult{}, fmt.Errorf("read seeded model catalog meta: %w", err)
		}
	}

	version, err := s.fetchSnapshotVersion(ctx, meta, force)
	if err != nil {
		updated := s.persistRefreshErrorLocked(meta, err, reason)
		result := catalogRefreshResultFromMeta(updated)
		result.UsedCache = updated.RecordCount > 0
		result.UsedPinned = updated.Source == catalogSourcePinned
		result.Manual = force
		return result, fmt.Errorf("fetch Swarm model snapshot version: %w", err)
	}

	now := s.now()
	nowMs := now.UnixMilli()
	expiresAt := now.Add(defaultCatalogTTL).UnixMilli()
	if version.NotModified {
		meta.VersionURL = s.versionURL
		meta.SourceURL = firstNonEmpty(meta.SourceURL, s.sourceURL)
		meta.SnapshotURL = firstNonEmpty(meta.SnapshotURL, s.sourceURL)
		meta.VersionETag = firstNonEmpty(version.ETag, meta.VersionETag)
		meta.LastCheckedAt = nowMs
		meta.ExpiresAt = expiresAt
		meta.LastError = ""
		meta.LastErrorAt = 0
		meta.LastRefreshReason = reason
		meta.UsingCacheFallback = false
		applyPinnedMetadata(&meta)
		applyLiveMetadataFromMeta(&meta, nowMs)
		if err := s.store.SetMeta(meta); err != nil {
			return CatalogRefreshResult{}, fmt.Errorf("persist model catalog meta: %w", err)
		}
		result := catalogRefreshResultFromMeta(meta)
		result.NotModified = true
		result.UsedCache = true
		result.UsedPinned = meta.Source == catalogSourcePinned
		result.Manual = force
		return result, nil
	}

	snapshotURL := resolveSnapshotURL(s.sourceURL, version.Version.SnapshotURL)
	if snapshotURL == "" {
		snapshotURL = s.sourceURL
	}
	changed := force || catalogMetaNeedsPinnedSeed(meta) || meta.RecordCount <= 0 || !sameCatalogSnapshot(meta, version.Version)
	if !changed {
		meta.SourceURL = firstNonEmpty(meta.SourceURL, snapshotURL)
		meta.SnapshotURL = firstNonEmpty(meta.SnapshotURL, snapshotURL)
		meta.VersionURL = s.versionURL
		meta.VersionETag = firstNonEmpty(version.ETag, meta.VersionETag)
		meta.LastCheckedAt = nowMs
		meta.ExpiresAt = expiresAt
		meta.LastError = ""
		meta.LastErrorAt = 0
		meta.LastRefreshReason = reason
		meta.UsingCacheFallback = false
		applyPinnedMetadata(&meta)
		applyLiveMetadata(&meta, version.Version, nowMs)
		if err := s.store.SetMeta(meta); err != nil {
			return CatalogRefreshResult{}, fmt.Errorf("persist model catalog meta: %w", err)
		}
		result := catalogRefreshResultFromMeta(meta)
		result.NotModified = true
		result.UsedCache = true
		result.UsedPinned = meta.Source == catalogSourcePinned
		result.Manual = force
		return result, nil
	}

	payload, snapshotETag, notModified, err := s.fetchSnapshot(ctx, snapshotURL, meta, !force && strings.TrimSpace(meta.ETag) != "")
	if err != nil {
		applyLiveMetadata(&meta, version.Version, nowMs)
		updated := s.persistRefreshErrorLocked(meta, err, reason)
		result := catalogRefreshResultFromMeta(updated)
		result.UsedCache = updated.RecordCount > 0
		result.UsedPinned = updated.Source == catalogSourcePinned
		result.Manual = force
		return result, fmt.Errorf("fetch Swarm model snapshot: %w", err)
	}
	if notModified && meta.RecordCount > 0 {
		meta.SourceURL = snapshotURL
		meta.SnapshotURL = snapshotURL
		meta.VersionURL = s.versionURL
		meta.VersionETag = firstNonEmpty(version.ETag, meta.VersionETag)
		meta.LastCheckedAt = nowMs
		meta.ExpiresAt = expiresAt
		meta.LastError = ""
		meta.LastErrorAt = 0
		meta.LastRefreshReason = reason
		meta.UsingCacheFallback = false
		applyPinnedMetadata(&meta)
		applyLiveMetadata(&meta, version.Version, nowMs)
		if err := s.store.SetMeta(meta); err != nil {
			return CatalogRefreshResult{}, fmt.Errorf("persist model catalog meta: %w", err)
		}
		result := catalogRefreshResultFromMeta(meta)
		result.NotModified = true
		result.UsedCache = true
		result.Manual = force
		return result, nil
	}

	result, err := s.replaceSnapshotLocked(payload, catalogSourceLive, snapshotURL, s.versionURL, snapshotETag, version.ETag, reason)
	if err != nil {
		applyLiveMetadata(&meta, version.Version, nowMs)
		updated := s.persistRefreshErrorLocked(meta, err, reason)
		fallbackResult := catalogRefreshResultFromMeta(updated)
		fallbackResult.UsedCache = updated.RecordCount > 0
		fallbackResult.UsedPinned = updated.Source == catalogSourcePinned
		fallbackResult.Manual = force
		return fallbackResult, err
	}
	result.Manual = force
	return result, nil
}

func (s *CatalogService) replaceSnapshotLocked(payload []byte, source, sourceURL, versionURL, etag, versionETag, reason string) (CatalogRefreshResult, error) {
	now := s.now()
	nowMs := now.UnixMilli()
	expiresAt := now.Add(defaultCatalogTTL).UnixMilli()
	records, snapshot, err := decodeSwarmSnapshotRecords(payload, nowMs, expiresAt, source, etag)
	if err != nil {
		return CatalogRefreshResult{}, err
	}
	if len(records) == 0 {
		return CatalogRefreshResult{}, fmt.Errorf("Swarm model snapshot returned no cacheable model records")
	}
	meta := metaFromSnapshot(snapshot, source, sourceURL, versionURL, etag, versionETag, nowMs, expiresAt, reason, len(records))
	applyPinnedMetadata(&meta)
	if source == catalogSourceLive {
		applyLiveMetadata(&meta, snapshot, nowMs)
	}
	if source == catalogSourcePinned {
		meta.PinnedSnapshotID = snapshot.SnapshotID
		meta.PinnedSnapshotVersion = snapshot.SnapshotVersion
		meta.PinnedGeneratedAt = snapshot.GeneratedAt
	}
	if err := s.store.ReplaceSnapshot(records, meta); err != nil {
		return CatalogRefreshResult{}, fmt.Errorf("persist model catalog snapshot: %w", err)
	}
	result := catalogRefreshResultFromMeta(meta)
	result.UsedPinned = source == catalogSourcePinned
	return result, nil
}

func (s *CatalogService) fetchSnapshotVersion(ctx context.Context, meta pebblestore.ModelCatalogMeta, force bool) (catalogVersionFetch, error) {
	versionURL := strings.TrimSpace(s.versionURL)
	if versionURL == "" {
		return catalogVersionFetch{}, fmt.Errorf("Swarm model snapshot version URL is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, versionURL, nil)
	if err != nil {
		return catalogVersionFetch{}, err
	}
	if !force && strings.TrimSpace(meta.VersionETag) != "" {
		req.Header.Set("If-None-Match", meta.VersionETag)
	}
	req.Header.Set("User-Agent", "swarmd/0")

	resp, err := s.client.Do(req)
	if err != nil {
		return catalogVersionFetch{}, err
	}
	defer resp.Body.Close()
	etag := strings.TrimSpace(resp.Header.Get("ETag"))
	if resp.StatusCode == http.StatusNotModified {
		return catalogVersionFetch{ETag: etag, NotModified: true}, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return catalogVersionFetch{}, fmt.Errorf("Swarm model snapshot version returned status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := readLimitedHTTPResponse(resp.Body, 1<<20, "Swarm model snapshot version")
	if err != nil {
		return catalogVersionFetch{}, err
	}
	version, err := decodeSwarmSnapshotVersion(body)
	if err != nil {
		return catalogVersionFetch{}, err
	}
	return catalogVersionFetch{Version: version, ETag: etag}, nil
}

func (s *CatalogService) fetchSnapshot(ctx context.Context, snapshotURL string, meta pebblestore.ModelCatalogMeta, useConditional bool) ([]byte, string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, snapshotURL, nil)
	if err != nil {
		return nil, "", false, err
	}
	if useConditional && strings.TrimSpace(meta.ETag) != "" {
		req.Header.Set("If-None-Match", meta.ETag)
	}
	req.Header.Set("User-Agent", "swarmd/0")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	etag := strings.TrimSpace(resp.Header.Get("ETag"))
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, "", false, fmt.Errorf("Swarm model snapshot returned status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := readLimitedHTTPResponse(resp.Body, 32<<20, "Swarm model snapshot")
	if err != nil {
		return nil, "", false, err
	}
	return body, etag, false, nil
}

func readLimitedHTTPResponse(body io.Reader, limit int64, label string) ([]byte, error) {
	limited := io.LimitReader(body, limit+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", label, err)
	}
	if int64(len(payload)) > limit {
		return nil, fmt.Errorf("read %s response: body exceeds %d byte limit", label, limit)
	}
	return payload, nil
}

func (s *CatalogService) persistRefreshErrorLocked(meta pebblestore.ModelCatalogMeta, err error, reason string) pebblestore.ModelCatalogMeta {
	nowMs := s.now().UnixMilli()
	meta.SourceURL = firstNonEmpty(meta.SourceURL, s.sourceURL)
	meta.SnapshotURL = firstNonEmpty(meta.SnapshotURL, s.sourceURL)
	meta.VersionURL = firstNonEmpty(meta.VersionURL, s.versionURL)
	meta.LastCheckedAt = nowMs
	meta.LastError = err.Error()
	meta.LastErrorAt = nowMs
	meta.LastRefreshReason = reason
	meta.UsingCacheFallback = meta.RecordCount > 0
	applyPinnedMetadata(&meta)
	_ = s.store.SetMeta(meta)
	return meta
}

func (s *CatalogService) StartAutoRefresh(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = defaultCatalogTTL
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_, _ = s.Refresh(context.Background())
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = s.Refresh(context.Background())
			}
		}
	}()
}

func decodeSwarmSnapshotVersion(payload []byte) (swarmSnapshotVersion, error) {
	var version swarmSnapshotVersion
	if err := json.Unmarshal(payload, &version); err != nil {
		return swarmSnapshotVersion{}, fmt.Errorf("decode Swarm model snapshot version payload: %w", err)
	}
	if strings.TrimSpace(version.SnapshotID) == "" || strings.TrimSpace(version.SnapshotVersion) == "" {
		return swarmSnapshotVersion{}, fmt.Errorf("Swarm model snapshot version is missing snapshot_id or snapshot_version")
	}
	return version, nil
}

func decodeSwarmSnapshotRecords(payload []byte, nowMs, expiresAt int64, source, etag string) ([]pebblestore.ModelCatalogRecord, swarmSnapshotVersion, error) {
	var snapshot swarmSnapshot
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, swarmSnapshotVersion{}, fmt.Errorf("decode Swarm model snapshot payload: %w", err)
	}
	version := snapshot.version()
	if strings.TrimSpace(version.SnapshotID) == "" || strings.TrimSpace(version.SnapshotVersion) == "" {
		return nil, swarmSnapshotVersion{}, fmt.Errorf("Swarm model snapshot is missing snapshot_id or snapshot_version")
	}
	if len(snapshot.Models) == 0 {
		return nil, swarmSnapshotVersion{}, fmt.Errorf("Swarm model snapshot returned an empty models array")
	}

	records := make([]pebblestore.ModelCatalogRecord, 0, len(snapshot.Models))
	seen := make(map[string]struct{}, len(snapshot.Models))
	for _, model := range snapshot.Models {
		providerID := canonicalCatalogProviderID(model.ProviderID)
		modelID := strings.TrimSpace(model.ModelID)
		if providerID == "" || modelID == "" {
			continue
		}
		contextWindow := modelDefaultContextWindow(model.ProviderSpecific, providerID, firstPositiveInt(model.Limits.ContextWindowTokens, model.Routing.TopProviderContextWindowTokens))
		if contextWindow <= 0 {
			continue
		}
		key := providerID + "\x00" + modelID
		if _, ok := seen[key]; ok {
			return nil, swarmSnapshotVersion{}, fmt.Errorf("Swarm model snapshot contains duplicate model record %q/%q", providerID, modelID)
		}
		seen[key] = struct{}{}

		maxOutputTokens := firstPositiveInt(model.Limits.MaxOutputTokens, model.Routing.TopProviderMaxOutputTokens)
		reasoning := model.Capabilities.SupportsReasoning != nil && *model.Capabilities.SupportsReasoning
		serviceTiers, defaultServiceTier := modelServingTiers(model.ProviderSpecific, providerID)
		thinkingOptions, defaultThinking, thinkingProviderParameter, thinkingMappings := modelThinkingMetadata(model.Thinking)
		record := pebblestore.ModelCatalogRecord{
			Provider:                  providerID,
			ProviderDisplayName:       strings.TrimSpace(model.ProviderDisplayName),
			Model:                     modelID,
			DisplayName:               strings.TrimSpace(model.DisplayName),
			CatalogID:                 strings.TrimSpace(model.CatalogID),
			ContextWindow:             contextWindow,
			MaxOutputTokens:           maxOutputTokens,
			Reasoning:                 reasoning,
			ThinkingOptions:           thinkingOptions,
			DefaultThinking:           defaultThinking,
			ThinkingProviderParameter: thinkingProviderParameter,
			ThinkingMappings:          thinkingMappings,
			ServiceTiers:              serviceTiers,
			DefaultServiceTier:        defaultServiceTier,
			ServiceTierMappings:       modelServiceTierMappings(model.ProviderSpecific, providerID),
			ContextModes:              modelContextModes(model.ProviderSpecific, providerID, contextWindow),
			Source:                    source,
			SourceSnapshotID:          snapshot.SnapshotID,
			SourceSnapshotVersion:     snapshot.SnapshotVersion,
			SourceGeneratedAt:         snapshot.GeneratedAt,
			ETag:                      etag,
			FetchedAt:                 nowMs,
			ExpiresAt:                 expiresAt,
			Pricing:                   cloneRawJSON(model.Pricing),
			Thinking:                  cloneRawJSON(model.Thinking),
			ProviderSpecific:          cloneRawJSON(model.ProviderSpecific),
		}
		records = append(records, record)
	}
	return records, version, nil
}

func modelDefaultContextWindow(providerSpecificRaw json.RawMessage, providerID string, fallback int) int {
	providerSpecific, ok := snapshotProviderSpecificFor(providerSpecificRaw, providerID)
	if ok && providerSpecific.ContextWindow > 0 {
		return providerSpecific.ContextWindow
	}
	return fallback
}

func modelServingTiers(providerSpecificRaw json.RawMessage, providerID string) ([]string, string) {
	providerSpecific, ok := snapshotProviderSpecificFor(providerSpecificRaw, providerID)
	if !ok {
		return nil, ""
	}
	out := normalizeCatalogStringList(providerSpecific.Serving.SupportedTiers)
	defaultTier := strings.ToLower(strings.TrimSpace(providerSpecific.Serving.DefaultTier))
	return out, defaultTier
}

func modelThinkingMetadata(thinkingRaw json.RawMessage) ([]string, string, string, []pebblestore.ModelCatalogThinkingMapping) {
	if len(bytes.TrimSpace(thinkingRaw)) == 0 {
		return nil, "", "", nil
	}
	var thinking swarmSnapshotThinking
	if err := json.Unmarshal(thinkingRaw, &thinking); err != nil {
		return nil, "", "", nil
	}
	options := normalizeCatalogStringList(thinking.SupportedSwarmSettings)
	defaultThinking := strings.ToLower(strings.TrimSpace(thinking.DefaultSwarmSetting))
	providerParameter := strings.TrimSpace(thinking.ProviderParameter)
	mappings := make([]pebblestore.ModelCatalogThinkingMapping, 0, len(thinking.SwarmSettingMappings))
	for _, mapping := range thinking.SwarmSettingMappings {
		swarmSetting := strings.ToLower(strings.TrimSpace(mapping.SwarmSetting))
		if swarmSetting == "" {
			continue
		}
		mappings = append(mappings, pebblestore.ModelCatalogThinkingMapping{
			SwarmSetting:           swarmSetting,
			ProviderParameter:      providerParameter,
			ProviderValue:          snapshotScalarString(mapping.ProviderValue),
			EffectiveProviderValue: snapshotScalarString(mapping.EffectiveProviderValue),
			Behavior:               strings.TrimSpace(mapping.Behavior),
		})
	}
	if len(options) == 0 && len(mappings) > 0 {
		options = make([]string, 0, len(mappings))
		for _, mapping := range mappings {
			options = append(options, mapping.SwarmSetting)
		}
	}
	return options, defaultThinking, providerParameter, mappings
}

func modelServiceTierMappings(providerSpecificRaw json.RawMessage, providerID string) []pebblestore.ModelCatalogServiceTierMapping {
	providerSpecific, ok := snapshotProviderSpecificFor(providerSpecificRaw, providerID)
	if !ok {
		return nil
	}
	rawByTier := make(map[string]swarmSnapshotRawTier)
	addRawTier := func(key string, raw *swarmSnapshotRawTier) {
		if raw == nil {
			return
		}
		copy := *raw
		if strings.TrimSpace(copy.Tier) == "" {
			copy.Tier = key
		}
		tier := strings.ToLower(strings.TrimSpace(copy.Tier))
		if tier == "" {
			return
		}
		rawByTier[tier] = copy
	}
	addRawTier("standard", providerSpecific.Serving.Standard)
	addRawTier("priority", providerSpecific.Serving.Priority)
	addRawTier("fast", providerSpecific.Serving.Fast)
	for key, raw := range providerSpecific.Serving.Tiers {
		copy := raw
		addRawTier(key, &copy)
	}

	order := normalizeCatalogStringList(providerSpecific.Serving.SupportedTiers)
	for tier := range rawByTier {
		if !stringInSlice(order, tier) {
			order = append(order, tier)
		}
	}
	mappings := make([]pebblestore.ModelCatalogServiceTierMapping, 0, len(order))
	for _, tier := range order {
		raw, ok := rawByTier[tier]
		if !ok {
			mappings = append(mappings, pebblestore.ModelCatalogServiceTierMapping{Tier: tier})
			continue
		}
		mappings = append(mappings, pebblestore.ModelCatalogServiceTierMapping{
			Tier:              tier,
			SwarmSetting:      strings.ToLower(strings.TrimSpace(raw.SwarmSetting)),
			ProviderParameter: strings.TrimSpace(raw.ProviderParameter),
			ProviderValue:     strings.TrimSpace(raw.ProviderValue),
			RequestModelPath:  firstNonEmpty(raw.RequestModelPath, providerSpecific.RequestModelPath),
		})
	}
	return mappings
}

func modelContextModes(providerSpecificRaw json.RawMessage, providerID string, defaultContextWindow int) []pebblestore.ModelCatalogContextMode {
	providerSpecific, ok := snapshotProviderSpecificFor(providerSpecificRaw, providerID)
	if !ok {
		return nil
	}
	out := make([]pebblestore.ModelCatalogContextMode, 0, len(providerSpecific.ContextModes)+1)
	for _, mode := range providerSpecific.ContextModes {
		modeID := strings.ToLower(strings.TrimSpace(mode.Mode))
		if modeID == "" {
			continue
		}
		contextWindow := mode.ContextWindow
		if contextWindow <= 0 && mode.Default {
			contextWindow = defaultContextWindow
		}
		out = append(out, pebblestore.ModelCatalogContextMode{
			Mode:          modeID,
			Label:         strings.TrimSpace(mode.Label),
			ContextWindow: contextWindow,
			Default:       mode.Default,
		})
	}
	if providerSpecific.MaxContextWindow > 0 && providerSpecific.MaxContextWindow != defaultContextWindow && !catalogContextModeExists(out, "1m") {
		out = append(out, pebblestore.ModelCatalogContextMode{
			Mode:          "1m",
			Label:         "1M context",
			ContextWindow: providerSpecific.MaxContextWindow,
		})
	}
	return out
}

func catalogContextModeExists(modes []pebblestore.ModelCatalogContextMode, modeID string) bool {
	modeID = strings.ToLower(strings.TrimSpace(modeID))
	for _, mode := range modes {
		if strings.ToLower(strings.TrimSpace(mode.Mode)) == modeID {
			return true
		}
	}
	return false
}

func normalizeCatalogStringList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func stringInSlice(values []string, value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, candidate := range values {
		if strings.EqualFold(candidate, value) {
			return true
		}
	}
	return false
}

func snapshotScalarString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func snapshotProviderSpecificFor(providerSpecificRaw json.RawMessage, providerID string) (swarmSnapshotProviderSpecific, bool) {
	if len(bytes.TrimSpace(providerSpecificRaw)) == 0 {
		return swarmSnapshotProviderSpecific{}, false
	}
	var decoded map[string]swarmSnapshotProviderSpecific
	if err := json.Unmarshal(providerSpecificRaw, &decoded); err != nil {
		return swarmSnapshotProviderSpecific{}, false
	}
	providerID = canonicalCatalogProviderID(providerID)
	if providerSpecific, ok := decoded[providerID]; ok {
		return providerSpecific, true
	}
	for key, providerSpecific := range decoded {
		if canonicalCatalogProviderID(key) == providerID {
			return providerSpecific, true
		}
	}
	return swarmSnapshotProviderSpecific{}, false
}

func (snapshot swarmSnapshot) version() swarmSnapshotVersion {
	return swarmSnapshotVersion{
		SchemaVersion:         snapshot.SchemaVersion,
		APIVersion:            snapshot.APIVersion,
		SnapshotSchemaVersion: snapshot.SnapshotSchemaVersion,
		SnapshotID:            snapshot.SnapshotID,
		SnapshotVersion:       snapshot.SnapshotVersion,
		GeneratedAt:           snapshot.GeneratedAt,
		ModelCount:            snapshot.ModelCount,
		ProviderCount:         snapshot.ProviderCount,
		HydratedProviderCount: snapshot.HydratedProviderCount,
		SnapshotURL:           "/v1/snapshot.json",
	}
}

func metaFromSnapshot(snapshot swarmSnapshotVersion, source, sourceURL, versionURL, etag, versionETag string, nowMs, expiresAt int64, reason string, recordCount int) pebblestore.ModelCatalogMeta {
	meta := pebblestore.ModelCatalogMeta{
		Source:                source,
		SourceURL:             sourceURL,
		SnapshotURL:           sourceURL,
		VersionURL:            versionURL,
		ETag:                  strings.TrimSpace(etag),
		VersionETag:           strings.TrimSpace(versionETag),
		SnapshotID:            snapshot.SnapshotID,
		SnapshotVersion:       snapshot.SnapshotVersion,
		SnapshotSchemaVersion: snapshot.SnapshotSchemaVersion,
		GeneratedAt:           snapshot.GeneratedAt,
		FetchedAt:             nowMs,
		LastCheckedAt:         nowMs,
		ExpiresAt:             expiresAt,
		RecordCount:           recordCount,
		ModelCount:            snapshot.ModelCount,
		ProviderCount:         snapshot.ProviderCount,
		HydratedProviderCount: snapshot.HydratedProviderCount,
		LastRefreshReason:     reason,
	}
	if source == catalogSourcePinned {
		meta.PinnedSnapshotID = snapshot.SnapshotID
		meta.PinnedSnapshotVersion = snapshot.SnapshotVersion
		meta.PinnedGeneratedAt = snapshot.GeneratedAt
	}
	if source == catalogSourceLive {
		meta.LiveSnapshotID = snapshot.SnapshotID
		meta.LiveSnapshotVersion = snapshot.SnapshotVersion
		meta.LiveGeneratedAt = snapshot.GeneratedAt
		meta.LiveCheckedAt = nowMs
	}
	return meta
}

func catalogRefreshResultFromMeta(meta pebblestore.ModelCatalogMeta) CatalogRefreshResult {
	return CatalogRefreshResult{
		Source:             meta.Source,
		SourceURL:          meta.SourceURL,
		VersionURL:         meta.VersionURL,
		SnapshotID:         meta.SnapshotID,
		SnapshotVersion:    meta.SnapshotVersion,
		GeneratedAt:        meta.GeneratedAt,
		ETag:               meta.ETag,
		VersionETag:        meta.VersionETag,
		FetchedAt:          meta.FetchedAt,
		LastCheckedAt:      meta.LastCheckedAt,
		ExpiresAt:          meta.ExpiresAt,
		RecordCount:        meta.RecordCount,
		ModelCount:         meta.ModelCount,
		UsingCacheFallback: meta.UsingCacheFallback,
		LastRefreshReason:  meta.LastRefreshReason,
	}
}

func catalogMetaNeedsPinnedSeed(meta pebblestore.ModelCatalogMeta) bool {
	if meta.RecordCount <= 0 {
		return true
	}
	if strings.TrimSpace(meta.SnapshotID) == "" || strings.TrimSpace(meta.SnapshotVersion) == "" {
		return true
	}
	pinned, ok := pinnedSnapshotVersionMetadata()
	if !ok {
		return false
	}
	if meta.Source == catalogSourcePinned && !sameCatalogSnapshot(meta, pinned) {
		return true
	}
	return false
}

func sameCatalogSnapshot(meta pebblestore.ModelCatalogMeta, version swarmSnapshotVersion) bool {
	return strings.TrimSpace(meta.SnapshotID) != "" &&
		strings.TrimSpace(meta.SnapshotVersion) != "" &&
		strings.TrimSpace(meta.SnapshotID) == strings.TrimSpace(version.SnapshotID) &&
		strings.TrimSpace(meta.SnapshotVersion) == strings.TrimSpace(version.SnapshotVersion)
}

func applyPinnedMetadata(meta *pebblestore.ModelCatalogMeta) {
	pinned, ok := pinnedSnapshotVersionMetadata()
	if !ok {
		return
	}
	meta.PinnedSnapshotID = pinned.SnapshotID
	meta.PinnedSnapshotVersion = pinned.SnapshotVersion
	meta.PinnedGeneratedAt = pinned.GeneratedAt
}

func applyLiveMetadata(meta *pebblestore.ModelCatalogMeta, version swarmSnapshotVersion, checkedAt int64) {
	meta.LiveSnapshotID = version.SnapshotID
	meta.LiveSnapshotVersion = version.SnapshotVersion
	meta.LiveGeneratedAt = version.GeneratedAt
	meta.LiveCheckedAt = checkedAt
	if version.SnapshotSchemaVersion != "" {
		meta.SnapshotSchemaVersion = version.SnapshotSchemaVersion
	}
	if version.ModelCount > 0 {
		meta.ModelCount = version.ModelCount
	}
	if version.ProviderCount > 0 {
		meta.ProviderCount = version.ProviderCount
	}
	if version.HydratedProviderCount > 0 {
		meta.HydratedProviderCount = version.HydratedProviderCount
	}
}

func applyLiveMetadataFromMeta(meta *pebblestore.ModelCatalogMeta, checkedAt int64) {
	if meta.LiveSnapshotID == "" {
		meta.LiveSnapshotID = meta.SnapshotID
	}
	if meta.LiveSnapshotVersion == "" {
		meta.LiveSnapshotVersion = meta.SnapshotVersion
	}
	if meta.LiveGeneratedAt == "" {
		meta.LiveGeneratedAt = meta.GeneratedAt
	}
	meta.LiveCheckedAt = checkedAt
}

func pinnedSnapshotVersionMetadata() (swarmSnapshotVersion, bool) {
	if len(pinnedSwarmSnapshotVersionJSON) > 0 {
		version, err := decodeSwarmSnapshotVersion(pinnedSwarmSnapshotVersionJSON)
		if err == nil {
			return version, true
		}
	}
	var snapshot swarmSnapshot
	decoder := json.NewDecoder(bytes.NewReader(pinnedSwarmSnapshotJSON))
	if err := decoder.Decode(&snapshot); err != nil {
		return swarmSnapshotVersion{}, false
	}
	version := snapshot.version()
	return version, strings.TrimSpace(version.SnapshotID) != "" && strings.TrimSpace(version.SnapshotVersion) != ""
}

func resolveSnapshotURL(baseURL, snapshotURL string) string {
	snapshotURL = strings.TrimSpace(snapshotURL)
	if snapshotURL == "" {
		return strings.TrimSpace(baseURL)
	}
	snapshot, err := url.Parse(snapshotURL)
	if err == nil && snapshot.IsAbs() {
		return snapshot.String()
	}
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return snapshotURL
	}
	return base.ResolveReference(snapshot).String()
}

func cloneRawJSON(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	return append(json.RawMessage(nil), trimmed...)
}

func firstPositiveInt(values ...*int) int {
	for _, value := range values {
		if value != nil && *value > 0 {
			return *value
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func canonicalCatalogProviderID(providerID string) string {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" {
		return ""
	}
	if canonicalID, ok := catalogProviderCanonicalIDs[providerID]; ok {
		return canonicalID
	}
	return providerID
}

func canonicalCatalogModelID(providerID, modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if !strings.EqualFold(canonicalCatalogProviderID(providerID), "fireworks") {
		return modelID
	}
	lower := strings.ToLower(modelID)
	for _, prefix := range []string{"accounts/fireworks/models/", "accounts/fireworks/routers/", "fireworks/"} {
		if strings.HasPrefix(lower, prefix) {
			suffix := strings.TrimSpace(modelID[len(prefix):])
			if suffix != "" && !strings.Contains(suffix, "/") {
				return suffix
			}
		}
	}
	return modelID
}
