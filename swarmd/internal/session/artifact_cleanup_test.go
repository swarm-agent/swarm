package session

import (
	"errors"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func newArtifactCleanupTestService(t *testing.T) *Service {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "artifact-cleanup.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	return NewService(pebblestore.NewSessionStore(store), events)
}

func artifactCleanupCreateOptions(sessionID, workspacePath, title string) CreateSessionOptions {
	return CreateSessionOptions{SessionID: sessionID, UserID: "user-1", AccountScopeID: "account-1", WorkspacePath: workspacePath, Title: title, Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "test-model", Thinking: "medium"}}
}

type artifactCleanerStub struct {
	calls []struct{ sessionID, workspacePath string }
	err   error
}

func (c *artifactCleanerStub) DeleteSession(sessionID, workspacePath string) error {
	c.calls = append(c.calls, struct{ sessionID, workspacePath string }{sessionID, workspacePath})
	return c.err
}

func TestPermanentDeleteCleansArtifactsAfterDurableTombstone(t *testing.T) {
	sessions := newArtifactCleanupTestService(t)
	created, _, err := sessions.CreateSessionWithOptions(artifactCleanupCreateOptions("artifact-delete", t.TempDir(), "delete"))
	if err != nil {
		t.Fatal(err)
	}
	cleaner := &artifactCleanerStub{}
	sessions.SetArtifactSessionCleaner(cleaner)
	if err := sessions.DeleteSession(created.ID); err != nil {
		t.Fatal(err)
	}
	if len(cleaner.calls) != 1 || cleaner.calls[0].workspacePath != created.WorkspacePath {
		t.Fatalf("cleanup calls = %+v", cleaner.calls)
	}
	if tombstone, ok, err := sessions.GetSessionTombstone(created.ID); err != nil || !ok || !tombstone.Deleted || tombstone.ArtifactCleanupPending {
		t.Fatalf("durable tombstone cleanup state incorrect: ok=%t tombstone=%+v err=%v", ok, tombstone, err)
	}
}

func TestArtifactCleanupFailureIsHonestAndRetryable(t *testing.T) {
	sessions := newArtifactCleanupTestService(t)
	created, _, err := sessions.CreateSessionWithOptions(artifactCleanupCreateOptions("artifact-retry", t.TempDir(), "delete"))
	if err != nil {
		t.Fatal(err)
	}
	cleaner := &artifactCleanerStub{err: errors.New("filesystem unavailable")}
	sessions.SetArtifactSessionCleaner(cleaner)
	if err := sessions.DeleteSession(created.ID); err == nil {
		t.Fatal("cleanup failure reported success")
	}
	if tombstone, ok, err := sessions.GetSessionTombstone(created.ID); err != nil || !ok || !tombstone.Deleted || !tombstone.ArtifactCleanupPending || tombstone.WorkspacePath != created.WorkspacePath {
		t.Fatalf("retry tombstone missing: ok=%t tombstone=%+v err=%v", ok, tombstone, err)
	}
}

func TestArchiveDoesNotCleanArtifacts(t *testing.T) {
	sessions := newArtifactCleanupTestService(t)
	created, _, err := sessions.CreateSessionWithOptions(artifactCleanupCreateOptions("artifact-archive", t.TempDir(), "archive"))
	if err != nil {
		t.Fatal(err)
	}
	cleaner := &artifactCleanerStub{}
	sessions.SetArtifactSessionCleaner(cleaner)
	if err := sessions.ArchiveSession(created.ID); err != nil {
		t.Fatal(err)
	}
	if len(cleaner.calls) != 0 {
		t.Fatalf("archive cleaned artifacts: %+v", cleaner.calls)
	}
}
