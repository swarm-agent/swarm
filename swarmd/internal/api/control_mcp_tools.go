package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Swarm Control tools. Each tool reaches V3 state only through
// controlMCPRouteAllowed routes with the caller's scoped token, so handler
// scope checks, ownership, idempotency, revision guards and permissions decide.
// Output is deliberately small: summaries, truncated text, no timestamps.

const (
	controlMCPMaxCheckpoints = 30
	controlMCPMaxListItems   = 50
	// The longest wait one tool call may hold. Relay devices give up on a call
	// after 120 seconds (packages/swarm-relay DEVICE_TIMEOUT_MS), so 110 keeps
	// a margin over the relay; Claude Code's MCP tool timeout is far longer.
	// Clients with a 60-second default (MCP TypeScript SDK, Codex CLI) should
	// pass at most 50.
	controlMCPMaxWaitSeconds = 110
	// Per-message text and per tool output/argument limits get_session can be
	// asked to raise to (detail_chars).
	controlMCPMaxDetailChars  = 20000
	controlMCPToolOutputLimit = 300
	controlMCPMaxModels       = 40
	controlMCPMaxChildren     = 20
	// project.md as read by get_project and written by update_project_context.
	// Generated documents are at most 12,000 bytes; edits get headroom.
	controlMCPMaxProjectContextChars = 50000
)

// Roles whose default model an AI client may change, mapped to the canonical
// agent-model-settings patch slot.
var controlMCPAgentModelRoles = map[string][2]string{
	"swarm":               {"swarm", "action"},
	"system-orchestrator": {"swarm", "plan"},
	"system-coder":        {"system_agents", "coder"},
	"system-reviewer":     {"system_agents", "reviewer"},
	"system-finder":       {"system_agents", "finder"},
	"system-designer":     {"system_agents", "designer"},
	"system-compact":      {"system_agents", "compact"},
	"system-router":       {"system_agents", "router"},
}

// The roles in a fixed order (set_agent_model role all applies them in turn).
var controlMCPAgentModelRoleOrder = []string{"swarm", "system-orchestrator", "system-coder", "system-reviewer", "system-finder", "system-designer", "system-compact", "system-router"}

var controlMCPSessionAgents = []string{"swarm", "system-orchestrator", "system-coder", "system-designer", "system-finder"}

var controlMCPSafeID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)

// New workspace folder names: one plain path segment, never hidden or relative.
var controlMCPWorkspaceName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// Literal segments must match exactly; "*" matches one id segment. Worker
// acceptance, import/migrate, capability grants, credential reads and saves,
// permission policy (rules, bypass) and the account default model are absent
// on purpose: they are owner approval, credential and policy surfaces. Token
// minting and ChatGPT sign-in appear only for the box-setup tools, which fix
// the scopes and the sign-in method themselves.
var controlMCPRoutes = []string{
	"GET v3/agents", "GET v3/agents/*", "PUT v3/agents/*", "POST v3/agents/*/disable",
	"GET v3/workflows", "POST v3/workflows", "PUT v3/workflows", "DELETE v3/workflows",
	"GET v3/workflows/*", "DELETE v3/workflows/*",
	"POST v3/workflows/stop", "POST v3/workflows/restore", "POST v3/workflows/import", "POST v3/workflows/export",
	"POST v3/subagents:stop",
	"GET v3/limits", "PUT v3/limits",
	"GET v1/workspace/list",
	"POST v1/workspace/repository/setup",
	"POST v1/workspace/add",
	"GET v1/providers",
	"GET v1/model/catalog",
	"GET v1/agent-model-settings",
	"PATCH v1/agent-model-settings",
	"GET v1/ui/settings",
	"POST v1/ui/settings",
	"GET v3/sessions",
	"POST v3/sessions",
	"GET v3/sessions/*",
	// The chunk read of one long message (session_message_chunk.go);
	// swarm_get_session only ever asks it with message_id.
	"GET v3/sessions/*/messages",
	"POST v3/sessions/*/messages",
	"PUT v3/sessions/*/model-profile",
	"POST v3/sessions/*/run/stop",
	"POST v3/sessions/*/permissions/*/resolve",
	"POST v3/sessions/*/access",
	"GET v3/permissions",
	"PATCH v3/permissions",
	// Fleet keys (control_mcp_fleet_keys.go): list, ask for a key page link,
	// revoke. The machine-side install and remove routes are never listed.
	"GET v3/fleet/keys",
	"POST v3/fleet/keys/requests",
	"POST v3/fleet/keys/*/revoke",
	// Linked tasks (fleet_linked_tasks.go): the home's records of tasks sent
	// to linked machines; read only.
	"GET v3/fleet/linked-tasks",
	"POST v3/sessions/*/media",
	"POST v3/sessions/*/plans",
	"GET v3/sessions/*/plans/active",
	"POST v3/sessions/*/plan-mode/plans/*/start-automatic",
	"GET v3/projects",
	"POST v3/projects",
	// Project read, project.md and workspace edits, and project.md retry. The
	// project tools fix the PATCH body to project_context or workspaces.
	"GET v3/projects/*",
	"PATCH v3/projects/*",
	"POST v3/projects/*/context:retry",
	"POST v3/projects/*/sessions",
	"GET v3/projects/*/tasks",
	// One-hop delegation (control_mcp_tasks.go): create an auto-approved task
	// card and read one task with fresh git and delivery state.
	"POST v3/projects/*/tasks",
	"GET v3/projects/*/tasks/*",
	"POST v3/projects/*/tasks/*/integrate",
	// Task launch approval (control_mcp_review.go): the Desktop's Approve and
	// Reject buttons on a pending_approval task. The media acceptance alias
	// (/accept), which publishes deliverables, stays unreachable.
	"POST v3/projects/*/tasks/*/approve",
	"POST v3/projects/*/tasks/*/reject",
	// Design jobs (control_mcp_design.go): make or edit standalone HTML designs
	// in a project folder, and stop one.
	"POST v3/projects/*/design-jobs",
	"POST v3/projects/*/design-jobs/*/cancel",
	// Closing a plan review: the Desktop's accept on a needs_review checkpoint.
	"POST v3/sessions/*/plan-mode/checkpoints/*/accept",
	"GET v3/workers",
	"POST v3/workers",
	// The preset coder, designer and finder (created when missing, never
	// overwritten) for the job tools (control_mcp_jobs.go).
	"POST v3/workers:ensure-presets",
	"GET v3/workers/*",
	"PUT v3/workers/*",
	"GET v3/workers/*/summary",
	"POST v3/workers/*/activate",
	"PUT v3/workers/*/model",
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
	"PUT v3/usage/worker-budget",
	"GET v1/codex/account/usage", "GET v1/claudecode/usage",
	"GET v3/usage/limits",
	"POST v3/usage/limits",
	// Monitoring: the machine summary and its signal feed (handlers require
	// signals:read), and which secrets exist where (secrets:read; never values).
	"GET v3/box/summary",
	"POST v3/box/drain",
	"DELETE v3/box/drain",
	"GET v3/box/drain",
	// Runs across every worker (swarm_list_runs; signals:read).
	"GET v3/runs",
	// Work waiting on a provider's usage limit retries now, once the provider
	// was fixed (manage_jobs retry_now; sessions:write, limit_retry.go).
	"POST v3/provider-limit-waits/retry",
	// The agents at work here and on linked machines (swarm_agents_at_work;
	// sessions:read, the same service as the console's read).
	"GET v3/fleet/agents",
	"GET v3/signals",
	"GET v3/activity",
	"GET v3/secrets/status",
	// A folder's sandbox environment (sandbox_environments.go): read, define
	// (Swarm builds it under gVisor) or clear.
	"GET v3/sandbox-environments",
	"PUT v3/sandbox-environments",
	"PATCH v3/sandbox-environments",
	"POST v3/sandbox-environments/rebuild",
	"GET v3/sandbox-environments/list",
	"DELETE v3/sandbox-environments",
	// Box setup (control_mcp_builders.go). Each tool fixes its body: custom
	// sub-agents only, client keys with sessions scopes only, ChatGPT device
	// sign-in only (no callback completion, no credential reads).
	"GET v2/agents",
	"GET v2/agents/*",
	"PUT v2/agents/*",
	"POST v3/auth/tokens",
	"POST v1/auth/codex/oauth/start",
	"GET v1/auth/codex/oauth/status",
	// Claude Code activation only: a fixed cli record with no secret
	// (control_mcp_claude_code.go).
	"POST v1/auth/credentials",
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
	// When Swarm made, last changed and last messaged it (Unix ms), from the
	// session's own record, so frontends order and age it without guessing.
	for _, key := range []string{"created_at", "updated_at", "last_message_at"} {
		if value, ok := session[key].(float64); ok && value > 0 {
			summary[key] = value
		}
	}
	if metadata := controlMCPMap(session["metadata"]); metadata != nil {
		if agent, ok := metadata["agent_name"]; ok {
			summary["agent"] = agent
		}
		// What the person lets the session's agent do (its shield).
		if access := pebblestore.SessionAccessFromMetadata(metadata); !access.Empty() {
			summary["access"] = access
		}
		// A project's sessions (its orchestrator conversations and task
		// sessions) name the project and their role in it, so frontends can
		// list a project's conversations.
		for _, key := range []string{"project_id", "role"} {
			if value, ok := metadata[key].(string); ok && value != "" {
				summary[key] = value
			}
		}
	}
	if lifecycle := controlMCPMap(session["lifecycle"]); lifecycle != nil {
		if active, _ := lifecycle["active"].(bool); active {
			summary["running"] = true
		}
		for _, key := range []string{"started_at", "ended_at"} {
			if value, ok := lifecycle[key].(float64); ok && value > 0 {
				summary["run_"+key] = value
			}
		}
		for _, key := range []string{"phase", "stop_reason", "error"} {
			if value, ok := lifecycle[key].(string); ok && value != "" {
				summary[key] = controlMCPTruncate(value, 200)
			}
		}
	}
	return summary
}

// controlMCPToolName is the canonical (underscore) spelling of a tool name, so
// a pending ask-user request reads tool_name ask_user like its requirement.
func controlMCPToolName(name string) string {
	return strings.ReplaceAll(strings.TrimSpace(name), "-", "_")
}

// controlMCPArguments decodes JSON-encoded tool arguments into an object for
// the caller, truncating long strings and lists so the result stays bounded.
// Text that is not a JSON object or array is returned truncated, as text.
func controlMCPArguments(raw string, limit int) any {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var parsed any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
			return controlMCPBoundJSON(parsed, limit, 0)
		}
	}
	return controlMCPTruncate(raw, limit)
}

func controlMCPBoundJSON(value any, limit, depth int) any {
	switch v := value.(type) {
	case string:
		return controlMCPTruncate(v, limit)
	case []any:
		if depth >= 6 {
			return "[nested list omitted]"
		}
		out := make([]any, 0, min(len(v), controlMCPMaxListItems))
		for i, item := range v {
			if i >= controlMCPMaxListItems {
				out = append(out, fmt.Sprintf("[%d more items omitted]", len(v)-i))
				break
			}
			out = append(out, controlMCPBoundJSON(item, limit, depth+1))
		}
		return out
	case map[string]any:
		if depth >= 6 {
			return "[nested object omitted]"
		}
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = controlMCPBoundJSON(item, limit, depth+1)
		}
		return out
	default:
		return v
	}
}

func controlMCPPermissionSummary(record map[string]any) map[string]any {
	summary := controlMCPPick(record, "id", "tool_name", "requirement")
	if name, ok := summary["tool_name"].(string); ok {
		summary["tool_name"] = controlMCPToolName(name)
	}
	switch {
	case summary["tool_name"] == "ask_user" || summary["requirement"] == "ask_user":
		summary["kind"] = "question"
	case summary["requirement"] == permission.RequirementClientTool:
		// A call the client runs (answered with result): it needs every
		// argument exactly, within the client tool argument limit.
		summary["kind"] = "client"
	case summary["requirement"] == permission.RequirementProposalDecision:
		// A workflow or worker proposal: only the person decides it, and a
		// decision anywhere settles the request with its outcome.
		summary["kind"] = "proposal"
		summary["answered_by"] = "person"
	default:
		summary["kind"] = "approval"
	}
	if args, ok := record["tool_arguments"].(string); ok && strings.TrimSpace(args) != "" {
		if summary["kind"] == "client" {
			summary["tool_arguments"] = controlMCPArguments(args, agentruntime.ClientToolArgumentsMaxBytes)
			return summary
		}
		summary["tool_arguments"] = controlMCPArguments(args, controlMCPArgumentLimit)
	}
	return summary
}

func controlMCPToolMessageSummary(message map[string]any, content string) (map[string]any, bool) {
	return controlMCPToolMessageDetail(message, content, controlMCPToolOutputLimit)
}

// controlMCPToolMessageDetail is controlMCPToolMessageSummary with an explicit
// per-field limit (get_session detail_chars).
func controlMCPToolMessageDetail(message map[string]any, content string, limit int) (map[string]any, bool) {
	if role, _ := message["role"].(string); role != "tool" {
		return nil, false
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(content), &record); err != nil {
		return nil, false
	}
	summary := controlMCPPick(record, "tool_name", "status")
	if name, ok := summary["tool_name"].(string); ok {
		summary["tool_name"] = controlMCPToolName(name)
	}
	if args, ok := record["arguments"].(string); ok {
		summary["arguments"] = controlMCPArguments(args, limit)
	}
	if errText, ok := record["error"].(string); ok && strings.TrimSpace(errText) != "" {
		summary["error"] = controlMCPTruncate(errText, limit)
	}
	if output, ok := record["output"].(string); ok && strings.TrimSpace(output) != "" {
		summary["output"] = controlMCPTruncate(output, limit)
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
	summary := controlMCPPick(intent, "run_id", "status", "blocked_reason", "plan_id", "checkpoint_id")
	// The run's times (Unix ms): time so far is now - started_at, and
	// cumulative_duration_ms is the session's earlier runs.
	for _, key := range []string{"created_at", "started_at", "cumulative_duration_ms"} {
		if value, ok := intent[key].(float64); ok && value > 0 {
			summary[key] = value
		}
	}
	return summary
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

// ---- tool annotations ------------------------------------------------------

var (
	controlMCPReadOnly = map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
	controlMCPWrites   = map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": true}
)

// ---- sessions ------------------------------------------------------------

func controlMCPSessionPath(sessionID string, tail ...string) string {
	return strings.Join(append([]string{"/v3/sessions", sessionID}, tail...), "/")
}

// controlMCPCreateWorkspace creates <root>/<name> as an empty repository with
// one initial commit and registers it, through the same setup and add routes
// the app uses. The root is the daemon's startup directory, never an argument,
// so a caller cannot choose where on the machine folders appear.
func controlMCPCreateWorkspace(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:write"); err != nil {
		return nil, err
	}
	name := controlMCPString(args, "name")
	if !controlMCPWorkspaceName.MatchString(name) {
		return nil, controlMCPToolFailure("name must be lowercase letters, digits, dashes and underscores (at most 63), starting with a letter or digit")
	}
	if c.server == nil || c.server.workspaceRoot == "" {
		return nil, controlMCPToolFailure("this machine has no workspace folder configured")
	}
	root, err := filepath.EvalSymlinks(c.server.workspaceRoot)
	if err != nil {
		return nil, controlMCPToolFailure("the workspace folder %s is unavailable", c.server.workspaceRoot)
	}
	path := filepath.Join(root, name)
	summary := func(entry map[string]any) map[string]any {
		return controlMCPPick(entry, "path", "workspace_name", "workspace_id")
	}
	if existing, err := c.registeredWorkspace(path); err != nil {
		return nil, err
	} else if existing != nil {
		return map[string]any{"workspace": summary(existing), "created": false}, nil
	}
	if _, err := c.dispatch(http.MethodPost, "/v1/workspace/repository/setup", nil, map[string]any{"path": path, "expected_resolved_path": path}); err != nil {
		return nil, err
	}
	if _, err := c.dispatch(http.MethodPost, "/v1/workspace/add", nil, map[string]any{"path": path, "name": name, "make_current": false}); err != nil {
		return nil, err
	}
	created, err := c.registeredWorkspace(path)
	if err != nil {
		return nil, err
	}
	if created == nil {
		return nil, controlMCPToolFailure("workspace %s was not registered", path)
	}
	return map[string]any{"workspace": summary(created), "created": true}, nil
}

// controlMCPAddWorkspace registers a folder the person named, through the same
// context-folder registration the Desktop's project onboarding uses, asking
// the workspace service for its existing-content checks (non-empty, outside
// system/Swarm storage, not inside a registered workspace). The folder is
// never initialised or committed.
func controlMCPAddWorkspace(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:write"); err != nil {
		return nil, err
	}
	path := controlMCPString(args, "path")
	if !filepath.IsAbs(path) {
		return nil, controlMCPToolFailure("path must be an absolute folder path on the machine running Swarm")
	}
	path = filepath.Clean(path)
	summary := func(entry map[string]any) map[string]any {
		return controlMCPPick(entry, "path", "workspace_name", "workspace_id", "is_git_repo")
	}
	if existing, err := c.registeredWorkspace(path); err != nil {
		return nil, err
	} else if existing != nil {
		return map[string]any{"workspace": summary(existing), "added": false}, nil
	}
	body := map[string]any{"path": path, "context_only": true, "require_existing_content": true, "make_current": false}
	if name := controlMCPString(args, "name"); name != "" {
		body["name"] = name
	}
	response, err := c.dispatch(http.MethodPost, "/v1/workspace/add", nil, body)
	if err != nil {
		return nil, err
	}
	resolved, _ := controlMCPMap(response["workspace"])["resolved_path"].(string)
	added, err := c.registeredWorkspace(resolved)
	if err != nil {
		return nil, err
	}
	if added == nil {
		return nil, controlMCPToolFailure("folder %s was not registered", path)
	}
	return map[string]any{"workspace": summary(added), "added": true}, nil
}

func (c *controlMCPCall) registeredWorkspace(path string) (map[string]any, error) {
	response, err := c.dispatch(http.MethodGet, "/v1/workspace/list", url.Values{"limit": {"500"}}, nil)
	if err != nil {
		return nil, err
	}
	for _, raw := range controlMCPList(response["workspaces"]) {
		if entry := controlMCPMap(raw); entry != nil && entry["path"] == path {
			return entry, nil
		}
	}
	return nil, nil
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
	projectID := controlMCPString(args, "project_id")
	if projectID != "" {
		// A project's orchestrator conversations, each with its run state
		// and pending requests as Swarm records them.
		query.Set("project_id", projectID)
		query.Set("view", "summary")
	}
	response, err := c.dispatch(http.MethodGet, "/v3/sessions", query, nil)
	if err != nil {
		return nil, err
	}
	sessions := []map[string]any{}
	for _, item := range controlMCPList(response["sessions"]) {
		entry := controlMCPMap(item)
		session := controlMCPMap(entry["session"])
		if session == nil {
			continue
		}
		summary := controlMCPSessionSummary(session)
		if attention := controlMCPMap(entry["attention"]); attention != nil {
			if run := controlMCPRunTiming(controlMCPMap(attention["current_run_state"])); run != nil {
				summary["run"] = run
			}
			if pending := controlMCPList(attention["pending_permissions"]); len(pending) > 0 {
				summary["pending_requests"] = len(pending)
			}
		}
		sessions = append(sessions, summary)
	}
	return map[string]any{"sessions": sessions}, nil
}

// controlMCPRunTiming is a session's current or last run as Swarm records it:
// status, whether it is active, and its times (Unix ms). While a run is
// active its time so far is now - started_at; cumulative_duration_ms holds the
// session's earlier runs, and a finished run's own time is duration_ms.
func controlMCPRunTiming(state map[string]any) map[string]any {
	if state == nil {
		return nil
	}
	run := controlMCPPick(state, "run_id", "status", "active", "blocked_reason")
	for _, key := range []string{"created_at", "started_at", "completed_at", "updated_at", "duration_ms", "cumulative_duration_ms"} {
		if value, ok := state[key].(float64); ok && value > 0 {
			run[key] = value
		}
	}
	return run
}

func controlMCPGetSession(c *controlMCPCall, args map[string]any) (any, error) {
	sessionID, err := controlMCPID(args, "session_id")
	if err != nil {
		return nil, err
	}
	if next, ok, err := tool.MessageContinuationFromArgs(args); err != nil {
		return nil, controlMCPToolFailure("%v", err)
	} else if ok {
		return c.sessionMessageChunk(sessionID, next, controlMCPInt(args, "detail_chars", 0))
	}
	settled, err := c.waitForSession(sessionID, controlMCPInt(args, "wait_seconds", 0))
	if err != nil {
		return nil, controlMCPNotFound(err, "session", sessionID, "manage_sessions list")
	}
	out, err := c.sessionState(sessionID, controlMCPInt(args, "messages", 5), controlMCPInt(args, "detail_chars", 0))
	if err != nil {
		return nil, controlMCPNotFound(err, "session", sessionID, "manage_sessions list")
	}
	if settled != nil {
		out["waited"] = settled
	}
	return out, nil
}

// sessionState is the get_session view: summary, model, run, pending
// requests, plan, delegated children and the latest messages. detail (0 for
// the defaults) raises the per-message and per tool-field character limits.
func (c *controlMCPCall) sessionState(sessionID string, limit, detail int) (map[string]any, error) {
	textLimit, toolLimit := controlMCPTextLimit, controlMCPToolOutputLimit
	if detail > 0 {
		textLimit, toolLimit = min(detail, controlMCPMaxDetailChars), min(detail, controlMCPMaxDetailChars)
	}
	response, err := c.dispatch(http.MethodGet, controlMCPSessionPath(sessionID), url.Values{"message_limit": {strconv.Itoa(limit)}, "event_limit": {"0"}}, nil)
	if err != nil {
		return nil, err
	}
	out := controlMCPSessionStateView(response, textLimit, toolLimit, c.sessionNextBase(sessionID))
	session := controlMCPMap(response["session"])
	if children := c.sessionChildren(sessionID, controlMCPMap(session["metadata"])); len(children) > 0 {
		out["children"] = children
	}
	// The session detail route does not populate the active plan; ask the
	// plan route directly (small response when there is none).
	if active, err := c.dispatch(http.MethodGet, controlMCPSessionPath(sessionID, "plans", "active"), nil, nil); err == nil {
		if progress := controlMCPActivePlan(active); progress != nil {
			out["plan"] = progress
		}
	}
	return out, nil
}

// sessionMessageChunk is get_session's continuation: the next chunk of one
// exact message, read from the session messages route
// (readSessionMessageChunk), with next while more remains.
func (c *controlMCPCall) sessionMessageChunk(sessionID string, next tool.MessageContinuation, maxChars int) (map[string]any, error) {
	if maxChars <= 0 {
		maxChars = controlMCPMaxDetailChars
	}
	query := url.Values{
		"message_id": {next.MessageID}, "message_seq": {strconv.FormatUint(next.MessageSeq, 10)},
		"content_offset": {strconv.Itoa(next.ContentOffset)}, "max_chars": {strconv.Itoa(min(maxChars, controlMCPMaxDetailChars))},
	}
	response, err := c.dispatch(http.MethodGet, controlMCPSessionPath(sessionID, "messages"), query, nil)
	if err != nil {
		return nil, err
	}
	message := controlMCPMap(response["message"])
	if message == nil {
		return nil, controlMCPToolFailure("Swarm returned no message chunk")
	}
	if message["truncated"] == true {
		next := c.sessionNextBase(sessionID)
		next["message_id"], next["message_seq"], next["content_offset"] = message["id"], message["global_seq"], message["content_offset"]
		message["next"] = next
	}
	return map[string]any{"session_id": sessionID, "message": message, "untrusted_content": true}, nil
}

// controlMCPActivePlan is the plan progress of a /plans/active answer; nil
// when there is none.
func controlMCPActivePlan(active map[string]any) map[string]any {
	if hasActive, _ := active["has_active"].(bool); hasActive {
		return controlMCPPlanProgress(controlMCPMap(active["active_plan"]))
	}
	return nil
}

// controlMCPSessionStateView is the part of the get_session view read from
// the session detail route (GET /v3/sessions/{id}): summary, model, active
// run, pending requests and the latest messages, trimmed. MCP get_session
// and a home's linked get_task_session both build it from that route.
//
// A message text longer than textLimit characters is cut, never silently:
// it says truncated, content_offset (the UTF-8 byte offset where the shown
// part ends), total_bytes and next, nextBase plus the exact message_id,
// message_seq and content_offset that read its following chunk.
// controlMCPContextGrowthLimit bounds the newest turns get_session lists in
// context_growth (oldest first); the count left out is reported.
const controlMCPContextGrowthLimit = 50

func controlMCPSessionStateView(response map[string]any, textLimit, toolLimit int, nextBase map[string]any) map[string]any {
	session := controlMCPMap(response["session"])
	out := map[string]any{"session": controlMCPSessionSummary(session)}
	if preference := controlMCPPick(controlMCPMap(session["preference"]), "provider", "model", "thinking"); len(preference) > 0 {
		out["model"] = preference
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
	// Session usage as the detail route reports it: request_count counts
	// provider requests, and context_growth gives each recent turn's
	// first-request context and request count. A replaying session shows
	// up as a growing first-request size.
	if usage := controlMCPPick(controlMCPMap(response["usage_summary"]), "provider", "model", "context_window", "turn_count", "request_count", "total_tokens", "remaining_tokens", "estimated_cost_usd"); len(usage) > 0 {
		out["usage"] = usage
	}
	if growth := controlMCPList(response["context_growth"]); len(growth) > 0 {
		if omitted := len(growth) - controlMCPContextGrowthLimit; omitted > 0 {
			growth = growth[omitted:]
			out["context_growth_omitted"] = omitted
		}
		out["context_growth"] = growth
	}
	messages := []map[string]any{}
	for _, raw := range controlMCPList(response["messages"]) {
		message := controlMCPMap(raw)
		if message == nil {
			continue
		}
		summary := controlMCPPick(message, "role")
		content, _ := message["content"].(string)
		// Provider internals (thought signatures, encrypted reasoning, raw
		// payloads) never spend the text budget; the chunk route continues
		// the same readable text (tool.ChunkReadableMessageContent).
		content = tool.ReadableMessageContent(content)
		if tool, ok := controlMCPToolMessageDetail(message, content, toolLimit); ok {
			summary["tool"] = tool
		} else {
			controlMCPMessageText(summary, message, content, textLimit, nextBase)
		}
		// Files the message carries (its session media), so a client shows them
		// and an agent can name them by asset_id.
		media := []map[string]any{}
		for _, item := range controlMCPList(message["media"]) {
			if reference := controlMCPMap(item); reference != nil {
				media = append(media, controlMCPPick(reference, "asset_id", "modality", "mime_type", "file_name", "size"))
			}
		}
		if len(media) > 0 {
			summary["media"] = media
		}
		messages = append(messages, summary)
	}
	out["messages"] = messages
	return out
}

// controlMCPMessageText puts a message's text in summary, cut to limit
// characters on a character boundary. A cut text says so: truncated,
// content_offset, total_bytes and next (nextBase with the message's exact
// message_id, message_seq and content_offset). A message without an id or
// sequence, which cannot be continued, keeps the "[truncated N characters]"
// marker.
func controlMCPMessageText(summary, message map[string]any, content string, limit int, nextBase map[string]any) {
	if utf8.RuneCountInString(content) <= limit {
		summary["content"] = content
		return
	}
	id, _ := message["id"].(string)
	seq, _ := message["global_seq"].(float64)
	if strings.TrimSpace(id) == "" || seq < 1 || nextBase == nil {
		summary["content"] = controlMCPTruncate(content, limit)
		return
	}
	cut := 0
	for count := 0; count < limit; count++ {
		_, size := utf8.DecodeRuneInString(content[cut:])
		cut += size
	}
	summary["content"] = content[:cut]
	summary["id"], summary["global_seq"] = id, seq
	summary["truncated"], summary["content_offset"], summary["total_bytes"] = true, cut, len(content)
	next := make(map[string]any, len(nextBase)+3)
	for key, value := range nextBase {
		next[key] = value
	}
	next["message_id"], next["message_seq"], next["content_offset"] = id, seq, cut
	summary["next"] = next
}

// sessionChildren lists work a session delegated: project tasks for an
// orchestrator, otherwise sessions whose metadata names it as parent. Both
// reads go through the caller's own scoped routes; failures omit the field.
func (c *controlMCPCall) sessionChildren(sessionID string, metadata map[string]any) []map[string]any {
	children := []map[string]any{}
	projectID, _ := metadata["project_id"].(string)
	if role, _ := metadata["role"].(string); role == "project_orchestrator" && controlMCPSafeID.MatchString(projectID) && !controlMCPReservedIDs[projectID] {
		// The route selects this session's tasks; the board is never read.
		query := url.Values{"origin_session_id": {sessionID}, "scope": {pebblestore.ProjectTaskScopeAll}, "limit": {strconv.Itoa(controlMCPMaxChildren)}}
		response, err := c.dispatch(http.MethodGet, "/v3/projects/"+projectID+"/tasks", query, nil)
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

// controlMCPRunOutcome names how a session's last run ended: failed, stopped
// (cancelled or interrupted) or finished. The run's own recorded status
// decides when there is one (V3 session details carry no lifecycle, and a run
// stopped before it started has nothing else); the lifecycle is the fallback.
func controlMCPRunOutcome(runStatus string, lifecycle map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(runStatus)) {
	case "cancelled", "canceled", "interrupted", "stopped":
		return "stopped"
	case "failed", "errored", "error", "blocked":
		return "failed"
	case "completed", "succeeded", "finished", "done":
		return "finished"
	}
	if errText, _ := lifecycle["error"].(string); strings.TrimSpace(errText) != "" {
		return "failed"
	}
	phase, _ := lifecycle["phase"].(string)
	stopReason, _ := lifecycle["stop_reason"].(string)
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "failed", "errored", "error":
		return "failed"
	case "cancelled", "canceled", "interrupted", "stopped":
		return "stopped"
	}
	reason := strings.ToLower(stopReason)
	if strings.Contains(reason, "cancel") || strings.Contains(reason, "interrupt") || strings.Contains(reason, "user") {
		return "stopped"
	}
	return "finished"
}

// controlMCPSettledState reports whether a session detail is settled: its
// run ended (no active run intent) or it waits on a decision (a pending
// permission or question). The map is the waited value tools return.
func controlMCPSettledState(detail map[string]any, runStatus string) map[string]any {
	if len(controlMCPList(detail["pending_permissions"])) > 0 {
		return map[string]any{"settled": true, "reason": "waiting_on_decision"}
	}
	if controlMCPMap(detail["active_run_intent"]) == nil {
		return map[string]any{"settled": true, "reason": "run_ended", "outcome": controlMCPRunOutcome(runStatus, controlMCPMap(controlMCPMap(detail["session"])["lifecycle"]))}
	}
	return map[string]any{"settled": false}
}

// waitForSession blocks up to seconds until the session settles (see
// controlMCPSettledState). It wakes on committed V3 outbox records (no timer
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
	settled := func() (map[string]any, error) {
		detail, err := c.dispatch(http.MethodGet, controlMCPSessionPath(sessionID), url.Values{"message_limit": {"0"}, "event_limit": {"0"}}, nil)
		if err != nil {
			return nil, err
		}
		return controlMCPSettledState(detail, c.lastRunStatus(sessionID, detail)), nil
	}
	state, err := settled()
	if err != nil {
		return nil, err
	}
	if state["settled"] == true || sub == nil {
		return state, nil
	}
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	for {
		select {
		case record := <-sub.send:
			if record.SessionID != sessionID {
				continue
			}
			if state, err = settled(); err != nil || state["settled"] == true {
				return state, err
			}
		case <-sub.slow:
			return settled()
		case <-timer.C:
			return map[string]any{"settled": false, "timed_out": true, "waited_seconds": seconds}, nil
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
	if fast, _ := args["fast"].(bool); fast {
		tier := controlMCPFastTier(record)
		if tier == "" {
			return nil, controlMCPToolFailure("%s has no fast serving on %s (see list_models)", model, provider)
		}
		preference["service_tier"] = tier
	}
	return preference, nil
}

// controlMCPFastTier is the serving tier behind the catalog's fast setting
// for a model (codex names it fast, other providers priority), or "".
func controlMCPFastTier(record map[string]any) string {
	for _, raw := range controlMCPList(record["service_tier_mappings"]) {
		mapping := controlMCPMap(raw)
		if setting, _ := mapping["swarm_setting"].(string); strings.EqualFold(setting, "fast") {
			tier, _ := mapping["tier"].(string)
			return strings.ToLower(strings.TrimSpace(tier))
		}
	}
	return ""
}

// controlMCPRoleModel is a role's assignment as the tools show it: fast is
// the fast serving tier.
func controlMCPRoleModel(assignment map[string]any) map[string]any {
	entry := controlMCPPick(assignment, "provider", "model", "thinking")
	tier, _ := assignment["service_tier"].(string)
	entry["fast"] = tier == "fast" || tier == "priority"
	return entry
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
	custom := !slices.Contains(controlMCPSessionAgents, agent)
	if custom {
		if err := c.customAgent(agent); err != nil {
			return nil, err
		}
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
		body["model_profile"] = controlMCPModelChoice(preference)
	} else if custom {
		// Only swarm and the orchestrator capture the account defaults on
		// their own; a custom agent with no model of its own needs them asked
		// for, resolved through the account's agent-model settings.
		body["model_profile"] = map[string]any{"use_account_default": true}
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
	if _, present := args["access"]; present {
		return nil, controlMCPToolFailure(retiredSessionAccess)
	}
	created, err := c.dispatch(http.MethodPost, createPath, nil, body)
	if err != nil {
		if workspacePath, ok := body["workspace_path"].(string); ok {
			return nil, c.unregisteredWorkspace(err, workspacePath)
		}
		return nil, controlMCPNotFound(err, "project", controlMCPString(args, "project_id"), "manage_projects list")
	}
	session := controlMCPMap(created["session"])
	sessionID, _ := session["id"].(string)
	if document != nil {
		started, err := c.runPlan(sessionID, document)
		if err != nil {
			return nil, controlMCPToolFailure("session %s created but the plan did not start: %s", sessionID, err.Error())
		}
		return c.currentState(map[string]any{"started": started}, sessionID, args)
	}
	media, err := c.uploadAttachments(sessionID, args["attachments"])
	if err != nil {
		return nil, controlMCPToolFailure("session %s created but its attachments did not upload: %s", sessionID, err.Error())
	}
	run, err := c.sendMessageWithMedia(sessionID, prompt, controlMCPRequestID(), media)
	if err != nil {
		return nil, controlMCPToolFailure("session %s created but the prompt was not accepted: %s", sessionID, err.Error())
	}
	return c.currentState(map[string]any{"started": run}, sessionID, args)
}

// unregisteredWorkspace explains a session refused because its workspace path
// is not a registered workspace (Swarm reports the missing canonical binding).
func (c *controlMCPCall) unregisteredWorkspace(err error, path string) error {
	var toolErr controlMCPToolError
	if !errors.As(err, &toolErr) || toolErr.status != http.StatusBadRequest || !strings.Contains(toolErr.message, "workspace") {
		return err
	}
	if entry, lookupErr := c.registeredWorkspace(path); lookupErr != nil || entry != nil {
		return err
	}
	return controlMCPToolFailure("workspace_path %q is not a registered workspace on this machine; use a path from manage_workspace list, or ask the person which existing folder holds this work and register it with manage_workspace create", path)
}

// currentState returns one coherent view after work was started: the start
// receipt (run or plan ids) plus the session state read afterwards, never the
// creation-time snapshot. It honours wait_seconds first.
func (c *controlMCPCall) currentState(out map[string]any, sessionID string, args map[string]any) (any, error) {
	settled, err := c.waitForSession(sessionID, controlMCPInt(args, "wait_seconds", 0))
	if err != nil {
		out["session_id"] = sessionID
		out["state_error"] = "work started, but its state could not be read; call manage_sessions get"
		return out, nil
	}
	state, err := c.sessionState(sessionID, 3, 0)
	if err != nil {
		out["session_id"] = sessionID
		out["state_error"] = "work started, but its state could not be read; call manage_sessions get"
		return out, nil
	}
	for key, value := range state {
		out[key] = value
	}
	if settled != nil {
		out["waited"] = settled
	}
	return out, nil
}

func (c *controlMCPCall) sendMessage(sessionID, content, requestID string) (map[string]any, error) {
	return c.sendMessageWithMedia(sessionID, content, requestID, nil)
}

// sendMessageWithMedia sends a user message carrying uploaded session media
// (uploadAttachments).
func (c *controlMCPCall) sendMessageWithMedia(sessionID, content, requestID string, media []map[string]any) (map[string]any, error) {
	body := map[string]any{
		"client_request_id": requestID,
		"role":              "user",
		"content":           content,
	}
	if len(media) > 0 {
		body["media"] = media
	}
	response, err := c.dispatch(http.MethodPost, controlMCPSessionPath(sessionID, "messages"), nil, body)
	if err != nil {
		return nil, err
	}
	// Only the identity: the intent's status at acceptance (pending_executor)
	// is stale by the time the caller reads it; active_run carries the current one.
	run := controlMCPPick(controlMCPMap(response["run_intent"]), "run_id")
	run["accepted"] = true
	// A message to a session whose run is active is queued for that run
	// (sessionsV3QueuedMessageResponseFields): it starts no run of its own.
	if queued, _ := response["queued"].(bool); queued {
		run["queued"] = true
		if forRun, _ := response["queued_for_run_id"].(string); forRun != "" {
			run["queued_for_run_id"] = forRun
		}
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
	media, err := c.uploadAttachments(sessionID, args["attachments"])
	if err != nil {
		return nil, err
	}
	run, err := c.sendMessageWithMedia(sessionID, controlMCPString(args, "content"), requestID, media)
	if err != nil {
		return nil, controlMCPNotFound(err, "session", sessionID, "manage_sessions list")
	}
	return c.currentState(map[string]any{"client_request_id": requestID, "started": run}, sessionID, args)
}

// controlMCPModelChoice is the session model-profile choice for a validated
// preference: a selection owned by that session. Swarm and orchestrator
// sessions otherwise capture the account role defaults when created, and that
// captured profile, not the plain preference, decides their model.
func controlMCPModelChoice(preference map[string]any) map[string]any {
	inline := controlMCPPick(preference, "provider", "model", "thinking")
	return map[string]any{"temporary": inline}
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
	response, err := c.dispatch(http.MethodPut, controlMCPSessionPath(sessionID, "model-profile"), nil, map[string]any{
		"client_request_id": controlMCPRequestID(),
		"choice":            controlMCPModelChoice(preference),
	})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"session_id": sessionID, "model": preference}
	if applied := controlMCPPick(controlMCPMap(response["preference"]), "provider", "model", "thinking"); len(applied) > 0 {
		out["model"] = applied
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
	if runID, ok := controlMCPMap(started["run_intent"])["run_id"].(string); ok && runID != "" {
		out["run_id"] = runID
	}
	return out, nil
}

// controlMCPStopWaitSeconds bounds how long stop_run waits for the stopped
// run to end before reporting.
const controlMCPStopWaitSeconds = 10

func controlMCPStopRun(c *controlMCPCall, args map[string]any) (any, error) {
	sessionID, err := controlMCPID(args, "session_id")
	if err != nil {
		return nil, err
	}
	detail, err := c.dispatch(http.MethodGet, controlMCPSessionPath(sessionID), url.Values{"message_limit": {"0"}, "event_limit": {"0"}}, nil)
	if err != nil {
		return nil, controlMCPNotFound(err, "session", sessionID, "manage_sessions list")
	}
	session := controlMCPMap(detail["session"])
	targetSwarmID, _ := controlMCPMap(session["metadata"])["swarm_v3_runtime_swarm_id"].(string)
	if strings.TrimSpace(targetSwarmID) == "" && controlMCPMap(session["metadata"])["lineage_kind"] == "delegated_subagent" {
		// Ordinary task children already have an authenticated V3 stop route.
		// They do not own the primary-session executor identity used below.
		if err := c.requireTokenScope("sessions:write"); err != nil {
			return nil, err
		}
		if _, err := c.dispatch(http.MethodPost, "/v3/subagents:stop", nil, map[string]any{"session_id": sessionID}); err != nil {
			return nil, err
		}
		return map[string]any{"session_id": sessionID, "run_status": "stop_requested", "summary": "Stop requested for the delegated agent; inspect manage_sessions get for its final state."}, nil
	}
	if strings.TrimSpace(targetSwarmID) == "" {
		return nil, controlMCPToolFailure("session has no runtime identity to stop")
	}
	runID, _ := controlMCPMap(detail["active_run_intent"])["run_id"].(string)
	if runID == "" {
		runID, _ = controlMCPMap(session["lifecycle"])["run_id"].(string)
	}
	if strings.TrimSpace(runID) == "" {
		return nil, controlMCPToolFailure("session %s has no active run to stop (its last run already ended; see manage_sessions get)", sessionID)
	}
	body := map[string]any{"run_id": runID, "target_swarm_id": targetSwarmID}
	if reason := controlMCPString(args, "reason"); reason != "" {
		body["reason"] = reason
	}
	if _, err := c.dispatch(http.MethodPost, controlMCPSessionPath(sessionID, "run", "stop"), nil, body); err != nil {
		return nil, err
	}
	waited, waitErr := c.waitForSession(sessionID, controlMCPStopWaitSeconds)
	state, stateErr := c.sessionState(sessionID, 1, 0)
	if waitErr != nil || stateErr != nil {
		return map[string]any{"session_id": sessionID, "run_id": runID, "run_status": "stop_requested", "summary": "Stop was requested; the final state could not be read. Check with manage_sessions get."}, nil
	}
	return controlMCPStopReport(sessionID, runID, waited, state), nil
}

// controlMCPStopReport is stop_run's answer: the run's final status, the
// plan's state (a stopped plan is paused, never cancelled) and one summary.
func controlMCPStopReport(sessionID, runID string, waited, state map[string]any) map[string]any {
	out := map[string]any{"session_id": sessionID, "run_id": runID}
	parts := []string{}
	switch {
	case waited["reason"] == "run_ended":
		// The lifecycle decides: a run that completed just before the stop
		// landed reports finished, not stopped.
		out["run_status"] = waited["outcome"]
		parts = append(parts, fmt.Sprintf("Run %s has ended (%s).", runID, out["run_status"]))
	case waited["reason"] == "waiting_on_decision":
		out["run_status"] = "stopping"
		parts = append(parts, fmt.Sprintf("Stop was requested for run %s; it still has a pending request, which is no longer acted on.", runID))
	default:
		out["run_status"] = "stopping"
		parts = append(parts, fmt.Sprintf("Stop was requested for run %s and it is still winding down; check with manage_sessions get.", runID))
	}
	if session := controlMCPMap(state["session"]); session != nil {
		out["session"] = session
	}
	if plan := controlMCPMap(state["plan"]); plan != nil {
		out["plan"] = plan
		status, _ := plan["status"].(string)
		checkpoints, _ := plan["checkpoints"].([]map[string]any)
		done, total := 0, len(checkpoints)
		for _, checkpoint := range checkpoints {
			if checkpoint["status"] == "completed" {
				done++
			}
		}
		switch status {
		case "paused":
			parts = append(parts, fmt.Sprintf("The plan is paused, not cancelled (%d of %d checkpoints completed): it keeps its progress, does not resume on its own, and continues its checkpoint when you send_message.", done, total))
		case "":
		default:
			parts = append(parts, fmt.Sprintf("The plan is %s (%d of %d checkpoints completed).", status, done, total))
		}
	}
	if messages, _ := state["messages"].([]map[string]any); len(messages) > 0 {
		out["last_message"] = messages[len(messages)-1]
	}
	out["summary"] = strings.Join(parts, " ")
	return out
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
	// A client tool's call is answered with its result.
	if result, given := args["result"]; given && result != nil {
		if action != "allow_once" {
			return nil, controlMCPToolFailure("result needs action allow_once")
		}
		body["approved_arguments"] = map[string]any{"result": result}
	}
	response, err := c.dispatch(http.MethodPost, controlMCPSessionPath(sessionID, "permissions", permissionID, "resolve"), nil, body)
	if err != nil {
		var toolErr controlMCPToolError
		if errors.As(err, &toolErr) && toolErr.status == http.StatusNotFound {
			return nil, controlMCPToolFailure("permission %q is not pending in session %q (HTTP 404); manage_sessions get lists its pending_permissions", permissionID, sessionID)
		}
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
					if controlMCPFastTier(controlMCPMap(record)) != "" {
						m["fast"] = true
					}
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
			entry := controlMCPRoleModel(controlMCPMap(controlMCPMap(assignments[slot[0]])[slot[1]]))
			if effective := controlMCPMap(controlMCPMap(settings["effective_roles"])[id]); len(effective) > 0 {
				entry = controlMCPRoleModel(controlMCPMap(effective["assignment"]))
				entry["source"] = effective["source"]
			}
			entry["role"] = id
			roles = append(roles, entry)
		}
		out["roles"] = roles
	}
	if media, err := c.controlMCPMediaModels(); err == nil {
		out["media"] = media
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
	roles := []string{role}
	if role == "all" {
		roles = controlMCPAgentModelRoleOrder
	} else if _, ok := controlMCPAgentModelRoles[role]; !ok {
		return nil, controlMCPToolFailure("unknown role %q", role)
	}
	preference, err := c.modelPreference(args)
	if err != nil {
		return nil, err
	}
	if preference == nil {
		return nil, controlMCPToolFailure("give provider and model (see list_models)")
	}
	// One canonical PATCH per role; on a failure, report which roles already
	// changed so the caller can retry or restore them.
	var applied map[string]any
	for i, name := range roles {
		slot := controlMCPAgentModelRoles[name]
		response, err := c.dispatch(http.MethodPatch, "/v1/agent-model-settings", nil, map[string]any{slot[0]: map[string]any{slot[1]: preference}})
		if err != nil {
			if i == 0 {
				return nil, err
			}
			return nil, controlMCPToolFailure("roles %v changed, then %s failed: %s", roles[:i], name, err.Error())
		}
		applied = controlMCPRoleModel(controlMCPMap(controlMCPMap(controlMCPMap(response["agent_model_settings"])[slot[0]])[slot[1]]))
	}
	return map[string]any{"role": role, "model": applied}, nil
}

// ---- projects ------------------------------------------------------------

func controlMCPProjectWorkspaces(project map[string]any) []map[string]any {
	workspaces := []map[string]any{}
	for _, ws := range controlMCPList(project["workspaces"]) {
		if entry := controlMCPMap(ws); entry != nil {
			workspaces = append(workspaces, controlMCPPick(entry, "path", "role", "workspace_id"))
		}
	}
	return workspaces
}

// controlMCPContextStatus is the project.md generation state; a project
// without a generation record (created before generation existed) reads ready.
func controlMCPContextStatus(project map[string]any) string {
	generation := controlMCPMap(project["context_generation"])
	if generation == nil {
		return "ready"
	}
	status, _ := generation["status"].(string)
	return status
}

// controlMCPProjectSummary is one project with its project.md (bounded) and
// the generation state the orchestrator start waits on.
func controlMCPProjectSummary(project map[string]any) map[string]any {
	summary := controlMCPPick(project, "id", "name", "description", "theme_id")
	summary["workspaces"] = controlMCPProjectWorkspaces(project)
	generation := controlMCPPick(controlMCPMap(project["context_generation"]), "status", "attempt", "error", "router_alert")
	generation["status"] = controlMCPContextStatus(project)
	summary["context_generation"] = generation
	if text, _ := project["project_context"].(string); text != "" {
		summary["project_md"] = controlMCPTruncate(text, controlMCPMaxProjectContextChars)
	}
	return summary
}

func (c *controlMCPCall) project(projectID string) (map[string]any, error) {
	response, err := c.dispatch(http.MethodGet, "/v3/projects/"+projectID, nil, nil)
	if err != nil {
		return nil, controlMCPNotFound(err, "project", projectID, "manage_projects list")
	}
	project := controlMCPMap(response["project"])
	if project == nil {
		return nil, controlMCPToolFailure("Swarm returned no project %s", projectID)
	}
	return project, nil
}

func controlMCPGetProject(c *controlMCPCall, args map[string]any) (any, error) {
	projectID, err := controlMCPID(args, "project_id")
	if err != nil {
		return nil, err
	}
	project, waited, err := c.waitForProjectContext(projectID, controlMCPInt(args, "wait_seconds", 0))
	if err != nil {
		return nil, err
	}
	summary := controlMCPProjectSummary(project)
	for _, ws := range summary["workspaces"].([]map[string]any) {
		path, _ := ws["path"].(string)
		rule, found, err := c.server.publishRule(path)
		if err != nil {
			return nil, err
		}
		if found {
			// Expose the command path and sync policy, never credentials.
			ws["publish"] = map[string]any{"command": rule.Command, "sync": rule.Sync}
		}
	}
	out := map[string]any{"project": summary}
	if status, err := c.projectStatus(projectID, project); err == nil {
		out["status"] = status
	}
	if waited != nil {
		out["waited"] = waited
	}
	return out, nil
}

// waitForProjectContext returns the project once its project.md generation is
// no longer pending or running, or at the deadline. Like waitForSession it
// wakes on committed realtime outbox records (project updates publish there,
// carrying project_id), never a timer poll, and re-reads through dispatch.
func (c *controlMCPCall) waitForProjectContext(projectID string, seconds int) (map[string]any, map[string]any, error) {
	project, err := c.project(projectID)
	if err != nil || seconds <= 0 {
		return project, nil, err
	}
	if seconds > controlMCPMaxWaitSeconds {
		seconds = controlMCPMaxWaitSeconds
	}
	generating := func(project map[string]any) bool {
		status := controlMCPContextStatus(project)
		return status == "pending" || status == "running" || status == "generating"
	}
	if !generating(project) {
		return project, map[string]any{"settled": true}, nil
	}
	var hub *v3RealtimeOutboxHub
	if c.server != nil {
		hub = c.server.v3RealtimeOutbox
	}
	sub := hub.subscribe()
	if sub == nil {
		return project, map[string]any{"settled": false}, nil
	}
	defer hub.unsubscribe(sub)
	// Re-read once after subscribing so a change committed in between is seen.
	if project, err = c.project(projectID); err != nil || !generating(project) {
		return project, map[string]any{"settled": err == nil}, err
	}
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	for {
		select {
		case record := <-sub.send:
			var payload struct {
				ProjectID string `json:"project_id"`
			}
			if json.Unmarshal(record.Event.Payload, &payload) != nil || payload.ProjectID != projectID {
				continue
			}
			if project, err = c.project(projectID); err != nil || !generating(project) {
				return project, map[string]any{"settled": err == nil}, err
			}
		case <-sub.slow:
			project, err = c.project(projectID)
			return project, map[string]any{"settled": err == nil && !generating(project)}, err
		case <-timer.C:
			return project, map[string]any{"settled": false, "timed_out": true, "waited_seconds": seconds}, nil
		case <-c.request.Context().Done():
			return project, map[string]any{"settled": false}, nil
		}
	}
}

func controlMCPRetryProjectContext(c *controlMCPCall, args map[string]any) (any, error) {
	projectID, err := controlMCPID(args, "project_id")
	if err != nil {
		return nil, err
	}
	project, err := c.project(projectID)
	if err != nil {
		return nil, err
	}
	attempt, _ := controlMCPMap(project["context_generation"])["attempt"].(float64)
	response, err := c.dispatch(http.MethodPost, "/v3/projects/"+projectID+"/context:retry", nil, map[string]any{"expected_attempt": int(attempt)})
	if err != nil {
		var toolErr controlMCPToolError
		if errors.As(err, &toolErr) && toolErr.status == http.StatusConflict {
			return nil, controlMCPToolFailure("project %s: project.md generation is still running or changed meanwhile; check it with manage_projects get", projectID)
		}
		return nil, err
	}
	return map[string]any{"project": controlMCPProjectSummary(controlMCPMap(response["project"]))}, nil
}

// ---- tasks ---------------------------------------------------------------

func controlMCPTaskSummary(task map[string]any) map[string]any {
	summary := controlMCPPick(task, "id", "title", "status", "agent", "worker_name", "worker_run_id", "session_id", "worktree_branch", "base_branch", "is_integrated", "unintegrated_commits")
	for key, limit := range map[string]int{"action_needed": 200, "last_error": 300} {
		if text, ok := task[key].(string); ok && strings.TrimSpace(text) != "" {
			summary[key] = controlMCPTruncate(text, limit)
		}
	}
	// what_did_do is a list of lines.
	did := []string{}
	for _, raw := range controlMCPList(task["what_did_do"]) {
		if line, _ := raw.(string); strings.TrimSpace(line) != "" {
			did = append(did, controlMCPTruncate(line, 300))
		}
	}
	if len(did) > 0 {
		summary["what_did_do"] = did
	}
	// Deliverables have no path: they are records (id, title, kind, status).
	deliverables := []map[string]any{}
	for i, raw := range controlMCPList(task["deliverables"]) {
		if i >= 10 {
			break
		}
		if item := controlMCPPick(controlMCPMap(raw), "id", "title", "kind", "status"); len(item) > 0 {
			deliverables = append(deliverables, item)
		}
	}
	if len(deliverables) > 0 {
		summary["deliverables"] = deliverables
	}
	if integration := controlMCPPick(controlMCPMap(task["integration"]), "state", "error", "resulting_target_head"); len(integration) > 0 {
		summary["integration"] = integration
	}
	return summary
}

// controlMCPIntegrateTask selects the task's own session, agent branch and
// captured target branch, so the integrate route's lineage checks decide; it
// never picks branches itself.
func controlMCPIntegrateTask(c *controlMCPCall, args map[string]any) (any, error) {
	return controlMCPIntegrate(c, args, "manage_projects integrate_task", "manage_projects list_tasks")
}

// controlMCPUnobservedDelivery reports a 409 whose delivery assessment could
// not observe the task's Git state (its reads ran out of time or the heads
// moved under them): a refusal made before any integration receipt or Git
// change, so repeating the request is safe.
func controlMCPUnobservedDelivery(err error) (map[string]any, bool) {
	var toolErr controlMCPToolError
	if !errors.As(err, &toolErr) || toolErr.status != http.StatusConflict {
		return nil, false
	}
	var body map[string]any
	if json.Unmarshal([]byte(toolErr.body), &body) != nil {
		return nil, false
	}
	delivery := controlMCPMap(body["delivery"])
	if delivery == nil || delivery["freshness"] == "observed" {
		return nil, false
	}
	return delivery, true
}

// controlMCPIntegrate integrates for the named tool. For an interactive
// caller an unobserved delivery is retried once in the same call; a workflow
// deliver step (c.workflowDelivery) integrates once so its durable attempts
// stay exact. A repeated refusal keeps its structured reason for the workflow
// retry policy and names callable tools rather than an internal HTTP route.
func controlMCPIntegrate(c *controlMCPCall, args map[string]any, tool, readTool string) (any, error) {
	projectID, err := controlMCPID(args, "project_id")
	if err != nil {
		return nil, err
	}
	taskID, err := controlMCPID(args, "task_id")
	if err != nil {
		return nil, err
	}
	// Read the one task, not the board, which a large project cannot return.
	response, err := c.dispatch(http.MethodGet, "/v3/projects/"+projectID+"/tasks/"+taskID, nil, nil)
	var toolErr controlMCPToolError
	if err != nil && !(errors.As(err, &toolErr) && toolErr.status == http.StatusNotFound) {
		return nil, err
	}
	task := controlMCPMap(response["task"])
	if err != nil || task == nil || task["id"] != taskID || task["archived"] == true {
		return nil, controlMCPToolFailure("task %s not found in project %s (see list_tasks)", taskID, projectID)
	}
	selection := map[string]any{}
	for key, field := range map[string]string{"session_id": "session_id", "source_branch": "worktree_branch", "target_branch": "base_branch"} {
		value, _ := task[field].(string)
		if strings.TrimSpace(value) == "" {
			return nil, controlMCPToolFailure("task %s has no %s to integrate", taskID, field)
		}
		selection[key] = value
	}
	path := "/v3/projects/" + projectID + "/tasks/" + taskID + "/integrate"
	integrated, err := c.dispatch(http.MethodPost, path, nil, selection)
	if _, unobserved := controlMCPUnobservedDelivery(err); unobserved && !c.workflowDelivery {
		integrated, err = c.dispatch(http.MethodPost, path, nil, selection)
	}
	if delivery, unobserved := controlMCPUnobservedDelivery(err); unobserved {
		reason, _ := delivery["reason"].(string)
		if code, _ := delivery["reason_code"].(string); code != "" {
			reason = strings.TrimSpace(reason + " (" + code + ")")
		}
		if reason == "" {
			reason = "its delivery state was not observed"
		}
		code, _ := delivery["reason_code"].(string)
		return nil, controlMCPToolError{message: fmt.Sprintf("Swarm could not read this task's Git state to merge it (%s); nothing was merged. Call %s again; if it repeats, read %s.", controlMCPTruncate(reason, 200), tool, readTool), inspectionCode: code}
	}
	if err != nil {
		return nil, err
	}
	out := map[string]any{"status": integrated["status"], "task": controlMCPTaskSummary(controlMCPMap(integrated["task"]))}
	if result := controlMCPPick(controlMCPMap(integrated["integration"]), "target_branch", "resulting_target_head", "commit", "warning"); len(result) > 0 {
		out["integration"] = result
	}
	return out, nil
}

// ---- workers -------------------------------------------------------------

func (c *controlMCPCall) worker(workerID string) (map[string]any, error) {
	response, err := c.dispatch(http.MethodGet, "/v3/workers/"+workerID, nil, nil)
	if err != nil {
		return nil, controlMCPNotFound(err, "worker", workerID, "manage_workers list")
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
			return nil, controlMCPNotFound(err, "worker run", runID, "manage_workers inspect without run_id")
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
		// The summary route requires a reporting day; next_scheduled_at does not
		// depend on it.
		day := url.Values{"timezone": {"UTC"}, "date": {time.Now().UTC().Format("2006-01-02")}}
		if summary, err := c.dispatch(http.MethodGet, "/v3/workers/"+workerID+"/summary", day, nil); err == nil {
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
	// A scheduled plan is a template: each run gets its own plan identity, and
	// the worker store refuses templates that carry one.
	document.ID = ""
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

// controlMCPWorkerCreateBody is the POST /v3/workers body. A schedule is part
// of the created worker, so it is approved with it; attaching one later to an
// established worker applies immediately through the versioned update boundary.
func controlMCPWorkerCreateBody(args map[string]any) (map[string]any, map[string]any, error) {
	automation, err := controlMCPAutomation(args)
	if err != nil {
		return nil, nil, err
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
			return nil, nil, err
		}
		body["metadata"] = map[string]any{"project_id": projectID}
	}
	// The other definition inputs a caller may give with a folder
	// (manage_workers create over Swarm Control): applied in the same create.
	if agent := controlMCPString(args, "agent"); agent != "" {
		body["agent"] = agent
	}
	if key := controlMCPString(args, "idempotency_key"); key != "" {
		body["idempotency_key"] = key
	}
	automations := []any{}
	if raw, ok := args["automations"]; ok && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return nil, nil, controlMCPToolFailure("automations must be a list")
		}
		automations = append(automations, list...)
	}
	if automation != nil {
		automations = append(automations, automation)
	}
	if len(automations) > 0 {
		body["automations"] = automations
	}
	return body, automation, nil
}

func controlMCPCreateWorker(c *controlMCPCall, args map[string]any) (any, error) {
	body, automation, err := controlMCPWorkerCreateBody(args)
	if err != nil {
		return nil, err
	}
	if raw, present := args["budget"]; present {
		if _, err := parseControlMCPWorkerBudget(raw); err != nil {
			return nil, err
		}
	}
	if raw, exists := args["model"]; exists && raw != nil {
		if _, err := c.workerModel(raw); err != nil {
			return nil, err
		}
	}
	workspaceID, err := c.workspaceID(controlMCPString(args, "workspace_path"))
	if err != nil {
		return nil, err
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
	// A budget and a model given with the folder are set before the worker
	// is active, so it never runs without them.
	if raw, present := args["budget"]; present {
		if _, err := c.deployWorkerWithBudget(worker, map[string]any{"budget": raw}); err != nil {
			return nil, controlMCPToolFailure("worker %s created but budget not applied; not activated: %s", workerID, err)
		}
	}
	if raw, exists := args["model"]; exists && raw != nil {
		pinned, err := c.dispatch(http.MethodPut, "/v3/workers/"+workerID+"/model", nil, map[string]any{"expected_revision": controlMCPRevision(worker), "model": raw})
		if err != nil {
			return nil, controlMCPToolFailure("worker %s created but model not pinned; not activated: %s", workerID, err)
		}
		worker = controlMCPMap(pinned["worker"])
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
		// The schedule is enabled only once the worker is bound to its
		// workspace, so no scheduled run starts before it can work.
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
		revision := controlMCPRevision(worker)
		if raw, given := args["expected_revision"]; given {
			// The caller's revision (manage_workers update), so a stale
			// one is refused by the route instead of overwritten.
			value, ok := raw.(float64)
			if !ok || value < 1 || value != float64(uint64(value)) {
				return nil, controlMCPToolFailure("expected_revision must be the worker's revision (a whole number)")
			}
			revision = uint64(value)
		}
		response, err := c.dispatch(http.MethodPost, "/v3/workers/"+workerID+"/automations/"+scheduleID+"/"+strings.TrimSuffix(action, "_schedule"), nil, map[string]any{"expected_worker_revision": revision})
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

// controlMCPUsageSessionsKey names the usage summary's session count for its
// period: sessions_today, sessions_7d or sessions_30d.
func controlMCPUsageSessionsKey(period string) string {
	return "sessions_" + period
}

func controlMCPGetUsage(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:read"); err != nil {
		return nil, err
	}
	period := controlMCPString(args, "period")
	if period == "" {
		period = "today"
	}
	detail, _ := args["detail"].(bool)
	sessionLimit := "1"
	if detail {
		sessionLimit = "10"
	}
	response, err := c.dispatch(http.MethodGet, "/v3/usage", url.Values{"time_range": {period}, "session_limit": {sessionLimit}}, nil)
	if err != nil {
		return nil, err
	}
	rawSummary := controlMCPMap(response["summary"])
	summary := controlMCPPick(rawSummary, "total_cost_usd", "total_tokens", "input_tokens", "output_tokens", "cached_tokens", "total_turns", "running_now")
	// The sessions that used a model in the period (archived ones too), named
	// for the period so it is never read as what runs now: running_now is.
	summary[controlMCPUsageSessionsKey(period)] = rawSummary["total_sessions"]
	out := map[string]any{"period": period, "summary": summary}
	if media := controlMCPMap(response["media"]); media != nil {
		out["media"] = controlMCPPick(media, "total_count", "total_cost_usd", "image_count", "image_cost_usd", "video_count", "video_cost_usd", "audio_count", "audio_cost_usd", "has_unknown_pricing")
	}
	if limits := controlMCPMap(response["limits"]); limits != nil {
		out["limits"] = controlMCPLimits(limits)
	}
	subscription := map[string]bool{}
	for _, raw := range controlMCPList(response["by_provider"]) {
		if provider := controlMCPMap(raw); provider != nil {
			if id, _ := provider["provider"].(string); id != "" {
				subscription[id], _ = provider["is_subscription"].(bool)
			}
		}
	}
	models := []map[string]any{}
	billing := map[string]bool{}
	for i, raw := range controlMCPList(response["by_model"]) {
		if i >= 5 {
			break
		}
		if model := controlMCPMap(raw); model != nil {
			entry := controlMCPPick(model, "provider", "model", "cost_usd", "total_tokens", "turns")
			entry["billing"] = controlMCPBilling(model, subscription)
			if nominal, _ := model["codex_nominal_cost_usd"].(float64); nominal > 0 {
				entry["nominal_cost_usd"] = nominal
			}
			billing[entry["billing"].(string)] = true
			models = append(models, entry)
		}
	}
	out["top_models"] = models
	if note := controlMCPCostNote(rawSummary, billing); note != "" {
		summary["cost_note"] = note
	}
	if detail {
		controlMCPUsageDetail(response, subscription, out)
		if token, ok := ScopedTokenFromRequest(c.request); ok && token != nil && token.WorkerID != "" {
			out["workers_note"] = "Worker breakdown omitted: worker-bound tokens cannot enumerate workers."
			return out, nil
		}
		sortPeriod := "all_time"
		if period == "today" {
			sortPeriod = "today"
		}
		workers, err := c.workerUsageDetail(sortPeriod)
		if err != nil {
			return nil, err
		}
		out["workers"] = workers
		out["workers_note"] = "Worker totals are today (UTC) and all_time observed receipts, not rolling-period totals; sorted by " + sortPeriod + ".cost_usd descending. Unknown pricing is not free."
	}
	return out, nil
}

// controlMCPBilling explains a model's reported cost: subscription (a sign-in
// provider, no per-token cost), unpriced (tokens with no known price, so 0)
// or metered (priced per token).
func controlMCPBilling(model map[string]any, subscription map[string]bool) string {
	provider, _ := model["provider"].(string)
	if subscription[provider] {
		return "subscription"
	}
	cost, _ := model["cost_usd"].(float64)
	tokens, _ := model["total_tokens"].(float64)
	inputPrice, _ := model["input_price_per_million"].(float64)
	outputPrice, _ := model["output_price_per_million"].(float64)
	if cost == 0 && tokens > 0 && inputPrice == 0 && outputPrice == 0 {
		return "unpriced"
	}
	return "metered"
}

func controlMCPCostNote(summary map[string]any, billing map[string]bool) string {
	notes := []string{}
	if billing["subscription"] {
		notes = append(notes, "subscription (sign-in) providers report no per-token cost, so their use adds 0 to total_cost_usd")
	}
	if billing["unpriced"] {
		notes = append(notes, "unpriced models have no known price and count as 0")
	}
	if unknown, _ := summary["has_unknown_pricing"].(bool); unknown && !billing["unpriced"] {
		notes = append(notes, "some usage has no known price and counts as 0")
	}
	if len(notes) == 0 {
		return ""
	}
	return strings.Join(notes, "; ") + "."
}

// controlMCPLimits reports daily limits with "none" for an unset (0) limit,
// so no caller reads 0 as a zero-dollar cap.
func controlMCPLimits(limits map[string]any) map[string]any {
	out := controlMCPPick(limits, "enabled", "daily_cost_limit_usd", "daily_tokens_limit", "today_cost_usd", "today_tokens", "today_unpriced_receipts", "today_unpriced_estimate_usd", "limit_exceeded")
	if receipts, _ := limits["today_unpriced_receipts"].(float64); receipts > 0 {
		out["unpriced_note"] = "unpriced receipts count at today_unpriced_estimate_usd against the daily cost limit; work stops when today_cost_usd plus that estimate reaches it"
	}
	for _, key := range []string{"daily_cost_limit_usd", "daily_tokens_limit"} {
		if value, _ := limits[key].(float64); value <= 0 {
			out[key] = "none"
		}
	}
	if enabled, ok := limits["enabled"].(bool); ok && !enabled {
		out["note"] = "limits are off: nothing is capped until enabled"
	}
	return out
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
	return map[string]any{"limits": controlMCPLimits(controlMCPMap(response["limits"]))}, nil
}

// lastRunStatus is the recorded status of the session's last run once no run
// is active, read only after the session detail itself was dispatched (and so
// ownership-checked) for this caller. Empty when unknown.
func (c *controlMCPCall) lastRunStatus(sessionID string, detail map[string]any) string {
	if controlMCPMap(detail["active_run_intent"]) != nil || c.server == nil || c.server.sessions == nil {
		return ""
	}
	state, ok, err := c.server.sessions.GetSessionRunState(sessionID)
	if err != nil || !ok || state.Active {
		return ""
	}
	return state.Status
}

// controlMCPGetTheme reads one theme from Swarm's theme catalog: the given
// id, else the account's active theme, else Swarm's default.
func controlMCPGetTheme(c *controlMCPCall, args map[string]any) (any, error) {
	response, err := c.dispatch(http.MethodGet, "/v1/ui/settings", nil, nil)
	if err != nil {
		return nil, err
	}
	catalog := controlMCPMap(response["theme"])
	id, source := controlMCPString(args, "theme_id"), "theme_id"
	if id == "" {
		id, source = controlMCPMapString(catalog, "active_id"), "active"
	}
	if id == "" {
		id, source = controlMCPMapString(catalog, "default_theme_id"), "default"
	}
	for _, list := range []string{"custom_themes", "builtin_themes"} {
		for _, raw := range controlMCPList(catalog[list]) {
			theme := controlMCPMap(raw)
			if theme == nil || controlMCPMapString(theme, "id") != id {
				continue
			}
			return map[string]any{"id": id, "name": theme["name"], "palette": theme["palette"], "custom": list == "custom_themes", "source": source}, nil
		}
	}
	return nil, controlMCPToolFailure("no theme %q in Swarm's catalog", id)
}

func controlMCPMapString(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return value
}

// controlMCPUsageDetail adds what a usage page shows to get_usage's answer:
// the summary's remaining totals, every provider and model, each day, media
// and the most active sessions, all as Swarm's usage dashboard reports them.
func controlMCPUsageDetail(response map[string]any, subscription map[string]bool, out map[string]any) {
	summary, _ := out["summary"].(map[string]any)
	for key, value := range controlMCPPick(controlMCPMap(response["summary"]), "thinking_tokens", "codex_nominal_cost_usd", "total_sessions", "archived_sessions", "total_media_calls", "media_cost_usd") {
		summary[key] = value
	}
	providers := []map[string]any{}
	for _, raw := range controlMCPList(response["by_provider"]) {
		if provider := controlMCPMap(raw); provider != nil {
			providers = append(providers, controlMCPPick(provider, "provider", "display_name", "total_tokens", "input_tokens", "output_tokens", "cached_tokens", "thinking_tokens", "cost_usd", "codex_nominal_cost_usd", "is_subscription", "turns", "sessions", "models"))
		}
	}
	out["by_provider"] = providers
	models := []map[string]any{}
	for _, raw := range controlMCPList(response["by_model"]) {
		if model := controlMCPMap(raw); model != nil {
			entry := controlMCPPick(model, "provider", "model", "display_name", "total_tokens", "input_tokens", "output_tokens", "cached_tokens", "thinking_tokens", "cost_usd", "codex_nominal_cost_usd", "turns", "sessions")
			entry["billing"] = controlMCPBilling(model, subscription)
			models = append(models, entry)
		}
	}
	out["by_model"] = models
	days := []map[string]any{}
	for _, raw := range controlMCPList(response["daily"]) {
		if day := controlMCPMap(raw); day != nil {
			days = append(days, controlMCPPick(day, "date", "timestamp", "total_tokens", "input_tokens", "output_tokens", "cached_tokens", "cost_usd", "codex_nominal_cost_usd", "turns", "media_calls", "media_cost_usd"))
		}
	}
	out["daily"] = days
	if media := controlMCPMap(response["media"]); media != nil {
		out["media"] = controlMCPPick(media, "total_count", "total_cost_usd", "image_count", "image_cost_usd", "video_count", "video_cost_usd", "audio_count", "audio_cost_usd")
	}
	sessions := []map[string]any{}
	for _, raw := range controlMCPList(response["recent_sessions"]) {
		if session := controlMCPMap(raw); session != nil {
			sessions = append(sessions, controlMCPPick(session, "session_id", "title", "provider", "model", "total_tokens", "cost_usd", "turn_count", "last_active_at", "archived"))
		}
	}
	out["sessions"] = sessions
}

// controlMCPGetPlanUsage reads each sign-in plan's rate limits: ChatGPT's from
// ChatGPT itself, Claude Code's from its latest reported reading.
func controlMCPGetPlanUsage(c *controlMCPCall, _ map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:read"); err != nil {
		return nil, err
	}
	chatgpt, chatgptErr := c.dispatch(http.MethodGet, "/v1/codex/account/usage", nil, nil)
	claude, claudeErr := c.dispatch(http.MethodGet, "/v1/claudecode/usage", nil, nil)
	return controlMCPPlanUsage(chatgpt, chatgptErr, claude, claudeErr), nil
}

func controlMCPPlanUsage(chatgpt map[string]any, chatgptErr error, claude map[string]any, claudeErr error) map[string]any {
	out := map[string]any{}
	if chatgptErr != nil {
		out["chatgpt"] = map[string]any{"connected": false, "reason": chatgptErr.Error()}
	} else {
		plan := map[string]any{"connected": true, "plan_type": chatgpt["plan_type"]}
		limit := controlMCPMap(chatgpt["rate_limit"])
		plan["limit_reached"], _ = limit["limit_reached"].(bool)
		windows := []map[string]any{}
		for _, key := range []string{"primary_window", "secondary_window"} {
			if window := controlMCPMap(limit[key]); window != nil {
				windows = append(windows, controlMCPPick(window, "used_percent", "limit_window_seconds", "reset_at", "reset_after_seconds"))
			}
		}
		plan["windows"] = windows
		if credits := controlMCPMap(chatgpt["rate_limit_reset_credits"]); credits != nil {
			plan["reset_credits_available"] = credits["available_count"]
		}
		out["chatgpt"] = plan
	}
	if claudeErr != nil {
		out["claude_code"] = map[string]any{"connected": false, "reason": claudeErr.Error()}
	} else {
		cli := controlMCPMap(claude["cli"])
		loggedIn, _ := cli["logged_in"].(bool)
		plan := map[string]any{"connected": loggedIn, "subscription_type": cli["subscription_type"]}
		if !loggedIn {
			plan["reason"] = cli["reason"]
		} else {
			for key, value := range controlMCPClaudeCodeAccount(cli) {
				plan[key] = value
			}
		}
		if limits := controlMCPMap(claude["rate_limits"]); limits != nil {
			plan["rate_limits"] = controlMCPPick(limits, "status", "resets_at", "is_using_overage", "windows", "observed_at")
		}
		if accounts, ok := claude["accounts"]; ok {
			plan["accounts"] = accounts
		}
		if summary, ok := claude["limit_summary"]; ok {
			plan["limit_summary"] = summary
		}
		out["claude_code"] = plan
	}
	return out
}

// controlMCPClaudeCodeAccountFields are the signed-in Claude account's
// identity fields of GET /v1/claudecode/usage's cli: never a credential.
var controlMCPClaudeCodeAccountFields = []string{"account_email", "account_uuid", "organization_name", "organization_uuid"}

// controlMCPClaudeCodeAccount picks the account fields the route reported;
// a field it left out stays out.
func controlMCPClaudeCodeAccount(cli map[string]any) map[string]any {
	return controlMCPPick(cli, controlMCPClaudeCodeAccountFields...)
}
