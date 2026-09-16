package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Purpose: ValidateOwnedExecutionIdentity must admit the owned branch while Git
// detaches HEAD during either rebase backend, but ValidateOwnedIdentity must not
// admit that state for transitions, even with an ancestor base. Real Git conflict
// fixtures prove per-worktree metadata resolution and byte-for-byte preservation
// of files, index, refs and rebase state; malformed/foreign evidence fails closed.
func TestOwnedExecutionIdentityInterruptedRebase(t *testing.T) {
	for _, backend := range []string{"merge", "apply"} {
		t.Run(backend, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			git := func(path string, wantFailure bool, args ...string) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				out, err := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...).CombinedOutput()
				if (err != nil) != wantFailure || ctx.Err() != nil {
					t.Fatalf("git %v: %v %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			write := func(path, value string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			source, lane := filepath.Join(root, "source"), filepath.Join(root, "lane")
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			git(source, false, "init", "-b", "dev")
			git(source, false, "config", "user.name", "Fixture")
			git(source, false, "config", "user.email", "fixture@example.invalid")
			write(filepath.Join(source, "conflict.txt"), "initial\n")
			git(source, false, "add", "conflict.txt")
			git(source, false, "commit", "-m", "initial")
			base := git(source, false, "rev-parse", "HEAD")
			git(source, false, "worktree", "add", "-b", "agent/owned", lane)
			write(filepath.Join(lane, "conflict.txt"), "owned change\n")
			git(lane, false, "commit", "-am", "owned change")
			write(filepath.Join(source, "conflict.txt"), "upstream change\n")
			git(source, false, "commit", "-am", "upstream change")
			git(lane, true, "rebase", "--"+backend, "dev")
			if git(lane, false, "rev-parse", "--abbrev-ref", "HEAD") != "HEAD" || git(lane, false, "ls-files", "--unmerged") == "" {
				t.Fatal("fixture did not pause detached with unresolved conflicts")
			}
			state := git(lane, false, "rev-parse", "--git-path", "rebase-"+backend)
			if !filepath.IsAbs(state) {
				state = filepath.Join(lane, state)
			}
			check := func(sourcePath, lanePath, branch, recordedBase string, wantOK bool) {
				t.Helper()
				before := snapshotIdentityTree(t, root)
				err := ValidateOwnedExecutionIdentity(sourcePath, lanePath, branch, recordedBase)
				if (err == nil) != wantOK {
					t.Fatalf("execution wantOK=%v: %v", wantOK, err)
				}
				if err := ValidateOwnedIdentity(sourcePath, lanePath, branch, recordedBase); err == nil {
					t.Fatal("strict transition admitted interrupted rebase or invalid identity")
				}
				if !reflect.DeepEqual(before, snapshotIdentityTree(t, root)) {
					t.Fatal("validation modified Git, conflict files or administrative state")
				}
			}
			check(source, lane, "agent/owned", base, true)
			for _, branch := range []string{"", "wrong", "HEAD", "agent/owned\n", "../owned"} {
				check(source, lane, branch, base, false)
			}
			for _, invalidBase := range []string{"", strings.Repeat("f", 40)} {
				check(source, lane, "agent/owned", invalidBase, false)
			}
			check(lane, lane, "agent/owned", base, false)
			if err := os.Mkdir(filepath.Join(lane, "sub"), 0700); err != nil {
				t.Fatal(err)
			}
			check(source, filepath.Join(lane, "sub"), "agent/owned", base, false)
			check(source, filepath.Join(root, "missing"), "agent/owned", base, false)
			foreign := filepath.Join(root, "foreign")
			if err := os.Mkdir(foreign, 0700); err != nil {
				t.Fatal(err)
			}
			git(foreign, false, "init", "-b", "dev")
			git(foreign, false, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "foreign")
			check(foreign, lane, "agent/owned", base, false)
			// A sibling with detached HEAD cannot borrow this lane's valid state.
			sibling := filepath.Join(root, "sibling")
			git(source, false, "worktree", "add", "--detach", sibling, base)
			check(source, sibling, "agent/owned", base, false)

			for _, test := range []struct{ file, value string }{
				{"head-name", "refs/heads/wrong\n"},
				{"head-name", "detached HEAD\n"},
				{"head-name", "refs/heads/agent/owned\nextra\n"},
				{"head-name", strings.Repeat("x", 4097)},
				{"orig-head", base + "\n"},
				{"orig-head", "HEAD\n"},
				{"onto", strings.Repeat("f", 40) + "\n"},
				{"onto", git(lane, false, "rev-parse", "refs/heads/agent/owned") + "\n"},
			} {
				path := filepath.Join(state, test.file)
				original, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				write(path, test.value)
				check(source, lane, "agent/owned", base, false)
				write(path, string(original))
			}
			// Missing files and symlinked metadata cannot supply recovery identity.
			for _, path := range []string{filepath.Join(state, "head-name"), filepath.Join(state, "orig-head"), state} {
				saved := path + ".saved"
				if err := os.Rename(path, saved); err != nil {
					t.Fatal(err)
				}
				check(source, lane, "agent/owned", base, false)
				if err := os.Symlink(saved, path); err != nil {
					t.Fatal(err)
				}
				check(source, lane, "agent/owned", base, false)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(saved, path); err != nil {
					t.Fatal(err)
				}
			}
			other := "rebase-apply"
			if backend == "apply" {
				other = "rebase-merge"
			}
			if err := os.Mkdir(filepath.Join(filepath.Dir(state), other), 0700); err != nil {
				t.Fatal(err)
			}
			check(source, lane, "agent/owned", base, false)
		})
	}
}

// Capture all fixture bytes and modes, including the worktree-private index,
// rebase files, shared refs and object database. No Git status refresh is needed.
func snapshotIdentityTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		value := ""
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value = string(data)
		} else if info.Mode()&os.ModeSymlink != 0 {
			value, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		files[rel] = fmt.Sprintf("%s:%s", info.Mode(), value)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
