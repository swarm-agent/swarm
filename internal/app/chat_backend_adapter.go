package app

import (
	"context"
	"strings"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
)

func convertChatRunToolScope(scope *ui.ChatRunToolScope) *client.RunToolScope {
	if scope == nil {
		return nil
	}
	return &client.RunToolScope{
		Preset:        scope.Preset,
		AllowTools:    append([]string(nil), scope.AllowTools...),
		DenyTools:     append([]string(nil), scope.DenyTools...),
		BashPrefixes:  append([]string(nil), scope.BashPrefixes...),
		InheritPolicy: scope.InheritPolicy,
	}
}

func convertChatRunExecutionContext(ctx *ui.ChatRunExecutionContext) *client.RunExecutionContext {
	if ctx == nil {
		return nil
	}
	return &client.RunExecutionContext{
		WorkspacePath:      ctx.WorkspacePath,
		CWD:                ctx.CWD,
		WorktreeMode:       ctx.WorktreeMode,
		WorktreeRootPath:   ctx.WorktreeRootPath,
		WorktreeBranch:     ctx.WorktreeBranch,
		WorktreeBaseBranch: ctx.WorktreeBaseBranch,
	}
}

type apiChatBackend struct {
	api *client.API
}

func newAPIChatBackend(api *client.API) *apiChatBackend {
	return &apiChatBackend{
		api: api,
	}
}

func (b *apiChatBackend) LoadMessages(ctx context.Context, sessionID string, afterSeq uint64, limit int) ([]ui.ChatMessageRecord, error) {
	messages, err := b.api.ListSessionMessages(ctx, sessionID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ui.ChatMessageRecord, 0, len(messages))
	for _, message := range messages {
		out = append(out, convertClientMessage(message))
	}
	return out, nil
}

func (b *apiChatBackend) GetSessionUsageSummary(ctx context.Context, sessionID string) (*ui.ChatUsageSummary, error) {
	summary, hasSummary, _, err := b.api.GetSessionUsage(ctx, sessionID, 1)
	if err != nil {
		return nil, err
	}
	if !hasSummary {
		return nil, nil
	}
	return convertClientUsageSummary(&summary), nil
}

func (b *apiChatBackend) GetSessionMode(ctx context.Context, sessionID string) (string, error) {
	return b.api.GetSessionMode(ctx, sessionID)
}

func (b *apiChatBackend) SetSessionMode(ctx context.Context, sessionID, mode string) (string, error) {
	return b.api.SetSessionMode(ctx, sessionID, mode)
}

func (b *apiChatBackend) GetSessionPreference(ctx context.Context, sessionID string) (string, string, string, string, string, int, error) {
	resolved, err := b.api.GetSessionPreference(ctx, sessionID)
	if err != nil {
		return "", "", "", "", "", 0, err
	}
	return resolved.Preference.Provider, resolved.Preference.Model, resolved.Preference.Thinking, resolved.Preference.ServiceTier, resolved.Preference.ContextMode, resolved.ContextWindow, nil
}

func (b *apiChatBackend) SetSessionPreference(ctx context.Context, sessionID, provider, model, thinking, serviceTier, contextMode string) (string, string, string, string, string, int, error) {
	resolved, err := b.api.SetSessionPreference(ctx, sessionID, map[string]any{
		"provider":     provider,
		"model":        model,
		"thinking":     thinking,
		"service_tier": serviceTier,
		"context_mode": contextMode,
	})
	if err != nil {
		return "", "", "", "", "", 0, err
	}
	return resolved.Preference.Provider, resolved.Preference.Model, resolved.Preference.Thinking, resolved.Preference.ServiceTier, resolved.Preference.ContextMode, resolved.ContextWindow, nil
}

func (b *apiChatBackend) GetActiveSessionPlan(ctx context.Context, sessionID string) (ui.ChatSessionPlan, bool, error) {
	plan, ok, err := b.api.GetActiveSessionPlan(ctx, sessionID)
	if err != nil {
		return ui.ChatSessionPlan{}, false, err
	}
	if !ok {
		return ui.ChatSessionPlan{}, false, nil
	}
	return convertClientSessionPlan(plan), true, nil
}

func (b *apiChatBackend) SaveSessionPlan(ctx context.Context, sessionID string, plan ui.ChatSessionPlan) (ui.ChatSessionPlan, error) {
	saved, err := b.api.SaveSessionPlan(ctx, sessionID, client.SessionPlanUpsertRequest{
		ID:            strings.TrimSpace(plan.ID),
		PlanID:        strings.TrimSpace(plan.ID),
		Title:         strings.TrimSpace(plan.Title),
		Plan:          plan.Plan,
		Status:        strings.TrimSpace(plan.Status),
		ApprovalState: strings.TrimSpace(plan.ApprovalState),
	})
	if err != nil {
		return ui.ChatSessionPlan{}, err
	}
	return convertClientSessionPlan(saved), nil
}

func (b *apiChatBackend) ListPermissions(ctx context.Context, sessionID string, limit int) ([]ui.ChatPermissionRecord, error) {
	records, err := b.api.ListPermissions(ctx, sessionID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ui.ChatPermissionRecord, 0, len(records))
	for _, record := range records {
		out = append(out, convertClientPermission(record))
	}
	return out, nil
}

func (b *apiChatBackend) ListPendingPermissions(ctx context.Context, sessionID string, limit int) ([]ui.ChatPermissionRecord, error) {
	records, err := b.api.ListPendingPermissions(ctx, sessionID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ui.ChatPermissionRecord, 0, len(records))
	for _, record := range records {
		out = append(out, convertClientPermission(record))
	}
	return out, nil
}

func (b *apiChatBackend) ResolvePermission(ctx context.Context, sessionID, permissionID, action, reason string) (ui.ChatPermissionRecord, error) {
	record, err := b.api.ResolvePermission(ctx, sessionID, permissionID, action, reason)
	if err != nil {
		return ui.ChatPermissionRecord{}, err
	}
	return convertClientPermission(record), nil
}

func (b *apiChatBackend) ResolvePermissionWithArguments(ctx context.Context, sessionID, permissionID, action, reason, approvedArguments string) (ui.ChatPermissionRecord, error) {
	record, err := b.api.ResolvePermissionWithArguments(ctx, sessionID, permissionID, action, reason, approvedArguments)
	if err != nil {
		return ui.ChatPermissionRecord{}, err
	}
	return convertClientPermission(record), nil
}

func (b *apiChatBackend) GetPermissionPolicy(ctx context.Context) (ui.ChatPermissionPolicy, error) {
	policy, err := b.api.GetPermissionPolicy(ctx)
	if err != nil {
		return ui.ChatPermissionPolicy{}, err
	}
	out := ui.ChatPermissionPolicy{Version: policy.Version, UpdatedAt: policy.UpdatedAt}
	if len(policy.Rules) > 0 {
		out.Rules = make([]ui.ChatPermissionRule, 0, len(policy.Rules))
		for _, rule := range policy.Rules {
			out.Rules = append(out.Rules, ui.ChatPermissionRule{ID: rule.ID, Kind: rule.Kind, Decision: rule.Decision, Tool: rule.Tool, Pattern: rule.Pattern, CreatedAt: rule.CreatedAt, UpdatedAt: rule.UpdatedAt})
		}
	}
	return out, nil
}

func (b *apiChatBackend) AddPermissionRule(ctx context.Context, rule ui.ChatPermissionRule) (ui.ChatPermissionRule, error) {
	record, err := b.api.AddPermissionRule(ctx, client.PermissionRule{ID: rule.ID, Kind: rule.Kind, Decision: rule.Decision, Tool: rule.Tool, Pattern: rule.Pattern, CreatedAt: rule.CreatedAt, UpdatedAt: rule.UpdatedAt})
	if err != nil {
		return ui.ChatPermissionRule{}, err
	}
	return ui.ChatPermissionRule{ID: record.ID, Kind: record.Kind, Decision: record.Decision, Tool: record.Tool, Pattern: record.Pattern, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}, nil
}

func (b *apiChatBackend) RemovePermissionRule(ctx context.Context, ruleID string) (bool, error) {
	return b.api.RemovePermissionRule(ctx, ruleID)
}

func (b *apiChatBackend) ResetPermissionPolicy(ctx context.Context) (ui.ChatPermissionPolicy, error) {
	policy, err := b.api.ResetPermissionPolicy(ctx)
	if err != nil {
		return ui.ChatPermissionPolicy{}, err
	}
	out := ui.ChatPermissionPolicy{Version: policy.Version, UpdatedAt: policy.UpdatedAt}
	if len(policy.Rules) > 0 {
		out.Rules = make([]ui.ChatPermissionRule, 0, len(policy.Rules))
		for _, rule := range policy.Rules {
			out.Rules = append(out.Rules, ui.ChatPermissionRule{ID: rule.ID, Kind: rule.Kind, Decision: rule.Decision, Tool: rule.Tool, Pattern: rule.Pattern, CreatedAt: rule.CreatedAt, UpdatedAt: rule.UpdatedAt})
		}
	}
	return out, nil
}

func (b *apiChatBackend) ExplainPermission(ctx context.Context, mode, toolName, arguments string) (ui.ChatPermissionExplain, error) {
	explain, err := b.api.ExplainPermission(ctx, mode, toolName, arguments)
	if err != nil {
		return ui.ChatPermissionExplain{}, err
	}
	out := ui.ChatPermissionExplain{Decision: explain.Decision, Source: explain.Source, Reason: explain.Reason, ToolName: explain.ToolName, Command: explain.Command, RulePreview: explain.RulePreview}
	if explain.Rule != nil {
		out.Rule = &ui.ChatPermissionRule{ID: explain.Rule.ID, Kind: explain.Rule.Kind, Decision: explain.Rule.Decision, Tool: explain.Rule.Tool, Pattern: explain.Rule.Pattern, CreatedAt: explain.Rule.CreatedAt, UpdatedAt: explain.Rule.UpdatedAt}
	}
	return out, nil
}

func (b *apiChatBackend) ResolveAllPermissions(ctx context.Context, sessionID, action, reason string) ([]ui.ChatPermissionRecord, error) {
	records, err := b.api.ResolveAllPermissions(ctx, sessionID, action, reason)
	if err != nil {
		return nil, err
	}
	out := make([]ui.ChatPermissionRecord, 0, len(records))
	for _, record := range records {
		out = append(out, convertClientPermission(record))
	}
	return out, nil
}

func (b *apiChatBackend) StopRun(ctx context.Context, sessionID, runID string) error {
	return b.api.StopSessionRun(ctx, sessionID, runID)
}

func (b *apiChatBackend) RunTurn(ctx context.Context, sessionID string, req ui.ChatRunRequest) (ui.ChatRunResponse, error) {
	result, err := b.api.RunSessionWithOptions(ctx, sessionID, req.Prompt, req.AgentName, req.Instructions, client.RunSessionOptions{
		Compact:          req.Compact,
		Background:       req.Background,
		TargetKind:       req.TargetKind,
		TargetName:       req.TargetName,
		ToolScope:        convertChatRunToolScope(req.ToolScope),
		ExecutionContext: convertChatRunExecutionContext(req.ExecutionContext),
	})
	if err != nil {
		return ui.ChatRunResponse{}, err
	}
	toolMessages := make([]ui.ChatMessageRecord, 0, len(result.ToolMessages))
	for _, message := range result.ToolMessages {
		toolMessages = append(toolMessages, convertClientMessage(message))
	}
	commentary := make([]ui.ChatMessageRecord, 0, len(result.Commentary))
	for _, message := range result.Commentary {
		commentary = append(commentary, convertClientMessage(message))
	}
	return ui.ChatRunResponse{
		Model:            result.Model,
		Thinking:         result.Thinking,
		ReasoningSummary: result.ReasoningSummary,
		TurnUsage:        convertClientTurnUsage(result.TurnUsage),
		UsageSummary:     convertClientUsageSummary(result.UsageSummary),
		UserMessage:      convertClientMessage(result.UserMessage),
		ToolMessages:     toolMessages,
		Commentary:       commentary,
		AssistantMessage: convertClientMessage(result.AssistantMessage),
		TargetKind:       result.TargetKind,
		TargetName:       result.TargetName,
	}, nil
}

func (b *apiChatBackend) RunTurnStream(ctx context.Context, sessionID string, req ui.ChatRunRequest, onEvent func(ui.ChatRunStreamEvent)) (ui.ChatRunResponse, error) {
	result, err := b.api.RunSessionStreamWithOptions(ctx, sessionID, req.Prompt, req.AgentName, req.Instructions, client.RunSessionOptions{
		Compact:          req.Compact,
		Background:       req.Background,
		TargetKind:       req.TargetKind,
		TargetName:       req.TargetName,
		ToolScope:        convertChatRunToolScope(req.ToolScope),
		ExecutionContext: convertChatRunExecutionContext(req.ExecutionContext),
	}, func(event client.SessionRunStreamEvent) {
		if onEvent == nil {
			return
		}
		onEvent(convertClientRunStreamEvent(event))
	})
	if err != nil {
		return ui.ChatRunResponse{}, err
	}

	toolMessages := make([]ui.ChatMessageRecord, 0, len(result.ToolMessages))
	for _, message := range result.ToolMessages {
		toolMessages = append(toolMessages, convertClientMessage(message))
	}
	commentary := make([]ui.ChatMessageRecord, 0, len(result.Commentary))
	for _, message := range result.Commentary {
		commentary = append(commentary, convertClientMessage(message))
	}
	return ui.ChatRunResponse{
		Model:            result.Model,
		Thinking:         result.Thinking,
		ReasoningSummary: result.ReasoningSummary,
		TurnUsage:        convertClientTurnUsage(result.TurnUsage),
		UsageSummary:     convertClientUsageSummary(result.UsageSummary),
		UserMessage:      convertClientMessage(result.UserMessage),
		ToolMessages:     toolMessages,
		Commentary:       commentary,
		AssistantMessage: convertClientMessage(result.AssistantMessage),
		TargetKind:       result.TargetKind,
		TargetName:       result.TargetName,
	}, nil
}

func convertClientMessage(message client.SessionMessage) ui.ChatMessageRecord {
	return ui.ChatMessageRecord{
		ID:        message.ID,
		SessionID: message.SessionID,
		GlobalSeq: message.GlobalSeq,
		Role:      message.Role,
		Content:   message.Content,
		Metadata:  message.Metadata,
		CreatedAt: message.CreatedAt,
	}
}

func convertClientPermission(record client.PermissionRecord) ui.ChatPermissionRecord {
	return ui.ChatPermissionRecord{
		ID:                    record.ID,
		SessionID:             record.SessionID,
		RunID:                 record.RunID,
		CallID:                record.CallID,
		ToolName:              record.ToolName,
		ToolArguments:         record.ToolArguments,
		ApprovedArguments:     record.ApprovedArguments,
		Requirement:           record.Requirement,
		Mode:                  record.Mode,
		Status:                record.Status,
		Decision:              record.Decision,
		Reason:                record.Reason,
		Step:                  record.Step,
		PermissionRequestedAt: record.PermissionRequestedAt,
		ResolvedAt:            record.ResolvedAt,
		ExecutionStatus:       record.ExecutionStatus,
		Output:                record.Output,
		Error:                 record.Error,
		DurationMS:            record.DurationMS,
		StartedAt:             record.StartedAt,
		CompletedAt:           record.CompletedAt,
		CreatedAt:             record.CreatedAt,
		UpdatedAt:             record.UpdatedAt,
	}
}

func convertClientSessionPlan(plan client.SessionPlan) ui.ChatSessionPlan {
	return ui.ChatSessionPlan{
		ID:            strings.TrimSpace(plan.ID),
		Title:         strings.TrimSpace(plan.Title),
		Plan:          plan.Plan,
		Status:        strings.TrimSpace(plan.Status),
		ApprovalState: strings.TrimSpace(plan.ApprovalState),
	}
}

func convertClientTurnUsage(turn *client.SessionTurnUsage) *ui.ChatTurnUsage {
	if turn == nil {
		return nil
	}
	return &ui.ChatTurnUsage{
		ContextWindow:   turn.ContextWindow,
		TotalTokens:     turn.TotalTokens,
		CacheReadTokens: turn.CacheReadTokens,
		Transport:       turn.Transport,
		ConnectedViaWS:  cloneBoolPointer(turn.ConnectedViaWS),
	}
}

func convertClientUsageSummary(summary *client.SessionUsageSummary) *ui.ChatUsageSummary {
	if summary == nil {
		return nil
	}
	return &ui.ChatUsageSummary{
		ContextWindow:      summary.ContextWindow,
		TotalTokens:        summary.TotalTokens,
		CacheReadTokens:    summary.CacheReadTokens,
		RemainingTokens:    summary.RemainingTokens,
		Source:             summary.Source,
		LastRunID:          summary.LastRunID,
		LastTransport:      summary.LastTransport,
		LastConnectedViaWS: cloneBoolPointer(summary.LastConnectedViaWS),
	}
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func convertClientRunStreamEvent(event client.SessionRunStreamEvent) ui.ChatRunStreamEvent {
	out := ui.ChatRunStreamEvent{
		Type:         event.Type,
		SessionID:    event.SessionID,
		RunID:        event.RunID,
		Agent:        event.Agent,
		Step:         event.Step,
		Delta:        event.Delta,
		Summary:      event.Summary,
		ToolName:     event.ToolName,
		CallID:       event.CallID,
		Arguments:    event.Arguments,
		Output:       event.Output,
		RawOutput:    event.RawOutput,
		Error:        event.Error,
		DurationMS:   event.DurationMS,
		TurnUsage:    convertClientTurnUsage(event.TurnUsage),
		UsageSummary: convertClientUsageSummary(event.UsageSummary),
		Title:        event.Title,
		TitleStage:   event.TitleStage,
		Warning:      event.Warning,
	}
	if event.Lifecycle != nil {
		out.Lifecycle = &ui.ChatSessionLifecycle{
			SessionID:      event.Lifecycle.SessionID,
			RunID:          event.Lifecycle.RunID,
			Active:         event.Lifecycle.Active,
			Phase:          event.Lifecycle.Phase,
			StartedAt:      event.Lifecycle.StartedAt,
			EndedAt:        event.Lifecycle.EndedAt,
			UpdatedAt:      event.Lifecycle.UpdatedAt,
			Generation:     event.Lifecycle.Generation,
			StopReason:     event.Lifecycle.StopReason,
			Error:          event.Lifecycle.Error,
			OwnerTransport: event.Lifecycle.OwnerTransport,
		}
	}
	if event.Message != nil {
		msg := convertClientMessage(*event.Message)
		out.Message = &msg
	}
	if event.Permission != nil {
		perm := convertClientPermission(*event.Permission)
		out.Permission = &perm
	}
	if event.Result.SessionID != "" {
		toolMessages := make([]ui.ChatMessageRecord, 0, len(event.Result.ToolMessages))
		for _, message := range event.Result.ToolMessages {
			toolMessages = append(toolMessages, convertClientMessage(message))
		}
		out.Result = ui.ChatRunResponse{
			Model:            event.Result.Model,
			Thinking:         event.Result.Thinking,
			ReasoningSummary: event.Result.ReasoningSummary,
			UsageSummary:     convertClientUsageSummary(event.Result.UsageSummary),
			UserMessage:      convertClientMessage(event.Result.UserMessage),
			ToolMessages:     toolMessages,
			AssistantMessage: convertClientMessage(event.Result.AssistantMessage),
			TargetKind:       event.Result.TargetKind,
			TargetName:       event.Result.TargetName,
		}
	}
	return out
}
