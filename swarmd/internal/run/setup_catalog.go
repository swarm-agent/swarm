package run

import (
	"context"
	"errors"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// errManageSetupNotOrchestrator refuses manage_setup for any agent but the
// Orchestrator: machine setup is never an agent's or a worker's.
var errManageSetupNotOrchestrator = errors.New("manage_setup is the Orchestrator's: machine setup is never an agent's or a worker's")

// errManageSetupUndecided refuses a manage_setup change the person did not
// decide in this conversation.
var errManageSetupUndecided = errors.New("manage_setup changes this machine's setup and needs the person's decision for this call; it was not given, so nothing changed")

// executeManageSetupTool runs a conversation's manage_setup call through the
// catalog handler (tool.Runtime, the one Swarm Control runs), as the
// session's own principal and agent. A change runs only when the person
// decided this exact call: gateToolCalls turns every manage_setup change into
// a pending request (an allow rule or bypass never stands in for it) and, on
// the person's approval, records the call's own arguments as its approved
// arguments. Without them the change is refused and nothing runs; with them
// the call runs as the person saw it, marked tool.WithPersonDecision.
func (s *Service) executeManageSetupTool(ctx context.Context, principal identity.Principal, sessionID string, call tool.Call, approvedArguments string, profile store.AgentProfile) (string, error) {
	if !agentruntime.IsOrchestratorAgentName(profile.Name) {
		return "", errManageSetupNotOrchestrator
	}
	arguments := strings.TrimSpace(call.Arguments)
	change := tool.ManageSetupChange(arguments)
	if change && (strings.TrimSpace(approvedArguments) == "" || strings.TrimSpace(approvedArguments) != arguments) {
		return "", errManageSetupUndecided
	}
	if s.sessions == nil {
		return "", errors.New("session service required")
	}
	current, found, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !found || !principal.Valid() || principal.AccountScopeID != current.AccountScopeID || principal.UserID != current.UserID {
		return "", errors.New("manage_setup session principal mismatch")
	}
	if s.tools == nil {
		return "", errors.New("setup service unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if change {
		ctx = tool.WithPersonDecision(ctx)
	}
	ctx = tool.WithCatalogAgent(ctx, profile.Name)
	return s.tools.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: sessionID, Principal: principal}, tool.Call{Name: tool.ManageSetupToolName, Arguments: arguments})
}
