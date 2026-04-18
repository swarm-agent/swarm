package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	localcontainers "swarm/packages/swarmd/internal/localcontainers"
)

func (s *Server) handleSwarmLocalContainerRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.localContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("local container service not configured"))
		return
	}
	status, err := s.localContainers.RuntimeStatus(context.Background())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": status.PathID,
		"runtime": status,
	})
}

func (s *Server) handleSwarmLocalContainers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.localContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("local container service not configured"))
		return
	}
	items, err := s.localContainers.List(context.Background())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"path_id":    localcontainers.PathContainerList,
		"containers": items,
	})
}

func (s *Server) handleSwarmLocalContainerCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.localContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("local container service not configured"))
		return
	}
	var req struct {
		Name              string `json:"name"`
		Runtime           string `json:"runtime"`
		HostAPIBaseURL    string `json:"host_api_base_url"`
		Image             string `json:"image"`
		ContainerPackages struct {
			BaseImage      string `json:"base_image,omitempty"`
			PackageManager string `json:"package_manager,omitempty"`
			Packages       []struct {
				Name   string `json:"name"`
				Source string `json:"source,omitempty"`
				Reason string `json:"reason,omitempty"`
			} `json:"packages,omitempty"`
		} `json:"container_packages,omitempty"`
		Mounts []localcontainers.Mount `json:"mounts"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	packages := make([]localcontainers.ContainerPackageSelection, 0, len(req.ContainerPackages.Packages))
	for _, pkg := range req.ContainerPackages.Packages {
		packages = append(packages, localcontainers.ContainerPackageSelection{
			Name:   pkg.Name,
			Source: pkg.Source,
			Reason: pkg.Reason,
		})
	}
	container, err := s.localContainers.Create(context.Background(), localcontainers.CreateInput{
		Name:           req.Name,
		Runtime:        req.Runtime,
		HostAPIBaseURL: req.HostAPIBaseURL,
		Image:          req.Image,
		ContainerPackages: localcontainers.ContainerPackageManifest{
			BaseImage:      req.ContainerPackages.BaseImage,
			PackageManager: req.ContainerPackages.PackageManager,
			Packages:       packages,
		},
		Mounts: req.Mounts,
	})
	if err != nil {
		statusCode := http.StatusBadRequest
		if strings.Contains(strings.ToLower(err.Error()), "start ") {
			statusCode = http.StatusConflict
		}
		writeJSON(w, statusCode, map[string]any{
			"ok":        false,
			"path_id":   localcontainers.PathContainerCreate,
			"container": container,
			"error":     err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"path_id":   localcontainers.PathContainerCreate,
		"container": container,
	})
}

func (s *Server) handleSwarmLocalContainerAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.localContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("local container service not configured"))
		return
	}
	var req struct {
		ID     string `json:"id"`
		Action string `json:"action"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	container, err := s.localContainers.Act(context.Background(), localcontainers.ActionInput{ID: req.ID, Action: req.Action})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":        false,
			"path_id":   localcontainers.PathContainerAction,
			"container": container,
			"error":     err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"path_id":   localcontainers.PathContainerAction,
		"container": container,
	})
}

func (s *Server) handleSwarmLocalContainerDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.localContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("local container service not configured"))
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.localContainers.BulkDelete(context.Background(), req.IDs)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"path_id": localcontainers.PathContainerDelete,
			"result":  result,
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": localcontainers.PathContainerDelete,
		"result":  result,
	})
}

func (s *Server) handleSwarmLocalContainerPruneMissing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.localContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("local container service not configured"))
		return
	}
	result, err := s.localContainers.PruneMissing(context.Background())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"path_id": localcontainers.PathContainerPrune,
			"result":  result,
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": localcontainers.PathContainerPrune,
		"result":  result,
	})
}
