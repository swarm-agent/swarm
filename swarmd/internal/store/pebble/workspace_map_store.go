package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const WorkspaceMapSchemaVersion = 1

var ErrWorkspaceMapRevisionConflict = errors.New("workspace map revision conflict")

type WorkspaceMap struct {
	SchemaVersion int    `json:"schema_version"`
	Revision      int64  `json:"revision"`
	Content       string `json:"content"`
	Digest        string `json:"digest"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

// WorkspaceMapStore is a compatibility view, not a second persistence authority.
// All reads and writes use MemoryStore and the same account mutation lock.
type WorkspaceMapStore struct {
	store *Store
	now   func() time.Time
}

func NewWorkspaceMapStore(store *Store) *WorkspaceMapStore {
	return &WorkspaceMapStore{store: store, now: time.Now}
}
func mapFromMemory(e MemoryEntry) WorkspaceMap {
	return WorkspaceMap{SchemaVersion: 1, Revision: e.Revision, Content: e.Content, Digest: workspaceMapDigest(e.Content), CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt}
}
func (s *WorkspaceMapStore) memory() *MemoryStore { return &MemoryStore{store: s.store, now: s.now} }
func (s *WorkspaceMapStore) GetForAccount(account string) (WorkspaceMap, bool, error) {
	if s == nil || s.store == nil {
		return WorkspaceMap{}, false, errors.New("workspace map store is not configured")
	}
	d, err := s.memory().GetForAccount(account)
	if err != nil {
		return WorkspaceMap{}, false, err
	}
	i := memoryEntryIndex(d, MemoryWorkspaceMapID)
	if i < 0 {
		return WorkspaceMap{}, false, nil
	}
	return mapFromMemory(d.Entries[i]), true, nil
}
func (s *WorkspaceMapStore) CreateDefaultForAccount(account, content string) (WorkspaceMap, bool, error) {
	if s == nil || s.store == nil {
		return WorkspaceMap{}, false, errors.New("workspace map store is not configured")
	}
	content, err := NormalizeWorkspaceMapContent(content)
	if err != nil {
		return WorkspaceMap{}, false, err
	}
	// A concurrent creator may win. Re-read only on CAS conflict; never overwrite it.
	for attempt := 0; attempt < 3; attempt++ {
		d, err := s.memory().GetForAccount(account)
		if err != nil {
			return WorkspaceMap{}, false, err
		}
		if i := memoryEntryIndex(d, MemoryWorkspaceMapID); i >= 0 {
			return mapFromMemory(d.Entries[i]), false, nil
		}
		if containsMemory(d.Forgotten, memoryMarker("entry", MemoryWorkspaceMapID)) {
			return WorkspaceMap{}, false, ErrMemoryPolicy
		}
		d, err = s.memory().MutateForAccount(account, MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "user", ID: "workspace-map-compatibility"}, Reason: "Create default workspace orientation", Operation: "put", Entry: MemoryEntry{ID: MemoryWorkspaceMapID, Kind: "orientation", Content: content}})
		if errors.Is(err, ErrMemoryConflict) {
			continue
		}
		if err != nil {
			return WorkspaceMap{}, false, err
		}
		return mapFromMemory(d.Entries[memoryEntryIndex(d, MemoryWorkspaceMapID)]), true, nil
	}
	return WorkspaceMap{}, false, ErrWorkspaceMapRevisionConflict
}
func (s *WorkspaceMapStore) UpdateForAccount(account string, expected int64, content string) (WorkspaceMap, error) {
	if s == nil || s.store == nil {
		return WorkspaceMap{}, errors.New("workspace map store is not configured")
	}
	if expected <= 0 {
		return WorkspaceMap{}, errors.New("expected revision is required")
	}
	content, err := NormalizeWorkspaceMapContent(content)
	if err != nil {
		return WorkspaceMap{}, err
	}
	d, err := s.memory().GetForAccount(account)
	if err != nil {
		return WorkspaceMap{}, err
	}
	i := memoryEntryIndex(d, MemoryWorkspaceMapID)
	if i < 0 {
		return WorkspaceMap{}, errors.New("workspace map does not exist")
	}
	if d.Entries[i].Revision != expected {
		return WorkspaceMap{}, ErrWorkspaceMapRevisionConflict
	}
	e := d.Entries[i]
	e.Content = content
	d, err = s.memory().MutateForAccount(account, MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "user", ID: "workspace-map-compatibility"}, Reason: "Explicit Workspace Map update", Operation: "put", Entry: e})
	if errors.Is(err, ErrMemoryConflict) {
		return WorkspaceMap{}, ErrWorkspaceMapRevisionConflict
	}
	if err != nil {
		return WorkspaceMap{}, err
	}
	return mapFromMemory(d.Entries[memoryEntryIndex(d, MemoryWorkspaceMapID)]), nil
}
func validateStoredWorkspaceMap(record WorkspaceMap) error {
	if len(record.Content) > WorkspaceMapMaxBytes {
		return fmt.Errorf("workspace map exceeds %d bytes", WorkspaceMapMaxBytes)
	}
	if record.SchemaVersion != 1 || record.Revision <= 0 || record.CreatedAt <= 0 || record.UpdatedAt <= 0 || record.Content == "" || record.Digest != workspaceMapDigest(record.Content) {
		return errors.New("workspace map metadata or digest is invalid")
	}
	return nil
}
func workspaceMapDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

const (
	WorkspaceMapMaxBytes = 32 * 1024
	DefaultWorkspaceMap  = "# Workspace Map\n\n## Orientation\n\nDescribe the account's workspaces, their purposes, and the keywords used to route work.\n\n## Workspaces\n\n- Add high-level workspace entries here. Keep detailed repository rules in each workspace's AGENTS.md.\n"
)

type WorkspaceMapService struct{ store *WorkspaceMapStore }

func NewWorkspaceMapService(store *WorkspaceMapStore) *WorkspaceMapService {
	return &WorkspaceMapService{store: store}
}
func (s *WorkspaceMapService) GetOrCreateDefault(account string) (WorkspaceMap, error) {
	if s == nil || s.store == nil {
		return WorkspaceMap{}, errors.New("workspace map service is not configured")
	}
	r, _, err := s.store.CreateDefaultForAccount(account, DefaultWorkspaceMap)
	return r, err
}
func (s *WorkspaceMapService) Update(account string, expected int64, content string) (WorkspaceMap, error) {
	if s == nil || s.store == nil {
		return WorkspaceMap{}, errors.New("workspace map service is not configured")
	}
	return s.store.UpdateForAccount(account, expected, content)
}
func NormalizeWorkspaceMapContent(content string) (string, error) {
	if !utf8.ValidString(content) {
		return "", errors.New("workspace map must be valid UTF-8")
	}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	content = strings.TrimSpace(content)
	if content == "" {
		return "", errors.New("workspace map content is required")
	}
	content += "\n"
	if len(content) > WorkspaceMapMaxBytes {
		return "", fmt.Errorf("workspace map exceeds %d bytes", WorkspaceMapMaxBytes)
	}
	if strings.IndexByte(content, 0) >= 0 {
		return "", errors.New("workspace map contains a NUL byte")
	}
	if !strings.HasPrefix(content, "# Workspace Map\n") {
		return "", errors.New("workspace map must start with # Workspace Map")
	}
	return content, nil
}
