package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"swarm/packages/swarmd/internal/identity"
)

// FleetAgentsAtWorkAction reads which agent runs are active now on this
// machine and its linked machines, and what each is doing: manage_projects
// action=agents_at_work, optional machine. It is read-only.
const FleetAgentsAtWorkAction = "agents_at_work"

// FleetAgentsAtWork is the fleet's agents at work, the same service as
// GET /v3/fleet/agents and Swarm Control's swarm_agents_at_work
// (api.Server.FleetAgents); machine, when set, reads that one machine.
type FleetAgentsAtWork interface {
	FleetAgentsAtWork(ctx context.Context, p identity.Principal, machine string) (json.RawMessage, error)
}

// FleetAgentsReadAllowed is the permission gate's view of the read-only
// agents-at-work read with a machine.
func FleetAgentsReadAllowed(name, action string) bool {
	return name == "manage_projects" && action == FleetAgentsAtWorkAction
}

// executeFleetAgentsAtWork answers agents_at_work for the home's
// Orchestrator, under the same owner checks as the other fleet actions
// (executeFleetProjects).
func (r *Runtime) executeFleetAgentsAtWork(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.sessions == nil || !scope.Principal.Valid() || scope.Principal.Type != identity.PrincipalTypeUser {
		return "", errors.New("agents_at_work requires an authenticated session")
	}
	session, found, err := r.sessions.GetSession(scope.SessionID)
	if err != nil {
		return "", err
	}
	if !found || session.AccountScopeID != scope.Principal.AccountScopeID || session.UserID != scope.Principal.UserID {
		return "", errors.New("fleet session ownership mismatch")
	}
	if scope.TaskHistoryOnly || asString(session.Metadata["task_id"]) != "" || asString(session.Metadata["project_task_id"]) != "" {
		return "", errors.New("agents_at_work is only available to the home's Orchestrator")
	}
	service, ok := r.getProjectTaskLifecycleService().(FleetAgentsAtWork)
	if !ok {
		return "", errors.New("agents at work unavailable")
	}
	machine := strings.TrimSpace(asString(args["machine"]))
	raw, err := service.FleetAgentsAtWork(ctx, scope.Principal, machine)
	if err != nil {
		return "", err
	}
	out := map[string]any{"tool": "manage_projects", "action": FleetAgentsAtWorkAction, "data": raw, "untrusted_content": true}
	if machine != "" {
		out["machine"] = machine
	}
	encoded, err := json.Marshal(out)
	return string(encoded), err
}
