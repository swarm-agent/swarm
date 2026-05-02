package api

import (
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/flow"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type flowSessionMirror struct {
	Session              pebblestore.SessionSnapshot
	Route                pebblestore.SessionRouteRecord
	HasRoute             bool
	ReportedMessages     []pebblestore.MessageSnapshot
	ReportedLifecycle    *pebblestore.SessionLifecycleSnapshot
	Target               swarmTarget
	TargetFound          bool
	TargetSwarmID        string
	HostWorkspacePath    string
	RuntimeWorkspacePath string
}

func (s *Server) buildCanonicalFlowSessionMirror(summary pebblestore.FlowRunSummaryRecord, definition pebblestore.FlowDefinitionRecord, reportedSession *pebblestore.SessionSnapshot, reportedMessages []pebblestore.MessageSnapshot) (flowSessionMirror, bool, error) {
	if s == nil {
		return flowSessionMirror{}, false, nil
	}
	sessionID := strings.TrimSpace(summary.SessionID)
	flowID := strings.TrimSpace(summary.FlowID)
	if flowID == "" {
		flowID = strings.TrimSpace(definition.FlowID)
	}
	if sessionID == "" || flowID == "" {
		return flowSessionMirror{}, false, nil
	}
	assignment := definition.Assignment
	if strings.TrimSpace(assignment.FlowID) == "" {
		assignment.FlowID = flowID
	}
	if assignment.Revision == 0 {
		assignment.Revision = firstNonZeroInt64(summary.Revision, definition.Revision)
	}

	target, targetFound := s.resolveFlowMirrorTarget(summary, assignment.Target)
	targetSwarmID := firstNonEmpty(strings.TrimSpace(summary.TargetSwarmID), strings.TrimSpace(target.SwarmID), strings.TrimSpace(assignment.Target.SwarmID))
	flowRouteDiagLog("controller_build_flow_mirror",
		"flow_id", flowID,
		"session_id", sessionID,
		"summary_target_swarm_id", summary.TargetSwarmID,
		"assignment_target_swarm_id", assignment.Target.SwarmID,
		"resolved_target_swarm_id", target.SwarmID,
		"computed_target_swarm_id", targetSwarmID,
		"target_found", targetFound,
		"target_kind", target.Kind,
		"target_name", target.Name,
		"target_backend_url_present", strings.TrimSpace(target.BackendURL) != "",
	)
	workspaceContext := assignment.Workspace
	hostWorkspacePath := firstNonEmpty(strings.TrimSpace(workspaceContext.HostWorkspacePath), strings.TrimSpace(workspaceContext.WorkspacePath))
	runtimeWorkspacePath := firstNonEmpty(strings.TrimSpace(workspaceContext.RuntimeWorkspacePath), strings.TrimSpace(workspaceContext.WorkspacePath))
	if reportedSession != nil && strings.TrimSpace(reportedSession.WorkspacePath) != "" {
		runtimeWorkspacePath = strings.TrimSpace(reportedSession.WorkspacePath)
	}
	if translated := s.resolveControllerFlowWorkspacePath(runtimeWorkspacePath, targetSwarmID, assignment.Target); translated != "" {
		hostWorkspacePath = translated
	}
	if hostWorkspacePath == "" {
		return flowSessionMirror{}, false, nil
	}

	createdAt := summary.StartedAt.UnixMilli()
	if createdAt <= 0 {
		createdAt = summary.ScheduledAt.UnixMilli()
	}
	if reportedSession != nil && reportedSession.CreatedAt > 0 && (createdAt <= 0 || reportedSession.CreatedAt < createdAt) {
		createdAt = reportedSession.CreatedAt
	}
	if createdAt <= 0 {
		createdAt = time.Now().UnixMilli()
	}
	updatedAt := summary.FinishedAt.UnixMilli()
	if reportedSession != nil && reportedSession.UpdatedAt > updatedAt {
		updatedAt = reportedSession.UpdatedAt
	}
	if updatedAt <= 0 {
		updatedAt = createdAt
	}

	metadata := map[string]any(nil)
	if reportedSession != nil {
		metadata = cloneFlowReportMetadata(reportedSession.Metadata)
	}
	metadata = canonicalFlowMirrorMetadata(metadata, assignment, summary, target, targetFound, targetSwarmID, hostWorkspacePath, runtimeWorkspacePath, s.flowLocalSwarmID())

	mirroredSession := pebblestore.SessionSnapshot{
		ID:            sessionID,
		WorkspacePath: hostWorkspacePath,
		WorkspaceName: filepath.Base(hostWorkspacePath),
		Title:         flowRunSessionTitle(assignment),
		Mode:          sessionruntime.ModeAuto,
		Metadata:      metadata,
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
	}
	if reportedSession != nil {
		mirroredSession.Preference = reportedSession.Preference
		mirroredSession.WorktreeEnabled = reportedSession.WorktreeEnabled
		mirroredSession.WorktreeRootPath = strings.TrimSpace(reportedSession.WorktreeRootPath)
		mirroredSession.WorktreeBaseBranch = strings.TrimSpace(reportedSession.WorktreeBaseBranch)
		mirroredSession.WorktreeBranch = strings.TrimSpace(reportedSession.WorktreeBranch)
		mirroredSession.MessageCount = reportedSession.MessageCount
		mirroredSession.LastMessageAt = reportedSession.LastMessageAt
		mirroredSession.TemporaryWorkspaceRoots = append([]string(nil), reportedSession.TemporaryWorkspaceRoots...)
	}

	mirror := flowSessionMirror{
		Session:              mirroredSession,
		ReportedMessages:     canonicalFlowMirrorMessages(sessionID, reportedMessages),
		ReportedLifecycle:    canonicalFlowMirrorLifecycle(sessionID, reportedSession, summary),
		Target:               target,
		TargetFound:          targetFound,
		TargetSwarmID:        targetSwarmID,
		HostWorkspacePath:    hostWorkspacePath,
		RuntimeWorkspacePath: runtimeWorkspacePath,
	}
	if targetFound && strings.TrimSpace(target.BackendURL) != "" && targetSwarmID != "" {
		mirror.HasRoute = true
		mirror.Route = pebblestore.SessionRouteRecord{
			SessionID:            sessionID,
			ChildSwarmID:         targetSwarmID,
			ChildBackendURL:      strings.TrimSpace(target.BackendURL),
			HostWorkspacePath:    hostWorkspacePath,
			RuntimeWorkspacePath: firstNonEmpty(runtimeWorkspacePath, hostWorkspacePath),
			CreatedAt:            createdAt,
			UpdatedAt:            updatedAt,
		}
	}
	return mirror, true, nil
}

func canonicalFlowMirrorMetadata(metadata map[string]any, assignment flow.Assignment, summary pebblestore.FlowRunSummaryRecord, target swarmTarget, targetFound bool, targetSwarmID, hostWorkspacePath, runtimeWorkspacePath, hostSwarmID string) map[string]any {
	if metadata == nil {
		metadata = make(map[string]any, 24)
	}
	metadata["background"] = true
	metadata["flow_id"] = strings.TrimSpace(assignment.FlowID)
	metadata["flow_revision"] = firstNonZeroInt64(summary.Revision, assignment.Revision)
	metadata["lineage_kind"] = "flow"
	metadata["owner_transport"] = "flow_scheduler"
	metadata["source"] = "flow"
	metadata["target_kind"] = firstNonEmpty(strings.TrimSpace(target.Kind), strings.TrimSpace(assignment.Target.Kind))
	metadata["target_name"] = firstNonEmpty(strings.TrimSpace(target.Name), strings.TrimSpace(assignment.Target.Name))
	metadata["flow_agent_kind"] = strings.TrimSpace(assignment.Agent.TargetKind)
	metadata["flow_agent_name"] = strings.TrimSpace(assignment.Agent.TargetName)
	metadata["target_swarm_id"] = strings.TrimSpace(targetSwarmID)
	metadata["swarm_target_swarm_id"] = strings.TrimSpace(targetSwarmID)
	metadata["workspace_context"] = assignment.Workspace
	metadata["runtime_state"] = firstNonEmpty(strings.TrimSpace(flowMetadataString(metadata, "runtime_state")), "standby")
	metadata["title_pending"] = false
	metadata["title_locked"] = true
	metadata["title_source"] = flowSessionTitleSourceTask
	if _, ok := metadata["run_now"]; !ok {
		metadata["run_now"] = strings.Contains(strings.ToLower(strings.TrimSpace(summary.RunID)), "run_now")
	}
	if targetName := firstNonEmpty(strings.TrimSpace(target.Name), strings.TrimSpace(assignment.Target.Name)); targetName != "" {
		metadata["swarm_target_name"] = targetName
		metadata["target_display_name"] = targetName
	}
	if targetKind := firstNonEmpty(strings.TrimSpace(target.Kind), strings.TrimSpace(assignment.Target.Kind)); targetKind != "" {
		metadata["swarm_target_kind"] = targetKind
	}
	if deploymentID := firstNonEmpty(strings.TrimSpace(target.DeploymentID), strings.TrimSpace(assignment.Target.DeploymentID)); deploymentID != "" {
		metadata["swarm_target_deployment_id"] = deploymentID
	}
	if runtimeWorkspacePath != "" {
		metadata["swarm_target_workspace_path"] = runtimeWorkspacePath
	}
	if hostWorkspacePath != "" {
		metadata["host_workspace_path"] = hostWorkspacePath
	}
	if !summary.ScheduledAt.IsZero() {
		metadata["scheduled_at"] = summary.ScheduledAt.UTC().Format(time.RFC3339Nano)
	}
	if runID := strings.TrimSpace(summary.RunID); runID != "" {
		metadata["mirrored_flow_run_id"] = runID
		metadata["flow_run_id"] = runID
	}
	descriptor := sessionruntime.HostedSessionDescriptor{
		HostSwarmID:          strings.TrimSpace(hostSwarmID),
		HostWorkspacePath:    strings.TrimSpace(hostWorkspacePath),
		RuntimeWorkspacePath: strings.TrimSpace(runtimeWorkspacePath),
		ChildSwarmID:         strings.TrimSpace(targetSwarmID),
	}
	metadata = descriptor.WithMetadata(metadata)
	if !targetFound && strings.TrimSpace(targetSwarmID) != "" {
		metadata[sessionruntime.HostedSessionMetadataChildSwarmID] = strings.TrimSpace(targetSwarmID)
	}
	return metadata
}

func canonicalFlowMirrorMessages(sessionID string, messages []pebblestore.MessageSnapshot) []pebblestore.MessageSnapshot {
	sessionID = strings.TrimSpace(sessionID)
	out := make([]pebblestore.MessageSnapshot, 0, len(messages))
	for _, message := range messages {
		if strings.TrimSpace(message.SessionID) == "" {
			message.SessionID = sessionID
		}
		if !strings.EqualFold(strings.TrimSpace(message.SessionID), sessionID) || message.GlobalSeq == 0 {
			continue
		}
		out = append(out, message)
	}
	return out
}

func canonicalFlowMirrorLifecycle(sessionID string, reportedSession *pebblestore.SessionSnapshot, summary pebblestore.FlowRunSummaryRecord) *pebblestore.SessionLifecycleSnapshot {
	if reportedSession != nil && reportedSession.Lifecycle != nil {
		lifecycle := *reportedSession.Lifecycle
		lifecycle.SessionID = strings.TrimSpace(sessionID)
		if strings.TrimSpace(lifecycle.OwnerTransport) == "" {
			lifecycle.OwnerTransport = "flow_scheduler"
		}
		return &lifecycle
	}
	if lifecycle, active := flowRunActiveLifecycleSnapshot(summary); active {
		lifecycle.SessionID = strings.TrimSpace(sessionID)
		return &lifecycle
	}
	return nil
}

func flowMetadataString(metadata map[string]any, key string) string {
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	if typed, ok := value.(string); ok {
		return strings.TrimSpace(typed)
	}
	return ""
}
