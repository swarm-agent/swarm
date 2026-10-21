package pebblestore

import (
	"path/filepath"
	"strings"
	"testing"
)

func designJobTask(id, state string, items int) *ProjectTaskRecord {
	job := &ProjectTaskDesignJob{Mode: "create", Brief: "pricing page", Count: 3, Folder: "designs/pricing-abc123", ResultsPath: "designs/pricing-abc123/results.json", State: state, StartedAt: 1000}
	for i := 1; i <= items; i++ {
		job.Items = append(job.Items, ProjectTaskDesignItem{N: i, Path: "designs/pricing-abc123/0" + string(rune('0'+i)) + ".html", Status: "passed"})
	}
	status := "in_progress"
	if state != DesignJobRunning {
		job.FinishedAt = 2000
		status = state
	}
	return &ProjectTaskRecord{ID: id, ProjectID: "project-a", Title: "Designs", Agent: "designer", OutcomeType: "ui_design", Status: status, SessionID: "session-" + id, Design: job}
}

// Requirement: a design job still running when the daemon starts is marked
// failed with how many designs finished; nothing resumes it, settled jobs are
// untouched and the running-job index is cleared.
// Threat: a job that stays "running" forever after a restart (an AI waits on
// it indefinitely) or a silent resume that spends again.
// Owners: setDesignJobActiveIndex (written in the task's own batch) and
// SessionStore.ReconcileInterruptedDesignJobs. A real temporary Pebble store
// reopened across a simulated restart is the narrowest layer that proves the
// durable index and record postconditions.
func TestReconcileInterruptedDesignJobsMarksRunningJobsFailed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "design-jobs.pebble")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionStore(store)
	if err := sessions.PutProjectTask("account-a", designJobTask("task_running", DesignJobRunning, 1)); err != nil {
		t.Fatal(err)
	}
	if err := sessions.PutProjectTask("account-a", designJobTask("task_done", DesignJobCompleted, 3)); err != nil {
		t.Fatal(err)
	}
	done, _, _ := sessions.GetProjectTask("account-a", "project-a", "task_done")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions = NewSessionStore(store)
	marked, err := sessions.ReconcileInterruptedDesignJobs(5000)
	if err != nil || marked != 1 {
		t.Fatalf("marked=%d err=%v", marked, err)
	}
	running, found, err := sessions.GetProjectTask("account-a", "project-a", "task_running")
	if err != nil || !found {
		t.Fatal(err)
	}
	want := "interrupted by daemon restart; 1/3 finished"
	if running.Status != "failed" || running.Design.State != DesignJobFailed || running.Design.Error != want || running.LastError != want || running.Design.FinishedAt != 5000 || len(running.Design.Items) != 1 {
		t.Fatalf("running job not settled as interrupted: status=%q design=%#v", running.Status, running.Design)
	}
	after, _, _ := sessions.GetProjectTask("account-a", "project-a", "task_done")
	if after.UpdatedAt != done.UpdatedAt || after.Status != DesignJobCompleted || after.Design.Error != "" {
		t.Fatal("a settled design job was rewritten")
	}
	if marked, err := sessions.ReconcileInterruptedDesignJobs(6000); err != nil || marked != 0 {
		t.Fatalf("index not cleared: marked=%d err=%v", marked, err)
	}
	count := 0
	if err := store.IteratePrefix(designJobActivePrefix, 10, func(string, []byte) error { count++; return nil }); err != nil || count != 0 {
		t.Fatalf("running-job index entries left: %d %v", count, err)
	}
}

// Requirement: the durable design job keeps workspace-relative paths and its
// bounds. Threat: a stored job that names a path outside the workspace or an
// unbounded request is accepted by any writer. Owner: ProjectTaskDesignJob.Validate
// through ProjectTaskRecord.Validate on PutProjectTask; the store is the layer
// every writer crosses.
func TestDesignJobRecordRejectsEscapesAndBadBounds(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "design-validate.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := NewSessionStore(store)
	cases := map[string]func(*ProjectTaskRecord){
		"parent folder": func(t *ProjectTaskRecord) { t.Design.Folder, t.Design.ResultsPath = "../out", "../out/results.json" },
		"absolute folder": func(t *ProjectTaskRecord) {
			t.Design.Folder, t.Design.ResultsPath = "/tmp/out", "/tmp/out/results.json"
		},
		"git folder": func(t *ProjectTaskRecord) {
			t.Design.Folder, t.Design.ResultsPath = ".git/out", ".git/out/results.json"
		},
		"results elsewhere": func(t *ProjectTaskRecord) { t.Design.ResultsPath = "other/results.json" },
		"count zero":        func(t *ProjectTaskRecord) { t.Design.Count = 0 },
		"count too high":    func(t *ProjectTaskRecord) { t.Design.Count = 51 },
		"edit escape": func(t *ProjectTaskRecord) {
			t.Design.Mode, t.Design.Folder, t.Design.ResultsPath, t.Design.EditPath, t.Design.Count, t.Design.Items = "edit", "", "", "../page.html", 1, nil
		},
		"edit not html": func(t *ProjectTaskRecord) {
			t.Design.Mode, t.Design.Folder, t.Design.ResultsPath, t.Design.EditPath, t.Design.Count, t.Design.Items = "edit", "", "", "page.txt", 1, nil
		},
		"item escape":      func(t *ProjectTaskRecord) { t.Design.Items[0].Path = "../01.html" },
		"wrong agent":      func(t *ProjectTaskRecord) { t.Agent, t.OutcomeType = "coder", "code_pr" },
		"running finished": func(t *ProjectTaskRecord) { t.Design.FinishedAt = 3000 },
		"brief too long":   func(t *ProjectTaskRecord) { t.Design.Brief = strings.Repeat("x", 20001) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			task := designJobTask("task_"+strings.ReplaceAll(name, " ", "_"), DesignJobRunning, 1)
			mutate(task)
			if err := sessions.PutProjectTask("account-a", task); err == nil {
				t.Fatal("invalid design job stored")
			}
			if _, found, _ := sessions.GetProjectTask("account-a", "project-a", task.ID); found {
				t.Fatal("rejected task was persisted")
			}
		})
	}
}
