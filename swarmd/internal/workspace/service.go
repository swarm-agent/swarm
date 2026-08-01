package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var errAccountOwnedWorkspaceRequired = errors.New("account-owned workspace path is required")

type Service struct {
	store   *pebblestore.WorkspaceStore
	events  *pebblestore.EventLog
	publish func(pebblestore.EventEnvelope)
}

type Resolution struct {
	RequestedPath                    string `json:"requested_path"`
	ResolvedPath                     string `json:"resolved_path"`
	WorkspaceID                      string `json:"workspace_id,omitempty"`
	LocalWorkspaceBindingID          string `json:"local_workspace_binding_id,omitempty"`
	WorkspaceGeneration              int64  `json:"workspace_generation,omitempty"`
	WorkspaceState                   string `json:"workspace_state,omitempty"`
	WorkspacePath                    string `json:"workspace_path"`
	WorkspaceName                    string `json:"workspace_name"`
	ThemeID                          string `json:"theme_id,omitempty"`
	Definition                       string `json:"definition,omitempty"`
	DefinitionStatus                 string `json:"definition_status,omitempty"`
	DefinitionAttemptCount           int    `json:"definition_attempt_count,omitempty"`
	DefinitionGeneration             int64  `json:"definition_generation,omitempty"`
	DefinitionError                  string `json:"definition_error,omitempty"`
	DefinitionModelSuggestion        string `json:"definition_model_suggestion,omitempty"`
	DefinitionPendingAt              int64  `json:"definition_pending_at,omitempty"`
	DefinitionCompletedAt            int64  `json:"definition_completed_at,omitempty"`
	DefinitionFailedAt               int64  `json:"definition_failed_at,omitempty"`
	DefinitionUpdatedAt              int64  `json:"definition_updated_at,omitempty"`
}

type Entry struct {
	Path                       string   `json:"path"`
	WorkspaceID                string   `json:"workspace_id,omitempty"`
	WorkspaceGeneration        int64    `json:"workspace_generation,omitempty"`
	State                      string   `json:"state,omitempty"`
	LocalWorkspaceBindingID    string   `json:"local_workspace_binding_id,omitempty"`
	WorkspaceName              string   `json:"workspace_name"`
	ThemeID                    string   `json:"theme_id,omitempty"`
	Directories                []string `json:"directories"`
	IsGitRepo                  bool     `json:"is_git_repo"`
	SortIndex                  int      `json:"sort_index"`
	AddedAt                    int64    `json:"added_at"`
	UpdatedAt                  int64    `json:"updated_at"`
	LastSelectedAt             int64    `json:"last_selected_at"`
	Active                     bool     `json:"active"`
	WorktreeEnabled            bool     `json:"worktree_enabled"`
	Definition                 string   `json:"definition,omitempty"`
	DefinitionStatus           string   `json:"definition_status,omitempty"`
	DefinitionAttemptCount     int      `json:"definition_attempt_count,omitempty"`
	DefinitionGeneration       int64    `json:"definition_generation,omitempty"`
	DefinitionError            string   `json:"definition_error,omitempty"`
	DefinitionModelSuggestion  string   `json:"definition_model_suggestion,omitempty"`
	DefinitionPendingAt        int64    `json:"definition_pending_at,omitempty"`
	DefinitionCompletedAt      int64    `json:"definition_completed_at,omitempty"`
	DefinitionFailedAt         int64    `json:"definition_failed_at,omitempty"`
	DefinitionUpdatedAt        int64    `json:"definition_updated_at,omitempty"`
}

type Scope struct {
	RequestedPath       string   `json:"requested_path"`
	ResolvedPath        string   `json:"resolved_path"`
	WorkspaceID         string   `json:"workspace_id,omitempty"`
	WorkspaceGeneration int64    `json:"workspace_generation,omitempty"`
	WorkspaceState      string   `json:"workspace_state,omitempty"`
	WorkspacePath       string   `json:"workspace_path"`
	WorkspaceName       string   `json:"workspace_name"`
	ThemeID             string   `json:"theme_id,omitempty"`
	Directories         []string `json:"directories"`
	Matched             bool     `json:"matched"`
}

type BrowseEntry struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	IsDirectory bool   `json:"is_directory"`
	IsGitRepo   bool   `json:"is_git_repo"`
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

type CreateFolderResult struct {
	Path                   string `json:"path"`
	Name                   string `json:"name"`
	ParentPath             string `json:"parent_path"`
	RequiresSudo           bool   `json:"requires_sudo"`
	PermissionErrorMessage string `json:"permission_error_message,omitempty"`
}

func NewService(store *pebblestore.WorkspaceStore) *Service {
	return &Service{store: store}
}

func (s *Service) GetByWorkspaceIDForPrincipal(principal identity.Principal, workspaceID string) (pebblestore.WorkspaceEntry, bool, error) {
	if err := requirePrincipal(principal); err != nil {
		return pebblestore.WorkspaceEntry{}, false, err
	}
	if s == nil || s.store == nil {
		return pebblestore.WorkspaceEntry{}, false, fmt.Errorf("workspace service is not configured")
	}
	return s.store.GetByWorkspaceIDForAccount(principal.AccountScopeID, workspaceID)
}

func (s *Service) MarkDefinitionPendingForPrincipal(principal identity.Principal, path string) (pebblestore.WorkspaceEntry, error) {
	if err := requirePrincipal(principal); err != nil {
		return pebblestore.WorkspaceEntry{}, err
	}
	return s.store.MarkDefinitionPendingForAccount(principal.AccountScopeID, path)
}

func (s *Service) RecordDefinitionAttemptForPrincipal(principal identity.Principal, path string, generation int64, attempt int) (pebblestore.WorkspaceEntry, bool, error) {
	if err := requirePrincipal(principal); err != nil {
		return pebblestore.WorkspaceEntry{}, false, err
	}
	return s.store.RecordDefinitionAttemptForAccount(principal.AccountScopeID, path, generation, attempt)
}

func (s *Service) CompleteDefinitionForPrincipal(principal identity.Principal, path string, generation int64, definition string, attempts int) (pebblestore.WorkspaceEntry, bool, error) {
	if err := requirePrincipal(principal); err != nil {
		return pebblestore.WorkspaceEntry{}, false, err
	}
	return s.store.CompleteDefinitionForAccount(principal.AccountScopeID, path, generation, definition, attempts)
}

func (s *Service) FailDefinitionForPrincipal(principal identity.Principal, path string, generation int64, failure, suggestion string, attempts int) (pebblestore.WorkspaceEntry, bool, error) {
	if err := requirePrincipal(principal); err != nil {
		return pebblestore.WorkspaceEntry{}, false, err
	}
	return s.store.FailDefinitionForAccount(principal.AccountScopeID, path, generation, failure, suggestion, attempts)
}

func (s *Service) SetEventPublisher(events *pebblestore.EventLog, publish func(pebblestore.EventEnvelope)) {
	if s == nil {
		return
	}
	s.events = events
	s.publish = publish
}

// Resolve is a legacy, principal-less resolver retained only for explicit
// bootstrap/migration callers. Runtime and authenticated paths must use
// ResolveForPrincipal so account-scoped workspace directories are authoritative.
func (s *Service) Resolve(cwd string) (Resolution, error) {
	scope, err := s.legacyScopeForPath(cwd)
	if err != nil {
		return Resolution{}, err
	}
	if !scope.Matched {
		return Resolution{}, errAccountOwnedWorkspaceRequired
	}
	return resolutionFromScope(cwd, scope), nil
}

func (s *Service) ResolveForPrincipal(principal identity.Principal, cwd string) (Resolution, error) {
	if err := requirePrincipal(principal); err != nil {
		return Resolution{}, err
	}
	scope, err := s.ScopeForPathForPrincipal(principal, cwd)
	if err != nil {
		return Resolution{}, err
	}
	if !scope.Matched {
		return Resolution{}, errAccountOwnedWorkspaceRequired
	}
	return resolutionFromScope(cwd, scope), nil
}

func (s *Service) Select(path string) (Resolution, error) {
	return Resolution{}, identity.ErrPrincipalRequired
}

func (s *Service) SelectForPrincipal(principal identity.Principal, path string) (Resolution, error) {
	if err := requirePrincipal(principal); err != nil {
		return Resolution{}, err
	}
	resolved, err := resolvePath(path)
	if err != nil {
		return Resolution{}, err
	}

	entry, ok, err := s.store.GetForAccount(principal.AccountScopeID, resolved)
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
	if _, err := s.store.SetCurrentForAccount(principal.AccountScopeID, principal.UserID, resolved, name); err != nil {
		return Resolution{}, fmt.Errorf("persist workspace selection: %w", err)
	}
	return resolutionForEntry(path, resolved, entry, name), nil
}

func (s *Service) Add(path, name, themeID string, makeCurrent bool) (Resolution, error) {
	return Resolution{}, identity.ErrPrincipalRequired
}

func (s *Service) AddForPrincipal(principal identity.Principal, path, name, themeID string, makeCurrent bool) (Resolution, error) {
	resolution, _, _, err := s.AddForPrincipalWithEntry(principal, path, name, themeID, makeCurrent)
	return resolution, err
}

func (s *Service) AddForPrincipalWithEntry(principal identity.Principal, path, name, themeID string, makeCurrent bool) (Resolution, pebblestore.WorkspaceEntry, bool, error) {
	return s.addForPrincipalWithEntrySelection(principal, path, name, themeID, makeCurrent)
}

func (s *Service) AddForPrincipalWithEntryWithoutSelection(principal identity.Principal, path, name, themeID string) (Resolution, pebblestore.WorkspaceEntry, bool, error) {
	return s.addForPrincipalWithEntrySelection(principal, path, name, themeID, false)
}

func (s *Service) addForPrincipalWithEntrySelection(principal identity.Principal, path, name, themeID string, selectCurrent bool) (Resolution, pebblestore.WorkspaceEntry, bool, error) {
	if s == nil || s.store == nil {
		return Resolution{}, pebblestore.WorkspaceEntry{}, false, fmt.Errorf("workspace service is not configured")
	}
	if err := requirePrincipal(principal); err != nil {
		return Resolution{}, pebblestore.WorkspaceEntry{}, false, err
	}
	resolved, err := resolvePath(path)
	if err != nil {
		return Resolution{}, pebblestore.WorkspaceEntry{}, false, err
	}
	if err := ensureWorkspaceDirectory(resolved); err != nil {
		return Resolution{}, pebblestore.WorkspaceEntry{}, false, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultWorkspaceName(resolved)
	}
	entry, created, err := s.store.SaveForAccountWithResult(principal.AccountScopeID, resolved, name, themeID, selectCurrent)
	if err != nil {
		if selectCurrent {
			return Resolution{}, pebblestore.WorkspaceEntry{}, false, fmt.Errorf("persist workspace binding: %w", err)
		}
		return Resolution{}, pebblestore.WorkspaceEntry{}, false, fmt.Errorf("persist workspace entry: %w", err)
	}
	if selectCurrent {
		if _, err := s.store.SetCurrentForAccount(principal.AccountScopeID, principal.UserID, resolved, name); err != nil {
			return Resolution{}, pebblestore.WorkspaceEntry{}, created, fmt.Errorf("persist workspace selection: %w", err)
		}
	}
	entry, ok, err := s.store.GetForAccount(principal.AccountScopeID, resolved)
	if err != nil {
		return Resolution{}, pebblestore.WorkspaceEntry{}, created, err
	}
	if !ok {
		return Resolution{}, pebblestore.WorkspaceEntry{}, created, fmt.Errorf("workspace not found after save for path %q", resolved)
	}
	return resolutionForEntry(path, resolved, entry, name), entry, created, nil
}

func (s *Service) SelectEntryForPrincipal(principal identity.Principal, entry pebblestore.WorkspaceEntry) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("workspace service is not configured")
	}
	if err := requirePrincipal(principal); err != nil {
		return err
	}
	entry = pebblestore.NormalizeWorkspaceEntryForAccount(principal.AccountScopeID, entry)
	path := strings.TrimSpace(entry.Path)
	if path == "" {
		return fmt.Errorf("workspace path is required")
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = defaultWorkspaceName(path)
	}
	if _, err := s.store.SetCurrentForAccount(principal.AccountScopeID, principal.UserID, path, name); err != nil {
		return fmt.Errorf("persist workspace selection: %w", err)
	}
	return nil
}

func (s *Service) RollbackCreatedWorkspaceForPrincipal(principal identity.Principal, entry pebblestore.WorkspaceEntry) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("workspace service is not configured")
	}
	if err := requirePrincipal(principal); err != nil {
		return err
	}
	entry = pebblestore.NormalizeWorkspaceEntryForAccount(principal.AccountScopeID, entry)
	path := strings.TrimSpace(entry.Path)
	if path == "" {
		return fmt.Errorf("workspace path is required")
	}
	if err := s.store.DeleteForAccount(principal.AccountScopeID, principal.UserID, path); err != nil {
		return fmt.Errorf("rollback workspace add: %w", err)
	}
	if existing, ok, err := s.store.GetForAccount(principal.AccountScopeID, path); err != nil {
		return fmt.Errorf("verify workspace rollback: %w", err)
	} else if ok {
		return fmt.Errorf("verify workspace rollback: workspace %q still exists with id %q", path, existing.WorkspaceID)
	}
	return nil
}

func (s *Service) AddDirectory(path, directory string) (Resolution, error) {
	return Resolution{}, identity.ErrPrincipalRequired
}

func (s *Service) AddDirectoryForPrincipal(principal identity.Principal, path, directory string) (Resolution, error) {
	if err := requirePrincipal(principal); err != nil {
		return Resolution{}, err
	}
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

	entry, err := s.store.AddDirectoryForAccount(principal.AccountScopeID, workspacePath, targetPath)
	if err != nil {
		return Resolution{}, fmt.Errorf("add workspace directory: %w", err)
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = defaultWorkspaceName(entry.Path)
	}
	return resolutionForEntry(directory, targetPath, entry, name), nil
}

func (s *Service) RemoveDirectory(path, directory string) (Resolution, error) {
	return Resolution{}, identity.ErrPrincipalRequired
}

func (s *Service) RemoveDirectoryForPrincipal(principal identity.Principal, path, directory string) (Resolution, error) {
	if err := requirePrincipal(principal); err != nil {
		return Resolution{}, err
	}
	workspacePath, err := resolvePath(path)
	if err != nil {
		return Resolution{}, err
	}
	targetPath, err := resolvePath(directory)
	if err != nil {
		return Resolution{}, err
	}

	entry, err := s.store.RemoveDirectoryForAccount(principal.AccountScopeID, workspacePath, targetPath)
	if err != nil {
		return Resolution{}, fmt.Errorf("remove workspace directory: %w", err)
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = defaultWorkspaceName(entry.Path)
	}
	return resolutionForEntry(directory, targetPath, entry, name), nil
}

func (s *Service) Rename(path, name string) (Resolution, error) {
	return Resolution{}, identity.ErrPrincipalRequired
}

func (s *Service) RenameForPrincipal(principal identity.Principal, path, name string) (Resolution, error) {
	if err := requirePrincipal(principal); err != nil {
		return Resolution{}, err
	}
	resolved, err := resolvePath(path)
	if err != nil {
		return Resolution{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Resolution{}, fmt.Errorf("workspace name is required")
	}

	if _, ok, err := s.store.GetForAccount(principal.AccountScopeID, resolved); err != nil {
		return Resolution{}, err
	} else if !ok {
		return Resolution{}, fmt.Errorf("workspace not found for path %q", resolved)
	}

	entry, err := s.store.RenameForAccount(principal.AccountScopeID, principal.UserID, resolved, name)
	if err != nil {
		return Resolution{}, fmt.Errorf("rename workspace: %w", err)
	}
	return resolutionForEntry(path, entry.Path, entry, entry.Name), nil
}

func (s *Service) SetThemeID(path, themeID string) (Resolution, error) {
	return Resolution{}, identity.ErrPrincipalRequired
}

func (s *Service) SetThemeIDForPrincipal(principal identity.Principal, path, themeID string) (Resolution, error) {
	if err := requirePrincipal(principal); err != nil {
		return Resolution{}, err
	}
	resolved, err := resolvePath(path)
	if err != nil {
		return Resolution{}, err
	}
	entry, err := s.store.SetThemeIDForAccount(principal.AccountScopeID, resolved, themeID)
	if err != nil {
		return Resolution{}, fmt.Errorf("set workspace theme: %w", err)
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = defaultWorkspaceName(entry.Path)
	}
	resolution := resolutionForEntry(path, entry.Path, entry, name)
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
	return Resolution{}, identity.ErrPrincipalRequired
}

func (s *Service) MoveForPrincipal(principal identity.Principal, path string, delta int) (Resolution, error) {
	if err := requirePrincipal(principal); err != nil {
		return Resolution{}, err
	}
	resolved, err := resolvePath(path)
	if err != nil {
		return Resolution{}, err
	}
	entry, err := s.store.MoveForAccount(principal.AccountScopeID, resolved, delta)
	if err != nil {
		return Resolution{}, fmt.Errorf("move workspace: %w", err)
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = defaultWorkspaceName(entry.Path)
	}
	return resolutionForEntry(path, entry.Path, entry, name), nil
}

func (s *Service) Delete(path string) (Resolution, error) {
	return Resolution{}, identity.ErrPrincipalRequired
}

func (s *Service) DeleteForPrincipal(principal identity.Principal, path string) (Resolution, error) {
	if err := requirePrincipal(principal); err != nil {
		return Resolution{}, err
	}
	resolved, err := resolvePath(path)
	if err != nil {
		return Resolution{}, err
	}

	entry, ok, err := s.store.GetForAccount(principal.AccountScopeID, resolved)
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

	if err := s.store.DeleteForAccount(principal.AccountScopeID, principal.UserID, resolved); err != nil {
		return Resolution{}, fmt.Errorf("delete workspace: %w", err)
	}
	return resolutionForEntry(path, resolved, entry, name), nil
}

func (s *Service) ListKnown(limit int) ([]Entry, error) {
	entries, err := s.store.ListLegacy(limit)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		isGitRepo, _ := detectWorkspaceSignals(entry.Path)
		out = append(out, Entry{
			Path:                entry.Path,
			WorkspaceID:         entry.WorkspaceID,
			WorkspaceGeneration: entry.WorkspaceGeneration,
			State:               entry.State,
			WorkspaceName:       entry.Name,
			ThemeID:             normalizeWorkspaceThemeID(entry.ThemeID),
			Directories:         append([]string(nil), entry.Directories...),
			IsGitRepo:           isGitRepo,
			SortIndex:           entry.SortIndex,
			AddedAt:             entry.AddedAt,
			UpdatedAt:           entry.UpdatedAt,
			LastSelectedAt:      entry.LastSelectedAt,
			Active:              false,
			WorktreeEnabled:           false,
			Definition:                entry.Definition,
			DefinitionStatus:          entry.DefinitionStatus,
			DefinitionAttemptCount:    entry.DefinitionAttemptCount,
			DefinitionGeneration:      entry.DefinitionGeneration,
			DefinitionError:           entry.DefinitionError,
			DefinitionModelSuggestion: entry.DefinitionModelSuggestion,
			DefinitionPendingAt:       entry.DefinitionPendingAt,
			DefinitionCompletedAt:     entry.DefinitionCompletedAt,
			DefinitionFailedAt:        entry.DefinitionFailedAt,
			DefinitionUpdatedAt:       entry.DefinitionUpdatedAt,
		})
	}
	return out, nil
}

func (s *Service) ListKnownForPrincipal(principal identity.Principal, limit int) ([]Entry, error) {
	if err := requirePrincipal(principal); err != nil {
		return nil, err
	}
	entries, err := s.store.ListForAccount(principal.AccountScopeID, limit)
	if err != nil {
		return nil, err
	}
	current, ok, err := s.store.GetCurrentForAccount(principal.AccountScopeID, principal.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		isGitRepo, _ := detectWorkspaceSignals(entry.Path)
		active := false
		if ok && entry.Path == current.Path {
			active = true
		}
		out = append(out, Entry{
			Path:                entry.Path,
			WorkspaceID:         entry.WorkspaceID,
			WorkspaceGeneration: entry.WorkspaceGeneration,
			State:               entry.State,
			WorkspaceName:       entry.Name,
			ThemeID:             normalizeWorkspaceThemeID(entry.ThemeID),
			Directories:         append([]string(nil), entry.Directories...),
			IsGitRepo:           isGitRepo,
			SortIndex:           entry.SortIndex,
			AddedAt:             entry.AddedAt,
			UpdatedAt:           entry.UpdatedAt,
			LastSelectedAt:      entry.LastSelectedAt,
			Active:              active,
			WorktreeEnabled:           false,
			Definition:                entry.Definition,
			DefinitionStatus:          entry.DefinitionStatus,
			DefinitionAttemptCount:    entry.DefinitionAttemptCount,
			DefinitionGeneration:      entry.DefinitionGeneration,
			DefinitionError:           entry.DefinitionError,
			DefinitionModelSuggestion: entry.DefinitionModelSuggestion,
			DefinitionPendingAt:       entry.DefinitionPendingAt,
			DefinitionCompletedAt:     entry.DefinitionCompletedAt,
			DefinitionFailedAt:        entry.DefinitionFailedAt,
			DefinitionUpdatedAt:       entry.DefinitionUpdatedAt,
		})
	}
	return out, nil
}

func (s *Service) CurrentBinding() (Resolution, bool, error) {
	return Resolution{}, false, identity.ErrPrincipalRequired
}

func (s *Service) CurrentBindingForPrincipal(principal identity.Principal) (Resolution, bool, error) {
	if err := requirePrincipal(principal); err != nil {
		return Resolution{}, false, err
	}
	binding, ok, err := s.store.GetCurrentForAccount(principal.AccountScopeID, principal.UserID)
	if err != nil {
		return Resolution{}, false, err
	}
	if !ok {
		return Resolution{}, false, nil
	}
	entry, entryOK, err := s.store.GetForAccount(principal.AccountScopeID, binding.Path)
	if err != nil {
		return Resolution{}, false, err
	}
	themeID := ""
	if entryOK {
		themeID = normalizeWorkspaceThemeID(entry.ThemeID)
	}
	if entryOK {
		name := strings.TrimSpace(binding.Name)
		if name == "" {
			name = strings.TrimSpace(entry.Name)
		}
		if name == "" {
			name = defaultWorkspaceName(entry.Path)
		}
		return resolutionForEntry(binding.Path, binding.Path, entry, name), true, nil
	}
	return resolutionForWorkspace(binding.Path, binding.Path, binding.Path, binding.WorkspaceID, binding.WorkspaceGeneration, "", binding.Name, themeID), true, nil
}

// ScopeForPath is a legacy, principal-less resolver. Do not use it from run,
// tool, API, or other principal-backed runtime paths; use
// ScopeForPathForPrincipal instead so account-scoped linked directories are
// included and legacy global entries are not consulted.
func (s *Service) ScopeForPath(path string) (Scope, error) {
	return s.legacyScopeForPath(path)
}

func (s *Service) ScopeForPathForPrincipal(principal identity.Principal, path string) (Scope, error) {
	if err := requirePrincipal(principal); err != nil {
		return Scope{}, err
	}
	resolved, err := resolvePath(path)
	if err != nil {
		return Scope{}, err
	}
	entries, err := s.store.ListForAccount(principal.AccountScopeID, 100000)
	if err != nil {
		return Scope{}, err
	}

	bestIndex := -1
	bestRoot := ""
	bestIsPrimary := false
	for i, entry := range entries {
		primaryPath := strings.TrimSpace(entry.Path)
		roots := entry.Directories
		if len(roots) == 0 {
			roots = []string{entry.Path}
		}
		for _, root := range roots {
			if !pathWithinRoot(root, resolved) {
				continue
			}
			canonicalRoot, err := revalidateStoredDirectory(root)
			if err != nil {
				return Scope{}, err
			}
			if !pathWithinRoot(canonicalRoot, resolved) {
				continue
			}
			isPrimary := canonicalRoot == primaryPath
			if len(canonicalRoot) > len(bestRoot) || (len(canonicalRoot) == len(bestRoot) && isPrimary && !bestIsPrimary) {
				bestRoot = canonicalRoot
				bestIndex = i
				bestIsPrimary = isPrimary
			}
		}
	}
	if bestIndex < 0 {
		return scopeForWorkspace(path, resolved, resolved, "", 0, "", defaultWorkspaceName(resolved), "", []string{resolved}, false), nil
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
	return scopeForEntry(path, resolved, entry, name, directories, true), nil
}

// ScopeForWorkspace reads only legacy global workspace entries. Principal-backed
// callers must use ScopeForWorkspaceForPrincipal.
func (s *Service) ScopeForWorkspace(path string) (Scope, error) {
	resolved, err := resolvePath(path)
	if err != nil {
		return Scope{}, err
	}
	entry, ok, err := s.store.GetLegacy(resolved)
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
	return scopeForEntry(path, resolved, entry, name, directories, true), nil
}

func (s *Service) ScopeForWorkspaceForPrincipal(principal identity.Principal, path string) (Scope, error) {
	if err := requirePrincipal(principal); err != nil {
		return Scope{}, err
	}
	resolved, err := resolvePath(path)
	if err != nil {
		return Scope{}, err
	}
	entry, ok, err := s.store.GetForAccount(principal.AccountScopeID, resolved)
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
	return scopeForEntry(path, resolved, entry, name, directories, true), nil
}

func (s *Service) Browse(path string) (BrowseResult, error) {
	return BrowseResult{}, identity.ErrPrincipalRequired
}

func (s *Service) BrowseForPrincipal(principal identity.Principal, path string) (BrowseResult, error) {
	if err := requirePrincipal(principal); err != nil {
		return BrowseResult{}, err
	}
	resolved, err := s.resolveBrowsePath(path)
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
		isGitRepo, hasSwarm := detectWorkspaceSignals(fullPath)
		items = append(items, BrowseEntry{
			Path:        fullPath,
			Name:        name,
			IsDirectory: true,
			IsGitRepo:   isGitRepo,
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
	homePath, err := s.resolveBrowseHomePath()
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

func (s *Service) CreateFolder(parentPath, name string) (CreateFolderResult, error) {
	return CreateFolderResult{}, identity.ErrPrincipalRequired
}

func (s *Service) CreateFolderForPrincipal(principal identity.Principal, parentPath, name string) (CreateFolderResult, error) {
	if err := requirePrincipal(principal); err != nil {
		return CreateFolderResult{}, err
	}
	parent, err := resolveBrowsePath(parentPath)
	if err != nil {
		return CreateFolderResult{}, err
	}
	if err := ensureWorkspaceDirectory(parent); err != nil {
		return CreateFolderResult{}, err
	}
	folderName, err := sanitizeCreateFolderName(name)
	if err != nil {
		return CreateFolderResult{}, err
	}
	target := filepath.Join(parent, folderName)
	if filepath.Dir(target) != parent {
		return CreateFolderResult{}, fmt.Errorf("folder name must stay inside the current folder")
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		result := createFolderErrorResult(target, folderName, parent, err)
		if result.RequiresSudo {
			return result, fmt.Errorf("opening %q requires sudo or read permission", parent)
		}
		return result, fmt.Errorf("open folder root %q: %w", parent, err)
	}
	defer root.Close()
	if err := root.Mkdir(folderName, 0o755); err != nil {
		result := createFolderErrorResult(target, folderName, parent, err)
		if result.RequiresSudo {
			return result, fmt.Errorf("creating %q requires sudo or write permission for %q", folderName, parent)
		}
		return result, fmt.Errorf("create folder %q: %w", target, err)
	}
	return CreateFolderResult{
		Path:       target,
		Name:       folderName,
		ParentPath: parent,
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
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("resolve canonical path for %q: %w", abs, err)
	}

	// Paths used for prospective files may not exist yet. Resolve the deepest
	// existing ancestor, but never treat a dangling symlink as lexical authority.
	cursor := abs
	missing := make([]string, 0, 4)
	for {
		if _, lstatErr := os.Lstat(cursor); lstatErr == nil {
			return "", fmt.Errorf("resolve canonical path for %q: %w", abs, err)
		} else if !errors.Is(lstatErr, os.ErrNotExist) {
			return "", fmt.Errorf("inspect path %q while resolving %q: %w", cursor, abs, lstatErr)
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			return "", fmt.Errorf("resolve canonical path for %q: %w", abs, err)
		}
		missing = append(missing, filepath.Base(cursor))
		cursor = parent
		ancestor, ancestorErr := filepath.EvalSymlinks(cursor)
		if ancestorErr != nil {
			if errors.Is(ancestorErr, os.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("resolve canonical ancestor %q for %q: %w", cursor, abs, ancestorErr)
		}
		for i := len(missing) - 1; i >= 0; i-- {
			ancestor = filepath.Join(ancestor, missing[i])
		}
		return ancestor, nil
	}
}

func resolveBrowsePath(input string) (string, error) {
	target := strings.TrimSpace(input)
	if target == "" {
		return resolveBrowseHomePath()
	}
	return resolvePath(target)
}

func (s *Service) resolveBrowsePath(input string) (string, error) {
	target := strings.TrimSpace(input)
	if target == "" {
		return s.resolveBrowseHomePath()
	}
	return resolvePath(target)
}

func (s *Service) resolveBrowseHomePath() (string, error) {
	return resolveBrowseHomePath()
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

// legacyScopeForPath deliberately consults only pre-account global workspace
// entries. It must remain unexported and must never be used as a fallback from
// principal-backed runtime resolution.
func (s *Service) legacyScopeForPath(path string) (Scope, error) {
	resolved, err := resolvePath(path)
	if err != nil {
		return Scope{}, err
	}
	entries, err := s.store.ListLegacy(100000)
	if err != nil {
		return Scope{}, err
	}
	bestIndex := -1
	bestRoot := ""
	bestIsPrimary := false
	for i, entry := range entries {
		primaryPath := strings.TrimSpace(entry.Path)
		roots := entry.Directories
		if len(roots) == 0 {
			roots = []string{entry.Path}
		}
		for _, root := range roots {
			if !pathWithinRoot(root, resolved) {
				continue
			}
			canonicalRoot, err := revalidateStoredDirectory(root)
			if err != nil {
				return Scope{}, err
			}
			if !pathWithinRoot(canonicalRoot, resolved) {
				continue
			}
			isPrimary := canonicalRoot == primaryPath
			if len(canonicalRoot) > len(bestRoot) || (len(canonicalRoot) == len(bestRoot) && isPrimary && !bestIsPrimary) {
				bestRoot = canonicalRoot
				bestIndex = i
				bestIsPrimary = isPrimary
			}
		}
	}
	if bestIndex < 0 {
		return scopeForWorkspace(path, resolved, resolved, "", 0, "", defaultWorkspaceName(resolved), "", []string{resolved}, false), nil
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
	return scopeForEntry(path, resolved, entry, name, directories, true), nil
}

func requirePrincipal(principal identity.Principal) error {
	if !principal.Valid() {
		return identity.ErrPrincipalRequired
	}
	return nil
}

func filesystemRootPath(path string) string {
	volume := filepath.VolumeName(path)
	if volume != "" {
		return volume + string(filepath.Separator)
	}
	return string(filepath.Separator)
}

func revalidateStoredDirectory(path string) (string, error) {
	stored := strings.TrimSpace(path)
	if stored == "" {
		return "", fmt.Errorf("stored workspace directory is empty")
	}
	abs, err := filepath.Abs(stored)
	if err != nil {
		return "", fmt.Errorf("resolve stored workspace directory %q: %w", stored, err)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("revalidate stored workspace directory %q: %w", stored, err)
	}
	if canonical != filepath.Clean(abs) {
		return "", fmt.Errorf("stored workspace directory %q no longer resolves to its saved identity %q", stored, canonical)
	}
	if err := ensureWorkspaceDirectory(canonical); err != nil {
		return "", err
	}
	return canonical, nil
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

func sanitizeCreateFolderName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("folder name is required")
	}
	if name == "." || name == ".." {
		return "", fmt.Errorf("folder name cannot be %q", name)
	}
	if strings.ContainsAny(name, `/\\`) {
		return "", fmt.Errorf("folder name cannot contain path separators")
	}
	if filepath.Clean(name) != name {
		return "", fmt.Errorf("folder name must be a single folder name")
	}
	return name, nil
}

func createFolderErrorResult(target, name, parent string, err error) CreateFolderResult {
	return CreateFolderResult{
		Path:                   target,
		Name:                   name,
		ParentPath:             parent,
		RequiresSudo:           isPermissionError(err),
		PermissionErrorMessage: permissionErrorMessage(err),
	}
}

func isPermissionError(err error) bool {
	return errors.Is(err, os.ErrPermission)
}

func permissionErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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

func resolutionFromScope(requestedPath string, scope Scope) Resolution {
	return Resolution{
		RequestedPath:       requestedPath,
		ResolvedPath:        scope.ResolvedPath,
		WorkspaceID:         scope.WorkspaceID,
		WorkspaceGeneration: scope.WorkspaceGeneration,
		WorkspaceState:      scope.WorkspaceState,
		WorkspacePath:       scope.WorkspacePath,
		WorkspaceName:       scope.WorkspaceName,
		ThemeID:             scope.ThemeID,
	}
}

func resolutionForEntry(requestedPath, resolvedPath string, entry pebblestore.WorkspaceEntry, workspaceName string) Resolution {
	resolution := resolutionForWorkspace(requestedPath, resolvedPath, entry.Path, entry.WorkspaceID, entry.WorkspaceGeneration, entry.State, workspaceName, normalizeWorkspaceThemeID(entry.ThemeID))
	resolution.Definition = entry.Definition
	resolution.DefinitionStatus = entry.DefinitionStatus
	resolution.DefinitionAttemptCount = entry.DefinitionAttemptCount
	resolution.DefinitionGeneration = entry.DefinitionGeneration
	resolution.DefinitionError = entry.DefinitionError
	resolution.DefinitionModelSuggestion = entry.DefinitionModelSuggestion
	resolution.DefinitionPendingAt = entry.DefinitionPendingAt
	resolution.DefinitionCompletedAt = entry.DefinitionCompletedAt
	resolution.DefinitionFailedAt = entry.DefinitionFailedAt
	resolution.DefinitionUpdatedAt = entry.DefinitionUpdatedAt
	return resolution
}

func resolutionForWorkspace(requestedPath, resolvedPath, workspacePath, workspaceID string, workspaceGeneration int64, workspaceState, workspaceName, themeID string) Resolution {
	return Resolution{
		RequestedPath:       requestedPath,
		ResolvedPath:        resolvedPath,
		WorkspaceID:         workspaceID,
		WorkspaceGeneration: workspaceGeneration,
		WorkspaceState:      workspaceState,
		WorkspacePath:       workspacePath,
		WorkspaceName:       workspaceName,
		ThemeID:             themeID,
	}
}

func scopeForEntry(requestedPath, resolvedPath string, entry pebblestore.WorkspaceEntry, workspaceName string, directories []string, matched bool) Scope {
	return scopeForWorkspace(requestedPath, resolvedPath, entry.Path, entry.WorkspaceID, entry.WorkspaceGeneration, entry.State, workspaceName, normalizeWorkspaceThemeID(entry.ThemeID), directories, matched)
}

func scopeForWorkspace(requestedPath, resolvedPath, workspacePath, workspaceID string, workspaceGeneration int64, workspaceState, workspaceName, themeID string, directories []string, matched bool) Scope {
	return Scope{
		RequestedPath:       requestedPath,
		ResolvedPath:        resolvedPath,
		WorkspaceID:         workspaceID,
		WorkspaceGeneration: workspaceGeneration,
		WorkspaceState:      workspaceState,
		WorkspacePath:       workspacePath,
		WorkspaceName:       workspaceName,
		ThemeID:             themeID,
		Directories:         directories,
		Matched:             matched,
	}
}
