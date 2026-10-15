package pebblestore

import "testing"

// Requirement: RecordWorkerRun only notifies the signal observer when cleanup
// crosses the failure threshold or recovers, never on each retry. The transition
// predicate is the narrowest layer proving duplicate suppression.
func TestCleanupSignalTransitions(t *testing.T) {
	old := &WorkerRunCleanup{State: "pending"}
	signals := 0
	for i := 1; i <= 7; i++ {
		next := &WorkerRunCleanup{State: "failed", Attempts: i, FailureReported: i >= 5}
		if cleanupSignalChanged(old, next) {
			signals++
		}
		old = next
	}
	if signals != 1 {
		t.Fatal(signals)
	}
	next := &WorkerRunCleanup{State: "done", Attempts: 8, FailureReported: true}
	if !cleanupSignalChanged(old, next) || cleanupSignalChanged(next, next) {
		t.Fatal("recovery transition incorrect")
	}
}
