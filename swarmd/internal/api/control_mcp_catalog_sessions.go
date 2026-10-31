package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// manage_sessions in the catalog (tool.manageSessionsCatalog): the API
// server's part. resolve_permission and set_model run the functions behind
// swarm_resolve_permission and swarm_set_session_model (and through them the
// session permission and model-profile routes); machine= reaches a linked
// machine's own catalog handler through CatalogLinkedPath.

// CatalogLinkedPath is the route a linked home calls to run a catalog tool
// on this machine: this machine's own handler (ExecuteControlCatalog), never
// a second implementation. Only a home over the link may call it.
const CatalogLinkedPath = "/v3/tool-catalog"

// catalogLinkedTools are the catalog tools a home runs here through
// CatalogLinkedPath. Other catalog tools keep their own linked routes.
var catalogLinkedTools = map[string]bool{"manage_sessions": true, "manage_workspace": true}

// ManageSessionControl runs manage_sessions resolve_permission or set_model
// for p. The internal request carries only the session scopes (for a Swarm
// Control caller, only those its key holds), so the routes' own checks see
// the caller, never a wider set.
func (s *Server) ManageSessionControl(ctx context.Context, p identity.Principal, action string, args map[string]any) (map[string]any, error) {
	c, err := s.sessionControlCall(ctx, p)
	if err != nil {
		return nil, err
	}
	if err := c.requireTokenScope("sessions:write"); err != nil {
		return nil, err
	}
	pick := func(keys ...string) map[string]any {
		out := map[string]any{}
		for _, key := range keys {
			if value, ok := args[key]; ok && value != nil {
				out[key] = value
			}
		}
		return out
	}
	var out any
	switch action {
	case tool.ManageSessionsResolvePermissionAction:
		call := pick("session_id", "permission_id", "reason", "answer", "result")
		call["action"] = controlMCPString(args, "decision")
		out, err = controlMCPResolvePermission(c, call)
	case tool.ManageSessionsSetModelAction:
		if s.model == nil {
			return nil, errors.New("the model catalog is not configured on this machine")
		}
		out, err = controlMCPSetSessionModel(c, pick("session_id", "provider", "model", "thinking"))
	default:
		return nil, fmt.Errorf("manage_sessions %s is not a session control action", action)
	}
	if err != nil {
		return nil, errors.New(controlMCPErrorText(err))
	}
	answer, _ := out.(map[string]any)
	return answer, nil
}

// sessionControlCall is the internal call the manage_sessions and
// manage_workspace services run the swarm_* session functions on: the
// session scopes only (for a Swarm Control caller, only those its key
// holds), so the routes' own checks see the caller, never a wider set.
// A cut message's next names manage_sessions get.
func (s *Server) sessionControlCall(ctx context.Context, p identity.Principal) (*controlMCPCall, error) {
	if s == nil || !p.Valid() {
		return nil, identity.ErrPrincipalRequired
	}
	scopes := []string{"sessions:read", "sessions:write"}
	if held, ok := tool.ControlScopesFromContext(ctx); ok {
		scopes = workflowScopesHeld(scopes, held)
	}
	req, err := http.NewRequestWithContext(identity.ContextWithPrincipal(ctx, p), http.MethodPost, controlMCPPath, nil)
	if err != nil {
		return nil, err
	}
	req = requestWithScopedToken(req, &pebblestore.ScopedTokenRecord{Scopes: scopes})
	return &controlMCPCall{request: req, next: s.internalRouteHandler(), server: s, sessionNext: map[string]any{"tool": "manage_sessions", "action": "get"}}, nil
}

// ManageSessionSwarm runs the swarm_* session tools' own code for one part
// of a manage_sessions answer (tool.SessionSwarmService), so the catalog
// action takes those tools' inputs and answers their fields with one
// implementation:
//   - get: swarm_get_session (session, messages, the next chunk of a cut
//     message, a held wait);
//   - list: swarm_list_sessions (sessions; project_id's conversations);
//   - start_session: swarm_start_session (a plan, attachments, an
//     orchestrator session's project; per-session access stays refused);
//   - send_message: swarm_send_message (idempotent by client_request_id,
//     attachments);
//   - state: the state swarm_start_session and swarm_send_message answer
//     after starting work, for a session the action already changed;
//   - stop_report: swarm_stop_run's report for a run the action stopped.
func (s *Server) ManageSessionSwarm(ctx context.Context, p identity.Principal, part string, args map[string]any) (map[string]any, error) {
	c, err := s.sessionControlCall(ctx, p)
	if err != nil {
		return nil, err
	}
	pick := func(keys ...string) map[string]any {
		out := map[string]any{}
		for _, key := range keys {
			if value, ok := args[key]; ok && value != nil {
				out[key] = value
			}
		}
		return out
	}
	sessionID := controlMCPString(args, "session_id")
	var out any
	switch part {
	case "get":
		out, err = controlMCPGetSession(c, pick("session_id", "messages", "detail_chars", "message_id", "message_seq", "content_offset", "wait_seconds"))
	case "list":
		call := pick("project_id")
		if limit := controlMCPInt(args, "limit", 0); limit > 0 {
			call["limit"] = float64(min(limit, controlMCPMaxListItems))
		}
		out, err = controlMCPListSessions(c, call)
	case "start_session":
		out, err = controlMCPStartSession(c, pick("workspace_path", "project_id", "prompt", "plan", "agent", "title", "provider", "model", "thinking", "wait_seconds", "access", "attachments"))
	case "send_message":
		out, err = controlMCPSendMessage(c, pick("session_id", "content", "client_request_id", "wait_seconds", "attachments"))
	case "state":
		state, stateErr := c.sessionState(sessionID, 3, 0)
		if stateErr != nil {
			return map[string]any{"state_error": "work started, but its state could not be read; call manage_sessions get"}, nil
		}
		if runID := controlMCPString(args, "run_id"); runID != "" {
			state["started"] = map[string]any{"run_id": runID, "accepted": true}
		}
		out = state
	case "stop_report":
		out, err = c.catalogStopReport(sessionID, controlMCPString(args, "run_id"), controlMCPString(args, "status"), controlMCPInt(args, "wait_seconds", 0))
	default:
		return nil, fmt.Errorf("manage_sessions has no swarm_* part %q", part)
	}
	if err != nil {
		return nil, errors.New(controlMCPErrorText(err))
	}
	answer, _ := out.(map[string]any)
	return answer, nil
}

// catalogStopReport is swarm_stop_run's report (controlMCPStopReport) for a
// run manage_sessions stop already stopped: the run's status, the plan's
// state, the session and its last message. waitSeconds holds a Swarm
// Control call until the run ends, as swarm_stop_run waits; a conversation
// passes 0. A session with no active run reports not_running.
func (c *controlMCPCall) catalogStopReport(sessionID, runID, status string, waitSeconds int) (map[string]any, error) {
	if status == "not_running" || runID == "" {
		state, err := c.sessionState(sessionID, 1, 0)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"session_id": sessionID, "run_status": "not_running", "summary": "The session has no active run; nothing was stopped."}
		if session := controlMCPMap(state["session"]); session != nil {
			out["session"] = session
		}
		if messages, _ := state["messages"].([]map[string]any); len(messages) > 0 {
			out["last_message"] = messages[len(messages)-1]
		}
		return out, nil
	}
	waited, waitErr := c.waitForSession(sessionID, min(waitSeconds, controlMCPStopWaitSeconds))
	state, stateErr := c.sessionState(sessionID, 1, 0)
	if waitErr != nil || stateErr != nil {
		return map[string]any{"session_id": sessionID, "run_id": runID, "run_status": "stop_requested", "summary": "Stop was requested; the final state could not be read. Check with manage_sessions get."}, nil
	}
	return controlMCPStopReport(sessionID, runID, waited, state), nil
}

// ManageWorkspaceSwarm runs swarm_add_workspace's or swarm_create_workspace's
// own handler for manage_workspace create over Swarm Control
// (tool.WorkspaceSwarmService): add registers an existing folder by path
// (name its display name), create makes a new, empty repository by name in
// this machine's workspace folder.
func (s *Server) ManageWorkspaceSwarm(ctx context.Context, p identity.Principal, part string, args map[string]any) (map[string]any, error) {
	c, err := s.sessionControlCall(ctx, p)
	if err != nil {
		return nil, err
	}
	call := map[string]any{}
	for _, key := range []string{"path", "name"} {
		if value, ok := args[key]; ok && value != nil {
			call[key] = value
		}
	}
	var out any
	switch part {
	case "add":
		out, err = controlMCPAddWorkspace(c, call)
	case "create":
		out, err = controlMCPCreateWorkspace(c, call)
	default:
		return nil, fmt.Errorf("manage_workspace has no swarm_* part %q", part)
	}
	if err != nil {
		return nil, errors.New(controlMCPErrorText(err))
	}
	answer, _ := out.(map[string]any)
	return answer, nil
}

// controlMCPErrorText is the text of an error a swarm_* function returns.
func controlMCPErrorText(err error) string {
	var toolErr controlMCPToolError
	if errors.As(err, &toolErr) {
		return toolErr.Error()
	}
	return err.Error()
}

// RegisteredWorkspacePath returns path as one of p's registered workspaces
// (the catalog behind manage_workspace list and swarm_list_workspaces).
func (s *Server) RegisteredWorkspacePath(p identity.Principal, path string) (string, error) {
	if s == nil || s.workspace == nil {
		return "", errors.New("workspace service unavailable")
	}
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("workspace_path %q must be the absolute path of a registered workspace", path)
	}
	clean := filepath.Clean(path)
	entries, err := s.workspace.ListKnownForPrincipal(p, 2000)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if filepath.Clean(entry.Path) == clean {
			return clean, nil
		}
	}
	return "", fmt.Errorf("workspace_path %q is not a registered workspace on this machine (manage_workspace list)", path)
}

// CallFleetCatalog runs a catalog tool's action on a linked machine through
// CatalogLinkedPath and returns that machine's envelope. The linked work
// rule decides as for every linked call: off refuses; ask refuses actions
// that start a run there (create with a prompt, a triggering send_message),
// which wait for the person on that machine instead.
func (s *Server) CallFleetCatalog(ctx context.Context, p identity.Principal, machine, name string, args map[string]any) (json.RawMessage, error) {
	if !catalogLinkedTools[name] {
		return nil, fmt.Errorf("%s does not run on a linked machine through the catalog route", name)
	}
	if s.sessions == nil {
		return nil, errors.New("session service unavailable")
	}
	if _, nested := args["machine"]; nested {
		return nil, errors.New("nested machine routing is not allowed")
	}
	access, err := s.sessions.Store().GetAccountPermissions(p.AccountScopeID)
	if err != nil {
		return nil, err
	}
	rule := access.Link(machine).Work
	if !pebblestore.LinkedWorkOn(rule) {
		return nil, fmt.Errorf("linked.%s.work is off", machine)
	}
	if rule == pebblestore.RuleAsk && catalogLinkedStartsRun(name, args) {
		return nil, fmt.Errorf("linked.%s.work is ask: starting a session's run there needs the person; ask them, or set it to auto", machine)
	}
	client, linked, _, err := s.fleetMachineClient(p.AccountScopeID, machine)
	if err != nil {
		return nil, errors.New("linked machine connection unavailable")
	}
	var result json.RawMessage
	err = client.JSON(ctx, http.MethodPost, CatalogLinkedPath+"/"+name, args, &result)
	s.noteFleetContact(linked, err)
	if err != nil {
		return nil, fmt.Errorf("linked machine %s refused %s %s: %w", machine, name, controlMCPString(args, "action"), err)
	}
	return result, nil
}

// catalogLinkedStartsRun reports whether a linked call starts agent work on
// the machine.
func catalogLinkedStartsRun(name string, args map[string]any) bool {
	if name != "manage_sessions" {
		return false
	}
	switch controlMCPString(args, "action") {
	case "create":
		// A prompt or a plan (swarm_start_session's) starts the session.
		_, plan := args["plan"]
		return controlMCPString(args, "prompt") != "" || plan
	case "send_message":
		for _, key := range []string{"trigger", "trigger_run"} {
			if value, ok := args[key].(bool); ok && !value {
				return false
			}
		}
		return true
	}
	return false
}

// handleCatalogLinked serves CatalogLinkedPath/{tool}: a linked home runs a
// catalog action on this machine with this machine's own handler. The
// caller is the home's link session (this machine's owner, as for every
// linked call); the action's API scopes bound what it may do. A call that
// did not come over the link is refused.
func (s *Server) handleCatalogLinked(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	home, ok := fleetHomeFromRequest(r)
	if !ok {
		writeError(w, http.StatusForbidden, errors.New("this route serves linked homes only"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, CatalogLinkedPath), "/")
	catalog, ok := tool.CatalogToolNamed(name)
	if !ok || !catalogLinkedTools[name] {
		writeError(w, http.StatusNotFound, fmt.Errorf("no linked catalog tool %q", name))
		return
	}
	if s.toolCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("%s is not available on this machine", name))
		return
	}
	var args map[string]any
	if err := decodeJSONStrict(w, r, 1<<20, &args); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if args == nil {
		writeError(w, http.StatusBadRequest, errors.New("object body required"))
		return
	}
	if _, nested := args["machine"]; nested {
		writeError(w, http.StatusBadRequest, errors.New("nested machine routing is not allowed"))
		return
	}
	action, ok := catalog.Action(controlMCPString(args, "action"))
	if !ok || !action.ControlCallable() || action.Reach == tool.CatalogReachHome {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%s has no linked action %q", name, controlMCPString(args, "action")))
		return
	}
	if !s.requireScopeAny(w, r, action.APIScopes...) {
		return
	}
	answer, err := s.toolCatalog.ExecuteControlCatalog(r.Context(), tool.ControlCatalogCall{Principal: principal, KeyID: "home-" + home.Fingerprint, Scopes: action.APIScopes, Tool: name, Arguments: args})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(answer))
}
