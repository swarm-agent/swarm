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

// V3 primary write handlers delegate through the ApplySessionMutation boundary.

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

type sessionsV3MessageRequest struct {
	ClientRequestID   string         `json:"client_request_id,omitempty"`
	IdempotencyKey    string         `json:"idempotency_key,omitempty"`
	MessageID         string         `json:"message_id,omitempty"`
	RunID             string         `json:"run_id,omitempty"`
	Role              string         `json:"role"`
	Content           string         `json:"content"`
	Metadata          map[string]any `json:"metadata,omitempty"`
	DispatchAuthority map[string]any `json:"dispatch_authority,omitempty"`
	Authority         map[string]any `json:"authority,omitempty"`
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
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	sessionID, subpath, ok := parseSessionsV3PrimaryPath(r.URL.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, errors.New("invalid sessions v3 path"))
		return
	}
	switch subpath {
	case "":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
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
	case "messages":
		s.handleSessionV3PrimaryMessages(w, r, principal, sessionID)
	case "events":
		s.handleSessionV3PrimaryEvents(w, r, principal, sessionID)
	case "stream":
		s.handleSessionV3PrimaryStream(w, r, principal, sessionID)
	default:
		writeError(w, http.StatusBadRequest, errors.New("unknown sessions v3 path"))
	}
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
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
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

func (s *Server) handleSessionV3PrimaryMessages(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if r.Method == http.MethodGet {
		afterSeq, limit, ok := parseAfterSeqAndLimit(w, r, 500)
		if !ok {
			return
		}
		if _, found, err := s.hydrateSessionsV3Primary(principal, sessionID); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		} else if !found {
			writeSessionNotFound(w)
			return
		}
		messages, err := s.sessions.ListSessionMessages(sessionID, afterSeq, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "messages": messages})
		return
	}

	var req sessionsV3MessageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	clientRequestID := strings.TrimSpace(firstNonEmpty(req.ClientRequestID, req.IdempotencyKey, r.Header.Get("Idempotency-Key")))
	if clientRequestID == "" {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id is required"))
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
	if err := validateSessionsV3CreateMetadata(req.Metadata); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	message := pebblestore.MessageSnapshot{
		ID:       strings.TrimSpace(req.MessageID),
		Role:     strings.TrimSpace(req.Role),
		Content:  req.Content,
		Metadata: cloneSessionsV3Metadata(req.Metadata),
	}
	if message.Role == "" {
		writeError(w, http.StatusBadRequest, errors.New("message role is required"))
		return
	}
	if message.Content == "" {
		writeError(w, http.StatusBadRequest, errors.New("message content is required"))
		return
	}
	now := time.Now().UnixMilli()
	runStatus, blockedReason := sessionsV3PrimaryRunIntentStatus(req)
	runIntent := &pebblestore.V3SessionRunIntent{
		RunID:         strings.TrimSpace(req.RunID),
		Status:        runStatus,
		BlockedReason: blockedReason,
	}
	payloadHash, err := sessionsV3MessagePayloadHash(sessionID, req, message, runIntent.Status, runIntent.BlockedReason)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationAppendMessage,
		Message:         &message,
		RunIntent:       runIntent,
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
	updated, found, err := s.hydrateSessionsV3Primary(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeError(w, http.StatusInternalServerError, errors.New("updated sessions v3 projection was not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"session":    updated.Session,
		"projection": updated.Projection,
		"message":    result.Message,
		"run_intent": result.RunIntent,
		"messages":   updated.Messages,
		"events":     updated.Events,
		"mutation":   result,
		"previous":   hydrated.Projection,
	})
}

func (s *Server) handleSessionV3PrimaryEvents(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	afterSeq, limit, ok := parseAfterSeqAndLimit(w, r, 500)
	if !ok {
		return
	}
	if _, found, err := s.hydrateSessionsV3Primary(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	replay, err := s.sessions.ReplaySessionEvents(sessionID, afterSeq, limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"session_id":         sessionID,
		"events":             replay.Events,
		"projection":         replay.Projection,
		"lifecycle":          replay.Lifecycle,
		"messages":           replay.Messages,
		"run_intents":        replay.RunIntents,
		"high_watermark_seq": replay.HighWatermarkSeq,
		"next_seq":           replay.NextSeq,
	})
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

func parseSessionsV3PrimaryPath(path string) (string, string, bool) {
	if !strings.HasPrefix(path, sessionsV3PrimaryPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, sessionsV3PrimaryPrefix)
	if rest == "" || strings.HasPrefix(rest, "/") || strings.HasSuffix(rest, "/") || strings.Contains(rest, "//") {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return "", "", false
		}
	}
	sessionID := strings.TrimSpace(parts[0])
	if sessionID == "" {
		return "", "", false
	}
	if len(parts) == 1 {
		return sessionID, "", true
	}
	return sessionID, strings.Join(parts[1:], "/"), true
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

func sessionsV3MessagePayloadHash(sessionID string, req sessionsV3MessageRequest, message pebblestore.MessageSnapshot, runStatus, blockedReason string) (string, error) {
	canonical := struct {
		Operation         string         `json:"operation"`
		SessionID         string         `json:"session_id"`
		MessageID         string         `json:"message_id,omitempty"`
		RunID             string         `json:"run_id,omitempty"`
		Role              string         `json:"role"`
		Content           string         `json:"content"`
		Metadata          map[string]any `json:"metadata,omitempty"`
		RunStatus         string         `json:"run_status"`
		BlockedReason     string         `json:"blocked_reason"`
		AuthorityStatus   string         `json:"authority_status"`
		Authority         map[string]any `json:"authority,omitempty"`
		DispatchAuthority map[string]any `json:"dispatch_authority,omitempty"`
	}{
		Operation:         sessionruntime.SessionMutationAppendMessage,
		SessionID:         strings.TrimSpace(sessionID),
		MessageID:         strings.TrimSpace(message.ID),
		RunID:             strings.TrimSpace(req.RunID),
		Role:              strings.TrimSpace(message.Role),
		Content:           message.Content,
		Metadata:          cloneSessionsV3Metadata(message.Metadata),
		RunStatus:         runStatus,
		BlockedReason:     blockedReason,
		AuthorityStatus:   sessionsV3PrimaryAuthorityStatus(req),
		Authority:         cloneSessionsV3Metadata(req.Authority),
		DispatchAuthority: cloneSessionsV3Metadata(req.DispatchAuthority),
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal sessions v3 message payload hash: %w", err)
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

func sessionsV3PrimaryAuthorityStatus(req sessionsV3MessageRequest) string {
	if len(req.DispatchAuthority) > 0 || len(req.Authority) > 0 {
		return "invalid"
	}
	return "absent"
}

func sessionsV3PrimaryRunIntentStatus(req sessionsV3MessageRequest) (string, string) {
	if sessionsV3PrimaryAuthorityStatus(req) == "invalid" {
		return sessionruntime.RunIntentDispatchBlocked, "invalid dispatch authority for primary-owned v3 stage 1"
	}
	return sessionruntime.RunIntentPendingExecutor, ""
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
