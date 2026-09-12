package api

import (
	"testing"

	"swarm/packages/swarmd/internal/automation"
)

// Purpose: startup configuration must remain safe when permission authority is
// unavailable. ConfigureAutomationApproval must not panic or manufacture a
// substitute resolver; PolicyApproval itself fails typed conversion closed.
func TestConfigureAutomationApprovalWithoutPermissionAuthority(t *testing.T) {
	s := &Server{}
	approval := &automation.PolicyApproval{}
	s.ConfigureAutomationApproval(approval)
	if s.automations == nil || s.automations.approval != approval {
		t.Fatal("approval authority was replaced")
	}
	s.ConfigureAutomationApproval(nil)
	if s.automations.approval != nil {
		t.Fatal("nil approval authority was not preserved")
	}
}
