package pebblestore

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type WorkspaceBinding struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	ResolvedAt int64  `json:"resolved_at"`
}

type WorkspaceReplicationSync struct {
	Enabled bool     `json:"enabled"`
	Mode    string   `json:"mode,omitempty"`
	Modules []string `json:"modules,omitempty"`
}

type WorkspaceReplicationLink struct {
	ID                  string                   `json:"id"`
	TargetKind          string                   `json:"target_kind"`
	TargetSwarmID       string                   `json:"target_swarm_id"`
	TargetSwarmName     string                   `json:"target_swarm_name"`
	TargetWorkspacePath string                   `json:"target_workspace_path"`
	ReplicationMode     string                   `json:"replication_mode"`
	Writable            bool                     `json:"writable"`
	Sync                WorkspaceReplicationSync `json:"sync"`
	CreatedAt           int64                    `json:"created_at"`
	UpdatedAt           int64                    `json:"updated_at"`
}

type WorkspaceEntry struct {
	AccountScopeID   string                     `json:"account_scope_id,omitempty"`
	Path             string                     `json:"path"`
	Name             string                     `json:"name"`
	ThemeID          string                     `json:"theme_id,omitempty"`
	Directories      []string                   `json:"directories,omitempty"`
	ReplicationLinks []WorkspaceReplicationLink `json:"replication_links,omitempty"`
	SortIndex        int                        `json:"sort_index,omitempty"`
	AddedAt          int64                      `json:"added_at"`
	UpdatedAt        int64                      `json:"updated_at"`
	LastSelectedAt   int64                      `json:"last_selected_at"`
}

type WorkspaceStore struct {
	store *Store
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
	binding := WorkspaceBinding{Path: entry.Path, Name: entry.Name, ResolvedAt: time.Now().UnixMilli()}
	if err := s.store.PutJSON(KeyWorkspaceCurrentForAccount(accountScopeID, userID), binding); err != nil {
		return WorkspaceBinding{}, err
	}
	return binding, nil
}

func (s *WorkspaceStore) AddForAccount(accountScopeID, path, name string) (WorkspaceEntry, error) {
	return s.upsertForAccount(accountScopeID, path, name, "", false)
}

func (s *WorkspaceStore) SaveForAccount(accountScopeID, path, name, themeID string, selected bool) (WorkspaceEntry, error) {
	return s.upsertForAccount(accountScopeID, path, name, themeID, selected)
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
	entry.AccountScopeID = accountScopeID
	entry.Name = name
	entry.ThemeID = normalizeWorkspaceThemeID(entry.ThemeID)
	entry.Directories = normalizeWorkspaceDirectories(entry.Path, entry.Directories)
	entry.ReplicationLinks = normalizeWorkspaceReplicationLinks(entry.ReplicationLinks)
	entry.UpdatedAt = now
	if err := s.store.PutJSON(key, entry); err != nil {
		return WorkspaceEntry{}, err
	}
	if userID != "" {
		current, hasCurrent, err := s.GetCurrentForAccount(accountScopeID, userID)
		if err != nil {
			return WorkspaceEntry{}, err
		}
		if hasCurrent && current.Path == path {
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
	entry.AccountScopeID = accountScopeID
	entry.ThemeID = normalizeWorkspaceThemeID(themeID)
	entry.Directories = normalizeWorkspaceDirectories(entry.Path, entry.Directories)
	entry.ReplicationLinks = normalizeWorkspaceReplicationLinks(entry.ReplicationLinks)
	entry.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.PutJSON(key, entry); err != nil {
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
	if err := s.store.Delete(KeyWorkspaceEntryForAccount(accountScopeID, path)); err != nil {
		return err
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
	entry.AccountScopeID = accountScopeID
	entry.ThemeID = normalizeWorkspaceThemeID(entry.ThemeID)
	entry.Directories = normalizeWorkspaceDirectories(entry.Path, entry.Directories)
	entry.ReplicationLinks = normalizeWorkspaceReplicationLinks(entry.ReplicationLinks)
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
		entries[i].AccountScopeID = accountScopeID
		entries[i].SortIndex = i
		if entries[i].Path == path {
			entries[i].UpdatedAt = now
		}
		if err := s.store.PutJSON(KeyWorkspaceEntryForAccount(accountScopeID, entries[i].Path), entries[i]); err != nil {
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
	owner, ownerOK, err := s.findLinkedDirectoryOwnerForAccount(accountScopeID, directory)
	if err != nil {
		return WorkspaceEntry{}, err
	}
	if ownerOK && owner.Path != entry.Path {
		return WorkspaceEntry{}, fmt.Errorf("directory %q already belongs to workspace %q", directory, owner.Path)
	}
	entry.AccountScopeID = accountScopeID
	entry.Directories = append(entry.Directories, directory)
	entry.Directories = normalizeWorkspaceDirectories(entry.Path, entry.Directories)
	entry.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.PutJSON(KeyWorkspaceEntryForAccount(accountScopeID, entry.Path), entry); err != nil {
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
	entry.AccountScopeID = accountScopeID
	entry.Directories = normalizeWorkspaceDirectories(entry.Path, updated)
	entry.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.PutJSON(KeyWorkspaceEntryForAccount(accountScopeID, entry.Path), entry); err != nil {
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
	entry.AccountScopeID = ""
	entry.ThemeID = normalizeWorkspaceThemeID(entry.ThemeID)
	entry.Directories = normalizeWorkspaceDirectories(entry.Path, entry.Directories)
	entry.ReplicationLinks = normalizeWorkspaceReplicationLinks(entry.ReplicationLinks)
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
		entry.AccountScopeID = ""
		entry.ThemeID = normalizeWorkspaceThemeID(entry.ThemeID)
		entry.Directories = normalizeWorkspaceDirectories(entry.Path, entry.Directories)
		entry.ReplicationLinks = normalizeWorkspaceReplicationLinks(entry.ReplicationLinks)
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

func (s *WorkspaceStore) AddReplicationLink(path string, link WorkspaceReplicationLink) (WorkspaceEntry, WorkspaceReplicationLink, error) {
	return WorkspaceEntry{}, WorkspaceReplicationLink{}, fmt.Errorf("legacy workspace replication links are disabled; write topology workspace bindings instead")
}

func (s *WorkspaceStore) PurgeAllReplicationLinks() (int, error) {
	if s == nil || s.store == nil {
		return 0, fmt.Errorf("workspace store is not configured")
	}
	removed := 0
	accounts, err := s.accountScopeIDsWithWorkspaceEntries()
	if err != nil {
		return 0, err
	}
	now := time.Now().UnixMilli()
	for _, accountScopeID := range accounts {
		entries, err := s.listAllForAccount(accountScopeID)
		if err != nil {
			return removed, err
		}
		for _, entry := range entries {
			if len(entry.ReplicationLinks) == 0 {
				continue
			}
			removed += len(entry.ReplicationLinks)
			entry.AccountScopeID = accountScopeID
			entry.ReplicationLinks = nil
			entry.UpdatedAt = now
			if err := s.store.PutJSON(KeyWorkspaceEntryForAccount(accountScopeID, entry.Path), entry); err != nil {
				return removed, err
			}
		}
	}
	return removed, nil
}

func (s *WorkspaceStore) RemoveReplicationLink(path, linkID string) (WorkspaceEntry, error) {
	return WorkspaceEntry{}, fmt.Errorf("legacy global workspace replication links are disabled; account scope is required")
}

func (s *WorkspaceStore) RemoveReplicationLinksByTargetSwarmIDForAccount(accountScopeID, targetSwarmID string) (int, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return 0, fmt.Errorf("account scope is required")
	}
	targetSwarmID = strings.TrimSpace(targetSwarmID)
	if targetSwarmID == "" {
		return 0, nil
	}
	entries, err := s.listAllForAccount(accountScopeID)
	if err != nil {
		return 0, err
	}
	removed := 0
	now := time.Now().UnixMilli()
	for _, entry := range entries {
		links := normalizeWorkspaceReplicationLinks(entry.ReplicationLinks)
		if len(links) == 0 {
			continue
		}
		kept := make([]WorkspaceReplicationLink, 0, len(links))
		for _, link := range links {
			if strings.TrimSpace(link.TargetSwarmID) == targetSwarmID {
				removed++
				continue
			}
			kept = append(kept, link)
		}
		if len(kept) == len(links) {
			continue
		}
		entry.AccountScopeID = accountScopeID
		entry.ReplicationLinks = normalizeWorkspaceReplicationLinks(kept)
		entry.UpdatedAt = now
		if err := s.store.PutJSON(KeyWorkspaceEntryForAccount(accountScopeID, entry.Path), entry); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func (s *WorkspaceStore) ListReplicationLinks(path string) ([]WorkspaceReplicationLink, error) {
	return nil, fmt.Errorf("legacy global workspace replication links are disabled; account scope is required")
}

func (s *WorkspaceStore) AddDirectory(path, directory string) (WorkspaceEntry, error) {
	return WorkspaceEntry{}, fmt.Errorf("legacy global workspace directory add is disabled; account scope is required")
}

func (s *WorkspaceStore) RemoveDirectory(path, directory string) (WorkspaceEntry, error) {
	return WorkspaceEntry{}, fmt.Errorf("legacy global workspace directory remove is disabled; account scope is required")
}

func (s *WorkspaceStore) upsertForAccount(accountScopeID, path, name, themeID string, selected bool) (WorkspaceEntry, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return WorkspaceEntry{}, fmt.Errorf("account scope is required")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return WorkspaceEntry{}, fmt.Errorf("workspace path is required")
	}
	name = strings.TrimSpace(name)
	themeProvided := strings.TrimSpace(themeID) != ""
	themeID = normalizeWorkspaceThemeID(themeID)
	now := time.Now().UnixMilli()
	entries, err := s.listAllForAccount(accountScopeID)
	if err != nil {
		return WorkspaceEntry{}, err
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
		entry.AddedAt = existing.AddedAt
		if strings.TrimSpace(entry.Name) == "" {
			entry.Name = existing.Name
		}
		if !themeProvided {
			entry.ThemeID = normalizeWorkspaceThemeID(existing.ThemeID)
		}
		entry.Directories = normalizeWorkspaceDirectories(path, existing.Directories)
		entry.ReplicationLinks = normalizeWorkspaceReplicationLinks(existing.ReplicationLinks)
		entry.LastSelectedAt = existing.LastSelectedAt
		entry.SortIndex = existing.SortIndex
	} else {
		entry.AddedAt = now
		entry.SortIndex = len(entries)
	}
	if selected {
		entry.LastSelectedAt = now
	}
	entry.UpdatedAt = now
	if err := s.store.PutJSON(KeyWorkspaceEntryForAccount(accountScopeID, path), entry); err != nil {
		return WorkspaceEntry{}, err
	}
	return entry, nil
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
		entry.AccountScopeID = accountScopeID
		entry.ThemeID = normalizeWorkspaceThemeID(entry.ThemeID)
		entry.Directories = normalizeWorkspaceDirectories(entry.Path, entry.Directories)
		entry.ReplicationLinks = normalizeWorkspaceReplicationLinks(entry.ReplicationLinks)
		out = append(out, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortWorkspaceEntries(out)
	return out, nil
}

func (s *WorkspaceStore) findLinkedDirectoryOwnerForAccount(accountScopeID, directory string) (WorkspaceEntry, bool, error) {
	entries, err := s.listAllForAccount(accountScopeID)
	if err != nil {
		return WorkspaceEntry{}, false, err
	}
	for _, entry := range entries {
		for _, existing := range entry.Directories {
			if existing != directory {
				continue
			}
			if existing == strings.TrimSpace(entry.Path) {
				continue
			}
			return entry, true, nil
		}
	}
	return WorkspaceEntry{}, false, nil
}

func (s *WorkspaceStore) accountScopeIDsWithWorkspaceEntries() ([]string, error) {
	seen := make(map[string]struct{})
	err := s.store.IteratePrefix(KeyWorkspaceEntryAccountPrefix, 100000, func(key string, _ []byte) error {
		part := strings.TrimPrefix(key, KeyWorkspaceEntryAccountPrefix)
		if idx := strings.Index(part, "/"); idx >= 0 {
			part = part[:idx]
		}
		if part != "" {
			seen[part] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for accountScopeID := range seen {
		out = append(out, accountScopeID)
	}
	sort.Strings(out)
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

func normalizeWorkspaceReplicationLinks(links []WorkspaceReplicationLink) []WorkspaceReplicationLink {
	if len(links) == 0 {
		return nil
	}
	out := make([]WorkspaceReplicationLink, 0, len(links))
	seen := make(map[string]struct{}, len(links))
	for _, raw := range links {
		link := normalizeWorkspaceReplicationLink(raw)
		if link.ID == "" {
			continue
		}
		if _, ok := seen[link.ID]; ok {
			continue
		}
		seen[link.ID] = struct{}{}
		out = append(out, link)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeWorkspaceReplicationLink(link WorkspaceReplicationLink) WorkspaceReplicationLink {
	link.ID = strings.TrimSpace(link.ID)
	link.TargetKind = strings.TrimSpace(strings.ToLower(link.TargetKind))
	link.TargetSwarmID = strings.TrimSpace(link.TargetSwarmID)
	link.TargetSwarmName = strings.TrimSpace(link.TargetSwarmName)
	link.TargetWorkspacePath = strings.TrimSpace(link.TargetWorkspacePath)
	link.ReplicationMode = strings.TrimSpace(strings.ToLower(link.ReplicationMode))
	link.Sync = normalizeWorkspaceReplicationSync(link.Sync)
	return link
}

func normalizeWorkspaceReplicationSync(sync WorkspaceReplicationSync) WorkspaceReplicationSync {
	sync.Mode = strings.TrimSpace(strings.ToLower(sync.Mode))
	sync.Modules = normalizeWorkspaceReplicationSyncModules(sync.Modules)
	return sync
}

func normalizeWorkspaceReplicationSyncModules(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func findWorkspaceReplicationLinkByID(links []WorkspaceReplicationLink, linkID string) WorkspaceReplicationLink {
	linkID = strings.TrimSpace(linkID)
	for _, link := range links {
		if link.ID == linkID {
			return link
		}
	}
	return WorkspaceReplicationLink{}
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
