package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
)

// SessionControlService is the API server's part of manage_sessions: the
// functions behind the session routes the runtime has no store for. It is
// the same service the swarm_* session tools reach through those routes.
type SessionControlService interface {
	// ManageSessionControl runs resolve_permission or set_model for the
	// caller through the session permission and model-profile routes.
	ManageSessionControl(ctx context.Context, p identity.Principal, action string, args map[string]any) (map[string]any, error)
	// RegisteredWorkspacePath returns path as the caller's registered
	// workspace, or an error naming why it is not one.
	RegisteredWorkspacePath(p identity.Principal, path string) (string, error)
}

// FleetCatalog runs a catalog tool's action on a linked machine: that
// machine's own catalog handler, reached through the link
// (api.Server.CallFleetCatalog). It returns that machine's envelope.
type FleetCatalog interface {
	CallFleetCatalog(ctx context.Context, p identity.Principal, machine, tool string, args map[string]any) (json.RawMessage, error)
}

// sessionControlService is the API server as the session control service.
func (r *Runtime) sessionControlService() (SessionControlService, bool) {
	service, ok := r.getProjectTaskLifecycleService().(SessionControlService)
	return service, ok
}

// fleetCatalog is the API server as the linked catalog caller.
func (r *Runtime) fleetCatalog() (FleetCatalog, bool) {
	service, ok := r.getProjectTaskLifecycleService().(FleetCatalog)
	return service, ok
}

// executeManageSessions runs one manage_sessions action and returns its
// answer in the catalog envelope, for the Orchestrator, the Swarm agent and
// Swarm Control alike: one handler. With machine= the action runs on the
// linked machine (its own manage_sessions handler, FleetCatalog).
func (r *Runtime) executeManageSessions(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	if err := CatalogActionKeyUnambiguous(args); err != nil {
		return "", err
	}
	catalog := catalogDeclared(manageSessionsToolName)
	name := strings.ToLower(strings.TrimSpace(stringValue(args["action"])))
	declared, ok := catalog.Action(name)
	if !ok {
		return "", fmt.Errorf("manage-sessions action %q is not supported", name)
	}
	call := make(map[string]any, len(args))
	for key, value := range args {
		call[key] = value
	}
	call["action"] = declared.Action
	if machine := strings.TrimSpace(stringValue(args["machine"])); machine != "" {
		if declared.Reach == CatalogReachHome {
			return "", fmt.Errorf("manage_sessions %s runs on this machine only; omit machine", declared.Action)
		}
		return r.linkedCatalogCall(ctx, scope, catalog, declared, machine, call)
	}
	delete(call, "machine")
	var answer string
	var err error
	switch declared.Action {
	case ManageSessionsResolvePermissionAction, ManageSessionsSetModelAction:
		answer, err = r.manageSessionsControl(ctx, scope, declared.Action, call)
	case "create":
		if scope, err = r.createSessionScope(scope, call); err != nil {
			return "", err
		}
		answer, err = r.manageSessionsAnswer(ctx, scope, call)
	default:
		answer, err = r.manageSessionsAnswer(ctx, scope, call)
	}
	if err != nil {
		return "", err
	}
	return r.catalogEnvelope(catalog, declared.Action, call, answer, false)
}

// CatalogActionKeyUnambiguous refuses a catalog call whose arguments carry
// a key that differs from "action" only in case. The handler reads the exact
// key, while a struct decoder (a permission classifier) matches any case and
// keeps the last: such a call could be approved as one action and run as
// another.
func CatalogActionKeyUnambiguous(args map[string]any) error {
	for key := range args {
		if key != "action" && strings.EqualFold(key, "action") {
			return fmt.Errorf("argument %q is not \"action\"; give the action once, as \"action\"", key)
		}
	}
	return nil
}

// createSessionScope authorizes create's workspace_path: inside the
// caller's own workspace roots, or one of the person's registered
// workspaces (as swarm_start_session requires). A Swarm Control caller has
// no conversation and no roots, so it must name a registered workspace.
func (r *Runtime) createSessionScope(scope WorkspaceScope, args map[string]any) (WorkspaceScope, error) {
	path := strings.TrimSpace(stringValue(args["workspace_path"]))
	if path == "" {
		if scope.ControlCaller {
			return scope, errors.New("manage_sessions create needs workspace_path: a registered workspace (manage_workspace list)")
		}
		return scope, nil
	}
	if !scope.ControlCaller && pathWithinScope(path, scope.Roots, scope.PrimaryPath) {
		return scope, nil
	}
	service, ok := r.sessionControlService()
	if !ok {
		return scope, fmt.Errorf("workspace path %q is outside authorized scope", path)
	}
	registered, err := service.RegisteredWorkspacePath(scope.Principal, path)
	if err != nil {
		return scope, err
	}
	args["workspace_path"] = registered
	scope.Roots = append(append([]string(nil), scope.Roots...), registered)
	if scope.ControlCaller {
		scope.PrimaryPath = registered
	}
	return scope, nil
}

// manageSessionsControl runs resolve_permission or set_model through the
// session control service (the routes behind the swarm_* tools).
func (r *Runtime) manageSessionsControl(ctx context.Context, scope WorkspaceScope, action string, args map[string]any) (string, error) {
	sessionID := strings.TrimSpace(stringValue(args["session_id"]))
	if sessionID == "" {
		return "", fmt.Errorf("%s requires session_id", action)
	}
	if r == nil || r.sessions == nil {
		return "", errors.New("manage-sessions service is not configured")
	}
	if _, _, err := r.ownedManageSession(scope, sessionID); err != nil {
		return "", err
	}
	if action == ManageSessionsResolvePermissionAction {
		// A conversation never answers its own pending requests: those
		// are the person's.
		if current := strings.TrimSpace(scope.SessionID); current != "" && current == sessionID {
			return "", errors.New("resolve_permission cannot answer this conversation's own requests; the person answers them")
		}
		switch stringValue(args["decision"]) {
		case "allow_once", "deny_once":
		default:
			return "", errors.New("resolve_permission needs decision allow_once or deny_once")
		}
		if strings.TrimSpace(stringValue(args["permission_id"])) == "" {
			return "", errors.New("resolve_permission requires permission_id")
		}
	} else if strings.TrimSpace(stringValue(args["provider"])) == "" || strings.TrimSpace(stringValue(args["model"])) == "" {
		return "", errors.New("set_model requires provider and model")
	}
	service, ok := r.sessionControlService()
	if !ok {
		return "", errors.New("session service unavailable")
	}
	out, err := service.ManageSessionControl(ctx, scope.Principal, action, args)
	if err != nil {
		return "", err
	}
	if out == nil {
		out = map[string]any{}
	}
	out["session_id"] = sessionID
	encoded, err := json.Marshal(out)
	return string(encoded), err
}

// linkedCatalogCall runs a catalog action on a linked machine through its
// own handler and puts that machine's answer in this machine's envelope:
// the machine's data as it came, untrusted.
func (r *Runtime) linkedCatalogCall(ctx context.Context, scope WorkspaceScope, catalog CatalogTool, declared CatalogAction, machine string, args map[string]any) (string, error) {
	fleet, ok := r.fleetCatalog()
	if !ok {
		return "", errors.New("linked work service unavailable")
	}
	remote := make(map[string]any, len(args))
	for key, value := range args {
		if key != "machine" {
			remote[key] = value
		}
	}
	raw, err := fleet.CallFleetCatalog(ctx, scope.Principal, machine, catalog.Name, remote)
	if err != nil {
		return "", err
	}
	data, err := EnvelopeData(string(raw))
	if err != nil {
		return "", fmt.Errorf("linked machine %s: %w", machine, err)
	}
	answer, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		return "", err
	}
	return r.catalogEnvelope(catalog, declared.Action, args, string(answer), true)
}
