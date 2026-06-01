package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	sessionsV2EndpointPrimary        = "primary"
	sessionsV2EndpointLocalContainer = "local_container"
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
	if s.sessions == nil || s.topology == nil {
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
	execution, err := s.buildSessionsV2Execution(r, principal, req, endpointClass)
	if err != nil {
		writeSessionsV2Error(w, err)
		return
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
	if err := validateSessionsV2Metadata(req.Metadata); err != nil {
		return sessionruntime.SessionsV2CreateRequest{}, err
	}
	return req, nil
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
