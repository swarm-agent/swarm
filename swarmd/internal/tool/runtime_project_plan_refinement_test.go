package tool

import (
	"context"
	"errors"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: executeManageProjects must send Orchestrator-authored replacements
// and exact review guards to the canonical lifecycle, never deploy a Plan run.
// This adapter test proves argument plumbing and failure propagation; API/store
// tests prove durable rejection and atomic publication rather than trusting mocks.
func TestManageProjectsStructuredRefinement(t *testing.T) {
	rt := NewRuntime(1)
	store := newMockProjectStore()
	lifecycle := newMockProjectTaskLifecycleService(store)
	rt.SetManageProjectStore(store)
	rt.SetProjectTaskLifecycleService(lifecycle)
	p := identity.Principal{Type: "user", UserID: "user", AccountScopeID: "account"}
	scope := WorkspaceScope{Principal: p}
	task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", SessionID: "session", Status: "pending_approval", PlanBinding: &pebblestore.ProjectTaskPlanBinding{SessionID: "session", PlanID: "plan", DefinitionRevision: 2}}
	if err := store.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{"id": "plan", "title": "Updated plan", "info": map[string]any{"goal": "Change"}, "checkpoints": []any{map[string]any{"id": "cp-1", "title": "Change", "tasks": []string{"Implement"}, "acceptance_criteria": []string{"Works"}}}}
	args := map[string]any{"action": "refine_task", "project_id": "project", "task_id": "task", "feedback": "Change details", "plan_document": doc, "session_id": "session", "plan_id": "plan", "definition_revision": 2}
	invoke := func(input map[string]any) error {
		_, err := rt.executeManageProjects(context.Background(), scope, input)
		return err
	}
	for _, key := range []string{"session_id", "plan_id", "definition_revision", "feedback"} {
		bad := map[string]any{}
		for k, v := range args {
			bad[k] = v
		}
		delete(bad, key)
		if err := invoke(bad); err == nil {
			t.Fatalf("accepted missing %s", key)
		}
	}
	if len(lifecycle.submittedPlans) != 0 {
		t.Fatal("invalid inputs reached lifecycle")
	}
	lifecycle.failSubmit = errors.New("stale definition")
	if err := invoke(args); err == nil {
		t.Fatal("lifecycle rejection swallowed")
	}
	if task.Status != "pending_approval" || task.PlanBinding.DefinitionRevision != 2 {
		t.Fatal("failed call changed card")
	}
	lifecycle.failSubmit = nil
	if err := invoke(args); err != nil {
		t.Fatal(err)
	}
	got := lifecycle.submittedPlans[len(lifecycle.submittedPlans)-1]
	if got.AccountScopeID != p.AccountScopeID || got.UserID != p.UserID || got.SessionID != "session" || got.ExpectedPlanID != "plan" || got.ExpectedDefinitionRevision != 2 || got.Document.Title != "Updated plan" || got.Feedback != "Change details" {
		t.Fatalf("submission lost guards: %+v", got)
	}
}
