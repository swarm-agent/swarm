package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodel"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/privacy"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/swarmmode"
	"swarm/packages/swarmd/internal/tool"
)

const (
	swarmModeHydrationPathID  = "tool.swarm_mode.hydration.v1"
	swarmModeResultPathID     = "tool.swarm_mode.v1"
	swarmRouterTimeout        = 90 * time.Second
	swarmRouterMaxOutputRunes = 200000
)

type swarmRouterBridge interface {
	OneShot(context.Context, string, any, map[string]any, string) (string, error)
}

type configuredSwarmRouterBridge struct {
	runner    provideriface.Runner
	runtime   compactModelRuntime
	principal identity.Principal
	parentID  string
	callID    string
}

func (s *Service) newConfiguredSwarmRouterBridge(parent pebblestore.SessionSnapshot, principal identity.Principal, callID string) (swarmRouterBridge, error) {
	if s == nil || s.model == nil || s.agents == nil || s.agentModelSettings == nil || s.providers == nil {
		return nil, errors.New("swarm Router services are not fully configured")
	}
	accountScopeID := strings.TrimSpace(firstNonEmptyString(principal.AccountScopeID, parent.AccountScopeID))
	resolved, _, err := agentmodel.ResolveSystemAgent(s.model, s.agents, s.agentModelSettings, accountScopeID, agentruntime.RouterAgentID, "")
	if err != nil {
		return nil, fmt.Errorf("resolve configured Router: %w", err)
	}
	runtime, err := resolveSwarmRouterModelRuntime(s.model, resolved)
	if err != nil {
		return nil, err
	}
	runner, ok := s.providers.GetRunner(runtime.ProviderID)
	if !ok {
		return nil, fmt.Errorf("configured Router provider %q is not runnable", runtime.ProviderID)
	}
	if !principal.Valid() {
		principal = identity.Principal{
			Type: identity.PrincipalTypeUser, UserID: strings.TrimSpace(parent.UserID),
			AccountScopeID: strings.TrimSpace(parent.AccountScopeID), SessionID: strings.TrimSpace(parent.ID),
			AccountScopeSource: identity.AccountScopeSourceSession,
		}
	}
	return &configuredSwarmRouterBridge{runner: runner, runtime: runtime, principal: principal, parentID: strings.TrimSpace(parent.ID), callID: strings.TrimSpace(callID)}, nil
}

func resolveSwarmRouterModelRuntime(modelService *model.Service, resolved model.ResolvedPreference) (compactModelRuntime, error) {
	runtime, err := resolveCompactModelRuntime(modelService, resolved)
	if err != nil {
		return compactModelRuntime{}, fmt.Errorf("resolve Router model runtime: %w", err)
	}
	return runtime, nil
}

func (b *configuredSwarmRouterBridge) OneShot(ctx context.Context, systemPrompt string, payload any, schema map[string]any, identityKey string) (string, error) {
	if b == nil || b.runner == nil {
		return "", errors.New("swarm Router bridge is not configured")
	}
	requestJSON, err := json.Marshal(map[string]any{"request": payload, "response_schema": schema})
	if err != nil {
		return "", fmt.Errorf("encode Router request: %w", err)
	}
	lineage := provideriface.ShortProviderLineageKey("swarm_mode_router", b.parentID, b.callID, identityKey, b.runtime.Preference.Model, b.runtime.Preference.Thinking, systemPrompt, string(requestJSON))
	req := provideriface.Request{
		SessionID: b.parentID, ProviderLineageID: lineage,
		ProviderCacheKey: providerScopedKey("cache", lineage), SessionAffinityKey: providerScopedKey("affinity", lineage),
		BoundaryReason: "swarm_mode_router_one_shot", NativeContinuationAllowed: false, ForceFreshProviderContext: true,
		Instructions: strings.TrimSpace(systemPrompt),
		Input:        []map[string]any{{"role": "user", "content": []map[string]any{{"type": "input_text", "text": string(requestJSON)}}}},
		ToolChoice:   "none", Tools: nil, ParallelToolCalls: false,
	}
	req = b.runtime.apply(req)
	callCtx := ctx
	if b.principal.Valid() {
		callCtx = identity.ContextWithPrincipal(callCtx, b.principal)
	}
	callCtx, cancel := context.WithTimeout(callCtx, swarmRouterTimeout)
	defer cancel()

	var output, reasoning strings.Builder
	overLimit := false
	response, err := b.runner.CreateResponseStreaming(callCtx, req, func(event provideriface.StreamEvent) {
		switch event.Type {
		case provideriface.StreamEventOutputTextDelta:
			output.WriteString(event.Delta)
		case provideriface.StreamEventReasoningSummaryDelta:
			if event.DeltaMode == provideriface.StreamEventDeltaModeReplace {
				reasoning.Reset()
			}
			reasoning.WriteString(event.Delta)
		}
		if len([]rune(output.String())) > swarmRouterMaxOutputRunes || len([]rune(reasoning.String())) > swarmRouterMaxOutputRunes {
			overLimit = true
			cancel()
		}
	})
	if overLimit {
		return "", fmt.Errorf("Router output exceeded %d characters", swarmRouterMaxOutputRunes)
	}
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(firstNonEmptyString(response.Text, output.String(), response.ReasoningSummary, reasoning.String()))
	if text == "" {
		return "", errors.New("Router returned no output")
	}
	return text, nil
}

func mustMarshalSwarmTaskArguments(parsed taskCallArguments, request swarmmode.ToolRequest) string {
	launches := make([]map[string]any, 0, len(parsed.Launches))
	for _, launch := range parsed.Launches {
		launches = append(launches, map[string]any{
			"subagent_type":       launch.RequestedSubagentType,
			"meta_prompt":         launch.MetaPrompt,
			"title":               launch.AssignmentLabel,
			"deliverable":         launch.Deliverable,
			"concurrency_reason":  launch.ConcurrencyReason,
			"owned_scope":         launch.OwnedScope,
			"dependency_evidence": launch.DependencyEvidence,
		})
	}
	raw, _ := json.Marshal(map[string]any{
		"action": parsed.Action, "description": parsed.Description, "prompt": parsed.Prompt,
		"launches": launches, "swarm_request": request,
	})
	return string(raw)
}

func buildSwarmTaskArguments(request swarmmode.ToolRequest, refined []swarmmode.RefinedPrompt) (taskCallArguments, error) {
	if err := swarmmode.ValidateToolRequest(request); err != nil {
		return taskCallArguments{}, err
	}
	if len(refined) != 0 {
		if err := swarmmode.ValidateRefinedPrompts(refined, request.Count); err != nil {
			return taskCallArguments{}, err
		}
	}
	launches := make([]taskLaunchSpec, request.Count)
	for i := 0; i < request.Count; i++ {
		index := i + 1
		metaPrompt := fmt.Sprintf("Pending Router hydration for swarm item %d.", index)
		if len(refined) != 0 {
			metaPrompt = strings.TrimSpace(refined[i].Prompt)
		}
		ownedScope := []string(nil)
		if strings.TrimSpace(request.OwnedScopeTemplate) != "" {
			target, err := swarmmode.OwnedScopeForIndex(request.OwnedScopeTemplate, index)
			if err != nil {
				return taskCallArguments{}, err
			}
			ownedScope = []string{target}
		}
		launches[i] = taskLaunchSpec{
			RequestedSubagentType: string(request.AgentType), MetaPrompt: metaPrompt,
			AssignmentLabel:    fmt.Sprintf("Swarm %s %d", request.AgentType, index),
			Deliverable:        request.OutputContract,
			ConcurrencyReason:  "Independent hydrated swarm specialization",
			OwnedScope:         ownedScope,
			DependencyEvidence: "The shared brief and two Router hydration rounds are complete before canonical task launch.",
			SourceArguments:    map[string]any{"swarm_index": index, "agent_type": request.AgentType},
		}
		applyCanonicalCoderOwnedScope(&launches[i])
	}
	if err := validateTaskDesignerScopes(launches); err != nil {
		return taskCallArguments{}, err
	}
	return taskCallArguments{
		Action:          "spawn",
		Description:     fmt.Sprintf("Hydrated %s swarm (%d agents)", request.AgentType, request.Count),
		Prompt:          strings.TrimSpace("Shared parent brief:\n" + request.Prompt + "\n\nShared output contract:\n" + request.OutputContract),
		Launches:        launches,
		SourceArguments: map[string]any{"swarm_request": request},
	}, nil
}

func (s *Service) buildSwarmModePermissionPayload(sessionID, sessionMode string, call tool.Call) (taskLaunchManifest, error) {
	request, err := swarmmode.DecodeToolRequest(call.Arguments)
	if err != nil {
		return taskLaunchManifest{}, err
	}
	parsed, err := buildSwarmTaskArguments(request, nil)
	if err != nil {
		return taskLaunchManifest{}, err
	}
	taskCall := tool.Call{CallID: call.CallID, Name: "task", Arguments: mustMarshalSwarmTaskArguments(parsed, request)}
	manifest, err := s.buildTaskLaunchPermissionPayload(sessionID, sessionMode, taskCall)
	if err != nil {
		return taskLaunchManifest{}, err
	}
	manifest.SwarmMode = true
	manifest.HydrationRounds = 2
	digest, err := taskLaunchManifestDigest(manifest)
	if err != nil {
		return taskLaunchManifest{}, err
	}
	manifest.ManifestHash = digest
	approvedManifest := manifest
	approvedManifest.ApprovedArguments = nil
	manifest.ApprovedArguments = map[string]any{"manifest_hash": digest, "manifest": approvedManifest}
	return manifest, nil
}

func validateApprovedSwarmRequest(request swarmmode.ToolRequest, approvedArguments string) error {
	placeholder, err := buildSwarmTaskArguments(request, nil)
	if err != nil {
		return err
	}
	manifest, err := parseApprovedTaskLaunchManifest(approvedArguments, placeholder.Launches)
	if err != nil {
		return err
	}
	if !manifest.SwarmMode || manifest.HydrationRounds != 2 {
		return errors.New("approved task manifest is not a two-round Swarm launch")
	}
	rawApproved, err := json.Marshal(manifest.SourceArguments["swarm_request"])
	if err != nil {
		return err
	}
	approvedRequest, err := swarmmode.DecodeToolRequest(string(rawApproved))
	if err != nil {
		return fmt.Errorf("approved Swarm request is invalid: %w", err)
	}
	currentRaw, _ := json.Marshal(request)
	approvedRaw, _ := json.Marshal(approvedRequest)
	if string(currentRaw) != string(approvedRaw) {
		return errors.New("approved Swarm request does not match the current request")
	}
	return nil
}

type swarmHydrationProgress func(round int, state, summary string)

func runSwarmRouterPipeline(ctx context.Context, request swarmmode.ToolRequest, bridge swarmRouterBridge, progress swarmHydrationProgress) ([]swarmmode.IndexedTheme, []swarmmode.RefinedPrompt, error) {
	if bridge == nil {
		return nil, nil, errors.New("swarm Router bridge is not configured")
	}
	if progress != nil {
		progress(1, "running", fmt.Sprintf("Router is hydrating %d distinct themes", request.Count))
	}
	roundOneRequest := swarmmode.RoundOneRequest{Prompt: request.Prompt, AgentType: request.AgentType, Count: request.Count, Themes: append([]string(nil), request.Themes...)}
	raw, err := bridge.OneShot(ctx, swarmmode.RoundOneSystemPrompt(), roundOneRequest, swarmmode.RoundOneResultSchema(request.Count), "hydrate:1")
	if err != nil {
		return nil, nil, fmt.Errorf("swarm hydration round 1 failed: %w", sanitizeSwarmRouterError(err))
	}
	roundOne, err := swarmmode.DecodeRoundOneResult(raw, roundOneRequest)
	if err != nil {
		return nil, nil, fmt.Errorf("swarm hydration round 1 failed: %w", err)
	}
	if progress != nil {
		progress(1, "completed", fmt.Sprintf("Hydrated %d distinct themes", len(roundOne.Themes)))
		progress(2, "running", fmt.Sprintf("Router is hydrating %d final worker prompts", request.Count))
	}
	roundTwoRequest := swarmmode.RoundTwoRequest{
		Prompt: request.Prompt, AgentType: request.AgentType, OutputContract: request.OutputContract,
		OwnedScopeTemplate: request.OwnedScopeTemplate, Themes: roundOne.Themes,
	}
	raw, err = bridge.OneShot(ctx, swarmmode.RoundTwoSystemPrompt(), roundTwoRequest, swarmmode.RoundTwoResultSchema(request.Count), "hydrate:2")
	if err != nil {
		return nil, nil, fmt.Errorf("swarm hydration round 2 failed: %w", sanitizeSwarmRouterError(err))
	}
	roundTwo, err := swarmmode.DecodeRoundTwoResult(raw, roundTwoRequest)
	if err != nil {
		return nil, nil, fmt.Errorf("swarm hydration round 2 failed: %w", err)
	}
	if progress != nil {
		progress(2, "completed", fmt.Sprintf("Hydrated %d final worker prompts", len(roundTwo.Prompts)))
	}
	return roundOne.Themes, roundTwo.Prompts, nil
}

func emitSwarmHydrationProgress(emit StreamHandler, step int, callID string, round int, state, summary string) {
	if emit == nil {
		return
	}
	payload := map[string]any{
		"tool": "swarm_mode", "path_id": swarmModeHydrationPathID,
		"stage": "hydration", "round": round, "rounds": 2,
		"status": strings.TrimSpace(state), "summary": strings.TrimSpace(summary),
	}
	raw, err := json.Marshal(payload)
	if err == nil {
		emit(StreamEvent{Type: StreamEventToolDelta, Step: step, ToolName: "swarm_mode", CallID: strings.TrimSpace(callID), Output: string(raw)})
	}
}

func (s *Service) executeSwarmModeTool(ctx context.Context, sessionID, sessionMode string, step int, call tool.Call, approvedArguments string, emit StreamHandler, req taskExecutionRequest) (string, error) {
	request, err := swarmmode.DecodeToolRequest(call.Arguments)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(approvedArguments) == "" {
		return "", errors.New("swarm_mode requires fresh user approval")
	}
	if err := validateApprovedSwarmRequest(request, approvedArguments); err != nil {
		return "", err
	}
	parent, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	bridge, err := s.newConfiguredSwarmRouterBridge(parent, req.Principal, call.CallID)
	if err != nil {
		return "", err
	}
	themes, refined, err := runSwarmRouterPipeline(ctx, request, bridge, func(round int, state, summary string) {
		emitSwarmHydrationProgress(emit, step, call.CallID, round, state, summary)
	})
	if err != nil {
		if s.permissions != nil && strings.TrimSpace(req.RunID) != "" {
			_ = s.permissions.FinishSubagentWave(parent.ID, req.RunID, strings.TrimSpace(call.CallID), "failed")
		}
		return "", err
	}
	parsed, err := buildSwarmTaskArguments(request, refined)
	if err != nil {
		return "", fmt.Errorf("build hydrated task launch specs: %w", err)
	}
	emitSwarmHydrationProgress(emit, step, call.CallID, 2, "launching", fmt.Sprintf("Hydration complete; launching %d workers through the task tool", request.Count))
	output, taskErr := s.executeTaskToolWithParsed(ctx, sessionID, sessionMode, step, call, emit, taskExecutionRequest{
		Parsed: parsed, ParsedProvided: true, ApprovedArguments: approvedArguments, RunID: req.RunID,
		Principal: req.Principal, ApplySessionMutation: req.ApplySessionMutation,
	})
	if output == "" {
		return output, taskErr
	}
	var payload map[string]any
	if json.Unmarshal([]byte(output), &payload) == nil {
		payload["tool"] = "swarm_mode"
		payload["path_id"] = swarmModeResultPathID
		payload["hydration_rounds"] = 2
		if launches, ok := payload["launches"].([]any); ok {
			for i, item := range launches {
				if row, ok := item.(map[string]any); ok && i < len(themes) {
					row["swarm_index"] = i + 1
					row["theme"] = truncateRunes(privacy.SanitizeText(themes[i].Theme), swarmmode.MaxThemeRunes)
				}
			}
		}
		if raw, marshalErr := json.Marshal(payload); marshalErr == nil {
			output = string(raw)
		}
	}
	return output, taskErr
}

func sanitizeSwarmRouterError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(privacy.SanitizeText(err.Error()))
	if message == "" {
		message = "Router request failed"
	}
	return errors.New(truncateRunes(message, taskLaunchReasonMaxRunes))
}
