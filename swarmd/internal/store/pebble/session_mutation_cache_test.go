package pebblestore

import (
	"reflect"
	"testing"
)

// Purpose: applyFreshV3SessionMutation must decode the session record once while
// preserving append, usage, event and persisted projection results. A real
// temporary Pebble store is the narrowest layer exercising all helper reads.
func TestMutationSessionCacheAppendUsage(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	createV3SessionForTest(t, s, "cache-session")
	before, ok, err := s.GetSession("cache-session")
	if err != nil || !ok {
		t.Fatal(err)
	}
	decodes := 0
	s.sessionDecoded = func(id string) {
		if id == "cache-session" {
			decodes++
		}
	}
	input := V3SessionMutationInput{
		SessionID: "cache-session", UserID: before.UserID, AccountScopeID: before.AccountScopeID,
		Kind: V3SessionMutationAppendMessage, IdempotencyKey: "cache-append", RequestHash: "cache-hash", NowUnixMs: 2000,
		Message:   &MessageSnapshot{ID: "cache-message", Role: "assistant", Content: "answer"},
		TurnUsage: &SessionTurnUsageSnapshot{RunID: "cache-run", Provider: "test", Model: "test", InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CostProvenance: "provider", PriceStatus: "priced"},
	}
	unlock := s.store.sessionMutations.lockSessions(input.SessionID, "account:"+input.AccountScopeID)
	defer unlock()
	input.accountLockHeld = true
	result, err := s.applyFreshV3SessionMutation(input, "cache-idempotency")
	if err != nil {
		t.Fatal(err)
	}
	if decodes != 1 {
		t.Fatalf("storage decodes = %d, want 1", decodes)
	}
	s.sessionDecoded = nil
	uncached := NewSessionStore(openV3SessionEventTestStore(t))
	createV3SessionForTest(t, uncached, "cache-session")
	uncachedDecodes := 0
	uncached.sessionDecoded = func(string) { uncachedDecodes++ }
	unlockReference := uncached.store.sessionMutations.lockSessions(input.SessionID, "account:"+input.AccountScopeID)
	defer unlockReference()
	want, err := uncached.applyFreshV3SessionMutationWithReader(input, "cache-idempotency")
	if err != nil {
		t.Fatal(err)
	}
	if uncachedDecodes <= 1 {
		t.Fatalf("reference did not exercise uncached reads: %d", uncachedDecodes)
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("cached result differs from uncached:\ngot %+v\nwant %+v", result, want)
	}
	// Compare every persisted record, including session, usage, scope totals,
	// summaries, events and outbox, without going through session read helpers.
	records := func(store *Store) map[string]string {
		t.Helper()
		values := make(map[string]string)
		if err := scanRangeFromReader(store.db, scanRangeOptions{LowerBound: "\x00", UpperBound: "\xff", Limit: 1000}, func(key string, value []byte) (bool, error) {
			// repositoryHistoryRevision generates a fresh opaque random token.
			// Compare its presence/size, not independently generated entropy.
			if key == repositoryHistoryRevisionKey+"/cache-session" {
				if len(value) != 32 {
					t.Fatalf("invalid history revision size: %d", len(value))
				}
				values[key] = "opaque revision"
			} else {
				values[key] = string(value)
			}
			return true, nil
		}); err != nil {
			t.Fatal(err)
		}
		if len(values) == 0 {
			t.Fatal("empty record comparison")
		}
		return values
	}
	if got, want := records(s.store), records(uncached.store); !reflect.DeepEqual(got, want) {
		for key, value := range got {
			if value != want[key] {
				t.Errorf("record %q differs: got %q want %q", key, value, want[key])
			}
		}
		for key := range want {
			if _, ok := got[key]; !ok {
				t.Errorf("missing record %q", key)
			}
		}
	}
	after, ok, err := s.GetSession("cache-session")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if after.MessageCount != before.MessageCount+1 || after.LastMessageAt != 2000 || after.Title != before.Title || !reflect.DeepEqual(after.Metadata, before.Metadata) {
		t.Fatalf("session changed unexpectedly: before=%+v after=%+v", before, after)
	}
	if result.Message == nil || result.Message.Content != "answer" || result.Message.GlobalSeq != result.PrimarySeq || result.TurnUsage == nil || result.TurnUsage.TotalTokens != 15 || result.UsageSummary == nil || result.UsageSummary.TotalTokens != 15 || result.RealtimeOutbox == nil || result.RealtimeOutbox.Event.Seq != result.PrimarySeq {
		t.Fatalf("append result: %+v", result)
	}
	if result.Session == nil || !reflect.DeepEqual(*result.Session, after) {
		t.Fatalf("result session differs from storage: %+v / %+v", result.Session, after)
	}
}

// Purpose: setSessionInBatch must provide read-your-writes without leaking
// mutable aliases or uncommitted state to other readers. A discarded real batch
// checks the failure boundary as well as successive reads on a private receiver.
func TestMutationSessionCacheWriteIsolation(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	createV3SessionForTest(t, s, "cache-session")
	local := s.withMutationSessionCache()
	original, _, err := s.GetSession("cache-session")
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := local.GetSession("cache-session")
	if err != nil {
		t.Fatal(err)
	}
	current.Title = "updated"
	current.Metadata = map[string]any{"nested": map[string]any{"value": "saved"}}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := local.setSessionInBatch(batch, current); err != nil {
		t.Fatal(err)
	}
	current.Metadata["nested"].(map[string]any)["value"] = "caller mutation"
	got, _, err := local.GetSession("cache-session")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "updated" || got.Metadata["nested"].(map[string]any)["value"] != "saved" {
		t.Fatalf("stale or aliased read: %+v", got)
	}
	got.Metadata["nested"].(map[string]any)["value"] = "reader mutation"
	again, _, err := local.GetSession("cache-session")
	if err != nil {
		t.Fatal(err)
	}
	if again.Metadata["nested"].(map[string]any)["value"] != "saved" {
		t.Fatal("read mutated cache")
	}
	outside, _, err := s.GetSession("cache-session")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(outside, original) {
		t.Fatal("uncommitted write escaped mutation")
	}
	fresh, _, err := s.withMutationSessionCache().GetSession("cache-session")
	if err != nil || !reflect.DeepEqual(fresh, original) {
		t.Fatal("cache survived mutation", err)
	}
}
