package api

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// fleetAgentsFinishedFromFullRecords is the finished-task summary as it was
// read before task briefs: every task record of every project decoded
// (ListProjectTaskRecordsForList), filtered and sorted. The equivalence test
// holds readAgentsFinished to it.
func fleetAgentsFinishedFromFullRecords(s *Server, p identity.Principal, machine string, now, since int64, progressReads *int) ([]fleetAgentFinished, int, int) {
	recent := []fleetAgentFinished{}
	store := s.sessions.Store()
	projects, err := store.ListProjects(p.AccountScopeID, fleetAgentsProjects)
	if err != nil {
		return recent, 0, 0
	}
	finished := []pebblestore.ProjectTaskRecord{}
	for _, project := range projects {
		tasks, _, err := store.ListProjectTaskRecordsForList(p.AccountScopeID, project.ID)
		if err != nil {
			continue
		}
		for _, task := range tasks {
			class := taskStatusClass(task.Status)
			if task.Archived || pebblestore.ProjectTaskWorkflowSpawned(task) || class == progressWorking || class == progressWaiting {
				continue
			}
			finished = append(finished, task)
		}
	}
	sort.SliceStable(finished, func(i, j int) bool { return finished[i].UpdatedAt > finished[j].UpdatedAt })
	landed, failed := 0, 0
	for i, task := range finished {
		today := task.UpdatedAt >= since
		shown := i < fleetAgentsRecent
		if !today && !shown {
			break
		}
		if today && taskStatusClass(task.Status) == progressFailed {
			failed++
		}
		task.EnsureTaskAttempts()
		progress := s.taskProgressFor(p.AccountScopeID, task, now)
		*progressReads++
		if today && progress.TaskState == pebblestore.ProjectTaskStateLanded {
			landed++
		}
		if shown {
			recent = append(recent, fleetAgentFinished{Machine: machine, ProjectID: task.ProjectID, TaskID: task.ID, SessionID: task.SessionID, Title: task.Title,
				State: progress.TaskState, Reason: oneLineProgress(progress.TaskStateReason, maxProgressSummary), At: task.UpdatedAt})
		}
	}
	return recent, landed, failed
}

// seedRawTasks writes task records in one batch beside the canonical write
// path (so their change times are the fixture's) and builds their briefs.
func seedRawTasks(t *testing.T, store *pebblestore.SessionStore, account string, tasks []pebblestore.ProjectTaskRecord) {
	t.Helper()
	batch := store.Underlying().NewBatch()
	defer batch.Close()
	for _, task := range tasks {
		task.AccountID = account
		raw, err := json.Marshal(task)
		if err != nil {
			t.Fatal(err)
		}
		if err := batch.Set([]byte(pebblestore.KeyProjectTask(account, task.ProjectID, task.ID)), raw, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := batch.Commit(nil); err != nil {
		t.Fatal(err)
	}
	if err := store.RebuildProjectTaskBriefs(); err != nil {
		t.Fatal(err)
	}
}

// Purpose: the agents-at-work finished summary (recent tasks, landed and
// failed today, and which tasks' progress it reads) selected on task briefs
// is exactly the summary the whole-history read gave, on a store mixing
// several projects, archived tasks, workflow-step tasks, working and
// waiting tasks, every finished outcome, equal change times across and
// within projects, tasks just before and after "today" and a workflow
// delivery that lands a task, for several day starts.
// Threat: a cheaper read that shows different tasks, orders ties
// differently, counts a different day, or misses a project's newest
// finished task behind its unfinished ones.
// Boundary: Server.readAgentsFinished (ListProjectTaskBriefs,
// GetProjectTaskRecord, taskProgressFor) against the prior algorithm over
// ListProjectTaskRecordsForList, on one real Pebble store.
func TestFleetAgentsFinishedFromBriefsMatchesFullRecords(t *testing.T) {
	f := newStallFixture(t, filepath.Join(t.TempDir(), "fleet-equivalence.pebble"))
	defer f.close()
	store := f.server.sessions.Store()
	p := testPrincipal()
	now := time.Now().UnixMilli()
	day := now - 6*time.Hour.Milliseconds()
	projects := []string{"proj-one", "proj-two", "proj-three"}
	for _, id := range projects {
		if err := store.PutProject(f.account, &pebblestore.ProjectRecord{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	statuses := []string{"done", "failed", "in_progress", "queued", "stopped", "completed", "blocked", "limit_waiting", "cancelled", "pending_approval"}
	var tasks []pebblestore.ProjectTaskRecord
	for i := 0; i < 90; i++ {
		task := pebblestore.ProjectTaskRecord{ID: fmt.Sprintf("task_%03d", (i*37)%90), ProjectID: projects[i%3], Title: fmt.Sprintf("Task %d", i),
			Status: statuses[i%len(statuses)], Agent: "coder", SessionID: fmt.Sprintf("sess-%d", i), CreatedAt: day - 1000}
		// Change times around the day start, with runs of equal times.
		task.UpdatedAt = day + int64((i/4)-10)*60_000
		switch {
		case i%11 == 0:
			task.Archived = true
		case i%13 == 0:
			task.AutomationID = "workflow:review"
		case i%17 == 0:
			task.ID = "task_wf_" + task.ID
		}
		tasks = append(tasks, task)
	}
	// One project whose newest tasks are all unfinished, with older finished ones behind them.
	for i := 0; i < 12; i++ {
		tasks = append(tasks, pebblestore.ProjectTaskRecord{ID: fmt.Sprintf("busy_%02d", i), ProjectID: "proj-three", Title: "busy", Status: "in_progress",
			Agent: "coder", CreatedAt: day, UpdatedAt: now - int64(i)*1000})
	}
	seedRawTasks(t, store, f.account, tasks)
	// A landed task: a workflow delivery that succeeded with its result integrated.
	f.delivery(t, "proj-one", tasks[0].ID, "wf-land", "", []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer"}}, func(d *jobautomation.Delivery) {
		d.State, d.FinishedAt = jobautomation.StateSucceeded, now
		d.Steps[0].State = jobautomation.StateSucceeded
	})

	for _, since := range []int64{day, day - 5*60_000, day + 7*60_000, now - time.Minute.Milliseconds(), 0} {
		var gotReads, wantReads int
		gotRecent, gotLanded, gotFailed := f.server.readAgentsFinished(p, "here", now, since, &gotReads)
		wantRecent, wantLanded, wantFailed := fleetAgentsFinishedFromFullRecords(f.server, p, "here", now, since, &wantReads)
		if !reflect.DeepEqual(gotRecent, wantRecent) || gotLanded != wantLanded || gotFailed != wantFailed || gotReads != wantReads {
			t.Fatalf("since %d: briefs gave recent %+v landed %d failed %d (%d progress reads)\nfull records gave %+v landed %d failed %d (%d progress reads)",
				since, gotRecent, gotLanded, gotFailed, gotReads, wantRecent, wantLanded, wantFailed, wantReads)
		}
		if len(wantRecent) != fleetAgentsRecent {
			t.Fatalf("since %d: fixture shows %d tasks, want %d", since, len(wantRecent), fleetAgentsRecent)
		}
		if since == day && (wantFailed == 0 || wantReads <= fleetAgentsRecent) {
			t.Fatalf("fixture: since the day start want failures and more tasks today than shown, got failed %d reads %d", wantFailed, wantReads)
		}
	}
}

// Purpose: the stall check leaves a task alone on its brief (no full record
// read) only when the full verdict would leave it alone too: for every task
// the brief path calls old, the full verdict over the same records finds it
// past the horizon, reaches the same change times, and makes no worktree
// read. Tasks across statuses, with and without worktrees, workflow
// histories, armed limit waits and active or finished runs are checked at
// times inside the horizon, at its edge, past it and inside the worktree
// window with a short horizon.
// Threat: a cheaper check that counts or clears a task the full check would
// still follow (alerting nothing for a stall, or summarizing live work as
// old), or that spends the per-check Git budget differently.
// Boundary: Server.readTaskStallInputs / taskStallInputs.leftAlone against
// Server.taskStallVerdict on the full record, over real sessions, run
// states, deliveries and task records; taskStallGitFn counts worktree reads.
func TestTaskStallBriefDecisionMatchesFullVerdict(t *testing.T) {
	f := newStallFixture(t, filepath.Join(t.TempDir(), "stall-equivalence.pebble"))
	defer f.close()
	gitReads := 0
	f.server.taskStallGitFn = func(context.Context, pebblestore.ProjectTaskRecord) (taskStallGit, bool) {
		gitReads++
		return taskStallGit{Head: strings.Repeat("e", 40), Dirty: true}, true
	}
	steps := []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer"}}
	var ids []string
	statuses := []string{"in_progress", "done", "failed", "queued", "limit_waiting", "stopped", "pending_approval", "done"}
	for i, status := range statuses {
		s := f.session(t, fmt.Sprintf("equiv-%d", i))
		rec := pebblestore.ProjectTaskRecord{ID: fmt.Sprintf("task_equiv_%d", i), ProjectID: "proj-equiv", Agent: "coder", Status: status}
		if i%2 == 1 {
			rec.WorkspacePath = t.TempDir()
		}
		if status == "limit_waiting" {
			rec.LimitRetryAt = time.Now().Add(30 * time.Hour).UnixMilli()
		}
		task := taskSessionFixture(t, f.server, s, rec)
		switch i % 4 {
		case 1:
			f.settleRun(t, s.ID, pebblestore.InitialTaskRunID(task.ID), sessionruntime.RunIntentCompleted)
		case 2:
			f.settleRun(t, s.ID, pebblestore.InitialTaskRunID(task.ID), "")
		case 3:
			f.delivery(t, task.ProjectID, task.ID, "wf-equiv", "", steps, func(d *jobautomation.Delivery) {
				at := time.Now().Add(10 * time.Hour).UnixMilli()
				d.State, d.FinishedAt = jobautomation.StateSucceeded, at
				d.Steps[0].State, d.Steps[0].FinishedAt = jobautomation.StateSucceeded, at
			})
		}
		ids = append(ids, task.ID)
	}
	db := f.server.sessions.Store()
	limits, err := db.GetLimits(f.account)
	if err != nil {
		t.Fatal(err)
	}
	short := limits
	short.StallHorizonHours = 1
	start := time.Now()
	leftAlone := 0
	for _, lim := range []pebblestore.Limits{limits, short} {
		for _, at := range []time.Duration{0, 30 * time.Minute, 3 * time.Hour, 23 * time.Hour, 25 * time.Hour, 40 * time.Hour, 72 * time.Hour} {
			now := start.Add(at)
			for _, id := range ids {
				// One record, read once: its brief and its full form (a run
				// may still write the task in the background).
				full, found, err := db.GetProjectTask(f.account, "proj-equiv", id)
				if err != nil || !found {
					t.Fatalf("task %s: %v %v", id, found, err)
				}
				brief := pebblestore.ProjectTaskBriefOf(*full)
				in, ok := f.server.readTaskStallInputs(f.account, brief)
				if !ok {
					t.Fatalf("inputs of %s not read", brief.ID)
				}
				before := gitReads
				budget := taskStallGitPerTick
				f.server.taskStallGitMu.Lock()
				f.server.taskStallGitCache = nil
				f.server.taskStallGitMu.Unlock()
				v, ok := f.server.taskStallVerdict(context.Background(), f.account, *full, in, now, lim, &budget)
				if !ok {
					t.Fatalf("verdict of %s not reached", brief.ID)
				}
				if v.LastChange != in.lastChange || v.Recent != in.recent {
					t.Fatalf("%s at +%v: brief times %d/%d, verdict %d/%d", brief.ID, at, in.lastChange, in.recent, v.LastChange, v.Recent)
				}
				fullOld := v.Recent > 0 && now.Sub(time.UnixMilli(v.Recent)) > taskStallHorizon(lim)
				if in.leftAlone(brief, now, lim) {
					leftAlone++
					if !fullOld || gitReads != before {
						t.Fatalf("%s at +%v (horizon %dh): left alone on its brief, but the full verdict is old=%v with %d worktree reads", brief.ID, at, lim.StallHorizonHours, fullOld, gitReads-before)
					}
				}
			}
		}
	}
	if leftAlone == 0 {
		t.Fatal("fixture: no task was left alone on its brief")
	}
}

// Purpose: on a store of a few thousand finished tasks with large results
// (diffs and code deliverables of tens of KB each), one agents-at-work
// finished read and one stall check allocate a small bounded amount, far
// below what decoding the whole history allocates, and decode only the
// full records of the tasks they return or act on: the shown and today's
// finished tasks for the fleet read; for the stall check, only the tasks
// still inside its horizon (today's and those with an armed wait).
// Threat: the deploy server's memory and CPU blowup: 335 GB allocated in 15
// minutes, 99.5% in decoding every task record's full JSON for GET
// /v3/fleet/agents every 5 s and for the stall check every minute.
// Boundary: Server.readAgentsFinished and Server.CheckStalledTasks over a
// real Pebble store; full-record decodes are counted by the store
// (SessionStore.ProjectTaskRecordDecodes) and allocation by
// runtime.MemStats.TotalAlloc deltas, compared with the whole-project reads
// the two callers used before (ListProjectTaskRecordsForList,
// ListProjectTasksByArchive) on the same store.
func TestTaskHistoryReadsCostWhatTheyReturn(t *testing.T) {
	f := newStallFixture(t, filepath.Join(t.TempDir(), "history-scale.pebble"))
	defer f.close()
	f.server.taskStallGitFn = func(context.Context, pebblestore.ProjectTaskRecord) (taskStallGit, bool) {
		t.Error("the stall check read a worktree")
		return taskStallGit{}, false
	}
	store := f.server.sessions.Store()
	p := testPrincipal()
	const project, history, today, waiting = "proj-history", 3000, 5, 4
	if err := store.PutProject(f.account, &pebblestore.ProjectRecord{ID: project, Name: "History"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	nowMs := now.UnixMilli()
	old := now.Add(-72 * time.Hour).UnixMilli()
	diff := strings.Repeat("+ a changed line of generated code in the result diff\n", 450) // ~24 KB
	var tasks []pebblestore.ProjectTaskRecord
	storedBytes := 0
	for i := 0; i < history+today+waiting; i++ {
		task := pebblestore.ProjectTaskRecord{ID: fmt.Sprintf("task_%05d", i), ProjectID: project, Title: fmt.Sprintf("Run %d", i), Status: "done", Agent: "coder",
			SessionID: fmt.Sprintf("sess-%05d", i), CreatedAt: old - int64(history-i)*1000, UpdatedAt: old + int64(i), DiffSummary: diff,
			Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "d1", Title: "Diff", Kind: "code_diff", Status: "ready", CodeDiff: diff}}}
		switch {
		case i >= history+today: // an armed provider-limit wait: still followed
			task.Status, task.LimitRetryAt = pebblestore.ProjectTaskStatusLimitWaiting, nowMs+time.Hour.Milliseconds()
		case i >= history: // ended in the last hour
			task.UpdatedAt = nowMs - int64(history+today-i)*time.Minute.Milliseconds()
			if i%2 == 0 {
				task.Status = "failed"
			}
		}
		raw, _ := json.Marshal(task)
		storedBytes += len(raw)
		tasks = append(tasks, task)
	}
	seedRawTasks(t, store, f.account, tasks)

	measure := func(read func()) (alloc uint64, decodes int64) {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		decoded := store.ProjectTaskRecordDecodes()
		read()
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc, store.ProjectTaskRecordDecodes() - decoded
	}
	// What the two callers decoded per call before: every record, in full.
	fullList, fullDecodes := measure(func() {
		if _, _, err := store.ListProjectTaskRecordsForList(f.account, project); err != nil {
			t.Fatal(err)
		}
	})
	fullBoard, _ := measure(func() {
		if _, err := store.ListProjectTasksByArchive(f.account, project, false); err != nil {
			t.Fatal(err)
		}
	})
	if fullDecodes != int64(len(tasks)) {
		t.Fatalf("fixture: the whole-project read decoded %d records, want %d", fullDecodes, len(tasks))
	}

	var reads int
	var recent []fleetAgentFinished
	var failed int
	since := nowMs - time.Hour.Milliseconds()
	fleetAlloc, fleetDecodes := measure(func() {
		recent, _, failed = f.server.readAgentsFinished(p, "here", nowMs, since, &reads)
	})
	stallAlloc, stallDecodes := measure(func() { f.check(now) })
	t.Logf("stored %d tasks, %d MB of records; whole-project reads allocate %d MB (records) and %d MB (board)", len(tasks), storedBytes>>20, fullList>>20, fullBoard>>20)
	t.Logf("fleet finished read: %d KB, %d full records; stall check: %d KB, %d full records", fleetAlloc>>10, fleetDecodes, stallAlloc>>10, stallDecodes)

	if len(recent) != fleetAgentsRecent || recent[0].TaskID != fmt.Sprintf("task_%05d", history+today-1) || failed != (today+1)/2 {
		t.Fatalf("fleet read = %d recent (first %+v), %d failed; want %d, the newest first, %d failed", len(recent), recent[0], failed, fleetAgentsRecent, (today+1)/2)
	}
	if fleetDecodes != fleetAgentsRecent {
		t.Fatalf("fleet read decoded %d full task records, want %d: the tasks it shows (today's among them)", fleetDecodes, fleetAgentsRecent)
	}
	if stallDecodes != today+waiting {
		t.Fatalf("stall check decoded %d full task records, want %d: today's tasks and the armed waits", stallDecodes, today+waiting)
	}
	// Generous absolute bounds, and far below the whole-history decode.
	const fleetBound, stallBound = 8 << 20, 64 << 20
	if fleetAlloc > fleetBound || fleetAlloc > fullList/20 {
		t.Fatalf("fleet read allocated %d bytes, want under %d and under 1/20 of the whole-history read (%d)", fleetAlloc, fleetBound, fullList)
	}
	if stallAlloc > stallBound || stallAlloc > fullBoard/4 {
		t.Fatalf("stall check allocated %d bytes, want under %d and under 1/4 of the whole-history read (%d)", stallAlloc, stallBound, fullBoard)
	}
}
