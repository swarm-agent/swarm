package api

import (
	"fmt"
	"net/http"
	"reflect"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: lifecycle and dispatch HTTP handlers must use the daemon's shared
// execution authority. Threat: an unavailable executor falls back to raw writes
// and reports activation, cancellation or admission without real execution.
// The real HTTP mux and temporary Pebble prove failure leaves records unchanged.
func TestWorkerAPIExecutionUnavailableDoesNotMutate(t *testing.T) {
	s, _, h := setupWorkerAPITestServer(t)
	w, err := s.sessions.Store().WorkerStore().CreateWorker("acct-test", "user-test", store.CreateWorkerRequest{Name: "unavailable"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, body string }{
		{"activate", `{"expected_revision":1,"local_bindings":{"primary":"unapproved"}}`},
		{"pause", `{"expected_revision":1}`},
		{"resume", `{"expected_revision":1}`},
		{"archive", `{"expected_revision":1}`},
		{"delete", `{"expected_revision":1}`},
		{"request", `{"prompt":"run","idempotency_key":"one"}`},
		{"test", `{"prompt":"test","idempotency_key":"two"}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			before, _, err := s.sessions.GetWorker("acct-test", w.ID)
			if err != nil {
				t.Fatal(err)
			}
			response := executeWorkerAPI(h, http.MethodPost, fmt.Sprintf("/%s/%s", w.ID, tc.path), tc.body, workerAPICallOptions{})
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503, got %d: %s", response.Code, response.Body.String())
			}
			after, _, err := s.sessions.GetWorker("acct-test", w.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("unavailable executor mutated worker: %v", err)
			}
			runs, _, err := s.sessions.ListWorkerRuns("acct-test", w.ID, 10, "")
			if err != nil || len(runs) != 0 {
				t.Fatalf("fabricated execution: %+v %v", runs, err)
			}
		})
	}
}

// Requirement: authenticated approval of bindings for an idle test must not
// enable schedules or ordinary dispatch. The HTTP/shared-service boundary is
// narrower than a provider test and verifies persisted state, not just status.
func TestWorkerAPIIdleTestApproval(t *testing.T) {
	s, db, h := setupWorkerAPITestServer(t)
	workspaceID := setupWorkerAPIExecution(t, s, db)
	ws := store.NewWorkerStore(db)
	w, err := ws.CreateWorker("acct-test", "user-test", store.CreateWorkerRequest{Name: "idle", WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	opts := workerAPICallOptions{scopes: []string{"automations:write"}}
	result := executeWorkerAPI(h, http.MethodPost, "/"+w.ID+"/activate", fmt.Sprintf(`{"expected_revision":1,"local_bindings":{"primary":%q},"activate":false}`, workspaceID), opts)
	if result.Code != http.StatusOK {
		t.Fatalf("approval: %d %s", result.Code, result.Body.String())
	}
	result = executeWorkerAPI(h, http.MethodPost, "/"+w.ID+"/test", `{"prompt":"Review","idempotency_key":"idle-test"}`, opts)
	if result.Code != http.StatusCreated {
		t.Fatalf("test: %d %s", result.Code, result.Body.String())
	}
	current, _, err := ws.GetWorker("acct-test", w.ID)
	if err != nil || current.LifecycleState != store.WorkerLifecycleStateIdle || current.Revision != 2 {
		t.Fatalf("test activated worker: %+v %v", current, err)
	}
	result = executeWorkerAPI(h, http.MethodPost, "/"+w.ID+"/direct", `{"prompt":"Review","idempotency_key":"idle-direct"}`, opts)
	if result.Code != http.StatusConflict {
		t.Fatalf("idle direct accepted: %d %s", result.Code, result.Body.String())
	}
	receipts, _, err := ws.ListWorkerRuns("acct-test", w.ID, 10, "")
	if err != nil || len(receipts) != 1 || receipts[0].RequestSource != "test_run" {
		t.Fatalf("unauthorized admission: %+v %v", receipts, err)
	}
}
