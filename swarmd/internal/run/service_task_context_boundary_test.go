package run

import (
	"context"
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

func TestRunContinuationBoundaryWaitsForToolPersistence(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "task-context-boundary.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID: "user-1", AccountScopeID: "account-1", Title: "boundary", WorkspacePath: t.TempDir(), WorkspaceName: "workspace",
		Mode: sessionruntime.ModeAuto, Preference: &pebblestore.ModelPreference{Provider: "boundary-fake", Model: "fake-model", Thinking: "off"},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	providers := registry.New()
	runner := &taskBoundaryProviderRunner{}
	providers.RegisterRunner(runner)
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	svc := NewService(sessions, model.NewService(pebblestore.NewModelStore(store), events, nil), providers, tool.NewRuntime(1), nil, agents, nil, events)

	callbackCalls := 0
	profile := agentruntime.CoderAgentProfileForParent(pebblestore.AgentProfile{Provider: "boundary-fake", Model: "fake-model", Thinking: "off"})
	result, err := svc.RunTurnStreaming(context.Background(), session.ID, RunRequest{Prompt: "work", TargetKind: RunTargetKindSubagent, TargetName: profile.Name, AgentName: profile.Name}, RunStartMeta{
		AllowSubagent:       true,
		Principal:           identity.Principal{UserID: "user-1", AccountScopeID: "account-1"},
		TrustedAgentProfile: &profile,
		ContinuationBoundary: func(input RunContinuationBoundaryInput) (RunContinuationBoundaryDecision, error) {
			callbackCalls++
			messages, listErr := sessions.ListMessages(session.ID, 0, 20)
			if listErr != nil {
				t.Fatalf("list messages at boundary: %v", listErr)
			}
			if len(messages) < 3 || messages[len(messages)-1].Role != "tool" || !strings.Contains(messages[len(messages)-1].Content, "tool ok") {
				t.Fatalf("boundary ran before tool persistence: %#v", messages)
			}
			if input.ToolCalls != 1 {
				t.Fatalf("boundary tool calls=%d, want 1", input.ToolCalls)
			}
			return RunContinuationBoundaryDecision{Kind: RunContinuationBoundaryTaskRotation, Reason: "threshold"}, nil
		},
	}, func(StreamEvent) {})
	if !IsTaskRotationBoundary(err) {
		t.Fatalf("run error=%v, want typed Task rotation boundary", err)
	}
	if result.SessionID != "" {
		t.Fatalf("rotation boundary unexpectedly returned completed result: %+v", result)
	}
	if runner.calls != 1 {
		t.Fatalf("provider calls=%d, want no ordinary continuation", runner.calls)
	}
	if callbackCalls != 1 {
		t.Fatalf("boundary calls=%d, want 1", callbackCalls)
	}
	lifecycle, ok, lifecycleErr := svc.GetSessionLifecycle(session.ID)
	if lifecycleErr != nil || !ok {
		t.Fatalf("get lifecycle: ok=%v err=%v", ok, lifecycleErr)
	}
	if strings.EqualFold(lifecycle.Phase, "failed") || strings.TrimSpace(lifecycle.Error) != "" {
		t.Fatalf("typed rotation boundary poisoned lifecycle: %+v", lifecycle)
	}
}

func TestRunWithoutContinuationBoundaryRetainsOrdinaryContinuation(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "ordinary-continuation.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID: "user-1", AccountScopeID: "account-1", Title: "ordinary", WorkspacePath: t.TempDir(), WorkspaceName: "workspace",
		Mode: sessionruntime.ModeAuto, Preference: &pebblestore.ModelPreference{Provider: "boundary-fake", Model: "fake-model", Thinking: "off"},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	providers := registry.New()
	runner := &ordinaryContinuationProviderRunner{}
	providers.RegisterRunner(runner)
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	svc := NewService(sessions, model.NewService(pebblestore.NewModelStore(store), events, nil), providers, tool.NewRuntime(1), nil, agents, nil, events)
	profile := agentruntime.CoderAgentProfileForParent(pebblestore.AgentProfile{Provider: "boundary-fake", Model: "fake-model", Thinking: "off"})
	result, err := svc.RunTurnStreaming(context.Background(), session.ID, RunRequest{Prompt: "work", TargetKind: RunTargetKindSubagent, TargetName: profile.Name, AgentName: profile.Name}, RunStartMeta{
		AllowSubagent: true, Principal: identity.Principal{UserID: "user-1", AccountScopeID: "account-1"}, TrustedAgentProfile: &profile,
	}, func(StreamEvent) {})
	if err != nil {
		t.Fatalf("ordinary run: %v", err)
	}
	if runner.calls != 2 || result.AssistantMessage.Content != "done" {
		t.Fatalf("ordinary continuation calls=%d result=%+v", runner.calls, result)
	}
}

type taskBoundaryProviderRunner struct{ calls int }

func (r *taskBoundaryProviderRunner) ID() string { return "boundary-fake" }
func (r *taskBoundaryProviderRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *taskBoundaryProviderRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.calls++
	return provideriface.Response{
		Text:          "using tool",
		FunctionCalls: []provideriface.FunctionCall{{CallID: "call-1", Name: "bash", Arguments: `{"command":"printf tool ok"}`}},
	}, nil
}

type ordinaryContinuationProviderRunner struct{ calls int }

func (r *ordinaryContinuationProviderRunner) ID() string { return "boundary-fake" }
func (r *ordinaryContinuationProviderRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *ordinaryContinuationProviderRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.calls++
	if r.calls == 1 {
		return provideriface.Response{Text: "using tool", FunctionCalls: []provideriface.FunctionCall{{CallID: "call-ordinary", Name: "bash", Arguments: `{"command":"printf tool ok"}`}}}, nil
	}
	if r.calls > 2 {
		return provideriface.Response{}, context.DeadlineExceeded
	}
	return provideriface.Response{Text: "done"}, nil
}
