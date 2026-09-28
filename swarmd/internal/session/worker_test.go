package session

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func setupTestSessionService(t *testing.T) (*Service, *pebblestore.Store) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "session_worker.pebble")
	store, err := pebblestore.Open(dir)
	if err != nil {
		t.Fatalf("Open pebble store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	ss := pebblestore.NewSessionStore(store)
	svc := NewService(ss, nil)
	return svc, store
}

func testExecutablePlan(title string) pebblestore.SessionPlanDocument {
	return pebblestore.SessionPlanDocument{
		Title:  title,
		Status: "pending",
		Info: pebblestore.SessionPlanInfo{
			Goal:    "Run automated checks",
			Context: "Context for automated execution",
		},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{
				ID:        "cp-1",
				Title:     "Execute checks",
				Objective: "Run verified checklist",
				Status:    "pending",
				Order:     1,
				Subtasks: []pebblestore.SessionPlanSubtask{
					{
						ID:     "sub-1",
						Title:  "Check lint",
						Status: "pending",
					},
				},
			},
		},
	}
}

// Invariant: session.Service provides canonical worker lifecycle methods with validation.
// Threat: Worker operations bypass canonical plan validation or break revision ordering.
// Boundary: session.Service CreateWorker, GetWorker, UpdateWorker, DeleteWorker.
func TestServiceWorkerLifecycle(t *testing.T) {
	svc, _ := setupTestSessionService(t)
	ctx := context.Background()

	// 1. Create worker with valid plan
	created, err := svc.CreateWorker(ctx, "acct-test", "user-1", pebblestore.CreateWorkerRequest{
		Name:         "DevOps Bot",
		Description:  "Maintains infrastructure",
		Instructions: "# DevOps Instructions\nKeep servers healthy.",
		Automations: []pebblestore.WorkerAutomationDefinition{
			{
				Name:           "Heartbeat Check",
				ActivationMode: "interval",
				Schedule: &pebblestore.AutomationV2Schedule{
					Kind:            "interval",
					IntervalSeconds: 300,
				},
				Enabled:      true,
				PlanDocument: testExecutablePlan("Heartbeat Plan"),
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}
	if created.LifecycleState != pebblestore.WorkerLifecycleStateIdle {
		t.Fatalf("expected state idle, got %s", created.LifecycleState)
	}
	if created.Revision != 1 {
		t.Fatalf("expected revision 1, got %d", created.Revision)
	}

	// 2. Reject worker with invalid plan (e.g. empty title or invalid checkpoints)
	_, err = svc.CreateWorker(ctx, "acct-test", "user-1", pebblestore.CreateWorkerRequest{
		Name:         "Bad Worker",
		Instructions: "Instructions",
		Automations: []pebblestore.WorkerAutomationDefinition{
			{
				Name:           "Invalid Plan Job",
				ActivationMode: "manual",
				Enabled:        true,
				PlanDocument: pebblestore.SessionPlanDocument{
					// Missing title and checkpoints
				},
			},
		},
	})
	if err == nil {
		t.Fatal("expected error creating worker with invalid plan document")
	}

	// 3. GetWorker
	loaded, ok, err := svc.GetWorker("acct-test", created.ID)
	if err != nil || !ok {
		t.Fatalf("GetWorker: ok=%v, err=%v", ok, err)
	}
	if loaded.Name != "DevOps Bot" {
		t.Fatalf("unexpected name: %s", loaded.Name)
	}

	// 4. UpdateWorker
	newDesc := "Updated infrastructure maintainer"
	updated, err := svc.UpdateWorker("acct-test", "user-1", created.ID, 1, pebblestore.UpdateWorkerRequest{
		Description:   &newDesc,
		ChangeSummary: "updated description",
	})
	if err != nil {
		t.Fatalf("UpdateWorker: %v", err)
	}
	if updated.Revision != 2 || updated.Description != newDesc {
		t.Fatalf("unexpected updated worker: %+v", updated)
	}

	// 5. Revision history inspection
	hist, _, err := svc.GetWorkerHistory("acct-test", created.ID, 10, "")
	if err != nil {
		t.Fatalf("GetWorkerHistory: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("expected 2 revisions, got %d", len(hist))
	}

	// 6. DeleteWorker
	err = svc.DeleteWorker("acct-test", "user-1", created.ID, 2)
	if err != nil {
		t.Fatalf("DeleteWorker: %v", err)
	}

	// Verify tombstoned state
	deleted, ok, err := svc.GetWorker("acct-test", created.ID)
	if err != nil || !ok {
		t.Fatalf("GetWorker deleted: ok=%v, err=%v", ok, err)
	}
	if deleted.LifecycleState != pebblestore.WorkerLifecycleStateDeleted {
		t.Fatalf("expected state deleted, got %s", deleted.LifecycleState)
	}
}

// Invariant: Exported definitions can be imported into fresh or existing identities.
// Threat: Schema mismatches or lossy exports.
// Boundary: session.Service ExportWorker, ImportWorkerAsNew, ImportWorkerUpdate.
func TestServiceWorkerImportExport(t *testing.T) {
	svc, _ := setupTestSessionService(t)
	ctx := context.Background()

	created, err := svc.CreateWorker(ctx, "acct-export", "user-1", pebblestore.CreateWorkerRequest{
		Name:         "Exportable Specialist",
		Description:  "Specialist for export tests",
		Instructions: "# Specialist Instructions\nRun specialized tasks.",
		Automations: []pebblestore.WorkerAutomationDefinition{
			{
				Name:           "Check Script",
				ActivationMode: "manual",
				Enabled:        true,
				PlanDocument:   testExecutablePlan("Check Script Plan"),
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}

	// Export
	def, rawJSON, err := svc.ExportWorker("acct-export", created.ID)
	if err != nil {
		t.Fatalf("ExportWorker: %v", err)
	}
	if def.Name != created.Name {
		t.Fatalf("unexpected export def name: %s", def.Name)
	}

	// Validate JSON directly
	validDef, err := svc.ValidatePortableWorkerDefinition(rawJSON)
	if err != nil {
		t.Fatalf("ValidatePortableWorkerDefinition: %v", err)
	}
	if validDef.Name != created.Name {
		t.Fatalf("validated def name mismatch: %s", validDef.Name)
	}

	// Import as new into target account
	importedNew, err := svc.ImportWorkerAsNew("acct-target", "user-2", rawJSON)
	if err != nil {
		t.Fatalf("ImportWorkerAsNew: %v", err)
	}
	if importedNew.ID == created.ID {
		t.Fatalf("ImportWorkerAsNew must allocate fresh ID, got %s", importedNew.ID)
	}
	if importedNew.LifecycleState != pebblestore.WorkerLifecycleStateIdle {
		t.Fatalf("imported worker must be idle, got %s", importedNew.LifecycleState)
	}

	// Import as update to an existing target worker
	existingTarget, err := svc.CreateWorker(ctx, "acct-target", "user-2", pebblestore.CreateWorkerRequest{
		Name:         "Old Target",
		Instructions: "Old instructions",
	})
	if err != nil {
		t.Fatalf("CreateWorker target: %v", err)
	}

	updatedTarget, err := svc.ImportWorkerUpdate("acct-target", "user-2", existingTarget.ID, 1, rawJSON)
	if err != nil {
		t.Fatalf("ImportWorkerUpdate: %v", err)
	}
	if updatedTarget.ID != existingTarget.ID || updatedTarget.Revision != 2 || updatedTarget.Name != created.Name {
		t.Fatalf("unexpected updated target: %+v", updatedTarget)
	}
}

// Invariant: Automations can be attached, updated, and removed with revision increments.
// Threat: Stale revision or broken plan document accepted into worker automation.
// Boundary: session.Service AttachWorkerAutomation, UpdateWorkerAutomation, RemoveWorkerAutomation.
func TestServiceWorkerAutomationOperations(t *testing.T) {
	svc, _ := setupTestSessionService(t)
	ctx := context.Background()

	w, err := svc.CreateWorker(ctx, "acct-auto", "user-1", pebblestore.CreateWorkerRequest{
		Name:         "Job Runner",
		Instructions: "Instructions",
	})
	if err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}

	// Attach
	w, err = svc.AttachWorkerAutomation("acct-auto", "user-1", w.ID, 1, pebblestore.WorkerAutomationDefinition{
		Name:           "Cron Job",
		ActivationMode: "cron",
		Schedule: &pebblestore.AutomationV2Schedule{
			Kind:     "cron",
			Cron:     "0 12 * * *",
			Timezone: "America/New_York",
		},
		Enabled:      true,
		PlanDocument: testExecutablePlan("Noon Cron Plan"),
	})
	if err != nil {
		t.Fatalf("AttachWorkerAutomation: %v", err)
	}
	if w.Revision != 2 || len(w.Automations) != 1 {
		t.Fatalf("expected rev 2 and 1 auto, got rev %d, %d autos", w.Revision, len(w.Automations))
	}
	autoID := w.Automations[0].ID

	// Update
	autoDef := w.Automations[0]
	autoDef.Name = "Updated Noon Job"
	w, err = svc.UpdateWorkerAutomation("acct-auto", "user-1", w.ID, autoID, 2, autoDef)
	if err != nil {
		t.Fatalf("UpdateWorkerAutomation: %v", err)
	}
	if w.Revision != 3 || w.Automations[0].Name != "Updated Noon Job" {
		t.Fatalf("unexpected updated worker: %+v", w)
	}

	// Remove
	w, err = svc.RemoveWorkerAutomation("acct-auto", "user-1", w.ID, autoID, 3)
	if err != nil {
		t.Fatalf("RemoveWorkerAutomation: %v", err)
	}
	if w.Revision != 4 || len(w.Automations) != 0 {
		t.Fatalf("unexpected removed worker: %+v", w)
	}
}

// Invariant: Migration converts legacy accepted records idempotently and links occurrence runs.
// Threat: Double migration duplicating records or failing to link occurrences.
// Boundary: session.Service MigrateLegacyAutomationsV2.
func TestServiceWorkerMigration(t *testing.T) {
	svc, store := setupTestSessionService(t)

	// Seed accepted legacy record
	autoID := "av2_service_mig_1"
	rec := pebblestore.AutomationV2Record{
		AutomationID: autoID,
		AcceptedBy:   "user-1",
		AcceptedAt:   time.Now().UnixMilli() - 1000,
		Enabled:      true,
		Generation:   1,
		AutomationV2Proposal: pebblestore.AutomationV2Proposal{
			AutomationV2Review: pebblestore.AutomationV2Review{ProposalID: "prop-mig", Revision: 1, Digest: "dig-mig"},
			AccountID:          "acct-service-mig",
			UserID:             "user-1",
			WorkspaceID:        "ws-mig",
			SessionID:          "sess-mig",
			Document:           testExecutablePlan("Service Migrated Plan"),
			CreatedAt:          time.Now().UnixMilli() - 2000,
		},
	}
	recBytes, _ := json.Marshal(rec)
	keyAccepted := fmt.Sprintf("automation/v2/accepted/%x/%x", "acct-service-mig", autoID)
	if err := store.PutBytes(keyAccepted, recBytes); err != nil {
		t.Fatalf("seed legacy record: %v", err)
	}

	// Run migration
	summary, err := svc.MigrateLegacyAutomationsV2("acct-service-mig")
	if err != nil {
		t.Fatalf("MigrateLegacyAutomationsV2: %v", err)
	}
	if summary.MigratedCount != 1 || summary.FailedCount != 0 {
		t.Fatalf("unexpected summary: %+v", summary)
	}

	// Retrieve migrated worker by preserved automation ID
	w, ok, err := svc.GetWorker("acct-service-mig", autoID)
	if err != nil || !ok {
		t.Fatalf("GetWorker by legacy av2 ID: ok=%v, err=%v", ok, err)
	}
	if w.LifecycleState != pebblestore.WorkerLifecycleStateActive {
		t.Fatalf("expected state active, got %s", w.LifecycleState)
	}
	if len(w.Automations) != 1 || w.Automations[0].ID != autoID {
		t.Fatalf("preserved automation ID mismatch: %+v", w.Automations)
	}

	// Run migration a second time: idempotent
	summary2, err := svc.MigrateLegacyAutomationsV2("acct-service-mig")
	if err != nil {
		t.Fatalf("MigrateLegacyAutomationsV2 pass 2: %v", err)
	}
	if summary2.MigratedCount != 0 || summary2.SkippedCount != 1 {
		t.Fatalf("expected 1 skipped, got %+v", summary2)
	}
}
