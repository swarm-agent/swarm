package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"swarm/packages/swarmd/internal/executioncapacity"
	"swarm/packages/swarmd/internal/identity"
	"testing"
	"time"
)

// Purpose: a drain must leave accepted V3 work durable and pending during
// executor shutdown. This uses the real store and capacity manager at the
// executor boundary, not a status-only assertion or a simulated worker.
func TestBoxDrainRetainsDurablePending(t *testing.T) {
	server, sessions, perms, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	session := createTestSession(t, sessions, "draining", testPrincipal().AccountScopeID, nil)
	if _, err := perms.ExecutionCapacity().BeginDrain(time.Hour); err != nil {
		t.Fatal(err)
	}
	recordTestPendingRunIntent(t, server, session.ID, "held", session.AccountScopeID, session.UserID)
	exec := newSessionV3Executor(server)
	server.v3SessionExecutor = exec
	deadline := time.After(2 * time.Second)
	for perms.ExecutionCapacity().Pending(session.AccountScopeID) == 0 {
		select {
		case <-deadline:
			t.Fatal("executor did not queue")
		default:
			runtime.Gosched()
		}
	}
	intent, ok, err := sessions.GetSessionRunIntent(session.ID, "held")
	if err != nil || !ok || string(intent.Status) != "pending_executor" {
		t.Fatalf("held intent: %+v %v", intent, err)
	}
	server.CancelInFlightRuns()
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatal("executor did not stop")
	}
	after, ok, err := sessions.GetSessionRunIntent(session.ID, "held")
	if err != nil || !ok || after.Status != intent.Status || after.BlockedReason != "" {
		t.Fatalf("shutdown lost held intent: %+v %v", after, err)
	}
	// Recreate the process-owned manager and executor against the same durable
	// store, as startup does; it must recover the held intent without resubmission.
	server.shuttingDown.Store(false)
	server.runCtx, server.runCancel = context.WithCancel(context.Background())
	perms.SetExecutionCapacity(executioncapacity.NewManager(executioncapacity.ManagerConfig{}))
	server.v3SessionExecutor = newSessionV3Executor(server)
	defer func() { server.CancelInFlightRuns(); server.WaitForInFlightRuns(2 * time.Second) }()
	deadline = time.After(3 * time.Second)
	for {
		recovered, found, err := sessions.GetSessionRunIntent(session.ID, "held")
		if err != nil || !found {
			t.Fatalf("recovered intent missing: %v", err)
		}
		if recovered.Status != intent.Status {
			break
		}
		select {
		case <-deadline:
			t.Fatal("startup did not resume held intent")
		default:
			runtime.Gosched()
		}
	}
}

// Purpose: handleBoxDrain must reject unauthenticated/invalid requests without
// changing admission state; valid owner requests establish the shared fence.
// Handler tests are the narrowest layer for input and authentication boundaries.
func TestBoxDrainRequestBoundary(t *testing.T) {
	server, _, perms, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	for _, tc := range []struct {
		body string
		auth bool
		code int
	}{
		{`{}`, false, http.StatusUnauthorized},
		{`{"max_seconds":-1}`, true, http.StatusBadRequest},
		{`{"max_seconds":7200}`, true, http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodPost, "/v3/box/drain", strings.NewReader(tc.body))
		if tc.auth {
			req = req.WithContext(identity.ContextWithPrincipal(req.Context(), testPrincipal()))
		}
		out := httptest.NewRecorder()
		server.handleBoxDrain(out, req)
		if out.Code != tc.code {
			t.Fatalf("status %d: %s", out.Code, out.Body.String())
		}
		if perms.ExecutionCapacity().DrainStatus().Draining != (tc.code == http.StatusOK) {
			t.Fatal("unexpected admission mutation")
		}
	}
}
