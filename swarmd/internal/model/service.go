package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	codexruntime "swarm/packages/swarmd/internal/provider/codex"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var allowedThinkingLevels = map[string]struct{}{
	"off":    {},
	"low":    {},
	"medium": {},
	"high":   {},
	"xhigh":  {},
}

type Service struct {
	store     *pebblestore.ModelStore
	events    *pebblestore.EventLog
	catalog   *CatalogService
	favorites *pebblestore.ModelFavoriteStore
}

type ResolvedPreference struct {
	Preference      pebblestore.ModelPreference   `json:"preference"`
	ContextWindow   int                           `json:"context_window"`
	MaxOutputTokens int                           `json:"max_output_tokens"`
	CatalogSource   string                        `json:"catalog_source,omitempty"`
	CatalogFetched  int64                         `json:"catalog_fetched_at,omitempty"`
	CatalogExpires  int64                         `json:"catalog_expires_at,omitempty"`
	CatalogStale    bool                          `json:"catalog_stale"`
	CatalogPresent  bool                          `json:"catalog_present"`
	CatalogMeta     *pebblestore.ModelCatalogMeta `json:"catalog_meta,omitempty"`
}

func NewService(store *pebblestore.ModelStore, events *pebblestore.EventLog, catalog *CatalogService) *Service {
	return &Service{store: store, events: events, catalog: catalog}
}

func NewServiceWithFavorites(store *pebblestore.ModelStore, events *pebblestore.EventLog, catalog *CatalogService, favorites *pebblestore.ModelFavoriteStore) *Service {
	return &Service{
		store:     store,
		events:    events,
		catalog:   catalog,
		favorites: favorites,
	}
}

func (s *Service) EnsureBootDefaults() error {
	if s.catalog != nil {
		if err := s.catalog.EnsureBootDefaults(); err != nil {
			return err
		}
	}
	if _, err := s.GetGlobalPreference(); err != nil {
		return err
	}
	return nil
}

func (s *Service) GetGlobalPreference() (pebblestore.ModelPreference, error) {
	pref, _, err := s.store.GetGlobalPreference()
	if err != nil {
		return pebblestore.ModelPreference{}, fmt.Errorf("read model preference: %w", err)
	}
	return pref, nil
}

func (s *Service) GetResolvedGlobalPreference() (ResolvedPreference, error) {
	pref, err := s.GetGlobalPreference()
	if err != nil {
		return ResolvedPreference{}, err
	}
	return s.ResolvePreference(pref)
}

func (s *Service) ResolvePreference(pref pebblestore.ModelPreference) (ResolvedPreference, error) {
	pref = normalizeRuntimePreference(pref)
	resolved, err := s.resolvePreference(pref)
	if err != nil {
		return ResolvedPreference{}, err
	}
	return applyResolvedRuntimePreference(resolved), nil
}

func (s *Service) SetGlobalPreference(provider, modelName, thinking string, codexRuntime ...string) (ResolvedPreference, *pebblestore.EventEnvelope, error) {
	provider = normalizeProviderID(provider)
	modelName = strings.TrimSpace(modelName)
	thinking = strings.ToLower(strings.TrimSpace(thinking))
	serviceTier := ""
	contextMode := ""
	if len(codexRuntime) > 0 {
		serviceTier = codexruntime.NormalizeServiceTier(codexRuntime[0])
	}
	if len(codexRuntime) > 1 {
		contextMode = codexruntime.NormalizeContextMode(codexRuntime[1])
	}

	if provider == "" {
		return ResolvedPreference{}, nil, errors.New("provider is required")
	}
	if modelName == "" {
		return ResolvedPreference{}, nil, errors.New("model is required")
	}
	if _, ok := allowedThinkingLevels[thinking]; !ok {
		return ResolvedPreference{}, nil, fmt.Errorf("invalid thinking level %q", thinking)
	}
	thinking = normalizeThinkingForProvider(provider, thinking)
	if !strings.EqualFold(provider, "codex") || !strings.EqualFold(modelName, "gpt-5.4") {
		serviceTier = ""
	}
	if !strings.EqualFold(provider, "codex") || (!strings.EqualFold(modelName, "gpt-5.4") && !strings.EqualFold(modelName, "gpt-5.5")) {
		contextMode = ""
	}

	pref, err := s.store.SetGlobalPreference(provider, modelName, thinking, serviceTier, contextMode)
	if err != nil {
		return ResolvedPreference{}, nil, fmt.Errorf("persist model preference: %w", err)
	}

	payload, err := json.Marshal(pref)
	if err != nil {
		return ResolvedPreference{}, nil, fmt.Errorf("marshal model event payload: %w", err)
	}
	env, err := s.events.Append("system:model", "model.preference.updated", "global", payload, "", "")
	if err != nil {
		return ResolvedPreference{}, nil, err
	}

	resolved, err := s.ResolvePreference(pref)
	if err != nil {
		return ResolvedPreference{}, nil, err
	}
	return resolved, &env, nil
}

func (s *Service) ClearGlobalPreference() (ResolvedPreference, *pebblestore.EventEnvelope, error) {
	if err := s.store.ClearGlobalPreference(); err != nil {
		return ResolvedPreference{}, nil, fmt.Errorf("clear model preference: %w", err)
	}
	pref, err := s.GetGlobalPreference()
	if err != nil {
		return ResolvedPreference{}, nil, err
	}
	payload, err := json.Marshal(pref)
	if err != nil {
		return ResolvedPreference{}, nil, fmt.Errorf("marshal cleared model event payload: %w", err)
	}
	env, err := s.events.Append("system:model", "model.preference.updated", "global", payload, "", "")
	if err != nil {
		return ResolvedPreference{}, nil, err
	}
	resolved, err := s.ResolvePreference(pref)
	if err != nil {
		return ResolvedPreference{}, nil, err
	}
	return resolved, &env, nil
}

func normalizeRuntimePreference(pref pebblestore.ModelPreference) pebblestore.ModelPreference {
	pref.Provider = normalizeProviderID(pref.Provider)
	pref.Model = strings.TrimSpace(pref.Model)
	pref.Thinking = normalizeThinkingForProvider(pref.Provider, pref.Thinking)
	pref.ServiceTier = codexruntime.NormalizeServiceTier(pref.ServiceTier)
	pref.ContextMode = codexruntime.NormalizeContextMode(pref.ContextMode)
	if !strings.EqualFold(pref.Provider, "codex") || !strings.EqualFold(pref.Model, "gpt-5.4") {
		pref.ServiceTier = ""
	}
	if !strings.EqualFold(pref.Provider, "codex") || (!strings.EqualFold(pref.Model, "gpt-5.4") && !strings.EqualFold(pref.Model, "gpt-5.5")) {
		pref.ContextMode = ""
	}
	return pref
}

func applyResolvedRuntimePreference(resolved ResolvedPreference) ResolvedPreference {
	resolved.Preference = normalizeRuntimePreference(resolved.Preference)
	if strings.EqualFold(resolved.Preference.Provider, "codex") {
		resolved.ContextWindow = codexruntime.EffectiveContextWindow(resolved.Preference.Model, resolved.Preference.ContextMode, resolved.ContextWindow)
	}
	if resolved.ContextWindow < 0 {
		resolved.ContextWindow = 0
	}
	return resolved
}

func normalizeThinkingForProvider(providerID, thinking string) string {
	providerID = normalizeProviderID(providerID)
	thinking = strings.ToLower(strings.TrimSpace(thinking))
	switch providerID {
	case "copilot":
		if thinking == "xhigh" {
			return "high"
		}
	case "fireworks":
		if thinking == "xhigh" {
			return "high"
		}
	case "openrouter":
		if thinking == "xhigh" {
			return "high"
		}
	}
	return thinking
}

func NormalizeThinkingForProvider(providerID, thinking string) string {
	return normalizeThinkingForProvider(providerID, thinking)
}

func NormalizeProviderID(providerID string) string {
	return normalizeProviderID(providerID)
}

func normalizeProviderID(providerID string) string {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	switch providerID {
	case "openai":
		return "codex"
	case "github-copilot":
		return "copilot"
	case "fireworks-ai":
		return "fireworks"
	default:
		return providerID
	}
}

func IsAllowedThinkingLevel(thinking string) bool {
	_, ok := allowedThinkingLevels[strings.ToLower(strings.TrimSpace(thinking))]
	return ok
}

func (s *Service) GetCatalog(providerID, modelID string) (CatalogLookup, error) {
	if s.catalog == nil {
		return CatalogLookup{}, errors.New("model catalog is not configured")
	}
	return s.catalog.Get(providerID, modelID)
}

func (s *Service) ListCatalog(providerID string, limit int) ([]pebblestore.ModelCatalogRecord, error) {
	if s.catalog == nil {
		return nil, errors.New("model catalog is not configured")
	}
	return s.catalog.List(providerID, limit)
}

func (s *Service) RefreshCatalog(ctx context.Context) (CatalogRefreshResult, error) {
	if s.catalog == nil {
		return CatalogRefreshResult{}, errors.New("model catalog is not configured")
	}
	return s.catalog.Refresh(ctx)
}

func (s *Service) CatalogMeta() (pebblestore.ModelCatalogMeta, bool, error) {
	if s.catalog == nil {
		return pebblestore.ModelCatalogMeta{}, false, nil
	}
	return s.catalog.Meta()
}

func (s *Service) StartCatalogAutoRefresh(ctx context.Context) {
	if s.catalog == nil {
		return
	}
	s.catalog.StartAutoRefresh(ctx, 24*time.Hour)
}

func (s *Service) ListFavorites(providerID, query string, limit int) ([]pebblestore.ModelFavoriteRecord, error) {
	providerID = normalizeProviderID(providerID)
	if s.favorites == nil {
		return []pebblestore.ModelFavoriteRecord{}, nil
	}
	if limit <= 0 {
		limit = 500
	}
	records, err := s.favorites.List(providerID, maxInt(limit*4, 2000))
	if err != nil {
		return nil, fmt.Errorf("list model favorites: %w", err)
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		if len(records) > limit {
			return records[:limit], nil
		}
		return records, nil
	}
	out := make([]pebblestore.ModelFavoriteRecord, 0, len(records))
	for _, record := range records {
		if matchesFavoriteQuery(record, query) {
			out = append(out, record)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
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

func (s *Service) UpsertFavorite(providerID, modelID, label, thinking string) (pebblestore.ModelFavoriteRecord, *pebblestore.EventEnvelope, error) {
	providerID = normalizeProviderID(providerID)
	if s.favorites == nil {
		return pebblestore.ModelFavoriteRecord{}, nil, errors.New("model favorites are not configured")
	}
	record, err := s.favorites.Upsert(pebblestore.ModelFavoriteRecord{
		Provider: providerID,
		Model:    modelID,
		Label:    label,
		Thinking: thinking,
	})
	if err != nil {
		return pebblestore.ModelFavoriteRecord{}, nil, err
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return pebblestore.ModelFavoriteRecord{}, nil, fmt.Errorf("marshal model favorite upsert payload: %w", err)
	}
	env, err := s.events.Append("system:model", "model.favorite.upserted", favoriteEntityID(record.Provider, record.Model), payload, "", "")
	if err != nil {
		return pebblestore.ModelFavoriteRecord{}, nil, err
	}
	return record, &env, nil
}

func (s *Service) DeleteFavorite(providerID, modelID string) (bool, *pebblestore.EventEnvelope, error) {
	providerID = normalizeProviderID(providerID)
	if s.favorites == nil {
		return false, nil, nil
	}
	deleted, err := s.favorites.Delete(providerID, modelID)
	if err != nil {
		return false, nil, err
	}
	if !deleted {
		return false, nil, nil
	}
	payload, err := json.Marshal(map[string]string{
		"provider": strings.ToLower(strings.TrimSpace(providerID)),
		"model":    strings.TrimSpace(modelID),
	})
	if err != nil {
		return false, nil, fmt.Errorf("marshal model favorite delete payload: %w", err)
	}
	env, err := s.events.Append("system:model", "model.favorite.deleted", favoriteEntityID(providerID, modelID), payload, "", "")
	if err != nil {
		return false, nil, err
	}
	return true, &env, nil
}

func (s *Service) resolvePreference(pref pebblestore.ModelPreference) (ResolvedPreference, error) {
	out := ResolvedPreference{
		Preference: pref,
	}
	if s.catalog == nil {
		return out, nil
	}

	lookup, err := s.catalog.Get(pref.Provider, pref.Model)
	if err != nil {
		return ResolvedPreference{}, err
	}
	if lookup.Found {
		out.CatalogPresent = true
		out.CatalogStale = lookup.Stale
		out.ContextWindow = lookup.Record.ContextWindow
		out.MaxOutputTokens = lookup.Record.MaxOutputTokens
		out.CatalogSource = lookup.Record.Source
		out.CatalogFetched = lookup.Record.FetchedAt
		out.CatalogExpires = lookup.Record.ExpiresAt
	}
	meta, ok, err := s.catalog.Meta()
	if err != nil {
		return ResolvedPreference{}, err
	}
	if ok {
		out.CatalogMeta = &meta
	}
	return out, nil
}

func matchesFavoriteQuery(record pebblestore.ModelFavoriteRecord, query string) bool {
	if query == "" {
		return true
	}
	terms := strings.Fields(query)
	for _, term := range terms {
		if term == "" {
			continue
		}
		if strings.HasPrefix(term, "provider:") {
			needle := strings.TrimSpace(strings.TrimPrefix(term, "provider:"))
			if needle == "" || !strings.Contains(strings.ToLower(record.Provider), needle) {
				return false
			}
			continue
		}
		if strings.HasPrefix(term, "thinking:") {
			needle := strings.TrimSpace(strings.TrimPrefix(term, "thinking:"))
			if needle == "" || !strings.Contains(strings.ToLower(record.Thinking), needle) {
				return false
			}
			continue
		}
		if !strings.Contains(strings.ToLower(record.Provider), term) &&
			!strings.Contains(strings.ToLower(record.Model), term) &&
			!strings.Contains(strings.ToLower(record.Label), term) &&
			!strings.Contains(strings.ToLower(record.Thinking), term) {
			return false
		}
	}
	return true
}

func favoriteEntityID(providerID, modelID string) string {
	return strings.ToLower(strings.TrimSpace(providerID)) + "/" + strings.TrimSpace(modelID)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
