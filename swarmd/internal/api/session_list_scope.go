package api

import (
	"strings"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	topologyruntime "swarm/packages/swarmd/internal/topology"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func listSessionsForCWD(sessionSvc *sessionruntime.Service, workspaceSvc *workspaceruntime.Service, principal identity.Principal, cwd string, limit int, exactPath bool) ([]pebblestore.SessionSnapshot, error) {
	return listSessionsForCWDWithTopology(sessionSvc, workspaceSvc, nil, principal, cwd, limit, exactPath)
}

func listSessionsForCWDWithTopology(sessionSvc *sessionruntime.Service, workspaceSvc *workspaceruntime.Service, topologySvc *topologyruntime.Service, principal identity.Principal, cwd string, limit int, exactPath bool) ([]pebblestore.SessionSnapshot, error) {
	if workspaceSvc == nil {
		return sessionSvc.ListSessionsForAccountPath(principal.AccountScopeID, cwd, limit)
	}
	scope, err := workspaceSvc.ScopeForPathForPrincipal(principal, cwd)
	if err != nil {
		return nil, err
	}
	if exactPath || !scope.Matched || strings.TrimSpace(scope.WorkspacePath) == "" {
		path := strings.TrimSpace(cwd)
		if scope.Matched && strings.TrimSpace(scope.ResolvedPath) != "" {
			path = scope.ResolvedPath
		}
		return sessionSvc.ListSessionsForAccountPath(principal.AccountScopeID, path, limit)
	}
	workspaceID := strings.TrimSpace(scope.WorkspaceID)
	if topologySvc == nil || workspaceID == "" {
		return sessionSvc.ListSessionsForAccountScope(principal.AccountScopeID, scope.WorkspacePath, limit)
	}
	bindings, err := topologySvc.ListWorkspaceBindingsForAccount(principal.AccountScopeID, 100000)
	if err != nil {
		return nil, err
	}
	bindingIDs := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if strings.TrimSpace(binding.SourceWorkspaceID) != workspaceID {
			continue
		}
		if strings.TrimSpace(binding.State) != pebblestore.TopologyWorkspaceBindingStateBound {
			continue
		}
		if bindingID := strings.TrimSpace(binding.BindingID); bindingID != "" {
			bindingIDs = append(bindingIDs, bindingID)
		}
	}
	return sessionSvc.ListSessionsForAccountWorkspaceBindings(principal.AccountScopeID, workspaceID, bindingIDs, scope.WorkspacePath, limit)
}
