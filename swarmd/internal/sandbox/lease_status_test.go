package sandbox

import "testing"

// Purpose: LeaseStatus must expose the actual lease manager runtime and both
// user selections, not the machine-wide sandbox setting. The existing fake
// command runner isolates runtime discovery without requiring Docker.
func TestLeaseRuntimeStatus(t *testing.T) {
	runner := readyRunner()
	runner.out["info --format"] = `{"runsc":{},"runc":{}}`
	m := newTestManager(t, ModeRequired, runner, Config{Engine: "fixture-engine", StateDir: t.TempDir()})
	root := t.TempDir()
	view := m.LeaseStatus(root)
	if view["runtime"] != "runsc" || view["engine"] != "fixture-engine" || view["available"] != true || view["lease_user"] == "" || view["owner_user"] == "" {
		t.Fatalf("lease status: %#v", view)
	}
	// Fixed ownership proves daemon and distinct-owner selection on every host.
	m.cfg.UID, m.cfg.GID = 997, 988
	for _, tc := range []struct {
		uid  int
		want string
	}{{0, "997:988"}, {997, "997:988"}, {1000, "1000:988"}} {
		m.owner = func(string) (int, bool) { return tc.uid, true }
		got := m.LeaseStatus(root)
		if got["owner_user"] != tc.want || got["lease_user"] != "997:988" {
			t.Fatalf("owner %d: %#v", tc.uid, got)
		}
	}
	var absent *Manager
	if absent.LeaseStatus(root)["available"] != false {
		t.Fatal("missing backend reported available")
	}
}
