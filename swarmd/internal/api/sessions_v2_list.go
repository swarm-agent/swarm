package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) handleSessionsV2List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.sessions == nil {
		writeSessionsV2Error(w, errors.New("session service not configured"))
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		writeSessionsV2Error(w, identity.ErrPrincipalRequired)
		return
	}

	mode, value, limit, err := decodeSessionsV2ListQuery(r)
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}

	var sessions []pebblestore.SessionSnapshot
	switch mode {
	case "cwd":
		sessions, err = s.sessions.ListSessionsForAccountPath(principal.AccountScopeID, value, limit)
	case "workspace_binding_id":
		sessions, err = s.listSessionsV2ForWorkspaceBinding(principal, value, limit)
	default:
		err = sessionV2BadRequest("sessions v2 list requires exactly one of cwd or workspace_binding_id")
	}
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	responseSessions, enrichErr := s.enrichSessionSummariesForList(sessions)
	if enrichErr != nil {
		writeSessionsV2Error(w, enrichErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"sessions": responseSessions,
	})
}

func decodeSessionsV2ListQuery(r *http.Request) (mode string, value string, limit int, err error) {
	limit = 100
	if r == nil || r.URL == nil {
		return "", "", 0, sessionV2BadRequest("sessions v2 list request is required")
	}
	query := r.URL.Query()
	for key := range query {
		switch key {
		case "cwd", "workspace_binding_id", "limit":
		default:
			return "", "", 0, sessionV2BadRequest("unsupported sessions v2 list query parameter %q", key)
		}
	}
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed <= 0 {
			return "", "", 0, sessionV2BadRequest("limit must be a positive integer")
		}
		limit = parsed
	}
	cwd := strings.TrimSpace(query.Get("cwd"))
	workspaceBindingID := strings.TrimSpace(query.Get("workspace_binding_id"))
	if cwd == "" && workspaceBindingID == "" {
		return "", "", 0, sessionV2BadRequest("sessions v2 list requires exactly one of cwd or workspace_binding_id")
	}
	if cwd != "" && workspaceBindingID != "" {
		return "", "", 0, sessionV2BadRequest("sessions v2 list accepts only one of cwd or workspace_binding_id")
	}
	if cwd != "" {
		return "cwd", cwd, limit, nil
	}
	return "workspace_binding_id", workspaceBindingID, limit, nil
}

func (s *Server) listSessionsV2ForWorkspaceBinding(principal identity.Principal, workspaceBindingID string, limit int) ([]pebblestore.SessionSnapshot, error) {
	if s.topology == nil {
		return nil, errors.New("topology service not configured")
	}
	workspaceBindingID = strings.TrimSpace(workspaceBindingID)
	binding, bindingOK, err := s.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, workspaceBindingID)
	if err != nil {
		return nil, err
	}
	if !bindingOK {
		return nil, sessionV2AuthorityNotFound("sessions v2 workspace binding %q was not found", workspaceBindingID)
	}
	if strings.TrimSpace(binding.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return nil, sessionV2AccessDenied("sessions v2 workspace binding account scope does not match principal")
	}
	if strings.TrimSpace(binding.UserID) != "" && strings.TrimSpace(binding.UserID) != strings.TrimSpace(principal.UserID) {
		return nil, sessionV2AccessDenied("sessions v2 workspace binding user does not match principal")
	}
	if strings.TrimSpace(binding.State) != pebblestore.TopologyWorkspaceBindingStateBound {
		return nil, sessionV2StaleAuthority("sessions v2 workspace binding is not bound")
	}
	sourceWorkspaceID := strings.TrimSpace(binding.SourceWorkspaceID)
	if sourceWorkspaceID == "" {
		return nil, sessionV2StaleAuthority("sessions v2 workspace binding source workspace identity is incomplete")
	}

	bindings, err := s.topology.ListWorkspaceBindingsForAccount(principal.AccountScopeID, 100000)
	if err != nil {
		return nil, err
	}
	bindingIDs := make([]string, 0, len(bindings))
	seenBindingIDs := make(map[string]struct{}, len(bindings))
	for _, candidate := range bindings {
		if strings.TrimSpace(candidate.SourceWorkspaceID) != sourceWorkspaceID {
			continue
		}
		if strings.TrimSpace(candidate.State) != pebblestore.TopologyWorkspaceBindingStateBound {
			continue
		}
		candidateID := strings.TrimSpace(candidate.BindingID)
		if candidateID == "" {
			continue
		}
		if _, exists := seenBindingIDs[candidateID]; exists {
			continue
		}
		seenBindingIDs[candidateID] = struct{}{}
		bindingIDs = append(bindingIDs, candidateID)
	}
	if _, exists := seenBindingIDs[workspaceBindingID]; !exists {
		bindingIDs = append(bindingIDs, workspaceBindingID)
	}
	return s.sessions.ListSessionsForAccountWorkspaceBindings(principal.AccountScopeID, sourceWorkspaceID, bindingIDs, "", limit)
}
