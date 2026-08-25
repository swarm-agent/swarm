package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/videoproject"
)

type workspaceVideoProjectForkRequest struct {
	WorkspacePath        string `json:"workspace_path"`
	SourceSessionID      string `json:"source_session_id"`
	SourceProjectID      string `json:"source_project_id"`
	SourceRevisionID     string `json:"source_revision_id"`
	DestinationSessionID string `json:"destination_session_id"`
	DestinationProjectID string `json:"destination_project_id,omitempty"`
	AttachToSession      bool   `json:"attach_to_session,omitempty"`
}

func (s *Server) handleWorkspaceVideoProjects(w http.ResponseWriter, r *http.Request) {
	if s.videoProjects == nil {
		writeError(w, http.StatusInternalServerError, errors.New("videoproject service is not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	owned, err := s.resolveAccountOwnedPath(principal, r.URL.Query().Get("workspace_path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, parseErr := strconv.Atoi(raw); parseErr == nil && parsed > 0 {
			limit = parsed
		}
	}
	items, err := s.videoProjects.ListWorkspaceCatalog(principal, owned.WorkspacePath, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "videos": items, "count": len(items)})
}

func (s *Server) handleWorkspaceVideoProjectFork(w http.ResponseWriter, r *http.Request) {
	if s.videoProjects == nil {
		writeError(w, http.StatusInternalServerError, errors.New("videoproject service is not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req workspaceVideoProjectForkRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	owned, err := s.resolveAccountOwnedPath(principal, req.WorkspacePath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sourceSession, sourceActive, sourceErr := s.sessions.GetSession(strings.TrimSpace(req.SourceSessionID))
	if sourceErr != nil {
		writeError(w, http.StatusInternalServerError, sourceErr)
		return
	}
	if !sourceActive {
		tombstone, tombstoneFound, tombstoneErr := s.sessions.Store().GetV3SessionTombstone(strings.TrimSpace(req.SourceSessionID))
		if tombstoneErr != nil {
			writeError(w, http.StatusInternalServerError, tombstoneErr)
			return
		}
		if !tombstoneFound || tombstone.Deleted || !tombstone.Archived {
			writeError(w, http.StatusNotFound, errors.New("source session not found"))
			return
		}
		sourceSession = tombstone.Session
	}
	if sourceSession.AccountScopeID != principal.AccountScopeID || (sourceSession.UserID != "" && sourceSession.UserID != principal.UserID) || strings.TrimSpace(sourceSession.WorkspacePath) != owned.WorkspacePath {
		writeError(w, http.StatusNotFound, errors.New("source session not found in current workspace"))
		return
	}
	destination, found, err := s.sessions.GetSession(strings.TrimSpace(req.DestinationSessionID))
	if err != nil || !found || destination.AccountScopeID != principal.AccountScopeID || (destination.UserID != "" && destination.UserID != principal.UserID) || strings.TrimSpace(destination.WorkspacePath) != owned.WorkspacePath {
		writeError(w, http.StatusNotFound, errors.New("destination session not found in current workspace"))
		return
	}
	workspaceID, _ := destination.Metadata["workspace_id"].(string)
	var sessionMetadata map[string]any
	var attachmentMessage *pebblestore.MessageSnapshot
	destinationProjectID := strings.TrimSpace(req.DestinationProjectID)
	destinationRevisionID := ""
	if req.AttachToSession {
		if destinationProjectID == "" {
			destinationProjectID = "vproj_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
		}
		destinationRevisionID = "vrev_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
		sessionMetadata = cloneSessionsV3Metadata(destination.Metadata)
		sessionMetadata["experience"] = "video_studio"
		sessionMetadata["launch_source"] = "video_library"
		sessionMetadata["lineage_kind"] = "video_project"
		sessionMetadata["creative_mode"] = "video"
		sessionMetadata["video_project_id"] = destinationProjectID
		sessionMetadata["video_revision_id"] = destinationRevisionID
		sessionMetadata["source_session_id"] = strings.TrimSpace(req.SourceSessionID)
		sessionMetadata["source_video_project_id"] = strings.TrimSpace(req.SourceProjectID)
		sessionMetadata["source_video_revision_id"] = strings.TrimSpace(req.SourceRevisionID)
		sessionMetadata["video_context"] = map[string]any{
			"source_session_id":       strings.TrimSpace(req.SourceSessionID),
			"source_project_id":       strings.TrimSpace(req.SourceProjectID),
			"source_revision_id":      strings.TrimSpace(req.SourceRevisionID),
			"destination_project_id":  destinationProjectID,
			"destination_revision_id": destinationRevisionID,
		}
		messageID := "video-attachment-" + uuid.NewString()
		attachmentMessage = &pebblestore.MessageSnapshot{
			ID: messageID, Role: "system",
			Content: "Attached the selected exact video revision to this Video Studio session. Use the durable destination project and revision below as the starting context for the user's next request.",
			Metadata: map[string]any{
				"source": "video_library_attachment", "creative_mode": "video",
				"video_project_id": destinationProjectID, "video_revision_id": destinationRevisionID,
				"source_session_id":        strings.TrimSpace(req.SourceSessionID),
				"source_video_project_id":  strings.TrimSpace(req.SourceProjectID),
				"source_video_revision_id": strings.TrimSpace(req.SourceRevisionID),
			},
		}
	}
	project, revision, err := s.videoProjects.ForkRevision(r.Context(), principal, videoproject.ForkRevisionInput{
		SourceSessionID: strings.TrimSpace(req.SourceSessionID), SourceProjectID: strings.TrimSpace(req.SourceProjectID), SourceRevisionID: strings.TrimSpace(req.SourceRevisionID),
		DestinationSessionID: destination.ID, DestinationWorkspaceID: strings.TrimSpace(workspaceID), ProjectID: destinationProjectID, InitialRevisionID: destinationRevisionID,
		SessionMetadata: sessionMetadata, AttachmentMessage: attachmentMessage,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "session_id": destination.ID, "project": project, "revision": revision})
}
