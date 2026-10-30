package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
)

// SetupService is the one setup authority (the API server's ManageSetup)
// behind manage_setup for the Orchestrator and Swarm Control. who names the
// caller: the calling session, or the Swarm Control caller's AI key
// ("key:ID"); control reports a Swarm Control (or linked) caller, whose key
// scopes ride in ctx (ControlScopesFromContext). With machine in args it runs
// the action on that linked machine and returns the machine's answer as it
// came.
type SetupService interface {
	ManageSetup(ctx context.Context, p identity.Principal, who string, args map[string]any) (any, error)
}

// executeManageSetup runs one manage_setup action and returns its answer in
// the catalog envelope, for the Orchestrator and Swarm Control alike: one
// handler, one service function (SetupService.ManageSetup).
//
// A conversation's caller must be the Orchestrator, and a change runs only
// with the person's decision for this call (PersonDecisionFromContext, set
// by the run's person-decision gate); without it the change is refused and
// nothing runs. A Swarm Control caller's person approves in their own AI's
// chat; its key's level was checked before this runs.
func (r *Runtime) executeManageSetup(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	catalog := catalogDeclared(manageSetupToolName)
	name := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	declared, ok := catalog.Action(name)
	if !ok {
		return "", fmt.Errorf("unknown manage_setup action %q; use one of: %s", name, strings.Join(catalogActionNames(catalog), ", "))
	}
	p := scope.Principal
	if !p.Valid() || strings.TrimSpace(p.AccountScopeID) == "" || strings.TrimSpace(p.UserID) == "" || p.Type != identity.PrincipalTypeUser {
		return "", errors.New("manage_setup requires an authenticated user identity")
	}
	service, ok := r.getWorkflowLifecycleService().(SetupService)
	if !ok {
		return "", errors.New("setup service unavailable")
	}
	who := strings.TrimSpace(scope.SessionID)
	if scope.ControlCaller {
		who = strings.TrimSpace(scope.ControlSender)
	} else {
		if !agentruntime.IsOrchestratorAgentName(CatalogAgentFromContext(ctx)) {
			return "", errors.New("manage_setup is the Orchestrator's: machine setup is never an agent's or a worker's")
		}
		if declared.Level != CatalogLevelRead && !PersonDecisionFromContext(ctx) {
			return "", fmt.Errorf("manage_setup %s changes this machine's setup and needs the person's decision for this call; it was not given, so nothing changed", declared.Action)
		}
	}
	if who == "" {
		return "", errors.New("manage_setup requires a session or an AI key")
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
		return "", fmt.Errorf("manage_setup %s runs on this machine only: keys, secrets, permissions and provider sign-in are never managed remotely; omit machine", declared.Action)
	}
	out, err := service.ManageSetup(ctx, p, who, call)
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
