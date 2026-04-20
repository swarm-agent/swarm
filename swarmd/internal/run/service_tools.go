package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
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
	ReportChars        int
	ReportExcerpt      string
	ReportFile         string
	ReportPersistErr   string
	ReportTruncated    bool
	Summary            string
	Error              string
}

func buildTaskLaunchOutcome(launch taskLaunchPrepared) taskLaunchOutcome {
	resolved := strings.TrimSpace(launch.SubagentProfile.Name)
	if resolved == "" {
		resolved = "explorer"
	}
	requested := strings.TrimSpace(launch.RequestedSubagent)
	if requested == "" {
		requested = "explorer"
	}
	metaPrompt := strings.TrimSpace(launch.MetaPrompt)
	if metaPrompt == "" {
		metaPrompt = fmt.Sprintf("Use the %s role.", resolved)
	}
	return taskLaunchOutcome{
		LaunchIndex:        launch.LaunchIndex,
		RequestedSubagent:  requested,
		ResolvedSubagent:   resolved,
		MetaPrompt:         metaPrompt,
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

func taskLaunchProgressDurations(launch taskLaunchOutcome) (elapsedMS, currentToolMS int64) {
	elapsedMS = launch.ElapsedMS
	if elapsedMS <= 0 && launch.LaunchStartedAtMS > 0 {
		elapsedMS = maxInt64(0, time.Now().UnixMilli()-launch.LaunchStartedAtMS)
	}
	currentToolMS = launch.CurrentToolMS
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

func buildTaskStreamPayload(parentSessionID, action, description string, launchCount int, launch taskLaunchOutcome, phase, summary string) map[string]any {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = fmt.Sprintf("subagent %s running", launch.ResolvedSubagent)
	}
	if launchCount <= 0 {
		launchCount = 1
	}
	elapsedMS, currentToolMS := taskLaunchProgressDurations(launch)
	status := taskStreamStatusForPhase(phase)
	previewKind, previewText := publicTaskPreview(launch.CurrentPreviewKind, launch.CurrentPreviewText)
	reasoningSummary := strings.TrimSpace(launch.ReasoningSummary)
	return map[string]any{
		"tool":              "task",
		"action":            action,
		"status":            status,
		"phase":             strings.TrimSpace(phase),
		"launch_count":      launchCount,
		"description":       description,
		"goal":              description,
		"parent_session_id": strings.TrimSpace(parentSessionID),
		"path_id":           "tool.task.stream.v1",
		"summary":           summary,
		"details_truncated": false,
		"launches": []map[string]any{{
			"launch_index":               launch.LaunchIndex,
			"status":                     status,
			"requested_subagent":         strings.TrimSpace(launch.RequestedSubagent),
			"subagent":                   strings.TrimSpace(launch.ResolvedSubagent),
			"agent_type":                 strings.TrimSpace(launch.ResolvedSubagent),
			"meta_prompt":                strings.TrimSpace(launch.MetaPrompt),
			"child_session_id":           strings.TrimSpace(launch.ChildSessionID),
			"child_mode":                 strings.TrimSpace(launch.ChildMode),
			"workspace_path":             strings.TrimSpace(launch.WorkspacePath),
			"workspace_name":             strings.TrimSpace(launch.WorkspaceName),
			"worktree_enabled":           launch.WorktreeEnabled,
			"worktree_root_path":         strings.TrimSpace(launch.WorktreeRootPath),
			"worktree_branch":            strings.TrimSpace(launch.WorktreeBranch),
			"phase":                      strings.TrimSpace(phase),
			"launch_started_at_ms":       launch.LaunchStartedAtMS,
			"current_tool":               strings.TrimSpace(launch.CurrentTool),
			"current_tool_started_at_ms": launch.CurrentToolStarted,
			"current_tool_ms":            currentToolMS,
			"current_preview_kind":       previewKind,
			"current_preview_text":       previewText,
			"reasoning_summary":          reasoningSummary,
			"elapsed_ms":                 elapsedMS,
			"tool_started":               launch.ToolStarted,
			"tool_completed":             launch.ToolCompleted,
			"tool_failed":                launch.ToolFailed,
			"tool_order":                 append([]string(nil), launch.ToolOrder...),
		}},
	}
}

func emitTaskStreamDelta(parentSessionID string, emit StreamHandler, step int, toolName, callID, action, description string, launchCount int, launch taskLaunchOutcome, phase, summary string) {
	if emit == nil {
		return
	}
	if strings.TrimSpace(toolName) == "" {
		toolName = "task"
	}
	payload := buildTaskStreamPayload(parentSessionID, action, description, launchCount, launch, phase, summary)
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

func (s *Service) prepareDelegatedSubagentLaunch(parentSession pebblestore.SessionSnapshot, sessionMode string, launch taskLaunchPrepared, description, targetedSubagentName string) (taskLaunchPrepared, error) {
	requestedSubagent := strings.TrimSpace(launch.RequestedSubagent)
	if requestedSubagent == "" {
		requestedSubagent = "explorer"
	}
	subagentProfile, err := s.resolveTaskSubagent(requestedSubagent)
	if err != nil {
		return taskLaunchPrepared{}, err
	}
	if strings.TrimSpace(subagentProfile.Name) == "" {
		subagentProfile.Name = "explorer"
	}

	childTitle := fmt.Sprintf("%s #%d (@%s subagent)", truncateRunes(description, 64), launch.LaunchIndex, strings.TrimSpace(subagentProfile.Name))
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
		"title_pending":      true,
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

	createOptions := sessionruntime.CreateSessionOptions{
		SessionID:     childSessionID,
		Title:         childTitle,
		WorkspacePath: childWorkspacePath,
		WorkspaceName: childWorkspaceName,
		Preference: func() *pebblestore.ModelPreference {
			preference := applyAgentPreferenceOverrides(parentSession.Preference, subagentProfile)
			return &preference
		}(),
		Metadata: childMetadata,
	}
	if childWorktreeEnabled {
		createOptions.Worktree = &sessionruntime.CreateSessionWorktree{
			RootPath:    childWorktreeRootPath,
			BaseBranch:  childWorktreeBaseBranch,
			BranchName:  childWorktreeBranch,
			WorkspaceID: childWorkspaceID,
		}
	}
	childSession, childCreatedEnv, err := s.sessions.CreateSessionWithOptions(createOptions)
	if err != nil {
		return taskLaunchPrepared{}, fmt.Errorf("task failed to create subagent session: %w", err)
	}
	if childCreatedEnv != nil {
		s.publishEventEnvelope(*childCreatedEnv)
	}

	childMode := effectiveTaskChildMode(sessionMode)
	if _, setModeEnv, setModeErr := s.sessions.SetMode(childSession.ID, childMode); setModeErr != nil {
		return taskLaunchPrepared{}, fmt.Errorf("task failed to set subagent mode: %w", setModeErr)
	} else if setModeEnv != nil {
		s.publishEventEnvelope(*setModeEnv)
	}

	launch.RequestedSubagent = requestedSubagent
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
		auth, err := s.permissions.AuthorizeToolCall(permission.AuthorizationInput{
			SessionID:     sessionID,
			RunID:         runID,
			CallID:        toolCalls[i].CallID,
			ToolName:      toolCalls[i].Name,
			ToolArguments: s.permissionArgumentsForCall(sessionID, sessionMode, toolCalls[i]),
			Mode:          sessionMode,
			Overlay:       overlay,
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
					Arguments:  strings.TrimSpace(toolCalls[i].Arguments),
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
						Arguments:  strings.TrimSpace(call.Arguments),
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
		output, err := s.executeExitPlanModeTool(sessionID, sessionMode, agentProfile, call.Arguments, approvedArguments)
		result.Output = output
		return true, result, err
	case "plan_manage":
		output, err := s.executePlanManageTool(sessionID, call.Arguments, approvedArguments)
		result.Output = output
		return true, result, err
	case "task":
		output, err := s.executeTaskTool(ctx, sessionID, sessionMode, step, call, emit)
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

func (s *Service) executeExitPlanModeTool(sessionID, sessionMode string, agentProfile pebblestore.AgentProfile, arguments, feedback string) (string, error) {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		arguments = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("exit_plan_mode arguments invalid: %w", err)
	}
	title := strings.TrimSpace(mapString(args, "title"))
	plan := strings.TrimSpace(mapString(args, "plan"))
	planID := strings.TrimSpace(mapString(args, "plan_id"))
	if planID == "" {
		planID = strings.TrimSpace(mapString(args, "planID"))
	}
	if planID == "" {
		planID = fmt.Sprintf("plan_%d", time.Now().UnixMilli())
	}

	if title == "" || plan == "" {
		return "", errors.New("exit_plan_mode requires title and plan")
	}
	if s.sessions == nil {
		return "", errors.New("session service is not configured")
	}

	userMessage := normalizePermissionFeedback(feedback)
	if !pebblestore.AgentExitPlanModeEnabled(agentProfile) {
		payload := map[string]any{
			"tool":              "exit_plan_mode",
			"status":            "rejected",
			"title":             title,
			"plan_id":           planID,
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
	status := "submitted"
	approvalState := "pending_review"
	if sessionruntime.NormalizeMode(sessionMode) != sessionruntime.ModePlan {
		status = "rejected"
		approvalState = "not_in_plan_mode"
	} else {
		status = "approved"
		approvalState = "approved"
	}
	savedPlan, _, saveErr := s.sessions.SavePlan(sessionID, planID, title, plan, status, approvalState, true)
	if saveErr != nil {
		return "", fmt.Errorf("exit_plan_mode failed to save plan: %w", saveErr)
	}
	planID = strings.TrimSpace(savedPlan.ID)

	if sessionruntime.NormalizeMode(sessionMode) != sessionruntime.ModePlan {
		payload := map[string]any{
			"tool":                    "exit_plan_mode",
			"status":                  "rejected",
			"plan_id":                 planID,
			"title":                   title,
			"approval_state":          "not_in_plan_mode",
			"requested_modifications": []string{"Do not call exit_plan_mode from auto. To update the active plan instead, use plan_manage with exactly: {\"action\":\"save\",\"plan\":\"# Plan\\n1. ...\"}. Only call exit_plan_mode when leaving plan mode."},
			"path_id":                 "tool.exit-plan-mode.v3",
			"summary":                 "plan saved but exit_plan_mode rejected: session not in plan mode; use plan_manage save to update the active plan instead",
			"user_message":            userMessage,
			"details_truncated":       false,
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}

	if _, setModeEnv, err := s.sessions.SetMode(sessionID, sessionruntime.ModeAuto); err != nil {
		return "", fmt.Errorf("exit_plan_mode failed to set mode: %w", err)
	} else if setModeEnv != nil {
		s.publishEventEnvelope(*setModeEnv)
	}

	payload := map[string]any{
		"tool":                    "exit_plan_mode",
		"status":                  "approved",
		"title":                   title,
		"plan_id":                 planID,
		"approval_state":          "approved",
		"requested_modifications": []string{},
		"mode_changed":            true,
		"target_mode":             sessionruntime.ModeAuto,
		"user_message":            userMessage,
		"path_id":                 "tool.exit-plan-mode.v3",
		"summary":                 "plan saved, approved; mode switched to auto",
		"details_truncated":       false,
	}
	encoded, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return "", marshalErr
	}
	return string(encoded), nil
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
	case "upsert", "set", "update", "edit", "write-active", "write_active":
		action = "save"
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
			items = append(items, map[string]any{
				"id":             plans[i].ID,
				"title":          plans[i].Title,
				"status":         plans[i].Status,
				"approval_state": plans[i].ApprovalState,
				"active":         plans[i].Active,
				"updated_at":     plans[i].UpdatedAt,
				"preview":        truncateRunes(plans[i].Plan, 180),
			})
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
		if planBody == "" {
			return "", errors.New("plan_manage save requires plan")
		}
		status := strings.TrimSpace(mapString(args, "status"))
		approvalState := strings.TrimSpace(mapString(args, "approval_state"))
		if planID != "" {
			existing, ok, err := s.sessions.GetPlan(sessionID, planID)
			if err != nil {
				return "", err
			}
			if ok {
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
		plan, _, err := s.sessions.SavePlan(sessionID, planID, title, planBody, status, approvalState, activate)
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
	case "new":
		title := strings.TrimSpace(mapString(args, "title"))
		if title == "" {
			title = "New Plan"
		}
		plan, _, err := s.sessions.StartNewPlan(sessionID, title)
		if err != nil {
			return "", err
		}
		payload := map[string]any{
			"tool":              "plan_manage",
			"action":            "new",
			"status":            "ok",
			"plan":              plan,
			"path_id":           "tool.plan-manage.v3",
			"summary":           fmt.Sprintf("created plan %s", plan.ID),
			"details_truncated": false,
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
	PermissionSessionID  string
	TargetedSubagentName string
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
	return s.executeTaskToolWithParsed(ctx, sessionID, sessionMode, step, call, emit, taskExecutionRequest{})
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
	reportMaxChars := parsed.ReportMaxChars
	launchSpecs := append([]taskLaunchSpec(nil), parsed.Launches...)
	if len(launchSpecs) == 0 {
		launchSpecs = []taskLaunchSpec{{RequestedSubagentType: "explorer"}}
	}
	if strings.TrimSpace(req.TargetedSubagentName) != "" {
		launchSpecs = []taskLaunchSpec{{RequestedSubagentType: strings.TrimSpace(req.TargetedSubagentName)}}
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
	if len(parentMessages) == 0 {
		parentMessages, err = s.loadDelegationTranscriptMessages(parentSession.ID)
		if err != nil {
			return "", err
		}
	}

	prepared := make([]taskLaunchPrepared, 0, len(launchSpecs))
	for i := range launchSpecs {
		spec := launchSpecs[i]
		requestedSubagent := strings.TrimSpace(spec.RequestedSubagentType)
		if requestedSubagent == "" {
			requestedSubagent = "explorer"
		}
		metaPrompt := strings.TrimSpace(spec.MetaPrompt)
		launch, prepareErr := s.prepareDelegatedSubagentLaunch(parentSession, sessionMode, taskLaunchPrepared{
			LaunchIndex:       i + 1,
			RequestedSubagent: requestedSubagent,
			MetaPrompt:        metaPrompt,
		}, description, strings.TrimSpace(req.TargetedSubagentName))
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
			elapsedMS := launch.ElapsedMS
			if elapsedMS <= 0 && launch.LaunchStartedAtMS > 0 {
				elapsedMS = maxInt64(0, time.Now().UnixMilli()-launch.LaunchStartedAtMS)
			}
			currentToolMS := launch.CurrentToolMS
			if currentToolMS <= 0 && launch.CurrentToolStarted > 0 && strings.TrimSpace(launch.CurrentTool) != "" {
				currentToolMS = maxInt64(0, time.Now().UnixMilli()-launch.CurrentToolStarted)
			}
			launchRows = append(launchRows, map[string]any{
				"launch_index":         launch.LaunchIndex,
				"requested_subagent":   strings.TrimSpace(launch.RequestedSubagent),
				"subagent":             strings.TrimSpace(launch.ResolvedSubagent),
				"meta_prompt":          strings.TrimSpace(launch.MetaPrompt),
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
			})
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
	emitTaskDelta := func(phase, summary string, launch taskLaunchOutcome) {
		emitTaskStreamDelta(parentSession.ID, emit, step, taskToolName, taskCallID, action, description, len(prepared), launch, phase, summary)
	}

	spawned := make([]taskLaunchOutcome, 0, len(prepared))
	for i := range prepared {
		launch := buildTaskLaunchOutcome(prepared[i])
		spawned = append(spawned, launch)
		emitTaskDelta("spawned", fmt.Sprintf("spawned launch %d %s subagent in %s", launch.LaunchIndex, launch.ResolvedSubagent, launch.ChildMode), launch)
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
			ReportMaxChars:       reportMaxChars,
			ParentSession:        parentSession,
			ParentMessages:       parentMessages,
			PermissionSessionID:  req.PermissionSessionID,
			TargetedSubagentName: req.TargetedSubagentName,
		})
		subResult, runErr := s.RunTurnStreaming(runCtx, launch.ChildSession.ID, RunRequest{
			Prompt:     delegatedPrompt,
			TargetKind: RunTargetKindSubagent,
			TargetName: launch.SubagentProfile.Name,
			AgentName:  launch.SubagentProfile.Name,
		}, RunStartMeta{
			AllowSubagent:       true,
			DisabledTools:       taskDisabledTools(false),
			PermissionSessionID: sessionID,
		}, func(event StreamEvent) {
			eventType := strings.ToLower(strings.TrimSpace(event.Type))
			switch eventType {
			case StreamEventStepStarted:
				if outcome.ElapsedMS <= 0 && outcome.LaunchStartedAtMS > 0 {
					outcome.ElapsedMS = maxInt64(0, time.Now().UnixMilli()-outcome.LaunchStartedAtMS)
				}
				emitTaskDelta("running", "", outcome)
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
				outcome.ElapsedMS = maxInt64(0, nowMS-outcome.LaunchStartedAtMS)
				emitTaskDelta("tool.started", fmt.Sprintf("launch %d running %s", outcome.LaunchIndex, outcome.CurrentTool), outcome)
			case StreamEventToolDelta:
				outcome.CurrentPreviewKind = "tool"
				outcome.CurrentPreviewText = appendTaskPreviewText(outcome.CurrentPreviewText, event.Output, taskStreamPreviewMaxChars, true)
				emitTaskDelta("tool.delta", "", outcome)
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
				emitTaskDelta("tool.completed", summary, outcome)
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
				emitTaskDelta("reasoning.delta", "", outcome)
			case StreamEventAssistantDelta, StreamEventAssistantCommentary:
				outcome.CurrentPreviewKind = "assistant"
				outcome.CurrentPreviewText = appendTaskPreviewText(outcome.CurrentPreviewText, event.Delta, taskStreamPreviewMaxChars, false)
				emitTaskDelta("assistant.delta", "", outcome)
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
			emitTaskDelta("failed", outcome.Summary, outcome)
			return outcome, runErr
		}

		report := strings.TrimSpace(subResult.AssistantMessage.Content)
		if report == "" {
			report = "Subagent completed without a textual report."
		}
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
		if outcome.ReportChars > reportMaxChars {
			outcome.ReportTruncated = true
			outcome.ReportExcerpt = truncateRunes(report, reportMaxChars)
			if path, writeErr := persistTaskReport(parentSession.WorkspacePath, outcome.ResolvedSubagent, description, report); writeErr != nil {
				outcome.ReportPersistErr = strings.TrimSpace(writeErr.Error())
			} else {
				outcome.ReportFile = strings.TrimSpace(path)
			}
		}
		outcome.Summary = summarizePlainToolOutput(report, taskReportPreviewChars, 2)
		if outcome.Summary == "" {
			outcome.Summary = fmt.Sprintf("launch %d subagent %s completed", outcome.LaunchIndex, outcome.ResolvedSubagent)
		}
		emitTaskDelta("completed", outcome.Summary, outcome)
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
	nextActions := make([]string, 0, len(outcomes))
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
		if strings.TrimSpace(launch.ReportFile) != "" {
			nextActions = append(nextActions, fmt.Sprintf("launch %d: read %s", launch.LaunchIndex, launch.ReportFile))
		}
		launchPreviewKind, launchPreviewText := publicTaskPreview(launch.CurrentPreviewKind, launch.CurrentPreviewText)
		launchPhase := "completed"
		if status == "error" {
			launchPhase = "failed"
		}
		launchPayloads = append(launchPayloads, map[string]any{
			"launch_index":               launch.LaunchIndex,
			"status":                     status,
			"requested_subagent":         strings.TrimSpace(launch.RequestedSubagent),
			"subagent":                   strings.TrimSpace(launch.ResolvedSubagent),
			"agent_type":                 strings.TrimSpace(launch.ResolvedSubagent),
			"meta_prompt":                strings.TrimSpace(launch.MetaPrompt),
			"session_id":                 strings.TrimSpace(launch.ChildSessionID),
			"mode":                       strings.TrimSpace(launch.ChildMode),
			"workspace_path":             strings.TrimSpace(launch.WorkspacePath),
			"workspace_name":             strings.TrimSpace(launch.WorkspaceName),
			"worktree_enabled":           launch.WorktreeEnabled,
			"worktree_root_path":         strings.TrimSpace(launch.WorktreeRootPath),
			"worktree_branch":            strings.TrimSpace(launch.WorktreeBranch),
			"phase":                      launchPhase,
			"launch_started_at_ms":       launch.LaunchStartedAtMS,
			"current_tool":               strings.TrimSpace(launch.CurrentTool),
			"current_tool_started_at_ms": launch.CurrentToolStarted,
			"current_tool_ms":            launch.CurrentToolMS,
			"current_preview_kind":       launchPreviewKind,
			"current_preview_text":       launchPreviewText,
			"reasoning_summary":          strings.TrimSpace(launch.ReasoningSummary),
			"elapsed_ms":                 launch.ElapsedMS,
			"error":                      strings.TrimSpace(launch.Error),
			"summary":                    strings.TrimSpace(launch.Summary),
			"report_chars":               launch.ReportChars,
			"report_truncated":           launch.ReportTruncated,
			"report_file":                strings.TrimSpace(launch.ReportFile),
			"tool_started":               launch.ToolStarted,
			"tool_completed":             launch.ToolCompleted,
			"tool_failed":                launch.ToolFailed,
			"tool_order":                 append([]string(nil), launch.ToolOrder...),
		})
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
	}
	if len(outcomes) > 0 {
		first := outcomes[0]
		payload["subagent"] = strings.TrimSpace(first.ResolvedSubagent)
		payload["agent_type"] = strings.TrimSpace(first.ResolvedSubagent)
		payload["requested_subagent"] = strings.TrimSpace(first.RequestedSubagent)
		payload["session_id"] = strings.TrimSpace(first.ChildSessionID)
		payload["mode"] = strings.TrimSpace(first.ChildMode)
		payload["workspace_path"] = strings.TrimSpace(first.WorkspacePath)
		payload["worktree_enabled"] = first.WorktreeEnabled
		payload["worktree_root_path"] = strings.TrimSpace(first.WorktreeRootPath)
		payload["worktree_branch"] = strings.TrimSpace(first.WorktreeBranch)
	}
	if len(nextActions) > 0 {
		payload["report_available"] = true
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
	nameOrPurpose = strings.TrimSpace(nameOrPurpose)
	if nameOrPurpose == "" {
		nameOrPurpose = "explorer"
	}
	if s.agents == nil {
		return pebblestore.NormalizeAgentProfile(pebblestore.AgentProfile{
			Name:             strings.ToLower(nameOrPurpose),
			Mode:             agentruntime.ModeSubagent,
			Prompt:           "You are a delegated subagent.",
			ExecutionSetting: pebblestore.AgentExecutionSettingRead,
			Enabled:          true,
		}), nil
	}
	return s.agents.ResolveSubagent(nameOrPurpose)
}

type taskDelegationPromptConfig struct {
	Description          string
	Prompt               string
	ReportMaxChars       int
	ParentSession        pebblestore.SessionSnapshot
	ParentMessages       []pebblestore.MessageSnapshot
	PermissionSessionID  string
	TargetedSubagentName string
}

func buildTaskDelegationPrompt(config taskDelegationPromptConfig) string {
	description := strings.TrimSpace(config.Description)
	prompt := strings.TrimSpace(config.Prompt)
	reportMaxChars := config.ReportMaxChars
	if description == "" {
		description = "delegated task"
	}
	if reportMaxChars <= 0 {
		reportMaxChars = taskReportDefaultChars
	}

	var b strings.Builder
	b.WriteString("Delegated task context:\n")
	b.WriteString("- description: ")
	b.WriteString(description)
	b.WriteString("\n")
	b.WriteString("- final report max chars: ")
	b.WriteString(fmt.Sprintf("%d", reportMaxChars))
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

func (s *Service) loadDelegationTranscriptMessages(sessionID string) ([]pebblestore.MessageSnapshot, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || s == nil || s.sessions == nil {
		return nil, nil
	}
	messages, err := s.sessions.ListMessages(sessionID, 0, taskDelegationTranscriptMsgLimit)
	if err != nil {
		return nil, fmt.Errorf("list parent transcript messages: %w", err)
	}
	return append([]pebblestore.MessageSnapshot(nil), messages...), nil
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

func persistTaskReport(workspacePath, subagentName, description, content string) (string, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return "", errors.New("workspace path is empty")
	}
	reportDir := filepath.Join(workspacePath, ".swarm", "subagent-reports")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		return "", fmt.Errorf("create report directory: %w", err)
	}

	stamp := time.Now().UTC().Format("20060102-150405")
	safeAgent := sanitizeTaskReportName(subagentName)
	if safeAgent == "" {
		safeAgent = "subagent"
	}
	filename := fmt.Sprintf("%s-%s.md", stamp, safeAgent)
	reportPath := filepath.Join(reportDir, filename)

	var b strings.Builder
	b.WriteString("# Subagent Report\n\n")
	b.WriteString("- subagent: ")
	b.WriteString(strings.TrimSpace(subagentName))
	b.WriteString("\n")
	b.WriteString("- description: ")
	b.WriteString(strings.TrimSpace(description))
	b.WriteString("\n")
	b.WriteString("- generated_at_utc: ")
	b.WriteString(time.Now().UTC().Format(time.RFC3339))
	b.WriteString("\n\n---\n\n")
	b.WriteString(strings.TrimSpace(content))
	b.WriteString("\n")

	if err := os.WriteFile(reportPath, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("write report file: %w", err)
	}

	relPath, err := filepath.Rel(workspacePath, reportPath)
	if err != nil {
		return reportPath, nil
	}
	relPath = strings.TrimSpace(filepath.ToSlash(relPath))
	if relPath == "" {
		return reportPath, nil
	}
	return relPath, nil
}

func sanitizeTaskReportName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-', r == '_':
			if !lastDash && b.Len() > 0 {
				b.WriteRune('-')
				lastDash = true
			}
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	result := strings.Trim(b.String(), "-")
	return result
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
	case "manage-theme", "manage_theme":
		return "manage_theme"
	case "manage-worktree", "manage_worktree":
		return "manage_worktree"
	case "manage-todos", "manage_todos":
		return "manage_todos"
	default:
		return strings.ToLower(strings.TrimSpace(name))
	}
}

func blockedToolReason(mode, toolName string) (bool, string) {
	mode = strings.TrimSpace(strings.ToLower(mode))
	toolName = canonicalToolName(toolName)
	if mode == sessionruntime.ModePlan {
		switch toolName {
		case "write", "edit":
			return true, fmt.Sprintf("%s is unavailable in plan mode", toolName)
		}
	}
	if mode == pebblestore.AgentExecutionSettingRead {
		switch toolName {
		case "write", "edit", "bash":
			return true, fmt.Sprintf("%s is unavailable for read execution setting", toolName)
		}
	}
	if mode == pebblestore.AgentExecutionSettingReadWrite {
		if toolName == "bash" {
			return true, "bash is unavailable for readwrite execution setting"
		}
	}
	return false, ""
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
	case "read", "search", "websearch", "webfetch", "agentic_search", "list", "skill_use", "manage_worktree", "manage_todos", "manage_theme":
		return toolName, false
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

func runToolDBDebugEnabled() bool {
	return false
}

func runToolDBDebugf(format string, args ...any) {
}

func isToolDBDebugMessage(content string) bool {
	return false
}

func (s *Service) appendToolDBDebug(sessionID string, payload map[string]any) {
}
