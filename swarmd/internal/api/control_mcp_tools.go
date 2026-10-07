package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Swarm Control tools. Each tool reaches V3 state only through
// controlMCPRouteAllowed routes with the caller's scoped token, so handler
// scope checks, ownership, idempotency, revision guards and permissions decide.
// Output is deliberately small: summaries, truncated text, no timestamps.

const (
	controlMCPMaxCheckpoints = 30
	controlMCPMaxListItems   = 50
	controlMCPMaxWaitSeconds = 45
	controlMCPMaxModels      = 40
	controlMCPMaxChildren    = 20
)

// Roles whose default model an AI client may change, mapped to the canonical
// agent-model-settings patch slot.
var controlMCPAgentModelRoles = map[string][2]string{
	"swarm":               {"swarm", "action"},
	"system-orchestrator": {"swarm", "plan"},
	"system-coder":        {"system_agents", "coder"},
	"system-finder":       {"system_agents", "finder"},
	"system-designer":     {"system_agents", "designer"},
	"system-compact":      {"system_agents", "compact"},
	"system-router":       {"system_agents", "router"},
}

var controlMCPSessionAgents = []string{"swarm", "system-orchestrator", "system-coder", "system-designer", "system-finder"}

var controlMCPSafeID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)

// Literal segments must match exactly; "*" matches one id segment. Worker
// acceptance, token minting, import/migrate, capability grants, credentials,
// permission policy (rules, bypass) and the account default model are absent
// on purpose: they are owner approval, credential and policy surfaces.
var controlMCPRoutes = []string{
	"GET v1/workspace/list",
	"GET v1/providers",
	"GET v1/model/catalog",
	"GET v1/agent-model-settings",
	"PATCH v1/agent-model-settings",
	"GET v3/sessions",
	"POST v3/sessions",
	"GET v3/sessions/*",
	"POST v3/sessions/*/messages",
	"POST v3/sessions/*/preference",
	"POST v3/sessions/*/run/stop",
	"POST v3/sessions/*/permissions/*/resolve",
	"POST v3/sessions/*/plans",
	"GET v3/sessions/*/plans/active",
	"POST v3/sessions/*/plan-mode/plans/*/start-automatic",
	"GET v3/projects",
	"POST v3/projects",
	"POST v3/projects/*/sessions",
	"GET v3/projects/*/tasks",
	"GET v3/workers",
	"POST v3/workers",
	"GET v3/workers/*",
	"PUT v3/workers/*",
	"GET v3/workers/*/summary",
	"POST v3/workers/*/activate",
	"POST v3/workers/*/automations",
	"POST v3/workers/*/automations/*/enable",
	"POST v3/workers/*/automations/*/disable",
	"POST v3/workers/*/pause",
	"POST v3/workers/*/resume",
	"POST v3/workers/*/archive",
	"POST v3/workers/*/delete",
	"POST v3/workers/*/direct",
	"GET v3/workers/*/runs",
	"GET v3/workers/*/runs/*",
	"POST v3/workers/*/runs/*/cancel",
	"GET v3/usage",
	"GET v3/usage/limits",
	"POST v3/usage/limits",
}

var controlMCPReservedIDs = map[string]bool{
	"active": true, "validate": true, "import": true, "migrate": true, "token": true,
	"accept": true, "deploy": true, "export": true, "execution": true,
}

func controlMCPRouteAllowed(method, path string) bool {
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, route := range controlMCPRoutes {
		parts := strings.SplitN(route, " ", 2)
		if parts[0] != method {
			continue
		}
		pattern := strings.Split(parts[1], "/")
		if len(pattern) != len(segments) {
			continue
		}
		match := true
		for i, want := range pattern {
			got := segments[i]
			if want == "*" {
				match = controlMCPSafeID.MatchString(got) && got != "." && got != ".." && !controlMCPReservedIDs[got]
			} else {
				match = got == want
			}
			if !match {
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func controlMCPID(args map[string]any, key string) (string, error) {
	id := controlMCPString(args, key)
	if !controlMCPSafeID.MatchString(id) || id == "." || id == ".." || controlMCPReservedIDs[id] {
		return "", controlMCPToolFailure("argument %q is not a valid id", key)
	}
	return id, nil
}

// requireTokenScope guards routes whose handlers do not check scopes
// themselves (workspace list, usage dashboard, usage limits).
func (c *controlMCPCall) requireTokenScope(scope string) error {
	record, ok := ScopedTokenFromRequest(c.request)
	if !ok || !record.HasScope(scope) {
		return controlMCPToolFailure("this connection lacks the %s permission", scope)
	}
	return nil
}

// ---- shared shapes -------------------------------------------------------

func controlMCPSessionSummary(session map[string]any) map[string]any {
	summary := controlMCPPick(session, "id", "title", "workspace_path", "message_count")
	if metadata := controlMCPMap(session["metadata"]); metadata != nil {
		if agent, ok := metadata["agent_name"]; ok {
			summary["agent"] = agent
		}
	}
	if lifecycle := controlMCPMap(session["lifecycle"]); lifecycle != nil {
		if active, _ := lifecycle["active"].(bool); active {
			summary["running"] = true
		}
		for _, key := range []string{"phase", "stop_reason", "error"} {
			if value, ok := lifecycle[key].(string); ok && value != "" {
				summary[key] = controlMCPTruncate(value, 200)
			}
		}
	}
	return summary
}

func controlMCPPermissionSummary(record map[string]any) map[string]any {
	summary := controlMCPPick(record, "id", "tool_name", "requirement")
	if args, ok := record["tool_arguments"].(string); ok {
		summary["tool_arguments"] = controlMCPTruncate(args, controlMCPArgumentLimit)
	}
	return summary
}

func controlMCPToolMessageSummary(message map[string]any, content string) (map[string]any, bool) {
	if role, _ := message["role"].(string); role != "tool" {
		return nil, false
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(content), &record); err != nil {
		return nil, false
	}
	summary := controlMCPPick(record, "tool_name", "status")
	if args, ok := record["arguments"].(string); ok {
		summary["arguments"] = controlMCPTruncate(args, 300)
	}
	if errText, ok := record["error"].(string); ok && strings.TrimSpace(errText) != "" {
		summary["error"] = controlMCPTruncate(errText, 300)
	}
	if output, ok := record["output"].(string); ok && strings.TrimSpace(output) != "" {
		summary["output"] = controlMCPTruncate(output, 300)
	}
	if len(summary) == 0 {
		return nil, false
	}
	return summary, true
}

func controlMCPRunIntentSummary(intent map[string]any) map[string]any {
	if intent == nil {
		return nil
	}
	return controlMCPPick(intent, "run_id", "status", "blocked_reason", "plan_id", "checkpoint_id")
}

// controlMCPPlanProgress reduces a plan snapshot to status and per-checkpoint
// state; reports, attempts and validation logs stay in Swarm.
func controlMCPPlanProgress(plan map[string]any) map[string]any {
	document := controlMCPMap(plan["document"])
	if document == nil {
		return nil
	}
	out := controlMCPPick(document, "id", "title", "active_checkpoint_id")
	if state := controlMCPMap(document["execution_state"]); state != nil {
		for key, value := range controlMCPPick(state, "status", "last_outcome") {
			out[key] = value
		}
	}
	checkpoints := []map[string]any{}
	for i, raw := range controlMCPList(document["checkpoints"]) {
		if i >= controlMCPMaxCheckpoints {
			break
		}
		if checkpoint := controlMCPMap(raw); checkpoint != nil {
			checkpoints = append(checkpoints, controlMCPPick(checkpoint, "id", "title", "status"))
		}
	}
	out["checkpoints"] = checkpoints
	return out
}

func controlMCPWorkerSummary(worker map[string]any) map[string]any {
	summary := controlMCPPick(worker, "id", "name", "lifecycle_state", "revision")
	if description, ok := worker["description"].(string); ok && description != "" {
		summary["description"] = controlMCPTruncate(description, 200)
	}
	return summary
}

func controlMCPWorkerRunSummary(run map[string]any) map[string]any {
	summary := controlMCPPick(run, "id", "status", "session_id", "request_source")
	if errText, ok := run["error"].(string); ok && errText != "" {
		summary["error"] = controlMCPTruncate(errText, 300)
	}
	deliverables := []any{}
	for i, raw := range controlMCPList(run["deliverables"]) {
		if i >= 10 {
			break
		}
		if item := controlMCPMap(raw); item != nil {
			deliverables = append(deliverables, controlMCPPick(item, "label", "path", "role"))
		}
	}
	if len(deliverables) > 0 {
		summary["deliverables"] = deliverables
	}
	return summary
}

func controlMCPScalars(source map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range source {
		switch value.(type) {
		case string, float64, bool:
			out[key] = value
		}
	}
	return out
}

// ---- schemas -------------------------------------------------------------

func controlMCPObject(required []string, properties map[string]any) map[string]any {
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func controlMCPStringProp(description string, min, max int) map[string]any {
	prop := map[string]any{"type": "string", "maxLength": max}
	if min > 0 {
		prop["minLength"] = min
	}
	if description != "" {
		prop["description"] = description
	}
	return prop
}

var (
	controlMCPReadOnly    = map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
	controlMCPWrites      = map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": true}
	controlMCPDestructive = map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": false}
)

func controlMCPWaitProp() map[string]any {
	return map[string]any{"type": "integer", "minimum": 0, "maximum": controlMCPMaxWaitSeconds, "description": "Wait up to this many seconds for the run to finish or ask for approval, then return the session's latest state. Default 0 (return at once)."}
}

func controlMCPScheduleSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Run scheduled_plan automatically: kind interval (interval_seconds, at least 60) or cron (5-field cron and an IANA timezone).",
		"required":    []string{"kind"},
		"properties": map[string]any{
			"kind":             map[string]any{"type": "string", "enum": []string{"interval", "cron"}},
			"interval_seconds": map[string]any{"type": "integer", "minimum": 60},
			"cron":             map[string]any{"type": "string", "maxLength": 100},
			"timezone":         map[string]any{"type": "string", "maxLength": 100},
		},
	}
}

func controlMCPPlanSchema() map[string]any {
	stringList := func(description string) map[string]any {
		return map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"type": "string", "maxLength": 1000}, "description": description}
	}
	return map[string]any{
		"type":        "object",
		"description": "A plan Swarm executes checkpoint by checkpoint without further approval.",
		"required":    []string{"goal", "checkpoints"},
		"properties": map[string]any{
			"title":       map[string]any{"type": "string", "maxLength": 200},
			"goal":        map[string]any{"type": "string", "maxLength": 2000, "description": "Outcome of the whole plan."},
			"context":     map[string]any{"type": "string", "maxLength": 4000, "description": "Background the agents need."},
			"constraints": stringList("Rules every checkpoint must respect."),
			"checkpoints": map[string]any{
				"type": "array", "minItems": 1, "maxItems": controlMCPMaxCheckpoints,
				"description": "Ordered steps. Each is run, verified against its acceptance criteria, then the next starts.",
				"items": map[string]any{
					"type": "object", "required": []string{"title", "acceptance_criteria"},
					"properties": map[string]any{
						"title":               map[string]any{"type": "string", "maxLength": 200},
						"objective":           map[string]any{"type": "string", "maxLength": 2000},
						"tasks":               stringList("Concrete work items."),
						"acceptance_criteria": stringList("Checkable conditions that mean this checkpoint is done."),
					},
				},
			},
		},
	}
}

func controlMCPTools() []controlMCPTool {
	sessionID := controlMCPStringProp("From list_sessions or start_session.", 1, 200)
	workerID := controlMCPStringProp("From list_workers.", 1, 200)
	agentProp := map[string]any{"type": "string", "enum": controlMCPSessionAgents, "description": "swarm (default) works in one workspace and delegates to sub-agents; system-orchestrator runs a project (needs project_id), turning work into tasks for agents and workers; system-coder, system-designer and system-finder work alone."}
	providerProp := controlMCPStringProp("Provider id from list_models.", 0, 100)
	modelProp := controlMCPStringProp("Model from list_models (needs provider).", 0, 200)
	thinkingProp := controlMCPStringProp("One of the model's thinking options from list_models.", 0, 50)
	return []controlMCPTool{
		{
			Name: "swarm_list_workspaces", Title: "List workspaces",
			Description: "Registered workspaces (repositories) that sessions and workers can use.",
			InputSchema: controlMCPObject(nil, map[string]any{}), Annotations: controlMCPReadOnly, call: controlMCPListWorkspaces,
		},
		{
			Name: "swarm_list_sessions", Title: "List sessions",
			Description: "Recent sessions with agent and running state.",
			InputSchema: controlMCPObject(nil, map[string]any{
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": controlMCPMaxListItems, "description": "Default 10."},
			}),
			Annotations: controlMCPReadOnly, call: controlMCPListSessions,
		},
		{
			Name: "swarm_get_session", Title: "Read a session",
			Description: "Session state, model, active run, plan progress, pending permission requests and questions, delegated child sessions or project tasks, and the last messages (truncated; tool results summarized).",
			InputSchema: controlMCPObject([]string{"session_id"}, map[string]any{
				"session_id":   sessionID,
				"messages":     map[string]any{"type": "integer", "minimum": 0, "maximum": 30, "description": "Recent messages to include. Default 5."},
				"wait_seconds": controlMCPWaitProp(),
			}),
			Annotations: controlMCPReadOnly, call: controlMCPGetSession,
		},
		{
			Name: "swarm_start_session", Title: "Start work in a new session",
			Description: "Create a session and start it with either a prompt or a plan (not both). Regular sessions need workspace_path; orchestrator sessions (agent system-orchestrator) need project_id instead. With a plan, Swarm executes every checkpoint automatically. Optionally pick the model; otherwise the role default from list_models applies.",
			InputSchema: controlMCPObject(nil, map[string]any{
				"workspace_path": controlMCPStringProp("Absolute path from list_workspaces (regular sessions).", 0, 4096),
				"project_id":     controlMCPStringProp("From list_projects (orchestrator sessions).", 0, 200),
				"prompt":         controlMCPStringProp("The task, for unplanned work.", 0, 100000),
				"plan":           controlMCPPlanSchema(),
				"agent":          agentProp,
				"title":          controlMCPStringProp("", 0, 200),
				"provider":       providerProp,
				"model":          modelProp,
				"thinking":       thinkingProp,
				"wait_seconds":   controlMCPWaitProp(),
			}),
			Annotations: controlMCPWrites, call: controlMCPStartSession,
		},
		{
			Name: "swarm_send_message", Title: "Message a session",
			Description: "Send a follow-up instruction to an existing session; it continues working asynchronously.",
			InputSchema: controlMCPObject([]string{"session_id", "content"}, map[string]any{
				"session_id":        sessionID,
				"content":           controlMCPStringProp("", 1, 100000),
				"client_request_id": controlMCPStringProp("Idempotency key; reuse when retrying.", 0, 200),
				"wait_seconds":      controlMCPWaitProp(),
			}),
			Annotations: controlMCPWrites, call: controlMCPSendMessage,
		},
		{
			Name: "swarm_run_plan", Title: "Run a plan in a session",
			Description: "Attach a plan to an existing idle session and execute it automatically.",
			InputSchema: controlMCPObject([]string{"session_id", "plan"}, map[string]any{"session_id": sessionID, "plan": controlMCPPlanSchema()}),
			Annotations: controlMCPWrites, call: controlMCPRunPlanTool,
		},
		{
			Name: "swarm_stop_run", Title: "Stop a session's run",
			Description: "Stop the session's active run.",
			InputSchema: controlMCPObject([]string{"session_id"}, map[string]any{"session_id": sessionID, "reason": controlMCPStringProp("", 0, 500)}),
			Annotations: controlMCPDestructive, call: controlMCPStopRun,
		},
		{
			Name: "swarm_resolve_permission", Title: "Approve, deny or answer one pending request",
			Description: "Resolve one pending permission from get_session, once. Read its tool_arguments first. For an agent question (tool ask_user) use allow_once with answer. No persistent rules.",
			InputSchema: controlMCPObject([]string{"session_id", "permission_id", "action"}, map[string]any{
				"session_id":    sessionID,
				"permission_id": controlMCPStringProp("", 1, 200),
				"action":        map[string]any{"type": "string", "enum": []string{"allow_once", "deny_once"}},
				"reason":        controlMCPStringProp("Shown to the agent.", 0, 500),
				"answer":        controlMCPStringProp("Reply to an ask_user question (allow_once only).", 0, 10000),
			}),
			Annotations: controlMCPDestructive, call: controlMCPResolvePermission,
		},
		{
			Name: "swarm_list_models", Title: "List models and role defaults",
			Description: "Connected providers with their models and thinking options, and the default model of each agent role.",
			InputSchema: controlMCPObject(nil, map[string]any{
				"provider": controlMCPStringProp("Only this provider.", 0, 100),
			}),
			Annotations: controlMCPReadOnly, call: controlMCPListModels,
		},
		{
			Name: "swarm_set_session_model", Title: "Change a session's model",
			Description: "Switch an existing session to another model and thinking level; applies from its next run.",
			InputSchema: controlMCPObject([]string{"session_id", "provider", "model"}, map[string]any{
				"session_id": sessionID,
				"provider":   providerProp,
				"model":      modelProp,
				"thinking":   thinkingProp,
			}),
			Annotations: controlMCPWrites, call: controlMCPSetSessionModel,
		},
		{
			Name: "swarm_set_agent_model", Title: "Change an agent role's default model",
			Description: "Set the account default model for one agent role (new sessions and delegated work use it).",
			InputSchema: controlMCPObject([]string{"role", "provider", "model"}, map[string]any{
				"role":     map[string]any{"type": "string", "enum": []string{"swarm", "system-orchestrator", "system-coder", "system-finder", "system-designer", "system-compact", "system-router"}},
				"provider": providerProp,
				"model":    modelProp,
				"thinking": thinkingProp,
			}),
			Annotations: controlMCPWrites, call: controlMCPSetAgentModel,
		},
		{
			Name: "swarm_list_projects", Title: "List projects",
			Description: "Projects group workspaces; workers run inside a project.",
			InputSchema: controlMCPObject(nil, map[string]any{}), Annotations: controlMCPReadOnly, call: controlMCPListProjects,
		},
		{
			Name: "swarm_create_project", Title: "Create a project",
			Description: "Group one or more registered workspaces into a project so workers can be assigned tasks there.",
			InputSchema: controlMCPObject([]string{"name", "workspace_paths"}, map[string]any{
				"name":            controlMCPStringProp("", 1, 200),
				"description":     controlMCPStringProp("", 0, 1000),
				"workspace_paths": map[string]any{"type": "array", "minItems": 1, "maxItems": 10, "items": map[string]any{"type": "string", "maxLength": 4096}, "description": "From list_workspaces; the first is the primary code workspace."},
			}),
			Annotations: controlMCPWrites, call: controlMCPCreateProject,
		},
		{
			Name: "swarm_list_workers", Title: "List workers",
			Description: "Durable workers (specialists that take tasks and run in the background) with lifecycle state.",
			InputSchema: controlMCPObject(nil, map[string]any{
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": controlMCPMaxListItems, "description": "Default 20."},
			}),
			Annotations: controlMCPReadOnly, call: controlMCPListWorkers,
		},
		{
			Name: "swarm_get_worker", Title: "Read a worker or one of its runs",
			Description: "Without run_id: the worker, its schedules, next scheduled run and 5 latest runs. With run_id: that run's status, session_id (read it with get_session), error and deliverables.",
			InputSchema: controlMCPObject([]string{"worker_id"}, map[string]any{"worker_id": workerID, "run_id": controlMCPStringProp("", 0, 200)}),
			Annotations: controlMCPReadOnly, call: controlMCPGetWorker,
		},
		{
			Name: "swarm_create_worker", Title: "Create a worker",
			Description: "Create a durable worker with standing instructions, bound to one workspace and ready for tasks. The workspace must belong to a project (or pass project_id). Optionally give it a schedule and the plan each scheduled run executes; the schedule starts enabled. Workers cannot be granted extra tool capabilities here.",
			InputSchema: controlMCPObject([]string{"name", "instructions", "workspace_path"}, map[string]any{
				"name":           controlMCPStringProp("", 1, 256),
				"instructions":   controlMCPStringProp("What this worker does and how; applies to every task.", 1, 20000),
				"description":    controlMCPStringProp("", 0, 1000),
				"workspace_path": controlMCPStringProp("Workspace it works in (list_workspaces).", 1, 4096),
				"project_id":     controlMCPStringProp("Needed only if the workspace is in several projects.", 0, 200),
				"schedule":       controlMCPScheduleSchema(),
				"scheduled_plan": controlMCPPlanSchema(),
			}),
			Annotations: controlMCPWrites, call: controlMCPCreateWorker,
		},
		{
			Name: "swarm_update_worker", Title: "Update a worker",
			Description: "Change a worker's name, description or instructions. Runs already started keep their revision.",
			InputSchema: controlMCPObject([]string{"worker_id"}, map[string]any{
				"worker_id":    workerID,
				"name":         controlMCPStringProp("", 0, 256),
				"description":  controlMCPStringProp("", 0, 1000),
				"instructions": controlMCPStringProp("", 0, 20000),
			}),
			Annotations: controlMCPWrites, call: controlMCPUpdateWorker,
		},
		{
			Name: "swarm_manage_worker", Title: "Pause, resume, archive or delete a worker, cancel a run, or switch a schedule",
			Description: "Lifecycle control. cancel_run needs run_id; enable_schedule and disable_schedule need schedule_id (from get_worker).",
			InputSchema: controlMCPObject([]string{"worker_id", "action"}, map[string]any{
				"worker_id":   workerID,
				"action":      map[string]any{"type": "string", "enum": []string{"pause", "resume", "archive", "delete", "cancel_run", "enable_schedule", "disable_schedule"}},
				"run_id":      controlMCPStringProp("", 0, 200),
				"schedule_id": controlMCPStringProp("", 0, 200),
			}),
			Annotations: controlMCPDestructive, call: controlMCPManageWorker,
		},
		{
			Name: "swarm_assign_worker_task", Title: "Give a worker a task",
			Description: "Start a run of the worker on this task. Returns the run; follow it with get_worker(run_id).",
			InputSchema: controlMCPObject([]string{"worker_id", "task"}, map[string]any{
				"worker_id":       workerID,
				"task":            controlMCPStringProp("", 1, 100000),
				"idempotency_key": controlMCPStringProp("Reuse when retrying the same task.", 0, 200),
			}),
			Annotations: controlMCPWrites, call: controlMCPAssignWorkerTask,
		},
		{
			Name: "swarm_get_usage", Title: "Read usage and limits",
			Description: "Model spend and tokens for a period, top models, and the account's daily limits.",
			InputSchema: controlMCPObject(nil, map[string]any{
				"period": map[string]any{"type": "string", "enum": []string{"today", "7d", "30d"}, "description": "Default today."},
			}),
			Annotations: controlMCPReadOnly, call: controlMCPGetUsage,
		},
		{
			Name: "swarm_set_usage_limits", Title: "Set daily usage limits",
			Description: "Set the account's daily spend/token limits (UTC day). Omitted fields keep their value. A limit already exceeded stops all running work immediately.",
			InputSchema: controlMCPObject(nil, map[string]any{
				"daily_cost_limit_usd": map[string]any{"type": "number", "minimum": 0},
				"daily_tokens_limit":   map[string]any{"type": "integer", "minimum": 0},
				"enabled":              map[string]any{"type": "boolean"},
			}),
			Annotations: controlMCPDestructive, call: controlMCPSetUsageLimits,
		},
	}
}

// ---- sessions ------------------------------------------------------------

func controlMCPSessionPath(sessionID string, tail ...string) string {
	return strings.Join(append([]string{"/v3/sessions", sessionID}, tail...), "/")
}

func controlMCPListWorkspaces(c *controlMCPCall, _ map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:read"); err != nil {
		return nil, err
	}
	response, err := c.dispatch(http.MethodGet, "/v1/workspace/list", url.Values{"limit": {"100"}}, nil)
	if err != nil {
		return nil, err
	}
	workspaces := []map[string]any{}
	for _, raw := range controlMCPList(response["workspaces"]) {
		if entry := controlMCPMap(raw); entry != nil {
			workspaces = append(workspaces, controlMCPPick(entry, "path", "workspace_name", "workspace_id", "is_git_repo"))
		}
	}
	return map[string]any{"workspaces": workspaces}, nil
}

func (c *controlMCPCall) workspaceID(path string) (string, error) {
	response, err := c.dispatch(http.MethodGet, "/v1/workspace/list", url.Values{"limit": {"500"}}, nil)
	if err != nil {
		return "", err
	}
	for _, raw := range controlMCPList(response["workspaces"]) {
		entry := controlMCPMap(raw)
		if entry != nil && entry["path"] == path {
			if id, ok := entry["workspace_id"].(string); ok && id != "" {
				return id, nil
			}
		}
	}
	return "", controlMCPToolFailure("no registered workspace at %s (see list_workspaces)", path)
}

func controlMCPListSessions(c *controlMCPCall, args map[string]any) (any, error) {
	query := url.Values{"limit": {strconv.Itoa(controlMCPInt(args, "limit", 10))}}
	response, err := c.dispatch(http.MethodGet, "/v3/sessions", query, nil)
	if err != nil {
		return nil, err
	}
	sessions := []map[string]any{}
	for _, item := range controlMCPList(response["sessions"]) {
		if session := controlMCPMap(controlMCPMap(item)["session"]); session != nil {
			sessions = append(sessions, controlMCPSessionSummary(session))
		}
	}
	return map[string]any{"sessions": sessions}, nil
}

func controlMCPGetSession(c *controlMCPCall, args map[string]any) (any, error) {
	sessionID, err := controlMCPID(args, "session_id")
	if err != nil {
		return nil, err
	}
	settled, err := c.waitForSession(sessionID, controlMCPInt(args, "wait_seconds", 0))
	if err != nil {
		return nil, err
	}
	out, err := c.sessionState(sessionID, controlMCPInt(args, "messages", 5))
	if err != nil {
		return nil, err
	}
	if settled != nil {
		out["waited"] = settled
	}
	return out, nil
}

// sessionState is the get_session view: summary, model, run, pending
// requests, plan, delegated children and the latest messages.
func (c *controlMCPCall) sessionState(sessionID string, limit int) (map[string]any, error) {
	response, err := c.dispatch(http.MethodGet, controlMCPSessionPath(sessionID), url.Values{"message_limit": {strconv.Itoa(limit)}, "event_limit": {"0"}}, nil)
	if err != nil {
		return nil, err
	}
	session := controlMCPMap(response["session"])
	out := map[string]any{"session": controlMCPSessionSummary(session)}
	if preference := controlMCPPick(controlMCPMap(session["preference"]), "provider", "model", "thinking"); len(preference) > 0 {
		out["model"] = preference
	}
	if children := c.sessionChildren(sessionID, controlMCPMap(session["metadata"])); len(children) > 0 {
		out["children"] = children
	}
	if intent := controlMCPRunIntentSummary(controlMCPMap(response["active_run_intent"])); intent != nil {
		out["active_run"] = intent
	}
	pending := []map[string]any{}
	for _, record := range controlMCPList(response["pending_permissions"]) {
		if m := controlMCPMap(record); m != nil {
			pending = append(pending, controlMCPPermissionSummary(m))
		}
	}
	if len(pending) > 0 {
		out["pending_permissions"] = pending
	}
	// The session detail route does not populate the active plan; ask the
	// plan route directly (small response when there is none).
	if active, err := c.dispatch(http.MethodGet, controlMCPSessionPath(sessionID, "plans", "active"), nil, nil); err == nil {
		if hasActive, _ := active["has_active"].(bool); hasActive {
			if progress := controlMCPPlanProgress(controlMCPMap(active["active_plan"])); progress != nil {
				out["plan"] = progress
			}
		}
	}
	messages := []map[string]any{}
	for _, raw := range controlMCPList(response["messages"]) {
		message := controlMCPMap(raw)
		if message == nil {
			continue
		}
		summary := controlMCPPick(message, "role")
		content, _ := message["content"].(string)
		if tool, ok := controlMCPToolMessageSummary(message, content); ok {
			summary["tool"] = tool
		} else {
			summary["content"] = controlMCPTruncate(content, controlMCPTextLimit)
		}
		messages = append(messages, summary)
	}
	out["messages"] = messages
	return out, nil
}

// sessionChildren lists work a session delegated: project tasks for an
// orchestrator, otherwise sessions whose metadata names it as parent. Both
// reads go through the caller's own scoped routes; failures omit the field.
func (c *controlMCPCall) sessionChildren(sessionID string, metadata map[string]any) []map[string]any {
	children := []map[string]any{}
	projectID, _ := metadata["project_id"].(string)
	if role, _ := metadata["role"].(string); role == "project_orchestrator" && controlMCPSafeID.MatchString(projectID) && !controlMCPReservedIDs[projectID] {
		response, err := c.dispatch(http.MethodGet, "/v3/projects/"+projectID+"/tasks", nil, nil)
		if err != nil {
			return nil
		}
		for _, raw := range controlMCPList(response["tasks"]) {
			task := controlMCPMap(raw)
			if task == nil || task["origin_session_id"] != sessionID {
				continue
			}
			children = append(children, controlMCPPick(task, "id", "title", "status", "agent", "session_id", "worker_name"))
			if len(children) >= controlMCPMaxChildren {
				break
			}
		}
		return children
	}
	response, err := c.dispatch(http.MethodGet, "/v3/sessions", url.Values{"limit": {"100"}}, nil)
	if err != nil {
		return nil
	}
	for _, item := range controlMCPList(response["sessions"]) {
		child := controlMCPMap(controlMCPMap(item)["session"])
		if child == nil || controlMCPMap(child["metadata"])["parent_session_id"] != sessionID {
			continue
		}
		children = append(children, controlMCPSessionSummary(child))
		if len(children) >= controlMCPMaxChildren {
			break
		}
	}
	return children
}

// waitForSession blocks up to seconds until the session has no active run or
// has a pending request. It wakes on committed V3 outbox records (no timer
// polling) and re-reads state through dispatch, so ownership checks decide.
// It returns nil when no wait was requested.
func (c *controlMCPCall) waitForSession(sessionID string, seconds int) (map[string]any, error) {
	if seconds <= 0 {
		return nil, nil
	}
	if seconds > controlMCPMaxWaitSeconds {
		seconds = controlMCPMaxWaitSeconds
	}
	var hub *v3RealtimeOutboxHub
	if c.server != nil {
		hub = c.server.v3RealtimeOutbox
	}
	sub := hub.subscribe()
	defer hub.unsubscribe(sub)
	settled := func() (bool, error) {
		detail, err := c.dispatch(http.MethodGet, controlMCPSessionPath(sessionID), url.Values{"message_limit": {"0"}, "event_limit": {"0"}}, nil)
		if err != nil {
			return false, err
		}
		return controlMCPMap(detail["active_run_intent"]) == nil || len(controlMCPList(detail["pending_permissions"])) > 0, nil
	}
	done, err := settled()
	if err != nil || done || sub == nil {
		return map[string]any{"settled": done}, err
	}
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	for {
		select {
		case record := <-sub.send:
			if record.SessionID != sessionID {
				continue
			}
			if done, err = settled(); err != nil || done {
				return map[string]any{"settled": done}, err
			}
		case <-sub.slow:
			done, err = settled()
			return map[string]any{"settled": done}, err
		case <-timer.C:
			return map[string]any{"settled": false, "timed_out": true}, nil
		case <-c.request.Context().Done():
			return map[string]any{"settled": false}, nil
		}
	}
}

// modelPreference validates an optional provider/model/thinking choice
// against the live catalog and returns the canonical preference, or nil when
// no model was given.
func (c *controlMCPCall) modelPreference(args map[string]any) (map[string]any, error) {
	provider, model, thinking := controlMCPString(args, "provider"), controlMCPString(args, "model"), controlMCPString(args, "thinking")
	if model == "" {
		if provider != "" || thinking != "" {
			return nil, controlMCPToolFailure("give model (with provider) to choose a model")
		}
		return nil, nil
	}
	if provider == "" {
		return nil, controlMCPToolFailure("model needs provider (see list_models)")
	}
	response, err := c.dispatch(http.MethodGet, "/v1/model/catalog", url.Values{"provider": {provider}, "model": {model}}, nil)
	if err != nil {
		return nil, controlMCPToolFailure("model %s/%s is not in the catalog (see list_models)", provider, model)
	}
	record := controlMCPMap(controlMCPMap(response["lookup"])["record"])
	if record == nil {
		return nil, controlMCPToolFailure("model %s/%s is not in the catalog (see list_models)", provider, model)
	}
	if thinking == "" {
		thinking, _ = record["default_thinking"].(string)
	} else {
		allowed := false
		for _, option := range controlMCPList(record["thinking_options"]) {
			allowed = allowed || option == thinking
		}
		if !allowed {
			return nil, controlMCPToolFailure("thinking %q is not offered by %s (see list_models)", thinking, model)
		}
	}
	preference := map[string]any{"provider": provider, "model": model}
	if thinking != "" {
		preference["thinking"] = thinking
	}
	return preference, nil
}

func controlMCPStartSession(c *controlMCPCall, args map[string]any) (any, error) {
	prompt := controlMCPString(args, "prompt")
	plan, hasPlan := args["plan"].(map[string]any)
	if (prompt == "") == !hasPlan {
		return nil, controlMCPToolFailure("give exactly one of prompt or plan")
	}
	var document *pebblestore.SessionPlanDocument
	if hasPlan {
		var err error
		if document, err = controlMCPPlanDocument(plan); err != nil {
			return nil, err
		}
	}
	agent := controlMCPString(args, "agent")
	if agent == "" {
		agent = "swarm"
	}
	preference, err := c.modelPreference(args)
	if err != nil {
		return nil, err
	}
	title := controlMCPString(args, "title")
	if title == "" && document != nil {
		title = document.Title
	}
	body := map[string]any{
		"client_request_id": controlMCPRequestID(),
		"agent_name":        agent,
		// Always auto: callers bring their own plan instead of plan mode.
		"mode": "auto",
	}
	if title != "" {
		body["title"] = title
	}
	if preference != nil {
		body["preference"] = preference
	}
	// Orchestrators run a project (server-stamped project identity, no
	// workspace fields); every other agent runs in one workspace.
	createPath := "/v3/sessions"
	if agent == "system-orchestrator" {
		if controlMCPString(args, "workspace_path") != "" {
			return nil, controlMCPToolFailure("orchestrator sessions take project_id, not workspace_path")
		}
		projectID, err := controlMCPID(args, "project_id")
		if err != nil {
			return nil, controlMCPToolFailure("orchestrator sessions need project_id (see list_projects)")
		}
		createPath = "/v3/projects/" + projectID + "/sessions"
	} else {
		if controlMCPString(args, "project_id") != "" {
			return nil, controlMCPToolFailure("project_id applies only to agent system-orchestrator")
		}
		workspacePath := controlMCPString(args, "workspace_path")
		if workspacePath == "" {
			return nil, controlMCPToolFailure("workspace_path is required (see list_workspaces)")
		}
		body["workspace_path"] = workspacePath
	}
	created, err := c.dispatch(http.MethodPost, createPath, nil, body)
	if err != nil {
		return nil, err
	}
	session := controlMCPMap(created["session"])
	sessionID, _ := session["id"].(string)
	out := map[string]any{"session": controlMCPSessionSummary(session)}
	if document != nil {
		started, err := c.runPlan(sessionID, document)
		if err != nil {
			return nil, controlMCPToolFailure("session %s created but the plan did not start: %s", sessionID, err.Error())
		}
		out["plan"] = started
		return c.withWait(out, sessionID, args)
	}
	run, err := c.sendMessage(sessionID, prompt, controlMCPRequestID())
	if err != nil {
		return nil, controlMCPToolFailure("session %s created but the prompt was not accepted: %s", sessionID, err.Error())
	}
	out["run"] = run
	return c.withWait(out, sessionID, args)
}

// withWait honours wait_seconds after work was started: it waits, then
// attaches the session's latest state (including the reply).
func (c *controlMCPCall) withWait(out map[string]any, sessionID string, args map[string]any) (any, error) {
	settled, err := c.waitForSession(sessionID, controlMCPInt(args, "wait_seconds", 0))
	if err != nil || settled == nil {
		return out, nil
	}
	state, err := c.sessionState(sessionID, 3)
	if err != nil {
		return out, nil
	}
	state["waited"] = settled
	out["latest"] = state
	return out, nil
}

func (c *controlMCPCall) sendMessage(sessionID, content, requestID string) (map[string]any, error) {
	response, err := c.dispatch(http.MethodPost, controlMCPSessionPath(sessionID, "messages"), nil, map[string]any{
		"client_request_id": requestID,
		"role":              "user",
		"content":           content,
	})
	if err != nil {
		return nil, err
	}
	run := controlMCPRunIntentSummary(controlMCPMap(response["run_intent"]))
	if run == nil {
		run = map[string]any{"accepted": true}
	}
	return run, nil
}

func controlMCPSendMessage(c *controlMCPCall, args map[string]any) (any, error) {
	sessionID, err := controlMCPID(args, "session_id")
	if err != nil {
		return nil, err
	}
	requestID := controlMCPString(args, "client_request_id")
	if requestID == "" {
		requestID = controlMCPRequestID()
	}
	run, err := c.sendMessage(sessionID, controlMCPString(args, "content"), requestID)
	if err != nil {
		return nil, err
	}
	return c.withWait(map[string]any{"session_id": sessionID, "client_request_id": requestID, "run": run}, sessionID, args)
}

func controlMCPSetSessionModel(c *controlMCPCall, args map[string]any) (any, error) {
	sessionID, err := controlMCPID(args, "session_id")
	if err != nil {
		return nil, err
	}
	preference, err := c.modelPreference(args)
	if err != nil {
		return nil, err
	}
	preference["client_request_id"] = controlMCPRequestID()
	response, err := c.dispatch(http.MethodPost, controlMCPSessionPath(sessionID, "preference"), nil, preference)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"session_id": sessionID}
	if applied := controlMCPPick(controlMCPMap(controlMCPMap(response["session"])["preference"]), "provider", "model", "thinking"); len(applied) > 0 {
		out["model"] = applied
	} else {
		delete(preference, "client_request_id")
		out["model"] = preference
	}
	return out, nil
}

// controlMCPPlanDocument turns the caller's plan into an executable Swarm plan
// document and validates it with the same strict rules as Swarm's executor.
func controlMCPPlanDocument(plan map[string]any) (*pebblestore.SessionPlanDocument, error) {
	strs := func(value any) []string {
		out := []string{}
		for _, item := range controlMCPList(value) {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	}
	text := func(m map[string]any, key string) string {
		value, _ := m[key].(string)
		return strings.TrimSpace(value)
	}
	planID := "plan_" + strings.TrimPrefix(controlMCPRequestID(), "mcp_")
	doc := &pebblestore.SessionPlanDocument{
		ID:    planID,
		Title: text(plan, "title"),
		Info:  pebblestore.SessionPlanInfo{Goal: text(plan, "goal"), Context: text(plan, "context"), Constraints: strs(plan["constraints"])},
	}
	if doc.Title == "" {
		doc.Title = controlMCPTruncate(doc.Info.Goal, 120)
	}
	rawCheckpoints := controlMCPList(plan["checkpoints"])
	if len(rawCheckpoints) == 0 || len(rawCheckpoints) > controlMCPMaxCheckpoints {
		return nil, controlMCPToolFailure("plan needs 1 to %d checkpoints", controlMCPMaxCheckpoints)
	}
	for i, raw := range rawCheckpoints {
		checkpoint := controlMCPMap(raw)
		if checkpoint == nil {
			return nil, controlMCPToolFailure("checkpoint %d must be an object", i+1)
		}
		doc.Checkpoints = append(doc.Checkpoints, pebblestore.SessionPlanCheckpoint{
			ID:                 fmt.Sprintf("cp%d", i+1),
			Order:              i + 1,
			Title:              text(checkpoint, "title"),
			Objective:          text(checkpoint, "objective"),
			Tasks:              strs(checkpoint["tasks"]),
			AcceptanceCriteria: strs(checkpoint["acceptance_criteria"]),
		})
	}
	if err := sessionruntime.ValidateExecutablePlanDocument(doc); err != nil {
		return nil, controlMCPToolFailure("plan is not executable: %s", err.Error())
	}
	return doc, nil
}

// runPlan attaches the plan to an auto-mode session and starts automatic
// execution: save the plan as active, then start-automatic.
func (c *controlMCPCall) runPlan(sessionID string, document *pebblestore.SessionPlanDocument) (map[string]any, error) {
	saved, err := c.dispatch(http.MethodPost, controlMCPSessionPath(sessionID, "plans"), nil, map[string]any{
		"id": document.ID, "title": document.Title, "document": document, "activate": true,
	})
	if err != nil {
		return nil, err
	}
	planID, _ := controlMCPMap(saved["plan"])["id"].(string)
	if planID == "" {
		planID = document.ID
	}
	started, err := c.dispatch(http.MethodPost, controlMCPSessionPath(sessionID, "plan-mode", "plans", planID, "start-automatic"), nil, map[string]any{})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"plan_id": planID, "checkpoints": len(document.Checkpoints)}
	if queued, ok := started["run_queued"].(bool); ok {
		out["run_queued"] = queued
	}
	if run := controlMCPRunIntentSummary(controlMCPMap(started["run_intent"])); run != nil {
		out["run"] = run
	}
	if summary := controlMCPMap(started["execution_summary"]); summary != nil {
		out["execution"] = controlMCPScalars(summary)
	}
	return out, nil
}

func controlMCPRunPlanTool(c *controlMCPCall, args map[string]any) (any, error) {
	sessionID, err := controlMCPID(args, "session_id")
	if err != nil {
		return nil, err
	}
	plan, _ := args["plan"].(map[string]any)
	document, err := controlMCPPlanDocument(plan)
	if err != nil {
		return nil, err
	}
	started, err := c.runPlan(sessionID, document)
	if err != nil {
		return nil, err
	}
	started["session_id"] = sessionID
	return started, nil
}

func controlMCPStopRun(c *controlMCPCall, args map[string]any) (any, error) {
	sessionID, err := controlMCPID(args, "session_id")
	if err != nil {
		return nil, err
	}
	detail, err := c.dispatch(http.MethodGet, controlMCPSessionPath(sessionID), url.Values{"message_limit": {"0"}, "event_limit": {"0"}}, nil)
	if err != nil {
		return nil, err
	}
	session := controlMCPMap(detail["session"])
	targetSwarmID, _ := controlMCPMap(session["metadata"])["swarm_v3_runtime_swarm_id"].(string)
	if strings.TrimSpace(targetSwarmID) == "" {
		return nil, controlMCPToolFailure("session has no runtime identity to stop")
	}
	runID, _ := controlMCPMap(detail["active_run_intent"])["run_id"].(string)
	if runID == "" {
		runID, _ = controlMCPMap(session["lifecycle"])["run_id"].(string)
	}
	if strings.TrimSpace(runID) == "" {
		return nil, controlMCPToolFailure("session has no active run")
	}
	body := map[string]any{"run_id": runID, "target_swarm_id": targetSwarmID}
	if reason := controlMCPString(args, "reason"); reason != "" {
		body["reason"] = reason
	}
	if _, err := c.dispatch(http.MethodPost, controlMCPSessionPath(sessionID, "run", "stop"), nil, body); err != nil {
		return nil, err
	}
	return map[string]any{"session_id": sessionID, "stopped": runID}, nil
}

func controlMCPResolvePermission(c *controlMCPCall, args map[string]any) (any, error) {
	sessionID, err := controlMCPID(args, "session_id")
	if err != nil {
		return nil, err
	}
	permissionID, err := controlMCPID(args, "permission_id")
	if err != nil {
		return nil, err
	}
	action := controlMCPString(args, "action")
	body := map[string]any{"action": action}
	if reason := controlMCPString(args, "reason"); reason != "" {
		body["reason"] = reason
	}
	// ask_user takes the permission message of an allow as its free-text reply.
	if answer := controlMCPString(args, "answer"); answer != "" {
		if action != "allow_once" {
			return nil, controlMCPToolFailure("answer needs action allow_once")
		}
		body["reason"] = answer
	}
	response, err := c.dispatch(http.MethodPost, controlMCPSessionPath(sessionID, "permissions", permissionID, "resolve"), nil, body)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"permission_id": permissionID}
	if record := controlMCPMap(response["permission"]); record != nil {
		out["status"] = record["status"]
	}
	return out, nil
}

// ---- models --------------------------------------------------------------

func controlMCPListModels(c *controlMCPCall, args map[string]any) (any, error) {
	// Provider, catalog and model-settings handlers check no scope.
	if err := c.requireTokenScope("sessions:read"); err != nil {
		return nil, err
	}
	only := controlMCPString(args, "provider")
	response, err := c.dispatch(http.MethodGet, "/v1/providers", nil, nil)
	if err != nil {
		return nil, err
	}
	providers := []map[string]any{}
	for _, raw := range controlMCPList(response["providers"]) {
		provider := controlMCPMap(raw)
		id, _ := provider["id"].(string)
		if runnable, _ := provider["runnable"].(bool); !runnable || id == "" || (only != "" && id != only) {
			continue
		}
		entry := controlMCPPick(provider, "id", "default_model", "default_thinking")
		catalog, err := c.dispatch(http.MethodGet, "/v1/model/catalog", url.Values{"provider": {id}, "limit": {strconv.Itoa(controlMCPMaxModels)}}, nil)
		if err == nil {
			models := []map[string]any{}
			for _, record := range controlMCPList(catalog["records"]) {
				if m := controlMCPPick(controlMCPMap(record), "model", "thinking_options", "default_thinking", "context_window"); len(m) > 0 {
					models = append(models, m)
				}
			}
			entry["models"] = models
		}
		providers = append(providers, entry)
	}
	out := map[string]any{"providers": providers}
	if settings, err := c.dispatch(http.MethodGet, "/v1/agent-model-settings", nil, nil); err == nil {
		assignments := controlMCPMap(settings["agent_model_settings"])
		roles := []map[string]any{}
		for _, raw := range controlMCPList(settings["roles"]) {
			role := controlMCPMap(raw)
			id, _ := role["id"].(string)
			slot, ok := controlMCPAgentModelRoles[id]
			if !ok {
				continue
			}
			entry := controlMCPPick(controlMCPMap(controlMCPMap(assignments[slot[0]])[slot[1]]), "provider", "model", "thinking")
			entry["role"] = id
			roles = append(roles, entry)
		}
		out["roles"] = roles
	}
	return out, nil
}

func controlMCPSetAgentModel(c *controlMCPCall, args map[string]any) (any, error) {
	// The model-settings handler checks no scope; changing account defaults
	// needs an explicit settings:write grant.
	if err := c.requireTokenScope("settings:write"); err != nil {
		return nil, err
	}
	role := controlMCPString(args, "role")
	slot, ok := controlMCPAgentModelRoles[role]
	if !ok {
		return nil, controlMCPToolFailure("unknown role %q", role)
	}
	preference, err := c.modelPreference(args)
	if err != nil {
		return nil, err
	}
	response, err := c.dispatch(http.MethodPatch, "/v1/agent-model-settings", nil, map[string]any{slot[0]: map[string]any{slot[1]: preference}})
	if err != nil {
		return nil, err
	}
	applied := controlMCPPick(controlMCPMap(controlMCPMap(controlMCPMap(response["agent_model_settings"])[slot[0]])[slot[1]]), "provider", "model", "thinking")
	return map[string]any{"role": role, "model": applied}, nil
}

// ---- projects ------------------------------------------------------------

func controlMCPListProjects(c *controlMCPCall, _ map[string]any) (any, error) {
	response, err := c.dispatch(http.MethodGet, "/v3/projects", url.Values{"limit": {"50"}}, nil)
	if err != nil {
		return nil, err
	}
	projects := []map[string]any{}
	for _, raw := range controlMCPList(response["projects"]) {
		project := controlMCPMap(raw)
		if project == nil {
			continue
		}
		summary := controlMCPPick(project, "id", "name")
		paths := []any{}
		for _, ws := range controlMCPList(project["workspaces"]) {
			if path, ok := controlMCPMap(ws)["path"]; ok {
				paths = append(paths, path)
			}
		}
		summary["workspaces"] = paths
		projects = append(projects, summary)
	}
	return map[string]any{"projects": projects}, nil
}

func controlMCPCreateProject(c *controlMCPCall, args map[string]any) (any, error) {
	refs := []map[string]any{}
	for i, raw := range controlMCPList(args["workspace_paths"]) {
		path, _ := raw.(string)
		id, err := c.workspaceID(strings.TrimSpace(path))
		if err != nil {
			return nil, err
		}
		role := "auxiliary"
		if i == 0 {
			role = "primary_code"
		}
		refs = append(refs, map[string]any{"workspace_id": id, "path": strings.TrimSpace(path), "role": role})
	}
	body := map[string]any{"name": controlMCPString(args, "name"), "workspaces": refs, "client_request_id": controlMCPRequestID()}
	if description := controlMCPString(args, "description"); description != "" {
		body["description"] = description
	}
	response, err := c.dispatch(http.MethodPost, "/v3/projects", nil, body)
	if err != nil {
		return nil, err
	}
	project := controlMCPMap(response["project"])
	if project == nil {
		project = response
	}
	return map[string]any{"project": controlMCPPick(project, "id", "name")}, nil
}

// ---- workers -------------------------------------------------------------

func controlMCPListWorkers(c *controlMCPCall, args map[string]any) (any, error) {
	response, err := c.dispatch(http.MethodGet, "/v3/workers", url.Values{"limit": {strconv.Itoa(controlMCPInt(args, "limit", 20))}}, nil)
	if err != nil {
		return nil, err
	}
	workers := []map[string]any{}
	for _, raw := range controlMCPList(response["workers"]) {
		if worker := controlMCPMap(raw); worker != nil {
			workers = append(workers, controlMCPWorkerSummary(worker))
		}
	}
	return map[string]any{"workers": workers}, nil
}

func (c *controlMCPCall) worker(workerID string) (map[string]any, error) {
	response, err := c.dispatch(http.MethodGet, "/v3/workers/"+workerID, nil, nil)
	if err != nil {
		return nil, err
	}
	worker := controlMCPMap(response["worker"])
	if worker == nil {
		return nil, controlMCPToolFailure("worker not found")
	}
	return worker, nil
}

func controlMCPGetWorker(c *controlMCPCall, args map[string]any) (any, error) {
	workerID, err := controlMCPID(args, "worker_id")
	if err != nil {
		return nil, err
	}
	if controlMCPString(args, "run_id") != "" {
		runID, err := controlMCPID(args, "run_id")
		if err != nil {
			return nil, err
		}
		response, err := c.dispatch(http.MethodGet, "/v3/workers/"+workerID+"/runs/"+runID, nil, nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"run": controlMCPWorkerRunSummary(controlMCPMap(response["run"]))}, nil
	}
	worker, err := c.worker(workerID)
	if err != nil {
		return nil, err
	}
	out := controlMCPWorkerSummary(worker)
	if instructions, ok := worker["instructions"].(string); ok {
		out["instructions"] = controlMCPTruncate(instructions, 500)
	}
	if bindings := controlMCPMap(worker["local_bindings"]); len(bindings) > 0 {
		out["workspace_bindings"] = bindings
	}
	schedules := []map[string]any{}
	for _, raw := range controlMCPList(worker["automations"]) {
		if automation := controlMCPMap(raw); automation != nil {
			schedule := controlMCPPick(automation, "id", "name", "activation_mode", "enabled")
			if spec := controlMCPPick(controlMCPMap(automation["schedule"]), "kind", "interval_seconds", "cron", "timezone"); len(spec) > 0 {
				schedule["schedule"] = spec
			}
			schedules = append(schedules, schedule)
		}
	}
	if len(schedules) > 0 {
		out["schedules"] = schedules
		if summary, err := c.dispatch(http.MethodGet, "/v3/workers/"+workerID+"/summary", nil, nil); err == nil {
			if next, ok := summary["next_scheduled_at"]; ok && next != nil {
				out["next_scheduled_at"] = next
			}
		}
	}
	if review := controlMCPMap(worker["pending_review"]); review != nil {
		out["pending_owner_review"] = true
	}
	if runs, err := c.dispatch(http.MethodGet, "/v3/workers/"+workerID+"/runs", url.Values{"limit": {"5"}}, nil); err == nil {
		recent := []map[string]any{}
		for _, raw := range controlMCPList(runs["runs"]) {
			if run := controlMCPMap(raw); run != nil {
				recent = append(recent, controlMCPWorkerRunSummary(run))
			}
		}
		out["recent_runs"] = recent
	}
	return out, nil
}

func controlMCPRevision(worker map[string]any) uint64 {
	value, _ := worker["revision"].(float64)
	return uint64(value)
}

// controlMCPAutomation builds the scheduled job a worker runs from the
// caller's schedule and plan; the store validates both again.
func controlMCPAutomation(args map[string]any) (map[string]any, error) {
	schedule, hasSchedule := args["schedule"].(map[string]any)
	plan, hasPlan := args["scheduled_plan"].(map[string]any)
	if !hasSchedule && !hasPlan {
		return nil, nil
	}
	if !hasSchedule || !hasPlan {
		return nil, controlMCPToolFailure("schedule and scheduled_plan go together")
	}
	document, err := controlMCPPlanDocument(plan)
	if err != nil {
		return nil, err
	}
	kind, _ := schedule["kind"].(string)
	spec := map[string]any{"kind": kind}
	switch kind {
	case "interval":
		seconds, _ := schedule["interval_seconds"].(float64)
		if seconds < 60 || seconds != float64(int64(seconds)) {
			return nil, controlMCPToolFailure("interval schedules need whole interval_seconds of at least 60")
		}
		spec["interval_seconds"] = int64(seconds)
	case "cron":
		expression, _ := schedule["cron"].(string)
		timezone, _ := schedule["timezone"].(string)
		if strings.TrimSpace(expression) == "" || strings.TrimSpace(timezone) == "" {
			return nil, controlMCPToolFailure("cron schedules need cron and timezone")
		}
		spec["cron"], spec["timezone"] = strings.TrimSpace(expression), strings.TrimSpace(timezone)
	default:
		return nil, controlMCPToolFailure("schedule kind must be interval or cron")
	}
	return map[string]any{"name": "Scheduled: " + controlMCPTruncate(document.Title, 80), "activation_mode": kind, "schedule": spec, "plan_document": document}, nil
}

func controlMCPCreateWorker(c *controlMCPCall, args map[string]any) (any, error) {
	automation, err := controlMCPAutomation(args)
	if err != nil {
		return nil, err
	}
	workspaceID, err := c.workspaceID(controlMCPString(args, "workspace_path"))
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"name":                   controlMCPString(args, "name"),
		"idempotency_key":        controlMCPRequestID(),
		"instructions":           controlMCPString(args, "instructions"),
		"workspace_requirements": []map[string]any{{"role": "primary", "required": true}},
	}
	if description := controlMCPString(args, "description"); description != "" {
		body["description"] = description
	}
	if controlMCPString(args, "project_id") != "" {
		projectID, err := controlMCPID(args, "project_id")
		if err != nil {
			return nil, err
		}
		body["metadata"] = map[string]any{"project_id": projectID}
	}
	created, err := c.dispatch(http.MethodPost, "/v3/workers", nil, body)
	if err != nil {
		return nil, err
	}
	worker := controlMCPMap(created["worker"])
	workerID, _ := worker["id"].(string)
	if !controlMCPSafeID.MatchString(workerID) {
		return nil, controlMCPToolFailure("worker created without a usable id")
	}
	// A schedule is attached while the worker is still pending so it is part
	// of what activation approves; later changes to an active worker are
	// staged for owner review.
	if automation != nil {
		attached, err := c.dispatch(http.MethodPost, "/v3/workers/"+workerID+"/automations", nil, map[string]any{
			"expected_worker_revision": controlMCPRevision(worker),
			"automation":               automation,
		})
		if err != nil {
			return nil, controlMCPToolFailure("worker %s created but its schedule was rejected: %s", workerID, err.Error())
		}
		worker = controlMCPMap(attached["worker"])
	}
	activated, err := c.dispatch(http.MethodPost, "/v3/workers/"+workerID+"/activate", nil, map[string]any{
		"expected_revision": controlMCPRevision(worker),
		"local_bindings":    map[string]string{"primary": workspaceID},
	})
	if err != nil {
		return nil, controlMCPToolFailure("worker %s created but not activated: %s", workerID, err.Error())
	}
	worker = controlMCPMap(activated["worker"])
	out := map[string]any{"worker": controlMCPWorkerSummary(worker)}
	if automation != nil {
		scheduleID := ""
		for _, raw := range controlMCPList(worker["automations"]) {
			if a := controlMCPMap(raw); a != nil && a["name"] == automation["name"] {
				scheduleID, _ = a["id"].(string)
			}
		}
		if !controlMCPSafeID.MatchString(scheduleID) {
			return nil, controlMCPToolFailure("worker %s activated but its schedule id is missing", workerID)
		}
		enabled, err := c.dispatch(http.MethodPost, "/v3/workers/"+workerID+"/automations/"+scheduleID+"/enable", nil, map[string]any{"expected_worker_revision": controlMCPRevision(worker)})
		if err != nil {
			return nil, controlMCPToolFailure("worker %s activated but schedule %s was not enabled: %s", workerID, scheduleID, err.Error())
		}
		out["worker"] = controlMCPWorkerSummary(controlMCPMap(enabled["worker"]))
		out["schedule_id"] = scheduleID
	}
	return out, nil
}

func controlMCPUpdateWorker(c *controlMCPCall, args map[string]any) (any, error) {
	workerID, err := controlMCPID(args, "worker_id")
	if err != nil {
		return nil, err
	}
	worker, err := c.worker(workerID)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"expected_revision": controlMCPRevision(worker)}
	for _, key := range []string{"name", "description", "instructions"} {
		if value := controlMCPString(args, key); value != "" {
			body[key] = value
		}
	}
	if len(body) == 1 {
		return nil, controlMCPToolFailure("nothing to update")
	}
	updated, err := c.dispatch(http.MethodPut, "/v3/workers/"+workerID, nil, body)
	if err != nil {
		return nil, err
	}
	return map[string]any{"worker": controlMCPWorkerSummary(controlMCPMap(updated["worker"]))}, nil
}

func controlMCPManageWorker(c *controlMCPCall, args map[string]any) (any, error) {
	workerID, err := controlMCPID(args, "worker_id")
	if err != nil {
		return nil, err
	}
	action := controlMCPString(args, "action")
	if action == "enable_schedule" || action == "disable_schedule" {
		scheduleID, err := controlMCPID(args, "schedule_id")
		if err != nil {
			return nil, err
		}
		worker, err := c.worker(workerID)
		if err != nil {
			return nil, err
		}
		response, err := c.dispatch(http.MethodPost, "/v3/workers/"+workerID+"/automations/"+scheduleID+"/"+strings.TrimSuffix(action, "_schedule"), nil, map[string]any{"expected_worker_revision": controlMCPRevision(worker)})
		if err != nil {
			return nil, err
		}
		return map[string]any{"worker": controlMCPWorkerSummary(controlMCPMap(response["worker"])), "schedule_id": scheduleID, "action": action}, nil
	}
	if action == "cancel_run" {
		runID, err := controlMCPID(args, "run_id")
		if err != nil {
			return nil, err
		}
		response, err := c.dispatch(http.MethodPost, "/v3/workers/"+workerID+"/runs/"+runID+"/cancel", nil, map[string]any{})
		if err != nil {
			return nil, err
		}
		return map[string]any{"run": controlMCPWorkerRunSummary(controlMCPMap(response["run"]))}, nil
	}
	worker, err := c.worker(workerID)
	if err != nil {
		return nil, err
	}
	response, err := c.dispatch(http.MethodPost, "/v3/workers/"+workerID+"/"+action, nil, map[string]any{"expected_revision": controlMCPRevision(worker)})
	if err != nil {
		return nil, err
	}
	if updated := controlMCPMap(response["worker"]); updated != nil {
		return map[string]any{"worker": controlMCPWorkerSummary(updated)}, nil
	}
	return map[string]any{"worker_id": workerID, "action": action, "done": true}, nil
}

func controlMCPAssignWorkerTask(c *controlMCPCall, args map[string]any) (any, error) {
	workerID, err := controlMCPID(args, "worker_id")
	if err != nil {
		return nil, err
	}
	key := controlMCPString(args, "idempotency_key")
	if key == "" {
		key = controlMCPRequestID()
	}
	response, err := c.dispatch(http.MethodPost, "/v3/workers/"+workerID+"/direct", nil, map[string]any{"prompt": controlMCPString(args, "task"), "idempotency_key": key})
	if err != nil {
		return nil, err
	}
	return map[string]any{"run": controlMCPWorkerRunSummary(controlMCPMap(response["run"])), "idempotency_key": key}, nil
}

// ---- usage ---------------------------------------------------------------

func controlMCPGetUsage(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:read"); err != nil {
		return nil, err
	}
	period := controlMCPString(args, "period")
	if period == "" {
		period = "today"
	}
	response, err := c.dispatch(http.MethodGet, "/v3/usage", url.Values{"time_range": {period}, "session_limit": {"1"}}, nil)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"period": period, "summary": controlMCPPick(controlMCPMap(response["summary"]), "total_cost_usd", "total_tokens", "input_tokens", "output_tokens", "cached_tokens", "total_turns", "active_sessions")}
	if limits := controlMCPMap(response["limits"]); limits != nil {
		out["limits"] = controlMCPPick(limits, "enabled", "daily_cost_limit_usd", "daily_tokens_limit", "today_cost_usd", "today_tokens", "limit_exceeded")
	}
	models := []map[string]any{}
	for i, raw := range controlMCPList(response["by_model"]) {
		if i >= 5 {
			break
		}
		if model := controlMCPMap(raw); model != nil {
			models = append(models, controlMCPPick(model, "provider", "model", "cost_usd", "total_tokens", "turns"))
		}
	}
	out["top_models"] = models
	return out, nil
}

func controlMCPSetUsageLimits(c *controlMCPCall, args map[string]any) (any, error) {
	// The limits handler checks no scope; raising or disabling a spend limit
	// needs an explicit usage:write grant.
	if err := c.requireTokenScope("usage:write"); err != nil {
		return nil, err
	}
	body := map[string]any{}
	for _, key := range []string{"daily_cost_limit_usd", "daily_tokens_limit", "enabled"} {
		if value, ok := args[key]; ok {
			body[key] = value
		}
	}
	if len(body) == 0 {
		return nil, controlMCPToolFailure("nothing to change")
	}
	response, err := c.dispatch(http.MethodPost, "/v3/usage/limits", nil, body)
	if err != nil {
		return nil, err
	}
	return map[string]any{"limits": controlMCPPick(controlMCPMap(response["limits"]), "enabled", "daily_cost_limit_usd", "daily_tokens_limit", "today_cost_usd", "today_tokens", "limit_exceeded")}, nil
}
