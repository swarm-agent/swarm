package api

import (
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A worker job that stops on a declared blocker ends its run normally, so the
// feed must say job.blocked (a warning) or nothing watching it hears that the
// job did not do its work.
func TestSignalJobBlockedNamesTheJob(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "signals.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := &Server{}
	s.SetSignalStore(pebblestore.NewSignalStore(store))
	task := &pebblestore.ProjectTaskRecord{ID: "task_1", ProjectID: "proj_1", WorkerID: "worker_1", WorkerRunID: "wrun_1", WorkerName: "Models curator", LastError: "every provider failed"}
	s.signalJobBlocked("acct", task)
	page, err := s.signals.ListAfter(0, 10, pebblestore.SignalFilter{Account: "acct"})
	if err != nil || len(page.Signals) != 1 {
		t.Fatalf("signals = %+v, %v", page.Signals, err)
	}
	sig := page.Signals[0]
	if sig.Kind != "job.blocked" || sig.Severity != pebblestore.SignalSeverityWarning || sig.Attrs["job_id"] != "worker/worker_1/wrun_1" || sig.Refs["run_id"] != "wrun_1" {
		t.Fatalf("signal = %+v", sig)
	}
}
