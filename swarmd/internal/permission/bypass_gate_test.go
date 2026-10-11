package permission

import (
	"testing"
)

// Purpose: "no sandbox, no autonomy". Permission bypass may take effect only
// while the agent sandbox is active. Service.SetBypassGate is the boundary the
// daemon installs; AuthorizeToolCall, BypassPermissions and
// CurrentPermissionStateForAccount must all read bypass as off while the gate
// is closed, without discarding the owner's stored choice, and honour it again
// once the gate opens. Unit level over a real permission store: the decision
// lives entirely in the permission service.
func TestBypassTakesEffectOnlyWhileGateIsOpen(t *testing.T) {
	s, _ := openSubagentReservationTestServices(t)
	s.SetBypassPermissions(true)
	open := false
	s.SetBypassGate(func() (bool, string) { return open, "the agent sandbox is not active: docker is not reachable" })

	authorize := func(call string) AuthorizationResult {
		t.Helper()
		result, err := s.AuthorizeToolCall(AuthorizationInput{
			SessionID: "session-gate", AccountScopeID: "account", RunID: "run", CallID: call,
			ToolName: "bash", ToolArguments: `{"command":"echo hi"}`, Mode: "auto",
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	if s.BypassPermissions() || s.BypassBlocked() == "" {
		t.Fatal("bypass reported on while the gate is closed")
	}
	state, err := s.CurrentPermissionStateForAccount("account")
	if err != nil || state.BypassPermissions {
		t.Fatalf("account state reports bypass while gate closed: %+v %v", state, err)
	}
	if result := authorize("closed"); result.Source == "bypass_permissions" || result.Decision == AuthorizationApprove {
		t.Fatalf("bash approved by bypass without a sandbox: %+v", result)
	}

	open = true
	if !s.BypassPermissions() || s.BypassBlocked() != "" {
		t.Fatal("stored bypass choice lost once the gate opened")
	}
	if result := authorize("open"); result.Source != "bypass_permissions" || result.Decision != AuthorizationApprove {
		t.Fatalf("bypass not honoured with an active sandbox: %+v", result)
	}
}
