package pebblestore

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type ModelFavoriteRecord struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Label     string `json:"label,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type ModelFavoriteStore struct {
	store *Store
}

func NewModelFavoriteStore(store *Store) *ModelFavoriteStore {
	return &ModelFavoriteStore{store: store}
}

func (s *ModelFavoriteStore) Upsert(record ModelFavoriteRecord) (ModelFavoriteRecord, error) {
	provider := normalizeFavoritePart(record.Provider)
	model := strings.TrimSpace(record.Model)
	if provider == "" {
		return ModelFavoriteRecord{}, fmt.Errorf("favorite provider is required")
	}
	if model == "" {
		return ModelFavoriteRecord{}, fmt.Errorf("favorite model is required")
	}

	key := KeyModelFavorite(provider, model)
	now := time.Now().UnixMilli()

	existing, ok, err := s.Get(provider, model)
	if err != nil {
		return ModelFavoriteRecord{}, err
	}

	next := ModelFavoriteRecord{
		Provider:  provider,
		Model:     model,
		Label:     strings.TrimSpace(record.Label),
		Thinking:  normalizeThinking(record.Thinking),
		UpdatedAt: now,
	}
	if ok {
		next.CreatedAt = existing.CreatedAt
		if next.Label == "" {
			next.Label = existing.Label
		}
		if next.Thinking == "" {
			next.Thinking = existing.Thinking
		}
	} else {
		next.CreatedAt = now
	}

	if err := s.store.PutJSON(key, next); err != nil {
		return ModelFavoriteRecord{}, err
	}
	return next, nil
}

func (s *ModelFavoriteStore) Get(providerID, modelID string) (ModelFavoriteRecord, bool, error) {
	provider := normalizeFavoritePart(providerID)
	model := strings.TrimSpace(modelID)
	if provider == "" || model == "" {
		return ModelFavoriteRecord{}, false, nil
	}
	var out ModelFavoriteRecord
	ok, err := s.store.GetJSON(KeyModelFavorite(provider, model), &out)
	if err != nil || !ok {
		return ModelFavoriteRecord{}, ok, err
	}
	out.Provider = provider
	out.Model = model
	return normalizeFavoriteRecord(out), true, nil
}

func (s *ModelFavoriteStore) Delete(providerID, modelID string) (bool, error) {
	provider := normalizeFavoritePart(providerID)
	model := strings.TrimSpace(modelID)
	if provider == "" || model == "" {
		return false, nil
	}
	_, ok, err := s.Get(provider, model)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if err := s.store.Delete(KeyModelFavorite(provider, model)); err != nil {
		return false, err
	}
	return true, nil
}

func (s *ModelFavoriteStore) List(providerID string, limit int) ([]ModelFavoriteRecord, error) {
	provider := normalizeFavoritePart(providerID)
	if limit <= 0 {
		limit = 500
	}
	prefix := ModelFavoritePrefix(provider)
	out := make([]ModelFavoriteRecord, 0, minFavorite(limit, 256))
	err := s.store.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
		var record ModelFavoriteRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		record = normalizeFavoriteRecord(record)
		if provider != "" && record.Provider != provider {
			return nil
		}
		out = append(out, record)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].Model < out[j].Model
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func normalizeFavoriteRecord(record ModelFavoriteRecord) ModelFavoriteRecord {
	record.Provider = normalizeFavoritePart(record.Provider)
	record.Model = strings.TrimSpace(record.Model)
	record.Label = strings.TrimSpace(record.Label)
	record.Thinking = normalizeThinking(record.Thinking)
	if record.UpdatedAt <= 0 {
		record.UpdatedAt = time.Now().UnixMilli()
	}
	if record.CreatedAt <= 0 {
		record.CreatedAt = record.UpdatedAt
	}
	return record
}

func normalizeFavoritePart(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeThinking(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "off", "low", "medium", "high", "xhigh":
		return value
	default:
		return ""
	}
}

func minFavorite(a, b int) int {
	if a < b {
		return a
	}
	return b
}
