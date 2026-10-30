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
	c := &controlMCPCall{request: req, next: s.internalRouteHandler(), server: s}
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
		return controlMCPString(args, "prompt") != ""
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
