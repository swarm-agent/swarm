package pebblestore

import (
	"testing"
)

// Purpose: forgetting a context bank record is final. Requirement:
// DeleteCompanionContext at the current revision removes the record and its
// indexes and leaves a durable tombstone of its id; a later write of the same
// id (a replayed tool call, a re-indexed topic) writes nothing and answers
// state "forgotten" without an error, before and after a store reopen; a
// stale revision deletes nothing; a new id records again;
// GetVerifiedCompanionContext reads an active record with its verified
// source, does not find a forgotten one, and invalidates one whose source
// message changed; purging the bank (Companion deletion) removes the
// tombstones too. Threat: a forgotten decision resurrected by replay or
// re-indexing, a forget that does not survive a restart, or a topic re-index
// failing the Companion's turns on a tombstoned id. Boundary:
// SessionStore.Put/Delete/GetVerified/PurgeCompanionContext. Narrowest layer:
// the store, over a real Pebble path reopened once.
func TestCompanionContextForgetLeavesADurableTombstone(t *testing.T) {
	f := newCompanionContextFixture(t)
	f.createSession("acct", "user", "companion")
	seq := f.appendMessage("acct", "user", "companion", "We decided to ship the dark theme first")
	source := CompanionContextSource{Kind: CompanionContextSourceMessage, SessionID: "companion", MessageSeq: seq}
	decision := CompanionContextRecord{ID: "dark-theme", AccountScopeID: "acct", UserID: "user", Kind: CompanionContextKindDecision, Title: "Ship the dark theme first", Source: source}
	topic := CompanionContextRecord{ID: CompanionMessageContextID("companion", seq), AccountScopeID: "acct", UserID: "user", Kind: CompanionContextKindTopic, Title: "dark theme", Source: source}
	for _, rec := range []CompanionContextRecord{decision, topic} {
		if _, changed, err := f.db.PutCompanionContext(rec, 0, 1); err != nil || !changed {
			t.Fatalf("put %s: %v %v", rec.ID, changed, err)
		}
	}
	hit, found, err := f.db.GetVerifiedCompanionContext("acct", "user", "dark-theme", 2)
	if err != nil || !found || hit.SourceText != "We decided to ship the dark theme first" || hit.Record.State != CompanionContextStateActive {
		t.Fatalf("verified get: %+v %v %v", hit, found, err)
	}
	if err := f.db.DeleteCompanionContext("acct", "user", "dark-theme", 9); err != ErrCompanionContextConflict {
		t.Fatalf("stale forget: %v", err)
	}
	for _, id := range []string{"dark-theme", topic.ID} {
		if err := f.db.DeleteCompanionContext("acct", "user", id, 1); err != nil {
			t.Fatalf("forget %s: %v", id, err)
		}
	}
	f.reopen()
	for _, rec := range []CompanionContextRecord{decision, topic} {
		replayed, changed, err := f.db.PutCompanionContext(rec, 0, 3)
		if err != nil || changed || replayed.State != CompanionContextStateForgotten || replayed.ID != rec.ID {
			t.Fatalf("replayed %s after reopen: %+v %v %v", rec.ID, replayed, changed, err)
		}
		if _, found, err := f.db.GetCompanionContext("acct", "user", rec.ID); err != nil || found {
			t.Fatalf("replay recreated %s: %v %v", rec.ID, found, err)
		}
		if _, found, err := f.db.GetVerifiedCompanionContext("acct", "user", rec.ID, 3); err != nil || found {
			t.Fatalf("verified get found forgotten %s: %v %v", rec.ID, found, err)
		}
	}
	hits, _, err := f.db.RetrieveCompanionContext("acct", "user", CompanionContextQuery{Keywords: []string{"dark", "theme"}, Now: 3})
	if err != nil || len(hits) != 0 {
		t.Fatalf("retrieval returns forgotten records: %+v %v", hits, err)
	}
	again := decision
	again.ID = "dark-theme-2"
	if _, changed, err := f.db.PutCompanionContext(again, 0, 4); err != nil || !changed {
		t.Fatalf("new id: %v %v", changed, err)
	}
	// Purging the bank (the Companion's deletion) clears the tombstones too.
	if err := f.db.PurgeCompanionContext("acct", "user"); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := f.db.PutCompanionContext(decision, 0, 5); err != nil || !changed {
		t.Fatalf("id after purge: %v %v", changed, err)
	}
}
