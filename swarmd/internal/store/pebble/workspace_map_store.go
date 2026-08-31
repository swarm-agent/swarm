package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cockroachdb/pebble"
)

const WorkspaceMapSchemaVersion = 1

var ErrWorkspaceMapRevisionConflict = errors.New("workspace map revision conflict")

// WorkspaceMap is the account-wide, high-level orientation document. Detailed
// repository rules remain in each workspace's AGENTS.md.
type WorkspaceMap struct {
	SchemaVersion int    `json:"schema_version"`
	Revision      int64  `json:"revision"`
	Content       string `json:"content"`
	Digest        string `json:"digest"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

type WorkspaceMapStore struct {
	store *Store
	now   func() time.Time
}

func NewWorkspaceMapStore(store *Store) *WorkspaceMapStore {
	return &WorkspaceMapStore{store: store, now: time.Now}
}

func (s *WorkspaceMapStore) GetForAccount(accountScopeID string) (WorkspaceMap, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return WorkspaceMap{}, false, fmt.Errorf("account scope is required")
	}
	if s == nil || s.store == nil {
		return WorkspaceMap{}, false, fmt.Errorf("workspace map store is not configured")
	}
	var record WorkspaceMap
	ok, err := s.store.GetJSON(KeyWorkspaceMapForAccount(accountScopeID), &record)
	if err != nil {
		return WorkspaceMap{}, false, err
	}
	if !ok {
		return WorkspaceMap{}, false, nil
	}
	if err := validateStoredWorkspaceMap(record); err != nil {
		return WorkspaceMap{}, false, err
	}
	return record, true, nil
}

// CreateDefaultForAccount atomically creates record when absent, otherwise it
// returns the current record without advancing its revision.
func (s *WorkspaceMapStore) CreateDefaultForAccount(accountScopeID, content string) (WorkspaceMap, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return WorkspaceMap{}, false, fmt.Errorf("account scope is required")
	}
	if s == nil || s.store == nil {
		return WorkspaceMap{}, false, fmt.Errorf("workspace map store is not configured")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return WorkspaceMap{}, false, fmt.Errorf("workspace map content is required")
	}
	content += "\n"

	workspaceMapMutationMu.Lock()
	defer workspaceMapMutationMu.Unlock()
	if current, ok, err := s.GetForAccount(accountScopeID); err != nil {
		return WorkspaceMap{}, false, err
	} else if ok {
		return current, false, nil
	}
	now := s.now().UTC().UnixMilli()
	record := WorkspaceMap{SchemaVersion: WorkspaceMapSchemaVersion, Revision: 1, Content: content, Digest: workspaceMapDigest(content), CreatedAt: now, UpdatedAt: now}
	if err := s.put(record, accountScopeID); err != nil {
		return WorkspaceMap{}, false, err
	}
	return record, true, nil
}

// UpdateForAccount applies an optimistic-concurrency update. Validation and
// serialization complete before the one atomic Pebble mutation.
func (s *WorkspaceMapStore) UpdateForAccount(accountScopeID string, expectedRevision int64, content string) (WorkspaceMap, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return WorkspaceMap{}, fmt.Errorf("account scope is required")
	}
	if expectedRevision <= 0 {
		return WorkspaceMap{}, fmt.Errorf("expected revision is required")
	}
	if s == nil || s.store == nil {
		return WorkspaceMap{}, fmt.Errorf("workspace map store is not configured")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return WorkspaceMap{}, fmt.Errorf("workspace map content is required")
	}
	content += "\n"

	workspaceMapMutationMu.Lock()
	defer workspaceMapMutationMu.Unlock()
	current, ok, err := s.GetForAccount(accountScopeID)
	if err != nil {
		return WorkspaceMap{}, err
	}
	if !ok {
		return WorkspaceMap{}, fmt.Errorf("workspace map does not exist")
	}
	if current.Revision != expectedRevision {
		return WorkspaceMap{}, fmt.Errorf("%w: expected %d, current %d", ErrWorkspaceMapRevisionConflict, expectedRevision, current.Revision)
	}
	updated := current
	updated.Revision++
	updated.Content = content
	updated.Digest = workspaceMapDigest(content)
	updated.UpdatedAt = s.now().UTC().UnixMilli()
	if err := s.put(updated, accountScopeID); err != nil {
		return WorkspaceMap{}, err
	}
	return updated, nil
}

func (s *WorkspaceMapStore) put(record WorkspaceMap, accountScopeID string) error {
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal workspace map: %w", err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeyWorkspaceMapForAccount(accountScopeID)), payload, nil); err != nil {
		return fmt.Errorf("stage workspace map: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("commit workspace map: %w", err)
	}
	return nil
}

func validateStoredWorkspaceMap(record WorkspaceMap) error {
	if len(record.Content) > WorkspaceMapMaxBytes {
		return fmt.Errorf("workspace map exceeds %d bytes", WorkspaceMapMaxBytes)
	}
	if record.SchemaVersion != WorkspaceMapSchemaVersion {
		return fmt.Errorf("unsupported workspace map schema version %d", record.SchemaVersion)
	}
	if record.Revision <= 0 || record.CreatedAt <= 0 || record.UpdatedAt <= 0 {
		return fmt.Errorf("workspace map metadata is invalid")
	}
	if record.Content == "" || record.Digest != workspaceMapDigest(record.Content) {
		return fmt.Errorf("workspace map digest is invalid")
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

// WorkspaceMapService owns content policy while WorkspaceMapStore owns durable
// account-scoped persistence and optimistic concurrency.
type WorkspaceMapService struct {
	store *WorkspaceMapStore
}

func NewWorkspaceMapService(store *WorkspaceMapStore) *WorkspaceMapService {
	return &WorkspaceMapService{store: store}
}

// GetOrCreateDefault never invokes a provider. A missing map is populated
// synchronously with the deterministic skeletal document above.
func (s *WorkspaceMapService) GetOrCreateDefault(accountScopeID string) (WorkspaceMap, error) {
	if s == nil || s.store == nil {
		return WorkspaceMap{}, fmt.Errorf("workspace map service is not configured")
	}
	content, err := NormalizeWorkspaceMapContent(DefaultWorkspaceMap)
	if err != nil {
		return WorkspaceMap{}, err
	}
	record, _, err := s.store.CreateDefaultForAccount(accountScopeID, content)
	return record, err
}

func (s *WorkspaceMapService) Update(accountScopeID string, expectedRevision int64, content string) (WorkspaceMap, error) {
	if s == nil || s.store == nil {
		return WorkspaceMap{}, fmt.Errorf("workspace map service is not configured")
	}
	normalized, err := NormalizeWorkspaceMapContent(content)
	if err != nil {
		return WorkspaceMap{}, err
	}
	return s.store.UpdateForAccount(accountScopeID, expectedRevision, normalized)
}

func NormalizeWorkspaceMapContent(content string) (string, error) {
	if !utf8.ValidString(content) {
		return "", fmt.Errorf("workspace map must be valid UTF-8")
	}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	content = strings.TrimSpace(content)
	if content == "" {
		return "", fmt.Errorf("workspace map content is required")
	}
	content += "\n"
	if len(content) > WorkspaceMapMaxBytes {
		return "", fmt.Errorf("workspace map exceeds %d bytes", WorkspaceMapMaxBytes)
	}
	if strings.IndexByte(content, 0) >= 0 {
		return "", fmt.Errorf("workspace map contains a NUL byte")
	}
	if !strings.HasPrefix(content, "# Workspace Map\n") {
		return "", fmt.Errorf("workspace map must start with %q", "# Workspace Map")
	}
	return content, nil
}
