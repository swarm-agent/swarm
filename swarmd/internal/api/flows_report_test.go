package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/flow"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func TestPeerFlowReportStoresMirroredSummary(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	report := pebblestore.FlowRunSummaryRecord{
		RunID:       "run-report-1",
		FlowID:      "flow-report",
		Revision:    3,
		ScheduledAt: time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC),
		StartedAt:   time.Date(2025, 1, 2, 9, 1, 0, 0, time.UTC),
		FinishedAt:  time.Date(2025, 1, 2, 9, 2, 0, 0, time.UTC),
		Status:      pebblestore.FlowRunStatusSuccess,
		Summary:     "finished",
		SessionID:   "session-report-1",
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, flowPeerReportPath, jsonReader(t, flowRunReportRequest{Summary: report}))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(peerAuthSwarmIDHeader, "target-swarm-1")
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	history, err := flows.ListMirroredRunSummaries("flow-report", 10)
	if err != nil {
		t.Fatalf("list mirrored summaries: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1", len(history))
	}
	stored := history[0]
	if stored.RunID != report.RunID || stored.TargetSwarmID != "target-swarm-1" || stored.Status != pebblestore.FlowRunStatusSuccess {
		t.Fatalf("stored report = %+v", stored)
	}
}

func TestFlowRunSummaryReportingIsNonFatalWhenControllerUnreachable(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	startupPath := writeFlowReportStartupConfig(t, startupconfig.FileConfig{
		Child:         true,
		ParentSwarmID: "controller-swarm",
		DeployContainer: startupconfig.DeployContainerBootstrap{
			HostAPIBaseURL: "http://127.0.0.1:1",
		},
	})
	server.SetStartupConfigPath(startupPath)
	server.SetSwarmService(fakeRoutedSwarmService{
		state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "target-swarm", Name: "target", Role: "child"}, Pairing: swarmruntime.PairingState{ParentSwarmID: "controller-swarm"}},
		token: "peer-token",
	})

	err := server.putFlowRunSummary(flowRunStartForReportTest(), time.Date(2025, 1, 2, 9, 1, 0, 0, time.UTC), time.Date(2025, 1, 2, 9, 2, 0, 0, time.UTC), "")
	if err != nil {
		t.Fatalf("put flow run summary: %v", err)
	}
	stored, ok, err := flows.GetTargetRun("run-report-nonfatal")
	if err != nil || !ok {
		t.Fatalf("target run ok=%v err=%v", ok, err)
	}
	if stored.Status != pebblestore.FlowRunStatusSuccess || stored.TargetSwarmID != "target-swarm" || stored.ReportAttemptCount != 1 || stored.ReportError == "" || stored.NextReportAt.IsZero() {
		t.Fatalf("stored summary = %+v", stored)
	}
}

func TestFlowRunSummaryReportsToController(t *testing.T) {
	server, _ := newFlowPeerTestServer(t)
	var got flowRunReportRequest
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != flowPeerReportPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get(peerAuthSwarmIDHeader) != "target-swarm" || r.Header.Get(peerAuthTokenHeader) != "peer-token" {
			t.Fatalf("peer auth headers = %q/%q", r.Header.Get(peerAuthSwarmIDHeader), r.Header.Get(peerAuthTokenHeader))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode report: %v", err)
		}
		writeJSON(w, http.StatusOK, flowRunReportResponse{OK: true, Summary: got.Summary})
	}))
	defer controller.Close()

	startupPath := writeFlowReportStartupConfig(t, startupconfig.FileConfig{
		Child:         true,
		ParentSwarmID: "controller-swarm",
		DeployContainer: startupconfig.DeployContainerBootstrap{
			HostAPIBaseURL: controller.URL,
		},
	})
	server.SetStartupConfigPath(startupPath)
	server.SetSwarmService(fakeRoutedSwarmService{
		state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "target-swarm", Name: "target", Role: "child"}, Pairing: swarmruntime.PairingState{ParentSwarmID: "controller-swarm"}},
		token: "peer-token",
	})

	if err := server.reportFlowRunSummary(context.Background(), pebblestore.FlowRunSummaryRecord{
		RunID:       "run-report-controller",
		FlowID:      "flow-report-controller",
		Revision:    4,
		ScheduledAt: time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC),
		StartedAt:   time.Date(2025, 1, 2, 9, 1, 0, 0, time.UTC),
		FinishedAt:  time.Date(2025, 1, 2, 9, 2, 0, 0, time.UTC),
		Status:      pebblestore.FlowRunStatusSuccess,
	}); err != nil {
		t.Fatalf("report summary: %v", err)
	}
	if got.Summary.RunID != "run-report-controller" || got.Summary.TargetSwarmID != "target-swarm" {
		t.Fatalf("got report = %+v", got.Summary)
	}
}

func TestFlowRunReportDeliveryRetriesPendingSummaries(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	var attempts int
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			http.Error(w, "temporary failure", http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, flowRunReportResponse{OK: true})
	}))
	defer controller.Close()

	startupPath := writeFlowReportStartupConfig(t, startupconfig.FileConfig{
		Child:         true,
		ParentSwarmID: "controller-swarm",
		DeployContainer: startupconfig.DeployContainerBootstrap{
			HostAPIBaseURL: controller.URL,
		},
	})
	server.SetStartupConfigPath(startupPath)
	server.SetSwarmService(fakeRoutedSwarmService{
		state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "target-swarm", Name: "target", Role: "child"}, Pairing: swarmruntime.PairingState{ParentSwarmID: "controller-swarm"}},
		token: "peer-token",
	})
	if _, err := flows.PutTargetRun(pebblestore.FlowRunSummaryRecord{
		RunID:       "run-retry",
		FlowID:      "flow-retry",
		Revision:    1,
		ScheduledAt: time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC),
		StartedAt:   time.Date(2025, 1, 2, 9, 1, 0, 0, time.UTC),
		FinishedAt:  time.Date(2025, 1, 2, 9, 2, 0, 0, time.UTC),
		Status:      pebblestore.FlowRunStatusSuccess,
	}); err != nil {
		t.Fatalf("put target run: %v", err)
	}

	server.runFlowReportDelivery(context.Background())
	failed, ok, err := flows.GetTargetRun("run-retry")
	if err != nil || !ok {
		t.Fatalf("get failed retry ok=%v err=%v", ok, err)
	}
	if failed.ReportAttemptCount != 1 || failed.ReportError == "" || failed.NextReportAt.IsZero() || !failed.ReportedAt.IsZero() {
		t.Fatalf("failed retry state = %+v", failed)
	}
	failed.NextReportAt = time.Now().Add(-time.Minute)
	if _, err := flows.PutTargetRun(failed); err != nil {
		t.Fatalf("update retry due: %v", err)
	}

	server.runFlowReportDelivery(context.Background())
	reported, ok, err := flows.GetTargetRun("run-retry")
	if err != nil || !ok {
		t.Fatalf("get reported retry ok=%v err=%v", ok, err)
	}
	if reported.ReportedAt.IsZero() || reported.ReportError != "" || !reported.NextReportAt.IsZero() || attempts != 2 {
		t.Fatalf("reported retry state = %+v attempts=%d", reported, attempts)
	}
}

func flowRunStartForReportTest() flow.RunStart {
	return flow.RunStart{
		FlowID:      "flow-report-nonfatal",
		Revision:    2,
		ScheduledAt: time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC),
		SessionID:   "session-report-nonfatal",
		RunID:       "run-report-nonfatal",
		Status:      pebblestore.FlowRunStatusRunning,
	}
}

func jsonReader(t *testing.T, payload any) *bytes.Reader {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return bytes.NewReader(raw)
}

func writeFlowReportStartupConfig(t *testing.T, overrides startupconfig.FileConfig) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(path)
	cfg.SwarmMode = true
	cfg.SwarmName = "flow-report-test"
	cfg.Host = "127.0.0.1"
	cfg.AdvertiseHost = "127.0.0.1"
	cfg.Port = 7781
	cfg.AdvertisePort = 7781
	cfg.Child = overrides.Child
	cfg.ParentSwarmID = overrides.ParentSwarmID
	cfg.DeployContainer = overrides.DeployContainer
	cfg.RemoteDeploy = overrides.RemoteDeploy
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	return path
}
