package pebblestore

import "testing"

// Purpose: last_used writes must preserve account rule values and other accounts,
// and make stale PATCH revisions conflict. A real store is the narrowest layer
// proving serialized durable usage updates for all four rule kinds.
func TestAccountPermissionUsage(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := DefaultAccountPermissions()
	p.Tasks = "auto"
	p.Devices["d"] = DevicePermissionRule{Files: "allowed", Browser: "ask"}
	p.Linked["box"] = LinkedPermissionRule{Work: "ask"}
	if _, err := s.SetAccountPermissions("a", 0, p); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"files", "browser", "linked", "tasks"} {
		target := "d"
		if kind == "linked" {
			target = "box"
		}
		if err := s.RecordPermissionUse("a", kind, target, 100); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetAccountPermissions("a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Device("d").LastUsed != 100 || got.Link("box").LastUsed != 100 || got.TasksLastUsed != 100 || got.Device("d").Browser != "ask" || got.Tasks != "auto" {
		t.Fatalf("usage: %+v", got)
	}
	if _, err := s.SetAccountPermissions("a", 1, p); err != ErrAccountPermissionsConflict {
		t.Fatalf("stale update: %v", err)
	}
	other, err := s.GetAccountPermissions("b")
	if err != nil || other.TasksLastUsed != 0 || len(other.Devices) != 0 {
		t.Fatalf("account leak: %+v %v", other, err)
	}
}
