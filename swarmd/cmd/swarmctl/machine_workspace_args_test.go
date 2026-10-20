package main

import (
	"os/user"
	"reflect"
	"testing"
)

// Purpose: `swarmctl workspace add` takes several folders (one apply and one
// restart for all of them) with an optional owner in any position, and refuses
// anything it does not understand instead of guessing.
func TestMachineWorkspaceAddArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		adds []string
		who  string
	}{
		{[]string{"/srv/a"}, []string{"/srv/a"}, ""},
		{[]string{"/srv/a", "/srv/b", "--for-user", "roy"}, []string{"/srv/a", "/srv/b"}, "roy"},
		{[]string{"--for-user", "roy", "/srv/a", "/srv/a"}, []string{"/srv/a"}, "roy"},
	} {
		adds, who, err := machineWorkspaceAddArgs(tc.args)
		if err != nil || !reflect.DeepEqual(adds, tc.adds) || who != tc.who {
			t.Fatalf("%v: %v %q %v", tc.args, adds, who, err)
		}
	}
	for _, bad := range [][]string{
		nil,
		{"--for-user", "roy"},
		{"/srv/a", "--for-user"},
		{"/srv/a", "--for-user", "a", "--for-user", "b"},
		{"/srv/a", "--force"},
	} {
		if _, _, err := machineWorkspaceAddArgs(bad); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
}

// Purpose: a folder added through pkexec belongs to the person who typed their
// password, as with sudo; sudo wins when both are set.
func TestMachineInvokingUser(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	t.Setenv("SUDO_USER", "")
	t.Setenv("PKEXEC_UID", me.Uid)
	if got := machineInvokingUser(); got != me.Username {
		t.Fatalf("pkexec: %q, want %q", got, me.Username)
	}
	t.Setenv("SUDO_USER", "someone")
	if got := machineInvokingUser(); got != "someone" {
		t.Fatalf("sudo: %q", got)
	}
	t.Setenv("SUDO_USER", "")
	t.Setenv("PKEXEC_UID", "not-a-number")
	if got := machineInvokingUser(); got != "" {
		t.Fatalf("bad uid: %q", got)
	}
}
