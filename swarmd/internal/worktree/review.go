package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sandbox"
	"time"
)

// AllocateReviewWorkspace creates no branch or writable publication lineage.
func (s *Service) AllocateReviewWorkspace(p identity.Principal, source, owner, head string, commits ...string) (Allocation, error) {
	if len(commits) == 0 {
		return Allocation{}, errors.New("review requires exact base evidence")
	}
	if err := requirePrincipal(p); err != nil {
		return Allocation{}, err
	}
	source, err := s.resolveWorkspaceConfigPathForPrincipal(p, source)
	if err != nil {
		return Allocation{}, err
	}
	// Preserve the admitted identity through sandbox cache admission.
	ctx := identity.ContextWithPrincipal(context.Background(), p)
	return s.allocateReviewWorkspaceContext(ctx, source, owner, head, worktreeAllocationTimeout, commits...)
}

func (s *Service) allocateReviewWorkspace(source, owner, head string) (Allocation, error) {
	return s.allocateReviewWorkspaceTimeout(source, owner, head, worktreeAllocationTimeout)
}

func (s *Service) allocateReviewWorkspaceTimeout(source, owner, head string, timeout time.Duration) (Allocation, error) {
	return s.allocateReviewWorkspaceContext(context.Background(), source, owner, head, timeout)
}

func (s *Service) allocateReviewWorkspaceContext(ctx context.Context, source, owner, head string, timeout time.Duration, commits ...string) (Allocation, error) {
	root, err := resolveRepositoryRootContext(ctx, source)
	if err != nil {
		return Allocation{}, err
	}
	cloneSource := root
	resolved, err := runGitContext(ctx, root, "rev-parse", "--verify", head+"^{commit}")
	if err != nil && validCommitID(head) {
		// Reviewed commits now live only in the coder clone, not the folder.
		cache, cacheErr := worktreeCacheRoot(root)
		if cacheErr != nil {
			return Allocation{}, cacheErr
		}
		entries, readErr := os.ReadDir(cache)
		if readErr != nil {
			return Allocation{}, readErr
		}
		for _, entry := range entries {
			candidate := filepath.Join(cache, entry.Name())
			if !entry.IsDir() || !managedCloneIdentityContext(ctx, root, candidate) {
				continue
			}
			if value, readErr := runGitContext(ctx, candidate, "rev-parse", "--verify", head+"^{commit}"); readErr == nil && value == head {
				cloneSource, resolved, err = candidate, value, nil
				break
			}
		}
	}
	if err != nil || resolved != head {
		return Allocation{}, errors.New("review requires an exact commit")
	}
	// Resolve immutable evidence only in the authorized folder or managed job
	// clone. Fetching occurs before sandbox admission; no source is writable.
	evidence := map[string]string{}
	if len(commits) > 4 {
		return Allocation{}, errors.New("too many review evidence commits")
	}
	for _, commit := range commits {
		if !validCommitID(commit) {
			return Allocation{}, errors.New("review requires exact base evidence")
		}
		for _, source := range []string{cloneSource, root} {
			if value, err := runGitContext(ctx, source, "rev-parse", "--verify", commit+"^{commit}"); err == nil && value == commit {
				evidence[commit] = source
				break
			}
		}
		if evidence[commit] == "" {
			return Allocation{}, fmt.Errorf("review evidence commit %s unavailable", commit)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := deterministicSessionWorktreePath(root, sessionWorkspaceID(owner))
	if err != nil {
		return Allocation{}, err
	}
	if err := ensureWorktreeParent(root); err != nil {
		return Allocation{}, err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return Allocation{}, errors.New("review checkout already exists")
	}
	guard := path + ".review-writer"
	guardFile, err := os.OpenFile(guard, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Allocation{}, fmt.Errorf("allocation writer guard: %w", err)
	}
	if _, err := guardFile.WriteString("not-started"); err != nil {
		guardFile.Close()
		return Allocation{}, err
	}
	if err := guardFile.Sync(); err != nil {
		guardFile.Close()
		return Allocation{}, err
	}
	if err := guardFile.Close(); err != nil {
		return Allocation{}, err
	}
	// Keep --no-local isolation while bounding the pack build: gVisor counts
	// the whole upload-pack/pack-objects process tree against sandbox memory.
	_, allocationErr := runGitAllocationGuardedContext(ctx, root, nil, timeout, guard, boundedCloneArgs(cloneSource, path)...)
	if errors.Is(allocationErr, ErrAllocationWriterUnconfirmed) {
		return Allocation{}, allocationErr
	}
	if err := os.Remove(guard); err != nil {
		return Allocation{}, errors.Join(allocationErr, err)
	}
	if err := allocationErr; err != nil {
		return Allocation{}, errors.Join(fmt.Errorf("review checkout admission: %w", err), s.CleanupReviewAdmission(root, owner))
	}
	commands := [][]string{{"fetch", "--no-tags", "--", cloneSource, head}}
	for _, commit := range commits {
		commands = append(commands, []string{"fetch", "--no-tags", "--", evidence[commit], commit})
	}
	commands = append(commands, []string{"remote", "set-url", "origin", root})
	for _, args := range commands {
		if _, err := runGitAllocationGuardedContext(ctx, path, nil, timeout, "", args...); err != nil {
			if errors.Is(err, ErrAllocationWriterUnconfirmed) {
				return Allocation{}, err
			}
			return Allocation{}, errors.Join(err, s.CleanupReviewAdmission(root, owner))
		}
	}
	refresh, err := reviewRemoteRefresh(ctx, path, root, timeout)
	if err != nil {
		if errors.Is(err, ErrAllocationWriterUnconfirmed) {
			return Allocation{}, err
		}
		return Allocation{}, errors.Join(err, s.CleanupReviewAdmission(root, owner))
	}
	for _, args := range append(refresh, []string{"checkout", "--detach", head}) {
		if _, err := runGitAllocationGuardedContext(ctx, path, nil, timeout, "", args...); err != nil {
			if errors.Is(err, ErrAllocationWriterUnconfirmed) {
				return Allocation{}, err
			}
			return Allocation{}, errors.Join(err, s.CleanupReviewAdmission(root, owner))
		}
	}
	return Allocation{WorkspacePath: path, RepoRoot: root, BaseCommit: head, BaseBranch: head, BranchName: "HEAD", WorkspaceID: sessionWorkspaceID(owner)}, nil
}

// reviewRemoteRefresh is the Git a review checkout runs so its origin
// branches are the folder's, as of now. The checkout is cloned from the
// reviewed job's clone, so its refs/remotes/origin/<branch> start as that
// clone's branches when the job was created (dev at the job's base); origin
// is the folder. Each such branch the folder still has is fetched from the
// folder, and one it no longer has is dropped, so a reviewer can tell from
// refs/remotes/origin/<target> what the target branch holds. Only the review
// checkout is written; the folder and the job clone are read.
func reviewRemoteRefresh(ctx context.Context, path, root string, timeout time.Duration) ([][]string, error) {
	refs, err := runGitAllocationGuardedContext(ctx, path, nil, timeout, "", "for-each-ref", "--format=%(refname)", "refs/remotes/origin/")
	if err != nil {
		return nil, err
	}
	fetch := []string{"fetch", "--no-tags", "--no-write-fetch-head", "--", root}
	var commands [][]string
	for _, ref := range strings.Fields(refs) {
		branch := strings.TrimPrefix(ref, "refs/remotes/origin/")
		if branch == ref || branch == "HEAD" || !targetBranchName.MatchString(branch) || strings.Contains(branch, "..") {
			continue
		}
		if TargetBranchTip(ctx, root, branch) == "" {
			commands = append(commands, []string{"update-ref", "-d", ref})
			continue
		}
		fetch = append(fetch, "+refs/heads/"+branch+":"+ref)
	}
	if len(fetch) > 5 {
		commands = append([][]string{fetch}, commands...)
	}
	return commands, nil
}

// RemoveReviewWorkspace discards build outputs only in the verified managed
// detached lane. No branch or commit is promoted or delivered.
func (s *Service) RemoveReviewWorkspace(source, path, owner, head string) error {
	if err := s.ValidateReviewWorkspaceRemoval(source, path, owner, head); err != nil {
		return err
	}
	return s.RemoveValidatedReviewWorkspace(source, path)
}

// ValidateReviewWorkspaceRemoval runs before terminal sandbox shutdown.
func (s *Service) ValidateReviewWorkspaceRemoval(source, path, owner, head string) error {
	root, err := resolveRepositoryRoot(source)
	if err != nil {
		return err
	}
	expected, err := deterministicSessionWorktreePath(root, sessionWorkspaceID(owner))
	if err != nil {
		return err
	}
	if filepath.Clean(path) != expected {
		return errors.New("review checkout owner mismatch")
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("review checkout must not be a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Lstat(filepath.Join(path, ".git")); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := s.ValidateSessionRepositoryLaneForRead(source, path, owner, "HEAD"); err != nil {
		return err
	}
	actual, err := runGit(path, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(actual) != head {
		return errors.New("review head changed")
	}
	return nil
}

// RemoveValidatedReviewWorkspace is daemon-only; no Git runs in the closing
// checkout. Caller must validate before shutdown and retain restrictive scope.
func (s *Service) RemoveValidatedReviewWorkspace(source, path string) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("review checkout must not be a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return removeManagedWorktree(source, path)
}

// ValidateReviewExecutionIdentity admits only the daemon-registered, owner-bound
// detached review checkout at its immutable reviewed commit. It grants no branch
// publication or mutation authority and never substitutes for writable validation.
func ValidateReviewExecutionIdentity(source, lane, owner, head string) error {
	if sandbox.ReviewSessionCheckout(owner) != lane {
		return errors.New("review session checkout is not registered")
	}
	if err := (&Service{}).ValidateSessionRepositoryLaneForRead(source, lane, owner, "HEAD"); err != nil {
		return err
	}
	resolved, err := runGit(lane, "rev-parse", "--verify", "--end-of-options", head+"^{commit}")
	if err != nil || resolved != head || head == "" {
		return errors.New("review requires an exact recorded head")
	}
	actual, err := resolveRepositoryHeadCommit(lane)
	if err != nil {
		return err
	}
	if actual != head {
		return errors.New("review head changed")
	}
	return nil
}

// CleanupReviewAdmission is daemon-only. The caller must establish review ownership
// from a durable run receipt and exclude live execution before calling it. Unlike
// normal removal it tolerates a checkout interrupted before HEAD was written.
func (s *Service) CleanupReviewAdmission(source, owner string) error {
	return s.cleanupReviewAdmission(source, owner, false)
}

// RecoverReviewAdmission is only for the startup sweep before admissions resume.
func (s *Service) RecoverReviewAdmission(source, owner string) error {
	return s.cleanupReviewAdmission(source, owner, true)
}

func (s *Service) cleanupReviewAdmission(source, owner string, startup bool) error {
	if strings.TrimSpace(owner) == "" {
		return errors.New("review owner required")
	}
	root, err := resolveRepositoryRoot(source)
	if err != nil {
		return err
	}
	path, err := deterministicSessionWorktreePath(root, sessionWorkspaceID(owner))
	if err != nil {
		return err
	}
	if err := recoverReviewWriter(path+".review-writer", startup); err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return err
	}
	if parent != filepath.Dir(path) {
		return errors.New("review parent is a symlink")
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("review checkout is a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	common, err := runGit(root, "rev-parse", "--git-common-dir")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	registrations := filepath.Join(common, "worktrees")
	if info, err := os.Lstat(registrations); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("worktree registrations directory is a symlink")
	}
	entries, err := os.ReadDir(registrations)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Identify only this checkout's registration; a global prune would also
	// remove unrelated missing user worktrees.
	var owned []string
	pointer, _ := os.ReadFile(filepath.Join(path, ".git"))
	linked := strings.TrimSpace(strings.TrimPrefix(string(pointer), "gitdir: "))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		registration := filepath.Join(registrations, entry.Name())
		data, err := os.ReadFile(filepath.Join(registration, "gitdir"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if strings.TrimSpace(string(data)) == filepath.Join(path, ".git") || (errors.Is(err, os.ErrNotExist) && linked == registration && entry.Name() == filepath.Base(path)) {
			head, err := os.ReadFile(filepath.Join(registration, "HEAD"))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if strings.HasPrefix(strings.TrimSpace(string(head)), "ref:") {
				return errors.New("refusing to remove a branch-backed checkout as a review")
			}
			owned = append(owned, registration)
		}
	}
	for _, registration := range owned {
		if err := os.RemoveAll(registration); err != nil {
			return fmt.Errorf("prune review registration: %w", err)
		}
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove partial review checkout: %w", err)
	}
	return nil
}
