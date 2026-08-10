package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
	SourceMessageID      string
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
	ProviderID           string
	Model                string
	MediaContract        provideriface.SessionMediaContract
	PlanContextGuard     *PlanContextGuard
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
	sourceMessageID      string
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
	providerID           string
	model                string
	mediaContract        provideriface.SessionMediaContract
	planContextGuard     *PlanContextGuard
}

func (config ProviderManagedToolInvokerConfig) internal() providerToolInvokerConfig {
	return providerToolInvokerConfig{
		sessionID:            strings.TrimSpace(config.SessionID),
		permissionSessionID:  strings.TrimSpace(config.PermissionSessionID),
		runID:                strings.TrimSpace(config.RunID),
		sourceMessageID:      strings.TrimSpace(config.SourceMessageID),
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
		providerID:           strings.ToLower(strings.TrimSpace(config.ProviderID)),
		model:                strings.TrimSpace(config.Model),
		mediaContract:        config.MediaContract,
		planContextGuard:     config.PlanContextGuard,
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
		Media:            providerMediaPayloadFromToolResult(result),
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
		roots, err := mergeValidatedTemporaryWorkspaceRoots(workspaceCtx.OriginWorkspaceRoots, session.TemporaryWorkspaceRoots)
		if err != nil {
			return runWorkspaceContext{}, err
		}
		workspaceCtx.OriginWorkspaceRoots = roots
		roots, err = mergeValidatedTemporaryWorkspaceRoots(workspaceCtx.WorkspaceRoots, session.TemporaryWorkspaceRoots)
		if err != nil {
			return runWorkspaceContext{}, err
		}
		workspaceCtx.WorkspaceRoots = roots
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

func providerManagedCheckpointBoundaryCall(call tool.Call) bool {
	if canonicalToolName(call.Name) != "plan_manage" {
		return false
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(call.Arguments)), &args); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(mapString(args, "action")), sessionruntime.CheckpointBoundaryTransitionAction)
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
	if strings.EqualFold(strings.TrimSpace(call.Name), "plan_manage") {
		switch strings.ToLower(strings.TrimSpace(mapString(payload, "next_action"))) {
		case "run_checkpoint_with_current_context", "run_checkpoint_with_fresh_context":
			return true
		}
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
	if canonicalToolName(call.Name) == "compact" {
		if config.planContextGuard == nil || !config.planContextGuard.DecisionActive() || config.planContextGuard.FinalizationOnly() {
			return tool.Result{}, 0, errors.New("compact rejected: no armed plan context guard compaction decision is active")
		}
	}
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
	// media_inspect has no permission prompt: its provider-visible schema exists
	// only after the current model/media intersection admits it, and the handler
	// below revalidates that contract plus ownership, scope, type, and size.
	if canonicalToolName(call.Name) != mediaInspectToolName {
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
		boundaryProgressEmitted := false
		if providerManagedCheckpointBoundaryCall(call) {
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
			boundaryProgressEmitted = true
		}
		handled := false
		var controlResult tool.Result
		var controlErr error
		if canonicalToolName(call.Name) == mediaInspectToolName {
			handled = true
			controlResult, controlErr = s.executeProviderManagedMediaInspect(ctx, config, call, principal)
		} else if guardErr := s.rejectProviderManagedCheckpointRunFollowup(config, call); guardErr != nil {
			handled = true
			controlErr = guardErr
			controlResult = tool.Result{CallID: call.CallID, Name: call.Name}
		} else {
			lifecycleRun := planLifecycleRunContext{
				RunID:           strings.TrimSpace(config.runID),
				RunSessionID:    strings.TrimSpace(config.sessionID),
				ParentSessionID: strings.TrimSpace(config.sessionID),
				SourceMessageID: strings.TrimSpace(config.sourceMessageID),
				Inline:          config.providerManagedV3 && sessionruntime.NormalizeMode(config.sessionMode) == sessionruntime.ModeAuto,
			}
			controlResponse := providerManagedControlPlaneResponse(call, feedback)
			handled, controlResult, controlErr = s.executeControlPlaneToolWithLifecycleRunContext(ctx, config.sessionID, config.sessionMode, config.agentProfile, config.step, call, controlResponse, config.emit, config.applySessionMutation, lifecycleRun)
		}
		if handled {
			progression := providerToolProgressionFromMetadata(metadata, name)
			if !boundaryProgressEmitted {
				progression = recordProviderToolProgression(config.toolProgression, metadata, name)
			}
			if config.emit != nil && !boundaryProgressEmitted {
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

func providerMediaPayloadFromToolResult(result tool.Result) *provideriface.SessionMediaPayload {
	if result.Media == nil {
		return nil
	}
	return &provideriface.SessionMediaPayload{
		AssetID: result.Media.AssetID, Modality: result.Media.Modality, MIMEType: result.Media.MIMEType,
		FileType: result.Media.FileType, DigestSHA256: result.Media.DigestSHA256, Size: result.Media.Size,
		Bytes: append([]byte(nil), result.Media.Bytes...),
	}
}

func (s *Service) executeProviderManagedMediaInspect(ctx context.Context, config providerToolInvokerConfig, call tool.Call, principal identity.Principal) (tool.Result, error) {
	result := tool.Result{CallID: call.CallID, Name: mediaInspectToolName}
	if s == nil || s.sessions == nil || s.providers == nil || s.model == nil {
		return result, errors.New("media inspection runtime is not configured")
	}
	args, err := decodeMediaInspectArguments(call.Arguments)
	if err != nil {
		return result, err
	}
	if !principal.Valid() {
		principal = config.principal
	}
	session, ok, err := s.sessions.GetSession(strings.TrimSpace(config.sessionID))
	if err != nil {
		return result, err
	}
	if !ok {
		return result, fmt.Errorf("session %q not found", strings.TrimSpace(config.sessionID))
	}
	if !principal.Valid() || session.AccountScopeID != principal.AccountScopeID || session.UserID != principal.UserID {
		return result, errors.New("media asset ownership does not match the authenticated session principal")
	}
	providerID := strings.ToLower(strings.TrimSpace(config.providerID))
	modelID := strings.TrimSpace(config.model)
	if providerID == "" || modelID == "" {
		return result, errors.New("media inspection run provider/model is unresolved")
	}
	runner, ok := s.providers.GetRunner(providerID)
	if !ok || runner == nil {
		return result, fmt.Errorf("provider %q is not runnable", providerID)
	}
	catalog, meta, err := modelCatalogLookupWithMeta(s.model, providerID, modelID)
	if err != nil {
		return result, err
	}
	currentContract := CompileSessionMediaContract(SessionMediaContractInput{
		ProviderID: providerID, Model: modelID, Catalog: catalog, CatalogMeta: meta,
		Adapter:         ResolveMediaAdapterDeclaration(ctx, providerID, runner),
		AgentAuthorized: AgentProfileAuthorizesMedia(config.agentProfile), ExecutionMode: config.sessionMode,
		WorkspaceScope: config.workspacePath, SessionScope: config.sessionID,
	})
	if currentContract.Hash != config.mediaContract.Hash {
		return result, errors.New("media_inspect call is stale or forged for the current run contract")
	}
	var asset pebblestore.SessionMediaAsset
	var payload []byte
	if args.AssetID != "" {
		asset, payload, err = s.sessions.ReadSessionMediaAsset(principal.AccountScopeID, config.sessionID, args.AssetID)
		if err != nil {
			return result, err
		}
		if asset.ContractHash != currentContract.Hash || asset.ProviderID != providerID || asset.Model != modelID {
			return result, errors.New("media asset admission contract does not match the current run")
		}
	} else {
		workspaceCtx, err := s.providerManagedWorkspaceContext(config, principal)
		if err != nil {
			return result, err
		}
		path, err := resolveProviderMediaWorkspacePath(workspaceCtx, args.Path)
		if err != nil {
			return result, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return result, fmt.Errorf("stat media path: %w", err)
		}
		if info.Size() <= 0 || info.Size() > pebblestore.SessionMediaDefaultMaxBytes {
			return result, fmt.Errorf("media path must be between 1 and %d bytes", pebblestore.SessionMediaDefaultMaxBytes)
		}
		payload, err = os.ReadFile(path)
		if err != nil {
			return result, fmt.Errorf("read media path: %w", err)
		}
		if len(payload) == 0 {
			return result, errors.New("media path is empty")
		}
		mimeType := strings.ToLower(strings.TrimSpace(http.DetectContentType(payload)))
		fileType := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
		capability, err := validateMediaInspectInvocation(currentContract, "image", mimeType, fileType)
		if err != nil {
			return result, err
		}
		maxBytes := capability.MaxBytes
		if maxBytes <= 0 || maxBytes > pebblestore.SessionMediaDefaultMaxBytes {
			maxBytes = pebblestore.SessionMediaDefaultMaxBytes
		}
		if int64(len(payload)) > maxBytes {
			return result, fmt.Errorf("media path exceeds %d byte limit", maxBytes)
		}
		digest := sha256.Sum256(payload)
		asset = pebblestore.SessionMediaAsset{
			ID: "workspace_" + hex.EncodeToString(digest[:]), Modality: "image", DetectedMIMEType: mimeType,
			FileType: fileType, Size: int64(len(payload)), DigestSHA256: hex.EncodeToString(digest[:]),
			ContractHash: currentContract.Hash, ProviderID: providerID, Model: modelID,
		}
	}
	capability, err := validateMediaInspectInvocation(currentContract, asset.Modality, asset.DetectedMIMEType, asset.FileType)
	if err != nil {
		return result, err
	}
	if int64(len(payload)) != asset.Size || capability.MaxBytes > 0 && asset.Size > capability.MaxBytes {
		return result, errors.New("media asset exceeds the current run limit or failed its size check")
	}
	output, err := mediaInspectResult(asset, capability, currentContract)
	if err != nil {
		return result, err
	}
	result.Output = output
	result.Media = &tool.MediaPayload{
		AssetID: asset.ID, Modality: asset.Modality, MIMEType: asset.DetectedMIMEType, FileType: asset.FileType,
		DigestSHA256: asset.DigestSHA256, Size: asset.Size, Bytes: append([]byte(nil), payload...),
	}
	return result, nil
}

func resolveProviderMediaWorkspacePath(workspaceCtx runWorkspaceContext, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", errors.New("media_inspect path is required")
	}
	base := strings.TrimSpace(workspaceCtx.WorkspacePath)
	if base == "" {
		return "", errors.New("media_inspect workspace path is unavailable")
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(base, candidate)
	}
	candidate, err := filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return "", fmt.Errorf("resolve media path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve media path symlinks: %w", err)
	}
	roots := normalizeExecutionRoots(workspaceCtx.WorkspacePath, workspaceCtx.WorkspaceRoots)
	if !runPathWithinRoots(roots, resolved) {
		return "", fmt.Errorf("media path %q escapes workspace scope", requested)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat media path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("media_inspect path must be a regular file")
	}
	return resolved, nil
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
	// A provider/tool retry of the exact parent boundary call must reach the
	// boundary service's durable source-message replay path. It is not a request
	// to append another checkpoint from inside the checkpoint-owned run.
	sourceMessageID := strings.TrimSpace(config.sourceMessageID)
	if sourceMessageID != "" {
		for _, checkpoint := range active.Document.Checkpoints {
			if strings.TrimSpace(checkpoint.ID) == checkpointID && strings.TrimSpace(checkpoint.SourceMessageID) == sourceMessageID {
				return nil
			}
		}
	}
	return fmt.Errorf("checkpoint boundary transition is not allowed from checkpoint run %q for active checkpoint %q; do not retry or claim a checkpoint was added: complete all work belonging to the current objective here; transition_checkpoint_boundary is reserved for a trusted parent provider turn and assigns the new checkpoint to that already-current run without restarting it; request_followup_checkpoint and its aliases are retired; if an unrelated request reached this checkpoint-owned run, preserve it verbatim in terminal next-action evidence so the parent conversation can choose transition_checkpoint_boundary or request_new_plan; finish the current checkpoint with complete_checkpoint, mark_needs_review, mark_blocked, or mark_failed", runID, checkpointID)
}

func isPlanManageSessionCheckpointCreationAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "start-session-checkpoint", "start_session_checkpoint", "session-checkpoint", "session_checkpoint", "auto-checkpoint", "auto_checkpoint", "transition-checkpoint-boundary", "transition_checkpoint_boundary", "checkpoint-boundary-transition", "checkpoint_boundary_transition", "request-followup-checkpoint", "request_followup_checkpoint", "followup-checkpoint", "followup_checkpoint", "request-changes", "request_changes":
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
	return s.appendPlanExecutionLifecycleSystemMessage(sessionID, action, plan, payload, applySessionMutation)
}

func (s *Service) appendExitPlanModeLifecycleMessage(sessionID string, payload map[string]any, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
	if !strings.EqualFold(strings.TrimSpace(mapString(payload, "status")), "approved") {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(mapString(payload, "next_action"))) {
	case "run_checkpoint_with_current_context", "run_checkpoint_with_fresh_context":
	default:
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
