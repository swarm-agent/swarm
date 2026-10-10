package runtime

import (
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/sandbox"
	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the startup and worker-run signals are what tell a remote monitor
// that a machine restarted, whether its agents are confined, and that a
// worker failed. An inactive sandbox must be a warning carrying its reason;
// a failed run must be a warning naming the worker and run but never the
// run's error text or input. Owner: emitStartupSignals and workerRunSignal,
// the functions the daemon wires at startup.
func TestStartupAndWorkerRunSignals(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	feed := pebblestore.NewSignalStore(store)
	emitter := signals.NewEmitter(feed)

	emitStartupSignals(emitter, sandbox.Status{Mode: sandbox.ModeOff, Reason: "container engine is not reachable"})
	workerRunSignal(emitter)(
		pebblestore.WorkerRecord{ID: "wrk_1", Name: "Poster"},
		pebblestore.WorkerRunRecord{ID: "wrun_1", AccountScopeID: "acct", Status: "failed", RequestSource: "schedule", Error: "provider said: private detail", SessionID: "s1"},
	)

	page, err := feed.ListAfter(0, 10, pebblestore.SignalFilter{Account: "acct"})
	if err != nil || len(page.Signals) != 3 {
		t.Fatalf("signals = %+v, %v", page, err)
	}
	started, inactive, failed := page.Signals[0], page.Signals[1], page.Signals[2]
	if started.Kind != "daemon.started" || started.Account != "" {
		t.Fatalf("started = %+v", started)
	}
	if inactive.Kind != "sandbox.inactive" || inactive.Severity != pebblestore.SignalSeverityWarning || inactive.Attrs["reason"] != "container engine is not reachable" || inactive.Account != "" {
		t.Fatalf("inactive = %+v", inactive)
	}
	if failed.Kind != "worker.run.failed" || failed.Severity != pebblestore.SignalSeverityWarning || failed.Refs["worker_id"] != "wrk_1" || failed.Refs["run_id"] != "wrun_1" || !strings.Contains(failed.Summary, "Poster") {
		t.Fatalf("failed = %+v", failed)
	}
	blob := failed.Summary
	for _, v := range failed.Attrs {
		blob += v
	}
	if strings.Contains(blob, "private detail") {
		t.Fatalf("worker signal carries the run error: %+v", failed)
	}
}
