package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type taskLaunchPrepared struct {
	LaunchIndex          int
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
	LaunchStartedAtMS    int64
}

type taskLaunchOutcome struct {
	LaunchIndex        int
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
	return taskLaunchOutcome{
		LaunchIndex:        launch.LaunchIndex,
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
	}
}

func taskStreamStatusForPhase(phase string) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "spawned":
		return "pending"
	case "completed":
		return "ok"
	case "failed":
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

func emitTaskStreamDelta(parentSessionID string, emit StreamHandler, step int, toolName, callID, action, description string, launchCount int, launch taskLaunchOutcome, phase, summary string) {
	payload := buildTaskStreamPayload(parentSessionID, action, description, launchCount, launch, phase, summary)
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

func (s *Service) prepareDelegatedSubagentLaunch(parentSession pebblestore.SessionSnapshot, sessionMode string, launch taskLaunchPrepared, description, targetedSubagentName string, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (taskLaunchPrepared, error) {
	requestedSubagent := strings.TrimSpace(launch.RequestedSubagent)
	if requestedSubagent == "" {
		return taskLaunchPrepared{}, errors.New("task launch requires saved subagent name or purpose")
	}
	subagentProfile, err := s.resolveTaskSubagentForAccount(parentSession.AccountScopeID, requestedSubagent)
	if err != nil {
		return taskLaunchPrepared{}, err
	}
	if strings.TrimSpace(subagentProfile.Name) == "" {
		return taskLaunchPrepared{}, errors.New("task resolved empty subagent")
	}

	preference := applyAgentPreferenceOverrides(parentSession.Preference, subagentProfile)
	assignmentLabel := taskAssignmentLabel(launch.AssignmentLabel, launch.MetaPrompt, description, strings.TrimSpace(subagentProfile.Name))
	childTitle := assignmentLabel
	childWorkspacePath := strings.TrimSpace(parentSession.WorkspacePath)
	childWorkspaceName := strings.TrimSpace(parentSession.WorkspaceName)
	childWorktreeEnabled := false
	childWorktreeRootPath := ""
	childWorktreeBaseBranch := ""
	childWorktreeBranch := ""
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
	if lineageSource := strings.TrimSpace(targetedSubagentName); lineageSource != "" {
		childMetadata["launch_source"] = "targeted_subagent"
		childMetadata["targeted_subagent"] = lineageSource
	}
	if parentSession.WorktreeEnabled && s.worktrees != nil {
		allocation, allocErr := s.worktrees.AllocateTaskWorkspace(parentSession.WorkspacePath, firstNonEmptyString(parentSession.WorktreeBranch, parentSession.WorktreeBaseBranch), childSessionID)
		if allocErr != nil {
			return taskLaunchPrepared{}, fmt.Errorf("task failed to allocate subagent worktree: %w", allocErr)
		}
		childWorkspacePath = strings.TrimSpace(allocation.WorkspacePath)
		if childWorkspacePath == "" {
			return taskLaunchPrepared{}, errors.New("task failed to allocate subagent worktree: empty workspace path")
		}
		childWorkspaceName = filepath.Base(childWorkspacePath)
		childWorktreeEnabled = true
		childWorktreeRootPath = strings.TrimSpace(allocation.RepoRoot)
		childWorktreeBaseBranch = strings.TrimSpace(allocation.BaseBranch)
		childWorktreeBranch = strings.TrimSpace(allocation.BranchName)
		childWorkspaceID = strings.TrimSpace(allocation.WorkspaceID)
	}

	childMode := effectiveTaskChildMode(sessionMode)
	if childWorkspaceID != "" {
		childMetadata["workspace_id"] = childWorkspaceID
	}
	nowMS := time.Now().UnixMilli()
	childSession := pebblestore.SessionSnapshot{
		ID:                 childSessionID,
		UserID:             strings.TrimSpace(parentSession.UserID),
		AccountScopeID:     strings.TrimSpace(parentSession.AccountScopeID),
		WorkspacePath:      childWorkspacePath,
		WorkspaceName:      childWorkspaceName,
		Title:              childTitle,
		Mode:               childMode,
		Preference:         preference,
		Metadata:           childMetadata,
		CreatedAt:          nowMS,
		UpdatedAt:          nowMS,
		WorktreeEnabled:    childWorktreeEnabled,
		WorktreeRootPath:   childWorktreeRootPath,
		WorktreeBaseBranch: childWorktreeBaseBranch,
		WorktreeBranch:     childWorktreeBranch,
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
		auth, err := s.permissions.AuthorizeToolCall(permission.AuthorizationInput{
			SessionID:         sessionID,
			AccountScopeID:    accountScopeID,
			RunID:             runID,
			Step:              step,
			CallID:            toolCalls[i].CallID,
			ToolName:          toolCalls[i].Name,
			ToolArguments:     permissionArguments,
			ToolCallArguments: strings.TrimSpace(toolCalls[i].Arguments),
			Mode:              sessionMode,
			Overlay:           overlay,
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

func (s *Service) executeControlPlaneTool(ctx context.Context, sessionID, sessionMode string, agentProfile pebblestore.AgentProfile, step int, call tool.Call, approvedArguments string, emit StreamHandler) (bool, tool.Result, error) {
	return s.executeControlPlaneToolWithMutation(ctx, sessionID, sessionMode, agentProfile, step, call, approvedArguments, emit, nil)
}

func (s *Service) executeControlPlaneToolWithMutation(ctx context.Context, sessionID, sessionMode string, agentProfile pebblestore.AgentProfile, step int, call tool.Call, approvedArguments string, emit StreamHandler, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (bool, tool.Result, error) {
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
	case "manage_flow":
		output, err := s.executeManageFlowTool(sessionID, call, approvedArguments)
		result.Output = output
		return true, result, err
	case "manage_theme":
		output, err := s.executeManageThemeTool(sessionID, call, approvedArguments)
		result.Output = output
		return true, result, err
	case "manage_worktree":
		output, err := s.executeManageWorktreeTool(sessionID, call)
		result.Output = output
		return true, result, err
	case "manage_todos":
		output, err := s.executeManageTodosTool(sessionID, call, approvedArguments)
		result.Output = output
		return true, result, err
	case "exit_plan_mode":
		output, err := s.executeExitPlanModeTool(sessionID, sessionMode, agentProfile, call.Arguments, approvedArguments, applySessionMutation)
		result.Output = output
		return true, result, err
	case "plan_manage":
		output, err := s.executePlanManageTool(sessionID, call.Arguments, approvedArguments)
		result.Output = output
		return true, result, err
	case "task":
		principal, _ := identity.PrincipalFromContext(ctx)
		output, err := s.executeTaskToolWithParsed(ctx, sessionID, sessionMode, step, call, emit, taskExecutionRequest{Principal: principal, ApplySessionMutation: applySessionMutation})
		result.Output = output
		return true, result, err
	default:
		return false, tool.Result{}, nil
	}
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

func (s *Service) executeManageFlowTool(sessionID string, call tool.Call, feedback string) (string, error) {
	feedback = strings.TrimSpace(feedback)
	if feedback != "" {
		var payload map[string]any
		if err := json.Unmarshal([]byte(feedback), &payload); err != nil {
			return "", fmt.Errorf("approved manage-flow payload invalid: %w", err)
		}
		args := manageFlowApprovalArguments(payload)
		if len(args) == 0 {
			return "", errors.New("approved manage-flow payload missing approved arguments")
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

func manageFlowApprovalArguments(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	if raw, ok := payload["approved_arguments"]; ok {
		if approved, ok := raw.(map[string]any); ok {
			return approved
		}
	}
	return cloneGenericMap(payload)
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

func (s *Service) executeManageWorktreeTool(sessionID string, call tool.Call) (string, error) {
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
					return "", err
				}
				var approvedArgs map[string]any
				if err := json.Unmarshal(raw, &approvedArgs); err != nil {
					return "", fmt.Errorf("approved exit_plan_mode arguments invalid: %w", err)
				}
				payload = approvedArgs
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				return "", err
			}
			arguments = string(raw)
		}
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("exit_plan_mode arguments invalid: %w", err)
	}
	document, err := planDocumentFromArgsForTool(args, "exit_plan_mode")
	if err != nil {
		return "", err
	}
	title := strings.TrimSpace(mapString(args, "title"))
	plan := strings.TrimSpace(mapString(args, "plan"))
	planID := strings.TrimSpace(mapString(args, "plan_id"))
	if planID == "" {
		planID = strings.TrimSpace(mapString(args, "planID"))
	}
	if planID == "" {
		planID = strings.TrimSpace(mapString(args, "id"))
	}
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

	if s.sessions == nil {
		return "", errors.New("session service is not configured")
	}
	if planID == "" {
		active, ok, err := s.sessions.GetActivePlan(sessionID)
		if err != nil {
			return "", fmt.Errorf("exit_plan_mode failed to inspect active plan: %w", err)
		}
		if ok {
			planID = strings.TrimSpace(active.ID)
			if title == "" {
				title = strings.TrimSpace(active.Title)
			}
			if plan == "" {
				plan = strings.TrimSpace(active.Plan)
			}
			if document == nil {
				document = active.Document
			}
		}
	} else if existing, ok, err := s.sessions.GetPlan(sessionID, planID); err != nil {
		return "", fmt.Errorf("exit_plan_mode failed to inspect plan: %w", err)
	} else if ok {
		if title == "" {
			title = strings.TrimSpace(existing.Title)
		}
		if plan == "" {
			plan = strings.TrimSpace(existing.Plan)
		}
		if document == nil {
			document = existing.Document
		}
	}
	if planID == "" {
		planID = fmt.Sprintf("plan_%d", time.Now().UnixMilli())
	}
	if title == "" {
		return "", errors.New("exit_plan_mode requires title or document.title")
	}
	if plan == "" && document == nil {
		return "", errors.New("exit_plan_mode requires plan or document")
	}
	if plan == "" && document != nil {
		plan = strings.TrimSpace(firstNonEmptyString(document.DisplayText, document.RenderedText))
	}
	if plan == "" {
		plan = "# " + title
	}

	if !pebblestore.AgentExitPlanModeEnabled(agentProfile) {
		payload := map[string]any{
			"tool":              "exit_plan_mode",
			"status":            "rejected",
			"title":             title,
			"plan_id":           planID,
			"plan":              plan,
			"document":          document,
			"approval_state":    "disabled_for_agent",
			"path_id":           "tool.exit-plan-mode.v3",
			"summary":           "exit_plan_mode rejected: disabled for agent",
			"user_message":      userMessage,
			"details_truncated": false,
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
	if sessionruntime.NormalizeMode(sessionMode) != sessionruntime.ModePlan {
		payload := map[string]any{
			"tool":                    "exit_plan_mode",
			"status":                  "rejected",
			"plan_id":                 planID,
			"title":                   title,
			"plan":                    plan,
			"document":                document,
			"approval_state":          "not_in_plan_mode",
			"requested_modifications": []string{"Do not call exit_plan_mode from auto. To update the active plan instead, use plan_manage save."},
			"path_id":                 "tool.exit-plan-mode.v3",
			"summary":                 "exit_plan_mode rejected: session not in plan mode; use plan_manage save to update the active plan instead",
			"user_message":            userMessage,
			"details_truncated":       false,
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
	status := "approved"
	approvalState := "approved"
	var documentForSave *pebblestore.SessionPlanDocument
	if document != nil {
		documentClone := *document
		documentClone.ID = strings.TrimSpace(firstNonEmptyString(planID, documentClone.ID))
		documentClone.Title = strings.TrimSpace(firstNonEmptyString(title, documentClone.Title))
		documentClone.Status = status
		documentForSave = &documentClone
	}
	savedPlan, _, saveErr := s.sessions.SavePlanWithMetadata(sessionID, planID, title, plan, status, approvalState, true, sessionruntime.PlanSaveMetadata{UpdateSummary: "exit plan mode submission", UpdateScope: "plan", UpdateKind: "exit_plan_mode", Document: documentForSave})
	if saveErr != nil {
		return "", fmt.Errorf("exit_plan_mode failed to save plan: %w", saveErr)
	}
	planID = strings.TrimSpace(savedPlan.ID)

	if applySessionMutation != nil {
		if err := s.applyExitPlanModeV3ModeMutation(sessionID, sessionruntime.ModeAuto, applySessionMutation); err != nil {
			return "", fmt.Errorf("exit_plan_mode failed to set mode: %w", err)
		}
	} else if _, setModeEnv, err := s.sessions.SetMode(sessionID, sessionruntime.ModeAuto); err != nil {
		return "", fmt.Errorf("exit_plan_mode failed to set mode: %w", err)
	} else if setModeEnv != nil {
		s.publishEventEnvelope(*setModeEnv)
	}

	payload := map[string]any{
		"tool":                    "exit_plan_mode",
		"status":                  "approved",
		"title":                   savedPlan.Title,
		"plan_id":                 planID,
		"plan":                    savedPlan.Plan,
		"document":                savedPlan.Document,
		"approval_state":          "approved",
		"requested_modifications": []string{},
		"mode_changed":            true,
		"target_mode":             sessionruntime.ModeAuto,
		"user_message":            userMessage,
		"path_id":                 "tool.exit-plan-mode.v3",
		"summary":                 "structured plan saved, approved; mode switched to auto",
		"details_truncated":       false,
		"version":                 savedPlan.Version,
		"parent_revision":         savedPlan.ParentRevision,
	}
	encoded, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return "", marshalErr
	}
	return string(encoded), nil
}

func (s *Service) applyExitPlanModeV3ModeMutation(sessionID, mode string, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
	if applySessionMutation == nil {
		return errors.New("v3 mode mutation callback is not configured")
	}
	if s == nil || s.sessions == nil {
		return errors.New("session service is not configured")
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("session %q not found", strings.TrimSpace(sessionID))
	}
	mode = sessionruntime.NormalizeMode(mode)
	if sessionruntime.NormalizeMode(session.Mode) == mode {
		return nil
	}
	now := time.Now().UnixMilli()
	next := session
	next.Mode = mode
	next.UpdatedAt = now
	eventPayload, err := json.Marshal(map[string]any{"session_id": sessionID, "mode": mode, "updated_at": now})
	if err != nil {
		return err
	}
	payloadHash := exitPlanModeV3ModePayloadHash(sessionID, mode, now)
	clientRequestID := fmt.Sprintf("exit_plan_mode:mode:%s:%s:%d", strings.TrimSpace(sessionID), mode, now)
	_, err = applySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          session.UserID,
		AccountScopeID:  session.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationUpdateMode,
		EventType:       "session.mode.updated",
		EventPayload:    eventPayload,
		Session:         &next,
		NowUnixMs:       now,
	})
	return err
}

func exitPlanModeV3ModePayloadHash(sessionID, mode string, now int64) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID) + "\x00exit_plan_mode\x00" + strings.TrimSpace(mode) + "\x00" + fmt.Sprint(now)))
	return hex.EncodeToString(sum[:])
}

func (s *Service) executePlanManageTool(sessionID, arguments, feedback string) (string, error) {
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
	case "update", "edit":
		if strings.TrimSpace(mapString(args, "plan")) == "" && args["document"] == nil {
			action = "patch"
		} else {
			action = "save"
		}
	case "update-info", "update_info", "patch-info", "patch_info":
		action = "update_info"
	case "upsert-checkpoint", "upsert_checkpoint", "replace-checkpoint", "replace_checkpoint":
		action = "upsert_checkpoint"
	case "update-checkpoint", "update_checkpoint", "patch-checkpoint", "patch_checkpoint":
		action = "update_checkpoint"
	case "complete-checkpoint", "complete_checkpoint", "finish-checkpoint", "finish_checkpoint":
		action = "complete_checkpoint"
	case "remove-checkpoint", "remove_checkpoint", "delete-checkpoint", "delete_checkpoint":
		action = "remove_checkpoint"
	case "reorder-checkpoints", "reorder_checkpoints":
		action = "reorder_checkpoints"
	case "set-active-checkpoint", "set_active_checkpoint", "activate-checkpoint", "activate_checkpoint":
		action = "set_active_checkpoint"
	case "update-section", "update_section":
		action = "update_section"
	}
	if action == "" {
		action = "list"
	}

	switch action {
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
		revisions, err := s.sessions.ListPlanRevisions(sessionID, planID, limit)
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
		plan, _, err := s.sessions.SavePlanWithMetadata(sessionID, planID, title, planBody, status, approvalState, activate, sessionruntime.PlanSaveMetadata{UpdateSummary: updateSummary, UpdateScope: updateScope, UpdateKind: updateKind, Checkpoint: checkpoint, Document: document})
		if err != nil {
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
	case "patch", "update_section", "update_info", "upsert_checkpoint", "update_checkpoint", "complete_checkpoint", "remove_checkpoint", "reorder_checkpoints", "set_active_checkpoint":
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
		var activate *bool
		if _, hasActivate := args["activate"]; hasActivate {
			value := mapBool(args, "activate")
			activate = &value
		}
		plan, _, err := s.sessions.PatchPlan(sessionID, sessionruntime.PlanPatchOptions{PlanID: planID, Title: title, Status: status, ApprovalState: approvalState, Activate: activate, Patch: patch, Document: document, DocumentPatch: documentPatch, Metadata: sessionruntime.PlanSaveMetadata{UpdateSummary: updateSummary, UpdateScope: updateScope, UpdateKind: updateKind, Checkpoint: checkpoint}})
		if err != nil {
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
		return marshalPlanManagePayload(payload)
	case "new":
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

func planManagePlanSummary(plan pebblestore.SessionPlanSnapshot, includePreview bool) map[string]any {
	item := map[string]any{
		"id":              plan.ID,
		"title":           plan.Title,
		"status":          plan.Status,
		"approval_state":  plan.ApprovalState,
		"active":          plan.Active,
		"updated_at":      plan.UpdatedAt,
		"version":         plan.Version,
		"parent_revision": plan.ParentRevision,
		"update_summary":  plan.UpdateSummary,
		"update_scope":    plan.UpdateScope,
		"update_kind":     plan.UpdateKind,
		"checkpoint":      plan.Checkpoint,
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
		launch, prepareErr := s.prepareDelegatedSubagentLaunch(parentSession, sessionMode, taskLaunchPrepared{
			LaunchIndex:       i + 1,
			RequestedSubagent: requestedSubagent,
			MetaPrompt:        metaPrompt,
			AssignmentLabel:   spec.AssignmentLabel,
		}, description, strings.TrimSpace(req.TargetedSubagentName), req.ApplySessionMutation)
		if prepareErr != nil {
			return "", prepareErr
		}
		prepared = append(prepared, launch)
	}

	taskToolName := strings.TrimSpace(call.Name)
	if taskToolName == "" {
		taskToolName = "task"
	}
	taskCallID := strings.TrimSpace(call.CallID)
	if taskCallID == "" {
		taskCallID = fmt.Sprintf("task_%d", time.Now().UnixMilli())
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
				"current_tool":         strings.TrimSpace(launch.CurrentTool),
				"current_tool_ms":      currentToolMS,
				"elapsed_ms":           elapsedMS,
				"tool_started":         launch.ToolStarted,
				"tool_completed":       launch.ToolCompleted,
				"tool_failed":          launch.ToolFailed,
				"tool_order":           append([]string(nil), launch.ToolOrder...),
				"error":                strings.TrimSpace(launch.Error),
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
	taskProgressMu := sync.Mutex{}
	taskProgress := make([]taskLaunchOutcome, len(prepared))
	taskProgressInitialized := make([]bool, len(prepared))
	emitTaskProgress := func(phase, summary string, launch taskLaunchOutcome) {
		phase = strings.TrimSpace(phase)
		status := taskStreamStatusForPhase(phase)
		terminal := status == "ok" || status == "error"
		launch.Phase = phase
		idx := launch.LaunchIndex - 1
		launches := make([]map[string]any, 0, len(prepared))
		taskProgressMu.Lock()
		if idx >= 0 && idx < len(taskProgress) {
			taskProgress[idx] = launch
			taskProgressInitialized[idx] = true
		}
		for i := range taskProgress {
			if !taskProgressInitialized[i] {
				continue
			}
			row := taskProgress[i]
			rowStatus := taskStreamStatusForPhase(row.Phase)
			rowTerminal := rowStatus == "ok" || rowStatus == "error"
			launches = append(launches, buildTaskStreamLaunchPayload(row, rowStatus, row.Phase, rowTerminal))
		}
		taskProgressMu.Unlock()
		if len(launches) == 0 {
			launches = append(launches, buildTaskStreamLaunchPayload(launch, status, phase, terminal))
		}
		summary = strings.TrimSpace(summary)
		if summary == "" {
			summary = fmt.Sprintf("%d subagent launch(es) active", len(launches))
		}
		payload := buildTaskStreamPayload(parentSession.ID, action, description, len(prepared), launch, phase, summary)
		payload["launches"] = launches
		emitTaskStreamPayload(emit, step, taskToolName, taskCallID, payload)
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
			DisabledTools:        taskDisabledTools(false),
			PermissionSessionID:  sessionID,
			Principal:            req.Principal,
			ApplySessionMutation: req.ApplySessionMutation,
		}, func(event StreamEvent) {
			eventType := strings.ToLower(strings.TrimSpace(event.Type))
			switch eventType {
			case StreamEventStepStarted:
				emitTaskProgress("running", "", outcome)
			case StreamEventToolStarted:
				nowMS := time.Now().UnixMilli()
				toolName := emptyToolName(strings.TrimSpace(event.ToolName))
				outcome.ToolStarted++
				outcome.CurrentTool = toolName
				outcome.CurrentToolStarted = nowMS
				outcome.CurrentToolMS = 0
				outcome.CurrentPreviewKind = "tool"
				outcome.CurrentPreviewText = ""
				if toolName != "" {
					outcome.ToolOrder = append(outcome.ToolOrder, toolName)
				}
				if outcome.LaunchStartedAtMS <= 0 {
					outcome.LaunchStartedAtMS = nowMS
				}
				emitTaskProgress("tool.started", fmt.Sprintf("launch %d running %s", outcome.LaunchIndex, outcome.CurrentTool), outcome)
			case StreamEventToolDelta:
				outcome.CurrentPreviewKind = "tool"
				outcome.CurrentPreviewText = appendTaskPreviewText(outcome.CurrentPreviewText, event.Output, taskStreamPreviewMaxChars, true)
				emitTaskProgress("tool.delta", "", outcome)
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
				if strings.TrimSpace(event.Error) != "" {
					outcome.ToolFailed++
				}
				summary := fmt.Sprintf("launch %d completed %s", outcome.LaunchIndex, completedTool)
				if strings.TrimSpace(event.Error) != "" {
					summary = fmt.Sprintf("launch %d failed %s: %s", outcome.LaunchIndex, completedTool, strings.TrimSpace(event.Error))
				}
				emitTaskProgress("tool.completed", summary, outcome)
				if strings.TrimSpace(event.Error) == "" {
					outcome.CurrentTool = ""
					outcome.CurrentToolStarted = 0
					outcome.CurrentToolMS = 0
					outcome.CurrentPreviewKind = ""
					outcome.CurrentPreviewText = ""
				}
			case StreamEventReasoningDelta:
				outcome.CurrentPreviewKind = "reasoning"
				outcome.CurrentPreviewText = setTaskPreviewText(event.Delta, taskStreamPreviewMaxChars, false)
				emitTaskProgress("reasoning.delta", "", outcome)
			case StreamEventAssistantDelta, StreamEventAssistantCommentary:
				outcome.CurrentPreviewKind = "assistant"
				outcome.CurrentPreviewText = appendTaskPreviewText(outcome.CurrentPreviewText, event.Delta, taskStreamPreviewMaxChars, false)
				emitTaskProgress("assistant.delta", "", outcome)
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
			outcome.Error = strings.TrimSpace(runErr.Error())
			outcome.Summary = fmt.Sprintf("launch %d subagent %s failed", outcome.LaunchIndex, outcome.ResolvedSubagent)
			if outcome.Error != "" {
				outcome.Summary += ": " + outcome.Error
			}
			emitTaskProgress("failed", outcome.Summary, outcome)
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
			failedCount++
			if firstErr == nil {
				firstErr = err
			}
			if strings.TrimSpace(launch.Error) == "" {
				launch.Error = strings.TrimSpace(err.Error())
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
		if strings.TrimSpace(launch.Error) != "" {
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
		if status == "error" {
			launchPhase = "failed"
		}
		launch.Phase = launchPhase
		launch.ReportTruncated = reportTruncated
		launchPayload := buildTaskStreamLaunchPayload(launch, status, launchPhase, true)
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
	if failedCount > 0 {
		overallStatus = "error"
	}
	aggregateSummary := strings.TrimSpace(strings.Join(summaryParts, " | "))
	if aggregateSummary == "" {
		aggregateSummary = fmt.Sprintf("%d launch(es) completed", len(outcomes))
	}
	if aggregateReportBudgetExceeded {
		aggregateSummary += " | warning: aggregate subagent reports exceeded inline context budget; inspect report_ref child session transcripts for full reports"
	}
	lineageUpdate(overallStatus, outcomes, map[string]any{
		"success_count":  successCount,
		"failed_count":   failedCount,
		"tool_started":   totalToolStarted,
		"tool_completed": totalToolCompleted,
		"tool_failed":    totalToolFailed,
		"summary":        aggregateSummary,
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
	case "manage-worktree", "manage_worktree":
		return "manage_worktree"
	case "manage-todos", "manage_todos":
		return "manage_todos"
	case "manage-flow", "manage_flow":
		return "manage_flow"
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
	case "read", "search", "websearch", "webfetch", "agentic_search", "list", "skill_use", "manage_worktree", "manage_todos", "manage_theme", "manage_integrations":
		return toolName, false
	case "manage_image":
		if shouldApproveManageImage(arguments) {
			return "image_generation", true
		}
		return "manage_image", false
	case "plan_manage":
		if permission.ShouldApprovePlanManageUpdate(arguments) {
			return "plan_update", true
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
	case "manage_flow":
		if permission.ShouldApproveManageFlowMutation(arguments) {
			return "flow_change", true
		}
		return "manage_flow", false
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
