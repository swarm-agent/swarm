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
	bindingID := ""
	if route, ok, err := s.topology.GetSessionRouteForAccount(principal.AccountScopeID, sessionID); err != nil {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, err
	} else if ok {
		bindingID = strings.TrimSpace(route.WorkspaceBindingID)
	}
	if bindingID == "" && s.sessionRoutes != nil {
		if route, ok, err := s.sessionRoutes.Get(sessionID); err != nil {
			return pebblestore.TopologyWorkspaceBindingRecord{}, false, err
		} else if ok && strings.TrimSpace(route.AccountScopeID) == strings.TrimSpace(principal.AccountScopeID) {
			bindingID = strings.TrimSpace(route.WorkspaceBindingID)
		}
	}
	if bindingID == "" && s.sessions != nil {
		if session, ok, err := s.sessions.GetSession(sessionID); err != nil {
			return pebblestore.TopologyWorkspaceBindingRecord{}, false, err
		} else if ok && strings.TrimSpace(session.AccountScopeID) == strings.TrimSpace(principal.AccountScopeID) {
			bindingID = firstNonEmpty(
				strings.TrimSpace(fmt.Sprint(session.Metadata["swarm_routed_workspace_binding_id"])),
				strings.TrimSpace(fmt.Sprint(session.Metadata["swarm_managed_host_workspace_binding_id"])),
				strings.TrimSpace(fmt.Sprint(session.Metadata["local_workspace_binding_id"])),
			)
		}
	}
	if bindingID == "" {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, nil
	}
	binding, ok, err := s.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, bindingID)
	if err != nil || !ok {
		return pebblestore.TopologyWorkspaceBindingRecord{}, ok, err
	}
	return binding, true, nil
}
