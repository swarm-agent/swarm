package worktree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/gitenv"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/lock"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

const (
	gitCommandTimeout           = 12 * time.Second
	defaultWorktreeBranchName   = "agent/<id>"
	defaultWorktreeBranchPrefix = "agent"
	worktreeBranchIDPlaceholder = "<id>"
)

const detachedWorkspaceFallbackWarning = "Opened without git worktree support; use a git repository and make sure git is installed for the app to work properly."

var validWorktreeWorkspace = regexp.MustCompile(`^(ws_)?[a-z0-9][a-z0-9-]*$`)

func DetachedWorkspaceFallbackWarning(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(text, "not a git repository") {
		return detachedWorkspaceFallbackWarning
	}
	if strings.Contains(text, "executable file not found") && strings.Contains(text, "git") {
		return detachedWorkspaceFallbackWarning
	}
	return ""
}

type Config struct {
	WorkspacePath    string `json:"workspace_path,omitempty"`
	UseCurrentBranch bool   `json:"use_current_branch"`
	BaseBranch       string `json:"base_branch,omitempty"`
	BranchName       string `json:"branch_name,omitempty"`
	UpdatedAt        int64  `json:"updated_at"`
}

type TaskBase struct {
	RepoRoot     string `json:"repo_root"`
	ParentBranch string `json:"parent_branch"`
	BaseCommit   string `json:"base_commit"`
}

type Allocation struct {
	WorkspacePath string `json:"workspace_path"`
	RepoRoot      string `json:"repo_root"`
	BaseBranch    string `json:"base_branch"`
	BaseCommit    string `json:"base_commit,omitempty"`
	BranchName    string `json:"branch_name,omitempty"`
	WorkspaceID   string `json:"workspace_id,omitempty"`
}

// RequestedWorktreeNameConflictError marks an exact requested branch/worktree
// name that cannot be allocated because it is already in use.
type RequestedWorktreeNameConflictError struct {
	WorktreeName string
	Cause        error
}

func (e *RequestedWorktreeNameConflictError) Error() string {
	if e == nil {
		return "requested worktree name is already taken"
	}
	name := strings.TrimSpace(e.WorktreeName)
	if e.Cause == nil {
		return fmt.Sprintf("worktree name %q is already taken", name)
	}
	return fmt.Sprintf("worktree name %q is already taken: %v", name, e.Cause)
}

func (e *RequestedWorktreeNameConflictError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func IsRequestedWorktreeNameConflict(err error) bool {
	var conflict *RequestedWorktreeNameConflictError
	return errors.As(err, &conflict)
}

type TaskWorkspaceState struct {
	WorkspacePath string `json:"workspace_path"`
	BranchName    string `json:"branch_name"`
	HeadCommit    string `json:"head_commit"`
	Status        string `json:"status"`
	Clean         bool   `json:"clean"`
}

type TaskIntegrationChild struct {
	SessionID   string   `json:"session_id"`
	BaseCommit  string   `json:"base_commit"`
	HeadCommit  string   `json:"head_commit"`
	OwnedScopes []string `json:"owned_scopes,omitempty"`
}

type TaskIntegrationEntry struct {
	SessionID  string   `json:"session_id"`
	BaseCommit string   `json:"base_commit"`
	HeadCommit string   `json:"head_commit"`
	Commits    []string `json:"commits"`
	Files      []string `json:"files"`
}

type TaskIntegrationPlan struct {
	ParentHead string                 `json:"parent_head"`
	Entries    []TaskIntegrationEntry `json:"entries"`
	Commits    []string               `json:"commits"`
	Overlaps   []string               `json:"overlaps,omitempty"`
}

type TaskIntegrationResult struct {
	TaskIntegrationPlan
	ResultingParentHead string `json:"resulting_parent_head"`
}

type TaskIntegrationConflictError struct {
	Commit string `json:"commit"`
	Detail string `json:"detail"`
}

func (e *TaskIntegrationConflictError) Error() string {
	if e == nil {
		return "integration conflict"
	}
	return fmt.Sprintf("integration conflict at commit %s: %s", e.Commit, e.Detail)
}

type ManagedWorktree struct {
	Path        string `json:"path"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Detached    bool   `json:"detached,omitempty"`
	Exists      bool   `json:"exists"`
	Managed     bool   `json:"managed"`
}

type PruneResult struct {
	Root    string   `json:"root"`
	Removed []string `json:"removed,omitempty"`
	Skipped []string `json:"skipped,omitempty"`
}

var errAccountOwnedWorkspaceRequired = errors.New("account-owned workspace path is required")

type Service struct {
	store     *pebblestore.WorktreeStore
	workspace *workspaceruntime.Service
	events    *pebblestore.EventLog
	mu        sync.Mutex
}

func NewService(store *pebblestore.WorktreeStore, workspace *workspaceruntime.Service, events *pebblestore.EventLog) *Service {
	return &Service{store: store, workspace: workspace, events: events}
}

func (s *Service) GetConfig(workspacePath string) (Config, error) {
	canonical, err := s.resolveWorkspaceConfigPath(workspacePath)
	if err != nil {
		return Config{}, err
	}
	record, _, err := s.store.GetConfigLegacy(canonical)
	if err != nil {
		return Config{}, fmt.Errorf("read legacy worktree config: %w", err)
	}
	useCurrentBranch := record.UseCurrentBranch != nil && *record.UseCurrentBranch
	return Config{
		WorkspacePath:    canonical,
		UseCurrentBranch: useCurrentBranch,
		BaseBranch:       strings.TrimSpace(record.BaseBranch),
		BranchName:       normalizeWorktreeBranchPrefix(record.BranchName),
		UpdatedAt:        record.UpdatedAt,
	}, nil
}

func (s *Service) GetConfigForPrincipal(principal identity.Principal, workspacePath string) (Config, error) {
	if err := requirePrincipal(principal); err != nil {
		return Config{}, err
	}
	canonical, matched, err := s.resolveWorkspaceConfigPathForPrincipalOptional(principal, workspacePath)
	if err != nil {
		return Config{}, err
	}
	if !matched {
		return defaultConfigForWorkspace(canonical), nil
	}
	record, _, err := s.store.GetConfigForAccount(principal.AccountScopeID, canonical)
	if err != nil {
		return Config{}, fmt.Errorf("read worktree config: %w", err)
	}
	return configFromRecord(canonical, record), nil
}

func (s *Service) SetConfig(workspacePath string, useCurrentBranch bool, baseBranch, branchName string) (Config, *pebblestore.EventEnvelope, error) {
	return Config{}, nil, identity.ErrPrincipalRequired
}

func (s *Service) SetConfigForPrincipal(principal identity.Principal, workspacePath string, useCurrentBranch bool, baseBranch, branchName string) (Config, *pebblestore.EventEnvelope, error) {
	if err := requirePrincipal(principal); err != nil {
		return Config{}, nil, err
	}
	canonical, err := s.resolveWorkspaceConfigPathForPrincipal(principal, workspacePath)
	if err != nil {
		return Config{}, nil, err
	}
	record, err := s.store.SetConfigForAccount(principal.AccountScopeID, canonical, useCurrentBranch, baseBranch, branchName)
	if err != nil {
		return Config{}, nil, fmt.Errorf("persist worktree config: %w", err)
	}
	cfg := Config{
		WorkspacePath:    canonical,
		UseCurrentBranch: record.UseCurrentBranch != nil && *record.UseCurrentBranch,
		BaseBranch:       strings.TrimSpace(record.BaseBranch),
		BranchName:       normalizeWorktreeBranchPrefix(record.BranchName),
		UpdatedAt:        record.UpdatedAt,
	}
	if s.events == nil {
		return cfg, nil, nil
	}
	payload, err := json.Marshal(cfg)
	if err != nil {
		return Config{}, nil, fmt.Errorf("marshal worktree config event: %w", err)
	}
	env, err := s.events.Append("system:worktrees", "worktrees.config.updated", canonical, payload, "", "")
	if err != nil {
		return Config{}, nil, err
	}
	return cfg, &env, nil
}

func (s *Service) AllocateDetachedWorkspace(workspacePath, nameSeed string) (Allocation, error) {
	return Allocation{}, identity.ErrPrincipalRequired
}

func (s *Service) AllocateDetachedWorkspaceForPrincipal(principal identity.Principal, workspacePath, nameSeed string) (Allocation, error) {
	if err := requirePrincipal(principal); err != nil {
		return Allocation{}, err
	}
	config, err := s.GetConfigForPrincipal(principal, workspacePath)
	if err != nil {
		return Allocation{}, err
	}
	return s.allocateSessionWorkspace(config.WorkspacePath, config.UseCurrentBranch, config.BaseBranch, config.BranchName, nameSeed)
}

func (s *Service) AllocateDetachedWorkspaceRequested(workspacePath, nameSeed, baseBranch, branchName string) (Allocation, error) {
	return Allocation{}, identity.ErrPrincipalRequired
}

func (s *Service) AllocateDetachedWorkspaceRequestedForPrincipal(principal identity.Principal, workspacePath, nameSeed, baseBranch, branchName string) (Allocation, error) {
	if err := requirePrincipal(principal); err != nil {
		return Allocation{}, err
	}
	canonical, err := s.resolveWorkspaceConfigPathForPrincipal(principal, workspacePath)
	if err != nil {
		return Allocation{}, err
	}
	useCurrentBranch := strings.TrimSpace(baseBranch) == ""
	return s.allocateSessionWorkspaceWithBranchMode(canonical, useCurrentBranch, baseBranch, branchName, nameSeed, true)
}

func (s *Service) allocateSessionWorkspace(workspacePath string, useCurrentBranch bool, baseBranch, configuredBranchName, sessionID string) (Allocation, error) {
	return s.allocateSessionWorkspaceWithBranchMode(workspacePath, useCurrentBranch, baseBranch, configuredBranchName, sessionID, false)
}

func (s *Service) allocateSessionWorkspaceWithBranchMode(workspacePath string, useCurrentBranch bool, baseBranch, configuredBranchName, sessionID string, exactBranchName bool) (Allocation, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	sessionID = strings.TrimSpace(sessionID)
	if workspacePath == "" {
		return Allocation{}, errors.New("workspace path is required")
	}
	if sessionID == "" {
		return Allocation{}, errors.New("session id is required")
	}

	repoRoot, err := resolveRepositoryRoot(workspacePath)
	if err != nil {
		return Allocation{}, err
	}
	effectiveBranch, err := resolveEffectiveBaseBranch(workspacePath, useCurrentBranch, baseBranch)
	if err != nil {
		return Allocation{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	branchName := strings.TrimSpace(configuredBranchName)
	workspaceID := sessionWorkspaceID(sessionID)
	if !exactBranchName {
		branchName = effectiveWorktreeBranchName(configuredBranchName, sessionID)
	} else {
		var workspaceIDErr error
		workspaceID, workspaceIDErr = workspaceIdentityForRequestedBranch(branchName)
		if workspaceIDErr != nil {
			return Allocation{}, workspaceIDErr
		}
	}
	if branchName == "" {
		return Allocation{}, errors.New("worktree branch name is required")
	}
	worktreePath, err := deterministicSessionWorktreePath(repoRoot, workspaceID)
	if err != nil {
		return Allocation{}, err
	}
	if err := ensureWorktreeParent(repoRoot); err != nil {
		return Allocation{}, err
	}
	if _, statErr := os.Stat(worktreePath); statErr == nil {
		cause := fmt.Errorf("target worktree path %q already exists", worktreePath)
		if exactBranchName {
			return Allocation{}, &RequestedWorktreeNameConflictError{WorktreeName: branchName, Cause: cause}
		}
		return Allocation{}, cause
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Allocation{}, fmt.Errorf("check target worktree path: %w", statErr)
	}
	branchExisted, branchErr := localBranchExists(repoRoot, branchName)
	if branchErr != nil {
		return Allocation{}, fmt.Errorf("check worktree branch: %w", branchErr)
	}
	if exactBranchName && branchExisted {
		return Allocation{}, &RequestedWorktreeNameConflictError{
			WorktreeName: branchName,
			Cause:        fmt.Errorf("branch %q already exists", branchName),
		}
	}
	if _, err := runGitWorktreeAdd(repoRoot, worktreePath, branchName, effectiveBranch); err != nil {
		cleanupErr := cleanupFailedWorktreeAllocation(repoRoot, worktreePath)
		if !branchExisted {
			cleanupErr = errors.Join(cleanupErr, cleanupPartialBranch(repoRoot, branchName, effectiveBranch))
		}
		cause := allocationFailureWithCleanup(err, cleanupErr)
		if exactBranchName && isExactRequestedWorktreeConflict(err, branchName, worktreePath) {
			return Allocation{}, &RequestedWorktreeNameConflictError{WorktreeName: branchName, Cause: cause}
		}
		return Allocation{}, fmt.Errorf("create session worktree: %w", cause)
	}
	if err := os.Chmod(worktreePath, appstorage.PrivateDirPerm); err != nil {
		cleanupErr := cleanupAllocatedWorktree(repoRoot, worktreePath, branchName)
		return Allocation{}, fmt.Errorf("set worktree directory permissions: %w", allocationFailureWithCleanup(err, cleanupErr))
	}
	return Allocation{
		WorkspacePath: worktreePath,
		RepoRoot:      repoRoot,
		BaseBranch:    effectiveBranch,
		BranchName:    branchName,
		WorkspaceID:   workspaceID,
	}, nil
}

func (s *Service) ResolveTaskBase(workspacePath string) (TaskBase, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return TaskBase{}, errors.New("workspace path is required")
	}
	repoRoot, err := resolveRepositoryRoot(workspacePath)
	if err != nil {
		return TaskBase{}, err
	}
	branch, err := currentBranch(workspacePath)
	if err != nil {
		return TaskBase{}, fmt.Errorf("detect current branch: %w", err)
	}
	if strings.TrimSpace(branch) == "" {
		return TaskBase{}, errors.New("detect current branch: repository is in detached HEAD state")
	}
	status, err := runGit(workspacePath, "status", "--short", "--untracked-files=all")
	if err != nil {
		return TaskBase{}, fmt.Errorf("inspect parent worktree status: %w", err)
	}
	if status = strings.TrimSpace(status); status != "" {
		return TaskBase{}, fmt.Errorf("parent worktree has uncommitted changes; commit or checkpoint required work before launching Clone:\n%s", status)
	}
	commit, err := runGit(workspacePath, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return TaskBase{}, fmt.Errorf("resolve parent HEAD: %w", err)
	}
	if strings.TrimSpace(commit) == "" {
		return TaskBase{}, errors.New("resolve parent HEAD: empty commit")
	}
	return TaskBase{RepoRoot: repoRoot, ParentBranch: branch, BaseCommit: commit}, nil
}

func (s *Service) TaskCommitDescendsFrom(workspacePath, baseCommit, headCommit string) (bool, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	baseCommit = strings.TrimSpace(baseCommit)
	headCommit = strings.TrimSpace(headCommit)
	if workspacePath == "" || !validCommitID(baseCommit) || !validCommitID(headCommit) {
		return false, errors.New("workspace path and full hexadecimal base/head commit ids are required")
	}
	cmd := exec.Command("git", "merge-base", "--is-ancestor", baseCommit, headCommit)
	cmd.Dir = workspacePath
	if output, err := cmd.CombinedOutput(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("validate task commit ancestry: %s", strings.TrimSpace(string(output)))
	}
	return true, nil
}

func (s *Service) PrepareTaskIntegration(parentPath, expectedParentHead string, children []TaskIntegrationChild) (TaskIntegrationPlan, error) {
	parentPath = strings.TrimSpace(parentPath)
	expectedParentHead = strings.TrimSpace(expectedParentHead)
	if parentPath == "" || expectedParentHead == "" || len(children) == 0 {
		return TaskIntegrationPlan{}, errors.New("parent path, expected parent HEAD, and at least one child are required")
	}
	state, err := s.InspectTaskWorkspace(parentPath)
	if err != nil {
		return TaskIntegrationPlan{}, fmt.Errorf("inspect parent worktree: %w", err)
	}
	if !state.Clean {
		return TaskIntegrationPlan{}, fmt.Errorf("parent worktree is dirty:\n%s", state.Status)
	}
	if !validCommitID(expectedParentHead) {
		return TaskIntegrationPlan{}, errors.New("expected parent HEAD must be a full hexadecimal commit id")
	}
	if state.HeadCommit != expectedParentHead {
		return TaskIntegrationPlan{}, fmt.Errorf("stale parent HEAD: expected %s, found %s", expectedParentHead, state.HeadCommit)
	}
	plan := TaskIntegrationPlan{ParentHead: state.HeadCommit}
	owners := map[string]string{}
	for _, child := range children {
		child.SessionID = strings.TrimSpace(child.SessionID)
		child.BaseCommit = strings.TrimSpace(child.BaseCommit)
		child.HeadCommit = strings.TrimSpace(child.HeadCommit)
		if child.SessionID == "" || child.BaseCommit == "" || child.HeadCommit == "" || child.BaseCommit == child.HeadCommit {
			return TaskIntegrationPlan{}, fmt.Errorf("child %q has incomplete committed lineage", child.SessionID)
		}
		descends, ancestryErr := s.TaskCommitDescendsFrom(parentPath, child.BaseCommit, child.HeadCommit)
		if ancestryErr != nil || !descends {
			if ancestryErr != nil {
				return TaskIntegrationPlan{}, ancestryErr
			}
			return TaskIntegrationPlan{}, fmt.Errorf("child %q HEAD does not descend from its recorded base", child.SessionID)
		}
		commitText, err := runGit(parentPath, "rev-list", "--reverse", child.BaseCommit+".."+child.HeadCommit)
		if err != nil {
			return TaskIntegrationPlan{}, fmt.Errorf("list child %q commits: %w", child.SessionID, err)
		}
		commits := strings.Fields(commitText)
		if len(commits) == 0 {
			return TaskIntegrationPlan{}, fmt.Errorf("child %q has no commits", child.SessionID)
		}
		fileText, err := runGit(parentPath, "diff", "--name-only", child.BaseCommit+".."+child.HeadCommit)
		if err != nil {
			return TaskIntegrationPlan{}, fmt.Errorf("list child %q files: %w", child.SessionID, err)
		}
		files := strings.Fields(fileText)
		for _, file := range files {
			if owner, ok := owners[file]; ok && owner != child.SessionID {
				plan.Overlaps = append(plan.Overlaps, fmt.Sprintf("%s: %s, %s", file, owner, child.SessionID))
			} else {
				owners[file] = child.SessionID
			}
		}
		plan.Entries = append(plan.Entries, TaskIntegrationEntry{SessionID: child.SessionID, BaseCommit: child.BaseCommit, HeadCommit: child.HeadCommit, Commits: commits, Files: files})
		plan.Commits = append(plan.Commits, commits...)
	}
	if err := preflightCherryPick(parentPath, plan.ParentHead, plan.Commits); err != nil {
		return TaskIntegrationPlan{}, err
	}
	return plan, nil
}

func (s *Service) ApplyTaskIntegration(parentPath string, plan TaskIntegrationPlan) (TaskIntegrationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	integrationLock, err := acquireIntegrationLock(parentPath)
	if err != nil {
		return TaskIntegrationResult{}, err
	}
	defer integrationLock.Release()

	current, err := s.PrepareTaskIntegration(parentPath, plan.ParentHead, integrationChildrenFromPlan(plan))
	if err != nil {
		return TaskIntegrationResult{}, err
	}
	if strings.Join(current.Commits, "\x00") != strings.Join(plan.Commits, "\x00") {
		return TaskIntegrationResult{}, errors.New("integration manifest became stale")
	}
	cherryPickArgs := append([]string{"cherry-pick"}, current.Commits...)
	if _, err := runGitWithEnv(parentPath, gitenv.FilterIdentityOverrides(os.Environ()), cherryPickArgs...); err != nil {
		rollbackErr := rollbackTaskIntegration(parentPath, current.ParentHead)
		if rollbackErr != nil {
			return TaskIntegrationResult{TaskIntegrationPlan: current}, fmt.Errorf("selected batch failed after preflight: %w; rollback failed: %v", err, rollbackErr)
		}
		return TaskIntegrationResult{TaskIntegrationPlan: current}, fmt.Errorf("selected batch failed after preflight; full batch rolled back: %w", err)
	}
	head, err := runGit(parentPath, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return TaskIntegrationResult{TaskIntegrationPlan: current}, fmt.Errorf("resolve integrated HEAD: %w", err)
	}
	return TaskIntegrationResult{TaskIntegrationPlan: current, ResultingParentHead: head}, nil
}

func rollbackTaskIntegration(parentPath, parentHead string) error {
	// One multi-commit cherry-pick gives Git a single sequencer transaction;
	// abort restores its original HEAD and index without an unconditional hard
	// reset that could overwrite independently created parent work.
	if _, err := runGit(parentPath, "cherry-pick", "--abort"); err != nil {
		return fmt.Errorf("abort selected batch: %w", err)
	}
	state, err := (&Service{}).InspectTaskWorkspace(parentPath)
	if err != nil {
		return fmt.Errorf("inspect parent after rollback: %w", err)
	}
	if state.HeadCommit != parentHead || !state.Clean {
		return fmt.Errorf("selected batch abort did not restore clean parent HEAD %s", parentHead)
	}
	return nil
}

func acquireIntegrationLock(parentPath string) (*lock.FileLock, error) {
	gitDir, err := runGit(parentPath, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("resolve repository lock directory: %w", err)
	}
	gitDir, err = resolveGitPath(parentPath, gitDir)
	if err != nil {
		return nil, fmt.Errorf("resolve repository lock path: %w", err)
	}
	fileLock, err := lock.Acquire(filepath.Join(gitDir, "swarm-integration.lock"), lock.Metadata{PID: os.Getpid(), StartedAt: time.Now().UnixMilli()})
	if err != nil {
		if errors.Is(err, lock.ErrAlreadyRunning) {
			return nil, errors.New("another Swarm integration owns this repository; retry after it completes")
		}
		return nil, fmt.Errorf("acquire repository integration lock: %w", err)
	}
	return fileLock, nil
}

func integrationChildrenFromPlan(plan TaskIntegrationPlan) []TaskIntegrationChild {
	out := make([]TaskIntegrationChild, 0, len(plan.Entries))
	for _, entry := range plan.Entries {
		out = append(out, TaskIntegrationChild{SessionID: entry.SessionID, BaseCommit: entry.BaseCommit, HeadCommit: entry.HeadCommit})
	}
	return out
}

func preflightCherryPick(parentPath, parentHead string, commits []string) error {
	gitDir, err := runGit(parentPath, "rev-parse", "--git-dir")
	if err != nil {
		return fmt.Errorf("resolve git directory for integration preflight: %w", err)
	}
	gitDir, err = resolveGitPath(parentPath, gitDir)
	if err != nil {
		return fmt.Errorf("resolve integration preflight git directory path: %w", err)
	}
	index, err := os.CreateTemp(gitDir, "swarm-integration-index-*")
	if err != nil {
		return fmt.Errorf("create integration preflight index: %w", err)
	}
	indexPath := index.Name()
	_ = index.Close()
	_ = os.Remove(indexPath)
	defer os.Remove(indexPath)
	env := append(os.Environ(), "GIT_INDEX_FILE="+indexPath)
	cmd := exec.Command("git", "-C", parentPath, "read-tree", parentHead)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("initialize integration preflight: %s", strings.TrimSpace(string(out)))
	}
	for _, commit := range commits {
		patch := exec.Command("git", "-C", parentPath, "diff-tree", "--binary", "--full-index", "--no-commit-id", "-p", commit+"^", commit)
		data, err := patch.Output()
		if err != nil {
			return fmt.Errorf("read commit %s patch: %w", commit, err)
		}
		apply := exec.Command("git", "-C", parentPath, "apply", "--cached", "--3way", "--whitespace=nowarn", "-")
		apply.Env = env
		apply.Stdin = bytes.NewReader(data)
		if out, err := apply.CombinedOutput(); err != nil {
			detail := strings.TrimSpace(string(out))
			if detail == "" {
				detail = err.Error()
			}
			return &TaskIntegrationConflictError{Commit: commit, Detail: detail}
		}
	}
	return nil
}

func (s *Service) InspectTaskWorkspace(workspacePath string) (TaskWorkspaceState, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return TaskWorkspaceState{}, errors.New("workspace path is required")
	}
	branch, err := currentBranch(workspacePath)
	if err != nil {
		return TaskWorkspaceState{}, fmt.Errorf("detect task branch: %w", err)
	}
	head, err := runGit(workspacePath, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return TaskWorkspaceState{}, fmt.Errorf("resolve task HEAD: %w", err)
	}
	status, err := runGit(workspacePath, "status", "--short", "--untracked-files=all")
	if err != nil {
		return TaskWorkspaceState{}, fmt.Errorf("inspect task worktree status: %w", err)
	}
	status = strings.TrimSpace(status)
	return TaskWorkspaceState{
		WorkspacePath: workspacePath,
		BranchName:    strings.TrimSpace(branch),
		HeadCommit:    strings.TrimSpace(head),
		Status:        status,
		Clean:         status == "",
	}, nil
}

func (s *Service) AllocateTaskWorkspace(workspacePath string, base TaskBase, nameSeed string) (Allocation, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return Allocation{}, errors.New("workspace path is required")
	}
	if strings.TrimSpace(base.RepoRoot) == "" || strings.TrimSpace(base.ParentBranch) == "" || strings.TrimSpace(base.BaseCommit) == "" {
		return Allocation{}, errors.New("task base requires repository root, parent branch, and base commit")
	}
	resolvedRoot, err := resolveRepositoryRoot(workspacePath)
	if err != nil {
		return Allocation{}, err
	}
	if !sameCleanPath(resolvedRoot, base.RepoRoot) {
		return Allocation{}, fmt.Errorf("task base repository %q does not match workspace repository %q", base.RepoRoot, resolvedRoot)
	}
	allocation, err := s.allocateSessionWorkspace(workspacePath, false, base.BaseCommit, "", nameSeed)
	if err != nil {
		return Allocation{}, err
	}
	allocation.BaseBranch = strings.TrimSpace(base.ParentBranch)
	allocation.BaseCommit = strings.TrimSpace(base.BaseCommit)
	return allocation, nil
}

func (s *Service) ListManaged(workspacePath string) ([]ManagedWorktree, error) {
	return nil, identity.ErrPrincipalRequired
}

func (s *Service) ListManagedForPrincipal(principal identity.Principal, workspacePath string) ([]ManagedWorktree, error) {
	workspacePath, err := s.resolveWorkspaceConfigPathForPrincipal(principal, workspacePath)
	if err != nil {
		return nil, err
	}
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return nil, errors.New("workspace path is required")
	}
	repoRoot, err := resolveRepositoryRoot(workspacePath)
	if err != nil {
		return nil, err
	}
	root, err := worktreeCacheRoot(repoRoot)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return []ManagedWorktree{}, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat managed worktree root: %w", err)
	}
	output, err := runGit(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("list git worktrees: %w", err)
	}
	entries := parseWorktreeList(output)
	managed := make([]ManagedWorktree, 0)
	for _, entry := range entries {
		path := filepath.Clean(strings.TrimSpace(entry.Path))
		if path == "" || !pathWithinRoot(root, path) {
			continue
		}
		managed = append(managed, ManagedWorktree{
			Path:        path,
			WorkspaceID: filepath.Base(path),
			Branch:      normalizeGitWorktreeBranch(entry.Branch),
			Detached:    entry.Detached,
			Exists:      pathExists(path),
			Managed:     true,
		})
	}
	return managed, nil
}

func (s *Service) PruneManaged(workspacePath string) (PruneResult, error) {
	return PruneResult{}, identity.ErrPrincipalRequired
}

func (s *Service) PruneManagedForPrincipal(principal identity.Principal, workspacePath string) (PruneResult, error) {
	workspacePath, err := s.resolveWorkspaceConfigPathForPrincipal(principal, workspacePath)
	if err != nil {
		return PruneResult{}, err
	}
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return PruneResult{}, errors.New("workspace path is required")
	}
	repoRoot, err := resolveRepositoryRoot(workspacePath)
	if err != nil {
		return PruneResult{}, err
	}
	root, err := worktreeCacheRoot(repoRoot)
	if err != nil {
		return PruneResult{}, err
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return PruneResult{Root: root}, nil
	} else if err != nil {
		return PruneResult{}, fmt.Errorf("stat managed worktree root: %w", err)
	}
	entries, err := s.ListManagedForPrincipal(principal, repoRoot)
	if err != nil {
		return PruneResult{}, err
	}
	result := PruneResult{Root: root}
	for _, entry := range entries {
		path := filepath.Clean(strings.TrimSpace(entry.Path))
		if path == "" || !pathWithinRoot(root, path) {
			result.Skipped = append(result.Skipped, path)
			continue
		}
		if entry.Exists {
			result.Skipped = append(result.Skipped, path)
			continue
		}
		if _, err := runGit(repoRoot, "worktree", "remove", "--force", path); err != nil {
			if _, pruneErr := runGit(repoRoot, "worktree", "prune"); pruneErr != nil {
				return result, fmt.Errorf("remove managed worktree metadata %q: %w", path, err)
			}
		}
		result.Removed = append(result.Removed, path)
	}
	return result, nil
}

func (s *Service) AttachBranch(workspacePath, sessionID, title string) (string, error) {
	return s.MoveWorkspaceToTitle(workspacePath, sessionID, title)
}

func (s *Service) MoveWorkspaceToTitle(workspacePath, sessionID, title string) (string, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	sessionID = strings.TrimSpace(sessionID)
	title = strings.TrimSpace(title)
	if workspacePath == "" {
		return "", errors.New("workspace path is required")
	}
	if sessionID == "" {
		return "", errors.New("session id is required")
	}

	repoRoot, err := resolveRepositoryRoot(workspacePath)
	if err != nil {
		return "", err
	}
	targetPath, err := deterministicWorktreePath(repoRoot, sessionID, title)
	if err != nil {
		return "", err
	}
	if err := ensureWorktreeParent(repoRoot); err != nil {
		return "", err
	}
	if sameCleanPath(workspacePath, targetPath) {
		return workspacePath, nil
	}
	if _, statErr := os.Stat(targetPath); statErr == nil {
		return "", fmt.Errorf("target worktree path %q already exists", targetPath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("check target worktree path: %w", statErr)
	}
	if _, err := runGit(repoRoot, "worktree", "move", workspacePath, targetPath); err != nil {
		return "", fmt.Errorf("move worktree path: %w", err)
	}
	if err := os.Chmod(targetPath, appstorage.PrivateDirPerm); err != nil {
		return "", fmt.Errorf("set worktree directory permissions: %w", err)
	}
	return targetPath, nil
}

func (s *Service) resolveWorkspaceConfigPath(workspacePath string) (string, error) {
	trimmed := strings.TrimSpace(workspacePath)
	if trimmed == "" {
		return "", errors.New("workspace path is required")
	}
	resolved, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func (s *Service) resolveWorkspaceConfigPathForPrincipal(principal identity.Principal, workspacePath string) (string, error) {
	canonical, matched, err := s.resolveWorkspaceConfigPathForPrincipalOptional(principal, workspacePath)
	if err != nil {
		return "", err
	}
	if !matched {
		return "", errAccountOwnedWorkspaceRequired
	}
	return canonical, nil
}

func (s *Service) resolveWorkspaceConfigPathForPrincipalOptional(principal identity.Principal, workspacePath string) (string, bool, error) {
	if err := requirePrincipal(principal); err != nil {
		return "", false, err
	}
	trimmed := strings.TrimSpace(workspacePath)
	if trimmed == "" {
		if s == nil || s.workspace == nil {
			return "", false, errors.New("workspace path is required")
		}
		current, ok, err := s.workspace.CurrentBindingForPrincipal(principal)
		if err != nil {
			return "", false, fmt.Errorf("resolve current workspace: %w", err)
		}
		if !ok {
			return "", false, errors.New("workspace path is required")
		}
		trimmed = strings.TrimSpace(current.ResolvedPath)
	}
	if trimmed == "" {
		return "", false, errors.New("workspace path is required")
	}
	if s != nil && s.workspace != nil {
		scope, err := s.workspace.ScopeForPathForPrincipal(principal, trimmed)
		if err != nil {
			return "", false, fmt.Errorf("resolve workspace scope: %w", err)
		}
		if scope.Matched && strings.TrimSpace(scope.WorkspacePath) != "" {
			return strings.TrimSpace(scope.WorkspacePath), true, nil
		}
		resolved := strings.TrimSpace(scope.ResolvedPath)
		if resolved == "" {
			resolved, err = filepath.Abs(trimmed)
			if err != nil {
				return "", false, fmt.Errorf("resolve workspace path: %w", err)
			}
		}
		return filepath.Clean(resolved), false, nil
	}
	resolved, err := filepath.Abs(trimmed)
	if err != nil {
		return "", false, fmt.Errorf("resolve workspace path: %w", err)
	}
	return filepath.Clean(resolved), true, nil
}

func defaultConfigForWorkspace(workspacePath string) Config {
	return Config{WorkspacePath: workspacePath, UseCurrentBranch: true, BranchName: defaultWorktreeBranchName}
}

func configFromRecord(workspacePath string, record pebblestore.WorktreeConfigRecord) Config {
	useCurrentBranch := record.UseCurrentBranch != nil && *record.UseCurrentBranch
	return Config{
		WorkspacePath:    workspacePath,
		UseCurrentBranch: useCurrentBranch,
		BaseBranch:       strings.TrimSpace(record.BaseBranch),
		BranchName:       normalizeWorktreeBranchPrefix(record.BranchName),
		UpdatedAt:        record.UpdatedAt,
	}
}

func requirePrincipal(principal identity.Principal) error {
	if !principal.Valid() {
		return identity.ErrPrincipalRequired
	}
	return nil
}

func resolveRepositoryRoot(workspacePath string) (string, error) {
	// In linked worktrees, --show-toplevel points at the active worktree path.
	// Anchor deterministic worktree paths under the shared repository root instead.
	commonDir, err := runGit(workspacePath, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("resolve git common dir: %w", err)
	}
	commonDir, err = resolveGitPath(workspacePath, commonDir)
	if err != nil {
		return "", fmt.Errorf("resolve git common dir path: %w", err)
	}
	if filepath.Base(commonDir) == ".git" {
		root := strings.TrimSpace(filepath.Dir(commonDir))
		if root == "" {
			return "", errors.New("git repository root is empty")
		}
		return root, nil
	}

	root, err := runGit(workspacePath, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	root, err = resolveGitPath(workspacePath, root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root path: %w", err)
	}
	if root == "" {
		return "", errors.New("git repository root is empty")
	}
	return root, nil
}

func (s *Service) VerifyTaskIntegrationWorkspace(parentPath, childPath, sessionID, branchName, baseCommit, headCommit string) (TaskWorkspaceState, error) {
	parentRoot, err := resolveRepositoryRoot(parentPath)
	if err != nil {
		return TaskWorkspaceState{}, fmt.Errorf("resolve parent repository: %w", err)
	}
	expectedPath, err := deterministicSessionWorktreePath(parentRoot, sessionWorkspaceID(sessionID))
	if err != nil {
		return TaskWorkspaceState{}, err
	}
	actualPath, err := filepath.EvalSymlinks(strings.TrimSpace(childPath))
	if err != nil {
		return TaskWorkspaceState{}, fmt.Errorf("canonicalize child worktree: %w", err)
	}
	expectedPath, err = filepath.EvalSymlinks(expectedPath)
	if err != nil {
		return TaskWorkspaceState{}, fmt.Errorf("canonicalize managed child worktree: %w", err)
	}
	managedRoot, err := worktreeCacheRoot(parentRoot)
	if err != nil {
		return TaskWorkspaceState{}, err
	}
	managedRoot, err = filepath.EvalSymlinks(managedRoot)
	if err != nil {
		return TaskWorkspaceState{}, fmt.Errorf("canonicalize managed worktree root: %w", err)
	}
	if !sameCleanPath(actualPath, expectedPath) || !pathWithinRoot(managedRoot, actualPath) {
		return TaskWorkspaceState{}, errors.New("child worktree is outside its expected private managed path")
	}
	childRoot, err := resolveRepositoryRoot(actualPath)
	if err != nil || !sameCleanPath(childRoot, parentRoot) {
		return TaskWorkspaceState{}, errors.New("child worktree does not belong to the parent repository")
	}
	branchName = strings.TrimSpace(branchName)
	if _, err := runGit(parentRoot, "check-ref-format", "--branch", branchName); err != nil {
		return TaskWorkspaceState{}, fmt.Errorf("invalid child branch: %w", err)
	}
	if !validCommitID(baseCommit) || !validCommitID(headCommit) {
		return TaskWorkspaceState{}, errors.New("child lineage requires full hexadecimal commit ids")
	}
	for label, commit := range map[string]string{"base": baseCommit, "head": headCommit} {
		if _, err := runGit(parentRoot, "cat-file", "-e", commit+"^{commit}"); err != nil {
			return TaskWorkspaceState{}, fmt.Errorf("invalid child %s commit: %w", label, err)
		}
	}
	branchHead, err := runGit(parentRoot, "rev-parse", "--verify", "refs/heads/"+branchName+"^{commit}")
	if err != nil || branchHead != headCommit {
		return TaskWorkspaceState{}, errors.New("child branch does not resolve to its immutable recorded HEAD")
	}
	state, err := s.InspectTaskWorkspace(actualPath)
	if err != nil {
		return TaskWorkspaceState{}, err
	}
	if state.BranchName != branchName || state.HeadCommit != headCommit {
		return TaskWorkspaceState{}, errors.New("child worktree no longer matches its branch and HEAD lineage")
	}
	return state, nil
}

func validCommitID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

func resolveGitPath(basePath, reportedPath string) (string, error) {
	basePath = strings.TrimSpace(basePath)
	reportedPath = strings.TrimSpace(reportedPath)
	if reportedPath == "" {
		return "", errors.New("git path is empty")
	}
	if !filepath.IsAbs(reportedPath) {
		if basePath == "" {
			return "", errors.New("base path is required for relative git path")
		}
		reportedPath = filepath.Join(basePath, reportedPath)
	}
	resolvedPath, err := filepath.Abs(reportedPath)
	if err != nil {
		return "", fmt.Errorf("resolve absolute git path: %w", err)
	}
	return filepath.Clean(resolvedPath), nil
}

type gitWorktreeListEntry struct {
	Path     string
	Branch   string
	Detached bool
}

func normalizeGitWorktreeBranch(branch string) string {
	branch = strings.TrimSpace(branch)
	branch = strings.TrimPrefix(branch, "refs/heads/")
	return strings.TrimSpace(branch)
}

func parseWorktreeList(output string) []gitWorktreeListEntry {
	var entries []gitWorktreeListEntry
	var current *gitWorktreeListEntry
	flush := func() {
		if current == nil || strings.TrimSpace(current.Path) == "" {
			current = nil
			return
		}
		entries = append(entries, *current)
		current = nil
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "worktree ") {
			flush()
			current = &gitWorktreeListEntry{Path: strings.TrimSpace(strings.TrimPrefix(line, "worktree "))}
			continue
		}
		if current == nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, "branch "):
			current.Branch = strings.TrimSpace(strings.TrimPrefix(line, "branch "))
		case line == "detached":
			current.Detached = true
		}
	}
	flush()
	return entries
}

func pathWithinRoot(root, target string) bool {
	root = filepath.Clean(strings.TrimSpace(root))
	target = filepath.Clean(strings.TrimSpace(target))
	if root == "." || root == "" || target == "." || target == "" {
		return false
	}
	if target == root {
		return true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func resolveEffectiveBaseBranch(workspacePath string, useCurrentBranch bool, configuredBaseBranch string) (string, error) {
	if useCurrentBranch {
		branch, err := currentBranch(workspacePath)
		if err != nil {
			return "", fmt.Errorf("detect current branch: %w", err)
		}
		if strings.TrimSpace(branch) == "" {
			return "", errors.New("detect current branch: repository is in detached HEAD state; set an explicit worktree base branch or check out a branch first")
		}
		return branch, nil
	}
	configuredBaseBranch = strings.TrimSpace(configuredBaseBranch)
	if configuredBaseBranch == "" {
		return "", errors.New("worktree base branch is required when current-branch mode is disabled")
	}
	return configuredBaseBranch, nil
}

func ensureWorktreeParent(repoRoot string) error {
	if _, err := worktreeCacheRoot(repoRoot); err != nil {
		return fmt.Errorf("create worktree parent directory: %w", err)
	}
	return nil
}

func worktreeCacheRoot(repoRoot string) (string, error) {
	return appstorage.WorktreeDataDir(repoRoot)
}

func deterministicSessionWorktreePath(repoRoot, workspaceID string) (string, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		workspaceID = "ws_session"
	}
	if !validWorktreeWorkspace.MatchString(workspaceID) {
		return "", fmt.Errorf("invalid worktree workspace id %q", workspaceID)
	}
	parent, err := worktreeCacheRoot(repoRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, workspaceID), nil
}

func sessionWorkspaceID(sessionID string) string {
	shortID := compactSessionID(sessionID)
	if shortID == "" {
		shortID = "session"
	}
	return "ws_" + shortID
}

func WorkspaceIdentityForSession(sessionID string) string {
	return sessionWorkspaceID(sessionID)
}

func WorkspaceIdentityForRequestedBranch(branchName string) (string, error) {
	return workspaceIdentityForRequestedBranch(branchName)
}

func workspaceIdentityForRequestedBranch(branchName string) (string, error) {
	slug := branchWorkspaceSlug(branchName)
	if slug == "" {
		return "", errors.New("worktree branch name is required")
	}
	return slug, nil
}

func branchWorkspaceSlug(branchName string) string {
	branchName = strings.ToLower(strings.TrimSpace(branchName))
	if branchName == "" {
		return ""
	}
	var b strings.Builder
	lastWasSeparator := false
	for _, r := range branchName {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastWasSeparator = false
			continue
		}
		if r == '/' || r == '\\' || r == '-' || r == '_' || r == '.' || r == ' ' || r == '\t' {
			if b.Len() > 0 && !lastWasSeparator {
				b.WriteByte('-')
				lastWasSeparator = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func normalizeWorktreeBranchPrefix(configured string) string {
	configured = strings.TrimSpace(configured)
	configured = strings.Trim(configured, "/")
	if configured == "" {
		return defaultWorktreeBranchPrefix
	}
	if strings.EqualFold(configured, defaultWorktreeBranchName) {
		return defaultWorktreeBranchPrefix
	}
	if strings.HasSuffix(configured, "/"+worktreeBranchIDPlaceholder) {
		configured = strings.TrimSuffix(configured, "/"+worktreeBranchIDPlaceholder)
		configured = strings.Trim(configured, "/")
	}
	if configured == "" {
		return defaultWorktreeBranchPrefix
	}
	return configured
}

func effectiveWorktreeBranchName(configuredBranchName, sessionID string) string {
	prefix := normalizeWorktreeBranchPrefix(configuredBranchName)
	shortID := compactSessionID(sessionID)
	if shortID == "" {
		shortID = "session"
	}
	return prefix + "/" + shortID
}

func deterministicWorktreePath(repoRoot, sessionID, title string) (string, error) {
	return deterministicSessionWorktreePath(repoRoot, sessionWorkspaceID(sessionID))
}

func currentBranch(workspacePath string) (string, error) {
	branch, err := runGit(workspacePath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	branch = strings.TrimSpace(branch)
	if branch == "" || branch == "HEAD" {
		return "", nil
	}
	return branch, nil
}

func runGit(path string, args ...string) (string, error) {
	return runGitWithEnv(path, nil, args...)
}

func runGitWithEnv(path string, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	defer cancel()
	cmdArgs := append([]string(nil), args...)
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, cmdArgs...)...)
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		if output == "" {
			output = strings.TrimSpace(err.Error())
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), output)
	}
	return output, nil
}

func localBranchExists(repoRoot, branchName string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("git show-ref --verify: %w", err)
	}
	return true, nil
}

func isExactRequestedWorktreeConflict(err error, branchName, worktreePath string) bool {
	var addErr *gitWorktreeAddError
	if !errors.As(err, &addErr) {
		return false
	}
	text := strings.ToLower(addErr.Output)
	branchName = strings.ToLower(strings.TrimSpace(branchName))
	worktreePath = strings.ToLower(filepath.Clean(strings.TrimSpace(worktreePath)))
	branchCollision := branchName != "" && strings.Contains(text, branchName) &&
		(strings.Contains(text, "already exists") || strings.Contains(text, "already checked out"))
	pathCollision := worktreePath != "" && strings.Contains(text, worktreePath) &&
		(strings.Contains(text, "already exists") || strings.Contains(text, "already checked out"))
	return branchCollision || pathCollision
}

func cleanupFailedWorktreeAllocation(repoRoot, worktreePath string) error {
	var cleanupErrs []error
	registered, err := worktreePathRegistered(repoRoot, worktreePath)
	if err != nil {
		cleanupErrs = append(cleanupErrs, err)
	} else if registered {
		if _, err := runGit(repoRoot, "worktree", "remove", "--force", worktreePath); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove partial worktree metadata: %w", err))
		} else if err := os.RemoveAll(worktreePath); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove partial worktree path: %w", err))
		}
	} else if owned, ownershipErr := partialWorktreePathOwnedByRepository(repoRoot, worktreePath); ownershipErr != nil {
		cleanupErrs = append(cleanupErrs, ownershipErr)
	} else if owned {
		if err := os.RemoveAll(worktreePath); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove partial worktree path: %w", err))
		}
	}
	if _, err := runGit(repoRoot, "worktree", "prune"); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("prune partial worktree metadata: %w", err))
	}
	return errors.Join(cleanupErrs...)
}

func worktreePathRegistered(repoRoot, worktreePath string) (bool, error) {
	output, err := runGit(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("inspect partial worktree metadata: %w", err)
	}
	for _, entry := range parseWorktreeList(output) {
		if sameCleanPath(entry.Path, worktreePath) {
			return true, nil
		}
	}
	return false, nil
}

func partialWorktreePathOwnedByRepository(repoRoot, worktreePath string) (bool, error) {
	gitMarker, err := os.ReadFile(filepath.Join(worktreePath, ".git"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect partial worktree ownership: %w", err)
	}
	markerText := strings.TrimSpace(string(gitMarker))
	if !strings.HasPrefix(markerText, "gitdir:") {
		return false, nil
	}
	marker := strings.TrimSpace(strings.TrimPrefix(markerText, "gitdir:"))
	if marker == "" {
		return false, nil
	}
	marker, err = resolveGitPath(worktreePath, marker)
	if err != nil {
		return false, fmt.Errorf("resolve partial worktree ownership: %w", err)
	}
	commonDir, err := runGit(repoRoot, "rev-parse", "--git-common-dir")
	if err != nil {
		return false, fmt.Errorf("resolve repository metadata for cleanup: %w", err)
	}
	commonDir, err = resolveGitPath(repoRoot, commonDir)
	if err != nil {
		return false, fmt.Errorf("resolve repository metadata path for cleanup: %w", err)
	}
	return pathWithinRoot(filepath.Join(commonDir, "worktrees"), marker), nil
}

func cleanupAllocatedWorktree(repoRoot, worktreePath, branchName string) error {
	cleanupErr := cleanupFailedWorktreeAllocation(repoRoot, worktreePath)
	if exists, err := localBranchExists(repoRoot, branchName); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("inspect partial worktree branch: %w", err))
	} else if exists {
		if _, err := runGit(repoRoot, "branch", "-D", branchName); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove partial worktree branch: %w", err))
		}
	}
	return cleanupErr
}

func cleanupPartialBranch(repoRoot, branchName, effectiveBranch string) error {
	branchHead, err := runGit(repoRoot, "rev-parse", "--verify", "refs/heads/"+branchName+"^{commit}")
	if err != nil {
		return nil
	}
	baseHead, err := runGit(repoRoot, "rev-parse", "--verify", effectiveBranch+"^{commit}")
	if err != nil || branchHead != baseHead {
		return nil
	}
	if _, err := runGit(repoRoot, "branch", "-D", branchName); err != nil {
		return fmt.Errorf("remove partial worktree branch: %w", err)
	}
	return nil
}

func allocationFailureWithCleanup(cause, cleanupErr error) error {
	if cleanupErr == nil {
		return cause
	}
	return fmt.Errorf("%w; cleanup failed: %v", cause, cleanupErr)
}

type gitWorktreeAddError struct {
	Args   []string
	Output string
	Cause  error
}

func (e *gitWorktreeAddError) Error() string {
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), e.Output)
}

func (e *gitWorktreeAddError) Unwrap() error { return e.Cause }

func runGitWorktreeAdd(repoRoot, worktreePath, branchName, effectiveBranch string) (string, error) {
	args := []string{"worktree", "add", "-b", branchName, worktreePath, effectiveBranch}
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoRoot}, args...)...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	stdoutText := strings.TrimSpace(stdout.String())
	stderrText := strings.TrimSpace(stderr.String())
	combinedOutput := strings.TrimSpace(strings.Join(nonEmptyStrings(stdoutText, stderrText), "\n"))
	if err != nil {
		if combinedOutput == "" {
			combinedOutput = strings.TrimSpace(err.Error())
		}
		return "", &gitWorktreeAddError{Args: append([]string(nil), args...), Output: combinedOutput, Cause: err}
	}
	return combinedOutput, nil
}

func nonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func sameCleanPath(left, right string) bool {
	left = filepath.Clean(strings.TrimSpace(left))
	right = filepath.Clean(strings.TrimSpace(right))
	return left != "" && left == right
}

func compactSessionID(sessionID string) string {
	sessionID = strings.ToLower(strings.TrimSpace(sessionID))
	if sessionID == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range sessionID {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	value := b.String()
	if len(value) > 10 {
		value = value[len(value)-10:]
	}
	return strings.TrimSpace(value)
}
