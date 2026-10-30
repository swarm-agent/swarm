package run

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// ManageWorkers runs one manage_workers action on this machine: the one
// implementation behind the catalog's manage_workers (api.Server.ManageWorkers
// calls it for every action but assign_task, which runs the
// swarm_assign_worker_task handler). A conversation's call (sessionID, its
// agent) keeps the Orchestrator-only rule and the conversation's pending
// proposals. A Swarm Control or linked caller (its key scopes in ctx,
// tool.ControlScopesFromContext) has no conversation: it acts with its
// principal's account and user only, and a worker it creates or proposes
// waits for the person's acceptance in Swarm like the Orchestrator's. machine
// never reaches here: the API server sends it to the linked machine.
func (s *Service) ManageWorkers(ctx context.Context, p identity.Principal, sessionID, agent string, args map[string]any) (string, error) {
	if s == nil || s.sessions == nil {
		return "", errors.New("session service required")
	}
	if !p.Valid() || strings.TrimSpace(p.AccountScopeID) == "" || strings.TrimSpace(p.UserID) == "" {
		return "", errors.New("manage_workers requires an authenticated user identity")
	}
	if _, routed := args["machine"]; routed {
		return "", errors.New("manage_workers machine is routed by the worker service")
	}
	action := strings.ToLower(strings.TrimSpace(mapString(args, "action")))
	if action == "" {
		return "", errors.New("manage_workers action is required")
	}
	arguments, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	if _, control := tool.ControlScopesFromContext(ctx); control {
		current := store.SessionSnapshot{AccountScopeID: p.AccountScopeID, UserID: p.UserID}
		return s.manageWorkersAction(current, "", action, args, string(arguments))
	}
	current, found, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !found || current.AccountScopeID != p.AccountScopeID || current.UserID != p.UserID {
		return "", errors.New("manage_workers session principal mismatch")
	}
	return s.executeManageWorkersTool(sessionID, string(arguments), store.AgentProfile{Name: agent})
}

// executeManageWorkersCatalogTool runs a session's manage_workers call through
// the catalog handler (tool.Runtime, the one Swarm Control runs), as the
// session's own principal and agent: its answer is the catalog envelope.
func (s *Service) executeManageWorkersCatalogTool(ctx context.Context, principal identity.Principal, sessionID, arguments string, profile store.AgentProfile) (string, error) {
	current, found, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !found || !principal.Valid() || principal.AccountScopeID != current.AccountScopeID || principal.UserID != current.UserID {
		return "", errors.New("manage_workers session principal mismatch")
	}
	if s.tools == nil {
		return "", errors.New("worker service unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = tool.WithCatalogAgent(ctx, profile.Name)
	return s.tools.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: sessionID, Principal: principal}, tool.Call{Name: "manage_workers", Arguments: arguments})
}
