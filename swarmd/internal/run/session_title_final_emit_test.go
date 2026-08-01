package run

import (
	"context"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
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
	providers := registry.New()
	providers.RegisterRunner(staticTitleRunner{text: "Final title"})
	svc := &Service{sessions: sessions, events: eventLog, providers: providers}

	if _, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:     "session-title",
		Title:         "New Session",
		WorkspacePath: "/workspace",
		WorkspaceName: "workspace",
		Preference:    &pebblestore.ModelPreference{Provider: "static", Model: "title-model", Thinking: "medium"},
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	var emitted []StreamEvent
	svc.generateAndApplySessionTitle("session-title", "user: fix the title", "final", 2, 5, pebblestore.ModelPreference{Provider: "static", Model: "title-model"}, pebblestore.AgentProfile{Name: "memory", Provider: "static", Model: "title-model", Enabled: true}, identity.Principal{}, func(event StreamEvent) {
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
		Metadata: map[string]any{"title_locked": "true", "title_pending": "false", "title_source": "router"},
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

func (staticTitleRunner) ID() string { return "static" }

func (r staticTitleRunner) CreateResponse(context.Context, provideriface.Request) (provideriface.Response, error) {
	return provideriface.Response{Text: r.text}, nil
}

func (r staticTitleRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	_ = onEvent
	return r.CreateResponse(ctx, req)
}
