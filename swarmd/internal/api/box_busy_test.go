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
// restart_resumable is what a starved automatic update checks at its drain
// cap before restarting with runs still going: an agent run holding a lease
// is interrupted by a restart (recoverDurableRuns), so it must say false while
// one is held, and true once nothing would be lost.
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
