package model

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	codexruntime "swarm/packages/swarmd/internal/provider/codex"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	defaultCatalogURL          = "https://models.dev/api.json"
	defaultCatalogTTL          = 24 * time.Hour
	defaultCatalogFetchTimeout = 10 * time.Second
)

var catalogProviderCanonicalIDs = map[string]string{
	"openai":         "codex",
	"github-copilot": "copilot",
	"fireworks-ai":   "fireworks",
	"openrouter":     "openrouter",
}

type CatalogService struct {
	store     *pebblestore.ModelCatalogStore
	client    *http.Client
	sourceURL string
	now       func() time.Time
	mu        sync.Mutex
}

type CatalogLookup struct {
	Record pebblestore.ModelCatalogRecord `json:"record"`
	Found  bool                           `json:"found"`
	Stale  bool                           `json:"stale"`
}

type CatalogRefreshResult struct {
	SourceURL   string `json:"source_url"`
	ETag        string `json:"etag,omitempty"`
	FetchedAt   int64  `json:"fetched_at"`
	ExpiresAt   int64  `json:"expires_at"`
	RecordCount int    `json:"record_count"`
	NotModified bool   `json:"not_modified"`
	UsedCache   bool   `json:"used_cache"`
}

type modelsDevProvider struct {
	ID     string                    `json:"id"`
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	Reasoning bool `json:"reasoning"`
	Limit     struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
}

func NewCatalogService(store *pebblestore.ModelCatalogStore) *CatalogService {
	return &CatalogService{
		store:     store,
		sourceURL: defaultCatalogURL,
		now:       time.Now,
		client: &http.Client{
			Timeout: defaultCatalogFetchTimeout,
		},
	}
}

func (s *CatalogService) EnsureBootDefaults() error {
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
	defaultRecord := pebblestore.ModelCatalogRecord{
		Provider:        providerID,
		Model:           modelID,
		ContextWindow:   200000,
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

func (s *CatalogService) Get(providerID, modelID string) (CatalogLookup, error) {
	normalizedProvider := canonicalCatalogProviderID(providerID)
	normalizedModel := strings.TrimSpace(modelID)
	record, ok, err := s.store.GetRecord(normalizedProvider, normalizedModel)
	if err != nil {
		return CatalogLookup{}, err
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
	records, err := s.store.ListProvider(providerID, limit)
	if err != nil {
		return nil, err
	}
	return records, nil
}

func (s *CatalogService) Meta() (pebblestore.ModelCatalogMeta, bool, error) {
	return s.store.GetMeta()
}

func (s *CatalogService) Refresh(ctx context.Context) (CatalogRefreshResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	meta, _, err := s.store.GetMeta()
	if err != nil {
		return CatalogRefreshResult{}, fmt.Errorf("read model catalog meta: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.sourceURL, nil)
	if err != nil {
		return CatalogRefreshResult{}, err
	}
	if strings.TrimSpace(meta.ETag) != "" {
		req.Header.Set("If-None-Match", meta.ETag)
	}
	req.Header.Set("User-Agent", "swarmd/0")

	resp, err := s.client.Do(req)
	if err != nil {
		nowMs := s.now().UnixMilli()
		meta.SourceURL = s.sourceURL
		meta.LastError = err.Error()
		meta.LastErrorAt = nowMs
		_ = s.store.SetMeta(meta)
		return CatalogRefreshResult{
			SourceURL:   s.sourceURL,
			FetchedAt:   meta.FetchedAt,
			ExpiresAt:   meta.ExpiresAt,
			RecordCount: meta.RecordCount,
			UsedCache:   meta.RecordCount > 0,
		}, fmt.Errorf("fetch model catalog: %w", err)
	}
	defer resp.Body.Close()

	now := s.now()
	nowMs := now.UnixMilli()
	expiresAt := now.Add(defaultCatalogTTL).UnixMilli()
	if resp.StatusCode == http.StatusNotModified {
		meta.SourceURL = s.sourceURL
		meta.FetchedAt = nowMs
		meta.ExpiresAt = expiresAt
		meta.LastError = ""
		meta.LastErrorAt = 0
		if err := s.store.SetMeta(meta); err != nil {
			return CatalogRefreshResult{}, fmt.Errorf("persist model catalog meta: %w", err)
		}
		return CatalogRefreshResult{
			SourceURL:   s.sourceURL,
			ETag:        meta.ETag,
			FetchedAt:   meta.FetchedAt,
			ExpiresAt:   meta.ExpiresAt,
			RecordCount: meta.RecordCount,
			NotModified: true,
		}, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return CatalogRefreshResult{}, fmt.Errorf("models.dev returned status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return CatalogRefreshResult{}, fmt.Errorf("read model catalog response: %w", err)
	}

	records, err := decodeModelsDevRecords(body)
	if err != nil {
		return CatalogRefreshResult{}, err
	}
	if len(records) == 0 {
		return CatalogRefreshResult{}, fmt.Errorf("models.dev returned an empty model catalog snapshot")
	}

	newMeta := pebblestore.ModelCatalogMeta{
		SourceURL:   s.sourceURL,
		ETag:        strings.TrimSpace(resp.Header.Get("ETag")),
		FetchedAt:   nowMs,
		ExpiresAt:   expiresAt,
		RecordCount: len(records),
	}
	if err := s.store.ReplaceSnapshot(records, newMeta); err != nil {
		return CatalogRefreshResult{}, fmt.Errorf("persist model catalog snapshot: %w", err)
	}

	return CatalogRefreshResult{
		SourceURL:   newMeta.SourceURL,
		ETag:        newMeta.ETag,
		FetchedAt:   newMeta.FetchedAt,
		ExpiresAt:   newMeta.ExpiresAt,
		RecordCount: newMeta.RecordCount,
	}, nil
}

func (s *CatalogService) StartAutoRefresh(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = defaultCatalogTTL
	}
	go func() {
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

func decodeModelsDevRecords(payload []byte) ([]pebblestore.ModelCatalogRecord, error) {
	var providers map[string]modelsDevProvider
	if err := json.Unmarshal(payload, &providers); err != nil {
		return nil, fmt.Errorf("decode models.dev payload: %w", err)
	}

	nowMs := time.Now().UnixMilli()
	expiresAt := time.Now().Add(defaultCatalogTTL).UnixMilli()
	records := make([]pebblestore.ModelCatalogRecord, 0, 2048)
	for providerKey, provider := range providers {
		providerID := strings.TrimSpace(provider.ID)
		if providerID == "" {
			providerID = strings.TrimSpace(providerKey)
		}
		providerID = canonicalCatalogProviderID(providerID)
		if providerID == "" {
			continue
		}
		for modelID, model := range provider.Models {
			modelID = strings.TrimSpace(modelID)
			if modelID == "" || model.Limit.Context <= 0 {
				continue
			}
			record := pebblestore.ModelCatalogRecord{
				Provider:        providerID,
				Model:           modelID,
				ContextWindow:   codexruntime.EffectiveContextWindow(modelID, "", model.Limit.Context),
				MaxOutputTokens: model.Limit.Output,
				Reasoning:       model.Reasoning,
				Source:          "models.dev",
				FetchedAt:       nowMs,
				ExpiresAt:       expiresAt,
			}
			records = append(records, record)
		}
	}
	return records, nil
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
