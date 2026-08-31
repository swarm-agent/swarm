package run

import (
	"context"
	"path/filepath"
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

func TestRunTurnPassesCanonicalV3MutationToPlanManage(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "direct-run-plan-mutation.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	workspace := t.TempDir()
	session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         "user-test",
		AccountScopeID: "account-test",
		Title:          "Direct run plan mutation",
		WorkspacePath:  workspace,
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{
			Provider: "fake",
			Model:    "fake-model",
			Thinking: "off",
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	providers := registry.New()
	providers.RegisterRunner(&directRunPlanMutationProvider{})
	modelSvc := model.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	permissions := permission.NewService(pebblestore.NewPermissionStore(store), eventLog, nil)
	svc := NewService(sessions, modelSvc, providers, tool.NewRuntime(1), permissions, agents, nil, eventLog)

	result, err := svc.RunTurn(context.Background(), session.ID, RunRequest{Prompt: "start the checkpoint"}, RunStartMeta{
		RunID:                "run-direct-plan",
		Principal:            identity.Principal{UserID: "user-test", AccountScopeID: "account-test"},
		ApplySessionMutation: sessions.ApplySessionMutation,
	})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if result.ToolCallCount != 1 {
		t.Fatalf("tool call count = %d, want 1", result.ToolCallCount)
	}
	plan, ok, err := sessions.GetActivePlan(session.ID)
	if err != nil {
		t.Fatalf("get active plan: %v", err)
	}
	if !ok || plan.Document == nil || len(plan.Document.Checkpoints) != 1 {
		messages, listErr := sessions.ListMessages(session.ID, 0, 20)
		t.Fatalf("active plan was not durably saved: ok=%t plan=%+v messages=%+v list_err=%v result=%+v", ok, plan, messages, listErr, result)
	}
	if plan.Document.Checkpoints[0].ID != "cp-1" || plan.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusInProgress {
		t.Fatalf("checkpoint = %+v, want cp-1 in progress", plan.Document.Checkpoints[0])
	}
}

type directRunPlanMutationProvider struct {
	calls int
}

func (r *directRunPlanMutationProvider) ID() string { return "fake" }

func (r *directRunPlanMutationProvider) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *directRunPlanMutationProvider) CreateResponseStreaming(_ context.Context, _ provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.calls++
	if r.calls == 1 {
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{
			CallID:    "call-plan",
			Name:      "plan_manage",
			Arguments: `{"action":"start_session_checkpoint","change_request":"prove direct plan persistence","checkpoint_title":"Direct plan persistence","tasks":["Persist the checkpoint"],"acceptance_criteria":["The checkpoint is durably active"],"notes":"Focused direct-run regression"}`,
		}}}, nil
	}
	return provideriface.Response{Text: "checkpoint started"}, nil
}
