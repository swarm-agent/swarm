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

func (s *WorkspaceStore) SetCurrent(path, name string) (WorkspaceBinding, error) {
	entry, err := s.upsert(path, name, "", true)
	if err != nil {
		return WorkspaceBinding{}, err
	}
	binding := WorkspaceBinding{
		Path:       entry.Path,
		Name:       entry.Name,
		ResolvedAt: time.Now().UnixMilli(),
	}
	if err := s.store.PutJSON(KeyWorkspaceCurrent, binding); err != nil {
		return WorkspaceBinding{}, err
	}
	return binding, nil
}

func (s *WorkspaceStore) Add(path, name string) (WorkspaceEntry, error) {
	return s.upsert(path, name, "", false)
}

func (s *WorkspaceStore) Save(path, name, themeID string, selected bool) (WorkspaceEntry, error) {
	return s.upsert(path, name, themeID, selected)
}

func (s *WorkspaceStore) Rename(path, name string) (WorkspaceEntry, error) {
	path = strings.TrimSpace(path)
	name = strings.TrimSpace(name)
	if path == "" {
		return WorkspaceEntry{}, fmt.Errorf("workspace path is required")
	}
	if name == "" {
		return WorkspaceEntry{}, fmt.Errorf("workspace name is required")
	}

	key := KeyWorkspaceEntry(path)
	var entry WorkspaceEntry
	ok, err := s.store.GetJSON(key, &entry)
	if err != nil {
		return WorkspaceEntry{}, err
	}
	if !ok {
		return WorkspaceEntry{}, fmt.Errorf("workspace %q not found", path)
	}

	now := time.Now().UnixMilli()
	entry.Name = name
	entry.ThemeID = normalizeWorkspaceThemeID(entry.ThemeID)
	entry.Directories = normalizeWorkspaceDirectories(entry.Path, entry.Directories)
	entry.ReplicationLinks = normalizeWorkspaceReplicationLinks(entry.ReplicationLinks)
	entry.UpdatedAt = now
	if err := s.store.PutJSON(key, entry); err != nil {
		return WorkspaceEntry{}, err
	}

	current, hasCurrent, err := s.GetCurrent()
	if err != nil {
		return WorkspaceEntry{}, err
	}
	if hasCurrent && current.Path == path {
		current.Name = name
		current.ResolvedAt = now
		if err := s.store.PutJSON(KeyWorkspaceCurrent, current); err != nil {
			return WorkspaceEntry{}, err
		}
	}
	return entry, nil
}

func (s *WorkspaceStore) SetThemeID(path, themeID string) (WorkspaceEntry, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return WorkspaceEntry{}, fmt.Errorf("workspace path is required")
	}
	key := KeyWorkspaceEntry(path)
	var entry WorkspaceEntry
	ok, err := s.store.GetJSON(key, &entry)
	if err != nil {
		return WorkspaceEntry{}, err
	}
	if !ok {
		return WorkspaceEntry{}, fmt.Errorf("workspace %q not found", path)
	}
	entry.ThemeID = normalizeWorkspaceThemeID(themeID)
	entry.Directories = normalizeWorkspaceDirectories(entry.Path, entry.Directories)
	entry.ReplicationLinks = normalizeWorkspaceReplicationLinks(entry.ReplicationLinks)
	entry.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.PutJSON(key, entry); err != nil {
		return WorkspaceEntry{}, err
	}
	return entry, nil
}

func (s *WorkspaceStore) Delete(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("workspace path is required")
	}

	if err := s.store.Delete(KeyWorkspaceEntry(path)); err != nil {
		return err
	}
	current, hasCurrent, err := s.GetCurrent()
	if err != nil {
		return err
	}
	if hasCurrent && current.Path == path {
		if err := s.store.Delete(KeyWorkspaceCurrent); err != nil {
			return err
		}
	}
	return nil
}

func (s *WorkspaceStore) Get(path string) (WorkspaceEntry, bool, error) {
	key := KeyWorkspaceEntry(strings.TrimSpace(path))
	var entry WorkspaceEntry
	ok, err := s.store.GetJSON(key, &entry)
	if err != nil {
		return WorkspaceEntry{}, false, err
	}
	if !ok {
		return WorkspaceEntry{}, false, nil
	}
	entry.ThemeID = normalizeWorkspaceThemeID(entry.ThemeID)
	entry.Directories = normalizeWorkspaceDirectories(entry.Path, entry.Directories)
	entry.ReplicationLinks = normalizeWorkspaceReplicationLinks(entry.ReplicationLinks)
	return entry, true, nil
}

func (s *WorkspaceStore) List(limit int) ([]WorkspaceEntry, error) {
	if limit <= 0 {
		limit = 200
	}
	out, err := s.listAll()
	if err != nil {
		return nil, err
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *WorkspaceStore) Move(path string, delta int) (WorkspaceEntry, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return WorkspaceEntry{}, fmt.Errorf("workspace path is required")
	}
	if delta == 0 {
		entry, ok, err := s.Get(path)
		if err != nil {
			return WorkspaceEntry{}, err
		}
		if !ok {
			return WorkspaceEntry{}, fmt.Errorf("workspace %q not found", path)
		}
		return entry, nil
	}

	entries, err := s.listAll()
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
		entries[i].SortIndex = i
		if entries[i].Path == path {
			entries[i].UpdatedAt = now
		}
		if err := s.store.PutJSON(KeyWorkspaceEntry(entries[i].Path), entries[i]); err != nil {
			return WorkspaceEntry{}, err
		}
	}
	return entries[target], nil
}

func (s *WorkspaceStore) AddReplicationLink(path string, link WorkspaceReplicationLink) (WorkspaceEntry, WorkspaceReplicationLink, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return WorkspaceEntry{}, WorkspaceReplicationLink{}, fmt.Errorf("workspace path is required")
	}

	entry, ok, err := s.Get(path)
	if err != nil {
		return WorkspaceEntry{}, WorkspaceReplicationLink{}, err
	}
	if !ok {
		return WorkspaceEntry{}, WorkspaceReplicationLink{}, fmt.Errorf("workspace %q not found", path)
	}

	now := time.Now().UnixMilli()
	link = normalizeWorkspaceReplicationLink(link)
	if link.ID == "" {
		link.ID = fmt.Sprintf("replication_%d", now)
	}
	if link.CreatedAt <= 0 {
		link.CreatedAt = now
	}
	link.UpdatedAt = now

	updated := make([]WorkspaceReplicationLink, 0, len(entry.ReplicationLinks)+1)
	replaced := false
	for _, existing := range entry.ReplicationLinks {
		if existing.ID == link.ID {
			if link.CreatedAt <= 0 {
				link.CreatedAt = existing.CreatedAt
			}
			updated = append(updated, link)
			replaced = true
			continue
		}
		updated = append(updated, existing)
	}
	if !replaced {
		updated = append(updated, link)
	}
	entry.ReplicationLinks = normalizeWorkspaceReplicationLinks(updated)
	entry.UpdatedAt = now
	if err := s.store.PutJSON(KeyWorkspaceEntry(entry.Path), entry); err != nil {
		return WorkspaceEntry{}, WorkspaceReplicationLink{}, err
	}
	stored := findWorkspaceReplicationLinkByID(entry.ReplicationLinks, link.ID)
	return entry, stored, nil
}

func (s *WorkspaceStore) RemoveReplicationLink(path, linkID string) (WorkspaceEntry, error) {
	path = strings.TrimSpace(path)
	linkID = strings.TrimSpace(linkID)
	if path == "" {
		return WorkspaceEntry{}, fmt.Errorf("workspace path is required")
	}
	if linkID == "" {
		return WorkspaceEntry{}, fmt.Errorf("replication link id is required")
	}

	entry, ok, err := s.Get(path)
	if err != nil {
		return WorkspaceEntry{}, err
	}
	if !ok {
		return WorkspaceEntry{}, fmt.Errorf("workspace %q not found", path)
	}

	updated := make([]WorkspaceReplicationLink, 0, len(entry.ReplicationLinks))
	removed := false
	for _, existing := range entry.ReplicationLinks {
		if existing.ID == linkID {
			removed = true
			continue
		}
		updated = append(updated, existing)
	}
	if !removed {
		return WorkspaceEntry{}, fmt.Errorf("replication link %q is not linked to workspace %q", linkID, path)
	}

	entry.ReplicationLinks = normalizeWorkspaceReplicationLinks(updated)
	entry.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.PutJSON(KeyWorkspaceEntry(entry.Path), entry); err != nil {
		return WorkspaceEntry{}, err
	}
	return entry, nil
}

func (s *WorkspaceStore) ListReplicationLinks(path string) ([]WorkspaceReplicationLink, error) {
	entry, ok, err := s.Get(path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("workspace %q not found", strings.TrimSpace(path))
	}
	return append([]WorkspaceReplicationLink(nil), entry.ReplicationLinks...), nil
}

func (s *WorkspaceStore) AddDirectory(path, directory string) (WorkspaceEntry, error) {
	path = strings.TrimSpace(path)
	directory = strings.TrimSpace(directory)
	if path == "" {
		return WorkspaceEntry{}, fmt.Errorf("workspace path is required")
	}
	if directory == "" {
		return WorkspaceEntry{}, fmt.Errorf("directory path is required")
	}

	entry, ok, err := s.Get(path)
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

	owner, ownerOK, err := s.findLinkedDirectoryOwner(directory)
	if err != nil {
		return WorkspaceEntry{}, err
	}
	if ownerOK && owner.Path != entry.Path {
		return WorkspaceEntry{}, fmt.Errorf("directory %q already belongs to workspace %q", directory, owner.Path)
	}

	entry.Directories = append(entry.Directories, directory)
	entry.Directories = normalizeWorkspaceDirectories(entry.Path, entry.Directories)
	entry.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.PutJSON(KeyWorkspaceEntry(entry.Path), entry); err != nil {
		return WorkspaceEntry{}, err
	}
	return entry, nil
}

func (s *WorkspaceStore) RemoveDirectory(path, directory string) (WorkspaceEntry, error) {
	path = strings.TrimSpace(path)
	directory = strings.TrimSpace(directory)
	if path == "" {
		return WorkspaceEntry{}, fmt.Errorf("workspace path is required")
	}
	if directory == "" {
		return WorkspaceEntry{}, fmt.Errorf("directory path is required")
	}

	entry, ok, err := s.Get(path)
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

	entry.Directories = normalizeWorkspaceDirectories(entry.Path, updated)
	entry.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.PutJSON(KeyWorkspaceEntry(entry.Path), entry); err != nil {
		return WorkspaceEntry{}, err
	}
	return entry, nil
}

func (s *WorkspaceStore) listAll() ([]WorkspaceEntry, error) {
	out := make([]WorkspaceEntry, 0, 200)
	err := s.store.IteratePrefix(WorkspaceEntryPrefix(), 100000, func(_ string, value []byte) error {
		var entry WorkspaceEntry
		if err := json.Unmarshal(value, &entry); err != nil {
			return err
		}
		if strings.TrimSpace(entry.Path) == "" {
			return nil
		}
		entry.ThemeID = normalizeWorkspaceThemeID(entry.ThemeID)
		entry.Directories = normalizeWorkspaceDirectories(entry.Path, entry.Directories)
		entry.ReplicationLinks = normalizeWorkspaceReplicationLinks(entry.ReplicationLinks)
		out = append(out, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
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
	return out, nil
}

func (s *WorkspaceStore) GetCurrent() (WorkspaceBinding, bool, error) {
	var binding WorkspaceBinding
	ok, err := s.store.GetJSON(KeyWorkspaceCurrent, &binding)
	if err != nil {
		return WorkspaceBinding{}, false, err
	}
	if !ok {
		return WorkspaceBinding{}, false, nil
	}
	return binding, true, nil
}

func (s *WorkspaceStore) upsert(path, name, themeID string, selected bool) (WorkspaceEntry, error) {
	path = strings.TrimSpace(path)
	name = strings.TrimSpace(name)
	themeProvided := strings.TrimSpace(themeID) != ""
	themeID = normalizeWorkspaceThemeID(themeID)
	now := time.Now().UnixMilli()
	key := KeyWorkspaceEntry(path)

	entries, err := s.listAll()
	if err != nil {
		return WorkspaceEntry{}, err
	}
	var (
		existing WorkspaceEntry
		ok       bool
	)
	for _, entry := range entries {
		if entry.Path == path {
			existing = entry
			ok = true
			break
		}
	}

	entry := WorkspaceEntry{
		Path:        path,
		Name:        name,
		ThemeID:     themeID,
		Directories: []string{path},
	}
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
	if err := s.store.PutJSON(key, entry); err != nil {
		return WorkspaceEntry{}, err
	}
	return entry, nil
}

func (s *WorkspaceStore) findLinkedDirectoryOwner(directory string) (WorkspaceEntry, bool, error) {
	entries, err := s.listAll()
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
