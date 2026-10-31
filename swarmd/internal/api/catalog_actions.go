package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// CatalogActionsPath is the route a linked home's catalog tools reach on this
// machine for the actions ManageCatalogAction serves: POST
// /v3/catalog-actions/{tool}/{action} runs this machine's ManageCatalogAction
// with the caller's own scopes. Only actions that reach a linked machine are
// served.
const CatalogActionsPath = "/v3/catalog-actions"

// catalogSwarmHandler runs one catalog action with the swarm_* tool handler
// that has the same effect.
type catalogSwarmHandler func(c *controlMCPCall, args map[string]any) (any, error)

// catalogSwarmHandlers are the catalog actions whose one implementation is a
// swarm_* tool's handler, by tool and action.
var catalogSwarmHandlers = map[string]map[string]catalogSwarmHandler{
	tool.ManageFleetToolName: {
		"status":   controlMCPStatus,
		"activity": controlMCPActivity,
	},
	tool.ManageMediaToolName: {
		"models":   controlMCPListMediaModels,
		"generate": controlMCPRequestMedia,
		"retry": func(c *controlMCPCall, args map[string]any) (any, error) {
			call := map[string]any{}
			for key, value := range args {
				if key != "job_id" {
					call[key] = value
				}
			}
			call["retry_of"] = args["job_id"]
			return controlMCPRequestMedia(c, call)
		},
		"get":  controlMCPGetJob,
		"save": controlMCPSaveMedia,
		"read": controlMCPReadMedia,
	},
	tool.ManageThemeToolName: {
		"get": controlMCPGetTheme,
	},
	tool.ManageDesignToolName: {
		"create": controlMCPDesign,
		"edit":   controlMCPDesign,
	},
	"manage_projects": {
		tool.TaskChangesAction: catalogTaskChanges,
		tool.TaskFileAction:    catalogTaskFile,
		// update with nothing to change only (tool.ProjectContextRetryCall);
		// every other update is the Orchestrator's own project edit.
		"update": catalogProjectContextRetry,
	},
}

// catalogConversationScopes are the route scopes a conversation's call of a
// tool's actions also needs beyond the action's own: the project a media job
// or design goes in is read and written through the project routes.
var catalogConversationScopes = map[string][]string{
	tool.ManageMediaToolName:  {"sessions:read", "projects:read", "projects:write"},
	tool.ManageDesignToolName: {"sessions:read", "projects:read", "projects:write"},
}

// catalogServedAction is the declared action ManageCatalogAction serves.
func catalogServedAction(toolName, action string) (tool.CatalogTool, tool.CatalogAction, catalogSwarmHandler, bool) {
	catalog, ok := tool.CatalogToolNamed(toolName)
	if !ok {
		return tool.CatalogTool{}, tool.CatalogAction{}, nil, false
	}
	declared, ok := catalog.Action(action)
	if !ok || declared.Action != action {
		return tool.CatalogTool{}, tool.CatalogAction{}, nil, false
	}
	handler, ok := catalogSwarmHandlers[toolName][action]
	return catalog, declared, handler, ok
}

// ManageCatalogAction is the one service behind the catalog actions whose
// implementation is a swarm_* tool's handler (catalogSwarmHandlers): the
// Orchestrator (who is its session), Swarm Control (who is "key:ID"; its key
// scopes ride in ctx, tool.ControlScopesFromContext) and a linked home (POST
// /v3/catalog-actions/{tool}/{action}) all run it. It runs the handler on an
// internal request that carries the caller's own key narrowed to the scopes
// it holds, only those scopes (a linked caller), or only the action's route
// scopes and level (a conversation): never wider. With machine in args the
// action runs on that linked machine's own ManageCatalogAction and its
// answer comes back as it came.
func (s *Server) ManageCatalogAction(ctx context.Context, p identity.Principal, who, toolName string, args map[string]any) (any, error) {
	if !p.Valid() || s.sessions == nil {
		return nil, fmt.Errorf("%s requires a session principal", toolName)
	}
	catalog, declared, handler, ok := catalogServedAction(toolName, controlMCPString(args, "action"))
	if !ok {
		return nil, fmt.Errorf("%s %s is not served here", toolName, controlMCPString(args, "action"))
	}
	held, control := tool.ControlScopesFromContext(ctx)
	if control {
		if !catalogScopesHeld(func(scope string) bool { return nativeJobScopeHeld(held, scope) }, declared) {
			return nil, controlMCPToolFailure("this connection lacks %s", strings.Join(declared.APIScopes, " or "))
		}
	} else {
		session, found, err := s.sessions.Store().GetSession(who)
		if err != nil {
			return nil, err
		}
		if !found || session.AccountScopeID != p.AccountScopeID || session.UserID != p.UserID {
			return nil, fmt.Errorf("%s session principal mismatch", toolName)
		}
		for _, key := range []string{"task_id", "project_task_id"} {
			if task, _ := session.Metadata[key].(string); task != "" {
				return nil, fmt.Errorf("%s %s is the home Orchestrator's; a task's session cannot run it", toolName, declared.Action)
			}
		}
		// A worker's job session acts only within what was accepted for
		// it, and no other agent runs these account-wide handlers: only the
		// home Orchestrator's own conversation does.
		if worker, _ := session.Metadata["worker_id"].(string); strings.TrimSpace(worker) != "" {
			return nil, fmt.Errorf("%s %s is the home Orchestrator's; a worker's session cannot run it", toolName, declared.Action)
		}
		if !agentruntime.IsOrchestratorAgentName(tool.CatalogAgentFromContext(ctx)) {
			return nil, fmt.Errorf("%s %s is the home Orchestrator's; another agent cannot run it", toolName, declared.Action)
		}
	}
	clean := map[string]any{}
	inputs := declared.Inputs
	if control {
		// A Swarm Control caller's own inputs (held waits) reach the
		// handler for it only.
		inputs = append(append([]string(nil), inputs...), declared.ControlInputs...)
	}
	for _, key := range inputs {
		if value, ok := args[key]; ok && key != "machine" {
			clean[key] = value
		}
	}
	validate := map[string]any{"action": declared.Action}
	for key, value := range clean {
		validate[key] = value
	}
	if err := controlMCPValidateArgs(catalog.InputSchema(declared), validate); err != nil {
		return nil, err
	}
	if machine := controlMCPString(args, "machine"); machine != "" {
		if declared.Reach != tool.CatalogReachAny {
			return nil, fmt.Errorf("%s %s runs on this machine only", toolName, declared.Action)
		}
		if !control && declared.Level != tool.CatalogLevelRead {
			// Under linked work ask, a conversation's change there (media
			// or a design: work that costs money on that machine) needs
			// the person's approval, and these tools have no approval
			// prompt for it: refused with the reason, as manage_jobs does.
			// A Swarm Control caller's person approves in their own chat.
			access, err := s.sessions.Store().GetAccountPermissions(p.AccountScopeID)
			if err != nil {
				return nil, err
			}
			if access.Link(machine).Work == store.RuleAsk {
				return nil, fmt.Errorf("linked.%s.work is ask: %s %s starts work on %s only with the person's approval, and %s has no approval prompt for it; the person can set the rule to auto on the Permissions page", machine, toolName, declared.Action, machine, toolName)
			}
		}
		return s.CallFleetWork(ctx, p, machine, toolName, declared.Action, clean)
	}
	if !control {
		// A conversation's call holds the action's route scopes and level,
		// plus the project routes its tool's handlers go through.
		declared.APIScopes = append(append([]string(nil), declared.APIScopes...), catalogConversationScopes[toolName]...)
	}
	call, err := s.setupCall(ctx, p, who, control, held, declared)
	if err != nil {
		return nil, err
	}
	out, err := handler(call, clean)
	if err != nil {
		var refused controlMCPToolError
		if errors.As(err, &refused) {
			refused.message = strings.TrimSpace(refused.message)
			return nil, refused
		}
		return nil, err
	}
	return out, nil
}

// catalogProjectContextRetry is manage_projects update with nothing to change:
// swarm_retry_project_context's handler writes project.md again.
func catalogProjectContextRetry(c *controlMCPCall, args map[string]any) (any, error) {
	if !tool.ProjectContextRetryCall(args) {
		return nil, controlMCPToolFailure("update writes project.md again only when it names the project (id) and changes nothing else")
	}
	return controlMCPRetryProjectContext(c, map[string]any{"project_id": controlMCPString(args, "id")})
}

// catalogTaskSession is a task's session: the one swarm_get_changes and
// swarm_read_file read. session_id names a session directly, as those tools
// take it; it is given instead of a task, never with one.
func catalogTaskSession(c *controlMCPCall, args map[string]any) (string, error) {
	if session := strings.TrimSpace(controlMCPString(args, "session_id")); session != "" {
		if strings.TrimSpace(controlMCPString(args, "task_id")) != "" {
			return "", controlMCPToolFailure("give task_id (with project_id) or session_id, not both")
		}
		if !controlMCPSafeID.MatchString(session) {
			return "", controlMCPToolFailure("session_id is not a valid id")
		}
		return session, nil
	}
	projectID, err := controlMCPID(args, "project_id")
	if err != nil {
		return "", err
	}
	taskID, err := controlMCPID(args, "task_id")
	if err != nil {
		return "", err
	}
	task, err := c.task(projectID, taskID)
	if err != nil {
		return "", err
	}
	session, _ := task["session_id"].(string)
	if strings.TrimSpace(session) == "" {
		return "", controlMCPToolFailure("task %s has not started: it has no working folder yet", taskID)
	}
	return session, nil
}

// catalogTaskChanges is get_task_changes: swarm_get_changes for the task's
// session, paged as a JSON text page when it is long (page_offset,
// page_digest).
func catalogTaskChanges(c *controlMCPCall, args map[string]any) (any, error) {
	session, err := catalogTaskSession(c, args)
	if err != nil {
		return nil, err
	}
	call := map[string]any{"session_id": session}
	for _, key := range []string{"path", "diff_chars"} {
		if value, ok := args[key]; ok {
			call[key] = value
		}
	}
	out, err := controlMCPGetChanges(c, call)
	if err != nil {
		return nil, err
	}
	return nativeJobPage(out, args, false)
}

// catalogTaskFile is read_task_file: swarm_read_file in the task's session's
// working folder.
func catalogTaskFile(c *controlMCPCall, args map[string]any) (any, error) {
	if folder := strings.TrimSpace(controlMCPString(args, "workspace_path")); folder != "" {
		// swarm_read_file's other folder: a registered workspace, given
		// instead of a task or session.
		if strings.TrimSpace(controlMCPString(args, "task_id")) != "" || strings.TrimSpace(controlMCPString(args, "session_id")) != "" {
			return nil, controlMCPToolFailure("give a task, a session_id or a workspace_path, only one")
		}
		call := map[string]any{"workspace_path": folder, "path": args["path"]}
		if strings.TrimSpace(controlMCPString(call, "path")) == "" {
			call["path"] = "."
		}
		return controlMCPReadFile(c, call)
	}
	session, err := catalogTaskSession(c, args)
	if err != nil {
		return nil, err
	}
	call := map[string]any{"session_id": session, "path": args["path"]}
	if strings.TrimSpace(controlMCPString(call, "path")) == "" {
		call["path"] = "."
	}
	return controlMCPReadFile(c, call)
}

// catalogLinkedScopes are the API scopes of a tool's actions that reach a
// linked machine: what a linked home's link holds on CatalogActionsPath.
func catalogLinkedScopes(toolName string) []string {
	seen := map[string]bool{}
	var out []string
	catalog, _ := tool.CatalogToolNamed(toolName)
	for _, action := range catalog.Actions {
		if _, served := catalogSwarmHandlers[toolName][action.Action]; !served || action.Reach != tool.CatalogReachAny {
			continue
		}
		// The link holds the project routes a media job or design goes
		// through too, as a conversation's call does.
		for _, scope := range append(append([]string(nil), action.APIScopes...), catalogConversationScopes[toolName]...) {
			if !seen[scope] {
				seen[scope] = true
				out = append(out, scope)
			}
		}
	}
	return out
}

// handleCatalogActions serves POST /v3/catalog-actions/{tool}/{action}: one
// action ManageCatalogAction serves that reaches a linked machine.
func (s *Server) handleCatalogActions(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, CatalogActionsPath), "/")
	toolName, action, _ := strings.Cut(rest, "/")
	if _, served := catalogSwarmHandlers[toolName]; !served {
		writeError(w, http.StatusNotFound, fmt.Errorf("no catalog actions of %q are served here", toolName))
		return
	}
	if _, declared, _, ok := catalogServedAction(toolName, action); !ok || declared.Reach != tool.CatalogReachAny {
		writeError(w, http.StatusNotFound, fmt.Errorf("%s %q is not served over a link", toolName, action))
		return
	}
	s.serveCatalogLinked(w, r, CatalogActionsPath+"/"+toolName, toolName, catalogLinkedScopes(toolName), func(ctx context.Context, p identity.Principal, args map[string]any) (any, error) {
		return s.ManageCatalogAction(ctx, p, "", toolName, args)
	})
}
