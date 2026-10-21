package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: /readyz must say "not ready" whenever the daemon cannot take work,
// not only during shutdown: no running session executor, or a durable store that
// no longer answers a read. Container health checks and external monitors rely
// on it to restart or alert. Execution readiness adds only fixed mode/reason
// words and a boolean; private engine errors must never reach this public route.
// Threat: a daemon whose store has failed (or whose executor stopped) reporting
// ready forever, so nothing restarts it while every chat run fails.
// Boundary: Server.handleReady, readinessProblem, sessionStoreAnswers.
// Narrowest layer: the handler on a minimal Server over a real pebble store the
// test can close.
func TestReadyzReportsStoreAndExecutorFailures(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "readyz.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = store.Close()
		}
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Server{
		sessions:          sessionruntime.NewService(pebblestore.NewSessionStore(store), events),
		v3SessionExecutor: &sessionV3Executor{ctx: ctx},
	}
	ready := func() (int, map[string]any) {
		w := httptest.NewRecorder()
		s.handleReady(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		for key, value := range body {
			switch key {
			case "ok", "agent_execution_ready":
				if _, ok := value.(bool); !ok {
					t.Fatalf("non-boolean readiness field %q", key)
				}
			case "reason":
				if value != "shutting_down" && value != "executor_unavailable" && value != "store_unavailable" {
					t.Fatalf("private or unknown readiness reason: %v", value)
				}
			case "agent_execution_mode":
				if value != "off" && value != "required" && value != "auto" {
					t.Fatalf("unknown execution mode: %v", value)
				}
			case "agent_execution_reason":
				if value != "" && value != "sandbox_unavailable" {
					t.Fatalf("private execution reason: %v", value)
				}
			default:
				t.Fatalf("unexpected public readiness field %q", key)
			}
		}
		if w.Code == http.StatusOK && len(body) != 4 {
			t.Fatal("execution readiness fields absent")
		}
		if w.Code != http.StatusOK && len(body) != 2 {
			t.Fatal("failed control readiness must retain only ok/reason")
		}
		return w.Code, body
	}

	if code, body := ready(); code != http.StatusOK || body["ok"] != true {
		t.Fatalf("healthy daemon: %d %v", code, body)
	}

	s.shuttingDown.Store(true)
	if code, body := ready(); code != http.StatusServiceUnavailable || body["reason"] != "shutting_down" {
		t.Fatalf("shutting down: %d %v", code, body)
	}
	s.shuttingDown.Store(false)

	executor := s.v3SessionExecutor
	s.v3SessionExecutor = nil
	if code, body := ready(); code != http.StatusServiceUnavailable || body["reason"] != "executor_unavailable" {
		t.Fatalf("no executor: %d %v", code, body)
	}
	s.v3SessionExecutor = executor
	cancel()
	if code, body := ready(); code != http.StatusServiceUnavailable || body["reason"] != "executor_unavailable" {
		t.Fatalf("stopped executor: %d %v", code, body)
	}
	s.v3SessionExecutor = &sessionV3Executor{ctx: context.Background()}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	if code, body := ready(); code != http.StatusServiceUnavailable || body["reason"] != "store_unavailable" {
		t.Fatalf("closed store: %d %v", code, body)
	}
}
