package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/appstorage"
)

// LandedWorktree names a finished job's checkout and where its work lands:
// Source is the folder (the repository checkout the job forked from), Path
// the job's managed worktree, Branch the job's own branch and TargetBranch the
// folder's branch the work lands on.
type LandedWorktree struct {
	Source       string
	Path         string
	Branch       string
	TargetBranch string
}

// LandedWorktreeRemoval is what was removed: the checkout at Path, whose
// Branch stays at Head, contained in TargetBranch at TargetHead.
type LandedWorktreeRemoval struct {
	Path         string `json:"path"`
	Branch       string `json:"branch"`
	Head         string `json:"head"`
	TargetBranch string `json:"target_branch"`
	TargetHead   string `json:"target_head"`
}

// LandedWorktreeKeptError is a refusal: the worktree is not safe to remove,
// and nothing was changed.
type LandedWorktreeKeptError struct{ Reason string }

func (e *LandedWorktreeKeptError) Error() string { return "worktree kept: " + e.Reason }

func keep(format string, args ...any) error {
	return &LandedWorktreeKeptError{Reason: fmt.Sprintf(format, args...)}
}

// IsLandedWorktreeKept reports a refusal of RemoveLandedWorktree (as opposed
// to a failure while removing).
func IsLandedWorktreeKept(err error) bool {
	var kept *LandedWorktreeKeptError
	return errors.As(err, &kept)
}

// landedRemovalWait bounds how long RemoveLandedWorktree waits for the
// quarantined files to be deleted.
const landedRemovalWait = 5 * time.Second

// ErrLandedWorktreeGone reports that the worktree is no longer on disk.
var ErrLandedWorktreeGone = errors.New("worktree is already gone")

// RemoveLandedWorktree removes a finished job's checkout once its work is in
// the folder's target branch, and keeps the branch. The caller proves the job
// is finished and that no other run or review uses the checkout; this checks
// Git: the path is a direct managed worktree of the folder's repository (never
// the folder itself), registered and unlocked on the job's branch, with no
// uncommitted or untracked changes, and its HEAD is an ancestor of the target
// branch. Any other state is a LandedWorktreeKeptError and nothing changes.
// Checks and removal run under the repository's integration lock, so an
// integration cannot move the target or the checkout in between.
func (s *Service) RemoveLandedWorktree(in LandedWorktree) (LandedWorktreeRemoval, error) {
	source := strings.TrimSpace(in.Source)
	path := strings.TrimSpace(in.Path)
	branch := strings.TrimSpace(in.Branch)
	target := strings.TrimSpace(in.TargetBranch)
	if source == "" || path == "" || branch == "" || target == "" {
		return LandedWorktreeRemoval{}, keep("the job's folder, worktree, branch and target branch are required")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return LandedWorktreeRemoval{}, keep("worktree path %q is not a clean absolute path", path)
	}
	if branch == target {
		return LandedWorktreeRemoval{}, keep("worktree is on the target branch %s", target)
	}
	repoRoot, err := resolveRepositoryRoot(source)
	if err != nil {
		return LandedWorktreeRemoval{}, err
	}
	if sameCleanPath(path, repoRoot) || sameCleanPath(path, source) {
		return LandedWorktreeRemoval{}, keep("worktree is the folder itself")
	}
	root, err := worktreeCacheRoot(repoRoot)
	if err != nil {
		return LandedWorktreeRemoval{}, err
	}
	if filepath.Dir(path) != filepath.Clean(root) || strings.HasPrefix(filepath.Base(path), ".") {
		return LandedWorktreeRemoval{}, keep("worktree is not a managed worktree of the folder")
	}
	if info, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return LandedWorktreeRemoval{}, ErrLandedWorktreeGone
	} else if err != nil {
		return LandedWorktreeRemoval{}, err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return LandedWorktreeRemoval{}, keep("worktree path is not a directory")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	integrationLock, err := acquireIntegrationLock(repoRoot)
	if err != nil {
		return LandedWorktreeRemoval{}, err
	}
	defer integrationLock.Release()

	listing, err := runGit(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return LandedWorktreeRemoval{}, err
	}
	registered := false
	for _, entry := range parseWorktreeList(listing) {
		if !sameCleanPath(entry.Path, path) {
			continue
		}
		registered = true
		if entry.Locked {
			return LandedWorktreeRemoval{}, keep("worktree is locked")
		}
		if entry.Detached || normalizeGitWorktreeBranch(entry.Branch) != branch {
			return LandedWorktreeRemoval{}, keep("worktree is not on the job's branch %s", branch)
		}
	}
	if !registered {
		return LandedWorktreeRemoval{}, keep("worktree is not registered with the folder's repository")
	}
	state, err := s.InspectTaskWorkspace(path)
	if err != nil {
		return LandedWorktreeRemoval{}, err
	}
	if state.BranchName != branch {
		return LandedWorktreeRemoval{}, keep("worktree is not on the job's branch %s", branch)
	}
	if !state.Clean {
		return LandedWorktreeRemoval{}, keep("worktree has uncommitted or untracked changes")
	}
	targetHead, err := runGit(repoRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+target+"^{commit}")
	if err != nil || strings.TrimSpace(targetHead) == "" {
		return LandedWorktreeRemoval{}, keep("target branch %s is not in the folder's repository", target)
	}
	targetHead = strings.TrimSpace(targetHead)
	contained, err := s.TaskCommitDescendsFrom(repoRoot, state.HeadCommit, targetHead)
	if err != nil {
		return LandedWorktreeRemoval{}, err
	}
	if !contained {
		return LandedWorktreeRemoval{}, keep("worktree has commits that are not in %s", target)
	}
	// A large checkout is deleted in the background once it left its path
	// and its registration; the caller is not held for it.
	if err := removeManagedWorktreeWait(repoRoot, path, landedRemovalWait); err != nil && !errors.Is(err, errTrashDeletionQueued) && !errors.Is(err, errTrashDeletionBackground) {
		return LandedWorktreeRemoval{}, fmt.Errorf("remove landed worktree: %w", err)
	}
	if still, err := worktreePathRegistered(repoRoot, path); err != nil {
		return LandedWorktreeRemoval{}, err
	} else if still {
		return LandedWorktreeRemoval{}, errors.New("landed worktree remains registered after removal")
	}
	if tip, err := runGit(repoRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}"); err != nil || strings.TrimSpace(tip) != state.HeadCommit {
		return LandedWorktreeRemoval{}, fmt.Errorf("branch %s no longer resolves to the removed worktree's HEAD %s", branch, state.HeadCommit)
	}
	return LandedWorktreeRemoval{Path: path, Branch: branch, Head: state.HeadCommit, TargetBranch: target, TargetHead: targetHead}, nil
}

// RestoreRemovedWorktree recreates a job's removed checkout at its recorded
// path from its kept branch, the way a new run's checkout is made. It reports
// false when the path already exists (nothing to do). It requires the path to
// be a direct managed worktree path of the folder's repository and, when head
// is set, the branch to still be at head; a branch that moved since the
// removal is never checked out silently.
func (s *Service) RestoreRemovedWorktree(source, path, branch, head string) (bool, error) {
	source, path, branch, head = strings.TrimSpace(source), strings.TrimSpace(path), strings.TrimSpace(branch), strings.TrimSpace(head)
	if source == "" || path == "" || branch == "" {
		return false, errors.New("restoring a worktree requires its folder, path and branch")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false, fmt.Errorf("worktree path %q is not a clean absolute path", path)
	}
	if _, err := os.Lstat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	repoRoot, err := resolveRepositoryRoot(source)
	if err != nil {
		return false, err
	}
	root, err := worktreeCacheRoot(repoRoot)
	if err != nil {
		return false, err
	}
	if filepath.Dir(path) != filepath.Clean(root) || strings.HasPrefix(filepath.Base(path), ".") || sameCleanPath(path, repoRoot) {
		return false, errors.New("removed worktree path is not a managed worktree of the folder")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Lstat(path); err == nil {
		return false, nil
	}
	tip, err := runGit(repoRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	if err != nil || strings.TrimSpace(tip) == "" {
		return false, fmt.Errorf("branch %s of the removed worktree is not in the folder's repository", branch)
	}
	if head != "" && strings.TrimSpace(tip) != head {
		return false, fmt.Errorf("branch %s moved from %s to %s since its worktree was removed; reconcile it explicitly", branch, head, strings.TrimSpace(tip))
	}
	if registered, err := worktreePathRegistered(repoRoot, path); err != nil {
		return false, err
	} else if registered {
		// Stale registration of the missing path; prune exactly that entry.
		if err := removeManagedWorktree(repoRoot, path); err != nil {
			return false, fmt.Errorf("clear stale worktree registration: %w", err)
		}
	}
	if _, err := runGitWorktreeAddArgs(repoRoot, []string{"worktree", "add", path, branch}); err != nil {
		cleanupErr := cleanupFailedWorktreeAllocation(repoRoot, path)
		return false, fmt.Errorf("recreate worktree from branch %s: %w", branch, allocationFailureWithCleanup(err, cleanupErr))
	}
	if err := os.Chmod(path, appstorage.PrivateDirPerm); err != nil {
		return false, fmt.Errorf("secure recreated worktree: %w", err)
	}
	return true, nil
}
