package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/appstorage"
)

func (s *Service) allocateWorkerClone(source string, current bool, base, branch, owner string, exact bool, scopes []string, sparse bool, initialize ...func(string) error) (Allocation, error) {
	root, err := resolveRepositoryRoot(source)
	if err != nil {
		return Allocation{}, err
	}
	base, err = resolveEffectiveBaseBranch(source, current, base)
	if err != nil {
		return Allocation{}, err
	}
	head, err := runGit(source, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return Allocation{}, err
	}
	id := sessionWorkspaceID(owner)
	if exact {
		id, err = workspaceIdentityForRequestedBranch(branch)
	} else {
		branch = effectiveWorktreeBranchName(branch, owner)
	}
	if err != nil {
		return Allocation{}, err
	}
	if _, err := runGit(source, "check-ref-format", "--branch", branch); err != nil {
		return Allocation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := deterministicSessionWorktreePath(root, id)
	if err != nil {
		return Allocation{}, err
	}
	if err := ensureWorktreeParent(root); err != nil {
		return Allocation{}, err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return Allocation{}, &RequestedWorktreeNameConflictError{WorktreeName: branch, Cause: fmt.Errorf("clone destination already exists or is inaccessible")}
	}
	cleanup := func(cause error) (Allocation, error) {
		if errors.Is(cause, ErrAllocationWriterUnconfirmed) {
			return Allocation{}, cause
		}
		return Allocation{}, errors.Join(cause, os.RemoveAll(path))
	}
	// --no-local copies objects through upload-pack: no alternates, hardlinks,
	// source config changes, or dependence on source garbage collection.
	if _, err := runGitAllocation(root, nil, "clone", "--no-local", "--no-checkout", "--", source, path); err != nil {
		if errors.Is(err, ErrAllocationWriterUnconfirmed) {
			return Allocation{}, err
		}
		return cleanup(err)
	}
	// A base may be an exact retained commit rather than an advertised branch.
	if _, err := runGitAllocation(path, nil, "fetch", "--no-tags", "--", source, head); err != nil {
		return cleanup(err)
	}
	if _, err := runGitAllocation(path, nil, "checkout", "-B", branch, head); err != nil {
		return cleanup(err)
	}
	if sparse {
		if err := prepareTaskWorktreeCheckout(path, scopes); err != nil {
			return cleanup(err)
		}
	}
	for _, init := range initialize {
		if init != nil {
			if err := init(path); err != nil {
				return cleanup(err)
			}
		}
	}
	if err := appstorage.SetPrivateDirPerm(path); err != nil {
		return cleanup(err)
	}
	return Allocation{WorkspacePath: path, RepoRoot: root, BaseBranch: base, BaseCommit: head, BranchName: branch, WorkspaceID: id}, nil
}

// managedCloneIdentity is a consistency check, not authorization: callers must
// first bind the exact managed path to durable session ownership.
func managedCloneIdentity(source, lane string) bool {
	root, err := worktreeCacheRoot(source)
	if err != nil || filepath.Dir(filepath.Clean(lane)) != root {
		return false
	}
	if err := noSymlinkDirectory(filepath.Join(lane, ".git")); err != nil {
		return false
	}
	top, err := resolveRepositoryRoot(lane)
	if err != nil || !sameCleanPath(top, lane) {
		return false
	}
	origin, err := runGit(lane, "config", "--get", "remote.origin.url")
	return err == nil && sameCleanPath(strings.TrimSpace(origin), source)
}
