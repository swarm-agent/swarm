package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Restore is explicit and separate from both catalog refresh and legacy profile defaults.
func (s *Server) handleRestoreAgentModelDefaults(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.agentModelSettingsContext(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var input struct {
		ExpectedUpdatedAt int64 `json:"expected_updated_at"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	current, err := s.agentModelSettings.Get(ctx)
	if err != nil {
		writeAgentModelSettingsError(w, err)
		return
	}
	if input.ExpectedUpdatedAt != current.UpdatedAt {
		writeError(w, http.StatusConflict, pebblestore.ErrAgentModelSettingsStale)
		return
	}
	if s.model == nil || s.providers == nil || s.agentModelSettingsStore == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("model defaults are not configured"))
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := s.model.CheckCatalog(ctx); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	statuses, err := s.providers.ListStatuses(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	ready := map[string]bool{}
	for _, status := range statuses {
		ready[strings.ToLower(status.ID)] = status.Ready
	}
	before, found, err := s.model.CatalogMeta()
	if err != nil || !found {
		writeError(w, http.StatusServiceUnavailable, errors.New("verified catalog unavailable"))
		return
	}
	replacement := current
	targets := []struct {
		role       string
		assignment *pebblestore.AgentModelAssignment
	}{
		{"auto", &replacement.Swarm.Action}, {"plan", &replacement.Swarm.Plan},
		{"compact", &replacement.SystemAgents.Compact}, {"finder", &replacement.SystemAgents.Finder},
		{"coder", &replacement.SystemAgents.Coder}, {"designer", &replacement.SystemAgents.Designer}, {"router", &replacement.SystemAgents.Router},
	}
	for _, target := range targets {
		provider := target.assignment.Provider
		if !ready[provider] {
			writeError(w, http.StatusConflict, fmt.Errorf("provider %q is not ready for this account", provider))
			return
		}
		records, ok, err := s.model.RecommendedCatalogRoleDefaults(provider, target.role)
		if err != nil || !ok {
			writeError(w, http.StatusConflict, fmt.Errorf("required %s recommendation unavailable for %q", target.role, provider))
			return
		}
		record := records[target.role]
		rec := recommendationForRole(record, target.role, "main")
		*target.assignment = pebblestore.AgentModelAssignment{Provider: provider, Model: record.Model, Thinking: recommendedThinking(rec, ""), ServiceTier: recommendedServiceTier(rec)}
	}
	after, found, err := s.model.CatalogMeta()
	if err != nil || !found || before.SnapshotID != after.SnapshotID || before.SnapshotVersion != after.SnapshotVersion || before.FetchedAt != after.FetchedAt {
		writeError(w, http.StatusConflict, errors.New("catalog changed while resolving defaults; retry"))
		return
	}
	principal, _ := identity.PrincipalFromContext(ctx)
	replacement.AccountScopeID = principal.AccountScopeID
	replacement.UpdatedAt = time.Now().UnixMilli()
	restored, err := s.agentModelSettingsStore.RestoreForAccount(current, replacement)
	if errors.Is(err, pebblestore.ErrAgentModelSettingsStale) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeAgentModelSettingsError(w, err)
		return
	}
	writeAgentModelSettingsResponse(w, restored)
}
