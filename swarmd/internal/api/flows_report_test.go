package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/flow"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
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

func TestPeerFlowReportMirrorsSessionIntoControllerWorkspace(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	hostWorkspace := filepath.Join(t.TempDir(), "swarm-go")
	if err := os.MkdirAll(hostWorkspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := server.workspace.Add(hostWorkspace, "swarm-go", "", true); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	if _, err := server.workspace.AddReplicationLink(hostWorkspace, pebblestore.WorkspaceReplicationLink{
		ID:                  "pc-container",
		TargetKind:          "local",
		TargetSwarmID:       "target-swarm-1",
		TargetSwarmName:     "pc container",
		TargetWorkspacePath: "/workspaces/swarm-go",
		ReplicationMode:     workspaceruntime.ReplicationModeBundle,
		Writable:            true,
	}); err != nil {
		t.Fatalf("add replication link: %v", err)
	}
	server.SetDeployContainerService(&fakeFlowDeployService{targets: []swarmTarget{{
		SwarmID:      "target-swarm-1",
		Name:         "pc container",
		Relationship: "child",
		Kind:         "local",
		DeploymentID: "pc-container",
		Online:       true,
		Selectable:   true,
		BackendURL:   "http://child.example",
	}}})
	assignment := testAPIFlowAssignment("flow-report-session", 2)
	assignment.Target = flow.TargetSelection{SwarmID: "target-swarm-1", Kind: "local", DeploymentID: "pc-container", Name: "pc container"}
	assignment.Workspace = flow.WorkspaceContext{WorkspacePath: "/workspaces/swarm-go"}
	if _, err := flows.PutDefinition(pebblestore.FlowDefinitionRecord{FlowID: assignment.FlowID, Revision: assignment.Revision, Assignment: assignment}); err != nil {
		t.Fatalf("put definition: %v", err)
	}

	rec := httptest.NewRecorder()
	reportedSession := pebblestore.SessionSnapshot{
		ID:            "session-report-session",
		WorkspacePath: "/workspaces/swarm-go",
		WorkspaceName: "swarm-go",
		Title:         "target flow title",
		Mode:          "auto",
		Metadata: map[string]any{
			"source":        "flow",
			"target_kind":   "background",
			"target_name":   "memory",
			"runtime_state": "standby",
		},
		CreatedAt:     time.Date(2025, 1, 2, 9, 1, 0, 0, time.UTC).UnixMilli(),
		UpdatedAt:     time.Date(2025, 1, 2, 9, 2, 0, 0, time.UTC).UnixMilli(),
		MessageCount:  2,
		LastMessageAt: time.Date(2025, 1, 2, 9, 1, 30, 0, time.UTC).UnixMilli(),
		Lifecycle: &pebblestore.SessionLifecycleSnapshot{
			SessionID:      "session-report-session",
			RunID:          "run-report-session",
			Active:         false,
			Phase:          "completed",
			StartedAt:      time.Date(2025, 1, 2, 9, 1, 0, 0, time.UTC).UnixMilli(),
			EndedAt:        time.Date(2025, 1, 2, 9, 2, 0, 0, time.UTC).UnixMilli(),
			UpdatedAt:      time.Date(2025, 1, 2, 9, 2, 0, 0, time.UTC).UnixMilli(),
			OwnerTransport: "flow_scheduler",
		},
	}
	reportedMessages := []pebblestore.MessageSnapshot{
		{ID: "msg_00000000000000000001", SessionID: "session-report-session", GlobalSeq: 1, Role: "user", Content: "Summarize outstanding work.", CreatedAt: time.Date(2025, 1, 2, 9, 1, 0, 0, time.UTC).UnixMilli()},
		{ID: "msg_00000000000000000002", SessionID: "session-report-session", GlobalSeq: 2, Role: "assistant", Content: "finished", CreatedAt: time.Date(2025, 1, 2, 9, 1, 30, 0, time.UTC).UnixMilli()},
	}
	req := httptest.NewRequest(http.MethodPost, flowPeerReportPath, jsonReader(t, flowRunReportRequest{
		Summary: pebblestore.FlowRunSummaryRecord{
			RunID:       "run-report-session",
			FlowID:      assignment.FlowID,
			Revision:    assignment.Revision,
			ScheduledAt: time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC),
			StartedAt:   time.Date(2025, 1, 2, 9, 1, 0, 0, time.UTC),
			FinishedAt:  time.Date(2025, 1, 2, 9, 2, 0, 0, time.UTC),
			Status:      pebblestore.FlowRunStatusSuccess,
			SessionID:   "session-report-session",
		},
		Session:  &reportedSession,
		Messages: reportedMessages,
	}))
	req.Header.Set(peerAuthSwarmIDHeader, "target-swarm-1")
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	session, ok, err := server.sessions.GetSession("session-report-session")
	if err != nil || !ok {
		t.Fatalf("get mirrored session ok=%v err=%v", ok, err)
	}
	if session.WorkspacePath != hostWorkspace {
		t.Fatalf("workspace path = %q, want %q", session.WorkspacePath, hostWorkspace)
	}
	if session.Title != "Read" {
		t.Fatalf("session title = %q, want task title", session.Title)
	}
	if session.Metadata["flow_id"] != assignment.FlowID || session.Metadata["target_swarm_id"] != "target-swarm-1" || session.Metadata["swarm_target_name"] != "pc container" {
		t.Fatalf("metadata = %+v", session.Metadata)
	}
	if session.Metadata["title_pending"] != false || session.Metadata["title_locked"] != true || session.Metadata["title_source"] != flowSessionTitleSourceTask {
		t.Fatalf("title metadata = %+v", session.Metadata)
	}
	messages, err := server.sessions.ListMessages("session-report-session", 0, 10)
	if err != nil {
		t.Fatalf("list mirrored messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Content != "Summarize outstanding work." || messages[1].Content != "finished" {
		t.Fatalf("messages = %+v", messages)
	}
	lifecycle, ok, err := server.sessions.GetLifecycle("session-report-session")
	if err != nil || !ok {
		t.Fatalf("get lifecycle ok=%v err=%v", ok, err)
	}
	if lifecycle.Phase != "completed" || lifecycle.OwnerTransport != "flow_scheduler" {
		t.Fatalf("lifecycle = %+v", lifecycle)
	}
	route, ok, err := server.sessionRoutes.Get("session-report-session")
	if err != nil || !ok {
		t.Fatalf("get route ok=%v err=%v", ok, err)
	}
	if route.ChildSwarmID != "target-swarm-1" || route.RuntimeWorkspacePath != "/workspaces/swarm-go" || route.HostWorkspacePath != hostWorkspace {
		t.Fatalf("route = %+v", route)
	}
}

func TestPeerFlowReportMirrorsRunningSessionIntoControllerWorkspace(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	hostWorkspace := filepath.Join(t.TempDir(), "swarm-go")
	if err := os.MkdirAll(hostWorkspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := server.workspace.Add(hostWorkspace, "swarm-go", "", true); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	if _, err := server.workspace.AddReplicationLink(hostWorkspace, pebblestore.WorkspaceReplicationLink{
		ID:                  "pc-container",
		TargetKind:          "local",
		TargetSwarmID:       "target-swarm-1",
		TargetSwarmName:     "pc container",
		TargetWorkspacePath: "/workspaces/swarm-go",
		ReplicationMode:     workspaceruntime.ReplicationModeBundle,
		Writable:            true,
	}); err != nil {
		t.Fatalf("add replication link: %v", err)
	}
	server.SetDeployContainerService(&fakeFlowDeployService{targets: []swarmTarget{{
		SwarmID:      "target-swarm-1",
		Name:         "pc container",
		Relationship: "child",
		Kind:         "local",
		DeploymentID: "pc-container",
		Online:       true,
		Selectable:   true,
		BackendURL:   "http://child.example",
	}}})
	assignment := testAPIFlowAssignment("flow-report-running-session", 2)
	assignment.Target = flow.TargetSelection{SwarmID: "target-swarm-1", Kind: "local", DeploymentID: "pc-container", Name: "pc container"}
	assignment.Workspace = flow.WorkspaceContext{WorkspacePath: "/workspaces/swarm-go"}
	if _, err := flows.PutDefinition(pebblestore.FlowDefinitionRecord{FlowID: assignment.FlowID, Revision: assignment.Revision, Assignment: assignment}); err != nil {
		t.Fatalf("put definition: %v", err)
	}

	startedAt := time.Date(2025, 1, 2, 9, 1, 0, 0, time.UTC)
	reportedSession := pebblestore.SessionSnapshot{
		ID:            "session-running-report",
		WorkspacePath: "/workspaces/swarm-go",
		WorkspaceName: "swarm-go",
		Title:         "running flow title",
		Mode:          "auto",
		Metadata: map[string]any{
			"source":        "flow",
			"target_kind":   "background",
			"target_name":   "memory",
			"runtime_state": "standby",
		},
		CreatedAt: startedAt.UnixMilli(),
		UpdatedAt: startedAt.UnixMilli(),
		Lifecycle: &pebblestore.SessionLifecycleSnapshot{
			SessionID:      "session-running-report",
			RunID:          "run-running-report",
			Active:         true,
			Phase:          "running",
			StartedAt:      startedAt.UnixMilli(),
			UpdatedAt:      startedAt.UnixMilli(),
			OwnerTransport: "flow_scheduler",
		},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, flowPeerReportPath, jsonReader(t, flowRunReportRequest{
		Summary: pebblestore.FlowRunSummaryRecord{
			RunID:       "run-running-report",
			FlowID:      assignment.FlowID,
			Revision:    assignment.Revision,
			ScheduledAt: time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC),
			StartedAt:   startedAt,
			Status:      pebblestore.FlowRunStatusRunning,
			SessionID:   "session-running-report",
		},
		Session: &reportedSession,
	}))
	req.Header.Set(peerAuthSwarmIDHeader, "target-swarm-1")
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	session, ok, err := server.sessions.GetSession("session-running-report")
	if err != nil || !ok {
		t.Fatalf("get mirrored session ok=%v err=%v", ok, err)
	}
	if session.Title != "Read" {
		t.Fatalf("session title = %q, want task title", session.Title)
	}
	if session.WorkspacePath != hostWorkspace || session.Metadata["swarm_target_name"] != "pc container" {
		t.Fatalf("session = %+v", session)
	}
	if session.Metadata["title_pending"] != false || session.Metadata["title_locked"] != true || session.Metadata["title_source"] != flowSessionTitleSourceTask {
		t.Fatalf("title metadata = %+v", session.Metadata)
	}
	lifecycle, ok, err := server.sessions.GetLifecycle("session-running-report")
	if err != nil || !ok {
		t.Fatalf("get lifecycle ok=%v err=%v", ok, err)
	}
	if !lifecycle.Active || lifecycle.Phase != "running" || lifecycle.OwnerTransport != "flow_scheduler" {
		t.Fatalf("lifecycle = %+v", lifecycle)
	}
	route, ok, err := server.sessionRoutes.Get("session-running-report")
	if err != nil || !ok {
		t.Fatalf("get route ok=%v err=%v", ok, err)
	}
	if route.ChildSwarmID != "target-swarm-1" || route.RuntimeWorkspacePath != "/workspaces/swarm-go" || route.HostWorkspacePath != hostWorkspace {
		t.Fatalf("route = %+v", route)
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

func TestFlowRunSummaryReportsRunningSessionToController(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	localSession := pebblestore.SessionSnapshot{
		ID:            "session-report-controller",
		WorkspacePath: filepath.Join(t.TempDir(), "workspace"),
		WorkspaceName: "workspace",
		Title:         "local flow run",
		Mode:          "auto",
		CreatedAt:     time.Date(2025, 1, 2, 9, 1, 0, 0, time.UTC).UnixMilli(),
		UpdatedAt:     time.Date(2025, 1, 2, 9, 2, 0, 0, time.UTC).UnixMilli(),
		MessageCount:  1,
		LastMessageAt: time.Date(2025, 1, 2, 9, 1, 30, 0, time.UTC).UnixMilli(),
	}
	if _, err := server.sessions.StoreMirroredSession(localSession); err != nil {
		t.Fatalf("store local session: %v", err)
	}
	if _, err := server.sessions.StoreMirroredMessage(localSession, pebblestore.MessageSnapshot{ID: "msg_00000000000000000001", SessionID: localSession.ID, GlobalSeq: 1, Role: "assistant", Content: "controller payload", CreatedAt: localSession.LastMessageAt}); err != nil {
		t.Fatalf("store local message: %v", err)
	}
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

	if err := server.putFlowRunSummary(flow.RunStart{
		RunID:       "run-report-controller",
		FlowID:      "flow-report-controller",
		Revision:    4,
		ScheduledAt: time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC),
		SessionID:   localSession.ID,
		Status:      pebblestore.FlowRunStatusRunning,
	}, time.Date(2025, 1, 2, 9, 1, 0, 0, time.UTC), time.Time{}, ""); err != nil {
		t.Fatalf("put/report running summary: %v", err)
	}
	if got.Summary.RunID != "run-report-controller" || got.Summary.TargetSwarmID != "target-swarm" || got.Summary.Status != pebblestore.FlowRunStatusRunning || !got.Summary.FinishedAt.IsZero() {
		t.Fatalf("got report = %+v", got.Summary)
	}
	if got.Session == nil || got.Session.ID != localSession.ID {
		t.Fatalf("got session = %+v", got.Session)
	}
	if got.Session.Lifecycle == nil || !got.Session.Lifecycle.Active || got.Session.Lifecycle.Phase != "running" || got.Session.Lifecycle.OwnerTransport != "flow_scheduler" {
		t.Fatalf("got lifecycle = %+v", got.Session.Lifecycle)
	}
	if len(got.Messages) != 1 || got.Messages[0].Content != "controller payload" {
		t.Fatalf("got messages = %+v", got.Messages)
	}
	stored, ok, err := flows.GetTargetRun("run-report-controller")
	if err != nil || !ok {
		t.Fatalf("get target run ok=%v err=%v", ok, err)
	}
	if stored.ReportedAt.IsZero() || stored.ReportError != "" || stored.ReportAttemptCount != 0 {
		t.Fatalf("stored running summary = %+v", stored)
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
