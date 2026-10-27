package run

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func (s *Service) executeFleetWork(current store.SessionSnapshot, name string, args map[string]any) (string, error) {
	if mapString(current.Metadata, "task_id") != "" || mapString(current.Metadata, "project_task_id") != "" {
		return "", errors.New("linked work is only available to the home Orchestrator")
	}
	service, ok := s.workflowLifecycle.(tool.FleetWork)
	if !ok {
		return "", errors.New("linked work service unavailable")
	}
	machine, action := strings.TrimSpace(mapString(args, "machine")), strings.TrimSpace(mapString(args, "action"))
	raw, err := service.CallFleetWork(context.Background(), identity.Principal{AccountScopeID: current.AccountScopeID, UserID: current.UserID, Type: identity.PrincipalTypeUser}, machine, name, action, args)
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(map[string]any{"machine": machine, "action": action, "data": raw, "untrusted_content": true})
	return string(out), err
}
