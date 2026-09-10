package tool

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Purpose: the allocation/materialization boundary must deliver retained bytes
// and Git executable intent only into a new scoped lane. A stale source, scope
// escape, or failure after writes must publish no allocation and preserve the
// original dirty files/index/HEAD. Real Git allocation is the narrowest layer
// proving cleanup rather than merely checking a returned error.
func TestRecoverySourceAllocation(t *testing.T) {
	for _, failure := range []string{"", "scope", "changed", "after-write", "symlink"} {
		t.Run(failure, func(t *testing.T) {
			f, req := recoverySourceFixture(t)
			if err := os.Chmod(filepath.Join(f.dirtyPath, "change.txt"), 0755); err != nil {
				t.Fatal(err)
			}
			source, err := f.runtime.InspectRecoverySource(f.scope, req)
			if err != nil {
				t.Fatal(err)
			}
			req.ExpectedDigest = source.Digest()
			if _, err := f.runtime.RetainRecoverySource(f.scope, req); err != nil {
				t.Fatal(err)
			}
			wt := &worktreeruntime.Service{}
			base, err := wt.ResolveTaskBase(f.lane)
			if err != nil {
				t.Fatal(err)
			}
			before := recoveryGit(t, f.dirtyPath, "status", "--porcelain")
			index := recoveryGit(t, f.dirtyPath, "ls-files", "--stage")
			if err := f.runtime.ValidateRecoverySourceTarget(f.scope, source, f.lane); err != nil {
				t.Fatal(err)
			}
			if err := f.runtime.ValidateRecoverySourceTarget(f.scope, source, f.primary); err == nil {
				t.Fatal("cross-repository accepted")
			}
			scopes := []string{"change.txt"}
			if failure == "scope" {
				scopes = []string{"other.txt"}
			}
			var destination string
			allocated, err := wt.AllocateTaskWorkspaceWithSource(f.lane, base, "replacement", scopes, func(dir string) error {
				destination = dir
				if failure == "changed" {
					if err := os.WriteFile(filepath.Join(f.dirtyPath, "change.txt"), []byte("changed"), 0755); err != nil {
						return err
					}
				}
				selected, err := f.runtime.ReadRecoverySource(f.scope, req.ExpectedDigest)
				if err != nil {
					return err
				}
				if failure == "symlink" {
					if err := os.Symlink(filepath.Join(f.dirtyPath, "change.txt"), filepath.Join(dir, "change.txt")); err != nil {
						return err
					}
				}
				if err := MaterializeRecoverySource(dir, selected, scopes); err != nil {
					return err
				}
				if failure == "after-write" {
					return errors.New("injected prepublication failure")
				}
				return nil
			})
			if failure != "" {
				if err == nil || allocated.WorkspacePath != "" {
					t.Fatal("failed allocation published")
				}
				if _, err := os.Stat(destination); !os.IsNotExist(err) {
					t.Fatalf("partial destination remains: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				body, err := os.ReadFile(filepath.Join(allocated.WorkspacePath, "change.txt"))
				if err != nil || string(body) != "recovery-dirty" {
					t.Fatalf("bytes: %q %v", body, err)
				}
				info, err := os.Stat(filepath.Join(allocated.WorkspacePath, "change.txt"))
				if err != nil || info.Mode().Perm() != 0755 {
					t.Fatalf("mode: %v %v", info, err)
				}
			}
			want := "recovery-dirty"
			if failure == "changed" {
				want = "changed"
			}
			body, _ := os.ReadFile(filepath.Join(f.dirtyPath, "change.txt"))
			if string(body) != want {
				t.Fatal("original overwritten")
			}
			if recoveryGit(t, f.dirtyPath, "rev-parse", "HEAD") != f.base || recoveryGit(t, f.dirtyPath, "ls-files", "--stage") != index || recoveryGit(t, f.dirtyPath, "status", "--porcelain") != before {
				t.Fatal("original Git changed")
			}
		})
	}
}

// Purpose: full preflight must reject a later invalid path without touching an
// earlier valid destination. Also prove deletion and documentation bytes are
// materialized exactly; these are source edits, not commits.
func TestRecoverySourceMaterializePreflight(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "first"), []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	source := RecoverySource{Files: []RecoverySourceFile{{Path: "first", Content: "new"}, {Path: "../escape", Content: "bad"}}}
	if err := MaterializeRecoverySource(dir, source, []string{"first"}); err == nil {
		t.Fatal("escape accepted")
	}
	body, _ := os.ReadFile(filepath.Join(dir, "first"))
	if string(body) != "original" {
		t.Fatal("preflight mutated destination")
	}
	source.Files = []RecoverySourceFile{{Path: "first", Deleted: true}, {Path: "docs/note.md", Content: "draft\n"}}
	if err := MaterializeRecoverySource(dir, source, []string{"first", "docs"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "first")); !os.IsNotExist(err) {
		t.Fatal("deletion lost")
	}
	body, _ = os.ReadFile(filepath.Join(dir, "docs/note.md"))
	if string(body) != "draft\n" {
		t.Fatal("documentation bytes lost")
	}
}

// Purpose: explicit ignored documentation recovery must preserve bytes without
// staging or broad force-add. Exercise tool selection/retention plus fresh Git
// materialization and assert both original and replacement indexes stay empty.
func TestRecoverySourceIgnoredDocumentation(t *testing.T) {
	f, req := recoverySourceFixture(t)
	for _, dir := range []string{f.dirtyPath} {
		if err := os.MkdirAll(filepath.Join(dir, "docs"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("docs/\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "docs/note.md"), []byte("exact draft\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	args := map[string]any{"action": "inspect_source", "task_call_id": req.TaskCallID, "child_session_id": req.ChildSessionID, "paths": []string{"docs/note.md"}}
	out, err := f.runtime.manageWorktreeRecoverySource(f.scope, args)
	if err != nil {
		t.Fatal(err)
	}
	var inspected map[string]any
	if err := json.Unmarshal([]byte(out), &inspected); err != nil {
		t.Fatal(err)
	}
	args["action"] = "retain_source"
	args["expected_digest"] = inspected["recovery_source_digest"]
	if _, err := f.runtime.manageWorktreeRecoverySource(f.scope, args); err != nil {
		t.Fatal(err)
	}
	selected, err := f.runtime.ReadRecoverySource(f.scope, asString(args["expected_digest"]))
	if err != nil {
		t.Fatal(err)
	}
	wt := &worktreeruntime.Service{}
	base, err := wt.ResolveTaskBase(f.lane)
	if err != nil {
		t.Fatal(err)
	}
	allocated, err := wt.AllocateTaskWorkspaceWithSource(f.lane, base, "ignored-replacement", []string{"docs"}, func(dir string) error {
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("docs/\n"), 0644); err != nil {
			return err
		}
		return MaterializeRecoverySource(dir, selected, []string{"docs"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if recoveryGit(t, allocated.WorkspacePath, "ls-files", "--stage") != "" || recoveryGit(t, f.dirtyPath, "ls-files", "--stage") != "" {
		t.Fatal("recovery staged ignored documentation")
	}
	if recoveryGit(t, allocated.WorkspacePath, "check-ignore", "docs/note.md") != "docs/note.md" {
		t.Fatal("fixture not ignored")
	}
	body, _ := os.ReadFile(filepath.Join(allocated.WorkspacePath, "docs/note.md"))
	if string(body) != "exact draft\n" {
		t.Fatal("ignored bytes lost")
	}
}
