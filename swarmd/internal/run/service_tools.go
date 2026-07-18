package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	"swarm/packages/swarmd/internal/privacy"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/uisettings"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type taskLaunchPrepared struct {
	LaunchIndex          int
	VirtualTarget        bool
	SourceAgentName      string
	RequestedSubagent    string
	MetaPrompt           string
	AssignmentLabel      string
	SubagentProvider     string
	SubagentModel        string
	SubagentProfile      pebblestore.AgentProfile
	ChildSession         pebblestore.SessionSnapshot
	ChildMode            string
	ChildWorkspacePath   string
	ChildWorkspaceName   string
	ChildWorktreeEnabled bool
	ChildWorktreeRoot    string
	ChildWorktreeBase    string
	ChildWorktreeBranch  string
	TaskBase             *worktreeruntime.TaskBase
	LaunchStartedAtMS    int64
}

type taskLaunchOutcome struct {
	LaunchIndex        int
	VirtualTarget      bool
	RequestedSubagent  string
	ResolvedSubagent   string
	MetaPrompt         string
	AssignmentLabel    string
	SubagentProvider   string
	SubagentModel      string
	ChildSessionID     string
	ChildMode          string
	WorkspacePath      string
	WorkspaceName      string
	WorktreeEnabled    bool
	WorktreeRootPath   string
	WorktreeBaseBranch string
	WorktreeBranch     string
	BaseCommit         string
	ParentBranch       string
	HeadCommit         string
	GitStatus          string
	WorktreeClean      bool
	LaunchStartedAtMS  int64
	CurrentTool        string
	CurrentToolStarted int64
	CurrentToolMS      int64
	ElapsedMS          int64
	ToolStarted        int
	ToolCompleted      int
	ToolFailed         int
	ToolOrder          []string
	ReasoningSummary   string
	CurrentPreviewKind string
	CurrentPreviewText string
	Phase              string
	ReportChars        int
	ReportExcerpt      string
	ReportRef          *taskReportRef
	ReportTruncated    bool
	Summary            string
	Error              string
	Reason             string
}

const taskLaunchReasonMaxRunes = 512

func boundedTaskLaunchReason(reason string) string {
	reason = strings.TrimSpace(privacy.SanitizeText(reason))
	if reason == "" {
		return ""
	}
	return truncateRunes(reason, taskLaunchReasonMaxRunes)
}

func (s *Service) cancelledTaskLaunchReason(childSessionID string, runErr error) (string, bool) {
	if s == nil || s.sessions == nil || !errors.Is(runErr, context.Canceled) {
		return "", false
	}
	snapshot, ok, err := s.GetSessionLifecycle(strings.TrimSpace(childSessionID))
	if err != nil || !ok || !strings.EqualFold(strings.TrimSpace(snapshot.Phase), lifecyclePhaseCancelled) {
		return "", false
	}
	reason := boundedTaskLaunchReason(snapshot.StopReason)
	if reason == "" {
		reason = "run stopped by user"
	}
	return reason, true
}

type taskReportRef struct {
	SessionID string `json:"session_id"`
	MessageID string `json:"message_id"`
	GlobalSeq uint64 `json:"global_seq"`
	Role      string `json:"role"`
	Source    string `json:"source"`
}

func buildTaskLaunchOutcome(launch taskLaunchPrepared) taskLaunchOutcome {
	resolved := strings.TrimSpace(launch.SubagentProfile.Name)
	requested := strings.TrimSpace(launch.RequestedSubagent)
	metaPrompt := strings.TrimSpace(launch.MetaPrompt)
	outcome := taskLaunchOutcome{
		LaunchIndex:        launch.LaunchIndex,
		VirtualTarget:      launch.VirtualTarget,
		RequestedSubagent:  requested,
		ResolvedSubagent:   resolved,
		MetaPrompt:         metaPrompt,
		AssignmentLabel:    strings.TrimSpace(launch.AssignmentLabel),
		SubagentProvider:   strings.TrimSpace(launch.SubagentProvider),
		SubagentModel:      strings.TrimSpace(launch.SubagentModel),
		ChildSessionID:     strings.TrimSpace(launch.ChildSession.ID),
		ChildMode:          strings.TrimSpace(launch.ChildMode),
		WorkspacePath:      strings.TrimSpace(launch.ChildSession.WorkspacePath),
		WorkspaceName:      strings.TrimSpace(launch.ChildSession.WorkspaceName),
		WorktreeEnabled:    launch.ChildSession.WorktreeEnabled,
		WorktreeRootPath:   strings.TrimSpace(launch.ChildSession.WorktreeRootPath),
		WorktreeBaseBranch: strings.TrimSpace(launch.ChildSession.WorktreeBaseBranch),
		WorktreeBranch:     strings.TrimSpace(launch.ChildSession.WorktreeBranch),
		LaunchStartedAtMS:  launch.LaunchStartedAtMS,
		WorktreeClean:      true,
	}
	if launch.TaskBase != nil {
		outcome.BaseCommit = strings.TrimSpace(launch.TaskBase.BaseCommit)
		outcome.ParentBranch = strings.TrimSpace(launch.TaskBase.ParentBranch)
	}
	return outcome
}

func taskStreamStatusForPhase(phase string) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "spawned":
		return "pending"
	case "completed":
		return "ok"
	case "failed", "cancelled", "canceled":
		return "error"
	default:
		return "running"
	}
}

const taskStreamPreviewMaxChars = 1600

func taskLaunchProgressDurations(launch taskLaunchOutcome, terminal bool) (elapsedMS, currentToolMS int64) {
	if !terminal {
		return 0, 0
	}
	elapsedMS = maxInt64(0, launch.ElapsedMS)
	currentToolMS = maxInt64(0, launch.CurrentToolMS)
	if elapsedMS <= 0 && launch.LaunchStartedAtMS > 0 {
		elapsedMS = maxInt64(0, time.Now().UnixMilli()-launch.LaunchStartedAtMS)
	}
	if currentToolMS <= 0 && launch.CurrentToolStarted > 0 && strings.TrimSpace(launch.CurrentTool) != "" {
		currentToolMS = maxInt64(0, time.Now().UnixMilli()-launch.CurrentToolStarted)
	}
	return elapsedMS, currentToolMS
}

func normalizeTaskPreviewText(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
}

func trimTaskPreviewText(value string, max int, keepTail bool) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(normalizeTaskPreviewText(value))
	if len(runes) <= max {
		return string(runes)
	}
	if keepTail {
		if max == 1 {
			return string(runes[len(runes)-1:])
		}
		return "…" + string(runes[len(runes)-max+1:])
	}
	if max == 1 {
		return string(runes[:1])
	}
	return string(runes[:max-1]) + "…"
}

func appendTaskPreviewText(current, chunk string, max int, keepTail bool) string {
	if chunk == "" {
		return current
	}
	return trimTaskPreviewText(current+normalizeTaskPreviewText(chunk), max, keepTail)
}

func setTaskPreviewText(value string, max int, keepTail bool) string {
	if value == "" {
		return ""
	}
	return trimTaskPreviewText(value, max, keepTail)
}

func taskPreviewKindLabel(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "assistant", "reasoning", "tool":
		return strings.ToLower(strings.TrimSpace(kind))
	default:
		return ""
	}
}

func publicTaskPreview(kind, text string) (string, string) {
	label := taskPreviewKindLabel(kind)
	text = strings.TrimSpace(text)
	switch label {
	case "assistant", "reasoning":
		return label, ""
	default:
		return label, text
	}
}

func buildTaskStreamLaunchPayload(launch taskLaunchOutcome, status, phase string, terminal bool) map[string]any {
	phase = strings.TrimSpace(phase)
	if phase == "" {
		phase = strings.TrimSpace(launch.Phase)
	}
	if phase == "" {
		phase = status
	}
	elapsedMS, currentToolMS := taskLaunchProgressDurations(launch, terminal)
	previewKind, previewText := publicTaskPreview(launch.CurrentPreviewKind, launch.CurrentPreviewText)
	row := map[string]any{
		"launch_index":               launch.LaunchIndex,
		"status":                     strings.TrimSpace(status),
		"requested_subagent":         strings.TrimSpace(launch.RequestedSubagent),
		"subagent":                   strings.TrimSpace(launch.ResolvedSubagent),
		"agent_type":                 strings.TrimSpace(launch.ResolvedSubagent),
		"meta_prompt":                strings.TrimSpace(launch.MetaPrompt),
		"assignment_label":           strings.TrimSpace(launch.AssignmentLabel),
		"subagent_provider":          strings.TrimSpace(launch.SubagentProvider),
		"subagent_model":             strings.TrimSpace(launch.SubagentModel),
		"child_session_id":           strings.TrimSpace(launch.ChildSessionID),
		"child_mode":                 strings.TrimSpace(launch.ChildMode),
		"workspace_path":             strings.TrimSpace(launch.WorkspacePath),
		"workspace_name":             strings.TrimSpace(launch.WorkspaceName),
		"worktree_enabled":           launch.WorktreeEnabled,
		"worktree_root_path":         strings.TrimSpace(launch.WorktreeRootPath),
		"worktree_branch":            strings.TrimSpace(launch.WorktreeBranch),
		"parent_branch":              strings.TrimSpace(launch.ParentBranch),
		"base_commit":                strings.TrimSpace(launch.BaseCommit),
		"head_commit":                strings.TrimSpace(launch.HeadCommit),
		"worktree_clean":             launch.WorktreeClean,
		"git_status":                 strings.TrimSpace(launch.GitStatus),
		"phase":                      phase,
		"launch_started_at_ms":       launch.LaunchStartedAtMS,
		"current_tool":               strings.TrimSpace(launch.CurrentTool),
		"current_tool_started_at_ms": launch.CurrentToolStarted,
		"current_tool_ms":            currentToolMS,
		"current_preview_kind":       previewKind,
		"current_preview_text":       previewText,
		"reasoning_summary":          strings.TrimSpace(launch.ReasoningSummary),
		"elapsed_ms":                 elapsedMS,
		"tool_started":               launch.ToolStarted,
		"tool_completed":             launch.ToolCompleted,
		"tool_failed":                launch.ToolFailed,
		"tool_order":                 append([]string(nil), launch.ToolOrder...),
		"summary":                    strings.TrimSpace(launch.Summary),
		"error":                      strings.TrimSpace(launch.Error),
		"reason":                     strings.TrimSpace(launch.Reason),
		"report_chars":               launch.ReportChars,
		"report_truncated":           launch.ReportTruncated,
	}
	if launch.ReportRef != nil {
		row["report_ref"] = launch.ReportRef
		row["report_persisted"] = true
	}
	return row
}

func buildTaskStreamPayload(parentSessionID, action, description string, launchCount int, launch taskLaunchOutcome, phase, summary string) map[string]any {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = fmt.Sprintf("subagent %s running", launch.ResolvedSubagent)
	}
	if launchCount <= 0 {
		launchCount = 1
	}
	status := taskStreamStatusForPhase(phase)
	terminal := status == "ok" || status == "error"
	return map[string]any{
		"tool":              "task",
		"action":            action,
		"status":            status,
		"phase":             strings.TrimSpace(phase),
		"launch_count":      launchCount,
		"description":       description,
		"goal":              description,
		"parent_session_id": strings.TrimSpace(parentSessionID),
		"assignment_label":  strings.TrimSpace(launch.AssignmentLabel),
		"subagent_provider": strings.TrimSpace(launch.SubagentProvider),
		"subagent_model":    strings.TrimSpace(launch.SubagentModel),
		"path_id":           "tool.task.stream.v1",
		"summary":           summary,
		"details_truncated": false,
		"launches": []map[string]any{
			buildTaskStreamLaunchPayload(launch, status, phase, terminal),
		},
	}
}

const taskStreamPathIDV2 = "tool.task.stream.v2"

func taskLaunchStreamKey(launch taskLaunchOutcome) string {
	if childSessionID := strings.TrimSpace(launch.ChildSessionID); childSessionID != "" {
		return childSessionID
	}
	if launch.LaunchIndex > 0 {
		return fmt.Sprintf("launch:%d", launch.LaunchIndex)
	}
	return "launch"
}

func buildTaskStreamLaunchPatchPayload(launch taskLaunchOutcome, status, phase string, terminal bool) map[string]any {
	phase = strings.TrimSpace(phase)
	if phase == "" {
		phase = strings.TrimSpace(launch.Phase)
	}
	if phase == "" {
		phase = status
	}
	elapsedMS, currentToolMS := taskLaunchProgressDurations(launch, terminal)
	patch := map[string]any{
		"launch_index":               launch.LaunchIndex,
		"launch_key":                 taskLaunchStreamKey(launch),
		"status":                     strings.TrimSpace(status),
		"phase":                      phase,
		"requested_subagent":         strings.TrimSpace(launch.RequestedSubagent),
		"subagent":                   strings.TrimSpace(launch.ResolvedSubagent),
		"agent_type":                 strings.TrimSpace(launch.ResolvedSubagent),
		"assignment_label":           strings.TrimSpace(launch.AssignmentLabel),
		"subagent_provider":          strings.TrimSpace(launch.SubagentProvider),
		"subagent_model":             strings.TrimSpace(launch.SubagentModel),
		"child_session_id":           strings.TrimSpace(launch.ChildSessionID),
		"child_mode":                 strings.TrimSpace(launch.ChildMode),
		"workspace_path":             strings.TrimSpace(launch.WorkspacePath),
		"worktree_branch":            strings.TrimSpace(launch.WorktreeBranch),
		"parent_branch":              strings.TrimSpace(launch.ParentBranch),
		"base_commit":                strings.TrimSpace(launch.BaseCommit),
		"head_commit":                strings.TrimSpace(launch.HeadCommit),
		"worktree_clean":             launch.WorktreeClean,
		"git_status":                 strings.TrimSpace(launch.GitStatus),
		"launch_started_at_ms":       launch.LaunchStartedAtMS,
		"current_tool":               strings.TrimSpace(launch.CurrentTool),
		"current_tool_started_at_ms": launch.CurrentToolStarted,
		"current_tool_ms":            currentToolMS,
		"elapsed_ms":                 elapsedMS,
		"tool_started":               launch.ToolStarted,
		"tool_completed":             launch.ToolCompleted,
		"tool_failed":                launch.ToolFailed,
		"summary":                    strings.TrimSpace(launch.Summary),
		"error":                      strings.TrimSpace(launch.Error),
		"reason":                     strings.TrimSpace(launch.Reason),
		"report_chars":               launch.ReportChars,
		"report_truncated":           launch.ReportTruncated,
		"terminal":                   terminal,
	}
	if launch.ReportRef != nil {
		patch["report_ref"] = launch.ReportRef
		patch["report_persisted"] = true
	}
	return patch
}

func buildTaskStreamPatchPayload(parentSessionID, taskCallID, action, description string, launchCount int, launch taskLaunchOutcome, phase, summary string) map[string]any {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = fmt.Sprintf("subagent %s running", launch.ResolvedSubagent)
	}
	if launchCount <= 0 {
		launchCount = 1
	}
	status := taskStreamStatusForPhase(phase)
	if strings.TrimSpace(launch.Error) != "" {
		status = "error"
	}
	terminal := status == "ok" || status == "error"
	patch := buildTaskStreamLaunchPatchPayload(launch, status, phase, terminal)
	launchKey := taskLaunchStreamKey(launch)
	return map[string]any{
		"tool":              "task",
		"action":            action,
		"status":            status,
		"phase":             strings.TrimSpace(phase),
		"launch_count":      launchCount,
		"description":       description,
		"goal":              description,
		"parent_session_id": strings.TrimSpace(parentSessionID),
		"task_call_id":      strings.TrimSpace(taskCallID),
		"path_id":           taskStreamPathIDV2,
		"stream_version":    2,
		"event":             "launch.patch",
		"launch_index":      launch.LaunchIndex,
		"launch_key":        launchKey,
		"child_session_id":  strings.TrimSpace(launch.ChildSessionID),
		"summary":           summary,
		"details_truncated": false,
		"launch":            patch,
	}
}

func emitTaskStreamDelta(parentSessionID string, emit StreamHandler, step int, toolName, callID, action, description string, launchCount int, launch taskLaunchOutcome, phase, summary string) {
	payload := buildTaskStreamPatchPayload(parentSessionID, callID, action, description, launchCount, launch, phase, summary)
	emitTaskStreamPayload(emit, step, toolName, callID, payload)
}

func emitTaskStreamPayload(emit StreamHandler, step int, toolName, callID string, payload map[string]any) {
	if emit == nil {
		return
	}
	if strings.TrimSpace(toolName) == "" {
		toolName = "task"
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	emit(StreamEvent{
		Type:     StreamEventToolDelta,
		Step:     step,
		ToolName: strings.TrimSpace(toolName),
		CallID:   strings.TrimSpace(callID),
		Output:   string(encoded),
	})
}

func (s *Service) prepareDelegatedSubagentLaunchWithProfile(parentSession pebblestore.SessionSnapshot, sessionMode string, launch taskLaunchPrepared, description, targetedSubagentName string, trustedProfile *pebblestore.AgentProfile, sourceAgentName string, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (taskLaunchPrepared, error) {
	requestedSubagent := strings.TrimSpace(launch.RequestedSubagent)
	if requestedSubagent == "" {
		return taskLaunchPrepared{}, errors.New("task launch requires saved subagent name or purpose")
	}
	var subagentProfile pebblestore.AgentProfile
	var err error
	if trustedProfile != nil {
		subagentProfile, err = cloneTaskAgentProfile(*trustedProfile)
		launch.SourceAgentName = strings.TrimSpace(sourceAgentName)
	} else {
		subagentProfile, launch.VirtualTarget, launch.SourceAgentName, err = s.resolveTaskLaunchProfile(parentSession, requestedSubagent)
	}
	if err != nil {
		return taskLaunchPrepared{}, err
	}
	if strings.TrimSpace(subagentProfile.Name) == "" {
		return taskLaunchPrepared{}, errors.New("task resolved empty subagent")
	}

	childMode := effectiveTaskChildMode(sessionMode)
	preference := applyAgentPreferenceOverridesForMode(parentSession.Preference, subagentProfile, childMode)
	assignmentLabel := taskAssignmentLabel(launch.AssignmentLabel, launch.MetaPrompt, description, strings.TrimSpace(subagentProfile.Name))
	childTitle := assignmentLabel
	childWorkspacePath := strings.TrimSpace(parentSession.WorkspacePath)
	childWorkspaceName := strings.TrimSpace(parentSession.WorkspaceName)
	childWorktreeEnabled := parentSession.WorktreeEnabled
	childWorktreeRootPath := strings.TrimSpace(parentSession.WorktreeRootPath)
	childWorktreeBaseBranch := strings.TrimSpace(parentSession.WorktreeBaseBranch)
	childWorktreeBranch := strings.TrimSpace(parentSession.WorktreeBranch)
	childTemporaryWorkspaceRoots := append([]string(nil), parentSession.TemporaryWorkspaceRoots...)
	childWorkspaceID := ""
	childSessionID := sessionruntime.NewSessionID()
	childMetadata := map[string]any{
		"workspace_id":       worktreeruntime.WorkspaceIdentityForSession(childSessionID),
		"runtime_state":      "standby",
		"title_pending":      false,
		"title_locked":       true,
		"assignment_label":   assignmentLabel,
		"subagent_provider":  strings.TrimSpace(preference.Provider),
		"subagent_model":     strings.TrimSpace(preference.Model),
		"parent_session_id":  strings.TrimSpace(parentSession.ID),
		"parent_title":       strings.TrimSpace(parentSession.Title),
		"lineage_kind":       "delegated_subagent",
		"lineage_label":      "@" + strings.TrimSpace(subagentProfile.Name),
		"launch_source":      "task",
		"launch_index":       launch.LaunchIndex,
		"requested_subagent": requestedSubagent,
		"subagent":           strings.TrimSpace(subagentProfile.Name),
	}
	if launch.VirtualTarget {
		childWorktreeEnabled = false
		childWorktreeRootPath = ""
		childWorktreeBaseBranch = ""
		childWorktreeBranch = ""
		childTemporaryWorkspaceRoots = nil
		profileSnapshot, snapshotErr := cloneTaskAgentProfile(subagentProfile)
		if snapshotErr != nil {
			return taskLaunchPrepared{}, snapshotErr
		}
		childMetadata["parent_copy"] = true
		childMetadata["source_agent_name"] = strings.TrimSpace(launch.SourceAgentName)
		childMetadata["source_profile_mode"] = strings.TrimSpace(profileSnapshot.Mode)
		childMetadata["inherited_runtime_mode"] = pebblestore.AgentProfileRuntimeMode(profileSnapshot)
		childMetadata["agent_profile"] = profileSnapshot
		if launch.TaskBase != nil {
			childMetadata["repository_root"] = strings.TrimSpace(launch.TaskBase.RepoRoot)
			childMetadata["parent_branch"] = strings.TrimSpace(launch.TaskBase.ParentBranch)
			childMetadata["base_commit"] = strings.TrimSpace(launch.TaskBase.BaseCommit)
		}
	}
	if lineageSource := strings.TrimSpace(targetedSubagentName); lineageSource != "" {
		childMetadata["launch_source"] = "targeted_subagent"
		childMetadata["targeted_subagent"] = lineageSource
	}
	if launch.VirtualTarget {
		if s.worktrees == nil {
			return taskLaunchPrepared{}, errors.New("task failed to allocate Clone worktree: worktree service is unavailable")
		}
		if launch.TaskBase == nil {
			return taskLaunchPrepared{}, errors.New("task failed to allocate Clone worktree: parent Git state was not resolved")
		}
		allocation, allocErr := s.worktrees.AllocateTaskWorkspace(parentSession.WorkspacePath, *launch.TaskBase, childSessionID)
		if allocErr != nil {
			return taskLaunchPrepared{}, fmt.Errorf("task failed to allocate subagent worktree: %w", allocErr)
		}
		childWorkspacePath = strings.TrimSpace(allocation.WorkspacePath)
		if childWorkspacePath == "" {
			return taskLaunchPrepared{}, errors.New("task failed to allocate subagent worktree: empty workspace path")
		}
		childWorkspaceName = filepath.Base(childWorkspacePath)
		childWorktreeEnabled = true
		childWorktreeRootPath = childWorkspacePath
		childWorktreeBaseBranch = strings.TrimSpace(allocation.BaseBranch)
		childWorktreeBranch = strings.TrimSpace(allocation.BranchName)
		childWorkspaceID = strings.TrimSpace(allocation.WorkspaceID)
		childTemporaryWorkspaceRoots = nil
	}

	if childWorkspaceID != "" {
		childMetadata["workspace_id"] = childWorkspaceID
	}
	if launch.VirtualTarget {
		childMetadata["worktree_path"] = childWorktreeRootPath
		childMetadata["child_branch"] = childWorktreeBranch
		childMetadata["worktree_base_branch"] = childWorktreeBaseBranch
	}
	nowMS := time.Now().UnixMilli()
	childSession := pebblestore.SessionSnapshot{
		ID:                      childSessionID,
		UserID:                  strings.TrimSpace(parentSession.UserID),
		AccountScopeID:          strings.TrimSpace(parentSession.AccountScopeID),
		WorkspacePath:           childWorkspacePath,
		WorkspaceName:           childWorkspaceName,
		Title:                   childTitle,
		Mode:                    childMode,
		Preference:              preference,
		Metadata:                childMetadata,
		CreatedAt:               nowMS,
		UpdatedAt:               nowMS,
		WorktreeEnabled:         childWorktreeEnabled,
		WorktreeRootPath:        childWorktreeRootPath,
		WorktreeBaseBranch:      childWorktreeBaseBranch,
		WorktreeBranch:          childWorktreeBranch,
		TemporaryWorkspaceRoots: childTemporaryWorkspaceRoots,
	}
	payloadHash := "task-child-create:" + childSessionID
	applyMutation := applySessionMutation
	if applyMutation == nil {
		applyMutation = s.sessions.ApplySessionMutation
	}
	created, err := applyMutation(sessionruntime.SessionMutationInput{
		SessionID:       childSessionID,
		UserID:          strings.TrimSpace(parentSession.UserID),
		AccountScopeID:  strings.TrimSpace(parentSession.AccountScopeID),
		ClientRequestID: "task-child-create:" + childSessionID,
		IdempotencyKey:  "task-child-create:" + childSessionID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &childSession,
		NowUnixMs:       nowMS,
	})
	if err != nil {
		return taskLaunchPrepared{}, fmt.Errorf("task failed to create canonical v3 subagent session: %w", err)
	}
	if created.Session != nil {
		childSession = *created.Session
	}

	launch.RequestedSubagent = requestedSubagent
	launch.AssignmentLabel = assignmentLabel
	launch.SubagentProvider = strings.TrimSpace(preference.Provider)
	launch.SubagentModel = strings.TrimSpace(preference.Model)
	launch.SubagentProfile = subagentProfile
	launch.ChildSession = childSession
	launch.ChildMode = childMode
	launch.ChildWorkspacePath = childWorkspacePath
	launch.ChildWorkspaceName = childWorkspaceName
	launch.ChildWorktreeEnabled = childWorktreeEnabled
	launch.ChildWorktreeRoot = childWorktreeRootPath
	launch.ChildWorktreeBase = childWorktreeBaseBranch
	launch.ChildWorktreeBranch = strings.TrimSpace(childSession.WorktreeBranch)
	launch.LaunchStartedAtMS = time.Now().UnixMilli()
	return launch, nil
}

func (s *Service) prepareDelegatedSubagentLaunch(parentSession pebblestore.SessionSnapshot, sessionMode string, launch taskLaunchPrepared, description, targetedSubagentName string, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (taskLaunchPrepared, error) {
	return s.prepareDelegatedSubagentLaunchWithProfile(parentSession, sessionMode, launch, description, targetedSubagentName, nil, "", applySessionMutation)
}

func (s *Service) gateToolCalls(ctx context.Context, sessionID, runID string, step int, sessionMode string, toolCalls []tool.Call, emit StreamHandler, overlay *permission.Policy) ([]tool.Result, []tool.Call, []int, []bool, []PermissionFeedback, error) {
	results := make([]tool.Result, len(toolCalls))
	approvedCalls := make([]tool.Call, 0, len(toolCalls))
	approvedIndexes := make([]int, 0, len(toolCalls))
	approvedMask := make([]bool, len(toolCalls))
	for i := range toolCalls {
		results[i] = tool.Result{
			CallID: strings.TrimSpace(toolCalls[i].CallID),
			Name:   strings.TrimSpace(toolCalls[i].Name),
		}
	}

	if s.permissions == nil {
		for i := range toolCalls {
			if err := rejectMalformedToolCallArguments(toolCalls[i]); err != nil {
				message := fmt.Sprintf("invalid tool arguments: %v", err)
				results[i].Output = permissionOutputPayload(false, "error", message, toolCalls[i].Name, toolCalls[i].Arguments)
				results[i].Error = message
				continue
			}
			approvedMask[i] = true
			approvedCalls = append(approvedCalls, toolCalls[i])
			approvedIndexes = append(approvedIndexes, i)
		}
		return results, approvedCalls, approvedIndexes, approvedMask, nil, nil
	}

	type permissionDecision struct {
		Index             int
		Approved          bool
		Result            tool.Result
		Feedback          string
		ApprovedArguments string
		Err               error
	}

	decisions := make([]permissionDecision, len(toolCalls))
	for i := range toolCalls {
		decisions[i] = permissionDecision{
			Index:    i,
			Approved: false,
			Result:   results[i],
		}
	}

	var wg sync.WaitGroup
	for i := range toolCalls {
		permissionArguments, err := s.permissionArgumentsForCall(sessionID, sessionMode, toolCalls[i])
		if err != nil {
			message := fmt.Sprintf("invalid tool arguments: %v", err)
			decisions[i].Err = err
			decisions[i].Result.Output = permissionOutputPayload(false, "error", message, toolCalls[i].Name, toolCalls[i].Arguments)
			decisions[i].Result.Error = message
			continue
		}
		accountScopeID := ""
		if s.sessions != nil {
			if session, ok, sessionErr := s.sessions.GetSession(sessionID); sessionErr != nil {
				decisions[i].Err = sessionErr
				decisions[i].Result.Output = permissionOutputPayload(false, "error", "permission authorization failed", toolCalls[i].Name, toolCalls[i].Arguments)
				decisions[i].Result.Error = fmt.Sprintf("permission authorization failed: %v", sessionErr)
				continue
			} else if ok {
				accountScopeID = strings.TrimSpace(session.AccountScopeID)
			}
		}
		var subagentReservation *permission.SubagentReservationResult
		if canonicalToolName(toolCalls[i].Name) == "task" {
			var manifest taskLaunchManifest
			if err := json.Unmarshal([]byte(permissionArguments), &manifest); err != nil {
				decisions[i].Err = err
				decisions[i].Result.Error = "task manifest is invalid"
				continue
			}
			callID := strings.TrimSpace(toolCalls[i].CallID)
			if callID == "" {
				decisions[i].Err = errors.New("task call ID is required for durable reservation")
				decisions[i].Result.Error = decisions[i].Err.Error()
				continue
			}
			depth := 0
			if session, ok, _ := s.sessions.GetSession(sessionID); ok {
				if parentID := strings.TrimSpace(mapString(session.Metadata, "task_parent_session_id")); parentID != "" {
					depth = 2
				}
			}
			reserved, reserveErr := s.permissions.ReserveSubagentWave(permission.SubagentReservationRequest{
				SessionID: sessionID, AccountScopeID: accountScopeID, RunID: runID, CallID: callID,
				ManifestHash: manifest.ManifestHash, LaunchCount: manifest.LaunchCount, Depth: depth,
			})
			if reserveErr != nil {
				decisions[i].Err = reserveErr
				decisions[i].Result.Error = fmt.Sprintf("subagent wave reservation failed: %v", reserveErr)
				continue
			}
			subagentReservation = &reserved
		}
		auth, err := s.permissions.AuthorizeToolCall(permission.AuthorizationInput{
			SessionID:           sessionID,
			AccountScopeID:      accountScopeID,
			RunID:               runID,
			Step:                step,
			CallID:              toolCalls[i].CallID,
			ToolName:            toolCalls[i].Name,
			ToolArguments:       permissionArguments,
			ToolCallArguments:   strings.TrimSpace(toolCalls[i].Arguments),
			Mode:                sessionMode,
			Overlay:             overlay,
			SubagentReservation: subagentReservation,
		})
		if err != nil {
			decisions[i].Err = err
			decisions[i].Result.Output = permissionOutputPayload(false, "error", "permission authorization failed", toolCalls[i].Name, toolCalls[i].Arguments)
			decisions[i].Result.Error = fmt.Sprintf("permission authorization failed: %v", err)
			continue
		}

		switch auth.Decision {
		case permission.AuthorizationApprove:
			decisions[i].Approved = true
			canonical := canonicalToolName(toolCalls[i].Name)
			if canonical == "task" || (canonical == "manage_sessions" && isCanonicalManageSessionsMutation(permission.ManageSessionsAction(toolCalls[i].Arguments))) {
				var permissionPayload map[string]any
				if json.Unmarshal([]byte(permissionArguments), &permissionPayload) == nil {
					if approved, ok := permissionPayload["approved_arguments"].(map[string]any); ok {
						if raw, marshalErr := json.Marshal(approved); marshalErr == nil {
							decisions[i].ApprovedArguments = string(raw)
						}
					}
				}
			}
		case permission.AuthorizationDeny:
			status := "denied"
			if strings.EqualFold(auth.Source, "builtin") {
				status = "blocked"
			}
			reason := strings.TrimSpace(auth.Reason)
			if reason == "" {
				if status == "blocked" {
					reason = "tool blocked"
				} else {
					reason = "permission denied"
				}
			}
			decisions[i].Result.Output = permissionOutputPayload(false, status, reason, toolCalls[i].Name, toolCalls[i].Arguments)
			decisions[i].Result.Error = reason
		case permission.AuthorizationPending:
			record := auth.Record
			if record == nil {
				decisions[i].Err = errors.New("permission authorization returned no pending record")
				decisions[i].Result.Output = permissionOutputPayload(false, "error", "permission request failed", toolCalls[i].Name, toolCalls[i].Arguments)
				decisions[i].Result.Error = "permission request failed"
				continue
			}
			if emit != nil {
				emit(StreamEvent{
					Type:       StreamEventPermissionReq,
					SessionID:  sessionID,
					Step:       step,
					ToolName:   strings.TrimSpace(toolCalls[i].Name),
					CallID:     strings.TrimSpace(toolCalls[i].CallID),
					Arguments:  strings.TrimSpace(firstNonEmptyString(record.ToolCallArguments, record.ToolArguments)),
					Permission: record,
				})
			}

			wg.Add(1)
			call := toolCalls[i]
			go func(index int, call tool.Call, record pebblestore.PermissionRecord) {
				defer wg.Done()
				waitStarted := time.Now()
				resolved, waitErr := s.permissions.WaitForResolution(ctx, sessionID, record.ID)
				if waitErr != nil {
					decisions[index].Err = waitErr
					decisions[index].Result.DurationMS = time.Since(waitStarted).Milliseconds()
					decisions[index].Result.Output = permissionOutputPayload(false, "error", "permission wait failed", call.Name, call.Arguments)
					decisions[index].Result.Error = fmt.Sprintf("permission wait failed: %v", waitErr)
					return
				}
				if emit != nil {
					emit(StreamEvent{
						Type:       StreamEventPermissionUpdate,
						SessionID:  sessionID,
						Step:       step,
						ToolName:   strings.TrimSpace(call.Name),
						CallID:     strings.TrimSpace(call.CallID),
						Arguments:  strings.TrimSpace(firstNonEmptyString(resolved.ToolCallArguments, resolved.ToolArguments)),
						Permission: &resolved,
					})
				}
				decisions[index].Result.DurationMS = time.Since(waitStarted).Milliseconds()

				switch strings.ToLower(strings.TrimSpace(resolved.Status)) {
				case pebblestore.PermissionStatusApproved:
					decisions[index].Approved = true
					decisions[index].Feedback = normalizePermissionFeedback(resolved.Reason)
					decisions[index].ApprovedArguments = strings.TrimSpace(resolved.ApprovedArguments)
				case pebblestore.PermissionStatusDenied:
					decisions[index].Result.Output = permissionOutputPayload(false, "denied", resolved.Reason, call.Name, call.Arguments)
					decisions[index].Result.Error = "permission denied"
				default:
					decisions[index].Result.Output = permissionOutputPayload(false, "cancelled", resolved.Reason, call.Name, call.Arguments)
					decisions[index].Result.Error = "permission cancelled"
				}
			}(i, call, *record)
		default:
			decisions[i].Err = fmt.Errorf("unsupported authorization decision %q", auth.Decision)
			decisions[i].Result.Output = permissionOutputPayload(false, "error", "permission authorization failed", toolCalls[i].Name, toolCalls[i].Arguments)
			decisions[i].Result.Error = fmt.Sprintf("permission authorization failed: unsupported decision %q", auth.Decision)
		}
	}
	wg.Wait()

	for i := range decisions {
		if !decisions[i].Approved && canonicalToolName(toolCalls[i].Name) == "task" && strings.TrimSpace(runID) != "" && strings.TrimSpace(toolCalls[i].CallID) != "" {
			if finishErr := s.permissions.FinishSubagentWave(sessionID, runID, toolCalls[i].CallID, "failed"); finishErr != nil && decisions[i].Err == nil {
				decisions[i].Err = fmt.Errorf("release subagent wave reservation: %w", finishErr)
				decisions[i].Result.Error = decisions[i].Err.Error()
			}
		}
		if decisions[i].Err != nil && errors.Is(decisions[i].Err, context.Canceled) {
			return nil, nil, nil, nil, nil, decisions[i].Err
		}
		if decisions[i].Err != nil && errors.Is(decisions[i].Err, context.DeadlineExceeded) {
			return nil, nil, nil, nil, nil, decisions[i].Err
		}
		results[i] = decisions[i].Result
		if decisions[i].Approved {
			approvedMask[i] = true
			approvedCalls = append(approvedCalls, toolCalls[i])
			approvedIndexes = append(approvedIndexes, i)
		}
	}
	feedback := make([]PermissionFeedback, 0, len(decisions))
	for i := range decisions {
		note := strings.TrimSpace(decisions[i].Feedback)
		approvedArgs := strings.TrimSpace(decisions[i].ApprovedArguments)
		if note == "" && approvedArgs == "" {
			continue
		}
		feedback = append(feedback, PermissionFeedback{
			CallID:            strings.TrimSpace(toolCalls[i].CallID),
			ToolName:          strings.TrimSpace(toolCalls[i].Name),
			Message:           note,
			ApprovedArguments: approvedArgs,
		})
	}
	runPermissionDebugf("gate_tool_calls.complete session=%s run=%s step=%d total_calls=%d approved=%d feedback_notes=%d", sessionID, runID, step, len(toolCalls), len(approvedCalls), len(feedback))
	return results, approvedCalls, approvedIndexes, approvedMask, feedback, nil
}

// planLifecycleRunContext carries trusted provider-run ownership into plan lifecycle
// actions. It is intentionally separate from tool arguments so a model cannot claim
// inline execution merely by supplying a run_id.
type planLifecycleRunContext struct {
	RunID           string
	RunSessionID    string
	ParentSessionID string
	SourceMessageID string
	Inline          bool
}

func (s *Service) executeControlPlaneTool(ctx context.Context, sessionID, sessionMode string, agentProfile pebblestore.AgentProfile, step int, call tool.Call, approvedArguments string, emit StreamHandler) (bool, tool.Result, error) {
	return s.executeControlPlaneToolWithMutation(ctx, sessionID, sessionMode, agentProfile, step, call, approvedArguments, emit, nil)
}

func (s *Service) executeControlPlaneToolWithMutation(ctx context.Context, sessionID, sessionMode string, agentProfile pebblestore.AgentProfile, step int, call tool.Call, approvedArguments string, emit StreamHandler, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (bool, tool.Result, error) {
	return s.executeControlPlaneToolWithLifecycleRunContext(ctx, sessionID, sessionMode, agentProfile, step, call, approvedArguments, emit, applySessionMutation, planLifecycleRunContext{})
}

func (s *Service) executeControlPlaneToolWithLifecycleRunContext(ctx context.Context, sessionID, sessionMode string, agentProfile pebblestore.AgentProfile, step int, call tool.Call, approvedArguments string, emit StreamHandler, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error), lifecycleRun planLifecycleRunContext) (bool, tool.Result, error) {
	name := canonicalToolName(call.Name)
	result := tool.Result{
		CallID: strings.TrimSpace(call.CallID),
		Name:   strings.TrimSpace(call.Name),
	}
	if result.Name == "" {
		result.Name = name
	}

	switch name {
	case "ask_user":
		output, err := executeAskUserTool(call.Arguments, approvedArguments)
		result.Output = output
		return true, result, err
	case "manage_skill":
		output, err := s.executeManageSkillTool(sessionID, call, approvedArguments)
		result.Output = output
		return true, result, err
	case "manage_agent":
		output, err := s.executeManageAgentTool(sessionID, call, approvedArguments)
		result.Output = output
		return true, result, err
	case "manage_integrations":
		output, err := s.executeManageIntegrationsTool(ctx, sessionID, call)
		result.Output = output
		return true, result, err
	case "manage_theme":
		output, err := s.executeManageThemeTool(sessionID, call, approvedArguments)
		result.Output = output
		return true, result, err
	case "manage_worktree":
		output, err := s.executeManageWorktreeTool(ctx, sessionID, call, approvedArguments)
		result.Output = output
		return true, result, err
	case "manage_todos":
		output, err := s.executeManageTodosTool(sessionID, call, approvedArguments)
		result.Output = output
		return true, result, err
	case "manage_sessions":
		switch permission.ManageSessionsAction(call.Arguments) {
		case "deploy":
			output, err := s.executeManageSessionsDeploy(ctx, sessionID, call, approvedArguments, applySessionMutation)
			result.Output = output
			return true, result, err
		case "commit":
			output, err := s.executeManageSessionsCommit(ctx, sessionID, call, approvedArguments)
			result.Output = output
			return true, result, err
		case "archive", "unarchive":
			if strings.TrimSpace(approvedArguments) == "" {
				return true, result, errors.New("manage-sessions mutation requires approved canonical arguments")
			}
			output, err := s.executeManageSessionsCanonicalMutation(ctx, sessionID, call, approvedArguments)
			result.Output = output
			return true, result, err
		default:
			return false, tool.Result{}, nil
		}
	case "exit_plan_mode":
		output, err := s.executeExitPlanModeTool(sessionID, sessionMode, agentProfile, call.Arguments, approvedArguments, applySessionMutation)
		result.Output = output
		return true, result, err
	case "plan_manage":
		output, err := s.executePlanManageToolWithLifecycleRunContext(sessionID, call.Arguments, approvedArguments, applySessionMutation, lifecycleRun)
		result.Output = output
		return true, result, err
	case "edit_pending_plan":
		output, err := s.executeEditPendingPlanTool(sessionID, call.Arguments)
		result.Output = output
		return true, result, err
	case "task":
		principal, _ := identity.PrincipalFromContext(ctx)
		output, err := s.executeTaskToolWithParsed(ctx, sessionID, sessionMode, step, call, emit, taskExecutionRequest{ApprovedArguments: approvedArguments, RunID: lifecycleRun.RunID, Principal: principal, ApplySessionMutation: applySessionMutation})
		result.Output = output
		return true, result, err
	default:
		return false, tool.Result{}, nil
	}
}

func (s *Service) executeEditPendingPlanTool(sessionID, arguments string) (string, error) {
	if s.permissions == nil || s.sessions == nil {
		return "", errors.New("pending plan editing is not configured")
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	if !strings.EqualFold(mapString(session.Metadata, "system_sidechat_kind"), "plan") || !strings.EqualFold(mapString(session.Metadata, "lineage_kind"), "system_sidechat") {
		return "", errors.New("edit_pending_plan is restricted to the reserved Plan sidechat")
	}
	parentID := strings.TrimSpace(mapString(session.Metadata, "parent_session_id"))
	permissionID := strings.TrimSpace(mapString(session.Metadata, "plan_permission_id"))
	if parentID == "" || permissionID == "" {
		return "", errors.New("Plan sidechat is not bound to a pending proposal")
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(firstNonEmptyString(strings.TrimSpace(arguments), "{}")), &args); err != nil {
		return "", fmt.Errorf("edit_pending_plan arguments invalid: %w", err)
	}
	expected := int64(0)
	if value, ok := args["expected_revision"].(float64); ok {
		expected = int64(value)
	}
	rawDocument, ok := args["document"]
	if !ok {
		return "", errors.New("edit_pending_plan requires document")
	}
	raw, err := json.Marshal(rawDocument)
	if err != nil {
		return "", err
	}
	var document pebblestore.SessionPlanDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return "", fmt.Errorf("edit_pending_plan document invalid: %w", err)
	}
	edited, err := s.permissions.EditPendingPlanProposal(permission.PendingPlanProposalEditInput{SessionID: parentID, PermissionID: permissionID, ExpectedRevision: expected, Document: &document})
	if err != nil {
		return "", err
	}
	var payload map[string]any
	_ = json.Unmarshal([]byte(edited.Record.ToolArguments), &payload)
	output, err := json.Marshal(map[string]any{"ok": true, "parent_session_id": parentID, "permission_id": permissionID, "proposal_revision": edited.ProposalRevision, "plan_id": payload["plan_id"], "document": payload["document"]})
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func (s *Service) executeManageSkillTool(sessionID string, call tool.Call, feedback string) (string, error) {
	feedback = strings.TrimSpace(feedback)
	if feedback != "" {
		var payload map[string]any
		if err := json.Unmarshal([]byte(feedback), &payload); err != nil {
			return "", fmt.Errorf("approved manage-skill payload invalid: %w", err)
		}
		args := manageSkillApprovalArguments(payload)
		if len(args) == 0 {
			return "", errors.New("approved manage-skill payload missing approved arguments")
		}
		session, ok, err := s.sessions.GetSession(sessionID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("session %q not found", sessionID)
		}
		scope := buildPermissionWorkspaceScope(session)
		raw, err := json.Marshal(args)
		if err != nil {
			return "", err
		}
		if s.tools != nil {
			output, err := s.tools.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: string(raw)})
			if err != nil {
				return output, err
			}
			return output, nil
		}
		output, err := tool.ExecuteForWorkspaceScope(context.Background(), scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: string(raw)})
		if err != nil {
			return output, err
		}
		return output, nil
	}

	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	scope := buildPermissionWorkspaceScope(session)
	if s.tools != nil {
		output, err := s.tools.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: arguments})
		if err != nil {
			return output, err
		}
		return output, nil
	}
	output, err := tool.ExecuteForWorkspaceScope(context.Background(), scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: arguments})
	if err != nil {
		return output, err
	}
	return output, nil
}

func (s *Service) executeManageAgentTool(sessionID string, call tool.Call, feedback string) (string, error) {
	feedback = strings.TrimSpace(feedback)
	runPermissionDebugf("manage_agent.execute session=%s call=%s approved_args_chars=%d approved_args_preview=%q", sessionID, strings.TrimSpace(call.CallID), len(feedback), runPermissionDebugPreview(feedback, 200))
	if feedback != "" {
		var args map[string]any
		if err := json.Unmarshal([]byte(feedback), &args); err != nil {
			return "", fmt.Errorf("approved manage-agent payload invalid: %w", err)
		}
		if len(args) == 0 {
			return "", errors.New("approved manage-agent payload missing approved arguments")
		}
		session, ok, err := s.sessions.GetSession(sessionID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("session %q not found", sessionID)
		}
		scope := buildPermissionWorkspaceScope(session)
		raw, err := json.Marshal(args)
		if err != nil {
			return "", err
		}
		if s.tools != nil {
			output, err := s.tools.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: string(raw)})
			if err != nil {
				return output, err
			}
			return output, nil
		}
		output, err := tool.ExecuteForWorkspaceScope(context.Background(), scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: string(raw)})
		if err != nil {
			return output, err
		}
		return output, nil
	}

	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	scope := buildPermissionWorkspaceScope(session)
	if s.tools != nil {
		output, err := s.tools.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: arguments})
		if err != nil {
			return output, err
		}
		return output, nil
	}
	output, err := tool.ExecuteForWorkspaceScope(context.Background(), scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: arguments})
	if err != nil {
		return output, err
	}
	return output, nil
}

func (s *Service) executeManageIntegrationsTool(ctx context.Context, sessionID string, call tool.Call) (string, error) {
	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	scope := buildPermissionWorkspaceScope(session)
	if principal, ok := identity.PrincipalFromContext(ctx); ok {
		scope.Principal = principal
	}
	if s.tools != nil {
		output, err := s.tools.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: arguments})
		if err != nil {
			return output, err
		}
		return output, nil
	}
	output, err := tool.ExecuteForWorkspaceScope(context.Background(), scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: arguments})
	if err != nil {
		return output, err
	}
	return output, nil
}

func (s *Service) executeManageThemeTool(sessionID string, call tool.Call, feedback string) (string, error) {
	feedback = strings.TrimSpace(feedback)
	if feedback != "" {
		var payload map[string]any
		if err := json.Unmarshal([]byte(feedback), &payload); err != nil {
			return "", fmt.Errorf("approved manage-theme payload invalid: %w", err)
		}
		args := manageThemeApprovalArguments(payload)
		if len(args) == 0 {
			return "", errors.New("approved manage-theme payload missing approved arguments")
		}
		session, ok, err := s.sessions.GetSession(sessionID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("session %q not found", sessionID)
		}
		scope := buildPermissionWorkspaceScope(session)
		raw, err := json.Marshal(args)
		if err != nil {
			return "", err
		}
		if s.tools != nil {
			output, err := s.tools.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: string(raw)})
			if err != nil {
				return output, err
			}
			return output, nil
		}
		output, err := tool.ExecuteForWorkspaceScope(context.Background(), scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: string(raw)})
		if err != nil {
			return output, err
		}
		return output, nil
	}

	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	scope := buildPermissionWorkspaceScope(session)
	if s.tools != nil {
		output, err := s.tools.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: arguments})
		if err != nil {
			return output, err
		}
		return output, nil
	}
	output, err := tool.ExecuteForWorkspaceScope(context.Background(), scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: arguments})
	if err != nil {
		return output, err
	}
	return output, nil
}

func (s *Service) executeManageWorktreeTool(ctx context.Context, sessionID string, call tool.Call, _ ...string) (string, error) {
	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	scope := buildPermissionWorkspaceScope(session)
	if principal, ok := identity.PrincipalFromContext(ctx); ok && principal.Valid() {
		if strings.TrimSpace(principal.UserID) != strings.TrimSpace(session.UserID) || strings.TrimSpace(principal.AccountScopeID) != strings.TrimSpace(session.AccountScopeID) {
			return "", errors.New("manage-worktree authenticated principal does not own the calling session")
		}
		principal.SessionID = strings.TrimSpace(session.ID)
		scope.Principal = principal
	}
	if !scope.Principal.Valid() {
		return "", errors.New("manage-worktree calling context missing authenticated principal")
	}
	executionCtx := identity.ContextWithPrincipal(context.Background(), scope.Principal)
	if s.tools != nil {
		output, err := s.tools.ExecuteForWorkspaceScopeWithRuntime(executionCtx, scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: arguments})
		if err != nil {
			return output, err
		}
		return output, nil
	}
	output, err := tool.ExecuteForWorkspaceScope(executionCtx, scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: arguments})
	if err != nil {
		return output, err
	}
	return output, nil
}

func (s *Service) executeManageTodosTool(sessionID string, call tool.Call, feedback string) (string, error) {
	feedback = strings.TrimSpace(feedback)
	if feedback != "" {
		var args map[string]any
		if err := json.Unmarshal([]byte(feedback), &args); err != nil {
			return "", fmt.Errorf("approved manage-todos payload invalid: %w", err)
		}
		if len(args) == 0 {
			return "", errors.New("approved manage-todos payload missing approved arguments")
		}
		session, ok, err := s.sessions.GetSession(sessionID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("session %q not found", sessionID)
		}
		scope := buildPermissionWorkspaceScope(session)
		raw, err := json.Marshal(args)
		if err != nil {
			return "", err
		}
		if s.tools != nil {
			output, err := s.tools.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: string(raw)})
			if err != nil {
				return output, err
			}
			return output, nil
		}
		output, err := tool.ExecuteForWorkspaceScope(context.Background(), scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: string(raw)})
		if err != nil {
			return output, err
		}
		return output, nil
	}

	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	scope := buildPermissionWorkspaceScope(session)
	if s.tools != nil {
		output, err := s.tools.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: arguments})
		if err != nil {
			return output, err
		}
		return output, nil
	}
	output, err := tool.ExecuteForWorkspaceScope(context.Background(), scope, tool.Call{CallID: call.CallID, Name: call.Name, Arguments: arguments})
	if err != nil {
		return output, err
	}
	return output, nil
}

func executeAskUserTool(arguments, feedback string) (string, error) {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		arguments = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("ask-user arguments invalid: %w", err)
	}

	question := strings.TrimSpace(mapString(args, "question"))
	if question == "" {
		question = strings.TrimSpace(mapString(args, "title"))
	}
	if question == "" {
		question = "User input requested"
	}

	questions := extractAskUserQuestions(args)
	options := make([]string, 0, 8)
	if raw, ok := args["options"]; ok {
		switch typed := raw.(type) {
		case []any:
			for _, item := range typed {
				switch option := item.(type) {
				case string:
					text := strings.TrimSpace(option)
					if text != "" {
						options = append(options, text)
					}
				case map[string]any:
					label := strings.TrimSpace(mapString(option, "label"))
					if label == "" {
						label = strings.TrimSpace(mapString(option, "value"))
					}
					if label != "" {
						options = append(options, label)
					}
				}
			}
		}
	}

	answer, structuredAnswers := decodeAskUserFeedback(feedback)
	status := "approved_no_response"
	summary := "ask-user approved without textual response"
	if answer != "" || len(structuredAnswers) > 0 {
		status = "answered"
		summary = "ask-user response captured"
	}

	payload := map[string]any{
		"tool":              "ask_user",
		"status":            status,
		"question":          question,
		"options":           options,
		"answer":            answer,
		"questions":         questions,
		"answers":           structuredAnswers,
		"path_id":           "tool.ask-user.v3",
		"summary":           summary,
		"details_truncated": false,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func extractAskUserQuestions(args map[string]any) []map[string]any {
	if len(args) == 0 {
		return nil
	}
	raw, ok := args["questions"]
	if !ok {
		return nil
	}
	typed, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(typed))
	for i := range typed {
		item, ok := typed[i].(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(mapString(item, "id"))
		if id == "" {
			id = fmt.Sprintf("q_%d", i+1)
		}
		question := strings.TrimSpace(mapString(item, "question"))
		if question == "" {
			question = strings.TrimSpace(mapString(item, "prompt"))
		}
		if question == "" {
			question = strings.TrimSpace(mapString(item, "title"))
		}
		options := make([]string, 0, 8)
		if rawOptions, ok := item["options"]; ok {
			if optionItems, ok := rawOptions.([]any); ok {
				for _, current := range optionItems {
					switch typedOption := current.(type) {
					case string:
						text := strings.TrimSpace(typedOption)
						if text != "" {
							options = append(options, text)
						}
					case map[string]any:
						label := strings.TrimSpace(mapString(typedOption, "label"))
						if label == "" {
							label = strings.TrimSpace(mapString(typedOption, "value"))
						}
						if label != "" {
							options = append(options, label)
						}
					}
				}
			}
		}
		out = append(out, map[string]any{
			"id":       id,
			"question": question,
			"options":  options,
		})
	}
	return out
}

func decodeAskUserFeedback(feedback string) (string, map[string]string) {
	feedback = strings.TrimSpace(feedback)
	if feedback == "" {
		return "", nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(feedback), &parsed); err != nil {
		return feedback, nil
	}
	rawAnswers, ok := parsed["answers"].(map[string]any)
	if !ok || len(rawAnswers) == 0 {
		return feedback, nil
	}
	answers := make(map[string]string, len(rawAnswers))
	for key, value := range rawAnswers {
		id := strings.TrimSpace(key)
		if id == "" {
			continue
		}
		switch typed := value.(type) {
		case string:
			if text := strings.TrimSpace(typed); text != "" {
				answers[id] = text
			}
		case fmt.Stringer:
			if text := strings.TrimSpace(typed.String()); text != "" {
				answers[id] = text
			}
		default:
			text := strings.TrimSpace(fmt.Sprintf("%v", typed))
			if text != "" {
				answers[id] = text
			}
		}
	}
	if len(answers) == 0 {
		return feedback, nil
	}
	return "", answers
}

func (s *Service) executeExitPlanModeTool(sessionID, sessionMode string, agentProfile pebblestore.AgentProfile, arguments, feedback string, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (string, error) {
	input, args, userMessage, err := s.prepareExitPlanModeLifecycleInput(sessionID, arguments, feedback)
	if err != nil {
		return "", err
	}
	if !pebblestore.AgentExitPlanModeEnabled(agentProfile) {
		return marshalExitPlanModeRejectionPayload(input, userMessage, "disabled_for_agent", "exit_plan_mode rejected: disabled for agent", nil)
	}
	if sessionruntime.NormalizeMode(sessionMode) != sessionruntime.ModePlan {
		return marshalExitPlanModeRejectionPayload(input, userMessage, "not_in_plan_mode", "exit_plan_mode rejected: session not in plan mode; use plan_manage save to update the active plan instead", []string{"Do not call exit_plan_mode from auto. To update the active plan instead, use plan_manage save."})
	}

	input.ApplySessionMutation = applySessionMutation
	input.BuildLifecycleMessage = func(plan pebblestore.SessionPlanSnapshot, summary sessionruntime.PlanExecutionSummary) *pebblestore.MessageSnapshot {
		message, ok := BuildPlanExecutionLifecycleSystemMessage(PlanExecutionLifecycleMessageInput{Action: "approve_and_start", Plan: plan, Payload: map[string]any{"action": "approve_and_start", "checkpoint_id": summary.NextCheckpointID, "next_checkpoint_id": summary.NextCheckpointID, "next_action": "run_checkpoint_with_fresh_context"}})
		if !ok {
			return nil
		}
		return &pebblestore.MessageSnapshot{Role: "system", Content: message.Content, Metadata: message.Metadata}
	}
	if applySessionMutation != nil {
		if current, ok, getErr := s.sessions.GetSession(sessionID); getErr == nil && ok {
			current.Mode = sessionruntime.ModeAuto
			preference, contextWindow, maxOutputTokens, policy, resolveErr := s.resolvePlanLifecycleModePreference(sessionruntime.PlanLifecycleResult{Session: current})
			if resolveErr == nil && strings.TrimSpace(preference.Provider) != "" && strings.TrimSpace(preference.Model) != "" {
				input.ModeEventFields = map[string]any{"preference": preference, "context_window": contextWindow, "max_output_tokens": maxOutputTokens, "agent_model_policy": policy, "swarm_conf_v3_diagnostics_enabled": os.Getenv("SWARM_V3_DIAGNOSTICS") == "1"}
			}
		}
	}
	lifecycleResult, lifecycleErr := sessionruntime.NewPlanLifecycleService(s.sessions).SubmitPlanForApproval(input)
	if lifecycleErr != nil {
		return "", fmt.Errorf("exit_plan_mode failed to submit plan: %w", lifecycleErr)
	}
	if err := s.persistPlanLifecycleResult(lifecycleResult, applySessionMutation); err != nil {
		return "", fmt.Errorf("exit_plan_mode failed to publish lifecycle result: %w", err)
	}
	return marshalExitPlanModeApprovedPayload(lifecycleResult, userMessage, strings.TrimSpace(firstNonEmptyString(input.PlanID, mapString(args, "plan_id"), mapString(args, "planID"), mapString(args, "id"))))
}

func (s *Service) executePlanManageTool(sessionID, arguments, feedback string) (string, error) {
	return s.executePlanManageToolWithMutation(sessionID, arguments, feedback, nil)
}

func (s *Service) prepareExitPlanModeLifecycleInput(sessionID, arguments, feedback string) (sessionruntime.PlanLifecyclePlanInput, map[string]any, string, error) {
	if s.sessions == nil {
		return sessionruntime.PlanLifecyclePlanInput{}, nil, "", errors.New("session service is not configured")
	}
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		arguments = "{}"
	}
	userMessage := ""
	if approved := strings.TrimSpace(feedback); approved != "" {
		var payload map[string]any
		if err := json.Unmarshal([]byte(approved), &payload); err != nil {
			userMessage = normalizePermissionFeedback(approved)
		} else {
			if reason := normalizePermissionFeedback(mapString(payload, "reason")); reason != "" {
				userMessage = reason
			}
			if rawApprovedArgs, ok := payload["approved_arguments"]; ok {
				raw, err := json.Marshal(rawApprovedArgs)
				if err != nil {
					return sessionruntime.PlanLifecyclePlanInput{}, nil, "", err
				}
				var approvedArgs map[string]any
				if err := json.Unmarshal(raw, &approvedArgs); err != nil {
					return sessionruntime.PlanLifecyclePlanInput{}, nil, "", fmt.Errorf("approved exit_plan_mode arguments invalid: %w", err)
				}
				payload = approvedArgs
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				return sessionruntime.PlanLifecyclePlanInput{}, nil, "", err
			}
			arguments = string(raw)
		}
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return sessionruntime.PlanLifecyclePlanInput{}, nil, "", fmt.Errorf("exit_plan_mode arguments invalid: %w", err)
	}
	document, err := planDocumentFromArgsForTool(args, "exit_plan_mode")
	if err != nil {
		return sessionruntime.PlanLifecyclePlanInput{}, nil, "", err
	}
	if document == nil {
		return sessionruntime.PlanLifecyclePlanInput{}, nil, "", errors.New("exit_plan_mode requires an explicit structured document; plan text and an existing saved plan are display context only")
	}
	planID := strings.TrimSpace(firstNonEmptyString(mapString(args, "plan_id"), mapString(args, "planID"), mapString(args, "id")))
	title := strings.TrimSpace(mapString(args, "title"))
	plan := strings.TrimSpace(mapString(args, "plan"))
	if document != nil {
		if planID == "" {
			planID = strings.TrimSpace(document.ID)
		}
		if title == "" {
			title = strings.TrimSpace(document.Title)
		}
		if plan == "" {
			plan = strings.TrimSpace(firstNonEmptyString(document.DisplayText, document.RenderedText))
		}
	}
	if planID == "" {
		active, ok, err := s.sessions.GetActivePlan(sessionID)
		if err != nil {
			return sessionruntime.PlanLifecyclePlanInput{}, nil, "", fmt.Errorf("exit_plan_mode failed to inspect active plan: %w", err)
		}
		if ok {
			planID = strings.TrimSpace(active.ID)
			if title == "" {
				title = strings.TrimSpace(active.Title)
			}
			if plan == "" {
				plan = strings.TrimSpace(active.Plan)
			}
		}
	} else if existing, ok, err := s.sessions.GetPlan(sessionID, planID); err != nil {
		return sessionruntime.PlanLifecyclePlanInput{}, nil, "", fmt.Errorf("exit_plan_mode failed to inspect plan: %w", err)
	} else if ok {
		if title == "" {
			title = strings.TrimSpace(existing.Title)
		}
		if plan == "" {
			plan = strings.TrimSpace(existing.Plan)
		}
	}
	if planID == "" {
		planID = fmt.Sprintf("plan_%d", time.Now().UnixMilli())
	}
	continuation := strings.TrimSpace(firstNonEmptyString(mapString(args, "continuation_policy"), mapString(args, "continuation"), mapString(args, "mode")))
	continueAutomatically := (*bool)(nil)
	if _, ok := args["continue_automatically"]; ok {
		value := mapBool(args, "continue_automatically")
		continueAutomatically = &value
		if value {
			continuation = sessionruntime.PlanAcceptanceContinuationAutomatic
		} else {
			continuation = sessionruntime.PlanAcceptanceContinuationReviewEachCheckpoint
		}
	}
	input := sessionruntime.PlanLifecyclePlanInput{
		SessionID:             sessionID,
		PlanID:                planID,
		Title:                 title,
		Plan:                  plan,
		Document:              document,
		AgentCanSubmit:        true,
		ExecutionGranularity:  strings.TrimSpace(firstNonEmptyString(mapString(args, "execution_granularity"), mapString(args, "granularity"), mapString(args, "execution_shape"), mapString(args, "shape"))),
		ContinuationPolicy:    continuation,
		ContinueAutomatically: continueAutomatically,
	}
	return input, args, userMessage, nil
}

func marshalExitPlanModeRejectionPayload(input sessionruntime.PlanLifecyclePlanInput, userMessage, approvalState, summary string, requestedModifications []string) (string, error) {
	payload := map[string]any{
		"tool":              "exit_plan_mode",
		"status":            "rejected",
		"title":             input.Title,
		"plan_id":           input.PlanID,
		"plan":              input.Plan,
		"document":          input.Document,
		"approval_state":    approvalState,
		"path_id":           "tool.exit-plan-mode.v3",
		"summary":           summary,
		"user_message":      userMessage,
		"details_truncated": false,
	}
	if requestedModifications != nil {
		payload["requested_modifications"] = requestedModifications
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func marshalExitPlanModeApprovedPayload(result sessionruntime.PlanLifecycleResult, userMessage, fallbackPlanID string) (string, error) {
	savedPlan := result.Plan
	planID := strings.TrimSpace(firstNonEmptyString(savedPlan.ID, fallbackPlanID))
	payload := map[string]any{
		"tool":                    "exit_plan_mode",
		"status":                  "approved",
		"title":                   savedPlan.Title,
		"plan_id":                 planID,
		"plan":                    savedPlan.Plan,
		"document":                savedPlan.Document,
		"approval_state":          "approved",
		"requested_modifications": []string{},
		"mode_changed":            result.ModeChanged || result.ModeEvent != nil,
		"target_mode":             sessionruntime.ModeAuto,
		"user_message":            userMessage,
		"path_id":                 "tool.exit-plan-mode.v3",
		"summary":                 result.Message,
		"details_truncated":       false,
		"version":                 savedPlan.Version,
		"parent_revision":         savedPlan.ParentRevision,
	}
	if strings.TrimSpace(payload["summary"].(string)) == "" {
		payload["summary"] = "structured plan saved, approved; mode switched to auto"
	}
	addPlanRunRequestPayloadFields(payload, planID, savedPlan.Document)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (s *Service) persistPlanLifecycleResult(result sessionruntime.PlanLifecycleResult, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
	if result.V3Mutation != nil {
		return nil
	}
	if err := s.persistPlanSavedV3Mutation(result.Plan, result.PlanEvent, applySessionMutation); err != nil {
		return fmt.Errorf("publish plan saved: %w", err)
	}
	if applySessionMutation != nil {
		preference, contextWindow, maxOutputTokens, agentModelPolicy, err := s.resolvePlanLifecycleModePreference(result)
		if err != nil {
			return fmt.Errorf("resolve mode preference: %w", err)
		}
		if err := s.persistModeUpdatedV3MutationWithPreference(result, preference, contextWindow, maxOutputTokens, agentModelPolicy, applySessionMutation); err != nil {
			return fmt.Errorf("publish mode updated: %w", err)
		}
	} else if result.ModeEvent != nil {
		s.publishEventEnvelope(*result.ModeEvent)
	}
	return nil
}

func (s *Service) resolvePlanLifecycleModePreference(result sessionruntime.PlanLifecycleResult) (pebblestore.ModelPreference, int, int, map[string]any, error) {
	if s == nil || s.model == nil {
		return pebblestore.ModelPreference{}, 0, 0, nil, nil
	}
	profile, err := sessionV3AgentProfileFromMetadataMap(result.Session.Metadata)
	if err != nil {
		return pebblestore.ModelPreference{}, 0, 0, nil, nil
	}
	preference := applyAgentPreferenceOverridesForMode(result.Session.Preference, profile, result.Session.Mode)
	resolved, err := s.model.ResolvePreference(preference)
	if err != nil {
		resolved.Preference = preference
	}
	policy := map[string]any{
		"agent_name":          strings.TrimSpace(profile.Name),
		"resolved_agent_name": strings.TrimSpace(profile.Name),
		"source":              "default",
		"locked":              false,
		"preference":          resolved.Preference,
		"context_window":      resolved.ContextWindow,
		"max_output_tokens":   resolved.MaxOutputTokens,
	}
	if pebblestore.AgentModelMode(profile) == "split" && pebblestore.AgentSupportsSplitModel(profile) {
		policy["source"] = "agent_auto_preset"
		policy["locked"] = true
		policy["reason"] = "Session exited plan mode; active model switched to the configured auto model."
	}
	return resolved.Preference, resolved.ContextWindow, resolved.MaxOutputTokens, policy, nil
}

func sessionV3AgentProfileFromMetadataMap(metadata map[string]any) (pebblestore.AgentProfile, error) {
	raw, ok := metadata["agent_profile"]
	if !ok || raw == nil {
		return pebblestore.AgentProfile{}, errors.New("session metadata is missing agent_profile")
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	var profile pebblestore.AgentProfile
	if err := json.Unmarshal(body, &profile); err != nil {
		return pebblestore.AgentProfile{}, err
	}
	return profile, nil
}

func (s *Service) executePlanManageToolWithMutation(sessionID, arguments, feedback string, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (string, error) {
	return s.executePlanManageToolWithLifecycleRunContext(sessionID, arguments, feedback, applySessionMutation, planLifecycleRunContext{})
}

func (s *Service) executePlanManageToolWithLifecycleRunContext(sessionID, arguments, feedback string, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error), lifecycleRun planLifecycleRunContext) (string, error) {
	if s.sessions == nil {
		return "", errors.New("session service is not configured")
	}
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		arguments = "{}"
	}
	if trimmed := strings.TrimSpace(feedback); trimmed != "" {
		var payload map[string]any
		if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
			return "", fmt.Errorf("approved plan-manage payload invalid: %w", err)
		}
		args := planManageApprovalArguments(payload)
		if len(args) == 0 {
			return "", errors.New("approved plan-manage payload missing approved arguments")
		}
		raw, err := json.Marshal(args)
		if err != nil {
			return "", err
		}
		arguments = string(raw)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("plan_manage arguments invalid: %w", err)
	}

	action := strings.ToLower(strings.TrimSpace(mapString(args, "action")))
	if action == "" {
		action = strings.ToLower(strings.TrimSpace(mapString(args, "op")))
	}
	switch action {
	case "ls":
		action = "list"
	case "show":
		action = "get"
	case "active", "current":
		action = "get-active"
	case "activate", "use":
		action = "set-active"
	case "create":
		action = "new"
	case "upsert", "set", "write-active", "write_active":
		action = "save"
	case "create-plan", "create_plan", "propose-plan", "propose_plan":
		action = "request_new_plan"
	case "update", "edit":
		if strings.TrimSpace(mapString(args, "plan")) == "" && args["document"] == nil {
			action = "patch"
		} else {
			action = "save"
		}
	case "update-info", "update_info", "patch-info", "patch_info":
		action = "update_info"
	case "update-execution-policy", "update_execution_policy", "set-execution-policy", "set_execution_policy", "execution-policy", "execution_policy":
		action = "update_execution_policy"
	case "update-execution-state", "update_execution_state", "set-execution-state", "set_execution_state", "execution-state", "execution_state":
		action = "update_execution_state"
	case "upsert-checkpoint", "upsert_checkpoint", "replace-checkpoint", "replace_checkpoint":
		action = "upsert_checkpoint"
	case "update-checkpoint", "update_checkpoint", "patch-checkpoint", "patch_checkpoint":
		action = "update_checkpoint"
	case "start-checkpoint", "start_checkpoint":
		action = "start_checkpoint"
	case "continue-checkpoint", "continue_checkpoint", "advance-checkpoint", "advance_checkpoint", "next-checkpoint", "next_checkpoint":
		action = "continue_checkpoint"
	case "complete-checkpoint", "complete_checkpoint", "finish-checkpoint", "finish_checkpoint", "mark-completed", "mark_completed":
		action = "complete_checkpoint"
	case "checkpoint-outcome", "checkpoint_outcome", "mark-checkpoint-outcome", "mark_checkpoint_outcome", "mark-checkpoint", "mark_checkpoint":
		action = "checkpoint_outcome"
	case "mark-needs-review", "mark_needs_review":
		action = "mark_needs_review"
	case "mark-blocked", "mark_blocked":
		action = "mark_blocked"
	case "resolve-blocked-checkpoint", "resolve_blocked_checkpoint", "resolve-block", "resolve_block", "clear-block", "clear_block", "unblock-checkpoint", "unblock_checkpoint":
		action = "resolve_blocked_checkpoint"
	case "mark-failed", "mark_failed":
		action = "mark_failed"
	case "add-subtask", "add_subtask", "create-subtask", "create_subtask", "upsert-subtask", "upsert_subtask":
		action = "add_subtask"
	case "update-subtask", "update_subtask", "patch-subtask", "patch_subtask":
		action = "update_subtask"
	case "remove-subtask", "remove_subtask", "delete-subtask", "delete_subtask":
		action = "remove_subtask"
	case "reorder-subtasks", "reorder_subtasks":
		action = "reorder_subtasks"
	case "focus-subtask", "focus_subtask", "set-active-subtask", "set_active_subtask", "start-subtask", "start_subtask":
		action = "focus_subtask"
	case "complete-subtask", "complete_subtask", "finish-subtask", "finish_subtask":
		action = "complete_subtask"
	case "remove-checkpoint", "remove_checkpoint", "delete-checkpoint", "delete_checkpoint":
		action = "remove_checkpoint"
	case "reorder-checkpoints", "reorder_checkpoints":
		action = "reorder_checkpoints"
	case "set-active-checkpoint", "set_active_checkpoint", "activate-checkpoint", "activate_checkpoint":
		action = "set_active_checkpoint"
	case "approve-and-start", "approve_and_start", "approve-start", "approve_start", "start-plan", "start_plan":
		action = "approve_and_start"
	case "start-session-checkpoint", "start_session_checkpoint", "session-checkpoint", "session_checkpoint", "auto-checkpoint", "auto_checkpoint":
		action = "start_session_checkpoint"
	case "request-followup-checkpoint", "request_followup_checkpoint", "followup-checkpoint", "followup_checkpoint", "request-changes", "request_changes":
		action = "request_followup_checkpoint"
	case "request-plan-revision", "request_plan_revision", "plan-revision", "plan_revision":
		action = "request_plan_revision"
	case "amend-plan", "amend_plan", "plan-amendment", "plan_amendment", "amend-future-checkpoints", "amend_future_checkpoints":
		action = "amend_plan"
	case "request-new-plan", "request_new_plan", "new-plan-proposal", "new_plan_proposal":
		action = "request_new_plan"
	case "restart-checkpoint", "restart_checkpoint", "retry-checkpoint", "retry_checkpoint", "restart-checkpoint-from-zero", "restart_checkpoint_from_zero":
		action = "restart_checkpoint"
	case "rewind-to-checkpoint", "rewind_to_checkpoint", "rewind-checkpoint", "rewind_checkpoint":
		action = "rewind_to_checkpoint"
	case "update-section", "update_section":
		action = "update_section"
	}
	if action == "" {
		action = "list"
	}

	switch action {
	case "approve_and_start", "restart_checkpoint", "rewind_to_checkpoint", "resolve_blocked_checkpoint", "start_session_checkpoint", "request_followup_checkpoint", "request_plan_revision", "amend_plan", "request_new_plan":
		return s.executePlanLifecycleControlAction(sessionID, action, args, applySessionMutation, lifecycleRun)
	case "list":
		limit := mapInt(args, "limit")
		if limit <= 0 {
			limit = 50
		}
		if limit > 500 {
			limit = 500
		}
		plans, activeID, err := s.sessions.ListPlans(sessionID, limit)
		if err != nil {
			return "", err
		}
		items := make([]map[string]any, 0, len(plans))
		for i := range plans {
			items = append(items, planManagePlanSummary(plans[i], true))
		}
		payload := map[string]any{
			"tool":              "plan_manage",
			"action":            "list",
			"active_plan_id":    activeID,
			"count":             len(items),
			"plans":             items,
			"path_id":           "tool.plan-manage.v3",
			"summary":           fmt.Sprintf("listed %d plans", len(items)),
			"details_truncated": false,
		}
		return marshalPlanManagePayload(payload)
	case "history", "revisions":
		planID := strings.TrimSpace(mapString(args, "plan_id"))
		if planID == "" {
			planID = strings.TrimSpace(mapString(args, "id"))
		}
		if planID == "" || strings.EqualFold(planID, "active") {
			active, ok, err := s.sessions.GetActivePlan(sessionID)
			if err != nil {
				return "", err
			}
			if !ok {
				payload := map[string]any{"tool": "plan_manage", "action": "history", "status": "empty", "path_id": "tool.plan-manage.v3", "summary": "no active plan", "details_truncated": false}
				return marshalPlanManagePayload(payload)
			}
			planID = strings.TrimSpace(active.ID)
		}
		limit := mapInt(args, "limit")
		if limit <= 0 {
			limit = 50
		}
		if limit > 500 {
			limit = 500
		}
		revisionKind := strings.TrimSpace(firstNonEmptyString(mapString(args, "revision_kind"), mapString(args, "kind")))
		if revisionKind == "" {
			revisionKind = sessionruntime.PlanRevisionKindDefinition
		}
		revisions, err := s.sessions.ListPlanRevisionsByKind(sessionID, planID, limit, revisionKind)
		if err != nil {
			return "", err
		}
		items := make([]map[string]any, 0, len(revisions))
		for i := range revisions {
			items = append(items, planManagePlanRevisionSummary(revisions[i]))
		}
		payload := map[string]any{
			"tool":              "plan_manage",
			"action":            "history",
			"status":            "ok",
			"plan_id":           planID,
			"revision_kind":     revisionKind,
			"count":             len(items),
			"revisions":         items,
			"path_id":           "tool.plan-manage.v3",
			"summary":           fmt.Sprintf("listed %d plan revisions", len(items)),
			"details_truncated": false,
		}
		return marshalPlanManagePayload(payload)
	case "get":
		planID := strings.TrimSpace(mapString(args, "plan_id"))
		if planID == "" {
			planID = strings.TrimSpace(mapString(args, "id"))
		}
		if strings.EqualFold(planID, "active") {
			action = "get-active"
			break
		}
		if planID == "" {
			return "", errors.New("plan_manage get requires plan_id")
		}
		plan, ok, err := s.sessions.GetPlan(sessionID, planID)
		if err != nil {
			return "", err
		}
		if !ok {
			payload := map[string]any{
				"tool":              "plan_manage",
				"action":            "get",
				"status":            "not_found",
				"plan_id":           planID,
				"path_id":           "tool.plan-manage.v3",
				"summary":           "plan not found",
				"details_truncated": false,
			}
			return marshalPlanManagePayload(payload)
		}
		payload := map[string]any{
			"tool":              "plan_manage",
			"action":            "get",
			"status":            "ok",
			"plan":              plan,
			"revision":          currentPlanRevision(plan),
			"current_revision":  currentPlanRevision(plan),
			"base_revision":     currentPlanRevision(plan),
			"path_id":           "tool.plan-manage.v3",
			"summary":           fmt.Sprintf("loaded plan %s", plan.ID),
			"details_truncated": false,
		}
		return marshalPlanManagePayload(payload)
	case "get-active":
		plan, ok, err := s.sessions.GetActivePlan(sessionID)
		if err != nil {
			return "", err
		}
		if !ok {
			payload := map[string]any{
				"tool":              "plan_manage",
				"action":            "get-active",
				"status":            "empty",
				"path_id":           "tool.plan-manage.v3",
				"summary":           "no active plan",
				"details_truncated": false,
			}
			return marshalPlanManagePayload(payload)
		}
		payload := map[string]any{
			"tool":              "plan_manage",
			"action":            "get-active",
			"status":            "ok",
			"plan":              plan,
			"revision":          currentPlanRevision(plan),
			"current_revision":  currentPlanRevision(plan),
			"base_revision":     currentPlanRevision(plan),
			"path_id":           "tool.plan-manage.v3",
			"summary":           fmt.Sprintf("active plan is %s", plan.ID),
			"details_truncated": false,
		}
		return marshalPlanManagePayload(payload)
	case "set-active":
		planID := strings.TrimSpace(mapString(args, "plan_id"))
		if planID == "" {
			planID = strings.TrimSpace(mapString(args, "id"))
		}
		if planID == "" {
			return "", errors.New("plan_manage set-active requires plan_id")
		}
		plan, _, err := s.sessions.SetActivePlan(sessionID, planID)
		if err != nil {
			return "", err
		}
		payload := map[string]any{
			"tool":              "plan_manage",
			"action":            "set-active",
			"status":            "ok",
			"plan":              plan,
			"path_id":           "tool.plan-manage.v3",
			"summary":           fmt.Sprintf("active plan set to %s", plan.ID),
			"details_truncated": false,
		}
		return marshalPlanManagePayload(payload)
	case "save":
		title := strings.TrimSpace(mapString(args, "title"))
		planBody := strings.TrimSpace(mapString(args, "plan"))
		planID := strings.TrimSpace(mapString(args, "plan_id"))
		if planID == "" {
			planID = strings.TrimSpace(mapString(args, "id"))
		}
		if planID == "" {
			active, ok, err := s.sessions.GetActivePlan(sessionID)
			if err != nil {
				return "", err
			}
			if ok {
				planID = strings.TrimSpace(active.ID)
			}
		}
		if planBody == "" && args["document"] == nil {
			return "", errors.New("plan_manage save requires plan or document")
		}
		status := strings.TrimSpace(mapString(args, "status"))
		approvalState := strings.TrimSpace(mapString(args, "approval_state"))
		updateSummary := strings.TrimSpace(firstNonEmptyString(mapString(args, "update_summary"), mapString(args, "summary")))
		updateScope := strings.TrimSpace(firstNonEmptyString(mapString(args, "update_scope"), mapString(args, "scope")))
		updateKind := strings.TrimSpace(firstNonEmptyString(mapString(args, "update_kind"), mapString(args, "kind")))
		revisionKind := strings.TrimSpace(mapString(args, "revision_kind"))
		checkpoint := mapBool(args, "checkpoint")
		document, err := planDocumentFromArgs(args)
		if err != nil {
			return "", err
		}
		if planID != "" {
			existing, ok, err := s.sessions.GetPlan(sessionID, planID)
			if err != nil {
				return "", err
			}
			if ok {
				if planBody == "" && document != nil {
					planBody = existing.Plan
				}
				if title == "" {
					title = strings.TrimSpace(existing.Title)
				}
				if status == "" {
					status = strings.TrimSpace(existing.Status)
				}
				if approvalState == "" {
					approvalState = strings.TrimSpace(existing.ApprovalState)
				}
			}
		}
		if title == "" {
			title = "Plan"
		}
		activate := true
		if _, hasActivate := args["activate"]; hasActivate {
			activate = mapBool(args, "activate")
		}
		plan, event, err := s.sessions.SavePlanWithMetadata(sessionID, planID, title, planBody, status, approvalState, activate, sessionruntime.PlanSaveMetadata{UpdateSummary: updateSummary, UpdateScope: updateScope, UpdateKind: updateKind, RevisionKind: revisionKind, Checkpoint: checkpoint, Document: document})
		if err != nil {
			return "", err
		}
		if err := s.persistPlanSavedV3Mutation(plan, event, applySessionMutation); err != nil {
			return "", err
		}
		payload := map[string]any{
			"tool":              "plan_manage",
			"action":            "save",
			"status":            "ok",
			"plan":              plan,
			"path_id":           "tool.plan-manage.v3",
			"summary":           fmt.Sprintf("saved plan %s", plan.ID),
			"details_truncated": false,
		}
		return marshalPlanManagePayload(payload)
	case "patch", "update_section", "update_info", "update_execution_policy", "update_execution_state", "upsert_checkpoint", "update_checkpoint", "start_checkpoint", "continue_checkpoint", "complete_checkpoint", "checkpoint_outcome", "mark_needs_review", "mark_blocked", "mark_failed", "remove_checkpoint", "reorder_checkpoints", "set_active_checkpoint", "add_subtask", "update_subtask", "remove_subtask", "reorder_subtasks", "focus_subtask", "complete_subtask":
		planID := strings.TrimSpace(mapString(args, "plan_id"))
		if planID == "" {
			planID = strings.TrimSpace(mapString(args, "id"))
		}
		var patch sessionruntime.PlanPatch
		if planPatchArgsPresent(args, action) {
			var err error
			patch, err = planPatchFromManageArgs(args, action)
			if err != nil {
				return "", err
			}
		}
		title := strings.TrimSpace(mapString(args, "title"))
		status := strings.TrimSpace(mapString(args, "status"))
		approvalState := strings.TrimSpace(mapString(args, "approval_state"))
		updateSummary := strings.TrimSpace(firstNonEmptyString(mapString(args, "update_summary"), mapString(args, "summary")))
		updateScope := strings.TrimSpace(firstNonEmptyString(mapString(args, "update_scope"), mapString(args, "scope")))
		updateKind := strings.TrimSpace(firstNonEmptyString(mapString(args, "update_kind"), mapString(args, "kind")))
		revisionKind := strings.TrimSpace(mapString(args, "revision_kind"))
		checkpoint := mapBool(args, "checkpoint")
		document, err := planDocumentFromArgs(args)
		if err != nil {
			return "", err
		}
		documentPatch, err := planDocumentPatchFromArgs(args)
		if err != nil {
			return "", err
		}
		if documentPatch != nil && documentPatch.Operation == "" {
			documentPatch.Operation = action
		}
		if lifecycleRun.Inline && (action == "start_checkpoint" || action == "continue_checkpoint") {
			plan, err := s.prepareProviderManagedCheckpointStart(sessionID, planID, documentPatch)
			if err != nil {
				return "", err
			}
			payload := map[string]any{
				"tool":                      "plan_manage",
				"action":                    action,
				"status":                    "ok",
				"plan":                      plan,
				"checkpoint_start_deferred": true,
				"path_id":                   "tool.plan-manage.v3",
				"summary":                   "validated checkpoint start; the executor will assign fresh run ownership",
				"details_truncated":         false,
			}
			addPlanExecutionPayloadFields(payload, action, plan.Document)
			return marshalPlanManagePayload(payload)
		}
		var activate *bool
		if _, hasActivate := args["activate"]; hasActivate {
			value := mapBool(args, "activate")
			activate = &value
		}
		plan, event, err := s.sessions.PatchPlan(sessionID, sessionruntime.PlanPatchOptions{PlanID: planID, Title: title, Status: status, ApprovalState: approvalState, Activate: activate, Patch: patch, Document: document, DocumentPatch: documentPatch, Metadata: sessionruntime.PlanSaveMetadata{UpdateSummary: updateSummary, UpdateScope: updateScope, UpdateKind: updateKind, RevisionKind: revisionKind, Checkpoint: checkpoint}})
		if err != nil {
			return "", err
		}
		if err := s.persistPlanSavedV3Mutation(plan, event, applySessionMutation); err != nil {
			return "", err
		}
		payload := map[string]any{
			"tool":              "plan_manage",
			"action":            action,
			"status":            "ok",
			"plan":              plan,
			"path_id":           "tool.plan-manage.v3",
			"summary":           fmt.Sprintf("patched plan %s", plan.ID),
			"details_truncated": false,
		}
		if documentPatch != nil {
			if documentPatch.Recommendation != nil {
				payload["recommendation"] = documentPatch.Recommendation
			}
			for key, value := range map[string]string{
				"checkpoint_id":     documentPatch.CheckpointID,
				"attempt_id":        documentPatch.AttemptID,
				"run_id":            documentPatch.RunID,
				"run_session_id":    documentPatch.RunSessionID,
				"parent_session_id": documentPatch.ParentSessionID,
			} {
				if trimmed := strings.TrimSpace(value); trimmed != "" {
					payload[key] = trimmed
				}
			}
			if strings.TrimSpace(documentPatch.Report) != "" {
				payload["report"] = strings.TrimSpace(documentPatch.Report)
			}
			if strings.TrimSpace(documentPatch.Result) != "" {
				payload["result"] = strings.TrimSpace(documentPatch.Result)
			}
			if len(documentPatch.ChangedFiles) > 0 {
				payload["changed_files"] = trimStringSliceForPrompt(documentPatch.ChangedFiles)
			}
			if len(documentPatch.Validation) > 0 {
				payload["validation"] = trimStringSliceForPrompt(documentPatch.Validation)
			}
		}
		executionAction := action
		if action == "complete_subtask" && documentPatch != nil && documentPatch.CompleteCheckpoint {
			executionAction = "complete_checkpoint"
			payload["requested_action"] = action
			payload["action"] = executionAction
			payload["checkpoint_completed"] = true
		}
		addPlanExecutionPayloadFields(payload, executionAction, plan.Document)
		return marshalPlanManagePayload(payload)
	case "new":
		if document, err := planDocumentFromArgs(args); err != nil {
			return "", err
		} else if document != nil || strings.TrimSpace(mapString(args, "plan")) != "" {
			if session, ok, err := s.sessions.GetSession(sessionID); err != nil {
				return "", err
			} else if ok && sessionruntime.NormalizeMode(session.Mode) == sessionruntime.ModeAuto {
				return "", errors.New("plan_manage new is only for an empty draft shell; in auto mode with no active plan, propose a multi-checkpoint plan with plan_manage request_new_plan so the user can approve it and execution can start, or use start_session_checkpoint for a single bounded checkpoint")
			}
		}
		title := strings.TrimSpace(mapString(args, "title"))
		if title == "" {
			title = "New Plan"
		}
		override := mapBool(args, "override")
		document, err := planDocumentFromArgs(args)
		if err != nil {
			return "", err
		}
		plan, _, err := s.sessions.StartNewPlan(sessionID, title, sessionruntime.StartNewPlanOptions{Override: override, Document: document})
		if err != nil {
			return "", err
		}
		payload := map[string]any{
			"tool":              "plan_manage",
			"action":            "new",
			"status":            "ok",
			"override":          override,
			"plan":              plan,
			"path_id":           "tool.plan-manage.v3",
			"summary":           fmt.Sprintf("created plan %s", plan.ID),
			"details_truncated": false,
		}
		if override {
			payload["warning"] = "override=true intentionally created a new active plan even though this session may already have had an active plan"
		}
		return marshalPlanManagePayload(payload)
	default:
		return "", fmt.Errorf("plan_manage action %q is not supported", action)
	}

	plan, ok, err := s.sessions.GetActivePlan(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		payload := map[string]any{
			"tool":              "plan_manage",
			"action":            "get-active",
			"status":            "empty",
			"path_id":           "tool.plan-manage.v3",
			"summary":           "no active plan",
			"details_truncated": false,
		}
		return marshalPlanManagePayload(payload)
	}
	payload := map[string]any{
		"tool":              "plan_manage",
		"action":            "get-active",
		"status":            "ok",
		"plan":              plan,
		"revision":          currentPlanRevision(plan),
		"current_revision":  currentPlanRevision(plan),
		"base_revision":     currentPlanRevision(plan),
		"path_id":           "tool.plan-manage.v3",
		"summary":           fmt.Sprintf("active plan is %s", plan.ID),
		"details_truncated": false,
	}
	return marshalPlanManagePayload(payload)
}

func planPatchFromManageArgs(args map[string]any, action string) (sessionruntime.PlanPatch, error) {
	patch := sessionruntime.PlanPatch{
		Operation:     strings.TrimSpace(firstNonEmptyString(mapString(args, "operation"), mapString(args, "patch_operation"), mapString(args, "op"))),
		Section:       strings.TrimSpace(firstNonEmptyString(mapString(args, "section"), mapString(args, "update_scope"), mapString(args, "scope"))),
		OldText:       rawStringArg(args, "old_text"),
		NewText:       rawStringArg(args, "new_text"),
		Text:          rawStringArg(args, "text"),
		ChecklistItem: strings.TrimSpace(firstNonEmptyString(mapString(args, "checklist_item"), mapString(args, "item"))),
		ReplaceAll:    mapBool(args, "replace_all"),
	}
	if action == "update_section" && patch.Operation == "" {
		patch.Operation = "replace_section"
	}
	if patch.Operation == "" {
		patch.Operation = strings.TrimSpace(mapString(args, "patch_action"))
	}
	if value, ok := args["checked"]; ok {
		checked, ok := value.(bool)
		if !ok {
			return sessionruntime.PlanPatch{}, errors.New("plan_manage patch checked must be boolean")
		}
		patch.Checked = &checked
	}
	if rawPatch, ok := args["patch"]; ok {
		patchMap, ok := rawPatch.(map[string]any)
		if !ok {
			return sessionruntime.PlanPatch{}, errors.New("plan_manage patch must be an object")
		}
		nested, err := planPatchFromManageArgs(patchMap, action)
		if err != nil {
			return sessionruntime.PlanPatch{}, err
		}
		patch = mergePlanPatch(patch, nested)
	}
	if patch.IsZero() {
		return sessionruntime.PlanPatch{}, errors.New("plan_manage patch requires edit fields such as old_text/new_text, section/new_text, text, checklist_item, or checked")
	}
	return patch, nil
}

func mergePlanPatch(base, overlay sessionruntime.PlanPatch) sessionruntime.PlanPatch {
	if strings.TrimSpace(overlay.Operation) != "" {
		base.Operation = overlay.Operation
	}
	if strings.TrimSpace(overlay.Section) != "" {
		base.Section = overlay.Section
	}
	if !reflect.ValueOf(overlay.OldText).IsZero() {
		base.OldText = overlay.OldText
	}
	if !reflect.ValueOf(overlay.NewText).IsZero() {
		base.NewText = overlay.NewText
	}
	if !reflect.ValueOf(overlay.Text).IsZero() {
		base.Text = overlay.Text
	}
	if strings.TrimSpace(overlay.ChecklistItem) != "" {
		base.ChecklistItem = overlay.ChecklistItem
	}
	if overlay.Checked != nil {
		base.Checked = overlay.Checked
	}
	if overlay.ReplaceAll {
		base.ReplaceAll = true
	}
	return base
}

func rawStringArg(payload map[string]any, key string) string {
	value, ok := payload[key]
	if !ok {
		return ""
	}
	typed, ok := value.(string)
	if !ok {
		return ""
	}
	return typed
}

func currentPlanRevision(plan pebblestore.SessionPlanSnapshot) int {
	if plan.Version > 0 {
		return plan.Version
	}
	return 1
}

func planManagePlanSummary(plan pebblestore.SessionPlanSnapshot, includePreview bool) map[string]any {
	item := map[string]any{
		"id":               plan.ID,
		"title":            plan.Title,
		"status":           plan.Status,
		"approval_state":   plan.ApprovalState,
		"active":           plan.Active,
		"updated_at":       plan.UpdatedAt,
		"version":          plan.Version,
		"revision":         currentPlanRevision(plan),
		"current_revision": currentPlanRevision(plan),
		"base_revision":    currentPlanRevision(plan),
		"parent_revision":  plan.ParentRevision,
		"update_summary":   plan.UpdateSummary,
		"update_scope":     plan.UpdateScope,
		"update_kind":      plan.UpdateKind,
		"checkpoint":       plan.Checkpoint,
	}
	if includePreview {
		item["preview"] = truncateRunes(plan.Plan, 180)
	}
	return item
}

func planManagePlanRevisionSummary(plan pebblestore.SessionPlanSnapshot) map[string]any {
	item := planManagePlanSummary(plan, true)
	item["created_at"] = plan.CreatedAt
	item["plan"] = plan.Plan
	if plan.PriorTitle != "" {
		item["prior_title"] = plan.PriorTitle
	}
	if plan.PriorPlan != "" {
		item["prior_plan"] = plan.PriorPlan
	}
	if len(plan.DiffLines) > 0 {
		item["diff_lines"] = append([]string(nil), plan.DiffLines...)
	}
	return item
}

func (s *Service) executePlanLifecycleControlAction(sessionID, action string, args map[string]any, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error), lifecycleRun planLifecycleRunContext) (string, error) {
	// A caller can deliberately ask for a handoff boundary, but cannot request
	// inline ownership without trusted provider-run context.
	if mapBool(args, "fresh_context") || strings.EqualFold(strings.TrimSpace(mapString(args, "execution_context")), "fresh") {
		lifecycleRun.Inline = false
	}
	planID := strings.TrimSpace(firstNonEmptyString(mapString(args, "plan_id"), mapString(args, "id")))
	checkpointID := strings.TrimSpace(firstNonEmptyString(mapString(args, "checkpoint_id"), mapString(args, "active_checkpoint_id"), mapString(args, "active_checkpoint")))
	document, err := planDocumentFromArgs(args)
	if err != nil {
		return "", err
	}
	continueAutomatically := (*bool)(nil)
	if _, ok := args["continue_automatically"]; ok {
		value := mapBool(args, "continue_automatically")
		continueAutomatically = &value
	}
	input := sessionruntime.PlanLifecycleExecutionInput{
		SessionID:             sessionID,
		PlanID:                planID,
		CheckpointID:          checkpointID,
		ExecutionGranularity:  strings.TrimSpace(firstNonEmptyString(mapString(args, "execution_granularity"), mapString(args, "granularity"), mapString(args, "execution_shape"), mapString(args, "shape"))),
		ContinuationPolicy:    strings.TrimSpace(firstNonEmptyString(mapString(args, "continuation_policy"), mapString(args, "continuation"), mapString(args, "mode"))),
		ContinueAutomatically: continueAutomatically,
	}
	lifecycle := sessionruntime.NewPlanLifecycleService(s.sessions)
	if s != nil && s.uiSettings != nil {
		lifecycle.SetGlobalFollowupCheckpointPolicyResolver(func(accountScopeID string) (string, error) {
			settings, err := s.uiSettings.GetForAccount(accountScopeID)
			if err != nil {
				return "", err
			}
			return settings.Chat.FollowupCheckpointPolicyDefault, nil
		})
	}
	var result sessionruntime.PlanLifecycleResult
	switch action {
	case "approve_and_start":
		if session, ok, getErr := s.sessions.GetSession(sessionID); getErr != nil {
			return "", getErr
		} else if ok && sessionruntime.NormalizeMode(session.Mode) == sessionruntime.ModePlan {
			input.PlanID = planID
			result, err = lifecycle.SubmitPlanForApproval(sessionruntime.PlanLifecyclePlanInput{
				SessionID:             input.SessionID,
				PlanID:                input.PlanID,
				AgentCanSubmit:        true,
				ExecutionGranularity:  input.ExecutionGranularity,
				ContinuationPolicy:    input.ContinuationPolicy,
				ContinueAutomatically: input.ContinueAutomatically,
			})
		} else {
			result, err = lifecycle.StartPlanCheckpointed(input)
		}
	case "restart_checkpoint":
		input.ReplacementRequest = strings.TrimSpace(firstNonEmptyString(mapString(args, "change_request"), mapString(args, "user_request"), mapString(args, "request"), mapString(args, "prompt"), mapString(args, "text")))
		input.ReplacementTitle = strings.TrimSpace(firstNonEmptyString(mapString(args, "checkpoint_title"), mapString(args, "title")))
		input.ReplacementTasks = mapStringSlice(args, "tasks")
		input.ReplacementCriteria = mapStringSlice(args, "acceptance_criteria")
		input.ReplacementNotes = strings.TrimSpace(firstNonEmptyString(mapString(args, "notes"), mapString(args, "handoff_notes"), mapString(args, "context")))
		input.ReplacementSourceID = strings.TrimSpace(firstNonEmptyString(mapString(args, "source_message_id"), mapString(args, "source_message")))
		result, err = lifecycle.RestartCheckpointFromZero(input)
	case "rewind_to_checkpoint":
		result, err = lifecycle.RewindToCheckpoint(input)
	case "resolve_blocked_checkpoint":
		input.Result = strings.TrimSpace(firstNonEmptyString(mapString(args, "result"), mapString(args, "resolution_result")))
		input.Notes = strings.TrimSpace(firstNonEmptyString(mapString(args, "notes"), mapString(args, "resolution_notes"), mapString(args, "report")))
		input.ReviewedAt = int64(mapInt(args, "reviewed_at"))
		input.StartNext = mapBool(args, "start_next") || mapBool(args, "continue_next")
		if input.StartNext {
			if strings.TrimSpace(input.RunID) == "" {
				input.RunID = strings.TrimSpace(mapString(args, "run_id"))
			}
			if strings.TrimSpace(input.RunSessionID) == "" {
				input.RunSessionID = strings.TrimSpace(firstNonEmptyString(mapString(args, "run_session_id"), mapString(args, "session_id")))
			}
			if strings.TrimSpace(input.ParentSessionID) == "" {
				input.ParentSessionID = strings.TrimSpace(mapString(args, "parent_session_id"))
			}
			input.StartedAt = int64(mapInt(args, "started_at"))
			input.AttemptID = strings.TrimSpace(mapString(args, "attempt_id"))
		}
		result, err = lifecycle.ResolveBlockedCheckpoint(input)
	case "start_session_checkpoint":
		input := sessionruntime.PlanLifecycleSessionCheckpointInput{
			SessionID:          sessionID,
			ChangeRequest:      strings.TrimSpace(mapString(args, "change_request")),
			UserRequest:        strings.TrimSpace(firstNonEmptyString(mapString(args, "user_request"), mapString(args, "request"), mapString(args, "prompt"), mapString(args, "text"))),
			Title:              strings.TrimSpace(firstNonEmptyString(mapString(args, "checkpoint_title"), mapString(args, "title"))),
			CheckpointID:       strings.TrimSpace(firstNonEmptyString(mapString(args, "checkpoint_id"), mapString(args, "id"))),
			Tasks:              mapStringSlice(args, "tasks"),
			AcceptanceCriteria: mapStringSlice(args, "acceptance_criteria"),
			Notes:              strings.TrimSpace(firstNonEmptyString(mapString(args, "notes"), mapString(args, "handoff_notes"), mapString(args, "context"))),
			SourceMessageID:    strings.TrimSpace(firstNonEmptyString(mapString(args, "source_message_id"), mapString(args, "source_message"), lifecycleRun.SourceMessageID)),
			RunID:              strings.TrimSpace(firstNonEmptyString(lifecycleRun.RunID, mapString(args, "run_id"))),
			RunSessionID:       strings.TrimSpace(firstNonEmptyString(lifecycleRun.RunSessionID, mapString(args, "run_session_id"), mapString(args, "session_id"))),
			ParentSessionID:    strings.TrimSpace(firstNonEmptyString(lifecycleRun.ParentSessionID, mapString(args, "parent_session_id"))),
			StartedAt:          int64(mapInt(args, "started_at")),
			AttemptID:          strings.TrimSpace(mapString(args, "attempt_id")),
		}
		result, err = lifecycle.StartSessionCheckpoint(input)
	case "request_followup_checkpoint":
		input := sessionruntime.PlanLifecycleFollowupCheckpointInput{
			SessionID:          sessionID,
			PlanID:             planID,
			ChangeRequest:      strings.TrimSpace(mapString(args, "change_request")),
			UserRequest:        strings.TrimSpace(firstNonEmptyString(mapString(args, "user_request"), mapString(args, "request"), mapString(args, "prompt"), mapString(args, "text"))),
			Title:              strings.TrimSpace(firstNonEmptyString(mapString(args, "checkpoint_title"), mapString(args, "title"))),
			Tasks:              mapStringSlice(args, "tasks"),
			AcceptanceCriteria: mapStringSlice(args, "acceptance_criteria"),
			Notes:              strings.TrimSpace(firstNonEmptyString(mapString(args, "notes"), mapString(args, "handoff_notes"), mapString(args, "context"))),
			SourceMessageID:    strings.TrimSpace(firstNonEmptyString(mapString(args, "source_message_id"), mapString(args, "source_message"))),
			ApprovalConfirmed:  mapBool(args, "approval_confirmed"),
			RunID:              strings.TrimSpace(mapString(args, "run_id")),
			RunSessionID:       strings.TrimSpace(firstNonEmptyString(mapString(args, "run_session_id"), mapString(args, "session_id"))),
			ParentSessionID:    strings.TrimSpace(mapString(args, "parent_session_id")),
			StartedAt:          int64(mapInt(args, "started_at")),
			AttemptID:          strings.TrimSpace(mapString(args, "attempt_id")),
		}
		result, err = lifecycle.RequestFollowupCheckpoint(input)
	case "request_plan_revision":
		result, err = lifecycle.RequestPlanRevision(sessionruntime.PlanLifecycleProposalInput{SessionID: sessionID, PlanID: planID, Title: strings.TrimSpace(mapString(args, "title")), Plan: strings.TrimSpace(mapString(args, "plan")), Document: document, Reason: strings.TrimSpace(firstNonEmptyString(mapString(args, "reason"), mapString(args, "update_summary"), mapString(args, "summary")))})
	case "amend_plan":
		result, err = lifecycle.AmendPlan(sessionruntime.PlanLifecycleAmendmentInput{SessionID: sessionID, PlanID: planID, Title: strings.TrimSpace(mapString(args, "title")), Plan: strings.TrimSpace(mapString(args, "plan")), Document: document, BaseRevision: mapInt(args, "base_revision"), UpdateSummary: strings.TrimSpace(firstNonEmptyString(mapString(args, "update_summary"), mapString(args, "summary"), mapString(args, "reason"))), ReplaceFromCheckpointID: strings.TrimSpace(firstNonEmptyString(mapString(args, "replace_from_checkpoint_id"), mapString(args, "checkpoint_id"))), AmendFutureCheckpoints: mapBool(args, "amend_future_checkpoints"), OverrideStale: mapBool(args, "override_stale")})
	case "request_new_plan":
		continuation := strings.TrimSpace(firstNonEmptyString(mapString(args, "continuation_policy"), mapString(args, "continuation"), mapString(args, "mode")))
		continueAutomatically := (*bool)(nil)
		if _, ok := args["continue_automatically"]; ok {
			value := mapBool(args, "continue_automatically")
			continueAutomatically = &value
			if value {
				continuation = sessionruntime.PlanAcceptanceContinuationAutomatic
			} else {
				continuation = sessionruntime.PlanAcceptanceContinuationReviewEachCheckpoint
			}
		}
		result, err = lifecycle.RequestNewPlan(sessionruntime.PlanLifecycleProposalInput{SessionID: sessionID, PlanID: planID, Title: strings.TrimSpace(mapString(args, "title")), Plan: strings.TrimSpace(mapString(args, "plan")), Document: document, Reason: strings.TrimSpace(firstNonEmptyString(mapString(args, "reason"), mapString(args, "update_summary"), mapString(args, "summary"))), ApprovalConfirmed: mapBool(args, "approval_confirmed"), ExecutionGranularity: strings.TrimSpace(firstNonEmptyString(mapString(args, "execution_granularity"), mapString(args, "granularity"), mapString(args, "execution_shape"), mapString(args, "shape"))), ContinuationPolicy: continuation, ContinueAutomatically: continueAutomatically})
	default:
		return "", fmt.Errorf("plan execution action %q is not supported", action)
	}
	if err != nil {
		return "", err
	}
	if err := s.persistPlanSavedV3Mutation(result.Plan, result.PlanEvent, applySessionMutation); err != nil {
		return "", err
	}
	payload := map[string]any{
		"tool":              "plan_manage",
		"action":            action,
		"status":            "ok",
		"plan":              result.Plan,
		"execution_summary": result.Summary,
		"path_id":           "tool.plan-manage.v3",
		"summary":           result.Message,
		"details_truncated": false,
	}
	if !strings.EqualFold(strings.TrimSpace(result.Plan.ApprovalState), "approved") {
		payload["next_action"] = "await_approval"
	} else if action == "resolve_blocked_checkpoint" && !input.StartNext {
		payload["next_checkpoint_id"] = result.Summary.NextCheckpointID
		if result.Summary.PlanComplete {
			payload["next_action"] = "plan_complete"
		} else if result.Summary.ReviewRequired {
			payload["next_action"] = "await_review"
		} else {
			payload["next_action"] = "blocked_resolved"
		}
	} else if result.Summary.PlanComplete {
		payload["next_action"] = "plan_complete"
	} else if result.Summary.ReviewRequired {
		payload["next_action"] = "await_review"
	} else if result.Summary.Blocked || result.Summary.Failed {
		payload["next_action"] = "stopped"
	} else if result.Summary.NextCheckpointID != "" {
		payload["checkpoint_id"] = result.Summary.NextCheckpointID
		if lifecycleRun.Inline && (action == "start_session_checkpoint" || action == "request_followup_checkpoint") && result.Plan.Document != nil && result.Plan.Document.ExecutionState != nil && sessionruntime.NormalizePlanExecutionOrigin(result.Plan.Document.ExecutionOrigin) == sessionruntime.PlanExecutionOriginAutoSession && strings.TrimSpace(result.Plan.Document.ExecutionState.CurrentRunID) == strings.TrimSpace(lifecycleRun.RunID) {
			payload["next_action"] = "continue_current_run"
			payload["context_preserved"] = true
			payload["run_ownership"] = map[string]any{"run_id": lifecycleRun.RunID, "checkpoint_id": result.Summary.NextCheckpointID, "attempt_id": result.AttemptID}
		} else {
			payload["next_action"] = "run_checkpoint_with_fresh_context"
			payload["run_request"] = planCheckpointRunRequestPayload(result.Plan.ID, result.Summary.NextCheckpointID, result.AttemptID)
		}
	}
	return marshalPlanManagePayload(payload)
}

func (s *Service) prepareProviderManagedCheckpointStart(sessionID, planID string, patch *sessionruntime.PlanDocumentPatch) (pebblestore.SessionPlanSnapshot, error) {
	var (
		plan pebblestore.SessionPlanSnapshot
		ok   bool
		err  error
	)
	if strings.TrimSpace(planID) == "" {
		plan, ok, err = s.sessions.GetActivePlan(sessionID)
	} else {
		plan, ok, err = s.sessions.GetPlan(sessionID, planID)
	}
	if err != nil {
		return pebblestore.SessionPlanSnapshot{}, err
	}
	if !ok || plan.Document == nil {
		return pebblestore.SessionPlanSnapshot{}, errors.New("checkpoint start requires an active structured plan")
	}
	if !strings.EqualFold(strings.TrimSpace(plan.ApprovalState), "approved") {
		return pebblestore.SessionPlanSnapshot{}, errors.New("checkpoint start requires an approved plan")
	}
	preview, err := clonePlanDocumentForExecutionAction(plan.Document)
	if err != nil {
		return pebblestore.SessionPlanSnapshot{}, err
	}
	if err := sessionruntime.ValidateExecutablePlanDocument(preview); err != nil {
		return pebblestore.SessionPlanSnapshot{}, err
	}
	checkpointID := ""
	if patch != nil {
		checkpointID = strings.TrimSpace(patch.CheckpointID)
	}
	if _, err := sessionruntime.ApplyPlanCheckpointStart(preview, sessionruntime.PlanCheckpointStartOptions{CheckpointID: checkpointID}); err != nil {
		return pebblestore.SessionPlanSnapshot{}, err
	}
	return plan, nil
}

func planCheckpointRunRequestPayload(planID, checkpointID, attemptID string) map[string]any {
	ctx := map[string]any{
		"plan_id":       strings.TrimSpace(planID),
		"checkpoint_id": strings.TrimSpace(checkpointID),
	}
	if trimmed := strings.TrimSpace(attemptID); trimmed != "" {
		ctx["attempt_id"] = trimmed
	}
	return map[string]any{
		"plan_checkpoint_context": ctx,
	}
}

func addPlanExecutionPayloadFields(payload map[string]any, action string, doc *pebblestore.SessionPlanDocument) {
	if payload == nil || doc == nil {
		return
	}
	summary := sessionruntime.SummarizePlanExecution(doc)
	payload["execution_summary"] = summary
	switch action {
	case "start_checkpoint", "continue_checkpoint", "restart_checkpoint", "rewind_to_checkpoint":
		if summary.NextCheckpointID != "" {
			payload["checkpoint_id"] = summary.NextCheckpointID
		}
		payload["next_action"] = "run_checkpoint_with_fresh_context"
	case "accept_checkpoint", "complete_checkpoint", "checkpoint_outcome", "mark_needs_review", "mark_blocked", "mark_failed":
		payload["next_checkpoint_id"] = summary.NextCheckpointID
		if summary.PlanComplete {
			payload["next_action"] = "plan_complete"
		} else if summary.ReviewRequired {
			payload["next_action"] = "await_review"
		} else if summary.Blocked || summary.Failed {
			payload["next_action"] = "stopped"
		} else if summary.AutoAdvanceAllowed && summary.NextCheckpointID != "" {
			payload["next_action"] = "run_checkpoint_with_fresh_context"
			payload["auto_advance"] = true
			payload["run_request"] = planCheckpointRunRequestPayload(doc.ID, summary.NextCheckpointID, "")
		} else if summary.NextCheckpointID != "" {
			payload["next_action"] = "continue_checkpoint"
		}
	}
}

func addPlanRunRequestPayloadFields(payload map[string]any, planID string, doc *pebblestore.SessionPlanDocument) {
	if payload == nil || doc == nil {
		return
	}
	summary := sessionruntime.SummarizePlanExecution(doc)
	payload["execution_summary"] = summary
	if summary.PlanComplete {
		payload["next_action"] = "plan_complete"
		return
	}
	if summary.ReviewRequired {
		payload["next_action"] = "await_review"
		return
	}
	if summary.Blocked || summary.Failed {
		payload["next_action"] = "stopped"
		return
	}
	if strings.TrimSpace(summary.NextCheckpointID) == "" {
		return
	}
	payload["checkpoint_id"] = strings.TrimSpace(summary.NextCheckpointID)
	payload["next_action"] = "run_checkpoint_with_fresh_context"
	payload["run_request"] = planCheckpointRunRequestPayload(planID, summary.NextCheckpointID, "")
}

func marshalPlanManagePayload(payload map[string]any) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

type taskExecutionRequest struct {
	Parsed               taskCallArguments
	ParsedProvided       bool
	DescriptionOverride  string
	PromptOverride       string
	ParentSession        *pebblestore.SessionSnapshot
	ParentMessages       []pebblestore.MessageSnapshot
	ParentActivePlan     *pebblestore.SessionPlanSnapshot
	PermissionSessionID  string
	TargetedSubagentName string
	ApprovedArguments    string
	RunID                string
	Principal            identity.Principal
	ApplySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)
}

func executeTaskLaunchesInParallel[T any](ctx context.Context, launchCount int, runOne func(context.Context, int) (T, error)) ([]T, []error) {
	if launchCount <= 0 {
		return nil, nil
	}
	results := make([]T, launchCount)
	errs := make([]error, launchCount)
	var wg sync.WaitGroup
	for i := 0; i < launchCount; i++ {
		idx := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := runOne(ctx, idx)
			results[idx] = res
			errs[idx] = err
		}()
	}
	wg.Wait()
	return results, errs
}

func (s *Service) executeTaskTool(ctx context.Context, sessionID, sessionMode string, step int, call tool.Call, emit StreamHandler) (string, error) {
	principal, _ := identity.PrincipalFromContext(ctx)
	return s.executeTaskToolWithParsed(ctx, sessionID, sessionMode, step, call, emit, taskExecutionRequest{Principal: principal})
}

func (s *Service) executeTaskToolWithParsed(ctx context.Context, sessionID, sessionMode string, step int, call tool.Call, emit StreamHandler, req taskExecutionRequest) (string, error) {
	if s.sessions == nil {
		return "", errors.New("session service is not configured")
	}
	var err error
	parsed := req.Parsed
	if !req.ParsedProvided {
		parsed, err = parseTaskCallArguments(call.Arguments)
		if err != nil {
			return "", err
		}
	}

	action := parsed.Action
	if strings.TrimSpace(action) == "" {
		action = "spawn"
	}
	description := parsed.Description
	if strings.TrimSpace(req.DescriptionOverride) != "" {
		description = strings.TrimSpace(req.DescriptionOverride)
	}
	prompt := parsed.Prompt
	if strings.TrimSpace(req.PromptOverride) != "" {
		prompt = strings.TrimSpace(req.PromptOverride)
	}
	launchSpecs := append([]taskLaunchSpec(nil), parsed.Launches...)
	if len(launchSpecs) == 0 {
		return "", errors.New("task requires at least one validated launch")
	}
	if strings.TrimSpace(req.TargetedSubagentName) != "" {
		launchSpecs = []taskLaunchSpec{{
			RequestedSubagentType: strings.TrimSpace(req.TargetedSubagentName),
			MetaPrompt:            strings.TrimSpace(parsed.Prompt),
		}}
	}

	parentSession := pebblestore.SessionSnapshot{}
	if req.ParentSession != nil {
		parentSession = *req.ParentSession
	} else {
		var ok bool
		parentSession, ok, err = s.sessions.GetSession(sessionID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("session %q not found", sessionID)
		}
	}
	if err := validatePlanSidechatTaskTargets(parentSession, launchSpecs); err != nil {
		return "", err
	}
	taskCallID := strings.TrimSpace(call.CallID)
	if taskCallID == "" {
		taskCallID = fmt.Sprintf("task_%d", time.Now().UnixMilli())
	}
	reservationFinished := false
	if s.permissions != nil && strings.TrimSpace(req.RunID) != "" {
		defer func() {
			if !reservationFinished {
				_ = s.permissions.FinishSubagentWave(parentSession.ID, req.RunID, taskCallID, "failed")
			}
		}()
	}

	parentMessages := append([]pebblestore.MessageSnapshot(nil), req.ParentMessages...)
	parentActivePlan := req.ParentActivePlan
	if len(parentMessages) == 0 {
		delegationContext, contextErr := s.loadTaskDelegationContext(parentSession.ID)
		if contextErr != nil {
			return "", contextErr
		}
		parentMessages = delegationContext.ParentMessages
		if parentActivePlan == nil {
			parentActivePlan = delegationContext.ActivePlan
		}
	} else if parentActivePlan == nil {
		parentActivePlan, err = s.activePlanForDelegation(parentSession.ID)
		if err != nil {
			return "", err
		}
	}

	trustedProfiles := make([]*pebblestore.AgentProfile, len(launchSpecs))
	trustedVirtualTargets := make([]bool, len(launchSpecs))
	trustedSources := make([]string, len(launchSpecs))
	if approved := strings.TrimSpace(req.ApprovedArguments); approved != "" {
		var envelope struct {
			ManifestHash string             `json:"manifest_hash"`
			Manifest     taskLaunchManifest `json:"manifest"`
		}
		if err := json.Unmarshal([]byte(approved), &envelope); err != nil {
			return "", fmt.Errorf("approved task manifest invalid: %w", err)
		}
		digest, err := taskLaunchManifestDigest(envelope.Manifest)
		if err != nil || digest != strings.TrimSpace(envelope.ManifestHash) || digest != strings.TrimSpace(envelope.Manifest.ManifestHash) {
			return "", errors.New("approved task manifest snapshot hash mismatch")
		}
		if len(envelope.Manifest.Launches) != len(launchSpecs) {
			return "", errors.New("approved task manifest launch count mismatch")
		}
		for i := range launchSpecs {
			row := envelope.Manifest.Launches[i]
			if !strings.EqualFold(strings.TrimSpace(row.RequestedSubagentType), strings.TrimSpace(launchSpecs[i].RequestedSubagentType)) {
				return "", fmt.Errorf("approved task manifest launch %d target mismatch", i)
			}
			if row.ProfileSnapshot == nil {
				return "", fmt.Errorf("approved task manifest launch %d is missing profile snapshot", i)
			}
			profile, err := cloneTaskAgentProfile(*row.ProfileSnapshot)
			if err != nil {
				return "", err
			}
			if agentruntime.IsCloneAgentName(launchSpecs[i].RequestedSubagentType) {
				if !row.ParentCopy || !agentruntime.IsCloneAgentName(row.ResolvedAgentName) {
					return "", fmt.Errorf("approved task manifest launch %d does not identify compiled Clone", i)
				}
				profile, err = s.agents.ReconcileSystemAgentSnapshot(agentruntime.CloneAgentID, profile)
				if err != nil {
					return "", fmt.Errorf("reconcile compiled Clone launch snapshot: %w", err)
				}
				trustedVirtualTargets[i] = true
			} else {
				trustedVirtualTargets[i] = row.ParentCopy
			}
			trustedProfiles[i] = &profile
			trustedSources[i] = strings.TrimSpace(row.SourceAgentName)
		}
	}

	cloneIndexes := make([]int, 0, len(launchSpecs))
	for i := range launchSpecs {
		if agentruntime.IsCloneAgentName(launchSpecs[i].RequestedSubagentType) {
			cloneIndexes = append(cloneIndexes, i)
		}
	}
	var cloneTaskBase *worktreeruntime.TaskBase
	if len(cloneIndexes) > 0 {
		if s.worktrees == nil {
			return "", errors.New("write-capable clones require separate worktree isolation")
		}
		resolved, resolveErr := s.worktrees.ResolveTaskBase(parentSession.WorkspacePath)
		if resolveErr != nil {
			return "", fmt.Errorf("task failed to resolve parent Git state before Clone execution: %w", resolveErr)
		}
		cloneTaskBase = &resolved
	}
	collisionWarnings := make([]string, 0)
	if len(cloneIndexes) > 1 {
		for _, index := range cloneIndexes {
			if len(launchSpecs[index].OwnedScope) == 0 {
				return "", fmt.Errorf("task launches[%d] clone requires declared owned_scope", index)
			}
		}
		for left := 0; left < len(cloneIndexes); left++ {
			for right := left + 1; right < len(cloneIndexes); right++ {
				if taskOwnedScopesOverlap(launchSpecs[cloneIndexes[left]].OwnedScope, launchSpecs[cloneIndexes[right]].OwnedScope) {
					collisionWarnings = append(collisionWarnings, fmt.Sprintf("owned scopes overlap between launches[%d] and launches[%d]; integrate these child commits sequentially and resolve conflicts explicitly", cloneIndexes[left], cloneIndexes[right]))
				}
			}
		}
	}

	prepared := make([]taskLaunchPrepared, 0, len(launchSpecs))
	for i := range launchSpecs {
		spec := launchSpecs[i]
		requestedSubagent := strings.TrimSpace(spec.RequestedSubagentType)
		if requestedSubagent == "" {
			return "", fmt.Errorf("task launches[%d] requires subagent_type, agent, or purpose", i)
		}
		metaPrompt := strings.TrimSpace(spec.MetaPrompt)
		if metaPrompt == "" {
			return "", fmt.Errorf("task launches[%d] requires meta_prompt or role assignment", i)
		}
		launch, prepareErr := s.prepareDelegatedSubagentLaunchWithProfile(parentSession, sessionMode, taskLaunchPrepared{
			LaunchIndex:       i + 1,
			VirtualTarget:     trustedVirtualTargets[i],
			TaskBase:          cloneTaskBase,
			RequestedSubagent: requestedSubagent,
			MetaPrompt:        metaPrompt,
			AssignmentLabel:   spec.AssignmentLabel,
		}, description, strings.TrimSpace(req.TargetedSubagentName), trustedProfiles[i], trustedSources[i], req.ApplySessionMutation)
		if prepareErr != nil {
			return "", prepareErr
		}
		prepared = append(prepared, launch)
	}

	taskToolName := strings.TrimSpace(call.Name)
	if taskToolName == "" {
		taskToolName = "task"
	}
	lineageUpdate := func(status string, launches []taskLaunchOutcome, extra map[string]any) {
		if s == nil || s.sessions == nil {
			return
		}
		metadata := cloneGenericMap(parentSession.Metadata)
		if metadata == nil {
			metadata = map[string]any{}
		}
		launchMap, _ := metadata["task_launches"].(map[string]any)
		launchMap = cloneGenericMap(launchMap)
		if launchMap == nil {
			launchMap = map[string]any{}
		}
		entry := map[string]any{
			"call_id":                 taskCallID,
			"status":                  strings.TrimSpace(status),
			"goal":                    description,
			"action":                  action,
			"parallel_launches":       true,
			"parallel_execution_mode": "all_at_once",
			"launch_count":            len(launches),
			"parent_session_id":       strings.TrimSpace(parentSession.ID),
			"collision_warnings":      append([]string(nil), collisionWarnings...),
		}
		if len(launches) > 0 {
			entry["subagent"] = strings.TrimSpace(launches[0].ResolvedSubagent)
			entry["requested_subagent"] = strings.TrimSpace(launches[0].RequestedSubagent)
			entry["assignment_label"] = strings.TrimSpace(launches[0].AssignmentLabel)
			entry["subagent_provider"] = strings.TrimSpace(launches[0].SubagentProvider)
			entry["subagent_model"] = strings.TrimSpace(launches[0].SubagentModel)
			entry["child_session_id"] = strings.TrimSpace(launches[0].ChildSessionID)
			entry["child_mode"] = strings.TrimSpace(launches[0].ChildMode)
			entry["workspace_path"] = strings.TrimSpace(launches[0].WorkspacePath)
			entry["workspace_name"] = strings.TrimSpace(launches[0].WorkspaceName)
			entry["worktree_enabled"] = launches[0].WorktreeEnabled
			entry["worktree_root_path"] = strings.TrimSpace(launches[0].WorktreeRootPath)
			entry["worktree_base_branch"] = strings.TrimSpace(launches[0].WorktreeBaseBranch)
			entry["worktree_branch"] = strings.TrimSpace(launches[0].WorktreeBranch)
		}
		launchRows := make([]map[string]any, 0, len(launches))
		for _, launch := range launches {
			elapsedMS, currentToolMS := taskLaunchProgressDurations(launch, strings.EqualFold(strings.TrimSpace(status), "ok") || strings.EqualFold(strings.TrimSpace(status), "error"))
			launchRow := map[string]any{
				"launch_index":         launch.LaunchIndex,
				"requested_subagent":   strings.TrimSpace(launch.RequestedSubagent),
				"subagent":             strings.TrimSpace(launch.ResolvedSubagent),
				"meta_prompt":          strings.TrimSpace(launch.MetaPrompt),
				"assignment_label":     strings.TrimSpace(launch.AssignmentLabel),
				"subagent_provider":    strings.TrimSpace(launch.SubagentProvider),
				"subagent_model":       strings.TrimSpace(launch.SubagentModel),
				"child_session_id":     strings.TrimSpace(launch.ChildSessionID),
				"child_mode":           strings.TrimSpace(launch.ChildMode),
				"workspace_path":       strings.TrimSpace(launch.WorkspacePath),
				"workspace_name":       strings.TrimSpace(launch.WorkspaceName),
				"worktree_enabled":     launch.WorktreeEnabled,
				"worktree_root_path":   strings.TrimSpace(launch.WorktreeRootPath),
				"worktree_base_branch": strings.TrimSpace(launch.WorktreeBaseBranch),
				"worktree_branch":      strings.TrimSpace(launch.WorktreeBranch),
				"parent_branch":        strings.TrimSpace(launch.ParentBranch),
				"base_commit":          strings.TrimSpace(launch.BaseCommit),
				"head_commit":          strings.TrimSpace(launch.HeadCommit),
				"worktree_clean":       launch.WorktreeClean,
				"git_status":           strings.TrimSpace(launch.GitStatus),
				"current_tool":         strings.TrimSpace(launch.CurrentTool),
				"current_tool_ms":      currentToolMS,
				"elapsed_ms":           elapsedMS,
				"tool_started":         launch.ToolStarted,
				"tool_completed":       launch.ToolCompleted,
				"tool_failed":          launch.ToolFailed,
				"tool_order":           append([]string(nil), launch.ToolOrder...),
				"error":                strings.TrimSpace(launch.Error),
				"reason":               strings.TrimSpace(launch.Reason),
				"phase":                strings.TrimSpace(launch.Phase),
			}
			if launch.ReportRef != nil {
				launchRow["report_ref"] = launch.ReportRef
				launchRow["report_persisted"] = true
			}
			launchRows = append(launchRows, launchRow)
		}
		if len(launchRows) > 0 {
			entry["launches"] = launchRows
		}
		for key, value := range cloneGenericMap(extra) {
			entry[key] = value
		}
		launchMap[taskCallID] = entry
		metadata["task_launches"] = launchMap
		updated, env, updateErr := s.sessions.UpdateMetadata(parentSession.ID, metadata)
		if updateErr != nil {
			return
		}
		parentSession = updated
		if env != nil {
			s.publishEventEnvelope(*env)
		}
	}
	emitTaskProgress := func(phase, summary string, launch taskLaunchOutcome) {
		phase = strings.TrimSpace(phase)
		launch.Phase = phase
		emitTaskStreamDelta(parentSession.ID, emit, step, taskToolName, taskCallID, action, description, len(prepared), launch, phase, summary)
	}

	spawned := make([]taskLaunchOutcome, 0, len(prepared))
	for i := range prepared {
		launch := buildTaskLaunchOutcome(prepared[i])
		spawned = append(spawned, launch)
		emitTaskProgress("spawned", fmt.Sprintf("spawned launch %d %s subagent in %s", launch.LaunchIndex, launch.ResolvedSubagent, launch.ChildMode), launch)
	}
	lineageUpdate("spawned", spawned, nil)

	outcomes, runErrs := executeTaskLaunchesInParallel(ctx, len(prepared), func(runCtx context.Context, idx int) (taskLaunchOutcome, error) {
		launch := prepared[idx]
		outcome := buildTaskLaunchOutcome(launch)
		metaPrompt := strings.TrimSpace(outcome.MetaPrompt)
		perLaunchPrompt := prompt
		if metaPrompt != "" {
			perLaunchPrompt = "Meta-prompt:\n" + metaPrompt + "\n\nPrompt:\n" + prompt
		}
		delegatedPrompt := buildTaskDelegationPrompt(taskDelegationPromptConfig{
			Description:          description,
			Prompt:               perLaunchPrompt,
			ParentSession:        parentSession,
			ParentMessages:       parentMessages,
			ParentActivePlan:     parentActivePlan,
			PermissionSessionID:  req.PermissionSessionID,
			TargetedSubagentName: req.TargetedSubagentName,
		})
		subResult, runErr := s.RunTurnStreaming(runCtx, launch.ChildSession.ID, RunRequest{
			Prompt:     delegatedPrompt,
			TargetKind: RunTargetKindSubagent,
			TargetName: launch.SubagentProfile.Name,
			AgentName:  launch.SubagentProfile.Name,
		}, RunStartMeta{
			AllowSubagent:        true,
			DisabledTools:        taskDisabledTools(launch.VirtualTarget),
			TrustedAgentProfile:  &launch.SubagentProfile,
			PermissionSessionID:  sessionID,
			Principal:            req.Principal,
			ApplySessionMutation: req.ApplySessionMutation,
		}, func(event StreamEvent) {
			eventType := strings.ToLower(strings.TrimSpace(event.Type))
			switch eventType {
			case StreamEventStepStarted:
				if strings.TrimSpace(outcome.Phase) == "" || strings.EqualFold(strings.TrimSpace(outcome.Phase), "spawned") {
					emitTaskProgress("running", "", outcome)
					outcome.Phase = "running"
				}
			case StreamEventToolStarted:
				nowMS := time.Now().UnixMilli()
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
				emitTaskProgress("tool.started", fmt.Sprintf("launch %d running %s", outcome.LaunchIndex, outcome.CurrentTool), outcome)
			case StreamEventToolDelta:
				// Child tool output text remains canonical in the child session transcript.
				// The parent task stream only emits lifecycle/tool-state patches.
			case StreamEventToolCompleted:
				nowMS := time.Now().UnixMilli()
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
				toolPhase := "tool.completed"
				if strings.TrimSpace(event.Error) != "" {
					outcome.ToolFailed++
					toolPhase = "tool.failed"
				}
				summary := fmt.Sprintf("launch %d completed %s", outcome.LaunchIndex, completedTool)
				if strings.TrimSpace(event.Error) != "" {
					summary = fmt.Sprintf("launch %d failed %s: %s", outcome.LaunchIndex, completedTool, strings.TrimSpace(event.Error))
				}
				emitTaskProgress(toolPhase, summary, outcome)
				if strings.TrimSpace(event.Error) == "" {
					outcome.CurrentTool = ""
					outcome.CurrentToolStarted = 0
					outcome.CurrentToolMS = 0
					outcome.CurrentPreviewKind = ""
					outcome.CurrentPreviewText = ""
				}
			case StreamEventReasoningDelta, StreamEventAssistantDelta, StreamEventAssistantCommentary:
				// Child model/reasoning deltas are intentionally not mirrored into the
				// parent task stream; child sessions remain the canonical transcript.
			case StreamEventMessageStored, StreamEventMessageUpdated:
				if event.Message != nil && strings.EqualFold(strings.TrimSpace(event.Message.Role), "reasoning") {
					outcome.ReasoningSummary = strings.TrimSpace(event.Message.Content)
				}
			case StreamEventPermissionReq, StreamEventPermissionUpdate:
				if emit != nil {
					emit(event)
				}
			}
		})
		if runErr != nil {
			if launch.VirtualTarget && s.worktrees != nil {
				if state, inspectErr := s.worktrees.InspectTaskWorkspace(outcome.WorkspacePath); inspectErr == nil {
					outcome.WorktreeBranch = strings.TrimSpace(state.BranchName)
					outcome.HeadCommit = strings.TrimSpace(state.HeadCommit)
					outcome.GitStatus = strings.TrimSpace(state.Status)
					outcome.WorktreeClean = state.Clean
				} else {
					outcome.GitStatus = "Git inspection failed: " + inspectErr.Error()
					outcome.WorktreeClean = false
				}
			}
			nowMS := time.Now().UnixMilli()
			if outcome.LaunchStartedAtMS <= 0 {
				outcome.LaunchStartedAtMS = nowMS
			}
			outcome.ElapsedMS = maxInt64(0, nowMS-outcome.LaunchStartedAtMS)
			if strings.TrimSpace(outcome.CurrentTool) != "" && outcome.CurrentToolStarted > 0 {
				outcome.CurrentToolMS = maxInt64(0, nowMS-outcome.CurrentToolStarted)
			}
			outcome.CurrentPreviewKind = ""
			outcome.CurrentPreviewText = ""
			phase := "failed"
			if reason, cancelled := s.cancelledTaskLaunchReason(outcome.ChildSessionID, runErr); cancelled {
				phase = "cancelled"
				outcome.Reason = reason
				outcome.Error = ""
				outcome.Summary = fmt.Sprintf("launch %d subagent %s cancelled (session %s): %s", outcome.LaunchIndex, outcome.ResolvedSubagent, outcome.ChildSessionID, reason)
			} else {
				outcome.Error = boundedTaskLaunchReason(runErr.Error())
				outcome.Reason = outcome.Error
				outcome.Summary = fmt.Sprintf("launch %d subagent %s failed (session %s)", outcome.LaunchIndex, outcome.ResolvedSubagent, outcome.ChildSessionID)
				if outcome.Error != "" {
					outcome.Summary += ": " + outcome.Error
				}
			}
			emitTaskProgress(phase, outcome.Summary, outcome)
			return outcome, runErr
		}

		report := strings.TrimSpace(subResult.AssistantMessage.Content)
		if report == "" {
			report = "Subagent completed without a textual report."
		}
		reportRef := taskReportRefFromMessage(subResult.AssistantMessage)
		nowMS := time.Now().UnixMilli()
		if outcome.LaunchStartedAtMS <= 0 {
			outcome.LaunchStartedAtMS = nowMS
		}
		outcome.ElapsedMS = maxInt64(0, nowMS-outcome.LaunchStartedAtMS)
		outcome.CurrentTool = ""
		outcome.CurrentToolStarted = 0
		outcome.CurrentToolMS = 0
		outcome.CurrentPreviewKind = ""
		outcome.CurrentPreviewText = ""
		outcome.ReportChars = len([]rune(report))
		outcome.ReportExcerpt = report
		outcome.ReportRef = reportRef
		if launch.VirtualTarget && s.worktrees != nil {
			state, inspectErr := s.worktrees.InspectTaskWorkspace(outcome.WorkspacePath)
			if inspectErr != nil {
				return outcome, fmt.Errorf("inspect Clone handoff Git state: %w", inspectErr)
			}
			outcome.WorktreeBranch = strings.TrimSpace(state.BranchName)
			outcome.HeadCommit = strings.TrimSpace(state.HeadCommit)
			outcome.GitStatus = strings.TrimSpace(state.Status)
			outcome.WorktreeClean = state.Clean
			if strings.TrimSpace(outcome.WorktreeBranch) != strings.TrimSpace(launch.ChildSession.WorktreeBranch) {
				return outcome, fmt.Errorf("Clone handoff branch %q does not match allocated branch %q", outcome.WorktreeBranch, launch.ChildSession.WorktreeBranch)
			}
			if outcome.HeadCommit == "" {
				return outcome, errors.New("Clone handoff is missing child HEAD commit")
			}
			if !outcome.WorktreeClean {
				return outcome, fmt.Errorf("implementation Clone completed with uncommitted work; commit the changes before a successful handoff:\n%s", outcome.GitStatus)
			}
			if outcome.HeadCommit == outcome.BaseCommit {
				return outcome, errors.New("implementation Clone completed without a commit")
			}
			descends, ancestryErr := s.worktrees.TaskCommitDescendsFrom(outcome.WorkspacePath, outcome.BaseCommit, outcome.HeadCommit)
			if ancestryErr != nil {
				return outcome, fmt.Errorf("validate Clone handoff ancestry: %w", ancestryErr)
			}
			if !descends {
				return outcome, errors.New("Clone handoff HEAD does not descend from its recorded immutable base")
			}
		}
		if outcome.ReportChars > taskReportDefaultChars {
			outcome.ReportTruncated = true
			outcome.ReportExcerpt = truncateRunes(report, taskReportDefaultChars)
		}
		outcome.Summary = summarizePlainToolOutput(report, taskReportPreviewChars, 2)
		if outcome.Summary == "" {
			outcome.Summary = fmt.Sprintf("launch %d subagent %s completed", outcome.LaunchIndex, outcome.ResolvedSubagent)
		}
		emitTaskProgress("completed", outcome.Summary, outcome)
		return outcome, nil
	})

	if len(outcomes) == 0 {
		return "", errors.New("task completed without launch outcomes")
	}

	failedCount := 0
	cancelledCount := 0
	successCount := 0
	totalToolStarted := 0
	totalToolCompleted := 0
	totalToolFailed := 0
	reportTruncatedAny := false
	taskStartedAtMS := time.Now().UnixMilli()
	var firstErr error
	launchPayloads := make([]map[string]any, 0, len(outcomes))
	summaryParts := make([]string, 0, len(outcomes))
	inlineReportChars := 0
	aggregateReportBudgetExceeded := false
	for i := range outcomes {
		launch := outcomes[i]
		err := runErrs[i]
		nowMS := time.Now().UnixMilli()
		if launch.LaunchStartedAtMS > 0 && (taskStartedAtMS <= 0 || launch.LaunchStartedAtMS < taskStartedAtMS) {
			taskStartedAtMS = launch.LaunchStartedAtMS
		}
		if launch.LaunchStartedAtMS <= 0 {
			launch.LaunchStartedAtMS = nowMS
		}
		if launch.ElapsedMS <= 0 {
			launch.ElapsedMS = maxInt64(0, nowMS-launch.LaunchStartedAtMS)
		}
		if strings.TrimSpace(launch.CurrentTool) != "" && launch.CurrentToolStarted > 0 && launch.CurrentToolMS <= 0 {
			launch.CurrentToolMS = maxInt64(0, nowMS-launch.CurrentToolStarted)
		}
		if err != nil {
			if strings.EqualFold(strings.TrimSpace(launch.Phase), "cancelled") {
				cancelledCount++
			} else {
				failedCount++
				if strings.TrimSpace(launch.Error) == "" {
					launch.Error = boundedTaskLaunchReason(err.Error())
				}
				if strings.TrimSpace(launch.Reason) == "" {
					launch.Reason = launch.Error
				}
			}
			if firstErr == nil {
				firstErr = err
			}
		} else {
			successCount++
		}
		totalToolStarted += launch.ToolStarted
		totalToolCompleted += launch.ToolCompleted
		totalToolFailed += launch.ToolFailed
		if launch.ReportTruncated {
			reportTruncatedAny = true
		}
		reportExcerpt := strings.TrimSpace(launch.ReportExcerpt)
		reportTruncated := launch.ReportTruncated
		if launch.ReportChars > 0 && len([]rune(reportExcerpt)) > taskReportDefaultChars {
			reportExcerpt = truncateRunes(reportExcerpt, taskReportDefaultChars)
			reportTruncated = true
			reportTruncatedAny = true
		}
		status := "ok"
		if strings.EqualFold(strings.TrimSpace(launch.Phase), "cancelled") {
			status = "cancelled"
		} else if err != nil || strings.TrimSpace(launch.Error) != "" {
			status = "error"
		}
		launchSummary := strings.TrimSpace(launch.Summary)
		if launchSummary == "" {
			if status == "error" {
				launchSummary = fmt.Sprintf("launch %d failed", launch.LaunchIndex)
			} else {
				launchSummary = fmt.Sprintf("launch %d completed", launch.LaunchIndex)
			}
		}
		summaryParts = append(summaryParts, fmt.Sprintf("[%d] %s", launch.LaunchIndex, launchSummary))
		launchPhase := "completed"
		switch status {
		case "cancelled":
			launchPhase = "cancelled"
		case "error":
			launchPhase = "failed"
		}
		launch.Phase = launchPhase
		launch.ReportTruncated = reportTruncated
		launchPayload := buildTaskStreamLaunchPayload(launch, status, launchPhase, true)
		if launch.VirtualTarget {
			childState := "committed"
			if status == "error" || status == "cancelled" {
				childState = "blocked"
				if !launch.WorktreeClean && strings.TrimSpace(launch.GitStatus) != "" {
					childState = "dirty-recoverable"
				}
			}
			launchPayload["child_state"] = childState
		}
		launchPayload["session_id"] = strings.TrimSpace(launch.ChildSessionID)
		launchPayload["mode"] = strings.TrimSpace(launch.ChildMode)
		if reportExcerpt != "" {
			excerptChars := len([]rune(reportExcerpt))
			if inlineReportChars+excerptChars > taskReportAggregateMaxChars {
				aggregateReportBudgetExceeded = true
				reportTruncatedAny = true
				launchPayload["report_excerpt"] = truncateRunes(firstNonEmptyString(launchSummary, reportExcerpt), taskReportAggregateSummaryChars)
				launchPayload["report"] = launchPayload["report_excerpt"]
				launchPayload["report_truncated"] = true
				launchPayload["report_omitted_for_context"] = true
				launchPayload["report_omission_reason"] = "aggregate subagent reports exceeded inline context budget; inspect report_ref/child session transcript for the full report"
			} else {
				launchPayload["report_excerpt"] = reportExcerpt
				launchPayload["report"] = reportExcerpt
				inlineReportChars += excerptChars
			}
		}
		if launch.ReportRef != nil {
			launchPayload["report_ref"] = launch.ReportRef
			launchPayload["report_persisted"] = true
		}
		launchPayloads = append(launchPayloads, launchPayload)
		outcomes[i] = launch
	}

	overallStatus := "ok"
	if failedCount > 0 || cancelledCount > 0 {
		overallStatus = "error"
	}
	if s.permissions != nil && strings.TrimSpace(req.RunID) != "" {
		reservationStatus := "completed"
		if failedCount > 0 || cancelledCount > 0 {
			reservationStatus = "failed"
		}
		if finishErr := s.permissions.FinishSubagentWave(parentSession.ID, req.RunID, taskCallID, reservationStatus); finishErr != nil {
			return "", fmt.Errorf("finish subagent wave reservation: %w", finishErr)
		}
		reservationFinished = true
	}
	aggregateSummary := strings.TrimSpace(strings.Join(summaryParts, " | "))
	if aggregateSummary == "" {
		aggregateSummary = fmt.Sprintf("%d launch(es) completed", len(outcomes))
	}
	if aggregateReportBudgetExceeded {
		aggregateSummary += " | warning: aggregate subagent reports exceeded inline context budget; inspect report_ref child session transcripts for full reports"
	}
	lineageUpdate(overallStatus, outcomes, map[string]any{
		"success_count":   successCount,
		"failed_count":    failedCount,
		"cancelled_count": cancelledCount,
		"tool_started":    totalToolStarted,
		"tool_completed":  totalToolCompleted,
		"tool_failed":     totalToolFailed,
		"summary":         aggregateSummary,
	})

	payload := map[string]any{
		"tool":                    "task",
		"action":                  action,
		"status":                  overallStatus,
		"description":             description,
		"goal":                    description,
		"prompt":                  prompt,
		"launch_count":            len(outcomes),
		"parallel_launches":       true,
		"parallel_execution_mode": "all_at_once",
		"launches":                launchPayloads,
		"success_count":           successCount,
		"failed_count":            failedCount,
		"cancelled_count":         cancelledCount,
		"stop_acknowledged":       cancelledCount > 0,
		"tool_started":            totalToolStarted,
		"tool_completed":          totalToolCompleted,
		"tool_failed":             totalToolFailed,
		"elapsed_ms":              maxInt64(0, time.Now().UnixMilli()-taskStartedAtMS),
		"summary":                 aggregateSummary,
		"path_id":                 "tool.task.v1",
		"details_truncated":       false,
		"report_truncated":        reportTruncatedAny,
		"report_inline_chars":     inlineReportChars,
	}
	if aggregateReportBudgetExceeded {
		payload["report_context_warning"] = "aggregate subagent reports exceeded inline context budget; summaries/excerpts are returned inline and full reports remain in child session transcripts via report_ref"
		payload["report_context_budget_chars"] = taskReportAggregateMaxChars
	}
	if len(outcomes) > 0 {
		first := outcomes[0]
		payload["subagent"] = strings.TrimSpace(first.ResolvedSubagent)
		payload["agent_type"] = strings.TrimSpace(first.ResolvedSubagent)
		payload["requested_subagent"] = strings.TrimSpace(first.RequestedSubagent)
		payload["assignment_label"] = strings.TrimSpace(first.AssignmentLabel)
		payload["subagent_provider"] = strings.TrimSpace(first.SubagentProvider)
		payload["subagent_model"] = strings.TrimSpace(first.SubagentModel)
		payload["session_id"] = strings.TrimSpace(first.ChildSessionID)
		payload["mode"] = strings.TrimSpace(first.ChildMode)
		payload["workspace_path"] = strings.TrimSpace(first.WorkspacePath)
		payload["worktree_enabled"] = first.WorktreeEnabled
		payload["worktree_root_path"] = strings.TrimSpace(first.WorktreeRootPath)
		payload["worktree_branch"] = strings.TrimSpace(first.WorktreeBranch)
	}
	encoded, encodeErr := json.Marshal(payload)
	if encodeErr != nil {
		if firstErr != nil {
			return "", firstErr
		}
		return "", encodeErr
	}
	if firstErr != nil {
		return string(encoded), firstErr
	}
	return string(encoded), nil
}

func (s *Service) resolveTaskSubagent(nameOrPurpose string) (pebblestore.AgentProfile, error) {
	return s.resolveTaskSubagentForAccount("", nameOrPurpose)
}

func (s *Service) resolveTaskSubagentForAccount(accountScopeID, nameOrPurpose string) (pebblestore.AgentProfile, error) {
	nameOrPurpose = strings.TrimSpace(nameOrPurpose)
	if nameOrPurpose == "" {
		return pebblestore.AgentProfile{}, errors.New("task subagent name or purpose is required")
	}
	if s == nil || s.agents == nil {
		return pebblestore.AgentProfile{}, errors.New("saved agent service is not configured")
	}
	if agentruntime.IsExplorerAgentName(nameOrPurpose) {
		if s.model == nil {
			return pebblestore.AgentProfile{}, errors.New("Explorer model service is not configured")
		}
		preference, err := s.model.GetPreferenceForAccount(strings.TrimSpace(accountScopeID))
		if err != nil {
			return pebblestore.AgentProfile{}, fmt.Errorf("read Explorer model preference: %w", err)
		}
		providerID := strings.ToLower(strings.TrimSpace(preference.Provider))
		override := uisettings.CompactAgentSettings{}
		if s.uiSettings != nil {
			if settings, settingsErr := s.uiSettings.GetForAccount(strings.TrimSpace(accountScopeID)); settingsErr == nil {
				override = settings.Agents.Explorer
			}
		}
		if override.Provider != "" {
			providerID = strings.ToLower(strings.TrimSpace(override.Provider))
		}
		if providerID == "" {
			return pebblestore.AgentProfile{}, errors.New("Explorer utility provider is empty")
		}
		_, _, utility, ok, err := s.model.RecommendedCatalogDefaults(providerID)
		if err != nil {
			return pebblestore.AgentProfile{}, fmt.Errorf("resolve Explorer utility recommendation: %w", err)
		}
		if !ok || strings.TrimSpace(utility.Model) == "" {
			return pebblestore.AgentProfile{}, fmt.Errorf("Explorer utility recommendation for provider %q is unavailable", providerID)
		}
		modelName := strings.TrimSpace(override.Model)
		if modelName == "" {
			modelName = strings.TrimSpace(utility.Model)
		}
		thinking := strings.TrimSpace(override.Thinking)
		for _, recommendation := range utility.Recommendations {
			if thinking == "" && strings.EqualFold(strings.TrimSpace(recommendation.Role), "utility") {
				thinking = strings.TrimSpace(recommendation.Thinking)
				break
			}
		}
		resolved, err := s.model.ResolvePreference(pebblestore.ModelPreference{Provider: providerID, Model: modelName, Thinking: thinking, ContextMode: preference.ContextMode})
		if err != nil {
			return pebblestore.AgentProfile{}, fmt.Errorf("resolve Explorer utility preference: %w", err)
		}
		serviceTier := strings.TrimSpace(override.ServiceTier)
		if serviceTier == "" {
			serviceTier = strings.TrimSpace(preference.ServiceTier)
		}
		return s.agents.ResolveSystemAgent(agentruntime.ExplorerAgentID, pebblestore.AgentProfile{Provider: resolved.Preference.Provider, Model: resolved.Preference.Model, Thinking: resolved.Preference.Thinking, AutoServiceTier: serviceTier})
	}
	if strings.TrimSpace(accountScopeID) != "" {
		return s.agents.ResolveSubagentForAccount(accountScopeID, nameOrPurpose)
	}
	return s.agents.ResolveSubagent(nameOrPurpose)
}

type taskDelegationPromptConfig struct {
	Description          string
	Prompt               string
	ParentSession        pebblestore.SessionSnapshot
	ParentMessages       []pebblestore.MessageSnapshot
	ParentActivePlan     *pebblestore.SessionPlanSnapshot
	PermissionSessionID  string
	TargetedSubagentName string
}

func buildTaskDelegationPrompt(config taskDelegationPromptConfig) string {
	description := strings.TrimSpace(config.Description)
	prompt := strings.TrimSpace(config.Prompt)
	if description == "" {
		description = "delegated task"
	}
	var b strings.Builder
	b.WriteString("Delegated task context:\n")
	b.WriteString("- description: ")
	b.WriteString(description)
	b.WriteString("\n")
	if targeted := strings.TrimSpace(config.TargetedSubagentName); targeted != "" {
		b.WriteString("- launch source: targeted_subagent\n")
		b.WriteString("- requested subagent: @")
		b.WriteString(targeted)
		b.WriteString("\n")
	}
	if parentBlock := buildTaskParentSessionContext(config.ParentSession, config.PermissionSessionID); parentBlock != "" {
		b.WriteString("\nParent session context:\n")
		b.WriteString(parentBlock)
		b.WriteString("\n")
	}
	if activePlanBlock := buildTaskActivePlanContext(config.ParentActivePlan); activePlanBlock != "" {
		b.WriteString("\nActive session plan:\n")
		b.WriteString(activePlanBlock)
		b.WriteString("\n")
	}
	if transcriptBlock := buildTaskParentTranscriptContext(config.ParentMessages); transcriptBlock != "" {
		b.WriteString("\nRecent visible parent transcript:\n")
		b.WriteString(transcriptBlock)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(prompt)
	b.WriteString("\n\n")
	b.WriteString("Completion contract:\n")
	b.WriteString("1. Execute the task autonomously using available tools and use the provided context as your starting point.\n")
	b.WriteString("2. First gauge scope quickly: if the request is narrow and explicit, complete it directly with minimal investigation; if broad/unclear, perform deeper exploration.\n")
	b.WriteString("3. Use search/list first, keep patterns and paths narrow, and avoid duplicate/broadened search loops; if results are truncated, tighten scope and rerun.\n")
	b.WriteString("4. Summarize the relevant architecture/flow, then identify areas of interest and likely attack points.\n")
	b.WriteString("5. Back key findings with concrete evidence (path and line anchors where possible).\n")
	b.WriteString("6. End with a `Relevant filepaths:` section listing the most important files and why each matters.\n")
	b.WriteString("7. If essential files are still unknown, include an `Open questions / missing filepaths:` section with exact paths needed.\n")
	b.WriteString("8. Keep the final response concise, factual, and implementation-focused.\n")
	b.WriteString("9. For implementation Clone work, finish with a scoped commit. If commit permission is denied or work fails, explicitly report the uncommitted/failed state; the parent records live HEAD and status for later repair.\n")
	return strings.TrimSpace(b.String())
}

func buildTaskParentSessionContext(session pebblestore.SessionSnapshot, permissionSessionID string) string {
	if strings.TrimSpace(session.ID) == "" {
		return ""
	}
	metadataJSON := compactTaskDelegationJSON(cloneGenericMap(session.Metadata), taskDelegationContextMaxChars)
	gitJSON := compactTaskDelegationJSON(sessionGitMetadata(session.Metadata), taskDelegationContextMaxChars)
	var b strings.Builder
	b.WriteString("- session_id: ")
	b.WriteString(strings.TrimSpace(session.ID))
	b.WriteString("\n")
	if permissionSessionID = strings.TrimSpace(permissionSessionID); permissionSessionID != "" {
		b.WriteString("- permission_session_id: ")
		b.WriteString(permissionSessionID)
		b.WriteString("\n")
	}
	if title := strings.TrimSpace(session.Title); title != "" {
		b.WriteString("- title: ")
		b.WriteString(title)
		b.WriteString("\n")
	}
	if mode := strings.TrimSpace(session.Mode); mode != "" {
		b.WriteString("- mode: ")
		b.WriteString(mode)
		b.WriteString("\n")
	}
	if workspacePath := strings.TrimSpace(session.WorkspacePath); workspacePath != "" {
		b.WriteString("- workspace_path: ")
		b.WriteString(workspacePath)
		b.WriteString("\n")
	}
	if workspaceName := strings.TrimSpace(session.WorkspaceName); workspaceName != "" {
		b.WriteString("- workspace_name: ")
		b.WriteString(workspaceName)
		b.WriteString("\n")
	}
	if session.WorktreeEnabled {
		b.WriteString("- worktree_enabled: true\n")
		if root := strings.TrimSpace(session.WorktreeRootPath); root != "" {
			b.WriteString("- worktree_root_path: ")
			b.WriteString(root)
			b.WriteString("\n")
		}
		if base := strings.TrimSpace(session.WorktreeBaseBranch); base != "" {
			b.WriteString("- worktree_base_branch: ")
			b.WriteString(base)
			b.WriteString("\n")
		}
		if branch := strings.TrimSpace(session.WorktreeBranch); branch != "" {
			b.WriteString("- worktree_branch: ")
			b.WriteString(branch)
			b.WriteString("\n")
		}
	}
	if metadataJSON != "" {
		b.WriteString("- metadata_json: ")
		b.WriteString(metadataJSON)
		b.WriteString("\n")
	}
	if gitJSON != "" {
		b.WriteString("- git_metadata_json: ")
		b.WriteString(gitJSON)
		b.WriteString("\n")
	}
	if roots := sanitizeTaskDelegationRoots(session.TemporaryWorkspaceRoots); len(roots) > 0 {
		b.WriteString("- temporary_workspace_roots_json: ")
		b.WriteString(compactTaskDelegationJSONArray(roots, taskDelegationContextMaxChars))
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

type taskDelegationContext struct {
	ParentMessages []pebblestore.MessageSnapshot
	ActivePlan     *pebblestore.SessionPlanSnapshot
}

func (s *Service) loadTaskDelegationContext(sessionID string) (taskDelegationContext, error) {
	messages, err := s.loadDelegationTranscriptMessages(sessionID)
	if err != nil {
		return taskDelegationContext{}, err
	}
	activePlan, err := s.activePlanForDelegation(sessionID)
	if err != nil {
		return taskDelegationContext{}, err
	}
	return taskDelegationContext{ParentMessages: messages, ActivePlan: activePlan}, nil
}

func (s *Service) activePlanForDelegation(sessionID string) (*pebblestore.SessionPlanSnapshot, error) {
	return s.activePlanForCompaction(sessionID)
}

func (s *Service) loadDelegationTranscriptMessages(sessionID string) ([]pebblestore.MessageSnapshot, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || s == nil || s.sessions == nil {
		return nil, nil
	}
	limit := taskDelegationTranscriptMsgLimit
	if session, ok, err := s.sessions.GetSession(sessionID); err != nil {
		return nil, fmt.Errorf("load parent session for delegation transcript: %w", err)
	} else if ok && session.MessageCount+memoryCompactionHistorySlack > limit {
		limit = session.MessageCount + memoryCompactionHistorySlack
	}
	messages, err := s.listRunMessages(sessionID, 0, limit, true)
	if err != nil {
		return nil, fmt.Errorf("list parent transcript messages: %w", err)
	}
	messages = compactMessagesForProviderContext(messages, taskDelegationTranscriptMsgLimit)
	return append([]pebblestore.MessageSnapshot(nil), messages...), nil
}

func buildTaskActivePlanContext(activePlan *pebblestore.SessionPlanSnapshot) string {
	return compactedActivePlanText(activePlan)
}

func buildTaskParentTranscriptContext(messages []pebblestore.MessageSnapshot) string {
	if len(messages) == 0 {
		return ""
	}
	entries := make([]string, 0, len(messages))
	remainingChars := taskDelegationTranscriptMaxChars
	for _, message := range messages {
		entry := formatTaskDelegationTranscriptMessage(message)
		if entry == "" {
			continue
		}
		entryChars := len([]rune(entry))
		if len(entries) > 0 {
			entryChars++
		}
		if remainingChars <= 0 {
			break
		}
		if entryChars > remainingChars {
			entry = truncateRunes(entry, maxInt(remainingChars, 32))
			entryChars = len([]rune(entry))
			if entry == "" {
				break
			}
		}
		entries = append(entries, entry)
		remainingChars -= entryChars
		if remainingChars <= 0 {
			break
		}
	}
	return strings.TrimSpace(strings.Join(entries, "\n"))
}

func formatTaskDelegationTranscriptMessage(message pebblestore.MessageSnapshot) string {
	role := strings.ToLower(strings.TrimSpace(message.Role))
	content := strings.TrimSpace(message.Content)
	if content == "" {
		return ""
	}
	switch role {
	case "reasoning":
		return ""
	case "system":
		if isToolDBDebugMessage(content) {
			return ""
		}
		content = "[system] " + content
	case "tool":
		content = summarizeTaskDelegationToolMessage(content)
		if content == "" {
			return ""
		}
	default:
		if shouldDropSensitiveConversationMessage(message) {
			return ""
		}
	}
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	if content == "" {
		return ""
	}
	content = truncateRunes(content, taskDelegationTranscriptMsgChars)
	prefix := role
	if prefix == "" {
		prefix = "message"
	}
	return fmt.Sprintf("- %s: %s", prefix, content)
}

func summarizeTaskDelegationToolMessage(content string) string {
	record, ok := decodeToolHistoryRecord(content)
	if ok {
		toolName := strings.TrimSpace(record.Tool)
		if toolName == "" {
			toolName = "tool"
		}
		summary := strings.TrimSpace(record.CompletedOutput)
		if summary == "" {
			summary = strings.TrimSpace(record.Output)
		}
		summary = summarizePlainToolOutput(summary, 240, 2)
		if summary == "" {
			summary = "completed"
		}
		if errText := strings.TrimSpace(record.Error); errText != "" {
			return fmt.Sprintf("[%s] error: %s | %s", toolName, truncateRunes(errText, 120), summary)
		}
		return fmt.Sprintf("[%s] %s", toolName, summary)
	}
	return summarizePlainToolOutput(content, 240, 2)
}

func compactTaskDelegationJSON(payload map[string]any, maxChars int) string {
	if len(payload) == 0 {
		return ""
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(raw))
	if maxChars > 0 && len([]rune(text)) > maxChars {
		text = truncateRunes(text, maxChars)
	}
	return text
}

func compactTaskDelegationJSONArray(values []string, maxChars int) string {
	if len(values) == 0 {
		return ""
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(raw))
	if maxChars > 0 && len([]rune(text)) > maxChars {
		text = truncateRunes(text, maxChars)
	}
	return text
}

func sanitizeTaskDelegationRoots(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func taskDisabledTools(allowBash bool) map[string]bool {
	disabled := map[string]bool{
		"ask_user":       true,
		"ask-user":       true,
		"exit_plan_mode": true,
		"exit-plan-mode": true,
		"plan_manage":    true,
		"plan-manage":    true,
		"manage_todos":   true,
		"manage-todos":   true,
		"manage_agent":   true,
		"manage-agent":   true,
		"task":           true,
	}
	if !allowBash {
		disabled["bash"] = true
	}
	return disabled
}

func taskReportRefFromMessage(message pebblestore.MessageSnapshot) *taskReportRef {
	sessionID := strings.TrimSpace(message.SessionID)
	if sessionID == "" || message.GlobalSeq == 0 {
		return nil
	}
	return &taskReportRef{
		SessionID: sessionID,
		MessageID: strings.TrimSpace(message.ID),
		GlobalSeq: message.GlobalSeq,
		Role:      strings.TrimSpace(message.Role),
		Source:    "child_session_transcript",
	}
}

func emptyToolName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "tool"
	}
	return name
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func canonicalToolName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "ask-user", "ask_user":
		return "ask_user"
	case "exit-plan-mode", "exit_plan_mode":
		return "exit_plan_mode"
	case "plan-manage", "plan_manage":
		return "plan_manage"
	case "edit-pending-plan", "edit_pending_plan":
		return "edit_pending_plan"
	case "skill-use", "skill_use":
		return "skill_use"
	case "manage-skill", "manage_skill":
		return "manage_skill"
	case "manage-agent", "manage_agent":
		return "manage_agent"
	case "manage-integrations", "manage_integrations":
		return "manage_integrations"
	case "manage-theme", "manage_theme":
		return "manage_theme"
	case "manage-sessions", "manage_sessions":
		return "manage_sessions"
	case "manage-worktree", "manage_worktree":
		return "manage_worktree"
	case "manage-todos", "manage_todos":
		return "manage_todos"
	case "manage-image", "manage_image":
		return "manage_image"
	default:
		return strings.ToLower(strings.TrimSpace(name))
	}
}

func permissionRequirement(mode, toolName, arguments string) (string, bool) {
	mode = strings.TrimSpace(strings.ToLower(mode))
	toolName = canonicalToolName(toolName)
	bypass := false
	if strings.Contains(mode, "+") {
		parts := strings.Split(mode, "+")
		mode = strings.TrimSpace(parts[0])
		for _, part := range parts[1:] {
			if strings.TrimSpace(part) == "bypass_permissions" {
				bypass = true
			}
		}
	}

	switch toolName {
	case "read", "search", "websearch", "webfetch", "agentic_search", "list", "skill_use", "manage_worktree", "manage_todos", "manage_theme", "manage_integrations", "edit_pending_plan":
		return toolName, false
	case "manage_sessions":
		if permission.ShouldApproveManageSessionsDeploy(arguments) {
			return "session_deploy", true
		}
		if permission.ShouldApproveManageSessionsCommit(arguments) {
			return "session_commit", true
		}
		if permission.ShouldApproveManageSessionsArchive(arguments) {
			return "session_archive", true
		}
		if permission.ShouldApproveManageSessionsUnarchive(arguments) {
			return "session_unarchive", true
		}
		return toolName, false
	case "manage_image":
		if shouldApproveManageImage(arguments) {
			return "image_generation", true
		}
		return "manage_image", false
	case "plan_manage":
		if requirement := permission.PlanManageLifecycleRequirement(arguments); requirement != "" {
			return requirement, true
		}
		return toolName, false
	case "manage_skill":
		if bypass {
			return "skill_change", false
		}
		return "skill_change", true
	case "manage_agent":
		if permission.ShouldApproveManageAgentMutation(arguments) {
			return "agent_change", true
		}
		return "manage_agent", false
	case "task":
		return "task_launch", true
	case "ask_user", "exit_plan_mode":
		return toolName, true
	case "write", "edit":
		return toolName, mode == sessionruntime.ModePlan
	case "bash":
		if mode == pebblestore.AgentExecutionSettingRead || mode == pebblestore.AgentExecutionSettingReadWrite {
			return "bash", false
		}
		if bypass {
			return "bash", false
		}
		return "bash", true
	default:
		if bypass {
			return toolName, false
		}
		return toolName, true
	}
}

func permissionOutputPayload(approved bool, status, reason, toolName, arguments string) string {
	payload := map[string]any{
		"permission": map[string]any{
			"approved": approved,
			"status":   strings.TrimSpace(status),
			"reason":   strings.TrimSpace(reason),
		},
		"tool": map[string]any{
			"name":      strings.TrimSpace(toolName),
			"arguments": strings.TrimSpace(arguments),
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return `{"permission":{"approved":false,"status":"error","reason":"encode failed"}}`
	}
	return string(raw)
}

func normalizePermissionFeedback(reason string) string {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		runPermissionDebugf("permission_feedback.normalize reason_present=false")
		return ""
	}
	switch strings.ToLower(trimmed) {
	case "approved by user", "approved", "allow", "allowed":
		runPermissionDebugf("permission_feedback.normalize dropped_default_reason=true reason_chars=%d", len(trimmed))
		return ""
	}
	runPermissionDebugf("permission_feedback.normalize kept_reason=true reason_chars=%d", len(trimmed))
	return trimmed
}

func buildPermissionFeedbackInput(feedback []PermissionFeedback) string {
	if len(feedback) == 0 {
		return ""
	}
	var b strings.Builder
	included := 0
	b.WriteString("User responded to tool permission requests with additional instructions:\n")
	for i := range feedback {
		line := strings.TrimSpace(feedback[i].Message)
		if line == "" {
			continue
		}
		included++
		line = strings.ReplaceAll(line, "\n", " ")
		if len(line) > 240 {
			line = line[:240] + "..."
		}
		callID := strings.TrimSpace(feedback[i].CallID)
		toolName := strings.TrimSpace(feedback[i].ToolName)
		if callID == "" {
			callID = "call"
		}
		if toolName == "" {
			toolName = "tool"
		}
		b.WriteString("- ")
		b.WriteString(callID)
		b.WriteString(" (")
		b.WriteString(toolName)
		b.WriteString("): ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	out := strings.TrimSpace(b.String())
	runPermissionDebugf("permission_feedback.input_built notes_total=%d notes_included=%d payload_chars=%d", len(feedback), included, len(out))
	return out
}

func runPermissionDebugEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("SWARMD_PERMISSION_DEBUG")))
	switch value {
	case "1", "true", "yes", "on", "debug":
		return true
	default:
		return false
	}
}

func runPermissionDebugf(format string, args ...any) {
	if !runPermissionDebugEnabled() {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "[swarmd.run.permission] "+format+"\n", args...)
}

func runPermissionDebugPreview(text string, max int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if max <= 0 {
		max = 160
	}
	if len(text) <= max {
		return text
	}
	return text[:max] + "…"
}

func isToolDBDebugMessage(content string) bool {
	return false
}
