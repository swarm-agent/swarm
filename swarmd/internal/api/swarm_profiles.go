package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/modelprofile"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const swarmProfilesPath = "/v1/swarm-profiles"

type swarmProfileMemberRequest struct {
	AgentID   string                             `json:"agent_id"`
	ModelMode string                             `json:"model_mode"`
	Single    *pebblestore.ModelProfileSelection `json:"single,omitempty"`
	Plan      *pebblestore.ModelProfileSelection `json:"plan,omitempty"`
	Auto      *pebblestore.ModelProfileSelection `json:"auto,omitempty"`
}
type swarmProfileRequest struct {
	Name    string                      `json:"name"`
	Members []swarmProfileMemberRequest `json:"members"`
}

type swarmProfileResponse struct {
	ProfileID string                           `json:"profile_id"`
	Name      string                           `json:"name"`
	Members   []pebblestore.SwarmProfileMember `json:"members"`
	CreatedAt int64                            `json:"created_at"`
	UpdatedAt int64                            `json:"updated_at"`
}

func (s *Server) handleSwarmProfiles(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.swarmProfileContext(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		profiles, err := s.swarmProfiles.List(ctx)
		if err != nil {
			writeSwarmProfileError(w, err)
			return
		}
		out := make([]swarmProfileResponse, len(profiles))
		for i, profile := range profiles {
			out[i] = swarmProfileFromRecord(profile)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "swarm_profiles": out})
	case http.MethodPost:
		var req swarmProfileRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		profile, err := s.swarmProfiles.Create(ctx, req.input())
		if err != nil {
			writeSwarmProfileError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "swarm_profile": swarmProfileFromRecord(profile)})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleSwarmProfileByID(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.swarmProfileContext(w, r)
	if !ok {
		return
	}
	profileID := strings.Trim(strings.TrimPrefix(r.URL.Path, swarmProfilesPath+"/"), "/")
	if profileID == "" || strings.Contains(profileID, "/") {
		writeError(w, http.StatusBadRequest, errors.New("invalid swarm profile path"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		profile, err := s.swarmProfiles.Get(ctx, profileID)
		if err != nil {
			writeSwarmProfileError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "swarm_profile": swarmProfileFromRecord(profile)})
	case http.MethodPut:
		var req swarmProfileRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		profile, err := s.swarmProfiles.Update(ctx, profileID, req.input())
		if err != nil {
			writeSwarmProfileError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "swarm_profile": swarmProfileFromRecord(profile)})
	case http.MethodDelete:
		deleted, err := s.swarmProfiles.Delete(ctx, profileID)
		if err != nil {
			writeSwarmProfileError(w, err)
			return
		}
		if !deleted {
			writeError(w, http.StatusNotFound, modelprofile.ErrSwarmProfileNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted_profile_id": profileID})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) swarmProfileContext(w http.ResponseWriter, r *http.Request) (context.Context, bool) {
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

func (r swarmProfileRequest) input() modelprofile.SwarmInput {
	members := make([]modelprofile.SwarmMemberInput, len(r.Members))
	for i, member := range r.Members {
		members[i] = modelprofile.SwarmMemberInput{AgentID: member.AgentID, ModelMode: member.ModelMode, Single: member.Single, Plan: member.Plan, Auto: member.Auto}
	}
	return modelprofile.SwarmInput{Name: r.Name, Members: members}
}
func swarmProfileFromRecord(profile modelprofile.SwarmProfile) swarmProfileResponse {
	return swarmProfileResponse{ProfileID: profile.ProfileID, Name: profile.Name, Members: profile.Members, CreatedAt: profile.CreatedAt, UpdatedAt: profile.UpdatedAt}
}
func writeSwarmProfileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrPrincipalRequired):
		writeError(w, http.StatusUnauthorized, err)
	case errors.Is(err, modelprofile.ErrSwarmProfileNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, pebblestore.ErrSwarmProfileNameConflict):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, modelprofile.ErrNotConfigured):
		writeError(w, http.StatusInternalServerError, err)
	default:
		writeError(w, http.StatusBadRequest, err)
	}
}
