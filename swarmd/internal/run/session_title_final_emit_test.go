package run

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestFinalSessionTitleUsesEmitterForHostedMirror(t *testing.T) {
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
	sync := &streamMirrorHostedSync{sessions: sessions}
	sessions.SetHostedSync(sync)
	providers := registry.New()
	providers.RegisterRunner(staticTitleRunner{text: "Final mirrored title"})
	svc := &Service{sessions: sessions, events: eventLog, providers: providers}

	descriptor := sessionruntime.HostedSessionDescriptor{HostSwarmID: "controller-swarm", HostBackendURL: "http://127.0.0.1:1", HostWorkspacePath: "/host/workspace", RuntimeWorkspacePath: "/runtime/workspace", ChildSwarmID: "target-swarm"}
	metadata := descriptor.WithMetadata(map[string]any{"source": "managed-host-title-test"})
	if _, err := sessions.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "session-title", WorkspacePath: "/runtime/workspace", WorkspaceName: "workspace", Title: "New Session", Mode: sessionruntime.ModeAuto, Metadata: metadata, CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatalf("store session: %v", err)
	}

	var emitted []StreamEvent
	svc.generateAndApplySessionTitle("session-title", "user: fix the title", "final", 2, 5, pebblestore.ModelPreference{Provider: "static", Model: "title-model"}, pebblestore.AgentProfile{Name: "memory", Provider: "static", Model: "title-model", Enabled: true}, identity.Principal{}, func(event StreamEvent) {
		emitted = append(emitted, event)
		if err := svc.mirrorHostedStreamEvent(context.Background(), event); err != nil {
			t.Fatalf("mirror hosted stream event: %v", err)
		}
	})

	if len(emitted) != 1 {
		t.Fatalf("emitted events = %d, want 1", len(emitted))
	}
	if emitted[0].Type != StreamEventSessionTitle || emitted[0].TitleStage != "final" || emitted[0].Title != "Final mirrored title" {
		t.Fatalf("emitted event = %+v", emitted[0])
	}

	events, err := eventLog.ReadFrom(1, 20)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var sawHostedFinal bool
	for _, event := range events {
		if event.EventType == "run.session.title.updated" && event.EntityID == "session-title" {
			sawHostedFinal = true
		}
	}
	if !sawHostedFinal {
		t.Fatalf("missing hosted run.session.title.updated event in %+v", events)
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
