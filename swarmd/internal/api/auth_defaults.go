package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/modelprofile"
	"swarm/packages/swarmd/internal/provider/defaults"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
)

// hydrateOnboardingProviderDefaultsAfterVerifiedCredentialActivationForAccount has one job:
// after a provider credential has been verified and activated, create the
// account's built-in agents already hydrated with verified snapshot defaults.
func (s *Server) hydrateOnboardingProviderDefaultsAfterVerifiedCredentialActivationForAccount(accountScopeID, userID, activatedProvider string) (*auth.AutoDefaultsStatus, error) {
	if s == nil || s.model == nil || s.agents == nil || s.providers == nil || s.modelProfiles == nil || s.uiSettings == nil {
		return nil, errors.New("onboarding provider hydration is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account scope ID is required")
	}
	state, err := s.agents.ListStateForAccount(accountScopeID, 1)
	if err != nil {
		return nil, fmt.Errorf("read onboarding agent state: %w", err)
	}
	if len(state.Profiles) != 0 {
		// This initializer belongs only to the first credential path. Existing
		// accounts keep their established Swarm and subagent preferences.
		return nil, nil
	}

	providerID, providerDefaults, ok, err := s.resolveOnboardingModelProvider(activatedProvider)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("provider %q is not available for onboarding defaults", strings.TrimSpace(activatedProvider))
	}
	settings, err := s.uiSettings.GetForAccount(accountScopeID)
	if err != nil {
		return nil, fmt.Errorf("read onboarding system-agent model settings: %w", err)
	}
	if hasConfiguredOnboardingSubagentSettings(settings.Agents) {
		return nil, nil
	}
	_, event, err := s.model.SetPreferenceForAccount(accountScopeID, userID, providerID, providerDefaults.PrimaryModel, providerDefaults.PrimaryThinking)
	if err != nil {
		return nil, fmt.Errorf("set global model default: %w", err)
	}
	result, err := s.agents.EnsureHydratedDefaultsForAccount(accountScopeID, agentruntime.DefaultModelHydrationInput{
		Provider:        providerID,
		PrimaryModel:    providerDefaults.PrimaryModel,
		PrimaryThinking: providerDefaults.PrimaryThinking,
		PlanModel:       providerDefaults.PlanModel,
		PlanThinking:    providerDefaults.PlanThinking,
		AutoModel:       providerDefaults.AutoModel,
		AutoThinking:    providerDefaults.AutoThinking,
	})
	if err != nil {
		return nil, fmt.Errorf("create hydrated agent defaults: %w", err)
	}
	{
		recommended, ok, recommendationsErr := s.recommendedOnboardingSubagentSettings(providerID)
		if recommendationsErr != nil {
			return nil, recommendationsErr
		}
		if !ok {
			return nil, fmt.Errorf("missing required snapshot subagent recommendations for provider %q", providerID)
		}
		settings.Agents.Compact = recommended.Compact
		settings.Agents.Finder = recommended.Finder
		settings.Agents.Coder = recommended.Coder
		settings.Agents.Designer = recommended.Designer
		if _, settingsErr := s.uiSettings.SetForAccount(accountScopeID, settings); settingsErr != nil {
			return nil, fmt.Errorf("set onboarding system-agent model settings: %w", settingsErr)
		}
	}
	planSelection := modelprofile.Selection{Provider: providerID, Model: providerDefaults.PlanModel, Thinking: providerDefaults.PlanThinking}
	actionSelection := modelprofile.Selection{Provider: providerID, Model: providerDefaults.AutoModel, Thinking: providerDefaults.AutoThinking}
	if _, _, err := s.modelProfiles.CreateFirstForAccount(accountScopeID, modelprofile.Input{
		Name: "Swarm recommended", ModelMode: pebblestore.ModelProfileModeSplit,
		Plan: &planSelection, Auto: &actionSelection,
	}); err != nil {
		return nil, fmt.Errorf("create onboarding recommended model profile: %w", err)
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}

	return &auth.AutoDefaultsStatus{
		Applied:     true,
		Provider:    providerID,
		Model:       providerDefaults.PrimaryModel,
		Thinking:    providerDefaults.PrimaryThinking,
		GlobalModel: true,
		Agents:      result.Agents,
		Subagents:   []string{"compact", "finder", "coder", "designer"},
	}, nil
}

func (s *Server) refreshModelCatalogForOnboardingDefaults() error {
	if s == nil || s.model == nil {
		return nil
	}
	meta, ok, err := s.model.CatalogMeta()
	if err != nil {
		return fmt.Errorf("read model catalog metadata: %w", err)
	}
	if ok && strings.TrimSpace(meta.LiveSnapshotVersion) != "" {
		if meta.ExpiresAt <= 0 || meta.ExpiresAt > time.Now().UnixMilli() {
			return nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.model.RefreshCatalog(ctx); err != nil {
		return fmt.Errorf("refresh model catalog recommendations: %w", err)
	}
	return nil
}

func (s *Server) applySnapshotRecommendedDefaults(providerID string, providerDefaults *defaults.ProviderDefaults) error {
	return s.applySnapshotRecommendedDefaultsForMode(providerID, providerDefaults, false)
}

func (s *Server) applyRequiredSnapshotRecommendedDefaults(providerID string, providerDefaults *defaults.ProviderDefaults) error {
	return s.applySnapshotRecommendedDefaultsForMode(providerID, providerDefaults, true)
}

func (s *Server) applySnapshotRecommendedDefaultsForMode(providerID string, providerDefaults *defaults.ProviderDefaults, required bool) error {
	if s == nil || s.model == nil || providerDefaults == nil {
		if required {
			return errors.New("model service is required for onboarding recommendations")
		}
		return nil
	}
	main, plan, utility, ok, err := s.model.RecommendedCatalogDefaults(providerID)
	if err != nil {
		return fmt.Errorf("read model catalog recommendations: %w", err)
	}
	if !ok {
		if required {
			return fmt.Errorf("missing required snapshot recommendations for provider %q", strings.TrimSpace(providerID))
		}
		return nil
	}
	mainRec := recommendationForRole(main, "main", "auto")
	planRec := recommendationForRole(plan, "plan")
	utilityRec := recommendationForRole(utility, "utility")
	if strings.TrimSpace(main.Model) == "" || strings.TrimSpace(plan.Model) == "" || strings.TrimSpace(utility.Model) == "" || strings.TrimSpace(mainRec.Role) == "" || strings.TrimSpace(planRec.Role) == "" || strings.TrimSpace(utilityRec.Role) == "" {
		if required {
			return fmt.Errorf("missing required snapshot recommendation roles for provider %q", strings.TrimSpace(providerID))
		}
		return nil
	}
	providerDefaults.ProviderID = strings.ToLower(strings.TrimSpace(main.Provider))
	if providerDefaults.ProviderID == "" {
		providerDefaults.ProviderID = strings.ToLower(strings.TrimSpace(providerID))
	}
	providerDefaults.PrimaryModel = strings.TrimSpace(main.Model)
	providerDefaults.PrimaryThinking = recommendedThinking(mainRec, providerDefaults.PrimaryThinking)
	providerDefaults.PlanModel = strings.TrimSpace(plan.Model)
	providerDefaults.PlanThinking = recommendedThinking(planRec, providerDefaults.PlanThinking)
	providerDefaults.AutoModel = strings.TrimSpace(main.Model)
	providerDefaults.AutoThinking = recommendedThinking(mainRec, providerDefaults.AutoThinking)
	providerDefaults.UtilityModel = strings.TrimSpace(utility.Model)
	providerDefaults.UtilityThinking = recommendedThinking(utilityRec, providerDefaults.UtilityThinking)
	return nil
}

func recommendationForRole(record pebblestore.ModelCatalogRecord, roles ...string) pebblestore.ModelCatalogRecommendation {
	for _, rec := range record.Recommendations {
		for _, role := range roles {
			if strings.EqualFold(strings.TrimSpace(rec.Role), role) {
				return rec
			}
		}
	}
	return pebblestore.ModelCatalogRecommendation{}
}

func recommendedThinking(rec pebblestore.ModelCatalogRecommendation, fallback string) string {
	if thinking := strings.ToLower(strings.TrimSpace(rec.Thinking)); thinking != "" {
		return thinking
	}
	return strings.TrimSpace(fallback)
}

func (s *Server) resolveUtilityModelProvider(preferredProvider string) (providerID string, providerDefaults defaults.ProviderDefaults, ok bool, err error) {
	preferredProvider = strings.ToLower(strings.TrimSpace(preferredProvider))
	if preferredProvider != "" {
		providerDefaults, ok, err := s.snapshotRecommendedProviderDefaults(preferredProvider, true)
		if err != nil || !ok {
			return "", defaults.ProviderDefaults{}, false, err
		}
		return providerDefaults.ProviderID, providerDefaults, true, nil
	}
	statuses, err := s.providers.ListStatuses(context.Background())
	if err != nil {
		return "", defaults.ProviderDefaults{}, false, fmt.Errorf("list provider statuses: %w", err)
	}
	for _, status := range statuses {
		id := strings.ToLower(strings.TrimSpace(status.ID))
		if id == "" || !status.Runnable {
			continue
		}
		providerDefaults, ok, err := s.snapshotRecommendedProviderDefaults(id, false)
		if err != nil {
			return "", defaults.ProviderDefaults{}, false, err
		}
		if ok {
			return providerDefaults.ProviderID, providerDefaults, true, nil
		}
	}
	return "", defaults.ProviderDefaults{}, false, nil
}

func (s *Server) resolveOnboardingModelProvider(preferredProvider string) (providerID string, providerDefaults defaults.ProviderDefaults, ok bool, err error) {
	preferredProvider = strings.ToLower(strings.TrimSpace(preferredProvider))
	if preferredProvider != "" {
		providerDefaults, ok, err := s.snapshotRecommendedOnboardingDefaults(preferredProvider, true)
		if err != nil {
			return "", defaults.ProviderDefaults{}, false, err
		}
		if !ok {
			return "", defaults.ProviderDefaults{}, false, nil
		}
		return providerDefaults.ProviderID, providerDefaults, true, nil
	}

	statuses, err := s.providers.ListStatuses(context.Background())
	if err != nil {
		return "", defaults.ProviderDefaults{}, false, fmt.Errorf("list provider statuses: %w", err)
	}
	for _, status := range statuses {
		id := strings.ToLower(strings.TrimSpace(status.ID))
		if id == "" || !status.Runnable {
			continue
		}
		providerDefaults, ok, err := s.snapshotRecommendedOnboardingDefaults(id, false)
		if err != nil {
			return "", defaults.ProviderDefaults{}, false, err
		}
		if !ok {
			continue
		}
		return providerDefaults.ProviderID, providerDefaults, true, nil
	}
	return "", defaults.ProviderDefaults{}, false, nil
}

func (s *Server) snapshotRecommendedOnboardingDefaults(providerID string, required bool) (defaults.ProviderDefaults, bool, error) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" {
		return defaults.ProviderDefaults{}, false, nil
	}
	recommended, ok, err := s.model.RecommendedCatalogRoleDefaults(providerID, "auto", "plan", "compact", "finder", "coder", "designer")
	if err != nil {
		return defaults.ProviderDefaults{}, false, fmt.Errorf("read onboarding model recommendations: %w", err)
	}
	if !ok {
		if required {
			return defaults.ProviderDefaults{}, false, fmt.Errorf("missing required snapshot recommendations for provider %q", providerID)
		}
		return defaults.ProviderDefaults{}, false, nil
	}
	autoRec := recommendationForRole(recommended["auto"], "auto", "main")
	planRec := recommendationForRole(recommended["plan"], "plan")
	providerDefaults := defaults.ProviderDefaults{
		ProviderID:      providerID,
		PrimaryModel:    strings.TrimSpace(recommended["auto"].Model),
		PrimaryThinking: recommendedThinking(autoRec, ""),
		PlanModel:       strings.TrimSpace(recommended["plan"].Model),
		PlanThinking:    recommendedThinking(planRec, ""),
		AutoModel:       strings.TrimSpace(recommended["auto"].Model),
		AutoThinking:    recommendedThinking(autoRec, ""),
	}
	return providerDefaults, true, nil
}

func hasConfiguredOnboardingSubagentSettings(settings uisettings.AgentSettings) bool {
	for _, configured := range []uisettings.CompactAgentSettings{settings.Compact, settings.Finder, settings.Coder, settings.Designer} {
		if strings.TrimSpace(configured.Provider) != "" || strings.TrimSpace(configured.Model) != "" || strings.TrimSpace(configured.Thinking) != "" || strings.TrimSpace(configured.ServiceTier) != "" {
			return true
		}
	}
	return false
}

type onboardingSubagentSettings struct {
	Compact  uisettings.CompactAgentSettings
	Finder   uisettings.CompactAgentSettings
	Coder    uisettings.CompactAgentSettings
	Designer uisettings.CompactAgentSettings
}

func (s *Server) recommendedOnboardingSubagentSettings(providerID string) (onboardingSubagentSettings, bool, error) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	recommended, ok, err := s.model.RecommendedCatalogRoleDefaults(providerID, "compact", "finder", "coder", "designer")
	if err != nil {
		return onboardingSubagentSettings{}, false, fmt.Errorf("read onboarding subagent recommendations: %w", err)
	}
	if !ok {
		return onboardingSubagentSettings{}, false, nil
	}
	selection := func(role string) uisettings.CompactAgentSettings {
		record := recommended[role]
		rec := recommendationForRole(record, role)
		return uisettings.CompactAgentSettings{
			Provider:    providerID,
			Model:       strings.TrimSpace(record.Model),
			Thinking:    recommendedThinking(rec, ""),
			ServiceTier: recommendedServiceTier(rec),
		}
	}
	return onboardingSubagentSettings{
		Compact: selection("compact"), Finder: selection("finder"),
		Coder: selection("coder"), Designer: selection("designer"),
	}, true, nil
}

func recommendedServiceTier(rec pebblestore.ModelCatalogRecommendation) string {
	switch strings.ToLower(strings.TrimSpace(rec.Serving)) {
	case "fast", "priority":
		return "priority"
	default:
		return ""
	}
}

func (s *Server) snapshotRecommendedProviderDefaults(providerID string, required bool) (defaults.ProviderDefaults, bool, error) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" {
		return defaults.ProviderDefaults{}, false, nil
	}
	providerDefaults := defaults.ProviderDefaults{
		ProviderID: providerID,
	}
	if err := s.applySnapshotRecommendedDefaultsForMode(providerID, &providerDefaults, required); err != nil {
		return defaults.ProviderDefaults{}, false, err
	}
	if strings.TrimSpace(providerDefaults.PrimaryModel) == "" || strings.TrimSpace(providerDefaults.PlanModel) == "" || strings.TrimSpace(providerDefaults.AutoModel) == "" || strings.TrimSpace(providerDefaults.UtilityModel) == "" {
		if required {
			return defaults.ProviderDefaults{}, false, fmt.Errorf("missing required snapshot recommendations for provider %q", providerID)
		}
		return defaults.ProviderDefaults{}, false, nil
	}
	if strings.TrimSpace(providerDefaults.ProviderID) == "" {
		providerDefaults.ProviderID = providerID
	}
	return providerDefaults, true, nil
}
