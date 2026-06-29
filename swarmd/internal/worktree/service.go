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
	"swarm/packages/swarmd/internal/flowdiaglog"
	"swarm/packages/swarmd/internal/identity"
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
	Enabled          bool   `json:"enabled"`
	UseCurrentBranch bool   `json:"use_current_branch"`
	BaseBranch       string `json:"base_branch,omitempty"`
	BranchName       string `json:"branch_name,omitempty"`
	UpdatedAt        int64  `json:"updated_at"`
}

type Allocation struct {
	WorkspacePath string `json:"workspace_path"`
	RepoRoot      string `json:"repo_root"`
	BaseBranch    string `json:"base_branch"`
	BranchName    string `json:"branch_name,omitempty"`
	WorkspaceID   string `json:"workspace_id,omitempty"`
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

func (s *Service) IsEnabled(workspacePath string) (bool, error) {
	return false, identity.ErrPrincipalRequired
}

func (s *Service) IsEnabledForPrincipal(principal identity.Principal, workspacePath string) (bool, error) {
	cfg, err := s.GetConfigForPrincipal(principal, workspacePath)
	if err != nil {
		return false, err
	}
	return cfg.Enabled, nil
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
		Enabled:          record.Enabled,
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

func (s *Service) SetConfig(workspacePath string, enabled, useCurrentBranch bool, baseBranch, branchName string) (Config, *pebblestore.EventEnvelope, error) {
	return Config{}, nil, identity.ErrPrincipalRequired
}

func (s *Service) SetConfigForPrincipal(principal identity.Principal, workspacePath string, enabled, useCurrentBranch bool, baseBranch, branchName string) (Config, *pebblestore.EventEnvelope, error) {
	if err := requirePrincipal(principal); err != nil {
		return Config{}, nil, err
	}
	canonical, err := s.resolveWorkspaceConfigPathForPrincipal(principal, workspacePath)
	if err != nil {
		return Config{}, nil, err
	}
	record, err := s.store.SetConfigForAccount(principal.AccountScopeID, canonical, enabled, useCurrentBranch, baseBranch, branchName)
	if err != nil {
		return Config{}, nil, fmt.Errorf("persist worktree config: %w", err)
	}
	cfg := Config{
		WorkspacePath:    canonical,
		Enabled:          record.Enabled,
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
		return Allocation{}, fmt.Errorf("target worktree path %q already exists", worktreePath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Allocation{}, fmt.Errorf("check target worktree path: %w", statErr)
	}
	if _, err := runGitWorktreeAdd(repoRoot, worktreePath, branchName, effectiveBranch, workspacePath, useCurrentBranch, baseBranch, sessionID, workspaceID); err != nil {
		_ = os.RemoveAll(worktreePath)
		return Allocation{}, fmt.Errorf("create session worktree: %w", err)
	}
	if err := os.Chmod(worktreePath, appstorage.PrivateDirPerm); err != nil {
		return Allocation{}, fmt.Errorf("set worktree directory permissions: %w", err)
	}
	return Allocation{
		WorkspacePath: worktreePath,
		RepoRoot:      repoRoot,
		BaseBranch:    effectiveBranch,
		BranchName:    branchName,
		WorkspaceID:   workspaceID,
	}, nil
}

func (s *Service) AllocateTaskWorkspace(workspacePath, baseBranch, nameSeed string) (Allocation, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return Allocation{}, errors.New("workspace path is required")
	}
	baseBranch = strings.TrimSpace(baseBranch)
	if baseBranch == "" {
		branch, branchErr := currentBranch(workspacePath)
		if branchErr != nil {
			return Allocation{}, fmt.Errorf("detect current branch: %w", branchErr)
		}
		if strings.TrimSpace(branch) == "" {
			return Allocation{}, errors.New("detect current branch: repository is in detached HEAD state; explicit base branch is required for task worktrees")
		}
		baseBranch = branch
	}
	return s.allocateSessionWorkspace(workspacePath, false, baseBranch, "", nameSeed)
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
			Branch:      entry.Branch,
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

func (s *Service) migrateLegacyConfig() {}

func defaultConfigForWorkspace(workspacePath string) Config {
	return configFromRecord(workspacePath, pebblestore.WorktreeConfigRecord{WorkspacePath: workspacePath})
}

func configFromRecord(workspacePath string, record pebblestore.WorktreeConfigRecord) Config {
	useCurrentBranch := record.UseCurrentBranch != nil && *record.UseCurrentBranch
	return Config{
		WorkspacePath:    workspacePath,
		Enabled:          record.Enabled,
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
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	defer cancel()
	cmdArgs := append([]string(nil), args...)
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, cmdArgs...)...)
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

func runGitWorktreeAdd(repoRoot, worktreePath, branchName, effectiveBranch, workspacePath string, useCurrentBranch bool, requestedBaseBranch, sessionID, workspaceID string) (string, error) {
	args := []string{"worktree", "add", "-b", branchName, worktreePath, effectiveBranch}
	command := strings.Join(append([]string{"git", "-C", repoRoot}, args...), " ")
	flowdiaglog.Printf("worktree_git_worktree_add_start", "repo_root=%q workspace_path=%q worktree_path=%q branch_name=%q effective_base_branch=%q requested_base_branch=%q use_current_branch=%t session_id=%q workspace_id=%q command=%q", repoRoot, workspacePath, worktreePath, branchName, effectiveBranch, strings.TrimSpace(requestedBaseBranch), useCurrentBranch, sessionID, workspaceID, command)

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
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		if combinedOutput == "" {
			combinedOutput = strings.TrimSpace(err.Error())
		}
		flowdiaglog.Printf("worktree_git_worktree_add_error", "repo_root=%q workspace_path=%q worktree_path=%q branch_name=%q effective_base_branch=%q requested_base_branch=%q use_current_branch=%t session_id=%q workspace_id=%q command=%q exit_code=%d stdout=%q stderr=%q output=%q error=%q timeout=%t", repoRoot, workspacePath, worktreePath, branchName, effectiveBranch, strings.TrimSpace(requestedBaseBranch), useCurrentBranch, sessionID, workspaceID, command, exitCode, stdoutText, stderrText, combinedOutput, err.Error(), errors.Is(ctx.Err(), context.DeadlineExceeded))
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), combinedOutput)
	}
	flowdiaglog.Printf("worktree_git_worktree_add_success", "repo_root=%q workspace_path=%q worktree_path=%q branch_name=%q effective_base_branch=%q requested_base_branch=%q use_current_branch=%t session_id=%q workspace_id=%q command=%q exit_code=%d stdout=%q stderr=%q output=%q", repoRoot, workspacePath, worktreePath, branchName, effectiveBranch, strings.TrimSpace(requestedBaseBranch), useCurrentBranch, sessionID, workspaceID, command, exitCode, stdoutText, stderrText, combinedOutput)
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
