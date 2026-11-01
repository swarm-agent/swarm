package tool

import (
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose:
//   - Requirement: a machine watch wakes exactly when the machine runs
//     another commit (full and short forms of one commit are the same), when
//     an update failed after the watch began, when the drain for it timed out
//     after the watch began or after the watch saw it still running (the
//     documented flow: request_update starts the drain, then wait_machine),
//     or when a candidate has waited longer than the
//     watch's stuck_minutes (measured from when the watch first saw it, kept
//     in the record); a candidate that changes restarts that clock, and an
//     update that is current clears it.
//   - Threat: a watch that never wakes (the observed silent update), one
//     that wakes at once for a failure or a timed-out drain from before it
//     began, or one that calls a short commit an update.
//   - Boundary: EvaluateMachineWatch, the pure decision runMachineWatch
//     applies to each read; the narrowest layer that holds every rule.
func TestEvaluateMachineWatch(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	created := now.Add(-time.Hour).UnixMilli()
	watch := func() *pebblestore.V3MachineWatchRecord {
		return &pebblestore.V3MachineWatchRecord{ID: "machinewatch-x", Running: "aaaaaaaaa1111", StuckAfterMinutes: 30, CreatedAt: created}
	}
	update := func(state, candidate string, at int64) *MachineUpdateState {
		return &MachineUpdateState{State: state, Reason: "why " + state, Running: "aaaaaaaaa1111", Candidate: candidate, At: at}
	}

	// Same commit, short form: no wake.
	w := watch()
	if event, _ := EvaluateMachineWatch(w, MachineUpdateView{Commit: "aaaaaaaaa"}, now); event != nil {
		t.Fatalf("a short form of the same commit woke: %+v", event)
	}
	// Another commit: updated, old -> new with the update's reason.
	event, _ := EvaluateMachineWatch(w, MachineUpdateView{Commit: "bbbbbbbbb2222", Update: &MachineUpdateState{State: "installed", Reason: "aaaaaaaaa -> bbbbbbbbb"}}, now)
	if event == nil || event.Kind != MachineWatchUpdated || event.From != "aaaaaaaaa1111" || event.To != "bbbbbbbbb2222" || event.Reason != "aaaaaaaaa -> bbbbbbbbb" {
		t.Fatalf("commit change: %+v", event)
	}
	// Unknown running commit at the start: recorded, no wake.
	w = watch()
	w.Running = ""
	if event, changed := EvaluateMachineWatch(w, MachineUpdateView{Update: update("current", "", 0)}, now); event != nil || !changed || w.Running != "aaaaaaaaa1111" {
		t.Fatalf("unknown start: %+v %v %+v", event, changed, w)
	}

	// A candidate first seen: its clock starts, no wake.
	w = watch()
	view := MachineUpdateView{Commit: "aaaaaaaaa1111", Update: update("waiting", "ccccccccc3333", now.UnixMilli())}
	if event, changed := EvaluateMachineWatch(w, view, now); event != nil || !changed || w.Candidate != "ccccccccc3333" || w.CandidateSince != now.UnixMilli() {
		t.Fatalf("candidate first seen: %+v %v %+v", event, changed, w)
	}
	// Waiting less than stuck_minutes: no wake, nothing to store.
	if event, changed := EvaluateMachineWatch(w, view, now.Add(29*time.Minute)); event != nil || changed {
		t.Fatalf("before the threshold: %+v %v", event, changed)
	}
	// Longer: stuck, with why.
	event, _ = EvaluateMachineWatch(w, view, now.Add(31*time.Minute))
	if event == nil || event.Kind != MachineWatchStuck || event.To != "ccccccccc3333" || event.Why != "runs blocking or waiting: why waiting" || event.WaitingSince != now.UnixMilli() {
		t.Fatalf("stuck: %+v", event)
	}
	// Draining: why names the drain.
	drained := view
	drained.Drain = &MachineDrainState{Draining: true, BlockingRuns: 2, Reason: "new runs held; waiting for 2 agent run(s) to finish"}
	if event, _ := EvaluateMachineWatch(w, drained, now.Add(31*time.Minute)); event == nil || !strings.Contains(event.Why, "waiting for 2 agent run(s)") {
		t.Fatalf("stuck while draining: %+v", event)
	}
	// A new candidate restarts the clock.
	next := MachineUpdateView{Commit: "aaaaaaaaa1111", Update: update("waiting", "ddddddddd4444", now.UnixMilli())}
	if event, changed := EvaluateMachineWatch(w, next, now.Add(31*time.Minute)); event != nil || !changed || w.Candidate != "ddddddddd4444" {
		t.Fatalf("new candidate: %+v %v %+v", event, changed, w)
	}
	// Current again: the candidate is cleared.
	if event, changed := EvaluateMachineWatch(w, MachineUpdateView{Commit: "aaaaaaaaa1111", Update: update("current", "", now.UnixMilli())}, now); event != nil || !changed || w.Candidate != "" || w.CandidateSince != 0 {
		t.Fatalf("current: %+v %v %+v", event, changed, w)
	}

	// A failure from before the watch began does not wake; one after does.
	w = watch()
	if event, _ := EvaluateMachineWatch(w, MachineUpdateView{Commit: "aaaaaaaaa1111", Update: update("rolled_back", "ccccccccc3333", created-1)}, now); event != nil {
		t.Fatalf("an old failure woke: %+v", event)
	}
	event, _ = EvaluateMachineWatch(w, MachineUpdateView{Commit: "aaaaaaaaa1111", Update: update("failed", "ccccccccc3333", created+1)}, now)
	if event == nil || event.Kind != MachineWatchFailed || event.Why != "install error: why failed" {
		t.Fatalf("failure: %+v", event)
	}
	// The drain for the update timed out after the watch began: stuck at once.
	w = watch()
	timedOut := MachineUpdateView{Commit: "aaaaaaaaa1111", Update: update("waiting", "ccccccccc3333", now.UnixMilli()), Drain: &MachineDrainState{TimedOut: true, EndReason: "expired", SinceMs: created + 1, Reason: "update refused: 1 agent run(s) still ran at the deadline"}}
	event, _ = EvaluateMachineWatch(w, timedOut, now)
	if event == nil || event.Kind != MachineWatchStuck || !strings.HasPrefix(event.Why, "drain timed out: update refused") {
		t.Fatalf("timed-out drain: %+v", event)
	}
	// request_update, then wait_machine: the drain began before the watch,
	// which saw it still running; its timing out wakes the watch.
	before := created - 60_000
	w = watch()
	runningDrain := MachineUpdateView{Commit: "aaaaaaaaa1111", Update: update("draining", "ccccccccc3333", now.UnixMilli()), Drain: &MachineDrainState{Draining: true, Requested: true, BlockingRuns: 1, SinceMs: before}}
	if event, changed := EvaluateMachineWatch(w, runningDrain, now); event != nil || !changed || w.DrainSince != before {
		t.Fatalf("a running drain was not remembered: %+v %v %+v", event, changed, w)
	}
	if event, changed := EvaluateMachineWatch(w, runningDrain, now.Add(time.Minute)); event != nil || changed {
		t.Fatalf("the same running drain woke or changed the record: %+v %v", event, changed)
	}
	timedOut.Drain.SinceMs = before
	event, _ = EvaluateMachineWatch(w, timedOut, now.Add(2*time.Minute))
	if event == nil || event.Kind != MachineWatchStuck || !strings.HasPrefix(event.Why, "drain timed out: update refused") {
		t.Fatalf("a drain seen running then timed out did not wake: %+v", event)
	}
	// A drain that began before the watch and had already ended when the
	// watch began (never seen running) does not wake it, nor does another
	// drain that timed out.
	if event, _ := EvaluateMachineWatch(watch(), timedOut, now); event != nil {
		t.Fatalf("a drain that timed out before the watch woke it: %+v", event)
	}
	other := timedOut
	other.Drain = &MachineDrainState{TimedOut: true, EndReason: "expired", SinceMs: before - 1, Reason: "update refused"}
	if event, _ := EvaluateMachineWatch(w, other, now.Add(2*time.Minute)); event != nil {
		t.Fatalf("an older drain the watch never saw woke it: %+v", event)
	}
}
