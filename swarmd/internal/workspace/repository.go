package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
)

const repositoryCommandTimeout = 12 * time.Second

const (
	RepositoryStateReady              = "ready"
	RepositoryStateGitUnavailable     = "git_unavailable"
	RepositoryStateNotRepository      = "not_repository"
	RepositoryStateNeedsInitialCommit = "needs_initial_commit"
	RepositoryStateNeedsAssistedSetup = "needs_assisted_setup"
	repositoryMessageNonWorkTree      = "Swarm requires a normal Git worktree with an initial commit"
)

// RepositoryState describes whether a directory can back Swarm's mandatory
// managed worktree lifecycle. It is safe to return to authenticated clients.
type RepositoryState struct {
	State       string `json:"state"`
	Path        string `json:"path"`
	Repository  string `json:"repository_root,omitempty"`
	HeadCommit  string `json:"head_commit,omitempty"`
	CanSetup    bool   `json:"can_setup"`
	NeedsReview bool   `json:"needs_review,omitempty"`
	Message     string `json:"message"`
}

// RepositoryPrerequisiteError lets API callers return an actionable typed
// failure without parsing Git's stderr.
type RepositoryPrerequisiteError struct {
	Repository RepositoryState
}

func (e *RepositoryPrerequisiteError) Error() string {
	if e == nil {
		return "workspace requires a committed Git repository"
	}
	return e.Repository.Message
}

func RepositoryStateFromError(err error) (RepositoryState, bool) {
	var prerequisite *RepositoryPrerequisiteError
	if !errors.As(err, &prerequisite) || prerequisite == nil {
		return RepositoryState{}, false
	}
	return prerequisite.Repository, true
}

// InspectRepositoryForPrincipal validates the filesystem prerequisite before
// any workspace catalog, topology, or session state may be mutated.
func (s *Service) InspectRepositoryForPrincipal(principal identity.Principal, path string) (RepositoryState, error) {
	if s == nil || s.store == nil {
		return RepositoryState{}, errors.New("workspace service is not configured")
	}
	if err := requirePrincipal(principal); err != nil {
		return RepositoryState{}, err
	}
	requested, err := absoluteWorkspacePath(path)
	if err != nil {
		return RepositoryState{}, err
	}
	resolved, err := resolvePath(path)
	if err != nil {
		return RepositoryState{}, err
	}
	if requested != resolved {
		return RepositoryState{}, fmt.Errorf("workspace paths must use their canonical directory; select %q instead of a symlink", resolved)
	}
	if err := ensureWorkspaceDirectory(resolved); err != nil {
		return RepositoryState{}, err
	}
	return inspectRepository(resolved), nil
}

func (s *Service) requireRepositoryForPrincipal(principal identity.Principal, path string) (RepositoryState, error) {
	state, err := s.InspectRepositoryForPrincipal(principal, path)
	if err != nil {
		return RepositoryState{}, err
	}
	if state.State != RepositoryStateReady {
		return RepositoryState{}, &RepositoryPrerequisiteError{Repository: state}
	}
	return state, nil
}

func inspectRepository(path string) RepositoryState {
	state := RepositoryState{Path: path}
	if _, err := exec.LookPath("git"); err != nil {
		state.State = RepositoryStateGitUnavailable
		state.Message = "Git is required to add a Swarm workspace; install Git and retry"
		return state
	}

	root, err := runRepositoryGit(path, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(root) == "" {
		if _, insideWorkTreeErr := runRepositoryGit(path, "rev-parse", "--is-inside-work-tree"); insideWorkTreeErr == nil {
			state.State = RepositoryStateNotRepository
			state.Message = repositoryMessageNonWorkTree
			return state
		}
		state.State = RepositoryStateNotRepository
		state.CanSetup = directoryIsEmpty(path)
		state.NeedsReview = !state.CanSetup
		if state.CanSetup {
			state.Message = "Swarm workspaces require a Git repository with an initial commit; this empty directory can be initialized safely"
		} else {
			state.State = RepositoryStateNeedsAssistedSetup
			state.Message = "Swarm workspaces require a Git repository with an initial commit; review and commit this directory's existing files before adding it"
		}
		return state
	}
	root = strings.TrimSpace(root)
	if !filepath.IsAbs(root) {
		root = filepath.Join(path, root)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		state.State = RepositoryStateNotRepository
		state.Message = "Swarm could not resolve this Git repository's root"
		return state
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		state.State = RepositoryStateNotRepository
		state.Message = "Swarm could not resolve this Git repository's canonical root"
		return state
	}
	state.Repository = filepath.Clean(root)
	if state.Repository != path {
		state.State = RepositoryStateNotRepository
		state.HeadCommit = ""
		state.CanSetup = false
		state.NeedsReview = true
		state.Message = "Select the Git repository root as the Swarm workspace"
		return state
	}
	head, err := runRepositoryGit(path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(head) == "" {
		state.State = RepositoryStateNeedsInitialCommit
		state.NeedsReview = true
		state.Message = "Swarm workspaces require an initial Git commit; create the first commit and retry"
		return state
	}
	state.State = RepositoryStateReady
	state.HeadCommit = strings.TrimSpace(head)
	state.Message = "Git repository is ready for managed worktrees"
	return state
}

// SetupRepositoryForPrincipal performs the only automatic repository setup
// Swarm can do without deciding which user files belong in source control. It
// initializes an empty, canonical directory and creates an empty first commit.
func (s *Service) SetupRepositoryForPrincipal(principal identity.Principal, path, expectedResolvedPath string) (RepositoryState, error) {
	if s == nil || s.store == nil {
		return RepositoryState{}, errors.New("workspace service is not configured")
	}
	if err := requirePrincipal(principal); err != nil {
		return RepositoryState{}, err
	}
	requested, err := absoluteWorkspacePath(path)
	if err != nil {
		return RepositoryState{}, err
	}
	resolved, err := resolvePath(path)
	if err != nil {
		return RepositoryState{}, err
	}
	if existing, ok, err := s.store.GetForAccount(principal.AccountScopeID, resolved); err != nil {
		return RepositoryState{}, err
	} else if ok {
		return RepositoryState{}, fmt.Errorf("workspace %q is already saved with id %q", resolved, existing.WorkspaceID)
	}
	if requested != resolved {
		return RepositoryState{}, fmt.Errorf("repository setup rejects symlinked paths; select the canonical directory %q", resolved)
	}
	expected := strings.TrimSpace(expectedResolvedPath)
	if expected == "" {
		return RepositoryState{}, errors.New("expected_resolved_path is required to guard repository setup against a stale selection")
	}
	expected, err = filepath.Abs(expected)
	if err != nil {
		return RepositoryState{}, fmt.Errorf("resolve expected directory: %w", err)
	}
	if filepath.Clean(expected) != resolved {
		return RepositoryState{}, fmt.Errorf("selected directory is stale: expected %q, current canonical path is %q", filepath.Clean(expected), resolved)
	}
	if err := ensureWorkspaceDirectory(resolved); err != nil {
		return RepositoryState{}, err
	}
	state := inspectRepository(resolved)
	if state.State == RepositoryStateGitUnavailable {
		return state, &RepositoryPrerequisiteError{Repository: state}
	}
	if state.State == RepositoryStateReady || state.State == RepositoryStateNeedsInitialCommit || state.Repository != "" || state.Message == repositoryMessageNonWorkTree {
		return state, errors.New("repository setup rejects directories that are already inside Git repositories")
	}
	if !directoryIsEmpty(resolved) {
		state.State = RepositoryStateNeedsAssistedSetup
		state.CanSetup = false
		state.NeedsReview = true
		state.Message = "Existing files were not staged or committed; initialize and review the repository manually, then create the first commit"
		return state, &RepositoryPrerequisiteError{Repository: state}
	}

	gitPath := filepath.Join(resolved, ".git")
	if err := os.Mkdir(gitPath, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return RepositoryState{}, errors.New("repository setup found an unexpected .git path")
		}
		return RepositoryState{}, fmt.Errorf("reserve repository metadata directory: %w", err)
	}
	cleanup := func(cause error) error {
		if err := os.RemoveAll(gitPath); err != nil {
			return errors.Join(cause, fmt.Errorf("rollback repository setup: %w", err))
		}
		return cause
	}
	if _, err := runRepositoryGit(resolved, "--git-dir=.git", "--work-tree=.", "init", "--initial-branch=main", "--template="); err != nil {
		return RepositoryState{}, cleanup(fmt.Errorf("initialize Git repository: %w", err))
	}
	if _, err := runRepositoryGit(resolved,
		"-c", "user.name=Swarm Workspace Setup",
		"-c", "user.email=swarm-workspace-setup@localhost",
		"-c", "commit.gpgSign=false",
		"-c", "core.hooksPath=/dev/null",
		"commit", "--allow-empty", "--no-verify", "-m", "Initialize Swarm workspace",
	); err != nil {
		return RepositoryState{}, cleanup(fmt.Errorf("create initial Git commit: %w", err))
	}
	ready := inspectRepository(resolved)
	if ready.State != RepositoryStateReady {
		return RepositoryState{}, cleanup(errors.New("repository setup did not produce a valid initial commit"))
	}
	return ready, nil
}

func directoryIsEmpty(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) == 0
}

func runRepositoryGit(path string, args ...string) (string, error) {
	return runRepositoryGitWithEnv(path, nil, args...)
}

func runRepositoryGitWithEnv(path string, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), repositoryCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
	if env != nil {
		cmd.Env = env
	}
	output, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", errors.New("Git command timed out")
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "", errors.New("Git is not installed")
	}
	if err != nil {
		return "", fmt.Errorf("git command failed: %s", strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}
