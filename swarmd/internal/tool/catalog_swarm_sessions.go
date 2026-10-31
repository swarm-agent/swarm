package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
)

// The swarm_* session and workspace tools' inputs and answer fields, taken by
// manage_sessions and manage_workspace (docs/tool-catalog.md: a catalog
// action accepts every input of the tool it replaced). Each is served by the
// one implementation that tool used, through the API server.

// SessionSwarmService runs the swarm_* session tools' own code for one part
// of a manage_sessions answer (api.Server.ManageSessionSwarm): get, list,
// start_session, send_message, state and stop_report.
type SessionSwarmService interface {
	ManageSessionSwarm(ctx context.Context, p identity.Principal, part string, args map[string]any) (map[string]any, error)
}

// WorkspaceSwarmService runs swarm_add_workspace's (add) or
// swarm_create_workspace's (create) own handler (api.Server.ManageWorkspaceSwarm).
type WorkspaceSwarmService interface {
	ManageWorkspaceSwarm(ctx context.Context, p identity.Principal, part string, args map[string]any) (map[string]any, error)
}

func (r *Runtime) sessionSwarmService() (SessionSwarmService, bool) {
	service, ok := r.getProjectTaskLifecycleService().(SessionSwarmService)
	return service, ok
}

func (r *Runtime) workspaceSwarmService() (WorkspaceSwarmService, bool) {
	service, ok := r.getProjectTaskLifecycleService().(WorkspaceSwarmService)
	return service, ok
}

// manageSessionsSwarmArgs maps swarm_send_message's content onto
// send_message's prompt (the action's own input wins), before the call runs
// here or goes to a linked machine.
func manageSessionsSwarmArgs(action string, args map[string]any) {
	if action != "send_message" {
		return
	}
	if content, ok := args["content"]; ok {
		if strings.TrimSpace(stringValue(args["prompt"])) == "" {
			args["prompt"] = content
		}
		delete(args, "content")
	}
}

// manageSessionsSwarmRoute reports a Swarm Control call that runs a swarm_*
// session tool's handler instead of the runtime's own action, and which:
//   - create with plan, attachments, project_id or access:
//     swarm_start_session (it refuses per-session access, as it always has);
//   - send_message that starts a run with attachments or a
//     client_request_id: swarm_send_message, which takes attachments and
//     whose client_request_id is an idempotency key (the runtime's own
//     send_message keys only a note, trigger false, by it).
//
// A conversation's call never reaches here with those inputs
// (CatalogConversationArgs) and runs the runtime's own action.
func manageSessionsSwarmRoute(action string, control bool, args map[string]any) (string, error) {
	if !control {
		return "", nil
	}
	switch action {
	case "create":
		for _, key := range []string{"plan", "attachments", "project_id", "access"} {
			if _, given := args[key]; given {
				return "start_session", nil
			}
		}
	case "send_message":
		triggers, err := sessionMessageTrigger(args)
		if err != nil {
			return "", err
		}
		_, attachments := args["attachments"]
		if !triggers {
			if attachments {
				return "", errors.New("attachments go with a message that starts a run; trigger false saves a note without files")
			}
			return "", nil
		}
		if attachments || strings.TrimSpace(stringValue(args["client_request_id"])) != "" {
			return "send_message", nil
		}
	}
	return "", nil
}

// manageSessionsSwarm runs a Swarm Control create or send_message through
// its swarm_* handler (manageSessionsSwarmRoute), after the same session
// checks the runtime's own action makes: a create names a registered
// workspace (or, for an orchestrator session, a project); a message goes to
// an owned, unarchived session that is not a task's.
func (r *Runtime) manageSessionsSwarm(ctx context.Context, scope WorkspaceScope, part string, args map[string]any) (string, error) {
	service, ok := r.sessionSwarmService()
	if !ok {
		return "", errors.New("manage_sessions: this option is unavailable on this machine")
	}
	switch part {
	case "start_session":
		if strings.TrimSpace(stringValue(args["workspace_path"])) != "" {
			var err error
			if scope, err = r.createSessionScope(scope, args); err != nil {
				return "", err
			}
		}
	case "send_message":
		sessionID := strings.TrimSpace(stringValue(args["session_id"]))
		if strings.TrimSpace(stringValue(args["prompt"])) == "" {
			return "", errors.New("send_message requires prompt")
		}
		session, archived, err := r.ownedManageSession(scope, sessionID)
		if err != nil {
			return "", err
		}
		if archived {
			return "", fmt.Errorf("cannot send message to archived session %s; unarchive first", sessionID)
		}
		if err := r.rejectTaskSessionContinuation(scope, session); err != nil {
			return "", err
		}
		args = withKey(args, "content", args["prompt"])
	}
	out, err := service.ManageSessionSwarm(ctx, scope.Principal, part, args)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}

// manageSessionsSwarmFields adds to the runtime's own answer the fields its
// swarm_* tool answered, from that tool's code; the action's own fields
// win. It runs for every caller, so the Orchestrator and Swarm Control get
// one answer:
//   - get: swarm_get_session's (session, messages, the next chunk of a cut
//     message; waited for Swarm Control's wait_seconds), for a live session;
//   - list: swarm_list_sessions' sessions (limit, project_id);
//   - create, send_message: the state they answered after starting work
//     (session, messages, started);
//   - stop: swarm_stop_run's report (run_status, summary, session).
//
// Without the API server (a runtime with no swarm_* code to run) the answer
// is the action's own. A part that cannot be read (a session the session
// route does not serve) is named in state_error; the action's answer stands.
func (r *Runtime) manageSessionsSwarmFields(ctx context.Context, scope WorkspaceScope, action string, args map[string]any, answer string) (string, error) {
	service, ok := r.sessionSwarmService()
	if !ok {
		return answer, nil
	}
	var own map[string]any
	if err := json.Unmarshal([]byte(answer), &own); err != nil || own == nil {
		return answer, nil
	}
	sessionID := strings.TrimSpace(stringValue(own["session_id"]))
	if sessionID == "" {
		sessionID = strings.TrimSpace(stringValue(args["session_id"]))
	}
	var part string
	call := map[string]any{"session_id": sessionID}
	switch action {
	case "get":
		if archived, _ := own["archived"].(bool); archived {
			// swarm_get_session reads only live sessions (an archived one is
			// not found there): an archived session is its own record.
			return answer, nil
		}
		part = "get"
		for _, key := range []string{"messages", "detail_chars", "message_id", "message_seq", "content_offset", "wait_seconds"} {
			if value, given := args[key]; given {
				call[key] = value
			}
		}
	case "list":
		part, call = "list", map[string]any{"limit": args["limit"], "project_id": args["project_id"]}
	case "create", "send_message":
		if sessionID == "" {
			return answer, nil
		}
		part, call["run_id"] = "state", own["run_id"]
	case "stop", "pause":
		part, call["run_id"], call["status"] = "stop_report", own["run_id"], own["status"]
		if scope.ControlCaller {
			// swarm_stop_run holds the call until the run ends; the
			// Orchestrator keeps its own watch model.
			call["wait_seconds"] = float64(10)
		}
	default:
		return answer, nil
	}
	fields, err := service.ManageSessionSwarm(ctx, scope.Principal, part, call)
	if err != nil {
		// The action's own answer stands; the part that could not be read
		// is named, never silently missing.
		fields = map[string]any{"state_error": truncateRunes(err.Error(), 300)}
	}
	for key, value := range fields {
		if _, taken := own[key]; !taken {
			own[key] = value
		}
	}
	raw, err := json.Marshal(own)
	return string(raw), err
}

// manageWorkspaceSwarmCreate runs a Swarm Control manage_workspace create
// given swarm_add_workspace's path (name its display name) or, with name
// only, swarm_create_workspace's new repository, through that tool's own
// handler. Inputs of the runtime's own create (workspace_path,
// workspace_name, intent) do not go with them: the call is refused naming
// them, nothing dropped. handled is false for the runtime's own create.
func (r *Runtime) manageWorkspaceSwarmCreate(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, bool, error) {
	_, path := args["path"]
	_, name := args["name"]
	if !path && !name {
		return "", false, nil
	}
	var own []string
	for _, key := range []string{"workspace_path", "workspace_name", "intent", "workspace_id", "workspace_generation", "theme_id"} {
		if _, given := args[key]; given {
			own = append(own, key)
		}
	}
	if len(own) > 0 {
		return "", true, fmt.Errorf("%s do not go with path or name: register a folder with path (and name), or with workspace_path (and workspace_name, intent)", strings.Join(own, ", "))
	}
	service, ok := r.workspaceSwarmService()
	if !ok {
		return "", true, errors.New("manage_workspace: this option is unavailable on this machine")
	}
	part := "add"
	if !path {
		part = "create"
	}
	out, err := service.ManageWorkspaceSwarm(ctx, scope.Principal, part, args)
	if err != nil {
		return "", true, err
	}
	raw, err := json.Marshal(out)
	return string(raw), true, err
}

// withKey is args with key set, the caller's map unchanged.
func withKey(args map[string]any, key string, value any) map[string]any {
	out := make(map[string]any, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	out[key] = value
	return out
}
