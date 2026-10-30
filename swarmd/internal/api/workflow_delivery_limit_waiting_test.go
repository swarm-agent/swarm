package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: a workflow delivery whose only unfinished work is a step
// waiting for a provider's limit to reset (no step running) shows state
// "waiting" with until (the reset, unix ms) and reason (the step's reason,
// built from the worker run's queue_reason) in GET /v3/workflows
// latest_deliveries, the delivery reads (GET /v3/job-deliveries and
// /v3/job-deliveries/{id}) and the fleet's compact list that passes the
// newest delivery on; a state=waiting filter finds it and state=running does
// not. Once the step runs again the delivery shows "running" again, and a
// delivery with a step running shows "running" even when another step waits
// on a limit. The stored delivery stays running (unchanged): only what is
// shown changes.
// Threat (seen live): a delivery parked for days on a provider's weekly
// limit showed "running", so nothing could tell working now from parked
// until a reset, and a machine that powers off when idle stayed up.
// Owners: jobautomation.Delivery.Status, putDeliveryStatus (jobDeliveryView,
// workflowDeliveryLine), Store.ListDeliveries' state filter,
// handleJobAutomations, handleJobDeliveries and compactFleetList over the
// real Pebble-backed jobautomation store.
// Layer: the HTTP handlers are the narrowest layer that shows what a client
// reads.
func TestWorkflowDeliveryShowsWaitingWhileItsStepWaitsOnAProviderLimit(t *testing.T) {
	server, store := newRunOwnerServer(t)
	account := testPrincipal().AccountScopeID
	folder := t.TempDir()
	a, _, err := store.Put(account, jobautomation.Automation{Name: "review loop", Scope: jobautomation.Scope{Folder: folder, Role: "coder"}, Enabled: true, Steps: []jobautomation.Step{
		{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer", Brief: "Review the changes."},
		{ID: "send-back", Type: jobautomation.StepReopen, From: "review", MaxRounds: 2, If: []jobautomation.Condition{{Step: "review", Verdict: jobautomation.VerdictBlock}}},
		{ID: "merge", Type: jobautomation.StepRun, Command: "/bin/true"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	d, created, err := store.CreateDelivery(a, jobautomation.Result{V: jobautomation.ResultVersion, JobID: "task/proj-1/task-1", Kind: jobautomation.KindTask, Role: "coder", Folder: folder,
		Status: jobautomation.StatusSucceeded, Git: &jobautomation.ResultGit{Head: strings.Repeat("a", 40)}})
	if err != nil || !created {
		t.Fatalf("create delivery: %v %v", created, err)
	}
	until := time.Date(2026, 11, 2, 16, 0, 0, 0, time.UTC).UnixMilli()
	reason := "waiting for the provider's limit to reset (until 2026-11-02T16:00:00Z): You've hit your weekly limit"
	d.State, d.StartedAt = jobautomation.StateRunning, until-3*24*3600*1000
	d.Steps[0].State, d.Steps[0].Attempts, d.Steps[0].NextAttemptAt, d.Steps[0].Reason, d.Steps[0].LimitWaits = jobautomation.StateWaiting, 1, until, reason, 1
	save := func() {
		t.Helper()
		if err := store.SaveDelivery(d); err != nil {
			t.Fatal(err)
		}
	}
	save()

	get := func(path string, handle func(http.ResponseWriter, *http.Request)) map[string]any {
		t.Helper()
		r := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodGet, path, nil)), &pebblestore.ScopedTokenRecord{Scopes: []string{"automations:read"}})
		w := httptest.NewRecorder()
		handle(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, w.Code, w.Body)
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	latest := func() map[string]any {
		t.Helper()
		list := get(JobAutomationsPath, server.handleJobAutomations)["automations"].([]any)
		return list[0].(map[string]any)["latest_deliveries"].([]any)[0].(map[string]any)
	}
	listed := func(state string) []any {
		t.Helper()
		return get(JobDeliveriesPath+"?state="+state, server.handleJobDeliveries)["deliveries"].([]any)
	}
	read := func() map[string]any {
		t.Helper()
		return get(JobDeliveriesPath+"/"+d.ID, server.handleJobDeliveries)["delivery"].(map[string]any)
	}
	fleetLast := func() map[string]any {
		t.Helper()
		r := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodGet, JobAutomationsPath, nil)), &pebblestore.ScopedTokenRecord{Scopes: []string{"automations:read"}})
		w := httptest.NewRecorder()
		server.handleJobAutomations(w, r)
		compact, err := compactFleetList(w.Body.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(compact, &out); err != nil {
			t.Fatal(err)
		}
		return out["automations"].([]any)[0].(map[string]any)["last_delivery"].(map[string]any)
	}
	wantWaiting := func(where string, view map[string]any) {
		t.Helper()
		if view["state"] != jobautomation.StateWaiting || view["until"] != float64(until) || view["reason"] != reason {
			t.Fatalf("%s: a delivery parked on a provider limit = %v, want waiting until %d with the reason", where, view, until)
		}
	}
	wantRunning := func(where string, view map[string]any) {
		t.Helper()
		if view["state"] != jobautomation.StateRunning || view["until"] != nil || view["reason"] != nil {
			t.Fatalf("%s: a delivery with a step running = %v, want running without until or reason", where, view)
		}
	}

	line := latest()
	wantWaiting("GET /v3/workflows latest_deliveries", line)
	if steps := line["steps"].([]any); steps[0].(map[string]any)["state"] != jobautomation.StateWaiting || steps[1].(map[string]any)["state"] != jobautomation.StatePending {
		t.Fatalf("step lines = %v", steps)
	}
	wantWaiting("GET /v3/job-deliveries/{id}", read())
	if waiting := listed(jobautomation.StateWaiting); len(waiting) != 1 {
		t.Fatalf("state=waiting lists %d deliveries, want the parked one", len(waiting))
	} else {
		wantWaiting("GET /v3/job-deliveries?state=waiting", waiting[0].(map[string]any))
	}
	if running := listed(jobautomation.StateRunning); len(running) != 0 {
		t.Fatalf("state=running lists the parked delivery: %v", running)
	}
	wantWaiting("fleet last_delivery", fleetLast())
	// What is stored is unchanged: the delivery is unfinished and running.
	if stored, err := store.GetDelivery(account, d.ID); err != nil || stored.State != jobautomation.StateRunning || jobautomation.Terminal(stored.State) {
		t.Fatalf("stored delivery = %s (%v), want running", stored.State, err)
	}

	// The reset came and the step runs again: running.
	d.Steps[0].State, d.Steps[0].NextAttemptAt, d.Steps[0].Reason, d.Steps[0].Attempts = jobautomation.StateRunning, 0, "", 2
	save()
	wantRunning("GET /v3/workflows after the reset", latest())
	wantRunning("GET /v3/job-deliveries/{id} after the reset", read())
	if running := listed(jobautomation.StateRunning); len(running) != 1 {
		t.Fatalf("state=running after the reset lists %d deliveries", len(running))
	}
	if waiting := listed(jobautomation.StateWaiting); len(waiting) != 0 {
		t.Fatalf("state=waiting after the reset lists %v", waiting)
	}

	// Mixed: one step running and one waiting on a limit: running.
	d.Steps[2].State, d.Steps[2].NextAttemptAt, d.Steps[2].Reason = jobautomation.StateWaiting, until, reason
	save()
	wantRunning("mixed delivery", latest())
	wantRunning("mixed delivery read", read())
}

// Requirement: GET /v3/box/busy (the check an update drain and a machine
// that powers itself off rely on) does not count a worker run waiting for
// its provider's limit to reset (status waiting, retry_at days ahead) as
// busy: it holds no capacity and no agent turn and reruns itself at the
// reset, also after a restart. It is shown as limit_waiting, still
// unfinished, and an if_idle drain proceeds instead of waiting days. A run
// actually running still makes the machine busy and refuses the drain.
// Threat: a drain (and power-off) waiting days on a delivery parked on a
// weekly provider limit; or a running run hidden by the change.
// Owners: Server.boxBusyFrom, WorkerStore.CountWorkerRunsByWait and
// BoxExecutionCounts, Server.handleBoxDrain (if_idle) over the real worker
// store and capacity manager.
// Layer: the HTTP handlers the installer calls.
func TestBoxBusyAndDrainDoNotWaitOnALimitWaitingWorkerRun(t *testing.T) {
	server, _, permissionSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	account := testPrincipal().AccountScopeID
	ws := server.sessions.Store().WorkerStore()
	reviewer, err := ws.CreateWorker(account, "user", pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleReviewer, Name: "Reviewer", IdempotencyKey: "reviewer"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	retryAt := time.Now().Add(72 * time.Hour).UnixMilli()
	waiting, err := ws.RecordWorkerRun(account, pebblestore.WorkerRunRecord{ID: "run-limit", WorkerID: reviewer.ID, UserID: "user", Role: pebblestore.WorkerRoleReviewer,
		Status: pebblestore.WorkerRunStatusWaiting, RetryAt: retryAt, QueueReason: "waiting for the provider's limit to reset", RequestSource: "direct", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	busy := getBoxBusy(t, server)
	if busy.Busy || busy.WorkerRuns != 0 || busy.Reviews != 0 || busy.LimitWaiting != 1 || !busy.RestartResumable {
		t.Fatalf("busy with only a limit-waiting run = %+v, want not busy, limit_waiting 1", busy)
	}
	if w := postBoxDrain(t, server, `{"max_seconds":60,"if_idle":true}`); w.Code != http.StatusOK || !permissionSvc.ExecutionCapacity().DrainStatus().Draining {
		t.Fatalf("if_idle drain with only a limit-waiting run: %d %s", w.Code, w.Body.String())
	}
	permissionSvc.ExecutionCapacity().CancelDrain("")
	// The waiting run is not lost: it is still unfinished, waiting.
	if run, found, err := ws.GetWorkerRun(account, reviewer.ID, waiting.ID); err != nil || !found || run.Status != pebblestore.WorkerRunStatusWaiting || run.RetryAt != retryAt {
		t.Fatalf("waiting run after the drain = %+v found=%v err=%v", run, found, err)
	}

	// The reset came and the run runs again: busy, and the drain refused.
	waiting.Status, waiting.RetryAt, waiting.QueueReason = "running", 0, ""
	if _, err := ws.RecordWorkerRun(account, waiting); err != nil {
		t.Fatal(err)
	}
	if busy := getBoxBusy(t, server); !busy.Busy || busy.WorkerRuns != 1 || busy.Reviews != 1 || busy.LimitWaiting != 0 {
		t.Fatalf("busy with a running run = %+v", busy)
	}
	if w := postBoxDrain(t, server, `{"max_seconds":60,"if_idle":true}`); w.Code != http.StatusConflict {
		t.Fatalf("if_idle drain with a running run: %d %s", w.Code, w.Body.String())
	}
}
