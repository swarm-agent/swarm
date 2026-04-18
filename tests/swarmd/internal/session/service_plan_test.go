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
