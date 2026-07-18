package pebblestore

import (
	"path/filepath"
	"testing"
)

func TestV3SessionModelProfileSnapshotPersistsReplaysAndClears(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "session-profile.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	sessions := NewSessionStore(store)
	profile := &SessionModelProfileSnapshot{Source: SessionModelProfileSourceTemporary, Name: "Temporary", ModelMode: ModelProfileModeSingle, Single: &ModelProfileSelection{Provider: "openai", Model: "temporary-model", Thinking: "high"}, AppliedAt: 100}
	created, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "session", UserID: "user", AccountScopeID: "account", ClientRequestID: "create", PayloadHash: "create-hash", Kind: V3SessionMutationCreateSession, Session: &SessionSnapshot{ID: "session", UserID: "user", AccountScopeID: "account", WorkspacePath: "/workspace", WorkspaceName: "workspace", ModelProfile: profile}, NowUnixMs: 100})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.RealtimeOutbox == nil {
		t.Fatal("create missing realtime outbox")
	}

	stored, ok, err := sessions.GetSession("session")
	if err != nil || !ok || stored.ModelProfile == nil || stored.ModelProfile.Single.Model != "temporary-model" {
		t.Fatalf("stored profile = %+v ok=%t err=%v", stored.ModelProfile, ok, err)
	}
	replay, err := sessions.ReplayV3SessionEvents("session", 0, 10)
	if err != nil || replay.Session == nil || replay.Session.ModelProfile == nil || replay.Session.ModelProfile.Single.Model != "temporary-model" {
		t.Fatalf("replay profile = %+v err=%v", replay.Session, err)
	}

	stored.ModelProfile = nil
	cleared, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "session", UserID: "user", AccountScopeID: "account", ClientRequestID: "clear", PayloadHash: "clear-hash", Kind: V3SessionMutationUpdateModelProfile, Session: &stored, NowUnixMs: 200})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared.Event.EventType != "session.model_profile.updated" || cleared.RealtimeOutbox == nil {
		t.Fatalf("clear event/outbox = %+v/%+v", cleared.Event, cleared.RealtimeOutbox)
	}
	after, ok, err := sessions.GetSession("session")
	if err != nil || !ok || after.ModelProfile != nil {
		t.Fatalf("cleared stored profile = %+v ok=%t err=%v", after.ModelProfile, ok, err)
	}
}
