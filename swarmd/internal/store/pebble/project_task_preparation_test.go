package pebblestore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Purpose: Open must finish legacy task and related projection preparation before
// any read. This disk/reopen layer exercises the actual startup barrier, durable
// partial cursor, archive partition and account isolation without prebuilt indexes.
func TestProjectTaskPreparationStartup(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessionStore(db)
	for _, account := range []string{"owner", "foreign"} {
		for i := 0; i < 75; i++ {
			task := ProjectTaskRecord{ID: fmt.Sprintf("t-%03d", i), AccountID: account, ProjectID: "project", Title: "Task", Agent: "swarm", Status: "pending", Archived: i >= 65, SessionID: account + fmt.Sprint(i), FullPlanMarkdown: strings.Repeat("detail", 1024)}
			if err := db.PutJSON(KeyProjectTask(account, "project", task.ID), task); err != nil {
				t.Fatal(err)
			}
			if err := db.PutJSON(KeySession(task.SessionID), SessionSnapshot{ID: task.SessionID, AccountScopeID: account}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A GET must not move the durable cursor or decode any canonical body.
	if rows, stats, err := s.ListProjectTaskSummaries("owner", "project", false); !errors.Is(err, ErrProjectTaskSummariesNotReady) || rows != nil || stats.BackfillBytes != 0 {
		t.Fatalf("read advanced preparation: %v %+v", err, stats)
	}
	if stats, err := s.BackfillProjectTaskSummaries("owner", "project"); !errors.Is(err, ErrProjectTaskSummariesNotReady) || stats.BackfillRows != 32 {
		t.Fatalf("partial preparation: %v %+v", err, stats)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = NewSessionStore(db)
	for _, account := range []string{"owner", "foreign"} {
		rows, stats, err := s.ListProjectTaskSummaries(account, "project", false)
		if err != nil || len(rows) != 65 || stats.BackfillBytes != 0 || stats.ScannedRows != 65 {
			t.Fatalf("first read: count=%d stats=%+v err=%v", len(rows), stats, err)
		}
		for _, row := range rows {
			if row.AccountID != account || row.FullPlanMarkdown != "" {
				t.Fatal("isolation or compactness violated")
			}
		}
	}
}

// Purpose: malformed legacy data must fail startup explicitly, preserve canonical
// bytes and durable progress, and permit recovery after an operator corrects the
// source. OpenReadOnly is deliberately not a migration writer.
func TestProjectTaskPreparationFailure(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	key := KeyProjectTask("owner", "project", "bad")
	if err := db.PutBytes(key, []byte("not-json")); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if reopened, err := Open(path); err == nil {
		reopened.Close()
		t.Fatal("corrupt preparation accepted")
	} else if !strings.Contains(err.Error(), "prepare project task indexes before serving") {
		t.Fatal(err)
	}
	db, err = OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	raw, found, err := db.GetBytes(key)
	if err != nil || !found || string(raw) != "not-json" {
		t.Fatal("failed preparation altered original")
	}
}

// Purpose: preparation cancellation must preserve a usable resumable store and
// never claim readiness. Explicit cancellation at the lifecycle boundary is the
// narrowest deterministic interruption case; disk restart is covered above.
func TestProjectTaskPreparationCancelled(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewSessionStore(db).PrepareProjectTaskIndex(ctx, "owner", "project"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// Purpose: a concurrent archive/create writer must not be lost behind a backfill
// cursor. Canonical PutProjectTask and startup chunks share projectsMu; the final
// indexed partition must equal canonical state after both finish.
func TestProjectTaskPreparationConcurrentMutation(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	for i := 0; i < 80; i++ {
		task := ProjectTaskRecord{ID: fmt.Sprintf("t-%03d", i), AccountID: "owner", ProjectID: "project", Title: "Task", Agent: "swarm", Status: "queued"}
		if err := db.PutJSON(KeyProjectTask("owner", "project", task.ID), task); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- s.PrepareProjectTaskIndex(context.Background(), "owner", "project") }()
	for i := 0; i < 30; i++ {
		task := ProjectTaskRecord{ID: fmt.Sprintf("t-%03d", i), AccountID: "owner", ProjectID: "project", Title: "Changed", Agent: "swarm", Status: "queued", Archived: i%2 == 0}
		if err := s.PutProjectTask("owner", &task); err != nil {
			t.Fatal(err)
		}
	}
	task := ProjectTaskRecord{ID: "a-new-before-cursor", ProjectID: "project", Title: "New", Agent: "swarm"}
	if err := s.PutProjectTask("owner", &task); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, archived := range []bool{false, true} {
		rows, stats, err := s.ListProjectTaskSummaries("owner", "project", archived)
		if err != nil || stats.BackfillBytes != 0 {
			t.Fatalf("read: %+v %v", stats, err)
		}
		want := 66
		if archived {
			want = 15
		}
		if len(rows) != want {
			t.Fatalf("archive=%v got=%d want=%d", archived, len(rows), want)
		}
		for _, row := range rows {
			canonical, found, err := s.GetProjectTask("owner", "project", row.ID)
			if err != nil || !found || canonical.Archived != row.Archived || canonical.Title != row.Title {
				t.Fatal("mutation lost or stale summary")
			}
		}
	}
}

// Purpose: Open must upgrade the version-3 card index in place without changing
// canonical tasks. A multi-batch rebuild and second reopen prove startup recovery,
// archive counts and account isolation at the narrowest durable store boundary.
func TestProjectTaskPreparationVersionUpgrade(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	originals := map[string][]byte{}
	for _, account := range []string{"owner", "foreign"} {
		for i := 0; i < 35; i++ {
			task := ProjectTaskRecord{ID: fmt.Sprintf("task-%02d", i), AccountID: account, ProjectID: "project", Title: "Retained", Archived: i >= 30, CreatedAt: int64(i)}
			if err := NewSessionStore(db).PutProjectTask(account, &task); err != nil {
				t.Fatal(err)
			}
			key := KeyProjectTask(account, "project", task.ID)
			raw, ok, err := db.GetBytes(key)
			if err != nil || !ok {
				t.Fatalf("canonical read: %v", err)
			}
			originals[key] = append([]byte(nil), raw...)
		}
	}
	// Old ready state and stale locators/counts must all be replaced, not reused.
	prefix := taskSummaryPrefix("owner", "project")
	if err := db.PutJSON(prefix+"state", taskSummaryState{Version: 3, Ready: true, Counts: [2]int{99, 99}}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(prefix+"rows/0/stale", taskSummaryRow{Version: 3}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(prefix+"ids/stale", prefix+"rows/0/stale"); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		db, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		for key, want := range originals {
			got, ok, err := db.GetBytes(key)
			if err != nil || !ok || !bytes.Equal(got, want) {
				t.Fatal("migration changed canonical task")
			}
		}
		for _, account := range []string{"owner", "foreign"} {
			for _, archived := range []bool{false, true} {
				want := 30
				if archived {
					want = 5
				}
				rows, _, err := NewSessionStore(db).ListProjectTaskSummaries(account, "project", archived)
				if err != nil || len(rows) != want {
					t.Fatalf("cards: %d want %d: %v", len(rows), want, err)
				}
				for _, row := range rows {
					if row.AccountID != account {
						t.Fatal("foreign task exposed")
					}
				}
			}
		}
		for _, key := range []string{prefix + "rows/0/stale", prefix + "ids/stale"} {
			if _, ok, err := db.GetBytes(key); err != nil || ok {
				t.Fatal("stale derived entry retained")
			}
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// Purpose: BackfillProjectTaskSummaries must reject unknown schemas without
// deleting derived or canonical bytes. Direct store calls isolate the migration
// rejection boundary and verify its no-mutation postcondition.
func TestProjectTaskPreparationUnknownVersion(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	task := ProjectTaskRecord{ID: "task", AccountID: "owner", ProjectID: "project"}
	key := KeyProjectTask("owner", "project", "task")
	if err := db.PutJSON(key, task); err != nil {
		t.Fatal(err)
	}
	stateKey := taskSummaryPrefix("owner", "project") + "state"
	for _, version := range []int{0, 2, taskSummaryVersion + 1} {
		if err := db.PutJSON(stateKey, taskSummaryState{Version: version, Ready: true}); err != nil {
			t.Fatal(err)
		}
		before := map[string][]byte{}
		for _, k := range []string{key, stateKey} {
			raw, ok, err := db.GetBytes(k)
			if err != nil || !ok {
				t.Fatal(err)
			}
			before[k] = append([]byte(nil), raw...)
		}
		if _, err := NewSessionStore(db).BackfillProjectTaskSummaries("owner", "project"); !errors.Is(err, ErrProjectTaskSummaryCorrupt) {
			t.Fatalf("unknown version accepted: %v", err)
		}
		for k, want := range before {
			got, ok, err := db.GetBytes(k)
			if err != nil || !ok || !bytes.Equal(got, want) {
				t.Fatal("rejection changed stored bytes")
			}
		}
	}
}
