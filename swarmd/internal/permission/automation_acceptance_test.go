package permission

import (
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the permission service must reject foreign conversion before invoking
// the atomic store callback, preserving unrelated pending permissions. A real
// permission/session fixture proves the service ownership gate, not HTTP status.
func TestAutomationAcceptanceForeignOwnerPreservesPermission(t *testing.T) {
	svc, id, _, cleanup := newPermissionLifecycleTestService(t, "")
	defer cleanup()
	record, err := svc.CreatePending(CreateInput{SessionID: id, RunID: "run", CallID: "call", ToolName: "bash", ToolArguments: `{"command":"echo test"}`})
	if err != nil { t.Fatal(err) }
	called := false
	_, err = svc.CoordinateAutomationAcceptance(store.AutomationApproval{SubjectID: "foreign", Scope: store.AutomationScope{AccountID: "foreign"}}, store.V3SessionMutationInput{SessionID: id, AutomationProposal: &store.AutomationPlanReference{PlanID: "proposal"}}, func(g store.AutomationApproval, in store.V3SessionMutationInput) (store.AutomationApproval, error) { called = true; return g, nil })
	if err == nil || called { t.Fatalf("foreign conversion reached commit: called=%v err=%v", called, err) }
	got, found, err := svc.store.GetPermission(id, record.ID)
	if err != nil || !found || got.Status != store.PermissionStatusPending { t.Fatalf("permission changed: %+v %v", got, err) }
}
