package pebblestore

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

type ModelCatalogRecord struct {
	Provider              string          `json:"provider"`
	ProviderDisplayName   string          `json:"provider_display_name,omitempty"`
	Model                 string          `json:"model"`
	DisplayName           string          `json:"display_name,omitempty"`
	CatalogID             string          `json:"catalog_id,omitempty"`
	ContextWindow         int             `json:"context_window"`
	MaxOutputTokens       int             `json:"max_output_tokens"`
	Reasoning             bool            `json:"reasoning"`
	ServiceTiers          []string        `json:"service_tiers,omitempty"`
	DefaultServiceTier    string          `json:"default_service_tier,omitempty"`
	Source                string          `json:"source"`
	SourceSnapshotID      string          `json:"source_snapshot_id,omitempty"`
	SourceSnapshotVersion string          `json:"source_snapshot_version,omitempty"`
	SourceGeneratedAt     string          `json:"source_generated_at,omitempty"`
	ETag                  string          `json:"etag,omitempty"`
	FetchedAt             int64           `json:"fetched_at"`
	ExpiresAt             int64           `json:"expires_at"`
	Pricing               json.RawMessage `json:"pricing,omitempty"`
	Thinking              json.RawMessage `json:"thinking,omitempty"`
	ProviderSpecific      json.RawMessage `json:"provider_specific,omitempty"`
}

type ModelCatalogMeta struct {
	Source                string `json:"source,omitempty"`
	SourceURL             string `json:"source_url"`
	VersionURL            string `json:"version_url,omitempty"`
	SnapshotURL           string `json:"snapshot_url,omitempty"`
	ETag                  string `json:"etag,omitempty"`
	VersionETag           string `json:"version_etag,omitempty"`
	SnapshotID            string `json:"snapshot_id,omitempty"`
	SnapshotVersion       string `json:"snapshot_version,omitempty"`
	SnapshotSchemaVersion string `json:"snapshot_schema_version,omitempty"`
	GeneratedAt           string `json:"generated_at,omitempty"`
	FetchedAt             int64  `json:"fetched_at"`
	LastCheckedAt         int64  `json:"last_checked_at,omitempty"`
	ExpiresAt             int64  `json:"expires_at"`
	LastError             string `json:"last_error,omitempty"`
	LastErrorAt           int64  `json:"last_error_at,omitempty"`
	LastRefreshReason     string `json:"last_refresh_reason,omitempty"`
	RecordCount           int    `json:"record_count"`
	ModelCount            int    `json:"model_count,omitempty"`
	ProviderCount         int    `json:"provider_count,omitempty"`
	HydratedProviderCount int    `json:"hydrated_provider_count,omitempty"`
	PinnedSnapshotID      string `json:"pinned_snapshot_id,omitempty"`
	PinnedSnapshotVersion string `json:"pinned_snapshot_version,omitempty"`
	PinnedGeneratedAt     string `json:"pinned_generated_at,omitempty"`
	LiveSnapshotID        string `json:"live_snapshot_id,omitempty"`
	LiveSnapshotVersion   string `json:"live_snapshot_version,omitempty"`
	LiveGeneratedAt       string `json:"live_generated_at,omitempty"`
	LiveCheckedAt         int64  `json:"live_checked_at,omitempty"`
	UsingCacheFallback    bool   `json:"using_cache_fallback,omitempty"`
}

type ModelCatalogStore struct {
	store *Store
}

func NewModelCatalogStore(store *Store) *ModelCatalogStore {
	return &ModelCatalogStore{store: store}
}

func (s *ModelCatalogStore) SetRecord(record ModelCatalogRecord) error {
	if strings.TrimSpace(record.Provider) == "" {
		return fmt.Errorf("model catalog provider is required")
	}
	if strings.TrimSpace(record.Model) == "" {
		return fmt.Errorf("model catalog model is required")
	}
	return s.store.PutJSON(KeyModelCatalog(record.Provider, record.Model), record)
}

func (s *ModelCatalogStore) SetRecords(records []ModelCatalogRecord) error {
	for i := range records {
		if err := s.SetRecord(records[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *ModelCatalogStore) ReplaceSnapshot(records []ModelCatalogRecord, meta ModelCatalogMeta) error {
	if len(records) == 0 {
		return fmt.Errorf("model catalog snapshot is empty")
	}

	batch := s.store.NewBatch()
	defer batch.Close()

	iter, err := s.store.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("model_catalog/"),
		UpperBound: []byte("model_catalog/\xff"),
	})
	if err != nil {
		return fmt.Errorf("create model catalog iterator: %w", err)
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := append([]byte(nil), iter.Key()...)
		if err := batch.Delete(key, nil); err != nil {
			return fmt.Errorf("delete stale model catalog key %q: %w", string(key), err)
		}
	}
	if err := iter.Error(); err != nil {
		return fmt.Errorf("iterate model catalog keys: %w", err)
	}

	for _, record := range records {
		if strings.TrimSpace(record.Provider) == "" {
			return fmt.Errorf("model catalog provider is required")
		}
		if strings.TrimSpace(record.Model) == "" {
			return fmt.Errorf("model catalog model is required")
		}
		payload, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("marshal model catalog record %q/%q: %w", record.Provider, record.Model, err)
		}
		if err := batch.Set([]byte(KeyModelCatalog(record.Provider, record.Model)), payload, nil); err != nil {
			return fmt.Errorf("set model catalog record %q/%q: %w", record.Provider, record.Model, err)
		}
	}

	metaPayload, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal model catalog meta: %w", err)
	}
	if err := batch.Set([]byte(KeyModelCatalogMeta), metaPayload, nil); err != nil {
		return fmt.Errorf("set model catalog meta: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("commit model catalog snapshot: %w", err)
	}
	return nil
}

func (s *ModelCatalogStore) GetRecord(providerID, modelID string) (ModelCatalogRecord, bool, error) {
	var record ModelCatalogRecord
	ok, err := s.store.GetJSON(KeyModelCatalog(providerID, modelID), &record)
	if err != nil {
		return ModelCatalogRecord{}, false, err
	}
	if !ok {
		return ModelCatalogRecord{}, false, nil
	}
	return record, true, nil
}

func (s *ModelCatalogStore) ListProvider(providerID string, limit int) ([]ModelCatalogRecord, error) {
	out := make([]ModelCatalogRecord, 0, 32)
	err := s.store.IteratePrefix(ModelCatalogPrefix(providerID), limit, func(_ string, value []byte) error {
		var record ModelCatalogRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		out = append(out, record)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ModelCatalogStore) SetMeta(meta ModelCatalogMeta) error {
	return s.store.PutJSON(KeyModelCatalogMeta, meta)
}

func (s *ModelCatalogStore) GetMeta() (ModelCatalogMeta, bool, error) {
	var meta ModelCatalogMeta
	ok, err := s.store.GetJSON(KeyModelCatalogMeta, &meta)
	if err != nil {
		return ModelCatalogMeta{}, false, err
	}
	if !ok {
		return ModelCatalogMeta{}, false, nil
	}
	return meta, true, nil
}
