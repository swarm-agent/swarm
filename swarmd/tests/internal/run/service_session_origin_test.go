package run_test

import (
	"context"
	"path/filepath"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runpkg "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type staticRunner struct {
	id        string
	responses []provideriface.Response
	index     int
}

func (r *staticRunner) ID() string {
	return r.id
}

func (r *staticRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *staticRunner) CreateResponseStreaming(_ context.Context, _ provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	if r.index >= len(r.responses) {
		return provideriface.Response{Text: "ok"}, nil
	}
	resp := r.responses[r.index]
	r.index++
	return resp, nil
}

func TestRunTurnStreamingSessionMetadataDistinguishesForegroundAndBackgroundRuns(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "session-origin.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5.4", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}

	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	preference := &pebblestore.ModelPreference{
		Provider: "codex",
		Model:    "gpt-5.4",
		Thinking: "high",
	}
	foregroundSession, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Title:         "foreground",
		WorkspacePath: t.TempDir(),
		WorkspaceName: "workspace",
		Preference:    preference,
	})
	if err != nil {
		t.Fatalf("create foreground session: %v", err)
	}
	backgroundSession, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Title:         "background",
		WorkspacePath: t.TempDir(),
		WorkspaceName: "workspace",
		Preference:    preference,
	})
	if err != nil {
		t.Fatalf("create background session: %v", err)
	}

	runner := &staticRunner{
		id: "codex",
		responses: []provideriface.Response{
			{Text: "foreground ok"},
			{Text: "background ok"},
		},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)

	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure default agents: %v", err)
	}

	svc := runpkg.NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(1), nil, agentSvc, nil, events)

	if _, err := svc.RunTurnStreaming(context.Background(), foregroundSession.ID, runpkg.RunRequest{
		Prompt:     "hello",
		Background: false,
	}, runpkg.RunStartMeta{
		RunID: "run-foreground",
	}, nil); err != nil {
		t.Fatalf("run foreground turn: %v", err)
	}

	if _, err := svc.RunTurnStreaming(context.Background(), backgroundSession.ID, runpkg.RunRequest{
		Prompt:     "commit now",
		Background: true,
		TargetKind: runpkg.RunTargetKindBackground,
		TargetName: "memory",
	}, runpkg.RunStartMeta{
		RunID: "run-background",
	}, nil); err != nil {
		t.Fatalf("run background turn: %v", err)
	}

	updatedForeground, ok, err := sessionSvc.GetSession(foregroundSession.ID)
	if err != nil {
		t.Fatalf("get foreground session: %v", err)
	}
	if !ok {
		t.Fatalf("foreground session missing")
	}
	if got, _ := updatedForeground.Metadata["background"].(bool); got {
		t.Fatalf("foreground session background metadata = true, want false")
	}
	if got, _ := updatedForeground.Metadata["launch_mode"].(string); got != "" {
		t.Fatalf("foreground session launch_mode = %q, want empty", got)
	}
	if got, _ := updatedForeground.Metadata["target_kind"].(string); got != "" {
		t.Fatalf("foreground session target_kind = %q, want empty", got)
	}
	if got, _ := updatedForeground.Metadata["target_name"].(string); got != "" {
		t.Fatalf("foreground session target_name = %q, want empty", got)
	}

	updatedBackground, ok, err := sessionSvc.GetSession(backgroundSession.ID)
	if err != nil {
		t.Fatalf("get background session: %v", err)
	}
	if !ok {
		t.Fatalf("background session missing")
	}
	if got, _ := updatedBackground.Metadata["background"].(bool); !got {
		t.Fatalf("background session background metadata = false, want true")
	}
	if got, _ := updatedBackground.Metadata["launch_mode"].(string); got != "background" {
		t.Fatalf("background session launch_mode = %q, want background", got)
	}
	if got, _ := updatedBackground.Metadata["target_kind"].(string); got != "background" {
		t.Fatalf("background session target_kind = %q, want background", got)
	}
	if got, _ := updatedBackground.Metadata["target_name"].(string); got != "memory" {
		t.Fatalf("background session target_name = %q, want memory", got)
	}
}
