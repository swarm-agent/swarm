package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

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
	if control {
		// The swarm_* worker tools' Swarm Control inputs: a worker made or
		// changed through Swarm Control is active at once, so these run
		// those tools' own handlers. A conversation never passes them
		// (manage_workers' ControlInputs; the run service ignores them).
		routed, handled, err := s.workerControlCall(ctx, p, held, declared.Action, args)
		if handled || err != nil {
			return routed, err
		}
	}
	runner, ok := s.runner.(workerCatalogRunner)
	if !ok {
		return nil, errors.New("worker service unavailable")
	}
	out, err := runner.ManageWorkers(ctx, p, who, agent, workerConversationArgs(declared, control, args))
	if err != nil {
		return nil, err
	}
	switch {
	case declared.Action == "list" && args["include_jobs"] == true:
		// swarm_list_workers' include_jobs: each worker's latest jobs, as
		// that tool lists them (recent_jobs), by worker id.
		listed, err := s.workerRouteCall(ctx, p, held, control, map[string]any{"include_jobs": true}, []string{"include_jobs"}, controlMCPListWorkerJobs)
		if err != nil {
			return nil, err
		}
		jobs := map[string]any{}
		workers, _ := controlMCPMap(listed)["workers"].([]map[string]any)
		for _, worker := range workers {
			if worker != nil {
				id := controlMCPString(worker, "id")
				if id == "" {
					id = controlMCPString(worker, "worker_id")
				}
				if id != "" {
					jobs[id] = worker["recent_jobs"]
				}
			}
		}
		return workerAnswerWith(out, map[string]any{"recent_jobs": jobs})
	}
	return json.RawMessage(out), nil
}

// workerControlRoutes are the Swarm Control inputs of manage_workers
// actions that run a swarm_* worker handler, and the inputs it takes.
var workerControlRoutes = []struct {
	action  string
	trigger []string
	inputs  []string
	run     func(*controlMCPCall, map[string]any) (any, error)
	fixed   map[string]any
}{
	// swarm_create_worker: a worker bound to one folder, with an optional
	// schedule, active at once.
	{"create", []string{"workspace_path", "project_id", "schedule", "scheduled_plan"}, []string{"name", "instructions", "description", "workspace_path", "project_id", "schedule", "scheduled_plan"}, controlMCPCreateWorker, nil},
	// swarm_deploy_worker: a preset or custom worker by role, or saving an
	// existing one by worker.
	{"create", []string{"preset", "role", "worker"}, []string{"preset", "worker", "model", "budget", "name", "agent", "role", "instructions", "description"}, controlMCPDeployWorker, nil},
	// swarm_update_worker: its custom agent, saved at once.
	{"update", []string{"agent"}, []string{"worker_id", "agent", "name", "description", "instructions"}, controlMCPUpdateWorker, nil},
	// swarm_manage_worker cancel_run.
	{"update", []string{"run_id"}, []string{"worker_id", "run_id"}, controlMCPManageWorker, map[string]any{"action": "cancel_run"}},
	// swarm_manage_worker enable_schedule and disable_schedule.
	{"update", []string{"schedule_id"}, []string{"worker_id", "schedule_id"}, controlMCPManageWorker, nil},
}

// workerControlCall runs the swarm_* worker handler a Swarm Control call's
// inputs name (workerControlRoutes); handled is false when none does. Inputs
// of two different handlers in one call are refused.
func (s *Server) workerControlCall(ctx context.Context, p identity.Principal, held []string, action string, args map[string]any) (any, bool, error) {
	matched := -1
	for i, route := range workerControlRoutes {
		if route.action != action {
			continue
		}
		for _, key := range route.trigger {
			if _, ok := args[key]; ok {
				if matched >= 0 && matched != i {
					return nil, true, controlMCPToolFailure("%s and %s are separate requests: give one", strings.Join(workerControlRoutes[matched].trigger, "/"), strings.Join(route.trigger, "/"))
				}
				matched = i
			}
		}
	}
	if matched < 0 {
		return nil, false, nil
	}
	route := workerControlRoutes[matched]
	call := map[string]any{}
	for key, value := range args {
		call[key] = value
	}
	for key, value := range route.fixed {
		call[key] = value
	}
	inputs := append([]string(nil), route.inputs...)
	if _, ok := args["schedule_id"]; ok && action == "update" {
		enabled, set := args["schedule_enabled"].(bool)
		if !set {
			return nil, true, controlMCPToolFailure("schedule_id needs schedule_enabled: true enables the schedule, false disables it")
		}
		call["action"] = "disable_schedule"
		if enabled {
			call["action"] = "enable_schedule"
		}
	}
	if _, ok := call["action"]; ok {
		inputs = append(inputs, "action")
	}
	out, err := s.workerRouteCall(ctx, p, held, true, call, inputs, route.run)
	return out, true, err
}

// workerConversationArgs drops an action's Swarm Control inputs from a
// conversation's call before the run service sees it.
func workerConversationArgs(declared tool.CatalogAction, control bool, args map[string]any) map[string]any {
	if control || len(declared.ControlInputs) == 0 {
		return args
	}
	out := make(map[string]any, len(args))
	for key, value := range args {
		out[key] = value
	}
	for _, key := range declared.ControlInputs {
		delete(out, key)
	}
	return out
}

// workerAnswerWith adds fields to the run service's worker answer (its data,
// when it is an envelope); the answer's own fields win.
func workerAnswerWith(out string, extra map[string]any) (any, error) {
	var answer map[string]any
	if err := json.Unmarshal([]byte(out), &answer); err != nil {
		return json.RawMessage(out), nil
	}
	target := answer
	if data, ok := answer["data"].(map[string]any); ok {
		target = data
	}
	for key, value := range extra {
		if _, own := target[key]; !own {
			target[key] = value
		}
	}
	return answer, nil
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
