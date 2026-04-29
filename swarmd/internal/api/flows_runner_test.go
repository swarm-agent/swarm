package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/flow"
	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestTargetLocalFlowRunnerLaunchesSavedAgentProfileWithoutToolScope(t *testing.T) {
	for _, tc := range []struct {
		name           string
		agent          flow.AgentSelection
		expectedKind   string
		expectedName   string
		expectedAgent  string
		expectedPreset string
	}{
		{
			name:           "saved subagent",
			agent:          flow.AgentSelection{TargetKind: runruntime.RunTargetKindSubagent, TargetName: "flow-test"},
			expectedKind:   runruntime.RunTargetKindSubagent,
			expectedName:   "flow-test",
			expectedAgent:  "flow-test",
			expectedPreset: "",
		},
		{
			name:           "memory background alias",
			agent:          flow.AgentSelection{TargetKind: runruntime.RunTargetKindBackground, TargetName: "memory"},
			expectedKind:   runruntime.RunTargetKindBackground,
			expectedName:   "memory",
			expectedAgent:  "memory",
			expectedPreset: "background_commit",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, flows := newFlowPeerTestServer(t)
			ensureFlowTestAgent(t, server)
			if tc.expectedAgent == "memory" {
				ensureFlowMemoryAgentRunnable(t, server)
			}
			runner := &fakeFlowRunService{}
			server.runner = runner
			assignment := testAPIFlowAssignment("flow-runner", 4)
			assignment.Agent = tc.agent
			assignment.Workspace.WorkspacePath = t.TempDir()
			assignment.Workspace.CWD = assignment.Workspace.WorkspacePath
			assignment.Workspace.WorktreeMode = runruntime.RunWorktreeModeOff
			assignment.Intent.Mode = "auto"
			accepted, err := flows.PutAcceptedAssignment(flow.AcceptedAssignment{Assignment: assignment})
			if err != nil {
				t.Fatalf("put accepted assignment: %v", err)
			}

			scheduledAt := time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC)
			start, err := server.NewTargetLocalFlowRunner().RunAcceptedFlow(t.Context(), accepted, flow.RunRequest{FlowID: assignment.FlowID, Revision: assignment.Revision, ScheduledAt: scheduledAt})
			if err != nil {
				t.Fatalf("run accepted flow: %v", err)
			}
			if start.FlowID != assignment.FlowID || start.Revision != assignment.Revision || !start.ScheduledAt.Equal(scheduledAt) {
				t.Fatalf("start identity = %+v", start)
			}
			if strings.TrimSpace(start.SessionID) == "" || strings.TrimSpace(start.RunID) == "" {
				t.Fatalf("start missing ids = %+v", start)
			}
			if runner.lastRequest.TargetKind != tc.expectedKind || runner.lastRequest.TargetName != tc.expectedName || !runner.lastRequest.Background {
				t.Fatalf("run request target/background = %+v", runner.lastRequest)
			}
			if runner.lastRequest.ToolScope != nil {
				t.Fatalf("flow run carried request-time tool scope: %+v", runner.lastRequest.ToolScope)
			}
			if runner.lastRequest.ExecutionContext == nil || runner.lastRequest.ExecutionContext.WorkspacePath != assignment.Workspace.WorkspacePath || runner.lastRequest.ExecutionContext.CWD != assignment.Workspace.CWD {
				t.Fatalf("execution context = %+v", runner.lastRequest.ExecutionContext)
			}
			if runner.lastMeta.OwnerTransport != "flow_scheduler" || runner.lastMeta.RunID == "" {
				t.Fatalf("run meta = %+v", runner.lastMeta)
			}
			session, ok, err := server.sessions.GetSession(start.SessionID)
			if err != nil || !ok {
				t.Fatalf("get session ok=%v err=%v", ok, err)
			}
			if session.Metadata["flow_id"] != assignment.FlowID || session.Metadata["flow_revision"] != float64(assignment.Revision) {
				t.Fatalf("session flow metadata = %+v", session.Metadata)
			}
			profile, err := server.flowRunAgentProfile(assignment.Agent)
			if err != nil {
				t.Fatalf("resolve flow agent profile: %v", err)
			}
			if profile.Name != tc.expectedAgent {
				t.Fatalf("profile name = %q, want %q", profile.Name, tc.expectedAgent)
			}
			preset := ""
			if profile.ToolContract != nil {
				preset = strings.TrimSpace(profile.ToolContract.Preset)
			}
			if preset != tc.expectedPreset {
				t.Fatalf("profile tool preset = %q, want %q", preset, tc.expectedPreset)
			}
		})
	}
}

func TestRunAcceptedFlowNowUsesTargetLocalAcceptedAssignment(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	ensureFlowTestAgent(t, server)
	runner := &fakeFlowRunService{}
	server.runner = runner
	assignment := testAPIFlowAssignment("flow-run-now", 5)
	assignment.Agent = flow.AgentSelection{TargetKind: runruntime.RunTargetKindSubagent, TargetName: "flow-test"}
	assignment.Workspace.WorkspacePath = t.TempDir()
	assignment.Workspace.WorktreeMode = runruntime.RunWorktreeModeOff
	if _, err := flows.PutAcceptedAssignment(flow.AcceptedAssignment{Assignment: assignment}); err != nil {
		t.Fatalf("put accepted assignment: %v", err)
	}

	start, err := server.RunAcceptedFlowNow(t.Context(), assignment.FlowID)
	if err != nil {
		t.Fatalf("run now: %v", err)
	}
	if start.FlowID != assignment.FlowID || start.Revision != assignment.Revision {
		t.Fatalf("start = %+v", start)
	}
	if runner.lastRequest.TargetKind != assignment.Agent.TargetKind || runner.lastRequest.TargetName != assignment.Agent.TargetName {
		t.Fatalf("run request = %+v", runner.lastRequest)
	}
}

func ensureFlowTestAgent(t *testing.T, server *Server) {
	t.Helper()
	if server == nil || server.agents == nil {
		t.Fatal("agent service not configured")
	}
	enabled := true
	_, _, _, err := server.agents.Upsert(agentruntime.UpsertInput{
		Name:                "flow-test",
		Mode:                agentruntime.ModeSubagent,
		Provider:            "test-provider",
		Model:               "test-model",
		Thinking:            "medium",
		ProviderSet:         true,
		ModelSet:            true,
		ThinkingSet:         true,
		ExecutionSetting:    pebblestore.AgentExecutionSettingRead,
		ExitPlanModeEnabled: pebblestore.BoolPtr(false),
		Prompt:              "Flow test agent",
		Enabled:             &enabled,
	})
	if err != nil {
		t.Fatalf("upsert flow-test agent: %v", err)
	}
	profile, err := server.agents.ResolveSubagent("flow-test")
	if err != nil {
		t.Fatalf("resolve flow-test agent: %v", err)
	}
	if profile.ExecutionSetting == "" || profile.Provider == "" || profile.Model == "" || profile.Thinking == "" || pebblestore.AgentExitPlanModeEnabled(profile) {
		t.Fatalf("flow-test profile not runnable: %+v", profile)
	}
}

func ensureFlowMemoryAgentRunnable(t *testing.T, server *Server) {
	t.Helper()
	if server == nil || server.agents == nil {
		t.Fatal("agent service not configured")
	}
	enabled := true
	_, _, _, err := server.agents.Upsert(agentruntime.UpsertInput{
		Name:        "memory",
		Provider:    "test-provider",
		Model:       "test-model",
		Thinking:    "medium",
		ProviderSet: true,
		ModelSet:    true,
		ThinkingSet: true,
		Enabled:     &enabled,
	})
	if err != nil {
		t.Fatalf("upsert memory agent runtime preferences: %v", err)
	}
}

type fakeFlowRunService struct {
	lastSessionID string
	lastRequest   runruntime.RunRequest
	lastMeta      runruntime.RunStartMeta
	err           error
}

func (f *fakeFlowRunService) RunTurn(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, errors.New("RunTurn should not be used by flow runner")
}

func (f *fakeFlowRunService) RunTurnStreaming(_ context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta, _ runruntime.StreamHandler) (runruntime.RunResult, error) {
	f.lastSessionID = sessionID
	f.lastRequest = request
	f.lastMeta = meta
	if f.err != nil {
		return runruntime.RunResult{}, f.err
	}
	return runruntime.RunResult{SessionID: sessionID, Background: request.Background, TargetKind: request.TargetKind, TargetName: request.TargetName}, nil
}

func (f *fakeFlowRunService) StopSessionRun(string, string, string) error {
	return nil
}

func (f *fakeFlowRunService) ExecuteToolForSessionScope(context.Context, string, tool.Call) (string, error) {
	return "", nil
}

func (f *fakeFlowRunService) ListAgentToolDefinitions() []tool.Definition {
	return nil
}

func (f *fakeFlowRunService) ResolveAgentToolContract(pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}
