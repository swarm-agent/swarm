package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
	"time"
)

// Purpose: a drain must leave accepted V3 work durable and pending during
// executor shutdown. This uses the real store and capacity manager at the
// executor boundary, not a status-only assertion or a simulated worker.
func TestBoxDrainRetainsDurablePending(t *testing.T) {
	for _, worker := range []bool{false, true} {
		t.Run(fmt.Sprintf("worker=%v", worker), func(t *testing.T) { testBoxDrainRecovery(t, worker) })
	}
}
func testBoxDrainRecovery(t *testing.T, worker bool) {
	path := filepath.Join(t.TempDir(), "recovery.pebble")
	server, sessions, perms, closeFirst, _ := newRoutedSessionTestServerAt(t, path, true)
	if _, err := perms.ExecutionCapacity().BeginDrain(time.Hour); err != nil {
		t.Fatal(err)
	}
	session := createSessionsV3PrimaryTestSessionWithPreference(t, server, "draining", "draining", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model"})
	if worker {
		session.Metadata["worker_id"], session.Metadata["worker_run_id"] = "worker", "worker-run"
		if _, err := sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, Kind: pebblestore.V3SessionMutationUpdateMetadata, IdempotencyKey: "worker-lineage", RequestHash: "worker-lineage", Session: &session, NowUnixMs: time.Now().UnixMilli()}); err != nil {
			t.Fatal(err)
		}
		db := sessions.Store().Underlying()
		if err := db.PutJSON(pebblestore.KeyWorker(session.AccountScopeID, "worker"), pebblestore.WorkerRecord{ID: "worker", AccountScopeID: session.AccountScopeID}); err != nil {
			t.Fatal(err)
		}
		if err := db.PutJSON(pebblestore.KeyWorkerRun(session.AccountScopeID, "worker", "worker-run"), pebblestore.WorkerRunRecord{ID: "worker-run", WorkerID: "worker", AccountScopeID: session.AccountScopeID, SessionID: session.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := perms.ExecutionCapacity().BeginDrain(time.Hour); err != nil {
		t.Fatal(err)
	}
	recordTestPendingRunIntent(t, server, session.ID, "held", session.AccountScopeID, session.UserID)
	if server.v3SessionExecutor == nil {
		server.v3SessionExecutor = newSessionV3Executor(server)
	}
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
	closeFirst.(func())()
	server, sessions, _, _, _ = newRoutedSessionTestServerAt(t, path, true)
	installSessionsV3TestProvider(server, "drain recovery succeeded")
	server.providers.RegisterRunner(&sessionsV3RecordingProviderRunner{id: "codex", text: "drain recovery succeeded"})
	server.v3SessionExecutor = newSessionV3Executor(server)
	defer func() { server.CancelInFlightRuns(); server.WaitForInFlightRuns(2 * time.Second) }()
	deadline = time.After(3 * time.Second)
	for {
		recovered, found, err := sessions.GetSessionRunIntent(session.ID, "held")
		if err != nil || !found {
			t.Fatalf("recovered intent missing: %v", err)
		}
		if string(recovered.Status) == "completed" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("startup did not complete held intent: %+v", recovered)
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

// Purpose: MCP level and API admin checks must both authorize drain mutations.
// Real scoped tokens through the SDK handler prove rejected calls have no effect.
func TestControlMCPDrainAuthority(t *testing.T) {
	s, _, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "permissions"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s.perm = permission.NewService(pebblestore.NewPermissionStore(db), nil, nil)
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	issue := func(scopes []string) string {
		token, _, err := sec.CreateScopedToken("drain", scopes, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	denied := controlMCPTestCaller(t, s, issue([]string{"admin", "swarm:read"}))
	owner := controlMCPTestCaller(t, s, issue([]string{"admin", "swarm:manage"}))
	if text, failed := denied("swarm_request_update", map[string]any{}); !failed || s.ExecutionCapacity().DrainStatus().Draining {
		t.Fatalf("denied changed admission: %s", text)
	}
	if text, failed := owner("swarm_request_update", map[string]any{}); failed || !s.ExecutionCapacity().DrainStatus().Draining {
		t.Fatalf("owner failed: %s", text)
	}
	if text, failed := denied("swarm_cancel_update", map[string]any{}); !failed || !s.ExecutionCapacity().DrainStatus().Draining {
		t.Fatalf("denied cancellation: %s", text)
	}
	if text, failed := owner("swarm_cancel_update", map[string]any{}); failed || s.ExecutionCapacity().DrainStatus().Draining {
		t.Fatalf("cancel failed: %s", text)
	}
}
