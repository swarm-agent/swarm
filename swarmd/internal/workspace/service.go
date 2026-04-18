package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type Service struct {
	store   *pebblestore.WorkspaceStore
	events  *pebblestore.EventLog
	publish func(pebblestore.EventEnvelope)
}

type Resolution struct {
	RequestedPath string `json:"requested_path"`
	ResolvedPath  string `json:"resolved_path"`
	WorkspacePath string `json:"workspace_path"`
	WorkspaceName string `json:"workspace_name"`
	ThemeID       string `json:"theme_id,omitempty"`
}

type Entry struct {
	Path             string                                 `json:"path"`
	WorkspaceName    string                                 `json:"workspace_name"`
	ThemeID          string                                 `json:"theme_id,omitempty"`
	Directories      []string                               `json:"directories"`
	IsGitRepo        bool                                   `json:"is_git_repo"`
	ReplicationLinks []pebblestore.WorkspaceReplicationLink `json:"replication_links,omitempty"`
	SortIndex        int                                    `json:"sort_index"`
	AddedAt          int64                                  `json:"added_at"`
	UpdatedAt        int64                                  `json:"updated_at"`
	LastSelectedAt   int64                                  `json:"last_selected_at"`
	Active           bool                                   `json:"active"`
	WorktreeEnabled  bool                                   `json:"worktree_enabled"`
}

type Scope struct {
	RequestedPath string   `json:"requested_path"`
	ResolvedPath  string   `json:"resolved_path"`
	WorkspacePath string   `json:"workspace_path"`
	WorkspaceName string   `json:"workspace_name"`
	ThemeID       string   `json:"theme_id,omitempty"`
	Directories   []string `json:"directories"`
	Matched       bool     `json:"matched"`
}

type BrowseEntry struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	IsDirectory bool   `json:"is_directory"`
	IsGitRepo   bool   `json:"is_git_repo"`
	HasClaude   bool   `json:"has_claude"`
	HasSwarm    bool   `json:"has_swarm"`
}

type BrowseResult struct {
	RequestedPath string        `json:"requested_path"`
	ResolvedPath  string        `json:"resolved_path"`
	ParentPath    string        `json:"parent_path,omitempty"`
	HomePath      string        `json:"home_path"`
	RootPath      string        `json:"root_path"`
	Entries       []BrowseEntry `json:"entries"`
}

func NewService(store *pebblestore.WorkspaceStore) *Service {
	return &Service{store: store}
}

func (s *Service) SetEventPublisher(events *pebblestore.EventLog, publish func(pebblestore.EventEnvelope)) {
	if s == nil {
		return
	}
	s.events = events
	s.publish = publish
}

func (s *Service) Resolve(cwd string) (Resolution, error) {
	scope, err := s.ScopeForPath(cwd)
	if err != nil {
		return Resolution{}, err
	}
	return Resolution{
		RequestedPath: cwd,
		ResolvedPath:  scope.ResolvedPath,
		WorkspacePath: scope.WorkspacePath,
		WorkspaceName: scope.WorkspaceName,
		ThemeID:       scope.ThemeID,
	}, nil
}

func (s *Service) Select(path string) (Resolution, error) {
	resolved, err := resolvePath(path)
	if err != nil {
		return Resolution{}, err
	}

	entry, ok, err := s.store.Get(resolved)
	if err != nil {
		return Resolution{}, err
	}
	if !ok {
		return Resolution{}, fmt.Errorf("workspace not found for path %q; use /workspace save first", resolved)
	}

	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = defaultWorkspaceName(resolved)
	}
	if _, err := s.store.SetCurrent(resolved, name); err != nil {
		return Resolution{}, fmt.Errorf("persist workspace selection: %w", err)
	}
	return Resolution{
		RequestedPath: path,
		ResolvedPath:  resolved,
		WorkspacePath: entry.Path,
		WorkspaceName: name,
		ThemeID:       normalizeWorkspaceThemeID(entry.ThemeID),
	}, nil
}

func (s *Service) Add(path, name, themeID string, makeCurrent bool) (Resolution, error) {
	resolved, err := resolvePath(path)
	if err != nil {
		return Resolution{}, err
	}
	if err := ensureWorkspaceDirectory(resolved); err != nil {
		return Resolution{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultWorkspaceName(resolved)
	}
	if makeCurrent {
		if _, err := s.store.Save(resolved, name, themeID, true); err != nil {
			return Resolution{}, fmt.Errorf("persist workspace binding: %w", err)
		}
		if _, err := s.store.SetCurrent(resolved, name); err != nil {
			return Resolution{}, fmt.Errorf("persist workspace selection: %w", err)
		}
	} else {
		if _, err := s.store.Save(resolved, name, themeID, false); err != nil {
			return Resolution{}, fmt.Errorf("persist workspace entry: %w", err)
		}
	}
	entry, ok, err := s.store.Get(resolved)
	if err != nil {
		return Resolution{}, err
	}
	if !ok {
		return Resolution{}, fmt.Errorf("workspace not found after save for path %q", resolved)
	}
	return Resolution{
		RequestedPath: path,
		ResolvedPath:  resolved,
		WorkspacePath: entry.Path,
		WorkspaceName: name,
		ThemeID:       normalizeWorkspaceThemeID(entry.ThemeID),
	}, nil
}

func (s *Service) AddDirectory(path, directory string) (Resolution, error) {
	workspacePath, err := resolvePath(path)
	if err != nil {
		return Resolution{}, err
	}
	targetPath, err := resolvePath(directory)
	if err != nil {
		return Resolution{}, err
	}
	if err := ensureWorkspaceDirectory(targetPath); err != nil {
		return Resolution{}, err
	}

	entry, err := s.store.AddDirectory(workspacePath, targetPath)
	if err != nil {
		return Resolution{}, fmt.Errorf("add workspace directory: %w", err)
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = defaultWorkspaceName(entry.Path)
	}
	return Resolution{
		RequestedPath: directory,
		ResolvedPath:  targetPath,
		WorkspacePath: entry.Path,
		WorkspaceName: name,
		ThemeID:       normalizeWorkspaceThemeID(entry.ThemeID),
	}, nil
}

func (s *Service) RemoveDirectory(path, directory string) (Resolution, error) {
	workspacePath, err := resolvePath(path)
	if err != nil {
		return Resolution{}, err
	}
	targetPath, err := resolvePath(directory)
	if err != nil {
		return Resolution{}, err
	}

	entry, err := s.store.RemoveDirectory(workspacePath, targetPath)
	if err != nil {
		return Resolution{}, fmt.Errorf("remove workspace directory: %w", err)
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = defaultWorkspaceName(entry.Path)
	}
	return Resolution{
		RequestedPath: directory,
		ResolvedPath:  targetPath,
		WorkspacePath: entry.Path,
		WorkspaceName: name,
		ThemeID:       normalizeWorkspaceThemeID(entry.ThemeID),
	}, nil
}

func (s *Service) Rename(path, name string) (Resolution, error) {
	resolved, err := resolvePath(path)
	if err != nil {
		return Resolution{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Resolution{}, fmt.Errorf("workspace name is required")
	}

	if _, ok, err := s.store.Get(resolved); err != nil {
		return Resolution{}, err
	} else if !ok {
		return Resolution{}, fmt.Errorf("workspace not found for path %q", resolved)
	}

	entry, err := s.store.Rename(resolved, name)
	if err != nil {
		return Resolution{}, fmt.Errorf("rename workspace: %w", err)
	}
	return Resolution{
		RequestedPath: path,
		ResolvedPath:  entry.Path,
		WorkspacePath: entry.Path,
		WorkspaceName: entry.Name,
		ThemeID:       normalizeWorkspaceThemeID(entry.ThemeID),
	}, nil
}

func (s *Service) SetThemeID(path, themeID string) (Resolution, error) {
	resolved, err := resolvePath(path)
	if err != nil {
		return Resolution{}, err
	}
	entry, err := s.store.SetThemeID(resolved, themeID)
	if err != nil {
		return Resolution{}, fmt.Errorf("set workspace theme: %w", err)
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = defaultWorkspaceName(entry.Path)
	}
	resolution := Resolution{
		RequestedPath: path,
		ResolvedPath:  entry.Path,
		WorkspacePath: entry.Path,
		WorkspaceName: name,
		ThemeID:       normalizeWorkspaceThemeID(entry.ThemeID),
	}
	if err := s.publishThemeUpdated(resolution); err != nil {
		return Resolution{}, err
	}
	return resolution, nil
}

func (s *Service) publishThemeUpdated(resolution Resolution) error {
	if s == nil || s.events == nil || s.publish == nil {
		return nil
	}
	workspacePath := strings.TrimSpace(resolution.WorkspacePath)
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(resolution.ResolvedPath)
	}
	workspacePath = filepath.Clean(workspacePath)
	if workspacePath == "" || workspacePath == "." {
		return nil
	}
	payload, err := json.Marshal(resolution)
	if err != nil {
		return fmt.Errorf("marshal workspace theme event payload: %w", err)
	}
	env, err := s.events.Append("workspace:"+workspacePath, "workspace.theme.updated", workspacePath, payload, "", "")
	if err != nil {
		return fmt.Errorf("append workspace theme event: %w", err)
	}
	s.publish(env)
	return nil
}

func (s *Service) Move(path string, delta int) (Resolution, error) {
	resolved, err := resolvePath(path)
	if err != nil {
		return Resolution{}, err
	}
	entry, err := s.store.Move(resolved, delta)
	if err != nil {
		return Resolution{}, fmt.Errorf("move workspace: %w", err)
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = defaultWorkspaceName(entry.Path)
	}
	return Resolution{
		RequestedPath: path,
		ResolvedPath:  entry.Path,
		WorkspacePath: entry.Path,
		WorkspaceName: name,
		ThemeID:       normalizeWorkspaceThemeID(entry.ThemeID),
	}, nil
}

func (s *Service) Delete(path string) (Resolution, error) {
	resolved, err := resolvePath(path)
	if err != nil {
		return Resolution{}, err
	}

	entry, ok, err := s.store.Get(resolved)
	if err != nil {
		return Resolution{}, err
	}
	if !ok {
		return Resolution{}, fmt.Errorf("workspace not found for path %q", resolved)
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = defaultWorkspaceName(resolved)
	}

	if err := s.store.Delete(resolved); err != nil {
		return Resolution{}, fmt.Errorf("delete workspace: %w", err)
	}
	return Resolution{
		RequestedPath: path,
		ResolvedPath:  resolved,
		WorkspacePath: resolved,
		WorkspaceName: name,
		ThemeID:       normalizeWorkspaceThemeID(entry.ThemeID),
	}, nil
}

func (s *Service) ListKnown(limit int) ([]Entry, error) {
	entries, err := s.store.List(limit)
	if err != nil {
		return nil, err
	}
	current, ok, err := s.store.GetCurrent()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		isGitRepo, _, _ := detectWorkspaceSignals(entry.Path)
		active := false
		if ok && entry.Path == current.Path {
			active = true
		}
		out = append(out, Entry{
			Path:             entry.Path,
			WorkspaceName:    entry.Name,
			ThemeID:          normalizeWorkspaceThemeID(entry.ThemeID),
			Directories:      append([]string(nil), entry.Directories...),
			IsGitRepo:        isGitRepo,
			ReplicationLinks: append([]pebblestore.WorkspaceReplicationLink(nil), entry.ReplicationLinks...),
			SortIndex:        entry.SortIndex,
			AddedAt:          entry.AddedAt,
			UpdatedAt:        entry.UpdatedAt,
			LastSelectedAt:   entry.LastSelectedAt,
			Active:           active,
			WorktreeEnabled:  false,
		})
	}
	return out, nil
}

func (s *Service) CurrentBinding() (Resolution, bool, error) {
	binding, ok, err := s.store.GetCurrent()
	if err != nil {
		return Resolution{}, false, err
	}
	if !ok {
		return Resolution{}, false, nil
	}
	return Resolution{
		RequestedPath: binding.Path,
		ResolvedPath:  binding.Path,
		WorkspacePath: binding.Path,
		WorkspaceName: binding.Name,
		ThemeID:       "",
	}, true, nil
}

func (s *Service) ScopeForPath(path string) (Scope, error) {
	resolved, err := resolvePath(path)
	if err != nil {
		return Scope{}, err
	}
	entries, err := s.store.List(100000)
	if err != nil {
		return Scope{}, err
	}

	bestIndex := -1
	bestRoot := ""
	bestIsPrimary := false
	for i, entry := range entries {
		primaryPath := strings.TrimSpace(entry.Path)
		for _, root := range entry.Directories {
			if !pathWithinRoot(root, resolved) {
				continue
			}
			trimmedRoot := strings.TrimSpace(root)
			isPrimary := trimmedRoot != "" && trimmedRoot == primaryPath
			if len(trimmedRoot) > len(bestRoot) || (len(trimmedRoot) == len(bestRoot) && isPrimary && !bestIsPrimary) {
				bestRoot = trimmedRoot
				bestIndex = i
				bestIsPrimary = isPrimary
			}
		}
	}
	if bestIndex < 0 {
		return Scope{
			RequestedPath: path,
			ResolvedPath:  resolved,
			WorkspacePath: resolved,
			WorkspaceName: defaultWorkspaceName(resolved),
			ThemeID:       "",
			Directories:   []string{resolved},
			Matched:       false,
		}, nil
	}

	entry := entries[bestIndex]
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = defaultWorkspaceName(entry.Path)
	}
	directories := append([]string(nil), entry.Directories...)
	if len(directories) == 0 {
		directories = []string{entry.Path}
	}
	return Scope{
		RequestedPath: path,
		ResolvedPath:  resolved,
		WorkspacePath: entry.Path,
		WorkspaceName: name,
		ThemeID:       normalizeWorkspaceThemeID(entry.ThemeID),
		Directories:   directories,
		Matched:       true,
	}, nil
}

func (s *Service) ScopeForWorkspace(path string) (Scope, error) {
	resolved, err := resolvePath(path)
	if err != nil {
		return Scope{}, err
	}
	entry, ok, err := s.store.Get(resolved)
	if err != nil {
		return Scope{}, err
	}
	if !ok {
		return Scope{}, fmt.Errorf("workspace not found for path %q", resolved)
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = defaultWorkspaceName(entry.Path)
	}
	directories := append([]string(nil), entry.Directories...)
	if len(directories) == 0 {
		directories = []string{entry.Path}
	}
	return Scope{
		RequestedPath: path,
		ResolvedPath:  resolved,
		WorkspacePath: entry.Path,
		WorkspaceName: name,
		ThemeID:       normalizeWorkspaceThemeID(entry.ThemeID),
		Directories:   directories,
		Matched:       true,
	}, nil
}

func (s *Service) Browse(path string) (BrowseResult, error) {
	resolved, err := resolveBrowsePath(path)
	if err != nil {
		return BrowseResult{}, err
	}
	if err := ensureWorkspaceDirectory(resolved); err != nil {
		return BrowseResult{}, err
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return BrowseResult{}, fmt.Errorf("browse workspace path: %w", err)
	}
	items := make([]BrowseEntry, 0, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name())
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		if !entry.IsDir() {
			continue
		}
		fullPath := filepath.Join(resolved, name)
		isGitRepo, hasClaude, hasSwarm := detectWorkspaceSignals(fullPath)
		items = append(items, BrowseEntry{
			Path:        fullPath,
			Name:        name,
			IsDirectory: true,
			IsGitRepo:   isGitRepo,
			HasClaude:   hasClaude,
			HasSwarm:    hasSwarm,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	parentPath := ""
	parent := filepath.Dir(resolved)
	if parent != "" && parent != resolved {
		parentPath = parent
	}
	homePath, err := resolveBrowseHomePath()
	if err != nil {
		return BrowseResult{}, err
	}
	return BrowseResult{
		RequestedPath: path,
		ResolvedPath:  resolved,
		ParentPath:    parentPath,
		HomePath:      homePath,
		RootPath:      filesystemRootPath(resolved),
		Entries:       items,
	}, nil
}

func normalizeWorkspaceThemeID(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "_", "-")
	value = strings.ReplaceAll(value, " ", "-")
	value = strings.ReplaceAll(value, "/", "-")
	var b strings.Builder
	b.Grow(len(value))
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-':
			if !lastDash {
				b.WriteRune(r)
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func resolvePath(input string) (string, error) {
	target := strings.TrimSpace(input)
	if target == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve cwd: %w", err)
		}
		target = cwd
	}
	target = expandHomePath(target)

	abs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path for %q: %w", target, err)
	}

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		resolved = abs
	}
	return resolved, nil
}

func resolveBrowsePath(input string) (string, error) {
	target := strings.TrimSpace(input)
	if target == "" {
		return resolveBrowseHomePath()
	}
	return resolvePath(target)
}

func resolveBrowseHomePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("home directory is unavailable")
	}
	return resolvePath(home)
}

func filesystemRootPath(path string) string {
	volume := filepath.VolumeName(path)
	if volume != "" {
		return volume + string(filepath.Separator)
	}
	return string(filepath.Separator)
}

func ensureWorkspaceDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("workspace path %q is unavailable: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace path %q is not a directory", path)
	}
	return nil
}

func expandHomePath(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			return home
		}
		return path
	}
	prefix := "~" + string(filepath.Separator)
	if strings.HasPrefix(path, prefix) {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			return filepath.Join(home, strings.TrimPrefix(path, prefix))
		}
	}
	return path
}

func defaultWorkspaceName(path string) string {
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) || strings.TrimSpace(name) == "" {
		return "workspace"
	}
	return name
}

func pathWithinRoot(root, target string) bool {
	root = strings.TrimSpace(root)
	target = strings.TrimSpace(target)
	if root == "" || target == "" {
		return false
	}
	if root == target {
		return true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
