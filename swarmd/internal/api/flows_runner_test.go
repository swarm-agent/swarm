package api

import (
	"context"
	"errors"
	"strings"
	"sync"
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
			start, err := server.runAcceptedFlow(t.Context(), accepted, flow.RunRequest{FlowID: assignment.FlowID, Revision: assignment.Revision, ScheduledAt: scheduledAt})
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
			if session.Title != "Memory sweep" {
				t.Fatalf("session title = %q, want flow name", session.Title)
			}
			createdPayload := requireSessionCreatedPayload(t, server, start.SessionID)
			if createdPayload.ID != start.SessionID || createdPayload.Title != "Memory sweep" {
				t.Fatalf("created payload = %+v, want flow name", createdPayload)
			}
			if session.Metadata["flow_id"] != assignment.FlowID || session.Metadata["flow_revision"] != float64(assignment.Revision) {
				t.Fatalf("session flow metadata = %+v", session.Metadata)
			}
			if session.Metadata["title_pending"] != false || session.Metadata["title_locked"] != true || session.Metadata["title_source"] != flowSessionTitleSourceTask || session.Metadata["source"] != "flow" {
				t.Fatalf("session title metadata = %+v", session.Metadata)
			}
			if session.Metadata["workspace_id"] == nil || session.Metadata["agent_name"] != "swarm" {
				t.Fatalf("session create metadata was not preserved: %+v", session.Metadata)
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

func TestFlowRunSessionTitlePrefersFlowNameThenTaskTitleDetailThenPrompt(t *testing.T) {
	assignment := testAPIFlowAssignment("flow-title", 1)
	assignment.Name = "Nightly Memory Sweep"
	assignment.Intent = flow.PromptIntent{Prompt: "Summarize outstanding work.", Tasks: []flow.TaskStep{{ID: "context", Title: "Prepare run context", Detail: "Target local.", Action: "read"}, {ID: "task", Title: "  Run agent task  ", Detail: "Refresh AGENTS memory", Action: "write"}}}
	if title := flowRunSessionTitle(assignment); title != "Nightly Memory Sweep" {
		t.Fatalf("flow name title = %q", title)
	}

	assignment.Name = ""
	if title := flowRunSessionTitle(assignment); title != "Refresh AGENTS memory" {
		t.Fatalf("title = %q, want task detail", title)
	}

	assignment.Intent.Tasks = []flow.TaskStep{{ID: "task", Title: "  Refresh AGENTS memory  ", Detail: "Detailed fallback", Action: "write"}}
	if title := flowRunSessionTitle(assignment); title != "Refresh AGENTS memory" {
		t.Fatalf("task title = %q", title)
	}

	assignment.Intent.Tasks[0].Title = ""
	if title := flowRunSessionTitle(assignment); title != "Detailed fallback" {
		t.Fatalf("detail title = %q", title)
	}

	assignment.Intent.Tasks = nil
	if title := flowRunSessionTitle(assignment); title != "Summarize outstanding work." {
		t.Fatalf("prompt title = %q", title)
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
	session, ok, err := server.sessions.GetSession(start.SessionID)
	if err != nil || !ok {
		t.Fatalf("get run-now session ok=%v err=%v", ok, err)
	}
	if session.Title != "Memory sweep" {
		t.Fatalf("run-now session title = %q, want flow name", session.Title)
	}
	if session.Metadata["title_pending"] != false || session.Metadata["title_locked"] != true || session.Metadata["title_source"] != flowSessionTitleSourceTask || session.Metadata["source"] != "flow" {
		t.Fatalf("run-now title metadata = %+v", session.Metadata)
	}
	if runner.lastSessionID != start.SessionID {
		t.Fatalf("runner session id = %q, want %q", runner.lastSessionID, start.SessionID)
	}
	createdPayload := requireSessionCreatedPayload(t, server, start.SessionID)
	if createdPayload.ID != start.SessionID || createdPayload.Title != "Memory sweep" {
		t.Fatalf("run-now created payload = %+v, want flow name", createdPayload)
	}
}

func TestFlowRunNowCommandReplaysUseOriginalCommandCreatedAt(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	ensureFlowTestAgent(t, server)
	started := make(chan struct{})
	unblock := make(chan struct{})
	runner := &fakeFlowRunService{started: started, block: unblock}
	server.runner = runner
	assignment := testAPIFlowAssignment("flow-run-now-replay", 7)
	assignment.Agent = flow.AgentSelection{TargetKind: runruntime.RunTargetKindSubagent, TargetName: "flow-test"}
	assignment.Workspace.WorkspacePath = t.TempDir()
	assignment.Workspace.WorktreeMode = runruntime.RunWorktreeModeOff
	accepted, err := flows.PutAcceptedAssignment(flow.AcceptedAssignment{Assignment: assignment})
	if err != nil {
		t.Fatalf("put accepted assignment: %v", err)
	}

	commandCreatedAt := time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC)
	command := testAPIFlowCommand("cmd-run-now-replay", accepted.Assignment, flow.CommandRunNow)
	command.CreatedAt = commandCreatedAt

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		if _, _, err := server.applyFlowRunNowCommand(t.Context(), command, commandCreatedAt.Add(5*time.Second)); err != nil {
			t.Errorf("first run-now command: %v", err)
		}
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first run-now command did not start")
	}

	secondAck, secondInserted, err := server.applyFlowRunNowCommand(t.Context(), command, commandCreatedAt.Add(12*time.Second))
	if err != nil {
		t.Fatalf("second run-now command: %v", err)
	}
	if !secondInserted || secondAck.Status != flow.AssignmentAccepted {
		t.Fatalf("second ack inserted=%v ack=%+v", secondInserted, secondAck)
	}
	close(unblock)
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first run-now command did not finish")
	}

	if got := runner.callCount(); got != 1 {
		t.Fatalf("runner call count = %d, want 1", got)
	}
	runs, err := flows.ListTargetRuns(assignment.FlowID, 10)
	if err != nil {
		t.Fatalf("list target runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("target run count = %d, want 1: %+v", len(runs), runs)
	}
	if !runs[0].ScheduledAt.Equal(commandCreatedAt) {
		t.Fatalf("scheduled_at = %s, want %s", runs[0].ScheduledAt, commandCreatedAt)
	}
	if !strings.Contains(runs[0].RunID, sanitizeFlowRunIDPart(command.CommandID)) {
		t.Fatalf("run id %q does not include command id %q", runs[0].RunID, command.CommandID)
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
	mu             sync.Mutex
	lastSessionID  string
	lastRequest    runruntime.RunRequest
	lastMeta       runruntime.RunStartMeta
	emitEvents     []runruntime.StreamEvent
	receivedEvents []runruntime.StreamEvent
	err            error
	started        chan struct{}
	block          chan struct{}
	calls          int
}

func (f *fakeFlowRunService) RunTurn(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, errors.New("RunTurn should not be used by flow runner")
}

func (f *fakeFlowRunService) RunTurnStreaming(_ context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta, onEvent runruntime.StreamHandler) (runruntime.RunResult, error) {
	f.mu.Lock()
	f.calls++
	f.lastSessionID = sessionID
	f.lastRequest = request
	f.lastMeta = meta
	started := f.started
	block := f.block
	emitEvents := append([]runruntime.StreamEvent(nil), f.emitEvents...)
	err := f.err
	f.mu.Unlock()
	if started != nil {
		close(started)
	}
	if block != nil {
		<-block
	}
	for _, event := range emitEvents {
		if strings.TrimSpace(event.SessionID) == "" {
			event.SessionID = sessionID
		}
		if strings.TrimSpace(event.RunID) == "" {
			event.RunID = meta.RunID
		}
		if onEvent != nil {
			onEvent(event)
		}
		f.mu.Lock()
		f.receivedEvents = append(f.receivedEvents, event)
		f.mu.Unlock()
	}
	if err != nil {
		return runruntime.RunResult{}, err
	}
	return runruntime.RunResult{SessionID: sessionID, Background: request.Background, TargetKind: request.TargetKind, TargetName: request.TargetName}, nil
}

func (f *fakeFlowRunService) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
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
