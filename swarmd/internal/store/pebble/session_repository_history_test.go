package pebblestore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func historyCreate(t *testing.T, sessions *SessionStore, id, account, user, parent string) SessionSnapshot {
	t.Helper()
	snapshot := SessionSnapshot{ID: id, UserID: user, AccountScopeID: account, WorkspacePath: filepath.Join(t.TempDir(), "source"), Metadata: map[string]any{"parent_session_id": parent, "integration_status": "dirty-recoverable"}}
	_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: id, UserID: user, AccountScopeID: account, Kind: V3SessionMutationCreateSession, Session: &snapshot, IdempotencyKey: "create-" + id, RequestHash: "create-" + id, NowUnixMs: 100})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func historyReady(t *testing.T, sessions *SessionStore) {
	t.Helper()
	for i := 0; i < 100; i++ {
		ready, err := sessions.BackfillRepositoryHistory(2)
		if err != nil {
			t.Fatal(err)
		}
		if ready {
			return
		}
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
	for i := 0; i < 5; i++ {
		historyCreate(t, sessions, fmt.Sprintf("child-%d", i), "account", "user", "parent")
	}
	historyCreate(t, sessions, "foreign-account", "foreign", "user", "parent")
	historyCreate(t, sessions, "foreign-user", "account", "foreign", "parent")
	historyCreate(t, sessions, "foreign-parent", "account", "user", "other-parent")
	q := RepositoryHistoryQuery{AccountScopeID: "account", UserID: "user", ParentSessionID: "parent", Limit: 2}
	if _, err := sessions.ListSessionRepositoryHistory(q); !errors.Is(err, ErrRepositoryHistoryNotReady) {
		t.Fatalf("read must not migrate: %v", err)
	}
	historyReady(t, sessions)
	first, err := sessions.ListSessionRepositoryHistory(q)
	if err != nil || len(first.Sessions) != 2 || first.NextCursor == "" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	seen := map[string]bool{}
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		page, err := sessions.ListSessionRepositoryHistory(q)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Sessions {
			if seen[row.Session.ID] {
				t.Fatalf("duplicate %s", row.Session.ID)
			}
			seen[row.Session.ID] = true
			if row.Session.UserID != "user" || row.Session.AccountScopeID != "account" {
				t.Fatal("principal leak")
			}
		}
		q.Cursor = page.NextCursor
		if q.Cursor == "" {
			break
		}
	}
	if len(seen) != 6 || seen["foreign-parent"] {
		t.Fatalf("wrong parent rows: %v", seen)
	}
	q.Cursor = first.NextCursor
	for _, mutate := range []func(*RepositoryHistoryQuery){
		func(q *RepositoryHistoryQuery) { q.AccountScopeID = "foreign" },
		func(q *RepositoryHistoryQuery) { q.UserID = "foreign" },
		func(q *RepositoryHistoryQuery) { q.ParentSessionID = "other-parent" },
		func(q *RepositoryHistoryQuery) { q.Limit = 3 },
		func(q *RepositoryHistoryQuery) { q.Cursor = "tampered" },
	} {
		bad := q
		mutate(&bad)
		if page, err := sessions.ListSessionRepositoryHistory(bad); err == nil || len(page.Sessions) != 0 {
			t.Fatalf("invalid scope/cursor accepted: %+v %v", page, err)
		}
	}
	// A new default must not erase the previous repository context.
	parent.WorkspacePath = filepath.Join(t.TempDir(), "new-default")
	_, err = sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: parent.ID, AccountScopeID: "account", UserID: "user", Kind: V3SessionMutationUpdateMetadata, Session: &parent, IdempotencyKey: "default-change", RequestHash: "default-change", NowUnixMs: 200})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.ListSessionRepositoryHistory(q); !errors.Is(err, ErrRepositoryHistoryCursor) {
		t.Fatalf("stale cursor: %v", err)
	}
	if err := sessions.ArchiveSession("child-0"); err != nil {
		t.Fatal(err)
	}
	q.Cursor, q.Limit = "", 100
	page, err := sessions.ListSessionRepositoryHistory(q)
	if err != nil || len(page.Sessions) != 7 {
		t.Fatalf("lost historical context: %+v %v", page, err)
	}
	archived := false
	for _, row := range page.Sessions {
		if row.Session.ID == "child-0" {
			archived = row.Archived
			if row.Session.Metadata["integration_status"] != "dirty-recoverable" {
				t.Fatal("lost retained worker state")
			}
		}
	}
	if !archived {
		t.Fatal("archived child missing")
	}
}

// Purpose: a read-only lane page must not reconcile a running program, write
// metadata, or lose continuation across restart. The store boundary proves the
// exact bytes and durable cursor key without requiring a scheduler/filesystem.
func TestRepositoryHistoryProgramsReadOnlyRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.pebble")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionStore(store)
	historyCreate(t, sessions, "parent", "account", "user", "")
	historyReady(t, sessions)
	for i := 0; i < 3; i++ {
		record := TaskProgramRecord{ParentSessionID: "parent", ProgramID: fmt.Sprintf("program-%d", i), State: TaskProgramStateRunning, Revision: 7, RepositoryLane: &TaskProgramRepositoryLane{SourcePath: "source", WorkspacePath: "lane", Branch: "branch", BaseCommit: "base"}, Jobs: []TaskProgramJobRecord{{JobID: "job", State: TaskProgramJobFailed, IntegrationState: "dirty-recoverable", GenerationHistory: []TaskProgramJobGeneration{{SessionID: "rotated", Generation: 1, State: "completed"}}}}}
		if err := sessions.putTaskProgramHistory(record); err != nil {
			t.Fatal(err)
		}
	}
	before, _, err := store.GetBytes(KeyTaskProgram("parent", "program-0"))
	if err != nil {
		t.Fatal(err)
	}
	q := RepositoryHistoryQuery{AccountScopeID: "account", UserID: "user", ParentSessionID: "parent", Limit: 1}
	page, err := sessions.ListTaskProgramRepositoryHistory(q)
	if err != nil || len(page.Programs) != 1 || page.NextCursor == "" {
		t.Fatalf("program page: %+v %v", page, err)
	}
	if page.Programs[0].State != TaskProgramStateRunning || page.Programs[0].Revision != 7 || page.Programs[0].Jobs[0].GenerationHistory[0].SessionID != "rotated" {
		t.Fatal("read reconciled or lost history")
	}
	after, _, err := store.GetBytes(KeyTaskProgram("parent", "program-0"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read mutated program")
	}
	q.Cursor = page.NextCursor
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions = NewSessionStore(store)
	page, err = sessions.ListTaskProgramRepositoryHistory(q)
	if err != nil || len(page.Programs) != 1 || page.Programs[0].ProgramID != "program-1" {
		t.Fatalf("restart continuation: %+v %v", page, err)
	}
	if _, err := sessions.ListSessionRepositoryHistory(q); !errors.Is(err, ErrRepositoryHistoryCursor) {
		t.Fatalf("cross-kind cursor accepted: %v", err)
	}
}

// Purpose: corrupt migration input must leave both progress and indexed rows
// unchanged. BackfillRepositoryHistory's single batch is the atomic authority;
// an injected malformed durable row proves rollback without ambient services.
func TestRepositoryHistoryBackfillFailureAtomic(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	historyCreate(t, sessions, "parent", "account", "user", "")
	before, _, err := store.GetBytes(repositoryHistoryRevisionKey + "/" + keyPart("parent"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.BackfillRepositoryHistory(100); err != nil {
		t.Fatal(err)
	}
	metaBefore, _, err := store.GetBytes(repositoryHistoryMetaKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Set([]byte(KeySession("zz-corrupt")), []byte("{"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.BackfillRepositoryHistory(100); err == nil {
		t.Fatal("corruption accepted")
	}
	metaAfter, _, err := store.GetBytes(repositoryHistoryMetaKey)
	if err != nil || !bytes.Equal(metaBefore, metaAfter) {
		t.Fatal("partial migration progress")
	}
	after, _, err := store.GetBytes(repositoryHistoryRevisionKey + "/" + keyPart("parent"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed migration committed partial index")
	}
}

// Purpose: startup maintenance must be cancellable and resumable; complete HTTP
// continuations must reject forged scope/limit/state while unrelated writes and
// chat-only metadata do not invalidate them. Store snapshots are the narrowest
// authority for atomic revision and no-partial-result assertions.
func TestRepositoryHistoryContinuationAndMaintenance(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	parent := historyCreate(t, sessions, "parent", "account", "user", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sessions.CompleteRepositoryHistoryMaintenance(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := sessions.CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	q := RepositoryHistoryQuery{AccountScopeID: "account", UserID: "user", ParentSessionID: "parent", Limit: 1}
	payload := []byte(`{"phase":"sessions","cursor":"","offset":1,"context":"first"}`)
	_, token, err := sessions.RepositoryContinuation(q, "", payload)
	if err != nil {
		t.Fatal(err)
	}
	historyCreate(t, sessions, "unrelated", "other-account", "user", "parent")
	parent.Metadata["chat_token"] = "streaming"
	_, err = sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: parent.ID, AccountScopeID: "account", UserID: "user", Kind: V3SessionMutationUpdateMetadata, Session: &parent, IdempotencyKey: "chat", RequestHash: "chat", NowUnixMs: 200})
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := sessions.RepositoryContinuation(q, token, nil)
	if err != nil || !bytes.Equal(decoded, payload) {
		t.Fatalf("unrelated write invalidated token: %s %v", decoded, err)
	}
	for _, bad := range []string{"forged", strings.Repeat("a", 24001), token[:len(token)/2]} {
		if decoded, _, err := sessions.RepositoryContinuation(q, bad, nil); err == nil || len(decoded) != 0 {
			t.Fatal("forged continuation returned state")
		}
	}
	other := q
	other.Limit = 2
	if decoded, _, err := sessions.RepositoryContinuation(other, token, nil); err == nil || len(decoded) != 0 {
		t.Fatal("limit change accepted")
	}
	parent.Metadata["integration_status"] = "integrated"
	_, err = sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: parent.ID, AccountScopeID: "account", UserID: "user", Kind: V3SessionMutationUpdateMetadata, Session: &parent, IdempotencyKey: "integrate", RequestHash: "integrate", NowUnixMs: 300})
	if err != nil {
		t.Fatal(err)
	}
	if decoded, _, err := sessions.RepositoryContinuation(q, token, nil); !errors.Is(err, ErrRepositoryHistoryCursor) || len(decoded) != 0 {
		t.Fatal("stale lifecycle accepted")
	}
}

// Purpose: indexed logical claims deduplicate unchanged attachments across
// default changes without removing distinct catalog IDs; exact retained lookup
// must not scan a capped program list. The store layer proves claim pagination
// and principal rejection independently of Git filesystem availability.
func TestRepositoryHistoryClaimsAndExactLookup(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	parent := historyCreate(t, sessions, "parent", "account", "user", "")
	source := parent.WorkspacePath
	parent.WorkspaceGrants = []WorkspaceGrant{{Kind: WorkspaceGrantPrimary, Path: source, WorkspaceID: "a"}, {Kind: WorkspaceGrantAdditional, Path: source, WorkspaceID: "b"}}
	update := func(key string) {
		t.Helper()
		_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: parent.ID, AccountScopeID: "account", UserID: "user", Kind: V3SessionMutationUpdateMetadata, Session: &parent, IdempotencyKey: key, RequestHash: key, NowUnixMs: 200})
		if err != nil {
			t.Fatal(err)
		}
	}
	update("attach")
	parent.WorkspaceGrants[0].Kind, parent.WorkspaceGrants[1].Kind = WorkspaceGrantAdditional, WorkspaceGrantPrimary
	update("default")
	historyReady(t, sessions)
	q := RepositoryHistoryQuery{AccountScopeID: "account", UserID: "user", ParentSessionID: "parent", Limit: 1}
	seen := map[string]bool{}
	for n := 0; n < 10; n++ {
		page, err := sessions.ListSessionRepositoryHistory(q)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Sessions {
			for _, grant := range row.Grants {
				key := grant.WorkspaceID + ":" + grant.Path
				if seen[key] {
					t.Fatalf("duplicate claim %s", key)
				}
				seen[key] = true
			}
		}
		q.Cursor = page.NextCursor
		if q.Cursor == "" {
			break
		}
	}
	if !seen["a:"+source] || !seen["b:"+source] {
		t.Fatal("distinct identities lost")
	}
	for i := 0; i < 30; i++ {
		record := TaskProgramRecord{ParentSessionID: "parent", ProgramID: fmt.Sprintf("program-%02d", i), State: TaskProgramStateCompleted, RepositoryLane: &TaskProgramRepositoryLane{SourcePath: source, WorkspacePath: filepath.Join(source, "lane")}}
		if err := sessions.putTaskProgramHistory(record); err != nil {
			t.Fatal(err)
		}
	}
	q.Cursor = ""
	page, err := sessions.ExactRepositoryHistory(q, filepath.Join(source, "lane"))
	if err != nil || len(page.Programs) != 1 || page.Programs[0].ProgramID != "program-29" {
		t.Fatalf("exact late lookup: %+v %v", page, err)
	}
	q.UserID = "foreign"
	if page, err := sessions.ExactRepositoryHistory(q, filepath.Join(source, "lane")); err == nil || len(page.Programs) != 0 {
		t.Fatal("foreign lookup returned lane")
	}
}

// Purpose: startup migration must recover pre-index canonical owned lanes, not
// fabricate old provenance from the current default. A raw legacy snapshot is
// the narrowest fixture proving backfill (rather than new-write indexing),
// bounded claim deduplication, owner rejection and unchanged source snapshots.
func TestRepositoryHistoryPreIndexWorktreeProvenance(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	root := t.TempDir()
	source, lane := filepath.Join(root, "source-a"), filepath.Join(root, "lane-a")
	owner := SessionSnapshot{ID: "parent", AccountScopeID: "account", UserID: "user", WorkspacePath: filepath.Join(root, "source-b"), Metadata: map[string]any{}}
	record := map[string]any{"owner_session_id": owner.ID, "path": lane, "source_workspace_path": source, "workspace_id": "a", "workspace_generation": 3, "branch": "agent/old", "base_branch": "dev", "base_commit": "original-base"}
	foreign := map[string]any{"owner_session_id": "foreign", "path": filepath.Join(root, "foreign"), "source_workspace_path": source, "workspace_id": "a", "workspace_generation": 3, "branch": "agent/foreign", "base_commit": "foreign-base"}
	owner.Metadata["swarm_v3_worktree_history"] = []any{record, foreign}
	if err := store.PutJSON(KeySession(owner.ID), owner); err != nil {
		t.Fatal(err)
	}
	before, _, err := store.GetBytes(KeySession(owner.ID))
	if err != nil {
		t.Fatal(err)
	}
	// An installation that completed v2 still needs the provenance migration.
	if err := store.PutJSON("v3/repository_history/meta_v2", repositoryHistoryMeta{Ready: true, Phase: 4, Secret: make([]byte, 32)}); err != nil {
		t.Fatal(err)
	}
	historyReady(t, sessions)
	q := RepositoryHistoryQuery{AccountScopeID: "account", UserID: "user", ParentSessionID: owner.ID, Limit: 1}
	page, err := sessions.ExactRepositoryHistory(q, lane)
	if err != nil || len(page.Sessions) != 1 {
		t.Fatalf("missing pre-index lane: %+v %v", page, err)
	}
	got := page.Sessions[0]
	if !got.HistoricalWorktree || got.Session.WorktreeRootPath != lane || got.Session.WorktreeBranch != "agent/old" || got.Session.Metadata["base_commit"] != "original-base" || got.Session.WorkspaceGrants[0].WorkspaceID != "a" || got.Session.WorkspaceGrants[0].WorkspaceGeneration != 3 {
		t.Fatalf("invented provenance: %+v", got)
	}
	if _, err := sessions.ExactRepositoryHistory(q, filepath.Join(root, "foreign")); err == nil {
		t.Fatal("foreign historical owner indexed")
	}
	seen := map[string]int{}
	for n := 0; n < 10; n++ {
		page, err := sessions.ListSessionRepositoryHistory(q)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Sessions {
			for _, grant := range row.Grants {
				seen[grant.Path]++
			}
		}
		q.Cursor = page.NextCursor
		if q.Cursor == "" {
			break
		}
	}
	if q.Cursor != "" || seen[lane] != 1 || seen[owner.WorkspacePath] != 1 || len(seen) != 2 {
		t.Fatalf("claims: %v", seen)
	}
	after, _, err := store.GetBytes(KeySession(owner.ID))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("backfill mutated canonical snapshot")
	}
	// A later ordinary write must keep the same exact old-lane evidence.
	owner.Metadata["chat_only"] = true
	batch := store.NewBatch()
	defer batch.Close()
	if err := sessions.retainRepositoryHistoryInBatch(batch, owner, false, false); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(nil); err != nil {
		t.Fatal(err)
	}
	q.Cursor = ""
	page, err = sessions.ExactRepositoryHistory(q, lane)
	if err != nil || len(page.Sessions) != 1 || page.Sessions[0].ContextID != got.ContextID {
		t.Fatal("ordinary write lost stable retained identity")
	}
}
