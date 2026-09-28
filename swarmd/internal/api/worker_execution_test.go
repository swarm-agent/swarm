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
