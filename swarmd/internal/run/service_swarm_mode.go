package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
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
	swarmModePermissionPathID = "permission.swarm_mode.v1"
	swarmModeStreamPathID     = "tool.swarm_mode.stream.v1"
	swarmModeResultPathID     = "tool.swarm_mode.v1"
	swarmRouterTimeout        = 90 * time.Second
	swarmRouterMaxOutputRunes = 20000
)

type swarmModePermissionManifest struct {
	PathID            string                `json:"path_id"`
	Tool              string                `json:"tool"`
	Request           swarmmode.ToolRequest `json:"request"`
	LaunchCount       int                   `json:"launch_count"`
	RouterGroupCount  int                   `json:"router_group_count"`
	TaskManifest      taskLaunchManifest    `json:"task_manifest"`
	ManifestHash      string                `json:"manifest_hash"`
	ApprovedArguments map[string]any        `json:"approved_arguments,omitempty"`
}

type swarmRouterBridge interface {
	OneShot(context.Context, string, any, map[string]any, string) (string, error)
}

type configuredSwarmRouterBridge struct {
	runner    provideriface.Runner
	runtime   compactModelRuntime
	profile   pebblestore.AgentProfile
	principal identity.Principal
	parentID  string
	callID    string
}

func (s *Service) newConfiguredSwarmRouterBridge(parent pebblestore.SessionSnapshot, principal identity.Principal, callID string) (swarmRouterBridge, error) {
	if s == nil || s.model == nil || s.agents == nil || s.agentModelSettings == nil || s.providers == nil {
		return nil, errors.New("swarm Router services are not fully configured")
	}
	accountScopeID := strings.TrimSpace(firstNonEmptyString(principal.AccountScopeID, parent.AccountScopeID))
	resolved, profile, err := agentmodel.ResolveSystemAgent(s.model, s.agents, s.agentModelSettings, accountScopeID, agentruntime.RouterAgentID, "")
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
	return &configuredSwarmRouterBridge{runner: runner, runtime: runtime, profile: profile, principal: principal, parentID: strings.TrimSpace(parent.ID), callID: strings.TrimSpace(callID)}, nil
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
	// The caller owns the stage-specific immutable prompt, schema, and strict
	// decoder; the configured profile contributes identity/model authority only.
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
	if len([]rune(text)) > swarmRouterMaxOutputRunes {
		return "", fmt.Errorf("Router output exceeded %d characters", swarmRouterMaxOutputRunes)
	}
	return text, nil
}

func swarmModeManifestDigest(manifest swarmModePermissionManifest) (string, error) {
	manifest.ManifestHash = ""
	manifest.ApprovedArguments = nil
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	var canonical any
	if err := json.Unmarshal(raw, &canonical); err != nil {
		return "", err
	}
	raw, err = json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) buildSwarmModePermissionPayload(sessionID, sessionMode string, call tool.Call) (swarmModePermissionManifest, error) {
	request, err := swarmmode.DecodeToolRequest(call.Arguments)
	if err != nil {
		return swarmModePermissionManifest{}, err
	}
	if s == nil || s.sessions == nil || s.uiSettings == nil {
		return swarmModePermissionManifest{}, errors.New("swarm_mode settings and session services are not configured")
	}
	parent, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return swarmModePermissionManifest{}, err
	}
	if !ok {
		return swarmModePermissionManifest{}, fmt.Errorf("session %q not found", sessionID)
	}
	settings, err := s.uiSettings.GetForAccount(parent.AccountScopeID)
	if err != nil {
		return swarmModePermissionManifest{}, fmt.Errorf("resolve swarm-mode maximum: %w", err)
	}
	maximum := swarmmode.NormalizeMaxAgents(settings.Chat.MaxSwarmAgents)
	if request.Count > maximum {
		return swarmModePermissionManifest{}, fmt.Errorf("swarm_mode count %d exceeds the account maximum of %d", request.Count, maximum)
	}
	parsed, err := buildSwarmTaskArguments(request, nil)
	if err != nil {
		return swarmModePermissionManifest{}, err
	}
	taskCall := tool.Call{CallID: call.CallID, Name: "task", Arguments: mustMarshalTaskArguments(parsed)}
	taskManifest, err := s.buildTaskLaunchPermissionPayload(sessionID, sessionMode, taskCall)
	if err != nil {
		return swarmModePermissionManifest{}, err
	}
	approvedTaskManifest := taskManifest
	approvedTaskManifest.ApprovedArguments = nil
	manifest := swarmModePermissionManifest{
		PathID: swarmModePermissionPathID, Tool: "swarm_mode", Request: request,
		LaunchCount: request.Count, RouterGroupCount: (request.Count + swarmmode.RouterGroupSize - 1) / swarmmode.RouterGroupSize,
		TaskManifest: approvedTaskManifest,
	}
	digest, err := swarmModeManifestDigest(manifest)
	if err != nil {
		return swarmModePermissionManifest{}, err
	}
	manifest.ManifestHash = digest
	manifest.ApprovedArguments = cloneGenericMap(taskManifest.ApprovedArguments)
	manifest.ApprovedArguments["swarm_manifest_hash"] = digest
	manifest.ApprovedArguments["swarm_request"] = request
	return manifest, nil
}

func validateApprovedSwarmModeArguments(request swarmmode.ToolRequest, approvedArguments string) error {
	var envelope struct {
		ManifestHash      string                `json:"manifest_hash"`
		TaskManifest      taskLaunchManifest    `json:"manifest"`
		SwarmManifestHash string                `json:"swarm_manifest_hash"`
		SwarmRequest      swarmmode.ToolRequest `json:"swarm_request"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(approvedArguments)), &envelope); err != nil {
		return fmt.Errorf("approved swarm_mode manifest invalid: %w", err)
	}
	if strings.TrimSpace(envelope.ManifestHash) == "" || strings.TrimSpace(envelope.SwarmManifestHash) == "" {
		return errors.New("approved swarm_mode manifest is incomplete")
	}
	if envelope.TaskManifest.LaunchCount != request.Count || envelope.SwarmRequest.Count != request.Count {
		return errors.New("approved swarm_mode final launch count mismatch")
	}
	taskDigest, err := taskLaunchManifestDigest(envelope.TaskManifest)
	if err != nil {
		return err
	}
	if taskDigest != strings.TrimSpace(envelope.ManifestHash) || taskDigest != strings.TrimSpace(envelope.TaskManifest.ManifestHash) {
		return errors.New("approved swarm_mode task manifest snapshot hash mismatch")
	}
	manifest := swarmModePermissionManifest{
		PathID: swarmModePermissionPathID, Tool: "swarm_mode", Request: envelope.SwarmRequest,
		LaunchCount:      envelope.SwarmRequest.Count,
		RouterGroupCount: (envelope.SwarmRequest.Count + swarmmode.RouterGroupSize - 1) / swarmmode.RouterGroupSize,
		TaskManifest:     envelope.TaskManifest,
	}
	digest, err := swarmModeManifestDigest(manifest)
	if err != nil {
		return err
	}
	if digest != strings.TrimSpace(envelope.SwarmManifestHash) {
		return errors.New("approved swarm_mode manifest snapshot hash mismatch")
	}
	current := manifest
	current.Request = request
	current.LaunchCount = request.Count
	current.RouterGroupCount = (request.Count + swarmmode.RouterGroupSize - 1) / swarmmode.RouterGroupSize
	currentDigest, err := swarmModeManifestDigest(current)
	if err != nil {
		return err
	}
	if currentDigest != digest {
		return errors.New("approved swarm_mode request does not match the current normalized request")
	}
	return nil
}

func mustMarshalTaskArguments(parsed taskCallArguments) string {
	launches := make([]map[string]any, 0, len(parsed.Launches))
	for _, launch := range parsed.Launches {
		launches = append(launches, map[string]any{
			"subagent_type": launch.RequestedSubagentType, "meta_prompt": launch.MetaPrompt,
			"title": launch.AssignmentLabel, "deliverable": launch.Deliverable,
			"concurrency_reason": launch.ConcurrencyReason, "owned_scope": launch.OwnedScope,
			"dependency_evidence": launch.DependencyEvidence,
		})
	}
	raw, _ := json.Marshal(map[string]any{"action": parsed.Action, "description": parsed.Description, "prompt": parsed.Prompt, "launches": launches})
	return string(raw)
}

func buildSwarmTaskArguments(request swarmmode.ToolRequest, refined []swarmmode.RefinementResult) (taskCallArguments, error) {
	if err := swarmmode.ValidateToolRequest(request); err != nil {
		return taskCallArguments{}, err
	}
	if len(refined) != 0 {
		if err := swarmmode.ValidateRefinementResults(refined, request.Count); err != nil {
			return taskCallArguments{}, err
		}
	}
	launches := make([]taskLaunchSpec, request.Count)
	for i := 0; i < request.Count; i++ {
		index := i + 1
		metaPrompt := fmt.Sprintf("Pending validated Router specialization for swarm item %d.", index)
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
			AssignmentLabel: fmt.Sprintf("Swarm %s %d", request.AgentType, index),
			Deliverable:     request.OutputContract, ConcurrencyReason: "Independent themed swarm specialization",
			OwnedScope: ownedScope, DependencyEvidence: "The parent brief, output contract, and indexed theme are finalized before launch.",
			SourceArguments: map[string]any{"swarm_index": index, "agent_type": request.AgentType},
		}
		applyCanonicalCoderOwnedScope(&launches[i])
	}
	if err := validateTaskDesignerScopes(launches); err != nil {
		return taskCallArguments{}, err
	}
	return taskCallArguments{
		Action: "spawn", Description: fmt.Sprintf("Hierarchical %s swarm (%d agents)", request.AgentType, request.Count),
		Prompt:          strings.TrimSpace("Shared parent brief:\n" + request.Prompt + "\n\nShared output contract:\n" + request.OutputContract),
		Launches:        launches,
		SourceArguments: map[string]any{"prompt": request.Prompt, "agent_type": request.AgentType, "count": request.Count, "output_contract": request.OutputContract, "owned_scope_template": request.OwnedScopeTemplate},
	}, nil
}

func emitSwarmModeProgress(emit StreamHandler, step int, callID, stage, summary string, completed, total int) {
	if emit == nil {
		return
	}
	payload := map[string]any{
		"tool": "swarm_mode", "path_id": swarmModeStreamPathID, "status": "running", "stage": strings.TrimSpace(stage),
		"summary": strings.TrimSpace(summary), "completed": completed, "total": total,
	}
	raw, err := json.Marshal(payload)
	if err == nil {
		emit(StreamEvent{Type: StreamEventToolDelta, Step: step, ToolName: "swarm_mode", CallID: strings.TrimSpace(callID), Output: string(raw)})
	}
}

func executeBoundedIndexed[T any](ctx context.Context, count, parallel int, run func(context.Context, int) (T, error)) ([]T, []error) {
	results := make([]T, count)
	errs := make([]error, count)
	if count <= 0 {
		return results, errs
	}
	if parallel < 1 {
		parallel = 1
	}
	if parallel > count {
		parallel = count
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < parallel; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					errs[index] = ctx.Err()
					continue
				}
				results[index], errs[index] = run(ctx, index)
			}
		}()
	}
	for i := 0; i < count; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results, errs
}

type swarmPipelineProgress func(stage, summary string, completed, total int)

func runSwarmRouterPipeline(ctx context.Context, request swarmmode.ToolRequest, bridge swarmRouterBridge, progress swarmPipelineProgress) ([]swarmmode.IndexedTheme, []swarmmode.RefinementResult, error) {
	if bridge == nil {
		return nil, nil, errors.New("swarm Router bridge is not configured")
	}
	groupCount := (request.Count + swarmmode.RouterGroupSize - 1) / swarmmode.RouterGroupSize
	if progress != nil {
		progress("expansion", fmt.Sprintf("expanding %d bounded theme groups", groupCount), 0, groupCount)
	}
	groups, groupErrs := executeBoundedIndexed(ctx, groupCount, groupCount, func(callCtx context.Context, group int) (swarmmode.GroupExpansionResult, error) {
		start := group*swarmmode.RouterGroupSize + 1
		count := minInt(swarmmode.RouterGroupSize, request.Count-start+1)
		groupRequest := swarmmode.GroupExpansionRequest{Prompt: request.Prompt, AgentType: request.AgentType, StartIndex: start, Count: count}
		if len(request.Themes) != 0 {
			groupRequest.Themes = append([]string(nil), request.Themes[start-1:start-1+count]...)
		}
		raw, callErr := bridge.OneShot(callCtx, swarmmode.GroupExpansionSystemPrompt(), groupRequest, swarmmode.GroupExpansionResultSchema(count), fmt.Sprintf("expand:%d", group+1))
		if callErr != nil {
			return swarmmode.GroupExpansionResult{}, sanitizeSwarmRouterError(callErr)
		}
		return swarmmode.DecodeGroupExpansionResult(raw, groupRequest)
	})
	themes := make([]swarmmode.IndexedTheme, 0, request.Count)
	for group := range groups {
		if groupErrs[group] != nil {
			return nil, nil, fmt.Errorf("swarm expansion stage group %d failed: %w", group+1, groupErrs[group])
		}
		themes = append(themes, groups[group].Themes...)
	}
	if err := swarmmode.ValidateExpandedThemes(themes, request.Count); err != nil {
		return nil, nil, fmt.Errorf("swarm expansion stage validation failed: %w", err)
	}
	if progress != nil {
		progress("expansion", fmt.Sprintf("expanded %d bounded theme groups", groupCount), groupCount, groupCount)
		progress("refinement", fmt.Sprintf("refining %d themes", request.Count), 0, request.Count)
	}
	refined, refineErrs := executeBoundedIndexed(ctx, request.Count, swarmmode.HardMaxAgents, func(callCtx context.Context, i int) (swarmmode.RefinementResult, error) {
		theme := themes[i]
		refinement := swarmmode.RefinementRequest{Prompt: request.Prompt, AgentType: request.AgentType, OutputContract: request.OutputContract, Index: theme.Index, Theme: theme.Theme}
		if strings.TrimSpace(request.OwnedScopeTemplate) != "" {
			var scopeErr error
			refinement.OwnedScope, scopeErr = swarmmode.OwnedScopeForIndex(request.OwnedScopeTemplate, theme.Index)
			if scopeErr != nil {
				return swarmmode.RefinementResult{}, scopeErr
			}
		}
		raw, callErr := bridge.OneShot(callCtx, swarmmode.RefinementSystemPrompt(), refinement, swarmmode.RefinementResultSchema(), fmt.Sprintf("refine:%d", theme.Index))
		if callErr != nil {
			return swarmmode.RefinementResult{}, sanitizeSwarmRouterError(callErr)
		}
		return swarmmode.DecodeRefinementResult(raw, refinement)
	})
	for i, refineErr := range refineErrs {
		if refineErr != nil {
			return nil, nil, fmt.Errorf("swarm refinement stage item %d failed: %w", i+1, refineErr)
		}
	}
	if err := swarmmode.ValidateRefinementResults(refined, request.Count); err != nil {
		return nil, nil, fmt.Errorf("swarm refinement stage validation failed: %w", err)
	}
	if progress != nil {
		progress("refinement", fmt.Sprintf("refined %d themes", request.Count), request.Count, request.Count)
	}
	return themes, refined, nil
}

func (s *Service) executeSwarmModeTool(ctx context.Context, sessionID, sessionMode string, step int, call tool.Call, approvedArguments string, emit StreamHandler, req taskExecutionRequest) (output string, retErr error) {
	request, err := swarmmode.DecodeToolRequest(call.Arguments)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(approvedArguments) == "" {
		return "", errors.New("swarm_mode requires an approved canonical final-wave manifest")
	}
	if err := validateApprovedSwarmModeArguments(request, approvedArguments); err != nil {
		return "", err
	}
	parent, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	callID := strings.TrimSpace(call.CallID)
	reservationTransferred := false
	if s.permissions != nil && strings.TrimSpace(req.RunID) != "" {
		defer func() {
			if reservationTransferred {
				return
			}
			if finishErr := s.permissions.FinishSubagentWave(parent.ID, req.RunID, callID, "failed"); finishErr != nil && retErr == nil {
				retErr = fmt.Errorf("finish failed swarm reservation: %w", finishErr)
			}
		}()
	}

	bridge, err := s.newConfiguredSwarmRouterBridge(parent, req.Principal, callID)
	if err != nil {
		return "", err
	}
	groupCount := (request.Count + swarmmode.RouterGroupSize - 1) / swarmmode.RouterGroupSize
	themes, refined, err := runSwarmRouterPipeline(ctx, request, bridge, func(stage, summary string, completed, total int) {
		emitSwarmModeProgress(emit, step, callID, stage, summary, completed, total)
	})
	if err != nil {
		return "", err
	}
	parsed, err := buildSwarmTaskArguments(request, refined)
	if err != nil {
		return "", fmt.Errorf("build canonical swarm launch specs: %w", err)
	}
	emitSwarmModeProgress(emit, step, callID, "launch", fmt.Sprintf("launching %d canonical durable children", request.Count), 0, request.Count)
	reservationTransferred = true
	output, err = s.executeTaskToolWithParsed(ctx, sessionID, sessionMode, step, call, emit, taskExecutionRequest{
		Parsed: parsed, ParsedProvided: true, ApprovedArguments: approvedArguments, RunID: req.RunID,
		Principal: req.Principal, ApplySessionMutation: req.ApplySessionMutation,
	})
	if output == "" {
		return output, err
	}
	var payload map[string]any
	if json.Unmarshal([]byte(output), &payload) == nil {
		payload["tool"] = "swarm_mode"
		payload["path_id"] = swarmModeResultPathID
		payload["router_group_count"] = groupCount
		payload["router_refinement_count"] = request.Count
		delete(payload, "prompt")
		if launches, ok := payload["launches"].([]any); ok {
			for i, item := range launches {
				if row, ok := item.(map[string]any); ok {
					delete(row, "meta_prompt")
					row["swarm_index"] = i + 1
					row["theme"] = truncateRunes(privacy.SanitizeText(themes[i].Theme), swarmmode.MaxThemeRunes)
				}
			}
		}
		if raw, marshalErr := json.Marshal(payload); marshalErr == nil {
			output = string(raw)
		}
	}
	return output, err
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

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
