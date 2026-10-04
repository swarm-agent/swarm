package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Inline generated media is retained in the task record, but must not be copied
// into every board refresh (often twice, as both thumbnail and media_url).
// Content-addressed, authenticated URLs preserve the exact bytes without Git
// inspection, remote fetching, a new storage authority, or a public media URL.
func projectDeliverableContentURL(task *pebblestore.ProjectTaskRecord, d pebblestore.ProjectTaskDeliverable, field, value string) string {
	if !strings.HasPrefix(value, "data:") || task.ID == "" || task.ProjectID == "" || d.ID == "" {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	return "/v3/projects/" + url.PathEscape(task.ProjectID) + "/tasks/" + url.PathEscape(task.ID) + "/deliverables/" + url.PathEscape(d.ID) + "?field=" + field + "&sha256=" + hex.EncodeToString(digest[:])
}

func projectDeliverableForClient(task *pebblestore.ProjectTaskRecord, d pebblestore.ProjectTaskDeliverable) pebblestore.ProjectTaskDeliverable {
	d.VideoProvenance = d.VideoProvenance.ClientSafeCopy()
	// Equal fields share a URL so the browser can coalesce the same bytes.
	if d.Thumbnail == d.MediaURL && strings.HasPrefix(d.MediaURL, "data:") {
		d.MediaURL = projectDeliverableContentURL(task, d, "media", d.MediaURL)
		d.Thumbnail = d.MediaURL
	} else {
		d.Thumbnail = projectDeliverableContentURL(task, d, "thumbnail", d.Thumbnail)
		d.MediaURL = projectDeliverableContentURL(task, d, "media", d.MediaURL)
	}
	return d
}

func (s *Server) handleProjectDeliverableContent(w http.ResponseWriter, r *http.Request, p identity.Principal, projectID, taskID, deliverableID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
		return
	}
	db := s.sessions.Store()
	project, found, err := db.GetProject(p.AccountScopeID, projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found || project == nil || project.AccountID != p.AccountScopeID {
		writeError(w, http.StatusNotFound, errors.New("project not found"))
		return
	}
	task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found || task == nil || task.AccountID != p.AccountScopeID || task.ProjectID != projectID {
		writeError(w, http.StatusNotFound, errors.New("task not found"))
		return
	}
	var value string
	field := r.URL.Query().Get("field")
	if field != "thumbnail" && field != "media" {
		writeError(w, http.StatusBadRequest, errors.New("invalid content field"))
		return
	}
	for _, d := range task.Deliverables {
		if d.ID == deliverableID {
			if field == "thumbnail" {
				value = d.Thumbnail
			} else {
				value = d.MediaURL
			}
			break
		}
	}
	digest := sha256.Sum256([]byte(value))
	if value == "" || r.URL.Query().Get("sha256") != hex.EncodeToString(digest[:]) {
		writeError(w, http.StatusNotFound, errors.New("deliverable content reference is stale or unavailable"))
		return
	}
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") || len(encoded) > 128*1024*1024 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("unsupported inline deliverable content"))
		return
	}
	mediaType := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	// Never serve active HTML/SVG/script in the authenticated Desktop origin.
	switch mediaType {
	case "image/png", "image/jpeg", "image/webp", "image/gif", "video/mp4", "video/webm", "audio/mpeg", "audio/mp4", "audio/wav", "audio/ogg", "audio/webm":
	default:
		writeError(w, http.StatusUnsupportedMediaType, errors.New("unsupported deliverable media type"))
		return
	}
	content, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, errors.New("invalid inline media encoding"))
		return
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", `"`+hex.EncodeToString(digest[:])+`"`)
	http.ServeContent(w, r, "deliverable", time.Time{}, bytes.NewReader(content))
}
