package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

const (
	sessionsV2EndpointPrimary        = "primary"
	sessionsV2EndpointLocalContainer = "local_container"

	runtimeSessionOpenHTTPTimeout = 30 * time.Second
)

type sessionsV2Error struct {
	status int
	code   string
	msg    string
}

func (e *sessionsV2Error) Error() string { return strings.TrimSpace(e.msg) }

func sessionV2BadRequest(format string, args ...any) error {
	return &sessionsV2Error{status: http.StatusBadRequest, code: "session_v2_bad_request", msg: fmt.Sprintf(format, args...)}
}

func sessionV2InvalidClass(format string, args ...any) error {
	return &sessionsV2Error{status: http.StatusBadRequest, code: "session_v2_invalid_execution_class", msg: fmt.Sprintf(format, args...)}
}

func sessionV2AuthorityNotFound(format string, args ...any) error {
	return &sessionsV2Error{status: http.StatusNotFound, code: "session_v2_authority_not_found", msg: fmt.Sprintf(format, args...)}
}

func sessionV2StaleAuthority(format string, args ...any) error {
	return &sessionsV2Error{status: http.StatusConflict, code: "session_v2_stale_authority", msg: fmt.Sprintf(format, args...)}
}

func sessionV2AccessDenied(format string, args ...any) error {
	return &sessionsV2Error{status: http.StatusForbidden, code: "session_v2_access_denied", msg: fmt.Sprintf(format, args...)}
}

func writeSessionsV2Error(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	var v2err *sessionsV2Error
	if errors.As(err, &v2err) {
		writeJSON(w, v2err.status, map[string]any{"ok": false, "error": v2err.Error(), "code": v2err.code})
		return
	}
	if errors.Is(err, identity.ErrPrincipalRequired) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": err.Error(), "code": "principal_required"})
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

func (s *Server) handleSessionsV2Primary(w http.ResponseWriter, r *http.Request) {
	s.handleSessionsV2Create(w, r, sessionsV2EndpointPrimary)
}

func (s *Server) handleSessionsV2LocalContainers(w http.ResponseWriter, r *http.Request) {
	s.handleSessionsV2Create(w, r, sessionsV2EndpointLocalContainer)
}

func (s *Server) handleSessionsV2Create(w http.ResponseWriter, r *http.Request, endpointClass string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.sessions == nil || s.topology == nil || (endpointClass == sessionsV2EndpointLocalContainer && s.sessions.Store() == nil) {
		writeSessionsV2Error(w, errors.New("sessions v2 service is not configured"))
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		writeSessionsV2Error(w, identity.ErrPrincipalRequired)
		return
	}
	req, err := decodeSessionsV2CreateRequestStrict(r)
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if endpointClass == sessionsV2EndpointLocalContainer {
		resp, err := s.createSessionsV2LocalContainer(r, principal, req)
		if err != nil {
			writeSessionsV2Error(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	execution, err := s.buildSessionsV2Execution(r, principal, req, endpointClass)
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if shouldRealizeSessionsV2Worktree(req) {
		execution, err = s.realizeSessionsV2PrimaryWorktree(principal, req, execution)
		if err != nil {
			writeSessionsV2Error(w, err)
			return
		}
	}
	session, persistedExecution, event, warning, modeWarning, err := s.sessions.CreateFromExecutionV2(r.Context(), sessionruntime.SessionsV2CreateCommand{Principal: principal, Request: req, Execution: execution})
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"session":           session,
		"session_execution": persistedExecution,
		"warning":           strings.TrimSpace(strings.Join([]string{warning, modeWarning}, " ")),
	})
}

func (s *Server) createSessionsV2LocalContainer(r *http.Request, principal identity.Principal, req sessionruntime.SessionsV2CreateRequest) (map[string]any, error) {
	if strings.TrimSpace(req.WorktreeMode) != "" && !strings.EqualFold(strings.TrimSpace(req.WorktreeMode), "off") {
		return nil, sessionV2BadRequest("local-container sessions v2 worktree settings are not supported before the dedicated worktree checkpoint")
	}
	if req.WorktreeUseCurrentBranch != nil {
		return nil, sessionV2BadRequest("local-container sessions v2 worktree settings are not supported before the dedicated worktree checkpoint")
	}
	execution, err := s.buildSessionsV2Execution(r, principal, req, sessionsV2EndpointLocalContainer)
	if err != nil {
		return nil, err
	}
	execution = sessionruntime.NormalizeSessionExecutionV2ForCreate(execution)
	if execution.ExecutionClass != sessionruntime.SessionExecutionClassLocalContainer {
		return nil, sessionV2InvalidClass("local-container sessions v2 requires local_container execution class")
	}
	if strings.TrimSpace(execution.SessionID) == "" {
		execution.SessionID = sessionruntime.NewSessionID()
	}
	if err := sessionruntime.ValidateSessionExecutionV2(execution); err != nil {
		return nil, err
	}
	frozenExecution := sessionruntime.SessionExecutionV2RecordFromExecution(principal, execution)
	openReq := runtimeSessionOpenRequestFromFrozenExecution(frozenExecution, req)
	openResp, err := s.dispatchRuntimeSessionV2Open(r, principal, execution, openReq)
	if err != nil {
		return nil, err
	}
	if !openResp.OK {
		return nil, sessionV2StaleAuthority("runtime session open failed")
	}
	if err := validateRuntimeSessionV2OpenResponse(openReq, openResp); err != nil {
		return nil, err
	}
	session, event, err := s.persistPrimarySideRuntimeSessionOpen(principal, req, frozenExecution, openResp)
	if err != nil {
		return nil, err
	}
	if err := s.ingestRuntimeSessionV2InitialMirror(openReq, openResp); err != nil {
		return nil, err
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	return map[string]any{
		"ok":                    true,
		"session":               session,
		"session_execution":     runtimeSessionsV2ExecutionFromRecord(frozenExecution),
		"runtime_open_response": openResp,
		"warning":               "",
	}, nil
}

func runtimeSessionOpenRequestFromFrozenExecution(execution pebblestore.SessionExecutionV2Record, req sessionruntime.SessionsV2CreateRequest) sessionruntime.RuntimeSessionOpenRequest {
	return sessionruntime.RuntimeSessionOpenRequest{
		SessionID: strings.TrimSpace(execution.SessionID),
		Authority: sessionruntime.RuntimeSessionAuthority{
			SessionID:                 strings.TrimSpace(execution.SessionID),
			UserID:                    strings.TrimSpace(execution.UserID),
			AccountScopeID:            strings.TrimSpace(execution.AccountScopeID),
			ExecutionClass:            strings.TrimSpace(execution.ExecutionClass),
			RuntimeSwarmID:            strings.TrimSpace(execution.RuntimeSwarmID),
			RuntimeKind:               strings.TrimSpace(execution.RuntimeKind),
			AuthorityHostSwarmID:      strings.TrimSpace(execution.AuthorityHostSwarmID),
			AuthorityContainerID:      strings.TrimSpace(execution.AuthorityContainerID),
			WorkspaceBindingID:        strings.TrimSpace(execution.WorkspaceBindingID),
			PlacementGeneration:       execution.PlacementGeneration,
			BindingGeneration:         execution.BindingGeneration,
			SourceWorkspaceID:         strings.TrimSpace(execution.SourceWorkspaceID),
			SourceWorkspaceGeneration: execution.SourceWorkspaceGeneration,
			SourceWorkspaceName:       strings.TrimSpace(execution.SourceWorkspaceName),
			SourceWorkspacePath:       strings.TrimSpace(execution.SourceWorkspacePath),
			DestinationRuntimeSwarmID: strings.TrimSpace(execution.RuntimeSwarmID),
			DestinationRuntimeKind:    strings.TrimSpace(execution.RuntimeKind),
			DestinationAuthorityHost:  strings.TrimSpace(execution.AuthorityHostSwarmID),
			DestinationContainerID:    strings.TrimSpace(execution.AuthorityContainerID),
			RuntimeWorkspacePath:      strings.TrimSpace(execution.RuntimeWorkspacePath),
		},
		SessionExecution: execution,
		SourceWorkspace: sessionruntime.RuntimeSessionWorkspaceFacts{
			WorkspaceID:          strings.TrimSpace(execution.SourceWorkspaceID),
			WorkspaceGeneration:  execution.SourceWorkspaceGeneration,
			WorkspaceName:        strings.TrimSpace(execution.SourceWorkspaceName),
			WorkspacePath:        strings.TrimSpace(execution.SourceWorkspacePath),
			RuntimeWorkspacePath: strings.TrimSpace(execution.SourceWorkspacePath),
		},
		DestinationRuntimeWorkspace: sessionruntime.RuntimeSessionWorkspaceFacts{
			WorkspaceID:          strings.TrimSpace(execution.SourceWorkspaceID),
			WorkspaceGeneration:  execution.SourceWorkspaceGeneration,
			WorkspaceName:        strings.TrimSpace(execution.SourceWorkspaceName),
			WorkspacePath:        strings.TrimSpace(execution.RuntimeWorkspacePath),
			RuntimeWorkspacePath: strings.TrimSpace(execution.RuntimeWorkspacePath),
		},
		Config: sessionruntime.RuntimeSessionConfig{
			Title:                    strings.TrimSpace(req.Title),
			Mode:                     strings.TrimSpace(req.Mode),
			AgentName:                strings.TrimSpace(req.AgentName),
			WorktreeMode:             "off",
			WorktreeUseCurrentBranch: nil,
			Preference:               req.Preference,
			Metadata:                 req.Metadata,
		},
	}
}

func (s *Server) dispatchRuntimeSessionV2Open(r *http.Request, principal identity.Principal, execution sessionruntime.SessionExecution, req sessionruntime.RuntimeSessionOpenRequest) (sessionruntime.RuntimeSessionOpenResponse, error) {
	localNode, localOK, err := s.swarmLocalNode()
	if err != nil {
		return sessionruntime.RuntimeSessionOpenResponse{}, err
	}
	localSwarmID := strings.TrimSpace(localNode.SwarmID)
	if localOK && strings.EqualFold(localSwarmID, strings.TrimSpace(execution.RuntimeSwarmID)) {
		return s.openRuntimeSessionV2(r, req)
	}
	conn, ok := s.ResolveAuthorityConnection(principal.AccountScopeID, execution.RuntimeSwarmID)
	if !ok || strings.TrimSpace(conn.endpoint()) == "" {
		return sessionruntime.RuntimeSessionOpenResponse{}, sessionV2AuthorityNotFound("runtime session authority connection for %q was not found", execution.RuntimeSwarmID)
	}
	if strings.EqualFold(conn.TransportKind, authorityConnectionTransportLocal) {
		return sessionruntime.RuntimeSessionOpenResponse{}, sessionV2StaleAuthority("runtime session authority connection for %q resolved local transport for non-local runtime", execution.RuntimeSwarmID)
	}
	if s.swarm == nil || localSwarmID == "" {
		return sessionruntime.RuntimeSessionOpenResponse{}, sessionV2AuthorityNotFound("runtime session peer authority for %q is not configured", execution.RuntimeSwarmID)
	}
	peerToken, ok, err := s.swarm.OutgoingPeerAuthToken(strings.TrimSpace(execution.RuntimeSwarmID))
	if err != nil {
		return sessionruntime.RuntimeSessionOpenResponse{}, err
	}
	if !ok || strings.TrimSpace(peerToken) == "" {
		return sessionruntime.RuntimeSessionOpenResponse{}, sessionV2AuthorityNotFound("runtime session peer authority for %q is not configured", execution.RuntimeSwarmID)
	}
	endpoint := strings.TrimRight(conn.endpoint(), "/") + runtimeSessionsV2OpenPath
	client := &http.Client{Timeout: runtimeSessionOpenHTTPTimeout}
	var resp sessionruntime.RuntimeSessionOpenResponse
	headers := map[string]string{peerAuthSwarmIDHeader: localSwarmID, peerAuthTokenHeader: peerToken}
	if err := remoteSwarmJSONRequestWithClientAndHeaders(http.MethodPost, endpoint, req, &resp, client, headers); err != nil {
		return sessionruntime.RuntimeSessionOpenResponse{}, sessionV2StaleAuthority("runtime session open failed: %v", err)
	}
	return resp, nil
}

func (s *Server) ingestRuntimeSessionV2InitialMirror(req sessionruntime.RuntimeSessionOpenRequest, resp sessionruntime.RuntimeSessionOpenResponse) error {
	if s == nil || s.sessions == nil {
		return errors.New("sessions v2 service is not configured")
	}
	if resp.MirrorBatch != nil {
		if strings.TrimSpace(resp.MirrorBatch.SessionID) != strings.TrimSpace(req.SessionID) {
			return sessionV2StaleAuthority("runtime session mirror batch session id mismatch")
		}
		for _, item := range resp.MirrorBatch.Items {
			if strings.TrimSpace(item.SessionID) != "" && strings.TrimSpace(item.SessionID) != strings.TrimSpace(req.SessionID) {
				return sessionV2StaleAuthority("runtime session mirror item session id mismatch")
			}
			switch strings.TrimSpace(item.Type) {
			case sessionruntime.RuntimeSessionMirrorTypeSessionSnapshot:
				var payload sessionruntime.RuntimeSessionSnapshotMirrorItem
				if err := json.Unmarshal(item.Payload, &payload); err != nil {
					return sessionV2BadRequest("invalid runtime session snapshot mirror item: %v", err)
				}
				if strings.TrimSpace(payload.Session.ID) != strings.TrimSpace(req.SessionID) {
					return sessionV2StaleAuthority("runtime session snapshot mirror item session id mismatch")
				}
				if len(resp.InitialMessages) > 0 {
					return sessionV2StaleAuthority("runtime session open response must not include both initial messages and mirror session snapshot")
				}
				if !runtimeSessionV2MirroredSnapshotMatchesAuthority(req, payload.Session) {
					return sessionV2StaleAuthority("runtime session snapshot mirror item authority mismatch")
				}
				if _, err := s.sessions.StoreMirroredSession(payload.Session); err != nil {
					return err
				}
			case sessionruntime.RuntimeSessionMirrorTypeSessionLifecycle:
				var payload sessionruntime.RuntimeSessionLifecycleMirrorItem
				if err := json.Unmarshal(item.Payload, &payload); err != nil {
					return sessionV2BadRequest("invalid runtime session lifecycle mirror item: %v", err)
				}
				if strings.TrimSpace(payload.Lifecycle.SessionID) != strings.TrimSpace(req.SessionID) {
					return sessionV2StaleAuthority("runtime session lifecycle mirror item session id mismatch")
				}
				if err := s.sessions.StoreMirroredLifecycle(payload.Lifecycle); err != nil {
					return err
				}
			case sessionruntime.RuntimeSessionMirrorTypeMessageStored:
				var payload sessionruntime.RuntimeSessionMessageStoredMirrorItem
				if err := json.Unmarshal(item.Payload, &payload); err != nil {
					return sessionV2BadRequest("invalid runtime session message mirror item: %v", err)
				}
				if strings.TrimSpace(payload.Message.SessionID) != strings.TrimSpace(req.SessionID) {
					return sessionV2StaleAuthority("runtime session message mirror item session id mismatch")
				}
				if payload.Message.GlobalSeq == 0 {
					return sessionV2BadRequest("runtime session message mirror item global seq is required")
				}
				if snapshot, ok, err := s.sessions.GetSession(req.SessionID); err != nil {
					return err
				} else if ok {
					if _, err := s.sessions.StoreMirroredMessage(snapshot, payload.Message); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func runtimeSessionV2MirroredSnapshotMatchesAuthority(req sessionruntime.RuntimeSessionOpenRequest, snapshot pebblestore.SessionSnapshot) bool {
	if strings.TrimSpace(snapshot.ID) != strings.TrimSpace(req.SessionID) {
		return false
	}
	if strings.TrimSpace(snapshot.UserID) != "" && strings.TrimSpace(snapshot.UserID) != strings.TrimSpace(req.SessionExecution.UserID) {
		return false
	}
	if strings.TrimSpace(snapshot.AccountScopeID) != "" && strings.TrimSpace(snapshot.AccountScopeID) != strings.TrimSpace(req.SessionExecution.AccountScopeID) {
		return false
	}
	if strings.TrimSpace(snapshot.WorkspacePath) != strings.TrimSpace(req.SessionExecution.RuntimeWorkspacePath) {
		return false
	}
	if snapshot.Metadata != nil {
		if value, _ := snapshot.Metadata["swarm_v2_runtime_swarm_id"].(string); strings.TrimSpace(value) != "" && strings.TrimSpace(value) != strings.TrimSpace(req.SessionExecution.RuntimeSwarmID) {
			return false
		}
		if value, _ := snapshot.Metadata["local_workspace_binding_id"].(string); strings.TrimSpace(value) != "" && strings.TrimSpace(value) != strings.TrimSpace(req.SessionExecution.WorkspaceBindingID) {
			return false
		}
	}
	return true
}

func validateRuntimeSessionV2OpenResponse(req sessionruntime.RuntimeSessionOpenRequest, resp sessionruntime.RuntimeSessionOpenResponse) error {
	if strings.TrimSpace(resp.SessionID) != strings.TrimSpace(req.SessionID) {
		return sessionV2StaleAuthority("runtime session open response session id mismatch")
	}
	if strings.TrimSpace(resp.RuntimeSwarmID) != strings.TrimSpace(req.SessionExecution.RuntimeSwarmID) {
		return sessionV2StaleAuthority("runtime session open response runtime swarm id mismatch")
	}
	if strings.TrimSpace(resp.AuthorityHostSwarmID) != strings.TrimSpace(req.SessionExecution.AuthorityHostSwarmID) {
		return sessionV2StaleAuthority("runtime session open response authority host mismatch")
	}
	if strings.TrimSpace(resp.AuthorityContainerID) != strings.TrimSpace(req.SessionExecution.AuthorityContainerID) {
		return sessionV2StaleAuthority("runtime session open response authority container mismatch")
	}
	if strings.TrimSpace(resp.WorkspaceBindingID) != strings.TrimSpace(req.SessionExecution.WorkspaceBindingID) {
		return sessionV2StaleAuthority("runtime session open response workspace binding mismatch")
	}
	if strings.TrimSpace(resp.RuntimeWorkspacePath) != strings.TrimSpace(req.SessionExecution.RuntimeWorkspacePath) {
		return sessionV2StaleAuthority("runtime session open response workspace path mismatch")
	}
	status := strings.ToLower(strings.TrimSpace(resp.Status))
	if status != "" && status != "opened" && status != "attached" {
		return sessionV2StaleAuthority("runtime session open response status mismatch")
	}
	if resp.Worktree.Enabled || strings.TrimSpace(resp.Worktree.RootPath) != "" || strings.TrimSpace(resp.Worktree.BaseBranch) != "" || strings.TrimSpace(resp.Worktree.Branch) != "" {
		return sessionV2StaleAuthority("runtime session open response included unsupported worktree facts")
	}
	return nil
}

func (s *Server) persistPrimarySideRuntimeSessionOpen(principal identity.Principal, req sessionruntime.SessionsV2CreateRequest, execution pebblestore.SessionExecutionV2Record, openResp sessionruntime.RuntimeSessionOpenResponse) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, error) {
	if s == nil || s.sessions == nil || s.sessions.Store() == nil {
		return pebblestore.SessionSnapshot{}, nil, errors.New("sessions v2 service is not configured")
	}
	metadata := sessionruntime.RuntimeSessionV2Metadata(req.Metadata, runtimeSessionsV2ExecutionFromRecord(execution))
	if metadata == nil {
		metadata = make(map[string]any, 24)
	}
	metadata["runtime_state"] = firstNonEmpty(strings.TrimSpace(openResp.LifecycleState), "standby")
	metadata["title_pending"] = true
	agentName := strings.TrimSpace(req.AgentName)
	if agentName == "" {
		agentName = "swarm"
	}
	metadata["agent_name"] = agentName
	workspaceName := firstNonEmpty(strings.TrimSpace(execution.SourceWorkspaceName), baseNameForPath(execution.RuntimeWorkspacePath))
	if openResp.Metadata != nil {
		if mirroredName, _ := openResp.Metadata["swarm_v2_source_workspace_name"].(string); strings.TrimSpace(mirroredName) != "" {
			workspaceName = strings.TrimSpace(mirroredName)
		}
	}
	session := pebblestore.SessionSnapshot{
		ID:                 strings.TrimSpace(execution.SessionID),
		UserID:             strings.TrimSpace(principal.UserID),
		AccountScopeID:     strings.TrimSpace(principal.AccountScopeID),
		WorkspacePath:      strings.TrimSpace(execution.RuntimeWorkspacePath),
		WorkspaceName:      workspaceName,
		Title:              firstNonEmpty(strings.TrimSpace(openResp.Title), strings.TrimSpace(req.Title), "New Session"),
		Mode:               sessionruntime.NormalizeMode(firstNonEmpty(strings.TrimSpace(openResp.Mode), strings.TrimSpace(req.Mode))),
		Preference:         openResp.Preference,
		WorktreeEnabled:    false,
		WorktreeRootPath:   "",
		WorktreeBaseBranch: "",
		WorktreeBranch:     "",
		Metadata:           metadata,
		CreatedAt:          execution.CreatedAt,
		UpdatedAt:          execution.UpdatedAt,
	}
	preference, err := sessionruntime.NormalizeSessionPreferenceValue(session.Preference)
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	if preference.Provider == "" && preference.Model == "" && preference.Thinking == "" {
		preference, err = sessionruntime.NormalizeSessionPreferenceValue(req.Preference)
		if err != nil {
			return pebblestore.SessionSnapshot{}, nil, err
		}
	}
	if strings.TrimSpace(preference.Provider) == "" || strings.TrimSpace(preference.Model) == "" || strings.TrimSpace(preference.Thinking) == "" {
		return pebblestore.SessionSnapshot{}, nil, sessionV2BadRequest("session execution preference is required")
	}
	session.Preference = preference
	if strings.TrimSpace(session.ID) == "" {
		return pebblestore.SessionSnapshot{}, nil, errors.New("session id is required")
	}
	now := time.Now().UnixMilli()
	if session.CreatedAt == 0 {
		session.CreatedAt = now
	}
	if session.UpdatedAt == 0 {
		session.UpdatedAt = now
	}
	execution.CreatedAt = session.CreatedAt
	execution.UpdatedAt = session.UpdatedAt
	if existing, exists, err := s.sessions.GetSession(session.ID); err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	} else if exists {
		if !runtimeSessionV2PrimarySnapshotMatchesFrozenExecution(existing, execution) {
			return pebblestore.SessionSnapshot{}, nil, sessionV2StaleAuthority("sessions v2 mirrored runtime session does not match frozen execution")
		}
		session = mergeRuntimeSessionV2PrimarySnapshot(existing, session)
	}
	if session.CreatedAt != execution.CreatedAt || session.UpdatedAt != execution.UpdatedAt {
		execution.CreatedAt = session.CreatedAt
		execution.UpdatedAt = session.UpdatedAt
	}
	if err := s.sessions.Store().CreateSessionWithExecutionV2(session, execution); err != nil {
		return pebblestore.SessionSnapshot{}, nil, fmt.Errorf("persist primary runtime session v2: %w", err)
	}
	if len(openResp.InitialMessages) > 0 {
		refreshed, ok, err := s.sessions.GetSession(session.ID)
		if err != nil {
			return pebblestore.SessionSnapshot{}, nil, err
		}
		if !ok {
			return pebblestore.SessionSnapshot{}, nil, sessionV2StaleAuthority("sessions v2 primary session missing after persist")
		}
		session = refreshed
		for _, message := range openResp.InitialMessages {
			if strings.TrimSpace(message.SessionID) != strings.TrimSpace(session.ID) {
				return pebblestore.SessionSnapshot{}, nil, sessionV2StaleAuthority("runtime session initial message session id mismatch")
			}
			if message.GlobalSeq == 0 {
				return pebblestore.SessionSnapshot{}, nil, sessionV2BadRequest("runtime session initial message global seq is required")
			}
			if _, err := s.sessions.StoreMirroredMessage(session, message); err != nil {
				return pebblestore.SessionSnapshot{}, nil, err
			}
		}
		if refreshed, ok, err := s.sessions.GetSession(session.ID); err != nil {
			return pebblestore.SessionSnapshot{}, nil, err
		} else if ok {
			session.MessageCount = refreshed.MessageCount
			session.LastMessageAt = refreshed.LastMessageAt
		}
	}
	var event *pebblestore.EventEnvelope
	if s.events != nil {
		payload, err := json.Marshal(session)
		if err != nil {
			return pebblestore.SessionSnapshot{}, nil, err
		}
		appended, err := s.events.Append("session:"+session.ID, "session.created", session.ID, payload, "", "")
		if err != nil {
			return pebblestore.SessionSnapshot{}, nil, err
		}
		event = &appended
	}
	return session, event, nil
}

func runtimeSessionV2PrimarySnapshotMatchesFrozenExecution(snapshot pebblestore.SessionSnapshot, execution pebblestore.SessionExecutionV2Record) bool {
	return strings.TrimSpace(snapshot.ID) == strings.TrimSpace(execution.SessionID) &&
		strings.TrimSpace(snapshot.UserID) == strings.TrimSpace(execution.UserID) &&
		strings.TrimSpace(snapshot.AccountScopeID) == strings.TrimSpace(execution.AccountScopeID) &&
		strings.TrimSpace(snapshot.WorkspacePath) == strings.TrimSpace(execution.RuntimeWorkspacePath)
}

func mergeRuntimeSessionV2PrimarySnapshot(mirrored, canonical pebblestore.SessionSnapshot) pebblestore.SessionSnapshot {
	canonical.MessageCount = mirrored.MessageCount
	canonical.LastMessageAt = mirrored.LastMessageAt
	canonical.Lifecycle = mirrored.Lifecycle
	canonical.TemporaryWorkspaceRoots = mirrored.TemporaryWorkspaceRoots
	if mirrored.CreatedAt > 0 {
		canonical.CreatedAt = mirrored.CreatedAt
	}
	if mirrored.UpdatedAt > 0 {
		canonical.UpdatedAt = mirrored.UpdatedAt
	}
	return canonical
}

func decodeSessionsV2CreateRequestStrict(r *http.Request) (sessionruntime.SessionsV2CreateRequest, error) {
	body, err := readRequestBody(r)
	if err != nil {
		return sessionruntime.SessionsV2CreateRequest{}, sessionV2BadRequest("%s", err.Error())
	}
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&raw); err != nil {
		return sessionruntime.SessionsV2CreateRequest{}, sessionV2BadRequest("invalid JSON body: %v", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return sessionruntime.SessionsV2CreateRequest{}, sessionV2BadRequest("request body must contain one JSON object")
	}
	allowed := map[string]struct{}{
		"swarm_id": {}, "workspace_binding_id": {}, "workspace_path": {}, "title": {}, "mode": {}, "agent_name": {},
		"worktree_mode": {}, "worktree_use_current_branch": {}, "worktree_base_branch": {}, "worktree_branch_name": {},
		"preference": {}, "metadata": {},
	}
	for key := range raw {
		if _, ok := allowed[key]; !ok {
			return sessionruntime.SessionsV2CreateRequest{}, sessionV2BadRequest("sessions v2 create must not include unknown or routing authority field %q", key)
		}
	}
	var req sessionruntime.SessionsV2CreateRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return sessionruntime.SessionsV2CreateRequest{}, sessionV2BadRequest("invalid sessions v2 create request: %v", err)
	}
	req.SwarmID = strings.TrimSpace(req.SwarmID)
	req.WorkspaceBindingID = strings.TrimSpace(req.WorkspaceBindingID)
	req.WorkspacePath = strings.TrimSpace(req.WorkspacePath)
	req.Title = strings.TrimSpace(req.Title)
	req.Mode = strings.TrimSpace(req.Mode)
	req.AgentName = strings.TrimSpace(req.AgentName)
	req.WorktreeMode = strings.TrimSpace(req.WorktreeMode)
	req.WorktreeBaseBranch = strings.TrimSpace(req.WorktreeBaseBranch)
	req.WorktreeBranchName = strings.TrimSpace(req.WorktreeBranchName)
	if req.SwarmID == "" {
		return sessionruntime.SessionsV2CreateRequest{}, sessionV2BadRequest("sessions v2 swarm_id is required")
	}
	if req.WorkspaceBindingID != "" && req.WorkspacePath != "" {
		return sessionruntime.SessionsV2CreateRequest{}, sessionV2BadRequest("sessions v2 create must not include unknown or routing authority field %q", "workspace_path")
	}
	if req.WorkspaceBindingID == "" && req.WorkspacePath == "" {
		return sessionruntime.SessionsV2CreateRequest{}, sessionV2BadRequest("sessions v2 workspace_binding_id is required")
	}
	if err := validateSessionsV2WorktreeRequest(req); err != nil {
		return sessionruntime.SessionsV2CreateRequest{}, err
	}
	if err := validateSessionsV2Metadata(req.Metadata); err != nil {
		return sessionruntime.SessionsV2CreateRequest{}, err
	}
	return req, nil
}

func validateSessionsV2WorktreeRequest(req sessionruntime.SessionsV2CreateRequest) error {
	mode := strings.ToLower(strings.TrimSpace(req.WorktreeMode))
	switch mode {
	case "", "off":
		if strings.TrimSpace(req.WorktreeBaseBranch) != "" || strings.TrimSpace(req.WorktreeBranchName) != "" {
			return sessionV2BadRequest("worktree fields are only allowed when worktree_mode is on")
		}
		return nil
	case "on":
		if req.WorktreeUseCurrentBranch != nil {
			useCurrentBranch := *req.WorktreeUseCurrentBranch
			if useCurrentBranch && strings.TrimSpace(req.WorktreeBaseBranch) != "" {
				return sessionV2BadRequest("worktree_use_current_branch cannot be true when worktree_base_branch is set")
			}
			if !useCurrentBranch && strings.TrimSpace(req.WorktreeBaseBranch) == "" {
				return sessionV2BadRequest("worktree_base_branch is required when worktree_use_current_branch is false")
			}
		}
		return nil
	default:
		return sessionV2BadRequest("unsupported worktree_mode %q", strings.TrimSpace(req.WorktreeMode))
	}
}

func shouldRealizeSessionsV2Worktree(req sessionruntime.SessionsV2CreateRequest) bool {
	return strings.EqualFold(strings.TrimSpace(req.WorktreeMode), "on")
}

func (s *Server) realizeSessionsV2PrimaryWorktree(principal identity.Principal, req sessionruntime.SessionsV2CreateRequest, execution sessionruntime.SessionExecution) (sessionruntime.SessionExecution, error) {
	execution = sessionruntime.NormalizeSessionExecutionV2ForCreate(execution)
	if execution.ExecutionClass != sessionruntime.SessionExecutionClassPrimary {
		return sessionruntime.SessionExecution{}, sessionV2BadRequest("worktree_mode on is only supported for primary sessions v2")
	}
	if execution.WorkspaceBindingID == "" {
		return sessionruntime.SessionExecution{}, sessionV2BadRequest("worktree_mode on requires workspace_binding_id")
	}
	if s.worktrees == nil {
		return sessionruntime.SessionExecution{}, sessionV2BadRequest("worktree_mode on requires worktree service")
	}
	sessionID := strings.TrimSpace(execution.SessionID)
	if sessionID == "" {
		sessionID = sessionruntime.NewSessionID()
		execution.SessionID = sessionID
	}
	allocation, err := s.worktrees.AllocateDetachedWorkspaceRequestedForPrincipal(principal, execution.SourceWorkspacePath, sessionID, req.WorktreeBaseBranch, req.WorktreeBranchName)
	if err != nil {
		return sessionruntime.SessionExecution{}, sessionV2BadRequest("realize primary sessions v2 worktree: %v", err)
	}
	if strings.TrimSpace(allocation.WorkspacePath) == "" || strings.TrimSpace(allocation.BaseBranch) == "" || strings.TrimSpace(allocation.BranchName) == "" {
		return sessionruntime.SessionExecution{}, sessionV2BadRequest("worktree_mode on did not allocate complete worktree facts")
	}
	if workspaceID := strings.TrimSpace(allocation.WorkspaceID); workspaceID != worktreeruntime.WorkspaceIdentityForSession(sessionID) {
		return sessionruntime.SessionExecution{}, sessionV2BadRequest("worktree_mode on allocation workspace identity mismatch")
	}
	execution.SessionID = sessionID
	execution.RuntimeWorkspacePath = strings.TrimSpace(allocation.WorkspacePath)
	execution.WorktreeEnabled = true
	execution.WorktreeRootPath = strings.TrimSpace(allocation.WorkspacePath)
	execution.WorktreeBaseBranch = strings.TrimSpace(allocation.BaseBranch)
	execution.WorktreeBranch = strings.TrimSpace(allocation.BranchName)
	return execution, nil
}

func (s *Server) buildSessionsV2Execution(r *http.Request, principal identity.Principal, req sessionruntime.SessionsV2CreateRequest, endpointClass string) (sessionruntime.SessionExecution, error) {
	localNode, localOK, err := s.swarmLocalNode()
	if err != nil {
		return sessionruntime.SessionExecution{}, err
	}
	primarySwarmID := strings.TrimSpace(localNode.SwarmID)
	if !localOK || primarySwarmID == "" {
		return sessionruntime.SessionExecution{}, sessionV2AuthorityNotFound("sessions v2 primary local node identity is required")
	}
	runtimeRecord, runtimeOK, err := s.topology.GetRuntimeForAccount(principal.AccountScopeID, req.SwarmID)
	if err != nil {
		return sessionruntime.SessionExecution{}, err
	}
	if !runtimeOK {
		return sessionruntime.SessionExecution{}, sessionV2AuthorityNotFound("sessions v2 runtime %q was not found", req.SwarmID)
	}
	if strings.TrimSpace(runtimeRecord.SwarmID) != req.SwarmID {
		return sessionruntime.SessionExecution{}, sessionV2StaleAuthority("sessions v2 runtime identity mismatch")
	}
	placement, placementOK, err := s.topology.GetRuntimePlacementForAccount(principal.AccountScopeID, req.SwarmID)
	if err != nil {
		return sessionruntime.SessionExecution{}, err
	}
	if !placementOK {
		return sessionruntime.SessionExecution{}, sessionV2AuthorityNotFound("sessions v2 runtime placement for %q was not found", req.SwarmID)
	}
	if req.WorkspaceBindingID == "" {
		if endpointClass != sessionsV2EndpointPrimary {
			return sessionruntime.SessionExecution{}, sessionV2BadRequest("sessions v2 workspace_binding_id is required")
		}
		if shouldRealizeSessionsV2Worktree(req) {
			return sessionruntime.SessionExecution{}, sessionV2BadRequest("worktree_mode on requires workspace_binding_id")
		}
		return s.buildSessionsV2PrimaryTUICWDExecution(r, req, primarySwarmID, placement)
	}
	if req.WorkspacePath != "" {
		return sessionruntime.SessionExecution{}, sessionV2BadRequest("sessions v2 workspace_path is only allowed for TUI primary cwd create without workspace_binding_id")
	}
	binding, bindingOK, err := s.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, req.WorkspaceBindingID)
	if err != nil {
		return sessionruntime.SessionExecution{}, err
	}
	if !bindingOK {
		return sessionruntime.SessionExecution{}, sessionV2AuthorityNotFound("sessions v2 workspace binding %q was not found", req.WorkspaceBindingID)
	}
	if strings.TrimSpace(binding.BindingID) != req.WorkspaceBindingID {
		return sessionruntime.SessionExecution{}, sessionV2StaleAuthority("sessions v2 workspace binding id mismatch")
	}
	switch endpointClass {
	case sessionsV2EndpointPrimary:
		if req.SwarmID != primarySwarmID {
			return sessionruntime.SessionExecution{}, sessionV2InvalidClass("primary sessions v2 swarm_id %q is not the primary runtime", req.SwarmID)
		}
		if err := validatePrimarySessionV2Placement(req.SwarmID, placement); err != nil {
			return sessionruntime.SessionExecution{}, err
		}
		if err := validatePrimarySessionV2Binding(principal, req.WorkspaceBindingID, req.SwarmID, placement, binding); err != nil {
			return sessionruntime.SessionExecution{}, err
		}
		return sessionsV2ExecutionFromBinding(sessionruntime.SessionExecutionClassPrimary, placement, binding), nil
	case sessionsV2EndpointLocalContainer:
		if err := validateLocalContainerSessionV2Placement(req.SwarmID, primarySwarmID, placement); err != nil {
			return sessionruntime.SessionExecution{}, err
		}
		if err := validateLocalContainerSessionV2Binding(principal, req.WorkspaceBindingID, req.SwarmID, primarySwarmID, placement, binding); err != nil {
			return sessionruntime.SessionExecution{}, err
		}
		return sessionsV2ExecutionFromBinding(sessionruntime.SessionExecutionClassLocalContainer, placement, binding), nil
	default:
		return sessionruntime.SessionExecution{}, sessionV2InvalidClass("unsupported sessions v2 endpoint class %q", endpointClass)
	}
}

func (s *Server) buildSessionsV2PrimaryTUICWDExecution(r *http.Request, req sessionruntime.SessionsV2CreateRequest, primarySwarmID string, placement pebblestore.TopologyRuntimePlacementRecord) (sessionruntime.SessionExecution, error) {
	if req.SwarmID != primarySwarmID {
		return sessionruntime.SessionExecution{}, sessionV2InvalidClass("primary sessions v2 swarm_id %q is not the primary runtime", req.SwarmID)
	}
	if err := validatePrimarySessionV2Placement(req.SwarmID, placement); err != nil {
		return sessionruntime.SessionExecution{}, err
	}
	if !isSessionsV2TUIClientRequest(r) {
		return sessionruntime.SessionExecution{}, sessionV2BadRequest("sessions v2 workspace_binding_id is required")
	}
	workspacePath := strings.TrimSpace(req.WorkspacePath)
	if workspacePath == "" {
		return sessionruntime.SessionExecution{}, sessionV2BadRequest("sessions v2 workspace_binding_id is required")
	}
	workspaceName := baseNameForPath(workspacePath)
	return sessionruntime.SessionExecution{
		ExecutionClass:            sessionruntime.SessionExecutionClassPrimary,
		RuntimeSwarmID:            strings.TrimSpace(placement.RuntimeSwarmID),
		RuntimeKind:               strings.TrimSpace(placement.RuntimeKind),
		AuthorityHostSwarmID:      strings.TrimSpace(placement.AuthorityHostSwarmID),
		AuthorityContainerID:      strings.TrimSpace(placement.AuthorityContainerID),
		WorkspaceBindingID:        "",
		SourceWorkspaceID:         sessionruntime.SessionExecutionTUICWDSourceIDPrefix + workspacePath,
		SourceWorkspaceGeneration: sessionruntime.SessionExecutionTUICWDSourceGeneration,
		SourceWorkspaceName:       workspaceName,
		SourceWorkspacePath:       workspacePath,
		RuntimeWorkspacePath:      workspacePath,
		PlacementGeneration:       placement.PlacementGeneration,
		BindingGeneration:         sessionruntime.SessionExecutionTUICWDBindingGeneration,
	}, nil
}

func isSessionsV2TUIClientRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Swarm-Client")), "swarmtui")
}

func validatePrimarySessionV2Placement(swarmID string, placement pebblestore.TopologyRuntimePlacementRecord) error {
	if strings.TrimSpace(placement.State) != pebblestore.TopologyRuntimePlacementStateActive {
		return sessionV2StaleAuthority("primary sessions v2 runtime placement is not active")
	}
	if strings.TrimSpace(placement.RuntimeSwarmID) != swarmID || strings.TrimSpace(placement.AuthorityHostSwarmID) != swarmID {
		return sessionV2StaleAuthority("primary sessions v2 runtime placement does not match selected self authority")
	}
	if strings.TrimSpace(placement.RuntimeKind) != pebblestore.TopologyRuntimeKindHost {
		return sessionV2InvalidClass("primary sessions v2 runtime placement kind must be host")
	}
	if strings.TrimSpace(placement.AuthorityContainerID) != "" {
		return sessionV2InvalidClass("primary sessions v2 runtime placement authority container id must be empty")
	}
	if placement.PlacementGeneration <= 0 {
		return sessionV2StaleAuthority("primary sessions v2 runtime placement generation is required")
	}
	return nil
}

func validateLocalContainerSessionV2Placement(swarmID, primarySwarmID string, placement pebblestore.TopologyRuntimePlacementRecord) error {
	if strings.TrimSpace(placement.State) != pebblestore.TopologyRuntimePlacementStateActive {
		return sessionV2StaleAuthority("local-container sessions v2 runtime placement is not active")
	}
	if strings.TrimSpace(placement.RuntimeSwarmID) != swarmID {
		return sessionV2StaleAuthority("local-container sessions v2 runtime placement does not match selected runtime")
	}
	if strings.TrimSpace(placement.RuntimeKind) != pebblestore.TopologyRuntimeKindContainer {
		return sessionV2InvalidClass("local-container sessions v2 runtime placement kind must be container")
	}
	if strings.TrimSpace(placement.AuthorityHostSwarmID) != primarySwarmID {
		return sessionV2InvalidClass("local-container sessions v2 authority host must be the primary runtime")
	}
	if strings.TrimSpace(placement.AuthorityContainerID) == "" {
		return sessionV2StaleAuthority("local-container sessions v2 authority container id is required")
	}
	if placement.PlacementGeneration <= 0 {
		return sessionV2StaleAuthority("local-container sessions v2 runtime placement generation is required")
	}
	return nil
}

func validatePrimarySessionV2Binding(principal identity.Principal, bindingID, swarmID string, placement pebblestore.TopologyRuntimePlacementRecord, binding pebblestore.TopologyWorkspaceBindingRecord) error {
	if err := validateCommonSessionV2Binding(principal, bindingID, placement, binding); err != nil {
		return err
	}
	if strings.TrimSpace(binding.DestinationRuntimeSwarmID) != swarmID || strings.TrimSpace(binding.DestinationAuthorityHostSwarmID) != swarmID {
		return sessionV2StaleAuthority("primary sessions v2 workspace binding does not match selected self authority")
	}
	if strings.TrimSpace(binding.DestinationRuntimeKind) != pebblestore.TopologyRuntimeKindHost {
		return sessionV2InvalidClass("primary sessions v2 workspace binding destination runtime kind must be host")
	}
	if strings.TrimSpace(binding.DestinationContainerID) != "" {
		return sessionV2InvalidClass("primary sessions v2 workspace binding destination container id must be empty")
	}
	return nil
}

func validateLocalContainerSessionV2Binding(principal identity.Principal, bindingID, swarmID, primarySwarmID string, placement pebblestore.TopologyRuntimePlacementRecord, binding pebblestore.TopologyWorkspaceBindingRecord) error {
	if err := validateCommonSessionV2Binding(principal, bindingID, placement, binding); err != nil {
		return err
	}
	if strings.TrimSpace(binding.DestinationRuntimeSwarmID) != swarmID || strings.TrimSpace(binding.DestinationAuthorityHostSwarmID) != primarySwarmID {
		return sessionV2StaleAuthority("local-container sessions v2 workspace binding does not match selected primary authority")
	}
	if strings.TrimSpace(binding.DestinationRuntimeKind) != pebblestore.TopologyRuntimeKindContainer {
		return sessionV2InvalidClass("local-container sessions v2 workspace binding destination runtime kind must be container")
	}
	if strings.TrimSpace(binding.DestinationContainerID) != strings.TrimSpace(placement.AuthorityContainerID) {
		return sessionV2StaleAuthority("local-container sessions v2 workspace binding destination container id does not match placement")
	}
	return nil
}

func validateCommonSessionV2Binding(principal identity.Principal, bindingID string, placement pebblestore.TopologyRuntimePlacementRecord, binding pebblestore.TopologyWorkspaceBindingRecord) error {
	if !principal.Valid() {
		return identity.ErrPrincipalRequired
	}
	if strings.TrimSpace(binding.BindingID) != strings.TrimSpace(bindingID) {
		return sessionV2StaleAuthority("sessions v2 workspace binding id mismatch")
	}
	if strings.TrimSpace(binding.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionV2AccessDenied("sessions v2 workspace binding account scope does not match principal")
	}
	if strings.TrimSpace(binding.State) != pebblestore.TopologyWorkspaceBindingStateBound {
		return sessionV2StaleAuthority("sessions v2 workspace binding is not bound")
	}
	if strings.TrimSpace(binding.SourceWorkspaceID) == "" || binding.SourceWorkspaceGeneration <= 0 || strings.TrimSpace(binding.SourceWorkspacePath) == "" {
		return sessionV2StaleAuthority("sessions v2 workspace binding source workspace identity is incomplete")
	}
	if strings.TrimSpace(binding.DestinationWorkspacePath) == "" {
		return sessionV2StaleAuthority("sessions v2 workspace binding destination workspace path is required")
	}
	if binding.PlacementGeneration != placement.PlacementGeneration || binding.BindingGeneration <= 0 {
		return sessionV2StaleAuthority("sessions v2 workspace binding generation does not match placement")
	}
	if strings.TrimSpace(binding.AttestedByHostSwarmID) != strings.TrimSpace(placement.AuthorityHostSwarmID) {
		return sessionV2StaleAuthority("sessions v2 workspace binding attesting host does not match authority host")
	}
	if strings.TrimSpace(binding.AccessMode) != pebblestore.TopologyWorkspaceBindingAccessModeReadWrite || !binding.Writable {
		return sessionV2AccessDenied("sessions v2 workspace binding must be read_write and writable")
	}
	return nil
}

func sessionsV2ExecutionFromBinding(class string, placement pebblestore.TopologyRuntimePlacementRecord, binding pebblestore.TopologyWorkspaceBindingRecord) sessionruntime.SessionExecution {
	return sessionruntime.SessionExecution{
		ExecutionClass:            class,
		RuntimeSwarmID:            strings.TrimSpace(placement.RuntimeSwarmID),
		RuntimeKind:               strings.TrimSpace(placement.RuntimeKind),
		AuthorityHostSwarmID:      strings.TrimSpace(placement.AuthorityHostSwarmID),
		AuthorityContainerID:      strings.TrimSpace(placement.AuthorityContainerID),
		WorkspaceBindingID:        strings.TrimSpace(binding.BindingID),
		SourceWorkspaceID:         strings.TrimSpace(binding.SourceWorkspaceID),
		SourceWorkspaceGeneration: binding.SourceWorkspaceGeneration,
		SourceWorkspaceName:       strings.TrimSpace(binding.SourceWorkspaceName),
		SourceWorkspacePath:       strings.TrimSpace(binding.SourceWorkspacePath),
		RuntimeWorkspacePath:      strings.TrimSpace(binding.DestinationWorkspacePath),
		PlacementGeneration:       placement.PlacementGeneration,
		BindingGeneration:         binding.BindingGeneration,
	}
}

func validateSessionsV2Metadata(metadata map[string]any) error {
	return validateSessionsV2MetadataValue(metadata)
}

func validateSessionsV2MetadataValue(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalizedKey := strings.ToLower(strings.TrimSpace(key))
			if sessionV2KeyLooksLikeAuthority(key) || strings.HasPrefix(normalizedKey, "swarm_v2_") || normalizedKey == "local_workspace_binding_id" {
				return sessionV2BadRequest("sessions v2 metadata must not include routing authority key %q", key)
			}
			if err := validateSessionsV2MetadataValue(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := validateSessionsV2MetadataValue(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func sessionV2KeyLooksLikeAuthority(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	needles := []string{"routing", "backend", "path", "workspace_name", "target", "next_hop", "nexthop", "hosted_session", "managed_host", "swarm_routed"}
	for _, needle := range needles {
		if strings.Contains(k, needle) {
			return true
		}
	}
	return false
}
