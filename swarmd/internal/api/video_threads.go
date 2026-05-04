package api

import (
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
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
		threadID := session.NewSessionID()
		storagePath, err := ensureWorkspaceToolStorage(workspacePath, "video", threadID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		metadata := ensureManagedToolStorageMetadata(req.Metadata, storagePath)
		thread, err := s.videoThreads.Create(pebblestore.VideoThreadSnapshot{
			ID:             threadID,
			WorkspacePath:  workspacePath,
			WorkspaceName:  strings.TrimSpace(req.WorkspaceName),
			Title:          strings.TrimSpace(req.Title),
			VideoFolders:   ensureManagedToolStorageFolders(workspacePath, storagePath, req.VideoFolders),
			VideoClips:     req.VideoClips,
			VideoClipOrder: req.VideoClipOrder,
			Metadata:       metadata,
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
	threadID, clipID, isClipMedia, err := parseVideoThreadPath(r.URL.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if isClipMedia {
		s.handleWorkspaceVideoClipMedia(w, r, threadID, clipID)
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
			if storagePath, ok := thread.Metadata["tool_storage_path"].(string); ok && strings.TrimSpace(storagePath) != "" {
				thread.VideoFolders = ensureManagedToolStorageFolders(thread.WorkspacePath, storagePath, req.VideoFolders)
			}
		}
		if req.VideoClips != nil {
			thread.VideoClips = req.VideoClips
		}
		if req.VideoClipOrder != nil {
			thread.VideoClipOrder = req.VideoClipOrder
		}
		if req.Metadata != nil {
			if storagePath, ok := thread.Metadata["tool_storage_path"].(string); ok && strings.TrimSpace(storagePath) != "" {
				thread.Metadata = ensureManagedToolStorageMetadata(req.Metadata, storagePath)
			}
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

func parseVideoThreadPath(requestPath string) (threadID string, clipID string, isClipMedia bool, err error) {
	remaining := strings.Trim(strings.TrimPrefix(requestPath, "/v1/workspace/video/threads/"), "/")
	if remaining == "" {
		return "", "", false, errors.New("video thread id is required")
	}
	parts := strings.Split(remaining, "/")
	if len(parts) == 1 {
		threadID = strings.TrimSpace(parts[0])
		if threadID == "" {
			return "", "", false, errors.New("video thread id is required")
		}
		return threadID, "", false, nil
	}
	if len(parts) == 3 && parts[1] == "clips" && parts[2] == "media" {
		threadID = strings.TrimSpace(parts[0])
		if threadID == "" {
			return "", "", false, errors.New("video thread id is required")
		}
		return threadID, "", true, nil
	}
	if len(parts) == 4 && parts[1] == "clips" && parts[3] == "media" {
		threadID = strings.TrimSpace(parts[0])
		clipID = strings.TrimSpace(parts[2])
		if threadID == "" || clipID == "" {
			return "", "", false, errors.New("video thread id and clip id are required")
		}
		return threadID, clipID, true, nil
	}
	return "", "", false, errors.New("invalid video thread path")
}

func (s *Server) handleWorkspaceVideoClipMedia(w http.ResponseWriter, r *http.Request, threadID string, clipID string) {
	if clipID == "" {
		clipID = strings.TrimSpace(r.URL.Query().Get("clip_id"))
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	thread, ok, err := s.videoThreads.Get(threadID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("video thread not found"))
		return
	}
	clip, ok := findVideoThreadClip(thread, clipID)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("video clip not found"))
		return
	}
	clipPath, err := resolveVideoClipFilePath(clip.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	file, err := os.Open(clipPath)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, errors.New("video clip path must be a file"))
		return
	}
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(clipPath)))
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func findVideoThreadClip(thread pebblestore.VideoThreadSnapshot, clipID string) (pebblestore.VideoClipSnapshot, bool) {
	clipID = strings.TrimSpace(clipID)
	for _, clip := range thread.VideoClips {
		if clip.ID == clipID {
			return clip, true
		}
	}
	return pebblestore.VideoClipSnapshot{}, false
}

func resolveVideoClipFilePath(clipPath string) (string, error) {
	clipPath = strings.TrimSpace(clipPath)
	if clipPath == "" {
		return "", errors.New("video clip path is required")
	}
	absClipPath, err := filepath.Abs(clipPath)
	if err != nil {
		return "", err
	}
	clipPath = filepath.Clean(absClipPath)
	ext := strings.ToLower(filepath.Ext(clipPath))
	if _, ok := acceptedVideoExtensions[ext]; !ok {
		return "", errors.New("video clip extension is not accepted")
	}
	return clipPath, nil
}
