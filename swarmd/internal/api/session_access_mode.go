package api

import (
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var errReadOnlyWorkspaceBinding = errors.New("workspace binding is read-only")

func (s *Server) enforceSessionBindingWriteAccess(principal identity.Principal, sessionID, operation string) error {
	if s == nil {
		return nil
	}
	binding, ok, err := s.sessionWorkspaceBindingForAccess(principal, sessionID)
	if err != nil || !ok {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(binding.AccessMode), pebblestore.TopologyWorkspaceBindingAccessModeReadOnly) {
		operation = strings.TrimSpace(operation)
		if operation == "" {
			operation = "mutating operation"
		}
		return fmt.Errorf("%w: %s is not allowed for read_only workspace binding %q", errReadOnlyWorkspaceBinding, operation, strings.TrimSpace(binding.BindingID))
	}
	return nil
}

func (s *Server) sessionWorkspaceBindingForAccess(principal identity.Principal, sessionID string) (pebblestore.TopologyWorkspaceBindingRecord, bool, error) {
	if s == nil || s.topology == nil || !principal.Valid() {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, nil
	}
	if s.sessions == nil {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, nil
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, err
	}
	if !ok || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) || strings.TrimSpace(session.UserID) != strings.TrimSpace(principal.UserID) {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, nil
	}
	return s.sessionWorkspaceBindingFromLineage(principal, session)
}

func (s *Server) sessionWorkspaceBindingFromLineage(principal identity.Principal, session pebblestore.SessionSnapshot) (pebblestore.TopologyWorkspaceBindingRecord, bool, error) {
	seen := make(map[string]struct{}, 8)
	for depth := 0; depth < 100; depth++ {
		if strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) || strings.TrimSpace(session.UserID) != strings.TrimSpace(principal.UserID) {
			return pebblestore.TopologyWorkspaceBindingRecord{}, false, nil
		}
		sessionID := strings.TrimSpace(session.ID)
		if sessionID == "" {
			return pebblestore.TopologyWorkspaceBindingRecord{}, false, nil
		}
		if _, exists := seen[sessionID]; exists {
			return pebblestore.TopologyWorkspaceBindingRecord{}, false, errors.New("worktree session lineage contains a cycle")
		}
		seen[sessionID] = struct{}{}
		bindingID := firstNonEmpty(
			strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "swarm_v3_workspace_binding_id")),
			strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "local_workspace_binding_id")),
		)
		if bindingID != "" {
			return s.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, bindingID)
		}
		parentSessionID := strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "parent_session_id"))
		if parentSessionID == "" {
			return pebblestore.TopologyWorkspaceBindingRecord{}, false, nil
		}
		parent, found, err := s.sessions.GetSession(parentSessionID)
		if err != nil || !found {
			return pebblestore.TopologyWorkspaceBindingRecord{}, false, err
		}
		session = parent
	}
	return pebblestore.TopologyWorkspaceBindingRecord{}, false, errors.New("worktree session lineage exceeds the supported depth")
}
