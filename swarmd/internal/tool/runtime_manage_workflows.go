package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
)

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
