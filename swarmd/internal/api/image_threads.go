package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type imageThreadCreateRequest struct {
	Title           string                           `json:"title"`
	WorkspacePath   string                           `json:"workspace_path"`
	WorkspaceName   string                           `json:"workspace_name"`
	ImageFolders    []string                         `json:"image_folders"`
	ImageAssets     []pebblestore.ImageAssetSnapshot `json:"image_assets"`
	ImageAssetOrder []string                         `json:"image_asset_order"`
	Metadata        map[string]any                   `json:"metadata"`
}

type imageThreadUpdateRequest struct {
	Title           *string                          `json:"title"`
	ImageFolders    []string                         `json:"image_folders"`
	ImageAssets     []pebblestore.ImageAssetSnapshot `json:"image_assets"`
	ImageAssetOrder []string                         `json:"image_asset_order"`
	Metadata        map[string]any                   `json:"metadata"`
}

func (s *Server) handleWorkspaceImageThreads(w http.ResponseWriter, r *http.Request) {
	if s.imageThreads == nil {
		writeError(w, http.StatusInternalServerError, errors.New("image thread store is not configured"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		workspacePath := strings.TrimSpace(r.URL.Query().Get("workspace_path"))
		if workspacePath == "" {
			workspacePath = strings.TrimSpace(r.URL.Query().Get("cwd"))
		}
		if workspacePath == "" {
			writeError(w, http.StatusBadRequest, errors.New("workspace path is required"))
			return
		}
		threads, err := s.imageThreads.ListForWorkspace(workspacePath, 200)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "threads": threads})
	case http.MethodPost:
		var req imageThreadCreateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		workspacePath := strings.TrimSpace(req.WorkspacePath)
		if workspacePath == "" {
			writeError(w, http.StatusBadRequest, errors.New("workspace path is required"))
			return
		}
		thread, err := s.imageThreads.Create(pebblestore.ImageThreadSnapshot{
			ID:              session.NewSessionID(),
			WorkspacePath:   workspacePath,
			WorkspaceName:   strings.TrimSpace(req.WorkspaceName),
			Title:           strings.TrimSpace(req.Title),
			ImageFolders:    req.ImageFolders,
			ImageAssets:     req.ImageAssets,
			ImageAssetOrder: req.ImageAssetOrder,
			Metadata:        req.Metadata,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "thread": thread})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleWorkspaceImageThread(w http.ResponseWriter, r *http.Request) {
	if s.imageThreads == nil {
		writeError(w, http.StatusInternalServerError, errors.New("image thread store is not configured"))
		return
	}
	threadID, err := parseImageThreadPath(r.URL.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		thread, ok, err := s.imageThreads.Get(threadID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("image thread not found"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "thread": thread})
	case http.MethodPost:
		thread, ok, err := s.imageThreads.Get(threadID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("image thread not found"))
			return
		}
		var req imageThreadUpdateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.Title != nil {
			thread.Title = strings.TrimSpace(*req.Title)
		}
		if req.ImageFolders != nil {
			thread.ImageFolders = req.ImageFolders
		}
		if req.ImageAssets != nil {
			thread.ImageAssets = req.ImageAssets
		}
		if req.ImageAssetOrder != nil {
			thread.ImageAssetOrder = req.ImageAssetOrder
		}
		if req.Metadata != nil {
			thread.Metadata = req.Metadata
		}
		thread, err = s.imageThreads.Update(thread)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "thread": thread})
	default:
		methodNotAllowed(w)
	}
}

func parseImageThreadPath(requestPath string) (string, error) {
	remaining := strings.Trim(strings.TrimPrefix(requestPath, "/v1/workspace/image/threads/"), "/")
	if remaining == "" {
		return "", errors.New("image thread id is required")
	}
	parts := strings.Split(remaining, "/")
	if len(parts) != 1 {
		return "", errors.New("invalid image thread path")
	}
	threadID := strings.TrimSpace(parts[0])
	if threadID == "" {
		return "", errors.New("image thread id is required")
	}
	return threadID, nil
}
