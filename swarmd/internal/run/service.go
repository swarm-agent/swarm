package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/discovery"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/permission"
	"swarm/packages/swarmd/internal/privacy"
	codexruntime "swarm/packages/swarmd/internal/provider/codex"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sandboxruntime "swarm/packages/swarmd/internal/sandbox"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

const (
	defaultHistoryLimit = 500
	maxToolPreviewChars = 280
	maxToolDeltaChars   = 4000
	maxToolInputBytes   = 96 * 1024
	maxToolInputPreview = 1200
	maxRulePromptFiles  = 3
	maxRulePromptBytes  = 4000
	runFailurePathID    = "run.turn.error.v3"
	emptyStepRetryBase  = 250 * time.Millisecond
	emptyStepRetryMax   = 2 * time.Second
	emptyStepRetryLimit = 2

	contextCompactionRetryLimit           = 2
	memoryCompactionHeartbeatInterval     = 2 * time.Second
	memoryCompactionFallbackChunkRunes    = 12000
	memoryCompactionMinimumChunkRunes     = 4000
	memoryCompactionChunkOverlapMinRunes  = 1200
	memoryCompactionChunkOverlapMaxRunes  = 6000
	memoryCompactionTokenEstimateDivisor  = 4
	memoryCompactionChunkRetryLimit       = 2
	memoryCompactionSummaryMaxRunes       = 9000
	memoryCompactionHistorySlack          = 8
	memoryCompactionOutputReserveTokens   = 4096
	memoryCompactionSafetyMarginMinTokens = 2048
	contextCompactionMarkerPrefix         = "[context-compact]"
	contextCompactionUsageSource          = "context_compaction_reset"
	contextCompactionPlanLabelMetadataKey = "context_compaction_attached_plan_label"
	contextCompactionPlanTextMetadataKey  = "context_compaction_attached_plan_text"

	taskReportMinChars               = 400
	taskReportDefaultChars           = 1800
	taskReportMaxChars               = 12000
	taskReportPreviewChars           = 320
	taskDelegationContextMaxChars    = 4000
	taskDelegationTranscriptMaxChars = 12000
	taskDelegationTranscriptMsgChars = 1600
	taskDelegationTranscriptMsgLimit = 64
	targetedSubagentSummaryRunes     = 96

	sessionTitleDefault              = "New Session"
	sessionTitleFinalDelay           = 2 * time.Minute
	sessionTitleGenerationTimeout    = 20 * time.Second
	sessionTitlePromptPreviewRunes   = 2000
	sessionTitleConversationLimit    = 24
	sessionTitleWarningPathID        = "run.session.title.warning.v1"
	sessionTitleProvisionalWords     = 2
	sessionTitleFinalWordsMin        = 5
	sessionTitleFinalWordsMax        = 6
	sessionTitleWordExtractionRegexp = `\b[\p{L}\p{N}][\p{L}\p{N}'-]*\b`
)

var sessionTitleWordPattern = regexp.MustCompile(sessionTitleWordExtractionRegexp)
var sessionCompactTitleSuffixPattern = regexp.MustCompile(`(?i)\s*\(compact\s*#\s*([0-9]+)\)\s*$`)

type Service struct {
	sessions     *sessionruntime.Service
	model        *model.Service
	providers    *registry.Registry
	tools        *tool.Runtime
	permissions  *permission.Service
	agents       *agentruntime.Service
	discovery    *discovery.Service
	workspace    *workspaceruntime.Service
	sandbox      sandboxService
	worktrees    worktreeService
	events       *pebblestore.EventLog
	eventPublish func(pebblestore.EventEnvelope)
	runCounter   atomic.Uint64
	lifecycleMu  sync.Mutex
	activeRuns   map[string]*activeSessionRun
}

type sandboxService interface {
	IsEnabled() (bool, error)
	GetStatus() (sandboxruntime.Status, error)
}

type worktreeService interface {
	AttachBranch(workspacePath, sessionID, title string) (string, error)
	AllocateTaskWorkspace(workspacePath, baseBranch, nameSeed string) (worktreeruntime.Allocation, error)
}

type RunOptions struct {
	Prompt              string
	AgentName           string
	Instructions        string
	Compact             bool
	AllowSubagent       bool
	DisabledTools       map[string]bool
	PermissionSessionID string
	RunID               string
	TargetKind          string
	TargetName          string
	Background          bool
	OwnerTransport      string
	ToolScope           *RunToolScope
	CompiledPolicy      *permission.Policy
	ExecutionContext    *RunExecutionContext
}

type RunResult struct {
	SessionID        string                                `json:"session_id"`
	Agent            string                                `json:"agent"`
	Model            string                                `json:"model"`
	Thinking         string                                `json:"thinking"`
	ReasoningSummary string                                `json:"reasoning_summary,omitempty"`
	Commentary       []pebblestore.MessageSnapshot         `json:"commentary,omitempty"`
	Steps            int                                   `json:"steps"`
	ToolCallCount    int                                   `json:"tool_call_count"`
	TurnUsage        *pebblestore.SessionTurnUsageSnapshot `json:"turn_usage,omitempty"`
	UsageSummary     *pebblestore.SessionUsageSummary      `json:"usage_summary,omitempty"`
	UserMessage      pebblestore.MessageSnapshot           `json:"user_message"`
	ToolMessages     []pebblestore.MessageSnapshot         `json:"tool_messages"`
	AssistantMessage pebblestore.MessageSnapshot           `json:"assistant_message"`
	Events           []pebblestore.EventEnvelope           `json:"-"`
	Background       bool                                  `json:"background,omitempty"`
	TargetKind       string                                `json:"target_kind,omitempty"`
	TargetName       string                                `json:"target_name,omitempty"`
}

func (s *Service) resolveExecutionMode(requestMode string, agentProfile pebblestore.AgentProfile) (string, string, error) {
	requestMode = sessionruntime.NormalizeMode(requestMode)
	if pebblestore.AgentExitPlanModeEnabled(agentProfile) {
		return requestMode, "", nil
	}
	setting, ok := pebblestore.AgentExecutionSetting(agentProfile)
	if !ok {
		agentName := strings.TrimSpace(agentProfile.Name)
		if agentName == "" {
			agentName = "agent"
		}
		return "", "", fmt.Errorf("agent %q has plan mode disabled but no execution_setting is configured", agentName)
	}
	warning := ""
	if requestMode != setting {
		agentName := strings.TrimSpace(agentProfile.Name)
		if agentName == "" {
			agentName = "agent"
		}
		warning = fmt.Sprintf("agent %q has plan mode disabled; ignoring session mode %q and using execution setting %q", agentName, requestMode, setting)
	}
	return setting, warning, nil
}

func (s *Service) resolveMainSessionPreference(sessionID string) (model.ResolvedPreference, error) {
	if s.sessions == nil {
		return model.ResolvedPreference{}, errors.New("session service is not configured")
	}
	if s.model == nil {
		return model.ResolvedPreference{}, errors.New("model service is not configured")
	}
	preference, err := s.sessions.GetSessionPreference(sessionID)
	if err != nil {
		return model.ResolvedPreference{}, err
	}
	if strings.TrimSpace(preference.Provider) == "" || strings.TrimSpace(preference.Model) == "" || strings.TrimSpace(preference.Thinking) == "" {
		return model.ResolvedPreference{}, fmt.Errorf("session %q execution preference is not configured", strings.TrimSpace(sessionID))
	}
	return s.model.ResolvePreference(preference)
}

const (
	StreamEventTurnStarted         = "turn.started"
	StreamEventTurnCompleted       = "turn.completed"
	StreamEventTurnError           = "turn.error"
	StreamEventSessionStatus       = "session.status"
	StreamEventSessionLifecycle    = "session.lifecycle.updated"
	StreamEventStepStarted         = "step.started"
	StreamEventAssistantDelta      = "assistant.delta"
	StreamEventAssistantCommentary = "assistant.commentary"
	StreamEventReasoningStarted    = "reasoning.started"
	StreamEventReasoningDelta      = "reasoning.delta"
	StreamEventReasoningCompleted  = "reasoning.completed"
	StreamEventReasoningSummary    = "reasoning.summary"
	StreamEventUsageUpdated        = "usage.updated"
	StreamEventToolStarted         = "tool.started"
	StreamEventToolDelta           = "tool.delta"
	StreamEventToolCompleted       = "tool.completed"
	StreamEventMessageStored       = "message.stored"
	StreamEventMessageUpdated      = "message.updated"
	StreamEventPermissionReq       = "permission.requested"
	StreamEventPermissionUpdate    = "permission.updated"
	StreamEventSessionTitle        = "session.title.updated"
	StreamEventSessionBranch       = "session.branch.updated"
	StreamEventSessionWarning      = "session.title.warning"
)

type StreamEvent struct {
	Type         string                                `json:"type"`
	SessionID    string                                `json:"session_id,omitempty"`
	RunID        string                                `json:"run_id,omitempty"`
	Agent        string                                `json:"agent,omitempty"`
	Status       string                                `json:"status,omitempty"`
	Step         int                                   `json:"step,omitempty"`
	ReasoningKey string                                `json:"reasoning_key,omitempty"`
	Delta        string                                `json:"delta,omitempty"`
	Summary      string                                `json:"summary,omitempty"`
	ToolName     string                                `json:"tool_name,omitempty"`
	CallID       string                                `json:"call_id,omitempty"`
	Arguments    string                                `json:"arguments,omitempty"`
	Output       string                                `json:"output,omitempty"`
	RawOutput    string                                `json:"raw_output,omitempty"`
	Error        string                                `json:"error,omitempty"`
	DurationMS   int64                                 `json:"duration_ms,omitempty"`
	Message      *pebblestore.MessageSnapshot          `json:"message,omitempty"`
	Permission   *pebblestore.PermissionRecord         `json:"permission,omitempty"`
	TurnUsage    *pebblestore.SessionTurnUsageSnapshot `json:"turn_usage,omitempty"`
	UsageSummary *pebblestore.SessionUsageSummary      `json:"usage_summary,omitempty"`
	Metadata     map[string]any                        `json:"metadata,omitempty"`
	Title        string                                `json:"title,omitempty"`
	TitleStage   string                                `json:"title_stage,omitempty"`
	Warning      string                                `json:"warning,omitempty"`
	Branch       string                                `json:"branch,omitempty"`
	Lifecycle    *pebblestore.SessionLifecycleSnapshot `json:"lifecycle,omitempty"`
}

func sessionStatusForEvent(event StreamEvent) string {
	switch strings.TrimSpace(event.Type) {
	case StreamEventPermissionReq:
		return "blocked"
	case StreamEventPermissionUpdate:
		if event.Permission != nil && strings.TrimSpace(event.Permission.Status) == "pending" {
			return "blocked"
		}
		return "running"
	default:
		return ""
	}
}

func (s *Service) persistReasoningMessageSnapshot(sessionID string, message *pebblestore.MessageSnapshot, content string) (*pebblestore.MessageSnapshot, *pebblestore.EventEnvelope, StreamEvent, error) {
	if s == nil || s.sessions == nil {
		return message, nil, StreamEvent{}, errors.New("session service is not configured")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return message, nil, StreamEvent{}, nil
	}
	if message == nil || strings.TrimSpace(message.ID) == "" || message.GlobalSeq == 0 {
		stored, _, env, err := s.sessions.AppendMessage(sessionID, "reasoning", content, nil)
		if err != nil {
			return nil, nil, StreamEvent{}, err
		}
		return &stored, env, StreamEvent{Type: StreamEventMessageStored, Message: &stored}, nil
	}
	updated, _, env, err := s.sessions.UpdateMessage(sessionID, message.GlobalSeq, content)
	if err != nil {
		return nil, nil, StreamEvent{}, err
	}
	if env == nil {
		return &updated, nil, StreamEvent{}, nil
	}
	return &updated, env, StreamEvent{Type: StreamEventMessageUpdated, Message: &updated}, nil
}

func (s *Service) emitSessionStatus(emit StreamHandler, sessionID, runID, status, summary, errText, agent string) {
	if emit == nil {
		return
	}
	status = strings.TrimSpace(status)
	if status == "" {
		return
	}
	emit(StreamEvent{
		Type:      StreamEventSessionStatus,
		SessionID: strings.TrimSpace(sessionID),
		RunID:     strings.TrimSpace(runID),
		Status:    status,
		Summary:   strings.TrimSpace(summary),
		Error:     strings.TrimSpace(errText),
		Agent:     strings.TrimSpace(agent),
	})
}

type PermissionFeedback struct {
	CallID            string
	ToolName          string
	Message           string
	ApprovedArguments string
}

type StreamHandler func(event StreamEvent)

type runSandboxContext struct {
	Enabled              bool
	WorkspacePath        string
	WorkspaceRoots       []string
	OriginWorkspacePath  string
	OriginWorkspaceRoots []string
}

func NewService(sessions *sessionruntime.Service, modelSvc *model.Service, providers *registry.Registry, tools *tool.Runtime, permissions *permission.Service, agents *agentruntime.Service, discoverySvc *discovery.Service, events *pebblestore.EventLog) *Service {
	return &Service{
		sessions:    sessions,
		model:       modelSvc,
		providers:   providers,
		tools:       tools,
		permissions: permissions,
		agents:      agents,
		discovery:   discoverySvc,
		events:      events,
		activeRuns:  make(map[string]*activeSessionRun),
	}
}

func (s *Service) ExecuteToolForSessionScope(ctx context.Context, workspacePath string, call tool.Call) (string, error) {
	if s == nil || s.tools == nil {
		return "", errors.New("tool runtime is not configured")
	}
	scope := tool.WorkspaceScope{PrimaryPath: strings.TrimSpace(workspacePath), Roots: []string{strings.TrimSpace(workspacePath)}}
	if s.workspace != nil {
		if resolved, err := s.workspace.ScopeForPath(workspacePath); err == nil {
			scope = tool.WorkspaceScope{PrimaryPath: strings.TrimSpace(resolved.ResolvedPath), Roots: []string{strings.TrimSpace(resolved.WorkspacePath), strings.TrimSpace(resolved.ResolvedPath)}}
		}
	}
	return s.tools.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
}

func (s *Service) SetWorkspaceService(workspaceSvc *workspaceruntime.Service) {
	if s == nil {
		return
	}
	s.workspace = workspaceSvc
}

func (s *Service) SetSandboxService(sandboxSvc sandboxService) {
	if s == nil {
		return
	}
	s.sandbox = sandboxSvc
}

func (s *Service) SetWorktreeService(worktreeSvc worktreeService) {
	if s == nil {
		return
	}
	s.worktrees = worktreeSvc
}

func (s *Service) SetEventPublisher(publish func(pebblestore.EventEnvelope)) {
	if s == nil {
		return
	}
	s.eventPublish = publish
}

func (s *Service) maybeRefreshSessionGitState(sessionID string, sessionSnapshot pebblestore.SessionSnapshot) {
	if s == nil || s.sessions == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	workspacePath := strings.TrimSpace(sessionSnapshot.WorkspacePath)
	if workspacePath == "" {
		return
	}
	metadata := sessionGitMetadata(sessionSnapshot.Metadata)
	gitMeta, _ := metadata["git"].(map[string]any)
	if gitMeta == nil {
		return
	}
	changed := false
	if sessionSnapshot.WorktreeEnabled {
		statusMeta, hasStatus := buildGitStatusMetadata(workspacePath, sessionSnapshot.WorktreeBaseBranch)
		currentStatus, _ := gitMeta["status"].(map[string]any)
		currentStatus = cloneGenericMap(currentStatus)
		if hasStatus {
			encodedCurrent, _ := json.Marshal(currentStatus)
			encodedNext, _ := json.Marshal(statusMeta)
			if string(encodedCurrent) != string(encodedNext) {
				gitMeta["status"] = statusMeta
				changed = true
			}
		} else if _, exists := gitMeta["status"]; exists {
			delete(gitMeta, "status")
			changed = true
		}
		nextCommitDetected := hasStatus && mapInt(statusMeta, "ahead_count") > 0
		nextCommitCount := 0
		if hasStatus {
			nextCommitCount = mapInt(statusMeta, "ahead_count")
		}
		if mapBool(gitMeta, "commit_detected") != nextCommitDetected {
			gitMeta["commit_detected"] = nextCommitDetected
			changed = true
		}
		if mapInt(gitMeta, "commit_count") != nextCommitCount {
			gitMeta["commit_count"] = nextCommitCount
			changed = true
		}
		if branch := strings.TrimSpace(mapString(statusMeta, "branch")); strings.TrimSpace(sessionSnapshot.WorktreeBranch) == "" && branch != "" {
			if updatedSession, env, err := s.sessions.SetWorktreeBranch(sessionID, branch); err == nil {
				sessionSnapshot = updatedSession
				if env != nil {
					s.publishEventEnvelope(*env)
				}
			}
		}
	} else if _, exists := gitMeta["status"]; exists {
		delete(gitMeta, "status")
		changed = true
	}
	if !changed {
		return
	}
	updated, env, err := s.sessions.UpdateMetadata(sessionID, metadata)
	if err != nil {
		return
	}
	_ = updated
	if env != nil {
		s.publishEventEnvelope(*env)
	}
}

func (s *Service) RunTurn(ctx context.Context, sessionID string, request RunRequest, meta RunStartMeta) (RunResult, error) {
	return s.runTurn(ctx, sessionID, NewRunOptions(request, meta), nil)
}

func (s *Service) RunTurnWithOptions(ctx context.Context, sessionID string, options RunOptions) (RunResult, error) {
	return s.runTurn(ctx, sessionID, options, nil)
}

func (s *Service) RunTurnStreaming(ctx context.Context, sessionID string, request RunRequest, meta RunStartMeta, onEvent StreamHandler) (RunResult, error) {
	return s.runTurn(ctx, sessionID, NewRunOptions(request, meta), onEvent)
}

func (s *Service) RunTurnStreamingWithOptions(ctx context.Context, sessionID string, options RunOptions, onEvent StreamHandler) (RunResult, error) {
	return s.runTurn(ctx, sessionID, options, onEvent)
}

func (s *Service) runTargetedSubagent(ctx context.Context, parentSession pebblestore.SessionSnapshot, options RunOptions, targetName string, emit StreamHandler) (RunResult, error) {
	targetName = strings.TrimSpace(targetName)
	if targetName == "" {
		return RunResult{}, fmt.Errorf("target_name is required for target_kind=%s", RunTargetKindSubagent)
	}
	prompt := strings.TrimSpace(options.Prompt)
	if prompt == "" {
		return RunResult{}, errors.New("prompt is required")
	}

	description := fmt.Sprintf("@%s %s", targetName, truncateRunes(prompt, targetedSubagentSummaryRunes))
	launch, err := s.prepareDelegatedSubagentLaunch(parentSession, sessionruntime.NormalizeMode(parentSession.Mode), taskLaunchPrepared{
		LaunchIndex:       1,
		RequestedSubagent: targetName,
	}, description, targetName)
	if err != nil {
		return RunResult{}, err
	}
	taskToolName := "task"
	taskCallID := "task_targeted_" + strings.TrimSpace(launch.ChildSession.ID)
	if strings.TrimSpace(taskCallID) == "task_targeted_" {
		taskCallID = fmt.Sprintf("task_targeted_%d", time.Now().UnixMilli())
	}
	taskAction := "spawn"
	taskStep := maxInt(1, launch.LaunchIndex)
	outcome := buildTaskLaunchOutcome(launch)
	emit(StreamEvent{
		Type:     StreamEventToolStarted,
		Step:     taskStep,
		ToolName: taskToolName,
		CallID:   taskCallID,
	})
	emitTaskStreamDelta(
		parentSession.ID,
		emit,
		taskStep,
		taskToolName,
		taskCallID,
		taskAction,
		description,
		1,
		outcome,
		"spawned",
		fmt.Sprintf("spawned launch %d %s subagent in %s", outcome.LaunchIndex, outcome.ResolvedSubagent, outcome.ChildMode),
	)

	parentMessages, err := s.loadDelegationTranscriptMessages(parentSession.ID)
	if err != nil {
		return RunResult{}, err
	}
	delegatedPrompt := buildTaskDelegationPrompt(taskDelegationPromptConfig{
		Description:          description,
		Prompt:               prompt,
		ReportMaxChars:       taskReportDefaultChars,
		ParentSession:        parentSession,
		ParentMessages:       parentMessages,
		PermissionSessionID:  firstNonEmptyString(strings.TrimSpace(options.PermissionSessionID), strings.TrimSpace(parentSession.ID)),
		TargetedSubagentName: targetName,
	})
	childResult, err := s.RunTurnStreaming(ctx, launch.ChildSession.ID, RunRequest{
		Prompt:     delegatedPrompt,
		TargetKind: RunTargetKindSubagent,
		TargetName: launch.SubagentProfile.Name,
		AgentName:  launch.SubagentProfile.Name,
	}, RunStartMeta{
		AllowSubagent: true,
		// Targeted subagent runs should honor the saved subagent profile's
		// resolved tool contract instead of inheriting the generic task baseline.
		PermissionSessionID: parentSession.ID,
	}, func(event StreamEvent) {
		switch strings.TrimSpace(event.Type) {
		case StreamEventStepStarted:
			taskStep = maxInt(taskStep, maxInt(1, event.Step))
			if outcome.ElapsedMS <= 0 && outcome.LaunchStartedAtMS > 0 {
				outcome.ElapsedMS = maxInt64(0, time.Now().UnixMilli()-outcome.LaunchStartedAtMS)
			}
			emitTaskStreamDelta(parentSession.ID, emit, taskStep, taskToolName, taskCallID, taskAction, description, 1, outcome, "running", "")
		case StreamEventToolStarted:
			nowMS := time.Now().UnixMilli()
			taskStep = maxInt(taskStep, maxInt(1, event.Step))
			toolName := emptyToolName(strings.TrimSpace(event.ToolName))
			outcome.ToolStarted++
			outcome.CurrentTool = toolName
			outcome.CurrentToolStarted = nowMS
			outcome.CurrentToolMS = 0
			if toolName != "" {
				outcome.ToolOrder = append(outcome.ToolOrder, toolName)
			}
			if outcome.LaunchStartedAtMS <= 0 {
				outcome.LaunchStartedAtMS = nowMS
			}
			outcome.ElapsedMS = maxInt64(0, nowMS-outcome.LaunchStartedAtMS)
			emitTaskStreamDelta(
				parentSession.ID,
				emit,
				taskStep,
				taskToolName,
				taskCallID,
				taskAction,
				description,
				1,
				outcome,
				"tool.started",
				fmt.Sprintf("launch %d running %s", outcome.LaunchIndex, outcome.CurrentTool),
			)
		case StreamEventToolCompleted:
			nowMS := time.Now().UnixMilli()
			taskStep = maxInt(taskStep, maxInt(1, event.Step))
			outcome.ToolCompleted++
			completedTool := emptyToolName(strings.TrimSpace(event.ToolName))
			if completedTool == "tool" && strings.TrimSpace(outcome.CurrentTool) != "" {
				completedTool = outcome.CurrentTool
			}
			if strings.TrimSpace(outcome.CurrentTool) != "" && outcome.CurrentToolStarted > 0 {
				outcome.CurrentToolMS = maxInt64(0, nowMS-outcome.CurrentToolStarted)
			}
			if outcome.LaunchStartedAtMS <= 0 {
				outcome.LaunchStartedAtMS = nowMS
			}
			outcome.ElapsedMS = maxInt64(0, nowMS-outcome.LaunchStartedAtMS)
			summary := fmt.Sprintf("launch %d completed %s", outcome.LaunchIndex, completedTool)
			if strings.TrimSpace(event.Error) != "" {
				outcome.ToolFailed++
				summary = fmt.Sprintf("launch %d failed %s: %s", outcome.LaunchIndex, completedTool, strings.TrimSpace(event.Error))
			}
			emitTaskStreamDelta(parentSession.ID, emit, taskStep, taskToolName, taskCallID, taskAction, description, 1, outcome, "tool.completed", summary)
			if strings.TrimSpace(event.Error) == "" {
				outcome.CurrentTool = ""
				outcome.CurrentToolStarted = 0
				outcome.CurrentToolMS = 0
			}
		case StreamEventMessageStored, StreamEventMessageUpdated:
			if event.Message != nil && strings.EqualFold(strings.TrimSpace(event.Message.Role), "reasoning") {
				outcome.ReasoningSummary = strings.TrimSpace(event.Message.Content)
			}
		}
		forwardTargetedSubagentStreamEvent(emit, event)
	})
	if err != nil {
		nowMS := time.Now().UnixMilli()
		if outcome.LaunchStartedAtMS <= 0 {
			outcome.LaunchStartedAtMS = nowMS
		}
		outcome.ElapsedMS = maxInt64(0, nowMS-outcome.LaunchStartedAtMS)
		if strings.TrimSpace(outcome.CurrentTool) != "" && outcome.CurrentToolStarted > 0 {
			outcome.CurrentToolMS = maxInt64(0, nowMS-outcome.CurrentToolStarted)
		}
		outcome.Error = strings.TrimSpace(err.Error())
		outcome.Summary = fmt.Sprintf("launch %d subagent %s failed", outcome.LaunchIndex, outcome.ResolvedSubagent)
		if outcome.Error != "" {
			outcome.Summary += ": " + outcome.Error
		}
		emitTaskStreamDelta(parentSession.ID, emit, taskStep, taskToolName, taskCallID, taskAction, description, 1, outcome, "failed", outcome.Summary)
		if finalPayload, marshalErr := json.Marshal(buildTaskStreamPayload(parentSession.ID, taskAction, description, 1, outcome, "failed", outcome.Summary)); marshalErr == nil {
			emit(StreamEvent{
				Type:       StreamEventToolCompleted,
				Step:       taskStep,
				ToolName:   taskToolName,
				CallID:     taskCallID,
				Output:     string(finalPayload),
				RawOutput:  string(finalPayload),
				Error:      outcome.Error,
				DurationMS: outcome.ElapsedMS,
			})
		}
		return RunResult{}, err
	}

	assistantText := strings.TrimSpace(childResult.AssistantMessage.Content)
	if assistantText == "" {
		assistantText = "Subagent completed without a textual report."
	}
	nowMS := time.Now().UnixMilli()
	if outcome.LaunchStartedAtMS <= 0 {
		outcome.LaunchStartedAtMS = nowMS
	}
	outcome.ElapsedMS = maxInt64(0, nowMS-outcome.LaunchStartedAtMS)
	outcome.CurrentTool = ""
	outcome.CurrentToolStarted = 0
	outcome.CurrentToolMS = 0
	outcome.ReportChars = len([]rune(assistantText))
	outcome.ReportExcerpt = assistantText
	outcome.Summary = summarizePlainToolOutput(assistantText, taskReportPreviewChars, 2)
	if outcome.Summary == "" {
		outcome.Summary = fmt.Sprintf("launch %d subagent %s completed", outcome.LaunchIndex, outcome.ResolvedSubagent)
	}
	emitTaskStreamDelta(parentSession.ID, emit, taskStep, taskToolName, taskCallID, taskAction, description, 1, outcome, "completed", outcome.Summary)
	if finalPayload, marshalErr := json.Marshal(buildTaskStreamPayload(parentSession.ID, taskAction, description, 1, outcome, "completed", outcome.Summary)); marshalErr == nil {
		emit(StreamEvent{
			Type:       StreamEventToolCompleted,
			Step:       taskStep,
			ToolName:   taskToolName,
			CallID:     taskCallID,
			Output:     string(finalPayload),
			RawOutput:  string(finalPayload),
			DurationMS: outcome.ElapsedMS,
		})
	}
	assistantMetadata := map[string]any{
		"source":             "targeted_subagent",
		"lineage_kind":       "delegated_subagent",
		"lineage_label":      "@" + targetName,
		"subagent":           targetName,
		"requested_subagent": targetName,
		"child_session_id":   strings.TrimSpace(launch.ChildSession.ID),
		"target_kind":        RunTargetKindSubagent,
		"target_name":        targetName,
	}
	assistantMessage, _, assistantEvent, appendErr := s.sessions.AppendMessage(parentSession.ID, "assistant", assistantText, assistantMetadata)
	if appendErr != nil {
		return RunResult{}, appendErr
	}
	emit(StreamEvent{Type: StreamEventMessageStored, Step: maxInt(1, childResult.Steps), Message: &assistantMessage})

	return RunResult{
		SessionID:        parentSession.ID,
		Agent:            strings.TrimSpace(targetName),
		Model:            strings.TrimSpace(parentSession.Preference.Model),
		Thinking:         strings.TrimSpace(parentSession.Preference.Thinking),
		ReasoningSummary: strings.TrimSpace(childResult.ReasoningSummary),
		Steps:            maxInt(1, childResult.Steps),
		ToolCallCount:    childResult.ToolCallCount,
		AssistantMessage: assistantMessage,
		Events: func() []pebblestore.EventEnvelope {
			out := make([]pebblestore.EventEnvelope, 0, 1)
			if assistantEvent != nil {
				out = append(out, *assistantEvent)
			}
			return out
		}(),
		TargetKind: RunTargetKindSubagent,
		TargetName: targetName,
	}, nil
}

func forwardTargetedSubagentStreamEvent(emit StreamHandler, event StreamEvent) {
	if emit == nil {
		return
	}
	switch strings.TrimSpace(event.Type) {
	case StreamEventStepStarted:
		emit(StreamEvent{Type: event.Type, Step: event.Step})
	case StreamEventAssistantDelta, StreamEventAssistantCommentary:
		emit(StreamEvent{Type: event.Type, Step: event.Step, Delta: event.Delta})
	case StreamEventReasoningStarted:
		emit(StreamEvent{Type: event.Type, Step: event.Step, ReasoningKey: event.ReasoningKey})
	case StreamEventReasoningDelta:
		emit(StreamEvent{Type: event.Type, Step: event.Step, ReasoningKey: event.ReasoningKey, Delta: event.Delta})
	case StreamEventReasoningSummary:
		emit(StreamEvent{Type: event.Type, Step: event.Step, ReasoningKey: event.ReasoningKey, Summary: event.Summary})
	case StreamEventReasoningCompleted:
		emit(StreamEvent{Type: event.Type, Step: event.Step, ReasoningKey: event.ReasoningKey, Summary: event.Summary})
	case StreamEventPermissionReq, StreamEventPermissionUpdate:
		emit(event)
	}
}

func (s *Service) runTurn(ctx context.Context, sessionID string, options RunOptions, onEvent StreamHandler) (result RunResult, runErr error) {
	sessionID = strings.TrimSpace(sessionID)
	emit := func(StreamEvent) {}
	sessionResolved := false
	lifecycleClaimed := false
	runID := strings.TrimSpace(options.RunID)
	defer func() {
		if !sessionResolved || !lifecycleClaimed {
			return
		}
		terminalErrText := ""
		if snapshot, changed, err := s.finishSessionLifecycle(sessionID, runID, runErr); err == nil && changed {
			emitLifecycleSnapshot(emit, snapshot)
			if strings.TrimSpace(snapshot.Error) != "" {
				terminalErrText = strings.TrimSpace(snapshot.Error)
			} else if strings.TrimSpace(snapshot.StopReason) != "" {
				terminalErrText = strings.TrimSpace(snapshot.StopReason)
			}
		}
		if runErr == nil {
			return
		}
		if terminalErrText == "" {
			terminalErrText = runErr.Error()
		}
		s.emitSessionStatus(emit, sessionID, runID, "error", "", terminalErrText, "")
		if onEvent != nil {
			onEvent(StreamEvent{Type: StreamEventTurnError, SessionID: sessionID, RunID: runID, Error: terminalErrText})
		}
		s.persistRunFailure(sessionID, runErr)
	}()

	if sessionID == "" {
		return RunResult{}, errors.New("session id is required")
	}
	permissionSessionID := strings.TrimSpace(options.PermissionSessionID)
	if permissionSessionID == "" {
		permissionSessionID = sessionID
	}
	manualCompact := options.Compact
	prompt := strings.TrimSpace(options.Prompt)
	if prompt == "" && !manualCompact {
		return RunResult{}, errors.New("prompt is required")
	}
	if prompt == "" && manualCompact {
		prompt = "manual context compact request"
	}

	sessionSnapshot, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return RunResult{}, err
	}
	if !ok {
		return RunResult{}, fmt.Errorf("session %q not found", sessionID)
	}
	sessionResolved = true
	targetKind, targetName, agentName, err := s.resolveRunTarget(options)
	if err != nil {
		return RunResult{}, err
	}
	targetedSubagentViaTask := targetKind == RunTargetKindSubagent && !options.AllowSubagent
	agentProfile, err := s.resolveAgentProfile(agentName, targetKind)
	if err != nil {
		return RunResult{}, err
	}
	activeAgent := strings.TrimSpace(agentProfile.Name)
	if activeAgent == "" {
		activeAgent = "swarm"
	}

	resolvedPreference, err := s.resolveMainSessionPreference(sessionID)
	if err != nil {
		return RunResult{}, err
	}
	providerID := strings.ToLower(strings.TrimSpace(resolvedPreference.Preference.Provider))
	if providerID == "" {
		return RunResult{}, errors.New("resolved model provider is empty")
	}
	serviceTier := ""
	if providerID == "codex" {
		serviceTier = codexruntime.NormalizeServiceTier(resolvedPreference.Preference.ServiceTier)
	}
	if s.providers == nil {
		return RunResult{}, errors.New("provider registry is not configured")
	}
	providerRunner, ok := s.providers.GetRunner(providerID)
	if !ok {
		return RunResult{}, fmt.Errorf("provider %q is configured but not runnable yet", providerID)
	}
	if runID == "" {
		runID = s.newRunID()
	}
	compiledPolicy := options.CompiledPolicy
	effectiveDisabledTools := cloneDisabledTools(options.DisabledTools)
	if agentPolicy, agentDisabled, scopeErr := s.compileAgentToolScope(agentProfile); scopeErr != nil {
		return RunResult{}, scopeErr
	} else {
		if agentPolicy != nil {
			merged := mergePermissionPolicies(agentPolicy, compiledPolicy)
			compiledPolicy = &merged
		}
		effectiveDisabledTools = mergeDisabledTools(effectiveDisabledTools, agentDisabled)
	}
	if options.ToolScope != nil {
		if targetKind == RunTargetKindSubagent || targetKind == RunTargetKindBackground {
			return RunResult{}, errors.New("request-time tool_scope is not supported for targeted agent runs; update the saved agent profile instead")
		}
		compiled, disabled, scopeErr := s.compileRunToolScope(*options.ToolScope)
		if scopeErr != nil {
			return RunResult{}, scopeErr
		}
		if compiled != nil {
			merged := mergePermissionPolicies(compiledPolicy, compiled)
			compiledPolicy = &merged
		}
		effectiveDisabledTools = mergeDisabledTools(effectiveDisabledTools, disabled)
	}
	resolvedExecutionContext, err := s.resolveRunExecutionContext(sessionSnapshot, options.ExecutionContextOrDefault())
	if err != nil {
		return RunResult{}, err
	}
	if options.Background {
		metadata := buildBackgroundRunMetadata(sessionSnapshot.Metadata, targetKind, targetName, resolvedExecutionContext)
		updatedSession, _, updateErr := s.sessions.UpdateMetadata(sessionID, metadata)
		if updateErr != nil {
			return RunResult{}, fmt.Errorf("persist background session metadata: %w", updateErr)
		}
		sessionSnapshot = updatedSession
	}
	sandboxCtx, sandboxCleanup, err := s.resolveRunSandboxContext(resolvedExecutionContext, runID)
	if err != nil {
		return RunResult{}, err
	}
	defer sandboxCleanup()

	baseInstructions := s.composeInstructionsForScope(tool.WorkspaceScope{
		PrimaryPath: sandboxCtx.WorkspacePath,
		Roots:       append([]string(nil), sandboxCtx.WorkspaceRoots...),
	}, agentProfile, options.Instructions)
	baseInstructions = appendSandboxRuntimeContext(baseInstructions, sandboxCtx.Enabled, sandboxCtx.WorkspacePath, sandboxCtx.WorkspaceRoots)

	runFailed := true
	defer func() {
		if !runFailed || s.permissions == nil {
			return
		}
		_, _ = s.permissions.CancelRunPending(permissionSessionID, runID, "run terminated before permission resolution")
	}()

	var emitMu sync.Mutex
	emit = func(event StreamEvent) {
		if strings.TrimSpace(event.SessionID) == "" {
			event.SessionID = sessionID
		}
		if strings.TrimSpace(event.RunID) == "" {
			event.RunID = runID
		}
		if s != nil {
			s.publishStreamEventEnvelope(event)
			s.mirrorHostedStreamEvent(event)
		}
		var derivedStatusEvent *StreamEvent
		var lifecycleEvent *StreamEvent
		if snapshot, changed, err := s.transitionSessionLifecycleForEvent(event); err == nil && changed {
			lifecycleEvent = &StreamEvent{
				Type:      StreamEventSessionLifecycle,
				SessionID: snapshot.SessionID,
				RunID:     snapshot.RunID,
				Lifecycle: &snapshot,
			}
			if s != nil {
				s.publishStreamEventEnvelope(*lifecycleEvent)
				s.mirrorHostedStreamEvent(*lifecycleEvent)
			}
		}
		if status := sessionStatusForEvent(event); status != "" {
			statusEvent := StreamEvent{
				Type:      StreamEventSessionStatus,
				SessionID: event.SessionID,
				RunID:     event.RunID,
				Status:    status,
				Summary:   event.Summary,
				Error:     event.Error,
				Agent:     event.Agent,
			}
			if s != nil {
				s.publishStreamEventEnvelope(statusEvent)
				s.mirrorHostedStreamEvent(statusEvent)
			}
			derivedStatusEvent = &statusEvent
		}
		if onEvent == nil {
			return
		}
		emitMu.Lock()
		onEvent(event)
		if lifecycleEvent != nil {
			onEvent(*lifecycleEvent)
		}
		if derivedStatusEvent != nil {
			onEvent(*derivedStatusEvent)
		}
		emitMu.Unlock()
	}
	startSnapshot, err := s.beginSessionLifecycle(sessionID, runID, s.effectiveRunOwnerTransport(options, onEvent))
	if err != nil {
		return RunResult{}, err
	}
	lifecycleClaimed = true
	emitLifecycleSnapshot(emit, startSnapshot)
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	s.attachLifecycleCancel(sessionID, runID, runCancel)
	ctx = runCtx
	if runningSnapshot, changed, err := s.transitionSessionLifecycle(sessionID, runID, lifecyclePhaseRunning); err == nil && changed {
		emitLifecycleSnapshot(emit, runningSnapshot)
	}
	emit(StreamEvent{Type: StreamEventTurnStarted, Agent: activeAgent})
	s.emitSessionStatus(emit, sessionID, runID, "running", activeAgent, "", activeAgent)

	markToolStart := func(step int, call tool.Call) {
		if s.permissions == nil {
			return
		}
		_, _, _ = s.permissions.MarkToolStarted(permissionSessionID, runID, call.CallID, step, time.Now().UnixMilli())
	}
	markToolCompleted := func(step int, call tool.Call, result tool.Result) {
		if s.permissions == nil {
			return
		}
		_, _, _ = s.permissions.MarkToolCompleted(permissionSessionID, runID, call.CallID, step, result, time.Now().UnixMilli())
	}

	var (
		userMessage pebblestore.MessageSnapshot
		userEvent   *pebblestore.EventEnvelope
	)
	if !manualCompact {
		userMessage, _, userEvent, err = s.sessions.AppendMessage(sessionID, "user", prompt, map[string]any{"source": "run_turn"})
		if err != nil {
			return RunResult{}, err
		}
		emit(StreamEvent{Type: StreamEventMessageStored, Message: &userMessage})
		if s.eventPublish != nil && shouldGenerateMemorySessionTitle(sessionSnapshot) {
			s.startMemorySessionTitleFlow(sessionID, prompt, resolvedPreference.Preference, emit)
		}
	}
	if targetedSubagentViaTask {
		result, err := s.runTargetedSubagent(ctx, sessionSnapshot, options, targetName, emit)
		if err != nil {
			return RunResult{}, err
		}
		result.SessionID = sessionID
		result.Agent = activeAgent
		result.Model = resolvedPreference.Preference.Model
		result.Thinking = resolvedPreference.Preference.Thinking
		result.UserMessage = userMessage
		result.Background = options.Background
		result.TargetKind = targetKind
		result.TargetName = targetName
		if userEvent != nil {
			result.Events = append([]pebblestore.EventEnvelope{*userEvent}, result.Events...)
		}
		if completedSnapshot, changed, lifecycleErr := s.finishSessionLifecycle(sessionID, runID, nil); lifecycleErr == nil && changed {
			emitLifecycleSnapshot(emit, completedSnapshot)
		}
		s.emitSessionStatus(emit, sessionID, runID, "idle", "", "", "")
		emit(StreamEvent{Type: StreamEventTurnCompleted, Summary: result.ReasoningSummary})
		runFailed = false
		return result, nil
	}

	messages, err := s.sessions.ListMessages(sessionID, 0, defaultHistoryLimit)
	if err != nil {
		return RunResult{}, err
	}
	messages = trimMessagesToLatestCompactionCheckpoint(messages)

	input := buildInput(messages)
	rawToolDefinitions := convertToolDefinitions(s.ListAgentToolDefinitions())
	rawCustomToolDefinitions := convertToolDefinitions(s.customAgentToolDefinitions())
	toolDefinitions := filterToolDefinitions(rawToolDefinitions, effectiveDisabledTools)
	runRequestDebugEvent("tool_inventory", map[string]any{
		"session_id":            sessionID,
		"run_id":                runID,
		"target_kind":           targetKind,
		"target_name":           targetName,
		"resolved_agent":        activeAgent,
		"raw_tool_count":        len(rawToolDefinitions),
		"raw_tools":             runRequestDebugToolDefinitions(rawToolDefinitions),
		"raw_custom_tool_count": len(rawCustomToolDefinitions),
		"raw_custom_tools":      runRequestDebugToolDefinitions(rawCustomToolDefinitions),
		"filtered_tool_count":   len(toolDefinitions),
		"filtered_tools":        runRequestDebugToolDefinitions(toolDefinitions),
		"disabled_tools":        runRequestDebugDisabledTools(effectiveDisabledTools),
	})

	assistantFragments := make([]string, 0, 8)
	toolMessages := make([]pebblestore.MessageSnapshot, 0, 8)
	commentaryMessages := make([]pebblestore.MessageSnapshot, 0, 8)
	events := make([]pebblestore.EventEnvelope, 0, 16)
	if userEvent != nil {
		events = append(events, *userEvent)
	}
	totalToolCalls := 0
	stepsCompleted := 0
	reasoningSummary := ""
	emptyStepRetries := 0
	contextCompactionAttempts := 0
	accumulatedUsage := provideriface.TokenUsage{}
	var (
		turnUsageRecord   *pebblestore.SessionTurnUsageSnapshot
		usageSummaryState *pebblestore.SessionUsageSummary
	)
	if !manualCompact {
		if compactedInput, resetSummary, compactEvents, compactErr := s.maybeAutoCompactRunContext(
			ctx,
			sessionID,
			prompt,
			providerID,
			resolvedPreference.Preference.Model,
			sessionSnapshot.Metadata,
			resolvedPreference.Preference,
			resolvedPreference.ContextWindow,
			resolvedPreference.MaxOutputTokens,
			0,
			emit,
		); compactErr != nil {
			return RunResult{}, compactErr
		} else if len(compactedInput) > 0 {
			input = compactedInput
			if resetSummary != nil {
				usageSummaryState = resetSummary
			}
			if len(compactEvents) > 0 {
				events = append(events, compactEvents...)
			}
			if refreshed, ok, refreshErr := s.sessions.GetSession(sessionID); refreshErr == nil && ok {
				sessionSnapshot = refreshed
			}
		}
	}
	if manualCompact {
		stepsCompleted = 1
		emit(StreamEvent{Type: StreamEventStepStarted, Step: stepsCompleted})
		compactedSummary, compactErr := s.compactRunContextWithMemory(
			ctx,
			sessionID,
			prompt,
			"",
			resolvedPreference.Preference,
			resolvedPreference.ContextWindow,
			resolvedPreference.MaxOutputTokens,
			true,
			stepsCompleted,
			1,
			emit,
		)
		if compactErr != nil {
			return RunResult{}, fmt.Errorf("manual compact failed: %w", compactErr)
		}
		resetSummary, compactIndex, compactEvents, compactErr := s.applyContextCompactionArtifacts(
			sessionID,
			compactedSummary,
			"manual",
			resolvedPreference.ContextWindow,
			providerID,
			resolvedPreference.Preference.Model,
			stepsCompleted,
			emit,
		)
		if compactErr != nil {
			return RunResult{}, fmt.Errorf("manual compact post-processing failed: %w", compactErr)
		}
		if len(compactEvents) > 0 {
			events = append(events, compactEvents...)
		}
		if resetSummary != nil {
			usageSummaryState = resetSummary
		}
		attachedPlanLabel := ""
		if s.sessions != nil {
			if activePlan, ok, planErr := s.sessions.GetActivePlan(sessionID); planErr != nil {
				return RunResult{}, fmt.Errorf("manual compact active plan lookup failed: %w", planErr)
			} else if ok {
				attachedPlanLabel = compactedActivePlanLabel(&activePlan)
			}
		}
		reasoningSummary = fmt.Sprintf("Context compacted into checkpoint #%d.", compactIndex)
		assistantText := buildManualCompactionAssistantText(compactedSummary, compactIndex, attachedPlanLabel)
		assistantMessage, _, assistantEvent, appendErr := s.sessions.AppendMessage(sessionID, "assistant", assistantText, nil)
		if appendErr != nil {
			return RunResult{}, appendErr
		}
		if assistantEvent != nil {
			events = append(events, *assistantEvent)
		}
		emit(StreamEvent{Type: StreamEventMessageStored, Step: stepsCompleted, Message: &assistantMessage})
		if completedSnapshot, changed, lifecycleErr := s.finishSessionLifecycle(sessionID, runID, nil); lifecycleErr == nil && changed {
			emitLifecycleSnapshot(emit, completedSnapshot)
		}
		s.emitSessionStatus(emit, sessionID, runID, "idle", "", "", "")
		emit(StreamEvent{Type: StreamEventTurnCompleted, Step: stepsCompleted, Summary: reasoningSummary})
		runFailed = false
		return RunResult{
			SessionID:        sessionID,
			Agent:            activeAgent,
			Model:            resolvedPreference.Preference.Model,
			Thinking:         resolvedPreference.Preference.Thinking,
			ReasoningSummary: reasoningSummary,
			Steps:            stepsCompleted,
			ToolCallCount:    totalToolCalls,
			TurnUsage:        turnUsageRecord,
			UsageSummary:     usageSummaryState,
			UserMessage:      userMessage,
			ToolMessages:     toolMessages,
			Commentary:       commentaryMessages,
			AssistantMessage: assistantMessage,
			Events:           events,
			Background:       options.Background,
			TargetKind:       targetKind,
			TargetName:       targetName,
		}, nil
	}

	flushAssistantFragments := func(step int) (pebblestore.MessageSnapshot, bool, error) {
		assistantText := strings.TrimSpace(strings.Join(assistantFragments, "\n\n"))
		if assistantText == "" {
			return pebblestore.MessageSnapshot{}, false, nil
		}
		assistantMessage, _, assistantEvent, appendErr := s.sessions.AppendMessage(sessionID, "assistant", assistantText, nil)
		if appendErr != nil {
			return pebblestore.MessageSnapshot{}, false, appendErr
		}
		assistantFragments = assistantFragments[:0]
		if assistantEvent != nil {
			events = append(events, *assistantEvent)
		}
		emit(StreamEvent{Type: StreamEventMessageStored, Step: step, Message: &assistantMessage})
		return assistantMessage, true, nil
	}

	tryContextOverflowCompaction := func(step int, assistantDraft string) (bool, error) {
		if contextCompactionAttempts >= contextCompactionRetryLimit {
			return false, nil
		}
		contextCompactionAttempts++
		compactedSummary, compactErr := s.compactRunContextWithMemory(
			ctx,
			sessionID,
			prompt,
			strings.TrimSpace(assistantDraft),
			resolvedPreference.Preference,
			resolvedPreference.ContextWindow,
			resolvedPreference.MaxOutputTokens,
			false,
			step,
			contextCompactionAttempts,
			emit,
		)
		if compactErr != nil {
			return false, fmt.Errorf("context overflow compact continuation failed: %w", compactErr)
		}
		resetSummary, _, compactEvents, compactErr := s.applyContextCompactionArtifacts(
			sessionID,
			compactedSummary,
			"overflow",
			resolvedPreference.ContextWindow,
			providerID,
			resolvedPreference.Preference.Model,
			step,
			emit,
		)
		if compactErr != nil {
			return false, fmt.Errorf("context overflow compact bookkeeping failed: %w", compactErr)
		}
		if len(compactEvents) > 0 {
			events = append(events, compactEvents...)
		}
		if resetSummary != nil {
			usageSummaryState = resetSummary
		}
		turnUsageRecord = nil
		accumulatedUsage = provideriface.TokenUsage{}
		var activePlan *pebblestore.SessionPlanSnapshot
		if s.sessions != nil {
			plan, ok, planErr := s.sessions.GetActivePlan(sessionID)
			if planErr != nil {
				return false, fmt.Errorf("context overflow compact continuation active plan lookup failed: %w", planErr)
			}
			if ok {
				activePlan = &plan
			}
		}
		compactedInput := buildCompactedContinuationInput(prompt, compactedSummary, activePlan, "overflow")
		if len(compactedInput) == 0 {
			return false, errors.New("context overflow compact continuation produced empty input")
		}
		input = compactedInput
		emptyStepRetries = 0
		return true, nil
	}

	for step := 1; ; step++ {
		if err := ctx.Err(); err != nil {
			return RunResult{}, err
		}
		emit(StreamEvent{Type: StreamEventStepStarted, Step: step})

		requestMode := sessionruntime.NormalizeMode(sessionSnapshot.Mode)
		if mode, modeErr := s.sessions.GetMode(sessionID); modeErr == nil {
			requestMode = mode
			sessionSnapshot.Mode = mode
		}
		executionMode, modeWarning, modeErr := s.resolveExecutionMode(requestMode, agentProfile)
		if modeErr != nil {
			return RunResult{}, modeErr
		}
		if modeWarning != "" {
			emit(StreamEvent{Type: StreamEventSessionWarning, Step: step, Warning: modeWarning})
		}
		stepInstructions := composeModeAwareInstructions(baseInstructions, executionMode, s.permissions != nil && s.permissions.BypassPermissions(), agentProfile)
		stepReasoningSummary := ""
		stepReasoningMessages := make(map[string]*pebblestore.MessageSnapshot, 4)
		stepReasoningByKey := make(map[string]string, 4)
		stepReasoningOrder := make([]string, 0, 4)
		stepReasoningLastEmitted := make(map[string]string, 4)
		stepReasoningLastEmitAt := make(map[string]time.Time, 4)
		activeReasoningKey := ""
		var stepReasoningErr error
		reasoningStreamingActive := false
		const reasoningStreamEmitMinInterval = 60 * time.Millisecond
		normalizeReasoningKey := func(key string) string {
			key = strings.TrimSpace(key)
			if key == "" {
				return "default"
			}
			return key
		}
		rebuildStepReasoningSummary := func() string {
			if len(stepReasoningOrder) == 0 {
				return ""
			}
			parts := make([]string, 0, len(stepReasoningOrder))
			for _, key := range stepReasoningOrder {
				content := strings.TrimSpace(stepReasoningByKey[key])
				if content == "" {
					continue
				}
				parts = append(parts, content)
			}
			return strings.TrimSpace(strings.Join(parts, "\n\n"))
		}
		rememberReasoningKey := func(key string) string {
			key = normalizeReasoningKey(key)
			if _, ok := stepReasoningByKey[key]; !ok {
				stepReasoningOrder = append(stepReasoningOrder, key)
			}
			return key
		}
		updateStepReasoning := func(key, content string) string {
			content = strings.TrimSpace(content)
			if content == "" {
				return ""
			}
			key = rememberReasoningKey(key)
			stepReasoningByKey[key] = content
			stepReasoningSummary = rebuildStepReasoningSummary()
			return key
		}
		latestReasoningSegmentSummary := func() string {
			if key := strings.TrimSpace(activeReasoningKey); key != "" {
				if content := strings.TrimSpace(stepReasoningByKey[key]); content != "" {
					return content
				}
			}
			for i := len(stepReasoningOrder) - 1; i >= 0; i-- {
				if content := strings.TrimSpace(stepReasoningByKey[stepReasoningOrder[i]]); content != "" {
					return content
				}
			}
			return strings.TrimSpace(stepReasoningSummary)
		}
		persistStepReasoning := func(key, content string) {
			if stepReasoningErr != nil {
				return
			}
			content = strings.TrimSpace(content)
			if content == "" {
				return
			}
			key = rememberReasoningKey(key)
			stepReasoningByKey[key] = content
			stepReasoningSummary = rebuildStepReasoningSummary()
			nextMessage, messageEvent, streamEvent, err := s.persistReasoningMessageSnapshot(sessionID, stepReasoningMessages[key], content)
			if err != nil {
				stepReasoningErr = err
				return
			}
			stepReasoningMessages[key] = nextMessage
			if messageEvent != nil {
				events = append(events, *messageEvent)
			}
			if strings.TrimSpace(streamEvent.Type) != "" {
				streamEvent.Step = step
				streamEvent.ReasoningKey = key
				emit(streamEvent)
			}
		}
		var emitReasoningDelta func(key, delta string)
		emitReasoningSnapshotIfDue := func(key string, force bool) {
			key = normalizeReasoningKey(key)
			snapshot := strings.TrimSpace(stepReasoningByKey[key])
			if snapshot == "" {
				return
			}
			lastSnapshot := strings.TrimSpace(stepReasoningLastEmitted[key])
			if snapshot == lastSnapshot {
				return
			}
			now := time.Now()
			if !force {
				if lastAt := stepReasoningLastEmitAt[key]; !lastAt.IsZero() && now.Sub(lastAt) < reasoningStreamEmitMinInterval {
					return
				}
			}
			stepReasoningLastEmitted[key] = snapshot
			stepReasoningLastEmitAt[key] = now
			emitReasoningDelta(key, snapshot)
		}
		var emitReasoningCompleted func(summary string)
		switchReasoningSegment := func(nextKey string) string {
			nextKey = normalizeReasoningKey(nextKey)
			if reasoningStreamingActive && activeReasoningKey != "" && activeReasoningKey != nextKey {
				emitReasoningSnapshotIfDue(activeReasoningKey, true)
				emitReasoningCompleted(stepReasoningByKey[activeReasoningKey])
			}
			activeReasoningKey = nextKey
			return nextKey
		}
		emitReasoningStarted := func(key string) {
			key = normalizeReasoningKey(key)
			if reasoningStreamingActive && activeReasoningKey == key {
				return
			}
			reasoningStreamingActive = true
			activeReasoningKey = key
			emit(StreamEvent{Type: StreamEventReasoningStarted, Step: step, ReasoningKey: key})
		}
		emitReasoningDelta = func(key, delta string) {
			delta = strings.TrimSpace(delta)
			if delta == "" {
				return
			}
			key = normalizeReasoningKey(key)
			emitReasoningStarted(key)
			emit(StreamEvent{Type: StreamEventReasoningDelta, Step: step, Delta: delta, ReasoningKey: key})
		}
		emitReasoningCompleted = func(summary string) {
			summary = strings.TrimSpace(summary)
			key := normalizeReasoningKey(activeReasoningKey)
			if summary == "" {
				summary = strings.TrimSpace(stepReasoningByKey[key])
			}
			if !reasoningStreamingActive && summary == "" {
				return
			}
			if key != "" {
				emitReasoningSnapshotIfDue(key, true)
				if summary != "" {
					persistStepReasoning(key, summary)
				}
			}
			if summary != "" {
				emit(StreamEvent{Type: StreamEventReasoningSummary, Step: step, Summary: summary, ReasoningKey: activeReasoningKey})
			}
			emit(StreamEvent{Type: StreamEventReasoningCompleted, Step: step, Summary: summary, ReasoningKey: activeReasoningKey})
			reasoningStreamingActive = false
		}

		stepRequest := provideriface.Request{
			SessionID:         sessionID,
			Model:             resolvedPreference.Preference.Model,
			Thinking:          resolvedPreference.Preference.Thinking,
			Instructions:      stepInstructions,
			Input:             input,
			Tools:             toolDefinitions,
			ToolChoice:        "auto",
			ServiceTier:       serviceTier,
			ContextMode:       resolvedPreference.Preference.ContextMode,
			ContextWindow:     resolvedPreference.ContextWindow,
			ParallelToolCalls: true,
			WorkspacePath:     sandboxCtx.WorkspacePath,
			ToolInvoker: s.newProviderToolInvoker(providerToolInvokerConfig{
				sessionID:            sessionID,
				permissionSessionID:  permissionSessionID,
				runID:                runID,
				step:                 step,
				sessionMode:          executionMode,
				agentProfile:         agentProfile,
				workspacePath:        sandboxCtx.WorkspacePath,
				workspaceRoots:       append([]string(nil), sandboxCtx.WorkspaceRoots...),
				workspaceOriginPath:  sandboxCtx.OriginWorkspacePath,
				workspaceOriginRoots: append([]string(nil), sandboxCtx.OriginWorkspaceRoots...),
				workspaceName:        sessionSnapshot.WorkspaceName,
				sandboxEnabled:       sandboxCtx.Enabled,
				emit:                 emit,
				policy:               compiledPolicy,
			}),
		}
		runRequestDebugEvent("provider_request", map[string]any{
			"session_id":          sessionID,
			"run_id":              runID,
			"step":                step,
			"provider":            providerID,
			"model":               resolvedPreference.Preference.Model,
			"thinking":            resolvedPreference.Preference.Thinking,
			"target_kind":         targetKind,
			"target_name":         targetName,
			"resolved_agent":      activeAgent,
			"agent_profile_name":  strings.TrimSpace(agentProfile.Name),
			"background":          options.Background,
			"routed_session":      sessionSnapshot.Metadata["swarm_routed_session"],
			"workspace_path":      sandboxCtx.WorkspacePath,
			"execution_mode":      executionMode,
			"tool_choice":         stepRequest.ToolChoice,
			"parallel_tool_calls": stepRequest.ParallelToolCalls,
			"tool_count":          len(toolDefinitions),
			"tools":               runRequestDebugToolDefinitions(toolDefinitions),
			"instructions":        stepInstructions,
			"input":               input,
		})

		response, err := providerRunner.CreateResponseStreaming(ctx, stepRequest, func(event provideriface.StreamEvent) {
			switch event.Type {
			case provideriface.StreamEventOutputTextDelta:
				emit(StreamEvent{Type: StreamEventAssistantDelta, Step: step, Delta: event.Delta})
			case provideriface.StreamEventAssistantCommentary:
				emit(StreamEvent{Type: StreamEventAssistantCommentary, Step: step, Delta: event.Delta})
			case provideriface.StreamEventReasoningSummaryDelta:
				reasoningKey := switchReasoningSegment(event.ReasoningKey)
				if updateStepReasoning(reasoningKey, event.Delta) != "" {
					emitReasoningSnapshotIfDue(reasoningKey, false)
				}
			}
		})
		if err != nil {
			if isContextOverflowDiagnostic(err.Error()) {
				assistantDraft := strings.TrimSpace(strings.Join(assistantFragments, "\n\n"))
				resumed, compactErr := tryContextOverflowCompaction(step, assistantDraft)
				if compactErr != nil {
					return RunResult{}, compactErr
				}
				if resumed {
					continue
				}
			}
			return RunResult{}, err
		}
		runRequestDebugEvent("provider_response", map[string]any{
			"session_id":          sessionID,
			"run_id":              runID,
			"step":                step,
			"provider":            providerID,
			"model":               resolvedPreference.Preference.Model,
			"target_kind":         targetKind,
			"target_name":         targetName,
			"resolved_agent":      activeAgent,
			"background":          options.Background,
			"routed_session":      sessionSnapshot.Metadata["swarm_routed_session"],
			"stop_reason":         response.StopReason,
			"text":                response.Text,
			"function_call_count": len(response.FunctionCalls),
			"function_calls":      response.FunctionCalls,
			"assistant_messages":  response.AssistantMessages,
			"usage":               response.Usage,
			"restart_turn":        response.RestartTurn,
		})
		if stepReasoningErr != nil {
			return RunResult{}, stepReasoningErr
		}
		stepsCompleted = step
		accumulatedUsage = mergeTokenUsage(accumulatedUsage, response.Usage)
		if shouldPersistProviderUsage(providerID, accumulatedUsage) {
			turnUsage, usageSummary, usageEvent, usageErr := s.recordProviderUsageSnapshot(sessionID, runID, providerID, resolvedPreference.Preference.Model, resolvedPreference.ContextWindow, stepsCompleted, accumulatedUsage)
			if usageErr != nil {
				return RunResult{}, usageErr
			}
			turnUsageCopy := turnUsage
			usageSummaryCopy := usageSummary
			turnUsageRecord = &turnUsageCopy
			usageSummaryState = &usageSummaryCopy
			if usageEvent != nil {
				events = append(events, *usageEvent)
			}
			emit(StreamEvent{
				Type:         StreamEventUsageUpdated,
				Step:         step,
				TurnUsage:    turnUsageRecord,
				UsageSummary: usageSummaryState,
			})
		}
		if responseReasoningSummary := strings.TrimSpace(response.ReasoningSummary); responseReasoningSummary != "" {
			if len(stepReasoningOrder) == 0 {
				activeReasoningKey = updateStepReasoning(activeReasoningKey, responseReasoningSummary)
			} else if stepReasoningSummary == "" {
				stepReasoningSummary = responseReasoningSummary
			}
		}
		if activeReasoningKey == "" && len(stepReasoningOrder) > 0 {
			activeReasoningKey = stepReasoningOrder[len(stepReasoningOrder)-1]
		}
		emitReasoningCompleted(latestReasoningSegmentSummary())
		if stepReasoningSummary == "" {
			if responseReasoningSummary := strings.TrimSpace(response.ReasoningSummary); responseReasoningSummary != "" {
				stepReasoningSummary = responseReasoningSummary
			}
		}
		if stepReasoningErr != nil {
			return RunResult{}, stepReasoningErr
		}
		if stepReasoningSummary != "" {
			reasoningSummary = stepReasoningSummary
		}

		responseText := strings.TrimSpace(response.Text)
		if responseText != "" {
			assistantFragments = append(assistantFragments, responseText)
		}
		if len(response.AssistantMessages) > 0 {
			for _, message := range response.AssistantMessages {
				if message.Phase != provideriface.AssistantPhaseCommentary {
					continue
				}
				commentaryText := strings.TrimSpace(message.Text)
				if commentaryText == "" {
					continue
				}
				commentaryMessage, _, commentaryEvent, appendErr := s.sessions.AppendMessage(sessionID, "assistant", commentaryText, map[string]any{"phase": string(provideriface.AssistantPhaseCommentary)})
				if appendErr != nil {
					return RunResult{}, appendErr
				}
				commentaryMessages = append(commentaryMessages, commentaryMessage)
				if commentaryEvent != nil {
					events = append(events, *commentaryEvent)
				}
				emit(StreamEvent{Type: StreamEventMessageStored, Step: step, Message: &commentaryMessage})
			}
		}

		if response.RestartTurn {
			messages, err = s.sessions.ListMessages(sessionID, 0, defaultHistoryLimit)
			if err != nil {
				return RunResult{}, err
			}
			messages = trimMessagesToLatestCompactionCheckpoint(messages)
			if responseText == "" && stepReasoningSummary == "" {
				input = buildInput(messages)
				emptyStepRetries = 0
				continue
			}
			if responseText != "" {
				assistantFragments = append(assistantFragments, responseText)
			}
			if stepReasoningSummary != "" {
				reasoningSummary = stepReasoningSummary
			}
			input = buildInput(messages)
			emptyStepRetries = 0
			continue
		}

		if len(response.FunctionCalls) == 0 {
			// Let the model decide loop length:
			// - text + no tool calls => assistant is done for this turn
			// - reasoning-only + no tool calls => keep looping for final answer
			// - fully empty response => retry briefly for transient provider gaps, then fail clearly
			if responseText == "" && shouldTriggerContextCompaction(response) {
				assistantDraft := strings.TrimSpace(strings.Join(assistantFragments, "\n\n"))
				resumed, compactErr := tryContextOverflowCompaction(step, assistantDraft)
				if compactErr != nil {
					return RunResult{}, compactErr
				}
				if resumed {
					continue
				}
			}
			if responseText == "" && stepReasoningSummary != "" {
				emptyStepRetries = 0
				continue
			}
			if responseText == "" {
				emptyStepRetries++
				if emptyStepRetries <= emptyStepRetryLimit {
					retryDelay := emptyStepRetryDelay(emptyStepRetries)
					if err := waitForRetryDelay(ctx, retryDelay); err != nil {
						return RunResult{}, err
					}
					continue
				}
				return RunResult{}, emptyProviderStepError(providerID, step, emptyStepRetries, response)
			}
			emptyStepRetries = 0
			break
		}
		emptyStepRetries = 0

		flushedAssistantInput := map[string]any(nil)
		if flushedAssistantMessage, flushed, flushErr := flushAssistantFragments(step); flushErr != nil {
			return RunResult{}, flushErr
		} else if flushed {
			if assistantInput, ok := buildAssistantOutputInput(flushedAssistantMessage.Content); ok {
				flushedAssistantInput = assistantInput
			}
		}

		totalToolCalls += len(response.FunctionCalls)
		toolCalls := make([]tool.Call, 0, len(response.FunctionCalls))
		toolCallMetadata := make([]map[string]any, 0, len(response.FunctionCalls))
		for i, call := range response.FunctionCalls {
			callID := strings.TrimSpace(call.CallID)
			if callID == "" {
				callID = fmt.Sprintf("call_%d_%d", step, i+1)
			}
			name := strings.TrimSpace(call.Name)
			if name == "" {
				name = "tool"
			}
			arguments := strings.TrimSpace(call.Arguments)
			if arguments == "" {
				arguments = "{}"
			}
			toolCalls = append(toolCalls, tool.Call{
				CallID:    callID,
				Name:      name,
				Arguments: arguments,
			})
			markToolStart(step, tool.Call{CallID: callID, Name: name, Arguments: arguments})
			toolCallMetadata = append(toolCallMetadata, cloneGenericMap(call.Metadata))
			emit(StreamEvent{
				Type:      StreamEventToolStarted,
				Step:      step,
				ToolName:  name,
				CallID:    callID,
				Arguments: arguments,
			})
		}
		executionMode, _, modeErr = s.resolveExecutionMode(requestMode, agentProfile)
		if modeErr != nil {
			return RunResult{}, modeErr
		}
		gatedResults, approvedCalls, approvedIndexes, approvedMask, permissionFeedback, err := s.gateToolCalls(ctx, permissionSessionID, runID, step, executionMode, toolCalls, emit, compiledPolicy)
		if err != nil {
			return RunResult{}, err
		}

		feedbackByCall := make(map[string]PermissionFeedback, len(permissionFeedback))
		for i := range permissionFeedback {
			callID := strings.TrimSpace(permissionFeedback[i].CallID)
			if callID == "" {
				continue
			}
			feedbackByCall[callID] = permissionFeedback[i]
		}

		runtimeCalls := make([]tool.Call, 0, len(approvedCalls))
		runtimeTargets := make([]int, 0, len(approvedIndexes))
		for i := range approvedCalls {
			call := approvedCalls[i]
			target := approvedIndexes[i]
			feedback := feedbackByCall[strings.TrimSpace(call.CallID)]
			handled, controlResult, controlErr := s.executeControlPlaneTool(ctx, sessionID, executionMode, agentProfile, step, call, feedback.ApprovedArguments, emit)
			if !handled {
				runtimeCalls = append(runtimeCalls, call)
				runtimeTargets = append(runtimeTargets, target)
				continue
			}
			if controlErr != nil {
				controlResult.Error = strings.TrimSpace(controlErr.Error())
				if strings.TrimSpace(controlResult.Output) == "" {
					controlResult.Output = strings.TrimSpace(controlErr.Error())
				}
			}
			if strings.TrimSpace(controlResult.CallID) == "" {
				controlResult.CallID = strings.TrimSpace(call.CallID)
			}
			if strings.TrimSpace(controlResult.Name) == "" {
				controlResult.Name = strings.TrimSpace(call.Name)
			}
			if target >= 0 && target < len(gatedResults) {
				gatedResults[target] = controlResult
			}
			emit(StreamEvent{
				Type:       StreamEventToolCompleted,
				Step:       step,
				ToolName:   strings.TrimSpace(controlResult.Name),
				CallID:     strings.TrimSpace(controlResult.CallID),
				Output:     formatToolCompletedOutput(call, controlResult),
				RawOutput:  liveStreamRawOutput(call, controlResult),
				Error:      strings.TrimSpace(controlResult.Error),
				DurationMS: controlResult.DurationMS,
			})
			markToolCompleted(step, call, controlResult)
		}

		scopeResults, scopeApprovedCalls, scopeApprovedIndexes, scopeChanged, err := s.gateWorkspaceScopeCalls(
			ctx,
			sessionID,
			permissionSessionID,
			runID,
			step,
			executionMode,
			sandboxCtx.OriginWorkspacePath,
			sessionSnapshot.WorkspaceName,
			&sandboxCtx,
			runtimeCalls,
			emit,
		)
		if err != nil {
			return RunResult{}, err
		}
		scopeApprovedMask := make([]bool, len(runtimeCalls))
		finalTargets := make([]int, 0, len(scopeApprovedIndexes))
		for _, idx := range scopeApprovedIndexes {
			if idx < 0 || idx >= len(runtimeCalls) {
				continue
			}
			scopeApprovedMask[idx] = true
			if idx < len(runtimeTargets) {
				finalTargets = append(finalTargets, runtimeTargets[idx])
			}
		}
		for i, result := range scopeResults {
			if i < 0 || i >= len(runtimeTargets) || scopeApprovedMask[i] {
				continue
			}
			target := runtimeTargets[i]
			if target >= 0 && target < len(gatedResults) {
				gatedResults[target] = result
			}
			emit(StreamEvent{
				Type:       StreamEventToolCompleted,
				Step:       step,
				ToolName:   strings.TrimSpace(result.Name),
				CallID:     strings.TrimSpace(result.CallID),
				Output:     formatToolCompletedOutput(runtimeCalls[i], result),
				RawOutput:  liveStreamRawOutput(runtimeCalls[i], result),
				Error:      strings.TrimSpace(result.Error),
				DurationMS: result.DurationMS,
			})
			markToolCompleted(step, runtimeCalls[i], result)
		}
		if scopeChanged {
			baseInstructions = s.composeInstructionsForScope(tool.WorkspaceScope{
				PrimaryPath: sandboxCtx.WorkspacePath,
				Roots:       append([]string(nil), sandboxCtx.WorkspaceRoots...),
			}, agentProfile, options.Instructions)
			baseInstructions = appendSandboxRuntimeContext(baseInstructions, sandboxCtx.Enabled, sandboxCtx.WorkspacePath, sandboxCtx.WorkspaceRoots)
		}

		runtimeCtx := tool.WithBashSandbox(ctx, tool.BashSandboxConfig{
			Enabled: sandboxCtx.Enabled,
			RunID:   runID,
		})
		runtimeCtx = tool.WithWorkspaceScope(runtimeCtx, tool.WorkspaceScope{
			PrimaryPath: sandboxCtx.WorkspacePath,
			Roots:       append([]string(nil), sandboxCtx.WorkspaceRoots...),
		})
		executedResults := s.tools.ExecuteBatchStreamingWithProgress(runtimeCtx, sandboxCtx.WorkspacePath, scopeApprovedCalls, func(_ int, call tool.Call, progress tool.Progress) {
			if strings.ToLower(strings.TrimSpace(progress.Stage)) != "output" {
				return
			}
			delta := progress.Output
			if delta == "" {
				return
			}
			emit(StreamEvent{
				Type:     StreamEventToolDelta,
				Step:     step,
				ToolName: strings.TrimSpace(call.Name),
				CallID:   strings.TrimSpace(call.CallID),
				Output:   truncateRunes(delta, maxToolDeltaChars),
			})
		}, func(index int, call tool.Call, result tool.Result) {
			if strings.TrimSpace(result.CallID) == "" {
				result.CallID = strings.TrimSpace(call.CallID)
			}
			if strings.TrimSpace(result.Name) == "" {
				result.Name = strings.TrimSpace(call.Name)
			}
			markToolCompleted(step, call, result)
			emit(StreamEvent{
				Type:       StreamEventToolCompleted,
				Step:       step,
				ToolName:   strings.TrimSpace(result.Name),
				CallID:     strings.TrimSpace(result.CallID),
				Output:     formatToolCompletedOutput(call, result),
				RawOutput:  liveStreamRawOutput(call, result),
				Error:      strings.TrimSpace(result.Error),
				DurationMS: result.DurationMS,
			})
		})
		for i, result := range executedResults {
			if i < 0 || i >= len(finalTargets) {
				continue
			}
			target := finalTargets[i]
			if target >= 0 && target < len(gatedResults) {
				gatedResults[target] = result
			}
		}
		for i := range toolCalls {
			if approvedMask[i] {
				continue
			}
			result := gatedResults[i]
			emit(StreamEvent{
				Type:       StreamEventToolCompleted,
				Step:       step,
				ToolName:   strings.TrimSpace(toolCalls[i].Name),
				CallID:     strings.TrimSpace(toolCalls[i].CallID),
				Output:     formatToolCompletedOutput(toolCalls[i], result),
				RawOutput:  liveStreamRawOutput(toolCalls[i], result),
				Error:      strings.TrimSpace(result.Error),
				DurationMS: result.DurationMS,
			})
			markToolCompleted(step, toolCalls[i], result)
		}

		nextInput := make([]map[string]any, 0, len(toolCalls)*2+1)
		if flushedAssistantInput != nil {
			nextInput = append(nextInput, flushedAssistantInput)
		}
		nextInputFunctionCalls := make([]map[string]any, 0, len(toolCalls))
		nextInputFunctionOutputs := make([]map[string]any, 0, len(toolCalls))
		for i := range toolCalls {
			call := toolCalls[i]
			result := gatedResults[i]

			if strings.TrimSpace(result.CallID) == "" {
				result.CallID = call.CallID
			}
			nextCallInput := map[string]any{
				"type":      "function_call",
				"call_id":   call.CallID,
				"name":      call.Name,
				"arguments": call.Arguments,
			}
			if metadata := cloneGenericMap(toolCallMetadata[i]); len(metadata) > 0 {
				nextCallInput["metadata"] = metadata
			}
			nextInputFunctionCalls = append(nextInputFunctionCalls, nextCallInput)
			nextInputFunctionOutputs = append(nextInputFunctionOutputs, map[string]any{
				"type":    "function_call_output",
				"call_id": call.CallID,
				"output":  prepareToolOutputForModel(call, result),
			})

			toolHistoryText := formatToolHistoryWithMetadata(call, toolCallMetadata[i], result)
			storedToolMessage, _, event, appendErr := s.sessions.AppendMessage(sessionID, "tool", toolHistoryText, nil)
			if appendErr != nil {
				return RunResult{}, appendErr
			}
			toolMessages = append(toolMessages, storedToolMessage)
			if event != nil {
				events = append(events, *event)
			}
			if sessionSnapshot, ok, sessionErr := s.sessions.GetSession(sessionID); sessionErr == nil && ok {
				if commitMeta, detected := detectGitCommit(call, result); detected {
					metadata := sessionGitMetadata(sessionSnapshot.Metadata)
					gitMeta, _ := metadata["git"].(map[string]any)
					if gitMeta != nil {
						gitMeta["commit_detected"] = true
						gitMeta["commit_count"] = sessionGitCommitCount(metadata) + 1
						gitMeta["last_commit"] = commitMeta
						gitMeta["last_commit_at"] = storedToolMessage.CreatedAt
						if updatedSession, env, updateErr := s.sessions.UpdateMetadata(sessionID, metadata); updateErr == nil {
							sessionSnapshot = updatedSession
							if env != nil {
								events = append(events, *env)
								s.publishEventEnvelope(*env)
							}
						}
					}
				}
				s.maybeRefreshSessionGitState(sessionID, sessionSnapshot)
			}
			emit(StreamEvent{Type: StreamEventMessageStored, Step: step, Message: &storedToolMessage})
		}
		nextInput = append(nextInput, nextInputFunctionCalls...)
		nextInput = append(nextInput, nextInputFunctionOutputs...)
		if feedbackInput := buildPermissionFeedbackInput(permissionFeedback); feedbackInput != "" {
			runPermissionDebugf("run_turn.feedback_append session=%s run=%s step=%d payload_chars=%d", sessionID, runID, step, len(feedbackInput))
			nextInput = append(nextInput, map[string]any{
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": feedbackInput},
				},
			})
		}

		input = append(input, nextInput...)
	}

	assistantMessage, flushedFinalAssistant, err := flushAssistantFragments(stepsCompleted)
	if err != nil {
		return RunResult{}, err
	}
	if !flushedFinalAssistant {
		assistantText := "No assistant text output."
		var assistantEvent *pebblestore.EventEnvelope
		assistantMessage, _, assistantEvent, err = s.sessions.AppendMessage(sessionID, "assistant", assistantText, nil)
		if err != nil {
			return RunResult{}, err
		}
		if assistantEvent != nil {
			events = append(events, *assistantEvent)
		}
		emit(StreamEvent{Type: StreamEventMessageStored, Step: stepsCompleted, Message: &assistantMessage})
	}
	if completedSnapshot, changed, lifecycleErr := s.finishSessionLifecycle(sessionID, runID, nil); lifecycleErr == nil && changed {
		emitLifecycleSnapshot(emit, completedSnapshot)
	}
	s.emitSessionStatus(emit, sessionID, runID, "idle", "", "", "")
	emit(StreamEvent{Type: StreamEventTurnCompleted, Step: stepsCompleted, Summary: reasoningSummary})
	runFailed = false

	return RunResult{
		SessionID:        sessionID,
		Agent:            activeAgent,
		Model:            resolvedPreference.Preference.Model,
		Thinking:         resolvedPreference.Preference.Thinking,
		ReasoningSummary: reasoningSummary,
		Steps:            stepsCompleted,
		ToolCallCount:    totalToolCalls,
		TurnUsage:        turnUsageRecord,
		UsageSummary:     usageSummaryState,
		UserMessage:      userMessage,
		ToolMessages:     toolMessages,
		Commentary:       commentaryMessages,
		AssistantMessage: assistantMessage,
		Events:           events,
		Background:       options.Background,
		TargetKind:       targetKind,
		TargetName:       targetName,
	}, nil
}

func (s *Service) persistRunFailure(sessionID string, runErr error) {
	if s == nil || s.sessions == nil || runErr == nil {
		return
	}
	if errors.Is(runErr, context.Canceled) {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	content := formatRunFailureMessage(runErr)
	if content == "" {
		return
	}
	_, _, _, _ = s.sessions.AppendMessage(sessionID, "system", content, nil)
}

func formatRunFailureMessage(runErr error) string {
	if runErr == nil {
		return ""
	}
	detail := strings.TrimSpace(runErr.Error())
	if detail == "" {
		detail = "unknown run error"
	}
	return fmt.Sprintf("Run failed [%s]: %s", runFailurePathID, detail)
}

func emptyProviderStepError(providerID string, step int, retries int, response provideriface.Response) error {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		providerID = "unknown"
	}
	base := fmt.Sprintf(
		"provider returned empty step (no text, no tool calls, no reasoning) after %d retries (provider=%s step=%d)",
		retries,
		providerID,
		step,
	)
	if detail := summarizeProviderResponseDiagnostics(response); detail != "" {
		return fmt.Errorf("%s; last provider response: %s", base, detail)
	}
	return errors.New(base)
}

func summarizeProviderResponseDiagnostics(response provideriface.Response) string {
	parts := make([]string, 0, 4)
	if responseID := strings.TrimSpace(response.ID); responseID != "" {
		parts = append(parts, "id="+responseID)
	}
	if modelID := strings.TrimSpace(response.Model); modelID != "" {
		parts = append(parts, "model="+modelID)
	}
	if stopReason := normalizeProviderDiagnostic(response.StopReason); stopReason != "" {
		parts = append(parts, fmt.Sprintf("stop_reason=%q", stopReason))
	}
	if usageSource := strings.TrimSpace(response.Usage.Source); usageSource != "" {
		parts = append(parts, "usage_source="+usageSource)
	}
	return strings.Join(parts, ", ")
}

func normalizeProviderDiagnostic(value string) string {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	return normalized
}

func emptyStepRetryDelay(retry int) time.Duration {
	if retry <= 0 {
		return 0
	}
	delay := emptyStepRetryBase
	for attempt := 1; attempt < retry; attempt++ {
		delay *= 2
		if delay >= emptyStepRetryMax {
			return emptyStepRetryMax
		}
	}
	if delay > emptyStepRetryMax {
		return emptyStepRetryMax
	}
	return delay
}

func waitForRetryDelay(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func shouldTriggerContextCompaction(response provideriface.Response) bool {
	return isContextOverflowDiagnostic(response.StopReason)
}

func isContextOverflowDiagnostic(detail string) bool {
	normalized := strings.ToLower(strings.TrimSpace(detail))
	if normalized == "" {
		return false
	}
	switch {
	case strings.Contains(normalized, "context_length_exceeded"):
		return true
	case strings.Contains(normalized, "context window exceeded"):
		return true
	case strings.Contains(normalized, "context length exceeded"):
		return true
	case strings.Contains(normalized, "input exceeds the context window"):
		return true
	case strings.Contains(normalized, "maximum context length"):
		return true
	case strings.Contains(normalized, "token limit exceeded"):
		return true
	default:
		return false
	}
}

func emitMemoryCompactionStatus(emit StreamHandler, step int, summary string) {
	summary = strings.TrimSpace(summary)
	if emit == nil || summary == "" {
		return
	}
	emit(StreamEvent{Type: StreamEventAssistantCommentary, Step: step, Delta: summary})
}

func trimMessagesToLatestCompactionCheckpoint(messages []pebblestore.MessageSnapshot) []pebblestore.MessageSnapshot {
	latest := -1
	for i := range messages {
		if strings.ToLower(strings.TrimSpace(messages[i].Role)) != "system" {
			continue
		}
		if !isCompactionCheckpointMessage(messages[i].Content) {
			continue
		}
		latest = i
	}
	if latest < 0 {
		return messages
	}
	return append([]pebblestore.MessageSnapshot(nil), messages[latest:]...)
}

func isCompactionCheckpointMessage(content string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}
	return strings.HasPrefix(content, contextCompactionMarkerPrefix)
}

func (s *Service) applyContextCompactionArtifacts(sessionID, compactSummary, origin string, contextWindow int, providerID, modelName string, step int, emit StreamHandler) (*pebblestore.SessionUsageSummary, int, []pebblestore.EventEnvelope, error) {
	if s == nil || s.sessions == nil {
		return nil, 0, nil, errors.New("run service is not fully configured")
	}
	var activePlan *pebblestore.SessionPlanSnapshot
	if plan, ok, err := s.sessions.GetActivePlan(sessionID); err != nil {
		return nil, 0, nil, err
	} else if ok {
		activePlan = &plan
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return nil, 0, nil, err
	}
	if !ok {
		return nil, 0, nil, fmt.Errorf("session %q not found", sessionID)
	}

	nextTitle, compactIndex := nextCompactSessionTitle(session.Title)
	checkpoint := buildCompactionCheckpointMessage(compactSummary, origin, compactIndex, compactedActivePlanLabel(activePlan))
	checkpointMetadata := compactedContextCheckpointMetadata(activePlan)
	checkpointMessage, _, checkpointEvent, err := s.sessions.AppendMessage(sessionID, "system", checkpoint, checkpointMetadata)
	if err != nil {
		return nil, 0, nil, err
	}
	events := make([]pebblestore.EventEnvelope, 0, 3)
	if checkpointEvent != nil {
		events = append(events, *checkpointEvent)
	}
	if emit != nil {
		emit(StreamEvent{Type: StreamEventMessageStored, Step: step, Message: &checkpointMessage})
	}

	updatedSession, titleEvent, err := s.sessions.SetTitle(sessionID, nextTitle)
	if err != nil {
		return nil, 0, events, err
	}
	if titleEvent != nil {
		events = append(events, *titleEvent)
	}
	finalTitle := strings.TrimSpace(updatedSession.Title)
	if finalTitle == "" {
		finalTitle = nextTitle
	}
	if emit != nil {
		emit(StreamEvent{
			Type:       StreamEventSessionTitle,
			Step:       step,
			SessionID:  sessionID,
			Title:      finalTitle,
			TitleStage: "compact",
		})
	}

	if contextWindow <= 0 {
		if usageState, hasUsage, usageErr := s.sessions.GetUsageSummary(sessionID); usageErr == nil && hasUsage && usageState.ContextWindow > 0 {
			contextWindow = usageState.ContextWindow
		}
	}
	if contextWindow < 0 {
		contextWindow = 0
	}

	resetSummary, usageEvent, err := s.sessions.ResetUsage(sessionID, contextWindow, providerID, modelName, contextCompactionUsageSource)
	if err != nil {
		return nil, 0, events, err
	}
	if usageEvent != nil {
		events = append(events, *usageEvent)
	}
	resetSummaryCopy := resetSummary
	if emit != nil {
		emit(StreamEvent{
			Type:         StreamEventUsageUpdated,
			Step:         step,
			UsageSummary: &resetSummaryCopy,
		})
	}

	emitMemoryCompactionStatus(emit, step, fmt.Sprintf("context checkpoint saved (%s #%d); usage counters reset", "Compact", compactIndex))
	return &resetSummaryCopy, compactIndex, events, nil
}

func nextCompactSessionTitle(current string) (string, int) {
	current = strings.TrimSpace(current)
	if current == "" {
		current = sessionTitleDefault
	}
	baseTitle := current
	nextIndex := 2
	if match := sessionCompactTitleSuffixPattern.FindStringSubmatch(current); len(match) == 2 {
		if parsed, err := strconv.Atoi(strings.TrimSpace(match[1])); err == nil && parsed > 0 {
			nextIndex = parsed + 1
		}
		baseTitle = strings.TrimSpace(current[:len(current)-len(match[0])])
		if baseTitle == "" {
			baseTitle = sessionTitleDefault
		}
	}
	return fmt.Sprintf("%s (Compact #%d)", baseTitle, nextIndex), nextIndex
}

func buildCompactionCheckpointMessage(compactSummary, origin string, compactIndex int, attachedPlanLabel string) string {
	compactSummary = strings.TrimSpace(compactSummary)
	if compactSummary == "" {
		compactSummary = "(empty compact summary)"
	}
	origin = strings.ToLower(strings.TrimSpace(origin))
	if origin == "" {
		origin = "unknown"
	}
	if compactIndex <= 0 {
		compactIndex = 2
	}
	lines := []string{
		fmt.Sprintf("%s index=%d origin=%s", contextCompactionMarkerPrefix, compactIndex, origin),
		"This checkpoint supersedes earlier transcript context for future model turns.",
		"Compacted recap:",
		compactSummary,
	}
	if attachedPlanLabel = strings.TrimSpace(attachedPlanLabel); attachedPlanLabel != "" {
		lines = append(lines, "Attached plan: "+attachedPlanLabel)
	}
	return strings.TrimSpace(strings.Join(lines, "\n\n"))
}

func buildManualCompactionAssistantText(compactSummary string, compactIndex int, attachedPlanLabel string) string {
	compactSummary = strings.TrimSpace(compactSummary)
	if compactSummary == "" {
		compactSummary = "(empty compact summary)"
	}
	if compactIndex <= 0 {
		compactIndex = 2
	}
	lines := []string{
		fmt.Sprintf("Manual context compact complete (Compact #%d).", compactIndex),
		"Compacted recap:",
		compactSummary,
	}
	if attachedPlanLabel = strings.TrimSpace(attachedPlanLabel); attachedPlanLabel != "" {
		lines = append(lines, "Attached plan: "+attachedPlanLabel)
	}
	return strings.TrimSpace(strings.Join(lines, "\n\n"))
}

func compactedContextCheckpointMetadata(activePlan *pebblestore.SessionPlanSnapshot) map[string]any {
	if activePlan == nil {
		return nil
	}
	planText := compactedActivePlanText(activePlan)
	if planText == "" {
		return nil
	}
	metadata := map[string]any{
		contextCompactionPlanTextMetadataKey: planText,
	}
	if label := compactedActivePlanLabel(activePlan); label != "" {
		metadata[contextCompactionPlanLabelMetadataKey] = label
	}
	return metadata
}

func compactedActivePlanLabel(activePlan *pebblestore.SessionPlanSnapshot) string {
	if activePlan == nil {
		return ""
	}
	title := strings.TrimSpace(activePlan.Title)
	id := strings.TrimSpace(activePlan.ID)
	switch {
	case title != "" && id != "":
		return fmt.Sprintf("%s (%s)", title, id)
	case title != "":
		return title
	default:
		return id
	}
}

func (s *Service) compactRunContextWithMemory(ctx context.Context, sessionID, runPrompt, assistantDraft string, basePreference pebblestore.ModelPreference, contextWindow, maxOutputTokens int, returnFullCompactionResponse bool, step, attempt int, emit StreamHandler) (string, error) {
	if s == nil || s.providers == nil || s.sessions == nil {
		return "", errors.New("run service is not fully configured")
	}
	memoryProfile, err := s.resolveTaskSubagent("memory")
	if err != nil {
		return "", fmt.Errorf("resolve memory subagent: %w", err)
	}
	preference := applyAgentPreferenceOverrides(basePreference, memoryProfile)
	providerID := strings.ToLower(strings.TrimSpace(preference.Provider))
	if providerID == "" {
		return "", errors.New("resolved memory compact provider is empty")
	}
	runner, ok := s.providers.GetRunner(providerID)
	if !ok {
		return "", fmt.Errorf("memory compact provider %q is not runnable", providerID)
	}
	modelName := strings.TrimSpace(preference.Model)
	if modelName == "" {
		return "", errors.New("resolved memory compact model is empty")
	}
	thinking := normalizeThinkingWithProvider(providerID, preference.Thinking)
	contextWindow, maxOutputTokens = s.resolveMemoryCompactionLimits(providerID, modelName, preference.ContextMode, contextWindow, maxOutputTokens)
	messages, err := s.listMessagesForMemoryCompaction(sessionID)
	if err != nil {
		return "", fmt.Errorf("list session messages for compaction: %w", err)
	}
	transcript := buildMemoryCompactionTranscript(messages, assistantDraft)
	if strings.TrimSpace(transcript) == "" {
		return "", errors.New("memory compaction transcript is empty")
	}
	summaryMaxRunes := memoryCompactionSummaryMaxRunes
	if returnFullCompactionResponse {
		summaryMaxRunes = 0
	}
	instructions := buildMemoryCompactionInstructions(memoryProfile.Prompt, summaryMaxRunes)
	transcriptRunes := len([]rune(transcript))
	inputBudgetTokens := effectiveMemoryCompactionInputBudget(contextWindow, maxOutputTokens, summaryMaxRunes)
	oneShotPrompt := buildMemoryCompactionPrompt(runPrompt, "", transcript, 1, 1)
	oneShotTokens := estimateMemoryCompactionTokens(instructions, oneShotPrompt)
	runCompactionDebugEvent("memory_compaction_start", map[string]any{
		"session_id":                strings.TrimSpace(sessionID),
		"provider":                  providerID,
		"model":                     modelName,
		"thinking":                  thinking,
		"context_window":            contextWindow,
		"max_output_tokens":         maxOutputTokens,
		"effective_input_budget":    inputBudgetTokens,
		"transcript_runes":          transcriptRunes,
		"estimated_one_shot_tokens": oneShotTokens,
		"attempt":                   attempt,
	})

	if inputBudgetTokens > 0 && oneShotTokens > 0 && !shouldAttemptOneShotMemoryCompaction(inputBudgetTokens, oneShotTokens) {
		emitMemoryCompactionStatus(emit, step, "full chat is too large for one-shot compaction; using chunked compaction")
	}
	if inputBudgetTokens > 0 && oneShotTokens > 0 && shouldAttemptOneShotMemoryCompaction(inputBudgetTokens, oneShotTokens) {
		oneShotStatus := fmt.Sprintf("compacting full chat with memory agent (one shot, attempt %d)", attempt)
		emitMemoryCompactionStatus(emit, step, oneShotStatus)
		oneShotResult, reqErr := executeMemoryCompactionRequest(ctx, runner, modelName, thinking, preference.ContextMode, instructions, oneShotPrompt, contextWindow, summaryMaxRunes, func(message string) {
			emitMemoryCompactionStatus(emit, step, oneShotStatus+"; "+strings.TrimSpace(message))
		})
		if reqErr == nil {
			runCompactionDebugEvent("memory_compaction_one_shot_success", map[string]any{
				"session_id": strings.TrimSpace(sessionID),
				"provider":   providerID,
				"model":      modelName,
				"attempt":    attempt,
			})
			emitMemoryCompactionStatus(emit, step, "context compacted by memory agent; resuming run")
			return oneShotResult.trimmedSummary(), nil
		}
		if isMemoryCompactionEmptySummaryError(reqErr) {
			runCompactionDebugEvent("memory_compaction_one_shot_empty_retry", map[string]any{
				"session_id": strings.TrimSpace(sessionID),
				"provider":   providerID,
				"model":      modelName,
				"attempt":    attempt,
				"error":      strings.TrimSpace(reqErr.Error()),
				"detail":     oneShotResult.diagnosticDetail(),
			})
			emitMemoryCompactionStatus(emit, step, "one-shot compaction returned no usable summary; retrying with chunked fallback")
		} else if !oneShotResult.indicatesOverflow() && !isContextOverflowDiagnostic(reqErr.Error()) {
			runCompactionDebugEvent("memory_compaction_one_shot_failed", map[string]any{
				"session_id": strings.TrimSpace(sessionID),
				"provider":   providerID,
				"model":      modelName,
				"attempt":    attempt,
				"error":      strings.TrimSpace(reqErr.Error()),
				"detail":     oneShotResult.diagnosticDetail(),
			})
			return "", fmt.Errorf("memory compaction one-shot failed: %w", reqErr)
		} else {
			runCompactionDebugEvent("memory_compaction_one_shot_overflow", map[string]any{
				"session_id": strings.TrimSpace(sessionID),
				"provider":   providerID,
				"model":      modelName,
				"attempt":    attempt,
				"error":      strings.TrimSpace(reqErr.Error()),
				"detail":     oneShotResult.diagnosticDetail(),
			})
			emitMemoryCompactionStatus(emit, step, "one-shot compaction overflowed; retrying with chunked fallback")
		}
	} else if inputBudgetTokens > 0 && oneShotTokens > 0 {
		runCompactionDebugEvent("memory_compaction_one_shot_skipped", map[string]any{
			"session_id":                strings.TrimSpace(sessionID),
			"provider":                  providerID,
			"model":                     modelName,
			"attempt":                   attempt,
			"effective_input_budget":    inputBudgetTokens,
			"estimated_one_shot_tokens": oneShotTokens,
		})
		emitMemoryCompactionStatus(emit, step, "transcript too large for one-shot compaction; using chunked fallback")
	}
	chunkRunes := deriveMemoryCompactionChunkRunes(runPrompt, instructions, inputBudgetTokens)
	if chunkRunes <= 0 {
		chunkRunes = memoryCompactionFallbackChunkRunes
	}
	chunkAttemptsUsed := 0
	var lastErr error
	for chunkAttempt := 1; chunkAttempt <= memoryCompactionChunkRetryLimit; chunkAttempt++ {
		chunkAttemptsUsed = chunkAttempt
		overlapRunes := deriveMemoryCompactionOverlapRunes(chunkRunes)
		chunks := splitCompactionTranscript(transcript, chunkRunes, overlapRunes)
		if len(chunks) == 0 {
			return "", errors.New("memory compaction transcript is empty")
		}
		runCompactionDebugEvent("memory_compaction_chunk_plan", map[string]any{
			"session_id":             strings.TrimSpace(sessionID),
			"provider":               providerID,
			"model":                  modelName,
			"attempt":                attempt,
			"chunk_attempt":          chunkAttempt,
			"effective_input_budget": inputBudgetTokens,
			"chunk_runes":            chunkRunes,
			"overlap_runes":          overlapRunes,
			"chunk_count":            len(chunks),
		})
		rollingSummary := ""
		overflowRetry := false
		for i := range chunks {
			chunkStatus := fmt.Sprintf("compacting full chat with memory agent (%d/%d, attempt %d)", i+1, len(chunks), attempt)
			emitMemoryCompactionStatus(emit, step, chunkStatus)
			promptText := buildMemoryCompactionPrompt(runPrompt, rollingSummary, chunks[i], i+1, len(chunks))
			chunkResult, reqErr := executeMemoryCompactionRequest(ctx, runner, modelName, thinking, preference.ContextMode, instructions, promptText, contextWindow, summaryMaxRunes, func(message string) {
				emitMemoryCompactionStatus(emit, step, chunkStatus+"; "+strings.TrimSpace(message))
			})
			if reqErr != nil {
				if chunkResult.indicatesOverflow() || isContextOverflowDiagnostic(reqErr.Error()) {
					overflowRetry = true
					lastErr = fmt.Errorf("memory compaction chunk %d/%d overflowed: %w", i+1, len(chunks), reqErr)
					break
				}
				return "", fmt.Errorf("memory compaction chunk %d/%d failed: %w", i+1, len(chunks), reqErr)
			}
			rollingSummary = chunkResult.trimmedSummary()
		}
		if !overflowRetry {
			runCompactionDebugEvent("memory_compaction_chunk_success", map[string]any{
				"session_id":    strings.TrimSpace(sessionID),
				"provider":      providerID,
				"model":         modelName,
				"attempt":       attempt,
				"chunk_attempt": chunkAttempt,
				"chunk_runes":   chunkRunes,
				"overlap_runes": overlapRunes,
				"chunk_count":   len(chunks),
			})
			emitMemoryCompactionStatus(emit, step, "context compacted by memory agent; resuming run")
			return rollingSummary, nil
		}
		nextChunkRunes := nextMemoryCompactionChunkRunes(transcript, chunkRunes)
		runCompactionDebugEvent("memory_compaction_chunk_overflow_retry", map[string]any{
			"session_id":       strings.TrimSpace(sessionID),
			"provider":         providerID,
			"model":            modelName,
			"attempt":          attempt,
			"chunk_attempt":    chunkAttempt,
			"chunk_runes":      chunkRunes,
			"next_chunk_runes": nextChunkRunes,
			"last_error":       strings.TrimSpace(lastErr.Error()),
		})
		if nextChunkRunes >= chunkRunes {
			break
		}
		chunkRunes = nextChunkRunes
		emitMemoryCompactionStatus(emit, step, fmt.Sprintf("compaction overflow retry; shrinking chunk size and retrying (%d chars)", chunkRunes))
	}
	if lastErr != nil {
		return "", fmt.Errorf("memory compaction transcript still exceeds %s input budget after one-shot and %d chunked attempt(s): %w", modelName, chunkAttemptsUsed, lastErr)
	}
	return "", errors.New("memory compaction failed: no usable chunk plan")
}

func (s *Service) listMessagesForMemoryCompaction(sessionID string) ([]pebblestore.MessageSnapshot, error) {
	if s == nil || s.sessions == nil {
		return nil, errors.New("session service is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	limit := session.MessageCount + memoryCompactionHistorySlack
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	messages, err := s.sessions.ListMessages(sessionID, 0, limit)
	if err != nil {
		return nil, err
	}
	return trimMessagesToLatestCompactionCheckpoint(messages), nil
}

func buildMemoryCompactionTranscript(messages []pebblestore.MessageSnapshot, assistantDraft string) string {
	entries := make([]string, 0, len(messages)+1)
	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role == "system" && isToolDBDebugMessage(content) {
			continue
		}
		if role == "" {
			role = "user"
		}
		entries = append(entries, fmt.Sprintf("[seq:%d role:%s]\n%s", message.GlobalSeq, role, content))
	}
	if draft := strings.TrimSpace(assistantDraft); draft != "" {
		entries = append(entries, "[role:assistant_draft]\n"+draft)
	}
	return strings.TrimSpace(strings.Join(entries, "\n\n"))
}

func splitCompactionTranscript(transcript string, chunkRunes, overlapRunes int) []string {
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		return nil
	}
	if chunkRunes <= 0 {
		chunkRunes = memoryCompactionFallbackChunkRunes
	}
	if chunkRunes < memoryCompactionMinimumChunkRunes {
		chunkRunes = memoryCompactionMinimumChunkRunes
	}
	if overlapRunes < 0 {
		overlapRunes = 0
	}
	if overlapRunes >= chunkRunes {
		overlapRunes = chunkRunes / 4
	}
	source := []rune(transcript)
	if len(source) == 0 {
		return nil
	}
	step := chunkRunes - overlapRunes
	if step <= 0 {
		step = chunkRunes
	}
	chunks := make([]string, 0, (len(source)+step-1)/step)
	for start := 0; start < len(source); start += step {
		end := start + chunkRunes
		if end > len(source) {
			end = len(source)
		}
		chunk := strings.TrimSpace(string(source[start:end]))
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		if end >= len(source) {
			break
		}
	}
	return chunks
}

func buildMemoryCompactionInstructions(memoryPrompt string, summaryMaxRunes int) string {
	lines := []string{
		strings.TrimSpace(memoryPrompt),
		"You are handling emergency context compaction for an active coding run.",
		"Produce a high-fidelity compact recap so execution can continue without the full transcript.",
		"Return plain text only (no markdown fences, no JSON).",
		"Required sections:",
		"1) Goal and non-negotiable constraints.",
		"2) Completed work and concrete outcomes.",
		"3) Relevant filepaths and locations (path + line/symbol when known).",
		"4) Outstanding issues, errors, and pending asks.",
		"5) Immediate next actions for the active agent.",
	}
	if summaryMaxRunes > 0 {
		lines = append(lines, fmt.Sprintf("Keep the summary under %d characters while preserving critical details.", summaryMaxRunes))
	}
	lines = append(lines, "Never invent filepaths, line numbers, commands, or outcomes.")
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func buildMemoryCompactionPrompt(runPrompt, rollingSummary, chunk string, index, total int) string {
	runPrompt = strings.TrimSpace(runPrompt)
	if runPrompt == "" {
		runPrompt = "(empty prompt)"
	}
	rollingSummary = strings.TrimSpace(rollingSummary)
	chunk = strings.TrimSpace(chunk)
	lines := []string{
		"Compaction context: the previous provider step failed with context-length overflow and no usable output.",
		"Current run user prompt:",
		runPrompt,
	}
	if total > 1 {
		lines = append(lines,
			fmt.Sprintf("Transcript chunk %d of %d:", index, total),
			chunk,
		)
	} else {
		lines = append(lines,
			"Full transcript for compaction:",
			chunk,
		)
	}
	if rollingSummary != "" {
		lines = append(lines,
			"Current rolling compact summary (update this by integrating the chunk):",
			rollingSummary,
		)
	}
	lines = append(lines, "Update and return the full compact summary now. Preserve explicit constraints, tool outcomes, and filepaths/locations.")
	return strings.TrimSpace(strings.Join(lines, "\n\n"))
}

func buildCompactedContinuationInput(runPrompt, compactSummary string, activePlan *pebblestore.SessionPlanSnapshot, origin string) []map[string]any {
	compacted := strings.TrimSpace(compactSummary)
	if compacted == "" {
		return nil
	}
	runPrompt = strings.TrimSpace(runPrompt)
	if runPrompt == "" {
		runPrompt = "(empty prompt)"
	}
	parts := []string{
		compactedContinuationLead(origin),
		"Continue the same task from this recap without restarting discovery.",
		"Original user prompt for this run:",
		runPrompt,
	}
	if activePlanText := compactedActivePlanText(activePlan); activePlanText != "" {
		parts = append(parts,
			"Active session plan (still in effect after compaction):",
			activePlanText,
		)
	}
	parts = append(parts,
		"Compacted conversation recap:",
		compacted,
	)
	text := strings.TrimSpace(strings.Join(parts, "\n\n"))
	return []map[string]any{
		{
			"role": "user",
			"content": []map[string]any{
				{"type": "input_text", "text": text},
			},
		},
	}
}

func compactedActivePlanText(activePlan *pebblestore.SessionPlanSnapshot) string {
	if activePlan == nil {
		return ""
	}
	lines := make([]string, 0, 5)
	if id := strings.TrimSpace(activePlan.ID); id != "" {
		lines = append(lines, "Plan ID: "+id)
	}
	if title := strings.TrimSpace(activePlan.Title); title != "" {
		lines = append(lines, "Title: "+title)
	}
	if status := strings.TrimSpace(activePlan.Status); status != "" {
		lines = append(lines, "Status: "+status)
	}
	if approval := strings.TrimSpace(activePlan.ApprovalState); approval != "" {
		lines = append(lines, "Approval state: "+approval)
	}
	if body := strings.TrimSpace(activePlan.Plan); body != "" {
		lines = append(lines, body)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func (s *Service) resolveMemoryCompactionLimits(providerID, modelName, contextMode string, contextWindow, maxOutputTokens int) (int, int) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	modelName = strings.TrimSpace(modelName)
	if providerID == "codex" {
		contextWindow = codexruntime.EffectiveContextWindow(modelName, contextMode, contextWindow)
	}
	if contextWindow < 0 {
		contextWindow = 0
	}
	if maxOutputTokens < 0 {
		maxOutputTokens = 0
	}
	return contextWindow, maxOutputTokens
}

func effectiveMemoryCompactionInputBudget(contextWindow, maxOutputTokens, summaryMaxRunes int) int {
	if contextWindow <= 0 {
		return 0
	}
	reserveTokens := memoryCompactionOutputReserveTokens
	if summaryMaxRunes > 0 {
		summaryReserve := estimateMemoryCompactionTokens(strings.Repeat("x", summaryMaxRunes))
		if summaryReserve > reserveTokens {
			reserveTokens = summaryReserve
		}
	}
	if maxOutputTokens > 0 && reserveTokens > maxOutputTokens {
		reserveTokens = maxOutputTokens
	}
	safetyMargin := contextWindow / 50
	if safetyMargin < memoryCompactionSafetyMarginMinTokens {
		safetyMargin = memoryCompactionSafetyMarginMinTokens
	}
	budget := contextWindow - reserveTokens - safetyMargin
	if budget < memoryCompactionMinimumChunkRunes/memoryCompactionTokenEstimateDivisor {
		return 0
	}
	return budget
}

func estimateMemoryCompactionTokens(parts ...string) int {
	totalRunes := 0
	for _, part := range parts {
		totalRunes += len([]rune(strings.TrimSpace(part)))
	}
	if totalRunes <= 0 {
		return 0
	}
	return (totalRunes + memoryCompactionTokenEstimateDivisor - 1) / memoryCompactionTokenEstimateDivisor
}

func deriveMemoryCompactionChunkRunes(runPrompt, instructions string, budgetTokens int) int {
	if budgetTokens <= 0 {
		return memoryCompactionFallbackChunkRunes
	}
	overheadTokens := estimateMemoryCompactionTokens(runPrompt, instructions, "Current rolling compact summary (update this by integrating the chunk):", "Update and return the full compact summary now. Preserve explicit constraints, tool outcomes, and filepaths/locations.")
	availableTokens := budgetTokens - overheadTokens
	if availableTokens <= 0 {
		return memoryCompactionMinimumChunkRunes
	}
	chunkRunes := availableTokens * memoryCompactionTokenEstimateDivisor
	if chunkRunes < memoryCompactionMinimumChunkRunes {
		return memoryCompactionMinimumChunkRunes
	}
	return chunkRunes
}

func deriveMemoryCompactionOverlapRunes(chunkRunes int) int {
	if chunkRunes <= 0 {
		return 0
	}
	overlap := chunkRunes / 10
	if overlap < memoryCompactionChunkOverlapMinRunes {
		overlap = memoryCompactionChunkOverlapMinRunes
	}
	if overlap > memoryCompactionChunkOverlapMaxRunes {
		overlap = memoryCompactionChunkOverlapMaxRunes
	}
	if overlap >= chunkRunes {
		overlap = chunkRunes / 4
	}
	if overlap < 0 {
		return 0
	}
	return overlap
}

func nextMemoryCompactionChunkRunes(transcript string, chunkRunes int) int {
	if chunkRunes <= memoryCompactionMinimumChunkRunes {
		return memoryCompactionMinimumChunkRunes
	}
	chunks := splitCompactionTranscript(transcript, chunkRunes, deriveMemoryCompactionOverlapRunes(chunkRunes))
	if len(chunks) == 0 {
		return memoryCompactionMinimumChunkRunes
	}
	source := []rune(strings.TrimSpace(transcript))
	if len(source) == 0 {
		return memoryCompactionMinimumChunkRunes
	}
	next := len(source) / (len(chunks) + 1)
	if len(source)%(len(chunks)+1) != 0 {
		next++
	}
	if next < memoryCompactionMinimumChunkRunes {
		next = memoryCompactionMinimumChunkRunes
	}
	if next >= chunkRunes {
		next = chunkRunes - memoryCompactionChunkOverlapMinRunes
	}
	if next < memoryCompactionMinimumChunkRunes {
		next = memoryCompactionMinimumChunkRunes
	}
	return next
}

func shouldAttemptOneShotMemoryCompaction(inputBudgetTokens, estimatedTokens int) bool {
	if inputBudgetTokens <= 0 || estimatedTokens <= 0 {
		return false
	}
	return estimatedTokens <= inputBudgetTokens
}

type memoryCompactionResult struct {
	Summary          string
	StopReason       string
	ProviderResponse string
}

func (r memoryCompactionResult) trimmedSummary() string {
	return strings.TrimSpace(r.Summary)
}

func (r memoryCompactionResult) trimmedStopReason() string {
	return strings.TrimSpace(r.StopReason)
}

func (r memoryCompactionResult) trimmedProviderResponse() string {
	return strings.TrimSpace(r.ProviderResponse)
}

func (r memoryCompactionResult) isEmpty() bool {
	return r.trimmedSummary() == ""
}

func (r memoryCompactionResult) diagnosticDetail() string {
	parts := make([]string, 0, 2)
	if detail := r.trimmedStopReason(); detail != "" {
		parts = append(parts, detail)
	}
	if detail := r.trimmedProviderResponse(); detail != "" {
		if len(parts) == 0 || !strings.EqualFold(parts[len(parts)-1], detail) {
			parts = append(parts, detail)
		}
	}
	return strings.Join(parts, "; ")
}

func (r memoryCompactionResult) indicatesOverflow() bool {
	return isContextOverflowDiagnostic(r.diagnosticDetail())
}

func isMemoryCompactionEmptySummaryError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(err.Error())), "memory compaction request returned empty summary")
}

func executeMemoryCompactionRequest(ctx context.Context, runner provideriface.Runner, modelName, thinking, contextMode, instructions, userPrompt string, contextWindow, summaryMaxRunes int, emitHeartbeat func(string)) (memoryCompactionResult, error) {
	req := provideriface.Request{
		Model:         modelName,
		Thinking:      thinking,
		Instructions:  instructions,
		ContextMode:   contextMode,
		ContextWindow: contextWindow,
		Input: []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": userPrompt},
				},
			},
		},
		ToolChoice: "none",
	}
	response, reqErr := runMemoryCompactionProviderCall(ctx, runner, req, emitHeartbeat)
	if reqErr != nil {
		return memoryCompactionResult{}, reqErr
	}
	summary := strings.TrimSpace(firstNonEmptyString(response.Text, response.ReasoningSummary))
	if summaryMaxRunes > 0 {
		summary = truncateRunes(summary, summaryMaxRunes)
	}
	result := memoryCompactionResult{
		Summary:          summary,
		StopReason:       strings.TrimSpace(response.StopReason),
		ProviderResponse: strings.TrimSpace(summarizeProviderResponseDiagnostics(response)),
	}
	if result.isEmpty() {
		detail := result.diagnosticDetail()
		if detail == "" {
			detail = "provider returned no compact summary text"
		}
		return result, fmt.Errorf("memory compaction request returned empty summary: %s", detail)
	}
	return result, nil
}

func runMemoryCompactionProviderCall(ctx context.Context, runner provideriface.Runner, req provideriface.Request, emitHeartbeat func(string)) (provideriface.Response, error) {
	if emitHeartbeat == nil {
		return runner.CreateResponse(ctx, req)
	}
	resultCh := make(chan struct {
		response provideriface.Response
		err      error
	}, 1)
	go func() {
		response, err := runner.CreateResponse(ctx, req)
		select {
		case resultCh <- struct {
			response provideriface.Response
			err      error
		}{response: response, err: err}:
		case <-ctx.Done():
		}
	}()
	if memoryCompactionHeartbeatInterval <= 0 {
		out := <-resultCh
		return out.response, out.err
	}
	ticker := time.NewTicker(memoryCompactionHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return provideriface.Response{}, ctx.Err()
		case out := <-resultCh:
			return out.response, out.err
		case <-ticker.C:
			emitHeartbeat("memory compaction still running...")
		}
	}
}

func shouldGenerateMemorySessionTitle(session pebblestore.SessionSnapshot) bool {
	if session.MessageCount > 0 {
		return false
	}
	if sessionTitleGenerationLocked(session.Metadata) {
		return false
	}
	title := strings.ToLower(strings.TrimSpace(session.Title))
	if strings.Contains(title, " subagent)") && strings.Contains(title, "(@") {
		return false
	}
	return true
}

func sessionTitleGenerationLocked(metadata map[string]any) bool {
	if len(metadata) == 0 {
		return false
	}
	if metadataBoolValue(metadata, "title_locked") {
		return true
	}
	if strings.EqualFold(metadataStringValue(metadata, "title_source"), "flow_task") {
		return true
	}
	if strings.EqualFold(metadataStringValue(metadata, "source"), "flow") ||
		strings.EqualFold(metadataStringValue(metadata, "owner_transport"), "flow_scheduler") ||
		strings.EqualFold(metadataStringValue(metadata, "lineage_kind"), "flow") {
		return true
	}
	return false
}

func metadataBoolValue(metadata map[string]any, key string) bool {
	if len(metadata) == 0 {
		return false
	}
	switch typed := metadata[key].(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func metadataStringValue(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	switch typed := metadata[key].(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func (s *Service) startMemorySessionTitleFlow(sessionID, firstPrompt string, basePreference pebblestore.ModelPreference, emit StreamHandler) {
	if s == nil || s.sessions == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	firstPrompt = truncateRunes(strings.TrimSpace(firstPrompt), sessionTitlePromptPreviewRunes)
	if firstPrompt == "" {
		firstPrompt = sessionTitleDefault
	}
	memoryProfile, err := s.resolveTaskSubagent("memory")
	if err != nil {
		s.emitSessionTitleWarning(sessionID, "provisional", err, emit)
		return
	}
	go s.generateAndApplySessionTitle(sessionID, firstPrompt, "provisional", sessionTitleProvisionalWords, sessionTitleProvisionalWords, basePreference, memoryProfile, emit)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.emitSessionTitleWarning(sessionID, "final", fmt.Errorf("session title background panic: %v", recovered), nil)
			}
		}()
		timer := time.NewTimer(sessionTitleFinalDelay)
		defer timer.Stop()
		<-timer.C
		conversation, convErr := s.buildSessionTitleConversation(sessionID, firstPrompt)
		if convErr != nil {
			s.emitSessionTitleWarning(sessionID, "final", convErr, nil)
			return
		}
		s.generateAndApplySessionTitle(sessionID, conversation, "final", sessionTitleFinalWordsMin, sessionTitleFinalWordsMax, basePreference, memoryProfile, nil)
	}()
}

func (s *Service) buildSessionTitleConversation(sessionID, fallbackPrompt string) (string, error) {
	if s == nil || s.sessions == nil {
		return "", errors.New("session service is not configured")
	}
	messages, err := s.sessions.ListMessages(sessionID, 0, sessionTitleConversationLimit)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(messages))
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		lines = append(lines, role+": "+truncateRunes(content, 240))
	}
	if len(lines) == 0 {
		return fallbackPrompt, nil
	}
	return strings.Join(lines, "\n"), nil
}

func (s *Service) generateAndApplySessionTitle(sessionID, promptContext, stage string, minWords, maxWords int, basePreference pebblestore.ModelPreference, memoryProfile pebblestore.AgentProfile, emit StreamHandler) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.emitSessionTitleWarning(sessionID, stage, fmt.Errorf("session title apply panic: %v", recovered), emit)
		}
	}()
	title, err := s.generateMemorySessionTitle(promptContext, stage, minWords, maxWords, basePreference, memoryProfile)
	if err != nil {
		s.emitSessionTitleWarning(sessionID, stage, err, emit)
		return
	}
	s.applySessionTitleUpdate(sessionID, title, stage, emit)
}

func (s *Service) generateMemorySessionTitle(promptContext, stage string, minWords, maxWords int, basePreference pebblestore.ModelPreference, memoryProfile pebblestore.AgentProfile) (string, error) {
	if s == nil || s.providers == nil {
		return "", errors.New("provider registry is not configured")
	}
	stage = strings.ToLower(strings.TrimSpace(stage))
	if minWords <= 0 {
		minWords = sessionTitleProvisionalWords
	}
	if maxWords < minWords {
		maxWords = minWords
	}
	promptContext = strings.TrimSpace(promptContext)
	if promptContext == "" {
		promptContext = sessionTitleDefault
	}

	preference := applyAgentPreferenceOverrides(basePreference, memoryProfile)
	providerID := strings.ToLower(strings.TrimSpace(preference.Provider))
	if providerID == "" {
		return "", errors.New("resolved memory title provider is empty")
	}
	runner, ok := s.providers.GetRunner(providerID)
	if !ok {
		return "", fmt.Errorf("memory title provider %q is not runnable", providerID)
	}

	modelName := strings.TrimSpace(preference.Model)
	if modelName == "" {
		return "", errors.New("resolved memory title model is empty")
	}
	thinking := normalizeThinkingWithProvider(providerID, preference.Thinking)
	stageLabel := stage
	if stageLabel == "" {
		stageLabel = "provisional"
	}

	instructions := strings.TrimSpace(strings.Join([]string{
		strings.TrimSpace(memoryProfile.Prompt),
		"You generate deterministic session titles.",
		fmt.Sprintf("Return only the title text with %d to %d words.", minWords, maxWords),
		"No markdown, no quotes, no explanations, no trailing punctuation.",
		fmt.Sprintf("Stage: %s.", stageLabel),
	}, "\n"))
	userPrompt := strings.TrimSpace("Conversation summary:\n" + truncateRunes(promptContext, sessionTitlePromptPreviewRunes))

	req := provideriface.Request{
		Model:        modelName,
		Thinking:     thinking,
		Instructions: instructions,
		Input: []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": userPrompt},
				},
			},
		},
		ToolChoice: "none",
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionTitleGenerationTimeout)
	defer cancel()
	response, err := runner.CreateResponse(ctx, req)
	if err != nil {
		return "", err
	}
	rawTitle := firstNonEmptyString(response.Text, response.ReasoningSummary)
	title := sanitizeGeneratedSessionTitle(rawTitle, minWords, maxWords)
	if title == "" {
		return "", errors.New("memory agent returned an empty/invalid title")
	}
	return title, nil
}

func sanitizeGeneratedSessionTitle(raw string, minWords, maxWords int) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	words := sessionTitleWordPattern.FindAllString(raw, -1)
	if len(words) == 0 {
		return ""
	}
	if maxWords <= 0 {
		maxWords = len(words)
	}
	if len(words) > maxWords {
		words = words[:maxWords]
	}
	if len(words) < minWords {
		return ""
	}
	return strings.Join(words, " ")
}

func (s *Service) applySessionTitleUpdate(sessionID, title, stage string, emit StreamHandler) {
	if s == nil || s.sessions == nil {
		return
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return
	}
	updated, env, err := s.sessions.SetTitle(sessionID, title)
	if err != nil {
		s.emitSessionTitleWarning(sessionID, stage, err, emit)
		return
	}
	if env != nil {
		s.publishEventEnvelope(*env)
	}
	finalTitle := strings.TrimSpace(updated.Title)
	if finalTitle == "" {
		finalTitle = title
	}
	if emit != nil {
		emit(StreamEvent{
			Type:       StreamEventSessionTitle,
			SessionID:  sessionID,
			Title:      finalTitle,
			TitleStage: strings.ToLower(strings.TrimSpace(stage)),
		})
	}
}

func (s *Service) maybeAttachWorktreeBranch(session pebblestore.SessionSnapshot, title, stage string, emit StreamHandler) {
	_ = session
	_ = title
	_ = stage
	_ = emit
}

func (s *Service) emitSessionTitleWarning(sessionID, stage string, titleErr error, emit StreamHandler) {
	if s == nil || s.sessions == nil || titleErr == nil {
		return
	}
	stage = strings.ToLower(strings.TrimSpace(stage))
	if stage == "" {
		stage = "provisional"
	}
	warning := strings.TrimSpace(titleErr.Error())
	if warning == "" {
		warning = "unknown session title failure"
	}
	warning = fmt.Sprintf("memory title (%s) fallback [%s]: %s", stage, sessionTitleWarningPathID, warning)

	if env, err := s.sessions.RecordTitleWarning(sessionID, stage, warning); err == nil && env != nil {
		s.publishEventEnvelope(*env)
	}
	if emit != nil {
		emit(StreamEvent{
			Type:       StreamEventSessionWarning,
			SessionID:  sessionID,
			TitleStage: stage,
			Warning:    warning,
		})
	}
}

func (s *Service) publishEventEnvelope(event pebblestore.EventEnvelope) {
	if s == nil || s.eventPublish == nil {
		return
	}
	s.eventPublish(event)
}

func (s *Service) mirrorHostedStreamEvent(event StreamEvent) {
	if s == nil || s.sessions == nil {
		return
	}
	sessionID := strings.TrimSpace(event.SessionID)
	if sessionID == "" {
		return
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return
	}
	if event.Message != nil {
		_, _ = s.sessions.StoreMirroredMessage(session, *event.Message)
	}
	if event.Type == StreamEventSessionLifecycle && event.Lifecycle != nil {
		_ = s.sessions.StoreMirroredLifecycle(*event.Lifecycle)
	}
	descriptor, hosted := s.sessions.HostedDescriptor(session.Metadata)
	if !hosted {
		return
	}
	eventType := streamEventEnvelopeType(event)
	if eventType == "" {
		return
	}
	payload := streamEventEnvelopePayload(event)
	if len(payload) == 0 {
		return
	}
	_, _ = s.sessions.PublishHostedEvent(context.Background(), descriptor, sessionID, eventType, payload, strings.TrimSpace(event.RunID), strings.TrimSpace(event.CallID))
}

func (s *Service) publishStreamEventEnvelope(event StreamEvent) {
	if s == nil || s.events == nil {
		return
	}
	sessionID := strings.TrimSpace(event.SessionID)
	if sessionID == "" {
		return
	}
	eventType := streamEventEnvelopeType(event)
	if eventType == "" {
		return
	}
	payload, err := json.Marshal(streamEventEnvelopePayload(event))
	if err != nil {
		return
	}
	env, err := s.events.Append("session:"+sessionID, eventType, sessionID, payload, strings.TrimSpace(event.RunID), strings.TrimSpace(event.CallID))
	if err != nil {
		return
	}
	s.publishEventEnvelope(env)
}

func streamEventEnvelopeType(event StreamEvent) string {
	switch strings.TrimSpace(event.Type) {
	case StreamEventTurnStarted:
		return "run.turn.started"
	case StreamEventTurnCompleted:
		return "run.turn.completed"
	case StreamEventTurnError:
		return "run.turn.error"
	case StreamEventSessionStatus:
		return "session.status"
	case StreamEventSessionLifecycle:
		return "session.lifecycle.updated"
	case StreamEventStepStarted:
		return "run.step.started"
	case StreamEventAssistantDelta:
		return "run.assistant.delta"
	case StreamEventAssistantCommentary:
		return "run.assistant.commentary"
	case StreamEventReasoningStarted:
		return "run.reasoning.started"
	case StreamEventReasoningDelta:
		return "run.reasoning.delta"
	case StreamEventReasoningCompleted:
		return "run.reasoning.completed"
	case StreamEventReasoningSummary:
		return "run.reasoning.summary"
	case StreamEventToolStarted:
		return "run.tool.started"
	case StreamEventToolDelta:
		return "run.tool.delta"
	case StreamEventToolCompleted:
		return "run.tool.completed"
	case StreamEventMessageStored:
		return "run.message.stored"
	case StreamEventMessageUpdated:
		return "run.message.updated"
	case StreamEventUsageUpdated:
		return "run.usage.updated"
	case StreamEventSessionTitle:
		return "run.session.title.updated"
	case StreamEventSessionWarning:
		return "run.session.warning"
	default:
		return ""
	}
}

func streamEventEnvelopePayload(event StreamEvent) map[string]any {
	payload := map[string]any{
		"type":       strings.TrimSpace(event.Type),
		"session_id": strings.TrimSpace(event.SessionID),
		"run_id":     strings.TrimSpace(event.RunID),
	}
	if agent := strings.TrimSpace(event.Agent); agent != "" {
		payload["agent"] = agent
	}
	if status := strings.TrimSpace(event.Status); status != "" {
		payload["status"] = status
	}
	if event.Lifecycle != nil {
		payload["lifecycle"] = event.Lifecycle
		payload["active"] = event.Lifecycle.Active
		payload["phase"] = strings.TrimSpace(event.Lifecycle.Phase)
		payload["started_at"] = event.Lifecycle.StartedAt
		payload["ended_at"] = event.Lifecycle.EndedAt
		payload["updated_at"] = event.Lifecycle.UpdatedAt
		payload["generation"] = event.Lifecycle.Generation
		if stopReason := strings.TrimSpace(event.Lifecycle.StopReason); stopReason != "" {
			payload["stop_reason"] = stopReason
		}
		if lifecycleError := strings.TrimSpace(event.Lifecycle.Error); lifecycleError != "" {
			payload["error"] = lifecycleError
		}
		if ownerTransport := strings.TrimSpace(event.Lifecycle.OwnerTransport); ownerTransport != "" {
			payload["owner_transport"] = ownerTransport
		}
	}
	if event.Step > 0 {
		payload["step"] = event.Step
	}
	if delta := strings.TrimSpace(event.Delta); delta != "" {
		payload["delta"] = delta
	}
	if summary := strings.TrimSpace(event.Summary); summary != "" {
		payload["summary"] = summary
	}
	if toolName := strings.TrimSpace(event.ToolName); toolName != "" {
		payload["tool_name"] = toolName
	}
	if callID := strings.TrimSpace(event.CallID); callID != "" {
		payload["call_id"] = callID
	}
	if arguments := strings.TrimSpace(event.Arguments); arguments != "" {
		payload["arguments"] = arguments
	}
	if output := strings.TrimSpace(event.Output); output != "" {
		payload["output"] = output
	}
	if rawOutput := strings.TrimSpace(event.RawOutput); rawOutput != "" {
		payload["raw_output"] = rawOutput
	}
	if errText := strings.TrimSpace(event.Error); errText != "" {
		payload["error"] = errText
	}
	if event.DurationMS > 0 {
		payload["duration_ms"] = event.DurationMS
	}
	if event.Message != nil {
		payload["message"] = event.Message
	}
	if len(event.Metadata) > 0 {
		payload["metadata"] = cloneGenericMap(event.Metadata)
	}
	if event.Permission != nil {
		payload["permission"] = event.Permission
	}
	if event.TurnUsage != nil {
		payload["turn_usage"] = event.TurnUsage
	}
	if event.UsageSummary != nil {
		payload["usage_summary"] = event.UsageSummary
	}
	if title := strings.TrimSpace(event.Title); title != "" {
		payload["title"] = title
	}
	if titleStage := strings.TrimSpace(event.TitleStage); titleStage != "" {
		payload["title_stage"] = titleStage
	}
	if warning := strings.TrimSpace(event.Warning); warning != "" {
		payload["warning"] = warning
	}
	if branch := strings.TrimSpace(event.Branch); branch != "" {
		payload["branch"] = branch
	}
	return payload
}

func runCompactionDebugEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("SWARMD_COMPACTION_DEBUG")))
	switch value {
	case "1", "true", "yes", "on", "debug":
		return true
	default:
		return false
	}
}

func runCompactionDebugf(format string, args ...any) {
	if !runCompactionDebugEnabled() {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "[swarmd.run.compaction] "+format+"\n", args...)
}

func runCompactionDebugEvent(event string, data map[string]any) {
	if !runCompactionDebugEnabled() {
		return
	}
	clean := map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"event": strings.TrimSpace(event),
		"data":  data,
	}
	encoded, err := json.Marshal(clean)
	if err != nil {
		runCompactionDebugf("event=%s encode_error=true", strings.TrimSpace(event))
		return
	}
	runCompactionDebugf("%s", string(encoded))
	logPath := strings.TrimSpace(os.Getenv("SWARMD_COMPACTION_LOG_PATH"))
	if logPath == "" {
		return
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		runCompactionDebugf("event=%s open_log_failed=%v", strings.TrimSpace(event), err)
		return
	}
	defer f.Close()
	_, _ = f.Write(append(encoded, '\n'))
}

func runRequestDebugEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("SWARMD_RUN_REQUEST_DEBUG")))
	switch value {
	case "1", "true", "yes", "on", "debug":
		return true
	default:
		return false
	}
}

func runRequestDebugf(format string, args ...any) {
	if !runRequestDebugEnabled() {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "[swarmd.run.request] "+format+"\n", args...)
}

func runRequestDebugEvent(event string, data map[string]any) {
	if !runRequestDebugEnabled() {
		return
	}
	clean := map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"event": strings.TrimSpace(event),
		"data":  privacy.SanitizeMap(data),
	}
	encoded, err := json.Marshal(clean)
	if err != nil {
		runRequestDebugf("event=%s encode_error=true", strings.TrimSpace(event))
		return
	}
	runRequestDebugf("%s", string(encoded))
}

func runRequestDebugToolDefinitions(definitions []provideriface.ToolDefinition) []map[string]any {
	if len(definitions) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		item := map[string]any{
			"name":        strings.TrimSpace(definition.Name),
			"type":        strings.TrimSpace(definition.Type),
			"description": strings.TrimSpace(definition.Description),
			"parameters":  privacy.SanitizeValue(definition.Parameters),
		}
		out = append(out, item)
	}
	return out
}

func runRequestDebugDisabledTools(disabled map[string]bool) []string {
	if len(disabled) == 0 {
		return nil
	}
	out := make([]string, 0, len(disabled))
	for rawName, rawDisabled := range disabled {
		if !rawDisabled {
			continue
		}
		name := canonicalToolName(rawName)
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}
