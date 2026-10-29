package pebblestore

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func milestoneRows(t *testing.T, message *MessageSnapshot) []map[string]any {
	t.Helper()
	const prefix = "Delegated project task milestones:\n"
	if message == nil || message.Role != "user" || !strings.HasPrefix(message.Content, prefix) || message.Metadata["task_watch_delivery"] != "milestone" {
		t.Fatalf("milestone message: %+v", message)
	}
	var body struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(message.Content, prefix)), &body); err != nil {
		t.Fatal(err)
	}
	return body.Tasks
}

// Purpose: a watch also tells the Orchestrator when its tasks reach a
// stage, not only when they settle, without flooding the conversation.
// Requirement: with milestones on (the default), review started wakes at
// once with the milestone named; changes within a minute of the last
// milestone wake are held (*ProjectTaskWatchMilestoneDue with the due time,
// and the daemon's hook is told) and coalesced: at the due time one wake
// names the stage reached then (an intermediate "sent back" is not reported
// on its own); an unchanged stage is not reported again; settling still
// delivers the outcomes at once, whatever the interval, and ends the watch;
// a watch registered with milestones off never wakes for a stage. Threat: a
// silent Orchestrator until the very end, or a wake storm on every workflow
// step. Authority: prepareProjectTaskWatchWake (projectTaskWatchMilestone,
// ProjectTaskWatchMilestoneInterval) through ApplyV3SessionMutation with an
// injected clock (NowUnixMs), and ReconcileProjectTaskWaits for the hook.
func TestProjectTaskWatchMilestonesCoalesceAndRateLimit(t *testing.T) {
	f, progress := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
	f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationAppendMessage, ClientRequestID: "task-watch:run-1", TaskWait: &V3ProjectTaskWaitMutation{RunID: "run-1", ProjectID: "project", TaskIDs: []string{"task"}}})
	f.endRun("parent", "run-1")
	if !f.onlyWatch("parent").Milestones {
		t.Fatal("milestones are not on by default")
	}
	f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review" })
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	wakeAt := func(at int64) (V3SessionMutationResult, error) {
		in := ProjectTaskWatchWakeInput(f.onlyWatch("parent"), nil, nil)
		in.NowUnixMs = at
		return f.try(in)
	}
	progress.set("task", workflowProgress("pending", 0, "", 0, ""))
	if _, err := wakeAt(t0); !errors.Is(err, ErrProjectTaskWaitNotReady) {
		t.Fatalf("no milestone yet: %v", err)
	}
	progress.set("task", workflowProgress("running", 1, "agent", 0, "not_integrated"))
	first, err := wakeAt(t0)
	if err != nil {
		t.Fatal(err)
	}
	if rows := milestoneRows(t, first.Message); len(rows) != 1 || rows[0]["milestone"] != "review started (round 0)" {
		t.Fatalf("first milestone: %v", rows)
	}
	if first.RunIntent == nil || first.RunIntent.RunID != SessionWakeRunID("parent", f.onlyWatch("parent").WatchID+":milestone:1") {
		t.Fatalf("milestone on an idle conversation did not start a turn: %+v", first.RunIntent)
	}
	if w := f.onlyWatch("parent"); w.Deliveries != 1 || w.LastMilestoneAt != t0 {
		t.Fatalf("watch after the milestone: %+v", w)
	}
	// The turn ends; the review sends the work back within the minute.
	f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "claim-m1", RunIntent: &V3SessionRunIntent{RunID: first.RunIntent.RunID, Status: V3RunIntentRunning}})
	f.endRun("parent", first.RunIntent.RunID)
	progress.set("task", workflowProgress("running", 2, "reopen", 0, "not_integrated"))
	var due *ProjectTaskWatchMilestoneDue
	if _, err := wakeAt(t0 + 10_000); !errors.As(err, &due) || due.At != t0+60_000 {
		t.Fatalf("sent back within the minute: %v", err)
	}
	progress.set("task", workflowProgress("running", 1, "agent", 1, "not_integrated"))
	if _, err := wakeAt(t0 + 30_000); !errors.As(err, &due) || due.At != t0+60_000 {
		t.Fatalf("second change within the minute: %v", err)
	}
	coalesced, err := wakeAt(t0 + 61_000)
	if err != nil {
		t.Fatal(err)
	}
	if rows := milestoneRows(t, coalesced.Message); len(rows) != 1 || rows[0]["milestone"] != "review started (round 1)" {
		t.Fatalf("coalesced milestone: %v", rows)
	}
	if w := f.onlyWatch("parent"); w.Deliveries != 2 {
		t.Fatalf("deliveries: %d", w.Deliveries)
	}
	if _, err := wakeAt(t0 + 200_000); !errors.Is(err, ErrProjectTaskWaitNotReady) {
		t.Fatalf("an unchanged stage reported again: %v", err)
	}
	// Settled within the minute of the last milestone: the outcome goes at once.
	landed := workflowProgress("done", 3, "run", 1, "integrated")
	landed["task_state"], landed["task_state_reason"] = ProjectTaskStateLanded, "pushed 7ec9a8e"
	progress.set("task", landed)
	final, err := wakeAt(t0 + 62_000)
	if err != nil {
		t.Fatal(err)
	}
	if out := taskWaitOutcome(t, final.Message); outcomeRow(t, out, 0)["task_state"] != ProjectTaskStateLanded {
		t.Fatalf("outcome: %v", out)
	}
	if n := len(f.watches("parent")); n != 0 {
		t.Fatalf("settled watch kept: %d", n)
	}

	// The reconcile path tells the daemon when a held milestone is due.
	g, gp := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store2"), "task")
	g.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationAppendMessage, ClientRequestID: "task-watch:run-1", TaskWait: &V3ProjectTaskWaitMutation{RunID: "run-1", ProjectID: "project", TaskIDs: []string{"task"}}})
	g.endRun("parent", "run-1")
	g.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review" })
	var dueAt []int64
	g.db.store.SetTaskWatchMilestoneDue(func(_, _ string, at int64) { dueAt = append(dueAt, at) })
	gp.set("task", workflowProgress("running", 1, "agent", 0, ""))
	if woke := g.reconcileCount(); woke != 1 {
		t.Fatalf("first milestone via reconcile: %d", woke)
	}
	last := g.onlyWatch("parent").LastMilestoneAt
	gp.set("task", workflowProgress("running", 2, "reopen", 0, ""))
	if woke := g.reconcileCount(); woke != 0 || len(dueAt) != 1 || dueAt[0] != last+ProjectTaskWatchMilestoneInterval.Milliseconds() {
		t.Fatalf("held milestone: woke %d due %v (last %d)", woke, dueAt, last)
	}

	// Milestones off: a stage never wakes.
	h, hp := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store3"), "task")
	off := false
	h.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationAppendMessage, ClientRequestID: "task-watch:run-1", TaskWait: &V3ProjectTaskWaitMutation{RunID: "run-1", ProjectID: "project", TaskIDs: []string{"task"}, Milestones: &off}})
	h.endRun("parent", "run-1")
	h.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review" })
	hp.set("task", workflowProgress("running", 1, "agent", 0, ""))
	if woke := h.reconcileCount(); woke != 0 || h.onlyWatch("parent").Milestones {
		t.Fatalf("milestones off woke: %d", woke)
	}
}
