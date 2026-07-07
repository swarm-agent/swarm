package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

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
	publish   func(pebblestore.EventEnvelope)
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

func (s *Service) SetEventPublisher(publish func(pebblestore.EventEnvelope)) {
	if s == nil {
		return
	}
	s.publish = publish
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
	return s.GetPreferenceForAccount("")
}

func (s *Service) GetPreferenceForAccount(accountScopeID string) (pebblestore.ModelPreference, error) {
	pref, _, err := s.store.GetPreferenceForAccount(accountScopeID)
	if err != nil {
		return pebblestore.ModelPreference{}, fmt.Errorf("read model preference: %w", err)
	}
	return pref, nil
}

func (s *Service) GetResolvedGlobalPreference() (ResolvedPreference, error) {
	return s.GetResolvedPreferenceForAccount("")
}

func (s *Service) GetResolvedPreferenceForAccount(accountScopeID string) (ResolvedPreference, error) {
	pref, err := s.GetPreferenceForAccount(accountScopeID)
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
	return s.SetPreferenceForAccount("", "", provider, modelName, thinking, codexRuntime...)
}

func (s *Service) SetPreferenceForAccount(accountScopeID, userID, provider, modelName, thinking string, codexRuntime ...string) (ResolvedPreference, *pebblestore.EventEnvelope, error) {
	provider = normalizeProviderID(provider)
	modelName = strings.TrimSpace(modelName)
	thinking = strings.ToLower(strings.TrimSpace(thinking))
	serviceTier := ""
	contextMode := ""
	if len(codexRuntime) > 0 {
		serviceTier = normalizeServiceTier(codexRuntime[0])
	}
	if len(codexRuntime) > 1 {
		contextMode = normalizeContextMode(codexRuntime[1])
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
	thinking = s.normalizeThinkingForModel(provider, modelName, thinking)
	if !s.supportsServiceTierRuntime(provider, modelName, serviceTier) {
		serviceTier = ""
	}
	contextMode = s.normalizeContextModeForModel(provider, modelName, contextMode)

	pref, err := s.store.SetPreferenceForAccount(strings.TrimSpace(accountScopeID), strings.TrimSpace(userID), provider, modelName, thinking, serviceTier, contextMode)
	if err != nil {
		return ResolvedPreference{}, nil, fmt.Errorf("persist model preference: %w", err)
	}

	payload, err := json.Marshal(pref)
	if err != nil {
		return ResolvedPreference{}, nil, fmt.Errorf("marshal model event payload: %w", err)
	}
	entityID := "global"
	if strings.TrimSpace(accountScopeID) != "" {
		entityID = "account:" + strings.TrimSpace(accountScopeID)
	}
	env, err := s.events.Append("system:model", "model.preference.updated", entityID, payload, "", "")
	if err != nil {
		return ResolvedPreference{}, nil, err
	}
	if s.publish != nil {
		s.publish(env)
	}

	resolved, err := s.ResolvePreference(pref)
	if err != nil {
		return ResolvedPreference{}, nil, err
	}
	return resolved, &env, nil
}

func (s *Service) ClearGlobalPreference() (ResolvedPreference, *pebblestore.EventEnvelope, error) {
	return s.ClearPreferenceForAccount("")
}

func (s *Service) ClearPreferenceForAccount(accountScopeID string) (ResolvedPreference, *pebblestore.EventEnvelope, error) {
	if err := s.store.ClearPreferenceForAccount(accountScopeID); err != nil {
		return ResolvedPreference{}, nil, fmt.Errorf("clear model preference: %w", err)
	}
	pref, err := s.GetPreferenceForAccount(accountScopeID)
	if err != nil {
		return ResolvedPreference{}, nil, err
	}
	payload, err := json.Marshal(pref)
	if err != nil {
		return ResolvedPreference{}, nil, fmt.Errorf("marshal cleared model event payload: %w", err)
	}
	entityID := "global"
	if strings.TrimSpace(accountScopeID) != "" {
		entityID = "account:" + strings.TrimSpace(accountScopeID)
	}
	env, err := s.events.Append("system:model", "model.preference.updated", entityID, payload, "", "")
	if err != nil {
		return ResolvedPreference{}, nil, err
	}
	if s.publish != nil {
		s.publish(env)
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
	pref.Thinking = normalizeThinking(pref.Thinking)
	pref.ServiceTier = normalizeServiceTier(pref.ServiceTier)
	pref.ContextMode = normalizeContextMode(pref.ContextMode)
	return pref
}

func normalizeServiceTier(serviceTier string) string {
	serviceTier = strings.ToLower(strings.TrimSpace(serviceTier))
	if serviceTier == "" || serviceTier == "standard" || serviceTier == "off" {
		return ""
	}
	return serviceTier
}

func normalizeServiceTierForProvider(providerID, serviceTier string) string {
	providerID = normalizeProviderID(providerID)
	serviceTier = normalizeServiceTier(serviceTier)
	if serviceTier == "" {
		return ""
	}
	switch providerID {
	case "anthropic":
		if serviceTier == "batch" {
			return ""
		}
		return serviceTier
	case "codex", "fireworks", "openai", "openrouter":
		return serviceTier
	default:
		return ""
	}
}

func NormalizeServiceTierForProvider(providerID, serviceTier string) string {
	return normalizeServiceTierForProvider(providerID, serviceTier)
}

func (s *Service) supportsServiceTierRuntime(provider, modelName, serviceTier string) bool {
	provider = normalizeProviderID(provider)
	serviceTier = normalizeServiceTier(serviceTier)
	if serviceTier == "" {
		return true
	}
	if s == nil || s.catalog == nil {
		return false
	}
	lookup, err := s.catalog.Get(provider, modelName)
	if err != nil || !lookup.Found {
		return false
	}
	return serviceTierListedForModel(serviceTier, lookup.Record)
}

func serviceTierListedForModel(serviceTier string, record pebblestore.ModelCatalogRecord) bool {
	serviceTier = normalizeServiceTier(serviceTier)
	if serviceTier == "" {
		return true
	}
	for _, supported := range record.ServiceTiers {
		if normalizeServiceTier(supported) == serviceTier {
			return true
		}
	}
	for _, mapping := range record.ServiceTierMappings {
		if normalizeServiceTier(mapping.Tier) == serviceTier || normalizeServiceTier(mapping.SwarmSetting) == serviceTier {
			return true
		}
	}
	return false
}

func normalizeContextMode(contextMode string) string {
	contextMode = strings.ToLower(strings.TrimSpace(contextMode))
	if contextMode == "" || contextMode == "default" || contextMode == "standard" || contextMode == "off" {
		return ""
	}
	return contextMode
}

func (s *Service) normalizeContextModeForModel(providerID, modelID, contextMode string) string {
	contextMode = normalizeContextMode(contextMode)
	if contextMode == "" {
		return ""
	}
	if s == nil || s.catalog == nil {
		return ""
	}
	lookup, err := s.catalog.Get(providerID, modelID)
	if err != nil || !lookup.Found {
		return ""
	}
	return contextModeForRecord(lookup.Record, contextMode)
}

func contextModeForRecord(record pebblestore.ModelCatalogRecord, contextMode string) string {
	contextMode = normalizeContextMode(contextMode)
	if contextMode == "" {
		return ""
	}
	for _, mode := range record.ContextModes {
		if normalizeContextMode(mode.Mode) == contextMode {
			return contextMode
		}
	}
	return ""
}

func contextWindowForMode(record pebblestore.ModelCatalogRecord, contextMode string) int {
	contextMode = contextModeForRecord(record, contextMode)
	if contextMode != "" {
		for _, mode := range record.ContextModes {
			if normalizeContextMode(mode.Mode) == contextMode && mode.ContextWindow > 0 {
				return mode.ContextWindow
			}
		}
	}
	return record.ContextWindow
}

func applyResolvedRuntimePreference(resolved ResolvedPreference) ResolvedPreference {
	resolved.Preference = normalizeRuntimePreference(resolved.Preference)
	if resolved.ContextWindow < 0 {
		resolved.ContextWindow = 0
	}
	return resolved
}

func normalizeThinking(thinking string) string {
	return strings.ToLower(strings.TrimSpace(thinking))
}

func normalizeThinkingForProvider(providerID, thinking string) string {
	_ = normalizeProviderID(providerID)
	return normalizeThinking(thinking)
}

func NormalizeThinkingForProvider(providerID, thinking string) string {
	return normalizeThinkingForProvider(providerID, thinking)
}

func (s *Service) normalizeThinkingForModel(providerID, modelID, thinking string) string {
	thinking = normalizeThinking(thinking)
	if s == nil || s.catalog == nil {
		return thinking
	}
	lookup, err := s.catalog.Get(providerID, modelID)
	if err != nil || !lookup.Found {
		return thinking
	}
	return normalizeThinkingForCatalogRecord(lookup.Record, thinking)
}

func normalizeThinkingForCatalogRecord(record pebblestore.ModelCatalogRecord, thinking string) string {
	thinking = normalizeThinking(thinking)
	if len(record.ThinkingOptions) == 0 {
		return thinking
	}
	for _, option := range record.ThinkingOptions {
		if strings.EqualFold(strings.TrimSpace(option), thinking) {
			return strings.ToLower(strings.TrimSpace(option))
		}
	}
	defaultThinking := strings.ToLower(strings.TrimSpace(record.DefaultThinking))
	if defaultThinking != "" {
		for _, option := range record.ThinkingOptions {
			if strings.EqualFold(strings.TrimSpace(option), defaultThinking) {
				return defaultThinking
			}
		}
	}
	for _, option := range record.ThinkingOptions {
		if normalized := strings.ToLower(strings.TrimSpace(option)); normalized != "" {
			return normalized
		}
	}
	return thinking
}

func NormalizeProviderID(providerID string) string {
	return normalizeProviderID(providerID)
}

func normalizeProviderID(providerID string) string {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	switch providerID {
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

func (s *Service) RecommendedCatalogDefaults(providerID string) (pebblestore.ModelCatalogRecord, pebblestore.ModelCatalogRecord, pebblestore.ModelCatalogRecord, bool, error) {
	if s.catalog == nil {
		return pebblestore.ModelCatalogRecord{}, pebblestore.ModelCatalogRecord{}, pebblestore.ModelCatalogRecord{}, false, nil
	}
	return s.catalog.RecommendedDefaults(providerID)
}

func (s *Service) RefreshCatalog(ctx context.Context) (CatalogRefreshResult, error) {
	if s.catalog == nil {
		return CatalogRefreshResult{}, errors.New("model catalog is not configured")
	}
	return s.catalog.Refresh(ctx)
}

func (s *Service) RefreshCatalogManual(ctx context.Context) (CatalogRefreshResult, error) {
	if s.catalog == nil {
		return CatalogRefreshResult{}, errors.New("model catalog is not configured")
	}
	return s.catalog.RefreshManual(ctx)
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
	s.catalog.StartAutoRefresh(ctx, time.Hour)
}

func (s *Service) ListFavorites(providerID, query string, limit int) ([]pebblestore.ModelFavoriteRecord, error) {
	return s.ListFavoritesForAccount("", providerID, query, limit)
}

func (s *Service) ListFavoritesForAccount(accountScopeID, providerID, query string, limit int) ([]pebblestore.ModelFavoriteRecord, error) {
	providerID = normalizeProviderID(providerID)
	if s.favorites == nil {
		return []pebblestore.ModelFavoriteRecord{}, nil
	}
	if limit <= 0 {
		limit = 500
	}
	records, err := s.favorites.ListForAccount(accountScopeID, providerID, maxInt(limit*4, 2000))
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
	return s.UpsertFavoriteForAccount("", "", providerID, modelID, label, thinking)
}

func (s *Service) UpsertFavoriteForAccount(accountScopeID, userID, providerID, modelID, label, thinking string) (pebblestore.ModelFavoriteRecord, *pebblestore.EventEnvelope, error) {
	providerID = normalizeProviderID(providerID)
	if s.favorites == nil {
		return pebblestore.ModelFavoriteRecord{}, nil, errors.New("model favorites are not configured")
	}
	record, err := s.favorites.UpsertForAccount(accountScopeID, userID, pebblestore.ModelFavoriteRecord{
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
	return s.DeleteFavoriteForAccount("", providerID, modelID)
}

func (s *Service) DeleteFavoriteForAccount(accountScopeID, providerID, modelID string) (bool, *pebblestore.EventEnvelope, error) {
	providerID = normalizeProviderID(providerID)
	if s.favorites == nil {
		return false, nil, nil
	}
	deleted, err := s.favorites.DeleteForAccount(accountScopeID, providerID, modelID)
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
		out.ContextWindow = contextWindowForMode(lookup.Record, out.Preference.ContextMode)
		out.MaxOutputTokens = lookup.Record.MaxOutputTokens
		out.CatalogSource = lookup.Record.Source
		out.CatalogFetched = lookup.Record.FetchedAt
		out.CatalogExpires = lookup.Record.ExpiresAt
		out.Preference.Thinking = normalizeThinkingForCatalogRecord(lookup.Record, out.Preference.Thinking)
	}
	if lookup.Found {
		out.Preference.ContextMode = contextModeForRecord(lookup.Record, out.Preference.ContextMode)
		out.ContextWindow = contextWindowForMode(lookup.Record, out.Preference.ContextMode)
	}
	if out.Preference.ServiceTier != "" && (!lookup.Found || !serviceTierListedForModel(out.Preference.ServiceTier, lookup.Record)) {
		out.Preference.ServiceTier = ""
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
