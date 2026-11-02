package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble"
)

type companionContextFixture struct {
	t        *testing.T
	path     string
	store    *Store
	db       *SessionStore
	requests int
}

func newCompanionContextFixture(t *testing.T) *companionContextFixture {
	t.Helper()
	f := &companionContextFixture{t: t, path: filepath.Join(t.TempDir(), "store")}
	f.open()
	t.Cleanup(func() {
		if f.store != nil {
			_ = f.store.Close()
		}
	})
	return f
}

func (f *companionContextFixture) open() {
	f.t.Helper()
	store, err := Open(f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	f.store, f.db = store, NewSessionStore(store)
}

func (f *companionContextFixture) reopen() {
	f.t.Helper()
	if err := f.store.Close(); err != nil {
		f.t.Fatal(err)
	}
	f.open()
}

func (f *companionContextFixture) createSession(account, user, id string) {
	f.t.Helper()
	_, err := f.db.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: id, AccountScopeID: account, UserID: user, Kind: V3SessionMutationCreateSession, ClientRequestID: "create-" + id, PayloadHash: "create-" + id, Session: &SessionSnapshot{ID: id, Mode: "auto", Metadata: map[string]any{SessionCompanionMetadataKey: map[string]any{"generation": 1}}}})
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *companionContextFixture) appendMessage(account, user, session, content string) uint64 {
	f.t.Helper()
	f.requests++
	request := fmt.Sprintf("msg-%s-%d", session, f.requests)
	result, err := f.db.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: session, AccountScopeID: account, UserID: user, Kind: V3SessionMutationAppendMessage, ClientRequestID: request, PayloadHash: request, Message: &MessageSnapshot{Role: "user", Content: content}})
	if err != nil {
		f.t.Fatal(err)
	}
	if result.Message == nil || result.Message.GlobalSeq == 0 {
		f.t.Fatalf("append returned no message sequence: %+v", result)
	}
	return result.Message.GlobalSeq
}

func (f *companionContextFixture) topic(account, user, session string, seq uint64, text string) CompanionContextRecord {
	f.t.Helper()
	rec, changed, err := f.db.PutCompanionContext(CompanionContextRecord{ID: CompanionMessageContextID(session, seq), AccountScopeID: account, UserID: user, Kind: CompanionContextKindTopic, Title: text, Summary: text, Source: CompanionContextSource{Kind: CompanionContextSourceMessage, SessionID: session, MessageSeq: seq}}, 0, int64(seq))
	if err != nil || !changed {
		f.t.Fatalf("index topic %d: changed=%v err=%v", seq, changed, err)
	}
	return rec
}

func companionReadBound(maxResults int) CompanionContextReadStats {
	return CompanionContextReadStats{IndexKeys: CompanionContextMaxQueryKeywords * CompanionContextIndexScanPerKeyword, Records: 2 * maxResults, Sources: 2 * maxResults}
}

func assertCompanionReadsWithin(t *testing.T, label string, got, bound CompanionContextReadStats) {
	t.Helper()
	if got.IndexKeys > bound.IndexKeys || got.Records > bound.Records || got.Sources > bound.Sources {
		t.Fatalf("%s read %+v, beyond the fixed bound %+v", label, got, bound)
	}
}

// Purpose: the Companion's earlier topics and decisions stay retrievable from a
// long history at a fixed read cost, with their original source, and the
// context bank is per account user and never touches account memory.
// Requirement: RetrieveCompanionContext reads at most a fixed number of index
// keys, records and sources however many topics exist; a topic switch and a
// return to an old topic both resolve from the keyword index (no history
// scan); results carry a verified source excerpt and digest; records, focus
// and indexes survive a reopen; another account or user sees nothing and
// cannot index another user's message; writes are idempotent and
// revision-guarded; ListCompanionContext pages without duplicates; account
// memory's revision is unchanged. Threat: per-turn cost growing with the
// conversation, a lost or cross-user topic, or the context bank silently
// becoming account memory. Boundary: SessionStore.PutCompanionContext,
// RetrieveCompanionContext, ListCompanionContext, Set/GetCompanionFocus and
// the V3 message store. Narrowest layer: a real Pebble store, reopened.
func TestCompanionContextBankBoundedRetrievalAcrossLargeHistory(t *testing.T) {
	f := newCompanionContextFixture(t)
	const account, user, session = "acct-a", "user-1", "companion-a"
	f.createSession(account, user, session)
	memoryBefore, err := NewMemoryStore(f.store).GetForAccount(account)
	if err != nil {
		t.Fatal(err)
	}

	printerSeq := f.appendMessage(account, user, session, "My printer driver keeps crashing the spooler on Windows")
	printer := f.topic(account, user, session, printerSeq, "My printer driver keeps crashing the spooler on Windows")
	// A long history on other topics, each indexed like a Companion turn.
	const filler = 400
	for i := 0; i < filler; i++ {
		text := fmt.Sprintf("Research battery chemistry note %d about solid electrolyte cells and cathode %d", i, i%7)
		seq := f.appendMessage(account, user, session, text)
		f.topic(account, user, session, seq, text)
	}
	if err := f.db.SetCompanionFocus(account, user, CompanionFocus{RecordID: "x", Keywords: []string{"battery", "electrolyte"}, MessageSeq: 9}); err != nil {
		t.Fatal(err)
	}

	bound := companionReadBound(8)
	// Return to the old topic: found through the keyword index, not a scan.
	hits, stats, err := f.db.RetrieveCompanionContext(account, user, CompanionContextQuery{Keywords: CompanionContextKeywords("Is the printer spooler fixed yet?"), MaxResults: 8})
	if err != nil {
		t.Fatal(err)
	}
	assertCompanionReadsWithin(t, "topic return", stats, bound)
	if len(hits) == 0 || hits[0].Record.ID != printer.ID {
		t.Fatalf("returning to the printer topic did not retrieve it first: %+v", hits)
	}
	if !strings.Contains(hits[0].SourceText, "spooler") || hits[0].Record.Source.MessageSeq != printerSeq || len(hits[0].Record.Source.ContentSHA256) != 64 {
		t.Fatalf("hit lacks verified source evidence: %+v", hits[0])
	}
	// The current topic dominates when switching to it.
	hits, stats, err = f.db.RetrieveCompanionContext(account, user, CompanionContextQuery{Keywords: CompanionContextKeywords("solid electrolyte cathode"), MaxResults: 8})
	if err != nil {
		t.Fatal(err)
	}
	assertCompanionReadsWithin(t, "topic switch", stats, bound)
	if len(hits) != 8 || hits[0].Record.ID == printer.ID {
		t.Fatalf("battery topic retrieval: %d hits, first %q", len(hits), hits[0].Record.ID)
	}
	// Excluding the recent tail drops sources the turn already shows.
	hits, _, err = f.db.RetrieveCompanionContext(account, user, CompanionContextQuery{Keywords: []string{"printer", "spooler"}, ExcludeSessionID: session, ExcludeFromSeq: printerSeq, MaxResults: 8})
	if err != nil || len(hits) != 0 {
		t.Fatalf("tail exclusion: hits=%d err=%v", len(hits), err)
	}

	// Idempotent and revision-guarded writes.
	again, changed, err := f.db.PutCompanionContext(CompanionContextRecord{ID: printer.ID, AccountScopeID: account, UserID: user, Kind: CompanionContextKindTopic, Title: printer.Title, Summary: printer.Summary, Source: CompanionContextSource{Kind: CompanionContextSourceMessage, SessionID: session, MessageSeq: printerSeq}}, 0, 999)
	if err != nil || changed || again.Revision != 1 {
		t.Fatalf("identical re-index: changed=%v rev=%d err=%v", changed, again.Revision, err)
	}
	if _, _, err := f.db.PutCompanionContext(CompanionContextRecord{ID: printer.ID, AccountScopeID: account, UserID: user, Kind: CompanionContextKindTopic, Title: "edited", Source: printer.Source}, 0, 999); !errors.Is(err, ErrCompanionContextConflict) {
		t.Fatalf("stale revision write: %v", err)
	}

	// Isolation: another user and another account see nothing and cannot
	// index this user's message.
	for _, other := range [][2]string{{account, "user-2"}, {"acct-b", user}} {
		hits, _, err := f.db.RetrieveCompanionContext(other[0], other[1], CompanionContextQuery{Keywords: []string{"printer", "spooler"}})
		if err != nil || len(hits) != 0 {
			t.Fatalf("%v sees %d records (err %v)", other, len(hits), err)
		}
		if _, ok, _ := f.db.GetCompanionContext(other[0], other[1], printer.ID); ok {
			t.Fatalf("%v reads the record by id", other)
		}
		_, _, err = f.db.PutCompanionContext(CompanionContextRecord{ID: "steal", AccountScopeID: other[0], UserID: other[1], Kind: CompanionContextKindDecision, Title: "steal", Source: CompanionContextSource{Kind: CompanionContextSourceMessage, SessionID: session, MessageSeq: printerSeq}}, 0, 1)
		if !errors.Is(err, ErrCompanionContextSource) {
			t.Fatalf("%v indexed a foreign message: %v", other, err)
		}
	}

	// Pagination: every record exactly once, each page bounded.
	seen := map[string]bool{}
	cursor, pages := "", 0
	for {
		page, next, stats, err := f.db.ListCompanionContext(account, user, cursor, 50)
		if err != nil {
			t.Fatal(err)
		}
		if stats.IndexKeys > 51 || stats.Records > 50 {
			t.Fatalf("page read %+v", stats)
		}
		for _, rec := range page {
			if seen[rec.ID] {
				t.Fatalf("record %s listed twice", rec.ID)
			}
			seen[rec.ID] = true
		}
		pages++
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != filler+1 || pages != 9 {
		t.Fatalf("listed %d records in %d pages", len(seen), pages)
	}
	if _, _, _, err := f.db.ListCompanionContext(account, user, "../../etc", 10); !errors.Is(err, ErrCompanionContextInvalid) {
		t.Fatalf("malformed cursor accepted: %v", err)
	}

	// Restart: records, indexes and focus are durable.
	f.reopen()
	hits, _, err = f.db.RetrieveCompanionContext(account, user, CompanionContextQuery{Keywords: []string{"printer", "driver"}, MaxResults: 4})
	if err != nil || len(hits) == 0 || hits[0].Record.ID != printer.ID {
		t.Fatalf("after reopen: %+v %v", hits, err)
	}
	if focus, ok, err := f.db.GetCompanionFocus(account, user); err != nil || !ok || strings.Join(focus.Keywords, ",") != "battery,electrolyte" {
		t.Fatalf("focus after reopen: %+v %v %v", focus, ok, err)
	}

	memoryAfter, err := NewMemoryStore(f.store).GetForAccount(account)
	if err != nil {
		t.Fatal(err)
	}
	if memoryAfter.Revision != memoryBefore.Revision || len(memoryAfter.Entries) != len(memoryBefore.Entries) {
		t.Fatalf("context bank changed account memory: %d/%d -> %d/%d", memoryBefore.Revision, len(memoryBefore.Entries), memoryAfter.Revision, len(memoryAfter.Entries))
	}
}

// Purpose: retrieved context is evidence only while its source still says so,
// and only inside the caller's current workspace permission scope.
// Requirement: a record whose source message changed or was deleted, or whose
// source session is gone, is not returned and is durably invalidated (kept
// listable with a reason, removed from retrieval); a workspace-bound decision
// is returned only within an authorized root, so revoking the workspace (or no
// scope) hides it; work records return the live source session; explicit
// delete and Companion purge remove records and focus. Threat: stale or
// deleted content presented as current, a revoked workspace's context leaking
// into a later turn, or path-prefix confusion (/ws/a vs /ws/ab). Boundary:
// verifyCompanionContextSource, invalidateCompanionContext,
// CompanionWorkspaceScope, DeleteCompanionContext, PurgeCompanionContext.
// Narrowest layer: a real Pebble store with injected source faults.
func TestCompanionContextSourceInvalidationAndWorkspaceScope(t *testing.T) {
	f := newCompanionContextFixture(t)
	const account, user, session, work = "acct-a", "user-1", "companion-a", "work-session"
	f.createSession(account, user, session)
	f.createSession(account, user, work)

	changedSeq := f.appendMessage(account, user, session, "Decide router firmware upgrade window tonight")
	deletedSeq := f.appendMessage(account, user, session, "Router firmware rollback plan if upgrade fails")
	changedRec := f.topic(account, user, session, changedSeq, "Decide router firmware upgrade window tonight")
	deletedRec := f.topic(account, user, session, deletedSeq, "Router firmware rollback plan if upgrade fails")

	// Fault injection: the first source's content changes, the second vanishes.
	key := KeyV3SessionMessage(session, changedSeq)
	var message MessageSnapshot
	if ok, err := getJSONFromReader(f.store.db, key, &message); err != nil || !ok {
		t.Fatalf("read source: %v %v", ok, err)
	}
	message.Content = "rewritten"
	raw, _ := json.Marshal(message)
	if err := f.store.db.Set([]byte(key), raw, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := f.store.db.Delete([]byte(KeyV3SessionMessage(session, deletedSeq)), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	hits, _, err := f.db.RetrieveCompanionContext(account, user, CompanionContextQuery{Keywords: []string{"router", "firmware"}})
	if err != nil || len(hits) != 0 {
		t.Fatalf("stale sources returned: %+v %v", hits, err)
	}
	for _, rec := range []CompanionContextRecord{changedRec, deletedRec} {
		got, ok, err := f.db.GetCompanionContext(account, user, rec.ID)
		if err != nil || !ok || got.State != CompanionContextStateInvalidated || got.InvalidReason == "" || got.Revision != rec.Revision+1 {
			t.Fatalf("record %s not invalidated: %+v", rec.ID, got)
		}
	}

	// Workspace scope and revocation.
	decisionSeq := f.appendMessage(account, user, session, "We chose Postgres for the inventory service")
	decision, _, err := f.db.PutCompanionContext(CompanionContextRecord{ID: "decision-db", AccountScopeID: account, UserID: user, Kind: CompanionContextKindDecision, Title: "Use Postgres", Summary: "inventory service database", WorkspacePath: "/ws/a/inventory", Source: CompanionContextSource{Kind: CompanionContextSourceMessage, SessionID: session, MessageSeq: decisionSeq}}, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		roots []string
		want  int
	}{{[]string{"/ws/a"}, 1}, {[]string{"/ws/ab"}, 0}, {[]string{"/ws/b"}, 0}, {nil, 0}, {[]string{"relative/ws"}, 0}} {
		hits, _, err := f.db.RetrieveCompanionContext(account, user, CompanionContextQuery{Kind: CompanionContextKindDecision, AllowWorkspace: CompanionWorkspaceScope(tc.roots)})
		if err != nil || len(hits) != tc.want {
			t.Fatalf("roots %v: %d hits, want %d (%v)", tc.roots, len(hits), tc.want, err)
		}
	}

	// Work pointer: status comes from the live session at retrieval time.
	if _, _, err := f.db.PutCompanionContext(CompanionContextRecord{ID: "work-1", AccountScopeID: account, UserID: user, Kind: CompanionContextKindWork, Title: "Battery research", Source: CompanionContextSource{Kind: CompanionContextSourceSession, SessionID: work}}, 0, 11); err != nil {
		t.Fatal(err)
	}
	hits, _, err = f.db.RetrieveCompanionContext(account, user, CompanionContextQuery{Kind: CompanionContextKindWork})
	if err != nil || len(hits) != 1 || hits[0].SourceSession == nil || hits[0].SourceSession.ID != work {
		t.Fatalf("work pointer: %+v %v", hits, err)
	}
	if err := f.db.DeleteSession(work); err != nil {
		t.Fatal(err)
	}
	hits, _, err = f.db.RetrieveCompanionContext(account, user, CompanionContextQuery{Kind: CompanionContextKindWork})
	if err != nil || len(hits) != 0 {
		t.Fatalf("deleted work session still returned: %+v %v", hits, err)
	}

	// Explicit delete is revision-guarded and clears a matching focus.
	if err := f.db.SetCompanionFocus(account, user, CompanionFocus{RecordID: decision.ID, Keywords: []string{"postgres"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.DeleteCompanionContext(account, user, decision.ID, decision.Revision+5); !errors.Is(err, ErrCompanionContextConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	if err := f.db.DeleteCompanionContext(account, user, decision.ID, decision.Revision); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := f.db.GetCompanionFocus(account, user); ok {
		t.Fatal("focus on a deleted record survived")
	}
	if hits, _, _ := f.db.RetrieveCompanionContext(account, user, CompanionContextQuery{Keywords: []string{"postgres"}, AllowWorkspace: CompanionWorkspaceScope([]string{"/ws/a"})}); len(hits) != 0 {
		t.Fatalf("deleted decision still indexed: %+v", hits)
	}

	// Purge removes everything for this user only.
	f.createSession("acct-b", user, "companion-b")
	otherSeq := f.appendMessage("acct-b", user, "companion-b", "Other account garden irrigation schedule")
	f.topic("acct-b", user, "companion-b", otherSeq, "Other account garden irrigation schedule")
	if err := f.db.PurgeCompanionContext(account, user); err != nil {
		t.Fatal(err)
	}
	if records, _, _, err := f.db.ListCompanionContext(account, user, "", 10); err != nil || len(records) != 0 {
		t.Fatalf("purge left %d records (%v)", len(records), err)
	}
	if records, _, _, err := f.db.ListCompanionContext("acct-b", user, "", 10); err != nil || len(records) != 1 {
		t.Fatalf("purge touched another account: %d (%v)", len(records), err)
	}
}
