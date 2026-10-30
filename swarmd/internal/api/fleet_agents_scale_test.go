package api

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// fleetAgentsScaleTasks and fleetAgentsScaleToday shape a machine like the
// deploy server: thousands of finished worker runs (a scheduled catalog
// refresh), each a planned task bound to its run's plan, a few of them today.
const (
	fleetAgentsScaleTasks = 3000
	fleetAgentsScaleToday = 10
	fleetAgentsScalePlan  = 16 << 10
	// fleetAgentsScaleBound is generous: the read takes about 0.13 s here
	// (about 1 s under -race); before the change it took 1.06 s on this
	// store without -race.
	fleetAgentsScaleBound = 2 * time.Second
)

func fleetAgentsScaleProgressReads(f *fleetTestSwarm) int {
	f.s.fleetAgentsFinished.mu.Lock()
	defer f.s.fleetAgentsFinished.mu.Unlock()
	return f.s.fleetAgentsFinished.progressReads
}

// fleetAgentsScaleSeed writes the finished worker-run tasks and their plans
// in one batch, as the canonical records a daemon leaves behind (the read
// under test reads only the canonical task records). Tasks are created in
// id order; the last fleetAgentsScaleToday ended within the hour, every
// third of them failed; the rest ended two days ago.
func fleetAgentsScaleSeed(t *testing.T, f *fleetTestSwarm, now int64) {
	t.Helper()
	store := f.s.sessions.Store()
	if err := store.PutProject(f.account, &pebblestore.ProjectRecord{ID: "catalog", Name: "Catalog"}); err != nil {
		t.Fatal(err)
	}
	batch := store.Underlying().NewBatch()
	defer batch.Close()
	plan := strings.Repeat("refresh the model catalog and verify every provider entry. ", fleetAgentsScalePlan/60)
	old := now - 2*24*time.Hour.Milliseconds()
	for i := 0; i < fleetAgentsScaleTasks; i++ {
		id := fmt.Sprintf("run-%05d", i)
		session, planID := "worker-"+id, "plan-"+id
		task := pebblestore.ProjectTaskRecord{ID: id, ProjectID: "catalog", AccountID: f.account, Title: "Catalog refresh " + id, Status: "done", Agent: "swarm",
			WorkerID: "catalog-refresh", WorkerName: "Catalog refresh", WorkerRunID: id, SessionID: session,
			PlanBinding: &pebblestore.ProjectTaskPlanBinding{PlanID: planID, SessionID: session},
			CreatedAt:   old - int64(fleetAgentsScaleTasks-i)*1000, UpdatedAt: old + int64(i)}
		if today := i - (fleetAgentsScaleTasks - fleetAgentsScaleToday); today >= 0 {
			task.UpdatedAt = now - int64(fleetAgentsScaleToday-today)*time.Minute.Milliseconds()
			if today%3 == 0 {
				task.Status, task.LastError = "failed", "provider timed out"
			}
		}
		raw, err := json.Marshal(task)
		if err != nil {
			t.Fatal(err)
		}
		if err := batch.Set([]byte(pebblestore.KeyProjectTask(f.account, "catalog", id)), raw, nil); err != nil {
			t.Fatal(err)
		}
		snapshot := pebblestore.SessionPlanSnapshot{ID: planID, SessionID: session, AccountScopeID: f.account, Title: "Catalog refresh", Plan: plan,
			Document: &pebblestore.SessionPlanDocument{}, Status: "completed", ApprovalState: "approved"}
		if raw, err = json.Marshal(snapshot); err != nil {
			t.Fatal(err)
		}
		if err := batch.Set([]byte(pebblestore.KeySessionPlan(session, planID)), raw, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := batch.Commit(nil); err != nil {
		t.Fatal(err)
	}
}

// Purpose: the agents-at-work read (GET /v3/fleet/agents, polled every few
// seconds by Swarm Control and the console) stays cheap on a machine with
// thousands of finished worker runs and nothing running: it answers in well
// under a second, lists no agents, and its recent list and today's counts
// are of the newest finished tasks. Task progress is read only for the
// tasks that ended today (the 8 shown are among them), never once per task
// in the store. A repeat read within
// fleetAgentsFinishedTTL is served from the summary kept by
// localAgentsFinished; once that passes, a task that ended since is shown.
// Threat: the 30-second read seen on the deploy server, where every task of
// every project was hydrated (its session, run state, terminal event and
// plan document, read twice) on each poll; and the old task bound, which
// kept the 1000 oldest-created tasks of a project, so a large project's
// newest finished tasks were never listed or counted.
// Boundary: Server.handleFleetAgents (FleetAgents, localAgentsFinished,
// readAgentsFinished) over a real Pebble store with canonical task and plan
// records: the narrowest layer that holds the route's reads and its cache.
func TestFleetAgentsStaysFastOnAStoreOfFinishedWorkerRuns(t *testing.T) {
	a := newFleetTestSwarm(t, "deploy")
	t.Cleanup(a.cleanup)
	now := time.Now().UnixMilli()
	fleetAgentsScaleSeed(t, a, now)
	since := now - time.Hour.Milliseconds()
	query := "?since=" + strconv.FormatInt(since, 10)

	start := time.Now()
	view := fleetAgentsTestRead(t, a, query)
	elapsed := time.Since(start)
	t.Logf("cold read over %d finished tasks: %v", fleetAgentsScaleTasks, elapsed)
	if elapsed > fleetAgentsScaleBound {
		t.Fatalf("cold read took %v over %d finished tasks, want under %v", elapsed, fleetAgentsScaleTasks, fleetAgentsScaleBound)
	}
	if reads := fleetAgentsScaleProgressReads(a); reads != fleetAgentsScaleToday {
		t.Fatalf("cold read made %d task progress reads, want %d: one per task that ended today", reads, fleetAgentsScaleToday)
	}
	if len(view.Agents) != 0 || view.Totals.Agents != 0 {
		t.Fatalf("agents = %+v, want none: nothing holds a lease", view.Agents)
	}
	wantFailed := (fleetAgentsScaleToday + 2) / 3
	m, ok := fleetAgentsTestMachine(view, "deploy")
	if !ok || m.State != "online" || m.FailedToday != wantFailed || view.Totals.FailedToday != wantFailed || view.Totals.LandedToday != 0 {
		t.Fatalf("machine = %+v (%v), totals %+v: want %d failed today", m, ok, view.Totals, wantFailed)
	}
	if len(view.Recent) != fleetAgentsRecent {
		t.Fatalf("recent = %d tasks, want %d", len(view.Recent), fleetAgentsRecent)
	}
	newest := fmt.Sprintf("run-%05d", fleetAgentsScaleTasks-1)
	if view.Recent[0].TaskID != newest || view.Recent[0].State == "" || view.Recent[0].Machine != "deploy" {
		t.Fatalf("recent[0] = %+v, want the newest task %s with its state", view.Recent[0], newest)
	}
	for i, r := range view.Recent {
		if r.At < since || (i > 0 && r.At > view.Recent[i-1].At) {
			t.Fatalf("recent = %+v, want today's tasks newest first", view.Recent)
		}
	}

	// Within the TTL a repeat read is served from the kept summary: a task
	// that failed since is not read yet.
	clock := time.Now()
	a.s.fleetAgentsFinished.mu.Lock()
	a.s.fleetAgentsFinished.now = func() time.Time { return clock }
	a.s.fleetAgentsFinished.entries = nil
	a.s.fleetAgentsFinished.mu.Unlock()
	fleetAgentsTestRead(t, a, query)
	before := fleetAgentsScaleProgressReads(a)
	store := a.s.sessions.Store()
	if err := store.PutProjectTask(a.account, &pebblestore.ProjectTaskRecord{ID: "run-late", ProjectID: "catalog", AccountID: a.account, Title: "Catalog refresh late", Status: "failed",
		Agent: "swarm", CreatedAt: now, UpdatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	cached := fleetAgentsTestRead(t, a, query)
	t.Logf("read within the TTL: %v", time.Since(start))
	if cached.Totals.FailedToday != wantFailed || cached.Recent[0].TaskID != newest {
		t.Fatalf("read within the TTL = %+v %+v, want the kept summary", cached.Totals, cached.Recent[0])
	}
	if reads := fleetAgentsScaleProgressReads(a); reads != before {
		t.Fatalf("read within the TTL made %d task progress reads, want none", reads-before)
	}
	a.s.fleetAgentsFinished.mu.Lock()
	clock = clock.Add(fleetAgentsFinishedTTL)
	a.s.fleetAgentsFinished.mu.Unlock()
	fresh := fleetAgentsTestRead(t, a, query)
	if fresh.Totals.FailedToday != wantFailed+1 || fresh.Recent[0].TaskID != "run-late" {
		t.Fatalf("read after the TTL = %+v %+v, want the late failure counted and first", fresh.Totals, fresh.Recent[0])
	}
}
