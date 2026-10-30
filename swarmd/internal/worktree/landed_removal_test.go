package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
)

// landedFixture is a real repository on dev with one job worktree on its own
// agent branch, allocated the way a worker run's checkout is.
type landedFixture struct {
	t      *testing.T
	repo   string
	s      *Service
	alloc  Allocation
	branch string
}

func newLandedFixture(t *testing.T) *landedFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	f := &landedFixture{t: t, repo: t.TempDir(), s: &Service{}, branch: "agent/worker-landed"}
	f.git(f.repo, "init", "-b", "dev")
	f.commit(f.repo, "base.txt", "base")
	alloc, err := f.s.allocateSessionWorkspaceWithBranchMode(f.repo, true, "", f.branch, "worker-execution-landed", true)
	if err != nil {
		t.Fatal(err)
	}
	f.alloc = alloc
	return f
}

func (f *landedFixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *landedFixture) commit(dir, name, content string) string {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
		f.t.Fatal(err)
	}
	f.git(dir, "add", name)
	f.git(dir, "commit", "-q", "-m", "change "+name)
	return f.git(dir, "rev-parse", "HEAD")
}

func (f *landedFixture) landed() LandedWorktree {
	return LandedWorktree{Source: f.repo, Path: f.alloc.WorkspacePath, Branch: f.branch, TargetBranch: "dev"}
}

func (f *landedFixture) branchTip() string {
	return f.git(f.repo, "rev-parse", "refs/heads/"+f.branch)
}

func (f *landedFixture) registered() bool {
	f.t.Helper()
	ok, err := worktreePathRegistered(f.repo, f.alloc.WorkspacePath)
	if err != nil {
		f.t.Fatal(err)
	}
	return ok
}

// Requirement: a finished job's checkout whose HEAD is already in the
// folder's target branch and that has no uncommitted or untracked changes is
// removed (files and Git registration) while its branch is kept at the same
// commit. Threat: the 0.6-1.2 GB checkouts of landed jobs pile up forever.
// Owner: Service.RemoveLandedWorktree. Real Git is the narrowest layer that
// proves both the removal and the kept branch.
func TestRemoveLandedWorktreeRemovesMergedCleanCheckoutAndKeepsBranch(t *testing.T) {
	f := newLandedFixture(t)
	head := f.commit(f.alloc.WorkspacePath, "work.txt", "work")
	f.git(f.repo, "merge", "--ff-only", "-q", f.branch)

	removed, err := f.s.RemoveLandedWorktree(f.landed())
	if err != nil {
		t.Fatal(err)
	}
	if removed.Head != head || removed.Branch != f.branch || removed.Path != f.alloc.WorkspacePath || removed.TargetBranch != "dev" || removed.TargetHead != head {
		t.Fatalf("removal record = %+v, want head %s", removed, head)
	}
	if _, err := os.Stat(f.alloc.WorkspacePath); !os.IsNotExist(err) {
		t.Fatalf("checkout still on disk: %v", err)
	}
	if f.registered() {
		t.Fatal("checkout still registered with git")
	}
	if tip := f.branchTip(); tip != head {
		t.Fatalf("branch moved to %s, want kept at %s", tip, head)
	}
	// A second pass finds nothing to do and reports it, never a failure.
	if _, err := f.s.RemoveLandedWorktree(f.landed()); err != ErrLandedWorktreeGone {
		t.Fatalf("second removal = %v, want ErrLandedWorktreeGone", err)
	}
}

// Requirement: every unsafe checkout is kept untouched: uncommitted or
// untracked changes, commits not in the target branch, the folder itself, a
// checkout that is not a managed worktree of the folder, and a missing target
// branch. Threat: deleting work that exists nowhere else. Each case asserts
// the refusal and that the checkout, its registration and its branch remain.
func TestRemoveLandedWorktreeKeepsUnsafeCheckouts(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(f *landedFixture) LandedWorktree
	}{
		{"untracked file", func(f *landedFixture) LandedWorktree {
			f.commit(f.alloc.WorkspacePath, "work.txt", "work")
			f.git(f.repo, "merge", "--ff-only", "-q", f.branch)
			if err := os.WriteFile(filepath.Join(f.alloc.WorkspacePath, "notes.txt"), []byte("draft"), 0600); err != nil {
				f.t.Fatal(err)
			}
			return f.landed()
		}},
		{"uncommitted change", func(f *landedFixture) LandedWorktree {
			f.commit(f.alloc.WorkspacePath, "work.txt", "work")
			f.git(f.repo, "merge", "--ff-only", "-q", f.branch)
			if err := os.WriteFile(filepath.Join(f.alloc.WorkspacePath, "work.txt"), []byte("edited"), 0600); err != nil {
				f.t.Fatal(err)
			}
			return f.landed()
		}},
		{"unmerged commit", func(f *landedFixture) LandedWorktree {
			f.commit(f.alloc.WorkspacePath, "work.txt", "work")
			return f.landed()
		}},
		{"commit after merge", func(f *landedFixture) LandedWorktree {
			f.commit(f.alloc.WorkspacePath, "work.txt", "work")
			f.git(f.repo, "merge", "--ff-only", "-q", f.branch)
			f.commit(f.alloc.WorkspacePath, "more.txt", "more")
			return f.landed()
		}},
		{"folder itself", func(f *landedFixture) LandedWorktree {
			in := f.landed()
			in.Path = f.repo
			return in
		}},
		{"missing target branch", func(f *landedFixture) LandedWorktree {
			in := f.landed()
			in.TargetBranch = "release"
			return in
		}},
		{"target is the job branch", func(f *landedFixture) LandedWorktree {
			in := f.landed()
			in.TargetBranch = f.branch
			return in
		}},
		{"not a managed worktree path", func(f *landedFixture) LandedWorktree {
			in := f.landed()
			in.Path = filepath.Join(f.t.TempDir(), "elsewhere")
			return in
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLandedFixture(t)
			in := tc.prepare(f)
			before := f.branchTip()
			_, err := f.s.RemoveLandedWorktree(in)
			if !IsLandedWorktreeKept(err) {
				t.Fatalf("RemoveLandedWorktree = %v, want a kept refusal", err)
			}
			if _, statErr := os.Stat(f.alloc.WorkspacePath); statErr != nil {
				t.Fatalf("checkout removed despite refusal: %v", statErr)
			}
			if !f.registered() {
				t.Fatal("checkout registration removed despite refusal")
			}
			if after := f.branchTip(); after != before {
				t.Fatalf("branch moved from %s to %s", before, after)
			}
			if _, statErr := os.Stat(filepath.Join(f.repo, "base.txt")); statErr != nil {
				t.Fatalf("folder damaged: %v", statErr)
			}
		})
	}
}

// Requirement: anything that needs a removed checkout again recreates it at
// its recorded path from the kept branch, and refuses when the branch moved
// since the removal. Owners: Service.RestoreRemovedWorktree and
// Service.AllocateProjectTaskFollowup (the task follow-up allocation).
func TestRestoreRemovedWorktreeRecreatesFromKeptBranch(t *testing.T) {
	f := newLandedFixture(t)
	head := f.commit(f.alloc.WorkspacePath, "work.txt", "work")
	f.git(f.repo, "merge", "--ff-only", "-q", f.branch)
	if _, err := f.s.RemoveLandedWorktree(f.landed()); err != nil {
		t.Fatal(err)
	}

	restored, err := f.s.RestoreRemovedWorktree(f.repo, f.alloc.WorkspacePath, f.branch, head)
	if err != nil || !restored {
		t.Fatalf("restore = %v, %v", restored, err)
	}
	state, err := f.s.InspectTaskWorkspace(f.alloc.WorkspacePath)
	if err != nil {
		t.Fatal(err)
	}
	if state.BranchName != f.branch || state.HeadCommit != head || !state.Clean {
		t.Fatalf("restored checkout = %+v, want clean %s at %s", state, f.branch, head)
	}
	if again, err := f.s.RestoreRemovedWorktree(f.repo, f.alloc.WorkspacePath, f.branch, head); err != nil || again {
		t.Fatalf("restore of an existing checkout = %v, %v; want nothing to do", again, err)
	}

	// The branch moved after a second removal: never checked out silently.
	if _, err := f.s.RemoveLandedWorktree(f.landed()); err != nil {
		t.Fatal(err)
	}
	f.git(f.repo, "branch", "-f", f.branch, "dev~0")
	f.commit(f.repo, "later.txt", "later")
	f.git(f.repo, "branch", "-f", f.branch, "HEAD")
	if _, err := f.s.RestoreRemovedWorktree(f.repo, f.alloc.WorkspacePath, f.branch, head); err == nil {
		t.Fatal("restore checked out a branch that moved since its removal")
	}
	if _, err := os.Stat(f.alloc.WorkspacePath); !os.IsNotExist(err) {
		t.Fatalf("refused restore left a checkout: %v", err)
	}
}

func TestProjectTaskFollowupRecreatesRemovedCheckoutFromBranch(t *testing.T) {
	f := newLandedFixture(t)
	f.branch = "agent/task-landed"
	alloc, err := f.s.allocateSessionWorkspaceWithBranchMode(f.repo, true, "", f.branch, "task-session-one", true)
	if err != nil {
		t.Fatal(err)
	}
	f.alloc = alloc
	head := f.commit(alloc.WorkspacePath, "task.txt", "task")
	f.git(f.repo, "merge", "--ff-only", "-q", f.branch)
	if _, err := f.s.RemoveLandedWorktree(f.landed()); err != nil {
		t.Fatal(err)
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"}

	got, err := f.s.AllocateProjectTaskFollowup(p, f.repo, "task-session-two", f.branch, head, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspacePath != alloc.WorkspacePath || got.BranchName != f.branch || got.BaseCommit != head {
		t.Fatalf("follow-up allocation = %+v, want %s on %s at %s", got, alloc.WorkspacePath, f.branch, head)
	}
	state, err := f.s.InspectTaskWorkspace(got.WorkspacePath)
	if err != nil || state.HeadCommit != head || state.BranchName != f.branch || !state.Clean {
		t.Fatalf("recreated follow-up checkout = %+v, %v", state, err)
	}

	// A branch that is not at the follow-up's exact commit stays refused.
	if _, err := f.s.RemoveLandedWorktree(f.landed()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.AllocateProjectTaskFollowup(p, f.repo, "task-session-three", f.branch, f.git(f.repo, "rev-parse", "HEAD~1"), "dev"); err == nil {
		t.Fatal("follow-up recreated a checkout at a commit its branch is not at")
	}
}
