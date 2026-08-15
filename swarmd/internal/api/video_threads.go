package api

import (
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videosource"
)

type videoThreadCreateRequest struct {
	SessionID      string                          `json:"session_id"`
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
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
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
		owned, err := s.resolveAccountOwnedPath(principal, workspacePath)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		threads, err := s.videoThreads.ListForWorkspaceForAccount(principal.AccountScopeID, owned.WorkspacePath, 200)
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
		owned, err := s.resolveAccountOwnedPath(principal, req.WorkspacePath)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		workspacePath := owned.WorkspacePath
		threadID := strings.TrimSpace(req.SessionID)
		if threadID == "" {
			writeError(w, http.StatusBadRequest, errors.New("session_id is required; video threads are session-owned"))
			return
		}
		boundSession, found, sessionErr := s.sessions.GetSession(threadID)
		if sessionErr != nil {
			writeError(w, http.StatusInternalServerError, sessionErr)
			return
		}
		if !found || boundSession.AccountScopeID != principal.AccountScopeID || (boundSession.UserID != "" && boundSession.UserID != principal.UserID) {
			writeError(w, http.StatusBadRequest, errors.New("video session does not belong to the authenticated principal"))
			return
		}
		if filepath.Clean(strings.TrimSpace(boundSession.WorkspacePath)) != filepath.Clean(workspacePath) {
			writeError(w, http.StatusBadRequest, errors.New("video session workspace does not match requested workspace"))
			return
		}
		storagePath, err := ensureWorkspaceToolStorage(workspacePath, "video", threadID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		metadata := ensureManagedToolStorageMetadata(req.Metadata, storagePath)
		req.VideoClips = sanitizeVideoThreadSourceClips(req.VideoClips)
		thread, err := s.videoThreads.CreateForAccount(principal.AccountScopeID, principal.UserID, pebblestore.VideoThreadSnapshot{
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
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	threadID, clipID, isClipMedia, err := parseVideoThreadPath(r.URL.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if isClipMedia {
		s.handleWorkspaceVideoClipMedia(w, r, principal, threadID, clipID)
		return
	}
	switch r.Method {
	case http.MethodGet:
		thread, ok, err := s.videoThreads.GetForAccount(principal.AccountScopeID, threadID)
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
		thread, ok, err := s.videoThreads.GetForAccount(principal.AccountScopeID, threadID)
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
			thread.VideoClips = sanitizeVideoThreadSourceClips(req.VideoClips)
		}
		if req.VideoClipOrder != nil {
			thread.VideoClipOrder = req.VideoClipOrder
		}
		if req.Metadata != nil {
			if storagePath, ok := thread.Metadata["tool_storage_path"].(string); ok && strings.TrimSpace(storagePath) != "" {
				thread.Metadata = ensureManagedToolStorageMetadata(req.Metadata, storagePath)
			}
		}
		thread, err = s.videoThreads.UpdateForAccount(principal.AccountScopeID, thread)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "thread": thread})
	default:
		methodNotAllowed(w)
	}
}

func sanitizeVideoThreadSourceClips(clips []pebblestore.VideoClipSnapshot) []pebblestore.VideoClipSnapshot {
	out := make([]pebblestore.VideoClipSnapshot, 0, len(clips))
	for _, clip := range clips {
		clip.SourceRef = strings.TrimSpace(clip.SourceRef)
		if clip.SourceRef != "" {
			clip.ID = clip.SourceRef
			clip.Path = ""
		}
		out = append(out, clip)
	}
	return out
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

func (s *Server) handleWorkspaceVideoClipMedia(w http.ResponseWriter, r *http.Request, principal identity.Principal, threadID string, clipID string) {
	if clipID == "" {
		clipID = strings.TrimSpace(r.URL.Query().Get("clip_id"))
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	thread, ok, err := s.videoThreads.GetForAccount(principal.AccountScopeID, threadID)
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
	var clipPath string
	var file *os.File
	if strings.TrimSpace(clip.SourceRef) != "" {
		owned, resolveErr := s.resolveAccountOwnedPath(principal, thread.WorkspacePath)
		if resolveErr != nil {
			writeError(w, http.StatusBadRequest, resolveErr)
			return
		}
		record, found, sourceErr := s.sessions.Store().GetVideoSourceRecord(principal.AccountScopeID, owned.Scope.WorkspaceID, clip.SourceRef)
		if sourceErr != nil || !found {
			writeError(w, http.StatusNotFound, errors.New("video source is no longer registered"))
			return
		}
		file, sourceErr = pebblestore.OpenValidatedVideoSource(record)
		if sourceErr != nil {
			writeError(w, http.StatusBadRequest, sourceErr)
			return
		}
		clipPath = record.RelativePath
	} else {
		clipPath, file, err = openManagedVideoClip(thread, clip.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
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
	if !videosource.IsAcceptedVideoExtension(ext) {
		return "", errors.New("video clip extension is not accepted")
	}
	return clipPath, nil
}

func managedVideoStoragePath(thread pebblestore.VideoThreadSnapshot) (string, error) {
	storagePath, ok := thread.Metadata["tool_storage_path"].(string)
	storagePath = filepath.Clean(strings.TrimSpace(storagePath))
	if !ok || storagePath == "" || storagePath == "." {
		return "", errors.New("video session storage path is not available")
	}
	expectedPath, err := ensureWorkspaceToolStorage(thread.WorkspacePath, "video", thread.ID)
	if err != nil {
		return "", err
	}
	if storagePath != filepath.Clean(expectedPath) {
		return "", errors.New("video session storage path is outside managed thread storage")
	}
	return storagePath, nil
}

func openManagedVideoClip(thread pebblestore.VideoThreadSnapshot, clipPath string) (string, *os.File, error) {
	storagePath, err := managedVideoStoragePath(thread)
	if err != nil {
		return "", nil, err
	}
	clipPath, err = resolveVideoClipFilePath(clipPath)
	if err != nil {
		return "", nil, err
	}
	relative, err := filepath.Rel(storagePath, clipPath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", nil, errors.New("video clip path escapes managed thread storage")
	}
	root, err := os.OpenRoot(storagePath)
	if err != nil {
		return "", nil, err
	}
	file, err := root.Open(relative)
	root.Close()
	if err != nil {
		return "", nil, err
	}
	return clipPath, file, nil
}
