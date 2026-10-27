package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
)

// FleetProjects keeps link credentials and transport entirely server-side.
// The existing lifecycle adapter provides this optional capability.
type FleetProjects interface {
	ListFleetMachines(context.Context, identity.Principal) ([]FleetMachineInfo, error)
	CallFleetProject(context.Context, identity.Principal, string, string, map[string]any) (json.RawMessage, error)
}
type FleetMachineInfo struct {
	Name      string `json:"name"`
	Online    bool   `json:"online"`
	LastError string `json:"last_error,omitempty"`
	Access    string `json:"access"`
}

func FleetProjectActionAllowed(action string) bool {
	switch action {
	case "list", "get", "list_tasks", "get_task", "propose_task", "create_task", "approve_task", "accept_task", "reopen_task", "refine_task", "archive_task":
		return true
	}
	return false
}
func (r *Runtime) executeFleetProjects(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.sessions == nil || !scope.Principal.Valid() || scope.Principal.Type != identity.PrincipalTypeUser {
		return "", errors.New("fleet requires an authenticated session")
	}
	session, found, err := r.sessions.GetSession(scope.SessionID)
	if err != nil {
		return "", err
	}
	if !found || session.AccountScopeID != scope.Principal.AccountScopeID || session.UserID != scope.Principal.UserID {
		return "", errors.New("fleet session ownership mismatch")
	}
	if scope.TaskHistoryOnly || asString(session.Metadata["task_id"]) != "" || asString(session.Metadata["project_task_id"]) != "" {
		return "", errors.New("linked machine delegation is only available to the home's Orchestrator")
	}
	service, ok := r.getProjectTaskLifecycleService().(FleetProjects)
	if !ok {
		return "", errors.New("fleet service unavailable")
	}
	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if action == "" {
		action = "list"
	}
	machine := strings.TrimSpace(asString(args["machine"]))
	if machine != "" && !FleetProjectActionAllowed(action) {
		return "", fmt.Errorf("%s is not available on a linked machine", action)
	}
	machines, err := service.ListFleetMachines(ctx, scope.Principal)
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
	if action == "list_machines" {
		raw, err := json.Marshal(map[string]any{"machines": machines})
		return string(raw), err
	}
	if !linked || (access.Link(machine).Work != "ask" && access.Link(machine).Work != "allowed") {
		return "", fmt.Errorf("linked work rule for machine %q is off or the machine is not linked; check the Permissions page", machine)
	}
	raw, err := service.CallFleetProject(ctx, scope.Principal, machine, action, args)
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(map[string]any{"machine": machine, "action": action, "data": raw, "untrusted_content": true})
	return string(out), err
}
