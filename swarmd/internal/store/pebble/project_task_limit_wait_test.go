package pebblestore

import (
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: a task waiting for its provider's limit (status
// limit_waiting) reads as task state waiting, counts as waiting (never
// needs_attention), and its reason says until when; every surface reads that
// from ProjectTaskState/ProjectTaskBucket (list_tasks, get_task, the linked
// machine rows, the Orchestrator's prompt).
// Threat: the wait shows as a failure the person must act on, or as an
// unknown run status read as done.
// Owners: ProjectTaskCountBucket, ProjectTaskState; a pure function test is
// the narrowest layer.
func TestProjectTaskLimitWaitingState(t *testing.T) {
	task := ProjectTaskRecord{ID: "t", Status: ProjectTaskStatusLimitWaiting, LimitReason: "waiting for the provider's limit to reset (until 2026-10-29T13:51:00Z): provider claudecode: You've hit your session limit"}
	if bucket := ProjectTaskBucket(task, "", false, false); bucket != ProjectTaskCountWaiting {
		t.Fatalf("bucket %s", bucket)
	}
	if ProjectTaskNeedsAttention(task, "", false, false) {
		t.Fatal("a limit wait asks for the person")
	}
	state, reason := ProjectTaskState(task, "", false, false)
	if state != ProjectTaskStateWaiting || !strings.Contains(reason, "until 2026-10-29T13:51:00Z") || !strings.Contains(reason, "claudecode") {
		t.Fatalf("state %s %q", state, reason)
	}
	task.LimitReason = ""
	if state, reason := ProjectTaskState(task, "", false, false); state != ProjectTaskStateWaiting || reason == "" {
		t.Fatalf("state without a recorded reason: %s %q", state, reason)
	}
}

// Requirement: a continuation's run id names the run it continues, so the
// task's job end and its one task.limit_wait signal are keyed by the run that
// first hit the limit; ids that only look similar are their own base.
// Owners: ProjectTaskLimitRunBase, ProjectTaskLimitRerunID.
func TestProjectTaskLimitRunIDs(t *testing.T) {
	base := InitialTaskRunID("task_1")
	first := ProjectTaskLimitRerunID(base, 1)
	if first != base+".limit1" || ProjectTaskLimitRerunID(first, 2) != base+".limit2" || ProjectTaskLimitRunBase(first) != base {
		t.Fatalf("rerun ids: %s", first)
	}
	for _, own := range []string{base, "run.limited", "run.limit", "run.limit0", ".limit1"} {
		if got := ProjectTaskLimitRunBase(own); got != own {
			t.Fatalf("%q read as a continuation of %q", own, got)
		}
	}
}

// Requirement: the wait index (which a restart re-arms from) follows the
// task record in the same commit: written when a wait starts, moved with
// its reset, removed when the wait ends or the task is deleted, and
// survives reopening the store.
// Threat: a restart forgets a wait, or re-arms one that ended.
// Owners: setTaskLimitWaitIndex (project task commits) and
// ListProjectTaskLimitWaits on a real Pebble store.
func TestProjectTaskLimitWaitIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "limit-index")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db := NewSessionStore(store)
	task := &ProjectTaskRecord{ID: "waiting", ProjectID: "p", Title: "Wait", Agent: "swarm", Status: "in_progress"}
	if err := db.PutProjectTask("acct", task); err != nil {
		t.Fatal(err)
	}
	if refs, err := db.ListProjectTaskLimitWaits(); err != nil || len(refs) != 0 {
		t.Fatalf("index before a wait: %+v %v", refs, err)
	}
	if _, err := db.UpdateProjectTask("acct", "p", "waiting", func(t *ProjectTaskRecord) error {
		t.Status, t.LimitWaits, t.LimitRetryAt = ProjectTaskStatusLimitWaiting, 1, 1000
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	db = NewSessionStore(store)
	refs, err := db.ListProjectTaskLimitWaits()
	if err != nil || len(refs) != 1 || refs[0] != (ProjectTaskLimitWaitRef{AccountScopeID: "acct", ProjectID: "p", TaskID: "waiting", RetryAt: 1000}) {
		t.Fatalf("index after reopen: %+v %v", refs, err)
	}
	if _, err := db.UpdateProjectTask("acct", "p", "waiting", func(t *ProjectTaskRecord) error {
		t.Status, t.LimitRetryAt = ProjectTaskStatusStopped, 0
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if refs, err := db.ListProjectTaskLimitWaits(); err != nil || len(refs) != 0 {
		t.Fatalf("ended wait still indexed: %+v %v", refs, err)
	}
	if _, err := db.UpdateProjectTask("acct", "p", "waiting", func(t *ProjectTaskRecord) error {
		t.Status, t.LimitRetryAt = ProjectTaskStatusLimitWaiting, 2000
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteProjectTask("acct", "p", "waiting"); err != nil {
		t.Fatal(err)
	}
	if refs, err := db.ListProjectTaskLimitWaits(); err != nil || len(refs) != 0 {
		t.Fatalf("deleted task still indexed: %+v %v", refs, err)
	}
}
