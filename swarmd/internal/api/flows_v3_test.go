package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/flow"
	remotedeploy "swarm/packages/swarmd/internal/remotedeploy"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func TestFlowsV3CreateListGetUpdateRunNowDeleteHistoryAndStatus(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	ensureFlowTestAgent(t, server)
	ensureFlowPrimaryAgentRunnable(t, server)
	runner := &fakeFlowRunService{}
	server.runner = runner
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != flowPeerApplyPath {
			http.NotFound(w, r)
			return
		}
		var command flow.AssignmentCommand
		if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
			t.Fatalf("decode child command: %v", err)
		}
		ack, inserted, err := server.applyFlowAssignmentCommandLocally(r.Context(), command, "child-remote")
		if err != nil {
			t.Fatalf("apply child command: %v", err)
		}
		writeJSON(w, http.StatusOK, flowAssignmentApplyResponse{OK: true, Ack: ack, Inserted: inserted})
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
	if _, err := server.swarmNodes.Put(pebblestore.SwarmNodeRecord{SwarmID: "child-remote", Name: "pc child", Role: swarmruntime.RelationshipChild, Kind: "remote", DeploymentID: "pc-child-remote", BackendURL: child.URL, Status: "online"}); err != nil {
		t.Fatalf("seed remote swarm node: %v", err)
	}
	workspace := t.TempDir()
	seedFlowTopologyWorkspaceBinding(t, server, workspace, filepath.Base(workspace), "pc-child-remote", "remote", "child-remote", "/workspaces/remote")
	req := flowV3UpsertRequest{
		FlowID:  "flow-v3-remote",
		Name:    "Remote V3 flow",
		Enabled: boolPtr(true),
		Target:  flow.TargetSelection{SwarmID: "child-remote", Kind: "remote", DeploymentID: "pc-child-remote", Name: "pc child"},
		Agent:   flow.AgentSelection{ProfileName: "flow-test", ProfileMode: "subagent"},
		Workspace: flow.WorkspaceContext{
			WorkspacePath: workspace,
		},
		Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceOnDemand},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent:        flow.PromptIntent{Prompt: "Refresh memory remotely."},
	}
	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/v3/flows", jsonReader(t, req))
	createReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(createRec, requestWithTestPrincipal(createReq))
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createRec.Code, createRec.Body.String())
	}
	var createPayload flowV3MutationResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &createPayload); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if createPayload.Flow.Definition.FlowID != "flow-v3-remote" || createPayload.Flow.Definition.Agent.ProfileName != "flow-test" || createPayload.Flow.Definition.Agent.ProfileMode != "subagent" {
		t.Fatalf("definition = %+v", createPayload.Flow.Definition)
	}
	if createPayload.Result == nil || !createPayload.Result.Delivered {
		t.Fatalf("create result = %+v", createPayload.Result)
	}
	if createPayload.Run != nil {
		t.Fatalf("manual on-demand create unexpectedly started run: %+v", createPayload.Run)
	}
	if createPayload.Flow.TargetDetail == nil || createPayload.Flow.TargetDetail.SwarmID != "child-remote" || createPayload.Flow.TargetDetail.Kind != "remote" {
		t.Fatalf("target detail = %+v", createPayload.Flow.TargetDetail)
	}
	if createPayload.Flow.AgentDetail == nil || createPayload.Flow.AgentDetail.Name != "flow-test" || createPayload.Flow.AgentDetail.Mode != agentruntime.ModeSubagent {
		t.Fatalf("agent detail = %+v", createPayload.Flow.AgentDetail)
	}
	definition, ok, err := flows.GetDefinitionForAccount(testAccountScopeID, "flow-v3-remote")
	if err != nil || !ok {
		t.Fatalf("get definition ok=%v err=%v", ok, err)
	}
	if definition.Assignment.Agent.ProfileName != "flow-test" || definition.Assignment.Agent.ProfileMode != "subagent" {
		t.Fatalf("stored assignment agent = %+v", definition.Assignment.Agent)
	}
	listRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(listRec, requestWithTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/flows?limit=200", nil)))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listRec.Code, listRec.Body.String())
	}
	var listPayload flowV3ListResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listPayload.Flows) != 1 || listPayload.Flows[0].Definition.FlowID != "flow-v3-remote" {
		t.Fatalf("list payload = %+v", listPayload)
	}
	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, requestWithTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/flows/flow-v3-remote", nil)))
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", getRec.Code, getRec.Body.String())
	}
	var getPayload flowV3RecordResponse
	if err := json.Unmarshal(getRec.Body.Bytes(), &getPayload); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if getPayload.TargetDetail == nil || getPayload.AgentDetail == nil {
		t.Fatalf("get payload = %+v", getPayload)
	}
	updateReq := flowV3UpsertRequest{
		Name:    "Remote V3 flow updated",
		Enabled: boolPtr(false),
		Target:  flow.TargetSelection{SwarmID: "child-remote", Kind: "remote", DeploymentID: "pc-child-remote", Name: "pc child"},
		Agent:   flow.AgentSelection{ProfileName: "swarm", ProfileMode: "primary"},
		Workspace: flow.WorkspaceContext{
			WorkspacePath: filepath.Join(workspace, "updated"),
		},
		Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceOnDemand},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent:        flow.PromptIntent{Prompt: "Use swarm primary."},
	}
	updateRec := httptest.NewRecorder()
	updateHTTP := httptest.NewRequest(http.MethodPut, "/v3/flows/flow-v3-remote", jsonReader(t, updateReq))
	updateHTTP.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(updateRec, requestWithTestPrincipal(updateHTTP))
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", updateRec.Code, updateRec.Body.String())
	}
	var updatePayload flowV3MutationResponse
	if err := json.Unmarshal(updateRec.Body.Bytes(), &updatePayload); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if updatePayload.Flow.Definition.Name != "Remote V3 flow updated" || updatePayload.Flow.Definition.Enabled {
		t.Fatalf("updated definition = %+v", updatePayload.Flow.Definition)
	}
	if updatePayload.Flow.Definition.Agent.ProfileName != "swarm" || updatePayload.Flow.Definition.Agent.ProfileMode != "primary" {
		t.Fatalf("updated agent = %+v", updatePayload.Flow.Definition.Agent)
	}
	updatedDefinition, ok, err := flows.GetDefinitionForAccount(testAccountScopeID, "flow-v3-remote")
	if err != nil || !ok {
		t.Fatalf("get updated definition ok=%v err=%v", ok, err)
	}
	if updatedDefinition.Revision != 2 {
		t.Fatalf("updated revision = %d", updatedDefinition.Revision)
	}
	if updatedDefinition.Assignment.Agent.ProfileName != "swarm" || updatedDefinition.Assignment.Agent.ProfileMode != "primary" {
		t.Fatalf("updated stored agent = %+v", updatedDefinition.Assignment.Agent)
	}
	unassignReq := flowV3UpsertRequest{UnassignTarget: true}
	unassignRec := httptest.NewRecorder()
	unassignHTTP := httptest.NewRequest(http.MethodPut, "/v3/flows/flow-v3-remote", jsonReader(t, unassignReq))
	unassignHTTP.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(unassignRec, requestWithTestPrincipal(unassignHTTP))
	if unassignRec.Code != http.StatusOK {
		t.Fatalf("unassign status = %d body=%s", unassignRec.Code, unassignRec.Body.String())
	}
	var unassignPayload flowV3MutationResponse
	if err := json.Unmarshal(unassignRec.Body.Bytes(), &unassignPayload); err != nil {
		t.Fatalf("decode unassign: %v", err)
	}
	if unassignPayload.Flow.Definition.Enabled || flowV3HasTargetSelection(unassignPayload.Flow.Definition.Target) {
		t.Fatalf("unassigned definition = %+v", unassignPayload.Flow.Definition)
	}
	if unassignPayload.Result == nil || unassignPayload.Result.Outbox.TargetSwarmID != "child-remote" || unassignPayload.Result.Outbox.Command.Assignment.Target.SwarmID != "child-remote" {
		t.Fatalf("unassign delivery result = %+v", unassignPayload.Result)
	}
	if storedUnassign, ok, err := flows.GetDefinitionForAccount(testAccountScopeID, "flow-v3-remote"); err != nil || !ok || storedUnassign.Assignment.Enabled || flowV3HasTargetSelection(storedUnassign.Assignment.Target) {
		t.Fatalf("stored unassign definition = %+v ok=%v err=%v", storedUnassign, ok, err)
	}
	if _, err := flows.PutMirroredRunSummary(pebblestore.FlowRunSummaryRecord{
		AccountScopeID: testAccountScopeID,
		UserID:         testUserID,
		RunID:          "run-v3-1",
		FlowID:         "flow-v3-remote",
		Revision:       2,
		ScheduledAt:    time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC),
		StartedAt:      time.Date(2025, 1, 2, 9, 0, 1, 0, time.UTC),
		FinishedAt:     time.Date(2025, 1, 2, 9, 0, 3, 0, time.UTC),
		Status:         pebblestore.FlowRunStatusSuccess,
		Summary:        "done",
		TargetSwarmID:  "child-remote",
	}); err != nil {
		t.Fatalf("put mirrored summary: %v", err)
	}
	historyRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(historyRec, requestWithTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/flows/flow-v3-remote/history", nil)))
	if historyRec.Code != http.StatusOK {
		t.Fatalf("history status = %d body=%s", historyRec.Code, historyRec.Body.String())
	}
	var historyPayload flowV3HistoryResponse
	if err := json.Unmarshal(historyRec.Body.Bytes(), &historyPayload); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(historyPayload.History) != 1 || historyPayload.History[0].RunID != "run-v3-1" {
		t.Fatalf("history payload = %+v", historyPayload)
	}
	statusRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(statusRec, requestWithTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/flows/flow-v3-remote/status", nil)))
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status code = %d body=%s", statusRec.Code, statusRec.Body.String())
	}
	var statusPayload flowV3StatusResponse
	if err := json.Unmarshal(statusRec.Body.Bytes(), &statusPayload); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if len(statusPayload.AssignmentStatuses) == 0 || len(statusPayload.History) != 1 {
		t.Fatalf("status payload = %+v", statusPayload)
	}
	runRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(runRec, requestWithTestPrincipal(httptest.NewRequest(http.MethodPost, "/v3/flows/flow-v3-remote/run-now", nil)))
	if runRec.Code != http.StatusAccepted {
		t.Fatalf("run-now status = %d body=%s", runRec.Code, runRec.Body.String())
	}
	if runner.lastRequest.TargetKind != "agent" || runner.lastRequest.TargetName != "swarm" {
		t.Fatalf("runner request = %+v", runner.lastRequest)
	}
	var runPayload flowV3MutationResponse
	if err := json.Unmarshal(runRec.Body.Bytes(), &runPayload); err != nil {
		t.Fatalf("decode run now: %v", err)
	}
	if runPayload.Run == nil || runPayload.Run.CommandID == "" || runPayload.Run.PendingSync {
		t.Fatalf("run payload = %+v", runPayload)
	}
	deleteRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(deleteRec, requestWithTestPrincipal(httptest.NewRequest(http.MethodDelete, "/v3/flows/flow-v3-remote", nil)))
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	var deletePayload flowV3MutationResponse
	if err := json.Unmarshal(deleteRec.Body.Bytes(), &deletePayload); err != nil {
		t.Fatalf("decode delete: %v", err)
	}
	if deletePayload.Flow.Definition.FlowID != "flow-v3-remote" || deletePayload.Flow.Definition.DeletedAt.IsZero() {
		t.Fatalf("delete payload = %+v", deletePayload.Flow.Definition)
	}
	if _, ok, err := flows.GetDefinitionForAccount(testAccountScopeID, "flow-v3-remote"); err != nil || ok {
		t.Fatalf("definition after delete ok=%v err=%v", ok, err)
	}
	if _, ok, err := flows.GetAcceptedAssignmentForAccount(testAccountScopeID, "flow-v3-remote"); err != nil || ok {
		t.Fatalf("accepted after delete ok=%v err=%v", ok, err)
	}
}

func TestFlowsV3LocalContainerCRUDSyncsAcrossHTTPBoundary(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	ensureFlowTestAgent(t, server)
	workspace := t.TempDir()
	var delivered []flow.AssignmentCommand
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != flowPeerApplyPath {
			http.NotFound(w, r)
			return
		}
		var command flow.AssignmentCommand
		if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
			t.Fatalf("decode child command: %v", err)
		}
		delivered = append(delivered, command)
		ack, inserted, err := server.applyFlowAssignmentCommandLocally(r.Context(), command, "child-local")
		if err != nil {
			t.Fatalf("apply child command: %v", err)
		}
		writeJSON(w, http.StatusOK, flowAssignmentApplyResponse{OK: true, Ack: ack, Inserted: inserted})
	}))
	defer child.Close()
	server.SetDeployContainerService(&fakeFlowDeployService{targets: []swarmTarget{{
		SwarmID:      "child-local",
		Name:         "local child",
		Relationship: "child",
		Kind:         "local",
		DeploymentID: "pc-child-local",
		Online:       true,
		Selectable:   true,
		BackendURL:   child.URL,
	}}})
	seedFlowTopologyWorkspaceBinding(t, server, workspace, filepath.Base(workspace), "pc-child-local", "local", "child-local", "/workspaces/local")
	req := flowV3UpsertRequest{
		FlowID:  "flow-v3-local-crud",
		Name:    "Local V3 flow",
		Enabled: boolPtr(true),
		Target:  flow.TargetSelection{SwarmID: "child-local", Kind: "local", DeploymentID: "pc-child-local", Name: "local child"},
		Agent:   flow.AgentSelection{ProfileName: "flow-test", ProfileMode: "subagent"},
		Workspace: flow.WorkspaceContext{
			WorkspacePath: workspace,
		},
		Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceOnDemand},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent:        flow.PromptIntent{Prompt: "Sync to local container."},
	}
	createRec := httptest.NewRecorder()
	createHTTP := httptest.NewRequest(http.MethodPost, "/v3/flows", jsonReader(t, req))
	createHTTP.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(createRec, requestWithTestPrincipal(createHTTP))
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createRec.Code, createRec.Body.String())
	}
	updateReq := req
	updateReq.Name = "Local V3 flow updated"
	updateReq.Workspace.WorkspacePath = filepath.Join(workspace, "updated")
	updateRec := httptest.NewRecorder()
	updateHTTP := httptest.NewRequest(http.MethodPut, "/v3/flows/flow-v3-local-crud", jsonReader(t, updateReq))
	updateHTTP.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(updateRec, requestWithTestPrincipal(updateHTTP))
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", updateRec.Code, updateRec.Body.String())
	}
	deleteRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(deleteRec, requestWithTestPrincipal(httptest.NewRequest(http.MethodDelete, "/v3/flows/flow-v3-local-crud", nil)))
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if len(delivered) != 3 {
		t.Fatalf("delivered commands = %d, want 3: %+v", len(delivered), delivered)
	}
	if delivered[0].Action != flow.CommandInstall || delivered[1].Action != flow.CommandUpdate || delivered[2].Action != flow.CommandDelete {
		t.Fatalf("delivered actions = %q/%q/%q", delivered[0].Action, delivered[1].Action, delivered[2].Action)
	}
	if delivered[0].Assignment.Target.SwarmID != "child-local" || delivered[0].Assignment.Target.Kind != "local" || delivered[0].Assignment.Target.DeploymentID != "pc-child-local" {
		t.Fatalf("delivered target = %+v", delivered[0].Assignment.Target)
	}
	if _, ok, err := flows.GetAcceptedAssignmentForAccount(testAccountScopeID, "flow-v3-local-crud"); err != nil || ok {
		t.Fatalf("accepted after delete ok=%v err=%v", ok, err)
	}
	status, ok, err := flows.GetAssignmentStatusForAccount(testAccountScopeID, "flow-v3-local-crud", "child-local")
	if err != nil || !ok {
		t.Fatalf("assignment status ok=%v err=%v", ok, err)
	}
	if status.PendingSync || status.Status != flow.AssignmentAccepted || status.AcceptedRevision != 2 {
		t.Fatalf("assignment status = %+v", status)
	}
}

func TestFlowsV3CreateAllowsLocalContainerTargetWithLocalOwnerHost(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	ensureFlowTestAgent(t, server)
	workspace := t.TempDir()
	var delivered []flow.AssignmentCommand
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != flowPeerApplyPath {
			http.NotFound(w, r)
			return
		}
		var command flow.AssignmentCommand
		if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
			t.Fatalf("decode child command: %v", err)
		}
		delivered = append(delivered, command)
		ack, inserted, err := server.applyFlowAssignmentCommandLocally(r.Context(), command, "local-container-swarm")
		if err != nil {
			t.Fatalf("apply child command: %v", err)
		}
		writeJSON(w, http.StatusOK, flowAssignmentApplyResponse{OK: true, Ack: ack, Inserted: inserted})
	}))
	defer child.Close()
	server.SetDeployContainerService(&fakeFlowDeployService{targets: []swarmTarget{{
		SwarmID:      "local-container-swarm",
		Name:         "local container",
		Relationship: "child",
		Kind:         "local",
		DeploymentID: "pc-local-container",
		HostSwarmID:  "host-swarm-id",
		Online:       true,
		Selectable:   true,
		BackendURL:   child.URL,
	}}})
	seedFlowTopologyWorkspaceBinding(t, server, workspace, filepath.Base(workspace), "pc-local-container", "local", "local-container-swarm", "/workspaces/local-container")
	req := flowV3UpsertRequest{
		FlowID:  "flow-v3-local-container-owner-host",
		Name:    "Local container owner-host flow",
		Enabled: boolPtr(true),
		Target:  flow.TargetSelection{SwarmID: "local-container-swarm", Kind: "local", DeploymentID: "pc-local-container", Name: "local container"},
		Agent:   flow.AgentSelection{ProfileName: "flow-test", ProfileMode: "subagent"},
		Workspace: flow.WorkspaceContext{
			WorkspacePath: workspace,
		},
		Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceOnDemand},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent:        flow.PromptIntent{Prompt: "Create on a local container target."},
	}
	createRec := httptest.NewRecorder()
	createHTTP := httptest.NewRequest(http.MethodPost, "/v3/flows", jsonReader(t, req))
	createHTTP.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(createRec, requestWithTestPrincipal(createHTTP))
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createRec.Code, createRec.Body.String())
	}
	if strings.Contains(createRec.Body.String(), "target must be a managed host") {
		t.Fatalf("create unexpectedly used managed-host-only validation: %s", createRec.Body.String())
	}
	if len(delivered) != 1 {
		t.Fatalf("delivered commands = %d, want 1: %+v", len(delivered), delivered)
	}
	if delivered[0].Action != flow.CommandInstall || delivered[0].Assignment.Target.SwarmID != "local-container-swarm" || delivered[0].Assignment.Target.Kind != "local" {
		t.Fatalf("delivered command = %+v", delivered[0])
	}
	if _, ok, err := flows.GetDefinitionForAccount(testAccountScopeID, "flow-v3-local-container-owner-host"); err != nil || !ok {
		t.Fatalf("stored definition ok=%v err=%v", ok, err)
	}
	status, ok, err := flows.GetAssignmentStatusForAccount(testAccountScopeID, "flow-v3-local-container-owner-host", "local-container-swarm")
	if err != nil || !ok {
		t.Fatalf("assignment status ok=%v err=%v", ok, err)
	}
	if status.PendingSync || status.Status != flow.AssignmentAccepted || status.AcceptedRevision != 1 {
		t.Fatalf("assignment status = %+v", status)
	}
}

func TestFlowsV3ChildListsTargetAcceptedAssignmentsWithoutControllerDefinitions(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	ensureFlowTestAgent(t, server)
	acceptedAt := time.Date(2026, 5, 11, 4, 52, 43, 0, time.UTC)
	accepted := flow.AcceptedAssignment{
		AccountScopeID: testAccountScopeID,
		UserID:         testUserID,
		Assignment: flow.Assignment{
			FlowID:   "flow-child-visible",
			Revision: 1,
			Name:     "Child Visible Flow",
			Enabled:  true,
			Target:   flow.TargetSelection{SwarmID: "child-local", Kind: "local", DeploymentID: "container", Name: "container"},
			Agent:    flow.AgentSelection{ProfileName: "flow-test", ProfileMode: "subagent"},
			Workspace: flow.WorkspaceContext{
				WorkspacePath: "/workspaces/swarm-go",
			},
			Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceDaily, Time: "00:00", Timezone: "Asia/Jerusalem"},
			CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
			Intent:        flow.PromptIntent{Prompt: "target-owned schedule"},
		},
		AcceptedAt: acceptedAt,
	}
	ack, inserted, err := flows.ApplyTargetAssignmentCommand(flow.AssignmentCommand{AccountScopeID: testAccountScopeID, UserID: testUserID, CommandID: "cmd-install", FlowID: accepted.FlowID, Revision: accepted.Revision, Action: flow.CommandInstall, CreatedAt: acceptedAt, Assignment: accepted.Assignment}, "child-local", acceptedAt)
	if err != nil || !inserted || ack.Status != flow.AssignmentAccepted {
		t.Fatalf("accept target command inserted=%v ack=%+v err=%v", inserted, ack, err)
	}
	if _, ok, err := flows.GetDefinitionForAccount(testAccountScopeID, "flow-child-visible"); err != nil || ok {
		t.Fatalf("controller definition ok=%v err=%v", ok, err)
	}

	listRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(listRec, requestWithTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/flows", nil)))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listRec.Code, listRec.Body.String())
	}
	var listPayload flowV3ListResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listPayload.Flows) != 1 || listPayload.Flows[0].Definition.FlowID != "flow-child-visible" || listPayload.Flows[0].Definition.Name != "Child Visible Flow" {
		t.Fatalf("list payload = %+v", listPayload)
	}
	if listPayload.Flows[0].Definition.Target.DeploymentID != "container" || listPayload.Flows[0].Definition.Agent.ProfileName != "flow-test" {
		t.Fatalf("listed definition = %+v", listPayload.Flows[0].Definition)
	}

	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, requestWithTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/flows/flow-child-visible", nil)))
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", getRec.Code, getRec.Body.String())
	}
	var getPayload flowV3RecordResponse
	if err := json.Unmarshal(getRec.Body.Bytes(), &getPayload); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if getPayload.Definition.FlowID != "flow-child-visible" || getPayload.Definition.Revision != 1 {
		t.Fatalf("get payload = %+v", getPayload)
	}
}

func TestFlowsV3CreateDoesNotAutoRunOnDemandFlow(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	ensureFlowTestAgent(t, server)
	runner := &fakeFlowRunService{}
	server.runner = runner
	workspace := t.TempDir()
	req := flowV3UpsertRequest{
		FlowID:  "flow-v3-one-shot",
		Name:    "One-shot V3 flow",
		Enabled: boolPtr(true),
		Target:  flow.TargetSelection{Kind: "self"},
		Agent:   flow.AgentSelection{ProfileName: "flow-test", ProfileMode: "subagent"},
		Workspace: flow.WorkspaceContext{
			WorkspacePath: workspace,
			CWD:           workspace,
		},
		Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceOnDemand},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent:        flow.PromptIntent{Prompt: "Run once immediately.", Mode: "one_shot_background"},
	}
	rec := httptest.NewRecorder()
	reqHTTP := httptest.NewRequest(http.MethodPost, "/v3/flows", jsonReader(t, req))
	reqHTTP.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(rec, requestWithTestPrincipal(reqHTTP))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload flowV3MutationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if payload.Result == nil || !payload.Result.Delivered || payload.Result.PendingSync {
		t.Fatalf("install result = %+v", payload.Result)
	}
	if payload.Run != nil {
		t.Fatalf("create unexpectedly returned run payload: %+v", payload.Run)
	}
	if got := runner.callCount(); got != 0 {
		t.Fatalf("runner call count = %d, want 0", got)
	}
	definition, ok, err := flows.GetDefinitionForAccount(testAccountScopeID, "flow-v3-one-shot")
	if err != nil || !ok {
		t.Fatalf("get definition ok=%v err=%v", ok, err)
	}
	if definition.Assignment.Intent.Mode != "one_shot_background" {
		t.Fatalf("stored intent mode = %q", definition.Assignment.Intent.Mode)
	}
	if payload.Flow.LastRun != nil {
		t.Fatalf("last run = %+v, want nil after create-only save", payload.Flow.LastRun)
	}
}

func TestFlowsV3CreateSchedulesMultipleTimesAndPreservesTimezone(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	ensureFlowTestAgent(t, server)
	req := flowV3UpsertRequest{
		FlowID:  "flow-v3-multi-time",
		Name:    "Multi-time flow",
		Enabled: boolPtr(true),
		Target:  flow.TargetSelection{Kind: "self"},
		Agent:   flow.AgentSelection{ProfileName: "flow-test", ProfileMode: "subagent"},
		Workspace: flow.WorkspaceContext{
			WorkspacePath: t.TempDir(),
		},
		Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceDaily, Times: []string{"17:00", "09:00"}, Timezone: "America/New_York"},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent:        flow.PromptIntent{Prompt: "Run twice daily."},
	}
	rec := httptest.NewRecorder()
	reqHTTP := httptest.NewRequest(http.MethodPost, "/v3/flows", jsonReader(t, req))
	reqHTTP.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(rec, requestWithTestPrincipal(reqHTTP))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload flowV3MutationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if payload.Flow.Definition.Schedule.Timezone != "America/New_York" {
		t.Fatalf("schedule timezone = %q", payload.Flow.Definition.Schedule.Timezone)
	}
	if len(payload.Flow.Definition.Schedule.Times) != 2 {
		t.Fatalf("schedule times = %+v", payload.Flow.Definition.Schedule.Times)
	}
	if !(payload.Flow.Definition.Schedule.Times[0] == "09:00" && payload.Flow.Definition.Schedule.Times[1] == "17:00") &&
		!(payload.Flow.Definition.Schedule.Times[0] == "17:00" && payload.Flow.Definition.Schedule.Times[1] == "09:00") {
		t.Fatalf("schedule times = %+v", payload.Flow.Definition.Schedule.Times)
	}
	definition, ok, err := flows.GetDefinitionForAccount(testAccountScopeID, "flow-v3-multi-time")
	if err != nil || !ok {
		t.Fatalf("get definition ok=%v err=%v", ok, err)
	}
	if len(definition.Assignment.Schedule.Times) != 2 {
		t.Fatalf("stored schedule times = %+v", definition.Assignment.Schedule.Times)
	}
	if !(definition.Assignment.Schedule.Times[0] == "09:00" && definition.Assignment.Schedule.Times[1] == "17:00") &&
		!(definition.Assignment.Schedule.Times[0] == "17:00" && definition.Assignment.Schedule.Times[1] == "09:00") {
		t.Fatalf("stored schedule times = %+v", definition.Assignment.Schedule.Times)
	}
}

func TestFlowsV3CreateAcceptsModalWeeklyMultiDayAndRawCron(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	ensureFlowTestAgent(t, server)
	workspace := t.TempDir()
	weeklyReq := flowV3UpsertRequest{
		FlowID:  "flow-v3-weekly-multi-day",
		Name:    "Weekly multi-day flow",
		Enabled: boolPtr(true),
		Target:  flow.TargetSelection{Kind: "self"},
		Agent:   flow.AgentSelection{ProfileName: "flow-test", ProfileMode: "subagent"},
		Workspace: flow.WorkspaceContext{
			WorkspacePath: workspace,
		},
		Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceWeekly, Time: "09:00", Times: []string{"09:00"}, Weekday: "Mon,Wed,Fri", Timezone: "UTC"},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent:        flow.PromptIntent{Prompt: "Run weekly."},
	}
	weeklyRec := httptest.NewRecorder()
	weeklyHTTP := httptest.NewRequest(http.MethodPost, "/v3/flows", jsonReader(t, weeklyReq))
	weeklyHTTP.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(weeklyRec, requestWithTestPrincipal(weeklyHTTP))
	if weeklyRec.Code != http.StatusCreated {
		t.Fatalf("weekly create status = %d body=%s", weeklyRec.Code, weeklyRec.Body.String())
	}
	weeklyDefinition, ok, err := flows.GetDefinitionForAccount(testAccountScopeID, "flow-v3-weekly-multi-day")
	if err != nil || !ok {
		t.Fatalf("weekly get definition ok=%v err=%v", ok, err)
	}
	if weeklyDefinition.Assignment.Schedule.Weekday != "Mon,Wed,Fri" {
		t.Fatalf("weekly stored weekday = %q", weeklyDefinition.Assignment.Schedule.Weekday)
	}
	cronReq := weeklyReq
	cronReq.FlowID = "flow-v3-raw-cron"
	cronReq.Name = "Raw cron flow"
	cronReq.Schedule = flow.ScheduleSpec{Cadence: flow.CadenceDaily, Time: "09:00", Times: []string{"09:00"}, Timezone: "UTC", Cron: "*/20 9-10 * * Mon-Fri"}
	cronRec := httptest.NewRecorder()
	cronHTTP := httptest.NewRequest(http.MethodPost, "/v3/flows", jsonReader(t, cronReq))
	cronHTTP.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(cronRec, requestWithTestPrincipal(cronHTTP))
	if cronRec.Code != http.StatusCreated {
		t.Fatalf("cron create status = %d body=%s", cronRec.Code, cronRec.Body.String())
	}
	cronDefinition, ok, err := flows.GetDefinitionForAccount(testAccountScopeID, "flow-v3-raw-cron")
	if err != nil || !ok {
		t.Fatalf("cron get definition ok=%v err=%v", ok, err)
	}
	if cronDefinition.Assignment.Schedule.Cron != "*/20 9-10 * * Mon-Fri" {
		t.Fatalf("cron stored expression = %q", cronDefinition.Assignment.Schedule.Cron)
	}
}

func TestFlowsV3CreatePersistsPendingSyncWhenTargetIsUnavailable(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	ensureFlowTestAgent(t, server)
	server.SetDeployContainerService(&fakeFlowDeployService{targets: []swarmTarget{{
		SwarmID:      "child-offline",
		Name:         "offline child",
		Relationship: "child",
		Kind:         "local",
		DeploymentID: "pc-offline",
		Online:       false,
		Selectable:   false,
		LastError:    "child is stopped",
	}}})
	workspace := t.TempDir()
	seedFlowTopologyWorkspaceBinding(t, server, workspace, filepath.Base(workspace), "pc-offline", "local", "child-offline", "/workspaces/offline")
	req := flowV3UpsertRequest{
		FlowID:  "flow-v3-pending-sync",
		Name:    "Pending sync flow",
		Enabled: boolPtr(true),
		Target:  flow.TargetSelection{SwarmID: "child-offline", Kind: "local", DeploymentID: "pc-offline", Name: "offline child"},
		Agent:   flow.AgentSelection{ProfileName: "flow-test", ProfileMode: "subagent"},
		Workspace: flow.WorkspaceContext{
			WorkspacePath: workspace,
		},
		Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceOnDemand},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent:        flow.PromptIntent{Prompt: "Wait until child returns."},
	}
	rec := httptest.NewRecorder()
	reqHTTP := httptest.NewRequest(http.MethodPost, "/v3/flows", jsonReader(t, req))
	reqHTTP.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(rec, requestWithTestPrincipal(reqHTTP))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload flowV3MutationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if payload.Result == nil || !payload.Result.PendingSync || payload.Result.Delivered {
		t.Fatalf("create result = %+v", payload.Result)
	}
	if payload.Result.AssignmentState.Status != flow.AssignmentTargetOffline {
		t.Fatalf("assignment state = %+v", payload.Result.AssignmentState)
	}
	if len(payload.Flow.Outbox) != 1 || payload.Flow.Outbox[0].Status != pebblestore.FlowOutboxStatusPending {
		t.Fatalf("flow outbox = %+v", payload.Flow.Outbox)
	}
	if len(payload.Flow.AssignmentStatuses) == 0 || !payload.Flow.AssignmentStatuses[0].PendingSync {
		t.Fatalf("assignment statuses = %+v", payload.Flow.AssignmentStatuses)
	}
	definition, ok, err := flows.GetDefinitionForAccount(testAccountScopeID, "flow-v3-pending-sync")
	if err != nil || !ok {
		t.Fatalf("get definition ok=%v err=%v", ok, err)
	}
	if definition.Assignment.Name != "Pending sync flow" {
		t.Fatalf("stored definition = %+v", definition.Assignment)
	}
	pending, err := flows.ListOutboxCommandsForAccount(testAccountScopeID, "", pebblestore.FlowOutboxStatusPending, 10)
	if err != nil {
		t.Fatalf("list pending outbox: %v", err)
	}
	if len(pending) != 1 || pending[0].FlowID != "flow-v3-pending-sync" {
		t.Fatalf("pending outbox = %+v", pending)
	}
	status, ok, err := flows.GetAssignmentStatusForAccount(testAccountScopeID, "flow-v3-pending-sync", "child-offline")
	if err != nil || !ok {
		t.Fatalf("get assignment status ok=%v err=%v", ok, err)
	}
	if !status.PendingSync || status.Status != flow.AssignmentTargetOffline {
		t.Fatalf("stored assignment status = %+v", status)
	}
	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, requestWithTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/flows/flow-v3-pending-sync", nil)))
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", getRec.Code, getRec.Body.String())
	}
	var getPayload flowV3RecordResponse
	if err := json.Unmarshal(getRec.Body.Bytes(), &getPayload); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if len(getPayload.Outbox) != 1 || getPayload.Outbox[0].Status != pebblestore.FlowOutboxStatusPending {
		t.Fatalf("get outbox = %+v", getPayload.Outbox)
	}
}

func TestFlowsV3RejectsUnknownDisabledAndMismatchedAgents(t *testing.T) {
	for _, tc := range []struct {
		name      string
		agent     flow.AgentSelection
		prepare   func(*testing.T, *Server)
		wantError string
	}{
		{name: "missing profile", agent: flow.AgentSelection{ProfileName: "does-not-exist", ProfileMode: "background"}, wantError: "was not found"},
		{name: "disabled profile", agent: flow.AgentSelection{ProfileName: "disabled-memory", ProfileMode: "subagent"}, prepare: func(t *testing.T, server *Server) {
			t.Helper()
			enabled := false
			_, _, _, err := server.agents.UpsertForAccount(testAccountScopeID, agentruntime.UpsertInput{Name: "disabled-memory", Mode: agentruntime.ModeSubagent, Provider: "test-provider", Model: "test-model", Thinking: "medium", ProviderSet: true, ModelSet: true, ThinkingSet: true, Enabled: &enabled})
			if err != nil {
				t.Fatalf("upsert disabled profile: %v", err)
			}
		}, wantError: "is disabled"},
		{name: "mismatched mode", agent: flow.AgentSelection{ProfileName: "memory", ProfileMode: "background"}, prepare: ensureFlowMemoryAgentRunnable, wantError: "does not match requested profile_mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newFlowPeerTestServer(t)
			if tc.prepare != nil {
				tc.prepare(t, server)
			}
			req := flowV3UpsertRequest{
				FlowID:        "flow-v3-invalid-agent",
				Name:          "Invalid agent",
				Target:        flow.TargetSelection{Kind: "self"},
				Agent:         tc.agent,
				Workspace:     flow.WorkspaceContext{WorkspacePath: t.TempDir()},
				Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceOnDemand},
				CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
				Intent:        flow.PromptIntent{Prompt: "Reject invalid agent."},
			}
			rec := httptest.NewRecorder()
			reqHTTP := httptest.NewRequest(http.MethodPost, "/v3/flows", jsonReader(t, req))
			reqHTTP.Header.Set("Content-Type", "application/json")
			server.Handler().ServeHTTP(rec, requestWithTestPrincipal(reqHTTP))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantError) {
				t.Fatalf("body = %s, want substring %q", rec.Body.String(), tc.wantError)
			}
		})
	}
}

func TestFlowsV3RejectsMissingTargetAndBadTarget(t *testing.T) {
	server, _ := newFlowPeerTestServer(t)
	ensureFlowPrimaryAgentRunnable(t, server)
	for _, tc := range []struct {
		name      string
		target    flow.TargetSelection
		wantError string
	}{
		{name: "missing target", target: flow.TargetSelection{}, wantError: "target selection is required"},
		{name: "unknown target", target: flow.TargetSelection{SwarmID: "missing-target", Kind: "remote"}, wantError: "flow target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := flowV3UpsertRequest{
				FlowID:        "flow-v3-invalid-target",
				Name:          "Invalid target",
				Target:        tc.target,
				Agent:         flow.AgentSelection{ProfileName: "swarm", ProfileMode: "primary"},
				Workspace:     flow.WorkspaceContext{WorkspacePath: t.TempDir()},
				Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceOnDemand},
				CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
				Intent:        flow.PromptIntent{Prompt: "Reject invalid target."},
			}
			rec := httptest.NewRecorder()
			reqHTTP := httptest.NewRequest(http.MethodPost, "/v3/flows", jsonReader(t, req))
			reqHTTP.Header.Set("Content-Type", "application/json")
			server.Handler().ServeHTTP(rec, requestWithTestPrincipal(reqHTTP))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantError) {
				t.Fatalf("body = %s, want substring %q", rec.Body.String(), tc.wantError)
			}
		})
	}
}

func boolPtr(value bool) *bool {
	return &value
}

func TestFlowsV3CreateResolvesWorkspaceBindingByName(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	ensureFlowTestAgent(t, server)
	server.SetDeployContainerService(&fakeFlowDeployService{targets: []swarmTarget{{
		SwarmID:      "managed-swarm",
		Name:         "managed",
		Relationship: "child",
		Kind:         "local",
		DeploymentID: "managed-deployment",
		Online:       true,
		Selectable:   true,
	}}})
	seedFlowTopologyWorkspaceBinding(t, server, "/home/installer/workspaces/swarm-go-6", "swarm-go", "managed-deployment", "local", "managed-swarm", "/workspaces/swarm-go")
	req := flowV3UpsertRequest{
		FlowID:  "flow-v3-binding-name",
		Name:    "Binding name flow",
		Enabled: boolPtr(true),
		Target:  flow.TargetSelection{SwarmID: "managed-swarm", Kind: "local", DeploymentID: "managed-deployment", Name: "managed"},
		Agent:   flow.AgentSelection{ProfileName: "flow-test", ProfileMode: "subagent"},
		Workspace: flow.WorkspaceContext{
			WorkspacePath:     "/home/installer/swarm-go",
			WorkspaceName:     "swarm-go",
			HostWorkspacePath: "/home/installer/swarm-go",
		},
		Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceOnDemand},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent:        flow.PromptIntent{Prompt: "Resolve by workspace name."},
	}
	rec := httptest.NewRecorder()
	reqHTTP := httptest.NewRequest(http.MethodPost, "/v3/flows", jsonReader(t, req))
	reqHTTP.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(rec, requestWithTestPrincipal(reqHTTP))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	definition, ok, err := flows.GetDefinitionForAccount(testAccountScopeID, "flow-v3-binding-name")
	if err != nil || !ok {
		t.Fatalf("get definition ok=%v err=%v", ok, err)
	}
	workspace := definition.Assignment.Workspace
	if workspace.WorkspaceBindingID == "" || workspace.HostWorkspacePath != "/home/installer/workspaces/swarm-go-6" || workspace.RuntimeWorkspacePath != "/workspaces/swarm-go" {
		t.Fatalf("stored workspace = %+v", workspace)
	}
	outbox, err := flows.ListOutboxCommandsForAccount(testAccountScopeID, "flow-v3-binding-name", "", 10)
	if err != nil || len(outbox) != 1 {
		t.Fatalf("outbox len=%d err=%v", len(outbox), err)
	}
	if outbox[0].Command.Assignment.Workspace.WorkspacePath != "/workspaces/swarm-go" || outbox[0].Command.Assignment.Workspace.RuntimeWorkspacePath != "/workspaces/swarm-go" {
		t.Fatalf("delivered workspace = %+v", outbox[0].Command.Assignment.Workspace)
	}
}

func TestFlowsV3CreateRejectsAmbiguousWorkspaceNameBinding(t *testing.T) {
	server, _ := newFlowPeerTestServer(t)
	ensureFlowTestAgent(t, server)
	server.SetDeployContainerService(&fakeFlowDeployService{targets: []swarmTarget{{
		SwarmID:      "managed-swarm",
		Name:         "managed",
		Relationship: "child",
		Kind:         "local",
		DeploymentID: "managed-deployment",
		Online:       true,
		Selectable:   true,
	}}})
	seedFlowTopologyWorkspaceBinding(t, server, "/source/one", "swarm-go", "managed-deployment", "local", "managed-swarm", "/workspaces/one")
	seedFlowTopologyWorkspaceBinding(t, server, "/source/two", "swarm-go", "managed-deployment", "local", "managed-swarm", "/workspaces/two")
	req := flowV3UpsertRequest{
		FlowID:  "flow-v3-binding-ambiguous",
		Name:    "Ambiguous binding flow",
		Enabled: boolPtr(true),
		Target:  flow.TargetSelection{SwarmID: "managed-swarm", Kind: "local", DeploymentID: "managed-deployment", Name: "managed"},
		Agent:   flow.AgentSelection{ProfileName: "flow-test", ProfileMode: "subagent"},
		Workspace: flow.WorkspaceContext{
			WorkspacePath: "/home/installer/swarm-go",
			WorkspaceName: "swarm-go",
		},
		Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceOnDemand},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent:        flow.PromptIntent{Prompt: "Reject ambiguous binding."},
	}
	rec := httptest.NewRecorder()
	reqHTTP := httptest.NewRequest(http.MethodPost, "/v3/flows", jsonReader(t, req))
	reqHTTP.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(rec, requestWithTestPrincipal(reqHTTP))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "matches multiple topology bindings") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestFlowsV3DeleteAllowsStaleMissingTarget(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	ensureFlowTestAgent(t, server)
	assignment := flow.Assignment{
		FlowID:   "flow-v3-stale-target",
		Revision: 1,
		Name:     "Stale target flow",
		Enabled:  true,
		Target:   flow.TargetSelection{SwarmID: "swarm-missing", Kind: "local"},
		Agent:    flow.AgentSelection{ProfileName: "flow-test", ProfileMode: "subagent"},
		Workspace: flow.WorkspaceContext{
			WorkspacePath: t.TempDir(),
		},
		Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceOnDemand},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent:        flow.PromptIntent{Prompt: "Clean up stale flow."},
	}
	if _, err := flows.PutDefinitionForAccount(pebblestore.FlowDefinitionRecord{AccountScopeID: testAccountScopeID, UserID: testUserID, FlowID: assignment.FlowID, Revision: assignment.Revision, Assignment: assignment}); err != nil {
		t.Fatalf("put stale definition: %v", err)
	}
	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, requestWithTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/flows/flow-v3-stale-target", nil)))
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", getRec.Code, getRec.Body.String())
	}
	var getPayload flowV3RecordResponse
	if err := json.Unmarshal(getRec.Body.Bytes(), &getPayload); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if !getPayload.TargetStale || !strings.Contains(getPayload.TargetStaleReason, "swarm-missing") {
		t.Fatalf("stale target markers = stale:%v reason:%q", getPayload.TargetStale, getPayload.TargetStaleReason)
	}

	deleteRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(deleteRec, requestWithTestPrincipal(httptest.NewRequest(http.MethodDelete, "/v3/flows/flow-v3-stale-target", nil)))
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	var deletePayload flowV3MutationResponse
	if err := json.Unmarshal(deleteRec.Body.Bytes(), &deletePayload); err != nil {
		t.Fatalf("decode delete: %v", err)
	}
	if !deletePayload.Flow.TargetStale || !strings.Contains(deletePayload.Flow.TargetStaleReason, "swarm-missing") {
		t.Fatalf("delete stale markers = stale:%v reason:%q", deletePayload.Flow.TargetStale, deletePayload.Flow.TargetStaleReason)
	}
	if _, ok, err := flows.GetDefinitionForAccount(testAccountScopeID, "flow-v3-stale-target"); err != nil || ok {
		t.Fatalf("definition after stale delete ok=%v err=%v", ok, err)
	}
}
