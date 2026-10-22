package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// setUpRecommendedAgentModels answers "use recommended models" on an account
// that has none yet. Settings are normally created when a provider credential
// is verified on save; a credential saved without that (an unverified key, or
// a failed first attempt) left the account with a connected provider and no
// way to pick models. Set them up from the first ready provider whose catalog
// has recommendations, the same way a verified first credential does; with
// none, say a provider must be connected first.
type recommendedAgentModelsError struct {
	status int
	err    error
}

func (e *recommendedAgentModelsError) Error() string { return e.err.Error() }
func (e *recommendedAgentModelsError) Unwrap() error { return e.err }

var errNoReadyModelProvider = &recommendedAgentModelsError{http.StatusConflict, errors.New("connect a model provider first: none is ready for this account")}

func (s *Server) setUpRecommendedAgentModels(w http.ResponseWriter, ctx context.Context) {
	settings, err := s.ensureRecommendedAgentModels(ctx)
	if err != nil {
		writeAgentModelSettingsError(w, err)
		return
	}
	writeAgentModelSettingsResponse(w, settings)
}

func (s *Server) ensureRecommendedAgentModels(ctx context.Context) (agentmodelsettings.Settings, error) {
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok || strings.TrimSpace(principal.AccountScopeID) == "" {
		return agentmodelsettings.Settings{}, identity.ErrProductIdentityRequired
	}
	if existing, err := s.agentModelSettings.Get(ctx); err == nil {
		return existing, nil
	} else if !errors.Is(err, agentmodelsettings.ErrNotFound) && !errors.Is(err, pebblestore.ErrAgentModelSettingsNotFound) {
		return agentmodelsettings.Settings{}, err
	}
	if s.providers == nil {
		return agentmodelsettings.Settings{}, &recommendedAgentModelsError{http.StatusServiceUnavailable, errors.New("model defaults are not configured")}
	}
	statuses, err := s.providers.ListStatuses(ctx)
	if err != nil {
		return agentmodelsettings.Settings{}, &recommendedAgentModelsError{http.StatusBadGateway, err}
	}
	var failures []string
	for _, status := range statuses {
		if !status.Ready {
			continue
		}
		if _, err := s.hydrateOnboardingProviderDefaultsAfterVerifiedCredentialActivationForAccount(principal.AccountScopeID, principal.UserID, status.ID); err != nil {
			failures = append(failures, status.ID+": "+err.Error())
			continue
		}
		return s.agentModelSettings.Get(ctx)
	}
	if len(failures) == 0 {
		return agentmodelsettings.Settings{}, errNoReadyModelProvider
	}
	return agentmodelsettings.Settings{}, &recommendedAgentModelsError{http.StatusConflict, fmt.Errorf("no connected provider has recommended models: %s", strings.Join(failures, "; "))}
}

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
	if errors.Is(err, agentmodelsettings.ErrNotFound) || errors.Is(err, pebblestore.ErrAgentModelSettingsNotFound) {
		s.setUpRecommendedAgentModels(w, ctx)
		return
	}
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
	replacement.SystemAgents.Reviewer = replacement.SystemAgents.Coder
	reviews, hasReview, reviewErr := s.model.RecommendedCatalogRoleDefaults(replacement.SystemAgents.Coder.Provider, "review")
	if reviewErr != nil {
		writeError(w, http.StatusConflict, reviewErr)
		return
	}
	if hasReview {
		record := reviews["review"]
		rec := recommendationForRole(record, "review")
		replacement.SystemAgents.Reviewer = pebblestore.AgentModelAssignment{Provider: replacement.SystemAgents.Coder.Provider, Model: record.Model, Thinking: recommendedThinking(rec, ""), ServiceTier: recommendedServiceTier(rec)}
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
