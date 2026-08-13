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

func TestParseDelegatedChildTargetedHandoffRequiresObjectiveAndActions(t *testing.T) {
	handoff, err := parseDelegatedChildTargetedHandoff(`{"objective":"finish rotation","completed":["watcher added"],"decisions":["use canonical usage"],"next_actions":["run focused tests"],"constraints":["no transcript replay"],"relevant_files":["swarmd/internal/run/service.go"],"validation":["watcher tests passed"]}`)
	if err != nil {
		t.Fatalf("parse handoff: %v", err)
	}
	if handoff.Objective != "finish rotation" || len(handoff.NextActions) != 1 || handoff.NextActions[0] != "run focused tests" {
		t.Fatalf("handoff = %+v", handoff)
	}
	for _, raw := range []string{
		`{"objective":"","next_actions":["continue"]}`,
		`{"objective":"continue","next_actions":[]}`,
		`{"objective":"continue","next_actions":["act"],"transcript":"forbidden"}`,
		"```json\n{\"objective\":\"continue\",\"next_actions\":[\"act\"]}\n```",
	} {
		if _, err := parseDelegatedChildTargetedHandoff(raw); err == nil {
			t.Fatalf("invalid handoff accepted: %s", raw)
		}
	}
}

func TestDelegatedLogicalLaunchRotatesToValidatedSuccessor(t *testing.T) {
	svc, launch, runner := newTaskRotationHarness(t)
	runner.responses = []provideriface.Response{
		{Text: "", FunctionCalls: []provideriface.FunctionCall{{CallID: "call-1", Name: "bash", Arguments: `{"command":"printf tool-ok"}`}}},
		{Text: `{"objective":"finish the delegated job","completed":["persisted tool work"],"decisions":["keep the current workspace"],"next_actions":["finish the implementation"],"constraints":["do not replay transcript"],"relevant_files":["swarmd/internal/run/service.go"],"validation":["tool persisted"]}`},
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
	if result.AssistantMessage.Content != "logical job complete" || runner.calls != 3 || resultLaunch.ChildSession.ID == launch.ChildSession.ID {
		t.Fatalf("logical result calls=%d launch=%s result=%+v", runner.calls, resultLaunch.ChildSession.ID, result)
	}
	stored, ok, err := svc.sessions.GetDelegatedChildHandoff("account-1", launch.LogicalTaskID, 1)
	if err != nil || !ok || stored.Objective != "finish the delegated job" || stored.SuccessorSessionID != resultLaunch.ChildSession.ID {
		t.Fatalf("durable handoff: ok=%t err=%v %+v", ok, err, stored)
	}
}

func TestDelegatedLogicalLaunchHandoffFailureCreatesNoSuccessor(t *testing.T) {
	svc, launch, runner := newTaskRotationHarness(t)
	runner.responses = []provideriface.Response{
		{Text: "", FunctionCalls: []provideriface.FunctionCall{{CallID: "call-1", Name: "bash", Arguments: `{"command":"printf tool-ok"}`}}},
		{Text: `{"objective":"","next_actions":[]}`},
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
	if !IsRecoverableTaskHandoffError(err) {
		t.Fatalf("handoff failure = %v", err)
	}
	lineage, ok, getErr := svc.sessions.GetDelegatedChildLineage("account-1", launch.LogicalTaskID)
	if getErr != nil || !ok || lineage.CurrentGeneration != 1 || lineage.CurrentSessionID != launch.ChildSession.ID {
		t.Fatalf("lineage changed after handoff failure: ok=%t err=%v %+v", ok, getErr, lineage)
	}
	if _, ok, getErr := svc.sessions.GetDelegatedChildGeneration("account-1", launch.LogicalTaskID, 2); getErr != nil || ok {
		t.Fatalf("successor exists after handoff failure: ok=%t err=%v", ok, getErr)
	}
}

func TestRotateDelegatedChildPersistsValidatedHandoffAndFencesPredecessor(t *testing.T) {
	svc, launch, _ := newTaskRotationHarness(t)
	handoff := pebblestore.DelegatedChildTargetedHandoff{Objective: "finish the job", NextActions: []string{"continue in successor"}, Completed: []string{"watcher complete"}}
	rotatedLaunch, err := svc.rotateDelegatedChild(launch, "immutable assignment", handoff, nil)
	if err != nil {
		t.Fatalf("rotate child: %v", err)
	}
	if rotatedLaunch.ChildSession.ID == launch.ChildSession.ID {
		t.Fatal("rotation retained predecessor session")
	}
	lineage, ok, err := svc.sessions.GetDelegatedChildLineage("account-1", launch.LogicalTaskID)
	if err != nil || !ok || lineage.CurrentGeneration != 2 || lineage.CurrentSessionID != rotatedLaunch.ChildSession.ID {
		t.Fatalf("lineage after rotation: ok=%t err=%v %+v", ok, err, lineage)
	}
	predecessor, ok, err := svc.sessions.GetDelegatedChildGeneration("account-1", launch.LogicalTaskID, 1)
	if err != nil || !ok || predecessor.State != pebblestore.DelegatedChildGenerationRetired || predecessor.SuccessorSessionID != rotatedLaunch.ChildSession.ID {
		t.Fatalf("predecessor after rotation: ok=%t err=%v %+v", ok, err, predecessor)
	}
	stored, ok, err := svc.sessions.GetDelegatedChildHandoff("account-1", launch.LogicalTaskID, 1)
	if err != nil || !ok || stored.Objective != handoff.Objective || len(stored.NextActions) != 1 {
		t.Fatalf("stored handoff: ok=%t err=%v %+v", ok, err, stored)
	}
	if _, _, err := svc.runDelegatedLogicalLaunch(context.Background(), launch, "stale write", launch.ChildSession.ID, identity.Principal{UserID: "user-1", AccountScopeID: "account-1"}, nil, func(StreamEvent) {}); err == nil || !strings.Contains(err.Error(), "stale delegated child") {
		t.Fatalf("stale predecessor outcome = %v", err)
	}
}

func TestRecoverableTaskHandoffErrorIsTyped(t *testing.T) {
	cause := errors.New("invalid JSON")
	err := &RecoverableTaskHandoffError{Err: cause}
	if !IsRecoverableTaskHandoffError(err) || !errors.Is(err, cause) || !strings.Contains(err.Error(), "no successor started") {
		t.Fatalf("typed error = %v", err)
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
