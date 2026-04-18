package worktree

import (
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

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

const (
	gitCommandTimeout           = 12 * time.Second
	sessionBranchMaxSlugRunes   = 42
	sessionBranchFallbackSlug   = "session"
	worktreePathMaxSlugRunes    = 36
	worktreePathFallbackSlug    = "session"
	defaultWorktreeBranchName   = "agent/<id>"
	defaultWorktreeBranchPrefix = "agent"
	worktreeBranchIDPlaceholder = "<id>"
)

const detachedWorkspaceFallbackWarning = "Opened without git worktree support; use a git repository and make sure git is installed for the app to work properly."

var nonBranchSlugRune = regexp.MustCompile(`[^a-z0-9]+`)

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

type Service struct {
	store     *pebblestore.WorktreeStore
	workspace *workspaceruntime.Service
	events    *pebblestore.EventLog
	mu        sync.Mutex
}

func NewService(store *pebblestore.WorktreeStore, workspace *workspaceruntime.Service, events *pebblestore.EventLog) *Service {
	service := &Service{store: store, workspace: workspace, events: events}
	service.migrateLegacyConfig()
	return service
}

func (s *Service) IsEnabled(workspacePath string) (bool, error) {
	cfg, err := s.GetConfig(workspacePath)
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
	record, _, err := s.store.GetConfig(canonical)
	if err != nil {
		return Config{}, fmt.Errorf("read worktree config: %w", err)
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

func (s *Service) SetConfig(workspacePath string, enabled, useCurrentBranch bool, baseBranch, branchName string) (Config, *pebblestore.EventEnvelope, error) {
	canonical, err := s.resolveWorkspaceConfigPath(workspacePath)
	if err != nil {
		return Config{}, nil, err
	}
	record, err := s.store.SetConfig(canonical, enabled, useCurrentBranch, baseBranch, branchName)
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
	cfg, err := s.GetConfig(workspacePath)
	if err != nil {
		return Allocation{}, err
	}
	if !cfg.Enabled {
		return Allocation{}, errors.New("worktrees mode is disabled for this workspace")
	}
	return s.allocateSessionWorkspace(workspacePath, cfg.UseCurrentBranch, cfg.BaseBranch, cfg.BranchName, nameSeed)
}

func (s *Service) AllocateDetachedWorkspaceRequested(workspacePath, nameSeed, baseBranch, branchName string) (Allocation, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return Allocation{}, errors.New("workspace path is required")
	}
	useCurrentBranch := strings.TrimSpace(baseBranch) == ""
	return s.allocateSessionWorkspace(workspacePath, useCurrentBranch, baseBranch, branchName, nameSeed)
}

func (s *Service) allocateSessionWorkspace(workspacePath string, useCurrentBranch bool, baseBranch, configuredBranchName, sessionID string) (Allocation, error) {
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

	workspaceID := sessionWorkspaceID(sessionID)
	branchName := effectiveWorktreeBranchName(configuredBranchName, sessionID)
	worktreePath := deterministicSessionWorktreePath(repoRoot, workspaceID)
	if _, statErr := os.Stat(worktreePath); statErr == nil {
		return Allocation{}, fmt.Errorf("target worktree path %q already exists", worktreePath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Allocation{}, fmt.Errorf("check target worktree path: %w", statErr)
	}
	if err := ensureWorktreeParent(repoRoot); err != nil {
		return Allocation{}, err
	}
	if _, err := runGit(repoRoot, "worktree", "add", "-b", branchName, worktreePath, effectiveBranch); err != nil {
		_ = os.RemoveAll(worktreePath)
		return Allocation{}, fmt.Errorf("create session worktree: %w", err)
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

func (s *Service) AttachBranch(workspacePath, sessionID, title string) (string, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	sessionID = strings.TrimSpace(sessionID)
	if workspacePath == "" {
		return "", errors.New("workspace path is required")
	}
	if sessionID == "" {
		return "", errors.New("session id is required")
	}
	cfg, err := s.GetConfig(workspacePath)
	if err != nil {
		return "", err
	}
	branchName := effectiveWorktreeBranchName(cfg.BranchName, sessionID)
	current, err := currentBranch(workspacePath)
	if err == nil && current == branchName {
		return branchName, nil
	}
	if _, createErr := runGit(workspacePath, "checkout", "-b", branchName); createErr != nil {
		if _, checkoutErr := runGit(workspacePath, "checkout", branchName); checkoutErr != nil {
			return "", fmt.Errorf("attach session branch %q: %w", branchName, createErr)
		}
	}
	return branchName, nil
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
	targetPath := deterministicWorktreePath(repoRoot, sessionID, title)
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
	return targetPath, nil
}

func (s *Service) resolveWorkspaceConfigPath(workspacePath string) (string, error) {
	trimmed := strings.TrimSpace(workspacePath)
	if trimmed == "" {
		if s == nil || s.workspace == nil {
			return "", errors.New("workspace path is required")
		}
		current, ok, err := s.workspace.CurrentBinding()
		if err != nil {
			return "", fmt.Errorf("resolve current workspace: %w", err)
		}
		if !ok {
			return "", errors.New("workspace path is required")
		}
		trimmed = strings.TrimSpace(current.ResolvedPath)
	}
	if trimmed == "" {
		return "", errors.New("workspace path is required")
	}
	if s != nil && s.workspace != nil {
		scope, err := s.workspace.ScopeForPath(trimmed)
		if err != nil {
			return "", fmt.Errorf("resolve workspace scope: %w", err)
		}
		if scope.Matched && strings.TrimSpace(scope.WorkspacePath) != "" {
			return strings.TrimSpace(scope.WorkspacePath), nil
		}
	}
	resolved, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func (s *Service) migrateLegacyConfig() {
	if s == nil || s.store == nil || s.workspace == nil {
		return
	}
	entries, err := s.workspace.ListKnown(100000)
	if err != nil {
		return
	}
	paths := make([]string, 0, len(entries)+1)
	for _, entry := range entries {
		paths = append(paths, strings.TrimSpace(entry.Path))
	}
	if current, ok, err := s.workspace.CurrentBinding(); err == nil && ok {
		paths = append(paths, strings.TrimSpace(current.ResolvedPath))
	}
	_, _ = s.store.MigrateLegacyGlobalConfig(paths)
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

func resolveBranchCommit(repoRoot, baseBranch string) (string, error) {
	commit, err := runGit(repoRoot, "rev-parse", "--verify", baseBranch)
	if err == nil {
		return strings.TrimSpace(commit), nil
	}
	return "", fmt.Errorf("resolve base branch %q: %w", baseBranch, err)
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
	parent := filepath.Join(repoRoot, ".swarm", "worktrees")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create worktree parent directory: %w", err)
	}
	return nil
}

func reserveWorktreePath(repoRoot, nameSeed string) (string, error) {
	if err := ensureWorktreeParent(repoRoot); err != nil {
		return "", err
	}
	parent := filepath.Join(repoRoot, ".swarm", "worktrees")
	prefix := "worktree-" + worktreePathSlug(nameSeed) + "-"
	temporary, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		return "", fmt.Errorf("allocate worktree path: %w", err)
	}
	if err := os.Remove(temporary); err != nil {
		return "", fmt.Errorf("prepare worktree path: %w", err)
	}
	return temporary, nil
}

func deterministicSessionWorktreePath(repoRoot, workspaceID string) string {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		workspaceID = "ws_session"
	}
	return filepath.Join(repoRoot, ".swarm", "worktrees", workspaceID)
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

func sessionWorkspaceBranchName(sessionID string) string {
	return effectiveWorktreeBranchName(defaultWorktreeBranchPrefix, sessionID)
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

func deterministicWorktreePath(repoRoot, sessionID, title string) string {
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

func sessionBranchName(sessionID, title string) string {
	return sessionWorkspaceBranchName(sessionID)
}

func branchSlug(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	title = nonBranchSlugRune.ReplaceAllString(title, "-")
	title = strings.Trim(title, "-")
	if title == "" {
		return sessionBranchFallbackSlug
	}
	runes := []rune(title)
	if len(runes) > sessionBranchMaxSlugRunes {
		title = strings.Trim(string(runes[:sessionBranchMaxSlugRunes]), "-")
	}
	if title == "" {
		return sessionBranchFallbackSlug
	}
	return title
}

func worktreePathSlug(seed string) string {
	seed = strings.ToLower(strings.TrimSpace(seed))
	seed = nonBranchSlugRune.ReplaceAllString(seed, "-")
	seed = strings.Trim(seed, "-")
	if seed == "" {
		return worktreePathFallbackSlug
	}
	runes := []rune(seed)
	if len(runes) > worktreePathMaxSlugRunes {
		seed = strings.Trim(string(runes[:worktreePathMaxSlugRunes]), "-")
	}
	if seed == "" {
		return worktreePathFallbackSlug
	}
	return seed
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
