package pebblestore

import (
	"errors"
	"reflect"
	"testing"
)

// Purpose: Get/SetAccountPermissions must isolate accounts, default closed for
// devices, and reject stale or invalid writes without changing durable state.
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
	if p.Tasks != "auto" || p.Device("device").Files != "off" || p.Device("device").Browser != "off" || p.Link("linked").Work != "ask" {
		t.Fatalf("unsafe defaults: %+v", p)
	}
	p.Tasks = "approve"
	p.Devices["device"] = DevicePermissionRule{Files: "ask", Browser: "allowed"}
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
