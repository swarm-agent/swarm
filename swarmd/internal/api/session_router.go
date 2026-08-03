package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodel"
	"swarm/packages/swarmd/internal/identity"
	modelruntime "swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	routerruntime "swarm/packages/swarmd/internal/router"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const maxSessionRouterOutputBytes = 64 << 10

// sessionRouterDecision is a non-durable managed-worktree naming decision.
// Workspace authority and worktree allocation intentionally remain at the API boundary.
type sessionRouterDecision struct {
	Result  routerruntime.Result
	Profile pebblestore.AgentProfile
}

// configuredRouterResponse is the result of one non-durable, tool-free call to
// the account's configured Router model. Callers own their prompt and strict
// output validation; this bridge owns model resolution and provider invocation.
type configuredRouterResponse struct {
	Text    string
	Profile pebblestore.AgentProfile
}

// routeSessionOnce invokes the configured hidden Router exactly once to name an
// already-authorized managed worktree. It receives no workspace information.
func (s *Server) routeSessionOnce(ctx context.Context, principal identity.Principal, input string) (sessionRouterDecision, error) {
	if s == nil || s.providers == nil {
		return sessionRouterDecision{}, errors.New("provider registry is not configured")
	}
	if s.agentModelSettings == nil {
		return sessionRouterDecision{}, errors.New("agent model settings service is not configured")
	}
	if !principal.Valid() {
		return sessionRouterDecision{}, identity.ErrPrincipalRequired
	}
	routerRequest := routerruntime.Request{Input: input}
	if err := routerruntime.ValidateRequest(routerRequest); err != nil {
		return sessionRouterDecision{}, err
	}
	prompt := routerruntime.Prompt()
	schema := routerruntime.ResultSchema()
	encodedSchema, err := json.Marshal(schema)
	if err != nil {
		return sessionRouterDecision{}, fmt.Errorf("encode Router result schema: %w", err)
	}

	// provideriface has no structured-output field shared by every adapter. Keep
	// the strict schema in system instructions and enforce it again with DecodeResult.
	instructions := strings.TrimSpace(prompt) + "\nOutput JSON schema (authoritative): " + string(encodedSchema)
	configuredResponse, err := s.invokeConfiguredRouterOnce(ctx, principal, instructions, routerRequest.Input, maxSessionRouterOutputBytes)
	if err != nil {
		return sessionRouterDecision{}, err
	}
	result, err := routerruntime.DecodeResult(normalizeConfiguredRouterJSONResponse(configuredResponse.Text))
	if err != nil {
		return sessionRouterDecision{}, err
	}
	return sessionRouterDecision{Result: result, Profile: configuredResponse.Profile}, nil
}

// normalizeConfiguredRouterJSONResponse removes only one complete JSON Markdown fence.
// Some otherwise-valid configured Router responses wrap schema-conforming JSON
// despite explicit instructions. Each caller's strict decoder remains authoritative
// for the enclosed object, unknown fields, and trailing JSON content.
func normalizeConfiguredRouterJSONResponse(raw string) string {
	raw = strings.TrimSpace(raw)
	openingEnd := strings.IndexByte(raw, '\n')
	if openingEnd < 0 {
		return raw
	}
	opener := strings.TrimSpace(raw[:openingEnd])
	if opener != "```" && !strings.EqualFold(opener, "```json") {
		return raw
	}
	bodyAndClosing := raw[openingEnd+1:]
	closingStart := strings.LastIndexByte(bodyAndClosing, '\n')
	if closingStart < 0 || strings.TrimSpace(bodyAndClosing[closingStart+1:]) != "```" {
		return raw
	}
	body := strings.TrimSpace(bodyAndClosing[:closingStart])
	if body == "" {
		return raw
	}
	return body
}

func (s *Server) invokeConfiguredRouterOnce(ctx context.Context, principal identity.Principal, instructions, input string, maxOutputBytes int) (configuredRouterResponse, error) {
	if s == nil || s.providers == nil {
		return configuredRouterResponse{}, errors.New("provider registry is not configured")
	}
	if s.agentModelSettings == nil {
		return configuredRouterResponse{}, errors.New("agent model settings service is not configured")
	}
	if !principal.Valid() {
		return configuredRouterResponse{}, identity.ErrPrincipalRequired
	}
	instructions = strings.TrimSpace(instructions)
	if instructions == "" {
		return configuredRouterResponse{}, errors.New("Router instructions are required")
	}
	if strings.TrimSpace(input) == "" {
		return configuredRouterResponse{}, errors.New("Router input is required")
	}
	if maxOutputBytes <= 0 {
		return configuredRouterResponse{}, errors.New("Router output byte limit must be positive")
	}

	if s.model == nil || s.agents == nil {
		return configuredRouterResponse{}, errors.New("Router model and agent services are not configured")
	}
	resolvedModel, profile, err := agentmodel.ResolveSystemAgent(s.model, s.agents, s.agentModelSettings, principal.AccountScopeID, agentruntime.RouterAgentID, "")
	if err != nil {
		return configuredRouterResponse{}, err
	}
	providerID := modelruntime.NormalizeProviderID(resolvedModel.Preference.Provider)
	modelID := strings.TrimSpace(resolvedModel.Preference.Model)
	if !resolvedModel.CatalogPresent {
		return configuredRouterResponse{}, fmt.Errorf("Router model catalog record for provider %q model %q is unavailable", providerID, modelID)
	}
	catalogLookup, err := s.model.GetCatalog(providerID, modelID)
	if err != nil {
		return configuredRouterResponse{}, fmt.Errorf("read Router model catalog for provider %q model %q: %w", providerID, modelID, err)
	}
	if !catalogLookup.Found {
		return configuredRouterResponse{}, fmt.Errorf("Router model catalog record for provider %q model %q is unavailable", providerID, modelID)
	}
	if profile.ToolContract == nil || len(profile.ToolContract.Tools) != 0 {
		return configuredRouterResponse{}, errors.New("compiled Router profile must be tool-free")
	}
	runner, ok := s.providers.GetRunner(providerID)
	if !ok || runner == nil {
		return configuredRouterResponse{}, fmt.Errorf("Router provider %q is not available", providerID)
	}

	authorityContext := identity.ContextWithPrincipal(ctx, principal)
	response, err := runner.CreateResponse(authorityContext, provideriface.Request{
		Model:        resolvedModel.Preference.Model,
		Thinking:     resolvedModel.Preference.Thinking,
		ServiceTier:  resolvedModel.Preference.ServiceTier,
		ModelCatalog: catalogLookup.Record,
		Instructions: instructions,
		Input: []map[string]any{{
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": input}},
		}},
		Tools:      []provideriface.ToolDefinition{},
		ToolChoice: "none",
	})
	if err != nil {
		return configuredRouterResponse{}, fmt.Errorf("Router provider response: %w", err)
	}
	if len(response.FunctionCalls) != 0 || response.RestartTurn {
		return configuredRouterResponse{}, errors.New("Router provider requested a tool or another turn")
	}
	if len(response.Text) > maxOutputBytes {
		return configuredRouterResponse{}, fmt.Errorf("Router provider output exceeds %d bytes", maxOutputBytes)
	}
	raw := strings.TrimSpace(response.Text)
	if raw == "" {
		return configuredRouterResponse{}, errors.New("Router provider returned empty output")
	}
	return configuredRouterResponse{Text: raw, Profile: profile}, nil
}
