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

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	catalogMaterializationVersion = 1
	defaultCatalogURL             = "https://models.swarmagent.dev/v1/snapshot.json"
	defaultCatalogVersionURL      = "https://models.swarmagent.dev/v1/snapshot-version.json"
	defaultCatalogTTL             = time.Hour
	defaultCatalogFetchTimeout    = 10 * time.Second

	catalogSourcePinned = "swarm_snapshot:pinned"
	catalogSourceLive   = "swarm_snapshot:live"

	pinnedCatalogSourceURL  = "embedded:swarm-models/v1/snapshot.json"
	pinnedCatalogVersionURL = "embedded:swarm-models/v1/snapshot-version.json"
)

var catalogProviderCanonicalIDs = map[string]string{
	"github-copilot": "copilot",
	"fireworks-ai":   "fireworks",
	"openai-api":     "openai",
	"openai_api":     "openai",
	"codex-oauth":    "codex",
	"codex_oauth":    "codex",
	"chatgpt-codex":  "codex",
	"chatgpt_codex":  "codex",
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
	Meta   *pebblestore.ModelCatalogMeta  `json:"meta,omitempty"`
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
	SchemaVersion         string                                                    `json:"schema_version"`
	APIVersion            string                                                    `json:"api_version"`
	SnapshotSchemaVersion string                                                    `json:"snapshot_schema_version"`
	SnapshotID            string                                                    `json:"snapshot_id"`
	SnapshotVersion       string                                                    `json:"snapshot_version"`
	GeneratedAt           string                                                    `json:"generated_at"`
	ModelCount            int                                                       `json:"model_count"`
	ProviderCount         int                                                       `json:"provider_count"`
	HydratedProviderCount int                                                       `json:"hydrated_provider_count"`
	Recommendations       map[string]map[string]swarmSnapshotProviderRecommendation `json:"recommendations"`
	Definitions           map[string]swarmSnapshotProviderDefinition                `json:"definitions"`
	Providers             []swarmSnapshotProvider                                   `json:"providers"`
	Models                []swarmSnapshotModel                                      `json:"models"`
}

type swarmSnapshotProvider struct {
	ProviderID      string                                         `json:"provider_id"`
	Recommendations map[string]swarmSnapshotProviderRecommendation `json:"recommendations"`
}

type swarmSnapshotProviderDefinition struct {
	Multimodal json.RawMessage `json:"multimodal"`
}

type swarmSnapshotProviderRecommendation struct {
	Model    string `json:"model"`
	Thinking string `json:"thinking"`
	Fast     string `json:"fast"`
	Serving  string `json:"serving"`
	Notes    string `json:"notes"`
}

type swarmSnapshotModel struct {
	CatalogID           string `json:"catalog_id"`
	ProviderID          string `json:"provider_id"`
	ProviderDisplayName string `json:"provider_display_name"`
	ModelID             string `json:"model_id"`
	DisplayName         string `json:"display_name"`
	Capabilities        struct {
		SupportsTextInput   *bool `json:"supports_text_input"`
		SupportsTextOutput  *bool `json:"supports_text_output"`
		SupportsImageInput  *bool `json:"supports_image_input"`
		SupportsImageOutput *bool `json:"supports_image_output"`
		SupportsAudioInput  *bool `json:"supports_audio_input"`
		SupportsAudioOutput *bool `json:"supports_audio_output"`
		SupportsVideoInput  *bool `json:"supports_video_input"`
		SupportsVideoOutput *bool `json:"supports_video_output"`
		SupportsFileInput   *bool `json:"supports_file_input"`
		SupportsPDFInput    *bool `json:"supports_pdf_input"`
		SupportsReasoning   *bool `json:"supports_reasoning"`
	} `json:"capabilities"`
	Limits struct {
		ContextWindowTokens *int `json:"context_window_tokens"`
		MaxOutputTokens     *int `json:"max_output_tokens"`
	} `json:"limits"`
	Pricing          json.RawMessage `json:"pricing"`
	Thinking         json.RawMessage `json:"thinking"`
	ProviderSpecific json.RawMessage `json:"provider_specific"`
	Swarm            struct {
		Recommendations []swarmSnapshotRecommendation `json:"recommendations"`
	} `json:"swarm"`
	Routing struct {
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

type swarmSnapshotRecommendation struct {
	Role     string `json:"role"`
	Mode     string `json:"mode"`
	Thinking string `json:"thinking"`
	Serving  string `json:"serving"`
	Notes    string `json:"notes"`
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
		FastMode       *swarmSnapshotRawTier           `json:"fast_mode"`
		Tiers          map[string]swarmSnapshotRawTier `json:"tiers"`
	} `json:"serving"`
}

type swarmSnapshotRawTier struct {
	Tier              string          `json:"tier"`
	Supported         *bool           `json:"supported"`
	Status            string          `json:"status"`
	SwarmSetting      string          `json:"swarm_setting"`
	ProviderParameter string          `json:"provider_parameter"`
	ProviderValue     string          `json:"provider_value"`
	BetaHeader        string          `json:"beta_header"`
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
		if catalogMetaPinnedMetadataNeedsUpdate(meta) {
			applyPinnedMetadata(&meta)
			if err := s.store.SetMeta(meta); err != nil {
				return fmt.Errorf("persist model catalog pinned snapshot metadata: %w", err)
			}
		}
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
	meta, metaFound, err := s.store.GetMeta()
	if err != nil {
		return CatalogLookup{}, fmt.Errorf("read model catalog meta for lookup: %w", err)
	}
	var matchedMeta *pebblestore.ModelCatalogMeta
	if metaFound && catalogRecordMatchesMeta(record, meta) {
		matchedMeta = &meta
	}

	stale := record.ExpiresAt > 0 && record.ExpiresAt < s.now().UnixMilli()
	return CatalogLookup{
		Record: record,
		Meta:   matchedMeta,
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

func (s *CatalogService) RecommendedDefaults(providerID string) (pebblestore.ModelCatalogRecord, pebblestore.ModelCatalogRecord, pebblestore.ModelCatalogRecord, bool, error) {
	recommended, ok, err := s.RecommendedRoleDefaults(providerID, "auto", "plan", "utility")
	if err != nil || !ok {
		return pebblestore.ModelCatalogRecord{}, pebblestore.ModelCatalogRecord{}, pebblestore.ModelCatalogRecord{}, false, err
	}
	return recommended["auto"], recommended["plan"], recommended["utility"], true, nil
}

// RecommendedRoleDefaults returns the catalog record carrying each requested
// provider-level recommendation. It is the canonical lookup for onboarding
// roles that have independent model recommendations.
func (s *CatalogService) RecommendedRoleDefaults(providerID string, roles ...string) (map[string]pebblestore.ModelCatalogRecord, bool, error) {
	providerID = canonicalCatalogProviderID(providerID)
	if providerID == "" || len(roles) == 0 {
		return nil, false, nil
	}
	wanted := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		role = strings.ToLower(strings.TrimSpace(role))
		if role == "main" {
			role = "auto"
		}
		if role != "" {
			wanted[role] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return nil, false, nil
	}
	records, err := s.store.ListProvider(providerID, 2000)
	if err != nil {
		return nil, false, err
	}
	out := make(map[string]pebblestore.ModelCatalogRecord, len(wanted))
	for _, record := range records {
		for _, rec := range record.Recommendations {
			role := strings.ToLower(strings.TrimSpace(rec.Role))
			if role == "main" {
				role = "auto"
			}
			if _, needed := wanted[role]; !needed || strings.TrimSpace(out[role].Model) != "" {
				continue
			}
			out[role] = record
		}
	}
	for role := range wanted {
		if strings.TrimSpace(out[role].Model) == "" {
			return nil, false, nil
		}
	}
	return out, true, nil
}

func (s *CatalogService) Meta() (pebblestore.ModelCatalogMeta, bool, error) {
	return s.store.GetMeta()
}

func (s *CatalogService) Refresh(ctx context.Context) (CatalogRefreshResult, error) {
	return s.refresh(ctx, false, "scheduled")
}

func (s *CatalogService) Check(ctx context.Context) (CatalogRefreshResult, error) {
	return s.refresh(ctx, false, "app_poll")
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
		if contextWindow <= 0 && !modelAllowsUnknownContextWindow(providerID, model) {
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
		media, err := snapshotModelMediaCapabilities(snapshot, providerID, model)
		if err != nil {
			return nil, swarmSnapshotVersion{}, fmt.Errorf("Swarm model snapshot media record %q/%q: %w", providerID, modelID, err)
		}
		catalogModalities, err := snapshotModelCatalogModalities(model, providerID)
		if err != nil {
			return nil, swarmSnapshotVersion{}, fmt.Errorf("Swarm model snapshot catalog modalities %q/%q: %w", providerID, modelID, err)
		}
		recommendations := modelRecommendations(model.Swarm.Recommendations)
		recommendations = appendProviderRecommendations(recommendations, providerID, modelID, model.CatalogID, snapshot.providerRecommendations(providerID))
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
			Recommendations:           recommendations,
			ContextModes:              modelContextModes(model.ProviderSpecific, providerID, contextWindow),
			Media:                     media,
			CatalogModalities:         catalogModalities,
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

type swarmSnapshotMultimodal struct {
	APISurface                  string                      `json:"api_surface"`
	InputModalities             []string                    `json:"input_modalities"`
	OutputModalities            []string                    `json:"output_modalities"`
	ModelNativeInputModalities  []string                    `json:"model_native_input_modalities"`
	ModelNativeOutputModalities []string                    `json:"model_native_output_modalities"`
	ClientAttachmentInputTypes  []string                    `json:"client_attachment_input_types"`
	ImageInputMediaTypes        []string                    `json:"image_input_media_types"`
	FileInputTypes              []string                    `json:"file_input_types"`
	FileInputCategories         []string                    `json:"file_input_categories"`
	InputContentItemTypes       []string                    `json:"input_content_item_types"`
	OutputContentTypes          []string                    `json:"output_content_types"`
	UnsupportedModalities       []string                    `json:"unsupported_modalities"`
	UnsupportedDirectModalities []string                    `json:"unsupported_direct_modalities"`
	UnsupportedNativeModalities []string                    `json:"unsupported_native_modalities"`
	SourceIDs                   []string                    `json:"source_ids"`
	Text                        swarmSnapshotModalityDetail `json:"text"`
	Image                       swarmSnapshotModalityDetail `json:"image"`
	File                        swarmSnapshotModalityDetail `json:"file"`
	PDF                         swarmSnapshotModalityDetail `json:"pdf"`
	Audio                       swarmSnapshotModalityDetail `json:"audio"`
	Video                       swarmSnapshotModalityDetail `json:"video"`
}

type swarmSnapshotModalityDetail struct {
	Input                    *bool    `json:"input"`
	Output                   *bool    `json:"output"`
	SupportedInputMediaTypes []string `json:"supported_input_media_types"`
	AcceptedMediaTypes       []string `json:"accepted_media_types"`
	SupportedInputTypes      []string `json:"supported_input_types"`
	SupportedInputCategories []string `json:"supported_input_categories"`
	MediaType                string   `json:"media_type"`
	Processing               string   `json:"processing"`
}

func snapshotModelCatalogModalities(model swarmSnapshotModel, providerID string) (pebblestore.ModelCatalogModalities, error) {
	modelRaw, err := snapshotProviderMultimodalRaw(model.ProviderSpecific, providerID)
	if err != nil {
		return pebblestore.ModelCatalogModalities{}, err
	}
	facts, _, err := decodeSnapshotMultimodal(modelRaw)
	if err != nil {
		return pebblestore.ModelCatalogModalities{}, err
	}
	inputs := mergeCatalogModalities(firstNonEmptyStringList(facts.ModelNativeInputModalities, facts.InputModalities), capabilityModalities(model, true))
	outputs := mergeCatalogModalities(firstNonEmptyStringList(facts.ModelNativeOutputModalities, facts.OutputModalities), capabilityModalities(model, false))
	categories := firstNonEmptyStringList(facts.File.SupportedInputCategories, facts.FileInputCategories)
	return pebblestore.ModelCatalogModalities{
		Inputs: normalizeCatalogStringList(inputs), Outputs: normalizeCatalogStringList(outputs), Categories: normalizeCatalogStringList(categories),
	}, nil
}

func mergeCatalogModalities(groups ...[]string) []string {
	merged := make([]string, 0, 6)
	for _, group := range groups {
		for _, value := range group {
			if value = strings.ToLower(strings.TrimSpace(value)); value != "" && !stringInSlice(merged, value) {
				merged = append(merged, value)
			}
		}
	}
	return merged
}

func capabilityModalities(model swarmSnapshotModel, input bool) []string {
	facts := []struct {
		name   string
		input  *bool
		output *bool
	}{
		{"text", model.Capabilities.SupportsTextInput, model.Capabilities.SupportsTextOutput},
		{"image", model.Capabilities.SupportsImageInput, model.Capabilities.SupportsImageOutput},
		{"audio", model.Capabilities.SupportsAudioInput, model.Capabilities.SupportsAudioOutput},
		{"video", model.Capabilities.SupportsVideoInput, model.Capabilities.SupportsVideoOutput},
		{"file", model.Capabilities.SupportsFileInput, nil},
		{"pdf", model.Capabilities.SupportsPDFInput, nil},
	}
	out := make([]string, 0, len(facts))
	for _, fact := range facts {
		value := fact.output
		if input {
			value = fact.input
		}
		if value != nil && *value {
			out = append(out, fact.name)
		}
	}
	return out
}

func catalogMediaProviderEnabled(providerID string) bool {
	switch canonicalCatalogProviderID(providerID) {
	case "openai", "codex", "google", "anthropic", "fireworks", "openrouter":
		return true
	default:
		return false
	}
}

func catalogMediaSurfaceAliasMatches(providerID, catalogSurface, runtimeSurface string) bool {
	catalogSurface = strings.ToLower(strings.TrimSpace(catalogSurface))
	if strings.EqualFold(catalogSurface, strings.TrimSpace(runtimeSurface)) {
		return true
	}
	switch canonicalCatalogProviderID(providerID) {
	case "google":
		return catalogSurface == "generate_content"
	case "anthropic":
		return catalogSurface == "messages"
	case "fireworks":
		return catalogSurface == "chat_completions" || catalogSurface == "serverless_chat_completions"
	case "openrouter":
		return catalogSurface == "chat_completions"
	default:
		return false
	}
}

func snapshotModelMediaCapabilities(snapshot swarmSnapshot, providerID string, model swarmSnapshotModel) (*pebblestore.ModelCatalogMediaCapabilities, error) {
	providerID = canonicalCatalogProviderID(providerID)
	if !catalogMediaProviderEnabled(providerID) {
		return nil, nil
	}

	providerRaw := snapshotProviderDefinitionMultimodal(snapshot, providerID)
	modelRaw, err := snapshotProviderMultimodalRaw(model.ProviderSpecific, providerID)
	if err != nil {
		return nil, err
	}
	providerFacts, providerPresent, err := decodeSnapshotMultimodal(providerRaw)
	if err != nil {
		return nil, fmt.Errorf("invalid provider multimodal definition: %w", err)
	}
	modelFacts, modelPresent, err := decodeSnapshotMultimodal(modelRaw)
	if err != nil {
		return nil, fmt.Errorf("invalid model multimodal definition: %w", err)
	}

	surface := ""
	credentialSurface := ""
	switch providerID {
	case "openai":
		surface = provideriface.MediaProviderSurfaceOpenAIResponses
		credentialSurface = provideriface.MediaCredentialSurfaceOpenAIAPIKey
	case "codex":
		surface = provideriface.MediaProviderSurfaceCodexChatGPT
		credentialSurface = provideriface.MediaCredentialSurfaceCodexOAuth
	case "google":
		surface = provideriface.MediaProviderSurfaceGoogleGenerateContent
		credentialSurface = provideriface.MediaCredentialSurfaceGoogleAPIKey
	case "anthropic":
		surface = provideriface.MediaProviderSurfaceAnthropicMessages
		credentialSurface = provideriface.MediaCredentialSurfaceAnthropicAPIKey
	case "fireworks":
		surface = provideriface.MediaProviderSurfaceFireworksChatCompletions
		credentialSurface = provideriface.MediaCredentialSurfaceFireworksAPIKey
	case "openrouter":
		surface = provideriface.MediaProviderSurfaceOpenRouterChatCompletions
		credentialSurface = provideriface.MediaCredentialSurfaceOpenRouterAPIKey
	}
	if modelPresent && strings.TrimSpace(modelFacts.APISurface) != "" && !catalogMediaSurfaceAliasMatches(providerID, modelFacts.APISurface, surface) {
		return nil, fmt.Errorf("provider surface %q contradicts %q", modelFacts.APISurface, surface)
	}

	media := &pebblestore.ModelCatalogMediaCapabilities{
		State:             pebblestore.ModelCatalogMediaStateUnknown,
		ProviderSurface:   surface,
		CredentialSurface: credentialSurface,
		SourceIDs:         normalizeCatalogStringList(firstNonEmptyStringList(modelFacts.SourceIDs, providerFacts.SourceIDs)),
	}
	if !providerPresent && !modelPresent && !snapshotCapabilitiesHaveMediaFacts(model) {
		return media, nil
	}

	inputModalities := firstNonEmptyStringList(modelFacts.ModelNativeInputModalities, modelFacts.InputModalities, providerFacts.ModelNativeInputModalities, providerFacts.InputModalities)
	outputModalities := firstNonEmptyStringList(modelFacts.ModelNativeOutputModalities, modelFacts.OutputModalities, providerFacts.ModelNativeOutputModalities, providerFacts.OutputModalities)
	unsupported := firstNonEmptyStringList(modelFacts.UnsupportedDirectModalities, modelFacts.UnsupportedNativeModalities, modelFacts.UnsupportedModalities, providerFacts.UnsupportedDirectModalities, providerFacts.UnsupportedNativeModalities, providerFacts.UnsupportedModalities)

	inputSemantics := pebblestore.ModelCatalogMediaSemanticsNative
	fileSemantics := pebblestore.ModelCatalogMediaSemanticsProviderProcessed
	fileTypes := firstNonEmptyStringList(modelFacts.File.SupportedInputTypes, modelFacts.File.SupportedInputCategories, modelFacts.ClientAttachmentInputTypes, providerFacts.FileInputTypes, providerFacts.FileInputCategories, providerFacts.ClientAttachmentInputTypes)
	if providerID == "codex" {
		fileSemantics = pebblestore.ModelCatalogMediaSemanticsClientProcessed
	}
	imageMIMETypes := firstNonEmptyStringList(
		modelFacts.Image.SupportedInputMediaTypes,
		modelFacts.Image.AcceptedMediaTypes,
		providerFacts.Image.SupportedInputMediaTypes,
		providerFacts.Image.AcceptedMediaTypes,
		providerFacts.ImageInputMediaTypes,
	)
	if providerID == "google" && len(imageMIMETypes) == 0 && (boolPtrValue(model.Capabilities.SupportsImageInput) || boolPtrValue(modelFacts.Image.Input)) {
		// Google's current snapshot nests the exact generateContent MIME contract
		// deeper than the legacy structural decoder. Apply it only after this exact
		// model has independently affirmed image input.
		imageMIMETypes = []string{"image/heic", "image/heif", "image/jpeg", "image/png", "image/webp"}
	}

	inputFacts := []struct {
		modality   string
		capability *bool
		detail     swarmSnapshotModalityDetail
		semantics  string
		mimeTypes  []string
		fileTypes  []string
	}{
		{"text", model.Capabilities.SupportsTextInput, modelFacts.Text, inputSemantics, nil, nil},
		{"image", model.Capabilities.SupportsImageInput, modelFacts.Image, inputSemantics, imageMIMETypes, nil},
		{"file", model.Capabilities.SupportsFileInput, modelFacts.File, fileSemantics, nil, fileTypes},
		{"pdf", model.Capabilities.SupportsPDFInput, modelFacts.PDF, fileSemantics, []string{"application/pdf"}, []string{"pdf"}},
		{"audio", model.Capabilities.SupportsAudioInput, modelFacts.Audio, inputSemantics, nil, nil},
		{"video", model.Capabilities.SupportsVideoInput, modelFacts.Video, inputSemantics, nil, nil},
	}
	for _, fact := range inputFacts {
		// Newly reviewed conversational surfaces intentionally hydrate image input
		// only. OpenAI/Codex retain their previously reviewed file/PDF vocabulary.
		if providerID != "openai" && providerID != "codex" && fact.modality != "image" {
			continue
		}
		state, err := reconcileSnapshotMediaState("input", fact.modality, fact.capability, fact.detail.Input, inputModalities, unsupported)
		if err != nil {
			return nil, err
		}
		media.Inputs = append(media.Inputs, pebblestore.ModelCatalogMediaDirection{
			Modality: fact.modality, State: state, Semantics: fact.semantics,
			MIMETypes: normalizeCatalogStringList(fact.mimeTypes), FileTypes: normalizeCatalogStringList(fact.fileTypes),
			Types: normalizeCatalogStringList(modelFacts.InputContentItemTypes), Processing: strings.TrimSpace(fact.detail.Processing),
		})
	}
	outputFacts := []struct {
		modality   string
		capability *bool
		detail     *bool
	}{
		{"text", model.Capabilities.SupportsTextOutput, modelFacts.Text.Output},
		{"image", model.Capabilities.SupportsImageOutput, modelFacts.Image.Output},
		{"audio", model.Capabilities.SupportsAudioOutput, modelFacts.Audio.Output},
		{"video", model.Capabilities.SupportsVideoOutput, modelFacts.Video.Output},
	}
	for _, fact := range outputFacts {
		if providerID != "openai" && providerID != "codex" {
			continue
		}
		state, err := reconcileSnapshotMediaState("output", fact.modality, fact.capability, fact.detail, outputModalities, unsupported)
		if err != nil {
			return nil, err
		}
		media.Outputs = append(media.Outputs, pebblestore.ModelCatalogMediaDirection{
			Modality: fact.modality, State: state, Semantics: pebblestore.ModelCatalogMediaSemanticsNative,
			Types: normalizeCatalogStringList(modelFacts.OutputContentTypes),
		})
	}

	for _, direction := range media.Inputs {
		if direction.State != pebblestore.ModelCatalogMediaStateSupported {
			continue
		}
		if direction.Modality == "file" && len(direction.FileTypes) == 0 {
			return nil, fmt.Errorf("supported file input is missing exact file types")
		}
	}
	media.State = pebblestore.ModelCatalogMediaStateSupported
	return media, nil
}

func snapshotProviderDefinitionMultimodal(snapshot swarmSnapshot, providerID string) json.RawMessage {
	for key, definition := range snapshot.Definitions {
		if canonicalCatalogProviderID(key) == providerID {
			return definition.Multimodal
		}
	}
	return nil
}

func snapshotProviderMultimodalRaw(providerSpecificRaw json.RawMessage, providerID string) (json.RawMessage, error) {
	if len(bytes.TrimSpace(providerSpecificRaw)) == 0 {
		return nil, nil
	}
	var providers map[string]json.RawMessage
	if err := json.Unmarshal(providerSpecificRaw, &providers); err != nil {
		return nil, fmt.Errorf("decode provider_specific: %w", err)
	}
	for key, raw := range providers {
		if canonicalCatalogProviderID(key) != providerID {
			continue
		}
		var provider struct {
			Multimodal json.RawMessage `json:"multimodal"`
		}
		if err := json.Unmarshal(raw, &provider); err != nil {
			return nil, fmt.Errorf("decode %s provider-specific facts: %w", providerID, err)
		}
		return provider.Multimodal, nil
	}
	return nil, nil
}

func decodeSnapshotMultimodal(raw json.RawMessage) (swarmSnapshotMultimodal, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return swarmSnapshotMultimodal{}, false, nil
	}
	var decoded swarmSnapshotMultimodal
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return swarmSnapshotMultimodal{}, false, err
	}
	return decoded, true, nil
}

func snapshotCapabilitiesHaveMediaFacts(model swarmSnapshotModel) bool {
	return model.Capabilities.SupportsTextInput != nil || model.Capabilities.SupportsTextOutput != nil ||
		model.Capabilities.SupportsImageInput != nil || model.Capabilities.SupportsImageOutput != nil ||
		model.Capabilities.SupportsAudioInput != nil || model.Capabilities.SupportsAudioOutput != nil ||
		model.Capabilities.SupportsVideoInput != nil || model.Capabilities.SupportsVideoOutput != nil ||
		model.Capabilities.SupportsFileInput != nil || model.Capabilities.SupportsPDFInput != nil
}

func reconcileSnapshotMediaState(direction, modality string, capability, detail *bool, admitted, unsupported []string) (string, error) {
	listed := stringInSlice(admitted, modality)
	denied := stringInSlice(unsupported, modality) || stringInSlice(unsupported, modality+"_"+direction)
	if listed && denied {
		return "", fmt.Errorf("%s %s is both admitted and unsupported", modality, direction)
	}
	if capability != nil && detail != nil && *capability != *detail {
		return "", fmt.Errorf("%s %s capability contradicts model multimodal detail", modality, direction)
	}
	if capability != nil && denied && *capability {
		return "", fmt.Errorf("%s %s capability contradicts unsupported modality", modality, direction)
	}
	if detail != nil && denied && *detail {
		return "", fmt.Errorf("%s %s detail contradicts unsupported modality", modality, direction)
	}
	if capability != nil {
		if *capability {
			return pebblestore.ModelCatalogMediaStateSupported, nil
		}
		return pebblestore.ModelCatalogMediaStateUnsupported, nil
	}
	if detail != nil {
		if *detail {
			return pebblestore.ModelCatalogMediaStateSupported, nil
		}
		return pebblestore.ModelCatalogMediaStateUnsupported, nil
	}
	if listed {
		return pebblestore.ModelCatalogMediaStateSupported, nil
	}
	if denied {
		return pebblestore.ModelCatalogMediaStateUnsupported, nil
	}
	return pebblestore.ModelCatalogMediaStateUnknown, nil
}

func firstNonEmptyStringList(values ...[]string) []string {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func catalogRecordMatchesMeta(record pebblestore.ModelCatalogRecord, meta pebblestore.ModelCatalogMeta) bool {
	return strings.TrimSpace(record.SourceSnapshotID) != "" && strings.TrimSpace(record.SourceSnapshotVersion) != "" &&
		strings.TrimSpace(record.SourceSnapshotID) == strings.TrimSpace(meta.SnapshotID) &&
		strings.TrimSpace(record.SourceSnapshotVersion) == strings.TrimSpace(meta.SnapshotVersion)
}

func modelDefaultContextWindow(providerSpecificRaw json.RawMessage, providerID string, fallback int) int {
	providerSpecific, ok := snapshotProviderSpecificFor(providerSpecificRaw, providerID)
	if ok && providerSpecific.ContextWindow > 0 {
		return providerSpecific.ContextWindow
	}
	return fallback
}

func modelAllowsUnknownContextWindow(providerID string, model swarmSnapshotModel) bool {
	providerID = canonicalCatalogProviderID(providerID)
	if providerID != "openai" {
		return false
	}
	return boolPtrValue(model.Capabilities.SupportsTextInput) && boolPtrValue(model.Capabilities.SupportsTextOutput)
}

func boolPtrValue(value *bool) bool {
	return value != nil && *value
}

func providerHidesAsyncBatchTier(providerID string) bool {
	switch canonicalCatalogProviderID(providerID) {
	case "anthropic", "openai":
		return true
	default:
		return false
	}
}

func modelServingTiers(providerSpecificRaw json.RawMessage, providerID string) ([]string, string) {
	providerSpecific, ok := snapshotProviderSpecificFor(providerSpecificRaw, providerID)
	if !ok {
		return nil, ""
	}
	out := normalizeCatalogStringList(providerSpecific.Serving.SupportedTiers)
	if providerHidesAsyncBatchTier(providerID) {
		out = removeCatalogString(out, "batch")
	}
	defaultTier := strings.ToLower(strings.TrimSpace(providerSpecific.Serving.DefaultTier))
	if providerHidesAsyncBatchTier(providerID) && defaultTier == "batch" {
		defaultTier = ""
	}
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

func (snapshot swarmSnapshot) providerRecommendations(providerID string) map[string]swarmSnapshotProviderRecommendation {
	providerID = canonicalCatalogProviderID(providerID)
	if providerID == "" {
		return nil
	}
	out := make(map[string]swarmSnapshotProviderRecommendation)
	add := func(role string, rec swarmSnapshotProviderRecommendation) {
		role = strings.ToLower(strings.TrimSpace(role))
		if role == "" || strings.TrimSpace(rec.Model) == "" {
			return
		}
		if _, exists := out[role]; exists {
			return
		}
		out[role] = rec
	}
	for rawProviderID, recommendations := range snapshot.Recommendations {
		if canonicalCatalogProviderID(rawProviderID) != providerID {
			continue
		}
		for role, rec := range recommendations {
			add(role, rec)
		}
	}
	for _, provider := range snapshot.Providers {
		if canonicalCatalogProviderID(provider.ProviderID) != providerID {
			continue
		}
		for role, rec := range provider.Recommendations {
			add(role, rec)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func appendProviderRecommendations(existing []pebblestore.ModelCatalogRecommendation, providerID, modelID, catalogID string, values map[string]swarmSnapshotProviderRecommendation) []pebblestore.ModelCatalogRecommendation {
	if len(values) == 0 {
		return existing
	}
	providerID = canonicalCatalogProviderID(providerID)
	modelID = canonicalCatalogModelID(providerID, modelID)
	catalogID = strings.TrimSpace(catalogID)
	existingRoles := make(map[string]struct{}, len(existing))
	for _, rec := range existing {
		role := strings.ToLower(strings.TrimSpace(rec.Role))
		if role != "" {
			existingRoles[role] = struct{}{}
		}
	}
	appendRole := func(role string) {
		role = strings.ToLower(strings.TrimSpace(role))
		if role == "" {
			return
		}
		if _, exists := existingRoles[role]; exists {
			return
		}
		rec, ok := values[role]
		if !ok {
			return
		}
		recommendedModel := strings.TrimSpace(rec.Model)
		if recommendedModel == "" {
			return
		}
		if canonicalCatalogModelID(providerID, recommendedModel) != modelID && !strings.EqualFold(recommendedModel, catalogID) {
			return
		}
		existing = append(existing, pebblestore.ModelCatalogRecommendation{
			Role:     role,
			Thinking: strings.ToLower(strings.TrimSpace(rec.Thinking)),
			Serving:  strings.ToLower(strings.TrimSpace(firstNonEmpty(rec.Serving, rec.Fast))),
			Notes:    strings.TrimSpace(rec.Notes),
		})
		existingRoles[role] = struct{}{}
	}
	for _, role := range []string{"main", "auto", "plan", "utility"} {
		appendRole(role)
	}
	for role := range values {
		appendRole(role)
	}
	return existing
}

func modelRecommendations(values []swarmSnapshotRecommendation) []pebblestore.ModelCatalogRecommendation {
	if len(values) == 0 {
		return nil
	}
	out := make([]pebblestore.ModelCatalogRecommendation, 0, len(values))
	for _, value := range values {
		role := strings.ToLower(strings.TrimSpace(value.Role))
		if role == "" {
			continue
		}
		out = append(out, pebblestore.ModelCatalogRecommendation{
			Role:     role,
			Mode:     strings.ToLower(strings.TrimSpace(value.Mode)),
			Thinking: strings.ToLower(strings.TrimSpace(value.Thinking)),
			Serving:  strings.ToLower(strings.TrimSpace(value.Serving)),
			Notes:    strings.TrimSpace(value.Notes),
		})
	}
	return out
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
		if copy.Supported != nil && !*copy.Supported {
			return
		}
		switch strings.ToLower(strings.TrimSpace(copy.Status)) {
		case "deprecated", "disabled", "removed", "retired", "unavailable":
			return
		}
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
	addRawTier("fast", providerSpecific.Serving.FastMode)
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
	if providerHidesAsyncBatchTier(providerID) {
		order = removeCatalogString(order, "batch")
		delete(rawByTier, "batch")
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
			BetaHeader:        strings.TrimSpace(raw.BetaHeader),
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

func removeCatalogString(values []string, remove string) []string {
	remove = strings.ToLower(strings.TrimSpace(remove))
	out := values[:0]
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), remove) {
			continue
		}
		out = append(out, value)
	}
	return out
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
		MaterializationVersion: catalogMaterializationVersion,
		Source:                 source,
		SourceURL:              sourceURL,
		SnapshotURL:            sourceURL,
		VersionURL:             versionURL,
		ETag:                   strings.TrimSpace(etag),
		VersionETag:            strings.TrimSpace(versionETag),
		SnapshotID:             snapshot.SnapshotID,
		SnapshotVersion:        snapshot.SnapshotVersion,
		SnapshotSchemaVersion:  snapshot.SnapshotSchemaVersion,
		GeneratedAt:            snapshot.GeneratedAt,
		FetchedAt:              nowMs,
		LastCheckedAt:          nowMs,
		ExpiresAt:              expiresAt,
		RecordCount:            recordCount,
		ModelCount:             snapshot.ModelCount,
		ProviderCount:          snapshot.ProviderCount,
		HydratedProviderCount:  snapshot.HydratedProviderCount,
		LastRefreshReason:      reason,
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
	if sameCatalogSnapshot(meta, pinned) {
		return meta.MaterializationVersion != catalogMaterializationVersion
	}
	if meta.Source == catalogSourcePinned {
		return true
	}
	if !catalogMetaPinnedMetadataNeedsUpdate(meta) {
		return false
	}
	return !catalogMetaGeneratedAfter(meta, pinned)
}

func catalogMetaPinnedMetadataNeedsUpdate(meta pebblestore.ModelCatalogMeta) bool {
	pinned, ok := pinnedSnapshotVersionMetadata()
	if !ok {
		return false
	}
	return strings.TrimSpace(meta.PinnedSnapshotID) != strings.TrimSpace(pinned.SnapshotID) ||
		strings.TrimSpace(meta.PinnedSnapshotVersion) != strings.TrimSpace(pinned.SnapshotVersion) ||
		strings.TrimSpace(meta.PinnedGeneratedAt) != strings.TrimSpace(pinned.GeneratedAt)
}

func catalogMetaGeneratedAfter(meta pebblestore.ModelCatalogMeta, version swarmSnapshotVersion) bool {
	metaGeneratedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(meta.GeneratedAt))
	if err != nil {
		return false
	}
	versionGeneratedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(version.GeneratedAt))
	if err != nil {
		return false
	}
	return metaGeneratedAt.After(versionGeneratedAt)
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
