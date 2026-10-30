package api

import (
	"context"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/jobautomation"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the stall check runs every minute for the life of the daemon, so
// one check must leave nothing behind. After many checks over a store with
// many projects and tasks of every shape the check reads (tasks with a long
// workflow delivery history, sessions with events, finished tasks with
// worktrees, a silent active run, legacy worker-run mirrors of a deleted
// worker, and tasks that alert and wake their delegator), closing the store
// reports no leaked Pebble iterator or snapshot, and further checks after a
// warm-up do not grow the heap or the goroutine count. Repetition also sends
// each task at most one task.stalled.
// Threat: the swarmd memory blowup after the stall detector shipped (12 GB
// within 27 minutes, OOM kills, "close store: leaked iterators" on shutdown):
// a read on the check's path that keeps an iterator, snapshot, goroutine or
// per-tick state alive.
// Boundary: Server.CheckStalledTasks over a real Pebble store
// (pebblestore.Store.Close reports leaked iterators and snapshots), real V3
// sessions and run states, workflow deliveries, worker records and the
// signal store. The worktree read is replaced by taskStallGitFn (Git output is
// bounded separately by the delivery assessment); no provider is reached.
// Heap is measured with runtime.ReadMemStats after forced collections with a
// generous threshold, so the check is not timing- or allocator-sensitive.
func TestTaskStallRepeatedChecksLeakNothingAndStayBounded(t *testing.T) {
	f := newStallFixture(t, filepath.Join(t.TempDir(), "memory.pebble"))
	f.server.taskStallGitFn = func(context.Context, pebblestore.ProjectTaskRecord) (taskStallGit, bool) {
		return taskStallGit{Head: strings.Repeat("c", 40), Dirty: true}, true
	}
	p := testPrincipal()
	ws := pebblestore.NewWorkerStore(f.server.sessions.Store().Underlying())
	retired, err := ws.CreateWorker(f.account, p.UserID, pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleCoder, Name: "Old coder", IdempotencyKey: "memory-retired"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	origin := f.session(t, "memory-orchestrator")
	steps := []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer"}, {ID: "fix", Type: jobautomation.StepReopen, From: "review", MaxRounds: 1}}
	blocked := func(d *jobautomation.Delivery) {
		at := time.Now().UnixMilli()
		d.State, d.StartedAt, d.FinishedAt = jobautomation.StateBlocked, at, at
		d.BlockedReason = jobautomation.RoundsExhaustedReason("fix")
		d.Steps[0].State, d.Steps[0].FinishedAt = jobautomation.StateSucceeded, at
		d.Steps[0].Outcome = map[string]any{"verdict": "block", "findings": "1. high api/a.go:1 " + strings.Repeat("unchecked ", 200)}
		d.Steps[1].State, d.Steps[1].FinishedAt, d.Steps[1].Reason = jobautomation.StateFailed, at, jobautomation.RoundsExhaustedReason("fix")
	}
	const projects, tasks = 6, 60
	noNextStep := []string{}
	for i := 0; i < tasks; i++ {
		id := "m" + strconv.Itoa(i)
		project := "proj-memory-" + strconv.Itoa(i%projects)
		name := "memory-" + id
		if i%5 == 2 {
			name = "worker-execution-wrun_" + id
		}
		s := f.session(t, name)
		rec := pebblestore.ProjectTaskRecord{ID: "task_" + id, ProjectID: project, Agent: "coder", Status: "in_progress", OriginSessionID: origin.ID}
		switch i % 5 {
		case 0:
			noNextStep = append(noNextStep, rec.ID)
		case 2: // a legacy worker-run mirror
			if _, err := ws.RecordWorkerRun(f.account, pebblestore.WorkerRunRecord{ID: "wrun_" + id, WorkerID: retired.ID, UserID: p.UserID, Role: pebblestore.WorkerRoleCoder, SessionID: s.ID, Status: "failed", Error: "old run", CreatedAt: 1, StartedAt: 1, CompletedAt: 2}); err != nil {
				t.Fatal(err)
			}
			rec.WorkerID, rec.WorkerRunID = retired.ID, "wrun_"+id
		case 3: // finished with a worktree: the uncommitted check
			rec.WorkspacePath = t.TempDir()
		}
		task := taskSessionFixture(t, f.server, s, rec)
		switch i % 5 {
		case 1, 2: // a finished run and a long review history
			f.settleRun(t, s.ID, pebblestore.InitialTaskRunID(task.ID), sessionruntime.RunIntentCompleted)
			for k := 0; k < 12; k++ {
				f.delivery(t, project, task.ID, "wf-"+id, "followup-"+strconv.Itoa(k), steps, blocked)
			}
		case 3:
			f.settleRun(t, s.ID, pebblestore.InitialTaskRunID(task.ID), sessionruntime.RunIntentCompleted)
		case 4: // an active run that goes silent
			f.settleRun(t, s.ID, pebblestore.InitialTaskRunID(task.ID), "")
		}
	}
	if err := ws.DeleteWorker(f.account, p.UserID, retired.ID, retired.Revision); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	tick := 0
	check := func() {
		f.check(start.Add(time.Duration(tick) * 5 * time.Minute))
		tick++
	}
	var ms runtime.MemStats
	heap := func() uint64 {
		runtime.GC()
		runtime.GC()
		runtime.ReadMemStats(&ms)
		return ms.HeapAlloc
	}
	for tick < 8 { // warm-up: alerts, recoveries and caches settle
		check()
	}
	baseHeap, baseGoroutines := heap(), runtime.NumGoroutine()
	for tick < 48 {
		check()
	}
	grownHeap, grownGoroutines := heap(), runtime.NumGoroutine()
	const heapSlack = 64 << 20 // generous: a leak here grows by hundreds of MB
	if grownHeap > baseHeap+heapSlack {
		t.Fatalf("heap grew from %d to %d bytes over 40 checks", baseHeap, grownHeap)
	}
	if grownGoroutines > baseGoroutines+5 {
		t.Fatalf("goroutines grew from %d to %d over 40 checks", baseGoroutines, grownGoroutines)
	}
	for _, id := range noNextStep {
		if n := len(f.signals(t, "task.stalled", id)); n != 1 {
			t.Fatalf("task %s: %d task.stalled over %d checks, want exactly 1", id, n, tick)
		}
	}
	f.server.taskStallGitMu.Lock()
	cached := len(f.server.taskStallGitCache)
	f.server.taskStallGitMu.Unlock()
	if cached > tasks {
		t.Fatalf("worktree cache holds %d entries for %d tasks", cached, tasks)
	}
	if err := f.server.sessions.Store().Underlying().Close(); err != nil {
		t.Fatalf("close store after %d checks: %v", tick, err)
	}
}

// Purpose: the check reads each task's delivery history through
// JobProgressDeliveries (each workflow's newest delivery in full; of older
// ones only ordering, states, times and job ends) instead of decoding every
// delivery whole every minute. Everything the check decides on is exactly
// what the full history gives: task state and its reason, outcome, summary,
// integration, workflow, idle and stale flags, attention and the time of the
// latest history entry (an older delivery's late step included). The newest
// delivery of each workflow is the complete record, and older ones carry no
// definition, outcome or result body.
// Threat: a cheaper read that silently changes a stall decision (a
// different idle time, a missing workflow, an older delivery taken as the
// newest), or one that still decodes the whole history.
// Boundary: jobautomation.Store.JobProgressDeliveries against
// jobautomation.Store.JobDeliveries over real deliveries in a Pebble store,
// compared through computeTaskProgress and markTaskProgressAttention, the
// functions the check uses.
func TestTaskStallProgressDeliveriesMatchFullHistory(t *testing.T) {
	f := newStallFixture(t, filepath.Join(t.TempDir(), "history.pebble"))
	defer f.close()
	work := f.session(t, "history-task")
	task := taskSessionFixture(t, f.server, work, pebblestore.ProjectTaskRecord{ID: "task_history", ProjectID: "proj-history", Agent: "coder", Status: "in_progress"})
	f.settleRun(t, work.ID, pebblestore.InitialTaskRunID(task.ID), sessionruntime.RunIntentCompleted)
	steps := []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer"}}
	base := time.Now().Add(-time.Hour).UnixMilli()
	n := 0
	for _, wf := range []string{"wf-a", "wf-b", "wf-c"} {
		for k := 0; k < 5; k++ {
			n++
			at := base + int64(n)*60_000
			commits := k
			f.delivery(t, task.ProjectID, task.ID, wf, "fu-"+wf+"-"+strconv.Itoa(k), steps, func(d *jobautomation.Delivery) {
				d.CreatedAt, d.StartedAt, d.FinishedAt = at, at, at+1000
				d.State = []string{jobautomation.StateSucceeded, jobautomation.StateBlocked, jobautomation.StateFailed}[k%3]
				d.Steps[0].State, d.Steps[0].StartedAt, d.Steps[0].FinishedAt = jobautomation.StateSucceeded, at, at+1000
				if wf == "wf-a" && k == 0 {
					// The oldest delivery holds the latest history entry.
					d.Steps[0].FinishedAt = base + 10*time.Hour.Milliseconds()
				}
				d.Steps[0].Outcome = map[string]any{"verdict": "block", "findings": "finding " + strconv.Itoa(n)}
				d.Result.Status = []string{jobautomation.StatusSucceeded, jobautomation.StatusFailed}[k%2]
				d.Result.FinishedAt = at + 500
				d.Result.Git = &jobautomation.ResultGit{Branch: "agent/" + wf, Commits: make([]jobautomation.ResultCommit, commits)}
				for c := range d.Result.Git.Commits {
					d.Result.Git.Commits[c] = jobautomation.ResultCommit{SHA: strings.Repeat("d", 40), Subject: "commit " + strconv.Itoa(c)}
				}
			})
		}
	}
	store, err := f.server.jobAutomationStore()
	if err != nil {
		t.Fatal(err)
	}
	job := taskProgressJobID(f.task(t, task.ProjectID, task.ID))
	full, err := store.JobDeliveries(f.account, job)
	if err != nil || len(full) != 15 {
		t.Fatalf("full history %d %v", len(full), err)
	}
	light, err := store.JobProgressDeliveries(f.account, job)
	if err != nil {
		t.Fatal(err)
	}
	if len(light) != len(full) {
		t.Fatalf("progress history has %d deliveries, full history %d", len(light), len(full))
	}
	newest := map[string]bool{}
	for i := range full {
		if light[i].ID != full[i].ID {
			t.Fatalf("order differs at %d: %s vs %s", i, light[i].ID, full[i].ID)
		}
		if !newest[full[i].AutomationID] {
			newest[full[i].AutomationID] = true
			if !reflect.DeepEqual(light[i], full[i]) {
				t.Fatalf("newest delivery of %s is not the complete record", full[i].AutomationID)
			}
			continue
		}
		if full[i].Result.JobID == "" || len(full[i].Definition) == 0 || full[i].Steps[0].Outcome == nil {
			t.Fatalf("fixture: delivery %s has no body to leave out", full[i].ID)
		}
		if len(light[i].Definition) != 0 || light[i].Spec != nil || light[i].Result.JobID != "" || (len(light[i].Steps) > 0 && light[i].Steps[0].Outcome != nil) {
			t.Fatalf("older delivery %s was decoded whole: %+v", light[i].ID, light[i])
		}
		if len(light[i].Steps) != len(full[i].Steps) || light[i].Steps[0].FinishedAt != full[i].Steps[0].FinishedAt {
			t.Fatalf("older delivery %s lost its step times", light[i].ID)
		}
	}
	// What the check reads from progress, and what decides it.
	type decided struct {
		TaskState, TaskStateReason, Outcome, Summary, State string
		Idle, Stale, NeedsAttention                         bool
		UpdatedAt, LatestHistory                            int64
		Integration, Workflow                               any
	}
	view := func(p taskProgress) decided {
		latest := int64(0)
		for _, e := range p.History {
			latest = max(latest, e.At)
		}
		return decided{p.TaskState, p.TaskStateReason, p.Outcome, p.Summary, p.State, p.Idle, p.Stale, p.NeedsAttention, p.UpdatedAt, latest, p.Integration, p.Workflow}
	}
	stored := f.task(t, task.ProjectID, task.ID)
	eval := &jobautomation.JobEvaluation{JobID: job, Followup: "fu-wf-a-4", EndedAt: base + 30*60_000}
	for _, at := range []int64{base + 20*60_000, time.Now().UnixMilli(), time.Now().Add(48 * time.Hour).UnixMilli()} {
		for _, e := range []*jobautomation.JobEvaluation{nil, eval} {
			want := computeTaskProgress(stored, full, e, at)
			markTaskProgressAttention(&want, stored)
			got := computeTaskProgress(stored, light, e, at)
			markTaskProgressAttention(&got, stored)
			if g, w := view(got), view(want); !reflect.DeepEqual(g, w) {
				t.Fatalf("progress at %d (eval %v) differs:\n got %+v\nwant %+v", at, e != nil, g, w)
			}
			if view(want).LatestHistory != base+10*time.Hour.Milliseconds() {
				t.Fatalf("fixture: latest history entry %d is not the old delivery's late step", view(want).LatestHistory)
			}
		}
	}
}
