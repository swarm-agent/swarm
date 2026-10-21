package run

import (
	"errors"
	"fmt"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// resolveTaskLaunchProfileWithFallback keeps compiled role authority independent
// of model recovery. Only the owning account's configured Swarm Action model can
// replace an unavailable specialist assignment; every replacement emits a warning.
func (s *Service) resolveTaskLaunchProfileWithFallback(parentSession pebblestore.SessionSnapshot, requested, childMode string) (profile pebblestore.AgentProfile, virtual bool, sourceName string, routerAlert string, err error) {
	if s == nil {
		return profile, false, "", "", errors.New("task launch service is not configured")
	}
	profile, virtual, sourceName, strictErr := s.resolveTaskLaunchProfileForMode(parentSession, requested, childMode)
	agentID := ""
	switch {
	case agentruntime.IsCoderAgentName(requested):
		agentID = agentruntime.CoderAgentID
	case agentruntime.IsFinderAgentName(requested):
		agentID = agentruntime.FinderAgentID
	case agentruntime.IsDesignerAgentName(requested):
		agentID = agentruntime.DesignerAgentID
	default:
		return profile, virtual, sourceName, "", strictErr
	}
	// A worker's helper restriction is authorization, not a model failure.
	if workerHelperParent(parentSession) && agentID != agentruntime.FinderAgentID {
		return profile, virtual, sourceName, "", strictErr
	}
	if strictErr == nil {
		return profile, virtual, sourceName, "", nil
	}
	fail := func(cause error) (pebblestore.AgentProfile, bool, string, string, error) {
		return pebblestore.AgentProfile{}, virtual, sourceName, "", fmt.Errorf("resolve %s: %v; account Swarm Action fallback unavailable: %w", agentID, strictErr, cause)
	}
	if s.agentModelSettings == nil || s.model == nil || s.agents == nil {
		return fail(errors.New("canonical model settings, model and agent services are required"))
	}
	settings, settingsErr := s.agentModelSettings.GetForAccount(parentSession.AccountScopeID)
	if settingsErr != nil {
		return fail(settingsErr)
	}
	assignment := settings.Swarm.Action
	if strings.TrimSpace(assignment.Provider) == "" || strings.TrimSpace(assignment.Model) == "" {
		return fail(errors.New("owning account has no configured Swarm Action provider/model"))
	}
	resolved, resolveErr := s.model.ResolvePreference(pebblestore.ModelPreference{Provider: assignment.Provider, Model: assignment.Model, Thinking: assignment.Thinking, ServiceTier: assignment.ServiceTier, ContextMode: assignment.ContextMode})
	if resolveErr != nil {
		return fail(resolveErr)
	}
	profile, resolveErr = s.agents.ResolveSystemAgent(agentID, pebblestore.AgentProfile{Provider: resolved.Preference.Provider, Model: resolved.Preference.Model, Thinking: resolved.Preference.Thinking, AutoServiceTier: resolved.Preference.ServiceTier, ContextMode: resolved.Preference.ContextMode})
	if resolveErr != nil {
		return fail(resolveErr)
	}
	if workerHelperParent(parentSession) {
		profile = agentruntime.HelperAgentProfileForParent(profile)
	}
	// Role materializers may omit runtime fields; preserve the exact canonical
	// fallback preference without inheriting any parent capability.
	profile.Provider, profile.Model, profile.Thinking = resolved.Preference.Provider, resolved.Preference.Model, resolved.Preference.Thinking
	profile.AutoServiceTier, profile.ContextMode = resolved.Preference.ServiceTier, resolved.Preference.ContextMode
	routerAlert = fmt.Sprintf("%s model resolution failed: %v. Using the owning account's configured Swarm Action model %s/%s; compiled role and tool permissions are unchanged.", agentID, strictErr, profile.Provider, profile.Model)
	return profile, virtual, sourceName, routerAlert, nil
}
