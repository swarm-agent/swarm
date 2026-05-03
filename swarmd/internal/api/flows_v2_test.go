package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/flow"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestFlowsV2HostOnlyCreateListRunNowAndDelete(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	ensureFlowTestAgent(t, server)
	runner := &fakeFlowRunService{}
	server.runner = runner
	workspacePath := t.TempDir()
	req := flowV2CreateRequest{
		FlowID:        "flow-v2-host",
		Name:          "Host V2 flow",
		Enabled:       boolPtr(true),
		Target:        flow.TargetSelection{Kind: "self", Name: "host-swarm"},
		Agent:         flow.AgentSelection{ProfileName: "flow-test", ProfileMode: "subagent"},
		Workspace:     flow.WorkspaceContext{WorkspacePath: workspacePath, HostWorkspacePath: workspacePath, CWD: workspacePath},
		Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceOnDemand},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent:        flow.PromptIntent{Prompt: "Refresh host memory.", Tasks: []flow.TaskStep{{ID: "task", Title: "Run task", Action: "propose"}}},
	}

	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/v2/flows", jsonReader(t, req))
	createReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createRec.Code, createRec.Body.String())
	}
	var createPayload flowV2MutationResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &createPayload); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	assignment := createPayload.Flow.Definition.Assignment
	if assignment.FlowID != "flow-v2-host" || assignment.Target.SwarmID != "host-swarm-id" || assignment.Target.Kind != "self" {
		t.Fatalf("assignment target = %+v", assignment)
	}
	if assignment.Agent.TargetKind != "subagent" || assignment.Agent.TargetName != "flow-test" {
		t.Fatalf("assignment agent = %+v", assignment.Agent)
	}
	if createPayload.Flow.TargetDetail == nil || createPayload.Flow.TargetDetail.SwarmID != "host-swarm-id" {
		t.Fatalf("target detail = %+v", createPayload.Flow.TargetDetail)
	}
	if createPayload.Flow.AgentDetail == nil || createPayload.Flow.AgentDetail.Name != "flow-test" {
		t.Fatalf("agent detail = %+v", createPayload.Flow.AgentDetail)
	}
	if createPayload.Flow.WorkspaceDetail == nil || createPayload.Flow.WorkspaceDetail.WorkspacePath != workspacePath {
		t.Fatalf("workspace detail = %+v", createPayload.Flow.WorkspaceDetail)
	}
	accepted, ok, err := flows.GetAcceptedAssignment("flow-v2-host")
	if err != nil || !ok || accepted.Revision != 1 {
		t.Fatalf("accepted ok=%v accepted=%+v err=%v", ok, accepted, err)
	}
	outbox, err := flows.ListOutboxCommands("", 100)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	if len(outbox) != 0 {
		t.Fatalf("v2 host-only create must not use outbox/network, got %+v", outbox)
	}

	if _, err := flows.PutMirroredRunSummary(pebblestore.FlowRunSummaryRecord{RunID: "run-v2-1", FlowID: "flow-v2-host", Revision: 1, ScheduledAt: time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC), StartedAt: time.Date(2025, 1, 2, 9, 0, 1, 0, time.UTC), Status: pebblestore.FlowRunStatusSuccess, Summary: "done", TargetSwarmID: "host-swarm-id"}); err != nil {
		t.Fatalf("put mirrored summary: %v", err)
	}

	listRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/v2/flows?limit=200", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listRec.Code, listRec.Body.String())
	}
	var listPayload flowV2ListResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listPayload.Flows) != 1 || listPayload.Flows[0].Definition.FlowID != "flow-v2-host" || listPayload.Flows[0].TargetDetail == nil || listPayload.Flows[0].AgentDetail == nil || listPayload.Flows[0].LastRun == nil {
		t.Fatalf("list payload = %+v", listPayload)
	}

	runRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(runRec, httptest.NewRequest(http.MethodPost, "/v2/flows/flow-v2-host/run-now", nil))
	if runRec.Code != http.StatusAccepted {
		t.Fatalf("run-now status = %d body=%s", runRec.Code, runRec.Body.String())
	}
	if runner.lastRequest.TargetKind != "subagent" || runner.lastRequest.TargetName != "flow-test" {
		t.Fatalf("runner request = %+v", runner.lastRequest)
	}

	deleteRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(deleteRec, httptest.NewRequest(http.MethodDelete, "/v2/flows/flow-v2-host", nil))
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, ok, err := flows.GetDefinition("flow-v2-host"); err != nil || ok {
		t.Fatalf("definition after delete ok=%v err=%v", ok, err)
	}
	if _, ok, err := flows.GetAcceptedAssignment("flow-v2-host"); err != nil || ok {
		t.Fatalf("accepted after delete ok=%v err=%v", ok, err)
	}
}

func TestFlowsV2RejectsNonHostTargets(t *testing.T) {
	server, _ := newFlowPeerTestServer(t)
	ensureFlowMemoryAgentRunnable(t, server)
	req := flowV2CreateRequest{
		FlowID:        "flow-v2-remote",
		Name:          "Remote V2 flow",
		Target:        flow.TargetSelection{SwarmID: "not-host", Kind: "remote", Name: "other"},
		Agent:         flow.AgentSelection{ProfileName: "memory", ProfileMode: "background"},
		Workspace:     flow.WorkspaceContext{WorkspacePath: t.TempDir()},
		Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceOnDemand},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent:        flow.PromptIntent{Prompt: "No remote."},
	}
	rec := httptest.NewRecorder()
	reqHTTP := httptest.NewRequest(http.MethodPost, "/v2/flows", jsonReader(t, req))
	reqHTTP.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(rec, reqHTTP)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "host-only") {
		t.Fatalf("body did not explain host-only rejection: %s", rec.Body.String())
	}
}

func TestFlowsV2RequiresConsistentSavedProfileSelection(t *testing.T) {
	for _, tc := range []struct {
		name           string
		agent          flow.AgentSelection
		wantCreateCode int
		wantMode       string
		wantRunner     bool
	}{
		{name: "subagent profile selector", agent: flow.AgentSelection{ProfileName: "memory", ProfileMode: "subagent"}, wantCreateCode: http.StatusCreated, wantMode: "subagent", wantRunner: true},
		{name: "primary profile selector", agent: flow.AgentSelection{ProfileName: "swarm", ProfileMode: "primary"}, wantCreateCode: http.StatusCreated, wantMode: "primary", wantRunner: true},
		{name: "mismatched profile mode is rejected", agent: flow.AgentSelection{ProfileName: "memory", ProfileMode: "background"}, wantCreateCode: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newFlowPeerTestServer(t)
			ensureFlowMemoryAgentRunnable(t, server)
			ensureFlowPrimaryAgentRunnable(t, server)
			runner := &fakeFlowRunService{}
			server.runner = runner
			workspacePath := t.TempDir()
			req := flowV2CreateRequest{
				FlowID:        "flow-v2-saved-profile",
				Name:          "Saved profile selector",
				Target:        flow.TargetSelection{Kind: "self"},
				Agent:         tc.agent,
				Workspace:     flow.WorkspaceContext{WorkspacePath: workspacePath},
				Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceOnDemand},
				CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
				Intent:        flow.PromptIntent{Prompt: "Run saved profile."},
			}
			rec := httptest.NewRecorder()
			reqHTTP := httptest.NewRequest(http.MethodPost, "/v2/flows", jsonReader(t, req))
			reqHTTP.Header.Set("Content-Type", "application/json")
			server.Handler().ServeHTTP(rec, reqHTTP)
			if rec.Code != tc.wantCreateCode {
				t.Fatalf("create status = %d want %d body=%s", rec.Code, tc.wantCreateCode, rec.Body.String())
			}
			if tc.wantCreateCode != http.StatusCreated {
				return
			}
			var payload flowV2MutationResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode create: %v", err)
			}
			if payload.Flow.AgentDetail == nil || payload.Flow.AgentDetail.Name != tc.agent.ProfileName || payload.Flow.AgentDetail.Mode != tc.wantMode {
				t.Fatalf("agent detail = %+v", payload.Flow.AgentDetail)
			}
			if tc.wantRunner {
				runRec := httptest.NewRecorder()
				server.Handler().ServeHTTP(runRec, httptest.NewRequest(http.MethodPost, "/v2/flows/flow-v2-saved-profile/run-now", nil))
				if runRec.Code != http.StatusAccepted {
					t.Fatalf("run-now status = %d body=%s", runRec.Code, runRec.Body.String())
				}
				normalizedAgent := flow.NormalizeAgentSelection(tc.agent)
				if runner.lastRequest.TargetKind != normalizedAgent.TargetKind || runner.lastRequest.TargetName != normalizedAgent.TargetName {
					t.Fatalf("runner request = %+v", runner.lastRequest)
				}
			}
		})
	}
}

func TestFlowsV2RejectsUnknownAgentProfile(t *testing.T) {
	server, _ := newFlowPeerTestServer(t)
	req := flowV2CreateRequest{
		FlowID:        "flow-v2-missing-agent",
		Name:          "Missing agent V2 flow",
		Target:        flow.TargetSelection{Kind: "self"},
		Agent:         flow.AgentSelection{ProfileName: "does-not-exist", ProfileMode: "background"},
		Workspace:     flow.WorkspaceContext{WorkspacePath: t.TempDir()},
		Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceOnDemand},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent:        flow.PromptIntent{Prompt: "No missing agent."},
	}
	rec := httptest.NewRecorder()
	reqHTTP := httptest.NewRequest(http.MethodPost, "/v2/flows", jsonReader(t, req))
	reqHTTP.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(rec, reqHTTP)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "saved agent profile") {
		t.Fatalf("body did not explain agent rejection: %s", rec.Body.String())
	}
}
