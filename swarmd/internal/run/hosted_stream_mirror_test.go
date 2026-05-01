package run

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestHostedStreamMirrorStoresMessageAndPublishesEvent(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "hosted-stream-mirror.pebble"))
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
	svc := &Service{sessions: sessions, events: eventLog}

	descriptor := sessionruntime.HostedSessionDescriptor{HostSwarmID: "controller-swarm", HostBackendURL: "http://127.0.0.1:1", HostWorkspacePath: "/host/workspace", RuntimeWorkspacePath: "/runtime/workspace", ChildSwarmID: "target-swarm"}
	session := descriptor.WithMetadata(map[string]any{"source": "flow", "owner_transport": "flow_scheduler"})
	if _, err := sessions.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "session-flow", WorkspacePath: "/runtime/workspace", WorkspaceName: "workspace", Title: "Flow", Mode: sessionruntime.ModeAuto, Metadata: session, CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatalf("store session: %v", err)
	}

	message := pebblestore.MessageSnapshot{ID: "msg_00000000000000000005", SessionID: "session-flow", GlobalSeq: 5, Role: "assistant", Content: "live parity", CreatedAt: time.Now().UnixMilli()}
	svc.mirrorHostedStreamEvent(StreamEvent{Type: StreamEventMessageStored, SessionID: "session-flow", RunID: "run-flow", Message: &message})
	svc.mirrorHostedStreamEvent(StreamEvent{Type: StreamEventMessageStored, SessionID: "session-flow", RunID: "run-flow", Message: &message})

	messages, err := sessions.ListMessages("session-flow", 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "live parity" {
		t.Fatalf("messages = %+v", messages)
	}
	events, err := eventLog.ReadFrom(1, 20)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var sawRunMessage bool
	for _, event := range events {
		if event.EventType == "run.message.stored" && event.EntityID == "session-flow" {
			sawRunMessage = true
		}
	}
	if !sawRunMessage {
		t.Fatalf("missing run.message.stored event in %+v", events)
	}
}

type streamMirrorHostedSync struct {
	sessions *sessionruntime.Service
}

func (streamMirrorHostedSync) AppendMessage(context.Context, sessionruntime.HostedSessionDescriptor, string, string, string, map[string]any) (pebblestore.MessageSnapshot, pebblestore.SessionSnapshot, error) {
	return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil
}

func (streamMirrorHostedSync) SetMode(context.Context, sessionruntime.HostedSessionDescriptor, string, string) (pebblestore.SessionSnapshot, error) {
	return pebblestore.SessionSnapshot{}, nil
}

func (streamMirrorHostedSync) SetTitle(context.Context, sessionruntime.HostedSessionDescriptor, string, string) (pebblestore.SessionSnapshot, error) {
	return pebblestore.SessionSnapshot{}, nil
}

func (streamMirrorHostedSync) UpdateMetadata(context.Context, sessionruntime.HostedSessionDescriptor, string, map[string]any) (pebblestore.SessionSnapshot, error) {
	return pebblestore.SessionSnapshot{}, nil
}

func (streamMirrorHostedSync) UpsertLifecycle(context.Context, sessionruntime.HostedSessionDescriptor, pebblestore.SessionLifecycleSnapshot) error {
	return nil
}

func (s *streamMirrorHostedSync) PublishEvent(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, sessionID, eventType string, payload map[string]any, causationID, correlationID string) (pebblestore.EventEnvelope, error) {
	_ = ctx
	_ = descriptor
	if s == nil || s.sessions == nil {
		return pebblestore.EventEnvelope{}, nil
	}
	return s.sessions.StoreMirroredEvent(sessionID, eventType, payload, causationID, correlationID)
}
