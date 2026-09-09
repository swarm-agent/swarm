package pebblestore

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Purpose: MemoryStore is the narrowest durable policy authority. These tests
// prove account isolation, CAS and failure atomicity, owner separation, bounded
// history/restore, forgetting, and migration across restart using a real temp DB.
// Threats include learned writes replacing rules, stale scope publishing, old
// snapshots resurrecting deleted sources, and two competing map authorities.
func memoryTest(t *testing.T) (*Store, *MemoryStore, MemoryDocument) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Source-bearing fixtures need owned sessions for read-time revocation.
	for _, id := range []string{"session", "source", "old-session"} {
		if err := NewSessionStore(db).CreateSessionForAccount(SessionSnapshot{ID: id, Metadata: map[string]any{}}, "user", "a"); err != nil {
			t.Fatal(err)
		}
	}
	s := NewMemoryStore(db)
	d, err := s.GetForAccount("a")
	if err != nil {
		t.Fatal(err)
	}
	return db, s, d
}
func memoryPut(d MemoryDocument, id, kind, content string) MemoryMutation {
	return MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "user", ID: "user"}, Reason: "Explicit remember", Operation: "put", Entry: MemoryEntry{ID: id, Kind: kind, Content: content}}
}
func memoryApply(t *testing.T, s *MemoryStore, m MemoryMutation) MemoryDocument {
	t.Helper()
	d, err := s.MutateForAccount("a", m)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func memoryUnchanged(t *testing.T, s *MemoryStore, want MemoryDocument, m MemoryMutation) {
	t.Helper()
	if _, err := s.MutateForAccount("a", m); err == nil {
		t.Fatal("rejected mutation succeeded")
	}
	got, err := s.GetForAccount("a")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("partial mutation: %v", err)
	}
}
func TestMemoryIsolationCASBudgets(t *testing.T) {
	_, s, d := memoryTest(t)
	if !d.Settings.ReadEnabled || !d.Settings.RememberEnabled || d.Settings.AutomationEnabled || d.Settings.Mode != "manual" || !d.Settings.ReviewBeforeApply {
		t.Fatal("unsafe defaults")
	}
	b, err := s.GetForAccount("b")
	if err != nil {
		t.Fatal(err)
	}
	stale := memoryPut(d, "rule", "rule", "user rule")
	d = memoryApply(t, s, stale)
	memoryUnchanged(t, s, d, stale)
	got, err := s.GetForAccount("b")
	if err != nil || !reflect.DeepEqual(got, b) {
		t.Fatal("cross-account mutation")
	}
	for _, account := range []string{"", "   "} {
		if _, err := s.GetForAccount(account); err == nil {
			t.Fatal("empty account accepted")
		}
	}
	bad := memoryPut(d, "big", "learned", strings.Repeat("x", WorkspaceMapMaxBytes+1))
	memoryUnchanged(t, s, d, bad)
	settings := d.Settings
	settings.StorageTokens = 1
	settings.InjectionTokens = 1
	memoryUnchanged(t, s, d, MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "user", ID: "user"}, Reason: "Budget", Operation: "settings", Settings: &settings})
	// Concurrent writers from one exact revision produce exactly one winner.
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.MutateForAccount("a", memoryPut(d, "race", "orientation", "context"))
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else if !errors.Is(err, ErrMemoryConflict) {
				t.Errorf("unexpected race error: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("winners=%d", wins)
	}
}
func TestMemoryLearnedOwnershipScopeAndRestore(t *testing.T) {
	_, s, d := memoryTest(t)
	d = memoryApply(t, s, memoryPut(d, "rule", "rule", "pinned preference"))
	settings := d.Settings
	settings.AutomationEnabled = true
	settings.IncludedSessions = []string{"session"}
	d = memoryApply(t, s, MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "user", ID: "user"}, Reason: "Opt in", Operation: "settings", Settings: &settings})
	learned := memoryPut(d, "context", "learned", "project context")
	learned.Actor = MemoryActor{Kind: "learned", ID: "memory", JobID: "job", Model: AgentModelAssignment{Provider: "test", Model: "test", Thinking: "low"}}
	learned.Entry.Sources = []MemorySource{{WorkspaceID: "workspace", SessionID: "session", EventSeq: 1}}
	memoryUnchanged(t, s, d, learned)
	learned.Approved = true
	attack := learned
	attack.Entry.ID = "rule"
	memoryUnchanged(t, s, d, attack)
	attack = learned
	attack.Entry.Pinned = true
	memoryUnchanged(t, s, d, attack)
	attack = learned
	attack.Entry.Sources = []MemorySource{{WorkspaceID: "workspace", SessionID: "other", EventSeq: 1}}
	memoryUnchanged(t, s, d, attack)
	d = memoryApply(t, s, learned)
	originalRevision := d.Revision
	d = memoryApply(t, s, memoryPut(d, "context", "learned", "edited context"))
	d = memoryApply(t, s, MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "user", ID: "user"}, Reason: "Backtrack", Operation: "restore", EntryID: "context", RestoreRevision: originalRevision})
	if d.Entries[memoryEntryIndex(d, "context")].Content != "project context" || d.Revision <= originalRevision {
		t.Fatal("restore did not create a revision")
	}
	d = memoryApply(t, s, MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "user", ID: "user"}, Reason: "Forget", Operation: "delete", EntryID: "context"})
	restore := MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "user", ID: "user"}, Reason: "Backtrack", Operation: "restore", EntryID: "context", RestoreRevision: originalRevision}
	memoryUnchanged(t, s, d, restore)
	learned.ExpectedRevision = d.Revision
	learned.Entry.ID = "different-id"
	memoryUnchanged(t, s, d, learned)
	payload, _ := json.Marshal(d)
	if strings.Contains(string(payload), "project context") || strings.Contains(string(payload), "edited context") {
		t.Fatal("forgotten content remains in history")
	}
}
func TestMemoryExclusionScrubsHistoricalSources(t *testing.T) {
	_, s, d := memoryTest(t)
	m := memoryPut(d, "context", "learned", "old source secret")
	m.Entry.Sources = []MemorySource{{WorkspaceID: "workspace", SessionID: "source", EventSeq: 1}}
	d = memoryApply(t, s, m)
	old := d.Revision
	d = memoryApply(t, s, memoryPut(d, "context", "learned", "new source"))
	settings := d.Settings
	settings.ExcludedSessions = []string{"source"}
	d = memoryApply(t, s, MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "user", ID: "user"}, Reason: "Exclude", Operation: "settings", Settings: &settings})
	payload, _ := json.Marshal(d)
	if strings.Contains(string(payload), "old source secret") {
		t.Fatal("history leaked excluded source")
	}
	memoryUnchanged(t, s, d, MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "user", ID: "user"}, Reason: "Restore", Operation: "restore", EntryID: "context", RestoreRevision: old})
	settings.ExcludedSessions = nil
	d = memoryApply(t, s, MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "user", ID: "user"}, Reason: "Remove exclusion", Operation: "settings", Settings: &settings})
	m.ExpectedRevision = d.Revision
	m.Entry.ID = "new-id"
	memoryUnchanged(t, s, d, m)
}
func TestMemoryMigrationRestartRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := WorkspaceMap{SchemaVersion: 1, Revision: 7, Content: "# Workspace Map\n\nexact legacy bytes\n", CreatedAt: 1, UpdatedAt: 2}
	legacy.Digest = workspaceMapDigest(legacy.Content)
	if err := db.PutJSON(KeyWorkspaceMapForAccount("a"), legacy); err != nil {
		t.Fatal(err)
	}
	s := NewMemoryStore(db)
	d, err := s.GetForAccount("a")
	if err != nil {
		t.Fatal(err)
	}
	if e := d.Entries[0]; e.Content != legacy.Content || e.Revision != 7 {
		t.Fatal("migration lost content or revision")
	}
	var old WorkspaceMap
	if ok, err := db.GetJSON(KeyWorkspaceMapForAccount("a"), &old); err != nil || ok {
		t.Fatal("legacy authority survived")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = NewMemoryStore(db)
	again, err := s.GetForAccount("a")
	if err != nil || !reflect.DeepEqual(d, again) {
		t.Fatal("restart migration not idempotent")
	}
	clock := time.Unix(2000000000, 0)
	s.now = func() time.Time { return clock }
	m := memoryPut(d, "expiring", "learned", "retained secret")
	m.Entry.ExpiresAt = clock.Add(time.Hour).UnixMilli()
	d = memoryApply(t, s, m)
	clock = clock.Add(2 * time.Hour)
	d, err = s.GetForAccount("a")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(d)
	if strings.Contains(string(payload), "retained secret") || memoryEntryIndex(d, "expiring") >= 0 {
		t.Fatal("retention did not redact")
	}
	memoryUnchanged(t, s, d, memoryPut(d, "expiring", "learned", "resurrection"))
}
func TestMemoryHistoryBoundAndFailedMigration(t *testing.T) {
	db, s, d := memoryTest(t)
	for i := 0; i < MemoryMaxHistory+5; i++ {
		d = memoryApply(t, s, memoryPut(d, "entry", "orientation", "context"))
	}
	if len(d.History) != MemoryMaxHistory {
		t.Fatal("unbounded history")
	}
	memoryUnchanged(t, s, d, MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "user", ID: "user"}, Reason: "Restore pruned", Operation: "restore", EntryID: "entry", RestoreRevision: 2})
	broken := WorkspaceMap{SchemaVersion: 99, Revision: 1, Content: "invalid", CreatedAt: 1, UpdatedAt: 1}
	if err := db.PutJSON(KeyWorkspaceMapForAccount("broken"), broken); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetForAccount("broken"); err == nil {
		t.Fatal("invalid migration accepted")
	}
	var saved WorkspaceMap
	ok, err := db.GetJSON(KeyWorkspaceMapForAccount("broken"), &saved)
	if err != nil || !ok || saved != broken {
		t.Fatal("migration partially changed source")
	}
	var out MemoryDocument
	if ok, err := db.GetJSON(memoryKey("broken"), &out); err != nil || ok {
		t.Fatal("migration partially created authority")
	}
}

// Purpose: deletion must retain historical source revocation; retention of an
// old snapshot cannot be extended by editing the live entry. Exercise the store
// directly so both data and audit postconditions are observed.
func TestMemoryHistoricalDeletionAndExpiry(t *testing.T) {
	_, s, d := memoryTest(t)
	clock := time.Unix(2000000000, 0)
	s.now = func() time.Time { return clock }
	m := memoryPut(d, "entry", "learned", "historical secret")
	m.Entry.Sources = []MemorySource{{SessionID: "old-session", WorkspaceID: "workspace", EventSeq: 1}}
	m.Entry.ExpiresAt = clock.Add(time.Hour).UnixMilli()
	d = memoryApply(t, s, m)
	d = memoryApply(t, s, memoryPut(d, "entry", "learned", "current content"))
	d = memoryApply(t, s, MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "user", ID: "user"}, Reason: "Delete", Operation: "delete", EntryID: "entry"})
	m.ExpectedRevision = d.Revision
	m.Entry.ID = "replacement"
	memoryUnchanged(t, s, d, m)
	m = memoryPut(d, "expiry", "learned", "expiring historical text")
	m.Entry.ExpiresAt = clock.Add(time.Hour).UnixMilli()
	d = memoryApply(t, s, m)
	d = memoryApply(t, s, memoryPut(d, "expiry", "learned", "new text"))
	previous := d.Revision
	clock = clock.Add(2 * time.Hour)
	d, err := s.GetForAccount("a")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(d)
	if strings.Contains(string(payload), "expiring historical text") || d.Revision <= previous {
		t.Fatal("historical expiry not redacted and revisioned")
	}
}

// Purpose: corrupt/foreign persisted ownership must fail closed; exhausted
// tombstone capacity must not partially delete an entry or its audit history.
func TestMemoryForeignRecordAndTombstoneFailureAtomicity(t *testing.T) {
	db, s, d := memoryTest(t)
	d = memoryApply(t, s, memoryPut(d, "entry", "rule", "protected"))
	if err := db.PutJSON(memoryKey("foreign"), d); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetForAccount("foreign"); err == nil {
		t.Fatal("foreign record accepted")
	}
	for i := 0; i < MemoryMaxTombstones; i++ {
		d.Forgotten = append(d.Forgotten, workspaceMapDigest(string(rune(i))+"marker"))
	}
	if err := db.PutJSON(memoryKey("a"), d); err != nil {
		t.Fatal(err)
	}
	memoryUnchanged(t, s, d, MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "user", ID: "user"}, Reason: "Forget", Operation: "delete", EntryID: "entry"})
}
