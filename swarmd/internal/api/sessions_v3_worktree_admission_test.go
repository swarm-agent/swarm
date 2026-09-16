package api

import (
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the create-session consumer must not mint trusted allocation evidence
// from caller-selected paths. The narrow resolver/helper layer rejects before
// invoking a worktree service and leaves no allocated or published state.
func TestSessionsV3AdmissionRejectsExistingAndInvalidLanes(t *testing.T) {
	s := &Server{}
	if _, err := s.resolveSessionsV3CreateWorktree(identity.Principal{}, "", "session", nil, "", "branch", "existing"); err == nil {
		t.Fatal("existing lane admitted as allocation")
	}
	if evidence, err := sessionsV3AllocatedLaneAdmission(pebblestore.SessionSnapshot{WorktreeEnabled: true}); err == nil || evidence != nil {
		t.Fatalf("invalid lane admitted: %+v, %v", evidence, err)
	}
	if evidence, err := sessionsV3AllocatedLaneAdmission(pebblestore.SessionSnapshot{}); err != nil || evidence != nil {
		t.Fatalf("unmanaged session received evidence: %+v, %v", evidence, err)
	}
}
