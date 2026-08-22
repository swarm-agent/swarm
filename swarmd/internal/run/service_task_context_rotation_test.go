package run

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func TestDelegatedLogicalLaunchCompactsAndContinuesSameSession(t *testing.T) {
	svc, launch, runner := newTaskRotationHarness(t)
	launch.TaskBase = &worktreeruntime.TaskBase{RepoRoot: "/repo", ParentBranch: "dev-parent", BaseCommit: "base-commit-123"}
	launch.ChildWorktreeBase = "dev-parent"
	launch.ChildWorktreeBranch = "agent/child-context"
	svc.SetWorktreeService(&taskRotationWorktreeStub{
		taskLaunchWorktreeStub: &taskLaunchWorktreeStub{},
		state: worktreeruntime.TaskWorkspaceState{
			WorkspacePath: launch.ChildWorkspacePath,
			BranchName:    launch.ChildWorktreeBranch,
			HeadCommit:    "child-head-456",
			Clean:         false,
			Status:        " M swarmd/internal/run/service.go\n?? swarmd/internal/run/compact_regression_test.go",
		},
	})
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
	parentPath := filepath.Join(filepath.Dir(launch.ChildWorkspacePath), "parent-checkout")
	alternatePath := filepath.Join(filepath.Dir(launch.ChildWorkspacePath), "alternate-recovery")
	original := "Implement the immutable delegated assignment. Historical parent=" + parentPath + " alternate=" + alternatePath
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
	messages, err := svc.sessions.ListSessionMessages(launch.ChildSession.ID, 0, 100)
	if err != nil {
		t.Fatalf("list compacted V3 child messages: %v", err)
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
	if len(runner.requests) != 3 {
		t.Fatalf("provider requests = %d, want 3", len(runner.requests))
	}
	compactPrompt := providerRequestInputText(runner.requests[1])
	for _, path := range []string{parentPath, launch.ChildWorkspacePath, alternatePath} {
		if !strings.Contains(compactPrompt, path) {
			t.Fatalf("three-path Compact provider request lost %q:\n%s", path, compactPrompt)
		}
	}
	if !strings.Contains(compactPrompt, "parent or alternate paths in transcript evidence are non-authoritative historical context") {
		t.Fatalf("three-path Compact provider request lost authority rule:\n%s", compactPrompt)
	}
	for _, want := range []string{
		"Trusted Task execution context (authoritative",
		"Canonical child execution root (authoritative)",
		"- canonical workspace: " + launch.ChildWorkspacePath,
		"- allocated child branch: " + launch.ChildWorktreeBranch,
		"- parent/base branch: " + launch.ChildWorktreeBase,
		"- immutable base commit: " + launch.TaskBase.BaseCommit,
		"- branch: " + launch.ChildWorktreeBranch,
		"- HEAD: child-head-456",
		"- clean: false",
		"M swarmd/internal/run/service.go",
		"?? swarmd/internal/run/compact_regression_test.go",
		"Selected transcript evidence for compaction",
	} {
		if !strings.Contains(compactPrompt, want) {
			t.Fatalf("Compact provider request missing %q:\n%s", want, compactPrompt)
		}
	}
	trustedIndex := strings.Index(compactPrompt, "Trusted Task execution context")
	transcriptIndex := strings.Index(compactPrompt, "Selected transcript evidence")
	if trustedIndex < 0 || transcriptIndex < 0 || trustedIndex >= transcriptIndex {
		t.Fatalf("trusted Task packet must precede transcript evidence:\n%s", compactPrompt)
	}
	if strings.Contains(compactPrompt, "immutable base commit: "+launch.ChildWorktreeBase) {
		t.Fatalf("Compact provider request mislabeled base branch as commit:\n%s", compactPrompt)
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

type taskRotationWorktreeStub struct {
	*taskLaunchWorktreeStub
	state worktreeruntime.TaskWorkspaceState
}

func (s *taskRotationWorktreeStub) InspectTaskWorkspace(path string) (worktreeruntime.TaskWorkspaceState, error) {
	state := s.state
	state.WorkspacePath = path
	return state, nil
}

type taskRotationHarnessRunner struct {
	responses []provideriface.Response
	requests  []provideriface.Request
	calls     int
}

func (r *taskRotationHarnessRunner) ID() string { return "rotation-fake" }
func (r *taskRotationHarnessRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *taskRotationHarnessRunner) CreateResponseStreaming(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.requests = append(r.requests, req)
	if r.calls >= len(r.responses) {
		return provideriface.Response{}, errors.New("unexpected provider continuation")
	}
	response := r.responses[r.calls]
	r.calls++
	return response, nil
}

func providerRequestInputText(req provideriface.Request) string {
	var parts []string
	for _, item := range req.Input {
		content, _ := item["content"].([]map[string]any)
		for _, part := range content {
			if text, _ := part["text"].(string); strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
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
	catalogStore := pebblestore.NewModelCatalogStore(store)
	catalog := model.NewCatalogService(catalogStore)
	models := model.NewService(pebblestore.NewModelStore(store), events, catalog)
	if err := models.EnsureBootDefaults(); err != nil {
		t.Fatalf("ensure model defaults: %v", err)
	}
	if err := catalogStore.SetRecord(pebblestore.ModelCatalogRecord{Provider: "rotation-fake", Model: "fake-model", ContextWindow: 200000, MaxOutputTokens: 32000}); err != nil {
		t.Fatalf("configure fake model catalog: %v", err)
	}
	svc := NewService(sessions, models, providers, tool.NewRuntime(1), nil, agents, nil, events)
	settingsStore := pebblestore.NewAgentModelSettingsStore(store)
	configured := pebblestore.AgentModelAssignment{Provider: "rotation-fake", Model: "fake-model", Thinking: "off"}
	if _, err := settingsStore.PutForAccount(pebblestore.AgentModelSettingsRecord{
		AccountScopeID: "account-1",
		Swarm:          pebblestore.SwarmAgentModelAssignments{Action: configured, Plan: configured},
		SystemAgents: pebblestore.SystemAgentModelAssignments{
			Compact: configured, Finder: configured, Coder: configured, Designer: configured, Router: configured,
		},
	}); err != nil {
		t.Fatalf("configure system-agent models: %v", err)
	}
	svc.SetAgentModelSettingsService(agentmodelsettings.NewService(settingsStore))
	profile := agentruntime.FinderAgentProfileForParent(pebblestore.AgentProfile{Provider: "rotation-fake", Model: "fake-model", Thinking: "off"})
	launch, err := svc.prepareDelegatedSubagentLaunchWithProfile(parent, sessionruntime.ModeAuto, taskLaunchPrepared{LaunchIndex: 1, RequestedSubagent: "finder", MetaPrompt: "immutable assignment", LogicalTaskID: "logical-rotation-1", TaskCallID: "task-call-1", ParentRunID: "parent-run-1", PermissionSessionID: parent.ID, ReservationSessionID: parent.ID}, "rotation", "", &profile, "finder", nil)
	if err != nil {
		t.Fatalf("prepare child: %v", err)
	}
	return svc, launch, runner
}
