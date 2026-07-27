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

const modelProfilesPath = "/v1/model-profiles"

type modelProfileRequest struct {
	Name      string                             `json:"name"`
	ModelMode string                             `json:"model_mode"`
	Single    *pebblestore.ModelProfileSelection `json:"single,omitempty"`
	Plan      *pebblestore.ModelProfileSelection `json:"plan,omitempty"`
	Auto      *pebblestore.ModelProfileSelection `json:"auto,omitempty"`
}

type modelProfileResponse struct {
	ProfileID string                             `json:"profile_id"`
	Name      string                             `json:"name"`
	ModelMode string                             `json:"model_mode"`
	Single    *pebblestore.ModelProfileSelection `json:"single,omitempty"`
	Plan      *pebblestore.ModelProfileSelection `json:"plan,omitempty"`
	Auto      *pebblestore.ModelProfileSelection `json:"auto,omitempty"`
	CreatedAt int64                              `json:"created_at"`
	UpdatedAt int64                              `json:"updated_at"`
	IsDefault bool                               `json:"is_default"`
}

type modelProfilesBulkDeleteRequest struct {
	ProfileIDs []string `json:"profile_ids"`
}

type modelProfileDefaultRequest struct {
	ProfileID string `json:"profile_id"`
}

func (s *Server) handleModelProfiles(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.modelProfileContext(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		state, err := s.modelProfiles.ListState(ctx)
		if err != nil {
			writeModelProfileError(w, err)
			return
		}
		out := make([]modelProfileResponse, 0, len(state.Profiles))
		for _, profile := range state.Profiles {
			out = append(out, modelProfileFromRecord(profile))
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model_profiles": out, "default_profile_id": state.DefaultProfileID})
	case http.MethodPost:
		var req modelProfileRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		profile, err := s.modelProfiles.Create(ctx, req.input())
		if err != nil {
			writeModelProfileError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "model_profile": modelProfileFromRecord(profile)})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleModelProfileByID(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.modelProfileContext(w, r)
	if !ok {
		return
	}
	profileID, err := parseModelProfileID(r.URL.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		profile, err := s.modelProfiles.Get(ctx, profileID)
		if err != nil {
			writeModelProfileError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model_profile": modelProfileFromRecord(profile)})
	case http.MethodPut:
		var req modelProfileRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		profile, err := s.modelProfiles.Update(ctx, profileID, req.input())
		if err != nil {
			writeModelProfileError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model_profile": modelProfileFromRecord(profile)})
	case http.MethodDelete:
		deleted, err := s.modelProfiles.Delete(ctx, profileID)
		if err != nil {
			writeModelProfileError(w, err)
			return
		}
		if !deleted {
			writeError(w, http.StatusNotFound, modelprofile.ErrNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted_profile_id": profileID})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleModelProfileDefault(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.modelProfileContext(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req modelProfileDefaultRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.ProfileID) == "" {
		writeError(w, http.StatusBadRequest, errors.New("profile_id is required"))
		return
	}
	profile, err := s.modelProfiles.SetDefault(ctx, req.ProfileID)
	if err != nil {
		writeModelProfileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "default_profile_id": profile.ProfileID, "model_profile": modelProfileFromRecord(profile)})
}

func (s *Server) handleModelProfilesBulkDelete(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.modelProfileContext(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req modelProfilesBulkDeleteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.ProfileIDs) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("profile_ids must contain at least one profile id"))
		return
	}
	for _, profileID := range req.ProfileIDs {
		if strings.TrimSpace(profileID) == "" {
			writeError(w, http.StatusBadRequest, errors.New("profile_ids must not contain empty profile ids"))
			return
		}
	}
	result, err := s.modelProfiles.BulkDelete(ctx, req.ProfileIDs)
	if err != nil {
		writeModelProfileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                  true,
		"deleted_profile_ids": result.DeletedIDs,
		"missing_profile_ids": result.MissingIDs,
		"deleted_count":       len(result.DeletedIDs),
	})
}

func (s *Server) modelProfileContext(w http.ResponseWriter, r *http.Request) (context.Context, bool) {
	if s == nil || s.modelProfiles == nil {
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

func (r modelProfileRequest) input() modelprofile.Input {
	return modelprofile.Input{Name: r.Name, ModelMode: r.ModelMode, Single: r.Single, Plan: r.Plan, Auto: r.Auto}
}

func modelProfileFromRecord(profile modelprofile.Profile) modelProfileResponse {
	return modelProfileResponse{ProfileID: profile.ProfileID, Name: profile.Name, ModelMode: profile.ModelMode, Single: profile.Single, Plan: profile.Plan, Auto: profile.Auto, CreatedAt: profile.CreatedAt, UpdatedAt: profile.UpdatedAt, IsDefault: profile.IsDefault}
}

func parseModelProfileID(path string) (string, error) {
	profileID := strings.Trim(strings.TrimPrefix(path, modelProfilesPath+"/"), "/")
	if profileID == "" || strings.Contains(profileID, "/") {
		return "", errors.New("invalid model profile path")
	}
	return profileID, nil
}

func writeModelProfileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrPrincipalRequired):
		writeError(w, http.StatusUnauthorized, err)
	case errors.Is(err, modelprofile.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, pebblestore.ErrModelProfileNameConflict):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, modelprofile.ErrNotConfigured):
		writeError(w, http.StatusInternalServerError, err)
	default:
		writeError(w, http.StatusBadRequest, err)
	}
}
