package pebblestore

import (
	"testing"

	"github.com/cockroachdb/pebble"
)

// Purpose: conversion permission writes must be atomic, exact-record CAS guarded,
// and cancellation-only. The batch helper is the narrowest real-store layer that
// proves stale rejection leaves pending indexes and run waits unchanged.
func TestAutomationPermissionBatchExactCancellation(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	permissions := NewPermissionStore(db)
	previous := PermissionRecord{ID: "review", SessionID: "session", RunID: "run", Status: PermissionStatusPending, CreatedAt: 1, UpdatedAt: 1}
	unrelated := PermissionRecord{ID: "other", SessionID: "session", RunID: "run", Status: PermissionStatusPending, CreatedAt: 2, UpdatedAt: 2}
	for _, record := range []PermissionRecord{previous, unrelated} {
		if err := permissions.PutPermission(record, nil); err != nil {
			t.Fatal(err)
		}
	}
	previous, _, err = permissions.GetPermission("session", "review")
	if err != nil {
		t.Fatal(err)
	}
	if err := permissions.UpsertRunWait(RunWaitState{SessionID: "session", RunID: "run", PendingPermissionIDs: []string{"review", "other"}}); err != nil {
		t.Fatal(err)
	}
	updated := previous
	updated.Status, updated.ExecutionStatus = PermissionStatusCancelled, PermissionExecCancelled
	in := V3SessionMutationInput{SessionID: "session", AccountScopeID: "account", UserID: "owner", AutomationProposal: &AutomationPlanReference{}, automationAcceptance: &AutomationApproval{}, AutomationPermission: &AutomationPermissionResolution{Previous: previous, Record: updated, Summary: PermissionSummary{SessionID: "session", AccountScopeID: "account", PrincipalID: "owner", PendingCount: 1}}}
	sessions := NewSessionStore(db)
	in.AutomationPermission.Previous.UpdatedAt++
	batch := db.NewBatch()
	if err := sessions.setAutomationPermissionInBatch(batch, in); err == nil {
		t.Fatal("stale permission accepted")
	}
	batch.Close()
	pending, err := permissions.ListPendingPermissions("session", 10)
	if err != nil || len(pending) != 2 {
		t.Fatalf("rejection mutated pending: %v %v", pending, err)
	}
	in.AutomationPermission.Previous = previous
	batch = db.NewBatch()
	defer batch.Close()
	if err := sessions.setAutomationPermissionInBatch(batch, in); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		t.Fatal(err)
	}
	pending, err = permissions.ListPendingPermissions("session", 10)
	if err != nil || len(pending) != 1 || pending[0].ID != "other" {
		t.Fatalf("wrong permission consumed: %v %v", pending, err)
	}
	got, _, err := permissions.GetPermission("session", "review")
	if err != nil || got.Status != PermissionStatusCancelled || got.ExecutionStatus != PermissionExecCancelled {
		t.Fatalf("review could execute: %+v %v", got, err)
	}
	wait, found, err := permissions.GetRunWait("session", "run")
	if err != nil || !found || len(wait.PendingPermissionIDs) != 1 || wait.PendingPermissionIDs[0] != "other" {
		t.Fatalf("wrong wait detached: %+v %v", wait, err)
	}
}
