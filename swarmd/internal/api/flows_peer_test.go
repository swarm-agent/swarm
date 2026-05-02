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
	agentruntime "swarm/packages/swarmd/internal/agent"
	deployruntime "swarm/packages/swarmd/internal/deploy"
	"swarm/packages/swarmd/internal/flow"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	modelruntime "swarm/packages/swarmd/internal/model"
	remotedeploy "swarm/packages/swarmd/internal/remotedeploy"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func TestPeerFlowApplyIsIdempotentAndRejectsOutOfOrder(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	command := testAPIFlowCommand("cmd-apply-1", testAPIFlowAssignment("flow-apply", 2), flow.CommandInstall)
	body, err := json.Marshal(command)
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}

	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, flowPeerApplyPath, bytes.NewReader(body))
	firstReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(first, firstReq)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d, body=%s", first.Code, http.StatusOK, first.Body.String())
	}
	var firstPayload flowAssignmentApplyResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstPayload); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if !firstPayload.Inserted || firstPayload.Ack.Status != flow.AssignmentAccepted || firstPayload.Ack.AcceptedRevision != 2 {
		t.Fatalf("first payload = %+v", firstPayload)
	}

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, flowPeerApplyPath, bytes.NewReader(body))
	secondReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(second, secondReq)
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d, body=%s", second.Code, http.StatusOK, second.Body.String())
	}
	var secondPayload flowAssignmentApplyResponse
	if err := json.Unmarshal(second.Body.Bytes(), &secondPayload); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if secondPayload.Inserted || secondPayload.Ack.Status != firstPayload.Ack.Status || secondPayload.Ack.AcceptedRevision != 2 {
		t.Fatalf("second payload = %+v", secondPayload)
	}

	accepted, ok, err := flows.GetAcceptedAssignment("flow-apply")
	if err != nil || !ok {
		t.Fatalf("accepted ok=%v err=%v", ok, err)
	}
	if accepted.Revision != 2 {
		t.Fatalf("accepted revision = %d, want 2", accepted.Revision)
	}
	due, err := flows.ListDue(time.Now().Add(48*time.Hour), 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due) != 1 || due[0].FlowID != "flow-apply" || due[0].Revision != 2 {
		t.Fatalf("due = %+v", due)
	}

	oldCommand := testAPIFlowCommand("cmd-apply-old", testAPIFlowAssignment("flow-apply", 1), flow.CommandUpdate)
	oldBody, err := json.Marshal(oldCommand)
	if err != nil {
		t.Fatalf("marshal old command: %v", err)
	}
	oldRec := httptest.NewRecorder()
	oldReq := httptest.NewRequest(http.MethodPost, flowPeerApplyPath, bytes.NewReader(oldBody))
	oldReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(oldRec, oldReq)
	if oldRec.Code != http.StatusOK {
		t.Fatalf("old status = %d, want %d, body=%s", oldRec.Code, http.StatusOK, oldRec.Body.String())
	}
	var oldPayload flowAssignmentApplyResponse
	if err := json.Unmarshal(oldRec.Body.Bytes(), &oldPayload); err != nil {
		t.Fatalf("decode old response: %v", err)
	}
	if oldPayload.Ack.Status != flow.AssignmentOutOfOrder || oldPayload.Ack.AcceptedRevision != 2 {
		t.Fatalf("old payload = %+v", oldPayload)
	}
}

func TestPeerFlowApplyReturnsConflictForProtocolMismatch(t *testing.T) {
	server, _ := newFlowPeerTestServer(t)
	body := []byte(`{"command_id":"cmd-unknown","flow_id":"flow-unknown","revision":1,"action":"install","created_at":"2026-01-01T00:00:00Z","assignment":{"flow_id":"flow-unknown","revision":1,"name":"Unknown field flow","enabled":true,"target":{"swarm_id":"child-swarm"},"agent":{"target_kind":"background","target_name":"memory"},"workspace":{"workspace_path":"/workspace","host_workspace_path":"/host/workspace"},"schedule":{"cadence":"on_demand","timezone":"UTC"},"intent":{"prompt":"hello"}}}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, flowPeerApplyPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestPeerFlowApplyAcksLocalTargetSwarmInsteadOfSender(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	server.SetSwarmService(fakeRoutedSwarmService{
		state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "child-swarm", Name: "child", Role: "child"}},
		token: "peer-token",
	})
	command := testAPIFlowCommand("cmd-apply-child", testAPIFlowAssignment("flow-apply-child", 1), flow.CommandInstall)
	command.Assignment.Target = flow.TargetSelection{SwarmID: "child-swarm", Kind: "remote", Name: "pc child", DeploymentID: "pc-child"}
	body, err := json.Marshal(command)
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, flowPeerApplyPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(peerAuthSwarmIDHeader, "host-swarm")
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload flowAssignmentApplyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Ack.Status != flow.AssignmentAccepted {
		t.Fatalf("ack status = %q, want accepted: %+v", payload.Ack.Status, payload.Ack)
	}
	if payload.Ack.TargetSwarmID != "child-swarm" {
		t.Fatalf("ack target swarm = %q, want child-swarm", payload.Ack.TargetSwarmID)
	}
	accepted, ok, err := flows.GetAcceptedAssignment("flow-apply-child")
	if err != nil || !ok {
		t.Fatalf("accepted ok=%v err=%v", ok, err)
	}
	if accepted.Target.SwarmID != "child-swarm" {
		t.Fatalf("accepted target swarm = %q, want child-swarm", accepted.Target.SwarmID)
	}
}

func TestFlowAssignmentDeliveryKeepsUnreachableTargetsPending(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	server.SetDeployContainerService(&fakeFlowDeployService{targets: []swarmTarget{{
		SwarmID:      "child-offline",
		Name:         "offline child",
		Relationship: "child",
		Kind:         "local",
		Online:       false,
		Selectable:   false,
		LastError:    "child is stopped",
	}}})
	command := testAPIFlowCommand("cmd-offline", testAPIFlowAssignment("flow-offline", 1), flow.CommandInstall)
	command.Assignment.Target = flow.TargetSelection{SwarmID: "child-offline", Kind: "local", Name: "offline child"}

	result, err := server.EnqueueAndDeliverFlowAssignmentCommand(t.Context(), command)
	if err != nil {
		t.Fatalf("enqueue deliver: %v", err)
	}
	if !result.PendingSync || result.Delivered || result.AssignmentState.Status != flow.AssignmentTargetOffline {
		t.Fatalf("result = %+v", result)
	}
	if result.Outbox.Status != pebblestore.FlowOutboxStatusPending {
		t.Fatalf("outbox status = %q", result.Outbox.Status)
	}
	pending, err := flows.ListOutboxCommands(pebblestore.FlowOutboxStatusPending, 10)
	if err != nil {
		t.Fatalf("list pending outbox: %v", err)
	}
	if len(pending) != 1 || pending[0].CommandID != "cmd-offline" {
		t.Fatalf("pending outbox = %+v", pending)
	}
	stored, ok, err := flows.GetAssignmentStatus("flow-offline", "child-offline")
	if err != nil || !ok {
		t.Fatalf("assignment status ok=%v err=%v", ok, err)
	}
	if !stored.PendingSync || stored.Status != flow.AssignmentTargetOffline {
		t.Fatalf("stored status = %+v", stored)
	}
}

func TestFlowAssignmentDeliveryUsesResolvedRemoteTargetSwarm(t *testing.T) {
	server, _ := newFlowPeerTestServer(t)
	var delivered flow.AssignmentCommand
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != flowPeerApplyPath {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&delivered); err != nil {
			t.Fatalf("decode child command: %v", err)
		}
		writeJSON(w, http.StatusOK, flowAssignmentApplyResponse{OK: true, Ack: flow.AssignmentAck{
			CommandID:        delivered.CommandID,
			FlowID:           delivered.FlowID,
			AcceptedRevision: delivered.Revision,
			Status:           flow.AssignmentAccepted,
			TargetSwarmID:    "child-remote",
			TargetClock:      time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC),
		}})
	}))
	defer child.Close()
	server.SetRemoteDeployService(&fakeRemoteDeployService{sessions: []remotedeploy.Session{{
		ID:               "pc-child-remote",
		Name:             "pc child",
		Status:           "attached",
		ChildSwarmID:     "child-remote",
		RemoteTailnetURL: child.URL,
		RemoteEndpoint:   child.URL,
	}}})
	assignment := testAPIFlowAssignment("flow-remote-target", 1)
	assignment.Target = flow.TargetSelection{SwarmID: "child-remote", Kind: "remote", Name: "pc child", DeploymentID: "pc-child-remote"}
	command := testAPIFlowCommand("cmd-remote-target", assignment, flow.CommandInstall)

	result, err := server.EnqueueAndDeliverFlowAssignmentCommand(t.Context(), command)
	if err != nil {
		t.Fatalf("enqueue deliver: %v", err)
	}
	if !result.Delivered || result.Ack.Status != flow.AssignmentAccepted {
		t.Fatalf("result = %+v", result)
	}
	if result.AssignmentState.TargetSwarmID != "child-remote" || result.Outbox.TargetSwarmID != "child-remote" {
		t.Fatalf("target attribution outbox=%q state=%q", result.Outbox.TargetSwarmID, result.AssignmentState.TargetSwarmID)
	}
	if delivered.Assignment.Target.SwarmID != "child-remote" || delivered.Assignment.Target.Kind != "remote" || delivered.Assignment.Target.DeploymentID != "pc-child-remote" {
		t.Fatalf("delivered target = %+v", delivered.Assignment.Target)
	}
}

func TestFlowAssignmentDeliveryTranslatesReplicatedWorkspacePath(t *testing.T) {
	server, _ := newFlowPeerTestServer(t)
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
		TargetSwarmID:       "child-container",
		TargetSwarmName:     "pc container",
		TargetWorkspacePath: "/root/swarm-go",
		ReplicationMode:     workspaceruntime.ReplicationModeBundle,
		Writable:            true,
	}); err != nil {
		t.Fatalf("add replication link: %v", err)
	}

	var delivered flow.AssignmentCommand
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != flowPeerApplyPath {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&delivered); err != nil {
			t.Fatalf("decode child command: %v", err)
		}
		writeJSON(w, http.StatusOK, flowAssignmentApplyResponse{OK: true, Ack: flow.AssignmentAck{
			CommandID:        delivered.CommandID,
			FlowID:           delivered.FlowID,
			AcceptedRevision: delivered.Revision,
			Status:           flow.AssignmentAccepted,
			TargetSwarmID:    "child-container",
			TargetClock:      time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC),
		}})
	}))
	defer child.Close()
	server.SetDeployContainerService(&fakeFlowDeployService{targets: []swarmTarget{{
		SwarmID:      "child-container",
		Name:         "pc container",
		Relationship: "child",
		Kind:         "local",
		DeploymentID: "pc-container",
		Online:       true,
		Selectable:   true,
		BackendURL:   child.URL,
	}}})
	assignment := testAPIFlowAssignment("flow-replicated-workspace", 1)
	assignment.Target = flow.TargetSelection{SwarmID: "child-container", Kind: "local", Name: "pc container", DeploymentID: "pc-container"}
	assignment.Workspace = flow.WorkspaceContext{WorkspacePath: filepath.Join(hostWorkspace, "subdir"), CWD: filepath.Join(hostWorkspace, "subdir", "nested")}
	command := testAPIFlowCommand("cmd-replicated-workspace", assignment, flow.CommandInstall)

	result, err := server.EnqueueAndDeliverFlowAssignmentCommand(t.Context(), command)
	if err != nil {
		t.Fatalf("enqueue deliver: %v", err)
	}
	if !result.Delivered || result.Ack.Status != flow.AssignmentAccepted {
		t.Fatalf("result = %+v", result)
	}
	if delivered.Assignment.Workspace.WorkspacePath != "/root/swarm-go/subdir" {
		t.Fatalf("delivered workspace path = %q", delivered.Assignment.Workspace.WorkspacePath)
	}
	if delivered.Assignment.Workspace.HostWorkspacePath != filepath.Join(hostWorkspace, "subdir") {
		t.Fatalf("delivered host workspace path = %q", delivered.Assignment.Workspace.HostWorkspacePath)
	}
	if delivered.Assignment.Workspace.RuntimeWorkspacePath != "/root/swarm-go/subdir" {
		t.Fatalf("delivered runtime workspace path = %q", delivered.Assignment.Workspace.RuntimeWorkspacePath)
	}
	if delivered.Assignment.Workspace.CWD != "/root/swarm-go/subdir/nested" {
		t.Fatalf("delivered cwd = %q", delivered.Assignment.Workspace.CWD)
	}
}

func TestFlowAssignmentDeliveryStoresRejection(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != flowPeerApplyPath {
			http.NotFound(w, r)
			return
		}
		var command flow.AssignmentCommand
		if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
			t.Fatalf("decode child command: %v", err)
		}
		writeJSON(w, http.StatusOK, flowAssignmentApplyResponse{OK: true, Ack: flow.AssignmentAck{
			CommandID:        command.CommandID,
			FlowID:           command.FlowID,
			AcceptedRevision: 0,
			Status:           flow.AssignmentRejected,
			Reason:           "missing agent profile",
			TargetSwarmID:    "child-reject",
			TargetClock:      time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC),
		}})
	}))
	defer child.Close()
	server.SetDeployContainerService(&fakeFlowDeployService{targets: []swarmTarget{{
		SwarmID:      "child-reject",
		Name:         "reject child",
		Relationship: "child",
		Kind:         "local",
		Online:       true,
		Selectable:   true,
		BackendURL:   child.URL,
	}}})
	command := testAPIFlowCommand("cmd-reject", testAPIFlowAssignment("flow-reject", 1), flow.CommandInstall)
	command.Assignment.Target = flow.TargetSelection{SwarmID: "child-reject", Kind: "local", Name: "reject child"}

	result, err := server.EnqueueAndDeliverFlowAssignmentCommand(t.Context(), command)
	if err != nil {
		t.Fatalf("enqueue deliver: %v", err)
	}
	if result.PendingSync || result.Delivered || result.Ack.Status != flow.AssignmentRejected {
		t.Fatalf("result = %+v", result)
	}
	if result.Outbox.Status != pebblestore.FlowOutboxStatusRejected {
		t.Fatalf("outbox status = %q", result.Outbox.Status)
	}
	stored, ok, err := flows.GetAssignmentStatus("flow-reject", "child-reject")
	if err != nil || !ok {
		t.Fatalf("assignment status ok=%v err=%v", ok, err)
	}
	if stored.PendingSync || stored.Status != flow.AssignmentRejected || stored.Reason != "missing agent profile" {
		t.Fatalf("stored status = %+v", stored)
	}
}

func newFlowPeerTestServer(t *testing.T) (*Server, *pebblestore.FlowStore) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "flows-peer-api.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure agent defaults: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	sessionSvc.SetLocalSwarmIDResolver(func() string { return "host-swarm-id" })
	modelStore := pebblestore.NewModelStore(store)
	if _, err := modelStore.SetGlobalPreference("test-provider", "test-model", "medium"); err != nil {
		t.Fatalf("set model preference: %v", err)
	}
	modelSvc := modelruntime.NewService(modelStore, eventLog, nil)
	workspaceSvc := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(store))
	server := NewServer("test", nil, agentSvc, modelSvc, nil, sessionSvc, workspaceSvc, nil, nil, nil, nil, nil, eventLog, stream.NewHub(eventLog))
	flows := pebblestore.NewFlowStore(store)
	server.SetFlowStore(flows)
	server.SetSessionRouteStore(pebblestore.NewSessionRouteStore(store))
	server.SetSwarmService(fakeRoutedSwarmService{
		state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "host-swarm-id", Name: "host-swarm", Role: "master"}},
		token: "peer-token",
	})
	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.SwarmMode = true
	cfg.SwarmName = "host-swarm"
	cfg.Host = "127.0.0.1"
	cfg.AdvertiseHost = "127.0.0.1"
	cfg.Port = 7781
	cfg.AdvertisePort = 7781
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	server.SetStartupConfigPath(startupPath)
	return server, flows
}

func testAPIFlowAssignment(flowID string, revision int64) flow.Assignment {
	return flow.Assignment{
		FlowID:   flowID,
		Revision: revision,
		Name:     "Memory sweep",
		Enabled:  true,
		Target:   flow.TargetSelection{SwarmID: "host-swarm-id", Kind: "self", Name: "host-swarm"},
		Agent:    flow.AgentSelection{TargetKind: "background", TargetName: "memory"},
		Workspace: flow.WorkspaceContext{
			WorkspacePath: filepath.Join("workspace", "project"),
		},
		Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceDaily, Time: "09:00", Timezone: "UTC"},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent:        flow.PromptIntent{Prompt: "Summarize outstanding work.", Tasks: []flow.TaskStep{{ID: "read", Title: "Read", Action: "read"}}},
	}
}

func testAPIFlowCommand(commandID string, assignment flow.Assignment, action flow.CommandAction) flow.AssignmentCommand {
	return flow.AssignmentCommand{
		CommandID:  commandID,
		FlowID:     assignment.FlowID,
		Revision:   assignment.Revision,
		Action:     action,
		CreatedAt:  time.Date(2025, 1, 2, 8, 0, 0, 0, time.UTC),
		Assignment: assignment,
	}
}

type fakeFlowDeployService struct {
	targets []swarmTarget
}

func (f *fakeFlowDeployService) RuntimeStatus(context.Context) (deployruntime.ContainerRuntimeStatus, error) {
	return deployruntime.ContainerRuntimeStatus{}, nil
}

func (f *fakeFlowDeployService) List(context.Context) ([]deployruntime.ContainerDeployment, error) {
	out := make([]deployruntime.ContainerDeployment, 0, len(f.targets))
	for _, target := range f.targets {
		attachStatus := "attached"
		if !target.Online || !target.Selectable {
			attachStatus = "offline"
		}
		out = append(out, deployruntime.ContainerDeployment{
			ID:               target.DeploymentID,
			Name:             target.Name,
			AttachStatus:     attachStatus,
			ChildSwarmID:     target.SwarmID,
			ChildDisplayName: target.Name,
			ChildBackendURL:  target.BackendURL,
			ChildDesktopURL:  target.DesktopURL,
			LastAttachError:  target.LastError,
		})
	}
	return out, nil
}

func (f *fakeFlowDeployService) Create(context.Context, deployruntime.ContainerCreateInput) (deployruntime.ContainerDeployment, error) {
	return deployruntime.ContainerDeployment{}, nil
}

func (f *fakeFlowDeployService) Act(context.Context, deployruntime.ContainerActionInput) (deployruntime.ContainerDeployment, error) {
	return deployruntime.ContainerDeployment{}, nil
}

func (f *fakeFlowDeployService) Delete(context.Context, []string) (localcontainers.DeleteResult, error) {
	return localcontainers.DeleteResult{}, nil
}

func (f *fakeFlowDeployService) ChildAttachState(context.Context, deployruntime.ContainerAttachStatusInput) (swarmruntime.LocalState, error) {
	return swarmruntime.LocalState{}, nil
}

func (f *fakeFlowDeployService) AttachRequest(context.Context, deployruntime.ContainerAttachRequestInput) (deployruntime.ContainerAttachState, error) {
	return deployruntime.ContainerAttachState{}, nil
}

func (f *fakeFlowDeployService) AttachStatus(context.Context, deployruntime.ContainerAttachStatusInput) (deployruntime.ContainerAttachState, error) {
	return deployruntime.ContainerAttachState{}, nil
}

func (f *fakeFlowDeployService) AttachApprove(context.Context, deployruntime.ContainerAttachApproveInput) (deployruntime.ContainerAttachState, error) {
	return deployruntime.ContainerAttachState{}, nil
}

func (f *fakeFlowDeployService) FinalizeAttachFromHost(context.Context, deployruntime.ContainerAttachFinalizeInput) error {
	return nil
}

func (f *fakeFlowDeployService) SyncCredentialBundle(context.Context, deployruntime.ContainerSyncCredentialRequestInput) (deployruntime.ContainerSyncCredentialBundle, error) {
	return deployruntime.ContainerSyncCredentialBundle{}, nil
}

func (f *fakeFlowDeployService) SyncAgentBundle(context.Context, deployruntime.ContainerSyncCredentialRequestInput) (deployruntime.ContainerSyncAgentBundle, error) {
	return deployruntime.ContainerSyncAgentBundle{}, nil
}

func (f *fakeFlowDeployService) WorkspaceBootstrap(context.Context, deployruntime.ContainerWorkspaceBootstrapRequestInput) ([]deployruntime.ContainerWorkspaceBootstrap, error) {
	return nil, nil
}

func (f *fakeFlowDeployService) AutoAttachChild(context.Context) error {
	return nil
}

func (f *fakeFlowDeployService) UnlockManagedLocalChildVaults(context.Context) error {
	return nil
}

var _ deployContainerService = (*fakeFlowDeployService)(nil)
