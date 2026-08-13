package run

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestDelegatedLogicalLaunchCompactsAndContinuesSameSession(t *testing.T) {
	svc, launch, runner := newTaskRotationHarness(t)
	runner.responses = []provideriface.Response{
		{Text: "", FunctionCalls: []provideriface.FunctionCall{{CallID: "call-1", Name: "bash", Arguments: `{"command":"printf tool-ok"}`}}},
		{Text: "Original/active goal: implement the immutable delegated assignment.\n\nWhat changed since any prior compact checkpoint: persisted tool work.\n\nImmediate next action: finish the implementation."},
		{Text: "logical job complete"},
	}
	first := true
	launch.ContinuationBoundary = func(RunContinuationBoundaryInput) (RunContinuationBoundaryDecision, error) {
		if first {
			first = false
			return RunContinuationBoundaryDecision{Kind: RunContinuationBoundaryTaskRotation, Reason: "threshold"}, nil
		}
		return RunContinuationBoundaryDecision{}, nil
	}
	original := "Implement the immutable delegated assignment"
	resultLaunch, result, err := svc.runDelegatedLogicalLaunch(context.Background(), launch, original, launch.ChildSession.ID, identity.Principal{UserID: "user-1", AccountScopeID: "account-1"}, nil, func(StreamEvent) {})
	if err != nil {
		t.Fatalf("run logical launch: %v", err)
	}
	if result.AssistantMessage.Content != "logical job complete" || runner.calls != 3 || resultLaunch.ChildSession.ID != launch.ChildSession.ID {
		t.Fatalf("same-session result calls=%d launch=%s result=%+v", runner.calls, resultLaunch.ChildSession.ID, result)
	}
	lineage, ok, err := svc.sessions.GetDelegatedChildLineage("account-1", launch.LogicalTaskID)
	if err != nil || !ok || lineage.CurrentGeneration != 1 || lineage.CurrentSessionID != launch.ChildSession.ID {
		t.Fatalf("same-session lineage: ok=%t err=%v %+v", ok, err, lineage)
	}
	messages, err := svc.sessions.ListMessages(launch.ChildSession.ID, 0, 100)
	if err != nil {
		t.Fatalf("list compacted child messages: %v", err)
	}
	foundCheckpoint := false
	for _, message := range messages {
		if strings.EqualFold(message.Role, "system") && strings.Contains(message.Content, "origin=task") {
			foundCheckpoint = true
		}
	}
	if !foundCheckpoint {
		t.Fatalf("Task compact checkpoint missing from child transcript: %+v", messages)
	}
	if _, ok, err := svc.sessions.GetDelegatedChildGeneration("account-1", launch.LogicalTaskID, 2); err != nil || ok {
		t.Fatalf("successor generation created by Task compact: ok=%t err=%v", ok, err)
	}
}

func TestDelegatedLogicalLaunchCompactFailureCreatesNoSuccessor(t *testing.T) {
	svc, launch, runner := newTaskRotationHarness(t)
	runner.responses = []provideriface.Response{
		{Text: "", FunctionCalls: []provideriface.FunctionCall{{CallID: "call-1", Name: "bash", Arguments: `{"command":"printf tool-ok"}`}}},
		{Text: ""},
	}
	first := true
	launch.ContinuationBoundary = func(RunContinuationBoundaryInput) (RunContinuationBoundaryDecision, error) {
		if first {
			first = false
			return RunContinuationBoundaryDecision{Kind: RunContinuationBoundaryTaskRotation}, nil
		}
		return RunContinuationBoundaryDecision{}, nil
	}
	_, _, err := svc.runDelegatedLogicalLaunch(context.Background(), launch, "immutable assignment", launch.ChildSession.ID, identity.Principal{UserID: "user-1", AccountScopeID: "account-1"}, nil, func(StreamEvent) {})
	if err == nil || !strings.Contains(err.Error(), "Task same-session compact failed") {
		t.Fatalf("compact failure = %v", err)
	}
	lineage, ok, getErr := svc.sessions.GetDelegatedChildLineage("account-1", launch.LogicalTaskID)
	if getErr != nil || !ok || lineage.CurrentGeneration != 1 || lineage.CurrentSessionID != launch.ChildSession.ID {
		t.Fatalf("lineage changed after compact failure: ok=%t err=%v %+v", ok, getErr, lineage)
	}
	if _, ok, getErr := svc.sessions.GetDelegatedChildGeneration("account-1", launch.LogicalTaskID, 2); getErr != nil || ok {
		t.Fatalf("successor exists after compact failure: ok=%t err=%v", ok, getErr)
	}
}

type taskRotationHarnessRunner struct {
	responses []provideriface.Response
	calls     int
}

func (r *taskRotationHarnessRunner) ID() string { return "rotation-fake" }
func (r *taskRotationHarnessRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *taskRotationHarnessRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	if r.calls >= len(r.responses) {
		return provideriface.Response{}, errors.New("unexpected provider continuation")
	}
	response := r.responses[r.calls]
	r.calls++
	return response, nil
}

func newTaskRotationHarness(t *testing.T) (*Service, taskLaunchPrepared, *taskRotationHarnessRunner) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "task-rotation.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	parent, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{UserID: "user-1", AccountScopeID: "account-1", Title: "parent", WorkspacePath: t.TempDir(), WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto, Preference: &pebblestore.ModelPreference{Provider: "rotation-fake", Model: "fake-model", Thinking: "off"}})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	runner := &taskRotationHarnessRunner{}
	providers := registry.New()
	providers.RegisterRunner(runner)
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	svc := NewService(sessions, model.NewService(pebblestore.NewModelStore(store), events, nil), providers, tool.NewRuntime(1), nil, agents, nil, events)
	profile := agentruntime.FinderAgentProfileForParent(pebblestore.AgentProfile{Provider: "rotation-fake", Model: "fake-model", Thinking: "off"})
	launch, err := svc.prepareDelegatedSubagentLaunchWithProfile(parent, sessionruntime.ModeAuto, taskLaunchPrepared{LaunchIndex: 1, RequestedSubagent: "finder", MetaPrompt: "immutable assignment", LogicalTaskID: "logical-rotation-1", TaskCallID: "task-call-1", ParentRunID: "parent-run-1", PermissionSessionID: parent.ID, ReservationSessionID: parent.ID}, "rotation", "", &profile, "finder", nil)
	if err != nil {
		t.Fatalf("prepare child: %v", err)
	}
	return svc, launch, runner
}
