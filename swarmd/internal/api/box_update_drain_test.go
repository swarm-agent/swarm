package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func postBoxDrain(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v3/box/drain", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleBoxDrain(w, withTestPrincipal(req))
	return w
}

// Purpose: an automatic update that waited past its bound on a busy machine
// drains, and the person's watcher hears about that wait once, not once per
// timer attempt. The installer names the wait by when it began
// (waiting_since) on every drain request of that wait.
// Threat: a signal per 5-minute attempt flooding the feed, a forged future or
// negative waiting_since being accepted, or a rejected request still fencing
// admission (a partial drain).
// Boundary: Server.handleBoxDrain (POST /v3/box/drain) -> SignalStore.AppendOnce.
// Narrowest layer: the handler over the real capacity manager and signal store.
func TestBoxDrainStarvedUpdateSignalsOnce(t *testing.T) {
	server, _, permissionSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "signals.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server.SetSignalStore(pebblestore.NewSignalStore(db))
	manager := permissionSvc.ExecutionCapacity()
	starved := func() []pebblestore.Signal {
		t.Helper()
		page, err := server.signals.ListAfter(0, 100, pebblestore.SignalFilter{})
		if err != nil {
			t.Fatal(err)
		}
		var out []pebblestore.Signal
		for _, sig := range page.Signals {
			if sig.Kind == "update.starved" {
				out = append(out, sig)
			}
		}
		return out
	}

	for _, bad := range []string{`{"waiting_since":-1}`, `{"waiting_since":` + jsonInt(time.Now().Add(time.Hour).Unix()) + `}`} {
		if w := postBoxDrain(t, server, bad); w.Code != http.StatusBadRequest {
			t.Fatalf("%s accepted: %d %s", bad, w.Code, w.Body.String())
		}
		if manager.DrainStatus().Draining || len(starved()) != 0 {
			t.Fatalf("rejected %s left a drain or a signal: %+v", bad, manager.DrainStatus())
		}
	}

	// An ordinary (person's) drain raises no starvation signal.
	if w := postBoxDrain(t, server, `{"max_seconds":60}`); w.Code != http.StatusOK {
		t.Fatalf("drain: %d %s", w.Code, w.Body.String())
	}
	if got := starved(); len(got) != 0 {
		t.Fatalf("a drain without waiting_since raised %+v", got)
	}
	manager.CancelDrain("")

	since := time.Now().Add(-45 * time.Minute).Unix()
	body := `{"max_seconds":60,"waiting_since":` + jsonInt(since) + `}`
	for attempt := 0; attempt < 3; attempt++ {
		if w := postBoxDrain(t, server, body); w.Code != http.StatusOK {
			t.Fatalf("starved drain %d: %d %s", attempt, w.Code, w.Body.String())
		}
		if !manager.DrainStatus().Draining {
			t.Fatal("starved drain did not fence admission")
		}
		// Each timer attempt after a drain ends asks again for the same wait.
		manager.CancelDrain("")
	}
	got := starved()
	if len(got) != 1 {
		t.Fatalf("update.starved signals = %d, want exactly 1 for one wait: %+v", len(got), got)
	}
	if got[0].Severity != pebblestore.SignalSeverityWarning || got[0].Attrs["waiting_since"] != time.Unix(since, 0).UTC().Format(time.RFC3339) {
		t.Fatalf("signal = %+v", got[0])
	}

	// A later wait (a new waiting_since) is a new episode: one more signal.
	if w := postBoxDrain(t, server, `{"max_seconds":60,"waiting_since":`+jsonInt(since+600)+`}`); w.Code != http.StatusOK {
		t.Fatalf("second wait: %d %s", w.Code, w.Body.String())
	}
	manager.CancelDrain("")
	if got := starved(); len(got) != 2 {
		t.Fatalf("a second wait raised %d signals in total, want 2", len(got))
	}
}

// Purpose: a drain for an update holds new runs instead of refusing them, and
// the held runs run after the restart. This is what lets a starved automatic
// update drain a machine that is never idle without losing work.
// Threat: a run started during the drain being failed, interrupted or lost
// across the restart, or slipping past the admission fence and being cut off
// by the restart.
// Boundary: POST /v3/box/drain (Manager.BeginDrain) -> sessionV3Executor.run
// (AdmitExecution keeps the intent pending_executor) -> restart:
// newSessionV3Executor (recoverDurableRuns + refillPendingBacklog).
// Narrowest layer: two daemons opened in turn over one store with a scripted
// provider; the capacity manager alone cannot show the durable intent replay.
func TestUpdateDrainHeldRunRunsAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drain-restart.pebble")
	type daemon struct {
		server   *Server
		sessions *sessionruntime.Service
		runner   *sessionsV3RecordingProviderRunner
		close    func()
	}
	open := func(recovery bool) daemon {
		t.Helper()
		server, sessions, permissions, closeStore, _ := newRoutedSessionTestServerAt(t, path, recovery)
		// The swarm agent resolves through the account's agent model settings
		// (testAgentModelSettingsRecord: provider codex).
		runner := &sessionsV3RecordingProviderRunner{id: "codex", text: "done after the update"}
		providers := registry.New()
		providers.RegisterRunner(runner)
		server.providers = providers
		server.runner = runruntime.NewService(sessions, server.model, providers, tool.NewRuntime(1), permissions, server.agents, nil, nil)
		exec := newSessionV3Executor(server)
		exec.startDelay = 0
		server.v3SessionExecutor = exec
		return daemon{server: server, sessions: sessions, runner: runner, close: closeStore.(func())}
	}
	calls := func(d daemon) int {
		d.runner.mu.Lock()
		defer d.runner.mu.Unlock()
		return d.runner.callCount
	}

	before := open(false)
	session := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, before.server, "held-create", "held", t.TempDir(), pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	if w := postBoxDrain(t, before.server, `{"max_seconds":3600}`); w.Code != http.StatusOK {
		t.Fatalf("drain: %d %s", w.Code, w.Body.String())
	}
	raw, _ := json.Marshal(map[string]any{"client_request_id": "held-message", "role": "user", "content": "hello"})
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+session.ID+"/messages", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	before.server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("a message during the drain was refused: %d %s", rec.Code, rec.Body.String())
	}
	runID := stableSessionsV3PrimaryRunID(session.ID, "held-message")
	account := testPrincipal().AccountScopeID
	manager := before.server.ExecutionCapacity()
	deadline := time.Now().Add(5 * time.Second)
	for manager.Pending(account) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the new run never queued behind the drain")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if intent, ok, err := before.sessions.GetSessionRunIntent(session.ID, runID); err != nil || !ok || intent.Status != sessionruntime.RunIntentPendingExecutor {
		t.Fatalf("held run is not pending: %+v ok=%v err=%v", intent, ok, err)
	}
	if n := calls(before); n != 0 {
		t.Fatalf("held run reached the model %d times during the drain", n)
	}
	if status := manager.DrainStatus(); !status.Ready || len(status.Blockers) != 0 {
		t.Fatalf("a held run blocks the drain: %+v", status)
	}

	// The installer restarts Swarm into the new build.
	before.close()
	after := open(true)
	deadline = time.Now().Add(10 * time.Second)
	for {
		intent, ok, err := after.sessions.GetSessionRunIntent(session.ID, runID)
		if err == nil && ok && intent.Status == sessionruntime.RunIntentCompleted {
			break
		}
		if err == nil && ok && (intent.Status == sessionruntime.RunIntentInterrupted || intent.Status == sessionruntime.RunIntentFailed || intent.Status == sessionruntime.RunIntentCancelled) {
			t.Fatalf("held run ended %s after the restart: %+v", intent.Status, intent)
		}
		if time.Now().After(deadline) {
			t.Fatalf("held run never ran after the restart: %+v ok=%v err=%v", intent, ok, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := calls(after); n < 1 {
		t.Fatalf("held run completed without reaching the model after the restart (%d calls)", n)
	}
}
