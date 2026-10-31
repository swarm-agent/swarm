package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
)

// WorkspaceCatalogService is the run service's manage_workspace for a caller
// with no conversation (Swarm Control, or a linked home through
// CatalogLinkedPath): the same workspace catalog and Workspace Map
// functions a conversation's call runs, without a session to retarget. It
// returns the action's own answer; the runtime puts it in the envelope.
type WorkspaceCatalogService interface {
	ManageWorkspaceControl(ctx context.Context, p identity.Principal, args map[string]any) (string, error)
}

// SetWorkspaceCatalogService installs manage_workspace's service (the run
// service registers itself).
func (r *Runtime) SetWorkspaceCatalogService(service WorkspaceCatalogService) {
	if r != nil {
		r.workspaceCatalog = service
	}
}

// ManageWorkspaceCatalog runs one manage_workspace action and answers in the
// catalog envelope: the one entry for the Orchestrator, the Swarm agent and
// Swarm Control. With machine= the action runs on the linked machine (its
// own manage_workspace handler); otherwise local runs it here with the
// call's arguments (machine removed) and returns its own answer.
func (r *Runtime) ManageWorkspaceCatalog(ctx context.Context, scope WorkspaceScope, args map[string]any, local func(args map[string]any) (string, error)) (string, error) {
	if err := CatalogActionKeyUnambiguous(args); err != nil {
		return "", err
	}
	catalog := catalogDeclared(manageWorkspaceToolName)
	name := strings.ToLower(strings.TrimSpace(stringValue(args["action"])))
	switch name {
	case "":
		name = "inspect"
	case "edit":
		// The handler's alias of update.
		name = "update"
	}
	declared, ok := catalog.Action(name)
	if !ok {
		return "", fmt.Errorf("unsupported manage_workspace action %q", name)
	}
	call := make(map[string]any, len(args))
	for key, value := range args {
		call[key] = value
	}
	call["action"] = declared.Action
	// A conversation's call never carries the action's Swarm Control inputs
	// (create's path and name) to a linked machine, which runs a linked call
	// as a Swarm Control one. Here a conversation's change runs only as the
	// person approved it, from its own arguments.
	call = CatalogConversationArgs(declared, scope.ControlCaller, call)
	if machine := strings.TrimSpace(stringValue(args["machine"])); machine != "" {
		if declared.Reach == CatalogReachHome {
			return "", fmt.Errorf("manage_workspace %s runs on this machine only; omit machine", declared.Action)
		}
		if r == nil {
			return "", errors.New("linked work service unavailable")
		}
		return r.linkedCatalogCall(ctx, scope, catalog, declared, machine, call)
	}
	delete(call, "machine")
	if local == nil {
		return "", errors.New("manage_workspace is not configured")
	}
	answer, err := local(call)
	if err != nil {
		return "", err
	}
	return r.catalogEnvelope(catalog, declared.Action, call, answer, false)
}

// executeManageWorkspaceControl is Swarm Control's manage_workspace.
func (r *Runtime) executeManageWorkspaceControl(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	return r.ManageWorkspaceCatalog(ctx, scope, args, func(call map[string]any) (string, error) {
		if mapString(call, "action") == "create" {
			// swarm_add_workspace's path or swarm_create_workspace's name.
			if answer, handled, err := r.manageWorkspaceSwarmCreate(ctx, scope, call); handled {
				return answer, err
			}
		}
		if r.workspaceCatalog == nil {
			return "", errors.New("manage_workspace is not configured on this machine")
		}
		return r.workspaceCatalog.ManageWorkspaceControl(ctx, scope.Principal, call)
	})
}

// ManageWorkspaceArgs decodes a manage_workspace call's arguments for the
// envelope (the run service's own parser validates them).
func ManageWorkspaceArgs(arguments string) map[string]any {
	var args map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(arguments)), &args) != nil || args == nil {
		args = map[string]any{}
	}
	return args
}
