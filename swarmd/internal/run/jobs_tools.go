package run

import (
	"context"
	"encoding/json"
	"errors"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

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
	service, ok := s.workflowLifecycle.(tool.JobsService)
	if !ok {
		return "", errors.New("jobs service unavailable")
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", err
	}
	out, err := service.ManageJobs(ctx, principal, sessionID, profile.Name, args)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(out)
	return string(data), err
}
