package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

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
	if err := s.validateRuntimeSessionV2OpenAuthority(r, principal, req); err != nil {
		return sessionruntime.RuntimeSessionOpenResponse{}, err
	}

	execution := runtimeSessionsV2ExecutionFromRecord(req.SessionExecution)
	if err := sessionruntime.ValidateSessionExecutionV2(execution); err != nil {
		return sessionruntime.RuntimeSessionOpenResponse{}, sessionV2BadRequest("%v", err)
	}
	preference, err := sessionruntime.NormalizeSessionPreferenceValue(req.Config.Preference)
	if err != nil {
		return sessionruntime.RuntimeSessionOpenResponse{}, err
	}
	if strings.TrimSpace(req.Config.WorktreeMode) != "" && !strings.EqualFold(strings.TrimSpace(req.Config.WorktreeMode), "off") {
		return sessionruntime.RuntimeSessionOpenResponse{}, sessionV2BadRequest("runtime session open does not install worktrees before the dedicated worktree checkpoint")
	}
	if req.Config.WorktreeUseCurrentBranch != nil {
		return sessionruntime.RuntimeSessionOpenResponse{}, sessionV2BadRequest("runtime session open does not install worktrees before the dedicated worktree checkpoint")
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

func (s *Server) validateRuntimeSessionV2OpenAuthority(r *http.Request, principal identity.Principal, req sessionruntime.RuntimeSessionOpenRequest) error {
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
	if strings.TrimSpace(runtimeRecord.AccountScopeID) == "" || strings.TrimSpace(runtimeRecord.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionV2AccessDenied("runtime session runtime account scope does not match principal")
	}
	if strings.TrimSpace(runtimeRecord.UserID) != "" && strings.TrimSpace(runtimeRecord.UserID) != strings.TrimSpace(principal.UserID) {
		return sessionV2AccessDenied("runtime session runtime user does not match principal")
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
	if strings.TrimSpace(placement.AccountScopeID) == "" || strings.TrimSpace(placement.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionV2AccessDenied("runtime session placement account scope does not match principal")
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

	if err := runtimeSessionsV2RequireBindingIdentity(req); err != nil {
		return err
	}
	if err := s.syncRuntimeSessionV2BindingAuthoritySnapshot(r, principal, req, placement); err != nil {
		return err
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

func (s *Server) syncRuntimeSessionV2BindingAuthoritySnapshot(r *http.Request, principal identity.Principal, req sessionruntime.RuntimeSessionOpenRequest, placement pebblestore.TopologyRuntimePlacementRecord) error {
	if s == nil || s.topology == nil {
		return errors.New("runtime sessions v2 topology service is not configured")
	}
	snapshot := req.BindingAuthoritySnapshot
	if snapshot == nil {
		return sessionV2BadRequest("runtime session binding_authority_snapshot is required")
	}
	sourceSwarmID := strings.TrimSpace(r.Header.Get(peerAuthSwarmIDHeader))
	execution := req.SessionExecution
	record := *snapshot
	if err := runtimeSessionsV2ValidateBindingAuthoritySnapshot(principal, sourceSwarmID, execution, placement, record); err != nil {
		return err
	}
	_, err := s.topology.PutWorkspaceBindingForAccount(principal.AccountScopeID, record)
	return err
}

func runtimeSessionsV2RequireBindingIdentity(req sessionruntime.RuntimeSessionOpenRequest) error {
	if strings.TrimSpace(req.Authority.WorkspaceBindingID) == "" || strings.TrimSpace(req.SessionExecution.WorkspaceBindingID) == "" {
		return sessionV2BadRequest("runtime session workspace_binding_id is required")
	}
	return runtimeSessionsV2RequireEqual("workspace binding id", req.Authority.WorkspaceBindingID, req.SessionExecution.WorkspaceBindingID)
}

func runtimeSessionsV2ValidateBindingAuthoritySnapshot(principal identity.Principal, sourceSwarmID string, execution pebblestore.SessionExecutionV2Record, placement pebblestore.TopologyRuntimePlacementRecord, record pebblestore.TopologyWorkspaceBindingRecord) error {
	if strings.TrimSpace(record.BindingID) == "" || strings.TrimSpace(record.BindingID) != strings.TrimSpace(execution.WorkspaceBindingID) {
		return sessionV2StaleAuthority("runtime session binding authority snapshot id mismatch")
	}
	if strings.TrimSpace(record.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionV2AccessDenied("runtime session binding authority snapshot account scope mismatch")
	}
	if strings.TrimSpace(record.UserID) == "" || strings.TrimSpace(record.UserID) != strings.TrimSpace(principal.UserID) {
		return sessionV2AccessDenied("runtime session binding authority snapshot user mismatch")
	}
	if sourceSwarmID == "" || strings.TrimSpace(record.DestinationAuthorityHostSwarmID) != sourceSwarmID || strings.TrimSpace(execution.AuthorityHostSwarmID) != sourceSwarmID {
		return sessionV2AccessDenied("runtime session binding authority snapshot must come from authority host")
	}
	if strings.TrimSpace(record.DestinationRuntimeSwarmID) != strings.TrimSpace(placement.RuntimeSwarmID) || strings.TrimSpace(record.DestinationRuntimeSwarmID) != strings.TrimSpace(execution.RuntimeSwarmID) {
		return sessionV2StaleAuthority("runtime session binding authority snapshot destination runtime mismatch")
	}
	if strings.TrimSpace(record.DestinationRuntimeKind) != pebblestore.TopologyRuntimeKindContainer || strings.TrimSpace(record.DestinationRuntimeKind) != strings.TrimSpace(placement.RuntimeKind) {
		return sessionV2StaleAuthority("runtime session binding authority snapshot destination kind mismatch")
	}
	if strings.TrimSpace(record.DestinationAuthorityHostSwarmID) != strings.TrimSpace(execution.AuthorityHostSwarmID) || strings.TrimSpace(record.DestinationAuthorityHostSwarmID) != strings.TrimSpace(placement.AuthorityHostSwarmID) {
		return sessionV2StaleAuthority("runtime session binding authority snapshot authority host mismatch")
	}
	if strings.TrimSpace(record.DestinationContainerID) != strings.TrimSpace(placement.AuthorityContainerID) || strings.TrimSpace(record.DestinationContainerID) != strings.TrimSpace(execution.AuthorityContainerID) {
		return sessionV2StaleAuthority("runtime session binding authority snapshot destination container mismatch")
	}
	if strings.TrimSpace(record.DestinationWorkspacePath) == "" || strings.TrimSpace(record.DestinationWorkspacePath) != strings.TrimSpace(execution.RuntimeWorkspacePath) {
		return sessionV2StaleAuthority("runtime session binding authority snapshot destination path mismatch")
	}
	if record.PlacementGeneration != placement.PlacementGeneration || record.PlacementGeneration != execution.PlacementGeneration {
		return sessionV2StaleAuthority("runtime session binding authority snapshot placement generation mismatch")
	}
	if record.BindingGeneration != execution.BindingGeneration {
		return sessionV2StaleAuthority("runtime session binding authority snapshot binding generation mismatch")
	}
	if strings.TrimSpace(record.SourceWorkspaceID) == "" || strings.TrimSpace(record.SourceWorkspaceID) != strings.TrimSpace(execution.SourceWorkspaceID) {
		return sessionV2StaleAuthority("runtime session binding authority snapshot source workspace id mismatch")
	}
	if record.SourceWorkspaceGeneration != execution.SourceWorkspaceGeneration {
		return sessionV2StaleAuthority("runtime session binding authority snapshot source workspace generation mismatch")
	}
	if strings.TrimSpace(record.SourceWorkspacePath) == "" || strings.TrimSpace(record.SourceWorkspacePath) != strings.TrimSpace(execution.SourceWorkspacePath) {
		return sessionV2StaleAuthority("runtime session binding authority snapshot source workspace path mismatch")
	}
	if strings.TrimSpace(record.State) != pebblestore.TopologyWorkspaceBindingStateBound {
		return sessionV2StaleAuthority("runtime session binding authority snapshot is not bound")
	}
	if strings.TrimSpace(record.AttestedByHostSwarmID) != sourceSwarmID || strings.TrimSpace(record.AttestedByHostSwarmID) != strings.TrimSpace(execution.AuthorityHostSwarmID) {
		return sessionV2StaleAuthority("runtime session binding authority snapshot attesting host mismatch")
	}
	if strings.TrimSpace(record.AccessMode) != pebblestore.TopologyWorkspaceBindingAccessModeReadWrite || !record.Writable {
		return sessionV2AccessDenied("runtime session binding authority snapshot must be read_write and writable")
	}
	return nil
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
	if strings.TrimSpace(binding.AccountScopeID) != strings.TrimSpace(req.SessionExecution.AccountScopeID) {
		return sessionV2AccessDenied("runtime session workspace binding account scope mismatch")
	}
	if strings.TrimSpace(binding.UserID) == "" || strings.TrimSpace(binding.UserID) != strings.TrimSpace(req.SessionExecution.UserID) {
		return sessionV2AccessDenied("runtime session workspace binding user mismatch")
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
	if strings.TrimSpace(req.SourceWorkspace.WorkspaceID) == "" {
		return sessionV2BadRequest("runtime session source workspace facts id is required")
	}
	if err := runtimeSessionsV2RequireEqual("source workspace facts id", req.SourceWorkspace.WorkspaceID, execution.SourceWorkspaceID); err != nil {
		return err
	}
	if req.SourceWorkspace.WorkspaceGeneration <= 0 {
		return sessionV2BadRequest("runtime session source workspace generation is required")
	}
	if binding.SourceWorkspaceGeneration != execution.SourceWorkspaceGeneration || authority.SourceWorkspaceGeneration != execution.SourceWorkspaceGeneration || req.SourceWorkspace.WorkspaceGeneration != execution.SourceWorkspaceGeneration {
		return sessionV2StaleAuthority("runtime session source workspace generation mismatch")
	}
	if strings.TrimSpace(req.DestinationRuntimeWorkspace.WorkspaceID) != "" {
		if err := runtimeSessionsV2RequireEqual("destination runtime workspace facts id", req.DestinationRuntimeWorkspace.WorkspaceID, execution.SourceWorkspaceID); err != nil {
			return err
		}
	}
	if req.DestinationRuntimeWorkspace.WorkspaceGeneration > 0 && req.DestinationRuntimeWorkspace.WorkspaceGeneration != execution.SourceWorkspaceGeneration {
		return sessionV2StaleAuthority("runtime session destination runtime workspace facts generation mismatch")
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
	case "":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		s.handleRuntimeSessionV2Get(w, r, sessionID)
	case "messages":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		s.handleRuntimeSessionV2Messages(w, r, sessionID)
	case "metadata":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		s.handleRuntimeSessionV2Metadata(w, r, sessionID)
	case "mode":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		s.handleRuntimeSessionV2Mode(w, r, sessionID)
	case "preference":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		s.handleRuntimeSessionV2Preference(w, r, sessionID)
	case "codex":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		s.handleRuntimeSessionV2Codex(w, r, sessionID)
	case "plans/active":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		s.handleRuntimeSessionV2ActivePlan(w, r, sessionID)
	case "plans":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		s.handleRuntimeSessionV2Plans(w, r, sessionID)
	case "permissions":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		s.handleRuntimeSessionV2Permissions(w, r, sessionID)
	case "permissions/resolve_all":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		s.handleRuntimeSessionV2PermissionResolveAll(w, r, sessionID)
	case "usage":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		s.handleRuntimeSessionV2Usage(w, r, sessionID)
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
		principal, err := s.requireRuntimeSessionV2Principal(r, sessionID)
		if err != nil {
			writeSessionsV2Error(w, err)
			return
		}
		if err := s.requireRuntimeSessionV2MutationAuthority(principal, sessionID); err != nil {
			writeSessionsV2Error(w, err)
			return
		}
		s.handleNativeSessionV2Run(w, r, sessionID, principal)
	case "run/stop":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		principal, err := s.requireRuntimeSessionV2Principal(r, sessionID)
		if err != nil {
			writeSessionsV2Error(w, err)
			return
		}
		if err := s.requireRuntimeSessionV2MutationAuthority(principal, sessionID); err != nil {
			writeSessionsV2Error(w, err)
			return
		}
		s.handleRuntimeSessionV2RunStop(w, r, sessionID, principal)
	case "run/stream":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		principal, err := s.requireRuntimeSessionV2Principal(r, sessionID)
		if err != nil {
			writeSessionsV2Error(w, err)
			return
		}
		s.handleAuthorizedSessionV2RunStream(w, r, sessionID, principal, func() error {
			return s.requireRuntimeSessionV2MutationAuthority(principal, sessionID)
		})
	case "mirror/batch":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		s.handleRuntimeSessionV2MirrorBatch(w, r, sessionID)
	default:
		if strings.HasPrefix(action, "plans/") {
			if r.Method != http.MethodGet {
				methodNotAllowed(w)
				return
			}
			s.handleRuntimeSessionV2PlanByID(w, r, sessionID, strings.TrimPrefix(action, "plans/"))
			return
		}
		if strings.HasPrefix(action, "permissions/") && strings.HasSuffix(action, "/resolve") {
			if r.Method != http.MethodPost {
				methodNotAllowed(w)
				return
			}
			s.handleRuntimeSessionV2PermissionResolve(w, r, sessionID, strings.TrimSuffix(strings.TrimPrefix(action, "permissions/"), "/resolve"))
			return
		}
		writeError(w, http.StatusNotFound, errors.New("runtime sessions v2 route not found"))
	}
}

func (s *Server) requireRuntimeSessionV2Principal(r *http.Request, sessionID string) (identity.Principal, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return identity.Principal{}, sessionV2BadRequest("runtime session id is required")
	}
	if !isTrustedPeerOrLocalTransport(r) {
		return identity.Principal{}, sessionV2AccessDenied("runtime session internal access requires trusted runtime transport")
	}
	principal, ok := s.trustedRuntimeSessionV2PrincipalForPeerRequest(r, sessionID)
	if !ok || !principal.Valid() {
		return identity.Principal{}, identity.ErrPrincipalRequired
	}
	if strings.TrimSpace(principal.SessionID) != sessionID {
		return identity.Principal{}, sessionV2AccessDenied("runtime session principal session id does not match request")
	}
	if s == nil || s.sessions == nil || s.sessions.Store() == nil {
		return identity.Principal{}, errors.New("runtime sessions v2 service is not configured")
	}
	execution, ok, err := s.sessions.Store().GetSessionExecutionV2(sessionID)
	if err != nil {
		return identity.Principal{}, err
	}
	if !ok {
		return identity.Principal{}, sessionV2AuthorityNotFound("runtime session execution for %q was not found", sessionID)
	}
	if strings.TrimSpace(execution.SessionID) != sessionID {
		return identity.Principal{}, sessionV2StaleAuthority("runtime session execution session id mismatch")
	}
	if strings.TrimSpace(execution.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return identity.Principal{}, sessionV2AccessDenied("runtime session execution account scope does not match principal")
	}
	if strings.TrimSpace(execution.UserID) != "" && strings.TrimSpace(execution.UserID) != strings.TrimSpace(principal.UserID) {
		return identity.Principal{}, sessionV2AccessDenied("runtime session execution user does not match principal")
	}
	ctx := identity.ContextWithPrincipal(r.Context(), principal)
	*r = *r.WithContext(ctx)
	return principal, nil
}

func (s *Server) handleRuntimeSessionV2RunStop(w http.ResponseWriter, r *http.Request, sessionID string, principal identity.Principal) {
	var req sessionruntime.RuntimeSessionStopRequest
	if err := decodeJSON(r, &req); err != nil {
		writeSessionsV2Error(w, sessionV2BadRequest("invalid runtime session stop request: %v", err))
		return
	}
	resp, err := s.stopRuntimeSessionV2Run(sessionID, principal, req)
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) stopRuntimeSessionV2Run(sessionID string, principal identity.Principal, req sessionruntime.RuntimeSessionStopRequest) (sessionruntime.RuntimeSessionStopResponse, error) {
	if s == nil || s.runner == nil || s.runStreams == nil || s.sessions == nil || s.sessions.Store() == nil {
		return sessionruntime.RuntimeSessionStopResponse{}, errors.New("runtime sessions v2 run stop service is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		return sessionruntime.RuntimeSessionStopResponse{}, sessionV2BadRequest("run_id is required for stop")
	}
	execution, ok, err := s.sessions.Store().GetSessionExecutionV2(sessionID)
	if err != nil {
		return sessionruntime.RuntimeSessionStopResponse{}, err
	}
	if !ok {
		return sessionruntime.RuntimeSessionStopResponse{}, sessionV2AuthorityNotFound("runtime session execution for %q was not found", sessionID)
	}
	if strings.TrimSpace(execution.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionruntime.RuntimeSessionStopResponse{}, sessionV2AccessDenied("runtime session execution account scope does not match principal")
	}

	const stopReason = "run stopped by user"
	lifecycle, lifecycleOK, err := s.sessions.GetLifecycle(sessionID)
	if err != nil {
		return sessionruntime.RuntimeSessionStopResponse{}, err
	}
	if !lifecycleOK || !lifecycle.Active || !strings.EqualFold(strings.TrimSpace(lifecycle.RunID), runID) {
		return sessionruntime.RuntimeSessionStopResponse{}, sessionV2StaleAuthority("runtime session stop failed: session has no active run")
	}
	s.runStreams.setStopReason(runID, stopReason)
	if err := s.runner.StopSessionRun(sessionID, runID, stopReason); err != nil {
		return sessionruntime.RuntimeSessionStopResponse{}, sessionV2StaleAuthority("runtime session stop failed: %v", err)
	}
	if refreshed, ok, err := s.sessions.GetLifecycle(sessionID); err != nil {
		return sessionruntime.RuntimeSessionStopResponse{}, err
	} else if ok && strings.EqualFold(strings.TrimSpace(refreshed.RunID), runID) {
		lifecycle = refreshed
	}
	{
		lifecycle.SessionID = sessionID
		if strings.TrimSpace(lifecycle.UserID) == "" {
			lifecycle.UserID = execution.UserID
		}
		if strings.TrimSpace(lifecycle.AccountScopeID) == "" {
			lifecycle.AccountScopeID = execution.AccountScopeID
		}
		lifecycle.Active = false
		lifecycle.Phase = "cancelled"
		if strings.TrimSpace(lifecycle.StopReason) == "" {
			lifecycle.StopReason = stopReason
		}
		if lifecycle.UpdatedAt == 0 {
			lifecycle.UpdatedAt = time.Now().UnixMilli()
		}
		if lifecycle.EndedAt == 0 {
			lifecycle.EndedAt = lifecycle.UpdatedAt
		}
	}
	mirrorBatch, err := runtimeSessionV2StopMirrorBatch(execution, lifecycle)
	if err != nil {
		return sessionruntime.RuntimeSessionStopResponse{}, err
	}
	return sessionruntime.RuntimeSessionStopResponse{OK: true, SessionID: sessionID, RunID: runID, Status: "stop_requested", TargetSwarmID: strings.TrimSpace(execution.RuntimeSwarmID), MirrorStatus: "ready", MirrorAccepted: len(mirrorBatch.Items), Lifecycle: &lifecycle, MirrorBatch: &mirrorBatch}, nil
}

func runtimeSessionV2StopMirrorBatch(execution pebblestore.SessionExecutionV2Record, lifecycle pebblestore.SessionLifecycleSnapshot) (sessionruntime.RuntimeSessionMirrorBatchRequest, error) {
	payload, err := json.Marshal(sessionruntime.RuntimeSessionLifecycleMirrorItem{Lifecycle: lifecycle})
	if err != nil {
		return sessionruntime.RuntimeSessionMirrorBatchRequest{}, err
	}
	return sessionruntime.RuntimeSessionMirrorBatchRequest{
		SessionID:        strings.TrimSpace(execution.SessionID),
		Authority:        runtimeSessionOpenRequestFromFrozenExecution(execution, sessionruntime.SessionsV2CreateRequest{}).Authority,
		SessionExecution: execution,
		Items: []sessionruntime.RuntimeSessionMirrorItem{{
			Type:      sessionruntime.RuntimeSessionMirrorTypeSessionLifecycle,
			SessionID: strings.TrimSpace(execution.SessionID),
			RunID:     strings.TrimSpace(lifecycle.RunID),
			CreatedAt: lifecycle.UpdatedAt,
			Payload:   payload,
		}},
	}, nil
}

func (s *Server) handleRuntimeSessionV2MirrorBatch(w http.ResponseWriter, r *http.Request, sessionID string) {
	principal, err := s.requireRuntimeSessionV2Principal(r, sessionID)
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if s == nil || s.sessions == nil || s.sessions.Store() == nil {
		writeSessionsV2Error(w, errors.New("runtime sessions v2 service is not configured"))
		return
	}
	execution, ok, err := s.sessions.Store().GetSessionExecutionV2(sessionID)
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if !ok {
		writeSessionsV2Error(w, sessionV2AuthorityNotFound("runtime session execution for %q was not found", sessionID))
		return
	}
	if strings.TrimSpace(execution.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		writeSessionsV2Error(w, sessionV2AccessDenied("runtime session execution account scope does not match principal"))
		return
	}
	peerSwarmID, authorizedPeer := authorizedPeerSwarmID(r)
	if !authorizedPeer || !strings.EqualFold(strings.TrimSpace(peerSwarmID), strings.TrimSpace(execution.RuntimeSwarmID)) {
		writeSessionsV2Error(w, sessionV2AccessDenied("runtime session mirror batch requires trusted runtime peer transport"))
		return
	}
	var batch sessionruntime.RuntimeSessionMirrorBatchRequest
	if err := decodeJSON(r, &batch); err != nil {
		writeSessionsV2Error(w, sessionV2BadRequest("invalid runtime session mirror batch request: %v", err))
		return
	}
	accepted, err := s.ingestRuntimeSessionV2MirrorBatch(execution, batch, runtimeSessionV2MirrorIngestionOptions{})
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionruntime.RuntimeSessionMirrorBatchResponse{OK: true, SessionID: sessionID, Accepted: accepted, Status: "accepted"})
}

func (s *Server) requireRuntimeSessionV2MutationAuthority(principal identity.Principal, sessionID string) error {
	if !principal.Valid() {
		return identity.ErrPrincipalRequired
	}
	if s == nil || s.sessions == nil || s.sessions.Store() == nil || s.topology == nil {
		return errors.New("runtime sessions v2 service is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	execution, ok, err := s.sessions.Store().GetSessionExecutionV2(sessionID)
	if err != nil {
		return err
	}
	if !ok {
		return sessionV2AuthorityNotFound("runtime session execution for %q was not found", sessionID)
	}
	if strings.TrimSpace(execution.SessionID) != sessionID {
		return sessionV2StaleAuthority("runtime session execution session id mismatch")
	}
	if strings.TrimSpace(execution.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionV2AccessDenied("runtime session execution account scope does not match principal")
	}
	binding, ok, err := s.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, execution.WorkspaceBindingID)
	if err != nil {
		return err
	}
	if !ok {
		return sessionV2AuthorityNotFound("runtime session workspace binding %q was not found", execution.WorkspaceBindingID)
	}
	if strings.TrimSpace(binding.BindingID) != strings.TrimSpace(execution.WorkspaceBindingID) || binding.BindingGeneration != execution.BindingGeneration || binding.PlacementGeneration != execution.PlacementGeneration {
		return sessionV2StaleAuthority("runtime session workspace binding generation mismatch")
	}
	if strings.TrimSpace(binding.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionV2AccessDenied("runtime session workspace binding account scope does not match principal")
	}
	if strings.TrimSpace(binding.State) != pebblestore.TopologyWorkspaceBindingStateBound {
		return sessionV2StaleAuthority("runtime session workspace binding is not bound")
	}
	if strings.TrimSpace(binding.AccessMode) != pebblestore.TopologyWorkspaceBindingAccessModeReadWrite || !binding.Writable {
		return sessionV2AccessDenied("runtime session workspace binding is read-only")
	}
	return nil
}

func (s *Server) handleRuntimeSessionV2Get(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !ok {
		writeSessionNotFound(w)
		return
	}
	s.writeSessionSnapshot(w, session)
}

func (s *Server) handleRuntimeSessionV2Messages(w http.ResponseWriter, r *http.Request, sessionID string) {
	switch r.Method {
	case http.MethodGet:
		afterSeq, limit, ok := parseAfterSeqAndLimit(w, r, 500)
		if !ok {
			return
		}
		messages, err := s.sessions.ListMessages(sessionID, afterSeq, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "messages": messages})
	case http.MethodPost:
		var req struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		message, updatedSession, event, err := s.sessions.AppendMessage(sessionID, req.Role, req.Content, nil)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if event != nil && s.hub != nil {
			s.hub.Publish(*event)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": message, "session": updatedSession})
	}
}

func (s *Server) handleRuntimeSessionV2Metadata(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method == http.MethodGet {
		session, ok, err := s.sessions.GetSession(sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !ok {
			writeSessionNotFound(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "metadata": session.Metadata, "updated_at": session.UpdatedAt})
		return
	}
	var req struct {
		Metadata map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validateSessionsV2Metadata(req.Metadata); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	session, event, err := s.sessions.UpdateMetadata(sessionID, req.Metadata)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": session})
}

func (s *Server) handleRuntimeSessionV2Mode(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method == http.MethodGet {
		mode, err := s.sessions.GetMode(sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "mode": mode})
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, event, err := s.sessions.SetMode(sessionID, req.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "mode": session.Mode, "updated_at": session.UpdatedAt, "warning": ""})
}

func (s *Server) handleRuntimeSessionV2Preference(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method == http.MethodGet {
		pref, err := s.sessions.GetSessionPreference(sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		resolved, err := s.model.ResolvePreference(pref)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, resolved)
		return
	}
	var req struct {
		Provider    *string `json:"provider,omitempty"`
		Model       *string `json:"model,omitempty"`
		Thinking    *string `json:"thinking,omitempty"`
		ServiceTier *string `json:"service_tier,omitempty"`
		ContextMode *string `json:"context_mode,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	pref, event, err := s.sessions.SetSessionPreference(sessionID, sessionruntime.SessionPreferenceUpdate{Provider: req.Provider, Model: req.Model, Thinking: req.Thinking, ServiceTier: req.ServiceTier, ContextMode: req.ContextMode})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	resolved, err := s.model.ResolvePreference(pref)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, resolved)
}

func (s *Server) handleRuntimeSessionV2Codex(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method == http.MethodGet {
		config, err := s.sessions.GetCodexConfig(sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, s.codexSessionConfigResponse(sessionID, config))
		return
	}
	var req struct {
		ServiceTier *string `json:"service_tier,omitempty"`
		ContextMode *string `json:"context_mode,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	config, event, err := s.sessions.SetCodexConfig(sessionID, sessionruntime.SessionCodexConfigUpdate{ServiceTier: req.ServiceTier, ContextMode: req.ContextMode})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, s.codexSessionConfigResponse(sessionID, config))
}

func (s *Server) handleRuntimeSessionV2ActivePlan(w http.ResponseWriter, r *http.Request, sessionID string) {
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

func (s *Server) handleRuntimeSessionV2Plans(w http.ResponseWriter, r *http.Request, sessionID string) {
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
	var req struct {
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

func (s *Server) handleRuntimeSessionV2PlanByID(w http.ResponseWriter, r *http.Request, sessionID, tail string) {
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

func (s *Server) handleRuntimeSessionV2Permissions(w http.ResponseWriter, r *http.Request, sessionID string) {
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	limit, ok := parseSessionsV2PositiveLimit(w, r, 200)
	if !ok {
		return
	}
	var permissions []pebblestore.PermissionRecord
	var err error
	switch status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status"))); status {
	case "", "all":
		permissions, err = s.perm.ListPermissions(sessionID, limit)
	case pebblestore.PermissionStatusPending:
		permissions, err = s.perm.ListPending(sessionID, limit)
	default:
		writeError(w, http.StatusBadRequest, errors.New("unsupported permission status"))
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "count": len(permissions), "permissions": permissions})
}

func (s *Server) handleRuntimeSessionV2PermissionResolve(w http.ResponseWriter, r *http.Request, sessionID, permissionID string) {
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	permissionID = strings.Trim(permissionID, "/")
	if permissionID == "" || strings.Contains(permissionID, "/") {
		writeError(w, http.StatusBadRequest, errors.New("permission id is required"))
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

func (s *Server) handleRuntimeSessionV2PermissionResolveAll(w http.ResponseWriter, r *http.Request, sessionID string) {
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
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

func (s *Server) handleRuntimeSessionV2Usage(w http.ResponseWriter, r *http.Request, sessionID string) {
	limit, ok := parseSessionsV2PositiveLimit(w, r, 50)
	if !ok {
		return
	}
	summary, hasSummary, err := s.sessions.GetUsageSummary(sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	turns, err := s.sessions.ListTurnUsage(sessionID, limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var summaryPayload any
	if hasSummary {
		summaryPayload = summary
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "has_usage_summary": hasSummary, "usage_summary": summaryPayload, "turn_usage_records": turns})
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
		return sessionID, "", true
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
