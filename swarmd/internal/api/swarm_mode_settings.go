package api

import (
	"context"
	"errors"
	"net/http"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/modelprofile"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// SwarmModeSettingsPath is the canonical account-scoped Swarm model-mode endpoint.
const SwarmModeSettingsPath = "/v1/swarm/model-settings"

type swarmModeSettingsRequest struct {
	ActionFavoriteID string `json:"action_favorite_id"`
	PlanEnabled      bool   `json:"plan_enabled"`
	PlanFavoriteID   string `json:"plan_favorite_id,omitempty"`
}

type swarmModeSettingsResponse struct {
	ActionFavoriteID string `json:"action_favorite_id"`
	PlanEnabled      bool   `json:"plan_enabled"`
	PlanFavoriteID   string `json:"plan_favorite_id,omitempty"`
	UpdatedAt        int64  `json:"updated_at"`
}

func (s *Server) handleSwarmModeSettings(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.swarmModeSettingsContext(w, r)
	if !ok {
		return
	}

	switch r.Method {
	case http.MethodGet:
		settings, err := s.swarmProfiles.Get(ctx)
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
		settings, err := s.swarmProfiles.Put(ctx, modelprofile.SwarmSettingsInput{
			ActionFavoriteID: req.ActionFavoriteID,
			PlanEnabled:      req.PlanEnabled,
			PlanFavoriteID:   req.PlanFavoriteID,
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
	if s == nil || s.swarmProfiles == nil {
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
		ActionFavoriteID: settings.ActionFavoriteID,
		PlanEnabled:      settings.PlanEnabled,
		PlanFavoriteID:   settings.PlanFavoriteID,
		UpdatedAt:        settings.UpdatedAt,
	}
}

func writeSwarmModeSettingsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrPrincipalRequired):
		writeError(w, http.StatusUnauthorized, err)
	case errors.Is(err, modelprofile.ErrSwarmModeSettingsNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, modelprofile.ErrSwarmActionFavoriteNotFound),
		errors.Is(err, modelprofile.ErrSwarmPlanFavoriteNotFound):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, pebblestore.ErrSwarmModeActionFavoriteIDRequired),
		errors.Is(err, modelprofile.ErrSwarmPlanConfigurationContradictory):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, modelprofile.ErrNotConfigured),
		errors.Is(err, pebblestore.ErrSwarmModeStoreNotConfigured):
		writeError(w, http.StatusInternalServerError, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}
