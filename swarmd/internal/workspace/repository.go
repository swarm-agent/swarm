package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"swarm/packages/swarmd/internal/identity"
)

// RuntimeWorkspaceGuidance is advisory only: setup must revalidate the selected
// path. Identity comes from the effective daemon UID, never HOME or the caller.
type RuntimeWorkspaceGuidance struct {
	RuntimeUsername string `json:"runtime_username,omitempty"`
	RuntimeUID      string `json:"runtime_uid"`
	RuntimeNonRoot  bool   `json:"runtime_non_root"`
	HomePath        string `json:"home_path,omitempty"`
	SetupRequired   bool   `json:"setup_required"`
	Message         string `json:"message"`
}

func DaemonWorkspaceGuidance() RuntimeWorkspaceGuidance {
	uid := strconv.Itoa(os.Geteuid())
	account, err := user.LookupId(uid)
	if err != nil {
		account = nil
	}
	return daemonWorkspaceGuidance(account, uid)
}

func daemonWorkspaceGuidance(account *user.User, uid string) RuntimeWorkspaceGuidance {
	g := RuntimeWorkspaceGuidance{
		RuntimeUID: uid, RuntimeNonRoot: uid != "0", SetupRequired: true,
		Message: "Workspace operations run as the daemon account; root access in your terminal does not give the daemon access. Choose an accessible project directory. Repository creation requires explicit setup consent.",
	}
	if account == nil || account.Uid != uid {
		return g
	}
	g.RuntimeUsername = account.Username
	home, err := writableDaemonHome(account, uid)
	if err != nil {
		return g
	}
	if uid != "0" {
		g.HomePath = home
		g.Message = "Use your home folder or create a new project folder (recommended). Git setup creates only an empty starting commit; existing files are not staged or committed."
	}
	return g
}

func writableDaemonHome(account *user.User, uid string) (string, error) {
	if account == nil || account.Uid != uid || !filepath.IsAbs(account.HomeDir) {
		return "", errors.New("daemon account home is unavailable")
	}
	home := filepath.Clean(account.HomeDir)
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil || resolved != home || filepath.Dir(home) == home {
		return "", errors.New("daemon account home must be a canonical directory")
	}
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() {
		return "", errors.New("daemon account home is unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || strconv.FormatUint(uint64(stat.Uid), 10) != uid || info.Mode().Perm()&0o300 != 0o300 || info.Mode().Perm()&0o022 != 0 {
		return "", errors.New("daemon account home must be owned and writable by the daemon, not writable by other users")
	}
	if err := unix.Faccessat(unix.AT_FDCWD, home, unix.W_OK|unix.X_OK, unix.AT_EACCESS); err != nil {
		return "", errors.New("daemon cannot write to its account home")
	}
	return home, nil
}

const repositoryCommandTimeout = 12 * time.Second

const (
	RepositoryStateReady              = "ready"
	RepositoryStateGitUnavailable     = "git_unavailable"
	RepositoryStateAccessDenied       = "access_denied"
	RepositoryStateTrustRequired      = "trust_required"
	RepositoryStateError              = "repository_error"
	RepositoryStateMissing            = "directory_missing"
	RepositoryStateNotRepository      = "not_repository"
	RepositoryStateNeedsInitialCommit = "needs_initial_commit"
	RepositoryStateNeedsAssistedSetup = "needs_assisted_setup"
	repositoryMessageNonWorkTree      = "Swarm requires a normal Git worktree with an initial commit"
)

// RepositoryState describes whether a directory can back Swarm's mandatory
// managed worktree lifecycle. It is safe to return to authenticated clients.
type RepositoryState struct {
	State             string   `json:"state"`
	Path              string   `json:"path"`
	Repository        string   `json:"repository_root,omitempty"`
	HeadCommit        string   `json:"head_commit,omitempty"`
	CanSetup          bool     `json:"can_setup"`
	NeedsReview       bool     `json:"needs_review,omitempty"`
	Message           string   `json:"message"`
	ContentReady      bool     `json:"content_ready"`
	RuntimeAccessible bool     `json:"runtime_accessible"`
	Actions           []string `json:"actions,omitempty"`
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
		state := repositoryFailure(requested, err)
		return state, &RepositoryPrerequisiteError{Repository: state}
	}
	if requested != resolved {
		return RepositoryState{}, fmt.Errorf("workspace paths must use their canonical directory; select %q instead of a symlink", resolved)
	}
	if err := ensureWorkspaceDirectory(resolved); err != nil {
		state := repositoryFailure(resolved, err)
		return state, &RepositoryPrerequisiteError{Repository: state}
	}
	return inspectRepository(resolved), nil
}

// InspectOnboardingRepositoryForPrincipal revalidates the exact unsaved path
// bound to an onboarding conversation. It permits the expected setup lifecycle
// (existing files -> unborn repository -> committed repository) without creating
// catalog, topology, selection, or session authority.
func (s *Service) InspectOnboardingRepositoryForPrincipal(principal identity.Principal, path, expectedResolvedPath string) (RepositoryState, error) {
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
		state := repositoryFailure(requested, err)
		return state, &RepositoryPrerequisiteError{Repository: state}
	}
	if requested != resolved {
		return RepositoryState{}, fmt.Errorf("workspace onboarding rejects symlinked paths; select the canonical directory %q", resolved)
	}
	expected := strings.TrimSpace(expectedResolvedPath)
	if expected == "" {
		return RepositoryState{}, errors.New("expected_resolved_path is required to guard workspace onboarding against a stale selection")
	}
	expected, err = filepath.Abs(expected)
	if err != nil {
		return RepositoryState{}, fmt.Errorf("resolve expected directory: %w", err)
	}
	if filepath.Clean(expected) != resolved {
		return RepositoryState{}, fmt.Errorf("selected directory is stale: expected %q, current canonical path is %q", filepath.Clean(expected), resolved)
	}
	if existing, ok, err := s.store.GetForAccount(principal.AccountScopeID, resolved); err != nil {
		return RepositoryState{}, err
	} else if ok {
		return RepositoryState{}, fmt.Errorf("workspace %q is already saved with id %q", resolved, existing.WorkspaceID)
	}
	if err := ensureWorkspaceDirectory(resolved); err != nil {
		state := repositoryFailure(resolved, err)
		return state, &RepositoryPrerequisiteError{Repository: state}
	}
	state := inspectRepository(resolved)
	switch state.State {
	case RepositoryStateGitUnavailable, RepositoryStateAccessDenied, RepositoryStateTrustRequired, RepositoryStateError, RepositoryStateMissing:
		return state, &RepositoryPrerequisiteError{Repository: state}
	case RepositoryStateNeedsAssistedSetup, RepositoryStateNeedsInitialCommit, RepositoryStateReady:
		return state, nil
	case RepositoryStateNotRepository:
		return state, errors.New("empty folders use the explicit repository setup action, not workspace onboarding assistance")
	default:
		return state, errors.New("workspace onboarding assistance found an unsupported repository state")
	}
}

// InspectAssistedRepositoryForPrincipal admits only the initial review-first
// onboarding state. Runtime scope revalidation uses
// InspectOnboardingRepositoryForPrincipal so a permissioned init/commit can
// complete without invalidating its own durable session.
func (s *Service) InspectAssistedRepositoryForPrincipal(principal identity.Principal, path, expectedResolvedPath string) (RepositoryState, error) {
	state, err := s.InspectOnboardingRepositoryForPrincipal(principal, path, expectedResolvedPath)
	if err != nil {
		return state, err
	}
	if state.State != RepositoryStateNeedsAssistedSetup {
		return state, errors.New("workspace onboarding assistance requires an unsaved non-repository directory containing existing files")
	}
	return state, nil
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
	account, _ := user.LookupId(strconv.Itoa(os.Geteuid()))
	return inspectRepositoryForAccount(path, account)
}

// Home is an explicit empty-baseline choice, never authority to import its files.
func isRuntimeHome(path string, account *user.User) bool {
	uid := strconv.Itoa(os.Geteuid())
	if uid == "0" {
		return false
	}
	home, err := writableDaemonHome(account, uid)
	return err == nil && path == home
}

func inspectRepositoryForAccount(path string, account *user.User) RepositoryState {
	state := RepositoryState{Path: path}
	if err := runtimeRepositoryAccess(path); err != nil {
		return repositoryFailure(path, err)
	}
	state.RuntimeAccessible = true
	if _, err := exec.LookPath("git"); err != nil {
		state.State = RepositoryStateGitUnavailable
		state.Message = "Git is unavailable to the daemon; repair the installation prerequisites or select another configured runtime"
		state.Actions = []string{"repair_installation", "retry"}
		return state
	}

	insideWorkTree, insideWorkTreeErr := runRepositoryGit(path, "rev-parse", "--is-inside-work-tree")
	if insideWorkTreeErr != nil && !isAbsentRepository(path, insideWorkTreeErr) {
		return repositoryFailure(path, insideWorkTreeErr)
	}
	if insideWorkTreeErr == nil && !strings.EqualFold(strings.TrimSpace(insideWorkTree), "true") {
		state.State = RepositoryStateNotRepository
		state.Message = repositoryMessageNonWorkTree
		return state
	}

	root, err := runRepositoryGit(path, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(root) == "" {
		if err == nil || !isAbsentRepository(path, err) {
			return repositoryFailure(path, err)
		}
		state.State = RepositoryStateNotRepository
		state.CanSetup = directoryIsEmpty(path) || isRuntimeHome(path, account)
		state.NeedsReview = !state.CanSetup
		if state.CanSetup {
			state.Message = "Swarm workspaces require a Git repository with an initial commit; setup creates only an empty starting commit, without staging or committing existing files"
		} else {
			state.State = RepositoryStateNeedsAssistedSetup
			state.Message = "Swarm workspaces require a Git repository with an initial commit; review and commit this directory's existing files before adding it"
		}
		state.Actions = []string{"review_content", "setup", "choose_directory"}
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
	for _, option := range []string{"--git-dir", "--git-common-dir"} {
		metadata, err := runRepositoryGit(path, "rev-parse", option)
		if err != nil {
			return repositoryFailure(path, err)
		}
		if !filepath.IsAbs(metadata) {
			metadata = filepath.Join(path, metadata)
		}
		if err := runtimeRepositoryAccess(metadata); err != nil {
			return repositoryFailure(path, err)
		}
	}
	if _, err := runRepositoryGit(path, "worktree", "list", "--porcelain"); err != nil {
		return repositoryFailure(path, err)
	}
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
		ref, refErr := runRepositoryGit(path, "symbolic-ref", "-q", "HEAD")
		if refErr != nil || !strings.HasPrefix(ref, "refs/heads/") {
			return repositoryFailure(path, err)
		}
		if _, refErr = runRepositoryGit(path, "show-ref", "--verify", "--quiet", ref); repositoryGitExitCode(refErr) != 1 {
			return repositoryFailure(path, err)
		}
		state.State = RepositoryStateNeedsInitialCommit
		state.NeedsReview = !isRuntimeHome(path, account)
		state.CanSetup = true
		state.Actions = []string{"review_content", "setup", "choose_directory"}
		state.Message = "Review existing content and explicitly choose the initial baseline before creating a managed workspace"
		if !state.NeedsReview {
			state.Message = "Create only an empty starting commit in home; existing files and staged content will not be committed"
		}
		return state
	}
	state.State = RepositoryStateReady
	state.HeadCommit = strings.TrimSpace(head)
	if status, err := runRepositoryGit(path, "--no-optional-locks", "status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching"); err != nil {
		return repositoryFailure(path, err)
	} else {
		state.ContentReady = status == ""
		state.NeedsReview = !state.ContentReady
	}
	state.Actions = []string{"save_workspace", "review_content"}
	state.Message = "Git HEAD is ready for managed worktrees; only committed content is included"
	return state
}

// SetupRepositoryForPrincipal performs the only automatic repository setup
// Swarm can do without deciding which user files belong in source control. It
// initializes an empty canonical project or the verified non-root runtime home,
// or completes an admitted unborn repository with an explicitly empty tree.
// Existing home content and any staged index are preserved, never imported.
func (s *Service) SetupRepositoryForPrincipal(principal identity.Principal, path, expectedResolvedPath string) (RepositoryState, error) {
	account, _ := user.LookupId(strconv.Itoa(os.Geteuid()))
	return s.setupRepositoryForPrincipal(principal, path, expectedResolvedPath, account)
}

func (s *Service) setupRepositoryForPrincipal(principal identity.Principal, path, expectedResolvedPath string, account *user.User) (RepositoryState, error) {
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
	unlock := lockRepositoryPreparation(requested)
	defer unlock()
	resolved, err := resolvePath(path)
	if err != nil {
		return RepositoryState{}, err
	}
	if existing, ok, err := s.store.GetForAccount(principal.AccountScopeID, resolved); err != nil {
		return RepositoryState{}, err
	} else if ok {
		if requested == resolved && expectedResolvedPath == resolved {
			state := inspectRepository(resolved)
			if state.State == RepositoryStateReady {
				return state, nil
			}
		}
		return RepositoryState{}, fmt.Errorf("workspace %q is already saved with id %q; repository repair cannot rewrite its history", resolved, existing.WorkspaceID)
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
	if _, err := os.Lstat(resolved); errors.Is(err, os.ErrNotExist) {
		if err := runtimeRepositoryAccess(filepath.Dir(resolved)); err != nil {
			state := repositoryFailure(resolved, err)
			return state, &RepositoryPrerequisiteError{Repository: state}
		}
		if _, err := exec.LookPath("git"); err != nil {
			state := RepositoryState{Path: resolved, State: RepositoryStateGitUnavailable, Message: "Git is required before creating a workspace directory; repair installation prerequisites and retry", Actions: []string{"repair_installation", "retry"}}
			return state, &RepositoryPrerequisiteError{Repository: state}
		}
		parent, err := os.OpenRoot(filepath.Dir(resolved))
		if err != nil {
			return RepositoryState{}, err
		}
		err = parent.Mkdir(filepath.Base(resolved), 0o700)
		parent.Close()
		if err != nil {
			return RepositoryState{}, fmt.Errorf("create new workspace directory: %w", err)
		}
	}
	if err := ensureWorkspaceDirectory(resolved); err != nil {
		state := repositoryFailure(resolved, err)
		return state, &RepositoryPrerequisiteError{Repository: state}
	}
	if filepath.Dir(resolved) == resolved {
		return RepositoryState{}, errors.New("choose a home or project directory instead of the filesystem root")
	}
	homeSelected := isRuntimeHome(resolved, account)
	if account != nil && filepath.Clean(account.HomeDir) == resolved && !homeSelected {
		return RepositoryState{}, errors.New("home setup requires a verified non-root runtime account and a private writable canonical home")
	}
	state := inspectRepositoryForAccount(resolved, account)
	if state.State == RepositoryStateGitUnavailable || state.State == RepositoryStateAccessDenied || state.State == RepositoryStateTrustRequired || state.State == RepositoryStateError {
		return state, &RepositoryPrerequisiteError{Repository: state}
	}
	if state.State == RepositoryStateNeedsInitialCommit && state.Repository == resolved {
		if !homeSelected {
			review, err := s.ReviewRepositoryForPrincipal(principal, resolved)
			if err != nil {
				return state, err
			}
			indexed, err := runRepositoryGit(resolved, "ls-files", "-z")
			if err != nil {
				return state, err
			}
			if len(review.Files) != 0 || indexed != "" {
				state.Message = "Existing content requires explicit baseline review; no files or index were changed"
				return state, &RepositoryPrerequisiteError{Repository: state}
			}
		}
		// Build an explicitly empty tree, never the user's index. Compare-and-swap
		// the unborn HEAD so a concurrent first commit cannot be overwritten.
		tree, err := runRepositoryGit(resolved, "hash-object", "-w", "-t", "tree", "--stdin")
		if err != nil {
			return state, err
		}
		commit, err := runRepositoryGit(resolved, "-c", "user.name=Swarm Workspace Setup", "-c", "user.email=swarm-workspace-setup@localhost", "commit-tree", tree, "-m", "Initialize Swarm workspace")
		if err != nil {
			return state, err
		}
		if _, err := runRepositoryGit(resolved, "-c", "core.hooksPath="+os.DevNull, "update-ref", "HEAD", commit, strings.Repeat("0", len(commit))); err != nil {
			return state, err
		}
		ready := inspectRepository(resolved)
		if ready.State != RepositoryStateReady {
			return ready, errors.New("repository setup did not produce a valid initial commit")
		}
		return ready, nil
	}
	if state.State == RepositoryStateReady && state.Repository == resolved {
		return state, nil // Response loss must not create another initial commit.
	}
	if state.State == RepositoryStateNeedsInitialCommit || state.Repository != "" || state.Message == repositoryMessageNonWorkTree {
		return state, errors.New("repository setup rejects directories that are already inside Git repositories")
	}
	if !homeSelected && !directoryIsEmpty(resolved) {
		state.State = RepositoryStateNeedsAssistedSetup
		state.CanSetup = false
		state.NeedsReview = true
		state.Message = "Existing files were not staged or committed; use content review to select an explicit baseline"
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
	// Never commit from the index, even if another process stages home files.
	tree, err := runRepositoryGit(resolved, "hash-object", "-w", "-t", "tree", "--stdin")
	if err != nil {
		return RepositoryState{}, cleanup(err)
	}
	commit, err := runRepositoryGit(resolved, "-c", "user.name=Swarm Workspace Setup", "-c", "user.email=swarm-workspace-setup@localhost", "commit-tree", tree, "-m", "Initialize Swarm workspace")
	if err != nil {
		return RepositoryState{}, cleanup(fmt.Errorf("create initial Git commit: %w", err))
	}
	if _, err := runRepositoryGit(resolved, "-c", "core.hooksPath="+os.DevNull, "update-ref", "HEAD", commit, strings.Repeat("0", len(commit))); err != nil {
		// Do not remove metadata if another writer won the first-commit race.
		return RepositoryState{}, fmt.Errorf("publish initial Git commit: %w", err)
	}
	ready := inspectRepository(resolved)
	if ready.State != RepositoryStateReady {
		// HEAD is already published; preserve it on a failed post-check so a
		// retry can inspect the same repository instead of destroying history.
		return ready, errors.New("repository setup post-check failed; Git metadata was preserved for retry")
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
	return runRepositoryGitInput(path, env, nil, args...)
}

func runRepositoryGitInput(path string, env []string, input io.Reader, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), repositoryCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path, "-c", "core.fsmonitor=false"}, args...)...)
	cmd.Env = repositoryEnvironment(env)
	cmd.Stdin = input
	cmd.WaitDelay = time.Second
	var output repositoryOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	if output.exceeded {
		return "", errors.New("Git output exceeds repository inspection limit")
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", errors.New("Git command timed out")
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "", errors.New("Git is not installed")
	}
	if err != nil {
		return "", repositoryCommandError(err, output.String())
	}
	return strings.TrimSuffix(output.String(), "\n"), nil
}
