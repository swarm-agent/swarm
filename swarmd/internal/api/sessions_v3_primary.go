package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

const (
	sessionsV3PrimaryPrefix                  = "/v3/sessions/"
	sessionsV3PrimaryDefaultMessageTailLimit = 500
	sessionsV3PrimaryDefaultEventLimit       = 0
	sessionsV3MessagesPageDefaultLimit       = 200
	sessionsV3MessagesPageMaxLimit           = 200
	sessionsV3PlansPageDefaultLimit          = 100
	sessionsV3PlansPageMaxLimit              = 100
)

// V3 primary write handlers delegate through the ApplySessionMutation boundary.

type sessionsV3CreateRequest struct {
	SessionID                string                      `json:"session_id,omitempty"`
	ClientRequestID          string                      `json:"client_request_id,omitempty"`
	IdempotencyKey           string                      `json:"idempotency_key,omitempty"`
	Title                    string                      `json:"title,omitempty"`
	WorkspacePath            string                      `json:"workspace_path"`
	WorkspaceName            string                      `json:"workspace_name,omitempty"`
	WorkspaceBindingID       string                      `json:"workspace_binding_id,omitempty"`
	SwarmID                  string                      `json:"swarm_id,omitempty"`
	TargetKind               string                      `json:"target_kind,omitempty"`
	TargetRelationship       string                      `json:"target_relationship,omitempty"`
	HostWorkspacePath        string                      `json:"host_workspace_path,omitempty"`
	RuntimeWorkspacePath     string                      `json:"runtime_workspace_path,omitempty"`
	Mode                     string                      `json:"mode,omitempty"`
	AgentName                string                      `json:"agent_name,omitempty"`
	Preference               pebblestore.ModelPreference `json:"preference,omitempty"`
	WorktreeMode             string                      `json:"worktree_mode,omitempty"`
	WorktreeUseCurrentBranch *bool                       `json:"worktree_use_current_branch,omitempty"`
	WorktreeBaseBranch       string                      `json:"worktree_base_branch,omitempty"`
	WorktreeBranchName       string                      `json:"worktree_branch_name,omitempty"`
	Metadata                 map[string]any              `json:"metadata,omitempty"`
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
	Type          string `json:"type,omitempty"`
	RunID         string `json:"run_id"`
	TargetSwarmID string `json:"target_swarm_id"`
	Reason        string `json:"reason,omitempty"`
}

type sessionsV3CompactRequest struct {
	ClientRequestID string `json:"client_request_id,omitempty"`
	IdempotencyKey  string `json:"idempotency_key,omitempty"`
	RunID           string `json:"run_id,omitempty"`
	Note            string `json:"note,omitempty"`
	AgentName       string `json:"agent_name,omitempty"`
	Instructions    string `json:"instructions,omitempty"`
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
	Session                pebblestore.SessionSnapshot       `json:"session"`
	Projection             sessionruntime.SessionProjection  `json:"projection"`
	Messages               []pebblestore.MessageSnapshot     `json:"messages"`
	Events                 []sessionruntime.SessionEvent     `json:"events"`
	PendingPermissions     []pebblestore.PermissionRecord    `json:"pending_permissions"`
	UsageSummary           *pebblestore.SessionUsageSummary  `json:"usage_summary,omitempty"`
	ActiveRunIntent        *pebblestore.V3SessionRunIntent   `json:"active_run_intent,omitempty"`
	Preference             pebblestore.ModelPreference       `json:"preference"`
	ContextWindow          int                               `json:"context_window"`
	MaxOutputTokens        int                               `json:"max_output_tokens"`
	AgentModelPolicy       sessionsV3AgentModelPolicy        `json:"agent_model_policy"`
	HasActivePlan          bool                              `json:"has_active_plan"`
	ActivePlan             pebblestore.SessionPlanSnapshot   `json:"active_plan,omitempty"`
	PlanRevisions          []pebblestore.SessionPlanSnapshot `json:"plan_revisions"`
	AppliedSeq             uint64                            `json:"applied_seq"`
	HighWatermark          uint64                            `json:"high_watermark"`
	SnapshotEndpointCursor string                            `json:"snapshot_endpoint_cursor"`
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
		if r.Method == http.MethodDelete {
			s.handleSessionV3PrimaryDelete(w, r, principal, sessionID)
			return
		}
		// Legacy/debug/compat full-hydrate endpoint only. Desktop V3
		// canonical boot, hydrate, replay, and realtime must use
		// /v3/sync/bootstrap, /v3/sync/hydrate, /v3/sync/stream, and
		// /v3/realtime/stream instead of this per-session snapshot shape.
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		messageLimit, eventLimit, ok := parseSessionsV3HydrationLimits(w, r)
		if !ok {
			return
		}
		hydrated, found, err := s.hydrateSessionsV3PrimaryWithLimits(principal, sessionID, messageLimit, eventLimit)
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
	case "compact":
		s.handleSessionV3PrimaryCompact(w, r, principal, sessionID)
	case "mode":
		s.handleSessionV3PrimaryMode(w, r, principal, sessionID)
	case "agent":
		s.handleSessionV3PrimaryAgent(w, r, principal, sessionID)
	case "preference":
		s.handleSessionV3PrimaryPreference(w, r, principal, sessionID)
	case "usage":
		s.handleSessionV3PrimaryUsage(w, r, principal, sessionID)
	case "metadata":
		s.handleSessionV3PrimaryMetadata(w, r, principal, sessionID)
	case "plans":
		s.handleSessionV3PrimaryPlans(w, r, principal, sessionID)
	case "plans/active":
		s.handleSessionV3PrimaryActivePlan(w, r, principal, sessionID)
	case "permissions":
		s.handleSessionV3PrimaryPermissions(w, r, principal, sessionID)
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
	if err := validateSessionsV3CreateMetadata(req.Metadata); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	requestedWorktreeMode, err := validateSessionsV3CreateWorktreeRequest(req.WorktreeMode, req.WorktreeUseCurrentBranch, req.WorktreeBaseBranch, req.WorktreeBranchName)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	binding, err := s.resolveSessionsV3PrimaryBinding(principal, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolvedAgent, err := s.resolveSessionsV3PrimaryCreateAgent(principal, req.AgentName)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	workspacePath := binding.SourceWorkspacePath
	workspaceName := binding.SourceWorkspaceName
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
		Metadata:       sessionsV3CreateServerMetadata(req.Metadata, resolvedAgent, binding),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	payloadHash, err := sessionsV3CreatePayloadHash(sessionID, req, workspacePath, workspaceName, title, session.Metadata)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s.handleSessionsV3CreateReplay(w, principal, sessionID, clientRequestID, payloadHash, session) {
		return
	}
	if requestedWorktreeMode == runruntime.RunWorktreeModeOn {
		allocation, err := s.allocateSessionsV3CreateWorktree(principal, workspacePath, sessionID, req.WorktreeUseCurrentBranch, req.WorktreeBaseBranch, req.WorktreeBranchName)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		session.WorkspacePath = strings.TrimSpace(allocation.WorkspacePath)
		session.WorktreeEnabled = true
		session.WorktreeRootPath = strings.TrimSpace(allocation.WorkspacePath)
		session.WorktreeBaseBranch = strings.TrimSpace(allocation.BaseBranch)
		session.WorktreeBranch = strings.TrimSpace(allocation.BranchName)
		if session.Metadata == nil {
			session.Metadata = make(map[string]any, 4)
		}
		session.Metadata["workspace_id"] = strings.TrimSpace(allocation.WorkspaceID)
		session.Metadata["swarm_v3_runtime_workspace_path"] = strings.TrimSpace(allocation.WorkspacePath)
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
	response, err := sessionV3CreateResultResponse(result)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
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

func (s *Server) handleSessionV3PrimaryDelete(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	event, err := s.sessions.DeleteSessionWithEvent(session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if head, headErr := s.sessions.CurrentRealtimeOutboxRevision(); headErr == nil && head > 0 {
		if record, ok, recordErr := s.sessions.LastRealtimeOutboxForSessionAtOrBeforeEndpoint(session.ID, head); recordErr == nil && ok && record.Event.EventType == "session.deleted" {
			if publishErr := s.publishCommittedV3RealtimeOutbox(record); publishErr != nil {
				// Durable commit succeeded; realtime wake is an accelerator only.
				_ = publishErr
			}
		}
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	response := map[string]any{
		"ok":         true,
		"session_id": session.ID,
		"deleted":    true,
		"tombstone": map[string]any{
			"session_id":     session.ID,
			"deleted":        true,
			"workspace_path": session.WorkspacePath,
		},
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleSessionV3PrimaryMessages(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if r.Method == http.MethodGet {
		query, ok := parseSessionsV3MessagesPageQuery(w, r)
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
		var messages []pebblestore.MessageSnapshot
		var err error
		fetchLimit := query.Limit + 1
		if query.Tail {
			messages, err = s.sessions.ListSessionMessageTail(sessionID, fetchLimit)
		} else if query.HasBeforeSeq {
			messages, err = s.sessions.ListSessionMessagesBefore(sessionID, query.BeforeSeq, fetchLimit)
		} else {
			messages, err = s.sessions.ListSessionMessages(sessionID, query.AfterSeq, fetchLimit)
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, sessionsV3MessagesPageResponse(sessionID, messages, query))
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
	session, found, err := s.requireSessionV3Access(principal, sessionID)
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
	runStatus, blockedReason := s.sessionsV3PrimaryRunIntentStatus(principal, session, req)
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
	writeJSON(w, http.StatusOK, sessionV3MessageMutationResponse(sessionID, result))
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
		"applied_seq":        replay.NextSeq,
		"high_watermark":     replay.Projection.ProjectionHighWatermarkSeq,
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
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	if sessionruntime.NormalizeMode(session.Mode) == mode {
		writeJSON(w, http.StatusOK, sessionV3ModeMutationResponse(session.ID, mode, nil))
		return
	}
	next := session
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
	writeJSON(w, http.StatusOK, sessionV3ModeMutationResponse(sessionID, mode, &result))
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
	current, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	next := current
	next.Metadata = sessionsV3AgentSwitchMetadata(current.Metadata, resolvedAgent)
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
	preference := normalizeSessionsV3ModelPreference(next.Preference)
	contextWindow := 0
	maxOutputTokens := 0
	if s.model != nil {
		if resolved, err := s.model.ResolvePreference(preference); err == nil {
			preference = normalizeSessionsV3ModelPreference(resolved.Preference)
			contextWindow = resolved.ContextWindow
			maxOutputTokens = resolved.MaxOutputTokens
		}
	}
	writeJSON(w, http.StatusOK, sessionV3AgentMutationResponse(sessionID, next, s.sessionsV3AgentModelPolicy(next, preference, contextWindow, maxOutputTokens), result))
}

func (s *Server) handleSessionV3PrimaryUsage(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	response, found, err := s.sessionsV3PrimaryUsageResponse(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleSessionV3PrimaryPreference(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if r.Method == http.MethodGet {
		response, found, err := s.sessionsV3PrimaryPreferenceResponse(principal, sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !found {
			writeSessionNotFound(w)
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}
	var req sessionsV3PreferenceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	session.Preference = normalizeSessionsV3ModelPreference(session.Preference)
	agentModelPolicy := s.sessionsV3AgentModelPolicy(session, session.Preference, 0, 0)
	if agentModelPolicy.Locked {
		writeError(w, http.StatusBadRequest, errors.New(agentModelPolicy.Reason))
		return
	}
	pref := mergeSessionsV3PreferenceUpdate(session.Preference, req)
	if s.model == nil {
		writeError(w, http.StatusInternalServerError, errors.New("model service is not configured"))
		return
	}
	resolved, err := s.model.ResolvePreference(pref)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	next := session
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
	writeJSON(w, http.StatusOK, s.sessionV3PreferenceMutationResponse(sessionID, next.Preference, resolved.ContextWindow, resolved.MaxOutputTokens, result))
}

func (s *Server) handleSessionV3PrimaryMetadata(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "metadata": session.Metadata})
		return
	}
	var req sessionsV3MetadataRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	next := session
	next.Metadata = mergeSessionsV3MetadataUpdate(session.Metadata, req.Metadata)
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
	writeJSON(w, http.StatusOK, sessionV3MetadataMutationResponse(sessionID, next.Metadata, result))
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
		limit, ok := parseSessionsV3PlansLimit(w, r)
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
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "plan": plan})
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
		limit, ok := parseSessionsV3PlansLimit(w, r)
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
	targetSwarmID := strings.TrimSpace(req.TargetSwarmID)
	if targetSwarmID == "" {
		writeError(w, http.StatusBadRequest, errors.New("target_swarm_id is required"))
		return
	}
	if found, err := s.validateSessionsV3PrimaryStopTarget(principal, sessionID, targetSwarmID); err != nil {
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
	status := sessionruntime.RunIntentCancelled
	if result.RunIntent != nil && strings.TrimSpace(result.RunIntent.Status) != "" {
		status = strings.TrimSpace(result.RunIntent.Status)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"session_id": sessionID,
		"run_id":     runID,
		"status":     status,
		"reason":     reason,
		"mutation":   result,
	})
}

func (s *Server) handleSessionV3PrimaryPermissions(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet {
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
	limit := 200
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a non-negative integer"))
			return
		}
		if parsed > 0 {
			limit = parsed
		}
	}
	status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	var records []pebblestore.PermissionRecord
	var err error
	switch status {
	case "", "all":
		records, err = s.perm.ListPermissions(sessionID, limit)
	case "pending":
		records, err = s.perm.ListPending(sessionID, limit)
	default:
		writeError(w, http.StatusBadRequest, errors.New("unsupported permission status"))
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "count": len(records), "permissions": records})
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
	mutation, published, err := s.publishSessionV3PermissionUpdatedFromRecord(principal, sessionID, record)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "permission": record, "saved_rule": savedRule, "mutation": mutation, "published": published})
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
	mutations := make([]sessionruntime.SessionMutationResult, 0, len(resolved))
	for _, record := range resolved {
		mutation, published, err := s.publishSessionV3PermissionUpdatedFromRecord(principal, sessionID, record)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if published {
			mutations = append(mutations, mutation)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "count": len(resolved), "resolved": resolved, "mutations": mutations})
}

func (s *Server) publishSessionV3PermissionUpdatedFromRecord(principal identity.Principal, sessionID string, record pebblestore.PermissionRecord) (sessionruntime.SessionMutationResult, bool, error) {
	if s == nil || s.sessions == nil {
		return sessionruntime.SessionMutationResult{}, false, errors.New("sessions v3 service is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(record.SessionID)
	}
	if sessionID == "" {
		return sessionruntime.SessionMutationResult{}, false, errors.New("session id is required")
	}
	if strings.TrimSpace(record.SessionID) != "" && strings.TrimSpace(record.SessionID) != sessionID {
		return sessionruntime.SessionMutationResult{}, false, errors.New("permission belongs to a different session")
	}
	runID := strings.TrimSpace(record.RunID)
	callID := strings.TrimSpace(record.CallID)
	if runID == "" || callID == "" {
		return sessionruntime.SessionMutationResult{}, false, nil
	}
	existingIntent, ok, err := s.sessions.GetSessionRunIntent(sessionID, runID)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, false, err
	}
	if !ok || strings.TrimSpace(existingIntent.Status) != sessionruntime.RunIntentRunning {
		return sessionruntime.SessionMutationResult{}, false, nil
	}
	step := record.Step
	if step <= 0 {
		step = 1
	}
	toolName := strings.TrimSpace(record.ToolName)
	if toolName == "" {
		toolName = "tool"
	}
	arguments := strings.TrimSpace(firstNonEmpty(record.ToolCallArguments, record.ToolArguments))
	payload := sessionV3PermissionUpdatedPayload(sessionID, runID, step, toolName, callID, arguments, record)
	raw, err := json.Marshal(payload)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, false, err
	}
	payloadHash, err := sessionV3ExecutorPayloadHash(sessionID, runID, sessionruntime.RunIntentRunning, "", "permission.updated", string(raw))
	if err != nil {
		return sessionruntime.SessionMutationResult{}, false, err
	}
	clientRequestID := sessionV3ProviderToolEventClientRequestID("permission.updated", runID, step, callID, 0)
	now := time.Now().UnixMilli()
	intent := pebblestore.V3SessionRunIntent{RunID: runID, Status: sessionruntime.RunIntentRunning, UpdatedAt: now}
	if principal.Valid() {
		intent.UserID = strings.TrimSpace(principal.UserID)
		intent.AccountScopeID = strings.TrimSpace(principal.AccountScopeID)
	} else if session, ok, sessionErr := s.sessions.GetSession(sessionID); sessionErr != nil {
		return sessionruntime.SessionMutationResult{}, false, sessionErr
	} else if ok {
		intent.UserID = strings.TrimSpace(session.UserID)
		intent.AccountScopeID = strings.TrimSpace(session.AccountScopeID)
		principal = identity.Principal{Type: identity.PrincipalTypeUser, UserID: session.UserID, AccountScopeID: session.AccountScopeID}
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          strings.TrimSpace(principal.UserID),
		AccountScopeID:  strings.TrimSpace(principal.AccountScopeID),
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       "permission.updated",
		EventPayload:    raw,
		RunIntent:       &intent,
		NowUnixMs:       now,
	})
	if err != nil {
		return result, false, err
	}
	return result, !result.Replayed, nil
}

func (s *Server) authorizeSessionsV3PrimarySession(principal identity.Principal, sessionID string) (bool, error) {
	_, ok, err := s.requireSessionV3Access(principal, sessionID)
	return ok, err
}

func (s *Server) requireSessionV3Access(principal identity.Principal, sessionID string) (pebblestore.SessionSnapshot, bool, error) {
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return pebblestore.SessionSnapshot{}, ok, err
	}
	if strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return pebblestore.SessionSnapshot{}, false, nil
	}
	if strings.TrimSpace(session.UserID) == "" || strings.TrimSpace(session.UserID) != strings.TrimSpace(principal.UserID) {
		return pebblestore.SessionSnapshot{}, false, nil
	}
	return session, true, nil
}

func sessionV3ModeMutationResponse(sessionID, mode string, mutation *sessionruntime.SessionMutationResult) map[string]any {
	response := map[string]any{
		"ok":              true,
		"session_id":      sessionID,
		"mode":            mode,
		"mutation":        nil,
		"realtime_outbox": nil,
	}
	if mutation != nil {
		response["mutation"] = sessionV3MutationResultResponse(*mutation)
		response["realtime_outbox"] = mutation.RealtimeOutbox
	}
	return response
}

func (s *Server) validateSessionsV3PrimaryStopTarget(principal identity.Principal, sessionID, targetSwarmID string) (bool, error) {
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return ok, err
	}
	if strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return false, nil
	}
	localNode, localOK, err := s.swarmLocalNode()
	if err != nil {
		return false, err
	}
	primarySwarmID := strings.TrimSpace(localNode.SwarmID)
	if !localOK || primarySwarmID == "" {
		return false, errors.New("sessions v3 primary local node identity is required")
	}
	if strings.TrimSpace(targetSwarmID) == "" {
		return false, errors.New("target_swarm_id is required")
	}
	if strings.TrimSpace(targetSwarmID) != primarySwarmID {
		return false, fmt.Errorf("target_swarm_id %q is not the primary runtime", strings.TrimSpace(targetSwarmID))
	}
	metadataSwarmID := sessionsV3MetadataString(session.Metadata, "swarm_v3_runtime_swarm_id")
	if metadataSwarmID != "" && metadataSwarmID != primarySwarmID {
		return false, fmt.Errorf("session runtime swarm_id %q is not the primary runtime", metadataSwarmID)
	}
	return true, nil
}

func (s *Server) hydrateSessionsV3Primary(principal identity.Principal, sessionID string) (sessionsV3HydratedSession, bool, error) {
	return s.hydrateSessionsV3PrimaryWithLimits(principal, sessionID, sessionsV3PrimaryDefaultMessageTailLimit, sessionsV3PrimaryDefaultEventLimit)
}

func (s *Server) sessionsV3PrimaryUsageResponse(principal identity.Principal, sessionID string) (map[string]any, bool, error) {
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return nil, ok, err
	}
	if strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return nil, false, nil
	}
	summary, hasSummary, err := s.sessions.GetUsageSummary(sessionID)
	if err != nil {
		return nil, false, err
	}
	var summaryPayload any
	if hasSummary {
		summaryPayload = summary
	}
	return map[string]any{
		"ok":                true,
		"session_id":        session.ID,
		"has_usage_summary": hasSummary,
		"usage_summary":     summaryPayload,
	}, true, nil
}

func (s *Server) sessionV3PreferenceMutationResponse(sessionID string, preference pebblestore.ModelPreference, contextWindow, maxOutputTokens int, result sessionruntime.SessionMutationResult) map[string]any {
	return map[string]any{
		"ok":                true,
		"session_id":        sessionID,
		"preference":        preference,
		"context_window":    contextWindow,
		"max_output_tokens": maxOutputTokens,
		"mutation":          sessionV3MutationResultResponse(result),
		"realtime_outbox":   result.RealtimeOutbox,
	}
}

func (s *Server) sessionsV3PrimaryPreferenceResponse(principal identity.Principal, sessionID string) (map[string]any, bool, error) {
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return nil, ok, err
	}
	if strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return nil, false, nil
	}
	preference := normalizeSessionsV3ModelPreference(session.Preference)
	contextWindow := 0
	maxOutputTokens := 0
	if s.model != nil {
		resolved, err := s.model.ResolvePreference(preference)
		if err != nil {
			return nil, false, err
		}
		preference = normalizeSessionsV3ModelPreference(resolved.Preference)
		contextWindow = resolved.ContextWindow
		maxOutputTokens = resolved.MaxOutputTokens
	}
	agentModelPolicy := s.sessionsV3AgentModelPolicy(session, preference, contextWindow, maxOutputTokens)
	if agentModelPolicy.Locked {
		preference = agentModelPolicy.Preference
		contextWindow = agentModelPolicy.ContextWindow
		maxOutputTokens = agentModelPolicy.MaxOutputTokens
	}
	return map[string]any{
		"ok":                true,
		"session_id":        session.ID,
		"preference":        preference,
		"context_window":    contextWindow,
		"max_output_tokens": maxOutputTokens,
	}, true, nil
}

func (s *Server) hydrateSessionsV3PrimaryWithLimits(principal identity.Principal, sessionID string, messageLimit, eventLimit int) (sessionsV3HydratedSession, bool, error) {
	hydrated, ok, err := s.sessions.HydrateSessionSnapshot(sessionID, messageLimit, eventLimit)
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
	var activeRunIntent *pebblestore.V3SessionRunIntent
	if intent, ok, err := s.sessions.GetSessionActiveRunIntent(sessionID); err != nil {
		return sessionsV3HydratedSession{}, false, err
	} else if ok && sessionV3RunIntentStatusActive(intent.Status) {
		activeRunIntent = &intent
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
	snapshotEndpointCursor, err := s.signV3SyncEndpointCursorFromLegacy(v3SyncCursorScopeForRealtime(principal, "desktop"), hydrated.SnapshotEndpointCursor)
	if err != nil {
		return sessionsV3HydratedSession{}, false, err
	}
	return sessionsV3HydratedSession{Session: hydrated.Session, Projection: hydrated.Projection, Messages: hydrated.Messages, Events: hydrated.Events, PendingPermissions: pendingPermissions, UsageSummary: usageSummary, ActiveRunIntent: activeRunIntent, Preference: preference, ContextWindow: contextWindow, MaxOutputTokens: maxOutputTokens, AgentModelPolicy: agentModelPolicy, PlanRevisions: []pebblestore.SessionPlanSnapshot{}, AppliedSeq: hydrated.Projection.LastEventSeq, HighWatermark: hydrated.Projection.ProjectionHighWatermarkSeq, SnapshotEndpointCursor: snapshotEndpointCursor}, true, nil
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

func sessionV3MutationResultResponse(result sessionruntime.SessionMutationResult) sessionruntime.SessionMutationResult {
	mutation := result
	mutation.Session = nil
	return mutation
}

func sessionV3MessageMutationResponse(sessionID string, result sessionruntime.SessionMutationResult) map[string]any {
	return map[string]any{
		"ok":              true,
		"session_id":      sessionID,
		"message":         result.Message,
		"run_intent":      result.RunIntent,
		"mutation":        sessionV3MutationResultResponse(result),
		"realtime_outbox": result.RealtimeOutbox,
	}
}

func sessionV3MetadataMutationResponse(sessionID string, metadata map[string]any, result sessionruntime.SessionMutationResult) map[string]any {
	return map[string]any{
		"ok":              true,
		"session_id":      sessionID,
		"metadata":        metadata,
		"mutation":        sessionV3MutationResultResponse(result),
		"realtime_outbox": result.RealtimeOutbox,
	}
}

func sessionV3AgentMutationResponse(sessionID string, session pebblestore.SessionSnapshot, agentModelPolicy sessionsV3AgentModelPolicy, result sessionruntime.SessionMutationResult) map[string]any {
	return map[string]any{
		"ok":                 true,
		"session_id":         sessionID,
		"agent":              sessionsV3AgentResource(session),
		"agent_model_policy": agentModelPolicy,
		"mutation":           sessionV3MutationResultResponse(result),
		"realtime_outbox":    result.RealtimeOutbox,
	}
}

func sessionsV3AgentResource(session pebblestore.SessionSnapshot) map[string]any {
	metadata := session.Metadata
	return map[string]any{
		"agent_name":             sessionsV3MetadataString(metadata, "agent_name"),
		"resolved_agent_name":    sessionsV3MetadataString(metadata, "resolved_agent_name"),
		"agent_mode":             sessionsV3MetadataString(metadata, "agent_mode"),
		"runtime_mode":           sessionsV3MetadataString(metadata, "runtime_mode"),
		"exit_plan_mode_enabled": metadata["exit_plan_mode_enabled"],
		"tool_contract_preset":   sessionsV3MetadataString(metadata, "tool_contract_preset"),
	}
}

func sessionV3CreateResultResponse(result sessionruntime.SessionMutationResult) (map[string]any, error) {
	if result.Session == nil {
		return nil, errors.New("created sessions v3 session was not returned")
	}
	if strings.TrimSpace(result.SessionID) == "" {
		return nil, errors.New("created sessions v3 session_id was not returned")
	}
	return map[string]any{
		"ok":              true,
		"session_id":      result.SessionID,
		"session":         result.Session,
		"projection":      result.Projection,
		"mutation":        sessionV3MutationResultResponse(result),
		"realtime_outbox": result.RealtimeOutbox,
	}, nil
}

func sessionsV3HydratedResponse(hydrated sessionsV3HydratedSession) map[string]any {
	response := map[string]any{
		"ok":                       true,
		"session":                  hydrated.Session,
		"projection":               hydrated.Projection,
		"messages":                 hydrated.Messages,
		"events":                   hydrated.Events,
		"pending_permissions":      hydrated.PendingPermissions,
		"usage_summary":            hydrated.UsageSummary,
		"active_run_intent":        hydrated.ActiveRunIntent,
		"preference":               hydrated.Preference,
		"context_window":           hydrated.ContextWindow,
		"max_output_tokens":        hydrated.MaxOutputTokens,
		"agent_model_policy":       hydrated.AgentModelPolicy,
		"has_active_plan":          hydrated.HasActivePlan,
		"active_plan":              nil,
		"plan_revisions":           hydrated.PlanRevisions,
		"applied_seq":              hydrated.AppliedSeq,
		"high_watermark":           hydrated.HighWatermark,
		"snapshot_endpoint_cursor": hydrated.SnapshotEndpointCursor,
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

func parseSessionsV3PlansLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	limit, ok := parseSessionsV2PositiveLimit(w, r, sessionsV3PlansPageDefaultLimit)
	if !ok {
		return 0, false
	}
	if limit > sessionsV3PlansPageMaxLimit {
		writeError(w, http.StatusBadRequest, errors.New("plan limit cannot exceed 100"))
		return 0, false
	}
	return limit, true
}

func parseSessionsV3HydrationLimits(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	messageLimit := sessionsV3PrimaryDefaultMessageTailLimit
	if raw := strings.TrimSpace(firstNonEmpty(r.URL.Query().Get("message_limit"), r.URL.Query().Get("tail_limit"))); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, errors.New("message_limit must be a non-negative integer"))
			return 0, 0, false
		}
		messageLimit = parsed
	}
	eventLimit := sessionsV3PrimaryDefaultEventLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("event_limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, errors.New("event_limit must be a non-negative integer"))
			return 0, 0, false
		}
		eventLimit = parsed
	}
	return messageLimit, eventLimit, true
}

type sessionsV3MessagesPageQuery struct {
	AfterSeq     uint64
	BeforeSeq    uint64
	HasBeforeSeq bool
	Tail         bool
	Limit        int
}

func parseSessionsV3MessagesPageQuery(w http.ResponseWriter, r *http.Request) (sessionsV3MessagesPageQuery, bool) {
	query := sessionsV3MessagesPageQuery{Limit: sessionsV3MessagesPageDefaultLimit}
	hasAfterSeq := false
	if raw := strings.TrimSpace(r.URL.Query().Get("after_seq")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("after_seq must be an unsigned integer"))
			return sessionsV3MessagesPageQuery{}, false
		}
		query.AfterSeq = parsed
		hasAfterSeq = true
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("before_seq")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("before_seq must be an unsigned integer"))
			return sessionsV3MessagesPageQuery{}, false
		}
		query.BeforeSeq = parsed
		query.HasBeforeSeq = true
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("tail")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("tail must be a boolean"))
			return sessionsV3MessagesPageQuery{}, false
		}
		query.Tail = parsed
	}
	if query.HasBeforeSeq && hasAfterSeq {
		writeError(w, http.StatusBadRequest, errors.New("after_seq and before_seq cannot be combined"))
		return sessionsV3MessagesPageQuery{}, false
	}
	if query.Tail && (query.HasBeforeSeq || hasAfterSeq) {
		writeError(w, http.StatusBadRequest, errors.New("tail cannot be combined with after_seq or before_seq"))
		return sessionsV3MessagesPageQuery{}, false
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
			return sessionsV3MessagesPageQuery{}, false
		}
		query.Limit = parsed
	}
	if query.Limit > sessionsV3MessagesPageMaxLimit {
		query.Limit = sessionsV3MessagesPageMaxLimit
	}
	return query, true
}

func sessionsV3MessagesPageResponse(sessionID string, messages []pebblestore.MessageSnapshot, query sessionsV3MessagesPageQuery) map[string]any {
	hasMoreOlder := false
	hasMoreNewer := false
	if query.Tail || query.HasBeforeSeq {
		if len(messages) > query.Limit {
			hasMoreOlder = true
			messages = messages[len(messages)-query.Limit:]
		}
	} else if len(messages) > query.Limit {
		hasMoreNewer = true
		messages = messages[:query.Limit]
	}

	oldestSeq := uint64(0)
	newestSeq := uint64(0)
	if len(messages) > 0 {
		oldestSeq = messages[0].GlobalSeq
		newestSeq = messages[len(messages)-1].GlobalSeq
	}
	response := map[string]any{
		"ok":              true,
		"session_id":      sessionID,
		"messages":        messages,
		"count":           len(messages),
		"limit":           query.Limit,
		"oldest_seq":      oldestSeq,
		"newest_seq":      newestSeq,
		"has_more":        hasMoreOlder || hasMoreNewer,
		"has_more_older":  hasMoreOlder,
		"has_more_newer":  hasMoreNewer,
		"next_before_seq": uint64(0),
		"next_after_seq":  uint64(0),
		"page_cursor":     nil,
	}
	if len(messages) > 0 {
		response["next_before_seq"] = oldestSeq
		response["next_after_seq"] = newestSeq
	}
	if query.Tail {
		response["tail"] = true
		response["has_more"] = hasMoreOlder
		if len(messages) > 0 {
			response["page_cursor"] = oldestSeq
		}
		return response
	}
	if query.HasBeforeSeq {
		hasMoreNewer = len(messages) > 0 && query.BeforeSeq > newestSeq
		response["before_seq"] = query.BeforeSeq
		response["has_more_newer"] = hasMoreNewer
		response["has_more"] = hasMoreOlder || hasMoreNewer
		if len(messages) > 0 {
			response["page_cursor"] = oldestSeq
		}
		return response
	}
	hasMoreOlder = len(messages) > 0 && query.AfterSeq > 0
	response["after_seq"] = query.AfterSeq
	response["has_more_older"] = hasMoreOlder
	response["has_more"] = hasMoreOlder || hasMoreNewer
	if len(messages) > 0 {
		response["page_cursor"] = newestSeq
	}
	return response
}

type sessionsV3PrimaryBinding struct {
	RuntimeSwarmID            string
	WorkspaceBindingID        string
	SourceWorkspaceID         string
	SourceWorkspaceGeneration int64
	SourceWorkspaceName       string
	SourceWorkspacePath       string
	RuntimeWorkspacePath      string
	PlacementGeneration       int
	BindingGeneration         int
}

func (s *Server) resolveSessionsV3PrimaryBinding(principal identity.Principal, req sessionsV3CreateRequest) (sessionsV3PrimaryBinding, error) {
	if s == nil || s.topology == nil {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary topology is not configured")
	}
	localNode, localOK, err := s.swarmLocalNode()
	if err != nil {
		return sessionsV3PrimaryBinding{}, err
	}
	primarySwarmID := strings.TrimSpace(localNode.SwarmID)
	if !localOK || primarySwarmID == "" {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary local node identity is required")
	}
	swarmID := strings.TrimSpace(req.SwarmID)
	if swarmID == "" {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary swarm_id is required")
	}
	if swarmID != primarySwarmID {
		return sessionsV3PrimaryBinding{}, fmt.Errorf("sessions v3 primary swarm_id %q is not the primary runtime", swarmID)
	}
	if targetKind := strings.ToLower(strings.TrimSpace(req.TargetKind)); targetKind != "" && targetKind != "host" && targetKind != "self" {
		return sessionsV3PrimaryBinding{}, fmt.Errorf("sessions v3 primary target_kind %q is not primary host", strings.TrimSpace(req.TargetKind))
	}
	if targetRelationship := strings.ToLower(strings.TrimSpace(req.TargetRelationship)); targetRelationship != "" && targetRelationship != "self" {
		return sessionsV3PrimaryBinding{}, fmt.Errorf("sessions v3 primary target_relationship %q is not self", strings.TrimSpace(req.TargetRelationship))
	}
	workspaceBindingID := strings.TrimSpace(req.WorkspaceBindingID)
	if workspaceBindingID == "" {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace_binding_id is required")
	}
	runtimeRecord, runtimeOK, err := s.topology.GetRuntimeForAccount(principal.AccountScopeID, swarmID)
	if err != nil {
		return sessionsV3PrimaryBinding{}, err
	}
	if !runtimeOK {
		return sessionsV3PrimaryBinding{}, fmt.Errorf("sessions v3 primary runtime %q was not found", swarmID)
	}
	if strings.TrimSpace(runtimeRecord.SwarmID) != swarmID {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime identity mismatch")
	}
	if strings.TrimSpace(runtimeRecord.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime account scope does not match principal")
	}
	if strings.TrimSpace(runtimeRecord.UserID) != "" && strings.TrimSpace(runtimeRecord.UserID) != strings.TrimSpace(principal.UserID) {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime user does not match principal")
	}
	placement, placementOK, err := s.topology.GetRuntimePlacementForAccount(principal.AccountScopeID, swarmID)
	if err != nil {
		return sessionsV3PrimaryBinding{}, err
	}
	if !placementOK {
		return sessionsV3PrimaryBinding{}, fmt.Errorf("sessions v3 primary runtime placement for %q was not found", swarmID)
	}
	if strings.TrimSpace(placement.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime placement account scope does not match principal")
	}
	if strings.TrimSpace(placement.State) != pebblestore.TopologyRuntimePlacementStateActive {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime placement is not active")
	}
	if strings.TrimSpace(placement.RuntimeSwarmID) != swarmID || strings.TrimSpace(placement.AuthorityHostSwarmID) != swarmID {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime placement does not match selected self authority")
	}
	if strings.TrimSpace(placement.RuntimeKind) != pebblestore.TopologyRuntimeKindHost {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime placement kind must be host")
	}
	if strings.TrimSpace(placement.AuthorityContainerID) != "" {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime placement authority container id must be empty")
	}
	if placement.PlacementGeneration <= 0 {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime placement generation is required")
	}
	binding, bindingOK, err := s.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, workspaceBindingID)
	if err != nil {
		return sessionsV3PrimaryBinding{}, err
	}
	if !bindingOK {
		return sessionsV3PrimaryBinding{}, fmt.Errorf("sessions v3 primary workspace binding %q was not found", workspaceBindingID)
	}
	if strings.TrimSpace(binding.BindingID) != workspaceBindingID {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding id mismatch")
	}
	if strings.TrimSpace(binding.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding account scope does not match principal")
	}
	if strings.TrimSpace(binding.UserID) != "" && strings.TrimSpace(binding.UserID) != strings.TrimSpace(principal.UserID) {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding user does not match principal")
	}
	if strings.TrimSpace(binding.State) != pebblestore.TopologyWorkspaceBindingStateBound {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding is not bound")
	}
	if strings.TrimSpace(binding.SourceWorkspaceID) == "" || binding.SourceWorkspaceGeneration <= 0 || strings.TrimSpace(binding.SourceWorkspacePath) == "" {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding source workspace identity is incomplete")
	}
	if strings.TrimSpace(binding.DestinationWorkspacePath) == "" {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding destination workspace path is required")
	}
	if binding.PlacementGeneration != placement.PlacementGeneration || binding.BindingGeneration <= 0 {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding generation does not match placement")
	}
	if strings.TrimSpace(binding.AttestedByHostSwarmID) != strings.TrimSpace(placement.AuthorityHostSwarmID) {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding attesting host does not match authority host")
	}
	if strings.TrimSpace(binding.AccessMode) != pebblestore.TopologyWorkspaceBindingAccessModeReadWrite || !binding.Writable {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding must be read_write and writable")
	}
	if strings.TrimSpace(binding.DestinationRuntimeSwarmID) != swarmID || strings.TrimSpace(binding.DestinationAuthorityHostSwarmID) != swarmID {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding does not match selected self authority")
	}
	if strings.TrimSpace(binding.DestinationRuntimeKind) != pebblestore.TopologyRuntimeKindHost {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding destination runtime kind must be host")
	}
	if strings.TrimSpace(binding.DestinationContainerID) != "" {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding destination container id must be empty")
	}
	if requestedWorkspacePath := strings.TrimSpace(req.WorkspacePath); requestedWorkspacePath != "" && filepath.Clean(requestedWorkspacePath) != filepath.Clean(strings.TrimSpace(binding.SourceWorkspacePath)) {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace_path does not match workspace binding source")
	}
	if requestedHostWorkspacePath := strings.TrimSpace(req.HostWorkspacePath); requestedHostWorkspacePath != "" && filepath.Clean(requestedHostWorkspacePath) != filepath.Clean(strings.TrimSpace(binding.SourceWorkspacePath)) {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary host_workspace_path does not match workspace binding source")
	}
	if requestedRuntimeWorkspacePath := strings.TrimSpace(req.RuntimeWorkspacePath); requestedRuntimeWorkspacePath != "" && filepath.Clean(requestedRuntimeWorkspacePath) != filepath.Clean(strings.TrimSpace(binding.DestinationWorkspacePath)) {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime_workspace_path does not match workspace binding destination")
	}
	return sessionsV3PrimaryBinding{
		RuntimeSwarmID:            swarmID,
		WorkspaceBindingID:        workspaceBindingID,
		SourceWorkspaceID:         strings.TrimSpace(binding.SourceWorkspaceID),
		SourceWorkspaceGeneration: binding.SourceWorkspaceGeneration,
		SourceWorkspaceName:       strings.TrimSpace(binding.SourceWorkspaceName),
		SourceWorkspacePath:       strings.TrimSpace(binding.SourceWorkspacePath),
		RuntimeWorkspacePath:      strings.TrimSpace(binding.DestinationWorkspacePath),
		PlacementGeneration:       placement.PlacementGeneration,
		BindingGeneration:         binding.BindingGeneration,
	}, nil
}

func validateSessionsV3CreateWorktreeRequest(rawMode string, useCurrentBranch *bool, baseBranch, branchName string) (string, error) {
	mode := runruntime.NormalizeRunWorktreeMode(rawMode)
	if strings.TrimSpace(rawMode) != "" && mode == "" {
		return "", fmt.Errorf("unsupported worktree_mode %q", strings.TrimSpace(rawMode))
	}
	switch mode {
	case "", runruntime.RunWorktreeModeInherit, runruntime.RunWorktreeModeOff:
		if useCurrentBranch != nil || strings.TrimSpace(baseBranch) != "" || strings.TrimSpace(branchName) != "" {
			return "", errors.New("worktree fields are only allowed when worktree_mode is on")
		}
		return mode, nil
	case runruntime.RunWorktreeModeOn:
		if strings.TrimSpace(branchName) == "" {
			return "", errors.New("worktree_branch_name is required when worktree_mode is on")
		}
		if useCurrentBranch != nil {
			if *useCurrentBranch && strings.TrimSpace(baseBranch) != "" {
				return "", errors.New("worktree_use_current_branch cannot be true when worktree_base_branch is set")
			}
			if !*useCurrentBranch && strings.TrimSpace(baseBranch) == "" {
				return "", errors.New("worktree_base_branch is required when worktree_use_current_branch is false")
			}
		}
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported worktree_mode %q", strings.TrimSpace(rawMode))
	}
}

func (s *Server) handleSessionsV3CreateReplay(w http.ResponseWriter, principal identity.Principal, sessionID, clientRequestID, payloadHash string, session pebblestore.SessionSnapshot) bool {
	if s == nil || s.sessions == nil || s.sessions.Store() == nil {
		return false
	}
	if _, ok, err := s.sessions.Store().GetV3SessionOperationIdempotencyRecord(principal.AccountScopeID, sessionID, sessionruntime.SessionMutationCreateSession, clientRequestID); err != nil || !ok {
		return false
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
		NowUnixMs:       time.Now().UnixMilli(),
	})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "conflict": result.Conflict, "result": result})
			return true
		}
		writeError(w, http.StatusBadRequest, err)
		return true
	}
	response, err := sessionV3CreateResultResponse(result)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return true
	}
	writeJSON(w, http.StatusOK, response)
	return true
}

func (s *Server) allocateSessionsV3CreateWorktree(principal identity.Principal, workspacePath, sessionID string, requestedUseCurrentBranch *bool, requestedBaseBranch, requestedBranchName string) (worktreeruntime.Allocation, error) {
	if s == nil || s.worktrees == nil {
		return worktreeruntime.Allocation{}, errors.New("worktree_mode on requires worktree service")
	}
	baseBranch := strings.TrimSpace(requestedBaseBranch)
	if requestedUseCurrentBranch != nil && *requestedUseCurrentBranch {
		baseBranch = ""
	}
	allocation, err := s.worktrees.AllocateDetachedWorkspaceRequestedForPrincipal(principal, workspacePath, sessionID, baseBranch, strings.TrimSpace(requestedBranchName))
	if err != nil {
		return worktreeruntime.Allocation{}, fmt.Errorf("realize sessions v3 worktree: %w", err)
	}
	if strings.TrimSpace(allocation.WorkspacePath) == "" || strings.TrimSpace(allocation.BaseBranch) == "" || strings.TrimSpace(allocation.BranchName) == "" || strings.TrimSpace(allocation.WorkspaceID) == "" {
		return worktreeruntime.Allocation{}, errors.New("worktree_mode on did not allocate complete worktree facts")
	}
	if workspaceID := strings.TrimSpace(allocation.WorkspaceID); workspaceID != worktreeruntime.WorkspaceIdentityForSession(sessionID) {
		return worktreeruntime.Allocation{}, errors.New("worktree_mode on allocation workspace identity mismatch")
	}
	return allocation, nil
}

func sessionsV3CreatePayloadHash(sessionID string, req sessionsV3CreateRequest, workspacePath, workspaceName, title string, metadata map[string]any) (string, error) {
	canonical := struct {
		Operation                string                      `json:"operation"`
		SessionID                string                      `json:"session_id"`
		Title                    string                      `json:"title"`
		WorkspacePath            string                      `json:"workspace_path"`
		WorkspaceName            string                      `json:"workspace_name"`
		WorkspaceBindingID       string                      `json:"workspace_binding_id"`
		SwarmID                  string                      `json:"swarm_id"`
		Mode                     string                      `json:"mode"`
		AgentName                string                      `json:"agent_name,omitempty"`
		Preference               pebblestore.ModelPreference `json:"preference"`
		WorktreeMode             string                      `json:"worktree_mode,omitempty"`
		WorktreeUseCurrentBranch *bool                       `json:"worktree_use_current_branch,omitempty"`
		WorktreeBaseBranch       string                      `json:"worktree_base_branch,omitempty"`
		WorktreeBranchName       string                      `json:"worktree_branch_name,omitempty"`
		Metadata                 map[string]any              `json:"metadata,omitempty"`
	}{
		Operation:                sessionruntime.SessionMutationCreateSession,
		SessionID:                strings.TrimSpace(sessionID),
		Title:                    title,
		WorkspacePath:            strings.TrimSpace(workspacePath),
		WorkspaceName:            workspaceName,
		WorkspaceBindingID:       strings.TrimSpace(req.WorkspaceBindingID),
		SwarmID:                  strings.TrimSpace(req.SwarmID),
		Mode:                     sessionruntime.NormalizeMode(req.Mode),
		AgentName:                strings.TrimSpace(req.AgentName),
		Preference:               normalizeSessionsV3ModelPreference(req.Preference),
		WorktreeMode:             runruntime.NormalizeRunWorktreeMode(req.WorktreeMode),
		WorktreeUseCurrentBranch: req.WorktreeUseCurrentBranch,
		WorktreeBaseBranch:       strings.TrimSpace(req.WorktreeBaseBranch),
		WorktreeBranchName:       strings.TrimSpace(req.WorktreeBranchName),
		Metadata:                 cloneSessionsV3Metadata(metadata),
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

func sessionsV3CreateServerMetadata(clientMetadata map[string]any, agent sessionsV3ResolvedAgentIdentity, binding sessionsV3PrimaryBinding) map[string]any {
	metadata := cloneSessionsV3Metadata(clientMetadata)
	if metadata == nil {
		metadata = make(map[string]any, 24)
	}
	metadata["agent_name"] = agent.Name
	metadata["resolved_agent_name"] = agent.ResolvedName
	metadata["agent_mode"] = agent.Mode
	metadata["runtime_mode"] = agent.RuntimeMode
	metadata["exit_plan_mode_enabled"] = agent.ExitPlanModeEnabled
	metadata["agent_profile"] = cloneSessionsV3AgentProfile(agent.Profile)
	metadata["swarm_v3_execution_class"] = sessionruntime.SessionExecutionClassPrimary
	metadata["swarm_v3_runtime_swarm_id"] = binding.RuntimeSwarmID
	metadata["swarm_v3_runtime_kind"] = pebblestore.TopologyRuntimeKindHost
	metadata["swarm_v3_authority_host_swarm_id"] = binding.RuntimeSwarmID
	metadata["swarm_v3_workspace_binding_id"] = binding.WorkspaceBindingID
	metadata["swarm_v3_source_workspace_id"] = binding.SourceWorkspaceID
	metadata["swarm_v3_source_workspace_generation"] = fmt.Sprintf("%d", binding.SourceWorkspaceGeneration)
	metadata["swarm_v3_source_workspace_name"] = binding.SourceWorkspaceName
	metadata["swarm_v3_source_workspace_path"] = binding.SourceWorkspacePath
	metadata["swarm_v3_runtime_workspace_path"] = binding.RuntimeWorkspacePath
	metadata["swarm_v3_placement_generation"] = binding.PlacementGeneration
	metadata["swarm_v3_binding_generation"] = binding.BindingGeneration
	metadata["local_workspace_binding_id"] = binding.WorkspaceBindingID
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
		"swarm_v3_runtime_kind",
		"swarm_v3_authority_host_swarm_id",
		"swarm_v3_authority_container_id",
		"swarm_v3_workspace_binding_id",
		"swarm_v3_source_workspace_id",
		"swarm_v3_source_workspace_generation",
		"swarm_v3_source_workspace_name",
		"swarm_v3_source_workspace_path",
		"swarm_v3_runtime_workspace_path",
		"swarm_v3_placement_generation",
		"swarm_v3_binding_generation",
		"swarm_v3_tui_directory_session",
		"swarm_v3_tui_cwd_path",
		"swarm_v3_tui_original_cwd_path":
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
