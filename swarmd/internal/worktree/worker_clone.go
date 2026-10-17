package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/sandbox"
)

// boundedCloneArgs bounds both upload-pack's pack-objects and the client without
// persisting configuration in either repository. Git passes the upload-pack
// command through a shell; keep it static and pass paths as separate arguments.
func boundedCloneArgs(source, destination string) []string {
	return []string{
		"-c", "core.bigFileThreshold=1m",
		"clone", "--no-local", "--no-checkout",
		"--upload-pack=git -c pack.threads=1 -c pack.windowMemory=64m -c pack.deltaCacheSize=16m -c core.bigFileThreshold=1m -c pack.window=10 upload-pack",
		"--", source, destination,
	}
}

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
	// gVisor counts the whole pack build against the sandbox memory limit, so
	// bound delta-search memory and threads and exclude large blobs from it.
	if _, err := runGitAllocation(root, nil, boundedCloneArgs(source, path)...); err != nil {
		if errors.Is(err, ErrAllocationWriterUnconfirmed) {
			return Allocation{}, err
		}
		return cleanup(err)
	}
	// Clones do not inherit local config. Copy only identity components, never
	// hooks, credentials or unrelated source configuration. Missing components
	// remain unset so the sandbox-home default applies to both tools and bash.
	for _, key := range []string{"user.name", "user.email"} {
		ctx := sandbox.WithGitCommandTimeout(context.Background(), gitCommandTimeout)
		value, err := sandbox.TimedGitOutput(ctx, source, nil, "config", "--local", "--get", key)
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			continue
		}
		if err != nil {
			return cleanup(fmt.Errorf("read folder Git %s: %w", key, err))
		}
		if _, err := runGitAllocation(path, nil, "config", "--local", key, strings.TrimSuffix(string(value), "\n")); err != nil {
			return cleanup(err)
		}
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
	return managedCloneIdentityContext(context.Background(), source, lane)
}

func managedCloneIdentityContext(ctx context.Context, source, lane string) bool {
	root, err := worktreeCacheRoot(source)
	if err != nil || filepath.Dir(filepath.Clean(lane)) != root {
		return false
	}
	if err := noSymlinkDirectory(filepath.Join(lane, ".git")); err != nil {
		return false
	}
	top, err := resolveRepositoryRootContext(ctx, lane)
	if err != nil || !sameCleanPath(top, lane) {
		return false
	}
	origin, err := runGitContext(ctx, lane, "config", "--get", "remote.origin.url")
	return err == nil && sameCleanPath(strings.TrimSpace(origin), source)
}
