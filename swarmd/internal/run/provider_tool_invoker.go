package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// ProviderManagedToolInvokerConfig configures provider-managed tool execution for
// callers that own the provider loop but need the canonical run.Service tool
// executor and persistence path.
type ProviderManagedToolInvokerConfig struct {
	SessionID            string
	PermissionSessionID  string
	RunID                string
	Step                 int
	SessionMode          string
	WorkspacePath        string
	WorkspaceRoots       []string
	WorkspaceOriginPath  string
	WorkspaceOriginRoots []string
	WorkspaceName        string
	Principal            identity.Principal
	Emit                 StreamHandler
	ApplySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)
	ProviderManagedV3    bool
	AgentProfile         pebblestore.AgentProfile
}

type providerToolInvokerConfig struct {
	sessionID            string
	permissionSessionID  string
	runID                string
	step                 int
	sessionMode          string
	workspacePath        string
	workspaceRoots       []string
	workspaceOriginPath  string
	workspaceOriginRoots []string
	workspaceName        string
	principal            identity.Principal
	emit                 StreamHandler
	applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)
	providerManagedV3    bool
	policy               *permission.Policy
	agentProfile         pebblestore.AgentProfile
}

func (config ProviderManagedToolInvokerConfig) internal() providerToolInvokerConfig {
	return providerToolInvokerConfig{
		sessionID:            strings.TrimSpace(config.SessionID),
		permissionSessionID:  strings.TrimSpace(config.PermissionSessionID),
		runID:                strings.TrimSpace(config.RunID),
		step:                 config.Step,
		sessionMode:          strings.TrimSpace(config.SessionMode),
		workspacePath:        strings.TrimSpace(config.WorkspacePath),
		workspaceRoots:       append([]string(nil), config.WorkspaceRoots...),
		workspaceOriginPath:  strings.TrimSpace(config.WorkspaceOriginPath),
		workspaceOriginRoots: append([]string(nil), config.WorkspaceOriginRoots...),
		workspaceName:        strings.TrimSpace(config.WorkspaceName),
		principal:            config.Principal,
		emit:                 config.Emit,
		applySessionMutation: config.ApplySessionMutation,
		providerManagedV3:    config.ProviderManagedV3,
		agentProfile:         config.AgentProfile,
	}
}

type providerToolInvoker struct {
	service *Service
	config  providerToolInvokerConfig
}

func (s *Service) newProviderToolInvoker(config providerToolInvokerConfig) provideriface.ToolInvoker {
	if s == nil {
		return nil
	}
	return &providerToolInvoker{
		service: s,
		config:  config,
	}
}

func (s *Service) NewProviderManagedToolInvoker(config ProviderManagedToolInvokerConfig) provideriface.ToolInvoker {
	return s.newProviderToolInvoker(config.internal())
}

func (i *providerToolInvoker) ExecuteTool(ctx context.Context, invocation provideriface.ToolInvocation) (provideriface.ToolExecutionResult, error) {
	if i == nil || i.service == nil {
		return provideriface.ToolExecutionResult{}, errors.New("provider tool invoker is not configured")
	}

	call := tool.Call{
		CallID:    strings.TrimSpace(invocation.CallID),
		Name:      strings.TrimSpace(invocation.Name),
		Arguments: strings.TrimSpace(invocation.Arguments),
	}
	if call.CallID == "" {
		call.CallID = "tool_call"
	}
	if call.Name == "" {
		call.Name = "tool"
	}
	if call.Arguments == "" {
		call.Arguments = "{}"
	}

	result, err := i.service.executeProviderManagedToolCall(ctx, i.config, call, cloneGenericMap(invocation.Metadata))
	if err != nil {
		return provideriface.ToolExecutionResult{}, err
	}

	return provideriface.ToolExecutionResult{
		CallID:       strings.TrimSpace(result.CallID),
		Name:         strings.TrimSpace(result.Name),
		Output:       strings.TrimSpace(result.Output),
		Error:        strings.TrimSpace(result.Error),
		DurationMS:   result.DurationMS,
		TextForModel: prepareToolOutputForModel(call, result),
		RestartTurn:  providerManagedToolRequiresTurnRestart(call, result),
	}, nil
}

func providerManagedToolRequiresTurnRestart(call tool.Call, result tool.Result) bool {
	payload := decodeToolPayload(strings.TrimSpace(result.Output))
	if payload == nil {
		return false
	}
	if mapBool(payload, "restart_turn") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(call.Name), "exit_plan_mode") && mapBool(payload, "mode_changed") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(call.Name), "plan_manage") && strings.EqualFold(strings.TrimSpace(mapString(payload, "next_action")), "run_checkpoint_with_fresh_context") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(call.Name), "plan_manage") && providerManagedTerminalPlanNextAction(mapString(payload, "next_action")) {
		return true
	}
	return false
}

func providerManagedTerminalPlanNextAction(nextAction string) bool {
	switch strings.ToLower(strings.TrimSpace(nextAction)) {
	case "await_review", "plan_complete", "stopped":
		return true
	default:
		return false
	}
}

func (s *Service) executeProviderManagedToolCall(ctx context.Context, config providerToolInvokerConfig, call tool.Call, metadata map[string]any) (tool.Result, error) {
	if s == nil {
		return tool.Result{}, errors.New("run service is not configured")
	}
	if config.providerManagedV3 && config.applySessionMutation == nil {
		return tool.Result{}, errors.New("v3 provider-managed tool execution requires applySessionV3PrimaryMutation")
	}

	name := strings.TrimSpace(call.Name)
	callID := strings.TrimSpace(call.CallID)
	if name == "" {
		name = "tool"
	}
	if callID == "" {
		callID = "tool_call"
	}
	call.Name = name
	call.CallID = callID
	if strings.TrimSpace(call.Arguments) == "" {
		call.Arguments = "{}"
	}

	permissionSessionID := strings.TrimSpace(config.permissionSessionID)
	if permissionSessionID == "" {
		permissionSessionID = strings.TrimSpace(config.sessionID)
	}

	gatedResults := []tool.Result{{CallID: call.CallID, Name: call.Name}}
	approvedCalls := []tool.Call{call}
	permissionFeedback := []PermissionFeedback(nil)
	var err error
	gatedResults, approvedCalls, _, _, permissionFeedback, err = s.gateToolCalls(
		ctx,
		permissionSessionID,
		config.runID,
		config.step,
		config.sessionMode,
		[]tool.Call{call},
		config.emit,
		config.policy,
	)
	if err != nil {
		return tool.Result{}, err
	}

	result := gatedResults[0]
	if len(approvedCalls) > 0 {
		feedback := PermissionFeedback{}
		if len(permissionFeedback) > 0 {
			feedback = permissionFeedback[0]
		}

		principal, _ := identity.PrincipalFromContext(ctx)
		if !principal.Valid() {
			principal = config.principal
		}
		ctx = identity.ContextWithPrincipal(ctx, principal)
		handled, controlResult, controlErr := s.executeControlPlaneToolWithMutation(ctx, config.sessionID, config.sessionMode, config.agentProfile, config.step, call, feedback.ApprovedArguments, config.emit, config.applySessionMutation)
		if handled {
			if config.emit != nil {
				config.emit(StreamEvent{
					Type:      StreamEventToolStarted,
					Step:      config.step,
					ToolName:  name,
					CallID:    callID,
					Arguments: call.Arguments,
				})
			}
			result = controlResult
			if controlErr != nil {
				result.Error = strings.TrimSpace(controlErr.Error())
				if strings.TrimSpace(result.Output) == "" {
					result.Output = strings.TrimSpace(controlErr.Error())
				}
			}
		} else {
			if s.tools == nil {
				result = tool.Result{
					CallID: call.CallID,
					Name:   call.Name,
					Output: "tool runtime is not configured",
					Error:  "tool runtime is not configured",
				}
			} else {
				originRoots := append([]string(nil), config.workspaceOriginRoots...)
				if len(originRoots) == 0 {
					originRoots = append([]string(nil), config.workspaceRoots...)
				}
				workspaceCtx := runWorkspaceContext{
					WorkspacePath:        config.workspacePath,
					WorkspaceRoots:       append([]string(nil), config.workspaceRoots...),
					OriginWorkspacePath:  strings.TrimSpace(firstNonEmptyString(config.workspaceOriginPath, config.workspacePath)),
					OriginWorkspaceRoots: originRoots,
				}
				if len(workspaceCtx.OriginWorkspaceRoots) == 0 && strings.TrimSpace(workspaceCtx.OriginWorkspacePath) != "" {
					workspaceCtx.OriginWorkspaceRoots = []string{workspaceCtx.OriginWorkspacePath}
				}
				runtimeCalls := []tool.Call{call}
				scopeResults, scopeApprovedCalls, _, _, scopeErr := s.gateWorkspaceScopeCalls(
					ctx,
					config.sessionID,
					permissionSessionID,
					config.runID,
					config.step,
					config.sessionMode,
					workspaceCtx.OriginWorkspacePath,
					config.workspaceName,
					principal,
					&workspaceCtx,
					[]tool.Call{call},
					config.emit,
				)
				if scopeErr != nil {
					return tool.Result{}, scopeErr
				}
				if len(scopeApprovedCalls) == 0 && len(scopeResults) > 0 {
					result = scopeResults[0]
					runtimeCalls = nil
				} else {
					runtimeCalls = scopeApprovedCalls
				}
				if len(runtimeCalls) > 0 {
					if config.emit != nil {
						config.emit(StreamEvent{
							Type:      StreamEventToolStarted,
							Step:      config.step,
							ToolName:  name,
							CallID:    callID,
							Arguments: call.Arguments,
						})
					}
					runtimeCtx := tool.WithWorkspaceScope(ctx, tool.WorkspaceScope{
						PrimaryPath: workspaceCtx.WorkspacePath,
						Roots:       append([]string(nil), workspaceCtx.WorkspaceRoots...),
						Principal:   principal,
						SessionID:   strings.TrimSpace(config.sessionID),
					})
					executed := s.tools.ExecuteBatchStreamingWithProgress(runtimeCtx, workspaceCtx.WorkspacePath, runtimeCalls, func(_ int, current tool.Call, progress tool.Progress) {
						if config.emit == nil {
							return
						}
						stage := strings.ToLower(strings.TrimSpace(progress.Stage))
						if stage != "output" && stage != "image" {
							return
						}
						delta := progress.Output
						if delta == "" {
							return
						}
						metadata := map[string]any(nil)
						if len(progress.Metadata) > 0 {
							metadata = cloneGenericMap(progress.Metadata)
						}
						config.emit(StreamEvent{
							Type:     StreamEventToolDelta,
							Step:     config.step,
							ToolName: strings.TrimSpace(current.Name),
							CallID:   strings.TrimSpace(current.CallID),
							Output:   truncateRunes(delta, maxToolDeltaChars),
							Metadata: metadata,
						})
					}, nil)
					if len(executed) > 0 {
						result = executed[0]
					}
				}
			}
		}
	}

	if strings.TrimSpace(result.CallID) == "" {
		result.CallID = call.CallID
	}
	if strings.TrimSpace(result.Name) == "" {
		result.Name = call.Name
	}

	if config.emit != nil {
		config.emit(StreamEvent{
			Type:       StreamEventToolCompleted,
			Step:       config.step,
			ToolName:   strings.TrimSpace(result.Name),
			CallID:     strings.TrimSpace(result.CallID),
			Output:     formatToolCompletedOutput(call, result),
			RawOutput:  liveStreamRawOutput(call, result),
			Error:      strings.TrimSpace(result.Error),
			DurationMS: result.DurationMS,
		})
	}

	if err := s.storeProviderManagedToolResult(config, call, metadata, result); err != nil {
		return tool.Result{}, err
	}

	return result, nil
}

func (s *Service) storeProviderManagedToolResult(config providerToolInvokerConfig, call tool.Call, metadata map[string]any, result tool.Result) error {
	if config.providerManagedV3 {
		return s.storeProviderManagedToolResultV3(config, call, metadata, result)
	}
	return s.storeProviderManagedToolResultLegacy(config, newToolCallSnapshot(call), metadata, newToolResultSnapshot(result))
}

func (s *Service) storeProviderManagedToolResultV3(config providerToolInvokerConfig, call tool.Call, metadata map[string]any, result tool.Result) error {
	if s == nil || s.sessions == nil {
		return errors.New("session store is not configured")
	}
	session, ok, err := s.sessions.GetSession(config.sessionID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("session %q not found", config.sessionID)
	}
	principal := config.principal
	if !principal.Valid() {
		principal = identity.Principal{Type: identity.PrincipalTypeUser, UserID: session.UserID, AccountScopeID: session.AccountScopeID}
	}
	messageMetadata := providerManagedV3ToolMessageMetadata(config, call, metadata, result)
	content, err := formatV3ProviderManagedToolResultRecord(call, messageMetadata, result)
	if err != nil {
		return fmt.Errorf("marshal v3 provider tool result record: %w", err)
	}
	now := time.Now().UnixMilli()
	eventType := providerManagedV3ToolTerminalEventType(result)
	message := pebblestore.MessageSnapshot{
		ID:        providerManagedV3ToolMessageID(config.sessionID, config.runID, config.step, call.CallID),
		Role:      "tool",
		Content:   content,
		CreatedAt: now,
		Metadata:  messageMetadata,
	}
	payloadHash, err := providerManagedV3ToolPayloadHash(eventType, config.sessionID, config.runID, config.step, call, messageMetadata, result, content)
	if err != nil {
		return err
	}
	clientRequestID := providerManagedV3ToolClientRequestID(config.runID, config.step, call.CallID)
	eventPayload, err := providerManagedV3ToolEventPayload(eventType, config, call, metadata, result, now)
	if err != nil {
		return err
	}
	applyMutation := config.applySessionMutation
	if applyMutation == nil {
		return errors.New("v3 provider tool persistence requires applySessionV3PrimaryMutation")
	}
	mutation, err := applyMutation(sessionruntime.SessionMutationInput{
		SessionID:       config.sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationAppendMessage,
		EventType:       eventType,
		EventPayload:    eventPayload,
		Message:         &message,
		NowUnixMs:       now,
	})
	if err != nil {
		return err
	}
	if config.emit != nil && mutation.Message != nil {
		config.emit(StreamEvent{Type: StreamEventMessageStored, Step: config.step, Message: mutation.Message})
	}
	return nil
}

func providerManagedV3ToolEventPayload(eventType string, config providerToolInvokerConfig, call tool.Call, metadata map[string]any, result tool.Result, recordedAt int64) (json.RawMessage, error) {
	step := config.step
	if step <= 0 {
		step = 1
	}
	callID := strings.TrimSpace(firstNonEmptyString(result.CallID, call.CallID, "tool_call"))
	stepID := providerManagedV3ToolStepID(step)
	toolInstanceID := providerManagedV3ToolInstanceID(step, callID)
	payload := map[string]any{
		"run_id":           strings.TrimSpace(config.runID),
		"step":             step,
		"step_id":          stepID,
		"tool_name":        strings.TrimSpace(firstNonEmptyString(result.Name, call.Name, "tool")),
		"call_id":          callID,
		"tool_instance_id": toolInstanceID,
		"recorded_at":      recordedAt,
	}
	if eventType = strings.TrimSpace(eventType); eventType != "" {
		payload["type"] = eventType
	}
	if status := providerManagedV3ToolTerminalStatusForEventType(eventType); status != "" {
		payload["status"] = status
	}
	if args := strings.TrimSpace(call.Arguments); args != "" {
		payload["arguments"] = args
	}
	if output := formatToolCompletedOutput(call, result); output != "" {
		payload["output"] = output
	}
	if rawOutput := liveStreamRawOutput(call, result); rawOutput != "" {
		payload["raw_output"] = rawOutput
	}
	if errText := strings.TrimSpace(result.Error); errText != "" {
		payload["error"] = errText
	}
	if result.DurationMS != 0 {
		payload["duration_ms"] = result.DurationMS
	}
	if len(metadata) > 0 {
		payload["metadata"] = cloneGenericMap(metadata)
	}
	return json.Marshal(payload)
}

func providerManagedV3ToolTerminalEventType(result tool.Result) string {
	errText := strings.ToLower(strings.TrimSpace(result.Error))
	if errText == "" {
		return "session.tool.completed"
	}
	if strings.Contains(errText, "context canceled") || strings.Contains(errText, "context cancelled") {
		return "session.tool.cancelled"
	}
	return "session.tool.failed"
}

func providerManagedV3ToolTerminalStatusForEventType(eventType string) string {
	switch strings.TrimSpace(eventType) {
	case "session.tool.completed":
		return "completed"
	case "session.tool.failed":
		return "failed"
	case "session.tool.cancelled", "session.tool.canceled":
		return "cancelled"
	default:
		return ""
	}
}

func providerManagedV3ToolMessageMetadata(config providerToolInvokerConfig, call tool.Call, metadata map[string]any, result tool.Result) map[string]any {
	out := cloneGenericMap(metadata)
	if out == nil {
		out = map[string]any{}
	}
	step := config.step
	if step <= 0 {
		step = 1
	}
	callID := strings.TrimSpace(firstNonEmptyString(result.CallID, call.CallID, "tool_call"))
	out["run_id"] = strings.TrimSpace(config.runID)
	out["step"] = step
	out["step_id"] = providerManagedV3ToolStepID(step)
	out["tool_call_id"] = strings.TrimSpace(call.CallID)
	out["call_id"] = callID
	out["tool_instance_id"] = providerManagedV3ToolInstanceID(step, callID)
	out["tool_name"] = strings.TrimSpace(call.Name)
	out["executor_kind"] = "v3_provider_tool"
	if errText := strings.TrimSpace(result.Error); errText != "" {
		out["error"] = errText
	}
	if result.DurationMS != 0 {
		out["duration_ms"] = result.DurationMS
	}
	return out
}

func providerManagedV3ToolStepID(step int) string {
	if step <= 0 {
		step = 1
	}
	return fmt.Sprintf("step-%d", step)
}

func providerManagedV3ToolInstanceID(step int, callID string) string {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		callID = "tool_call"
	}
	return providerManagedV3ToolStepID(step) + ":" + callID
}

func providerManagedV3ToolClientRequestID(runID string, step int, callID string) string {
	callID = strings.NewReplacer(".", "_", "/", "_", " ", "_", ":", "_").Replace(providerManagedV3ToolInstanceID(step, callID))
	return fmt.Sprintf("v3-tool-%s-%04d-%s", strings.TrimSpace(runID), step, callID)
}

func providerManagedV3ToolMessageID(sessionID, runID string, step int, callID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(runID) + "\x00" + fmt.Sprint(step) + "\x00" + strings.TrimSpace(callID) + "\x00tool"))
	return "v3msg_tool_" + hex.EncodeToString(sum[:16])
}

func providerManagedV3ToolPayloadHash(eventType, sessionID, runID string, step int, call tool.Call, metadata map[string]any, result tool.Result, content string) (string, error) {
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		eventType = "session.tool.completed"
	}
	canonical := struct {
		Operation string         `json:"operation"`
		EventType string         `json:"event_type"`
		SessionID string         `json:"session_id"`
		RunID     string         `json:"run_id"`
		Step      int            `json:"step"`
		CallID    string         `json:"call_id"`
		Name      string         `json:"name"`
		Arguments string         `json:"arguments"`
		Metadata  map[string]any `json:"metadata,omitempty"`
		Output    string         `json:"output,omitempty"`
		Error     string         `json:"error,omitempty"`
		Content   string         `json:"content"`
	}{
		Operation: "v3.provider.tool.terminal",
		EventType: eventType,
		SessionID: strings.TrimSpace(sessionID),
		RunID:     strings.TrimSpace(runID),
		Step:      step,
		CallID:    strings.TrimSpace(call.CallID),
		Name:      strings.TrimSpace(call.Name),
		Arguments: strings.TrimSpace(call.Arguments),
		Metadata:  cloneGenericMap(metadata),
		Output:    strings.TrimSpace(result.Output),
		Error:     strings.TrimSpace(result.Error),
		Content:   content,
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal v3 tool payload hash: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
