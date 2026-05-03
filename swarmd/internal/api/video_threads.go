package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type videoThreadCreateRequest struct {
	Title          string                          `json:"title"`
	WorkspacePath  string                          `json:"workspace_path"`
	WorkspaceName  string                          `json:"workspace_name"`
	VideoFolders   []string                        `json:"video_folders"`
	VideoClips     []pebblestore.VideoClipSnapshot `json:"video_clips"`
	VideoClipOrder []string                        `json:"video_clip_order"`
	Metadata       map[string]any                  `json:"metadata"`
}

type videoThreadUpdateRequest struct {
	Title          *string                         `json:"title"`
	VideoFolders   []string                        `json:"video_folders"`
	VideoClips     []pebblestore.VideoClipSnapshot `json:"video_clips"`
	VideoClipOrder []string                        `json:"video_clip_order"`
	Metadata       map[string]any                  `json:"metadata"`
}

func (s *Server) handleWorkspaceVideoThreads(w http.ResponseWriter, r *http.Request) {
	if s.videoThreads == nil {
		writeError(w, http.StatusInternalServerError, errors.New("video thread store is not configured"))
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
		threads, err := s.videoThreads.ListForWorkspace(workspacePath, 200)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "threads": threads})
	case http.MethodPost:
		var req videoThreadCreateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		workspacePath := strings.TrimSpace(req.WorkspacePath)
		if workspacePath == "" {
			writeError(w, http.StatusBadRequest, errors.New("workspace path is required"))
			return
		}
		thread, err := s.videoThreads.Create(pebblestore.VideoThreadSnapshot{
			ID:             session.NewSessionID(),
			WorkspacePath:  workspacePath,
			WorkspaceName:  strings.TrimSpace(req.WorkspaceName),
			Title:          strings.TrimSpace(req.Title),
			VideoFolders:   req.VideoFolders,
			VideoClips:     req.VideoClips,
			VideoClipOrder: req.VideoClipOrder,
			Metadata:       req.Metadata,
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

func (s *Server) handleWorkspaceVideoThread(w http.ResponseWriter, r *http.Request) {
	if s.videoThreads == nil {
		writeError(w, http.StatusInternalServerError, errors.New("video thread store is not configured"))
		return
	}
	threadID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/workspace/video/threads/"), "/")
	if threadID == "" || strings.Contains(threadID, "/") {
		writeError(w, http.StatusBadRequest, errors.New("video thread id is required"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		thread, ok, err := s.videoThreads.Get(threadID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("video thread not found"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "thread": thread})
	case http.MethodPost:
		thread, ok, err := s.videoThreads.Get(threadID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("video thread not found"))
			return
		}
		var req videoThreadUpdateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.Title != nil {
			thread.Title = strings.TrimSpace(*req.Title)
		}
		if req.VideoFolders != nil {
			thread.VideoFolders = req.VideoFolders
		}
		if req.VideoClips != nil {
			thread.VideoClips = req.VideoClips
		}
		if req.VideoClipOrder != nil {
			thread.VideoClipOrder = req.VideoClipOrder
		}
		if req.Metadata != nil {
			thread.Metadata = req.Metadata
		}
		thread, err = s.videoThreads.Update(thread)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "thread": thread})
	default:
		methodNotAllowed(w)
	}
}
