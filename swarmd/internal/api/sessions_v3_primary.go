package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const sessionsV3PrimaryPrefix = "/v3/sessions/"

type sessionsV3CreateRequest struct {
	SessionID       string                      `json:"session_id,omitempty"`
	ClientRequestID string                      `json:"client_request_id,omitempty"`
	IdempotencyKey  string                      `json:"idempotency_key,omitempty"`
	Title           string                      `json:"title,omitempty"`
	WorkspacePath   string                      `json:"workspace_path"`
	WorkspaceName   string                      `json:"workspace_name,omitempty"`
	Mode            string                      `json:"mode,omitempty"`
	Preference      pebblestore.ModelPreference `json:"preference,omitempty"`
	Metadata        map[string]any              `json:"metadata,omitempty"`
}

type sessionsV3HydratedSession struct {
	Session    pebblestore.SessionSnapshot      `json:"session"`
	Projection sessionruntime.SessionProjection `json:"projection"`
	Messages   []pebblestore.MessageSnapshot    `json:"messages"`
	Events     []sessionruntime.SessionEvent    `json:"events"`
}

func (s *Server) handleSessionsV3Primary(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleSessionsV3PrimaryList(w, r, principal)
	case http.MethodPost:
		s.handleSessionsV3PrimaryCreate(w, r, principal)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleSessionV3PrimaryByID(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	sessionID, ok := parseSessionsV3PrimarySessionID(r.URL.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, errors.New("invalid sessions v3 path"))
		return
	}
	hydrated, found, err := s.hydrateSessionsV3Primary(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"session":    hydrated.Session,
		"projection": hydrated.Projection,
		"messages":   hydrated.Messages,
		"events":     hydrated.Events,
	})
}

func (s *Server) handleSessionsV3PrimaryCreate(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
	var req sessionsV3CreateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	clientRequestID := strings.TrimSpace(firstNonEmpty(req.ClientRequestID, req.IdempotencyKey, r.Header.Get("Idempotency-Key")))
	if clientRequestID == "" {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id is required"))
		return
	}
	if sessionID == "" {
		sessionID = stableSessionsV3PrimarySessionID(principal, clientRequestID)
	}
	workspacePath := strings.TrimSpace(req.WorkspacePath)
	if workspacePath == "" {
		writeError(w, http.StatusBadRequest, errors.New("workspace_path is required"))
		return
	}
	if err := validateSessionsV3CreateMetadata(req.Metadata); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	workspaceName := strings.TrimSpace(req.WorkspaceName)
	if workspaceName == "" {
		workspaceName = filepath.Base(workspacePath)
		if workspaceName == "." || workspaceName == string(filepath.Separator) {
			workspaceName = "workspace"
		}
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "New Session"
	}
	now := time.Now().UnixMilli()
	session := pebblestore.SessionSnapshot{
		ID:             sessionID,
		UserID:         strings.TrimSpace(principal.UserID),
		AccountScopeID: strings.TrimSpace(principal.AccountScopeID),
		WorkspacePath:  workspacePath,
		WorkspaceName:  workspaceName,
		Title:          title,
		Mode:           sessionruntime.NormalizeMode(req.Mode),
		Preference:     normalizeSessionsV3ModelPreference(req.Preference),
		Metadata:       cloneSessionsV3Metadata(req.Metadata),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	payloadHash, err := sessionsV3CreatePayloadHash(sessionID, req, workspaceName, title)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &session,
		NowUnixMs:       now,
	})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "conflict": result.Conflict, "result": result})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	hydrated, found, err := s.hydrateSessionsV3Primary(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeError(w, http.StatusInternalServerError, errors.New("created sessions v3 projection was not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"session":    hydrated.Session,
		"projection": hydrated.Projection,
		"messages":   hydrated.Messages,
		"events":     hydrated.Events,
		"mutation":   result,
	})
}

func (s *Server) handleSessionsV3PrimaryList(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
	limit, ok := parseSessionsV2PositiveLimit(w, r, 100)
	if !ok {
		return
	}
	sessions, err := s.sessions.ListSessionsForAccount(principal.AccountScopeID, limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	items := make([]map[string]any, 0, len(sessions))
	for _, item := range sessions {
		projection, projectionOK, err := s.sessions.GetSessionProjection(item.ID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !projectionOK {
			continue
		}
		items = append(items, map[string]any{"session": item, "projection": projection})
		if len(items) >= limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions": items})
}

func (s *Server) hydrateSessionsV3Primary(principal identity.Principal, sessionID string) (sessionsV3HydratedSession, bool, error) {
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return sessionsV3HydratedSession{}, ok, err
	}
	if strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionsV3HydratedSession{}, false, nil
	}
	projection, projectionOK, err := s.sessions.GetSessionProjection(sessionID)
	if err != nil || !projectionOK {
		return sessionsV3HydratedSession{}, projectionOK, err
	}
	messages, err := s.sessions.ListSessionMessages(sessionID, 0, 500)
	if err != nil {
		return sessionsV3HydratedSession{}, false, err
	}
	events, err := s.sessions.ListSessionEvents(sessionID, 0, 500)
	if err != nil {
		return sessionsV3HydratedSession{}, false, err
	}
	return sessionsV3HydratedSession{Session: session, Projection: projection, Messages: messages, Events: events}, true, nil
}

func parseSessionsV3PrimarySessionID(path string) (string, bool) {
	if !strings.HasPrefix(path, sessionsV3PrimaryPrefix) {
		return "", false
	}
	rest := strings.TrimPrefix(path, sessionsV3PrimaryPrefix)
	if rest == "" || strings.HasPrefix(rest, "/") || strings.HasSuffix(rest, "/") || strings.Contains(rest, "/") {
		return "", false
	}
	return strings.TrimSpace(rest), strings.TrimSpace(rest) != ""
}

func sessionsV3CreatePayloadHash(sessionID string, req sessionsV3CreateRequest, workspaceName, title string) (string, error) {
	canonical := struct {
		Operation     string                      `json:"operation"`
		SessionID     string                      `json:"session_id"`
		Title         string                      `json:"title"`
		WorkspacePath string                      `json:"workspace_path"`
		WorkspaceName string                      `json:"workspace_name"`
		Mode          string                      `json:"mode"`
		Preference    pebblestore.ModelPreference `json:"preference"`
		Metadata      map[string]any              `json:"metadata,omitempty"`
	}{
		Operation:     sessionruntime.SessionMutationCreateSession,
		SessionID:     strings.TrimSpace(sessionID),
		Title:         title,
		WorkspacePath: strings.TrimSpace(req.WorkspacePath),
		WorkspaceName: workspaceName,
		Mode:          sessionruntime.NormalizeMode(req.Mode),
		Preference:    normalizeSessionsV3ModelPreference(req.Preference),
		Metadata:      cloneSessionsV3Metadata(req.Metadata),
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal sessions v3 create payload hash: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func stableSessionsV3PrimarySessionID(principal identity.Principal, clientRequestID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(principal.AccountScopeID) + "\x00" + strings.TrimSpace(clientRequestID)))
	return "v3session_" + hex.EncodeToString(sum[:16])
}

func normalizeSessionsV3ModelPreference(pref pebblestore.ModelPreference) pebblestore.ModelPreference {
	pref.Provider = strings.TrimSpace(pref.Provider)
	pref.Model = strings.TrimSpace(pref.Model)
	pref.Thinking = strings.TrimSpace(pref.Thinking)
	pref.ServiceTier = strings.TrimSpace(pref.ServiceTier)
	pref.ContextMode = strings.TrimSpace(pref.ContextMode)
	pref.AccountScopeID = ""
	pref.UserID = ""
	pref.UpdatedAt = 0
	return pref
}

func validateSessionsV3CreateMetadata(metadata map[string]any) error {
	for key := range metadata {
		if isProtectedSessionsV3MetadataKey(key) {
			return fmt.Errorf("metadata key %q is reserved for primary authority state", key)
		}
	}
	return nil
}

func isProtectedSessionsV3MetadataKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "workspace_binding_id",
		"local_workspace_binding_id",
		"source_workspace_id",
		"source_workspace_path",
		"runtime_workspace_path",
		"runtime_swarm_id",
		"runtime_kind",
		"authority_host_swarm_id",
		"authority_container_id",
		"target_swarm_id",
		"target_kind",
		"target_name",
		"swarm_target_swarm_id",
		"route",
		"routes",
		"topology",
		"swarm_v2_execution_class",
		"swarm_v2_runtime_swarm_id",
		"swarm_v2_runtime_kind",
		"swarm_v2_authority_host_swarm_id",
		"swarm_v2_authority_container_id",
		"swarm_v2_source_workspace_path",
		"swarm_v2_runtime_workspace_path",
		"swarm_v2_workspace_binding_id",
		"swarm_v3_execution_class",
		"swarm_v3_runtime_swarm_id",
		"swarm_v3_authority_host_swarm_id",
		"swarm_v3_authority_container_id":
		return true
	default:
		return false
	}
}

func cloneSessionsV3Metadata(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
