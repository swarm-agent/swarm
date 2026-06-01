package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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
	var req sessionruntime.RuntimeSessionOpenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeSessionsV2Error(w, sessionV2BadRequest("invalid runtime session open request: %v", err))
		return
	}
	resp, err := s.openRuntimeSessionV2(r, req)
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) openRuntimeSessionV2(r *http.Request, req sessionruntime.RuntimeSessionOpenRequest) (sessionruntime.RuntimeSessionOpenResponse, error) {
	if s == nil || s.sessions == nil || s.sessions.Store() == nil || s.topology == nil {
		return sessionruntime.RuntimeSessionOpenResponse{}, errors.New("runtime sessions v2 service is not configured")
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		return sessionruntime.RuntimeSessionOpenResponse{}, identity.ErrPrincipalRequired
	}
	if err := s.validateRuntimeSessionV2OpenAuthority(principal, req); err != nil {
		return sessionruntime.RuntimeSessionOpenResponse{}, err
	}

	execution := runtimeSessionsV2ExecutionFromRecord(req.SessionExecution)
	if err := sessionruntime.ValidateSessionExecutionV2(execution); err != nil {
		return sessionruntime.RuntimeSessionOpenResponse{}, err
	}
	preference, err := sessionruntime.NormalizeSessionPreferenceValue(req.Config.Preference)
	if err != nil {
		return sessionruntime.RuntimeSessionOpenResponse{}, err
	}
	if strings.TrimSpace(req.Config.WorktreeMode) != "" && !strings.EqualFold(strings.TrimSpace(req.Config.WorktreeMode), "off") {
		return sessionruntime.RuntimeSessionOpenResponse{}, sessionV2BadRequest("runtime session open does not install worktrees in checkpoint 8")
	}
	sessionID := strings.TrimSpace(req.SessionID)
	session, exists, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return sessionruntime.RuntimeSessionOpenResponse{}, err
	}
	status := "attached"
	var createdEvent *pebblestore.EventEnvelope
	if exists {
		storedExecution, executionOK, err := s.sessions.Store().GetSessionExecutionV2(sessionID)
		if err != nil {
			return sessionruntime.RuntimeSessionOpenResponse{}, err
		}
		if !executionOK {
			if err := s.sessions.Store().CreateSessionWithExecutionV2(session, req.SessionExecution); err != nil {
				return sessionruntime.RuntimeSessionOpenResponse{}, err
			}
			storedExecution = req.SessionExecution
		}
		if err := runtimeSessionsV2ValidateStoredExecution(req.SessionExecution, storedExecution); err != nil {
			return sessionruntime.RuntimeSessionOpenResponse{}, err
		}
		if strings.TrimSpace(session.WorkspacePath) != strings.TrimSpace(req.SessionExecution.RuntimeWorkspacePath) {
			return sessionruntime.RuntimeSessionOpenResponse{}, sessionV2StaleAuthority("runtime session workspace path does not match frozen execution")
		}
	} else {
		createReq := sessionruntime.SessionsV2CreateRequest{
			Title:      req.Config.Title,
			Mode:       req.Config.Mode,
			Preference: preference,
			Metadata:   req.Config.Metadata,
		}
		created, env, err := s.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
			SessionID:      execution.SessionID,
			UserID:         principal.UserID,
			AccountScopeID: principal.AccountScopeID,
			Title:          createReq.Title,
			WorkspacePath:  execution.RuntimeWorkspacePath,
			WorkspaceName:  execution.SourceWorkspaceName,
			Mode:           createReq.Mode,
			Preference:     &createReq.Preference,
			Metadata:       sessionruntime.RuntimeSessionV2Metadata(createReq.Metadata, execution),
		})
		if err != nil {
			return sessionruntime.RuntimeSessionOpenResponse{}, err
		}
		executionRecord := req.SessionExecution
		executionRecord.CreatedAt = created.CreatedAt
		executionRecord.UpdatedAt = created.UpdatedAt
		if err := s.sessions.Store().CreateSessionWithExecutionV2(created, executionRecord); err != nil {
			return sessionruntime.RuntimeSessionOpenResponse{}, err
		}
		req.SessionExecution = executionRecord
		session = created
		createdEvent = env
		status = "opened"
	}

	messages, err := s.sessions.ListMessages(sessionID, 0, 100)
	if err != nil {
		return sessionruntime.RuntimeSessionOpenResponse{}, err
	}
	initialEvents := []pebblestore.EventEnvelope(nil)
	if createdEvent != nil {
		initialEvents = append(initialEvents, *createdEvent)
	}
	lifecycleState := "standby"
	if lifecycle, ok, err := s.sessions.GetLifecycle(sessionID); err != nil {
		return sessionruntime.RuntimeSessionOpenResponse{}, err
	} else if ok {
		lifecycleState = runtimeSessionsV2LifecycleState(lifecycle)
	}

	return sessionruntime.RuntimeSessionOpenResponse{
		OK:                   true,
		SessionID:            sessionID,
		RuntimeSwarmID:       req.SessionExecution.RuntimeSwarmID,
		AuthorityHostSwarmID: req.SessionExecution.AuthorityHostSwarmID,
		AuthorityContainerID: req.SessionExecution.AuthorityContainerID,
		WorkspaceBindingID:   req.SessionExecution.WorkspaceBindingID,
		Status:               status,
		LifecycleState:       lifecycleState,
		RuntimeWorkspacePath: req.SessionExecution.RuntimeWorkspacePath,
		Worktree: sessionruntime.RuntimeSessionWorktreeFacts{
			Enabled:    session.WorktreeEnabled,
			RootPath:   session.WorktreeRootPath,
			BaseBranch: session.WorktreeBaseBranch,
			Branch:     session.WorktreeBranch,
		},
		Title:           session.Title,
		Mode:            session.Mode,
		Preference:      session.Preference,
		Metadata:        session.Metadata,
		InitialMessages: messages,
		InitialEvents:   initialEvents,
	}, nil
}

func (s *Server) validateRuntimeSessionV2OpenAuthority(principal identity.Principal, req sessionruntime.RuntimeSessionOpenRequest) error {
	requestSessionID := strings.TrimSpace(req.SessionID)
	if requestSessionID == "" {
		return sessionV2BadRequest("runtime session id is required")
	}
	authority := req.Authority
	execution := req.SessionExecution
	if err := runtimeSessionsV2RequireEqual("authority session id", authority.SessionID, requestSessionID); err != nil {
		return err
	}
	if err := runtimeSessionsV2RequireEqual("session execution session id", execution.SessionID, requestSessionID); err != nil {
		return err
	}
	if strings.TrimSpace(authority.AccountScopeID) == "" || strings.TrimSpace(execution.AccountScopeID) == "" {
		return sessionV2BadRequest("runtime session authority account scope is required")
	}
	if strings.TrimSpace(authority.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) || strings.TrimSpace(execution.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionV2AccessDenied("runtime session authority account scope does not match principal")
	}
	if strings.TrimSpace(authority.UserID) != "" && strings.TrimSpace(authority.UserID) != strings.TrimSpace(principal.UserID) {
		return sessionV2AccessDenied("runtime session authority user does not match principal")
	}
	if strings.TrimSpace(execution.UserID) != "" && strings.TrimSpace(execution.UserID) != strings.TrimSpace(principal.UserID) {
		return sessionV2AccessDenied("runtime session execution user does not match principal")
	}

	if strings.TrimSpace(authority.ExecutionClass) != sessionruntime.SessionExecutionClassLocalContainer || strings.TrimSpace(execution.ExecutionClass) != sessionruntime.SessionExecutionClassLocalContainer {
		return sessionV2InvalidClass("runtime session open requires local_container execution class")
	}
	if strings.TrimSpace(authority.RuntimeKind) != pebblestore.TopologyRuntimeKindContainer || strings.TrimSpace(execution.RuntimeKind) != pebblestore.TopologyRuntimeKindContainer || strings.TrimSpace(authority.DestinationRuntimeKind) != pebblestore.TopologyRuntimeKindContainer {
		return sessionV2InvalidClass("runtime session open requires container runtime kind")
	}

	localNode, localOK, err := s.swarmLocalNode()
	if err != nil {
		return err
	}
	localSwarmID := strings.TrimSpace(localNode.SwarmID)
	if !localOK || localSwarmID == "" {
		return sessionV2AuthorityNotFound("runtime session local container identity is required")
	}
	if err := runtimeSessionsV2RequireEqual("runtime swarm id", authority.RuntimeSwarmID, localSwarmID); err != nil {
		return err
	}
	if err := runtimeSessionsV2RequireEqual("session execution runtime swarm id", execution.RuntimeSwarmID, localSwarmID); err != nil {
		return err
	}
	if err := runtimeSessionsV2RequireEqual("destination runtime swarm id", authority.DestinationRuntimeSwarmID, localSwarmID); err != nil {
		return err
	}
	if strings.TrimSpace(authority.AuthorityHostSwarmID) == "" || strings.TrimSpace(execution.AuthorityHostSwarmID) == "" {
		return sessionV2BadRequest("runtime session authority host swarm id is required")
	}
	if err := runtimeSessionsV2RequireEqual("authority host swarm id", authority.AuthorityHostSwarmID, execution.AuthorityHostSwarmID); err != nil {
		return err
	}
	if err := runtimeSessionsV2RequireEqual("destination authority host swarm id", authority.DestinationAuthorityHost, execution.AuthorityHostSwarmID); err != nil {
		return err
	}
	if strings.TrimSpace(authority.AuthorityContainerID) == "" || strings.TrimSpace(execution.AuthorityContainerID) == "" {
		return sessionV2BadRequest("runtime session authority container id is required")
	}
	if err := runtimeSessionsV2RequireEqual("authority container id", authority.AuthorityContainerID, execution.AuthorityContainerID); err != nil {
		return err
	}
	if err := runtimeSessionsV2RequireEqual("destination container id", authority.DestinationContainerID, execution.AuthorityContainerID); err != nil {
		return err
	}

	runtimeRecord, runtimeOK, err := s.topology.GetRuntimeForAccount(principal.AccountScopeID, localSwarmID)
	if err != nil {
		return err
	}
	if !runtimeOK {
		return sessionV2AuthorityNotFound("runtime session runtime %q was not found", localSwarmID)
	}
	if strings.TrimSpace(runtimeRecord.SwarmID) != localSwarmID {
		return sessionV2StaleAuthority("runtime session runtime identity mismatch")
	}
	if strings.TrimSpace(runtimeRecord.OwnerHostSwarmID) == "" || strings.TrimSpace(runtimeRecord.OwnerHostContainerID) == "" {
		return sessionV2StaleAuthority("runtime session runtime owner identity is incomplete")
	}
	if strings.TrimSpace(runtimeRecord.OwnerHostSwarmID) != strings.TrimSpace(execution.AuthorityHostSwarmID) {
		return sessionV2StaleAuthority("runtime session authority host does not match runtime owner")
	}
	if strings.TrimSpace(runtimeRecord.OwnerHostContainerID) != strings.TrimSpace(execution.AuthorityContainerID) {
		return sessionV2StaleAuthority("runtime session authority container does not match runtime owner")
	}

	placement, placementOK, err := s.topology.GetRuntimePlacementForAccount(principal.AccountScopeID, localSwarmID)
	if err != nil {
		return err
	}
	if !placementOK {
		return sessionV2AuthorityNotFound("runtime session placement for %q was not found", localSwarmID)
	}
	if strings.TrimSpace(placement.State) != pebblestore.TopologyRuntimePlacementStateActive {
		return sessionV2StaleAuthority("runtime session placement is not active")
	}
	if strings.TrimSpace(placement.RuntimeSwarmID) != localSwarmID || strings.TrimSpace(placement.RuntimeKind) != pebblestore.TopologyRuntimeKindContainer {
		return sessionV2StaleAuthority("runtime session placement does not match local container runtime")
	}
	if strings.TrimSpace(placement.AuthorityHostSwarmID) != strings.TrimSpace(execution.AuthorityHostSwarmID) || strings.TrimSpace(placement.AuthorityContainerID) != strings.TrimSpace(execution.AuthorityContainerID) {
		return sessionV2StaleAuthority("runtime session placement authority mismatch")
	}
	if placement.PlacementGeneration != execution.PlacementGeneration || placement.PlacementGeneration != authority.PlacementGeneration {
		return sessionV2StaleAuthority("runtime session placement generation mismatch")
	}

	binding, bindingOK, err := s.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, execution.WorkspaceBindingID)
	if err != nil {
		return err
	}
	if !bindingOK {
		return sessionV2AuthorityNotFound("runtime session workspace binding %q was not found", execution.WorkspaceBindingID)
	}
	return runtimeSessionsV2ValidateBindingAuthority(req, placement, binding)
}

func runtimeSessionsV2ValidateBindingAuthority(req sessionruntime.RuntimeSessionOpenRequest, placement pebblestore.TopologyRuntimePlacementRecord, binding pebblestore.TopologyWorkspaceBindingRecord) error {
	authority := req.Authority
	execution := req.SessionExecution
	if strings.TrimSpace(authority.WorkspaceBindingID) == "" || strings.TrimSpace(execution.WorkspaceBindingID) == "" {
		return sessionV2BadRequest("runtime session workspace_binding_id is required")
	}
	if err := runtimeSessionsV2RequireEqual("workspace binding id", authority.WorkspaceBindingID, execution.WorkspaceBindingID); err != nil {
		return err
	}
	if err := runtimeSessionsV2RequireEqual("workspace binding record id", binding.BindingID, execution.WorkspaceBindingID); err != nil {
		return err
	}
	if strings.TrimSpace(binding.State) != pebblestore.TopologyWorkspaceBindingStateBound {
		return sessionV2StaleAuthority("runtime session workspace binding is not bound")
	}
	if strings.TrimSpace(binding.AttestedByHostSwarmID) != strings.TrimSpace(placement.AuthorityHostSwarmID) {
		return sessionV2StaleAuthority("runtime session workspace binding attesting host mismatch")
	}
	if binding.PlacementGeneration != execution.PlacementGeneration || binding.PlacementGeneration != authority.PlacementGeneration {
		return sessionV2StaleAuthority("runtime session workspace binding placement generation mismatch")
	}
	if binding.BindingGeneration != execution.BindingGeneration || binding.BindingGeneration != authority.BindingGeneration {
		return sessionV2StaleAuthority("runtime session workspace binding generation mismatch")
	}
	if strings.TrimSpace(binding.AccessMode) != pebblestore.TopologyWorkspaceBindingAccessModeReadWrite || !binding.Writable {
		return sessionV2AccessDenied("runtime session workspace binding must be read_write and writable")
	}

	if err := runtimeSessionsV2RequireEqual("source workspace id", authority.SourceWorkspaceID, execution.SourceWorkspaceID); err != nil {
		return err
	}
	if strings.TrimSpace(req.SourceWorkspace.WorkspaceID) != "" {
		if err := runtimeSessionsV2RequireEqual("source workspace facts id", req.SourceWorkspace.WorkspaceID, execution.SourceWorkspaceID); err != nil {
			return err
		}
	}
	if req.SourceWorkspace.WorkspaceGeneration <= 0 {
		return sessionV2BadRequest("runtime session source workspace generation is required")
	}
	if binding.SourceWorkspaceGeneration != execution.SourceWorkspaceGeneration || authority.SourceWorkspaceGeneration != execution.SourceWorkspaceGeneration || req.SourceWorkspace.WorkspaceGeneration != execution.SourceWorkspaceGeneration {
		return sessionV2StaleAuthority("runtime session source workspace generation mismatch")
	}
	if err := runtimeSessionsV2RequireEqual("source workspace record id", binding.SourceWorkspaceID, execution.SourceWorkspaceID); err != nil {
		return err
	}
	if err := runtimeSessionsV2RequireEqual("source workspace path", authority.SourceWorkspacePath, execution.SourceWorkspacePath); err != nil {
		return err
	}
	if err := runtimeSessionsV2RequireEqual("source workspace facts path", req.SourceWorkspace.WorkspacePath, execution.SourceWorkspacePath); err != nil {
		return err
	}
	if err := runtimeSessionsV2RequireEqual("source workspace binding path", binding.SourceWorkspacePath, execution.SourceWorkspacePath); err != nil {
		return err
	}
	if strings.TrimSpace(authority.SourceWorkspaceName) != "" && strings.TrimSpace(execution.SourceWorkspaceName) != "" && strings.TrimSpace(authority.SourceWorkspaceName) != strings.TrimSpace(execution.SourceWorkspaceName) {
		return sessionV2StaleAuthority("runtime session source workspace name mismatch")
	}
	if strings.TrimSpace(req.SourceWorkspace.WorkspaceName) != "" && strings.TrimSpace(execution.SourceWorkspaceName) != "" && strings.TrimSpace(req.SourceWorkspace.WorkspaceName) != strings.TrimSpace(execution.SourceWorkspaceName) {
		return sessionV2StaleAuthority("runtime session source workspace facts name mismatch")
	}

	if strings.TrimSpace(execution.RuntimeWorkspacePath) == "" || strings.TrimSpace(authority.RuntimeWorkspacePath) == "" || strings.TrimSpace(binding.DestinationWorkspacePath) == "" || strings.TrimSpace(req.DestinationRuntimeWorkspace.RuntimeWorkspacePath) == "" {
		return sessionV2BadRequest("runtime session runtime workspace path is required")
	}
	if err := runtimeSessionsV2RequireEqual("runtime workspace path", authority.RuntimeWorkspacePath, execution.RuntimeWorkspacePath); err != nil {
		return err
	}
	if err := runtimeSessionsV2RequireEqual("destination workspace binding path", binding.DestinationWorkspacePath, execution.RuntimeWorkspacePath); err != nil {
		return err
	}
	if err := runtimeSessionsV2RequireEqual("destination runtime workspace path", req.DestinationRuntimeWorkspace.RuntimeWorkspacePath, execution.RuntimeWorkspacePath); err != nil {
		return err
	}
	if strings.TrimSpace(req.DestinationRuntimeWorkspace.WorkspacePath) != "" {
		if err := runtimeSessionsV2RequireEqual("destination runtime workspace facts path", req.DestinationRuntimeWorkspace.WorkspacePath, execution.RuntimeWorkspacePath); err != nil {
			return err
		}
	}
	if strings.TrimSpace(binding.DestinationRuntimeSwarmID) != strings.TrimSpace(execution.RuntimeSwarmID) || strings.TrimSpace(binding.DestinationAuthorityHostSwarmID) != strings.TrimSpace(execution.AuthorityHostSwarmID) {
		return sessionV2StaleAuthority("runtime session workspace binding destination runtime mismatch")
	}
	if strings.TrimSpace(binding.DestinationRuntimeKind) != pebblestore.TopologyRuntimeKindContainer || strings.TrimSpace(binding.DestinationContainerID) != strings.TrimSpace(execution.AuthorityContainerID) {
		return sessionV2StaleAuthority("runtime session workspace binding destination container mismatch")
	}
	if execution.WorktreeEnabled || req.Worktree.Enabled {
		if !execution.WorktreeEnabled || !req.Worktree.Enabled || strings.TrimSpace(req.Worktree.RootPath) != strings.TrimSpace(execution.WorktreeRootPath) || strings.TrimSpace(req.Worktree.Branch) != strings.TrimSpace(execution.WorktreeBranch) || strings.TrimSpace(req.Worktree.BaseBranch) != strings.TrimSpace(execution.WorktreeBaseBranch) {
			return sessionV2StaleAuthority("runtime session worktree facts mismatch")
		}
	}
	return nil
}

func runtimeSessionsV2RequireEqual(field, got, want string) error {
	if strings.TrimSpace(got) == "" || strings.TrimSpace(want) == "" {
		return sessionV2BadRequest("runtime session %s is required", field)
	}
	if strings.TrimSpace(got) != strings.TrimSpace(want) {
		return sessionV2StaleAuthority("runtime session %s mismatch", field)
	}
	return nil
}

func runtimeSessionsV2ValidateStoredExecution(requested, stored pebblestore.SessionExecutionV2Record) error {
	requested.CreatedAt = stored.CreatedAt
	requested.UpdatedAt = stored.UpdatedAt
	reqJSON, err := json.Marshal(requested)
	if err != nil {
		return fmt.Errorf("marshal requested runtime session execution: %w", err)
	}
	storedJSON, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("marshal stored runtime session execution: %w", err)
	}
	if string(reqJSON) != string(storedJSON) {
		return sessionV2StaleAuthority("runtime session stored execution does not match open authority")
	}
	return nil
}

func runtimeSessionsV2ExecutionFromRecord(record pebblestore.SessionExecutionV2Record) sessionruntime.SessionExecution {
	return sessionruntime.SessionExecution{
		SessionID:                 strings.TrimSpace(record.SessionID),
		ExecutionClass:            strings.TrimSpace(record.ExecutionClass),
		RuntimeSwarmID:            strings.TrimSpace(record.RuntimeSwarmID),
		RuntimeKind:               strings.TrimSpace(record.RuntimeKind),
		AuthorityHostSwarmID:      strings.TrimSpace(record.AuthorityHostSwarmID),
		AuthorityContainerID:      strings.TrimSpace(record.AuthorityContainerID),
		WorkspaceBindingID:        strings.TrimSpace(record.WorkspaceBindingID),
		SourceWorkspaceID:         strings.TrimSpace(record.SourceWorkspaceID),
		SourceWorkspaceGeneration: record.SourceWorkspaceGeneration,
		SourceWorkspaceName:       strings.TrimSpace(record.SourceWorkspaceName),
		SourceWorkspacePath:       strings.TrimSpace(record.SourceWorkspacePath),
		RuntimeWorkspacePath:      strings.TrimSpace(record.RuntimeWorkspacePath),
		WorktreeEnabled:           record.WorktreeEnabled,
		WorktreeRootPath:          strings.TrimSpace(record.WorktreeRootPath),
		WorktreeBaseBranch:        strings.TrimSpace(record.WorktreeBaseBranch),
		WorktreeBranch:            strings.TrimSpace(record.WorktreeBranch),
		PlacementGeneration:       record.PlacementGeneration,
		BindingGeneration:         record.BindingGeneration,
		CreatedAt:                 record.CreatedAt,
		UpdatedAt:                 record.UpdatedAt,
	}
}

func runtimeSessionsV2LifecycleState(lifecycle pebblestore.SessionLifecycleSnapshot) string {
	if strings.TrimSpace(lifecycle.Phase) != "" {
		return strings.TrimSpace(lifecycle.Phase)
	}
	if lifecycle.Active {
		return "active"
	}
	return "standby"
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
