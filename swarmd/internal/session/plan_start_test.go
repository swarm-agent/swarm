package session

import (
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestStartNewPlanRefusesWhenActivePlanExists(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	first, _, err := svc.StartNewPlan(sessionID, "First Plan")
	if err != nil {
		t.Fatalf("create first plan: %v", err)
	}

	_, _, err = svc.StartNewPlan(sessionID, "Second Plan")
	if err == nil {
		t.Fatal("expected StartNewPlan without override to fail when an active plan exists")
	}
	if got := err.Error(); !strings.Contains(got, "already has active plan") || !strings.Contains(got, first.ID) || !strings.Contains(got, "override=true") {
		t.Fatalf("error = %q, want active-plan warning with override guidance", got)
	}

	active, ok, err := svc.GetActivePlan(sessionID)
	if err != nil {
		t.Fatalf("get active plan: %v", err)
	}
	if !ok {
		t.Fatal("expected active plan to remain set")
	}
	if active.ID != first.ID {
		t.Fatalf("active plan = %q, want original %q", active.ID, first.ID)
	}
}

func TestStartNewPlanOverrideCreatesReplacementActivePlan(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	first, _, err := svc.StartNewPlan(sessionID, "First Plan")
	if err != nil {
		t.Fatalf("create first plan: %v", err)
	}

	second, _, err := svc.StartNewPlan(sessionID, "Second Plan", StartNewPlanOptions{Override: true})
	if err != nil {
		t.Fatalf("create second plan with override: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("override created plan id %q, want a new plan id", second.ID)
	}

	active, ok, err := svc.GetActivePlan(sessionID)
	if err != nil {
		t.Fatalf("get active plan: %v", err)
	}
	if !ok {
		t.Fatal("expected active plan")
	}
	if active.ID != second.ID {
		t.Fatalf("active plan = %q, want replacement %q", active.ID, second.ID)
	}

	plans, activeID, err := svc.ListPlans(sessionID, 10)
	if err != nil {
		t.Fatalf("list plans: %v", err)
	}
	if activeID != second.ID {
		t.Fatalf("active id = %q, want %q", activeID, second.ID)
	}
	if len(plans) != 2 {
		t.Fatalf("plan count = %d, want 2", len(plans))
	}
}

func newPlanTestService(t *testing.T) (*Service, func()) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	sessions := pebblestore.NewSessionStore(store)
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		_ = store.Close()
		t.Fatalf("open event log: %v", err)
	}
	return NewService(sessions, events), func() { _ = store.Close() }
}

func createPlanTestSession(t *testing.T, svc *Service) string {
	t.Helper()
	snapshot, _, err := svc.CreateSessionWithOptions(CreateSessionOptions{
		SessionID:     "session-plan-test",
		Title:         "Plan Test",
		WorkspacePath: t.TempDir(),
		WorkspaceName: "workspace",
		Preference: &pebblestore.ModelPreference{
			Provider: "codex",
			Model:    "gpt-5.4",
			Thinking: "medium",
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return snapshot.ID
}
