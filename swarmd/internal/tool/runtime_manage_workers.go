package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
)

// WorkersService is the one worker authority (the API server's
// ManageWorkers) behind manage_workers for the Orchestrator and Swarm
// Control. who names the caller: the calling session, or the Swarm Control
// caller's AI key; agent is the calling session's agent (empty for Swarm
// Control). With machine in args it runs the action on that linked machine
// and returns the machine's answer as it came.
type WorkersService interface {
	ManageWorkers(ctx context.Context, p identity.Principal, who, agent string, args map[string]any) (any, error)
}

// executeManageWorkers runs one manage_workers action and returns its answer
// in the catalog envelope, for the Orchestrator and Swarm Control alike: one
// handler, one service function (WorkersService.ManageWorkers).
func (r *Runtime) executeManageWorkers(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	catalog := catalogDeclared(manageWorkersToolName)
	name := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	declared, ok := catalog.Action(name)
	if !ok {
		return "", fmt.Errorf("unknown manage_workers action %q; use one of: %s", name, strings.Join(catalogActionNames(catalog), ", "))
	}
	p := scope.Principal
	if !p.Valid() || strings.TrimSpace(p.AccountScopeID) == "" || strings.TrimSpace(p.UserID) == "" || p.Type != identity.PrincipalTypeUser {
		return "", errors.New("manage_workers requires an authenticated user identity")
	}
	service, ok := r.getWorkflowLifecycleService().(WorkersService)
	if !ok {
		return "", errors.New("worker service unavailable")
	}
	who, agent := strings.TrimSpace(scope.SessionID), ""
	if scope.ControlCaller {
		who = strings.TrimSpace(scope.ControlSender)
	} else {
		agent = CatalogAgentFromContext(ctx)
		if agent == "" {
			return "", errors.New("manage_workers requires the calling agent")
		}
	}
	if who == "" {
		return "", errors.New("manage_workers requires a session or an AI key")
	}
	call := make(map[string]any, len(args))
	for key, value := range args {
		call[key] = value
	}
	call["action"] = declared.Action
	machine := strings.TrimSpace(asString(args["machine"]))
	if machine == "" {
		delete(call, "machine")
	} else if declared.Reach == CatalogReachHome {
		return "", fmt.Errorf("manage_workers %s runs on this machine only; omit machine", declared.Action)
	}
	out, err := service.ManageWorkers(ctx, p, who, agent, call)
	if err != nil {
		return "", err
	}
	if machine != "" {
		answer, err := json.Marshal(map[string]any{"data": out})
		if err != nil {
			return "", err
		}
		return r.catalogEnvelope(catalog, declared.Action, call, string(answer), true)
	}
	answer, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return r.catalogEnvelope(catalog, declared.Action, call, string(answer), false)
}
