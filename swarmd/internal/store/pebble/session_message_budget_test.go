package pebblestore

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Requirement: whole-message tail pages must be bounded without losing Unicode
// content or usage metadata. The reader is the narrowest boundary proving scan
// stopping, chronological continuation, and snapshot isolation under appends.
func TestV3MessageByteBudgetReconstruction(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	want := []MessageSnapshot{}
	for i := 1; i <= 40; i++ {
		content := strings.Repeat("界", 6000)
		if i == 12 || i == 40 {
			content = strings.Repeat("界", 400000)
		}
		message := MessageSnapshot{ID: fmt.Sprint(i), SessionID: "budget", GlobalSeq: uint64(i), Content: content, Metadata: map[string]any{"usage": strings.Repeat("x", 40000)}}
		if err := store.PutJSON(KeyV3SessionMessage("budget", uint64(i)), message); err != nil {
			t.Fatal(err)
		}
		want = append(want, message)
	}
	snapshot := store.db.NewSnapshot()
	defer snapshot.Close()
	if err := store.PutJSON(KeyV3SessionMessage("budget", 41), MessageSnapshot{ID: "later", GlobalSeq: 41}); err != nil {
		t.Fatal(err)
	}
	got := []MessageSnapshot{}
	before := uint64(0)
	for page := 0; page < 41; page++ {
		messages, more, err := listV3SessionMessagesBeforeByteBudget(snapshot, "budget", before, 200, V3RecentMessageByteBudget)
		if err != nil || len(messages) == 0 {
			t.Fatalf("page %d: count=%d error=%v", page, len(messages), err)
		}
		wire, err := json.Marshal(messages)
		if err != nil {
			t.Fatal(err)
		}
		if len(wire) > V3RecentMessageByteBudget && len(messages) != 1 {
			t.Fatalf("oversized multi-record page: %d bytes", len(wire))
		}
		got = append(messages, got...)
		before = messages[0].GlobalSeq
		if !more {
			break
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("pages did not reconstruct exact snapshot content and metadata")
	}
}

// Requirement: once a page is full, older values must not be decoded. Corrupt
// out-of-page data must not break initial hydration, but explicit retrieval must
// surface the error rather than silently hiding history.
func TestV3MessageByteBudgetStopsBeforeDecode(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	if err := store.PutBytes(KeyV3SessionMessage("budget", 1), []byte("invalid JSON")); err != nil {
		t.Fatal(err)
	}
	if err := store.PutJSON(KeyV3SessionMessage("budget", 2), MessageSnapshot{GlobalSeq: 2, Content: "whole"}); err != nil {
		t.Fatal(err)
	}
	messages, more, err := listV3SessionMessagesBeforeByteBudget(store.db, "budget", 0, 1, V3RecentMessageByteBudget)
	if err != nil || !more || len(messages) != 1 || messages[0].GlobalSeq != 2 {
		t.Fatalf("initial page: count=%d more=%v err=%v", len(messages), more, err)
	}
	if _, _, err := listV3SessionMessagesBeforeByteBudget(store.db, "budget", 2, 1, V3RecentMessageByteBudget); err == nil {
		t.Fatal("corrupt older record silently ignored")
	}
}

// Requirement: BuildV3SyncSnapshot applies byte bounds within its authorized
// snapshot, emits a truthful partial-history omission, and never leaks messages
// to another account. This integration layer exercises the actual selector.
func TestV3MessageByteBudgetSnapshot(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "budget", "user-1", "account-1", "/workspace/budget", 1000)
	for i := 0; i < 8; i++ {
		appendV3SessionMessageForStoreTest(t, sessions, "budget", fmt.Sprintf("m%d", i), strings.Repeat("界", 20000), "user-1", "account-1")
	}
	options := V3SyncSnapshotOptions{AccountScopeID: "account-1", UserID: "user-1", SessionIDs: []string{"budget"}, History: V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeTail, IncludeMessages: true, MaxMessagesPerSession: 200, MaxMessageBytesPerSession: V3RecentMessageByteBudget, ManifestPolicy: V3SyncSnapshotManifestPolicyManifest}}
	result, err := sessions.BuildV3SyncSnapshot(options)
	if err != nil {
		t.Fatal(err)
	}
	messages := result.MessagesBySession["budget"]
	wire, err := json.Marshal(messages)
	if err != nil || len(messages) == 0 || len(messages) >= 8 || len(wire) > V3RecentMessageByteBudget {
		t.Fatalf("unbounded snapshot: count=%d bytes=%d err=%v", len(messages), len(wire), err)
	}
	found := false
	for _, omission := range result.Omissions {
		if omission.SessionID == "budget" && omission.Resource == "messages" {
			found = omission.NextCursor == fmt.Sprintf("budget:messages:before:%d", messages[0].GlobalSeq)
		}
	}
	if !found {
		t.Fatal("missing backwards continuation omission")
	}
	options.AccountScopeID = "other-account"
	denied, err := sessions.BuildV3SyncSnapshot(options)
	if err == nil && (len(denied.MessagesBySession) != 0 || len(denied.SessionsByID) != 0) {
		t.Fatal("foreign account received session state")
	}
	all, err := sessions.ListV3SessionMessages("budget", 0, 200)
	if err != nil || len(all) != 8 {
		t.Fatalf("read altered durable history: count=%d err=%v", len(all), err)
	}
}
