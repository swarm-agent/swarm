package run

import (
	"context"
	"path/filepath"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestFinalSessionTitleUsesEmitter(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "session-title-final-emit.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agents.EnsureDefaults(); err != nil {
		t.Fatalf("ensure agent defaults: %v", err)
	}
	catalog := model.NewCatalogService(pebblestore.NewModelCatalogStore(store))
	models := model.NewService(pebblestore.NewModelStore(store), eventLog, catalog)
	if err := models.EnsureBootDefaults(); err != nil {
		t.Fatalf("ensure model defaults: %v", err)
	}
	_, _, utility, ok, err := models.RecommendedCatalogDefaults("codex")
	if err != nil || !ok {
		t.Fatalf("resolve Compact utility model: ok=%t err=%v", ok, err)
	}
	configured := pebblestore.AgentModelAssignment{Provider: "codex", Model: utility.Model, Thinking: utility.DefaultThinking}
	agentSettingsStore := pebblestore.NewAgentModelSettingsStore(store)
	if _, err := agentSettingsStore.PutForAccount(pebblestore.AgentModelSettingsRecord{
		AccountScopeID: "account-title",
		Swarm:          pebblestore.SwarmAgentModelAssignments{Action: configured, Plan: configured},
		SystemAgents:   pebblestore.SystemAgentModelAssignments{Compact: configured, Finder: configured, Coder: configured, Designer: configured, Router: configured},
	}); err != nil {
		t.Fatalf("set Compact settings: %v", err)
	}
	providers := registry.New()
	providers.RegisterRunner(staticTitleRunner{text: "Final title"})
	svc := &Service{sessions: sessions, events: eventLog, providers: providers, model: models, agents: agents, agentModelSettings: agentmodelsettings.NewService(agentSettingsStore)}
	preference := pebblestore.ModelPreference{Provider: "codex", Model: utility.Model, Thinking: utility.DefaultThinking}

	if _, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "session-title",
		AccountScopeID: "account-title",
		Title:          "New Session",
		WorkspacePath: "/workspace",
		WorkspaceName: "workspace",
		Preference:    &preference,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	var emitted []StreamEvent
	svc.generateAndApplySessionTitle("session-title", "user: fix the title", "final", 2, 5, preference, pebblestore.AgentProfile{}, identity.Principal{}, func(event StreamEvent) {
		emitted = append(emitted, event)
	})

	if len(emitted) != 1 {
		t.Fatalf("emitted events = %d, want 1", len(emitted))
	}
	if emitted[0].Type != StreamEventSessionTitle || emitted[0].TitleStage != "final" || emitted[0].Title != "Final title" {
		t.Fatalf("emitted event = %+v", emitted[0])
	}
}

func TestApplySessionTitleUpdateDoesNotOverwriteRouterOwnedTitle(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "session-title-router-lock.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	svc := &Service{sessions: sessions, events: eventLog}
	if _, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "router-owned-title", Title: "Router Owned Title", WorkspacePath: "/workspace", WorkspaceName: "workspace",
		Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "title-model", Thinking: "medium"},
		Metadata:   map[string]any{"title_locked": "true", "title_pending": "false", "title_source": "router"},
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	var emitted []StreamEvent
	svc.applySessionTitleUpdate("router-owned-title", "Late Compact Title", "final", func(event StreamEvent) {
		emitted = append(emitted, event)
	})

	stored, ok, err := sessions.GetSession("router-owned-title")
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	if stored.Title != "Router Owned Title" {
		t.Fatalf("title = %q, want Router Owned Title", stored.Title)
	}
	if len(emitted) != 0 {
		t.Fatalf("emitted events = %#v, want none", emitted)
	}
}

type staticTitleRunner struct {
	text string
}

func (staticTitleRunner) ID() string { return "codex" }

func (r staticTitleRunner) CreateResponse(context.Context, provideriface.Request) (provideriface.Response, error) {
	return provideriface.Response{Text: r.text}, nil
}

func (r staticTitleRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	_ = onEvent
	return r.CreateResponse(ctx, req)
}
