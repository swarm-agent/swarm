package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	deployruntime "swarm/packages/swarmd/internal/deploy"
	"swarm/packages/swarmd/internal/flow"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	remotedeploy "swarm/packages/swarmd/internal/remotedeploy"
)

func TestFlowsV3CreateListGetAndUpdate(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	ensureFlowMemoryAgentRunnable(t, server)
	server.remoteDeploys = &fakeFlowRemoteDeployService{sessions: []remotedeploy.Session{{
		ID:             "pc-child-remote",
		Name:           "pc child",
		Status:         "attached",
		RemoteEndpoint: "http://child.example",
		ChildSwarmID:   "child-remote",
	}}}
	req := flowV3UpsertRequest{
		FlowID:  "flow-v3-remote",
		Name:    "Remote V3 flow",
		Enabled: boolPtr(true),
		Target:  flow.TargetSelection{SwarmID: "child-remote", Kind: "remote", DeploymentID: "pc-child-remote", Name: "pc child"},
		Agent:   flow.AgentSelection{ProfileName: "memory", ProfileMode: "subagent"},
		Workspace: flow.WorkspaceContext{
			WorkspacePath: t.TempDir(),
		},
		Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceOnDemand},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent:        flow.PromptIntent{Prompt: "Refresh memory remotely."},
	}
	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/v3/flows", jsonReader(t, req))
	createReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createRec.Code, createRec.Body.String())
	}
	var createPayload flowV3RecordResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &createPayload); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if createPayload.Definition.FlowID != "flow-v3-remote" || createPayload.Definition.Agent.ProfileName != "memory" || createPayload.Definition.Agent.ProfileMode != "subagent" {
		t.Fatalf("definition = %+v", createPayload.Definition)
	}
	if createPayload.TargetDetail == nil || createPayload.TargetDetail.SwarmID != "child-remote" || createPayload.TargetDetail.Kind != "remote" {
		t.Fatalf("target detail = %+v", createPayload.TargetDetail)
	}
	if createPayload.AgentDetail == nil || createPayload.AgentDetail.Name != "memory" || createPayload.AgentDetail.Mode != agentruntime.ModeSubagent {
		t.Fatalf("agent detail = %+v", createPayload.AgentDetail)
	}
	definition, ok, err := flows.GetDefinition("flow-v3-remote")
	if err != nil || !ok {
		t.Fatalf("get definition ok=%v err=%v", ok, err)
	}
	if definition.Assignment.Agent.ProfileName != "memory" || definition.Assignment.Agent.ProfileMode != "subagent" {
		t.Fatalf("stored assignment agent = %+v", definition.Assignment.Agent)
	}
	listRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/v3/flows?limit=200", nil))
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
	server.Handler().ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/v3/flows/flow-v3-remote", nil))
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
			WorkspacePath: t.TempDir(),
		},
		Schedule:      flow.ScheduleSpec{Cadence: flow.CadenceOnDemand},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent:        flow.PromptIntent{Prompt: "Use swarm primary."},
	}
	updateRec := httptest.NewRecorder()
	updateHTTP := httptest.NewRequest(http.MethodPut, "/v3/flows/flow-v3-remote", jsonReader(t, updateReq))
	updateHTTP.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(updateRec, updateHTTP)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", updateRec.Code, updateRec.Body.String())
	}
	var updatePayload flowV3RecordResponse
	if err := json.Unmarshal(updateRec.Body.Bytes(), &updatePayload); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if updatePayload.Definition.Name != "Remote V3 flow updated" || updatePayload.Definition.Enabled {
		t.Fatalf("updated definition = %+v", updatePayload.Definition)
	}
	if updatePayload.Definition.Agent.ProfileName != "swarm" || updatePayload.Definition.Agent.ProfileMode != "primary" {
		t.Fatalf("updated agent = %+v", updatePayload.Definition.Agent)
	}
	updatedDefinition, ok, err := flows.GetDefinition("flow-v3-remote")
	if err != nil || !ok {
		t.Fatalf("get updated definition ok=%v err=%v", ok, err)
	}
	if updatedDefinition.Revision != 2 {
		t.Fatalf("updated revision = %d", updatedDefinition.Revision)
	}
	if updatedDefinition.Assignment.Agent.ProfileName != "swarm" || updatedDefinition.Assignment.Agent.ProfileMode != "primary" {
		t.Fatalf("updated stored agent = %+v", updatedDefinition.Assignment.Agent)
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
			_, _, _, err := server.agents.Upsert(agentruntime.UpsertInput{Name: "disabled-memory", Mode: agentruntime.ModeSubagent, Provider: "test-provider", Model: "test-model", Thinking: "medium", ProviderSet: true, ModelSet: true, ThinkingSet: true, Enabled: &enabled})
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
			server.Handler().ServeHTTP(rec, reqHTTP)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantError) {
				t.Fatalf("body = %s, want substring %q", rec.Body.String(), tc.wantError)
			}
		})
	}
}

type fakeFlowRemoteDeployService struct {
	sessions []remotedeploy.Session
}

func (f *fakeFlowRemoteDeployService) List(context.Context) ([]remotedeploy.Session, error) {
	return append([]remotedeploy.Session(nil), f.sessions...), nil
}

func (f *fakeFlowRemoteDeployService) ListCached(context.Context) ([]remotedeploy.Session, error) {
	return append([]remotedeploy.Session(nil), f.sessions...), nil
}

func (f *fakeFlowRemoteDeployService) Get(context.Context, string, bool) (remotedeploy.Session, error) {
	return remotedeploy.Session{}, nil
}

func (f *fakeFlowRemoteDeployService) Create(context.Context, remotedeploy.CreateSessionInput) (remotedeploy.Session, error) {
	return remotedeploy.Session{}, nil
}

func (f *fakeFlowRemoteDeployService) Delete(context.Context, remotedeploy.DeleteSessionInput) (localcontainers.DeleteResult, error) {
	return localcontainers.DeleteResult{}, nil
}

func (f *fakeFlowRemoteDeployService) Start(context.Context, remotedeploy.StartSessionInput) (remotedeploy.Session, error) {
	return remotedeploy.Session{}, nil
}

func (f *fakeFlowRemoteDeployService) RunUpdateJob(context.Context, remotedeploy.UpdateJobInput) (remotedeploy.UpdateJobResult, error) {
	return remotedeploy.UpdateJobResult{}, nil
}

func (f *fakeFlowRemoteDeployService) Approve(context.Context, remotedeploy.ApproveSessionInput) (remotedeploy.Session, error) {
	return remotedeploy.Session{}, nil
}

func (f *fakeFlowRemoteDeployService) ChildStatus(context.Context, remotedeploy.ChildStatusInput) (remotedeploy.Session, error) {
	return remotedeploy.Session{}, nil
}

func (f *fakeFlowRemoteDeployService) SyncCredentialBundle(context.Context, remotedeploy.SyncCredentialRequestInput) (deployruntime.ContainerSyncCredentialBundle, error) {
	return deployruntime.ContainerSyncCredentialBundle{}, nil
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
			server.Handler().ServeHTTP(rec, reqHTTP)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantError) {
				t.Fatalf("body = %s, want substring %q", rec.Body.String(), tc.wantError)
			}
		})
	}
}
