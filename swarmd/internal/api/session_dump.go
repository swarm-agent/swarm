package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	SessionDumpPath           = "/v3/developer/session-dump"
	sessionDumpDirectoryName  = "session-dumps"
	sessionDumpMaxMessages    = 1_000_000
	sessionDumpMaxEvents      = 1_000_000
	sessionDumpMaxRunIntents  = 100_000
	sessionDumpMaxPlans       = 100_000
	sessionDumpMaxPermissions = 100_000
)

var sessionDumpFilenameCharacters = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

type sessionDumpHTTPRequest struct {
	SessionID string `json:"session_id"`
}

type sessionDumpFile struct {
	FormatVersion   int                               `json:"format_version"`
	GeneratedAt     string                            `json:"generated_at"`
	Session         pebblestore.SessionSnapshot       `json:"session"`
	Projection      pebblestore.V3SessionProjection   `json:"projection"`
	Messages        []pebblestore.MessageSnapshot     `json:"messages"`
	Events          []pebblestore.V3SessionEvent      `json:"events"`
	RunIntents      []pebblestore.V3SessionRunIntent  `json:"run_intents"`
	CurrentRunState *pebblestore.V3SessionRunState    `json:"current_run_state,omitempty"`
	Plans           []pebblestore.SessionPlanSnapshot `json:"plans"`
	ActivePlanID    string                            `json:"active_plan_id,omitempty"`
	Permissions     []pebblestore.PermissionRecord    `json:"permissions"`
	UsageSummary    *pebblestore.SessionUsageSummary  `json:"usage_summary,omitempty"`
}

func (s *Server) handleSessionDump(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		methodNotAllowed(w)
		return
	}
	if s == nil || s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("session dump service is not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("load startup config: %w", err))
		return
	}
	if !cfg.DevMode {
		writeError(w, http.StatusForbidden, errors.New("session dump requires dev_mode=true in swarm.conf"))
		return
	}
	var request sessionDumpHTTPRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	request.SessionID = strings.TrimSpace(request.SessionID)
	if request.SessionID == "" {
		writeError(w, http.StatusBadRequest, errors.New("session_id is required"))
		return
	}

	dump, found, err := s.buildSessionDump(request.SessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("build session dump: %w", err))
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	path, size, err := s.writeSessionDumpFile(request.SessionID, dump)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("write session dump: %w", err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok":            true,
		"session_id":    request.SessionID,
		"path":          path,
		"bytes_written": size,
	})
}

func (s *Server) buildSessionDump(sessionID string) (sessionDumpFile, bool, error) {
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return sessionDumpFile{}, ok, err
	}
	projection, projectionOK, err := s.sessions.GetSessionProjection(sessionID)
	if err != nil {
		return sessionDumpFile{}, false, err
	}
	if !projectionOK {
		projection = pebblestore.V3SessionProjection{SessionID: sessionID}
	}
	messages, err := s.sessions.ListSessionMessages(sessionID, 0, sessionDumpMaxMessages)
	if err != nil {
		return sessionDumpFile{}, false, err
	}
	events, err := s.sessions.ListSessionEvents(sessionID, 0, sessionDumpMaxEvents)
	if err != nil {
		return sessionDumpFile{}, false, err
	}
	runIntents, err := s.sessions.ListSessionRunIntents(sessionID, 0, sessionDumpMaxRunIntents)
	if err != nil {
		return sessionDumpFile{}, false, err
	}
	var currentRunState *pebblestore.V3SessionRunState
	if state, stateOK, err := s.sessions.GetSessionRunState(sessionID); err != nil {
		return sessionDumpFile{}, false, err
	} else if stateOK {
		currentRunState = &state
	}
	plans, activePlanID, err := s.sessions.ListPlans(sessionID, sessionDumpMaxPlans)
	if err != nil {
		return sessionDumpFile{}, false, err
	}
	permissions := []pebblestore.PermissionRecord{}
	if s.perm != nil {
		permissions, err = s.perm.ListPermissions(sessionID, sessionDumpMaxPermissions)
		if err != nil {
			return sessionDumpFile{}, false, err
		}
	}
	var usageSummary *pebblestore.SessionUsageSummary
	if summary, summaryOK, err := s.sessions.GetUsageSummary(sessionID); err != nil {
		return sessionDumpFile{}, false, err
	} else if summaryOK {
		usageSummary = &summary
	}
	return sessionDumpFile{
		FormatVersion:   1,
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		Session:         session,
		Projection:      projection,
		Messages:        nonNilMessages(messages),
		Events:          nonNilSessionEvents(events),
		RunIntents:      nonNilRunIntents(runIntents),
		CurrentRunState: currentRunState,
		Plans:           nonNilPlans(plans),
		ActivePlanID:    activePlanID,
		Permissions:     nonNilPermissions(permissions),
		UsageSummary:    usageSummary,
	}, true, nil
}

func (s *Server) writeSessionDumpFile(sessionID string, dump sessionDumpFile) (string, int64, error) {
	dataDir := strings.TrimSpace(s.dataDir)
	if dataDir == "" {
		return "", 0, errors.New("daemon data directory is not configured")
	}
	directory := filepath.Join(dataDir, sessionDumpDirectoryName)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", 0, err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", 0, err
	}
	name := safeSessionDumpFilename(sessionID)
	filename := fmt.Sprintf("session-%s-%s.json", name, time.Now().UTC().Format("20060102T150405.000000000Z"))
	path := filepath.Join(directory, filename)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	writeErr := encoder.Encode(dump)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return "", 0, writeErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", 0, closeErr
	}
	info, err := os.Stat(path)
	if err != nil {
		_ = os.Remove(path)
		return "", 0, err
	}
	return path, info.Size(), nil
}

func safeSessionDumpFilename(sessionID string) string {
	name := strings.Trim(sessionDumpFilenameCharacters.ReplaceAllString(strings.TrimSpace(sessionID), "_"), "._-")
	if len(name) > 80 {
		name = name[:80]
	}
	if name == "" {
		return "session"
	}
	return name
}

func nonNilMessages(values []pebblestore.MessageSnapshot) []pebblestore.MessageSnapshot {
	if values == nil {
		return []pebblestore.MessageSnapshot{}
	}
	return values
}

func nonNilSessionEvents(values []pebblestore.V3SessionEvent) []pebblestore.V3SessionEvent {
	if values == nil {
		return []pebblestore.V3SessionEvent{}
	}
	return values
}

func nonNilRunIntents(values []pebblestore.V3SessionRunIntent) []pebblestore.V3SessionRunIntent {
	if values == nil {
		return []pebblestore.V3SessionRunIntent{}
	}
	return values
}

func nonNilPlans(values []pebblestore.SessionPlanSnapshot) []pebblestore.SessionPlanSnapshot {
	if values == nil {
		return []pebblestore.SessionPlanSnapshot{}
	}
	return values
}

func nonNilPermissions(values []pebblestore.PermissionRecord) []pebblestore.PermissionRecord {
	if values == nil {
		return []pebblestore.PermissionRecord{}
	}
	return values
}
