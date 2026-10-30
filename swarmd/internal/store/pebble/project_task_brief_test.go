package pebblestore

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

// projectTaskBriefsFromRecords is the reference: each task record of the
// project as the whole-project read decodes it (ListProjectTaskRecordsForList,
// canonical key order), in archived's partition, newest UpdatedAt first.
func projectTaskBriefsFromRecords(t *testing.T, s *SessionStore, account, project string, archived bool) []ProjectTaskBrief {
	t.Helper()
	records, _, err := s.ListProjectTaskRecordsForList(account, project)
	if err != nil {
		t.Fatal(err)
	}
	out := []ProjectTaskBrief{}
	for _, rec := range records {
		if rec.Archived == archived {
			out = append(out, ProjectTaskBriefOf(rec))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

func projectTaskBriefsFromIndex(t *testing.T, s *SessionStore, account, project string, archived bool) []ProjectTaskBrief {
	t.Helper()
	out := []ProjectTaskBrief{}
	if err := s.ListProjectTaskBriefs(account, project, archived, func(b ProjectTaskBrief) bool {
		out = append(out, b)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// Purpose: the task brief index (ListProjectTaskBriefs), which the
// agents-at-work read and the stall check select tasks on instead of
// decoding every task record, holds exactly the brief of every task record
// a whole-project read finds, in each archived partition, newest change
// first with ties in canonical key order: after creates, updates, archive,
// a stall marker, a delete, records written beside the write path and
// rebuilt, a record stored under another account's key, and a reopen.
// Threat: a stale or missing brief (an archived task still listed as open,
// a deleted task listed, a write that left its old row behind, an order
// that differs from the full read) silently changing which finished tasks
// the fleet shows or which tasks the stall check follows.
// Boundary: SessionStore writes through setProjectRealtimeMutationInBatch
// (PutProjectTask, UpdateProjectTask, ArchiveProjectTaskIfRevision,
// SetProjectTaskStall, DeleteProjectTask), RebuildProjectTaskBriefs and the
// open-time backfill, compared against ListProjectTaskRecordsForList over a
// real Pebble store: the narrowest layer that holds both the index and the
// canonical records.
func TestProjectTaskBriefsMatchFullRecordsOnAMixedStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "briefs.pebble")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessionStore(store)
	const account, other = "acct-briefs", "acct-other"
	projects := []string{"proj-a", "proj-b"}
	for _, p := range projects {
		if err := s.PutProject(account, &ProjectRecord{ID: p, Name: p}); err != nil {
			t.Fatal(err)
		}
	}
	statuses := []string{"done", "failed", "in_progress", "queued", "stopped", "completed", "pending_approval", "blocked"}
	for i := 0; i < 24; i++ {
		task := &ProjectTaskRecord{ID: "task_" + string(rune('a'+i)), ProjectID: projects[i%2], Title: "Task " + string(rune('a'+i)),
			Status: statuses[i%len(statuses)], Agent: "coder", SessionID: "sess-" + string(rune('a'+i))}
		if i%5 == 0 {
			task.AutomationID = "workflow:review"
		}
		if i%7 == 0 {
			task.WorkspacePath = "/work/" + task.ID
		}
		if err := s.PutProjectTask(account, task); err != nil {
			t.Fatalf("put %s: %v", task.ID, err)
		}
	}
	// An update moves a task to the newest change.
	if _, err := s.UpdateProjectTask(account, "proj-a", "task_a", func(r *ProjectTaskRecord) error { r.Status = "done"; return nil }); err != nil {
		t.Fatal(err)
	}
	// Archive moves a task between partitions.
	archived, _, err := s.GetProjectTask(account, "proj-b", "task_b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveProjectTaskIfRevision(account, "proj-b", "task_b", archived.Revision); err != nil {
		t.Fatal(err)
	}
	// A stall marker is in the brief.
	marked, _, err := s.GetProjectTask(account, "proj-a", "task_c")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SetProjectTaskStall(account, "proj-a", "task_c", marked.UpdatedAt, &ProjectTaskStall{Kind: ProjectTaskStallNoNextStep, Reason: "no run", Since: 1, TaskUpdatedAt: marked.UpdatedAt}); err != nil || !ok {
		t.Fatalf("mark stall: %v %v", ok, err)
	}
	if err := s.DeleteProjectTask(account, "proj-b", "task_d"); err != nil {
		t.Fatal(err)
	}

	check := func(stage string) {
		t.Helper()
		total := 0
		for _, p := range projects {
			for _, arch := range []bool{false, true} {
				want := projectTaskBriefsFromRecords(t, s, account, p, arch)
				got := projectTaskBriefsFromIndex(t, s, account, p, arch)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s: %s archived=%v briefs differ from full records:\n got %+v\nwant %+v", stage, p, arch, got, want)
				}
				total += len(got)
			}
		}
		if total == 0 {
			t.Fatalf("%s: fixture has no tasks", stage)
		}
	}
	check("canonical writes")
	if got := projectTaskBriefsFromIndex(t, s, account, "proj-b", true); len(got) != 1 || got[0].ID != "task_b" || !got[0].Archived {
		t.Fatalf("archived partition = %+v, want task_b alone", got)
	}
	for _, b := range projectTaskBriefsFromIndex(t, s, account, "proj-b", false) {
		if b.ID == "task_b" || b.ID == "task_d" {
			t.Fatalf("unarchived partition still lists %s", b.ID)
		}
	}
	if got := projectTaskBriefsFromIndex(t, s, account, "proj-a", false); len(got) == 0 || got[0].ID != "task_a" {
		t.Fatalf("newest change first: %+v", got)
	}

	// Records written beside the write path, with equal change times (ties
	// in key order), a design job, and a record under the wrong account's
	// key, are not indexed until a rebuild; the rebuild matches the full read.
	batch := store.db.NewBatch()
	put := func(key string, rec ProjectTaskRecord) {
		raw, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := batch.Set([]byte(key), raw, nil); err != nil {
			t.Fatal(err)
		}
	}
	tie := time.Now().Add(-time.Hour).UnixMilli()
	for _, id := range []string{"raw_c", "raw_a", "raw_b"} {
		put(KeyProjectTask(account, "proj-a", id), ProjectTaskRecord{ID: id, ProjectID: "proj-a", AccountID: account, Title: id, Status: "done", SessionID: "s-" + id, CreatedAt: tie, UpdatedAt: tie})
	}
	put(KeyProjectTask(account, "proj-a", "raw_design"), ProjectTaskRecord{ID: "raw_design", ProjectID: "proj-a", AccountID: account, Status: "done", Design: &ProjectTaskDesignJob{}, CreatedAt: tie, UpdatedAt: tie + 1})
	put(KeyProjectTask(account, "proj-a", "raw_foreign"), ProjectTaskRecord{ID: "raw_foreign", ProjectID: "proj-a", AccountID: other, Status: "done", CreatedAt: tie, UpdatedAt: tie})
	if err := batch.Commit(nil); err != nil {
		t.Fatal(err)
	}
	batch.Close()
	if err := s.RebuildProjectTaskBriefs(); err != nil {
		t.Fatal(err)
	}
	check("rebuild")
	got := projectTaskBriefsFromIndex(t, s, account, "proj-a", false)
	ids := []string{}
	for _, b := range got {
		if b.UpdatedAt == tie {
			ids = append(ids, b.ID)
		}
		if b.ID == "raw_foreign" {
			t.Fatalf("a record under another account's key is listed: %+v", b)
		}
		if b.ID == "raw_design" && !b.Design {
			t.Fatalf("design job brief = %+v", b)
		}
		if b.ID == "task_c" && !b.Stalled {
			t.Fatalf("stall marker missing from brief %+v", b)
		}
	}
	if !reflect.DeepEqual(ids, []string{"raw_a", "raw_b", "raw_c"}) {
		t.Fatalf("equal change times in %v, want key order", ids)
	}

	// The index survives a reopen without a second backfill.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s = NewSessionStore(store)
	check("reopen")
}

// Purpose: a store written before the brief index existed gets every task's
// brief at open (the one-time backfill, in bounded batches with a resumable
// cursor), so the first agents-at-work read and stall check after an upgrade
// see the whole history; until then, a read reports not ready rather than
// an empty project.
// Threat: an upgrade where the index starts empty and the fleet and the
// stall check silently see no tasks, or where a backfill interrupted between
// batches skips or duplicates records.
// Boundary: Open -> prepareProjectTaskBriefs over canonical task records
// written directly (as a store from before this index), with more records
// than one backfill batch.
func TestProjectTaskBriefsBackfillAtOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backfill.pebble")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const account, project = "acct-backfill", "proj-backfill"
	s := NewSessionStore(store)
	// As a store from before the index: no brief state, no rows.
	reset := store.db.NewBatch()
	if err := reset.DeleteRange([]byte(projectTaskBriefNamespace), []byte(projectTaskBriefNamespace+"\xff"), nil); err != nil {
		t.Fatal(err)
	}
	n := projectTaskBriefBackfillRows*2 + 7
	base := time.Now().Add(-48 * time.Hour).UnixMilli()
	for i := 0; i < n; i++ {
		id := "task_" + time.UnixMilli(int64(i)).UTC().Format("150405.000")
		raw, err := json.Marshal(ProjectTaskRecord{ID: id, ProjectID: project, AccountID: account, Status: "done", CreatedAt: base, UpdatedAt: base + int64(i)})
		if err != nil {
			t.Fatal(err)
		}
		if err := reset.Set([]byte(KeyProjectTask(account, project, id)), raw, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := reset.Commit(nil); err != nil {
		t.Fatal(err)
	}
	reset.Close()
	if err := s.ListProjectTaskBriefs(account, project, false, func(ProjectTaskBrief) bool { return true }); err != ErrProjectTaskBriefsNotReady {
		t.Fatalf("read before the backfill = %v, want ErrProjectTaskBriefsNotReady", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s = NewSessionStore(store)
	got := projectTaskBriefsFromIndex(t, s, account, project, false)
	if want := projectTaskBriefsFromRecords(t, s, account, project, false); len(got) != n || !reflect.DeepEqual(got, want) {
		t.Fatalf("backfilled %d briefs, want %d matching the records", len(got), n)
	}
}
