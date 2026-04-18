package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	sdk "github.com/github/copilot-sdk/go"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type AuthStatus struct {
	IsAuthenticated bool   `json:"isAuthenticated"`
	AuthType        string `json:"authType,omitempty"`
	Host            string `json:"host,omitempty"`
	Login           string `json:"login,omitempty"`
	StatusMessage   string `json:"statusMessage,omitempty"`
	CredentialID    string `json:"credentialId,omitempty"`
	CredentialLabel string `json:"credentialLabel,omitempty"`
}

type turnState struct {
	mu sync.Mutex

	assistantDeltas string
	assistantText   string
	reasoningDeltas string
	reasoningText   string
	model           string
	stopReason      string
	sessionError    string
	usage           provideriface.TokenUsage
}

func (m *Manager) GetAuthStatus(ctx context.Context) (AuthStatus, error) {
	callCtx, cancel := ensureTimeout(ctx, copilotUtilityTimeout)
	defer cancel()

	binding, err := m.resolveActiveAuthBinding(callCtx)
	if err != nil {
		return AuthStatus{}, err
	}
	return m.getAuthStatusForBinding(callCtx, binding)
}

func (m *Manager) GetAuthStatusForCredential(ctx context.Context, credential provideriface.AuthCredential) (AuthStatus, error) {
	callCtx, cancel := ensureTimeout(ctx, copilotUtilityTimeout)
	defer cancel()

	binding, err := m.resolveCredentialBinding(callCtx, pebblestore.AuthCredentialRecord{
		ID:           credential.ID,
		Provider:     credential.Provider,
		Type:         credential.Type,
		Label:        credential.Label,
		APIKey:       credential.APIKey,
		AccessToken:  credential.AccessToken,
		RefreshToken: credential.RefreshToken,
		AccountID:    credential.AccountID,
		ExpiresAt:    credential.ExpiresAt,
	})
	if err != nil {
		return AuthStatus{}, err
	}
	return m.getAuthStatusForBinding(callCtx, binding)
}

func (m *Manager) getAuthStatusForBinding(ctx context.Context, binding runtimeAuthBinding) (AuthStatus, error) {
	client, binding, err := m.getClientForBinding(ctx, binding)
	if err != nil {
		return AuthStatus{}, err
	}
	status, err := client.GetAuthStatus(ctx)
	if err != nil {
		return AuthStatus{}, fmt.Errorf("copilot auth status: %w", err)
	}

	out := AuthStatus{
		IsAuthenticated: status.IsAuthenticated,
		CredentialID:    strings.TrimSpace(binding.CredentialID),
		CredentialLabel: strings.TrimSpace(binding.CredentialLabel),
	}
	if status.AuthType != nil {
		out.AuthType = strings.TrimSpace(*status.AuthType)
	}
	if status.Host != nil {
		out.Host = strings.TrimSpace(*status.Host)
	}
	if status.Login != nil {
		out.Login = strings.TrimSpace(*status.Login)
	}
	if status.StatusMessage != nil {
		out.StatusMessage = strings.TrimSpace(*status.StatusMessage)
	}
	return out, nil
}

func (m *Manager) ListModels(ctx context.Context) ([]sdk.ModelInfo, error) {
	callCtx, cancel := ensureTimeout(ctx, copilotUtilityTimeout)
	defer cancel()

	client, _, err := m.getClient(callCtx)
	if err != nil {
		return nil, err
	}

	models, err := client.ListModels(callCtx)
	if err != nil {
		return nil, fmt.Errorf("copilot models.list: %w", err)
	}

	out := make([]sdk.ModelInfo, 0, len(models))
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		model.ID = id
		model.Name = strings.TrimSpace(model.Name)
		model.SupportedReasoningEfforts = normalizeReasoningEffortLevels(model.SupportedReasoningEfforts)
		out = append(out, model)
	}
	return out, nil
}

func (m *Manager) resolveSessionReasoningEffort(ctx context.Context, client *sdk.Client, modelID, thinking string) (string, error) {
	effort := normalizeReasoningEffort(thinking)
	if effort == "" {
		return "", nil
	}

	models, err := client.ListModels(ctx)
	if err != nil {
		return effort, nil
	}
	for _, model := range models {
		if !strings.EqualFold(strings.TrimSpace(model.ID), strings.TrimSpace(modelID)) {
			continue
		}
		supported := normalizeReasoningEffortLevels(model.SupportedReasoningEfforts)
		supportsEffort := model.Capabilities.Supports.ReasoningEffort || len(supported) > 0
		if !supportsEffort {
			return "", nil
		}
		if len(supported) == 0 {
			return effort, nil
		}
		for _, level := range supported {
			if level == effort {
				return effort, nil
			}
		}
		return "", nil
	}
	return effort, nil
}

func (m *Manager) RunTurn(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	if m == nil {
		return provideriface.Response{}, errors.New("copilot manager is not configured")
	}
	if strings.TrimSpace(req.Model) == "" {
		return provideriface.Response{}, errors.New("model is required")
	}

	callCtx, cancel := inheritContext(ctx)
	defer cancel()

	client, _, err := m.getClient(callCtx)
	if err != nil {
		return provideriface.Response{}, err
	}
	restartCh := make(chan struct{}, 1)

	reasoningEffort, err := m.resolveSessionReasoningEffort(callCtx, client, req.Model, req.Thinking)
	if err != nil {
		return provideriface.Response{}, err
	}

	sessionConfig, err := buildSessionConfig(callCtx, req, reasoningEffort, func() {
		select {
		case restartCh <- struct{}{}:
		default:
		}
	})
	if err != nil {
		return provideriface.Response{}, err
	}

	session, err := client.CreateSession(callCtx, &sessionConfig)
	if err != nil {
		return provideriface.Response{}, fmt.Errorf("create copilot sdk session: %w", err)
	}
	m.activeSessions.Add(1)
	defer m.activeSessions.Add(-1)
	defer func() {
		_ = session.Destroy()
	}()

	state := &turnState{}
	idleCh := make(chan struct{}, 1)
	errCh := make(chan error, 1)
	restartTurn := false

	unsubscribe := session.On(func(event sdk.SessionEvent) {
		state.applyEvent(event, onEvent)
		switch event.Type {
		case sdk.SessionIdle:
			select {
			case idleCh <- struct{}{}:
			default:
			}
		case sdk.SessionError:
			if message := strings.TrimSpace(state.sessionErrorValue()); message != "" {
				select {
				case errCh <- errors.New(message):
				default:
				}
			}
		}
	})
	defer unsubscribe()

	if _, err := session.Send(callCtx, sdk.MessageOptions{
		Prompt: buildPromptFromInput(req.Input),
	}); err != nil {
		return provideriface.Response{}, fmt.Errorf("send copilot sdk message: %w", err)
	}

	select {
	case <-idleCh:
	case <-restartCh:
		restartTurn = true
		abortCtx, abortCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = session.Abort(abortCtx)
		abortCancel()
		select {
		case <-idleCh:
		case err := <-errCh:
			return provideriface.Response{}, err
		case <-callCtx.Done():
			return provideriface.Response{}, callCtx.Err()
		}
	case err := <-errCh:
		return provideriface.Response{}, err
	case <-callCtx.Done():
		abortCtx, abortCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = session.Abort(abortCtx)
		abortCancel()
		return provideriface.Response{}, callCtx.Err()
	}

	response := state.response(req.Model)
	response.RestartTurn = restartTurn
	if !response.RestartTurn {
		if strings.TrimSpace(response.Text) == "" && strings.TrimSpace(response.ReasoningSummary) == "" {
			if sessionErr := strings.TrimSpace(state.sessionErrorValue()); sessionErr != "" {
				return provideriface.Response{}, errors.New(sessionErr)
			}
		}
	} else {
		messages, msgErr := session.GetMessages(callCtx)
		if msgErr == nil {
			response = state.snapshot(req.Model)
			response.RestartTurn = true
			if len(messages) > 0 {
				last := messages[len(messages)-1]
				if string(last.Type) == "abort" && strings.TrimSpace(response.StopReason) == "" {
					response.StopReason = "abort"
				}
			}
		}
	}
	return response, nil
}

func buildSessionConfig(ctx context.Context, req provideriface.Request, reasoningEffort string, onRestartTurn func()) (sdk.SessionConfig, error) {
	tools, availableTools, err := buildToolWrappers(ctx, req.Tools, req.ToolInvoker, onRestartTurn)
	if err != nil {
		return sdk.SessionConfig{}, err
	}

	instructions := composeSystemMessage(req.Instructions)
	config := sdk.SessionConfig{
		ClientName:          "swarmd",
		Model:               strings.TrimSpace(req.Model),
		ReasoningEffort:     reasoningEffort,
		Tools:               tools,
		SystemMessage:       &sdk.SystemMessageConfig{Mode: "append", Content: instructions},
		AvailableTools:      availableTools,
		OnPermissionRequest: buildPermissionHandler(availableTools),
		WorkingDirectory:    strings.TrimSpace(req.WorkspacePath),
		Streaming:           true,
		CustomAgents:        buildCustomAgents(req.Instructions, availableTools),
		InfiniteSessions:    &sdk.InfiniteSessionConfig{Enabled: sdk.Bool(false)},
	}
	return config, nil
}

func normalizeReasoningEffort(thinking string) string {
	switch strings.ToLower(strings.TrimSpace(thinking)) {
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh":
		return "high"
	default:
		return ""
	}
}

func normalizeReasoningEffortLevels(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		level := normalizeReasoningEffort(value)
		if level == "" {
			continue
		}
		if _, ok := seen[level]; ok {
			continue
		}
		seen[level] = struct{}{}
		out = append(out, level)
	}
	return out
}

func composeSystemMessage(instructions string) string {
	instructions = strings.TrimSpace(instructions)
	guardrail := "Use only the provided Swarm wrapper tools when tool use is required. Never call built-in Copilot tools, and never fabricate tool results."
	if instructions == "" {
		return guardrail
	}
	return instructions + "\n\n" + guardrail
}

func buildPromptFromInput(input []map[string]any) string {
	if len(input) == 0 {
		return "Continue the task using the latest context."
	}

	var builder strings.Builder
	builder.WriteString("Conversation transcript:\n\n")
	for _, item := range input {
		if len(item) == 0 {
			continue
		}
		if typeName, ok := stringField(item, "type"); ok {
			switch strings.ToLower(strings.TrimSpace(typeName)) {
			case "function_call":
				name, _ := stringField(item, "name")
				callID, _ := stringField(item, "call_id")
				arguments, _ := stringField(item, "arguments")
				builder.WriteString("[assistant.tool_call]")
				if strings.TrimSpace(name) != "" {
					builder.WriteString(" name=")
					builder.WriteString(strings.TrimSpace(name))
				}
				if strings.TrimSpace(callID) != "" {
					builder.WriteString(" call_id=")
					builder.WriteString(strings.TrimSpace(callID))
				}
				builder.WriteString("\n")
				if arguments = strings.TrimSpace(arguments); arguments != "" {
					builder.WriteString(arguments)
					builder.WriteString("\n")
				}
				builder.WriteString("\n")
				continue
			case "function_call_output":
				callID, _ := stringField(item, "call_id")
				output := normalizeModelInputScalar(item["output"])
				builder.WriteString("[tool.result]")
				if strings.TrimSpace(callID) != "" {
					builder.WriteString(" call_id=")
					builder.WriteString(strings.TrimSpace(callID))
				}
				builder.WriteString("\n")
				if output != "" {
					builder.WriteString(output)
					builder.WriteString("\n")
				}
				builder.WriteString("\n")
				continue
			}
		}

		role, _ := stringField(item, "role")
		content := normalizeConversationInputContent(item["content"])
		role = strings.TrimSpace(role)
		if role == "" {
			role = "unknown"
		}
		builder.WriteString("[")
		builder.WriteString(role)
		builder.WriteString("]\n")
		if content != "" {
			builder.WriteString(content)
			builder.WriteString("\n")
		}
		builder.WriteString("\n")
	}
	prompt := strings.TrimSpace(builder.String())
	if prompt == "" {
		return "Continue the task using the latest context."
	}
	return prompt
}

func normalizeConversationInputContent(raw any) string {
	switch value := raw.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(value)
	case []map[string]any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if text := normalizeConversationInputContent(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if text := normalizeConversationInputContent(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		typeName, _ := stringField(value, "type")
		switch strings.ToLower(strings.TrimSpace(typeName)) {
		case "input_text", "output_text", "text":
			text, _ := stringField(value, "text")
			return strings.TrimSpace(text)
		case "reasoning":
			summary, _ := stringField(value, "summary")
			if strings.TrimSpace(summary) != "" {
				return strings.TrimSpace(summary)
			}
			text, _ := stringField(value, "text")
			return strings.TrimSpace(text)
		default:
			if text, _ := stringField(value, "text"); strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(encoded))
		}
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(encoded))
	}
}

func normalizeModelInputScalar(raw any) string {
	switch value := raw.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(value)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(encoded))
	}
}

func stringField(item map[string]any, key string) (string, bool) {
	if item == nil {
		return "", false
	}
	value, ok := item[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	return text, true
}

func (s *turnState) applyEvent(event sdk.SessionEvent, onEvent func(provideriface.StreamEvent)) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	data := event.Data
	switch event.Type {
	case sdk.AssistantMessageDelta, sdk.AssistantStreamingDelta:
		delta := firstRawString(data.DeltaContent, data.Content)
		if delta == "" {
			return
		}
		s.assistantDeltas += delta
		if onEvent != nil {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: delta})
		}
	case sdk.AssistantMessage:
		text := strings.TrimSpace(firstString(data.Content, data.TransformedContent))
		if text != "" {
			s.assistantText = text
		}
	case sdk.AssistantReasoningDelta:
		delta := firstRawString(data.DeltaContent, data.ReasoningText, data.Content)
		if delta == "" {
			return
		}
		s.reasoningDeltas += delta
		if onEvent != nil {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventReasoningSummaryDelta, Delta: delta})
		}
	case sdk.AssistantReasoning:
		text := strings.TrimSpace(firstString(data.ReasoningText, data.Content))
		if text != "" {
			s.reasoningText = text
		}
	case sdk.SessionModelChange:
		if model := strings.TrimSpace(firstString(data.NewModel, data.CurrentModel, data.Model, data.SelectedModel)); model != "" {
			s.model = model
		}
	case sdk.AssistantTurnEnd:
		if stopReason := strings.TrimSpace(firstString(data.Reason, data.Message)); stopReason != "" {
			s.stopReason = stopReason
		}
	case sdk.AssistantUsage:
		applyCopilotUsageFields(&s.usage, data)
	case sdk.SessionUsageInfo:
		applyCopilotUsageFields(&s.usage, data)
		s.usage.Source = "copilot_session_usage"
		raw := buildCopilotSessionUsageRaw(data)
		if len(raw) > 0 {
			s.usage.APIUsageRaw = raw
			s.usage.APIUsageRawPath = "session.usage_info"
			s.usage.APIUsageHistory = append(s.usage.APIUsageHistory, cloneMap(raw))
			s.usage.APIUsagePaths = append(s.usage.APIUsagePaths, "session.usage_info")
		}
		if model := strings.TrimSpace(firstString(data.CurrentModel, data.Model, data.SelectedModel)); model != "" {
			s.model = model
		}
	case sdk.SessionError:
		if message := strings.TrimSpace(sessionErrorMessage(data)); message != "" {
			s.sessionError = message
		}
	}
}

func (s *turnState) sessionErrorValue() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionError
}

func (s *turnState) response(fallbackModel string) provideriface.Response {
	if s == nil {
		return provideriface.Response{Model: strings.TrimSpace(fallbackModel)}
	}
	return s.snapshot(fallbackModel)
}

func (s *turnState) snapshot(fallbackModel string) provideriface.Response {
	s.mu.Lock()
	defer s.mu.Unlock()

	assistantText := strings.TrimSpace(s.assistantText)
	if assistantText == "" {
		assistantText = strings.TrimSpace(s.assistantDeltas)
	}
	reasoningText := strings.TrimSpace(s.reasoningText)
	if reasoningText == "" {
		reasoningText = strings.TrimSpace(s.reasoningDeltas)
	}
	model := strings.TrimSpace(s.model)
	if model == "" {
		model = strings.TrimSpace(fallbackModel)
	}
	return provideriface.Response{
		Model:            model,
		Text:             assistantText,
		ReasoningSummary: reasoningText,
		StopReason:       strings.TrimSpace(s.stopReason),
		Usage:            s.usage,
	}
}

func applyCopilotUsageFields(usage *provideriface.TokenUsage, data sdk.Data) {
	if usage == nil {
		return
	}
	if value, ok := floatValue(data.InputTokens); ok {
		usage.InputTokens = value
	}
	if value, ok := floatValue(data.OutputTokens); ok {
		usage.OutputTokens = value
	}
	if value, ok := floatValue(data.CacheReadTokens); ok {
		usage.CacheReadTokens = value
	}
	if value, ok := floatValue(data.CacheWriteTokens); ok {
		usage.CacheWriteTokens = value
	}
	if value, ok := floatValue(data.CurrentTokens); ok {
		usage.TotalTokens = value
	}
}

func buildCopilotSessionUsageRaw(data sdk.Data) map[string]any {
	raw := map[string]any{}
	if value, ok := floatValue(data.CurrentTokens); ok {
		raw["current_tokens"] = value
	}
	if value, ok := floatValue(data.TokenLimit); ok {
		raw["token_limit"] = value
		remaining := value
		if current, ok := floatValue(data.CurrentTokens); ok {
			remaining -= current
			if remaining < 0 {
				remaining = 0
			}
		}
		raw["remaining_tokens"] = remaining
	}
	if value, ok := floatValue(data.InputTokens); ok {
		raw["input_tokens"] = value
	}
	if value, ok := floatValue(data.OutputTokens); ok {
		raw["output_tokens"] = value
	}
	if value, ok := floatValue(data.CacheReadTokens); ok {
		raw["cache_read_tokens"] = value
	}
	if value, ok := floatValue(data.CacheWriteTokens); ok {
		raw["cache_write_tokens"] = value
	}
	if model := strings.TrimSpace(firstString(data.CurrentModel, data.Model, data.SelectedModel)); model != "" {
		raw["model"] = model
	}
	if len(data.QuotaSnapshots) > 0 {
		quotas := make(map[string]any, len(data.QuotaSnapshots))
		for key, snapshot := range data.QuotaSnapshots {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			entry := map[string]any{
				"remaining_percentage":                 snapshot.RemainingPercentage,
				"used_requests":                        snapshot.UsedRequests,
				"entitlement_requests":                 snapshot.EntitlementRequests,
				"overage":                              snapshot.Overage,
				"is_unlimited_entitlement":             snapshot.IsUnlimitedEntitlement,
				"usage_allowed_with_exhausted_quota":   snapshot.UsageAllowedWithExhaustedQuota,
				"overage_allowed_with_exhausted_quota": snapshot.OverageAllowedWithExhaustedQuota,
			}
			if snapshot.ResetDate != nil {
				entry["reset_date"] = snapshot.ResetDate.UnixMilli()
			}
			quotas[key] = entry
		}
		if len(quotas) > 0 {
			raw["quota_snapshots"] = quotas
		}
	}
	if data.CopilotUsage != nil {
		tokenDetails := make([]map[string]any, 0, len(data.CopilotUsage.TokenDetails))
		for _, detail := range data.CopilotUsage.TokenDetails {
			tokenDetails = append(tokenDetails, map[string]any{
				"token_type":     detail.TokenType,
				"token_count":    detail.TokenCount,
				"batch_size":     detail.BatchSize,
				"cost_per_batch": detail.CostPerBatch,
			})
		}
		raw["copilot_usage"] = map[string]any{
			"total_nano_aiu": data.CopilotUsage.TotalNanoAiu,
			"token_details":  tokenDetails,
		}
	}
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func sessionErrorMessage(data sdk.Data) string {
	if message := strings.TrimSpace(firstString(data.Message)); message != "" {
		return message
	}
	if data.Error != nil && data.Error.ErrorClass != nil {
		if message := strings.TrimSpace(data.Error.ErrorClass.Message); message != "" {
			return message
		}
	}
	if message := strings.TrimSpace(firstString(data.ErrorReason)); message != "" {
		return message
	}
	return ""
}

func firstString(values ...*string) string {
	for _, value := range values {
		if value == nil {
			continue
		}
		if text := strings.TrimSpace(*value); text != "" {
			return text
		}
	}
	return ""
}

func firstRawString(values ...*string) string {
	for _, value := range values {
		if value == nil {
			continue
		}
		if *value != "" {
			return *value
		}
	}
	return ""
}

func floatValue(value *float64) (int64, bool) {
	if value == nil {
		return 0, false
	}
	return int64(*value), true
}

func cloneMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = cloneValue(value)
	}
	return out
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneValue(item))
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneMap(item))
		}
		return out
	default:
		return value
	}
}
