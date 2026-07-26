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

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// ToolProgressionState owns the run-local consecutive-tool grouping contract.
type ToolProgressionState struct {
	mu           sync.Mutex
	lastIdentity string
	runCount     int
}

type ToolProgression struct {
	Identity string
	RunCount int
	Display  string
}

func (s *ToolProgressionState) Observe(toolName string) ToolProgression {
	identity := canonicalToolName(emptyToolName(toolName))
	if identity == "" {
		identity = "tool"
	}
	if s == nil {
		return ToolProgression{Identity: identity, RunCount: 1, Display: toolProgressionDisplay(identity, 1)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if identity == s.lastIdentity {
		s.runCount++
	} else {
		s.lastIdentity = identity
		s.runCount = 1
	}
	return ToolProgression{Identity: identity, RunCount: s.runCount, Display: toolProgressionDisplay(identity, s.runCount)}
}

func toolProgressionDisplay(identity string, runCount int) string {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return ""
	}
	if runCount <= 1 {
		return identity
	}
	return fmt.Sprintf("%s x%d", identity, runCount)
}

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
	ToolProgression      *ToolProgressionState
}

type terminalPlanToolState struct {
	mu       sync.Mutex
	terminal bool
}

func (s *terminalPlanToolState) MarkTerminal() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.terminal = true
	s.mu.Unlock()
}

func (s *terminalPlanToolState) IsTerminal() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminal
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
	toolProgression      *ToolProgressionState
	terminalPlanState    *terminalPlanToolState
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
		toolProgression:      config.ToolProgression,
		terminalPlanState:    &terminalPlanToolState{},
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

	result, permissionWaitMS, err := i.service.executeProviderManagedToolCall(ctx, i.config, call, cloneGenericMap(invocation.Metadata))
	if err != nil {
		return provideriface.ToolExecutionResult{}, err
	}

	restartTurn := providerManagedToolRequiresTurnRestart(call, result)
	if restartTurn && providerManagedToolResultIsTerminalPlan(call, result) {
		i.config.terminalPlanState.MarkTerminal()
	}
	return provideriface.ToolExecutionResult{
		CallID:           strings.TrimSpace(result.CallID),
		Name:             strings.TrimSpace(result.Name),
		Output:           strings.TrimSpace(result.Output),
		Error:            strings.TrimSpace(result.Error),
		DurationMS:       result.DurationMS,
		PermissionWaitMS: permissionWaitMS,
		TextForModel:     prepareToolOutputForModel(call, result),
		RestartTurn:      restartTurn,
	}, nil
}

func (s *Service) providerManagedWorkspaceContext(config providerToolInvokerConfig, principal identity.Principal) (runWorkspaceContext, error) {
	workspaceCtx := runWorkspaceContext{
		WorkspacePath:        strings.TrimSpace(config.workspacePath),
		WorkspaceRoots:       append([]string(nil), config.workspaceRoots...),
		OriginWorkspacePath:  strings.TrimSpace(firstNonEmptyString(config.workspaceOriginPath, config.workspacePath)),
		OriginWorkspaceRoots: providerManagedOriginWorkspaceRoots(config),
	}
	if s == nil || s.sessions == nil || strings.TrimSpace(config.sessionID) == "" {
		return normalizeProviderManagedWorkspaceContext(workspaceCtx), nil
	}
	session, ok, err := s.sessions.GetSession(config.sessionID)
	if err != nil {
		return runWorkspaceContext{}, err
	}
	if !ok {
		return runWorkspaceContext{}, fmt.Errorf("session %q not found", strings.TrimSpace(config.sessionID))
	}
	identityAvailable := principal.Valid() || (strings.TrimSpace(session.UserID) != "" && strings.TrimSpace(session.AccountScopeID) != "")
	if identityAvailable {
		if _, err := s.syncWorkspaceScopeFromSession(session, principal, &workspaceCtx); err != nil {
			return runWorkspaceContext{}, err
		}
		return normalizeProviderManagedWorkspaceContext(workspaceCtx), nil
	}
	if len(session.TemporaryWorkspaceRoots) > 0 {
		workspaceCtx.OriginWorkspaceRoots = mergeSessionWorkspaceRoots(workspaceCtx.OriginWorkspaceRoots, session.TemporaryWorkspaceRoots)
		workspaceCtx.WorkspaceRoots = mergeSessionWorkspaceRoots(workspaceCtx.WorkspaceRoots, session.TemporaryWorkspaceRoots)
	}
	return normalizeProviderManagedWorkspaceContext(workspaceCtx), nil
}

func normalizeProviderManagedWorkspaceContext(workspaceCtx runWorkspaceContext) runWorkspaceContext {
	if len(workspaceCtx.OriginWorkspaceRoots) == 0 && strings.TrimSpace(workspaceCtx.OriginWorkspacePath) != "" {
		workspaceCtx.OriginWorkspaceRoots = []string{workspaceCtx.OriginWorkspacePath}
	}
	if len(workspaceCtx.WorkspaceRoots) == 0 && strings.TrimSpace(workspaceCtx.WorkspacePath) != "" {
		workspaceCtx.WorkspaceRoots = []string{workspaceCtx.WorkspacePath}
	}
	return workspaceCtx
}

func providerManagedOriginWorkspaceRoots(config providerToolInvokerConfig) []string {
	originRoots := append([]string(nil), config.workspaceOriginRoots...)
	if len(originRoots) == 0 {
		originRoots = append([]string(nil), config.workspaceRoots...)
	}
	if len(originRoots) == 0 && strings.TrimSpace(config.workspaceOriginPath) != "" {
		originRoots = []string{strings.TrimSpace(config.workspaceOriginPath)}
	}
	return originRoots
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

func providerManagedToolResultIsTerminalPlan(call tool.Call, result tool.Result) bool {
	if canonicalToolName(call.Name) != "plan_manage" || strings.TrimSpace(result.Error) != "" {
		return false
	}
	payload := decodeToolPayload(strings.TrimSpace(result.Output))
	return payload != nil && providerManagedTerminalPlanNextAction(mapString(payload, "next_action"))
}

func providerManagedControlPlaneResponse(call tool.Call, feedback PermissionFeedback) string {
	approvedArguments := strings.TrimSpace(feedback.ApprovedArguments)
	if canonicalToolName(call.Name) != "ask_user" {
		return approvedArguments
	}
	// Permission storage normalizes an omitted approved_arguments value to an
	// empty JSON object. For ask_user that placeholder is not a response, so use
	// the captured permission message instead.
	if approvedArguments != "" && approvedArguments != "{}" {
		return approvedArguments
	}
	return strings.TrimSpace(feedback.Message)
}

func (s *Service) executeProviderManagedToolCall(ctx context.Context, config providerToolInvokerConfig, call tool.Call, metadata map[string]any) (tool.Result, int64, error) {
	if s == nil {
		return tool.Result{}, 0, errors.New("run service is not configured")
	}
	if config.providerManagedV3 && config.applySessionMutation == nil {
		return tool.Result{}, 0, errors.New("v3 provider-managed tool execution requires applySessionV3PrimaryMutation")
	}
	if metadata == nil {
		metadata = map[string]any{}
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
		return tool.Result{}, 0, err
	}

	permissionWaitMS := int64(0)
	if len(gatedResults) > 0 {
		permissionWaitMS = gatedResults[0].DurationMS
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
		handled := false
		var controlResult tool.Result
		var controlErr error
		if guardErr := s.rejectProviderManagedCheckpointRunFollowup(config, call); guardErr != nil {
			handled = true
			controlErr = guardErr
			controlResult = tool.Result{CallID: call.CallID, Name: call.Name}
		} else {
			lifecycleRun := planLifecycleRunContext{
				RunID:           strings.TrimSpace(config.runID),
				RunSessionID:    strings.TrimSpace(config.sessionID),
				ParentSessionID: strings.TrimSpace(config.sessionID),
				SourceMessageID: fmt.Sprintf("provider-run:%s:step:%d:call:%s", strings.TrimSpace(config.runID), config.step, strings.TrimSpace(call.CallID)),
				Inline:          config.providerManagedV3 && sessionruntime.NormalizeMode(config.sessionMode) == sessionruntime.ModeAuto,
			}
			controlResponse := providerManagedControlPlaneResponse(call, feedback)
			handled, controlResult, controlErr = s.executeControlPlaneToolWithLifecycleRunContext(ctx, config.sessionID, config.sessionMode, config.agentProfile, config.step, call, controlResponse, config.emit, config.applySessionMutation, lifecycleRun)
		}
		if handled {
			progression := recordProviderToolProgression(config.toolProgression, metadata, name)
			if config.emit != nil {
				config.emit(StreamEvent{
					Type:         StreamEventToolStarted,
					Step:         config.step,
					ToolName:     name,
					CallID:       callID,
					Arguments:    call.Arguments,
					ToolIdentity: progression.Identity,
					ToolRunCount: progression.RunCount,
					ToolDisplay:  progression.Display,
					Metadata:     cloneGenericMap(metadata),
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
				workspaceCtx, err := s.providerManagedWorkspaceContext(config, principal)
				if err != nil {
					return tool.Result{}, permissionWaitMS, err
				}
				runtimeCalls := []tool.Call{call}
				scopeResults, scopeApprovedCalls, _, _, scopePermissionWaitMS, scopeErr := s.gateWorkspaceScopeCalls(
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
				permissionWaitMS += scopePermissionWaitMS
				if scopeErr != nil {
					return tool.Result{}, permissionWaitMS, scopeErr
				}
				if len(scopeApprovedCalls) == 0 && len(scopeResults) > 0 {
					result = scopeResults[0]
					runtimeCalls = nil
				} else {
					runtimeCalls = scopeApprovedCalls
				}
				if len(runtimeCalls) > 0 {
					progression := recordProviderToolProgression(config.toolProgression, metadata, name)
					if config.emit != nil {
						config.emit(StreamEvent{
							Type:         StreamEventToolStarted,
							Step:         config.step,
							ToolName:     name,
							CallID:       callID,
							Arguments:    call.Arguments,
							ToolIdentity: progression.Identity,
							ToolRunCount: progression.RunCount,
							ToolDisplay:  progression.Display,
							Metadata:     cloneGenericMap(metadata),
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
						delta := truncateRunes(progress.Output, maxToolDeltaChars)
						if delta == "" {
							return
						}
						// Progress callbacks are already chunked by the tool runtime. Emit each
						// accepted chunk immediately so line-oriented commands remain live;
						// buffering here until maxToolDeltaChars or process exit defeats streaming.
						config.emit(StreamEvent{
							Type:     StreamEventToolDelta,
							Step:     config.step,
							ToolName: strings.TrimSpace(current.Name),
							CallID:   strings.TrimSpace(current.CallID),
							Output:   delta,
							Metadata: cloneGenericMap(progress.Metadata),
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
		progression := providerToolProgressionFromMetadata(metadata, result.Name)
		config.emit(StreamEvent{
			Type:         StreamEventToolCompleted,
			Step:         config.step,
			ToolName:     strings.TrimSpace(result.Name),
			CallID:       strings.TrimSpace(result.CallID),
			Output:       formatProviderManagedToolCompletedOutput(call, result),
			RawOutput:    liveStreamRawOutput(call, result),
			Error:        strings.TrimSpace(result.Error),
			DurationMS:   result.DurationMS,
			ToolIdentity: progression.Identity,
			ToolRunCount: progression.RunCount,
			ToolDisplay:  progression.Display,
			Metadata:     cloneGenericMap(metadata),
		})
	}

	if err := s.storeProviderManagedToolResult(config, call, metadata, result); err != nil {
		return tool.Result{}, permissionWaitMS, err
	}
	if err := s.appendPlanLifecycleMessageForToolResult(config.sessionID, call, result, config.applySessionMutation); err != nil {
		return tool.Result{}, permissionWaitMS, err
	}

	return result, permissionWaitMS, nil
}

func (s *Service) rejectProviderManagedCheckpointRunFollowup(config providerToolInvokerConfig, call tool.Call) error {
	if !config.providerManagedV3 || s == nil || s.sessions == nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(call.Name), "plan_manage") {
		return nil
	}
	args := decodeToolPayload(strings.TrimSpace(call.Arguments))
	if args == nil || !isPlanManageSessionCheckpointCreationAction(mapString(args, "action")) {
		return nil
	}
	active, ok, err := s.sessions.GetActivePlan(strings.TrimSpace(config.sessionID))
	if err != nil || !ok || active.Document == nil || active.Document.ExecutionState == nil {
		return err
	}
	state := active.Document.ExecutionState
	runID := strings.TrimSpace(config.runID)
	if runID == "" || strings.TrimSpace(state.CurrentRunID) != runID {
		return nil
	}
	checkpointID := strings.TrimSpace(active.Document.ActiveCheckpointID)
	if checkpointID == "" {
		checkpointID = strings.TrimSpace(state.LastCheckpointID)
	}
	if checkpointID == "" {
		return nil
	}
	return fmt.Errorf("recursive session checkpoint creation is not allowed from checkpoint run %q for active checkpoint %q; do not retry or claim a checkpoint was added: complete all work belonging to the current objective here; request_followup_checkpoint is reserved for related ordered work from the parent conversation, while an unrelated product goal must use request_new_plan with one checkpoint when bounded or multiple ordered checkpoints when intrinsically multi-stage, high-risk, fresh-context, or independently reviewable; if an unrelated request somehow reached this checkpoint-owned run, preserve it verbatim in terminal next-action evidence without asking the user to resend so the parent conversation can call request_new_plan; finish the current checkpoint with complete_checkpoint, mark_needs_review, mark_blocked, or mark_failed", runID, checkpointID)
}

func isPlanManageSessionCheckpointCreationAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "start-session-checkpoint", "start_session_checkpoint", "session-checkpoint", "session_checkpoint", "auto-checkpoint", "auto_checkpoint", "request-followup-checkpoint", "request_followup_checkpoint", "followup-checkpoint", "followup_checkpoint", "request-changes", "request_changes":
		return true
	default:
		return false
	}
}

func (s *Service) appendPlanLifecycleMessageForToolResult(sessionID string, call tool.Call, result tool.Result, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
	payload := decodeToolPayload(strings.TrimSpace(result.Output))
	if payload == nil {
		return nil
	}
	toolName := strings.TrimSpace(call.Name)
	if strings.EqualFold(toolName, "exit_plan_mode") {
		// Native V3 exit-plan acceptance commits its lifecycle break message in
		// the canonical acceptance transaction. Legacy invokers still append it.
		if applySessionMutation != nil {
			return nil
		}
		return s.appendExitPlanModeLifecycleMessage(sessionID, payload, applySessionMutation)
	}
	if !strings.EqualFold(toolName, "plan_manage") {
		return nil
	}
	action := strings.TrimSpace(mapString(payload, "action"))
	if !isPlanExecutionOutcomeMessageAction(action) && action != "resolve_blocked_checkpoint" {
		return nil
	}
	planPayload, ok := payload["plan"].(map[string]any)
	if !ok || planPayload == nil {
		return nil
	}
	planRaw, err := json.Marshal(planPayload)
	if err != nil {
		return fmt.Errorf("marshal plan lifecycle payload: %w", err)
	}
	var plan pebblestore.SessionPlanSnapshot
	if err := json.Unmarshal(planRaw, &plan); err != nil {
		return fmt.Errorf("decode plan lifecycle payload: %w", err)
	}
	if action == "resolve_blocked_checkpoint" && payload["resolved_checkpoint_id"] == nil {
		if resolvedID := resolvedPlanLifecycleCheckpointID(plan.Document, strings.TrimSpace(mapString(payload, "checkpoint_id"))); resolvedID != "" {
			payload["resolved_checkpoint_id"] = resolvedID
		}
	}
	return s.appendPlanExecutionLifecycleSystemMessage(sessionID, action, plan, payload, applySessionMutation)
}

func resolvedPlanLifecycleCheckpointID(doc *pebblestore.SessionPlanDocument, selectedCheckpointID string) string {
	if doc == nil {
		return ""
	}
	selectedCheckpointID = strings.TrimSpace(selectedCheckpointID)
	for _, checkpoint := range doc.Checkpoints {
		checkpointID := strings.TrimSpace(checkpoint.ID)
		if checkpointID == "" || checkpointID == selectedCheckpointID {
			continue
		}
		if strings.TrimSpace(checkpoint.Result) == "blocked_resolved" {
			return checkpointID
		}
		if checkpoint.Review != nil && strings.TrimSpace(checkpoint.Review.Result) == "blocked_resolved" {
			return checkpointID
		}
	}
	return ""
}

func (s *Service) appendExitPlanModeLifecycleMessage(sessionID string, payload map[string]any, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
	if !strings.EqualFold(strings.TrimSpace(mapString(payload, "status")), "approved") || !strings.EqualFold(strings.TrimSpace(mapString(payload, "next_action")), "run_checkpoint_with_fresh_context") {
		return nil
	}
	if s == nil || s.sessions == nil {
		return nil
	}
	planID := strings.TrimSpace(mapString(payload, "plan_id"))
	var plan pebblestore.SessionPlanSnapshot
	var ok bool
	var err error
	if planID != "" {
		plan, ok, err = s.sessions.GetPlan(sessionID, planID)
	} else {
		plan, ok, err = s.sessions.GetActivePlan(sessionID)
	}
	if err != nil || !ok || plan.Document == nil {
		return err
	}
	lifecyclePayload := cloneGenericMap(payload)
	lifecyclePayload["action"] = "approve_and_start"
	return s.appendPlanExecutionLifecycleSystemMessage(sessionID, "approve_and_start", plan, lifecyclePayload, applySessionMutation)
}

func recordProviderToolProgression(state *ToolProgressionState, metadata map[string]any, toolName string) ToolProgression {
	progression := state.Observe(toolName)
	metadata["tool_identity"] = progression.Identity
	metadata["tool_run_count"] = progression.RunCount
	metadata["tool_display"] = progression.Display
	return progression
}

func providerToolProgressionFromMetadata(metadata map[string]any, toolName string) ToolProgression {
	identity := canonicalToolName(firstNonEmptyString(mapString(metadata, "tool_identity"), toolName, "tool"))
	runCount := mapInt(metadata, "tool_run_count")
	if runCount <= 0 {
		runCount = 1
	}
	display := firstNonEmptyString(mapString(metadata, "tool_display"), toolProgressionDisplay(identity, runCount))
	return ToolProgression{Identity: identity, RunCount: runCount, Display: display}
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
	toolName := canonicalToolName(call.Name)
	if (toolName == "read" && len(strings.TrimSpace(result.Output)) > maxToolInputBytes) || toolName == "websearch" || toolName == "webfetch" {
		// Session search needs a representative tool result, not every token in a
		// large read or web response. Keep durable history/realtime content full,
		// but build postings from the bounded completion summary only.
		messageMetadata["search_index_content"] = formatProviderManagedToolCompletedOutput(call, result)
	}
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

const (
	providerManagedClientEffectRefreshAgents = "refresh_agents"
	providerManagedClientEffectRefreshThemes = "refresh_themes"
)

type providerManagedV3ClientEffect struct {
	Type string `json:"type"`
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
	if output := formatProviderManagedToolCompletedOutput(call, result); output != "" {
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
	if effects := providerManagedV3ClientEffects(eventType, call, result); len(effects) > 0 {
		payload["client_effects"] = effects
	}
	return json.Marshal(payload)
}

func providerManagedV3ClientEffects(eventType string, call tool.Call, result tool.Result) []providerManagedV3ClientEffect {
	if strings.TrimSpace(eventType) != "session.tool.completed" || strings.TrimSpace(result.Error) != "" {
		return nil
	}
	resultPayload := decodeToolPayload(strings.TrimSpace(result.Output))
	if resultPayload == nil || !mapBool(resultPayload, "applied") || !strings.EqualFold(strings.TrimSpace(mapString(resultPayload, "status")), "ok") {
		return nil
	}
	toolName := canonicalToolName(result.Name)
	if toolName != "manage_agent" && toolName != "manage_theme" {
		toolName = canonicalToolName(call.Name)
	}
	switch toolName {
	case "manage_agent":
		return []providerManagedV3ClientEffect{{Type: providerManagedClientEffectRefreshAgents}}
	case "manage_theme":
		return []providerManagedV3ClientEffect{{Type: providerManagedClientEffectRefreshThemes}}
	default:
		return nil
	}
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
