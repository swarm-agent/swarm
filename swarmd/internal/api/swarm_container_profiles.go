package api

import (
	"context"
	"errors"
	"net/http"

	containerprofiles "swarm/packages/swarmd/internal/containerprofiles"
)

func (s *Server) handleSwarmContainerProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.containerProfiles == nil {
		writeError(w, http.StatusInternalServerError, errors.New("container profile service not configured"))
		return
	}
	profiles, err := s.containerProfiles.ListProfiles(context.Background())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"path_id":  containerprofiles.PathProfilesList,
		"profiles": profiles,
	})
}

func (s *Server) handleSwarmContainerProfileUpsert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.containerProfiles == nil {
		writeError(w, http.StatusInternalServerError, errors.New("container profile service not configured"))
		return
	}
	var req struct {
		ID                string                    `json:"id"`
		Name              string                    `json:"name"`
		Description       string                    `json:"description"`
		RoleHint          string                    `json:"role_hint"`
		AccessMode        string                    `json:"access_mode"`
		ContainerName     string                    `json:"container_name"`
		Hostname          string                    `json:"hostname"`
		NetworkName       string                    `json:"network_name"`
		APIPort           int                       `json:"api_port"`
		AdvertiseHost     string                    `json:"advertise_host"`
		AdvertisePort     int                       `json:"advertise_port"`
		TailscaleHostname string                    `json:"tailscale_hostname"`
		Mounts            []containerprofiles.Mount `json:"mounts"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	profile, err := s.containerProfiles.UpsertProfile(context.Background(), containerprofiles.UpsertInput{
		ID:                req.ID,
		Name:              req.Name,
		Description:       req.Description,
		RoleHint:          req.RoleHint,
		AccessMode:        req.AccessMode,
		ContainerName:     req.ContainerName,
		Hostname:          req.Hostname,
		NetworkName:       req.NetworkName,
		APIPort:           req.APIPort,
		AdvertiseHost:     req.AdvertiseHost,
		AdvertisePort:     req.AdvertisePort,
		TailscaleHostname: req.TailscaleHostname,
		Mounts:            req.Mounts,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": containerprofiles.PathProfilesUpsert,
		"profile": profile,
	})
}

func (s *Server) handleSwarmContainerProfileDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.containerProfiles == nil {
		writeError(w, http.StatusInternalServerError, errors.New("container profile service not configured"))
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.containerProfiles.DeleteProfile(context.Background(), req.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": result.PathID,
		"deleted": result.Deleted,
	})
}
