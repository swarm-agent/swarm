package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
)

// JobsService is the one job authority (the API server's ManageJobs) behind
// manage_jobs for the Orchestrator, every agent granted manage_jobs, and
// Swarm Control. who names the caller: the calling session, or the Swarm
// Control caller's AI key; agent is the calling session's agent (empty for
// Swarm Control). With machine in args it runs the action on that linked
// machine and returns the machine's answer as it came.
type JobsService interface {
	ManageJobs(ctx context.Context, p identity.Principal, who, agent string, args map[string]any) (any, error)
}

type catalogAgentContextKey struct{}

// WithCatalogAgent carries the calling session's agent name (a trusted run
// input, never a model argument) to a catalog handler that grants per agent
// (manage_jobs.<agent>.<action>).
func WithCatalogAgent(ctx context.Context, agent string) context.Context {
	return context.WithValue(ctx, catalogAgentContextKey{}, strings.TrimSpace(agent))
}

// CatalogAgentFromContext returns the calling session's agent name.
func CatalogAgentFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	agent, _ := ctx.Value(catalogAgentContextKey{}).(string)
	return agent
}

// executeManageJobs runs one manage_jobs action and returns its answer in the
// catalog envelope, for the Orchestrator (and every agent granted
// manage_jobs) and Swarm Control alike: one handler, one service function
// (JobsService.ManageJobs), which with machine= runs the action on the linked
// machine's own job service.
func (r *Runtime) executeManageJobs(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	catalog := catalogDeclared(manageJobsToolName)
	name := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	declared, ok := catalog.Action(name)
	if !ok {
		return "", fmt.Errorf("unknown manage_jobs action %q; use one of: %s", name, strings.Join(catalogActionNames(catalog), ", "))
	}
	p := scope.Principal
	if !p.Valid() || strings.TrimSpace(p.AccountScopeID) == "" || strings.TrimSpace(p.UserID) == "" || p.Type != identity.PrincipalTypeUser {
		return "", errors.New("manage_jobs requires an authenticated user identity")
	}
	service, ok := r.getWorkflowLifecycleService().(JobsService)
	if !ok {
		return "", errors.New("jobs service unavailable")
	}
	who, agent := strings.TrimSpace(scope.SessionID), ""
	if scope.ControlCaller {
		who = strings.TrimSpace(scope.ControlSender)
	} else {
		agent = CatalogAgentFromContext(ctx)
		if agent == "" {
			return "", errors.New("manage_jobs requires the calling agent")
		}
	}
	if who == "" {
		return "", errors.New("manage_jobs requires a session or an AI key")
	}
	call := make(map[string]any, len(args))
	for key, value := range args {
		call[key] = value
	}
	call["action"] = declared.Action
	machine := strings.TrimSpace(asString(args["machine"]))
	if machine == "" {
		delete(call, "machine")
	} else if declared.Reach == CatalogReachHome {
		return "", fmt.Errorf("manage_jobs %s runs on this machine only; omit machine", declared.Action)
	}
	out, err := service.ManageJobs(ctx, p, who, agent, call)
	if err != nil {
		return "", err
	}
	if machine != "" {
		// The linked machine's answer is the envelope's data as it came.
		answer, err := json.Marshal(map[string]any{"data": out})
		if err != nil {
			return "", err
		}
		return r.catalogEnvelope(catalog, declared.Action, call, string(answer), true)
	}
	answer, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return r.catalogEnvelope(catalog, declared.Action, call, string(answer), false)
}
