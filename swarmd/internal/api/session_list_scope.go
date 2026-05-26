package api

import (
	"strings"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func listSessionsForCWD(sessionSvc *sessionruntime.Service, workspaceSvc *workspaceruntime.Service, principal identity.Principal, cwd string, limit int, exactPath bool) ([]pebblestore.SessionSnapshot, error) {
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
	return sessionSvc.ListSessionsForAccountScope(principal.AccountScopeID, scope.WorkspacePath, limit)
}
