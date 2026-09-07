package pebblestore

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func historyCreate(t *testing.T, sessions *SessionStore, id, account, user, parent string) SessionSnapshot {
	t.Helper()
	snapshot := SessionSnapshot{ID: id, UserID: user, AccountScopeID: account, WorkspacePath: filepath.Join(t.TempDir(), "source"), Metadata: map[string]any{"parent_session_id": parent, "integration_status": "dirty-recoverable"}}
	_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: id, UserID: user, AccountScopeID: account, Kind: V3SessionMutationCreateSession, Session: &snapshot, IdempotencyKey: "create-" + id, RequestHash: "create-" + id, NowUnixMs: 100})
	if err != nil { t.Fatal(err) }
	return snapshot
}

func historyReady(t *testing.T, sessions *SessionStore) {
	t.Helper()
	for i := 0; i < 100; i++ {
		ready, err := sessions.BackfillRepositoryHistory(2)
		if err != nil { t.Fatal(err) }
		if ready { return }
	}
	t.Fatal("bounded migration did not finish")
}

// Purpose: retained child/default contexts must survive lifecycle and pagination
// without crossing principal/parent boundaries. ApplyV3SessionMutation and the
// repository index own this invariant; a hermetic store test is the narrowest
// layer that proves durable rows and opaque cursor rejection together.
func TestRepositoryHistoryIsolationPaginationAndStale(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	parent := historyCreate(t, sessions, "parent", "account", "user", "")
	historyCreate(t, sessions, "other-parent", "account", "user", "")
	for i := 0; i < 5; i++ { historyCreate(t, sessions, fmt.Sprintf("child-%d", i), "account", "user", "parent") }
	historyCreate(t, sessions, "foreign-account", "foreign", "user", "parent")
	historyCreate(t, sessions, "foreign-user", "account", "foreign", "parent")
	historyCreate(t, sessions, "foreign-parent", "account", "user", "other-parent")
	q := RepositoryHistoryQuery{AccountScopeID: "account", UserID: "user", ParentSessionID: "parent", Limit: 2}
	if _, err := sessions.ListSessionRepositoryHistory(q); !errors.Is(err, ErrRepositoryHistoryNotReady) { t.Fatalf("read must not migrate: %v", err) }
	historyReady(t, sessions)
	first, err := sessions.ListSessionRepositoryHistory(q)
	if err != nil || len(first.Sessions) != 2 || first.NextCursor == "" { t.Fatalf("first page: %+v %v", first, err) }
	seen := map[string]bool{}
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		page, err := sessions.ListSessionRepositoryHistory(q)
		if err != nil { t.Fatal(err) }
		for _, row := range page.Sessions {
			if seen[row.Session.ID] { t.Fatalf("duplicate %s", row.Session.ID) }
			seen[row.Session.ID] = true
			if row.Session.UserID != "user" || row.Session.AccountScopeID != "account" { t.Fatal("principal leak") }
		}
		q.Cursor = page.NextCursor
		if q.Cursor == "" { break }
	}
	if len(seen) != 6 || seen["foreign-parent"] { t.Fatalf("wrong parent rows: %v", seen) }
	q.Cursor = first.NextCursor
	for _, mutate := range []func(*RepositoryHistoryQuery){
		func(q *RepositoryHistoryQuery) { q.AccountScopeID = "foreign" },
		func(q *RepositoryHistoryQuery) { q.UserID = "foreign" },
		func(q *RepositoryHistoryQuery) { q.ParentSessionID = "other-parent" },
		func(q *RepositoryHistoryQuery) { q.Limit = 3 },
		func(q *RepositoryHistoryQuery) { q.Cursor = "tampered" },
	} {
		bad := q; mutate(&bad)
		if page, err := sessions.ListSessionRepositoryHistory(bad); err == nil || len(page.Sessions) != 0 { t.Fatalf("invalid scope/cursor accepted: %+v %v", page, err) }
	}
	// A new default must not erase the previous repository context.
	parent.WorkspacePath = filepath.Join(t.TempDir(), "new-default")
	_, err = sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: parent.ID, AccountScopeID: "account", UserID: "user", Kind: V3SessionMutationUpdateMetadata, Session: &parent, IdempotencyKey: "default-change", RequestHash: "default-change", NowUnixMs: 200})
	if err != nil { t.Fatal(err) }
	if _, err := sessions.ListSessionRepositoryHistory(q); !errors.Is(err, ErrRepositoryHistoryCursor) { t.Fatalf("stale cursor: %v", err) }
	if err := sessions.ArchiveSession("child-0"); err != nil { t.Fatal(err) }
	q.Cursor, q.Limit = "", 100
	page, err := sessions.ListSessionRepositoryHistory(q)
	if err != nil || len(page.Sessions) != 7 { t.Fatalf("lost historical context: %+v %v", page, err) }
	archived := false
	for _, row := range page.Sessions {
		if row.Session.ID == "child-0" {
			archived = row.Archived
			if row.Session.Metadata["integration_status"] != "dirty-recoverable" { t.Fatal("lost retained worker state") }
		}
	}
	if !archived { t.Fatal("archived child missing") }
}

// Purpose: a read-only lane page must not reconcile a running program, write
// metadata, or lose continuation across restart. The store boundary proves the
// exact bytes and durable cursor key without requiring a scheduler/filesystem.
func TestRepositoryHistoryProgramsReadOnlyRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.pebble")
	store, err := Open(path)
	if err != nil { t.Fatal(err) }
	sessions := NewSessionStore(store)
	historyCreate(t, sessions, "parent", "account", "user", "")
	historyReady(t, sessions)
	for i := 0; i < 3; i++ {
		record := TaskProgramRecord{ParentSessionID: "parent", ProgramID: fmt.Sprintf("program-%d", i), State: TaskProgramStateRunning, Revision: 7, RepositoryLane: &TaskProgramRepositoryLane{SourcePath: "source", WorkspacePath: "lane", Branch: "branch", BaseCommit: "base"}, Jobs: []TaskProgramJobRecord{{JobID: "job", State: TaskProgramJobFailed, IntegrationState: "dirty-recoverable", GenerationHistory: []TaskProgramJobGeneration{{SessionID: "rotated", Generation: 1, State: "completed"}}}}}
		if err := sessions.putTaskProgramHistory(record); err != nil { t.Fatal(err) }
	}
	before, _, err := store.GetBytes(KeyTaskProgram("parent", "program-0"))
	if err != nil { t.Fatal(err) }
	q := RepositoryHistoryQuery{AccountScopeID: "account", UserID: "user", ParentSessionID: "parent", Limit: 1}
	page, err := sessions.ListTaskProgramRepositoryHistory(q)
	if err != nil || len(page.Programs) != 1 || page.NextCursor == "" { t.Fatalf("program page: %+v %v", page, err) }
	if page.Programs[0].State != TaskProgramStateRunning || page.Programs[0].Revision != 7 || page.Programs[0].Jobs[0].GenerationHistory[0].SessionID != "rotated" { t.Fatal("read reconciled or lost history") }
	after, _, err := store.GetBytes(KeyTaskProgram("parent", "program-0"))
	if err != nil || !bytes.Equal(before, after) { t.Fatal("read mutated program") }
	q.Cursor = page.NextCursor
	if err := store.Close(); err != nil { t.Fatal(err) }
	store, err = Open(path)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	sessions = NewSessionStore(store)
	page, err = sessions.ListTaskProgramRepositoryHistory(q)
	if err != nil || len(page.Programs) != 1 || page.Programs[0].ProgramID != "program-1" { t.Fatalf("restart continuation: %+v %v", page, err) }
	if _, err := sessions.ListSessionRepositoryHistory(q); !errors.Is(err, ErrRepositoryHistoryCursor) { t.Fatalf("cross-kind cursor accepted: %v", err) }
}

// Purpose: corrupt migration input must leave both progress and indexed rows
// unchanged. BackfillRepositoryHistory's single batch is the atomic authority;
// an injected malformed durable row proves rollback without ambient services.
func TestRepositoryHistoryBackfillFailureAtomic(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	historyCreate(t, sessions, "parent", "account", "user", "")
	before, _, err := store.GetBytes(repositoryHistoryRevisionKey)
	if err != nil { t.Fatal(err) }
	if err := store.db.Set([]byte(KeySession("zz-corrupt")), []byte("{"), nil); err != nil { t.Fatal(err) }
	if _, err := sessions.BackfillRepositoryHistory(100); err == nil { t.Fatal("corruption accepted") }
	var meta repositoryHistoryMeta
	if ok, err := store.GetJSON(repositoryHistoryMetaKey, &meta); err != nil || ok { t.Fatalf("partial migration progress: %+v %v", meta, err) }
	after, _, err := store.GetBytes(repositoryHistoryRevisionKey)
	if err != nil || !bytes.Equal(before, after) { t.Fatal("failed migration committed partial index") }
}
