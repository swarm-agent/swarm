package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Folder environments through manage_projects: what a registered folder's
// sandbox and leases run in (base image, setup commands, environment
// variables, max leases). Without machine the actions use this Swarm's
// environment service; with machine they use the same service on a linked
// machine through the link (the home's linked work rule gates them). The
// service and its HTTP route (/v3/sandbox-environments) own every rule.
const (
	FolderEnvironmentList    = "list_environments"
	FolderEnvironmentGet     = "get_environment"
	FolderEnvironmentUpdate  = "update_environment"
	FolderEnvironmentRebuild = "rebuild_environment"
)

// FolderEnvironments runs a folder environment action here (machine "") or on
// a linked machine. The API server provides it.
type FolderEnvironments interface {
	ManageFolderEnvironment(ctx context.Context, p identity.Principal, machine, action string, args map[string]any) (json.RawMessage, error)
}

// FolderEnvironmentActions lists the folder environment actions.
func FolderEnvironmentActions() []string {
	return []string{FolderEnvironmentList, FolderEnvironmentGet, FolderEnvironmentUpdate, FolderEnvironmentRebuild}
}

// FolderEnvironmentAction reports whether action is a folder environment action.
func FolderEnvironmentAction(action string) bool {
	for _, known := range FolderEnvironmentActions() {
		if action == known {
			return true
		}
	}
	return false
}

// FleetEnvironmentPermissionAllowed is the permission gate's view of folder
// environment actions sent to a linked machine.
func FleetEnvironmentPermissionAllowed(name, action string) bool {
	return name == "manage_projects" && FolderEnvironmentAction(action)
}

// FleetEnvironmentChange reports whether a linked action changes a machine's
// folder environment (and so runs setup commands there): under
// linked.{name}.work ask it waits for the person, like sending a task.
func FleetEnvironmentChange(name, action string) bool {
	return name == "manage_projects" && (action == FolderEnvironmentUpdate || action == FolderEnvironmentRebuild)
}

// FolderEnvironmentDescription is the manage_projects description of the
// environment actions (kept short: the Orchestrator's tool budget).
const FolderEnvironmentDescription = " Folder environments: list_environments, get/update/rebuild_environment (workspace_path; machine ok)."

// folderEnvironmentArgs flattens the environment object (update_environment's
// fields and wait_seconds) into the call's arguments; top-level fields of the
// same name are accepted too. action and machine are not sent.
func folderEnvironmentArgs(args map[string]any) (map[string]any, error) {
	sent := make(map[string]any, len(args))
	for key, value := range args {
		if key != "action" && key != "machine" && key != "environment" {
			sent[key] = value
		}
	}
	if raw, ok := args["environment"]; ok && raw != nil {
		fields, isObject := raw.(map[string]any)
		if !isObject {
			return nil, errors.New("environment must be an object: base_image, append_setup, setup, env, unset_env, max_leases, wait_seconds")
		}
		for key, value := range fields {
			if _, dup := sent[key]; dup {
				return nil, errors.New(key + " is given twice (top level and in environment)")
			}
			sent[key] = value
		}
	}
	return sent, nil
}

// folderEnvironmentSwarmShaped reports a get_environment or
// update_environment call in the shape swarm_get_environment and
// swarm_set_environment took: a project's folder (project, workspace, or no
// workspace_path), the build log, or reset. Those run the swarm_* handlers
// (CatalogActionService); a folder named by workspace_path runs here.
func folderEnvironmentSwarmShaped(action string, args map[string]any) bool {
	if action != FolderEnvironmentGet && action != FolderEnvironmentUpdate {
		return false
	}
	if strings.TrimSpace(asString(args["workspace_path"])) == "" {
		return true
	}
	for _, key := range []string{"project", "workspace"} {
		if strings.TrimSpace(asString(args[key])) != "" {
			return true
		}
	}
	return asBool(args["include_log"]) || asBool(args["reset"])
}

func (r *Runtime) executeFolderEnvironments(ctx context.Context, scope WorkspaceScope, action string, args map[string]any) (string, error) {
	if r == nil || r.sessions == nil || !scope.Principal.Valid() || scope.Principal.Type != identity.PrincipalTypeUser {
		return "", errors.New("folder environments require an authenticated session")
	}
	if _, err := r.homeConversation(scope, "folder environment session ownership mismatch", "folder environments are managed by the Orchestrator, not by tasks; agents in a folder use lease_environment"); err != nil {
		return "", err
	}
	service, ok := r.getProjectTaskLifecycleService().(FolderEnvironments)
	if !ok {
		return "", errors.New("folder environment service unavailable")
	}
	machine := strings.TrimSpace(asString(args["machine"]))
	if machine != "" {
		fleetService, ok := r.getProjectTaskLifecycleService().(FleetProjects)
		if !ok {
			return "", errors.New("fleet service unavailable")
		}
		machines, err := fleetService.ListFleetMachines(ctx, scope.Principal)
		if err != nil {
			return "", err
		}
		access, err := r.sessions.GetAccountPermissions(scope.Principal.AccountScopeID)
		if err != nil {
			return "", err
		}
		linked := false
		for i := range machines {
			machines[i].Access = access.Link(machines[i].Name).Work
			if machines[i].Name == machine {
				linked = true
			}
		}
		if !linked || !pebblestore.LinkedWorkOn(access.Link(machine).Work) {
			return "", FleetMachineUnavailableError(machine, machines, linked)
		}
	}
	sent, err := folderEnvironmentArgs(args)
	if err != nil {
		return "", err
	}
	if machine == "" && folderEnvironmentSwarmShaped(action, sent) {
		// The swarm_get_environment and swarm_set_environment shape (a
		// project's folder, the log, reset): their own handlers.
		call := map[string]any{"action": action}
		for key, value := range sent {
			call[key] = value
		}
		declared, _ := manageProjectsDeclared().Action(action)
		answer, err := r.catalogServiceAnswer(ctx, scope, manageProjectsDeclared(), declared, call)
		if err != nil {
			return "", err
		}
		out, err := json.Marshal(map[string]any{"tool": "manage_projects", "action": action, "data": json.RawMessage(answer)})
		return string(out), err
	}
	if action == FolderEnvironmentUpdate && asBool(sent["rebuild"]) {
		// rebuild on a folder named by workspace_path is rebuild_environment.
		action = FolderEnvironmentRebuild
		delete(sent, "rebuild")
	}
	raw, err := service.ManageFolderEnvironment(ctx, scope.Principal, machine, action, sent)
	if err != nil {
		return "", err
	}
	result := map[string]any{"tool": "manage_projects", "action": action, "data": raw}
	if machine != "" {
		result["machine"], result["untrusted_content"] = machine, true
	}
	out, err := json.Marshal(result)
	return string(out), err
}
