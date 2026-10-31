package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
)

// CatalogActionService runs a catalog action whose one implementation is the
// handler of the swarm_* tool with the same effect (the API server's
// ManageCatalogAction): manage_fleet's status and activity on this machine,
// manage_media's read, manage_themes, manage_designs and manage_projects'
// get_task_changes and read_task_file. who names the caller: the calling
// session, or the Swarm Control caller's AI key ("key:ID"), whose scopes ride
// in ctx (ControlScopesFromContext). With machine in args it runs the action
// on that linked machine's own service and returns the machine's answer as
// it came.
type CatalogActionService interface {
	ManageCatalogAction(ctx context.Context, p identity.Principal, who, toolName string, args map[string]any) (any, error)
}

// catalogServiceCaller is the caller CatalogActionService runs an action for:
// the calling session, or the Swarm Control caller's key.
func catalogServiceCaller(scope WorkspaceScope, toolName string) (string, error) {
	p := scope.Principal
	if !p.Valid() || strings.TrimSpace(p.AccountScopeID) == "" || strings.TrimSpace(p.UserID) == "" || p.Type != identity.PrincipalTypeUser {
		return "", fmt.Errorf("%s requires an authenticated user identity", toolName)
	}
	who := strings.TrimSpace(scope.SessionID)
	if scope.ControlCaller {
		who = strings.TrimSpace(scope.ControlSender)
	}
	if who == "" {
		return "", fmt.Errorf("%s requires a session or an AI key", toolName)
	}
	return who, nil
}

// catalogOrchestratorOnly refuses a conversation's call that is not the
// home Orchestrator's: the swarm_* handlers behind CatalogActionService read
// and change account-wide records (any project's media and task work) and
// start work on linked machines, which a worker or another agent must never
// do on its own. A Swarm Control caller is held by its key's levels instead.
func catalogOrchestratorOnly(ctx context.Context, scope WorkspaceScope, toolName, action string) error {
	if scope.ControlCaller {
		return nil
	}
	if !agentruntime.IsOrchestratorAgentName(CatalogAgentFromContext(ctx)) {
		return fmt.Errorf("%s %s is the home Orchestrator's: it reads or changes account-wide records, never an agent's or a worker's", toolName, action)
	}
	return nil
}

// catalogServiceAnswer runs one action through CatalogActionService and
// returns its answer as JSON: the answer itself here, or {"data": answer}
// from a linked machine (the envelope's data is then the machine's answer as
// it came).
func (r *Runtime) catalogServiceAnswer(ctx context.Context, scope WorkspaceScope, catalog CatalogTool, declared CatalogAction, args map[string]any) (string, error) {
	who, err := catalogServiceCaller(scope, catalog.Name)
	if err != nil {
		return "", err
	}
	if err := catalogOrchestratorOnly(ctx, scope, catalog.Name, declared.Action); err != nil {
		return "", err
	}
	service, ok := r.getWorkflowLifecycleService().(CatalogActionService)
	if !ok {
		return "", fmt.Errorf("%s service unavailable", catalog.Name)
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
		return "", fmt.Errorf("%s %s runs on this machine only; omit machine", catalog.Name, declared.Action)
	}
	out, err := service.ManageCatalogAction(ctx, scope.Principal, who, catalog.Name, call)
	if err != nil {
		return "", err
	}
	if machine != "" {
		raw, err := json.Marshal(map[string]any{"data": out})
		return string(raw), err
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}

// executeCatalogServiceTool runs one action of a catalog tool whose every
// action is served by CatalogActionService (manage_themes, manage_designs) and
// returns its answer in the catalog envelope: one handler for the
// Orchestrator and Swarm Control.
func (r *Runtime) executeCatalogServiceTool(ctx context.Context, scope WorkspaceScope, toolName string, args map[string]any) (string, error) {
	catalog := catalogDeclared(toolName)
	if catalog.Name == "" {
		return "", fmt.Errorf("%s is not a catalog tool", toolName)
	}
	name := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	declared, ok := catalog.Action(name)
	if !ok {
		return "", fmt.Errorf("unknown %s action %q; use one of: %s", toolName, name, strings.Join(catalogActionNames(catalog), ", "))
	}
	call := make(map[string]any, len(args))
	for key, value := range args {
		call[key] = value
	}
	call["action"] = declared.Action
	if strings.TrimSpace(asString(call["machine"])) == "" {
		delete(call, "machine")
	}
	answer, err := r.catalogServiceAnswer(ctx, scope, catalog, declared, call)
	if err != nil {
		return "", err
	}
	return r.catalogEnvelope(catalog, declared.Action, call, answer, strings.TrimSpace(asString(call["machine"])) != "")
}
