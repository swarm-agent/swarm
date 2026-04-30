package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/flow"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tailscalehttp"
)

const (
	flowPeerReportPath          = "/v1/swarm/peer/flows/report"
	flowReportTimeout           = 15 * time.Second
	flowReportDeliveryInterval  = 30 * time.Second
	flowReportDeliveryLimit     = 25
	flowReportLocalTransportURL = "http://swarm-local-transport"
)

type flowRunReportRequest struct {
	Summary  pebblestore.FlowRunSummaryRecord `json:"summary"`
	Session  *pebblestore.SessionSnapshot     `json:"session,omitempty"`
	Messages []pebblestore.MessageSnapshot    `json:"messages,omitempty"`
}

type flowRunReportResponse struct {
	OK      bool                             `json:"ok"`
	Summary pebblestore.FlowRunSummaryRecord `json:"summary"`
}

type flowControllerReportTarget struct {
	Endpoint          string
	Client            *http.Client
	LocalTransport    bool
	ControllerSwarmID string
	LocalSwarmID      string
	PeerToken         string
}

func (s *Server) handlePeerFlowReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.flows == nil {
		writeError(w, http.StatusInternalServerError, errors.New("flow store is not configured"))
		return
	}
	var req flowRunReportRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	peerSwarmID, _ := extractPeerAuth(r)
	summary := req.Summary
	if strings.TrimSpace(peerSwarmID) != "" {
		summary.TargetSwarmID = strings.TrimSpace(peerSwarmID)
	}
	summary = flowReportSummaryPayload(summary)
	stored, err := s.flows.PutMirroredRunSummary(summary)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.mirrorFlowRunSessionFromReport(stored, req.Session, req.Messages); err != nil {
		log.Printf("warning: mirror flow run session failed flow_id=%q run_id=%q session_id=%q: %v", strings.TrimSpace(stored.FlowID), strings.TrimSpace(stored.RunID), strings.TrimSpace(stored.SessionID), err)
	}
	writeJSON(w, http.StatusOK, flowRunReportResponse{OK: true, Summary: stored})
}

func (s *Server) StartFlowReportDeliveryLoop(ctx context.Context) {
	if s == nil || s.flows == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.runFlowReportDelivery(ctx)
	ticker := time.NewTicker(flowReportDeliveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runFlowReportDelivery(ctx)
		}
	}
}

func (s *Server) runFlowReportDelivery(ctx context.Context) {
	if err := ctx.Err(); err != nil || s == nil || s.flows == nil {
		return
	}
	now := time.Now().UTC()
	pending, err := s.flows.ListPendingTargetRunReports(now, flowReportDeliveryLimit)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Printf("warning: flow run report delivery failed: %v", err)
		}
		return
	}
	for _, record := range pending {
		if err := ctx.Err(); err != nil {
			return
		}
		reportCtx, cancel := flowReportContext(ctx)
		err := s.reportFlowRunSummary(reportCtx, record)
		cancel()
		if err != nil {
			s.markFlowRunReportFailure(record, err)
			continue
		}
		s.markFlowRunReported(record)
	}
}

func (s *Server) reportFlowRunSummaryNonFatal(ctx context.Context, summary pebblestore.FlowRunSummaryRecord) {
	if s == nil {
		return
	}
	reportCtx, cancel := flowReportContext(ctx)
	err := s.reportFlowRunSummary(reportCtx, summary)
	cancel()
	if err != nil {
		s.markFlowRunReportFailure(summary, err)
		log.Printf("warning: flow run summary report failed flow_id=%q run_id=%q: %v", strings.TrimSpace(summary.FlowID), strings.TrimSpace(summary.RunID), err)
		return
	}
	s.markFlowRunReported(summary)
}

func (s *Server) mirrorFlowRunSessionFromReport(summary pebblestore.FlowRunSummaryRecord, reportedSession *pebblestore.SessionSnapshot, reportedMessages []pebblestore.MessageSnapshot) error {
	if s == nil || s.sessions == nil {
		return nil
	}
	sessionID := strings.TrimSpace(summary.SessionID)
	flowID := strings.TrimSpace(summary.FlowID)
	if sessionID == "" || flowID == "" {
		return nil
	}
	definition, ok, err := s.flows.GetDefinition(flowID)
	if err != nil || !ok {
		return err
	}
	target, targetFound := s.resolveFlowMirrorTarget(summary, definition.Assignment.Target)
	targetSwarmID := firstNonEmpty(strings.TrimSpace(summary.TargetSwarmID), strings.TrimSpace(target.SwarmID), strings.TrimSpace(definition.Assignment.Target.SwarmID))
	runtimeWorkspacePath := strings.TrimSpace(definition.Assignment.Workspace.WorkspacePath)
	if reportedSession != nil && strings.TrimSpace(reportedSession.WorkspacePath) != "" {
		runtimeWorkspacePath = strings.TrimSpace(reportedSession.WorkspacePath)
	}
	workspacePath := strings.TrimSpace(definition.Assignment.Workspace.WorkspacePath)
	if translated := s.resolveControllerFlowWorkspacePath(runtimeWorkspacePath, targetSwarmID, definition.Assignment.Target); translated != "" {
		workspacePath = translated
	}
	if workspacePath == "" {
		return nil
	}
	createdAt := summary.StartedAt.UnixMilli()
	if createdAt <= 0 {
		createdAt = summary.ScheduledAt.UnixMilli()
	}
	if createdAt <= 0 {
		createdAt = time.Now().UnixMilli()
	}
	updatedAt := summary.FinishedAt.UnixMilli()
	if updatedAt <= 0 {
		updatedAt = createdAt
	}
	metadata := map[string]any(nil)
	if reportedSession != nil {
		metadata = cloneFlowReportMetadata(reportedSession.Metadata)
	}
	if metadata == nil {
		metadata = make(map[string]any, 16)
	}
	metadata["background"] = true
	metadata["flow_id"] = flowID
	metadata["flow_revision"] = summary.Revision
	metadata["lineage_kind"] = "flow"
	metadata["owner_transport"] = "flow_scheduler"
	metadata["source"] = "flow"
	metadata["target_kind"] = strings.TrimSpace(definition.Assignment.Agent.TargetKind)
	metadata["target_name"] = strings.TrimSpace(definition.Assignment.Agent.TargetName)
	metadata["flow_agent_kind"] = strings.TrimSpace(definition.Assignment.Agent.TargetKind)
	metadata["flow_agent_name"] = strings.TrimSpace(definition.Assignment.Agent.TargetName)
	metadata["target_swarm_id"] = targetSwarmID
	metadata["swarm_target_swarm_id"] = targetSwarmID
	metadata["workspace_context"] = definition.Assignment.Workspace
	if _, ok := metadata["runtime_state"]; !ok {
		metadata["runtime_state"] = "standby"
	}
	if _, ok := metadata["title_pending"]; !ok {
		metadata["title_pending"] = true
	}
	if _, ok := metadata["run_now"]; !ok {
		metadata["run_now"] = strings.Contains(strings.ToLower(strings.TrimSpace(summary.RunID)), "run_now")
	}
	if targetName := firstNonEmpty(strings.TrimSpace(target.Name), strings.TrimSpace(definition.Assignment.Target.Name)); targetName != "" {
		metadata["swarm_target_name"] = targetName
	}
	if targetKind := firstNonEmpty(strings.TrimSpace(target.Kind), strings.TrimSpace(definition.Assignment.Target.Kind)); targetKind != "" {
		metadata["swarm_target_kind"] = targetKind
	}
	if deploymentID := firstNonEmpty(strings.TrimSpace(target.DeploymentID), strings.TrimSpace(definition.Assignment.Target.DeploymentID)); deploymentID != "" {
		metadata["swarm_target_deployment_id"] = deploymentID
	}
	if runtimeWorkspacePath != "" {
		metadata["swarm_target_workspace_path"] = runtimeWorkspacePath
	}
	if workspacePath != "" {
		metadata["host_workspace_path"] = workspacePath
	}
	if !summary.ScheduledAt.IsZero() {
		metadata["scheduled_at"] = summary.ScheduledAt.UTC().Format(time.RFC3339Nano)
	}
	if runID := strings.TrimSpace(summary.RunID); runID != "" {
		metadata["mirrored_flow_run_id"] = runID
	}
	mirroredSession := pebblestore.SessionSnapshot{
		ID:            sessionID,
		WorkspacePath: workspacePath,
		WorkspaceName: filepath.Base(workspacePath),
		Title:         flowRunSessionTitle(definition.Assignment),
		Mode:          sessionruntime.ModeAuto,
		Metadata:      metadata,
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
	}
	if reportedSession != nil {
		mirroredSession = *reportedSession
		mirroredSession.ID = sessionID
		mirroredSession.WorkspacePath = workspacePath
		mirroredSession.WorkspaceName = firstNonEmpty(strings.TrimSpace(mirroredSession.WorkspaceName), filepath.Base(workspacePath))
		mirroredSession.Title = firstNonEmpty(strings.TrimSpace(mirroredSession.Title), flowRunSessionTitle(definition.Assignment))
		mirroredSession.Mode = firstNonEmpty(strings.TrimSpace(mirroredSession.Mode), sessionruntime.ModeAuto)
		mirroredSession.Metadata = metadata
		if mirroredSession.CreatedAt <= 0 {
			mirroredSession.CreatedAt = createdAt
		}
		if mirroredSession.UpdatedAt <= 0 {
			mirroredSession.UpdatedAt = updatedAt
		}
	}
	storedSession, err := s.sessions.StoreMirroredSession(mirroredSession)
	if err != nil {
		return err
	}
	for _, message := range reportedMessages {
		if strings.TrimSpace(message.SessionID) == "" {
			message.SessionID = sessionID
		}
		if !strings.EqualFold(strings.TrimSpace(message.SessionID), sessionID) || message.GlobalSeq == 0 {
			continue
		}
		if _, err := s.sessions.StoreMirroredMessage(storedSession, message); err != nil {
			return err
		}
	}
	if reportedSession != nil && reportedSession.Lifecycle != nil {
		lifecycle := *reportedSession.Lifecycle
		lifecycle.SessionID = sessionID
		if err := s.sessions.StoreMirroredLifecycle(lifecycle); err != nil {
			return err
		}
	}
	if targetFound && strings.TrimSpace(target.BackendURL) != "" && s.sessionRoutes != nil {
		routeUpdatedAt := storedSession.UpdatedAt
		if routeUpdatedAt <= 0 {
			routeUpdatedAt = updatedAt
		}
		if _, err := s.sessionRoutes.Put(pebblestore.SessionRouteRecord{
			SessionID:            sessionID,
			ChildSwarmID:         targetSwarmID,
			ChildBackendURL:      strings.TrimSpace(target.BackendURL),
			HostWorkspacePath:    workspacePath,
			RuntimeWorkspacePath: firstNonEmpty(runtimeWorkspacePath, workspacePath),
			CreatedAt:            storedSession.CreatedAt,
			UpdatedAt:            routeUpdatedAt,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) flowRunReportSessionPayload(summary pebblestore.FlowRunSummaryRecord) (*pebblestore.SessionSnapshot, []pebblestore.MessageSnapshot, error) {
	if s == nil || s.sessions == nil {
		return nil, nil, nil
	}
	sessionID := strings.TrimSpace(summary.SessionID)
	if sessionID == "" {
		return nil, nil, nil
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, nil
	}
	if session.Lifecycle == nil {
		if lifecycle, active := flowRunActiveLifecycleSnapshot(summary); active {
			session.Lifecycle = &lifecycle
		}
	}
	messages, err := s.sessions.ListMessages(sessionID, 0, 10000)
	if err != nil {
		return nil, nil, err
	}
	return &session, messages, nil
}

func flowRunActiveLifecycleSnapshot(summary pebblestore.FlowRunSummaryRecord) (pebblestore.SessionLifecycleSnapshot, bool) {
	sessionID := strings.TrimSpace(summary.SessionID)
	runID := strings.TrimSpace(summary.RunID)
	if sessionID == "" || runID == "" || !summary.FinishedAt.IsZero() {
		return pebblestore.SessionLifecycleSnapshot{}, false
	}
	status := strings.ToLower(strings.TrimSpace(summary.Status))
	if status == "" {
		status = pebblestore.FlowRunStatusRunning
	}
	if status != pebblestore.FlowRunStatusRunning && status != pebblestore.FlowRunStatusClaimed {
		return pebblestore.SessionLifecycleSnapshot{}, false
	}
	startedAt := summary.StartedAt.UnixMilli()
	if startedAt <= 0 {
		startedAt = summary.ScheduledAt.UnixMilli()
	}
	if startedAt <= 0 {
		startedAt = time.Now().UnixMilli()
	}
	return pebblestore.SessionLifecycleSnapshot{
		SessionID:      sessionID,
		RunID:          runID,
		Active:         true,
		Phase:          pebblestore.FlowRunStatusRunning,
		StartedAt:      startedAt,
		UpdatedAt:      startedAt,
		Generation:     1,
		OwnerTransport: "flow_scheduler",
	}, true
}

func (s *Server) resolveFlowMirrorTarget(summary pebblestore.FlowRunSummaryRecord, selection flow.TargetSelection) (swarmTarget, bool) {
	if s == nil {
		return swarmTarget{}, false
	}
	targetSwarmID := firstNonEmpty(strings.TrimSpace(summary.TargetSwarmID), strings.TrimSpace(selection.SwarmID))
	if targetSwarmID == "" && strings.TrimSpace(selection.DeploymentID) == "" && strings.TrimSpace(selection.Kind) == "" && strings.TrimSpace(selection.Name) == "" {
		return swarmTarget{}, false
	}
	req, err := http.NewRequest(http.MethodGet, "/v1/swarm/targets", nil)
	if err != nil {
		return swarmTarget{}, false
	}
	targets, _, err := s.swarmTargetsForRequest(req)
	if err != nil {
		return swarmTarget{}, false
	}
	selection.SwarmID = targetSwarmID
	selection = normalizeFlowTargetSelection(selection)
	for _, candidate := range targets {
		if flowTargetMatchesSelection(candidate, selection) {
			return candidate, true
		}
	}
	return swarmTarget{}, false
}

func cloneFlowReportMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func (s *Server) resolveControllerFlowWorkspacePath(runtimeWorkspacePath, targetSwarmID string, selection flow.TargetSelection) string {
	if s == nil || s.workspace == nil {
		return ""
	}
	runtimeWorkspacePath = filepath.Clean(strings.TrimSpace(runtimeWorkspacePath))
	if runtimeWorkspacePath == "" || runtimeWorkspacePath == "." || runtimeWorkspacePath == string(filepath.Separator) {
		return ""
	}
	targetSwarmID = firstNonEmpty(strings.TrimSpace(targetSwarmID), strings.TrimSpace(selection.SwarmID))
	targetKind := strings.TrimSpace(selection.Kind)
	deploymentID := strings.TrimSpace(selection.DeploymentID)
	entries, err := s.workspace.ListKnown(100000)
	if err != nil {
		return ""
	}
	bestSource := ""
	bestTarget := ""
	for _, entry := range entries {
		for _, link := range entry.ReplicationLinks {
			linkTargetPath := strings.TrimSpace(link.TargetWorkspacePath)
			if linkTargetPath == "" || !flowReplicationLinkMatchesTarget(link, targetSwarmID, targetKind, deploymentID) {
				continue
			}
			if !flowPathWithinRoot(linkTargetPath, runtimeWorkspacePath) {
				continue
			}
			if len(linkTargetPath) > len(bestTarget) {
				bestSource = strings.TrimSpace(entry.Path)
				bestTarget = linkTargetPath
			}
		}
	}
	if bestSource == "" || bestTarget == "" {
		return ""
	}
	return translateFlowSubpath(bestTarget, bestSource, runtimeWorkspacePath)
}

func (s *Server) reportFlowRunSummary(ctx context.Context, summary pebblestore.FlowRunSummaryRecord) error {
	if s == nil || s.flows == nil {
		return nil
	}
	summary.RunID = strings.TrimSpace(summary.RunID)
	summary.FlowID = strings.TrimSpace(summary.FlowID)
	if summary.RunID == "" || summary.FlowID == "" {
		return errors.New("flow run summary run_id and flow_id are required")
	}
	if strings.TrimSpace(summary.TargetSwarmID) == "" {
		summary.TargetSwarmID = s.flowLocalSwarmID()
	}
	summary = flowReportSummaryPayload(summary)
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return err
	}
	if flowShouldMirrorRunSummaryLocally(cfg) {
		stored, err := s.flows.PutMirroredRunSummary(summary)
		if err != nil {
			return err
		}
		if s.sessions != nil && strings.TrimSpace(stored.SessionID) != "" {
			if _, ok, sessionErr := s.sessions.GetSession(stored.SessionID); sessionErr != nil {
				return sessionErr
			} else if ok {
				return nil
			}
		}
		return s.mirrorFlowRunSessionFromReport(stored, nil, nil)
	}
	target, err := s.resolveFlowControllerReportTarget(ctx, cfg)
	if err != nil {
		return err
	}
	if strings.TrimSpace(summary.TargetSwarmID) == "" {
		summary.TargetSwarmID = target.LocalSwarmID
	}
	reportedSession, reportedMessages, err := s.flowRunReportSessionPayload(summary)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(flowRunReportRequest{Summary: summary, Session: reportedSession, Messages: reportedMessages})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if !target.LocalTransport {
		req.Header.Set(peerAuthSwarmIDHeader, strings.TrimSpace(target.LocalSwarmID))
		req.Header.Set(peerAuthTokenHeader, strings.TrimSpace(target.PeerToken))
	}
	var respPayload flowRunReportResponse
	if err := doFlowReportRequest(target.Client, req, &respPayload); err != nil {
		return err
	}
	return nil
}

func (s *Server) markFlowRunReported(record pebblestore.FlowRunSummaryRecord) {
	if s == nil || s.flows == nil || strings.TrimSpace(record.RunID) == "" {
		return
	}
	current, ok, err := s.flows.GetTargetRun(record.RunID)
	if err != nil || !ok {
		return
	}
	current.ReportedAt = time.Now().UTC()
	current.ReportError = ""
	current.NextReportAt = time.Time{}
	_, _ = s.flows.PutTargetRun(current)
}

func (s *Server) markFlowRunReportFailure(record pebblestore.FlowRunSummaryRecord, reportErr error) {
	if s == nil || s.flows == nil || reportErr == nil || strings.TrimSpace(record.RunID) == "" {
		return
	}
	current, ok, err := s.flows.GetTargetRun(record.RunID)
	if err != nil || !ok {
		return
	}
	current.ReportAttemptCount++
	current.ReportError = strings.TrimSpace(reportErr.Error())
	current.NextReportAt = time.Now().UTC().Add(flowReportRetryDelay(current.ReportAttemptCount))
	_, _ = s.flows.PutTargetRun(current)
}

func (s *Server) resolveFlowControllerReportTarget(ctx context.Context, cfg startupconfig.FileConfig) (flowControllerReportTarget, error) {
	_ = ctx
	if socketPath := strings.TrimSpace(cfg.DeployContainer.LocalTransportSocketPath); socketPath != "" {
		return flowControllerReportTarget{
			Endpoint:       strings.TrimRight(flowReportLocalTransportURL, "/") + flowPeerReportPath,
			Client:         newFlowUnixSocketClient(socketPath),
			LocalTransport: true,
		}, nil
	}
	baseURL := firstNonEmpty(strings.TrimSpace(cfg.DeployContainer.HostAPIBaseURL), strings.TrimSpace(cfg.RemoteDeploy.HostAPIBaseURL))
	if baseURL == "" {
		return flowControllerReportTarget{}, errors.New("flow controller report endpoint is not configured")
	}
	if s.swarm == nil {
		return flowControllerReportTarget{}, errors.New("swarm service not configured")
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return flowControllerReportTarget{}, err
	}
	controllerSwarmID := firstNonEmpty(
		strings.TrimSpace(cfg.ParentSwarmID),
		strings.TrimSpace(cfg.DeployContainer.SyncOwnerSwarmID),
		strings.TrimSpace(cfg.RemoteDeploy.SyncOwnerSwarmID),
		strings.TrimSpace(state.Pairing.ParentSwarmID),
	)
	if controllerSwarmID == "" {
		return flowControllerReportTarget{}, errors.New("flow controller swarm id is not configured")
	}
	peerToken, err := s.outgoingPeerAuthTokenForTarget(nil, swarmTarget{SwarmID: controllerSwarmID})
	if err != nil {
		return flowControllerReportTarget{}, err
	}
	client, err := tailscalehttp.ClientForEndpoint(baseURL, &http.Client{Timeout: flowReportTimeout})
	if err != nil {
		return flowControllerReportTarget{}, err
	}
	return flowControllerReportTarget{
		Endpoint:          strings.TrimRight(strings.TrimSpace(baseURL), "/") + flowPeerReportPath,
		Client:            client,
		ControllerSwarmID: controllerSwarmID,
		LocalSwarmID:      strings.TrimSpace(state.Node.SwarmID),
		PeerToken:         peerToken,
	}, nil
}

func flowReportContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil || parent.Err() != nil {
		return context.WithTimeout(context.Background(), flowReportTimeout)
	}
	return context.WithTimeout(parent, flowReportTimeout)
}

func flowShouldMirrorRunSummaryLocally(cfg startupconfig.FileConfig) bool {
	if cfg.Child {
		return false
	}
	if strings.TrimSpace(cfg.DeployContainer.LocalTransportSocketPath) != "" {
		return false
	}
	if strings.TrimSpace(cfg.DeployContainer.HostAPIBaseURL) != "" || strings.TrimSpace(cfg.RemoteDeploy.HostAPIBaseURL) != "" {
		return false
	}
	return strings.TrimSpace(cfg.ParentSwarmID) == ""
}

func flowReportSummaryPayload(summary pebblestore.FlowRunSummaryRecord) pebblestore.FlowRunSummaryRecord {
	summary.ReportedAt = time.Time{}
	summary.ReportAttemptCount = 0
	summary.NextReportAt = time.Time{}
	summary.ReportError = ""
	return summary
}

func flowReportRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Duration(attempt) * time.Minute
	if delay > 30*time.Minute {
		return 30 * time.Minute
	}
	return delay
}

func newFlowUnixSocketClient(socketPath string) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", strings.TrimSpace(socketPath))
	}
	return &http.Client{Timeout: flowReportTimeout, Transport: transport}
}

func doFlowReportRequest(client *http.Client, req *http.Request, out any) error {
	if client == nil {
		client = &http.Client{Timeout: flowReportTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&failure)
		if strings.TrimSpace(failure.Error) != "" {
			return errors.New(strings.TrimSpace(failure.Error))
		}
		return fmt.Errorf("flow report failed with status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
