package pebblestore

import (
	"errors"
	"reflect"
	"testing"
)

// Purpose: Get/SetAccountPermissions must isolate accounts, default closed for
// devices, open (auto) for a newly linked machine, and reject stale or invalid
// writes without changing durable state.
// A temporary Pebble store is the narrowest layer proving the CAS postcondition.
func TestAccountPermissionsDefaultsAndConflict(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.GetAccountPermissions("account-a")
	if err != nil {
		t.Fatal(err)
	}
	if p.Tasks != "auto" || p.Device("device").Files != "off" || p.Device("device").Browser != "off" || p.Link("linked").Work != "auto" {
		t.Fatalf("unsafe defaults: %+v", p)
	}
	p.Tasks = "approve"
	p.Devices["device"] = DevicePermissionRule{Files: "off", Browser: "allowed"}
	saved, err := s.SetAccountPermissions("account-a", 0, p)
	if err != nil || saved.Revision != 1 {
		t.Fatalf("save: %+v %v", saved, err)
	}
	conflict, err := s.SetAccountPermissions("account-a", 0, DefaultAccountPermissions())
	if !errors.Is(err, ErrAccountPermissionsConflict) || !reflect.DeepEqual(conflict, saved) {
		t.Fatalf("conflict: %+v %v", conflict, err)
	}
	invalid := DefaultAccountPermissions()
	invalid.Tasks = "free"
	if _, err := s.SetAccountPermissions("account-a", 1, invalid); err == nil {
		t.Fatal("invalid tasks accepted")
	}
	got, err := s.GetAccountPermissions("account-a")
	if err != nil || !reflect.DeepEqual(got, saved) {
		t.Fatalf("failed write mutated state: %+v %v", got, err)
	}
	other, err := s.GetAccountPermissions("account-b")
	if err != nil || !reflect.DeepEqual(other, DefaultAccountPermissions()) {
		t.Fatalf("account leak: %+v %v", other, err)
	}
}

// Purpose: the permissions live summary counts only pending, non-archived
// account tasks. Direct store fixtures isolate the count from task scheduling.
func TestAccountPermissionsWaitingTasks(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, task := range []ProjectTaskRecord{
		{AccountID: "a", Status: "pending_approval"},
		{AccountID: "a", Status: "pending_approval", Archived: true},
		{AccountID: "a", Status: "completed"},
		{AccountID: "b", Status: "pending_approval"},
	} {
		if err := s.PutJSON(KeyProjectTaskAccountPrefix+keyPart(task.AccountID)+"/project/"+string(rune('a'+i)), task); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.CountPermissionWaitingTasks("a"); err != nil || n != 1 {
		t.Fatalf("count: %d %v", n, err)
	}
}

// Purpose: the rule values are off|allowed for devices and off|ask|auto for
// linked machines. Writes accept the retired linked allowed as auto and refuse
// a device ask; records stored before the change read linked allowed as auto
// (stored ask keeps meaning ask) and a device ask as off, naming it in was_ask.
func TestAccountPermissionRuleValuesAndMigration(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := DefaultAccountPermissions()
	p.Linked["old"] = LinkedPermissionRule{Work: "allowed"}
	p.Linked["asks"] = LinkedPermissionRule{Work: "ask"}
	saved, err := s.SetAccountPermissions("a", 0, p)
	if err != nil || saved.Linked["old"].Work != "auto" || saved.Linked["asks"].Work != "ask" {
		t.Fatalf("linked alias: %+v %v", saved, err)
	}
	for _, rule := range []DevicePermissionRule{{Files: "ask", Browser: "off"}, {Files: "off", Browser: "ask"}} {
		bad := saved
		bad.Devices = map[string]DevicePermissionRule{"d": rule}
		if _, err := s.SetAccountPermissions("a", saved.Revision, bad); !errors.Is(err, ErrDeviceAskRetired) {
			t.Fatalf("device ask accepted: %v", err)
		}
	}
	for _, bad := range []string{"allowed-ish", "free", ""} {
		q := saved
		q.Linked = map[string]LinkedPermissionRule{"box": {Work: bad}}
		if _, err := s.SetAccountPermissions("a", saved.Revision, q); err == nil {
			t.Fatalf("linked %q accepted", bad)
		}
	}
	if err := s.PutJSON("account_permissions/b", map[string]any{"tasks": "auto", "devices": map[string]any{"d": map[string]any{"files": "ask", "browser": "allowed"}}, "linked": map[string]any{"x": map[string]any{"work": "allowed"}, "y": map[string]any{"work": "ask"}}}); err != nil {
		t.Fatal(err)
	}
	old, err := s.GetAccountPermissions("b")
	if err != nil {
		t.Fatal(err)
	}
	d := old.Device("d")
	if d.Files != "off" || d.Browser != "allowed" || !reflect.DeepEqual(d.WasAsk, []string{"files"}) || old.Link("x").Work != "auto" || old.Link("y").Work != "ask" || old.Link("new").Work != "auto" {
		t.Fatalf("migration: %+v", old)
	}
	// A migrated record is valid and writes back without the retired values.
	if _, err := s.SetAccountPermissions("b", old.Revision, old); err != nil {
		t.Fatalf("migrated record refused: %v", err)
	}
	if !LinkedWorkOn("ask") || !LinkedWorkOn("auto") || !LinkedWorkOn("allowed") || LinkedWorkOn("off") {
		t.Fatal("LinkedWorkOn")
	}
}
