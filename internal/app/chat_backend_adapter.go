package app

import (
	"context"
	"encoding/json"
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
	api        *client.API
	sessionAPI string
}

func newAPIChatBackend(api *client.API, sessionAPI ...string) *apiChatBackend {
	backend := &apiChatBackend{api: api}
	if len(sessionAPI) > 0 {
		backend.sessionAPI = strings.ToLower(strings.TrimSpace(sessionAPI[0]))
	}
	return backend
}

func (b *apiChatBackend) LoadMessages(ctx context.Context, sessionID string, afterSeq uint64, limit int) ([]ui.ChatMessageRecord, error) {
	var (
		messages []client.SessionMessage
		err      error
	)
	if strings.EqualFold(strings.TrimSpace(b.sessionAPI), "v3") {
		messages, err = b.api.ListSessionV3Messages(ctx, sessionID, afterSeq, limit)
	} else {
		messages, err = b.api.ListSessionMessages(ctx, sessionID, afterSeq, limit)
	}
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
	if strings.EqualFold(strings.TrimSpace(b.sessionAPI), "v3") {
		result, err := b.api.SendSessionV3Message(ctx, sessionID, client.SessionV3MessageOptions{Role: "user", Content: req.Prompt})
		if err != nil {
			return ui.ChatRunResponse{}, err
		}
		if onEvent != nil {
			onEvent(ui.ChatRunStreamEvent{Type: "message.stored", SessionID: sessionID, RunID: result.RunIntent.RunID, Message: ptrChatMessageRecord(convertClientMessage(result.Message))})
			onEvent(ui.ChatRunStreamEvent{Type: "session.lifecycle.updated", SessionID: sessionID, RunID: result.RunIntent.RunID, Lifecycle: primaryRunIntentLifecycle(sessionID, result.RunIntent)})
		}
		if !strings.EqualFold(strings.TrimSpace(result.RunIntent.Status), "pending_executor") {
			return ui.ChatRunResponse{
				UserMessage:          convertClientMessage(result.Message),
				NoAssistant:          true,
				PrimaryRunStatus:     strings.TrimSpace(result.RunIntent.Status),
				PrimaryBlockedReason: strings.TrimSpace(result.RunIntent.BlockedReason),
			}, nil
		}
		streamResp, streamErr := b.consumeSessionV3Run(ctx, sessionID, result.RunIntent, onEvent)
		if streamErr != nil {
			return ui.ChatRunResponse{}, streamErr
		}
		streamResp.UserMessage = convertClientMessage(result.Message)
		if strings.TrimSpace(streamResp.PrimaryRunStatus) == "" {
			streamResp.PrimaryRunStatus = strings.TrimSpace(result.RunIntent.Status)
		}
		return streamResp, nil
	}

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

func (b *apiChatBackend) consumeSessionV3Run(ctx context.Context, sessionID string, intent client.SessionV3RunIntent, onEvent func(ui.ChatRunStreamEvent)) (ui.ChatRunResponse, error) {
	var response ui.ChatRunResponse
	runID := strings.TrimSpace(intent.RunID)
	response.PrimaryRunStatus = strings.TrimSpace(intent.Status)
	response.PrimaryBlockedReason = strings.TrimSpace(intent.BlockedReason)
	afterSeq := intent.EventSeq
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	err := b.api.StreamSessionsV3Realtime(streamCtx, []client.V3RealtimeSubscription{{SessionID: sessionID, AfterSeq: afterSeq, SubscriptionID: "active-turn"}}, func(frame client.V3RealtimeFrame) {
		if frame.Event == nil || strings.ToLower(strings.TrimSpace(frame.Kind)) != "event" {
			return
		}
		event := v3StreamEventToChatEvent(*frame.Event)
		if strings.TrimSpace(event.SessionID) != strings.TrimSpace(sessionID) {
			return
		}
		if strings.TrimSpace(event.RunID) == "" {
			event.RunID = runID
		}
		if onEvent != nil {
			onEvent(event)
		}
		if event.Message != nil && strings.EqualFold(strings.TrimSpace(event.Message.Role), "assistant") {
			response.AssistantMessage = *event.Message
		}
		if runIntent, ok := v3RunIntentFromEvent(*frame.Event); ok && v3RunIntentMatchesRun(runIntent, runID) {
			response.PrimaryRunStatus = strings.TrimSpace(runIntent.Status)
			response.PrimaryBlockedReason = strings.TrimSpace(runIntent.BlockedReason)
			if v3RunIntentStatusTerminal(runIntent.Status) {
				if onEvent != nil {
					onEvent(ui.ChatRunStreamEvent{Type: "session.lifecycle.updated", SessionID: sessionID, RunID: runIntent.RunID, Lifecycle: primaryRunIntentLifecycle(sessionID, runIntent)})
				}
				cancel()
			}
		}
	})
	if err != nil {
		if v3RunIntentStatusTerminal(response.PrimaryRunStatus) {
			return response, nil
		}
		return ui.ChatRunResponse{}, err
	}
	if strings.TrimSpace(response.AssistantMessage.Content) == "" && v3RunIntentStatusTerminal(response.PrimaryRunStatus) {
		response.NoAssistant = true
	}
	return response, nil
}

func v3RunIntentFromEvent(event client.SessionV3Event) (client.SessionV3RunIntent, bool) {
	var payload map[string]any
	if len(event.Payload) > 0 {
		_ = json.Unmarshal(event.Payload, &payload)
	}
	intentPayload, _ := payload["run_intent"].(map[string]any)
	intent := client.SessionV3RunIntent{
		SessionID:     firstNonEmptyV3String(stringValue(intentPayload, "session_id"), stringValue(payload, "session_id"), strings.TrimSpace(event.SessionID)),
		RunID:         firstNonEmptyV3String(stringValue(intentPayload, "run_id"), stringValue(payload, "run_id")),
		Status:        firstNonEmptyV3String(stringValue(intentPayload, "status"), stringValue(payload, "status")),
		BlockedReason: firstNonEmptyV3String(stringValue(intentPayload, "blocked_reason"), stringValue(payload, "blocked_reason"), stringValue(payload, "error")),
		CreatedAt:     firstNonZeroInt64(int64Number(intentPayload, "created_at"), int64Number(payload, "created_at")),
		UpdatedAt:     firstNonZeroInt64(int64Number(intentPayload, "updated_at"), int64Number(payload, "updated_at"), event.TsUnixMS),
		EventSeq:      firstNonZeroUint64(uint64Number(intentPayload, "event_seq"), uint64Number(payload, "event_seq"), event.Seq),
	}
	if strings.TrimSpace(intent.RunID) == "" || strings.TrimSpace(intent.Status) == "" {
		return client.SessionV3RunIntent{}, false
	}
	return intent, true
}

func v3RunIntentMatchesRun(intent client.SessionV3RunIntent, runID string) bool {
	runID = strings.TrimSpace(runID)
	return runID == "" || strings.TrimSpace(intent.RunID) == runID
}

func v3RunIntentStatusTerminal(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "dispatch_blocked":
		return true
	default:
		return false
	}
}

func v3StreamEventToChatEvent(event client.SessionV3Event) ui.ChatRunStreamEvent {
	var payload map[string]any
	if len(event.Payload) > 0 {
		_ = json.Unmarshal(event.Payload, &payload)
	}
	out := ui.ChatRunStreamEvent{Type: strings.TrimSpace(event.EventType), SessionID: strings.TrimSpace(event.SessionID)}
	if out.SessionID == "" {
		out.SessionID = stringValue(payload, "session_id")
	}
	out.RunID = stringValue(payload, "run_id")
	out.Status = stringValue(payload, "status")
	out.Error = stringValue(payload, "error")
	switch strings.TrimSpace(event.EventType) {
	case "session.message.appended":
		out.Type = "message.stored"
		out.Message = messageFromV3Payload(payload, out.SessionID)
	case "session.assistant.started":
		out.Type = "session.lifecycle.updated"
		out.Lifecycle = &ui.ChatSessionLifecycle{SessionID: out.SessionID, RunID: out.RunID, Active: true, Phase: "running", StartedAt: event.TsUnixMS, UpdatedAt: event.TsUnixMS}
	case "session.assistant.delta":
		out.Type = "assistant.delta"
		out.Delta = stringValue(payload, "delta")
	case "session.assistant.completed":
		out.Type = "message.stored"
		out.Message = messageFromV3Payload(payload, out.SessionID)
	case "session.tool.started":
		out.Type = "tool.started"
		out.ToolName = firstNonEmptyV3String(stringValue(payload, "tool_name"), "tool")
		out.CallID = stringValue(payload, "call_id")
		out.Arguments = stringValue(payload, "arguments")
		out.Output = stringValue(payload, "output")
		out.RawOutput = stringValue(payload, "raw_output")
		out.Step = int(int64Number(payload, "step"))
		out.DurationMS = int64Number(payload, "duration_ms")
		if out.Summary == "" {
			out.Summary = out.ToolName
		}
	case "session.tool.delta":
		out.Type = "tool.delta"
		out.ToolName = firstNonEmptyV3String(stringValue(payload, "tool_name"), "tool")
		out.CallID = stringValue(payload, "call_id")
		out.Output = stringValue(payload, "output")
		out.RawOutput = stringValue(payload, "raw_output")
		out.Step = int(int64Number(payload, "step"))
		out.DurationMS = int64Number(payload, "duration_ms")
	case "session.tool.completed":
		out.Type = "tool.completed"
		out.ToolName = firstNonEmptyV3String(stringValue(payload, "tool_name"), "tool")
		out.CallID = stringValue(payload, "call_id")
		out.Arguments = stringValue(payload, "arguments")
		out.Output = stringValue(payload, "output")
		out.RawOutput = stringValue(payload, "raw_output")
		out.Step = int(int64Number(payload, "step"))
		out.DurationMS = int64Number(payload, "duration_ms")
	case "session.run.failed", "session.assistant.failed":
		out.Type = "turn.error"
		if out.Error == "" {
			out.Error = "Run failed"
		}
	}
	if out.RunID == "" && out.Message != nil {
		out.RunID = stringValue(out.Message.Metadata, "run_id")
	}
	return out
}

func messageFromV3Payload(payload map[string]any, fallbackSessionID string) *ui.ChatMessageRecord {
	messagePayload, _ := payload["message"].(map[string]any)
	if len(messagePayload) == 0 {
		return nil
	}
	metadata, _ := messagePayload["metadata"].(map[string]any)
	message := ui.ChatMessageRecord{
		ID:        stringValue(messagePayload, "id"),
		SessionID: firstNonEmptyV3String(stringValue(messagePayload, "session_id"), fallbackSessionID),
		Role:      stringValue(messagePayload, "role"),
		Content:   stringValue(messagePayload, "content"),
		GlobalSeq: uint64Number(messagePayload, "global_seq"),
		CreatedAt: int64Number(messagePayload, "created_at"),
		Metadata:  metadata,
	}
	return &message
}

func stringValue(payload map[string]any, key string) string {
	if value, ok := payload[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func firstNonEmptyV3String(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func uint64Number(payload map[string]any, key string) uint64 {
	switch value := payload[key].(type) {
	case float64:
		if value > 0 {
			return uint64(value)
		}
	case int:
		if value > 0 {
			return uint64(value)
		}
	case int64:
		if value > 0 {
			return uint64(value)
		}
	case uint64:
		return value
	}
	return 0
}

func int64Number(payload map[string]any, key string) int64 {
	switch value := payload[key].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	case uint64:
		return int64(value)
	}
	return 0
}

func firstNonZeroUint64(values ...uint64) uint64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func ptrChatMessageRecord(record ui.ChatMessageRecord) *ui.ChatMessageRecord {
	return &record
}

func primaryRunIntentLifecycle(sessionID string, intent client.SessionV3RunIntent) *ui.ChatSessionLifecycle {
	status := strings.ToLower(strings.TrimSpace(intent.Status))
	phase := status
	active := false
	if phase == "" {
		phase = "pending_executor"
	}
	switch phase {
	case "pending_executor":
		phase = "starting"
		active = true
	case "running":
		active = true
	case "completed", "failed", "dispatch_blocked":
		active = false
	}
	return &ui.ChatSessionLifecycle{
		SessionID:  strings.TrimSpace(sessionID),
		RunID:      strings.TrimSpace(intent.RunID),
		Active:     active,
		Phase:      phase,
		StartedAt:  intent.CreatedAt,
		EndedAt:    intent.UpdatedAt,
		UpdatedAt:  intent.UpdatedAt,
		StopReason: strings.TrimSpace(intent.BlockedReason),
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
