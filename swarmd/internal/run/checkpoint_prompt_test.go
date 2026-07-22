package run

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestBuildPlanCheckpointRunInputUsesOnlyPlanContextWithoutStartLifecycleMessage(t *testing.T) {
	svc, sessionID, cleanup := newCheckpointRunPromptTestService(t)
	defer cleanup()
	var appliedMutations []sessionruntime.SessionMutationInput
	applyMutation := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		appliedMutations = append(appliedMutations, input)
		return svc.sessions.ApplySessionMutation(input)
	}
	if _, _, err := svc.sessions.SavePlanWithMetadata(sessionID, "plan-cp", "Plan CP", "# ignored display", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ID:    "plan-cp",
		Title: "Plan CP",
		Info: pebblestore.SessionPlanInfo{
			Goal:               "Stale original plan goal that must not govern cp-2",
			Scope:              "Backend only",
			Decisions:          []string{"clear history"},
			RelevantFiles:      []string{"swarmd/internal/run/service.go"},
			ValidationStrategy: "targeted tests",
		},
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Artifacts:       []pebblestore.SessionPlanArtifactReference{{Path: "docs/plan-brief.md", Role: "input", Description: "plan context"}},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Done", Status: sessionruntime.PlanCheckpointStatusCompleted, Artifacts: []pebblestore.SessionPlanArtifactReference{{Path: "out/uncited-prior.json", Role: "deliverable"}, {Path: "out/shared-result.json", Role: "deliverable"}}},
			{ID: "cp-2", Title: "Fresh handoff", Status: sessionruntime.PlanCheckpointStatusPending, Objective: "Use plan context only", Tasks: []string{"Build prompt"}, AcceptanceCriteria: []string{"No old chat"}, Artifacts: []pebblestore.SessionPlanArtifactReference{{Path: "out/shared-result.json", Role: "input", Description: "consume the cited prior checkpoint result", MediaType: "application/json"}, {Path: "out/user-summary.md", Role: "deliverable", Description: "user-visible deliverable", MediaType: "text/markdown"}}},
		},
		ActiveCheckpointID: "cp-2",
	}}); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if _, _, _, err := svc.appendRunMessage(runAppendMessageInput{SessionID: sessionID, Role: "user", Content: "old chat that must not appear", LogicalKey: "old-chat"}); err != nil {
		t.Fatalf("append old chat: %v", err)
	}

	input, ok, err := svc.buildPlanCheckpointRunInput(sessionID, "run-cp", RunOptions{PlanCheckpointContext: &RunPlanCheckpointContext{PlanID: "plan-cp", CheckpointID: "cp-2", ParentSessionID: "parent-session"}, ApplySessionMutation: applyMutation})
	if err != nil {
		t.Fatalf("build checkpoint input: %v", err)
	}
	if !ok || len(input) != 1 {
		t.Fatalf("checkpoint input = %#v, ok=%v", input, ok)
	}
	text := inputTextFromProviderInput(t, input[0])
	if !strings.Contains(text, "Conversation history has been intentionally cleared") {
		t.Fatalf("prompt missing cleared-history instruction: %s", text)
	}
	if !strings.Contains(text, "Execute exactly one checkpoint: cp-2") {
		t.Fatalf("prompt missing one-checkpoint assignment: %s", text)
	}
	if !strings.Contains(text, "checkpoint.objective is the sole current objective") {
		t.Fatalf("prompt missing canonical objective instruction: %s", text)
	}
	if strings.Contains(text, "Stale original plan goal that must not govern cp-2") || strings.Contains(text, `"goal"`) {
		t.Fatalf("prompt injected the plan goal as a competing current objective: %s", text)
	}
	if !strings.Contains(text, `"objective": "Use plan context only"`) {
		t.Fatalf("prompt missing selected checkpoint objective: %s", text)
	}
	for _, want := range []string{"docs/plan-brief.md", "out/shared-result.json", "consume the cited prior checkpoint result", "out/user-summary.md", "workspace-relative metadata, not embedded file contents", "Read only artifacts with role=input", "role=deliverable", "assistant response itself", "terminal report is internal execution evidence"} {
		if !strings.Contains(text, want) {
			t.Fatalf("prompt missing artifact contract %q: %s", want, text)
		}
	}
	if strings.Contains(text, "out/uncited-prior.json") {
		t.Fatalf("prompt included an uncited prior-checkpoint artifact: %s", text)
	}
	if !strings.Contains(text, "a final complete_subtask call with complete_checkpoint=true, mark_needs_review, mark_blocked, or mark_failed") {
		t.Fatalf("prompt missing terminal outcome instruction: %s", text)
	}
	for _, want := range []string{
		"Use plan_manage as the only checkpoint lifecycle surface",
		"Do not use manage_todos for agent self-tracking",
		"Do not call plan_manage update_checkpoint",
		"pass subtask_ids to atomically record every task completed since the last progress call",
		"transition advances the next task and makes live client state visible",
		"Do not call complete_subtask for discovery-only activity or for a single-step checkpoint",
		"Do not call start_session_checkpoint or request_followup_checkpoint from this checkpoint run",
		"backend rejects session-checkpoint creation owned by an active provider-managed checkpoint run",
		"record the proposed title, tasks, acceptance criteria, notes, and artifact inputs in the terminal result/next-action evidence",
		"a later parent-conversation turn must append it with request_followup_checkpoint",
		"never claim a checkpoint was added unless the plan_manage result succeeded and returned the new checkpoint",
		"original request already requires multiple AIs, fresh-context stages, or ordered checkpoints",
		"Always include the current checkpoint_id from the payload",
		"setting complete_checkpoint=true on the final complete_subtask call",
		"use it to avoid a redundant second tool call",
		"If the checkpoint is not done, record only the completed subset and keep working",
		"The backend alone decides whether completion continues to a next checkpoint",
		"Keep implementing until every acceptance criterion is met whenever the remaining gap is resolvable",
		"a missing interface or API, scope growth, uncertainty, or an incomplete/failed first approach is implementation work",
		"Use mark_needs_review only when user or audit judgment is inherently required",
		"Use mark_blocked only for a named external dependency, required input, or unavailable permission",
		"Use mark_failed only for a nonrecoverable execution error after reasonable recovery attempts",
		"backend durable plan state decides continuation",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("prompt missing %q instruction: %s", want, text)
		}
	}
	for _, want := range []string{
		"Final checkpoint handoff required",
		"last remaining checkpoint",
		"final waiting_review/final-review state",
		"keep report substantive and lossless",
		"ensure the assistant response actually contains or links every requested user-visible artifact",
		"terminal report metadata is internal evidence and is not the user-facing deliverable",
		"handoff_overview is required and concise",
		"handoff_title is optional",
		"impact_bullets contains at most three",
		"suggested_prompts contains at most three inert label/prompt objects",
		"ordinary future user chat messages only",
		"never be tool calls, shell commands, Git operations, or lifecycle mutations",
		"single canonical recommendation",
		"Do not put handoff content inside XML-like tags",
		"joins report, result, changed_files, and validation as lossless details",
		`"final_checkpoint": true`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("final checkpoint prompt missing %q: %s", want, text)
		}
	}
	for _, unwanted := range []string{
		"do not issue repeated complete_subtask calls first",
		"do not call complete_subtask repeatedly first",
		"<swarm-handoff-summary>",
	} {
		if strings.Contains(strings.ToLower(text), unwanted) {
			t.Fatalf("prompt retained contradictory subtask suppression %q: %s", unwanted, text)
		}
	}
	if strings.Contains(text, "old chat that must not appear") {
		t.Fatalf("prompt leaked prior conversation: %s", text)
	}
	if !strings.Contains(text, `"status": "pending"`) {
		t.Fatalf("prompt did not preserve lifecycle-selected checkpoint state: %s", text)
	}
	plan, ok, err := svc.sessions.GetPlan(sessionID, "plan-cp")
	if err != nil || !ok {
		t.Fatalf("load patched plan ok=%v err=%v", ok, err)
	}
	if got := plan.Document.Checkpoints[1].Status; got != sessionruntime.PlanCheckpointStatusPending {
		t.Fatalf("checkpoint status = %q, want pending lifecycle-selected state", got)
	}
	if got := plan.Document.Checkpoints[1].RunID; got != "" {
		t.Fatalf("checkpoint prompt builder unexpectedly assigned run id %q", got)
	}
	if got := plan.Document.Checkpoints[1].SessionID; got != "" {
		t.Fatalf("checkpoint prompt builder unexpectedly assigned session id %q", got)
	}
	if len(appliedMutations) != 0 {
		t.Fatalf("checkpoint prompt builder applied mutations: %#v", appliedMutations)
	}
	messages, err := svc.sessions.ListMessages(sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "old chat that must not appear" {
		t.Fatalf("start checkpoint should not append lifecycle message, messages = %#v", messages)
	}
	outbox, err := svc.sessions.ListRealtimeOutboxForSessionAfterSeq(sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list realtime outbox: %v", err)
	}
	if len(outbox) != 0 {
		t.Fatalf("checkpoint prompt builder unexpectedly wrote realtime outbox: %#v", outbox)
	}
}

func TestRunTurnCheckpointContextSendsFreshProviderInput(t *testing.T) {
	svc, sessionID, cleanup := newCheckpointRunPromptTestService(t)
	defer cleanup()
	runner := &checkpointPromptCaptureRunner{id: "test-provider"}
	svc.providers.RegisterRunner(runner)
	if _, _, err := svc.sessions.SavePlanWithMetadata(sessionID, "plan-cp", "Plan CP", "# ignored display", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ID:                 "plan-cp",
		Info:               pebblestore.SessionPlanInfo{Goal: "Fresh provider input"},
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Fresh", Status: sessionruntime.PlanCheckpointStatusPending, Objective: "Current replacement objective"}},
		ActiveCheckpointID: "cp-1",
	}}); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if _, _, _, err := svc.appendRunMessage(runAppendMessageInput{SessionID: sessionID, Role: "user", Content: "legacy context should be absent", LogicalKey: "legacy-chat"}); err != nil {
		t.Fatalf("append old chat: %v", err)
	}

	_, err := svc.RunTurn(context.Background(), sessionID, RunRequest{PlanCheckpointContext: &RunPlanCheckpointContext{PlanID: "plan-cp", CheckpointID: "cp-1"}}, RunStartMeta{RunID: "run-fresh", Principal: identity.Principal{UserID: "user-test", AccountScopeID: "acct-test"}})
	if err != nil {
		t.Fatalf("run checkpoint turn: %v", err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("provider request count = %d, want 1", len(runner.requests))
	}
	if len(runner.requests[0].Input) != 1 {
		t.Fatalf("provider input = %#v", runner.requests[0].Input)
	}
	text := inputTextFromProviderInput(t, runner.requests[0].Input[0])
	if strings.Contains(text, "legacy context should be absent") {
		t.Fatalf("provider input leaked prior conversation: %s", text)
	}
	if !strings.Contains(text, "Current replacement objective") || !strings.Contains(text, "cp-1") {
		t.Fatalf("provider input missing selected checkpoint objective: %s", text)
	}
	if strings.Contains(text, "Fresh provider input") || strings.Contains(text, `"goal"`) {
		t.Fatalf("provider input retained stale plan goal as a competing objective: %s", text)
	}
	if runner.requests[0].SessionID != sessionID {
		t.Fatalf("durable session id = %q, want %q", runner.requests[0].SessionID, sessionID)
	}
	if runner.requests[0].ProviderLineageID == "" || runner.requests[0].ProviderCacheKey == "" || runner.requests[0].SessionAffinityKey == "" {
		t.Fatalf("checkpoint request missing lineage/cache keys: %+v", runner.requests[0])
	}
	if runner.requests[0].ProviderCacheKey == sessionID || runner.requests[0].SessionAffinityKey == sessionID {
		t.Fatalf("checkpoint request reused raw session id for provider cache/affinity: %+v", runner.requests[0])
	}
	if runner.requests[0].BoundaryReason != "checkpoint_fresh_context" || runner.requests[0].NativeContinuationAllowed || !runner.requests[0].ForceFreshProviderContext {
		t.Fatalf("checkpoint request did not force fresh provider context: %+v", runner.requests[0])
	}
}

type checkpointPromptCaptureRunner struct {
	id       string
	requests []provideriface.Request
}

func (r *checkpointPromptCaptureRunner) ID() string { return r.id }

func (r *checkpointPromptCaptureRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	r.requests = append(r.requests, req)
	return provideriface.Response{Text: "checkpoint response"}, nil
}

func (r *checkpointPromptCaptureRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return r.CreateResponse(ctx, req)
}

type checkpointPromptAdapter struct{ id string }

func (a checkpointPromptAdapter) ID() string { return a.id }
func (a checkpointPromptAdapter) Status(context.Context) (provideriface.Status, error) {
	return provideriface.Status{ID: a.id, Ready: true, DefaultModel: "test-model", DefaultThinking: "off"}, nil
}

func newCheckpointRunPromptTestService(t *testing.T) (*Service, string, func()) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "state.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cleanup := func() { _ = store.Close() }
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		cleanup()
		t.Fatalf("open event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         "user-test",
		AccountScopeID: "acct-test",
		Title:          "Checkpoint run",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModeAuto,
		Preference:     &pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "off"},
	})
	if err != nil {
		cleanup()
		t.Fatalf("create session: %v", err)
	}
	providers := registry.New(checkpointPromptAdapter{id: "test-provider"})
	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	permissions := permission.NewService(pebblestore.NewPermissionStore(store), events, nil)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	enabled := true
	if _, _, _, err := agentSvc.UpsertForAccount("acct-test", agentruntime.UpsertInput{
		Name:                "swarm",
		Mode:                agentruntime.ModePrimary,
		RuntimeMode:         pebblestore.AgentRuntimeModePlanAuto,
		ExitPlanModeEnabled: pebblestore.BoolPtr(true),
		ToolContract:        &pebblestore.AgentToolContract{Preset: "read_write"},
		Enabled:             &enabled,
	}); err != nil {
		cleanup()
		t.Fatalf("seed swarm agent: %v", err)
	}
	svc := NewService(sessions, modelSvc, providers, tool.NewRuntime(1), permissions, agentSvc, nil, events)
	return svc, session.ID, cleanup
}

func inputTextFromProviderInput(t *testing.T, input map[string]any) string {
	t.Helper()
	content, ok := input["content"].([]map[string]any)
	if !ok || len(content) == 0 {
		t.Fatalf("input content = %#v", input["content"])
	}
	text, _ := content[0]["text"].(string)
	return text
}
