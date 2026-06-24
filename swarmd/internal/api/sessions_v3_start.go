package api

import (
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
	"swarm/packages/swarmd/internal/sessionconnection"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type sessionsV3StartRequest struct {
	ClientID                 string                      `json:"client_id"`
	RequestID                string                      `json:"request_id"`
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
	FirstMessage             sessionsV3StartMessage      `json:"first_message"`
}

type sessionsV3StartMessage struct {
	ClientRequestID   string         `json:"client_request_id,omitempty"`
	IdempotencyKey    string         `json:"idempotency_key,omitempty"`
	MessageID         string         `json:"message_id"`
	RunID             string         `json:"run_id"`
	Role              string         `json:"role"`
	Content           string         `json:"content"`
	Metadata          map[string]any `json:"metadata,omitempty"`
	DispatchAuthority map[string]any `json:"dispatch_authority,omitempty"`
	Authority         map[string]any `json:"authority,omitempty"`
}

func (s *Server) handleSessionsV3Start(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeSessionsV3StartError(w, http.StatusInternalServerError, "service_unavailable", "sessions v3 service is not configured", true)
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		writeSessionsV3StartError(w, http.StatusUnauthorized, "authorization_failed", identity.ErrPrincipalRequired.Error(), false)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req sessionsV3StartRequest
	if err := decodeJSON(r, &req); err != nil {
		writeSessionsV3StartError(w, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	requestID := strings.TrimSpace(firstNonEmpty(req.RequestID, req.ClientRequestID, req.IdempotencyKey, r.Header.Get("Idempotency-Key")))
	if requestID == "" {
		writeSessionsV3StartError(w, http.StatusBadRequest, "invalid_request", "request_id is required", false)
		return
	}
	clientID := strings.TrimSpace(req.ClientID)
	if clientID == "" {
		writeSessionsV3StartError(w, http.StatusBadRequest, "invalid_request", "client_id is required", false)
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		writeSessionsV3StartError(w, http.StatusBadRequest, "invalid_request", "session_id is required", false)
		return
	}
	messageReq := req.toMessageRequest(requestID)
	if err := validateSessionsV3StartFirstMessage(messageReq); err != nil {
		writeSessionsV3StartError(w, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	createReq := req.toCreateRequest(requestID)
	session, createPayloadHash, now, err := s.prepareSessionsV3StartSession(principal, sessionID, createReq)
	if err != nil {
		writeSessionsV3StartError(w, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	createClientRequestID := requestID + ":create"
	createResult, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: createClientRequestID,
		IdempotencyKey:  createClientRequestID,
		PayloadHash:     createPayloadHash,
		RequestHash:     createPayloadHash,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &session,
		NowUnixMs:       now,
	})
	if err != nil {
		writeSessionsV3StartMutationError(w, sessionID, createResult, err)
		return
	}
	messageResult, enqueueJob, err := s.acceptSessionsV3Message(principal, sessionID, messageReq)
	if err != nil {
		writeSessionsV3StartMutationError(w, sessionID, messageResult, err)
		return
	}
	accepted, err := sessionV3MessageAcceptedResponse(sessionID, messageResult)
	if err != nil {
		writeSessionsV3StartError(w, http.StatusInternalServerError, "service_unavailable", err.Error(), true)
		return
	}
	connectResp, err := s.connectSessionsV3Snapshot(principal, sessionID, clientID, requestID, "")
	if err != nil {
		writeSessionConnectionConnectError(w, sessionID, err)
		return
	}
	writeJSON(w, http.StatusOK, SessionStartResponse{
		Ok:               true,
		ContractVersion:  connectResp.ContractVersion,
		SessionId:        connectResp.SessionId,
		Snapshot:         connectResp.Snapshot,
		Connection:       connectResp.Connection,
		Message:          accepted.Message,
		Run:              accepted.Run,
		AcceptedEventSeq: accepted.AcceptedEventSeq,
	})
	if enqueueJob != nil {
		s.v3SessionExecutor.EnqueueRun(*enqueueJob)
	}
}

func (req sessionsV3StartRequest) toCreateRequest(requestID string) sessionsV3CreateRequest {
	return sessionsV3CreateRequest{
		SessionID:                strings.TrimSpace(req.SessionID),
		ClientRequestID:          requestID + ":create",
		IdempotencyKey:           requestID + ":create",
		Title:                    req.Title,
		WorkspacePath:            req.WorkspacePath,
		WorkspaceName:            req.WorkspaceName,
		WorkspaceBindingID:       req.WorkspaceBindingID,
		SwarmID:                  req.SwarmID,
		TargetKind:               req.TargetKind,
		TargetRelationship:       req.TargetRelationship,
		HostWorkspacePath:        req.HostWorkspacePath,
		RuntimeWorkspacePath:     req.RuntimeWorkspacePath,
		Mode:                     req.Mode,
		AgentName:                req.AgentName,
		Preference:               req.Preference,
		WorktreeMode:             req.WorktreeMode,
		WorktreeUseCurrentBranch: req.WorktreeUseCurrentBranch,
		WorktreeBaseBranch:       req.WorktreeBaseBranch,
		WorktreeBranchName:       req.WorktreeBranchName,
		Metadata:                 cloneSessionsV3Metadata(req.Metadata),
	}
}

func (req sessionsV3StartRequest) toMessageRequest(requestID string) sessionsV3MessageRequest {
	message := req.FirstMessage
	messageClientRequestID := strings.TrimSpace(firstNonEmpty(message.ClientRequestID, message.IdempotencyKey))
	if messageClientRequestID == "" {
		messageClientRequestID = requestID + ":message"
	}
	return sessionsV3MessageRequest{
		ClientRequestID:   messageClientRequestID,
		IdempotencyKey:    messageClientRequestID,
		MessageID:         message.MessageID,
		RunID:             message.RunID,
		Role:              message.Role,
		Content:           message.Content,
		Metadata:          cloneSessionsV3Metadata(message.Metadata),
		DispatchAuthority: cloneSessionsV3Metadata(message.DispatchAuthority),
		Authority:         cloneSessionsV3Metadata(message.Authority),
	}
}

func validateSessionsV3StartFirstMessage(req sessionsV3MessageRequest) error {
	if strings.TrimSpace(req.ClientRequestID) == "" {
		return errors.New("first_message client_request_id or request_id is required")
	}
	if strings.TrimSpace(req.MessageID) == "" {
		return errors.New("first_message message_id is required")
	}
	if strings.TrimSpace(req.RunID) == "" {
		return errors.New("first_message run_id is required")
	}
	if strings.TrimSpace(req.Role) == "" {
		return errors.New("first_message role is required")
	}
	if req.Content == "" {
		return errors.New("first_message content is required")
	}
	return nil
}

func writeSessionsV3StartError(w http.ResponseWriter, status int, code, message string, retryable bool) {
	writeJSON(w, status, SessionConnectionError{Code: code, Message: message, Retryable: retryable, Action: SessionErrorAction{Method: http.MethodPost, Path: "/v3/sessions:start"}})
}

func (s *Server) prepareSessionsV3StartSession(principal identity.Principal, sessionID string, req sessionsV3CreateRequest) (pebblestore.SessionSnapshot, string, int64, error) {
	if err := validateSessionsV3CreateMetadata(req.Metadata); err != nil {
		return pebblestore.SessionSnapshot{}, "", 0, err
	}
	requestedWorktreeMode, err := validateSessionsV3CreateWorktreeRequest(req.WorktreeMode, req.WorktreeUseCurrentBranch, req.WorktreeBaseBranch, req.WorktreeBranchName)
	if err != nil {
		return pebblestore.SessionSnapshot{}, "", 0, err
	}
	binding, err := s.resolveSessionsV3PrimaryBinding(principal, req)
	if err != nil {
		return pebblestore.SessionSnapshot{}, "", 0, err
	}
	resolvedAgent, err := s.resolveSessionsV3PrimaryCreateAgent(principal, req.AgentName)
	if err != nil {
		return pebblestore.SessionSnapshot{}, "", 0, err
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
	if requestedWorktreeMode == runruntime.RunWorktreeModeOn {
		allocation, err := s.allocateSessionsV3CreateWorktree(principal, workspacePath, sessionID, req.WorktreeUseCurrentBranch, req.WorktreeBaseBranch, req.WorktreeBranchName)
		if err != nil {
			return pebblestore.SessionSnapshot{}, "", 0, err
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
	payloadHash, err := sessionsV3CreatePayloadHash(sessionID, req, session.WorkspacePath, workspaceName, title, session.Metadata)
	if err != nil {
		return pebblestore.SessionSnapshot{}, "", 0, err
	}
	return session, payloadHash, now, nil
}

func (s *Server) acceptSessionsV3Message(principal identity.Principal, sessionID string, req sessionsV3MessageRequest) (sessionruntime.SessionMutationResult, *sessionV3ExecutorJob, error) {
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, nil, err
	}
	if !found {
		return sessionruntime.SessionMutationResult{}, nil, errors.New("session not found")
	}
	if err := validateSessionsV3CreateMetadata(req.Metadata); err != nil {
		return sessionruntime.SessionMutationResult{}, nil, err
	}
	message := pebblestore.MessageSnapshot{ID: strings.TrimSpace(req.MessageID), Role: strings.TrimSpace(req.Role), Content: req.Content, Metadata: cloneSessionsV3Metadata(req.Metadata)}
	if message.Role == "" {
		return sessionruntime.SessionMutationResult{}, nil, errors.New("message role is required")
	}
	if message.Content == "" {
		return sessionruntime.SessionMutationResult{}, nil, errors.New("message content is required")
	}
	now := time.Now().UnixMilli()
	runStatus, blockedReason := s.sessionsV3PrimaryRunIntentStatus(principal, session, req)
	runIntent := &pebblestore.V3SessionRunIntent{RunID: strings.TrimSpace(req.RunID), Status: runStatus, BlockedReason: blockedReason}
	clientRequestID := strings.TrimSpace(firstNonEmpty(req.ClientRequestID, req.IdempotencyKey))
	if clientRequestID == "" {
		return sessionruntime.SessionMutationResult{}, nil, errors.New("client_request_id is required")
	}
	if runIntent.RunID == "" {
		runIntent.RunID = stableSessionsV3PrimaryRunID(sessionID, clientRequestID)
	}
	payloadHash, err := sessionsV3MessagePayloadHash(sessionID, req, message, runIntent.Status, runIntent.BlockedReason)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, nil, err
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
		return result, nil, err
	}
	var enqueueJob *sessionV3ExecutorJob
	if !result.Replayed && result.RunIntent != nil && result.RunIntent.Status == sessionruntime.RunIntentPendingExecutor && s.v3SessionExecutor != nil {
		enqueueJob = &sessionV3ExecutorJob{Principal: principal, SessionID: sessionID, RunID: result.RunIntent.RunID}
	}
	return result, enqueueJob, nil
}

func sessionV3MessageAcceptedResponse(sessionID string, result sessionruntime.SessionMutationResult) (SessionMessageAcceptedResponse, error) {
	if result.Message == nil {
		return SessionMessageAcceptedResponse{}, errors.New("accepted message was not returned")
	}
	if result.RunIntent == nil || strings.TrimSpace(result.RunIntent.RunID) == "" {
		return SessionMessageAcceptedResponse{}, errors.New("accepted run was not returned")
	}
	rawMessage, err := json.Marshal(result.Message)
	if err != nil {
		return SessionMessageAcceptedResponse{}, fmt.Errorf("marshal accepted message: %w", err)
	}
	acceptedSeq := result.LastSeq
	if acceptedSeq == 0 {
		acceptedSeq = result.PrimarySeq
	}
	if acceptedSeq == 0 {
		acceptedSeq = result.Event.Seq
	}
	return SessionMessageAcceptedResponse{Ok: true, SessionId: sessionID, Message: rawMessage, Run: SessionAcceptedRun{RunId: result.RunIntent.RunID, Phase: RunPhaseAccepted}, AcceptedEventSeq: acceptedSeq}, nil
}

func writeSessionsV3StartMutationError(w http.ResponseWriter, sessionID string, result sessionruntime.SessionMutationResult, err error) {
	if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, SessionConnectionError{Code: "idempotency_conflict", Message: err.Error(), Retryable: false, Action: SessionErrorAction{Method: http.MethodPost, Path: "/v3/sessions:start"}})
		return
	}
	if strings.Contains(err.Error(), "not found") {
		writeSessionsV3StartError(w, http.StatusNotFound, "session_not_found", err.Error(), false)
		return
	}
	_ = result
	writeSessionsV3StartError(w, http.StatusBadRequest, "invalid_request", err.Error(), false)
}

func (s *Server) connectSessionsV3Snapshot(principal identity.Principal, sessionID, clientID, requestID, resumeToken string) (SessionConnectResponse, error) {
	svc, err := s.sessionConnectionService()
	if err != nil {
		return SessionConnectResponse{}, err
	}
	pending := func(sessionID string, limit int) ([]pebblestore.PermissionRecord, error) {
		if s.perm == nil {
			return nil, nil
		}
		return s.perm.ListPending(sessionID, limit)
	}
	result, err := svc.Connect(sessionconnection.ConnectInput{
		Principal:   principal,
		SessionID:   sessionID,
		ClientID:    clientID,
		RequestID:   requestID,
		ResumeToken: resumeToken,
		Store:       s.sessions,
		Pending:     pending,
		StreamPath: func(connectionID, token string) string {
			return sessionConnectionStreamPath(connectionID, token)
		},
	})
	if err != nil {
		return SessionConnectResponse{}, err
	}
	return sessionConnectResponseFromResult(result), nil
}
