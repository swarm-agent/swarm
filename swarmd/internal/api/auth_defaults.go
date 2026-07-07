package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/provider/defaults"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) applyUtilityModelDefaults(preferredProvider string) (*auth.AutoDefaultsStatus, error) {
	return s.applyUtilityModelDefaultsForAccount("", "", preferredProvider)
}

func (s *Server) applyUtilityModelDefaultsForAccount(accountScopeID, userID, preferredProvider string) (*auth.AutoDefaultsStatus, error) {
	if s == nil || s.model == nil || s.agents == nil || s.providers == nil {
		return nil, nil
	}

	if err := s.refreshModelCatalogForOnboardingDefaults(); err != nil {
		return nil, err
	}

	providerID, providerDefaults, ok, err := s.resolveUtilityModelProvider(preferredProvider)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	if err := s.applySnapshotRecommendedDefaults(providerID, &providerDefaults); err != nil {
		return nil, err
	}

	out := &auth.AutoDefaultsStatus{
		Provider:        providerID,
		Model:           providerDefaults.PrimaryModel,
		Thinking:        providerDefaults.PrimaryThinking,
		UtilityProvider: providerID,
		UtilityModel:    providerDefaults.UtilityModel,
		UtilityThinking: providerDefaults.UtilityThinking,
	}

	pref, err := s.model.GetPreferenceForAccount(accountScopeID)
	if err != nil {
		return nil, fmt.Errorf("read model preference: %w", err)
	}
	firstProviderOnboarding := strings.TrimSpace(pref.Provider) == ""
	if firstProviderOnboarding {
		_, event, err := s.model.SetPreferenceForAccount(accountScopeID, userID, providerID, providerDefaults.PrimaryModel, providerDefaults.PrimaryThinking)
		if err != nil {
			return nil, fmt.Errorf("set global model default: %w", err)
		}
		if event != nil && s.hub != nil {
			s.hub.Publish(*event)
		}
		if err := s.seedSplitModelDefaultsForAccount(accountScopeID, providerID, providerDefaults); err != nil {
			return nil, fmt.Errorf("set plan/auto model defaults: %w", err)
		}
		out.Applied = true
		out.GlobalModel = true
	}

	state, err := s.agents.ListStateForAccount(accountScopeID, 2000)
	if err != nil {
		return nil, fmt.Errorf("list agent state: %w", err)
	}

	assignments := make(map[string]struct{}, len(builtinUtilityAgentNames()))
	for _, name := range builtinUtilityAgentNames() {
		if normalized := strings.ToLower(strings.TrimSpace(name)); normalized != "" {
			assignments[normalized] = struct{}{}
		}
	}
	if len(assignments) == 0 {
		return nil, nil
	}
	agentsSeen := make(map[string]struct{}, len(assignments))
	subagentsSeen := make(map[string]struct{}, len(assignments))
	if firstProviderOnboarding {
		state, err = s.applyUtilityAIToBuiltInsForAccount(accountScopeID, state, providerID, providerDefaults.UtilityModel, providerDefaults.UtilityThinking, false)
		if err != nil {
			return nil, fmt.Errorf("set utility AI defaults: %w", err)
		}
		for _, profile := range state.Profiles {
			name := strings.ToLower(strings.TrimSpace(profile.Name))
			if name == "" {
				continue
			}
			if _, target := assignments[name]; !target {
				continue
			}
			agentsSeen[name] = struct{}{}
			if strings.EqualFold(strings.TrimSpace(profile.Mode), agentruntime.ModeSubagent) {
				subagentsSeen[name] = struct{}{}
			}
		}
		if len(agentsSeen) > 0 {
			out.Applied = true
		}
	}

	if !out.Applied {
		return nil, nil
	}
	out.Agents = sortedKeys(agentsSeen)
	out.Subagents = sortedKeys(subagentsSeen)
	return out, nil
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
	if s == nil || s.model == nil || providerDefaults == nil {
		return nil
	}
	main, plan, utility, ok, err := s.model.RecommendedCatalogDefaults(providerID)
	if err != nil {
		return fmt.Errorf("read model catalog recommendations: %w", err)
	}
	if !ok {
		return nil
	}
	mainRec := recommendationForRole(main, "main", "auto")
	planRec := recommendationForRole(plan, "plan")
	utilityRec := recommendationForRole(utility, "utility")
	if strings.TrimSpace(main.Model) == "" || strings.TrimSpace(plan.Model) == "" || strings.TrimSpace(utility.Model) == "" {
		return nil
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

func (s *Server) seedSplitModelDefaultsForAccount(accountScopeID, providerID string, providerDefaults defaults.ProviderDefaults) error {
	if s == nil || s.agents == nil {
		return nil
	}
	state, err := s.agents.ListStateForAccount(accountScopeID, 2000)
	if err != nil {
		return err
	}
	planProvider := strings.ToLower(strings.TrimSpace(providerID))
	planModel := strings.TrimSpace(providerDefaults.PlanModel)
	planThinking := strings.TrimSpace(providerDefaults.PlanThinking)
	autoProvider := planProvider
	autoModel := strings.TrimSpace(providerDefaults.AutoModel)
	autoThinking := strings.TrimSpace(providerDefaults.AutoThinking)
	if planProvider == "" || planModel == "" || autoModel == "" {
		return nil
	}
	for _, profile := range state.Profiles {
		if !strings.EqualFold(strings.TrimSpace(profile.Name), "swarm") {
			continue
		}
		if strings.TrimSpace(profile.Provider) != "" || strings.TrimSpace(profile.Model) != "" || strings.TrimSpace(profile.PlanProvider) != "" || strings.TrimSpace(profile.PlanModel) != "" || strings.TrimSpace(profile.AutoProvider) != "" || strings.TrimSpace(profile.AutoModel) != "" {
			return nil
		}
		enabled := profile.Enabled
		_, _, _, err := s.agents.UpsertForAccount(accountScopeID, agentruntime.UpsertInput{
			Name:                profile.Name,
			Mode:                profile.Mode,
			Description:         profile.Description,
			Provider:            profile.Provider,
			ProviderSet:         true,
			Model:               profile.Model,
			ModelSet:            true,
			Thinking:            profile.Thinking,
			ThinkingSet:         true,
			ModelMode:           "split",
			PlanProvider:        planProvider,
			PlanModel:           planModel,
			PlanThinking:        planThinking,
			AutoProvider:        autoProvider,
			AutoModel:           autoModel,
			AutoThinking:        autoThinking,
			Prompt:              profile.Prompt,
			RuntimeMode:         profile.RuntimeMode,
			ExecutionSetting:    profile.ExecutionSetting,
			ExitPlanModeEnabled: profile.ExitPlanModeEnabled,
			ToolScope:           profile.ToolScope,
			ToolContract:        profile.ToolContract,
			Enabled:             &enabled,
		})
		return err
	}
	return nil
}

func (s *Server) resolveUtilityModelProvider(preferredProvider string) (providerID string, providerDefaults defaults.ProviderDefaults, ok bool, err error) {
	statuses, err := s.providers.ListStatuses(context.Background())
	if err != nil {
		return "", defaults.ProviderDefaults{}, false, fmt.Errorf("list provider statuses: %w", err)
	}

	preferredProvider = strings.ToLower(strings.TrimSpace(preferredProvider))
	if preferredProvider != "" {
		for _, status := range statuses {
			id := strings.ToLower(strings.TrimSpace(status.ID))
			if id != preferredProvider || !status.Runnable {
				continue
			}
			providerDefaults, ok := defaults.Lookup(id)
			if !ok || strings.TrimSpace(providerDefaults.PrimaryModel) == "" || strings.TrimSpace(providerDefaults.UtilityModel) == "" {
				continue
			}
			return id, providerDefaults, true, nil
		}
	}

	for _, status := range statuses {
		id := strings.ToLower(strings.TrimSpace(status.ID))
		if id == "" || !status.Runnable {
			continue
		}
		providerDefaults, ok := defaults.Lookup(id)
		if !ok || strings.TrimSpace(providerDefaults.PrimaryModel) == "" || strings.TrimSpace(providerDefaults.UtilityModel) == "" {
			continue
		}
		return id, providerDefaults, true, nil
	}
	return "", defaults.ProviderDefaults{}, false, nil
}

func sortedKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, strings.TrimSpace(value))
		}
	}
	sort.Strings(out)
	return out
}
