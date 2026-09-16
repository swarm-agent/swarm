package session

import (
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: approved reusable instructions must satisfy the executable contract
// without activating a plan or creating a run. PreparePlanSaveWithMetadata and
// its canonical commit are the narrowest real-store boundary for this invariant.
func TestInactiveApprovedInstructionsValidateWithoutActivation(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()
	id := "instruction-session"
	_, err := svc.ApplySessionMutation(SessionMutationInput{SessionID: id, UserID: "user", AccountScopeID: "account", Kind: SessionMutationCreateSession, Session: &store.SessionSnapshot{ID: id, UserID: "user", AccountScopeID: "account"}, IdempotencyKey: "create", PayloadHash: "create"})
	if err != nil {
		t.Fatal(err)
	}
	invalid := &store.SessionPlanDocument{Title: "Instructions"}
	if _, err := svc.PreparePlanSaveWithMetadata(id, "instructions", "Instructions", "", "approved", "approved", false, PlanSaveMetadata{Document: invalid}); err == nil {
		t.Fatal("incomplete executable instructions accepted")
	}
	if _, found, err := svc.GetPlan(id, "instructions"); err != nil || found {
		t.Fatalf("rejected instructions persisted: found=%v err=%v", found, err)
	}
	doc := &store.SessionPlanDocument{Title: "Instructions", Status: "draft", Info: store.SessionPlanInfo{Goal: "Perform the scheduled check"}, Checkpoints: []store.SessionPlanCheckpoint{{ID: "cp-1", Title: "Check", Status: "pending", Order: 1, Tasks: []string{"Inspect current state"}, AcceptanceCriteria: []string{"State is checked"}}}}
	prepared, err := svc.PreparePlanSaveWithMetadata(id, "instructions", doc.Title, "", "approved", "approved", false, PlanSaveMetadata{Document: doc})
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.CommitPreparedPlanSave(prepared, svc.ApplySessionMutation)
	if err != nil {
		t.Fatal(err)
	}
	if result.Plan == nil || result.Plan.Active || result.Plan.Document.Status != "approved" {
		t.Fatalf("incorrect saved instructions: %#v", result.Plan)
	}
	if _, found, err := svc.GetActivePlan(id); err != nil || found {
		t.Fatalf("instruction save activated plan: found=%v err=%v", found, err)
	}
}
