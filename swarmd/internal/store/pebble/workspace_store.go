package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type WorkspaceBinding struct {
	WorkspaceID         string `json:"workspace_id,omitempty"`
	WorkspaceGeneration int64  `json:"workspace_generation,omitempty"`
	Path                string `json:"path"`
	Name                string `json:"name"`
	ResolvedAt          int64  `json:"resolved_at"`
}

type WorkspaceReplicationSync struct {
	Enabled bool     `json:"enabled"`
	Mode    string   `json:"mode,omitempty"`
	Modules []string `json:"modules,omitempty"`
}

const (
	WorkspaceDefinitionStatusPending   = "pending"
	WorkspaceDefinitionStatusCompleted = "completed"
	WorkspaceDefinitionStatusFailed    = "failed"
)

type WorkspaceEntry struct {
	AccountScopeID            string   `json:"account_scope_id,omitempty"`
	WorkspaceID               string   `json:"workspace_id"`
	WorkspaceGeneration       int64    `json:"workspace_generation"`
	State                     string   `json:"state,omitempty"`
	Path                      string   `json:"path"`
	Name                      string   `json:"name"`
	ThemeID                   string   `json:"theme_id,omitempty"`
	IconPNGDataURL            string   `json:"icon_png_data_url,omitempty"`
	Directories               []string `json:"directories,omitempty"`
	SortIndex                 int      `json:"sort_index,omitempty"`
	AddedAt                   int64    `json:"added_at"`
	UpdatedAt                 int64    `json:"updated_at"`
	LastSelectedAt            int64    `json:"last_selected_at"`
	Definition                string   `json:"definition,omitempty"`
	DefinitionStatus          string   `json:"definition_status,omitempty"`
	DefinitionAttemptCount    int      `json:"definition_attempt_count,omitempty"`
	DefinitionGeneration      int64    `json:"definition_generation,omitempty"`
	DefinitionError           string   `json:"definition_error,omitempty"`
	DefinitionModelSuggestion string   `json:"definition_model_suggestion,omitempty"`
	DefinitionPendingAt       int64    `json:"definition_pending_at,omitempty"`
	DefinitionCompletedAt     int64    `json:"definition_completed_at,omitempty"`
	DefinitionFailedAt        int64    `json:"definition_failed_at,omitempty"`
	DefinitionUpdatedAt       int64    `json:"definition_updated_at,omitempty"`
}

type WorkspaceStore struct {
	store        *Store
	definitionMu sync.Mutex
}

func NewWorkspaceStore(store *Store) *WorkspaceStore {
	return &WorkspaceStore{store: store}
}

func (s *WorkspaceStore) SetCurrentForAccount(accountScopeID, userID, path, name string) (WorkspaceBinding, error) {
	accountScopeID, userID, err := requireWorkspacePrincipalParts(accountScopeID, userID)
	if err != nil {
		return WorkspaceBinding{}, err
	}
	entry, err := s.upsertForAccount(accountScopeID, path, name, "", true)
	if err != nil {
		return WorkspaceBinding{}, err
	}
	binding := WorkspaceBinding{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: entry.Path, Name: entry.Name, ResolvedAt: time.Now().UnixMilli()}
	if err := s.store.PutJSON(KeyWorkspaceCurrentForAccount(accountScopeID, userID), binding); err != nil {
		return WorkspaceBinding{}, err
	}
	return binding, nil
}

func (s *WorkspaceStore) AddForAccount(accountScopeID, path, name string) (WorkspaceEntry, error) {
	return s.upsertForAccount(accountScopeID, path, name, "", false)
}

// MarkDefinitionPendingForAccount starts a new workspace-definition generation.
// It is intentionally persisted before the caller launches asynchronous analysis.
func (s *WorkspaceStore) MarkDefinitionPendingForAccount(accountScopeID, path string) (WorkspaceEntry, error) {
	s.definitionMu.Lock()
	defer s.definitionMu.Unlock()
	entry, ok, err := s.GetForAccount(accountScopeID, path)
	if err != nil {
		return WorkspaceEntry{}, err
	}
	if !ok {
		return WorkspaceEntry{}, fmt.Errorf("workspace %q not found", strings.TrimSpace(path))
	}
	now := time.Now().UnixMilli()
	entry.DefinitionGeneration++
	if entry.DefinitionGeneration <= 0 {
		entry.DefinitionGeneration = 1
	}
	entry.Definition = ""
	entry.DefinitionStatus = WorkspaceDefinitionStatusPending
	entry.DefinitionAttemptCount = 0
	entry.DefinitionError = ""
	entry.DefinitionModelSuggestion = ""
	entry.DefinitionPendingAt = now
	entry.DefinitionCompletedAt = 0
	entry.DefinitionFailedAt = 0
	entry.DefinitionUpdatedAt = now
	entry.UpdatedAt = now
	if err := s.putWorkspaceEntryForAccount(accountScopeID, entry); err != nil {
		return WorkspaceEntry{}, err
	}
	return entry, nil
}

func (s *WorkspaceStore) RecordDefinitionAttemptForAccount(accountScopeID, path string, generation int64, attempt int) (WorkspaceEntry, bool, error) {
	s.definitionMu.Lock()
	defer s.definitionMu.Unlock()
	entry, current, err := s.definitionEntryForGeneration(accountScopeID, path, generation)
	if err != nil || !current {
		return entry, current, err
	}
	if attempt < 1 {
		attempt = 1
	}
	entry.DefinitionAttemptCount = attempt
	entry.DefinitionUpdatedAt = time.Now().UnixMilli()
	entry.UpdatedAt = entry.DefinitionUpdatedAt
	if err := s.putWorkspaceEntryForAccount(accountScopeID, entry); err != nil {
		return WorkspaceEntry{}, false, err
	}
	return entry, true, nil
}

func (s *WorkspaceStore) CompleteDefinitionForAccount(accountScopeID, path string, generation int64, definition string, attempts int) (WorkspaceEntry, bool, error) {
	s.definitionMu.Lock()
	defer s.definitionMu.Unlock()
	entry, current, err := s.definitionEntryForGeneration(accountScopeID, path, generation)
	if err != nil || !current {
		return entry, current, err
	}
	now := time.Now().UnixMilli()
	entry.Definition = strings.TrimSpace(definition)
	entry.DefinitionStatus = WorkspaceDefinitionStatusCompleted
	entry.DefinitionAttemptCount = attempts
	entry.DefinitionError = ""
	entry.DefinitionModelSuggestion = ""
	entry.DefinitionCompletedAt = now
	entry.DefinitionFailedAt = 0
	entry.DefinitionUpdatedAt = now
	entry.UpdatedAt = now
	if err := s.putWorkspaceEntryForAccount(accountScopeID, entry); err != nil {
		return WorkspaceEntry{}, false, err
	}
	return entry, true, nil
}

func (s *WorkspaceStore) FailDefinitionForAccount(accountScopeID, path string, generation int64, failure, suggestion string, attempts int) (WorkspaceEntry, bool, error) {
	s.definitionMu.Lock()
	defer s.definitionMu.Unlock()
	entry, current, err := s.definitionEntryForGeneration(accountScopeID, path, generation)
	if err != nil || !current {
		return entry, current, err
	}
	now := time.Now().UnixMilli()
	entry.Definition = ""
	entry.DefinitionStatus = WorkspaceDefinitionStatusFailed
	entry.DefinitionAttemptCount = attempts
	entry.DefinitionError = strings.TrimSpace(failure)
	entry.DefinitionModelSuggestion = strings.TrimSpace(suggestion)
	entry.DefinitionCompletedAt = 0
	entry.DefinitionFailedAt = now
	entry.DefinitionUpdatedAt = now
	entry.UpdatedAt = now
	if err := s.putWorkspaceEntryForAccount(accountScopeID, entry); err != nil {
		return WorkspaceEntry{}, false, err
	}
	return entry, true, nil
}

func (s *WorkspaceStore) definitionEntryForGeneration(accountScopeID, path string, generation int64) (WorkspaceEntry, bool, error) {
	entry, ok, err := s.GetForAccount(accountScopeID, path)
	if err != nil {
		return WorkspaceEntry{}, false, err
	}
	if !ok {
		return WorkspaceEntry{}, false, fmt.Errorf("workspace %q not found", strings.TrimSpace(path))
	}
	return entry, entry.DefinitionGeneration == generation && entry.DefinitionStatus == WorkspaceDefinitionStatusPending, nil
}

func (s *WorkspaceStore) SaveForAccount(accountScopeID, path, name, themeID string, selected bool) (WorkspaceEntry, error) {
	return s.upsertForAccount(accountScopeID, path, name, themeID, selected)
}

func (s *WorkspaceStore) SaveForAccountWithResult(accountScopeID, path, name, themeID string, selected bool) (WorkspaceEntry, bool, error) {
	return s.upsertForAccountWithResult(accountScopeID, path, name, themeID, selected)
}

func (s *WorkspaceStore) RenameForAccount(accountScopeID, userID, path, name string) (WorkspaceEntry, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	userID = strings.TrimSpace(userID)
	if accountScopeID == "" {
		return WorkspaceEntry{}, fmt.Errorf("account scope is required")
	}
	path = strings.TrimSpace(path)
	name = strings.TrimSpace(name)
	if path == "" {
		return WorkspaceEntry{}, fmt.Errorf("workspace path is required")
	}
	if name == "" {
		return WorkspaceEntry{}, fmt.Errorf("workspace name is required")
	}
	key := KeyWorkspaceEntryForAccount(accountScopeID, path)
	var entry WorkspaceEntry
	ok, err := s.store.GetJSON(key, &entry)
	if err != nil {
		return WorkspaceEntry{}, err
	}
	if !ok {
		return WorkspaceEntry{}, fmt.Errorf("workspace %q not found", path)
	}
	now := time.Now().UnixMilli()
	entry = normalizeWorkspaceEntryForAccount(accountScopeID, entry)
	entry.Name = name
	entry.UpdatedAt = now
	if err := s.putWorkspaceEntryForAccount(accountScopeID, entry); err != nil {
		return WorkspaceEntry{}, err
	}
	if userID != "" {
		current, hasCurrent, err := s.GetCurrentForAccount(accountScopeID, userID)
		if err != nil {
			return WorkspaceEntry{}, err
		}
		if hasCurrent && current.Path == path {
			current.WorkspaceID = entry.WorkspaceID
			current.WorkspaceGeneration = entry.WorkspaceGeneration
			current.Name = name
			current.ResolvedAt = now
			if err := s.store.PutJSON(KeyWorkspaceCurrentForAccount(accountScopeID, userID), current); err != nil {
				return WorkspaceEntry{}, err
			}
		}
	}
	return entry, nil
}

func (s *WorkspaceStore) SetThemeIDForAccount(accountScopeID, path, themeID string) (WorkspaceEntry, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return WorkspaceEntry{}, fmt.Errorf("account scope is required")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return WorkspaceEntry{}, fmt.Errorf("workspace path is required")
	}
	key := KeyWorkspaceEntryForAccount(accountScopeID, path)
	var entry WorkspaceEntry
	ok, err := s.store.GetJSON(key, &entry)
	if err != nil {
		return WorkspaceEntry{}, err
	}
	if !ok {
		return WorkspaceEntry{}, fmt.Errorf("workspace %q not found", path)
	}
	entry = normalizeWorkspaceEntryForAccount(accountScopeID, entry)
	entry.ThemeID = normalizeWorkspaceThemeID(themeID)
	entry.UpdatedAt = time.Now().UnixMilli()
	if err := s.putWorkspaceEntryForAccount(accountScopeID, entry); err != nil {
		return WorkspaceEntry{}, err
	}
	return entry, nil
}

func (s *WorkspaceStore) SetIconPNGDataURLForAccount(accountScopeID, path, iconPNGDataURL string) (WorkspaceEntry, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return WorkspaceEntry{}, fmt.Errorf("account scope is required")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return WorkspaceEntry{}, fmt.Errorf("workspace path is required")
	}
	key := KeyWorkspaceEntryForAccount(accountScopeID, path)
	var entry WorkspaceEntry
	ok, err := s.store.GetJSON(key, &entry)
	if err != nil {
		return WorkspaceEntry{}, err
	}
	if !ok {
		return WorkspaceEntry{}, fmt.Errorf("workspace %q not found", path)
	}
	entry = normalizeWorkspaceEntryForAccount(accountScopeID, entry)
	entry.IconPNGDataURL = strings.TrimSpace(iconPNGDataURL)
	entry.UpdatedAt = time.Now().UnixMilli()
	if err := s.putWorkspaceEntryForAccount(accountScopeID, entry); err != nil {
		return WorkspaceEntry{}, err
	}
	return entry, nil
}

func (s *WorkspaceStore) DeleteForAccount(accountScopeID, userID, path string) error {
	accountScopeID = strings.TrimSpace(accountScopeID)
	userID = strings.TrimSpace(userID)
	if accountScopeID == "" {
		return fmt.Errorf("account scope is required")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("workspace path is required")
	}
	entry, ok, err := s.GetForAccount(accountScopeID, path)
	if err != nil {
		return err
	}
	if err := s.store.Delete(KeyWorkspaceEntryForAccount(accountScopeID, path)); err != nil {
		return err
	}
	if ok && strings.TrimSpace(entry.WorkspaceID) != "" {
		if err := s.store.Delete(KeyWorkspaceEntryByIDForAccount(accountScopeID, entry.WorkspaceID)); err != nil {
			return err
		}
	}
	if userID != "" {
		current, hasCurrent, err := s.GetCurrentForAccount(accountScopeID, userID)
		if err != nil {
			return err
		}
		if hasCurrent && current.Path == path {
			if err := s.store.Delete(KeyWorkspaceCurrentForAccount(accountScopeID, userID)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *WorkspaceStore) GetForAccount(accountScopeID, path string) (WorkspaceEntry, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return WorkspaceEntry{}, false, fmt.Errorf("account scope is required")
	}
	var entry WorkspaceEntry
	ok, err := s.store.GetJSON(KeyWorkspaceEntryForAccount(accountScopeID, strings.TrimSpace(path)), &entry)
	if err != nil {
		return WorkspaceEntry{}, false, err
	}
	if !ok {
		return WorkspaceEntry{}, false, nil
	}
	entry = normalizeWorkspaceEntryForAccount(accountScopeID, entry)
	return entry, true, nil
}

func (s *WorkspaceStore) ListForAccount(accountScopeID string, limit int) ([]WorkspaceEntry, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, fmt.Errorf("account scope is required")
	}
	if limit <= 0 {
		limit = 200
	}
	out, err := s.listAllForAccount(accountScopeID)
	if err != nil {
		return nil, err
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *WorkspaceStore) MoveForAccount(accountScopeID, path string, delta int) (WorkspaceEntry, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return WorkspaceEntry{}, fmt.Errorf("account scope is required")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return WorkspaceEntry{}, fmt.Errorf("workspace path is required")
	}
	if delta == 0 {
		entry, ok, err := s.GetForAccount(accountScopeID, path)
		if err != nil {
			return WorkspaceEntry{}, err
		}
		if !ok {
			return WorkspaceEntry{}, fmt.Errorf("workspace %q not found", path)
		}
		return entry, nil
	}
	entries, err := s.listAllForAccount(accountScopeID)
	if err != nil {
		return WorkspaceEntry{}, err
	}
	index := -1
	for i, entry := range entries {
		if entry.Path == path {
			index = i
			break
		}
	}
	if index < 0 {
		return WorkspaceEntry{}, fmt.Errorf("workspace %q not found", path)
	}
	target := index + delta
	if target < 0 {
		target = 0
	}
	if target >= len(entries) {
		target = len(entries) - 1
	}
	if target == index {
		return entries[index], nil
	}
	moved := entries[index]
	copy(entries[index:], entries[index+1:])
	entries[len(entries)-1] = moved
	if target < len(entries)-1 {
		copy(entries[target+1:], entries[target:len(entries)-1])
		entries[target] = moved
	}
	now := time.Now().UnixMilli()
	for i := range entries {
		entries[i] = normalizeWorkspaceEntryForAccount(accountScopeID, entries[i])
		entries[i].SortIndex = i
		if entries[i].Path == path {
			entries[i].UpdatedAt = now
		}
		if err := s.putWorkspaceEntryForAccount(accountScopeID, entries[i]); err != nil {
			return WorkspaceEntry{}, err
		}
	}
	return entries[target], nil
}

func (s *WorkspaceStore) AddDirectoryForAccount(accountScopeID, path, directory string) (WorkspaceEntry, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return WorkspaceEntry{}, fmt.Errorf("account scope is required")
	}
	path = strings.TrimSpace(path)
	directory = strings.TrimSpace(directory)
	if path == "" {
		return WorkspaceEntry{}, fmt.Errorf("workspace path is required")
	}
	if directory == "" {
		return WorkspaceEntry{}, fmt.Errorf("directory path is required")
	}
	entry, ok, err := s.GetForAccount(accountScopeID, path)
	if err != nil {
		return WorkspaceEntry{}, err
	}
	if !ok {
		return WorkspaceEntry{}, fmt.Errorf("workspace %q not found", path)
	}
	for _, existing := range entry.Directories {
		if existing == directory {
			return WorkspaceEntry{}, fmt.Errorf("directory %q is already linked to workspace %q", directory, path)
		}
	}
	entry = normalizeWorkspaceEntryForAccount(accountScopeID, entry)
	entry.Directories = append(entry.Directories, directory)
	entry.Directories = normalizeWorkspaceDirectories(entry.Path, entry.Directories)
	entry.UpdatedAt = time.Now().UnixMilli()
	if err := s.putWorkspaceEntryForAccount(accountScopeID, entry); err != nil {
		return WorkspaceEntry{}, err
	}
	return entry, nil
}

func (s *WorkspaceStore) RemoveDirectoryForAccount(accountScopeID, path, directory string) (WorkspaceEntry, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return WorkspaceEntry{}, fmt.Errorf("account scope is required")
	}
	path = strings.TrimSpace(path)
	directory = strings.TrimSpace(directory)
	if path == "" {
		return WorkspaceEntry{}, fmt.Errorf("workspace path is required")
	}
	if directory == "" {
		return WorkspaceEntry{}, fmt.Errorf("directory path is required")
	}
	entry, ok, err := s.GetForAccount(accountScopeID, path)
	if err != nil {
		return WorkspaceEntry{}, err
	}
	if !ok {
		return WorkspaceEntry{}, fmt.Errorf("workspace %q not found", path)
	}
	if entry.Path == directory {
		return WorkspaceEntry{}, fmt.Errorf("primary workspace directory cannot be removed")
	}
	updated := make([]string, 0, len(entry.Directories))
	removed := false
	for _, existing := range entry.Directories {
		if existing == directory {
			removed = true
			continue
		}
		updated = append(updated, existing)
	}
	if !removed {
		return WorkspaceEntry{}, fmt.Errorf("directory %q is not linked to workspace %q", directory, path)
	}
	entry = normalizeWorkspaceEntryForAccount(accountScopeID, entry)
	entry.Directories = normalizeWorkspaceDirectories(entry.Path, updated)
	entry.UpdatedAt = time.Now().UnixMilli()
	if err := s.putWorkspaceEntryForAccount(accountScopeID, entry); err != nil {
		return WorkspaceEntry{}, err
	}
	return entry, nil
}

func (s *WorkspaceStore) GetCurrentForAccount(accountScopeID, userID string) (WorkspaceBinding, bool, error) {
	accountScopeID, userID, err := requireWorkspacePrincipalParts(accountScopeID, userID)
	if err != nil {
		return WorkspaceBinding{}, false, err
	}
	var binding WorkspaceBinding
	ok, err := s.store.GetJSON(KeyWorkspaceCurrentForAccount(accountScopeID, userID), &binding)
	if err != nil {
		return WorkspaceBinding{}, false, err
	}
	if !ok {
		return WorkspaceBinding{}, false, nil
	}
	return binding, true, nil
}

// Principal-less workspace mutations are intentionally disabled. Runtime and
// API callers must use the ForAccount methods so saved linked directories are
// isolated by account scope instead of drifting back to legacy global keys.
func (s *WorkspaceStore) SetCurrent(path, name string) (WorkspaceBinding, error) {
	return WorkspaceBinding{}, fmt.Errorf("legacy global workspace current is disabled; account scope is required")
}

func (s *WorkspaceStore) Add(path, name string) (WorkspaceEntry, error) {
	return WorkspaceEntry{}, fmt.Errorf("legacy global workspace add is disabled; account scope is required")
}

func (s *WorkspaceStore) Save(path, name, themeID string, selected bool) (WorkspaceEntry, error) {
	return WorkspaceEntry{}, fmt.Errorf("legacy global workspace save is disabled; account scope is required")
}

func (s *WorkspaceStore) Rename(path, name string) (WorkspaceEntry, error) {
	return WorkspaceEntry{}, fmt.Errorf("legacy global workspace rename is disabled; account scope is required")
}

func (s *WorkspaceStore) SetThemeID(path, themeID string) (WorkspaceEntry, error) {
	return WorkspaceEntry{}, fmt.Errorf("legacy global workspace theme is disabled; account scope is required")
}

func (s *WorkspaceStore) Delete(path string) error {
	return fmt.Errorf("legacy global workspace delete is disabled; account scope is required")
}

func (s *WorkspaceStore) Get(path string) (WorkspaceEntry, bool, error) {
	return WorkspaceEntry{}, false, fmt.Errorf("legacy global workspace get is disabled; account scope is required")
}

func (s *WorkspaceStore) List(limit int) ([]WorkspaceEntry, error) {
	return nil, fmt.Errorf("legacy global workspace list is disabled; account scope is required")
}

// GetLegacy reads only pre-account global workspace entries for internal/bootstrap
// flows that have not been given a canonical principal yet. It never falls back
// from account-scoped keys and must not be used by authenticated API handlers.
func (s *WorkspaceStore) GetLegacy(path string) (WorkspaceEntry, bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return WorkspaceEntry{}, false, fmt.Errorf("workspace path is required")
	}
	var entry WorkspaceEntry
	ok, err := s.store.GetJSON(KeyWorkspaceEntry(path), &entry)
	if err != nil {
		return WorkspaceEntry{}, false, err
	}
	if !ok {
		return WorkspaceEntry{}, false, nil
	}
	entry = normalizeWorkspaceEntryForAccount("", entry)
	return entry, true, nil
}

// ListLegacy lists only pre-account global workspace entries for explicit
// internal/bootstrap compatibility. It never reads account-scoped workspace keys.
func (s *WorkspaceStore) ListLegacy(limit int) ([]WorkspaceEntry, error) {
	if limit <= 0 {
		limit = 200
	}
	out := make([]WorkspaceEntry, 0, 200)
	err := s.store.IteratePrefix(KeyWorkspaceEntryPrefix, 100000, func(_ string, value []byte) error {
		var entry WorkspaceEntry
		if err := json.Unmarshal(value, &entry); err != nil {
			return err
		}
		if strings.TrimSpace(entry.Path) == "" {
			return nil
		}
		entry = normalizeWorkspaceEntryForAccount("", entry)
		out = append(out, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortWorkspaceEntries(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *WorkspaceStore) Move(path string, delta int) (WorkspaceEntry, error) {
	return WorkspaceEntry{}, fmt.Errorf("legacy global workspace move is disabled; account scope is required")
}

func (s *WorkspaceStore) GetCurrent() (WorkspaceBinding, bool, error) {
	return WorkspaceBinding{}, false, fmt.Errorf("legacy global workspace current is disabled; account scope is required")
}

// AddDirectory is disabled for legacy global workspaces. Use
// AddDirectoryForAccount for persistent linked roots.
func (s *WorkspaceStore) AddDirectory(path, directory string) (WorkspaceEntry, error) {
	return WorkspaceEntry{}, fmt.Errorf("legacy global workspace directory add is disabled; account scope is required")
}

func (s *WorkspaceStore) RemoveDirectory(path, directory string) (WorkspaceEntry, error) {
	return WorkspaceEntry{}, fmt.Errorf("legacy global workspace directory remove is disabled; account scope is required")
}

func (s *WorkspaceStore) upsertForAccount(accountScopeID, path, name, themeID string, selected bool) (WorkspaceEntry, error) {
	entry, _, err := s.upsertForAccountWithResult(accountScopeID, path, name, themeID, selected)
	return entry, err
}

func (s *WorkspaceStore) upsertForAccountWithResult(accountScopeID, path, name, themeID string, selected bool) (WorkspaceEntry, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return WorkspaceEntry{}, false, fmt.Errorf("account scope is required")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return WorkspaceEntry{}, false, fmt.Errorf("workspace path is required")
	}
	name = strings.TrimSpace(name)
	themeProvided := strings.TrimSpace(themeID) != ""
	themeID = normalizeWorkspaceThemeID(themeID)
	now := time.Now().UnixMilli()
	entries, err := s.listAllForAccount(accountScopeID)
	if err != nil {
		return WorkspaceEntry{}, false, err
	}
	var existing WorkspaceEntry
	ok := false
	for _, entry := range entries {
		if entry.Path == path {
			existing = entry
			ok = true
			break
		}
	}
	entry := WorkspaceEntry{AccountScopeID: accountScopeID, Path: path, Name: name, ThemeID: themeID, Directories: []string{path}}
	if ok {
		entry = normalizeWorkspaceEntryForAccount(accountScopeID, existing)
		entry.Path = path
		if strings.TrimSpace(name) != "" {
			entry.Name = name
		}
		if themeProvided {
			entry.ThemeID = themeID
		}
		entry.Directories = normalizeWorkspaceDirectories(path, entry.Directories)
	} else {
		entry.WorkspaceID = newWorkspaceID()
		entry.WorkspaceGeneration = 1
		entry.State = normalizeWorkspaceState(entry.State)
		entry.AddedAt = now
		entry.SortIndex = len(entries)
	}
	if selected {
		entry.LastSelectedAt = now
	}
	entry.UpdatedAt = now
	if err := s.putWorkspaceEntryForAccount(accountScopeID, entry); err != nil {
		return WorkspaceEntry{}, false, err
	}
	return entry, !ok, nil
}

func (s *WorkspaceStore) UpdatePathForWorkspaceIDForAccount(accountScopeID, workspaceID, newPath string) (WorkspaceEntry, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return WorkspaceEntry{}, fmt.Errorf("account scope is required")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return WorkspaceEntry{}, fmt.Errorf("workspace id is required")
	}
	newPath = strings.TrimSpace(newPath)
	if newPath == "" {
		return WorkspaceEntry{}, fmt.Errorf("workspace path is required")
	}
	entry, ok, err := s.GetByWorkspaceIDForAccount(accountScopeID, workspaceID)
	if err != nil {
		return WorkspaceEntry{}, err
	}
	if !ok {
		return WorkspaceEntry{}, fmt.Errorf("workspace id %q not found", workspaceID)
	}
	if entry.Path == newPath {
		return entry, nil
	}
	existingAtPath, existsAtPath, err := s.GetForAccount(accountScopeID, newPath)
	if err != nil {
		return WorkspaceEntry{}, err
	}
	if existsAtPath && existingAtPath.WorkspaceID != entry.WorkspaceID {
		return WorkspaceEntry{}, fmt.Errorf("workspace path %q already belongs to workspace id %q", newPath, existingAtPath.WorkspaceID)
	}
	entry.Path = newPath
	entry.Directories = normalizeWorkspaceDirectories(newPath, entry.Directories)
	entry.WorkspaceGeneration++
	entry.UpdatedAt = time.Now().UnixMilli()
	if err := s.putWorkspaceEntryForAccount(accountScopeID, entry); err != nil {
		return WorkspaceEntry{}, err
	}
	return entry, nil
}

func (s *WorkspaceStore) GetByWorkspaceIDForAccount(accountScopeID, workspaceID string) (WorkspaceEntry, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return WorkspaceEntry{}, false, fmt.Errorf("account scope is required")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return WorkspaceEntry{}, false, fmt.Errorf("workspace id is required")
	}
	var entry WorkspaceEntry
	ok, err := s.store.GetJSON(KeyWorkspaceEntryByIDForAccount(accountScopeID, workspaceID), &entry)
	if err != nil {
		return WorkspaceEntry{}, false, err
	}
	if !ok {
		entries, err := s.listAllForAccount(accountScopeID)
		if err != nil {
			return WorkspaceEntry{}, false, err
		}
		for _, entry := range entries {
			if entry.WorkspaceID != workspaceID {
				continue
			}
			if err := s.putWorkspaceEntryForAccount(accountScopeID, entry); err != nil {
				return WorkspaceEntry{}, false, err
			}
			return entry, true, nil
		}
		return WorkspaceEntry{}, false, nil
	}
	entry = normalizeWorkspaceEntryForAccount(accountScopeID, entry)
	return entry, true, nil
}

func (s *WorkspaceStore) listAllForAccount(accountScopeID string) ([]WorkspaceEntry, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, fmt.Errorf("account scope is required")
	}
	out := make([]WorkspaceEntry, 0, 200)
	err := s.store.IteratePrefix(WorkspaceEntryPrefixForAccount(accountScopeID), 100000, func(_ string, value []byte) error {
		var entry WorkspaceEntry
		if err := json.Unmarshal(value, &entry); err != nil {
			return err
		}
		if strings.TrimSpace(entry.Path) == "" {
			return nil
		}
		entry = normalizeWorkspaceEntryForAccount(accountScopeID, entry)
		out = append(out, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortWorkspaceEntries(out)
	return out, nil
}

func sortWorkspaceEntries(out []WorkspaceEntry) {
	sort.SliceStable(out, func(i, j int) bool {
		left := out[i]
		right := out[j]
		if left.SortIndex != right.SortIndex {
			return left.SortIndex < right.SortIndex
		}
		if left.LastSelectedAt != right.LastSelectedAt {
			return left.LastSelectedAt > right.LastSelectedAt
		}
		if left.UpdatedAt != right.UpdatedAt {
			return left.UpdatedAt > right.UpdatedAt
		}
		return left.Path < right.Path
	})
	for i := range out {
		out[i].SortIndex = i
	}
}

func requireWorkspacePrincipalParts(accountScopeID, userID string) (string, string, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	userID = strings.TrimSpace(userID)
	if accountScopeID == "" {
		return "", "", fmt.Errorf("account scope is required")
	}
	if userID == "" {
		return "", "", fmt.Errorf("user id is required")
	}
	return accountScopeID, userID, nil
}

func normalizeWorkspaceDirectories(primary string, directories []string) []string {
	primary = strings.TrimSpace(primary)
	seen := make(map[string]struct{}, len(directories)+1)
	out := make([]string, 0, len(directories)+1)
	if primary != "" {
		out = append(out, primary)
		seen[primary] = struct{}{}
	}
	for _, raw := range directories {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	if len(out) == 0 && primary != "" {
		return []string{primary}
	}
	return out
}

func (s *WorkspaceStore) putWorkspaceEntryForAccount(accountScopeID string, entry WorkspaceEntry) error {
	entry = normalizeWorkspaceEntryForAccount(accountScopeID, entry)
	if strings.TrimSpace(entry.Path) == "" {
		return fmt.Errorf("workspace path is required")
	}
	if strings.TrimSpace(entry.WorkspaceID) == "" {
		return fmt.Errorf("workspace id is required")
	}
	oldByID := WorkspaceEntry{}
	if ok, err := s.store.GetJSON(KeyWorkspaceEntryByIDForAccount(accountScopeID, entry.WorkspaceID), &oldByID); err != nil {
		return err
	} else if ok && strings.TrimSpace(oldByID.Path) != "" && strings.TrimSpace(oldByID.Path) != entry.Path {
		if err := s.store.Delete(KeyWorkspaceEntryForAccount(accountScopeID, oldByID.Path)); err != nil {
			return err
		}
	}
	if err := s.store.PutJSON(KeyWorkspaceEntryForAccount(accountScopeID, entry.Path), entry); err != nil {
		return err
	}
	return s.store.PutJSON(KeyWorkspaceEntryByIDForAccount(accountScopeID, entry.WorkspaceID), entry)
}

func NormalizeWorkspaceEntryForAccount(accountScopeID string, entry WorkspaceEntry) WorkspaceEntry {
	return normalizeWorkspaceEntryForAccount(accountScopeID, entry)
}

func normalizeWorkspaceEntryForAccount(accountScopeID string, entry WorkspaceEntry) WorkspaceEntry {
	entry.AccountScopeID = strings.TrimSpace(accountScopeID)
	entry.Path = strings.TrimSpace(entry.Path)
	entry.Name = strings.TrimSpace(entry.Name)
	entry.ThemeID = normalizeWorkspaceThemeID(entry.ThemeID)
	entry.Directories = normalizeWorkspaceDirectories(entry.Path, entry.Directories)
	entry.State = normalizeWorkspaceState(entry.State)
	if entry.WorkspaceGeneration <= 0 {
		entry.WorkspaceGeneration = 1
	}
	if strings.TrimSpace(entry.WorkspaceID) == "" {
		entry.WorkspaceID = legacyWorkspaceID(accountScopeID, entry.Path)
	} else {
		entry.WorkspaceID = strings.TrimSpace(entry.WorkspaceID)
	}
	return entry
}

func normalizeWorkspaceState(state string) string {
	state = strings.TrimSpace(strings.ToLower(state))
	if state == "" {
		return "active"
	}
	return state
}

func newWorkspaceID() string {
	return "ws_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func legacyWorkspaceID(accountScopeID, path string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(accountScopeID) + "\x00" + strings.TrimSpace(path)))
	return "ws_legacy_" + hex.EncodeToString(sum[:16])
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
