package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// workerCatalogScopes are the API scopes of the worker routes: what a
// manage_workers action's internal request carries, and what a linked home's
// link holds on POST /v3/worker-actions/{action}.
var workerCatalogScopes = []string{"automations:read", "automations:write"}

// workerCatalogRunner is the run service's manage_workers implementation
// (run.Service.ManageWorkers): the Orchestrator's worker actions, for a
// conversation or for a caller with none.
type workerCatalogRunner interface {
	ManageWorkers(ctx context.Context, p identity.Principal, sessionID, agent string, args map[string]any) (string, error)
}

// ManageWorkers is the one worker service behind the catalog's
// manage_workers: the Orchestrator (a session caller, Orchestrator only),
// Swarm Control (a caller whose key scopes ride in ctx,
// tool.ControlScopesFromContext) and a linked home (POST
// /v3/worker-actions/{action}) all run it. assign_task runs the
// swarm_assign_worker_task handler over the worker routes; every other
// action runs the run service's worker implementation (the Orchestrator's
// manage_workers). With machine in args the action runs on that linked
// machine's own ManageWorkers and its answer comes back as it came.
func (s *Server) ManageWorkers(ctx context.Context, p identity.Principal, who, agent string, args map[string]any) (any, error) {
	if !p.Valid() || s.sessions == nil {
		return nil, errors.New("manage_workers requires a session principal")
	}
	action := controlMCPString(args, "action")
	declared, ok := manageWorkersAction(action)
	if !ok {
		return nil, errors.New("unknown manage_workers action")
	}
	held, control := tool.ControlScopesFromContext(ctx)
	var current store.SessionSnapshot
	if control {
		// A Swarm Control or linked caller acts within its key: one of the
		// action's route scopes (the catalog gate checked it too).
		allowed := false
		for _, scope := range declared.APIScopes {
			allowed = allowed || nativeJobScopeHeld(held, scope)
		}
		if !allowed {
			return nil, controlMCPToolFailure("this connection lacks %s", declared.APIScopes[0])
		}
	} else {
		session, found, err := s.sessions.Store().GetSession(who)
		if err != nil {
			return nil, err
		}
		if !found || session.AccountScopeID != p.AccountScopeID || session.UserID != p.UserID {
			return nil, errors.New("manage_workers session principal mismatch")
		}
		if !agentruntime.IsOrchestratorAgentName(agent) {
			return nil, errors.New("Worker and automation management is exclusive to Swarm Orchestrator in Swarm mode")
		}
		current = session
	}
	if machine := controlMCPString(args, "machine"); machine != "" {
		// Linked work is the home Orchestrator's (or a Swarm Control
		// caller's); the linked work rule (off, and ask before a run starts
		// there) is the permission service's for a conversation and
		// CallFleetWork's off check for everyone.
		if !control {
			if task, _ := current.Metadata["task_id"].(string); task != "" {
				return nil, errors.New("linked work is only available to the home Orchestrator")
			}
			if task, _ := current.Metadata["project_task_id"].(string); task != "" {
				return nil, errors.New("linked work is only available to the home Orchestrator")
			}
		}
		body := map[string]any{}
		for key, value := range args {
			if key != "machine" && key != "action" {
				body[key] = value
			}
		}
		return s.CallFleetWork(ctx, p, machine, tool.ManageWorkersToolName, declared.Action, body)
	}
	if declared.Action == "assign_task" {
		return s.assignWorkerTask(ctx, p, held, control, args)
	}
	if declared.Action == "inspect" && controlMCPString(args, "run_id") != "" {
		// One run of the worker: swarm_get_worker's run read.
		return s.workerRouteCall(ctx, p, held, control, args, []string{"worker_id", "run_id"}, controlMCPGetWorker)
	}
	runner, ok := s.runner.(workerCatalogRunner)
	if !ok {
		return nil, errors.New("worker service unavailable")
	}
	out, err := runner.ManageWorkers(ctx, p, who, agent, args)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

// assignWorkerTask runs swarm_assign_worker_task's handler (POST
// /v3/workers/{id}/direct) on an internal request with the worker routes'
// scopes, narrowed to a Swarm Control caller's.
func (s *Server) assignWorkerTask(ctx context.Context, p identity.Principal, held []string, control bool, args map[string]any) (any, error) {
	return s.workerRouteCall(ctx, p, held, control, args, []string{"worker_id", "task", "idempotency_key"}, controlMCPAssignWorkerTask)
}

// workerRouteCall runs one swarm_* worker handler over the worker routes on
// an internal request with the worker routes' scopes, narrowed to a Swarm
// Control caller's, with only the named inputs.
func (s *Server) workerRouteCall(ctx context.Context, p identity.Principal, held []string, control bool, args map[string]any, inputs []string, handler func(*controlMCPCall, map[string]any) (any, error)) (any, error) {
	scopes := append([]string(nil), workerCatalogScopes...)
	if control {
		scopes = nativeJobScopesHeld(scopes, held)
	}
	req, err := http.NewRequestWithContext(identity.ContextWithPrincipal(ctx, p), http.MethodPost, "/mcp", nil)
	if err != nil {
		return nil, err
	}
	req = requestWithScopedToken(req, &store.ScopedTokenRecord{Scopes: scopes})
	c := &controlMCPCall{request: req, next: s.internalRouteHandler(), server: s}
	clean := map[string]any{}
	for _, key := range inputs {
		if value, ok := args[key]; ok {
			clean[key] = value
		}
	}
	return handler(c, clean)
}

// manageWorkersAction is the declared manage_workers action.
func manageWorkersAction(name string) (tool.CatalogAction, bool) {
	catalog, ok := tool.CatalogToolNamed(tool.ManageWorkersToolName)
	if !ok {
		return tool.CatalogAction{}, false
	}
	return catalog.Action(name)
}
