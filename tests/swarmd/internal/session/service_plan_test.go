package session

import (
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestServicePlanLifecycle(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "session-plan.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	svc := NewService(pebblestore.NewSessionStore(store), events)
	created, _, err := svc.CreateSession("Plan Session", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	first, _, err := svc.SavePlan(created.ID, "plan_alpha", "Alpha", "# Alpha\n\n- [ ] one", "draft", "draft", true)
	if err != nil {
		t.Fatalf("save first plan: %v", err)
	}
	if first.ID != "plan_alpha" {
		t.Fatalf("expected first plan id plan_alpha, got %q", first.ID)
	}

	second, _, err := svc.SavePlan(created.ID, "plan_beta", "Beta", "# Beta\n\n- [ ] two", "draft", "draft", false)
	if err != nil {
		t.Fatalf("save second plan: %v", err)
	}

	plans, activeID, err := svc.ListPlans(created.ID, 10)
	if err != nil {
		t.Fatalf("list plans: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("expected 2 plans, got %d", len(plans))
	}
	if activeID != first.ID {
		t.Fatalf("expected active plan %q, got %q", first.ID, activeID)
	}

	activated, _, err := svc.SetActivePlan(created.ID, second.ID)
	if err != nil {
		t.Fatalf("set active plan: %v", err)
	}
	if activated.ID != second.ID {
		t.Fatalf("expected activated plan %q, got %q", second.ID, activated.ID)
	}

	activePlan, ok, err := svc.GetActivePlan(created.ID)
	if err != nil {
		t.Fatalf("get active plan: %v", err)
	}
	if !ok {
		t.Fatalf("expected active plan to exist")
	}
	if activePlan.ID != second.ID {
		t.Fatalf("expected active plan %q, got %q", second.ID, activePlan.ID)
	}
}

func TestServicePlanUpdateLineage(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "session-plan-lineage.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	svc := NewService(pebblestore.NewSessionStore(store), events)
	created, _, err := svc.CreateSession("Plan Lineage", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	first, _, err := svc.SavePlanWithMetadata(created.ID, "plan_alpha", "Alpha", "# Alpha\n\n- [ ] one", "draft", "draft", true, PlanSaveMetadata{UpdateSummary: "initial checkpoint", UpdateScope: "phase 1", UpdateKind: "checkpoint", Checkpoint: true})
	if err != nil {
		t.Fatalf("save first plan: %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("first version = %d, want 1", first.Version)
	}

	updated, _, err := svc.SavePlanWithMetadata(created.ID, "plan_alpha", "Alpha", "# Alpha\n\n- [x] one\n- [ ] two", "draft", "draft", true, PlanSaveMetadata{UpdateSummary: "mark phase one complete", UpdateScope: "phase 1", UpdateKind: "scope_update", Checkpoint: true})
	if err != nil {
		t.Fatalf("save updated plan: %v", err)
	}
	if updated.Version != 2 || updated.ParentRevision != 1 {
		t.Fatalf("updated lineage version=%d parent=%d, want version=2 parent=1", updated.Version, updated.ParentRevision)
	}
	if updated.UpdateSummary != "mark phase one complete" || updated.UpdateScope != "phase 1" || !updated.Checkpoint {
		t.Fatalf("updated metadata summary=%q scope=%q checkpoint=%v", updated.UpdateSummary, updated.UpdateScope, updated.Checkpoint)
	}
	if !strings.Contains(updated.PriorPlan, "- [ ] one") {
		t.Fatalf("updated prior plan did not preserve previous body: %q", updated.PriorPlan)
	}

	revisions, err := svc.ListPlanRevisions(created.ID, "plan_alpha", 10)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revisions) != 1 {
		t.Fatalf("expected 1 archived revision, got %d", len(revisions))
	}
	if revisions[0].Version != 1 || !strings.Contains(revisions[0].Plan, "- [ ] one") {
		t.Fatalf("archived revision version=%d plan=%q", revisions[0].Version, revisions[0].Plan)
	}
}

func TestSessionMetadataRoundTrip(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "session-metadata.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	svc := NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := svc.CreateSession("metadata", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	metadata := map[string]any{
		"task_launches": map[string]any{
			"task_1": map[string]any{
				"status":      "requested",
				"goal":        "Inspect repo",
				"child_count": 1,
			},
		},
	}
	updated, event, err := svc.UpdateMetadata(session.ID, metadata)
	if err != nil {
		t.Fatalf("update metadata: %v", err)
	}
	if event == nil {
		t.Fatalf("expected metadata update event")
	}
	if updated.Metadata == nil {
		t.Fatalf("expected metadata on updated session")
	}
	launches, ok := updated.Metadata["task_launches"].(map[string]any)
	if !ok {
		t.Fatalf("task_launches type = %T", updated.Metadata["task_launches"])
	}
	entry, ok := launches["task_1"].(map[string]any)
	if !ok {
		t.Fatalf("task_1 type = %T", launches["task_1"])
	}
	if got := strings.TrimSpace(mapString(entry, "status")); got != "requested" {
		t.Fatalf("status = %q, want requested", got)
	}

	persisted, ok, err := svc.GetSession(session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if !ok {
		t.Fatalf("expected persisted session")
	}
	if persisted.Metadata == nil {
		t.Fatalf("expected persisted metadata")
	}
	persistedLaunches, ok := persisted.Metadata["task_launches"].(map[string]any)
	if !ok {
		t.Fatalf("persisted task_launches type = %T", persisted.Metadata["task_launches"])
	}
	persistedEntry, ok := persistedLaunches["task_1"].(map[string]any)
	if !ok {
		t.Fatalf("persisted task_1 type = %T", persistedLaunches["task_1"])
	}
	if got := strings.TrimSpace(mapString(persistedEntry, "goal")); got != "Inspect repo" {
		t.Fatalf("goal = %q, want Inspect repo", got)
	}
}
