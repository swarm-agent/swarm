package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// AgentModelSettingsPath is the canonical authenticated account-scoped endpoint
// for direct Swarm and compiled system-agent model assignments.
const AgentModelSettingsPath = "/v1/agent-model-settings"

type agentModelSettingsSwarmPatch struct {
	Action *agentmodelsettings.Assignment `json:"action"`
	Plan   *agentmodelsettings.Assignment `json:"plan"`
}

type agentModelSettingsSystemAgentPatch struct {
	Compact  *agentmodelsettings.Assignment `json:"compact"`
	Finder   *agentmodelsettings.Assignment `json:"finder"`
	Coder    *agentmodelsettings.Assignment `json:"coder"`
	Designer *agentmodelsettings.Assignment `json:"designer"`
	Router   *agentmodelsettings.Assignment `json:"router"`
}

type agentModelSettingsPatch struct {
	Swarm        *agentModelSettingsSwarmPatch       `json:"swarm"`
	SystemAgents *agentModelSettingsSystemAgentPatch `json:"system_agents"`
}

func (s *Server) handleAgentModelSettings(w http.ResponseWriter, r *http.Request) {
	if r == nil {
		writeError(w, http.StatusBadRequest, errors.New("request is required"))
		return
	}
	ctx, ok := s.agentModelSettingsContext(w, r)
	if !ok {
		return
	}

	switch r.Method {
	case http.MethodGet:
		settings, err := s.agentModelSettings.Get(ctx)
		if err != nil {
			writeAgentModelSettingsError(w, err)
			return
		}
		writeAgentModelSettingsResponse(w, settings)
	case http.MethodPatch:
		var patch agentModelSettingsPatch
		if err := decodeJSON(r, &patch); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		operationCount := 0
		if patch.Swarm != nil {
			operationCount++
		}
		if patch.SystemAgents != nil {
			operationCount++
		}
		if operationCount != 1 {
			writeError(w, http.StatusBadRequest, errors.New("patch must contain exactly one of swarm or system_agents"))
			return
		}

		var (
			settings agentmodelsettings.Settings
			err      error
		)
		if patch.Swarm != nil {
			if patch.Swarm.Action == nil || patch.Swarm.Plan == nil {
				writeError(w, http.StatusBadRequest, errors.New("swarm patch requires complete action and plan assignments"))
				return
			}
			settings, err = s.agentModelSettings.ReplaceSwarm(ctx, agentmodelsettings.SwarmInput{
				Action: *patch.Swarm.Action,
				Plan:   *patch.Swarm.Plan,
			})
		} else {
			name, assignment, selectionErr := patch.SystemAgents.target()
			if selectionErr != nil {
				writeError(w, http.StatusBadRequest, selectionErr)
				return
			}
			settings, err = s.agentModelSettings.UpdateSystemAgent(ctx, name, assignment)
		}
		if err != nil {
			writeAgentModelSettingsError(w, err)
			return
		}
		writeAgentModelSettingsResponse(w, settings)
	default:
		methodNotAllowed(w)
	}
}

func (patch *agentModelSettingsSystemAgentPatch) target() (string, agentmodelsettings.Assignment, error) {
	if patch == nil {
		return "", agentmodelsettings.Assignment{}, errors.New("system_agents patch must contain exactly one assignment")
	}
	var selected []struct {
		name       string
		assignment *agentmodelsettings.Assignment
	}
	for _, item := range []struct {
		name       string
		assignment *agentmodelsettings.Assignment
	}{
		{pebblestore.SystemAgentCompact, patch.Compact},
		{pebblestore.SystemAgentFinder, patch.Finder},
		{pebblestore.SystemAgentCoder, patch.Coder},
		{pebblestore.SystemAgentDesigner, patch.Designer},
		{pebblestore.SystemAgentRouter, patch.Router},
	} {
		if item.assignment != nil {
			selected = append(selected, item)
		}
	}
	if len(selected) != 1 {
		return "", agentmodelsettings.Assignment{}, errors.New("system_agents patch must contain exactly one assignment")
	}
	return selected[0].name, *selected[0].assignment, nil
}

func (s *Server) agentModelSettingsContext(w http.ResponseWriter, r *http.Request) (context.Context, bool) {
	if s == nil || s.agentModelSettings == nil {
		writeError(w, http.StatusInternalServerError, agentmodelsettings.ErrNotConfigured)
		return nil, false
	}
	if r == nil {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return nil, false
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return nil, false
	}
	return identity.ContextWithPrincipal(r.Context(), principal), true
}

func writeAgentModelSettingsResponse(w http.ResponseWriter, settings agentmodelsettings.Settings) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                   true,
		"agent_model_settings": settings,
	})
}

func writeAgentModelSettingsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrPrincipalRequired),
		errors.Is(err, identity.ErrProductIdentityRequired):
		writeError(w, http.StatusUnauthorized, err)
	case errors.Is(err, agentmodelsettings.ErrNotFound),
		errors.Is(err, pebblestore.ErrAgentModelSettingsNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, pebblestore.ErrAgentModelSettingsAssignmentInvalid),
		errors.Is(err, pebblestore.ErrAgentModelSettingsAgentUnknown),
		errors.Is(err, pebblestore.ErrAgentModelSettingsAccountRequired):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, agentmodelsettings.ErrNotConfigured),
		errors.Is(err, pebblestore.ErrAgentModelSettingsStoreNotConfigured):
		writeError(w, http.StatusInternalServerError, err)
	default:
		writeError(w, http.StatusInternalServerError, fmt.Errorf("agent model settings: %w", err))
	}
}
