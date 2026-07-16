package app

import (
	"context"
	"strings"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
)

type apiChatBackend struct {
	api           *client.API
	sessionAPI    string
	targetSwarmID string
}

func newAPIChatBackend(api *client.API, sessionAPI ...string) *apiChatBackend {
	backend := &apiChatBackend{api: api, sessionAPI: "v3"}
	if len(sessionAPI) > 0 {
		backend.sessionAPI = strings.ToLower(strings.TrimSpace(sessionAPI[0]))
	}
	if len(sessionAPI) > 1 {
		backend.targetSwarmID = strings.TrimSpace(sessionAPI[1])
	}
	return backend
}

func (b *apiChatBackend) LoadMessages(ctx context.Context, sessionID string, afterSeq uint64, limit int) ([]ui.ChatMessageRecord, error) {
	if err := requireTUIV3SessionAPI(b.sessionAPI, "load chat messages"); err != nil {
		return nil, err
	}
	messages, err := b.api.ListSessionV3Messages(ctx, sessionID, afterSeq, limit)
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
	summary, hasSummary, _, err := b.api.GetSessionV3Usage(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !hasSummary {
		return nil, nil
	}
	return convertClientUsageSummary(&summary), nil
}

func (b *apiChatBackend) GetSessionMode(ctx context.Context, sessionID string) (string, error) {
	return b.api.GetSessionV3Mode(ctx, sessionID)
}

func (b *apiChatBackend) SetSessionMode(ctx context.Context, sessionID, mode string) (string, error) {
	return b.api.SetSessionV3Mode(ctx, sessionID, mode)
}

func (b *apiChatBackend) GetSessionPreference(ctx context.Context, sessionID string) (string, string, string, string, string, int, error) {
	resolved, err := b.api.GetSessionV3Preference(ctx, sessionID)
	if err != nil {
		return "", "", "", "", "", 0, err
	}
	return resolved.Preference.Provider, resolved.Preference.Model, resolved.Preference.Thinking, resolved.Preference.ServiceTier, resolved.Preference.ContextMode, resolved.ContextWindow, nil
}

func (b *apiChatBackend) SetSessionPreference(ctx context.Context, sessionID, provider, model, thinking, serviceTier, contextMode string) (string, string, string, string, string, int, error) {
	resolved, err := b.api.SetSessionV3Preference(ctx, sessionID, map[string]any{
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
	plan, ok, err := b.api.GetActiveSessionV3Plan(ctx, sessionID)
	if err != nil {
		return ui.ChatSessionPlan{}, false, err
	}
	if !ok {
		return ui.ChatSessionPlan{}, false, nil
	}
	return convertClientSessionPlan(plan), true, nil
}

func (b *apiChatBackend) SaveSessionPlan(ctx context.Context, sessionID string, plan ui.ChatSessionPlan) (ui.ChatSessionPlan, error) {
	saved, err := b.api.SaveSessionV3Plan(ctx, sessionID, client.SessionPlanUpsertRequest{
		ID:            strings.TrimSpace(plan.ID),
		PlanID:        strings.TrimSpace(plan.ID),
		Title:         strings.TrimSpace(plan.Title),
		Plan:          plan.Plan,
		Document:      clientSessionPlanDocumentFromAny(plan.Document),
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
	if err := requireTUIV3SessionAPI(b.sessionAPI, "stop chat run"); err != nil {
		return err
	}
	return b.api.StopSessionV3Run(ctx, sessionID, runID, b.targetSwarmID, "")
}

func v3RunIntentStatusTerminal(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled", "expired", "interrupted", "dispatch_blocked":
		return true
	default:
		return false
	}
}

func activeRunIntentLifecycle(sessionID string, intent *client.SessionV3RunIntent) *ui.ChatSessionLifecycle {
	if intent == nil || strings.TrimSpace(intent.RunID) == "" {
		return nil
	}
	return primaryRunIntentLifecycle(sessionID, *intent)
}

func primaryRunIntentLifecycle(sessionID string, intent client.SessionV3RunIntent) *ui.ChatSessionLifecycle {
	status := strings.ToLower(strings.TrimSpace(intent.Status))
	phase := status
	active := false
	if phase == "" {
		phase = "pending_executor"
	}
	stopReason := strings.TrimSpace(intent.BlockedReason)
	errorText := ""
	switch phase {
	case "pending_executor":
		phase = "starting"
		active = true
	case "running":
		active = true
	case "completed", "cancelled", "interrupted":
		active = false
	case "failed", "expired":
		phase = "errored"
		active = false
		errorText = stopReason
	case "dispatch_blocked":
		phase = "blocked"
		active = false
		errorText = stopReason
	}
	return &ui.ChatSessionLifecycle{
		SessionID:  strings.TrimSpace(sessionID),
		RunID:      strings.TrimSpace(intent.RunID),
		Active:     active,
		Phase:      phase,
		StartedAt:  intent.CreatedAt,
		EndedAt:    intent.UpdatedAt,
		UpdatedAt:  intent.UpdatedAt,
		StopReason: stopReason,
		Error:      errorText,
	}
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

func convertClientPermissions(records []client.PermissionRecord) []ui.ChatPermissionRecord {
	if len(records) == 0 {
		return nil
	}
	out := make([]ui.ChatPermissionRecord, 0, len(records))
	for _, record := range records {
		out = append(out, convertClientPermission(record))
	}
	return out
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
		ID:             strings.TrimSpace(plan.ID),
		Title:          strings.TrimSpace(plan.Title),
		Plan:           plan.Plan,
		Document:       plan.Document,
		Status:         strings.TrimSpace(plan.Status),
		ApprovalState:  strings.TrimSpace(plan.ApprovalState),
		Active:         plan.Active,
		CreatedAt:      plan.CreatedAt,
		UpdatedAt:      plan.UpdatedAt,
		PriorTitle:     strings.TrimSpace(plan.PriorTitle),
		PriorPlan:      plan.PriorPlan,
		DiffLines:      append([]string(nil), plan.DiffLines...),
		UpdateSummary:  strings.TrimSpace(plan.UpdateSummary),
		UpdateScope:    strings.TrimSpace(plan.UpdateScope),
		UpdateKind:     strings.TrimSpace(plan.UpdateKind),
		Version:        plan.Version,
		ParentRevision: plan.ParentRevision,
		Checkpoint:     plan.Checkpoint,
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
		Status:       event.Status,
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
		out.UsageSummary = convertClientUsageSummary(event.Result.UsageSummary)
	}
	return out
}
