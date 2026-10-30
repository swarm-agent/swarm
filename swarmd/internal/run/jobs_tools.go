package run

import (
	"context"
	"errors"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// executeManageJobsTool runs a session's manage_jobs call through the catalog
// handler (tool.Runtime, the one Swarm Control runs), as the session's own
// principal and agent: its answer is the catalog envelope.
func (s *Service) executeManageJobsTool(ctx context.Context, principal identity.Principal, sessionID, arguments string, profile store.AgentProfile) (string, error) {
	current, found, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !found || !principal.Valid() || principal.AccountScopeID != current.AccountScopeID || principal.UserID != current.UserID {
		return "", errors.New("manage_jobs session principal mismatch")
	}
	if profile.ToolContract == nil {
		return "", errors.New("manage_jobs must be enabled in the agent tool contract")
	}
	enabled := profile.ToolContract.Tools["manage_jobs"].Enabled
	if enabled == nil || !*enabled {
		return "", errors.New("manage_jobs must be enabled in the agent tool contract")
	}
	if _, ok := s.workflowLifecycle.(tool.JobsService); !ok {
		return "", errors.New("jobs service unavailable")
	}
	if s.tools == nil {
		return "", errors.New("jobs service unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = tool.WithCatalogAgent(ctx, profile.Name)
	return s.tools.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: sessionID, Principal: principal}, tool.Call{Name: "manage_jobs", Arguments: arguments})
}
