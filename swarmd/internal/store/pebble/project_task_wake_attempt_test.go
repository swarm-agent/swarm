package pebblestore

import (
	"errors"
	"testing"
)

// linkedAttemptProgress is task progress as a linked machine's get_task
// reports it: one delivery of the review loop for attemptID (previous marks
// it previous_attempt, as a machine with this rule does).
func linkedAttemptProgress(workflowState, deliveryState, stepType, attemptID string, previous bool) map[string]any {
	steps := []ProjectTaskStepResult{{ID: "console", Type: "run", Status: "failed", DurationMs: 4100, Error: "exited with code 1: OLD"}}
	delivery := map[string]any{"id": "jdel-" + attemptID, "workflow": "review loop", "attempt_id": attemptID, "run": 1, "previous_attempt": previous, "state": deliveryState, "step": 1, "steps": 3, "step_type": stepType, "round": 1, "steps_result": steps}
	return map[string]any{"workflow": map[string]any{"state": workflowState, "deliveries": []any{delivery}}}
}

// Purpose (linked machines): a linked wake row is the followed attempt's
// only, as a local one. Requirement: the task was sent back on the machine
// (attempt a2 follows pinned a1) and its run is in progress; the machine's
// progress still shows a1's failed delivery, either marked previous_attempt
// (a machine with this rule) or only naming attempt_id a1 with a failed
// workflow state: the watch (milestones on) wakes with no `failed`
// milestone and no outcome. When a2's own delivery runs its review, the
// milestone row carries delivery_attempt_id a2 and delivery_run, and no
// failed workflow state. Threat (observed from home): a `failed` wake with
// the old delivery's workflow_steps_result attached to the new attempt.
// Boundary: projectTaskWatchRemoteOutcome (ForAttempt) and
// prepareProjectTaskWatchWake through ApplyV3SessionMutation over a
// temporary real store.
func TestProjectTaskLinkedWakeRowIsTheFollowedAttemptsOnly(t *testing.T) {
	r := newTaskWaitFixture(t, "home", map[string]any{})
	r.apply(remoteTaskWaitRegistration(V3ProjectTaskWaitTarget{TaskID: "t1", AttemptID: "a1", SessionID: "rs1"}))
	r.endRun("home", "run-1")
	if !r.onlyWatch("home").Milestones {
		t.Fatal("milestones not on")
	}
	observe := func(progress map[string]any) []V3ProjectTaskRemoteObservation {
		return []V3ProjectTaskRemoteObservation{{TaskID: "t1", Title: "One", Status: "in_progress", AttemptID: "a2", SessionID: "rs2", Progress: ProjectTaskWaitProgressFrom(progress)}}
	}
	for name, progress := range map[string]map[string]any{
		"marked previous_attempt": linkedAttemptProgress("none", "failed", "run", "a1", true),
		"naming attempt a1 only":  linkedAttemptProgress("failed", "failed", "run", "a1", false),
	} {
		if _, err := r.try(r.remoteWake(observe(progress), nil)); !errors.Is(err, ErrProjectTaskWaitNotReady) {
			t.Fatalf("%s: the old delivery woke the new attempt's watch: %v", name, err)
		}
	}
	// Control: the same failure on a2's own delivery is a failed milestone.
	own, err := r.try(r.remoteWake(observe(linkedAttemptProgress("failed", "failed", "run", "a2", false)), nil))
	if err != nil {
		t.Fatal(err)
	}
	if rows := milestoneRows(t, own.Message); len(rows) != 1 || rows[0]["milestone"] != "failed" || rows[0]["delivery_attempt_id"] != "a2" {
		t.Fatalf("a2's own failure: %v", rows)
	}

	f := newTaskWaitFixture(t, "home", map[string]any{})
	f.apply(remoteTaskWaitRegistration(V3ProjectTaskWaitTarget{TaskID: "t1", AttemptID: "a1", SessionID: "rs1"}))
	f.endRun("home", "run-1")
	review, err := f.try(f.remoteWake(observe(linkedAttemptProgress("running", "running", "agent", "a2", false)), nil))
	if err != nil {
		t.Fatal(err)
	}
	rows := milestoneRows(t, review.Message)
	if len(rows) != 1 || rows[0]["milestone"] != "review started (round 1)" || rows[0]["attempt_id"] != "a2" || rows[0]["delivery_attempt_id"] != "a2" || rows[0]["delivery_run"] != float64(1) || rows[0]["workflow_state"] != "running" || rows[0]["machine"] != "box" {
		t.Fatalf("a2's review milestone row: %v", rows)
	}
}
