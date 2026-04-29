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
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
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
	Summary pebblestore.FlowRunSummaryRecord `json:"summary"`
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
		_, err := s.flows.PutMirroredRunSummary(summary)
		return err
	}
	target, err := s.resolveFlowControllerReportTarget(ctx, cfg)
	if err != nil {
		return err
	}
	if strings.TrimSpace(summary.TargetSwarmID) == "" {
		summary.TargetSwarmID = target.LocalSwarmID
	}
	payload, err := json.Marshal(flowRunReportRequest{Summary: summary})
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
