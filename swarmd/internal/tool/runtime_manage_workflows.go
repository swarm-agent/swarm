package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
)

type controlScopesContextKey struct{}

// controlScopes is a Swarm Control caller's key scopes, carried in context.
type controlScopes struct{ scopes []string }

// WithControlScopes marks ctx as a Swarm Control call whose key holds
// scopes: services bound the caller to them (ControlScopesFromContext).
func WithControlScopes(ctx context.Context, scopes []string) context.Context {
	return context.WithValue(ctx, controlScopesContextKey{}, controlScopes{scopes: append([]string{}, scopes...)})
}

// ControlScopesFromContext returns a Swarm Control caller's key scopes; ok
// is false for any other caller (the Orchestrator, a route).
func ControlScopesFromContext(ctx context.Context) ([]string, bool) {
	if ctx == nil {
		return nil, false
	}
	value, ok := ctx.Value(controlScopesContextKey{}).(controlScopes)
	if !ok {
		return nil, false
	}
	return append([]string{}, value.scopes...), true
}

// controlScopeHeld reports whether a Swarm Control caller's key holds scope.
func controlScopeHeld(scope WorkspaceScope, want string) bool {
	for _, held := range scope.ControlScopes {
		if held == want {
			return true
		}
	}
	return false
}

// SetWorkflowLifecycleService wires the shared workflow authority (the API
// server) that serves manage_workflows for the Orchestrator and Swarm
// Control. Without it manage_workflows refuses every call.
func (r *Runtime) SetWorkflowLifecycleService(service WorkflowLifecycleService) {
	if r == nil {
		return
	}
	r.workflowLifecycle = service
}

func (r *Runtime) getWorkflowLifecycleService() WorkflowLifecycleService {
	if r == nil {
		return nil
	}
	return r.workflowLifecycle
}

// executeManageWorkflows runs one manage_workflows action and returns its
// answer in the catalog envelope, for the Orchestrator and Swarm Control
// alike: one handler, one service function (WorkflowLifecycleService), and
// with machine= the linked machine's workflow routes (FleetWork).
func (r *Runtime) executeManageWorkflows(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	catalog := catalogDeclared(manageWorkflowsToolName)
	name := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	declared, ok := catalog.Action(name)
	if !ok {
		return "", errors.New("unknown manage_workflows action; use help")
	}
	p := scope.Principal
	if !p.Valid() || strings.TrimSpace(p.AccountScopeID) == "" || strings.TrimSpace(p.UserID) == "" || p.Type != identity.PrincipalTypeUser {
		return "", errors.New("manage_workflows requires an authenticated user identity")
	}
	service := r.getWorkflowLifecycleService()
	if service == nil {
		return "", errors.New("workflow service unavailable")
	}
	if rotate, _ := args["rotate_key"].(bool); rotate {
		return "", errors.New("webhook key rotation is not supported for native workflow proposals; use the person's HTTP save or MCP path")
	}
	// The caller is named where a session would be: the Orchestrator's
	// conversation, or the Swarm Control caller's AI key.
	who := strings.TrimSpace(scope.SessionID)
	if scope.ControlCaller {
		who = strings.TrimSpace(scope.ControlSender)
	} else {
		if r.sessions == nil {
			return "", errors.New("manage_workflows requires the session service")
		}
		if _, err := r.homeConversation(scope, "workflow session ownership mismatch", "workflow management is only available to the home's Orchestrator"); err != nil {
			return "", err
		}
	}
	if who == "" {
		return "", errors.New("manage_workflows requires a session or an AI key")
	}
	call := make(map[string]any, len(args))
	for key, value := range args {
		call[key] = value
	}
	call["action"] = declared.Action
	// export reads at automations:read, but writing its file is a change:
	// a Swarm Control key needs automations:write for path, as on the
	// export route, here and on a linked machine (whose route sees the
	// link's credentials, not this key).
	if _, writes := args["path"]; writes && declared.Action == "export" && scope.ControlCaller && !controlScopeHeld(scope, "automations:write") {
		return "", errors.New("manage_workflows export with path writes a file and needs automations:write; this key does not hold it")
	}
	if machine := strings.TrimSpace(asString(args["machine"])); machine != "" {
		if declared.Reach == CatalogReachHome {
			return "", fmt.Errorf("manage_workflows %s runs on this machine only; omit machine", declared.Action)
		}
		fleet, ok := service.(FleetWork)
		if !ok {
			return "", errors.New("linked work service unavailable")
		}
		raw, err := fleet.CallFleetWork(ctx, p, machine, manageWorkflowsToolName, declared.Action, call)
		if err != nil {
			return "", err
		}
		answer, err := json.Marshal(map[string]any{"data": raw})
		if err != nil {
			return "", err
		}
		return r.catalogEnvelope(catalog, declared.Action, call, string(answer), true)
	}
	delete(call, "machine")
	out, err := service.ManageWorkflows(ctx, p, who, call)
	if err != nil {
		return "", err
	}
	answer, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return r.catalogEnvelope(catalog, declared.Action, call, string(answer), false)
}
