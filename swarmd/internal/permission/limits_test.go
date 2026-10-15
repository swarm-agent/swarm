package permission

import (
	"path/filepath"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: refreshPermissionStatePolicyLocked must reflect box limits in an
// existing permission service; prevents cached prompts/admission using old caps.
func TestLimitsLiveDelegationPolicy(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := NewService(store.NewPermissionStore(db), nil, nil)
	before, err := svc.CurrentPolicyForAccount("a")
	if err != nil {
		t.Fatal(err)
	}
	l := store.DefaultLimits()
	l.ActiveChildren = 2
	l.SwarmChildren = 3
	l.DelegationWaves = 0
	if _, err = db.SetLimits("a", 0, l); err != nil {
		t.Fatal(err)
	}
	after, err := svc.CurrentPolicyForAccount("a")
	if err != nil {
		t.Fatal(err)
	}
	if after.Subagents.ActiveChildLimit != 2 || after.Subagents.SwarmActiveChildLimit != 3 || after.Subagents.AutomaticLaunchesPerParentRun != 0 || after.Subagents.Mode != before.Subagents.Mode {
		t.Fatalf("live policy %+v", after.Subagents)
	}
}
