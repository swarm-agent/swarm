package pebblestore

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSessionStorageMaintenanceDryRunReconcilesWithApply(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	const sessionID = "session-maintenance-report"
	createV3SessionForStoreTest(t, sessions, sessionID, "user-1", "account-1")
	for i := 0; i < 4; i++ {
		appendV3SessionMessageForStoreTest(t, sessions, sessionID, "maintenance-message-"+string(rune('a'+i)), "sensitive-payload-must-not-leak", "user-1", "account-1")
	}
	policy := V3SessionRetentionPolicy{RealtimeReplayRetention: time.Hour, CompletedIdempotencyRetention: time.Hour, RealtimeMinimumRecords: 1, BatchRecords: 2}
	now := time.UnixMilli(10_000_000)

	beforeState, beforeStateOK, err := sessions.GetV3SessionMaintenanceState()
	if err != nil {
		t.Fatal(err)
	}
	preview, err := sessions.RunSessionStorageMaintenance(context.Background(), SessionStorageMaintenanceRequest{Now: now, RetentionPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	afterState, afterStateOK, err := sessions.GetV3SessionMaintenanceState()
	if err != nil {
		t.Fatal(err)
	}
	if beforeStateOK != afterStateOK || !reflect.DeepEqual(beforeState, afterState) {
		t.Fatalf("dry-run wrote maintenance state: before=%+v/%t after=%+v/%t", beforeState, beforeStateOK, afterState, afterStateOK)
	}
	if preview.Mode != "dry_run" || preview.After.Namespaces.TotalLogicalBytes != preview.Before.Namespaces.TotalLogicalBytes || preview.PhysicalCompaction.Performed || !preview.PhysicalCompaction.RequiresExplicitOperatorAction {
		t.Fatalf("dry-run report=%+v", preview)
	}
	applied, err := sessions.RunSessionStorageMaintenance(context.Background(), SessionStorageMaintenanceRequest{Apply: true, Now: now, RetentionPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if applied.AppliedCleanup == nil || !reflect.DeepEqual(preview.CandidateCleanup, *applied.AppliedCleanup) {
		t.Fatalf("preview=%+v applied=%+v", preview.CandidateCleanup, applied.AppliedCleanup)
	}
	if applied.After.Namespaces.TotalLogicalBytes >= applied.Before.Namespaces.TotalLogicalBytes {
		t.Fatalf("apply did not reduce logical bytes: before=%d after=%d", applied.Before.Namespaces.TotalLogicalBytes, applied.After.Namespaces.TotalLogicalBytes)
	}
	encoded, err := json.Marshal(applied)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{sessionID, "user-1", "account-1", "sensitive-payload-must-not-leak"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("maintenance report leaked %q: %s", forbidden, text)
		}
	}
}

func TestSessionStorageMaintenanceHonorsCancellationWithoutWrites(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := sessions.RunSessionStorageMaintenance(ctx, SessionStorageMaintenanceRequest{})
	if err == nil {
		t.Fatal("cancelled maintenance unexpectedly succeeded")
	}
	if _, ok, stateErr := sessions.GetV3SessionMaintenanceState(); stateErr != nil || ok {
		t.Fatalf("cancelled maintenance state ok=%t err=%v", ok, stateErr)
	}
}
