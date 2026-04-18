package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	ContainerAccessModeLocal    = "local"
	ContainerAccessModeLAN      = "lan"
	ContainerAccessModeTailnet  = "tailnet"
	ContainerRoleHintMaster     = "master"
	ContainerRoleHintChild      = "child"
	ContainerMountModeReadWrite = "rw"
	ContainerMountModeReadOnly  = "ro"
)

type ContainerProfileMount struct {
	SourcePath    string `json:"source_path"`
	TargetPath    string `json:"target_path,omitempty"`
	Mode          string `json:"mode,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
}

type ContainerProfileRecord struct {
	ID                string                  `json:"id"`
	Name              string                  `json:"name"`
	Description       string                  `json:"description,omitempty"`
	RoleHint          string                  `json:"role_hint,omitempty"`
	AccessMode        string                  `json:"access_mode,omitempty"`
	ContainerName     string                  `json:"container_name,omitempty"`
	Hostname          string                  `json:"hostname,omitempty"`
	NetworkName       string                  `json:"network_name,omitempty"`
	APIPort           int                     `json:"api_port,omitempty"`
	AdvertiseHost     string                  `json:"advertise_host,omitempty"`
	AdvertisePort     int                     `json:"advertise_port,omitempty"`
	TailscaleHostname string                  `json:"tailscale_hostname,omitempty"`
	Mounts            []ContainerProfileMount `json:"mounts,omitempty"`
	CreatedAt         int64                   `json:"created_at"`
	UpdatedAt         int64                   `json:"updated_at"`
}

type SwarmContainerProfileStore struct {
	store *Store
}

func NewSwarmContainerProfileStore(store *Store) *SwarmContainerProfileStore {
	return &SwarmContainerProfileStore{store: store}
}

func (s *SwarmContainerProfileStore) GetProfile(profileID string) (ContainerProfileRecord, bool, error) {
	if s == nil || s.store == nil {
		return ContainerProfileRecord{}, false, nil
	}
	profileID = normalizeContainerProfileID(profileID)
	if profileID == "" {
		return ContainerProfileRecord{}, false, errors.New("container profile id is required")
	}
	var record ContainerProfileRecord
	ok, err := s.store.GetJSON(KeySwarmContainerProfile(profileID), &record)
	if err != nil {
		return ContainerProfileRecord{}, false, err
	}
	if !ok {
		return ContainerProfileRecord{}, false, nil
	}
	record = normalizeContainerProfileRecord(record)
	if record.ID == "" {
		record.ID = profileID
	}
	return record, true, nil
}

func (s *SwarmContainerProfileStore) PutProfile(profile ContainerProfileRecord) (ContainerProfileRecord, error) {
	if s == nil || s.store == nil {
		return ContainerProfileRecord{}, errors.New("container profile store is not configured")
	}
	profile = normalizeContainerProfileRecord(profile)
	if profile.ID == "" {
		return ContainerProfileRecord{}, errors.New("container profile id is required")
	}
	if profile.Name == "" {
		return ContainerProfileRecord{}, errors.New("container profile name is required")
	}
	now := time.Now().UnixMilli()
	if profile.CreatedAt <= 0 {
		profile.CreatedAt = now
	}
	profile.UpdatedAt = now
	if err := s.store.PutJSON(KeySwarmContainerProfile(profile.ID), profile); err != nil {
		return ContainerProfileRecord{}, err
	}
	return profile, nil
}

func (s *SwarmContainerProfileStore) DeleteProfile(profileID string) error {
	if s == nil || s.store == nil {
		return nil
	}
	profileID = normalizeContainerProfileID(profileID)
	if profileID == "" {
		return errors.New("container profile id is required")
	}
	return s.store.Delete(KeySwarmContainerProfile(profileID))
}

func (s *SwarmContainerProfileStore) ListProfiles(limit int) ([]ContainerProfileRecord, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]ContainerProfileRecord, 0, min(limit, 32))
	err := s.store.IteratePrefix(SwarmContainerProfilePrefix(), limit, func(key string, value []byte) error {
		var record ContainerProfileRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode container profile: %w", err)
		}
		record = normalizeContainerProfileRecord(record)
		if record.ID == "" {
			record.ID = decodeContainerProfileIDFromKey(key)
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
	sort.Slice(out, func(i, j int) bool {
		left := strings.ToLower(strings.TrimSpace(out[i].Name))
		right := strings.ToLower(strings.TrimSpace(out[j].Name))
		if left == right {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return left < right
	})
	return out, nil
}

func normalizeContainerProfileRecord(record ContainerProfileRecord) ContainerProfileRecord {
	record.Name = strings.TrimSpace(record.Name)
	record.ID = normalizeContainerProfileID(record.ID)
	if record.ID == "" {
		record.ID = normalizeContainerProfileID(record.Name)
	}
	record.Description = strings.TrimSpace(record.Description)
	record.RoleHint = normalizeContainerRoleHint(record.RoleHint)
	record.AccessMode = normalizeContainerAccessMode(record.AccessMode)
	record.ContainerName = normalizeContainerSlug(record.ContainerName)
	if record.ContainerName == "" {
		record.ContainerName = record.ID
	}
	record.Hostname = normalizeContainerSlug(record.Hostname)
	if record.Hostname == "" {
		record.Hostname = record.ContainerName
	}
	record.NetworkName = normalizeContainerSlug(record.NetworkName)
	record.AdvertiseHost = strings.TrimSpace(record.AdvertiseHost)
	record.TailscaleHostname = normalizeContainerSlug(record.TailscaleHostname)
	record.Mounts = normalizeContainerProfileMounts(record.Mounts)
	if record.APIPort < 0 {
		record.APIPort = 0
	}
	if record.AdvertisePort < 0 {
		record.AdvertisePort = 0
	}
	if record.CreatedAt < 0 {
		record.CreatedAt = 0
	}
	if record.UpdatedAt < 0 {
		record.UpdatedAt = 0
	}
	return record
}

func normalizeContainerProfileMounts(mounts []ContainerProfileMount) []ContainerProfileMount {
	if len(mounts) == 0 {
		return nil
	}
	out := make([]ContainerProfileMount, 0, len(mounts))
	seen := make(map[string]struct{}, len(mounts))
	for _, mount := range mounts {
		mount.SourcePath = strings.TrimSpace(mount.SourcePath)
		if mount.SourcePath == "" {
			continue
		}
		mount.TargetPath = strings.TrimSpace(mount.TargetPath)
		if mount.TargetPath == "" {
			base := filepath.Base(filepath.Clean(mount.SourcePath))
			base = normalizeContainerSlug(base)
			if base == "" {
				base = "workspace"
			}
			mount.TargetPath = "/workspace/" + base
		}
		mount.Mode = normalizeContainerMountMode(mount.Mode)
		mount.WorkspacePath = strings.TrimSpace(mount.WorkspacePath)
		mount.WorkspaceName = strings.TrimSpace(mount.WorkspaceName)
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

func normalizeContainerAccessMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ContainerAccessModeLAN:
		return ContainerAccessModeLAN
	case ContainerAccessModeTailnet:
		return ContainerAccessModeTailnet
	default:
		return ContainerAccessModeLocal
	}
}

func normalizeContainerRoleHint(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ContainerRoleHintChild:
		return ContainerRoleHintChild
	default:
		return ContainerRoleHintMaster
	}
}

func normalizeContainerMountMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ContainerMountModeReadOnly:
		return ContainerMountModeReadOnly
	default:
		return ContainerMountModeReadWrite
	}
}

func normalizeContainerProfileID(value string) string {
	return normalizeContainerSlug(value)
}

func normalizeContainerSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
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
		case r == '-', r == '_', r == ' ', r == '.', r == '/':
			if b.Len() == 0 || lastDash {
				continue
			}
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	return out
}

func decodeContainerProfileIDFromKey(key string) string {
	if !strings.HasPrefix(key, SwarmContainerProfilePrefix()) {
		return ""
	}
	raw := strings.TrimPrefix(key, SwarmContainerProfilePrefix())
	decoded, err := url.QueryUnescape(raw)
	if err != nil {
		return ""
	}
	return normalizeContainerProfileID(decoded)
}
