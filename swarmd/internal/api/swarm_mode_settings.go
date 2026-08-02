package api

import (
	"context"
	"errors"
	"net/http"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/modelprofile"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// SwarmModeSettingsPath is the canonical account-scoped direct Swarm model endpoint.
const SwarmModeSettingsPath = "/v1/swarm/model-settings"

type swarmModeSettingsRequest struct {
	Action pebblestore.ModelProfileSelection `json:"action"`
	Plan   pebblestore.ModelProfileSelection `json:"plan"`
}

type swarmModeSettingsResponse struct {
	Action    pebblestore.ModelProfileSelection `json:"action"`
	Plan      pebblestore.ModelProfileSelection `json:"plan"`
	UpdatedAt int64                             `json:"updated_at"`
}

func (s *Server) handleSwarmModeSettings(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.swarmModeSettingsContext(w, r)
	if !ok {
		return
	}

	switch r.Method {
	case http.MethodGet:
		settings, err := s.swarmModelSettings.Get(ctx)
		if err != nil {
			writeSwarmModeSettingsError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":             true,
			"model_settings": swarmModeSettingsFromRecord(settings),
		})
	case http.MethodPut:
		var req swarmModeSettingsRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		settings, err := s.swarmModelSettings.Put(ctx, modelprofile.SwarmSettingsInput{
			Action: req.Action,
			Plan:   req.Plan,
		})
		if err != nil {
			writeSwarmModeSettingsError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":             true,
			"model_settings": swarmModeSettingsFromRecord(settings),
		})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) swarmModeSettingsContext(w http.ResponseWriter, r *http.Request) (context.Context, bool) {
	if s == nil || s.swarmModelSettings == nil {
		writeError(w, http.StatusInternalServerError, modelprofile.ErrNotConfigured)
		return nil, false
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return nil, false
	}
	return identity.ContextWithPrincipal(r.Context(), principal), true
}

func swarmModeSettingsFromRecord(settings modelprofile.SwarmSettings) swarmModeSettingsResponse {
	return swarmModeSettingsResponse{
		Action:    settings.Action,
		Plan:      settings.Plan,
		UpdatedAt: settings.UpdatedAt,
	}
}

func writeSwarmModeSettingsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrPrincipalRequired):
		writeError(w, http.StatusUnauthorized, err)
	case errors.Is(err, modelprofile.ErrSwarmModeSettingsNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, pebblestore.ErrSwarmModeActionRequired),
		errors.Is(err, pebblestore.ErrSwarmModePlanRequired),
		errors.Is(err, modelprofile.ErrSwarmSelectionInvalid):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, modelprofile.ErrNotConfigured),
		errors.Is(err, pebblestore.ErrSwarmModeStoreNotConfigured):
		writeError(w, http.StatusInternalServerError, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}
