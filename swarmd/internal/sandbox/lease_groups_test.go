package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// Purpose: runArgsImage must grant only daemon leases the real Git directory
// group, never follow a symlink or affect owner containers. Testing actual run
// arguments and the acquire spec boundary is the narrowest hermetic protection
// against gVisor's ACL omission and stale-container reuse.
func TestLeaseGitGroupArgs(t *testing.T) {
	for _, kind := range []string{"directory", "gitfile", "symlink", "missing"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			common := filepath.Join(root, "common")
			if err := os.Mkdir(common, 0755); err != nil {
				t.Fatal(err)
			}
			st, err := os.Lstat(common)
			if err != nil {
				t.Fatal(err)
			}
			gid := int(st.Sys().(*syscall.Stat_t).Gid)
			dot := filepath.Join(root, ".git")
			switch kind {
			case "directory":
				err = os.Mkdir(dot, 0755)
			case "symlink":
				err = os.Symlink(common, dot)
			case "gitfile":
				linked := filepath.Join(root, "linked")
				if err := os.Mkdir(linked, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(linked, "commondir"), []byte("../common\n"), 0644); err != nil {
					t.Fatal(err)
				}
				err = os.WriteFile(dot, []byte("gitdir: linked\n"), 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			m := newTestManager(t, ModeRequired, readyRunner(), Config{UID: 997, GID: gid + 1, StateDir: t.TempDir()})
			name := leaseContainerName(root, "fixture")
			table := m.leaseTable()
			lease := &Lease{Container: name, asDaemon: true}
			table.byID["fixture"] = lease
			check := func(want bool) {
				t.Helper()
				args := m.RunArgs(name, root, "spec", nil, SecretSetup{})
				count := 0
				for i, arg := range args {
					if arg == "--group-add" {
						count++
						if i+1 >= len(args) || args[i+1] != strconv.Itoa(gid) {
							t.Fatalf("wrong group: %v", args)
						}
					}
				}
				expected := 0
				if want {
					expected = 1
				}
				if count != expected {
					t.Fatalf("group count %d want %d", count, expected)
				}
			}
			want := kind == "directory" || kind == "gitfile"
			check(want)
			withGroup := m.leaseSpec(name, root, "base")
			m.cfg.GID = gid
			check(false)
			if want && withGroup == m.leaseSpec(name, root, "base") {
				t.Fatal("changed arguments retained old spec")
			}
			m.cfg.GID = gid + 1
			lease.asDaemon = false
			check(false)
		})
	}
}

// Purpose: acquire must invalidate even a recently verified lease when its Git
// group arguments change. The fake engine observes ensure's removal and new
// run, without requiring Docker or altering host ownership.
func TestLeaseGitGroupRecreates(t *testing.T) {
	m, runner, root := newLeaseTestManager(t, 1, nil)
	table := m.leaseTable()
	name := leaseContainerName(root, "fixture")
	table.byID["fixture"] = &Lease{Container: name, asDaemon: true}
	scope := Scope{Root: root, Lease: "fixture"}
	ctx := context.Background()
	b, err := m.acquire(ctx, scope, "test")
	if err != nil {
		t.Fatal(err)
	}
	m.release(b)
	old := b.spec
	n := runner.count()
	b, err = m.acquire(ctx, scope, "test")
	if err != nil {
		t.Fatal(err)
	}
	m.release(b)
	if runner.ranSince(n, "rm -f "+name) {
		t.Fatal("unchanged lease recreated")
	}
	dot := filepath.Join(root, ".git")
	if err := os.Mkdir(dot, 0755); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(dot)
	if err != nil {
		t.Fatal(err)
	}
	m.cfg.GID = int(st.Sys().(*syscall.Stat_t).Gid) + 1
	runner.out["container inspect"] = old + " true first"
	n = runner.count()
	b, err = m.acquire(ctx, scope, "test")
	if err != nil {
		t.Fatal(err)
	}
	m.release(b)
	if b.spec == old || !runner.ranSince(n, "rm -f "+name) {
		t.Fatal("stale lease was not replaced")
	}
	found := false
	for _, args := range runner.calls[n:] {
		if len(args) > 0 && args[0] == "run" && strings.Contains(strings.Join(args, " "), "--group-add "+strconv.Itoa(int(st.Sys().(*syscall.Stat_t).Gid))) {
			found = true
		}
	}
	if !found {
		t.Fatal("replacement did not receive Git group")
	}
}
