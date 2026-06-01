package api

import (
	"errors"
	"net/http"
	"strings"
)

const (
	runtimeSessionsV2OpenPath = "/v2/internal/runtime-sessions/open"
	runtimeSessionsV2Prefix   = "/v2/internal/runtime-sessions/"
)

func (s *Server) handleRuntimeSessionsV2Open(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != runtimeSessionsV2OpenPath {
		writeError(w, http.StatusNotFound, errors.New("runtime sessions v2 route not found"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	writeRuntimeSessionsV2NotImplemented(w, "runtime session open is not implemented")
}

func (s *Server) handleRuntimeSessionsV2ByID(w http.ResponseWriter, r *http.Request) {
	sessionID, action, ok := parseRuntimeSessionsV2ByIDPath(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("runtime sessions v2 route not found"))
		return
	}
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, errors.New("runtime session id is required"))
		return
	}

	switch action {
	case "sync/state":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		writeRuntimeSessionsV2NotImplemented(w, "runtime session sync state is not implemented")
	case "run":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		writeRuntimeSessionsV2NotImplemented(w, "runtime session run is not implemented")
	case "run/stream":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		writeRuntimeSessionsV2NotImplemented(w, "runtime session run stream is not implemented")
	case "mirror/batch":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		writeRuntimeSessionsV2NotImplemented(w, "runtime session mirror batch is not implemented")
	default:
		writeError(w, http.StatusNotFound, errors.New("runtime sessions v2 route not found"))
	}
}

func parseRuntimeSessionsV2ByIDPath(rawPath string) (string, string, bool) {
	if !strings.HasPrefix(rawPath, runtimeSessionsV2Prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(rawPath, runtimeSessionsV2Prefix)
	if rest == "" {
		return "", "", false
	}
	parts := strings.SplitN(rest, "/", 2)
	sessionID := parts[0]
	if strings.TrimSpace(sessionID) == "" {
		return "", "", true
	}
	if sessionID != strings.TrimSpace(sessionID) {
		return "", "", false
	}
	if len(parts) != 2 {
		return sessionID, "", false
	}
	action := parts[1]
	if action == "" || action != strings.TrimSpace(action) || strings.HasPrefix(action, "/") || strings.HasSuffix(action, "/") {
		return sessionID, "", false
	}
	return sessionID, action, true
}

func writeRuntimeSessionsV2NotImplemented(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusNotImplemented, map[string]any{
		"ok":    false,
		"code":  "runtime_session_not_implemented",
		"error": message,
	})
}
