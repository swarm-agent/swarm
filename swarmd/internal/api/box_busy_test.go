package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/executioncapacity"
)

// Purpose: GET /v3/box/busy is the installer's "may Swarm restart now?" check;
// an automatic update drains only when it answers busy:false. The drain
// (executioncapacity.Manager.drainStatusLocked) waits on every run holding an
// execution lease, and project task runs and their follow-up runs hold leases
// without an active session lifecycle record. Threat: busy:false while a
// lease is held, so the automatic update drains a busy machine and holds the
// next steps running work needs. The handler (Server.handleBoxBusy ->
// boxBusy) over the real permission service's capacity manager is the
// narrowest layer joining both authorities.
// restart_resumable says whether a restart now would lose nothing: an agent
// run holding a lease is interrupted by a restart (recoverDurableRuns), so it
// must say false while one is held, and true once nothing would be lost.
func TestBoxBusyCountsHeldExecutionLeases(t *testing.T) {
	server, _, permissionSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	get := func() boxBusy {
		t.Helper()
		w := httptest.NewRecorder()
		server.handleBoxBusy(w, withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/box/busy", nil)))
		if w.Code != http.StatusOK {
			t.Fatalf("busy status %d: %s", w.Code, w.Body.String())
		}
		var out boxBusy
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode busy: %v", err)
		}
		return out
	}
	if idle := get(); idle.Busy || idle.Sessions != 0 || !idle.RestartResumable {
		t.Fatalf("nothing held but busy or not restart-safe: %+v", idle)
	}

	manager := permissionSvc.ExecutionCapacity()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lease, err := manager.Acquire(ctx, executioncapacity.AcquireRequest{AccountScopeID: testPrincipal().AccountScopeID, SessionID: "task-session", RunID: "desktop-v3-run:task-task_1"})
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if blockers := manager.DrainStatus().Blockers; len(blockers) != 1 {
		t.Fatalf("drain blockers = %+v, want the held run", blockers)
	}
	if busy := get(); !busy.Busy || busy.Sessions != 1 || busy.RestartResumable {
		t.Fatalf("held lease without lifecycle reported idle or restart-safe: %+v", busy)
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("release lease: %v", err)
	}
	if idle := get(); idle.Busy || idle.Sessions != 0 || !idle.RestartResumable {
		t.Fatalf("released lease still busy: %+v", idle)
	}
}

func getBoxBusy(t *testing.T, server *Server) boxBusy {
	t.Helper()
	w := httptest.NewRecorder()
	server.handleBoxBusy(w, withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/box/busy", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("busy status %d: %s", w.Code, w.Body.String())
	}
	var out boxBusy
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode busy: %v", err)
	}
	return out
}

func waitQueued(t *testing.T, manager *executioncapacity.Manager, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(manager.QueuedSessions()) != n {
		if time.Now().After(deadline) {
			t.Fatalf("queued sessions = %v, want %d", manager.QueuedSessions(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Purpose: an automatic update never starts an admission drain while GET
// /v3/box/busy would say busy (seen on a forge: a drain on a busy machine held
// the review steps running work needed for 30 minutes). Requirements: (1)
// /v3/box/busy counts runs waiting for admission (queued for capacity or
// behind a drain), which a drain holds, as busy; (2) POST /v3/box/drain with
// if_idle refuses (409, with the counts) and leaves admission open while
// anything runs or waits, checking under the admission lock
// (Manager.BeginDrainIf) so a run admitted after the installer's own busy
// check cannot slip in; (3) once idle it drains, and a drain already in
// progress is joined, not refused. Threat: busy:false with a queued run, or an
// if_idle request fencing a busy machine (a partial drain).
// Boundary: Server.handleBoxBusy / Server.handleBoxDrain over the real
// permission service's executioncapacity.Manager: the narrowest layer joining
// the busy count and the admission fence.
func TestBoxDrainIfIdleNeverFencesBusyMachine(t *testing.T) {
	server, _, permissionSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	manager := permissionSvc.ExecutionCapacity()
	account := testPrincipal().AccountScopeID
	manager.SetLimit(account, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ifIdle := `{"max_seconds":60,"if_idle":true}`

	running, err := manager.Acquire(ctx, executioncapacity.AcquireRequest{AccountScopeID: account, SessionID: "task-session", RunID: "desktop-v3-run:task-task_1"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// A review run waits for capacity behind the task run.
	waitCtx, stopWaiting := context.WithCancel(ctx)
	waited := make(chan error, 1)
	go func() {
		lease, err := manager.Acquire(waitCtx, executioncapacity.AcquireRequest{AccountScopeID: account, SessionID: "review-session", RunID: "run:review"})
		if err == nil {
			_ = lease.Release()
		}
		waited <- err
	}()
	waitQueued(t, manager, 1)
	if busy := getBoxBusy(t, server); !busy.Busy || busy.Sessions != 1 || busy.Queued != 1 {
		t.Fatalf("busy with a queued run = %+v, want sessions 1 queued 1", busy)
	}

	w := postBoxDrain(t, server, ifIdle)
	if w.Code != http.StatusConflict {
		t.Fatalf("if_idle drain on a busy machine: %d %s", w.Code, w.Body.String())
	}
	var refused struct {
		Busy boxBusy `json:"busy"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &refused); err != nil || !refused.Busy.Busy || refused.Busy.Sessions != 1 || refused.Busy.Queued != 1 {
		t.Fatalf("refusal body = %s (%v)", w.Body.String(), err)
	}
	if status := manager.DrainStatus(); status.Draining {
		t.Fatalf("a refused if_idle drain fenced admission: %+v", status)
	}

	// The waiting review gives up and the task run finishes: idle.
	stopWaiting()
	if err := <-waited; err == nil {
		t.Fatal("cancelled waiter was admitted")
	}
	if err := running.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if busy := getBoxBusy(t, server); busy.Busy {
		t.Fatalf("idle machine reported busy: %+v", busy)
	}

	// Idle: the if_idle drain starts. A run asked for now is held, counted as
	// queued (busy, but it resumes after a restart), and a second if_idle
	// request joins the drain in progress instead of being refused.
	if w := postBoxDrain(t, server, ifIdle); w.Code != http.StatusOK || !manager.DrainStatus().Draining {
		t.Fatalf("if_idle drain on an idle machine: %d %s", w.Code, w.Body.String())
	}
	held := make(chan error, 1)
	go func() {
		lease, err := manager.Acquire(ctx, executioncapacity.AcquireRequest{AccountScopeID: account, SessionID: "followup-session", RunID: "run:followup"})
		if err == nil {
			_ = lease.Release()
		}
		held <- err
	}()
	waitQueued(t, manager, 1)
	if busy := getBoxBusy(t, server); !busy.Busy || busy.Sessions != 0 || busy.Queued != 1 || !busy.RestartResumable {
		t.Fatalf("busy with a run held by the drain = %+v", busy)
	}
	if w := postBoxDrain(t, server, ifIdle); w.Code != http.StatusOK {
		t.Fatalf("if_idle request during a drain: %d %s", w.Code, w.Body.String())
	}
	manager.CancelDrain("")
	if err := <-held; err != nil {
		t.Fatalf("held run after the drain ended: %v", err)
	}
}
