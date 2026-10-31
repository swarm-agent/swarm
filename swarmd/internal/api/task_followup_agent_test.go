package api

import (
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a follow-up (message_task) to a finished finder task keeps agent
// finder, so the task's next job end starts only the workflows that apply to
// finder tasks; a workflow scoped to swarm tasks never fires for it. A task
// result that positively has no commits says so (NoCommits), which is what
// lets a review step skip it (Runner.skipEmptyReview) instead of failing with
// "no head to review"; a result whose Git state was not read never claims it.
// Threat: the live defect where a read-only follow-up turned a finder task
// into a swarm task, fired the swarm-scoped review loop and left the task
// needs_you with no head to review.
// Authority: SessionStore.ReserveTaskFollowup (the reservation
// ReopenProjectTask and MessageProjectTask use), the Role taskJobResult
// copies from task.Agent, Automation.Matches (the workflow scope), and
// taskViewAssessedEmpty (taskJobResult's no-commits rule). The store plus the
// pure scope matcher are the narrowest layers that decide which workflows run.
func TestFollowupKeepsFinderAgentAndItsWorkflows(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db := pebblestore.NewSessionStore(store)
	if err := db.PutProject("account", &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutProjectTask("account", &pebblestore.ProjectTaskRecord{ID: "task_find", ProjectID: "project", AccountID: "account", Title: "Find the limit", Agent: "finder", Status: "needs_review"}); err != nil {
		t.Fatal(err)
	}
	task, _, err := db.GetProjectTask("account", "project", "task_find")
	if err != nil {
		t.Fatal(err)
	}
	followup, err := db.ReserveTaskFollowup("account", "project", task.ID, "user", "message-1", "Which file sets it?", task.Revision, 1)
	if err != nil {
		t.Fatal(err)
	}
	if followup.Agent != "finder" || followup.OutcomeType != task.OutcomeType || followup.ActiveAttempt().Role != "finder" {
		t.Fatalf("follow-up agent %q outcome %q role %q, want finder %q finder", followup.Agent, followup.OutcomeType, followup.ActiveAttempt().Role, task.OutcomeType)
	}

	folder := t.TempDir()
	// The result taskJobResult builds for the follow-up's end: Role is the
	// task's agent; a finder commits nothing and its lane is assessed empty.
	result := &jobautomation.Result{V: jobautomation.ResultVersion, JobID: taskRunJobID("project", task.ID), Kind: jobautomation.KindTask,
		Role: followup.Agent, Folder: folder, Status: jobautomation.StatusSucceeded,
		NoCommits: taskViewAssessedEmpty(map[string]any{"delivery_state": "empty"})}
	workflow := func(role string) jobautomation.Automation {
		return jobautomation.Automation{Name: role + " review", Enabled: true, Scope: jobautomation.Scope{Folder: folder, Role: role},
			Steps: []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer", Brief: "Review"}}}
	}
	if workflow("swarm").Matches(result) {
		t.Fatal("a workflow scoped to swarm tasks matched the finder task's follow-up")
	}
	if !workflow("finder").Matches(result) {
		t.Fatal("a workflow scoped to finder tasks did not match the finder task's follow-up")
	}
	if !result.NoCommits || result.Git != nil {
		t.Fatalf("finder result = %+v, want positively no commits", result)
	}

	for state, want := range map[string]bool{"empty": true, "report_only": true, "": false, "unavailable": false, "not_integrated": false, "in_folder": false} {
		if got := taskViewAssessedEmpty(map[string]any{"delivery_state": state}); got != want {
			t.Fatalf("delivery_state %q: assessed empty = %v, want %v", state, got, want)
		}
	}
}
