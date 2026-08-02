package pebblestore

import (
	"path/filepath"
	"testing"
)

func TestExecutionEpochRecoveryIsOneAttemptPerEpoch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.pebble")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionStore(store)

	first, claimed, err := sessions.ClaimExecutionEpochRecovery("session-1", "epoch-1", "owner-1", 10)
	if err != nil || !claimed || first.Status != ExecutionEpochRecoveryStatusInProgress || first.Phase != "detected" || first.Attempts != 1 {
		t.Fatalf("first claim = %+v claimed=%t err=%v", first, claimed, err)
	}
	phase, err := sessions.UpdateExecutionEpochRecoveryPhase("session-1", "epoch-1", "owner-1", "compacting", "stale_high_context", 20)
	if err != nil || phase.Phase != "compacting" || phase.Reason != "stale_high_context" {
		t.Fatalf("phase = %+v err=%v", phase, err)
	}
	if _, claimed, err := sessions.ClaimExecutionEpochRecovery("session-1", "epoch-1", "owner-2", 30); err != nil || claimed {
		t.Fatalf("duplicate claim claimed=%t err=%v", claimed, err)
	}
	failed, err := sessions.FinishExecutionEpochRecovery("session-1", "epoch-1", "owner-1", ExecutionEpochRecoveryStatusFailed, "compact failed", 40)
	if err != nil || failed.OwnerRunID != "owner-1" || failed.Outcome != "compact failed" {
		t.Fatalf("failed recovery = %+v err=%v", failed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := NewSessionStore(reopened)
	if record, claimed, err := restarted.ClaimExecutionEpochRecovery("session-1", "epoch-1", "owner-after-restart", 50); err != nil || claimed || record.Status != ExecutionEpochRecoveryStatusFailed || record.Attempts != 1 {
		t.Fatalf("restart claim = %+v claimed=%t err=%v", record, claimed, err)
	}
}

func TestExecutionEpochRecoveryCompletedCannotBeReclaimed(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "completed.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := NewSessionStore(store)
	if _, claimed, err := sessions.ClaimExecutionEpochRecovery("session-2", "epoch-2", "owner", 10); err != nil || !claimed {
		t.Fatalf("claim claimed=%t err=%v", claimed, err)
	}
	completed, err := sessions.FinishExecutionEpochRecovery("session-2", "epoch-2", "owner", ExecutionEpochRecoveryStatusCompleted, "resumed", 20)
	if err != nil || completed.Status != ExecutionEpochRecoveryStatusCompleted {
		t.Fatalf("completed = %+v err=%v", completed, err)
	}
	if _, claimed, err := sessions.ClaimExecutionEpochRecovery("session-2", "epoch-2", "other", 30); err != nil || claimed {
		t.Fatalf("completed epoch reclaimed=%t err=%v", claimed, err)
	}
}
