package pebblestore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestV3SessionSearchMigrationRebuildsVerifiesAndDeletesLegacyPostings(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	workspace := t.TempDir()
	createSearchTestSession(t, sessions, SessionSnapshot{ID: "migrate-search", UserID: "user-1", AccountScopeID: "acct-1", WorkspacePath: workspace, Title: "Title Alpha", CreatedAt: 1000, UpdatedAt: 1000})
	appendSearchTestMessage(t, sessions, "migrate-search", "user-1", "acct-1", "body beta", 2000)

	legacyTitle := keyV3SessionSearchAccount("acct-1", false, "alpha", 2000, "migrate-search")
	legacyBody := keyV3SessionSearchAccount("acct-1", false, "beta", 2000, "migrate-search")
	if err := store.PutJSON(legacyTitle, v3SessionSearchIndexRecord{SessionID: "migrate-search"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutJSON(legacyBody, v3SessionSearchIndexRecord{SessionID: "migrate-search", Snippet: &V3SessionSearchSnippet{Source: "message", MessageID: "legacy", Text: "body beta"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutJSON(keyV3SessionSearchMeta("migrate-search"), v3SessionSearchSessionMeta{SessionID: "migrate-search", Keys: []string{legacyTitle, legacyBody}}); err != nil {
		t.Fatal(err)
	}

	result, err := sessions.RunV3SessionSearchMigrationPass(context.Background(), time.UnixMilli(3000), 1)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if result.SessionsMigrated != 1 || result.SessionsDeferred != 0 {
		t.Fatalf("result = %+v", result)
	}
	for _, key := range []string{legacyTitle, legacyBody} {
		if _, ok, err := store.GetBytes(key); err != nil || ok {
			t.Fatalf("legacy posting %q retained ok=%v err=%v", key, ok, err)
		}
	}
	var meta v3SessionSearchSessionMeta
	if ok, err := store.GetJSON(keyV3SessionSearchMeta("migrate-search"), &meta); err != nil || !ok || meta.Version != v3SessionSearchIndexVersion || len(meta.Keys) != 0 {
		t.Fatalf("stable meta = %+v ok=%v err=%v", meta, ok, err)
	}
	for _, query := range []string{"alpha", "beta"} {
		search, err := sessions.SearchV3Sessions(V3SessionSearchOptions{AccountScopeID: "acct-1", UserID: "user-1", Global: true, Query: query, Limit: 10})
		if err != nil || len(search.Items) != 1 || search.Items[0].ID != "migrate-search" {
			t.Fatalf("search %q = %+v err=%v", query, search.Items, err)
		}
		if query == "beta" && (len(search.Items[0].Snippets) == 0 || search.Items[0].Snippets[0].Text != "body beta") {
			t.Fatalf("message snippet after migration = %+v", search.Items[0].Snippets)
		}
	}
}

func TestV3SessionSearchMigrationResumesWithoutRescanningCommittedSession(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	for _, id := range []string{"resume-a", "resume-b"} {
		createSearchTestSession(t, sessions, SessionSnapshot{ID: id, UserID: "user-1", AccountScopeID: "acct-1", WorkspacePath: t.TempDir(), Title: id, CreatedAt: 1000, UpdatedAt: 1000})
		var meta v3SessionSearchSessionMeta
		if ok, err := store.GetJSON(keyV3SessionSearchMeta(id), &meta); err != nil || !ok {
			t.Fatalf("meta %s ok=%v err=%v", id, ok, err)
		}
		meta.Version = 0
		meta.Keys = []string{}
		if err := store.PutJSON(keyV3SessionSearchMeta(id), meta); err != nil {
			t.Fatal(err)
		}
	}

	first, err := sessions.RunV3SessionSearchMigrationPass(context.Background(), time.UnixMilli(2000), 1)
	if err != nil || first.SessionsScanned != 1 {
		t.Fatalf("first = %+v err=%v", first, err)
	}
	state, ok, err := sessions.GetV3SessionSearchMigrationState()
	if err != nil || !ok || state.ResumeKey == "" {
		t.Fatalf("state = %+v ok=%v err=%v", state, ok, err)
	}
	firstResume := state.ResumeKey
	second, err := sessions.RunV3SessionSearchMigrationPass(context.Background(), time.UnixMilli(3000), 1)
	if err != nil || second.SessionsScanned != 1 {
		t.Fatalf("second = %+v err=%v", second, err)
	}
	state, _, _ = sessions.GetV3SessionSearchMigrationState()
	if state.ResumeKey == firstResume {
		t.Fatalf("resume key did not advance: %+v", state)
	}
}

func TestV3SessionSearchMigrationFailClosedRetainsLegacyData(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createSearchTestSession(t, sessions, SessionSnapshot{ID: "defer-search", UserID: "user-1", AccountScopeID: "acct-1", WorkspacePath: t.TempDir(), Title: "alpha", CreatedAt: 1000, UpdatedAt: 1000})
	legacy := keyV3SessionSearchAccount("acct-1", false, "alpha", 1000, "defer-search")
	if err := store.PutJSON(legacy, v3SessionSearchIndexRecord{SessionID: "defer-search"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutJSON(keyV3SessionSearchMeta("defer-search"), v3SessionSearchSessionMeta{SessionID: "defer-search", Keys: []string{legacy}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(KeyV3SessionProjection("defer-search")); err != nil {
		t.Fatal(err)
	}

	result, err := sessions.RunV3SessionSearchMigrationPass(context.Background(), time.UnixMilli(2000), 1)
	if err != nil || result.SessionsDeferred != 1 || result.SessionsMigrated != 0 {
		t.Fatalf("result = %+v err=%v", result, err)
	}
	if _, ok, err := store.GetBytes(legacy); err != nil || !ok {
		t.Fatalf("legacy data was not retained ok=%v err=%v", ok, err)
	}
}

func TestV3SessionSearchMigrationCommitFailureLeavesRowsAndProgressUnchanged(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createSearchTestSession(t, sessions, SessionSnapshot{ID: "atomic-search", UserID: "user-1", AccountScopeID: "acct-1", WorkspacePath: t.TempDir(), Title: "alpha", CreatedAt: 1000, UpdatedAt: 1000})
	legacy := keyV3SessionSearchAccount("acct-1", false, "alpha", 1000, "atomic-search")
	if err := store.PutJSON(legacy, v3SessionSearchIndexRecord{SessionID: "atomic-search"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutJSON(keyV3SessionSearchMeta("atomic-search"), v3SessionSearchSessionMeta{SessionID: "atomic-search", Keys: []string{legacy}}); err != nil {
		t.Fatal(err)
	}
	store.sessionMutations.beforeSearchMigrationCommit = func(string) error { return errors.New("injected") }
	_, err := sessions.RunV3SessionSearchMigrationPass(context.Background(), time.UnixMilli(2000), 1)
	if err == nil {
		t.Fatal("expected injected commit error")
	}
	if _, ok, _ := store.GetBytes(legacy); !ok {
		t.Fatal("legacy posting deleted despite failed commit")
	}
	if _, ok, _ := sessions.GetV3SessionSearchMigrationState(); ok {
		t.Fatal("migration progress advanced despite failed commit")
	}
}

func TestLegacySessionNamespaceCleanupAuditDefersEvtAndMsg(t *testing.T) {
	if prefixes := ProvenRedundantLegacySessionPrefixes(); len(prefixes) != 0 {
		t.Fatalf("unproven cleanup prefixes = %#v", prefixes)
	}
	decisions := LegacySessionNamespaceCleanupAudit()
	if len(decisions) != 3 {
		t.Fatalf("audit decisions = %#v", decisions)
	}
	for _, decision := range decisions {
		if decision.Eligible || decision.Reason == "" {
			t.Fatalf("unsafe audit decision = %+v", decision)
		}
	}
}
