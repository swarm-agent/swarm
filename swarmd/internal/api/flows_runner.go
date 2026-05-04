package api

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/flow"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	flowSessionTitleSourceTask = "flow_task"
	flowSessionTitleMaxRunes   = 96
)

type targetLocalFlowRunner struct {
	server *Server
}

func (s *Server) NewTargetLocalFlowRunner() flow.FlowRunner {
	return targetLocalFlowRunner{server: s}
}

func (r targetLocalFlowRunner) RunAcceptedFlow(ctx context.Context, assignment flow.AcceptedAssignment, request flow.RunRequest) (flow.RunStart, error) {
	if r.server == nil {
		return flow.RunStart{}, errors.New("api server is not configured")
	}
	return r.server.runAcceptedFlow(ctx, assignment, request)
}

func (s *Server) RunAcceptedFlowNow(ctx context.Context, flowID string) (flow.RunStart, error) {
	if s == nil || s.flows == nil {
		return flow.RunStart{}, errors.New("flow store is not configured")
	}
	flowID = strings.TrimSpace(flowID)
	if flowID == "" {
		return flow.RunStart{}, errors.New("flow_id is required")
	}
	accepted, ok, err := s.flows.GetAcceptedAssignment(flowID)
	if err != nil {
		return flow.RunStart{}, err
	}
	if !ok {
		return flow.RunStart{}, fmt.Errorf("flow %q is not installed on this target", flowID)
	}
	now := time.Now().UTC()
	return s.RunAcceptedFlowNowAt(ctx, accepted, now, "")
}

func (s *Server) RunAcceptedFlowNowAt(ctx context.Context, accepted flow.AcceptedAssignment, scheduledAt time.Time, commandID string) (flow.RunStart, error) {
	if s == nil || s.flows == nil {
		return flow.RunStart{}, errors.New("flow store is not configured")
	}
	assignment := accepted.Assignment
	if strings.TrimSpace(assignment.FlowID) == "" {
		return flow.RunStart{}, errors.New("flow_id is required")
	}
	if assignment.Revision <= 0 {
		return flow.RunStart{}, errors.New("revision is required")
	}
	scheduledAt = scheduledAt.UTC()
	if scheduledAt.IsZero() {
		scheduledAt = time.Now().UTC()
	}
	runID := flowRunID(assignment.FlowID, assignment.Revision, scheduledAt, true)
	if trimmedCommandID := strings.TrimSpace(commandID); trimmedCommandID != "" {
		runID = flowRunNowCommandRunID(assignment.FlowID, assignment.Revision, scheduledAt, trimmedCommandID)
	}
	claim, inserted, err := s.flows.ClaimRun(pebblestore.FlowRunClaimRecord{
		FlowID:      assignment.FlowID,
		Revision:    assignment.Revision,
		ScheduledAt: scheduledAt,
		RunID:       runID,
		ClaimedAt:   time.Now().UTC(),
	})
	if err != nil {
		return flow.RunStart{}, err
	}
	if !inserted {
		return s.existingFlowRunStart(claim)
	}
	return s.runAcceptedFlow(ctx, accepted, flow.RunRequest{FlowID: accepted.FlowID, Revision: accepted.Revision, ScheduledAt: scheduledAt, RunNow: true, RunID: claim.RunID})
}

func (s *Server) existingFlowRunStart(claim pebblestore.FlowRunClaimRecord) (flow.RunStart, error) {
	if s == nil || s.flows == nil {
		return flow.RunStart{}, errors.New("flow store is not configured")
	}
	if strings.TrimSpace(claim.RunID) != "" {
		if run, ok, err := s.flows.GetTargetRun(claim.RunID); err != nil {
			return flow.RunStart{}, err
		} else if ok {
			return flow.RunStart{
				FlowID:      run.FlowID,
				Revision:    run.Revision,
				ScheduledAt: run.ScheduledAt,
				SessionID:   run.SessionID,
				RunID:       run.RunID,
				Status:      run.Status,
			}, nil
		}
	}
	return flow.RunStart{
		FlowID:      claim.FlowID,
		Revision:    claim.Revision,
		ScheduledAt: claim.ScheduledAt,
		RunID:       claim.RunID,
		Status:      pebblestore.FlowRunStatusClaimed,
	}, nil
}

func (s *Server) runAcceptedFlow(ctx context.Context, accepted flow.AcceptedAssignment, request flow.RunRequest) (flow.RunStart, error) {
	if s == nil {
		return flow.RunStart{}, errors.New("api server is not configured")
	}
	if s.sessions == nil {
		return flow.RunStart{}, errors.New("session service not configured")
	}
	if s.runner == nil {
		return flow.RunStart{}, errors.New("run service not configured")
	}
	assignment := accepted.Assignment
	if strings.TrimSpace(assignment.FlowID) == "" {
		assignment.FlowID = strings.TrimSpace(request.FlowID)
	}
	if assignment.Revision == 0 {
		assignment.Revision = request.Revision
	}
	if err := flow.ValidateAssignment(assignment); err != nil {
		return flow.RunStart{}, err
	}
	if request.Revision != 0 && request.Revision != assignment.Revision {
		return flow.RunStart{}, fmt.Errorf("request revision %d does not match accepted revision %d", request.Revision, assignment.Revision)
	}
	scheduledAt := request.ScheduledAt.UTC()
	if scheduledAt.IsZero() {
		scheduledAt = time.Now().UTC()
	}
	runID := strings.TrimSpace(request.RunID)
	if runID == "" {
		runID = flowRunID(assignment.FlowID, assignment.Revision, scheduledAt, request.RunNow)
	}
	sessionID := flowSessionID(runID)
	resolvedAgent, err := s.resolveFlowRunAgent(assignment.Agent)
	if err != nil {
		return flow.RunStart{}, err
	}
	pref, err := s.flowRunSessionPreference(resolvedAgent)
	if err != nil {
		return flow.RunStart{}, err
	}
	metadata := flowRunMetadata(assignment, resolvedAgent, scheduledAt, request.RunNow)
	if descriptor, ok := s.flowHostedSessionDescriptor(assignment); ok {
		metadata = descriptor.WithMetadata(metadata)
	}
	flowRouteDiagLog("target_run_create_session",
		"flow_id", assignment.FlowID,
		"run_id", runID,
		"session_id", sessionID,
		"assignment_target_swarm_id", assignment.Target.SwarmID,
		"assignment_target_kind", assignment.Target.Kind,
		"assignment_target_name", assignment.Target.Name,
		"flow_local_swarm_id", s.flowLocalSwarmID(),
		"metadata_target_swarm_id", flowRouteDiagMetadataValue(metadata, "target_swarm_id"),
		"metadata_swarm_target_swarm_id", flowRouteDiagMetadataValue(metadata, "swarm_target_swarm_id"),
		"metadata_routed_child_swarm_id", flowRouteDiagMetadataValue(metadata, sessionruntime.HostedSessionMetadataChildSwarmID),
		"metadata_routed_host_swarm_id", flowRouteDiagMetadataValue(metadata, sessionruntime.HostedSessionMetadataHostSwarmID),
		"metadata_source", flowRouteDiagMetadataValue(metadata, "source"),
		"metadata_owner_transport", flowRouteDiagMetadataValue(metadata, "owner_transport"),
	)
	runtimeWorkspacePath := firstNonEmpty(strings.TrimSpace(assignment.Workspace.RuntimeWorkspacePath), strings.TrimSpace(assignment.Workspace.WorkspacePath))
	hostWorkspacePath := firstNonEmpty(strings.TrimSpace(assignment.Workspace.HostWorkspacePath), strings.TrimSpace(assignment.Workspace.WorkspacePath))
	sessionReq := sessionCreateRequest{
		Title:                flowRunSessionTitle(assignment),
		WorkspacePath:        runtimeWorkspacePath,
		HostWorkspacePath:    hostWorkspacePath,
		RuntimeWorkspacePath: runtimeWorkspacePath,
		WorkspaceName:        filepath.Base(runtimeWorkspacePath),
		Mode:                 sessionruntime.ModeAuto,
		AgentName:            resolvedAgent.RuntimeTargetName,
		WorktreeMode:         strings.TrimSpace(assignment.Workspace.WorktreeMode),
		Metadata:             metadata,
	}
	sessionReq.Preference.Provider = pref.Provider
	sessionReq.Preference.Model = pref.Model
	sessionReq.Preference.Thinking = pref.Thinking
	sessionReq.Preference.ServiceTier = pref.ServiceTier
	sessionReq.Preference.ContextMode = pref.ContextMode
	if sessionReq.WorkspaceName == "." || sessionReq.WorkspaceName == string(filepath.Separator) {
		sessionReq.WorkspaceName = "workspace"
	}
	if strings.TrimSpace(sessionReq.WorkspacePath) == "" {
		return flow.RunStart{}, errors.New("flow workspace_path is required")
	}
	if strings.TrimSpace(sessionReq.WorkspaceName) == "" || sessionReq.WorkspaceName == "." || sessionReq.WorkspaceName == string(filepath.Separator) {
		sessionReq.WorkspaceName = "workspace"
	}
	session, _, _, _, err := s.createSessionFromRequestWithSessionID(sessionReq, nil, true, sessionID)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return s.existingFlowRunStart(pebblestore.FlowRunClaimRecord{FlowID: assignment.FlowID, Revision: assignment.Revision, ScheduledAt: scheduledAt, RunID: runID})
		}
		return flow.RunStart{}, err
	}
	runReq := runruntime.RunRequest{
		Prompt:     flowRunPrompt(assignment.Intent),
		TargetKind: resolvedAgent.RuntimeTargetKind,
		TargetName: resolvedAgent.RuntimeTargetName,
		Background: true,
		ExecutionContext: &runruntime.RunExecutionContext{
			WorkspacePath: runtimeWorkspacePath,
			CWD:           strings.TrimSpace(assignment.Workspace.CWD),
			WorktreeMode:  strings.TrimSpace(assignment.Workspace.WorktreeMode),
		},
	}
	startedAt := time.Now().UTC()
	start := flow.RunStart{
		FlowID:      assignment.FlowID,
		Revision:    assignment.Revision,
		ScheduledAt: scheduledAt,
		SessionID:   session.ID,
		RunID:       runID,
		Status:      pebblestore.FlowRunStatusRunning,
	}
	if err := s.putFlowRunSummary(start, startedAt, time.Time{}, ""); err != nil {
		return flow.RunStart{}, err
	}
	result, err := s.runner.RunTurnStreaming(ctx, session.ID, runReq, runruntime.RunStartMeta{
		RunID:          runID,
		OwnerTransport: "flow_scheduler",
		AllowSubagent:  resolvedAgent.RuntimeTargetKind == runruntime.RunTargetKindSubagent,
	}, func(event runruntime.StreamEvent) {
		if strings.TrimSpace(event.SessionID) == "" {
			event.SessionID = session.ID
		}
		if strings.TrimSpace(event.RunID) == "" {
			event.RunID = runID
		}
		if event.Metadata == nil {
			event.Metadata = make(map[string]any, 4)
		}
		event.Metadata["flow_id"] = strings.TrimSpace(assignment.FlowID)
		event.Metadata["flow_run_id"] = runID
		event.Metadata["source"] = "flow"
		event.Metadata["owner_transport"] = "flow_scheduler"
	})
	finishedAt := time.Now().UTC()
	if err != nil {
		_ = result
		if summaryErr := s.putFlowRunSummary(start, startedAt, finishedAt, err.Error()); summaryErr != nil {
			return flow.RunStart{}, summaryErr
		}
		return flow.RunStart{}, err
	}
	if s.flowRunFinished(result, session.ID, runID) {
		if summaryErr := s.putFlowRunSummary(start, startedAt, finishedAt, ""); summaryErr != nil {
			return flow.RunStart{}, summaryErr
		}
	}
	return start, nil
}

func (s *Server) putFlowRunSummary(start flow.RunStart, startedAt, finishedAt time.Time, errText string) error {
	if s == nil || s.flows == nil {
		return nil
	}
	status := strings.TrimSpace(start.Status)
	if status == "" {
		status = pebblestore.FlowRunStatusRunning
	}
	if !finishedAt.IsZero() {
		if strings.TrimSpace(errText) != "" {
			status = pebblestore.FlowRunStatusFailed
		} else {
			status = pebblestore.FlowRunStatusSuccess
		}
	}
	startedAt = startedAt.UTC()
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	finishedAt = finishedAt.UTC()
	durationMS := int64(0)
	if !finishedAt.IsZero() {
		durationMS = finishedAt.Sub(startedAt).Milliseconds()
		if durationMS < 0 {
			durationMS = 0
		}
	}
	summary := ""
	if errText != "" {
		summary = strings.TrimSpace(errText)
	}
	targetSwarmID := s.flowLocalSwarmID()
	flowRouteDiagLog("target_put_run_summary",
		"flow_id", start.FlowID,
		"run_id", start.RunID,
		"session_id", start.SessionID,
		"status", status,
		"target_swarm_id", targetSwarmID,
	)
	record, err := s.flows.PutTargetRun(pebblestore.FlowRunSummaryRecord{
		RunID:         strings.TrimSpace(start.RunID),
		FlowID:        strings.TrimSpace(start.FlowID),
		Revision:      start.Revision,
		ScheduledAt:   start.ScheduledAt,
		StartedAt:     startedAt,
		FinishedAt:    finishedAt,
		DurationMS:    durationMS,
		Status:        status,
		Summary:       summary,
		SessionID:     strings.TrimSpace(start.SessionID),
		TargetSwarmID: targetSwarmID,
	})
	if err != nil {
		return err
	}
	s.reportFlowRunSummaryNonFatal(context.Background(), record)
	return nil
}

func (s *Server) flowLocalSwarmID() string {
	if s == nil || s.swarm == nil {
		return ""
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return ""
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(state.Node.SwarmID)
}

func (s *Server) flowRunFinished(result runruntime.RunResult, sessionID, runID string) bool {
	if result.SessionID != "" && !strings.EqualFold(strings.TrimSpace(result.SessionID), strings.TrimSpace(sessionID)) {
		return false
	}
	if s == nil || s.sessions == nil {
		return true
	}
	lifecycle, ok, err := s.sessions.GetLifecycle(sessionID)
	if err != nil || !ok {
		return true
	}
	if runID != "" && !strings.EqualFold(strings.TrimSpace(lifecycle.RunID), strings.TrimSpace(runID)) {
		return false
	}
	return !lifecycle.Active
}

type resolvedFlowRunAgent struct {
	Profile           pebblestore.AgentProfile
	SavedProfileMode  string
	SavedProfileName  string
	RuntimeTargetKind string
	RuntimeTargetName string
}

func (s *Server) flowRunSessionPreference(agent resolvedFlowRunAgent) (pref struct {
	Provider    string
	Model       string
	Thinking    string
	ServiceTier string
	ContextMode string
}, err error) {
	profile := agent.Profile
	pref.Provider = strings.TrimSpace(profile.Provider)
	if pref.Model == "" {
		pref.Model = strings.TrimSpace(profile.Model)
	}
	if pref.Thinking == "" {
		pref.Thinking = strings.TrimSpace(profile.Thinking)
	}
	if pref.Provider == "" || pref.Model == "" || pref.Thinking == "" {
		if s != nil && s.model != nil {
			global, globalErr := s.model.GetGlobalPreference()
			if globalErr != nil {
				return pref, fmt.Errorf("flow agent %q execution preference is not configured on target: %w", agent.RuntimeTargetName, globalErr)
			}
			pref.Provider = firstNonEmpty(pref.Provider, strings.TrimSpace(global.Provider))
			pref.Model = firstNonEmpty(pref.Model, strings.TrimSpace(global.Model))
			pref.Thinking = firstNonEmpty(pref.Thinking, strings.TrimSpace(global.Thinking))
			pref.ServiceTier = strings.TrimSpace(global.ServiceTier)
			pref.ContextMode = strings.TrimSpace(global.ContextMode)
		}
	}
	if pref.Provider == "" || pref.Model == "" || pref.Thinking == "" {
		if runruntime.NormalizeRunTargetKind(agent.RuntimeTargetKind) == runruntime.RunTargetKindAgent {
			return pref, nil
		}
		return pref, fmt.Errorf("flow agent %q execution preference is not configured on target", agent.RuntimeTargetName)
	}
	return pref, nil
}

func (s *Server) flowRunAgentProfile(agent flow.AgentSelection) (pebblestore.AgentProfile, error) {
	resolved, err := s.resolveFlowRunAgent(agent)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	return resolved.Profile, nil
}

func (s *Server) resolveFlowRunAgent(agent flow.AgentSelection) (resolvedFlowRunAgent, error) {
	if s == nil || s.agents == nil {
		return resolvedFlowRunAgent{}, errors.New("agent service not configured")
	}
	agent = flow.NormalizeAgentSelection(agent)
	if strings.TrimSpace(agent.ProfileName) == "" {
		return resolvedFlowRunAgent{}, errors.New("agent profile_name is required")
	}
	if strings.TrimSpace(agent.ProfileMode) == "" {
		return resolvedFlowRunAgent{}, errors.New("agent profile_mode is required")
	}
	profile, err := s.agents.ResolveAgent(agent.ProfileName)
	if err != nil {
		return resolvedFlowRunAgent{}, err
	}
	profileMode := flow.NormalizeAgentProfileMode(profile.Mode)
	if profileMode == "" {
		return resolvedFlowRunAgent{}, fmt.Errorf("saved agent profile %q mode %q does not resolve to a runtime target", profile.Name, strings.TrimSpace(profile.Mode))
	}
	runtimeTargetKind := flow.RuntimeTargetKindForProfileMode(profileMode)
	if runtimeTargetKind == "" {
		return resolvedFlowRunAgent{}, fmt.Errorf("saved agent profile %q mode %q does not resolve to a runtime target", profile.Name, profileMode)
	}
	runtimeTargetName := strings.TrimSpace(profile.Name)
	if runtimeTargetName == "" {
		return resolvedFlowRunAgent{}, errors.New("saved agent profile name is required")
	}
	return resolvedFlowRunAgent{
		Profile:           profile,
		SavedProfileMode:  profileMode,
		SavedProfileName:  runtimeTargetName,
		RuntimeTargetKind: runtimeTargetKind,
		RuntimeTargetName: runtimeTargetName,
	}, nil
}

func flowAgentMetadataKind(agent flow.AgentSelection, resolvedAgent resolvedFlowRunAgent) string {
	if mode := flow.NormalizeAgentProfileMode(strings.TrimSpace(resolvedAgent.SavedProfileMode)); mode != "" {
		return mode
	}
	if mode := flow.NormalizeAgentProfileMode(strings.TrimSpace(resolvedAgent.Profile.Mode)); mode != "" {
		return mode
	}
	if mode := flow.NormalizeAgentProfileMode(strings.TrimSpace(agent.ProfileMode)); mode != "" {
		return mode
	}
	return strings.TrimSpace(agent.ProfileMode)
}

func flowAgentMetadataName(agent flow.AgentSelection, resolvedAgent resolvedFlowRunAgent) string {
	return firstNonEmpty(
		strings.TrimSpace(resolvedAgent.SavedProfileName),
		strings.TrimSpace(resolvedAgent.Profile.Name),
		strings.TrimSpace(agent.ProfileName),
	)
}

func (s *Server) flowHostedSessionDescriptor(assignment flow.Assignment) (sessionruntime.HostedSessionDescriptor, bool) {
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return sessionruntime.HostedSessionDescriptor{}, false
	}
	if flowShouldMirrorRunSummaryLocally(cfg) {
		return sessionruntime.HostedSessionDescriptor{}, false
	}
	localSwarmID := s.flowLocalSwarmID()
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return sessionruntime.HostedSessionDescriptor{}, false
	}
	controllerSwarmID := firstNonEmpty(
		strings.TrimSpace(cfg.ParentSwarmID),
		strings.TrimSpace(cfg.DeployContainer.SyncOwnerSwarmID),
		strings.TrimSpace(cfg.RemoteDeploy.SyncOwnerSwarmID),
		strings.TrimSpace(state.Pairing.ParentSwarmID),
	)
	if localSwarmID == "" || controllerSwarmID == "" || strings.EqualFold(localSwarmID, controllerSwarmID) {
		return sessionruntime.HostedSessionDescriptor{}, false
	}
	hostBackendURL := firstNonEmpty(strings.TrimSpace(cfg.DeployContainer.HostAPIBaseURL), strings.TrimSpace(cfg.RemoteDeploy.HostAPIBaseURL))
	return sessionruntime.HostedSessionDescriptor{
		HostSwarmID:          controllerSwarmID,
		HostBackendURL:       hostBackendURL,
		HostWorkspacePath:    firstNonEmpty(strings.TrimSpace(assignment.Workspace.HostWorkspacePath), strings.TrimSpace(assignment.Workspace.WorkspacePath)),
		RuntimeWorkspacePath: firstNonEmpty(strings.TrimSpace(assignment.Workspace.RuntimeWorkspacePath), strings.TrimSpace(assignment.Workspace.WorkspacePath)),
		ChildSwarmID:         localSwarmID,
	}, true
}

func flowRunPrompt(intent flow.PromptIntent) string {
	parts := make([]string, 0, 1+len(intent.Tasks))
	if prompt := strings.TrimSpace(intent.Prompt); prompt != "" {
		parts = append(parts, prompt)
	}
	for _, task := range intent.Tasks {
		title := strings.TrimSpace(task.Title)
		if title == "" {
			title = strings.TrimSpace(task.ID)
		}
		if title == "" {
			continue
		}
		detail := strings.TrimSpace(task.Detail)
		if detail != "" {
			parts = append(parts, fmt.Sprintf("- [%s] %s: %s", strings.TrimSpace(task.Action), title, detail))
		} else {
			parts = append(parts, fmt.Sprintf("- [%s] %s", strings.TrimSpace(task.Action), title))
		}
	}
	return strings.Join(parts, "\n")
}

func flowRunMetadata(assignment flow.Assignment, resolvedAgent resolvedFlowRunAgent, scheduledAt time.Time, runNow bool) map[string]any {
	targetKind := strings.TrimSpace(assignment.Target.Kind)
	targetName := strings.TrimSpace(assignment.Target.Name)
	metadata := map[string]any{
		"runtime_state":     "standby",
		"title_pending":     false,
		"title_locked":      true,
		"title_source":      flowSessionTitleSourceTask,
		"source":            "flow",
		"lineage_kind":      "flow",
		"flow_id":           strings.TrimSpace(assignment.FlowID),
		"flow_revision":     assignment.Revision,
		"scheduled_at":      scheduledAt.UTC().Format(time.RFC3339Nano),
		"run_now":           runNow,
		"background":        true,
		"target_kind":       targetKind,
		"target_name":       targetName,
		"flow_agent_kind":   flowAgentMetadataKind(assignment.Agent, resolvedAgent),
		"flow_agent_name":   flowAgentMetadataName(assignment.Agent, resolvedAgent),
		"owner_transport":   "flow_scheduler",
		"workspace_context": assignment.Workspace,
	}
	if targetSwarmID := strings.TrimSpace(assignment.Target.SwarmID); targetSwarmID != "" {
		metadata["target_swarm_id"] = targetSwarmID
		metadata["swarm_target_swarm_id"] = targetSwarmID
	}
	if targetName != "" {
		metadata["swarm_target_name"] = targetName
		metadata["target_display_name"] = targetName
	}
	if targetKind != "" {
		metadata["swarm_target_kind"] = targetKind
	}
	if deploymentID := strings.TrimSpace(assignment.Target.DeploymentID); deploymentID != "" {
		metadata["swarm_target_deployment_id"] = deploymentID
	}
	if runtimeWorkspacePath := firstNonEmpty(strings.TrimSpace(assignment.Workspace.RuntimeWorkspacePath), strings.TrimSpace(assignment.Workspace.WorkspacePath)); runtimeWorkspacePath != "" {
		metadata["swarm_target_workspace_path"] = runtimeWorkspacePath
	}
	return metadata
}

func flowRunSessionTitle(assignment flow.Assignment) string {
	if name := flowTitleText(assignment.Name); name != "" {
		return name
	}
	if title := flowTaskSessionTitle(assignment.Intent); title != "" {
		return title
	}
	if prompt := flowTitleText(assignment.Intent.Prompt); prompt != "" {
		return prompt
	}
	if flowID := flowTitleText(assignment.FlowID); flowID != "" {
		return flowID
	}
	return "Flow run"
}

func flowTaskSessionTitle(intent flow.PromptIntent) string {
	for _, task := range intent.Tasks {
		if flowTaskLooksLikeContext(task) {
			continue
		}
		if title := flowTaskTitleCandidate(task); title != "" {
			return title
		}
	}
	for _, task := range intent.Tasks {
		if title := flowTaskTitleCandidate(task); title != "" {
			return title
		}
	}
	return ""
}

func flowTaskTitleCandidate(task flow.TaskStep) string {
	title := flowTitleText(task.Title)
	detail := flowTitleText(task.Detail)
	if detail != "" && flowTaskTitleIsGeneric(title) {
		return detail
	}
	if title != "" {
		return title
	}
	return detail
}

func flowTaskLooksLikeContext(task flow.TaskStep) bool {
	id := strings.ToLower(strings.TrimSpace(task.ID))
	if id == "context" || id == "setup" {
		return true
	}
	switch strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(task.Title)), " ")) {
	case "prepare run context":
		return true
	default:
		return false
	}
}

func flowTaskTitleIsGeneric(title string) bool {
	switch strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(title)), " ")) {
	case "", "run agent task", "run prompt", "task":
		return true
	default:
		return false
	}
}

func flowTitleText(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= flowSessionTitleMaxRunes {
		return value
	}
	return strings.TrimSpace(string(runes[:flowSessionTitleMaxRunes-1])) + "…"
}

func flowRunID(flowID string, revision int64, scheduledAt time.Time, runNow bool) string {
	kind := "scheduled"
	if runNow {
		kind = "run_now"
	}
	return fmt.Sprintf("flow_%s_%s_%d_%d", sanitizeFlowRunIDPart(flowID), kind, revision, scheduledAt.UTC().UnixMilli())
}

func flowRunNowCommandRunID(flowID string, revision int64, scheduledAt time.Time, commandID string) string {
	return fmt.Sprintf("flow_%s_run_now_%d_%d_%s", sanitizeFlowRunIDPart(flowID), revision, scheduledAt.UTC().UnixMilli(), sanitizeFlowRunIDPart(commandID))
}

func flowSessionID(runID string) string {
	return "session_" + sanitizeFlowRunIDPart(runID)
}

func sanitizeFlowRunIDPart(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "unknown"
	}
	return out
}

var _ flow.FlowRunner = targetLocalFlowRunner{}
