package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/flow"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestFlowManagementCreateListHistoryAndStatus(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	server.runner = &fakeFlowRunService{}
	req := flowManagementCreateRequest{
		FlowID:  "flow-api",
		Name:    "API flow",
		Enabled: boolPtr(true),
		Target:  flow.TargetSelection{Kind: "self", Name: "host-swarm"},
		Agent:   flow.AgentSelection{TargetKind: "background", TargetName: "memory"},
		Workspace: flow.WorkspaceContext{
			WorkspacePath: t.TempDir(),
		},
		Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceDaily, Time: "09:00", Timezone: "UTC"},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent:        flow.PromptIntent{Prompt: "Refresh memory.", Tasks: []flow.TaskStep{{ID: "read", Title: "Read", Action: "read"}}},
	}
	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/v1/flows", jsonReader(t, req))
	createReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createRec.Code, createRec.Body.String())
	}
	var createPayload flowManagementMutationResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &createPayload); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if createPayload.Flow.Definition.FlowID != "flow-api" || createPayload.Result == nil || !createPayload.Result.Delivered {
		t.Fatalf("create payload = %+v", createPayload)
	}
	accepted, ok, err := flows.GetAcceptedAssignment("flow-api")
	if err != nil || !ok || accepted.Revision != 1 {
		t.Fatalf("accepted ok=%v accepted=%+v err=%v", ok, accepted, err)
	}

	if _, err := flows.PutMirroredRunSummary(pebblestore.FlowRunSummaryRecord{
		RunID:         "run-api-1",
		FlowID:        "flow-api",
		Revision:      1,
		ScheduledAt:   time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC),
		StartedAt:     time.Date(2025, 1, 2, 9, 0, 1, 0, time.UTC),
		FinishedAt:    time.Date(2025, 1, 2, 9, 0, 3, 0, time.UTC),
		Status:        pebblestore.FlowRunStatusSuccess,
		Summary:       "done",
		TargetSwarmID: "host-swarm-id",
	}); err != nil {
		t.Fatalf("put mirrored summary: %v", err)
	}

	listRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/v1/flows", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listRec.Code, listRec.Body.String())
	}
	var listPayload flowManagementListResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listPayload.Flows) != 1 || listPayload.Flows[0].Definition.FlowID != "flow-api" || listPayload.Flows[0].LastRun == nil {
		t.Fatalf("list payload = %+v", listPayload)
	}

	historyRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(historyRec, httptest.NewRequest(http.MethodGet, "/v1/flows/flow-api/history", nil))
	if historyRec.Code != http.StatusOK {
		t.Fatalf("history status = %d body=%s", historyRec.Code, historyRec.Body.String())
	}
	var historyPayload flowManagementHistoryResponse
	if err := json.Unmarshal(historyRec.Body.Bytes(), &historyPayload); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(historyPayload.History) != 1 || historyPayload.History[0].RunID != "run-api-1" {
		t.Fatalf("history payload = %+v", historyPayload)
	}

	statusRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(statusRec, httptest.NewRequest(http.MethodGet, "/v1/flows/flow-api/status", nil))
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status code = %d body=%s", statusRec.Code, statusRec.Body.String())
	}
	var statusPayload flowManagementStatusResponse
	if err := json.Unmarshal(statusRec.Body.Bytes(), &statusPayload); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if len(statusPayload.AssignmentStatuses) != 1 || len(statusPayload.History) != 1 {
		t.Fatalf("status payload = %+v", statusPayload)
	}
}

func TestFlowManagementRunNowAndDelete(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	ensureFlowTestAgent(t, server)
	runner := &fakeFlowRunService{}
	server.runner = runner
	assignment := testAPIFlowAssignment("flow-api-run", 1)
	assignment.Agent = flow.AgentSelection{TargetKind: "subagent", TargetName: "flow-test"}
	assignment.Workspace.WorkspacePath = t.TempDir()
	definition, err := flows.PutDefinition(pebblestore.FlowDefinitionRecord{FlowID: assignment.FlowID, Revision: assignment.Revision, Assignment: assignment})
	if err != nil {
		t.Fatalf("put definition: %v", err)
	}
	if _, err := flows.PutAcceptedAssignment(flow.AcceptedAssignment{Assignment: definition.Assignment}); err != nil {
		t.Fatalf("put accepted: %v", err)
	}

	runRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(runRec, httptest.NewRequest(http.MethodPost, "/v1/flows/flow-api-run/run-now", nil))
	if runRec.Code != http.StatusAccepted {
		t.Fatalf("run-now status = %d body=%s", runRec.Code, runRec.Body.String())
	}
	if runner.lastRequest.TargetKind != "subagent" || runner.lastRequest.TargetName != "flow-test" {
		t.Fatalf("runner request = %+v", runner.lastRequest)
	}
	var runPayload flowManagementRunNowResponse
	if err := json.Unmarshal(runRec.Body.Bytes(), &runPayload); err != nil {
		t.Fatalf("decode run now: %v", err)
	}
	if runPayload.Run.CommandID == "" || runPayload.Run.PendingSync {
		t.Fatalf("run payload = %+v", runPayload)
	}

	deleteRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(deleteRec, httptest.NewRequest(http.MethodDelete, "/v1/flows/flow-api-run", nil))
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, ok, err := flows.GetDefinition("flow-api-run"); err != nil || ok {
		t.Fatalf("definition after delete ok=%v err=%v", ok, err)
	}
	if _, ok, err := flows.GetAcceptedAssignment("flow-api-run"); err != nil || ok {
		t.Fatalf("accepted after delete ok=%v err=%v", ok, err)
	}
}

func boolPtr(value bool) *bool {
	return &value
}
