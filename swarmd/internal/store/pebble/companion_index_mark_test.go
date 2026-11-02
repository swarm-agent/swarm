package pebblestore

import "testing"

// Purpose: Companion indexing happens at most once per message across run
// replays and daemon restarts. Requirement: AdvanceCompanionIndexMark only
// moves forward for the same Companion session, survives a reopen of the
// store, is replaced (not compared) for a new Companion generation's session,
// is isolated per account user, and is removed with the context bank by
// PurgeCompanionContext; invalid principals are refused without writing.
// Threat: a replayed or restarted run re-indexing (and resurrecting forgotten
// topics), one user's mark suppressing another's indexing, a new Companion
// inheriting the deleted one's position. Boundary: GetCompanionIndexMark,
// AdvanceCompanionIndexMark, PurgeCompanionContext. Narrowest layer: the store
// over a real Pebble database reopened at the same path.
func TestCompanionIndexMarkIsMonotonicDurableAndScoped(t *testing.T) {
	f := newCompanionContextFixture(t)
	advance := func(account, user, session string, seq uint64, wantMoved bool) {
		t.Helper()
		moved, err := f.db.AdvanceCompanionIndexMark(account, user, session, seq)
		if err != nil || moved != wantMoved {
			t.Fatalf("advance %s/%s %s to %d: moved=%v err=%v, want moved=%v", account, user, session, seq, moved, err, wantMoved)
		}
	}
	mark := func(account, user string) CompanionIndexMark {
		t.Helper()
		got, err := f.db.GetCompanionIndexMark(account, user)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := mark("acct", "user"); got != (CompanionIndexMark{}) {
		t.Fatalf("fresh mark = %+v", got)
	}
	advance("acct", "user", "companion-1", 10, true)
	advance("acct", "user", "companion-1", 10, false)
	advance("acct", "user", "companion-1", 4, false)
	f.reopen()
	if got := mark("acct", "user"); got != (CompanionIndexMark{SessionID: "companion-1", MessageSeq: 10}) {
		t.Fatalf("mark after restart = %+v", got)
	}
	if got := mark("acct", "other"); got != (CompanionIndexMark{}) {
		t.Fatalf("another user's mark = %+v, want none", got)
	}
	// A new Companion generation starts from its own position.
	advance("acct", "user", "companion-2", 3, true)
	if got := mark("acct", "user"); got != (CompanionIndexMark{SessionID: "companion-2", MessageSeq: 3}) {
		t.Fatalf("mark for the new Companion = %+v", got)
	}
	if _, err := f.db.AdvanceCompanionIndexMark("", "user", "companion-2", 9); err == nil {
		t.Fatal("advance without an account was accepted")
	}
	if _, err := f.db.AdvanceCompanionIndexMark("acct", "user", " ", 9); err == nil {
		t.Fatal("advance without a session was accepted")
	}
	if got := mark("acct", "user"); got.MessageSeq != 3 {
		t.Fatalf("a refused advance changed the mark: %+v", got)
	}
	if err := f.db.PurgeCompanionContext("acct", "user"); err != nil {
		t.Fatal(err)
	}
	if got := mark("acct", "user"); got != (CompanionIndexMark{}) {
		t.Fatalf("mark after purge = %+v", got)
	}
}
