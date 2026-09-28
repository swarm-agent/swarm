package pebblestore

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestStore(t *testing.T) (*Store, *WorkerStore) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "worker_test.pebble")
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s, NewWorkerStore(s)
}

func testPlanDoc(title string) SessionPlanDocument {
	return SessionPlanDocument{
		Title:  title,
		Status: "pending",
		Info: SessionPlanInfo{
			Goal:    "Execute unit tests",
			Context: "Automated test environment context",
		},
		Checkpoints: []SessionPlanCheckpoint{
			{
				ID:     "cp-1",
				Title:  "First checkpoint",
				Status: "pending",
				Subtasks: []SessionPlanSubtask{
					{
						ID:     "sub-1",
						Title:  "First subtask",
						Status: "pending",
					},
				},
			},
		},
	}
}

// Invariant: A worker record and its attached automations must survive a complete store restart.
// Threat: Restart loses durable state, revisions, or detached automations.
// Boundary: WorkerStore GetWorker, CreateWorker, Close, Open.
func TestWorkerPersistenceAndRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "restart_worker.pebble")
	s1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open s1: %v", err)
	}
	ws1 := NewWorkerStore(s1)

	created, err := ws1.CreateWorker("acct-1", "user-1", CreateWorkerRequest{
		Name:         "Code Reviewer",
		Description:  "Automated reviewer",
		Instructions: "# Instructions\nAudit changes carefully.",
		Automations: []WorkerAutomationDefinition{
			{
				Name:           "Hourly Scan",
				ActivationMode: "interval",
				Schedule: &AutomationV2Schedule{
					Kind:            "interval",
					IntervalSeconds: 3600,
				},
				Enabled:      true,
				PlanDocument: testPlanDoc("Hourly Scan Plan"),
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}

	if created.LifecycleState != WorkerLifecycleStateIdle {
		t.Fatalf("expected state idle, got %s", created.LifecycleState)
	}
	if created.Revision != 1 {
		t.Fatalf("expected revision 1, got %d", created.Revision)
	}
	if len(created.Automations) != 1 {
		t.Fatalf("expected 1 automation, got %d", len(created.Automations))
	}
	autoID := created.Automations[0].ID

	// Close store and reopen from same path
	if err := s1.Close(); err != nil {
		t.Fatalf("Close s1: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open s2: %v", err)
	}
	defer s2.Close()
	ws2 := NewWorkerStore(s2)

	loaded, ok, err := ws2.GetWorker("acct-1", created.ID)
	if err != nil || !ok {
		t.Fatalf("GetWorker after restart: ok=%v, err=%v", ok, err)
	}
	if loaded.Name != created.Name || loaded.Instructions != created.Instructions {
		t.Fatalf("mismatched worker data after restart: %+v vs %+v", loaded, created)
	}
	if loaded.Revision != 1 || loaded.LifecycleState != WorkerLifecycleStateIdle {
		t.Fatalf("mismatched revision/state: %+v", loaded)
	}
	if len(loaded.Automations) != 1 || loaded.Automations[0].ID != autoID {
		t.Fatalf("mismatched automations after restart: %+v", loaded.Automations)
	}

	// Lookup by automation ID
	byAuto, ok, err := ws2.GetWorkerByAutomation("acct-1", autoID)
	if err != nil || !ok || byAuto.ID != created.ID {
		t.Fatalf("GetWorkerByAutomation: ok=%v, err=%v, id=%s", ok, err, byAuto.ID)
	}
}

// Invariant: Workers belonging to account A must never be accessible or mutable by account B.
// Threat: Cross-account data leak or unauthorized mutation.
// Boundary: WorkerStore GetWorker, ListWorkers, UpdateWorker, DeleteWorker.
func TestWorkerAccountIsolation(t *testing.T) {
	_, ws := openTestStore(t)

	wA, err := ws.CreateWorker("acct-A", "user-1", CreateWorkerRequest{
		Name:         "Secret Worker A",
		Instructions: "Top secret instructions A",
	}, nil)
	if err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}

	// Account B cannot Get
	_, ok, err := ws.GetWorker("acct-B", wA.ID)
	if err != nil {
		t.Fatalf("GetWorker unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("account B unexpectedly found account A worker")
	}

	// Account B List does not include account A
	resB, err := ws.ListWorkers("acct-B", ListWorkersQuery{})
	if err != nil {
		t.Fatalf("ListWorkers: %v", err)
	}
	if len(resB.Workers) != 0 {
		t.Fatalf("account B list should be empty, got %d", len(resB.Workers))
	}

	// Account B cannot Update
	newName := "Hacked Name"
	_, err = ws.UpdateWorker("acct-B", "user-2", wA.ID, 1, UpdateWorkerRequest{
		Name: &newName,
	}, nil)
	if !errors.Is(err, ErrWorkerNotFound) {
		t.Fatalf("expected ErrWorkerNotFound on cross-account update, got %v", err)
	}

	// Account B cannot Delete
	err = ws.DeleteWorker("acct-B", "user-2", wA.ID, 1)
	if !errors.Is(err, ErrWorkerNotFound) {
		t.Fatalf("expected ErrWorkerNotFound on cross-account delete, got %v", err)
	}
}

// Invariant: Updates must enforce expected revision guards to prevent concurrent overwrite.
// Threat: Lost updates or race conditions overwriting concurrent revisions.
// Boundary: WorkerStore UpdateWorker.
func TestWorkerStaleUpdateConflict(t *testing.T) {
	_, ws := openTestStore(t)

	created, err := ws.CreateWorker("acct-1", "user-1", CreateWorkerRequest{
		Name:         "Initial Worker",
		Instructions: "Initial instructions",
	}, nil)
	if err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}

	// Valid update at revision 1
	name1 := "Updated Name 1"
	u1, err := ws.UpdateWorker("acct-1", "user-1", created.ID, 1, UpdateWorkerRequest{
		Name: &name1,
	}, nil)
	if err != nil {
		t.Fatalf("UpdateWorker rev 1: %v", err)
	}
	if u1.Revision != 2 {
		t.Fatalf("expected rev 2, got %d", u1.Revision)
	}

	// Stale update claiming revision 1 must conflict
	nameStale := "Stale Name"
	_, err = ws.UpdateWorker("acct-1", "user-1", created.ID, 1, UpdateWorkerRequest{
		Name: &nameStale,
	}, nil)
	if !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("expected ErrWorkerConflict on stale update, got %v", err)
	}

	// Valid update at revision 2 succeeds
	name2 := "Updated Name 2"
	u2, err := ws.UpdateWorker("acct-1", "user-1", created.ID, 2, UpdateWorkerRequest{
		Name: &name2,
	}, nil)
	if err != nil {
		t.Fatalf("UpdateWorker rev 2: %v", err)
	}
	if u2.Revision != 3 || u2.Name != name2 {
		t.Fatalf("expected rev 3 with %s, got %+v", name2, u2)
	}
}

// Invariant: Every worker revision must be recorded in immutable history.
// Threat: Historical revisions lost or mutable.
// Boundary: WorkerStore GetWorkerRevision, ListWorkerRevisions.
func TestWorkerHistoryAndRevisions(t *testing.T) {
	_, ws := openTestStore(t)

	w, err := ws.CreateWorker("acct-1", "user-1", CreateWorkerRequest{
		Name:         "Worker V1",
		Instructions: "Inst V1",
	}, nil)
	if err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}

	name2 := "Worker V2"
	_, err = ws.UpdateWorker("acct-1", "user-1", w.ID, 1, UpdateWorkerRequest{
		Name:          &name2,
		ChangeSummary: "changed to V2",
	}, nil)
	if err != nil {
		t.Fatalf("UpdateWorker V2: %v", err)
	}

	name3 := "Worker V3"
	_, err = ws.UpdateWorker("acct-1", "user-1", w.ID, 2, UpdateWorkerRequest{
		Name:          &name3,
		ChangeSummary: "changed to V3",
	}, nil)
	if err != nil {
		t.Fatalf("UpdateWorker V3: %v", err)
	}

	// Check revision 1
	rev1, ok, err := ws.GetWorkerRevision("acct-1", w.ID, 1)
	if err != nil || !ok {
		t.Fatalf("GetWorkerRevision 1: ok=%v, err=%v", ok, err)
	}
	if rev1.Worker.Name != "Worker V1" || rev1.Revision != 1 {
		t.Fatalf("mismatched rev1: %+v", rev1)
	}

	// Check revision 2
	rev2, ok, err := ws.GetWorkerRevision("acct-1", w.ID, 2)
	if err != nil || !ok {
		t.Fatalf("GetWorkerRevision 2: ok=%v, err=%v", ok, err)
	}
	if rev2.Worker.Name != "Worker V2" || rev2.Revision != 2 || rev2.ChangeSummary != "changed to V2" {
		t.Fatalf("mismatched rev2: %+v", rev2)
	}

	// Check revision 3
	rev3, ok, err := ws.GetWorkerRevision("acct-1", w.ID, 3)
	if err != nil || !ok {
		t.Fatalf("GetWorkerRevision 3: ok=%v, err=%v", ok, err)
	}
	if rev3.Worker.Name != "Worker V3" || rev3.Revision != 3 {
		t.Fatalf("mismatched rev3: %+v", rev3)
	}

	// List revisions
	histList, _, err := ws.ListWorkerRevisions("acct-1", w.ID, 10, "")
	if err != nil {
		t.Fatalf("ListWorkerRevisions: %v", err)
	}
	if len(histList) != 3 {
		t.Fatalf("expected 3 revisions in history, got %d", len(histList))
	}
}

// Invariant: Portable worker definition import must strictly reject invalid, unknown, or oversized payloads.
// Threat: Malformed JSON or unknown structural fields escaping validation.
// Boundary: ValidatePortableWorkerDefinition.
func TestPortableWorkerDefinitionStrictValidation(t *testing.T) {
	// 1. Oversized payload
	oversized := make([]byte, 513*1024)
	for i := range oversized {
		oversized[i] = ' '
	}
	if _, err := ValidatePortableWorkerDefinition(oversized, nil); err == nil {
		t.Fatal("expected error on oversized definition")
	}

	// 2. Unsupported schema version
	badVersion := `{"schema_version": 2, "name": "Test", "instructions": "inst"}`
	if _, err := ValidatePortableWorkerDefinition([]byte(badVersion), nil); err == nil {
		t.Fatal("expected error on schema_version 2")
	}

	// 3. Unknown structural fields
	unknownField := `{"schema_version": 1, "name": "Test", "instructions": "inst", "unrecognized_extra": "danger"}`
	if _, err := ValidatePortableWorkerDefinition([]byte(unknownField), nil); err == nil {
		t.Fatal("expected error on unknown structural field")
	}

	// 4. Missing name
	missingName := `{"schema_version": 1, "name": "  ", "instructions": "inst"}`
	if _, err := ValidatePortableWorkerDefinition([]byte(missingName), nil); err == nil {
		t.Fatal("expected error on missing name")
	}

	// 5. Invalid plan with executed state
	executedPlan := `{
		"schema_version": 1,
		"name": "Test",
		"instructions": "inst",
		"automations": [
			{
				"name": "auto-1",
				"activation_mode": "manual",
				"enabled": true,
				"plan": {
					"title": "Executed Plan",
					"status": "completed",
					"checkpoints": [{"id": "c1", "status": "completed"}]
				}
			}
		]
	}`
	if _, err := ValidatePortableWorkerDefinition([]byte(executedPlan), nil); err == nil {
		t.Fatal("expected error on plan with executed state")
	}

	// 6. Valid definition parses cleanly
	validJSON := `{
		"schema_version": 1,
		"name": "Valid Worker",
		"description": "A valid portable worker",
		"instructions": "# Instructions\nDo valid work.",
		"capabilities": [
			{"type": "tool", "name": "git", "description": "Git inspections", "required": true}
		],
		"workspace_requirements": [
			{"role": "primary", "description": "Source checkout", "required": true}
		],
		"automations": [
			{
				"name": "Daily Job",
				"activation_mode": "cron",
				"schedule": {
					"kind": "cron",
					"cron": "0 2 * * *",
					"timezone": "UTC"
				},
				"enabled": true,
				"plan": {
					"title": "Daily Job Plan",
					"status": "pending",
					"checkpoints": [{"id": "c1", "status": "pending"}]
				}
			}
		]
	}`
	def, err := ValidatePortableWorkerDefinition([]byte(validJSON), nil)
	if err != nil {
		t.Fatalf("ValidatePortableWorkerDefinition valid: %v", err)
	}
	if def.Name != "Valid Worker" || len(def.Automations) != 1 {
		t.Fatalf("unexpected parsed def: %+v", def)
	}
}

// Invariant: Exporting a worker definition and re-importing it must preserve all definition fields without semantic loss.
// Threat: Lossy translation or dropped metadata across export/import.
// Boundary: WorkerStore ExportWorker, ImportWorkerAsNew.
func TestWorkerSemanticRoundtrip(t *testing.T) {
	_, ws := openTestStore(t)

	w, err := ws.CreateWorker("acct-1", "user-1", CreateWorkerRequest{
		Name:         "Roundtrip Worker",
		Description:  "Tests export/import fidelity",
		Instructions: "# Roundtrip Instructions\nExecute faithfully.",
		RequestedCapabilities: []WorkerCapabilityRequest{
			{Type: "tool", Name: "bash", Description: "Command runner", Required: false},
		},
		WorkspaceRequirements: []WorkerWorkspaceRequirement{
			{Role: "primary", Description: "Primary repo", Required: true},
		},
		Automations: []WorkerAutomationDefinition{
			{
				Name:           "Nightly Audit",
				ActivationMode: "cron",
				Schedule: &AutomationV2Schedule{
					Kind:     "cron",
					Cron:     "30 3 * * *",
					Timezone: "UTC",
				},
				Enabled:                 true,
				PlanDocument:            testPlanDoc("Nightly Plan"),
				InputRequirements:       []WorkerInputRequirement{{Name: "branch", Kind: "string", Required: false, Default: "dev"}},
				DeliverableRequirements: []WorkerDeliverableRequirement{{Name: "audit_report", Kind: "report", Required: true}},
			},
		},
		Metadata: map[string]any{"env": "staging", "tier": float64(2)},
	}, nil)
	if err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}

	def, rawJSON, err := ws.ExportWorker("acct-1", w.ID)
	if err != nil {
		t.Fatalf("ExportWorker: %v", err)
	}
	if def.Name != w.Name || def.Instructions != w.Instructions {
		t.Fatalf("exported def mismatch: %+v", def)
	}

	// Import as new worker in account 2
	imported, err := ws.ImportWorkerAsNew("acct-2", "user-2", rawJSON, nil)
	if err != nil {
		t.Fatalf("ImportWorkerAsNew: %v", err)
	}

	// Verify imported properties
	if imported.ID == w.ID {
		t.Fatalf("imported worker must receive fresh ID, got %s", imported.ID)
	}
	if imported.AccountScopeID != "acct-2" {
		t.Fatalf("expected acct-2, got %s", imported.AccountScopeID)
	}
	if imported.LifecycleState != WorkerLifecycleStateIdle {
		t.Fatalf("imported worker must be idle, got %s", imported.LifecycleState)
	}
	if imported.Revision != 1 {
		t.Fatalf("imported worker must have revision 1, got %d", imported.Revision)
	}
	if imported.Name != w.Name || imported.Description != w.Description || imported.Instructions != w.Instructions {
		t.Fatalf("semantic mismatch on basic fields: %+v vs %+v", imported, w)
	}
	if len(imported.RequestedCapabilities) != len(w.RequestedCapabilities) || imported.RequestedCapabilities[0].Name != "bash" {
		t.Fatalf("capabilities mismatch: %+v", imported.RequestedCapabilities)
	}
	if len(imported.WorkspaceRequirements) != len(w.WorkspaceRequirements) || imported.WorkspaceRequirements[0].Role != "primary" {
		t.Fatalf("workspace reqs mismatch: %+v", imported.WorkspaceRequirements)
	}
	if len(imported.Automations) != len(w.Automations) {
		t.Fatalf("automations count mismatch: %d vs %d", len(imported.Automations), len(w.Automations))
	}
	importedAuto := imported.Automations[0]
	origAuto := w.Automations[0]
	if importedAuto.Name != origAuto.Name || importedAuto.ActivationMode != origAuto.ActivationMode {
		t.Fatalf("automation mismatch: %+v vs %+v", importedAuto, origAuto)
	}
	if importedAuto.Schedule.Cron != origAuto.Schedule.Cron || importedAuto.Schedule.Timezone != origAuto.Schedule.Timezone {
		t.Fatalf("schedule mismatch: %+v vs %+v", importedAuto.Schedule, origAuto.Schedule)
	}
	if imported.Provenance == nil || imported.Provenance.SourceWorkerID != w.ID || imported.Provenance.SourceRevision != w.Revision {
		t.Fatalf("provenance mismatch: %+v", imported.Provenance)
	}
}

// Invariant: Updating an existing worker with an active schedule is rejected until Checkpoint 2 controls exist.
// Threat: Mutating an active schedule creates desync with running daemon scheduler.
// Boundary: WorkerStore UpdateWorker, ImportWorkerUpdate.
func TestWorkerActiveScheduleUpdateRejection(t *testing.T) {
	_, ws := openTestStore(t)

	w, err := ws.CreateWorker("acct-1", "user-1", CreateWorkerRequest{
		Name:         "Scheduled Worker",
		Instructions: "Instructions",
		Automations: []WorkerAutomationDefinition{
			{
				Name:           "Active Schedule",
				ActivationMode: "interval",
				Schedule: &AutomationV2Schedule{
					Kind:            "interval",
					IntervalSeconds: 120,
				},
				Enabled:      true,
				PlanDocument: testPlanDoc("Active Plan"),
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}

	// Simulate activating the worker (e.g. from deployment/migration)
	w.LifecycleState = WorkerLifecycleStateActive
	wBytes, _ := json.Marshal(w)
	_ = ws.store.PutBytes(KeyWorker("acct-1", w.ID), wBytes)

	// Attempting to update the active worker with enabled interval schedule must be rejected
	newName := "Renamed Worker"
	_, err = ws.UpdateWorker("acct-1", "user-1", w.ID, 1, UpdateWorkerRequest{
		Name: &newName,
	}, nil)
	if !errors.Is(err, ErrActiveScheduleUpdateRejected) {
		t.Fatalf("expected ErrActiveScheduleUpdateRejected, got %v", err)
	}

	// Attempting import update must also be rejected
	_, expJSON, _ := ws.ExportWorker("acct-1", w.ID)
	_, err = ws.ImportWorkerUpdate("acct-1", "user-1", w.ID, 1, expJSON, nil)
	if !errors.Is(err, ErrActiveScheduleUpdateRejected) {
		t.Fatalf("expected ErrActiveScheduleUpdateRejected on import update, got %v", err)
	}
}

// Invariant: Automation attachment, update, and detachment must update worker revision and lookup index.
// Threat: Orphaned automations or broken reverse lookup.
// Boundary: WorkerStore AttachWorkerAutomation, UpdateWorkerAutomation, RemoveWorkerAutomation.
func TestWorkerAttachedAutomations(t *testing.T) {
	_, ws := openTestStore(t)

	w, err := ws.CreateWorker("acct-1", "user-1", CreateWorkerRequest{
		Name:         "Modular Worker",
		Instructions: "Modular instructions",
	}, nil)
	if err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}

	// Attach automation
	w, err = ws.AttachWorkerAutomation("acct-1", "user-1", w.ID, 1, WorkerAutomationDefinition{
		Name:           "Task A",
		ActivationMode: "manual",
		Enabled:        true,
		PlanDocument:   testPlanDoc("Plan A"),
	}, nil)
	if err != nil {
		t.Fatalf("AttachWorkerAutomation: %v", err)
	}
	if w.Revision != 2 || len(w.Automations) != 1 {
		t.Fatalf("expected rev 2 and 1 auto, got rev %d, %d autos", w.Revision, len(w.Automations))
	}
	autoID := w.Automations[0].ID

	// Lookup by auto ID
	byAuto, ok, err := ws.GetWorkerByAutomation("acct-1", autoID)
	if err != nil || !ok || byAuto.ID != w.ID {
		t.Fatalf("lookup by automation ID failed: ok=%v, id=%s", ok, byAuto.ID)
	}

	// Update automation
	autoUpdate := w.Automations[0]
	autoUpdate.Name = "Task A Renamed"
	w, err = ws.UpdateWorkerAutomation("acct-1", "user-1", w.ID, autoID, 2, autoUpdate, nil)
	if err != nil {
		t.Fatalf("UpdateWorkerAutomation: %v", err)
	}
	if w.Revision != 3 || w.Automations[0].Name != "Task A Renamed" {
		t.Fatalf("expected rev 3 with renamed auto, got %+v", w)
	}

	// Remove automation
	w, err = ws.RemoveWorkerAutomation("acct-1", "user-1", w.ID, autoID, 3)
	if err != nil {
		t.Fatalf("RemoveWorkerAutomation: %v", err)
	}
	if w.Revision != 4 || len(w.Automations) != 0 {
		t.Fatalf("expected rev 4 and 0 autos, got %+v", w)
	}

	// Lookup by removed auto ID must now fail
	_, ok, err = ws.GetWorkerByAutomation("acct-1", autoID)
	if err != nil {
		t.Fatalf("lookup error: %v", err)
	}
	if ok {
		t.Fatalf("lookup for removed automation should not succeed")
	}
}

// Invariant: Worker runs must link to worker ID, occurrence ID, and pin revisions immutably.
// Threat: Mutating worker later rewrites historical run revisions.
// Boundary: WorkerStore RecordWorkerRun, GetWorkerRun, ListWorkerRuns.
func TestWorkerRunsLinkageAndRevisionPinning(t *testing.T) {
	_, ws := openTestStore(t)

	w, err := ws.CreateWorker("acct-1", "user-1", CreateWorkerRequest{
		Name:         "Run Worker",
		Instructions: "Run instructions",
		Automations: []WorkerAutomationDefinition{
			{
				Name:           "Job 1",
				ActivationMode: "manual",
				Enabled:        true,
				PlanDocument:   testPlanDoc("Plan 1"),
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}
	autoID := w.Automations[0].ID

	// Record run pinning to worker revision 1
	run, err := ws.RecordWorkerRun("acct-1", WorkerRunRecord{
		WorkerID:           w.ID,
		WorkerRevision:     1,
		AutomationID:       autoID,
		AutomationRevision: 1,
		OccurrenceID:       "occ-123",
		SessionID:          "sess-456",
		RequestSource:      "manual",
		Status:             "succeeded",
		Deliverables: []SessionPlanArtifactReference{
			{Type: "workspace_file", Path: "output.txt"},
		},
	})
	if err != nil {
		t.Fatalf("RecordWorkerRun: %v", err)
	}
	if run.ID == "" || run.WorkerRevision != 1 {
		t.Fatalf("unexpected run: %+v", run)
	}

	// Update worker to revision 2
	newName := "Run Worker V2"
	w, err = ws.UpdateWorker("acct-1", "user-1", w.ID, 1, UpdateWorkerRequest{
		Name: &newName,
	}, nil)
	if err != nil {
		t.Fatalf("UpdateWorker: %v", err)
	}
	if w.Revision != 2 {
		t.Fatalf("expected rev 2, got %d", w.Revision)
	}

	// Check historical run: still pinned to revision 1
	loadedRun, ok, err := ws.GetWorkerRun("acct-1", w.ID, run.ID)
	if err != nil || !ok {
		t.Fatalf("GetWorkerRun: ok=%v, err=%v", ok, err)
	}
	if loadedRun.WorkerRevision != 1 {
		t.Fatalf("run revision must remain pinned to 1, got %d", loadedRun.WorkerRevision)
	}
	if loadedRun.OccurrenceID != "occ-123" || len(loadedRun.Deliverables) != 1 {
		t.Fatalf("run data mismatch: %+v", loadedRun)
	}

	// List runs
	runs, _, err := ws.ListWorkerRuns("acct-1", w.ID, 10, "")
	if err != nil {
		t.Fatalf("ListWorkerRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
}

// Invariant: Migration must idempotently convert accepted AutomationV2 records, preserve av2 IDs,
// maintain active/cancelled status truthfully, link occurrences, and reject collisions without merge.
// Threat: Duplicated workers, lost history, silent schedule changes or collision corruption.
// Boundary: WorkerStore MigrateLegacyAutomationsV2.
func TestLegacyAutomationV2Migration(t *testing.T) {
	s, ws := openTestStore(t)

	// Seed 3 legacy accepted AutomationV2Records:
	// 1. Scheduled active record
	activeAutoID := "av2_scheduled_01"
	activeDoc := testPlanDoc("Active Scheduled Plan")
	activeDoc.AutomationV2 = &AutomationV2Settings{
		Schedule: AutomationV2Schedule{
			Kind:            "interval",
			IntervalSeconds: 300,
		},
	}
	recActive := AutomationV2Record{
		AutomationID: activeAutoID,
		AcceptedBy:   "user-1",
		AcceptedAt:   time.Now().UnixMilli() - 10000,
		Enabled:      true,
		Generation:   2,
		AutomationV2Proposal: AutomationV2Proposal{
			AutomationV2Review: AutomationV2Review{ProposalID: "prop-1", Revision: 2, Digest: "dig-1"},
			AccountID:          "acct-mig",
			UserID:             "user-1",
			WorkspaceID:        "ws-mig-1",
			SessionID:          "sess-legacy-1",
			Document:           activeDoc,
			CreatedAt:          time.Now().UnixMilli() - 20000,
		},
	}

	// 2. Cancelled record
	cancelledAutoID := "av2_cancelled_02"
	recCancelled := AutomationV2Record{
		AutomationID: cancelledAutoID,
		AcceptedBy:   "user-1",
		AcceptedAt:   time.Now().UnixMilli() - 5000,
		Enabled:      false,
		Cancelled:    true,
		Generation:   1,
		AutomationV2Proposal: AutomationV2Proposal{
			AutomationV2Review: AutomationV2Review{ProposalID: "prop-2", Revision: 1, Digest: "dig-2"},
			AccountID:          "acct-mig",
			UserID:             "user-1",
			WorkspaceID:        "ws-mig-1",
			SessionID:          "sess-legacy-2",
			Document:           testPlanDoc("Cancelled Plan"),
			CreatedAt:          time.Now().UnixMilli() - 10000,
		},
	}

	// Write directly to legacy Pebble keys
	bActive, _ := json.Marshal(recActive)
	bCancelled, _ := json.Marshal(recCancelled)
	_ = s.PutBytes(automationV2Key("accepted", "acct-mig", activeAutoID), bActive)
	_ = s.PutBytes(automationV2Key("accepted", "acct-mig", cancelledAutoID), bCancelled)

	// Seed occurrence for active record
	occ := AutomationV2Occurrence{
		ID:         "occ_mig_1",
		Record:     recActive,
		AdmittedAt: time.Now().UnixMilli() - 5000,
		ObservedAt: time.Now().UnixMilli() - 1000,
		SessionID:  "exec-sess-1",
		RunID:      "run-1",
		State:      "succeeded",
		Deliverables: []SessionPlanArtifactReference{
			{Type: "workspace_file", Path: "migrated_output.json"},
		},
	}
	bOcc, _ := json.Marshal(occ)
	_ = s.PutBytes(automationV2OccurrenceKey(occ), bOcc)

	// Run migration pass 1
	summary1, err := ws.MigrateLegacyAutomationsV2("acct-mig")
	if err != nil {
		t.Fatalf("MigrateLegacyAutomationsV2 pass 1: %v", err)
	}
	if summary1.MigratedCount != 2 || summary1.FailedCount != 0 {
		t.Fatalf("unexpected summary pass 1: %+v", summary1)
	}

	// Verify active record was migrated truthfully as active with interval schedule
	wActive, ok, err := ws.GetWorker("acct-mig", "worker_"+activeAutoID)
	if err != nil || !ok {
		t.Fatalf("GetWorker active: ok=%v, err=%v", ok, err)
	}
	if wActive.LifecycleState != WorkerLifecycleStateActive {
		t.Fatalf("expected state active, got %s", wActive.LifecycleState)
	}
	if wActive.Revision != 2 {
		t.Fatalf("expected revision 2, got %d", wActive.Revision)
	}
	if len(wActive.Automations) != 1 || wActive.Automations[0].ID != activeAutoID {
		t.Fatalf("expected preserved av2 ID on automation, got %+v", wActive.Automations)
	}
	if wActive.Automations[0].ActivationMode != "interval" || !wActive.Automations[0].Enabled {
		t.Fatalf("expected enabled interval automation, got %+v", wActive.Automations[0])
	}

	// Verify occurrence was linked as run
	runs, _, err := ws.ListWorkerRuns("acct-mig", wActive.ID, 10, "")
	if err != nil {
		t.Fatalf("ListWorkerRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].OccurrenceID != "occ_mig_1" {
		t.Fatalf("expected linked occurrence run, got %+v", runs)
	}
	if runs[0].WorkerRevision != 2 || runs[0].AutomationRevision != 2 {
		t.Fatalf("expected pinned revisions 2/2, got %+v", runs[0])
	}

	// Verify cancelled record was migrated as archived
	wCancelled, ok, err := ws.GetWorker("acct-mig", "worker_"+cancelledAutoID)
	if err != nil || !ok {
		t.Fatalf("GetWorker cancelled: ok=%v, err=%v", ok, err)
	}
	if wCancelled.LifecycleState != WorkerLifecycleStateArchived {
		t.Fatalf("expected state archived, got %s", wCancelled.LifecycleState)
	}

	// Run migration pass 2: must be idempotent!
	summary2, err := ws.MigrateLegacyAutomationsV2("acct-mig")
	if err != nil {
		t.Fatalf("MigrateLegacyAutomationsV2 pass 2: %v", err)
	}
	if summary2.MigratedCount != 0 || summary2.SkippedCount != 2 || summary2.FailedCount != 0 {
		t.Fatalf("expected 2 skipped and 0 migrated on pass 2, got %+v", summary2)
	}

	// Verify collision detection without merge
	// Seed a conflicting legacy record with same ID but different session/proposal
	recCollision := AutomationV2Record{
		AutomationID: activeAutoID,
		AcceptedBy:   "user-hacker",
		AcceptedAt:   time.Now().UnixMilli(),
		Enabled:      true,
		Generation:   1,
		AutomationV2Proposal: AutomationV2Proposal{
			AutomationV2Review: AutomationV2Review{ProposalID: "prop-different", Revision: 1, Digest: "dig-diff"},
			AccountID:          "acct-collision",
			UserID:             "user-hacker",
			WorkspaceID:        "ws-diff",
			SessionID:          "sess-different",
			Document:           testPlanDoc("Collision Plan"),
		},
	}
	bCollision, _ := json.Marshal(recCollision)
	// Place collision in acct-mig under different session key in accepted index
	_ = s.PutBytes(automationV2Key("accepted", "acct-collision", activeAutoID), bCollision)
	// Mutate existing worker's provenance in acct-collision to create collision
	_ = s.PutBytes(KeyWorker("acct-collision", "worker_"+activeAutoID), []byte(`{"id":"worker_`+activeAutoID+`","account_scope_id":"acct-collision","provenance":{"source_proposal_id":"prop-original","source_session_id":"sess-orig"}}`))

	colSummary, err := ws.MigrateLegacyAutomationsV2("acct-collision")
	if err != nil {
		t.Fatalf("Migrate collision: %v", err)
	}
	if colSummary.FailedCount != 1 || len(colSummary.Errors) != 1 {
		t.Fatalf("expected collision failure, got %+v", colSummary)
	}
}

// Invariant: New idle workers created via the SDK/store must never appear in the legacy scheduler's accepted scan.
// Threat: New idle worker accidentally treated as active legacy schedule.
// Boundary: ScanAutomationV2Accepted vs KeyWorker.
func TestNewSDKIdleWorkerDoesNotEnterLegacyScheduler(t *testing.T) {
	s, ws := openTestStore(t)

	w, err := ws.CreateWorker("acct-sched", "user-1", CreateWorkerRequest{
		Name:         "Idle SDK Worker",
		Instructions: "Standby for commands",
		Automations: []WorkerAutomationDefinition{
			{
				Name:           "Idle Job",
				ActivationMode: "interval",
				Schedule: &AutomationV2Schedule{
					Kind:            "interval",
					IntervalSeconds: 600,
				},
				Enabled:      false,
				PlanDocument: testPlanDoc("Idle Plan"),
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}

	if w.LifecycleState != WorkerLifecycleStateIdle {
		t.Fatalf("expected state idle, got %s", w.LifecycleState)
	}

	// Scan legacy accepted records
	rows, _, err := NewSessionStore(s).ScanAutomationV2Accepted("")
	if err != nil {
		t.Fatalf("ScanAutomationV2Accepted: %v", err)
	}
	for _, r := range rows {
		if r.AccountID == "acct-sched" || strings.Contains(r.AutomationID, w.ID) {
			t.Fatalf("new SDK idle worker leaked into legacy scheduler scan: %+v", r)
		}
	}
}
