package pebblestore

import (
	"reflect"
	"strings"
	"testing"
)

// Purpose: SelectMemory is the shared runtime/API budget boundary. Prove whole
// entries, metadata-inclusive budgets, deterministic priority and explicit scope
// omissions without evicting stored rules; no provider or transport is needed.
func TestMemorySelectionScopeAndBudget(t *testing.T) {
	d := MemoryDocument{Revision: 3, Settings: DefaultMemorySettings(), Entries: []MemoryEntry{
		{ID: "learned", Kind: "learned", Content: strings.Repeat("x", 500)},
		{ID: "rule", Kind: "rule", Content: "explicit rule"},
		{ID: "foreign", Kind: "learned", WorkspaceID: "other", Content: "secret context"},
		{ID: "orientation", Kind: "orientation", WorkspaceID: "other", Content: "known workspace, not access"},
		{ID: "session", Kind: "rule", SessionID: "other", Content: "session rule"},
	}}
	before := append([]MemoryEntry(nil), d.Entries...)
	d.Settings.InjectionTokens = 600
	got := SelectMemory(d, "current", "session")
	if got.InjectedTokens != len(got.Payload) || got.InjectedTokens > 600 || len(got.Entries) != 2 || got.Entries[0].ID != "rule" || got.Entries[1].ID != "orientation" {
		t.Fatalf("selection: %+v", got)
	}
	reasons := map[string]string{}
	for _, o := range got.Omitted {
		reasons[o.ID] = o.Reason
	}
	if reasons["foreign"] != "different workspace" || reasons["session"] != "different session" || reasons["learned"] != "injection budget" {
		t.Fatal(reasons)
	}
	if !reflect.DeepEqual(d.Entries, before) {
		t.Fatal("selection mutated storage")
	}
	d.Settings.ReadEnabled = false
	got = SelectMemory(d, "current", "session")
	if got.Payload != "" || len(got.Omitted) != 5 {
		t.Fatal("disabled read leaked content")
	}
}

// Purpose: SelectionForSession must fail closed for unknown/foreign sessions,
// rather than using caller scope to read content. The store is the narrow layer.
func TestMemorySelectionRejectsUnknownSession(t *testing.T) {
	_, s, d := memoryTest(t)
	if _, err := s.SelectionForSession("a", "user", "missing"); err != ErrMemoryPolicy {
		t.Fatal(err)
	}
	after, err := s.GetForAccount("a")
	if err != nil || !reflect.DeepEqual(d, after) {
		t.Fatal("unauthorized read mutated memory")
	}
}

// Purpose: public memory reads must redact orphaned history as well as live
// source-derived content. A raw historical fixture simulates a deleted source.
func TestMemorySelectionRedactsOrphanedHistory(t *testing.T) {
	db, s, d := memoryTest(t)
	old := MemoryEntry{ID: "orphan", Kind: "learned", Content: "deleted source secret", Revision: 1, Sources: []MemorySource{{SessionID: "deleted-session"}}}
	d.History = []MemoryChange{{Revision: 1, EntryID: old.ID, After: &old}}
	if err := db.PutJSON(memoryKey("a"), d); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetForAccount("a")
	if err != nil {
		t.Fatal(err)
	}
	if !got.History[0].Redacted || got.History[0].After != nil {
		t.Fatal("deleted content remained readable")
	}
	if _, err := s.MutateForAccount("a", MemoryMutation{ExpectedRevision: got.Revision, Actor: MemoryActor{Kind: "user", ID: "user"}, Reason: "restore", Operation: "restore", EntryID: old.ID, RestoreRevision: 1}); err != ErrMemoryPolicy {
		t.Fatal("orphan restore accepted", err)
	}
}
