package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

type SwarmLocalContainerMount struct {
	SourcePath    string `json:"source_path"`
	TargetPath    string `json:"target_path,omitempty"`
	Mode          string `json:"mode,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
}

type SwarmLocalContainerRecord struct {
	ID             string                     `json:"id"`
	UserID         string                     `json:"user_id,omitempty"`
	AccountScopeID string                     `json:"account_scope_id,omitempty"`
	Name           string                     `json:"name"`
	ContainerName  string                     `json:"container_name,omitempty"`
	Runtime        string                     `json:"runtime,omitempty"`
	NetworkName    string                     `json:"network_name,omitempty"`
	Status         string                     `json:"status,omitempty"`
	ContainerID    string                     `json:"container_id,omitempty"`
	HostAPIBaseURL string                     `json:"host_api_base_url,omitempty"`
	HostPort       int                        `json:"host_port,omitempty"`
	RuntimePort    int                        `json:"runtime_port,omitempty"`
	Image          string                     `json:"image,omitempty"`
	Warning        string                     `json:"warning,omitempty"`
	Mounts         []SwarmLocalContainerMount `json:"mounts,omitempty"`
	CreatedAt      int64                      `json:"created_at"`
	UpdatedAt      int64                      `json:"updated_at"`
}

type SwarmLocalContainerStore struct {
	store *Store
}

func NewSwarmLocalContainerStore(store *Store) *SwarmLocalContainerStore {
	return &SwarmLocalContainerStore{store: store}
}

func (s *SwarmLocalContainerStore) Get(containerID string) (SwarmLocalContainerRecord, bool, error) {
	if s == nil || s.store == nil {
		return SwarmLocalContainerRecord{}, false, nil
	}
	containerID = normalizeSwarmLocalContainerID(containerID)
	if containerID == "" {
		return SwarmLocalContainerRecord{}, false, errors.New("local container id is required")
	}
	var record SwarmLocalContainerRecord
	ok, err := s.store.GetJSON(KeySwarmLocalContainer(containerID), &record)
	if err != nil {
		return SwarmLocalContainerRecord{}, false, err
	}
	if !ok {
		return SwarmLocalContainerRecord{}, false, nil
	}
	record = normalizeSwarmLocalContainerRecord(record)
	if record.ID == "" {
		record.ID = containerID
	}
	return record, true, nil
}

func (s *SwarmLocalContainerStore) GetForAccount(accountScopeID, containerID string) (SwarmLocalContainerRecord, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return SwarmLocalContainerRecord{}, false, errors.New("account scope id is required")
	}
	record, ok, err := s.Get(containerID)
	if err != nil || !ok {
		return record, ok, err
	}
	if record.AccountScopeID != accountScopeID {
		return SwarmLocalContainerRecord{}, false, nil
	}
	return record, true, nil
}

func (s *SwarmLocalContainerStore) Put(record SwarmLocalContainerRecord) (SwarmLocalContainerRecord, error) {
	if s == nil || s.store == nil {
		return SwarmLocalContainerRecord{}, errors.New("local container store is not configured")
	}
	record = normalizeSwarmLocalContainerRecord(record)
	if record.ID == "" {
		return SwarmLocalContainerRecord{}, errors.New("local container id is required")
	}
	if record.Name == "" {
		return SwarmLocalContainerRecord{}, errors.New("local container name is required")
	}
	now := time.Now().UnixMilli()
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	payload, err := json.Marshal(record)
	if err != nil {
		return SwarmLocalContainerRecord{}, fmt.Errorf("marshal local container %q: %w", record.ID, err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if existing, ok, err := s.Get(record.ID); err != nil {
		return SwarmLocalContainerRecord{}, err
	} else if ok && existing.AccountScopeID != "" && existing.AccountScopeID != record.AccountScopeID {
		if err := batch.Delete([]byte(KeySwarmLocalContainerByAccount(existing.AccountScopeID, record.ID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
			return SwarmLocalContainerRecord{}, err
		}
	}
	if err := batch.Set([]byte(KeySwarmLocalContainer(record.ID)), payload, nil); err != nil {
		return SwarmLocalContainerRecord{}, err
	}
	if record.AccountScopeID != "" {
		if err := batch.Set([]byte(KeySwarmLocalContainerByAccount(record.AccountScopeID, record.ID)), []byte(record.ID), nil); err != nil {
			return SwarmLocalContainerRecord{}, err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return SwarmLocalContainerRecord{}, err
	}
	return record, nil
}

func (s *SwarmLocalContainerStore) PutForAccount(record SwarmLocalContainerRecord, userID, accountScopeID string) (SwarmLocalContainerRecord, error) {
	record = normalizeSwarmLocalContainerRecord(record)
	record.UserID = strings.TrimSpace(userID)
	record.AccountScopeID = strings.TrimSpace(accountScopeID)
	if record.AccountScopeID == "" {
		return SwarmLocalContainerRecord{}, errors.New("account scope id is required")
	}
	if record.ID != "" {
		existing, ok, err := s.Get(record.ID)
		if err != nil {
			return SwarmLocalContainerRecord{}, err
		}
		if ok && existing.AccountScopeID != "" && existing.AccountScopeID != record.AccountScopeID {
			return SwarmLocalContainerRecord{}, errors.New("local container belongs to another account")
		}
	}
	return s.Put(record)
}

func (s *SwarmLocalContainerStore) Delete(containerID string) error {
	if s == nil || s.store == nil {
		return errors.New("local container store is not configured")
	}
	containerID = normalizeSwarmLocalContainerID(containerID)
	if containerID == "" {
		return errors.New("local container id is required")
	}
	return s.delete(containerID, "")
}

func (s *SwarmLocalContainerStore) DeleteForAccount(accountScopeID, containerID string) error {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return errors.New("account scope id is required")
	}
	containerID = normalizeSwarmLocalContainerID(containerID)
	if containerID == "" {
		return errors.New("local container id is required")
	}
	return s.delete(containerID, accountScopeID)
}

func (s *SwarmLocalContainerStore) delete(containerID, accountScopeID string) error {
	var existing SwarmLocalContainerRecord
	if loaded, ok, err := s.Get(containerID); err != nil {
		return err
	} else if ok {
		existing = loaded
	}
	if accountScopeID != "" {
		if existing.ID == "" || existing.AccountScopeID != accountScopeID {
			return nil
		}
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Delete([]byte(KeySwarmLocalContainer(containerID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
		return err
	}
	if existing.AccountScopeID != "" {
		if err := batch.Delete([]byte(KeySwarmLocalContainerByAccount(existing.AccountScopeID, containerID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
}

func (s *SwarmLocalContainerStore) List(limit int) ([]SwarmLocalContainerRecord, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]SwarmLocalContainerRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(SwarmLocalContainerPrefix(), limit, func(key string, value []byte) error {
		var record SwarmLocalContainerRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode local container: %w", err)
		}
		record = normalizeSwarmLocalContainerRecord(record)
		if record.ID == "" {
			record.ID = decodeSwarmLocalContainerIDFromKey(key)
		}
		if record.ID == "" || record.Name == "" {
			return nil
		}
		out = append(out, record)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return finalizeSwarmLocalContainerList(out, limit), nil
}

func (s *SwarmLocalContainerStore) ListForAccount(accountScopeID string, limit int) ([]SwarmLocalContainerRecord, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account scope id is required")
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]SwarmLocalContainerRecord, 0, min(limit, 16))
	const iterateAll = int(^uint(0) >> 1)
	err := s.store.IteratePrefix(SwarmLocalContainerByAccountPrefix(accountScopeID), iterateAll, func(_ string, value []byte) error {
		containerID := strings.TrimSpace(string(value))
		if containerID == "" {
			return nil
		}
		record, ok, err := s.Get(containerID)
		if err != nil {
			return err
		}
		if !ok || record.AccountScopeID != accountScopeID || record.ID == "" || record.Name == "" {
			return nil
		}
		out = append(out, record)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return finalizeSwarmLocalContainerList(out, limit), nil
}

func finalizeSwarmLocalContainerList(out []SwarmLocalContainerRecord, limit int) []SwarmLocalContainerRecord {
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	if limit > 0 && len(out) > limit {
		return out[:limit]
	}
	return out
}

func normalizeSwarmLocalContainerRecord(record SwarmLocalContainerRecord) SwarmLocalContainerRecord {
	record.ID = normalizeSwarmLocalContainerID(record.ID)
	record.UserID = strings.TrimSpace(record.UserID)
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.Name = strings.TrimSpace(record.Name)
	record.ContainerName = normalizeContainerSlug(record.ContainerName)
	if record.ContainerName == "" {
		record.ContainerName = record.ID
	}
	record.Runtime = normalizeSwarmLocalContainerRuntime(record.Runtime)
	record.NetworkName = normalizeContainerSlug(record.NetworkName)
	record.Status = normalizeSwarmLocalContainerStatus(record.Status)
	record.ContainerID = strings.TrimSpace(record.ContainerID)
	record.HostAPIBaseURL = strings.TrimSpace(record.HostAPIBaseURL)
	record.Image = strings.TrimSpace(record.Image)
	record.Warning = strings.TrimSpace(record.Warning)
	if record.HostPort < 0 {
		record.HostPort = 0
	}
	if record.RuntimePort < 0 {
		record.RuntimePort = 0
	}
	record.Mounts = normalizeSwarmLocalContainerMounts(record.Mounts)
	if record.CreatedAt < 0 {
		record.CreatedAt = 0
	}
	if record.UpdatedAt < 0 {
		record.UpdatedAt = 0
	}
	return record
}

func normalizeSwarmLocalContainerMounts(mounts []SwarmLocalContainerMount) []SwarmLocalContainerMount {
	if len(mounts) == 0 {
		return nil
	}
	out := make([]SwarmLocalContainerMount, 0, len(mounts))
	seen := map[string]struct{}{}
	for _, mount := range mounts {
		mount.SourcePath = strings.TrimSpace(mount.SourcePath)
		mount.TargetPath = strings.TrimSpace(mount.TargetPath)
		mount.WorkspacePath = strings.TrimSpace(mount.WorkspacePath)
		mount.WorkspaceName = strings.TrimSpace(mount.WorkspaceName)
		mount.Mode = normalizeContainerMountMode(mount.Mode)
		if mount.SourcePath == "" || mount.TargetPath == "" {
			continue
		}
		key := strings.ToLower(mount.SourcePath) + "|" + strings.ToLower(mount.TargetPath)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, mount)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeSwarmLocalContainerID(value string) string {
	return normalizeContainerSlug(value)
}

func normalizeSwarmLocalContainerRuntime(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "docker":
		return "docker"
	default:
		return "podman"
	}
}

func normalizeSwarmLocalContainerStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "running":
		return "running"
	case "exited":
		return "exited"
	case "missing":
		return "missing"
	default:
		return "created"
	}
}

func decodeSwarmLocalContainerIDFromKey(key string) string {
	if !strings.HasPrefix(key, SwarmLocalContainerPrefix()) {
		return ""
	}
	raw := strings.TrimPrefix(key, SwarmLocalContainerPrefix())
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return ""
	}
	return normalizeSwarmLocalContainerID(decoded)
}
