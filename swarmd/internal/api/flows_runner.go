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
	pref, err := s.flowRunSessionPreference(assignment)
	if err != nil {
		return flow.RunStart{}, err
	}
	sessionReq := sessionCreateRequest{
		Title:                flowRunSessionTitle(assignment),
		WorkspacePath:        strings.TrimSpace(assignment.Workspace.WorkspacePath),
		HostWorkspacePath:    strings.TrimSpace(assignment.Workspace.WorkspacePath),
		RuntimeWorkspacePath: strings.TrimSpace(assignment.Workspace.WorkspacePath),
		WorkspaceName:        filepath.Base(strings.TrimSpace(assignment.Workspace.WorkspacePath)),
		Mode:                 sessionruntime.ModeAuto,
		AgentName:            "",
		WorktreeMode:         strings.TrimSpace(assignment.Workspace.WorktreeMode),
		Metadata:             flowRunMetadata(assignment, scheduledAt, request.RunNow),
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
	session, _, _, _, err := s.createSessionFromRequestWithSessionID(sessionReq, nil, true, sessionID)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return s.existingFlowRunStart(pebblestore.FlowRunClaimRecord{FlowID: assignment.FlowID, Revision: assignment.Revision, ScheduledAt: scheduledAt, RunID: runID})
		}
		return flow.RunStart{}, err
	}
	runReq := runruntime.RunRequest{
		Prompt:     flowRunPrompt(assignment.Intent),
		TargetKind: strings.TrimSpace(assignment.Agent.TargetKind),
		TargetName: strings.TrimSpace(assignment.Agent.TargetName),
		Background: true,
		ExecutionContext: &runruntime.RunExecutionContext{
			WorkspacePath: strings.TrimSpace(assignment.Workspace.WorkspacePath),
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
	}, nil)
	finishedAt := time.Now().UTC()
	if err != nil {
		_ = result
		if summaryErr := s.putFlowRunSummary(start, startedAt, finishedAt, err.Error()); summaryErr != nil {
			return flow.RunStart{}, summaryErr
		}
		return flow.RunStart{}, err
	}
	_ = result
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
		TargetSwarmID: s.flowLocalSwarmID(),
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

func (s *Server) flowRunSessionPreference(assignment flow.Assignment) (pref struct {
	Provider    string
	Model       string
	Thinking    string
	ServiceTier string
	ContextMode string
}, err error) {
	profile, err := s.flowRunAgentProfile(assignment.Agent)
	if err != nil {
		return pref, err
	}
	pref.Provider = strings.TrimSpace(profile.Provider)
	pref.Model = strings.TrimSpace(profile.Model)
	pref.Thinking = strings.TrimSpace(profile.Thinking)
	if pref.Provider == "" || pref.Model == "" || pref.Thinking == "" {
		return pref, fmt.Errorf("flow agent %q execution preference is not configured on target", strings.TrimSpace(assignment.Agent.TargetName))
	}
	return pref, nil
}

func (s *Server) flowRunAgentProfile(agent flow.AgentSelection) (pebblestore.AgentProfile, error) {
	if s == nil || s.agents == nil {
		return pebblestore.AgentProfile{}, errors.New("agent service not configured")
	}
	name := strings.TrimSpace(agent.TargetName)
	switch runruntime.NormalizeRunTargetKind(agent.TargetKind) {
	case runruntime.RunTargetKindAgent:
		return s.agents.ResolvePrimary(name)
	case runruntime.RunTargetKindSubagent:
		return s.agents.ResolveSubagent(name)
	case runruntime.RunTargetKindBackground:
		if strings.EqualFold(name, "memory") {
			return s.agents.ResolveSubagent(name)
		}
		return s.agents.ResolveBackground(name)
	default:
		return pebblestore.AgentProfile{}, fmt.Errorf("unsupported target_kind %q", strings.TrimSpace(agent.TargetKind))
	}
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

func flowRunMetadata(assignment flow.Assignment, scheduledAt time.Time, runNow bool) map[string]any {
	metadata := map[string]any{
		"runtime_state":     "standby",
		"title_pending":     false,
		"source":            "flow",
		"lineage_kind":      "flow",
		"flow_id":           strings.TrimSpace(assignment.FlowID),
		"flow_revision":     assignment.Revision,
		"scheduled_at":      scheduledAt.UTC().Format(time.RFC3339Nano),
		"run_now":           runNow,
		"background":        true,
		"target_kind":       strings.TrimSpace(assignment.Agent.TargetKind),
		"target_name":       strings.TrimSpace(assignment.Agent.TargetName),
		"owner_transport":   "flow_scheduler",
		"workspace_context": assignment.Workspace,
	}
	if targetSwarmID := strings.TrimSpace(assignment.Target.SwarmID); targetSwarmID != "" {
		metadata["target_swarm_id"] = targetSwarmID
	}
	return metadata
}

func flowRunSessionTitle(assignment flow.Assignment) string {
	name := strings.TrimSpace(assignment.Name)
	if name == "" {
		name = strings.TrimSpace(assignment.FlowID)
	}
	if name == "" {
		name = "Flow run"
	}
	return "Flow: " + name
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
