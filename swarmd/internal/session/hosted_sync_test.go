package session

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestHostedAppendMessageMirrorsCanonicalStateIntoLocalRuntimeCache(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "hosted-session.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	svc := NewService(pebblestore.NewSessionStore(store), eventLog)
	fakeSync := &fakeHostedSessionSync{
		message: pebblestore.MessageSnapshot{
			ID:        "msg_000001",
			SessionID: "session-routed",
			GlobalSeq: 1,
			Role:      "user",
			Content:   "hello from child",
			CreatedAt: time.Now().UnixMilli(),
		},
		session: pebblestore.SessionSnapshot{
			ID:            "session-routed",
			WorkspacePath: "/runtime/workspace",
			WorkspaceName: "workspace",
			Title:         "Routed Session",
			Mode:          ModePlan,
			Metadata: map[string]any{
				HostedSessionMetadataHostBackendURL:       "http://127.0.0.1:7781",
				HostedSessionMetadataHostWorkspacePath:    "/host/workspace",
				HostedSessionMetadataRuntimeWorkspacePath: "/runtime/workspace",
				HostedSessionMetadataChildSwarmID:         "child-swarm",
			},
			MessageCount:  1,
			LastMessageAt: time.Now().UnixMilli(),
			UpdatedAt:     time.Now().UnixMilli(),
			CreatedAt:     time.Now().UnixMilli(),
		},
	}
	svc.SetHostedSync(fakeSync)

	initial := HostedSessionDescriptor{
		HostSwarmID:          "host-swarm",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
		ChildSwarmID:         "child-swarm",
	}.apply(pebblestore.SessionSnapshot{
		ID:            "session-routed",
		WorkspacePath: "/runtime/workspace",
		WorkspaceName: "workspace",
		Title:         "Routed Session",
		Mode:          ModePlan,
		UpdatedAt:     time.Now().UnixMilli(),
		CreatedAt:     time.Now().UnixMilli(),
	})
	if _, err := svc.StoreMirroredSession(initial); err != nil {
		t.Fatalf("store mirrored session: %v", err)
	}

	message, updated, _, err := svc.AppendMessage("session-routed", "user", "hello from child", nil)
	if err != nil {
		t.Fatalf("append hosted message: %v", err)
	}
	if fakeSync.appendCalls != 1 {
		t.Fatalf("append calls = %d, want 1", fakeSync.appendCalls)
	}
	if message.Content != "hello from child" {
		t.Fatalf("message content = %q, want %q", message.Content, "hello from child")
	}
	if updated.WorkspacePath != "/runtime/workspace" {
		t.Fatalf("returned workspace path = %q, want runtime path", updated.WorkspacePath)
	}

	cached, ok, err := svc.GetSession("session-routed")
	if err != nil {
		t.Fatalf("get mirrored session: %v", err)
	}
	if !ok {
		t.Fatal("mirrored session missing from local cache")
	}
	if cached.WorkspacePath != "/runtime/workspace" {
		t.Fatalf("cached workspace path = %q, want %q", cached.WorkspacePath, "/runtime/workspace")
	}
	descriptor, hosted := HostedSessionFromMetadata(cached.Metadata)
	if !hosted {
		t.Fatal("cached session lost hosted-session descriptor")
	}
	if descriptor.HostSwarmID != "host-swarm" {
		t.Fatalf("cached host swarm id = %q, want %q", descriptor.HostSwarmID, "host-swarm")
	}

	messages, err := svc.ListMessages("session-routed", 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(messages))
	}
	if messages[0].Content != "hello from child" {
		t.Fatalf("cached message content = %q, want %q", messages[0].Content, "hello from child")
	}
}

func TestMirroredRoutedSessionUsesLocalRuntimeWorkspaceWhenLocalSwarmIsUnknown(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "runtime-mirrored-routed-session.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	svc := NewService(pebblestore.NewSessionStore(store), eventLog)
	descriptor := HostedSessionDescriptor{
		HostSwarmID:          "host-swarm",
		HostBackendURL:       "http://127.0.0.1:8781",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
		ChildSwarmID:         "child-swarm",
	}

	mirrored, err := svc.StoreMirroredSession(descriptor.apply(pebblestore.SessionSnapshot{
		ID:            "session-flow",
		WorkspacePath: "/runtime/workspace",
		WorkspaceName: "workspace",
		Title:         "Flow",
		Mode:          ModeAuto,
		UpdatedAt:     time.Now().UnixMilli(),
		CreatedAt:     time.Now().UnixMilli(),
	}))
	if err != nil {
		t.Fatalf("store mirrored session: %v", err)
	}
	if mirrored.WorkspacePath != "/runtime/workspace" {
		t.Fatalf("mirrored workspace path = %q, want runtime workspace", mirrored.WorkspacePath)
	}

	cached, ok, err := svc.GetSession("session-flow")
	if err != nil || !ok {
		t.Fatalf("get mirrored session ok=%v err=%v", ok, err)
	}
	if cached.WorkspacePath != "/runtime/workspace" {
		t.Fatalf("cached workspace path = %q, want runtime workspace", cached.WorkspacePath)
	}
}

func TestExistingControllerMirroredRoutedSessionStaysInHostWorkspaceWithoutLocalSwarmID(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "controller-mirrored-routed-session.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	svc := NewService(pebblestore.NewSessionStore(store), eventLog)
	descriptor := HostedSessionDescriptor{
		HostSwarmID:          "host-swarm",
		HostBackendURL:       "http://127.0.0.1:8781",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
		ChildSwarmID:         "child-swarm",
	}

	initial, err := svc.StoreMirroredSession(descriptor.apply(pebblestore.SessionSnapshot{
		ID:            "session-flow",
		WorkspacePath: "/host/workspace",
		WorkspaceName: "workspace",
		Title:         "Flow",
		Mode:          ModeAuto,
		UpdatedAt:     time.Now().UnixMilli(),
		CreatedAt:     time.Now().UnixMilli(),
	}))
	if err != nil {
		t.Fatalf("store initial mirrored session: %v", err)
	}
	if initial.WorkspacePath != "/host/workspace" {
		t.Fatalf("initial workspace path = %q, want host workspace", initial.WorkspacePath)
	}

	mirrored, err := svc.StoreMirroredSession(descriptor.apply(pebblestore.SessionSnapshot{
		ID:            "session-flow",
		WorkspacePath: "/runtime/workspace",
		WorkspaceName: "workspace",
		Title:         "Flow",
		Mode:          ModeAuto,
		UpdatedAt:     time.Now().UnixMilli(),
		CreatedAt:     time.Now().UnixMilli(),
	}))
	if err != nil {
		t.Fatalf("store updated mirrored session: %v", err)
	}
	if mirrored.WorkspacePath != "/host/workspace" {
		t.Fatalf("mirrored workspace path = %q, want host workspace", mirrored.WorkspacePath)
	}

	cached, ok, err := svc.GetSession("session-flow")
	if err != nil || !ok {
		t.Fatalf("get mirrored session ok=%v err=%v", ok, err)
	}
	if cached.WorkspacePath != "/host/workspace" {
		t.Fatalf("cached workspace path = %q, want host workspace", cached.WorkspacePath)
	}
}

func TestHostOwnedRoutedSessionDoesNotHostedSyncBackToItself(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "host-owned-routed-session.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	svc := NewService(pebblestore.NewSessionStore(store), eventLog)
	svc.SetLocalSwarmIDResolver(func() string { return "host-swarm" })
	fakeSync := &fakeHostedSessionSync{}
	svc.SetHostedSync(fakeSync)

	session, _, err := svc.CreateSessionWithOptions(CreateSessionOptions{
		SessionID:     "session-host-owned",
		Title:         "Host Owned Routed Session",
		WorkspacePath: "/host/workspace",
		WorkspaceName: "workspace",
		Mode:          ModePlan,
		Preference: &pebblestore.ModelPreference{
			Provider: "codex",
			Model:    "gpt-5.4",
			Thinking: "medium",
		},
		Metadata: HostedSessionDescriptor{
			HostSwarmID:          "host-swarm",
			HostBackendURL:       "http://127.0.0.1:8781",
			HostWorkspacePath:    "/host/workspace",
			RuntimeWorkspacePath: "/runtime/workspace",
			ChildSwarmID:         "child-swarm",
		}.WithMetadata(nil),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	message, updated, _, err := svc.AppendMessage(session.ID, "user", "hello from host", nil)
	if err != nil {
		t.Fatalf("append message: %v", err)
	}
	if fakeSync.appendCalls != 0 {
		t.Fatalf("append calls = %d, want 0 for host-owned routed session", fakeSync.appendCalls)
	}
	if message.Content != "hello from host" {
		t.Fatalf("message content = %q, want %q", message.Content, "hello from host")
	}
	if updated.WorkspacePath != "/host/workspace" {
		t.Fatalf("workspace path = %q, want %q", updated.WorkspacePath, "/host/workspace")
	}
}

type fakeHostedSessionSync struct {
	appendCalls int
	message     pebblestore.MessageSnapshot
	session     pebblestore.SessionSnapshot
}

func (f *fakeHostedSessionSync) AppendMessage(context.Context, HostedSessionDescriptor, string, string, string, map[string]any) (pebblestore.MessageSnapshot, pebblestore.SessionSnapshot, error) {
	f.appendCalls++
	return f.message, f.session, nil
}

func (f *fakeHostedSessionSync) SetMode(context.Context, HostedSessionDescriptor, string, string) (pebblestore.SessionSnapshot, error) {
	return f.session, nil
}

func (f *fakeHostedSessionSync) SetTitle(context.Context, HostedSessionDescriptor, string, string) (pebblestore.SessionSnapshot, error) {
	return f.session, nil
}

func (f *fakeHostedSessionSync) UpdateMetadata(context.Context, HostedSessionDescriptor, string, map[string]any) (pebblestore.SessionSnapshot, error) {
	return f.session, nil
}

func (f *fakeHostedSessionSync) UpsertLifecycle(context.Context, HostedSessionDescriptor, pebblestore.SessionLifecycleSnapshot) error {
	return nil
}

func (f *fakeHostedSessionSync) PublishEvent(context.Context, HostedSessionDescriptor, string, string, map[string]any, string, string) (pebblestore.EventEnvelope, error) {
	return pebblestore.EventEnvelope{}, nil
}

func (d HostedSessionDescriptor) apply(session pebblestore.SessionSnapshot) pebblestore.SessionSnapshot {
	session.Metadata = d.WithMetadata(session.Metadata)
	return session
}
