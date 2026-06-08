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
	runruntime "swarm/packages/swarmd/internal/run"
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
	AgentName       string                      `json:"agent_name,omitempty"`
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

type sessionsV3StopRequest struct {
	Type   string `json:"type,omitempty"`
	RunID  string `json:"run_id"`
	Reason string `json:"reason,omitempty"`
}

type sessionsV3ModeRequest struct {
	Mode string `json:"mode"`
}

type sessionsV3AgentRequest struct {
	AgentName string `json:"agent_name"`
}

type sessionsV3PreferenceRequest struct {
	Provider    *string `json:"provider,omitempty"`
	Model       *string `json:"model,omitempty"`
	Thinking    *string `json:"thinking,omitempty"`
	ServiceTier *string `json:"service_tier,omitempty"`
	ContextMode *string `json:"context_mode,omitempty"`
}

type sessionsV3MetadataRequest struct {
	Metadata map[string]any `json:"metadata"`
}

type sessionsV3PlanUpsertRequest struct {
	ID            string                            `json:"id"`
	PlanID        string                            `json:"plan_id"`
	Title         string                            `json:"title"`
	Plan          string                            `json:"plan"`
	Document      *pebblestore.SessionPlanDocument  `json:"document"`
	DocumentPatch *sessionruntime.PlanDocumentPatch `json:"document_patch"`
	Status        string                            `json:"status"`
	ApprovalState string                            `json:"approval_state"`
	UpdateSummary string                            `json:"update_summary"`
	UpdateScope   string                            `json:"update_scope"`
	Scope         string                            `json:"scope"`
	UpdateKind    string                            `json:"update_kind"`
	Checkpoint    bool                              `json:"checkpoint"`
	Activate      *bool                             `json:"activate"`
}

type sessionsV3HydratedSession struct {
	Session            pebblestore.SessionSnapshot       `json:"session"`
	Projection         sessionruntime.SessionProjection  `json:"projection"`
	Messages           []pebblestore.MessageSnapshot     `json:"messages"`
	Events             []sessionruntime.SessionEvent     `json:"events"`
	PendingPermissions []pebblestore.PermissionRecord    `json:"pending_permissions"`
	UsageSummary       *pebblestore.SessionUsageSummary  `json:"usage_summary,omitempty"`
	Preference         pebblestore.ModelPreference       `json:"preference"`
	ContextWindow      int                               `json:"context_window"`
	MaxOutputTokens    int                               `json:"max_output_tokens"`
	AgentModelPolicy   sessionsV3AgentModelPolicy        `json:"agent_model_policy"`
	HasActivePlan      bool                              `json:"has_active_plan"`
	ActivePlan         pebblestore.SessionPlanSnapshot   `json:"active_plan,omitempty"`
	PlanRevisions      []pebblestore.SessionPlanSnapshot `json:"plan_revisions"`
}

type sessionsV3AgentModelPolicy struct {
	AgentName       string                      `json:"agent_name"`
	ResolvedAgent   string                      `json:"resolved_agent_name"`
	Source          string                      `json:"source"`
	Locked          bool                        `json:"locked"`
	Reason          string                      `json:"reason,omitempty"`
	Preference      pebblestore.ModelPreference `json:"preference"`
	ContextWindow   int                         `json:"context_window"`
	MaxOutputTokens int                         `json:"max_output_tokens"`
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
		writeJSON(w, http.StatusOK, sessionsV3HydratedResponse(hydrated))
	case "messages":
		s.handleSessionV3PrimaryMessages(w, r, principal, sessionID)
	case "events":
		s.handleSessionV3PrimaryEvents(w, r, principal, sessionID)
	case "stream":
		s.handleSessionV3PrimaryStream(w, r, principal, sessionID)
	case "run/stop":
		s.handleSessionV3PrimaryRunStop(w, r, principal, sessionID)
	case "mode":
		s.handleSessionV3PrimaryMode(w, r, principal, sessionID)
	case "agent":
		s.handleSessionV3PrimaryAgent(w, r, principal, sessionID)
	case "preference":
		s.handleSessionV3PrimaryPreference(w, r, principal, sessionID)
	case "metadata":
		s.handleSessionV3PrimaryMetadata(w, r, principal, sessionID)
	case "plans":
		s.handleSessionV3PrimaryPlans(w, r, principal, sessionID)
	case "plans/active":
		s.handleSessionV3PrimaryActivePlan(w, r, principal, sessionID)
	case "permissions/resolve_all":
		s.handleSessionV3PrimaryPermissionResolveAll(w, r, principal, sessionID)
	default:
		if strings.HasPrefix(subpath, "permissions/") && strings.HasSuffix(subpath, "/resolve") {
			s.handleSessionV3PrimaryPermissionResolve(w, r, principal, sessionID, strings.TrimSuffix(strings.TrimPrefix(subpath, "permissions/"), "/resolve"))
			return
		}
		if strings.HasPrefix(subpath, "plans/") {
			s.handleSessionV3PrimaryPlanByID(w, r, principal, sessionID, strings.TrimPrefix(subpath, "plans/"))
			return
		}
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
	resolvedAgent, err := s.resolveSessionsV3PrimaryCreateAgent(principal, req.AgentName)
	if err != nil {
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
		Metadata:       sessionsV3CreateServerMetadata(req.Metadata, resolvedAgent),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	payloadHash, err := sessionsV3CreatePayloadHash(sessionID, req, workspaceName, title, session.Metadata)
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
	runStatus, blockedReason := s.sessionsV3PrimaryRunIntentStatus(principal, hydrated.Session, req)
	runIntent := &pebblestore.V3SessionRunIntent{
		RunID:         strings.TrimSpace(req.RunID),
		Status:        runStatus,
		BlockedReason: blockedReason,
	}
	if runIntent.RunID == "" {
		runIntent.RunID = stableSessionsV3PrimaryRunID(sessionID, clientRequestID)
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
	var enqueueJob *sessionV3ExecutorJob
	if !result.Replayed && result.RunIntent != nil && result.RunIntent.Status == sessionruntime.RunIntentPendingExecutor && s.v3SessionExecutor != nil {
		enqueueJob = &sessionV3ExecutorJob{Principal: principal, SessionID: sessionID, RunID: result.RunIntent.RunID}
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
	if enqueueJob != nil {
		s.v3SessionExecutor.EnqueueRun(*enqueueJob)
	}
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
	if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
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

func (s *Server) handleSessionV3PrimaryMode(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req sessionsV3ModeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if !sessionruntime.IsValidMode(mode) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid mode %q", req.Mode))
		return
	}
	mode = sessionruntime.NormalizeMode(mode)
	hydrated, found, err := s.hydrateSessionsV3Primary(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	if sessionruntime.NormalizeMode(hydrated.Session.Mode) == mode {
		writeJSON(w, http.StatusOK, sessionsV3HydratedResponse(hydrated))
		return
	}
	next := hydrated.Session
	next.Mode = mode
	next.UpdatedAt = time.Now().UnixMilli()
	payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, sessionruntime.SessionMutationUpdateMode, map[string]any{"mode": mode})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	eventPayload, err := json.Marshal(map[string]any{"session_id": sessionID, "mode": mode, "updated_at": next.UpdatedAt})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: "mode:" + sessionID + ":" + mode + ":" + fmt.Sprint(next.UpdatedAt),
		IdempotencyKey:  "mode:" + sessionID + ":" + mode + ":" + fmt.Sprint(next.UpdatedAt),
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationUpdateMode,
		EventPayload:    eventPayload,
		Session:         &next,
		NowUnixMs:       next.UpdatedAt,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	refreshed, _, err := s.hydrateSessionsV3Primary(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	response := sessionsV3HydratedResponse(refreshed)
	response["mutation"] = result
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleSessionV3PrimaryAgent(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req sessionsV3AgentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolvedAgent, err := s.resolveSessionsV3PrimaryCreateAgent(principal, req.AgentName)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
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
	next := hydrated.Session
	next.Metadata = sessionsV3AgentSwitchMetadata(hydrated.Session.Metadata, resolvedAgent)
	next.UpdatedAt = time.Now().UnixMilli()
	payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, sessionruntime.SessionMutationUpdateMetadata, map[string]any{"agent_name": resolvedAgent.Name, "metadata": next.Metadata})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	eventPayload, err := json.Marshal(map[string]any{"session_id": sessionID, "agent_name": resolvedAgent.Name, "resolved_agent_name": resolvedAgent.ResolvedName, "agent_mode": resolvedAgent.Mode, "runtime_mode": resolvedAgent.RuntimeMode, "metadata": next.Metadata, "updated_at": next.UpdatedAt})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: "agent:" + sessionID + ":" + resolvedAgent.Name + ":" + fmt.Sprint(next.UpdatedAt),
		IdempotencyKey:  "agent:" + sessionID + ":" + resolvedAgent.Name + ":" + fmt.Sprint(next.UpdatedAt),
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationUpdateMetadata,
		EventType:       "session.agent.updated",
		EventPayload:    eventPayload,
		Session:         &next,
		NowUnixMs:       next.UpdatedAt,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	refreshed, _, err := s.hydrateSessionsV3Primary(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	response := sessionsV3HydratedResponse(refreshed)
	response["mutation"] = result
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleSessionV3PrimaryPreference(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req sessionsV3PreferenceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
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
	agentModelPolicy := s.sessionsV3AgentModelPolicy(hydrated.Session, hydrated.Session.Preference, 0, 0)
	if agentModelPolicy.Locked {
		writeError(w, http.StatusBadRequest, errors.New(agentModelPolicy.Reason))
		return
	}
	pref := mergeSessionsV3PreferenceUpdate(hydrated.Session.Preference, req)
	if s.model == nil {
		writeError(w, http.StatusInternalServerError, errors.New("model service is not configured"))
		return
	}
	resolved, err := s.model.ResolvePreference(pref)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	next := hydrated.Session
	next.Preference = normalizeSessionsV3ModelPreference(resolved.Preference)
	next.UpdatedAt = time.Now().UnixMilli()
	payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, sessionruntime.SessionMutationUpdatePreference, map[string]any{"preference": next.Preference})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	eventPayload, err := json.Marshal(map[string]any{"session_id": sessionID, "preference": next.Preference, "updated_at": next.UpdatedAt})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: "preference:" + sessionID + ":" + fmt.Sprint(next.UpdatedAt),
		IdempotencyKey:  "preference:" + sessionID + ":" + fmt.Sprint(next.UpdatedAt),
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationUpdatePreference,
		EventPayload:    eventPayload,
		Session:         &next,
		NowUnixMs:       next.UpdatedAt,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	refreshed, _, err := s.hydrateSessionsV3Primary(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	response := sessionsV3HydratedResponse(refreshed)
	response["mutation"] = result
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleSessionV3PrimaryMetadata(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
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
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "metadata": hydrated.Session.Metadata})
		return
	}
	var req sessionsV3MetadataRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	next := hydrated.Session
	next.Metadata = mergeSessionsV3MetadataUpdate(hydrated.Session.Metadata, req.Metadata)
	next.UpdatedAt = time.Now().UnixMilli()
	payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, sessionruntime.SessionMutationUpdateMetadata, map[string]any{"metadata": next.Metadata})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	eventPayload, err := json.Marshal(map[string]any{"session_id": sessionID, "metadata": next.Metadata, "updated_at": next.UpdatedAt})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: "metadata:" + sessionID + ":" + fmt.Sprint(next.UpdatedAt),
		IdempotencyKey:  "metadata:" + sessionID + ":" + fmt.Sprint(next.UpdatedAt),
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationUpdateMetadata,
		EventPayload:    eventPayload,
		Session:         &next,
		NowUnixMs:       next.UpdatedAt,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	refreshed, _, err := s.hydrateSessionsV3Primary(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	response := sessionsV3HydratedResponse(refreshed)
	response["mutation"] = result
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleSessionV3PrimaryActivePlan(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	if r.Method == http.MethodGet {
		plan, ok, err := s.sessions.GetActivePlan(sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "has_active": false, "active_plan": nil})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "has_active": true, "active_plan": plan})
		return
	}
	var req struct {
		PlanID string `json:"plan_id"`
		ID     string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	planID := strings.TrimSpace(req.PlanID)
	if planID == "" {
		planID = strings.TrimSpace(req.ID)
	}
	plan, event, err := s.sessions.SetActivePlan(sessionID, planID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "active_plan": plan})
}

func (s *Server) handleSessionV3PrimaryPlans(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	if r.Method == http.MethodGet {
		limit, ok := parseSessionsV2PositiveLimit(w, r, 100)
		if !ok {
			return
		}
		plans, activeID, err := s.sessions.ListPlans(sessionID, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "active_plan_id": activeID, "count": len(plans), "plans": plans})
		return
	}
	var req sessionsV3PlanUpsertRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	planID := strings.TrimSpace(req.PlanID)
	if planID == "" {
		planID = strings.TrimSpace(req.ID)
	}
	activate := true
	if req.Activate != nil {
		activate = *req.Activate
	}
	updateScope := strings.TrimSpace(req.UpdateScope)
	if updateScope == "" {
		updateScope = strings.TrimSpace(req.Scope)
	}
	metadata := sessionruntime.PlanSaveMetadata{UpdateSummary: req.UpdateSummary, UpdateScope: updateScope, UpdateKind: req.UpdateKind, Checkpoint: req.Checkpoint, Document: req.Document}
	var plan pebblestore.SessionPlanSnapshot
	var event *pebblestore.EventEnvelope
	var err error
	if req.DocumentPatch != nil {
		activatePtr := &activate
		plan, event, err = s.sessions.PatchPlan(sessionID, sessionruntime.PlanPatchOptions{PlanID: planID, Title: req.Title, Status: req.Status, ApprovalState: req.ApprovalState, Activate: activatePtr, Document: req.Document, DocumentPatch: req.DocumentPatch, Metadata: metadata})
	} else {
		plan, event, err = s.sessions.SavePlanWithMetadata(sessionID, planID, req.Title, req.Plan, req.Status, req.ApprovalState, activate, metadata)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	refreshed, _, err := s.hydrateSessionsV3Primary(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	response := sessionsV3HydratedResponse(refreshed)
	response["plan"] = plan
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleSessionV3PrimaryPlanByID(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, tail string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	if strings.HasSuffix(tail, "/history") {
		planID := strings.TrimSpace(strings.TrimSuffix(tail, "/history"))
		if planID == "" || strings.Contains(planID, "/") {
			writeError(w, http.StatusBadRequest, errors.New("plan id is required"))
			return
		}
		limit, ok := parseSessionsV2PositiveLimit(w, r, 100)
		if !ok {
			return
		}
		revisions, err := s.sessions.ListPlanRevisions(sessionID, planID, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "plan_id": planID, "count": len(revisions), "revisions": revisions})
		return
	}
	planID := strings.TrimSpace(tail)
	if planID == "" || strings.Contains(planID, "/") {
		writeError(w, http.StatusBadRequest, errors.New("plan id is required"))
		return
	}
	plan, ok, err := s.sessions.GetPlan(sessionID, planID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "plan not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "plan": plan})
}

func (s *Server) handleSessionV3PrimaryRunStop(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.v3SessionExecutor == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 executor is not configured"))
		return
	}
	var req sessionsV3StopRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		writeError(w, http.StatusBadRequest, errors.New("run_id is required"))
		return
	}
	if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = sessionV3RunStopDefaultReason
	}
	result, cancelled, err := s.v3SessionExecutor.CancelRun(sessionV3ExecutorJob{Principal: principal, SessionID: sessionID, RunID: runID}, reason)
	if err != nil {
		status := http.StatusBadRequest
		if !cancelled {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	if s.perm != nil {
		_, _ = s.perm.CancelRunPending(sessionID, runID, reason)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"session_id": sessionID,
		"run_id":     runID,
		"status":     sessionruntime.RunIntentFailed,
		"reason":     reason,
		"mutation":   result,
	})
}

func (s *Server) handleSessionV3PrimaryPermissionResolve(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, permissionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	permissionID = strings.Trim(permissionID, "/")
	if permissionID == "" || strings.Contains(permissionID, "/") {
		writeError(w, http.StatusBadRequest, errors.New("permission id is required"))
		return
	}
	if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	var req struct {
		Action            string          `json:"action"`
		Reason            string          `json:"reason"`
		ApprovedArguments json.RawMessage `json:"approved_arguments,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	record, savedRule, err := s.perm.ResolveWithPolicyAndArguments(sessionID, permissionID, req.Action, req.Reason, string(req.ApprovedArguments))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "permission": record, "saved_rule": savedRule})
}

func (s *Server) handleSessionV3PrimaryPermissionResolveAll(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	var req struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
		Limit  int    `json:"limit"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolved, err := s.perm.ResolveAll(sessionID, req.Action, req.Reason, req.Limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "count": len(resolved), "resolved": resolved})
}

func (s *Server) authorizeSessionsV3PrimarySession(principal identity.Principal, sessionID string) (bool, error) {
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return ok, err
	}
	if strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return false, nil
	}
	return true, nil
}

func (s *Server) hydrateSessionsV3Primary(principal identity.Principal, sessionID string) (sessionsV3HydratedSession, bool, error) {
	hydrated, ok, err := s.sessions.HydrateSessionSnapshot(sessionID, 500, 500)
	if err != nil || !ok {
		return sessionsV3HydratedSession{}, ok, err
	}
	if strings.TrimSpace(hydrated.Session.AccountScopeID) == "" || strings.TrimSpace(hydrated.Session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionsV3HydratedSession{}, false, nil
	}
	pendingPermissions := []pebblestore.PermissionRecord{}
	if s.perm != nil {
		permissions, err := s.perm.ListPending(sessionID, 200)
		if err != nil {
			return sessionsV3HydratedSession{}, false, err
		}
		pendingPermissions = permissions
	}
	var usageSummary *pebblestore.SessionUsageSummary
	if summary, hasSummary, err := s.sessions.GetUsageSummary(sessionID); err != nil {
		return sessionsV3HydratedSession{}, false, err
	} else if hasSummary {
		usageSummary = &summary
	}
	hydrated.Session.Preference = normalizeSessionsV3ModelPreference(hydrated.Session.Preference)
	preference := hydrated.Session.Preference
	contextWindow := 0
	maxOutputTokens := 0
	if s.model != nil {
		if resolved, err := s.model.ResolvePreference(hydrated.Session.Preference); err == nil {
			preference = normalizeSessionsV3ModelPreference(resolved.Preference)
			contextWindow = resolved.ContextWindow
			maxOutputTokens = resolved.MaxOutputTokens
		}
	}
	agentModelPolicy := s.sessionsV3AgentModelPolicy(hydrated.Session, preference, contextWindow, maxOutputTokens)
	if agentModelPolicy.Locked {
		preference = agentModelPolicy.Preference
		contextWindow = agentModelPolicy.ContextWindow
		maxOutputTokens = agentModelPolicy.MaxOutputTokens
	}
	activePlan, hasActivePlan, err := s.sessions.GetActivePlan(sessionID)
	if err != nil {
		return sessionsV3HydratedSession{}, false, err
	}
	planRevisions := []pebblestore.SessionPlanSnapshot{}
	if hasActivePlan && strings.TrimSpace(activePlan.ID) != "" {
		revisions, err := s.sessions.ListPlanRevisions(sessionID, activePlan.ID, 100)
		if err != nil {
			return sessionsV3HydratedSession{}, false, err
		}
		planRevisions = revisions
	}
	return sessionsV3HydratedSession{Session: hydrated.Session, Projection: hydrated.Projection, Messages: hydrated.Messages, Events: hydrated.Events, PendingPermissions: pendingPermissions, UsageSummary: usageSummary, Preference: preference, ContextWindow: contextWindow, MaxOutputTokens: maxOutputTokens, AgentModelPolicy: agentModelPolicy, HasActivePlan: hasActivePlan, ActivePlan: activePlan, PlanRevisions: planRevisions}, true, nil
}

func (s *Server) sessionsV3AgentModelPolicy(session pebblestore.SessionSnapshot, defaultPreference pebblestore.ModelPreference, defaultContextWindow, defaultMaxOutputTokens int) sessionsV3AgentModelPolicy {
	policy := sessionsV3AgentModelPolicy{
		AgentName:       sessionsV3MetadataString(session.Metadata, "agent_name"),
		ResolvedAgent:   sessionsV3MetadataString(session.Metadata, "resolved_agent_name"),
		Source:          "default",
		Locked:          false,
		Preference:      normalizeSessionsV3ModelPreference(defaultPreference),
		ContextWindow:   defaultContextWindow,
		MaxOutputTokens: defaultMaxOutputTokens,
	}
	if policy.AgentName == "" {
		policy.AgentName = policy.ResolvedAgent
	}
	if policy.ResolvedAgent == "" {
		policy.ResolvedAgent = policy.AgentName
	}
	profile, err := sessionV3AgentProfileFromMetadata(session.Metadata)
	if err != nil {
		return policy
	}
	if strings.TrimSpace(profile.Name) != "" {
		policy.AgentName = strings.TrimSpace(profile.Name)
		policy.ResolvedAgent = strings.TrimSpace(profile.Name)
	}
	agentPref := sessionsV3AgentPresetPreference(profile)
	if strings.TrimSpace(agentPref.Provider) == "" || strings.TrimSpace(agentPref.Model) == "" {
		return policy
	}
	policy.Source = "agent_preset"
	policy.Locked = true
	policy.Reason = "Agent model is set in agent settings; set the agent model to Default to choose a different model."
	policy.Preference = normalizeSessionsV3ModelPreference(agentPref)
	policy.ContextWindow = 0
	policy.MaxOutputTokens = 0
	if s != nil && s.model != nil {
		if resolved, err := s.model.ResolvePreference(policy.Preference); err == nil {
			policy.Preference = normalizeSessionsV3ModelPreference(resolved.Preference)
			policy.ContextWindow = resolved.ContextWindow
			policy.MaxOutputTokens = resolved.MaxOutputTokens
		}
	}
	return policy
}

func sessionsV3AgentPresetPreference(profile pebblestore.AgentProfile) pebblestore.ModelPreference {
	provider := strings.ToLower(strings.TrimSpace(profile.Provider))
	model := strings.TrimSpace(profile.Model)
	if provider == "" || model == "" {
		return pebblestore.ModelPreference{}
	}
	return pebblestore.ModelPreference{
		Provider:  provider,
		Model:     model,
		Thinking:  normalizeSessionV3ThinkingWithProvider(provider, profile.Thinking),
		UpdatedAt: profile.UpdatedAt,
	}
}

func sessionsV3MetadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func sessionsV3HydratedResponse(hydrated sessionsV3HydratedSession) map[string]any {
	response := map[string]any{
		"ok":                  true,
		"session":             hydrated.Session,
		"projection":          hydrated.Projection,
		"messages":            hydrated.Messages,
		"events":              hydrated.Events,
		"pending_permissions": hydrated.PendingPermissions,
		"usage_summary":       hydrated.UsageSummary,
		"preference":          hydrated.Preference,
		"context_window":      hydrated.ContextWindow,
		"max_output_tokens":   hydrated.MaxOutputTokens,
		"agent_model_policy":  hydrated.AgentModelPolicy,
		"has_active_plan":     hydrated.HasActivePlan,
		"active_plan":         nil,
		"plan_revisions":      hydrated.PlanRevisions,
	}
	if hydrated.HasActivePlan {
		response["active_plan"] = hydrated.ActivePlan
	}
	return response
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

func sessionsV3CreatePayloadHash(sessionID string, req sessionsV3CreateRequest, workspaceName, title string, metadata map[string]any) (string, error) {
	canonical := struct {
		Operation     string                      `json:"operation"`
		SessionID     string                      `json:"session_id"`
		Title         string                      `json:"title"`
		WorkspacePath string                      `json:"workspace_path"`
		WorkspaceName string                      `json:"workspace_name"`
		Mode          string                      `json:"mode"`
		AgentName     string                      `json:"agent_name,omitempty"`
		Preference    pebblestore.ModelPreference `json:"preference"`
		Metadata      map[string]any              `json:"metadata,omitempty"`
	}{
		Operation:     sessionruntime.SessionMutationCreateSession,
		SessionID:     strings.TrimSpace(sessionID),
		Title:         title,
		WorkspacePath: strings.TrimSpace(req.WorkspacePath),
		WorkspaceName: workspaceName,
		Mode:          sessionruntime.NormalizeMode(req.Mode),
		AgentName:     strings.TrimSpace(req.AgentName),
		Preference:    normalizeSessionsV3ModelPreference(req.Preference),
		Metadata:      cloneSessionsV3Metadata(metadata),
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal sessions v3 create payload hash: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func sessionsV3UpdatePayloadHash(sessionID, operation string, payload map[string]any) (string, error) {
	canonical := map[string]any{
		"operation":  strings.TrimSpace(operation),
		"session_id": strings.TrimSpace(sessionID),
		"payload":    cloneSessionsV3Metadata(payload),
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal sessions v3 update payload hash: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func mergeSessionsV3PreferenceUpdate(current pebblestore.ModelPreference, req sessionsV3PreferenceRequest) pebblestore.ModelPreference {
	next := normalizeSessionsV3ModelPreference(current)
	if req.Provider != nil {
		next.Provider = strings.ToLower(strings.TrimSpace(*req.Provider))
	}
	if req.Model != nil {
		next.Model = strings.TrimSpace(*req.Model)
	}
	if req.Thinking != nil {
		next.Thinking = strings.ToLower(strings.TrimSpace(*req.Thinking))
	}
	if req.ServiceTier != nil {
		next.ServiceTier = strings.TrimSpace(*req.ServiceTier)
	}
	if req.ContextMode != nil {
		next.ContextMode = strings.TrimSpace(*req.ContextMode)
	}
	return normalizeSessionsV3ModelPreference(next)
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
	return hex.EncodeToString(sum[:16])
}

func stableSessionsV3PrimaryRunID(sessionID, clientRequestID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(clientRequestID)))
	return "v3run_" + hex.EncodeToString(sum[:16])
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

func mergeSessionsV3MetadataUpdate(current map[string]any, requested map[string]any) map[string]any {
	metadata := cloneSessionsV3Metadata(current)
	if metadata == nil {
		metadata = make(map[string]any)
	}
	for key := range metadata {
		if !isProtectedSessionsV3MetadataKey(key) {
			delete(metadata, key)
		}
	}
	for key, value := range requested {
		if isProtectedSessionsV3MetadataKey(key) {
			continue
		}
		metadata[key] = cloneSessionsV3MetadataValue(value)
	}
	return metadata
}

type sessionsV3ResolvedAgentIdentity struct {
	Name                string
	ResolvedName        string
	Mode                string
	RuntimeMode         string
	ExitPlanModeEnabled bool
	ToolContractPreset  string
	Profile             pebblestore.AgentProfile
}

type sessionsV3StoredAgentToolContractCompiler interface {
	CompileStoredV3AgentToolContract(accountScopeID string, profile pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, map[string]bool, error)
}

func (s *Server) resolveSessionsV3PrimaryCreateAgent(principal identity.Principal, requestedName string) (sessionsV3ResolvedAgentIdentity, error) {
	requestedName = strings.TrimSpace(requestedName)
	if requestedName == "" {
		return sessionsV3ResolvedAgentIdentity{}, errors.New("agent_name is required")
	}
	if s == nil || s.agents == nil {
		return sessionsV3ResolvedAgentIdentity{}, errors.New("agent service is not configured")
	}
	profile, ok, err := s.agents.GetProfileForAccount(principal.AccountScopeID, requestedName)
	if err != nil {
		return sessionsV3ResolvedAgentIdentity{}, err
	}
	if !ok {
		return sessionsV3ResolvedAgentIdentity{}, fmt.Errorf("agent %q not found", requestedName)
	}
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		return sessionsV3ResolvedAgentIdentity{}, fmt.Errorf("agent %q saved profile is missing name", requestedName)
	}
	mode := strings.TrimSpace(profile.Mode)
	if mode == "" {
		return sessionsV3ResolvedAgentIdentity{}, fmt.Errorf("agent %q saved profile is missing mode", name)
	}
	if !profile.Enabled {
		return sessionsV3ResolvedAgentIdentity{}, fmt.Errorf("agent %q is disabled", name)
	}
	if profile.ToolContract == nil {
		return sessionsV3ResolvedAgentIdentity{}, fmt.Errorf("agent %q tool_contract is not configured", name)
	}
	runtimeMode := strings.TrimSpace(profile.RuntimeMode)
	if runtimeMode == "" {
		return sessionsV3ResolvedAgentIdentity{}, fmt.Errorf("agent %q saved profile is missing runtime_mode", name)
	}
	if profile.ExitPlanModeEnabled == nil {
		return sessionsV3ResolvedAgentIdentity{}, fmt.Errorf("agent %q saved profile is missing exit_plan_mode_enabled", name)
	}
	compiler, ok := s.runner.(sessionsV3StoredAgentToolContractCompiler)
	if !ok || compiler == nil {
		return sessionsV3ResolvedAgentIdentity{}, errors.New("v3 tool contract compiler is not configured")
	}
	profile = cloneSessionsV3AgentProfile(profile)
	if _, _, err := compiler.CompileStoredV3AgentToolContract(principal.AccountScopeID, profile); err != nil {
		return sessionsV3ResolvedAgentIdentity{}, err
	}
	return sessionsV3ResolvedAgentIdentity{
		Name:                name,
		ResolvedName:        name,
		Mode:                mode,
		RuntimeMode:         runtimeMode,
		ExitPlanModeEnabled: *profile.ExitPlanModeEnabled,
		ToolContractPreset:  strings.TrimSpace(profile.ToolContract.Preset),
		Profile:             profile,
	}, nil
}

func sessionsV3CreateServerMetadata(clientMetadata map[string]any, agent sessionsV3ResolvedAgentIdentity) map[string]any {
	metadata := cloneSessionsV3Metadata(clientMetadata)
	if metadata == nil {
		metadata = make(map[string]any, 8)
	}
	metadata["agent_name"] = agent.Name
	metadata["resolved_agent_name"] = agent.ResolvedName
	metadata["agent_mode"] = agent.Mode
	metadata["runtime_mode"] = agent.RuntimeMode
	metadata["exit_plan_mode_enabled"] = agent.ExitPlanModeEnabled
	metadata["agent_profile"] = cloneSessionsV3AgentProfile(agent.Profile)
	if agent.ToolContractPreset != "" {
		metadata["tool_contract_preset"] = agent.ToolContractPreset
	}
	return metadata
}

func sessionsV3AgentSwitchMetadata(current map[string]any, agent sessionsV3ResolvedAgentIdentity) map[string]any {
	metadata := cloneSessionsV3Metadata(current)
	if metadata == nil {
		metadata = make(map[string]any, 8)
	}
	for _, key := range []string{"agent_name", "agent_profile", "resolved_agent_name", "agent_mode", "runtime_mode", "exit_plan_mode_enabled", "tool_contract_preset", "subagent", "requested_subagent"} {
		delete(metadata, key)
	}
	return sessionsV3CreateServerMetadata(metadata, agent)
}

func sessionsV3PrimaryAuthorityStatus(req sessionsV3MessageRequest) string {
	if len(req.DispatchAuthority) > 0 || len(req.Authority) > 0 {
		return "invalid"
	}
	return "absent"
}

func (s *Server) sessionsV3PrimaryRunIntentStatus(principal identity.Principal, session pebblestore.SessionSnapshot, req sessionsV3MessageRequest) (string, string) {
	if reason := s.sessionsV3PrimaryDispatchBlockedReason(principal, session, req); reason != "" {
		return sessionruntime.RunIntentDispatchBlocked, reason
	}
	return sessionruntime.RunIntentPendingExecutor, ""
}

func (s *Server) sessionsV3PrimaryDispatchBlockedReason(principal identity.Principal, session pebblestore.SessionSnapshot, req sessionsV3MessageRequest) string {
	authority := firstNonEmptyMap(req.DispatchAuthority, req.Authority)
	if len(authority) == 0 {
		return ""
	}
	if strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return "dispatch authority account mismatch"
	}
	accountScopeID := sessionsV3AuthorityString(authority, "account_scope_id")
	if accountScopeID != "" && accountScopeID != strings.TrimSpace(principal.AccountScopeID) {
		return "dispatch authority account mismatch"
	}
	runtimeSwarmID := sessionsV3AuthorityString(authority, "runtime_swarm_id", "swarm_id", "target_swarm_id")
	if runtimeSwarmID == "" {
		return "dispatch authority missing executor runtime"
	}
	runtimeKind := sessionsV3AuthorityString(authority, "runtime_kind", "target_kind")
	workspaceBindingID := sessionsV3AuthorityString(authority, "workspace_binding_id", "local_workspace_binding_id")
	placementGeneration := sessionsV3AuthorityInt(authority, "placement_generation")
	bindingGeneration := sessionsV3AuthorityInt(authority, "binding_generation")
	authorityHostSwarmID := sessionsV3AuthorityString(authority, "authority_host_swarm_id", "host_swarm_id")
	authorityContainerID := sessionsV3AuthorityString(authority, "authority_container_id", "host_container_id", "container_id")
	sourceWorkspacePath := sessionsV3AuthorityString(authority, "source_workspace_path", "workspace_path", "host_workspace_path")
	runtimeWorkspacePath := sessionsV3AuthorityString(authority, "runtime_workspace_path", "destination_workspace_path")

	if s == nil || s.topology == nil {
		return "dispatch authority unavailable: topology is not configured"
	}
	placement, placementOK, err := s.topology.GetRuntimePlacementForAccount(principal.AccountScopeID, runtimeSwarmID)
	if err != nil {
		return "dispatch authority unavailable: " + err.Error()
	}
	if !placementOK {
		return "dispatch authority unavailable: runtime placement not found"
	}
	if strings.TrimSpace(placement.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return "dispatch authority account mismatch"
	}
	if strings.TrimSpace(placement.State) != pebblestore.TopologyRuntimePlacementStateActive {
		return "dispatch authority stale: runtime placement is not active"
	}
	if placementGeneration > 0 && placement.PlacementGeneration != placementGeneration {
		return "dispatch authority stale: runtime placement generation mismatch"
	}
	if runtimeKind != "" && strings.TrimSpace(placement.RuntimeKind) != runtimeKind {
		return "dispatch authority runtime kind mismatch"
	}
	if authorityHostSwarmID != "" && strings.TrimSpace(placement.AuthorityHostSwarmID) != authorityHostSwarmID {
		return "dispatch authority placement authority host mismatch"
	}
	if authorityContainerID != "" && strings.TrimSpace(placement.AuthorityContainerID) != authorityContainerID {
		return "dispatch authority placement container mismatch"
	}
	if workspaceBindingID == "" {
		return "dispatch authority missing workspace binding"
	}
	binding, bindingOK, err := s.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, workspaceBindingID)
	if err != nil {
		return "dispatch authority unavailable: " + err.Error()
	}
	if !bindingOK {
		return "dispatch authority unavailable: workspace binding not found"
	}
	if strings.TrimSpace(binding.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return "dispatch authority account mismatch"
	}
	if strings.TrimSpace(binding.State) != pebblestore.TopologyWorkspaceBindingStateBound {
		return "dispatch authority stale: workspace binding is not bound"
	}
	if placementGeneration > 0 && binding.PlacementGeneration != placementGeneration {
		return "dispatch authority stale: workspace binding placement generation mismatch"
	}
	if bindingGeneration > 0 && binding.BindingGeneration != bindingGeneration {
		return "dispatch authority stale: workspace binding generation mismatch"
	}
	if strings.TrimSpace(binding.DestinationRuntimeSwarmID) != runtimeSwarmID {
		return "dispatch authority workspace binding runtime mismatch"
	}
	if authorityHostSwarmID != "" && strings.TrimSpace(binding.DestinationAuthorityHostSwarmID) != authorityHostSwarmID {
		return "dispatch authority workspace binding authority host mismatch"
	}
	if runtimeKind != "" && strings.TrimSpace(binding.DestinationRuntimeKind) != runtimeKind {
		return "dispatch authority workspace binding runtime kind mismatch"
	}
	if authorityContainerID != "" && strings.TrimSpace(binding.DestinationContainerID) != authorityContainerID {
		return "dispatch authority workspace binding container mismatch"
	}
	if sourceWorkspacePath != "" && filepath.Clean(strings.TrimSpace(binding.SourceWorkspacePath)) != filepath.Clean(sourceWorkspacePath) {
		return "dispatch authority source workspace path mismatch"
	}
	if strings.TrimSpace(session.WorkspacePath) != "" && strings.TrimSpace(binding.SourceWorkspacePath) != "" && filepath.Clean(strings.TrimSpace(session.WorkspacePath)) != filepath.Clean(strings.TrimSpace(binding.SourceWorkspacePath)) {
		return "dispatch authority session workspace path mismatch"
	}
	if runtimeWorkspacePath != "" && filepath.Clean(strings.TrimSpace(binding.DestinationWorkspacePath)) != filepath.Clean(runtimeWorkspacePath) {
		return "dispatch authority runtime workspace path mismatch"
	}
	return ""
}

func firstNonEmptyMap(maps ...map[string]any) map[string]any {
	for _, item := range maps {
		if len(item) > 0 {
			return item
		}
	}
	return nil
}

func sessionsV3AuthorityString(authority map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := authority[key]
		if !ok {
			value, ok = authority[strings.ToLower(key)]
		}
		if !ok {
			continue
		}
		s := strings.TrimSpace(fmt.Sprint(value))
		if s != "" {
			return s
		}
	}
	return ""
}

func sessionsV3AuthorityInt(authority map[string]any, keys ...string) int {
	raw := sessionsV3AuthorityString(authority, keys...)
	if raw == "" {
		return 0
	}
	var parsed int
	if _, err := fmt.Sscanf(raw, "%d", &parsed); err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

func isProtectedSessionsV3MetadataKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "agent_name",
		"agent_profile",
		"resolved_agent_name",
		"agent_mode",
		"runtime_mode",
		"exit_plan_mode_enabled",
		"tool_contract_preset",
		"workspace_binding_id",
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

func sessionV3AgentProfileFromMetadata(metadata map[string]any) (pebblestore.AgentProfile, error) {
	raw, ok := metadata["agent_profile"]
	if !ok || raw == nil {
		return pebblestore.AgentProfile{}, errors.New("v3 session is missing stored agent profile")
	}
	switch typed := raw.(type) {
	case pebblestore.AgentProfile:
		return cloneSessionsV3AgentProfile(typed), nil
	case map[string]any:
		var profile pebblestore.AgentProfile
		encoded, err := json.Marshal(typed)
		if err != nil {
			return pebblestore.AgentProfile{}, err
		}
		if err := json.Unmarshal(encoded, &profile); err != nil {
			return pebblestore.AgentProfile{}, err
		}
		return cloneSessionsV3AgentProfile(profile), nil
	default:
		encoded, err := json.Marshal(raw)
		if err != nil {
			return pebblestore.AgentProfile{}, err
		}
		var profile pebblestore.AgentProfile
		if err := json.Unmarshal(encoded, &profile); err != nil {
			return pebblestore.AgentProfile{}, err
		}
		return cloneSessionsV3AgentProfile(profile), nil
	}
}

func cloneSessionsV3AgentProfile(profile pebblestore.AgentProfile) pebblestore.AgentProfile {
	profile.ExitPlanModeEnabled = pebblestore.CloneBoolPtr(profile.ExitPlanModeEnabled)
	profile.ToolScope = pebblestore.CloneAgentToolScope(profile.ToolScope)
	profile.ToolContract = pebblestore.CloneAgentToolContract(profile.ToolContract)
	return profile
}

func cloneSessionsV3Metadata(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneSessionsV3MetadataValue(value)
	}
	return out
}

func cloneSessionsV3MetadataValue(value any) any {
	switch typed := value.(type) {
	case pebblestore.AgentProfile:
		return cloneSessionsV3AgentProfile(typed)
	case map[string]any:
		return cloneSessionsV3Metadata(typed)
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = cloneSessionsV3MetadataValue(child)
		}
		return out
	default:
		return value
	}
}
