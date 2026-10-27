package permission

import (
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: resolveForDeviceLocked must separate person approval from device
// completion. A temporary store exercises the real mutation and verifies that
// premature and wrong-device results do not change pending state.
func TestDeviceResolutionPhases(t *testing.T) {
	s, _ := openSubagentReservationTestServices(t)
	p, err := s.CreatePending(CreateInput{SessionID: "session", RunID: "run", CallID: "call", ToolName: "machine_read", ToolArguments: `{"path":"/srv/example"}`, Requirement: RequirementClientTool, Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	previous := p
	p.TargetDevice = "device-one"
	p.Execution = "awaiting_approval"
	if err := s.store.PutPermission(p, &previous); err != nil {
		t.Fatal(err)
	}
	result := `{"result":"contents"}`
	for _, id := range []string{"", "device-one", "device-two"} {
		if _, _, err := s.ResolveFromClient("session", p.ID, "allow", "", result, id); err == nil {
			t.Fatalf("premature result from %q accepted", id)
		}
		got, _, err := s.store.GetPermission("session", p.ID)
		if err != nil || got.Execution != "awaiting_approval" || got.Status != pebblestore.PermissionStatusPending {
			t.Fatalf("rejection changed state: %+v %v", got, err)
		}
	}
	approved, _, err := s.ResolveFromClient("session", p.ID, "allow", "", "", "")
	if err != nil || approved.Execution != "ready" || approved.Status != pebblestore.PermissionStatusPending {
		t.Fatalf("approval: %+v %v", approved, err)
	}
	for _, id := range []string{"", "device-two"} {
		if _, _, err := s.ResolveFromClient("session", p.ID, "allow", "", result, id); err == nil {
			t.Fatalf("wrong device %q accepted", id)
		}
	}
	completed, _, err := s.ResolveFromClient("session", p.ID, "allow", "", result, "device-one")
	if err != nil || completed.Status != pebblestore.PermissionStatusApproved {
		t.Fatalf("completion: %+v %v", completed, err)
	}
}
