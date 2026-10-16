package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: machineWorkspaceGrant must configure shared Git creation and repair
// existing object directories, preventing owner-created 0755 fanout directories
// from blocking service fetches. Real Git and modes exercise the admin boundary;
// command shims replace only privileged ACL/user switching in this unit test.
func TestWorkspaceSharedGitGrant(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"setfacl": "#!/bin/sh\nexit 0\n",
		"runuser": "#!/bin/sh\n[ \"$1\" = -u ] && [ \"$2\" = fixture-owner ] || exit 91\nshift 3\nexec \"$@\"\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	repo := filepath.Join(root, "repo")
	if out, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("init: %s %v", out, err)
	}
	objects := filepath.Join(repo, ".git", "objects", "ab")
	if err := os.Mkdir(objects, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(objects, 0755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, ".git", "objects", "linked")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := machineWorkspaceGrant(repo, "fixture-owner"); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("git", "-C", repo, "config", "--get", "core.sharedRepository").CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "group" {
			t.Fatalf("shared config=%q err=%v", out, err)
		}
		info, err := os.Stat(objects)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0020 == 0 {
			t.Fatalf("object directory not group writable: %v", info.Mode())
		}
		info, err = os.Stat(outside)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0755 {
			t.Fatalf("followed object symlink: %v", info.Mode())
		}
	}
}
