package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	routerruntime "swarm/packages/swarmd/internal/router"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

const maxSessionRouterOutputBytes = 64 << 10

// sessionRouterDecision is an account-validated, non-durable routing decision.
// Session creation and worktree allocation intentionally happen at a later API boundary.
type sessionRouterDecision struct {
	Result    routerruntime.Result
	Workspace workspace.RoutingWorkspaceSelection
	Profile   pebblestore.AgentProfile
}

// routeSessionOnce invokes the configured hidden Router exactly once. It does not
// create a session, allocate a worktree, or mutate workspace selection.
func (s *Server) routeSessionOnce(ctx context.Context, principal identity.Principal, input string, worktreeRequested bool) (sessionRouterDecision, error) {
	if s == nil || s.workspace == nil {
		return sessionRouterDecision{}, errors.New("workspace service is not configured")
	}
	if s.providers == nil {
		return sessionRouterDecision{}, errors.New("provider registry is not configured")
	}
	if s.uiSettings == nil {
		return sessionRouterDecision{}, errors.New("ui settings service is not configured")
	}
	if s.swarmProfiles == nil {
		return sessionRouterDecision{}, errors.New("swarm model settings service is not configured")
	}
	if !principal.Valid() {
		return sessionRouterDecision{}, identity.ErrPrincipalRequired
	}

	workspaceContext, err := s.workspace.BuildRoutingContextForPrincipal(principal)
	if err != nil {
		return sessionRouterDecision{}, fmt.Errorf("build Router workspace context: %w", err)
	}

	routerContext := routerruntime.Context{WorktreeRequested: worktreeRequested}
	if workspaceContext.WorkspaceSelectionRequired {
		routerContext.Workspaces = make([]routerruntime.Workspace, 0, len(workspaceContext.Workspaces))
		for _, candidate := range workspaceContext.Workspaces {
			routerContext.Workspaces = append(routerContext.Workspaces, routerruntime.Workspace{
				ID: candidate.WorkspaceID, Name: candidate.Name, Definition: candidate.Definition,
			})
		}
	} else {
		// RoutingContext deliberately hides a sole binding. Resolve it server-side
		// so Router still receives its bounded descriptive context, but cannot select it.
		bound, resolveErr := s.workspace.ResolveRoutingWorkspaceForPrincipal(principal, workspaceContext, "")
		if resolveErr != nil {
			return sessionRouterDecision{}, fmt.Errorf("resolve Router server-bound workspace: %w", resolveErr)
		}
		routerContext.ServerBoundWorkspaceID = bound.WorkspaceID
		routerContext.Workspaces = []routerruntime.Workspace{{ID: bound.WorkspaceID, Name: bound.WorkspaceName, Definition: bound.Definition}}
	}

	authorityContext := identity.ContextWithPrincipal(ctx, principal)
	swarmSettings, err := s.swarmProfiles.Get(authorityContext)
	if err != nil {
		return sessionRouterDecision{}, fmt.Errorf("read Swarm mode settings for Router: %w", err)
	}
	routerContext.PlanEnabled = swarmSettings.PlanEnabled

	routerRequest := routerruntime.Request{Input: input, Context: routerContext}
	if err := routerruntime.ValidateRequest(routerRequest); err != nil {
		return sessionRouterDecision{}, err
	}
	prompt, err := routerruntime.Prompt(routerContext)
	if err != nil {
		return sessionRouterDecision{}, fmt.Errorf("build Router prompt: %w", err)
	}
	schema, err := routerruntime.ResultSchema(routerContext)
	if err != nil {
		return sessionRouterDecision{}, fmt.Errorf("build Router result schema: %w", err)
	}
	encodedSchema, err := json.Marshal(schema)
	if err != nil {
		return sessionRouterDecision{}, fmt.Errorf("encode Router result schema: %w", err)
	}

	uiSettings, err := s.uiSettings.GetForAccount(principal.AccountScopeID)
	if err != nil {
		return sessionRouterDecision{}, fmt.Errorf("read Router model settings: %w", err)
	}
	configured := uiSettings.Agents.Router
	profile := agentruntime.RouterAgentProfileForParent(pebblestore.AgentProfile{
		Provider: configured.Provider, Model: configured.Model, Thinking: configured.Thinking, AutoServiceTier: configured.ServiceTier,
	})
	providerID := strings.ToLower(strings.TrimSpace(profile.Provider))
	if providerID == "" || strings.TrimSpace(profile.Model) == "" {
		return sessionRouterDecision{}, errors.New("Router provider and model must be configured")
	}
	if profile.ToolContract == nil || len(profile.ToolContract.Tools) != 0 {
		return sessionRouterDecision{}, errors.New("compiled Router profile must be tool-free")
	}
	runner, ok := s.providers.GetRunner(providerID)
	if !ok || runner == nil {
		return sessionRouterDecision{}, fmt.Errorf("Router provider %q is not available", providerID)
	}

	// provideriface has no structured-output field shared by every adapter. Keep
	// the strict schema in system instructions and enforce it again with DecodeResult.
	instructions := strings.TrimSpace(prompt) + "\nOutput JSON schema (authoritative): " + string(encodedSchema)
	response, err := runner.CreateResponse(authorityContext, provideriface.Request{
		Model:        strings.TrimSpace(profile.Model),
		Thinking:     strings.TrimSpace(profile.Thinking),
		ServiceTier:  strings.TrimSpace(profile.AutoServiceTier),
		Instructions: instructions,
		Input: []map[string]any{{
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": input}},
		}},
		Tools:      []provideriface.ToolDefinition{},
		ToolChoice: "none",
	})
	if err != nil {
		return sessionRouterDecision{}, fmt.Errorf("Router provider response: %w", err)
	}
	if len(response.FunctionCalls) != 0 || response.RestartTurn {
		return sessionRouterDecision{}, errors.New("Router provider requested a tool or another turn")
	}
	if len(response.Text) > maxSessionRouterOutputBytes {
		return sessionRouterDecision{}, fmt.Errorf("Router provider output exceeds %d bytes", maxSessionRouterOutputBytes)
	}
	raw := strings.TrimSpace(response.Text)
	if raw == "" {
		return sessionRouterDecision{}, errors.New("Router provider returned empty output")
	}
	result, err := routerruntime.DecodeResult(raw, routerContext)
	if err != nil {
		return sessionRouterDecision{}, err
	}

	selectedWorkspaceID := ""
	if result.WorkspaceID != nil {
		selectedWorkspaceID = *result.WorkspaceID
	}
	selection, err := s.workspace.ResolveRoutingWorkspaceForPrincipal(principal, workspaceContext, selectedWorkspaceID)
	if err != nil {
		return sessionRouterDecision{}, fmt.Errorf("revalidate Router workspace selection: %w", err)
	}
	return sessionRouterDecision{Result: result, Workspace: selection, Profile: profile}, nil
}
