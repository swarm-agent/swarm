package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

type ContainerPackageSelectionRecord struct {
	Name   string `json:"name"`
	Source string `json:"source,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type ContainerPackageManifestRecord struct {
	BaseImage      string                            `json:"base_image,omitempty"`
	PackageManager string                            `json:"package_manager,omitempty"`
	Packages       []ContainerPackageSelectionRecord `json:"packages,omitempty"`
}

type DeployContainerRecord struct {
	ID                    string                              `json:"id"`
	Kind                  string                              `json:"kind"`
	Name                  string                              `json:"name"`
	Status                string                              `json:"status"`
	Runtime               string                              `json:"runtime,omitempty"`
	ContainerName         string                              `json:"container_name,omitempty"`
	ContainerID           string                              `json:"container_id,omitempty"`
	HostAPIBaseURL        string                              `json:"host_api_base_url,omitempty"`
	BackendHostPort       int                                 `json:"backend_host_port,omitempty"`
	DesktopHostPort       int                                 `json:"desktop_host_port,omitempty"`
	Image                 string                              `json:"image,omitempty"`
	SyncEnabled           bool                                `json:"sync_enabled,omitempty"`
	SyncMode              string                              `json:"sync_mode,omitempty"`
	SyncModules           []string                            `json:"sync_modules,omitempty"`
	SyncOwnerSwarmID      string                              `json:"sync_owner_swarm_id,omitempty"`
	SyncCredentialURL     string                              `json:"sync_credential_url,omitempty"`
	SyncAgentURL          string                              `json:"sync_agent_url,omitempty"`
	SyncBundlePassword    string                              `json:"sync_bundle_password,omitempty"`
	SyncBundleExportedAt  int64                               `json:"sync_bundle_exported_at,omitempty"`
	SyncBundleExportCount int                                 `json:"sync_bundle_export_count,omitempty"`
	AttachStatus          string                              `json:"attach_status,omitempty"`
	VerificationCode      string                              `json:"verification_code,omitempty"`
	BootstrapSecret       string                              `json:"bootstrap_secret,omitempty"`
	BootstrapExpiresAt    int64                               `json:"bootstrap_secret_expires_at,omitempty"`
	BootstrapSecretUsedAt int64                               `json:"bootstrap_secret_used_at,omitempty"`
	BootstrapSecretSent   bool                                `json:"bootstrap_secret_sent,omitempty"`
	BypassPermissions     bool                                `json:"bypass_permissions,omitempty"`
	ChildSwarmID          string                              `json:"child_swarm_id,omitempty"`
	ChildDisplayName      string                              `json:"child_display_name,omitempty"`
	ChildBackendURL       string                              `json:"child_backend_url,omitempty"`
	ChildDesktopURL       string                              `json:"child_desktop_url,omitempty"`
	ChildPublicKey        string                              `json:"child_public_key,omitempty"`
	ChildFingerprint      string                              `json:"child_fingerprint,omitempty"`
	HostSwarmID           string                              `json:"host_swarm_id,omitempty"`
	HostDisplayName       string                              `json:"host_display_name,omitempty"`
	HostPublicKey         string                              `json:"host_public_key,omitempty"`
	HostFingerprint       string                              `json:"host_fingerprint,omitempty"`
	HostBackendURL        string                              `json:"host_backend_url,omitempty"`
	HostDesktopURL        string                              `json:"host_desktop_url,omitempty"`
	GroupID               string                              `json:"group_id,omitempty"`
	GroupName             string                              `json:"group_name,omitempty"`
	GroupNetworkName      string                              `json:"group_network_name,omitempty"`
	WorkspaceBootstrap    []DeployContainerWorkspaceBootstrap `json:"workspace_bootstrap,omitempty"`
	ContainerPackages     ContainerPackageManifestRecord      `json:"container_packages,omitempty"`
	LastAttachError       string                              `json:"last_attach_error,omitempty"`
	DecidedAt             int64                               `json:"decided_at,omitempty"`
	CreatedAt             int64                               `json:"created_at"`
	UpdatedAt             int64                               `json:"updated_at"`
}

type DeployContainerWorkspaceBootstrapDirectory struct {
	SourcePath string `json:"source_path"`
	TargetPath string `json:"target_path"`
}

type DeployContainerWorkspaceBootstrap struct {
	SourceWorkspacePath string                                       `json:"source_workspace_path"`
	SourceWorkspaceName string                                       `json:"source_workspace_name"`
	TargetWorkspacePath string                                       `json:"target_workspace_path"`
	ThemeID             string                                       `json:"theme_id,omitempty"`
	Directories         []DeployContainerWorkspaceBootstrapDirectory `json:"directories,omitempty"`
	ReplicationMode     string                                       `json:"replication_mode,omitempty"`
	Writable            bool                                         `json:"writable"`
	Sync                WorkspaceReplicationSync                     `json:"sync,omitempty"`
	MakeCurrent         bool                                         `json:"make_current,omitempty"`
}

type DeployContainerStore struct {
	store *Store
}

func NewDeployContainerStore(store *Store) *DeployContainerStore {
	return &DeployContainerStore{store: store}
}

func (s *DeployContainerStore) Get(deploymentID string) (DeployContainerRecord, bool, error) {
	if s == nil || s.store == nil {
		return DeployContainerRecord{}, false, nil
	}
	deploymentID = normalizeDeployContainerID(deploymentID)
	if deploymentID == "" {
		return DeployContainerRecord{}, false, errors.New("deploy container id is required")
	}
	var record DeployContainerRecord
	ok, err := s.store.GetJSON(KeyDeployContainer(deploymentID), &record)
	if err != nil {
		return DeployContainerRecord{}, false, err
	}
	if !ok {
		return DeployContainerRecord{}, false, nil
	}
	record = normalizeDeployContainerRecord(record)
	if record.ID == "" {
		record.ID = deploymentID
	}
	return record, true, nil
}

func (s *DeployContainerStore) Put(record DeployContainerRecord) (DeployContainerRecord, error) {
	if s == nil || s.store == nil {
		return DeployContainerRecord{}, errors.New("deploy container store is not configured")
	}
	record = normalizeDeployContainerRecord(record)
	if record.ID == "" {
		return DeployContainerRecord{}, errors.New("deploy container id is required")
	}
	if record.Name == "" {
		return DeployContainerRecord{}, errors.New("deploy container name is required")
	}
	now := time.Now().UnixMilli()
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeyDeployContainer(record.ID), record); err != nil {
		return DeployContainerRecord{}, err
	}
	return record, nil
}

func (s *DeployContainerStore) Delete(deploymentID string) error {
	if s == nil || s.store == nil {
		return errors.New("deploy container store is not configured")
	}
	deploymentID = normalizeDeployContainerID(deploymentID)
	if deploymentID == "" {
		return errors.New("deploy container id is required")
	}
	return s.store.Delete(KeyDeployContainer(deploymentID))
}

func (s *DeployContainerStore) List(limit int) ([]DeployContainerRecord, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]DeployContainerRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(DeployContainerPrefix(), limit, func(key string, value []byte) error {
		var record DeployContainerRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode deploy container: %w", err)
		}
		record = normalizeDeployContainerRecord(record)
		if record.ID == "" {
			record.ID = decodeDeployContainerIDFromKey(key)
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
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

func normalizeDeployContainerRecord(record DeployContainerRecord) DeployContainerRecord {
	record.ID = normalizeDeployContainerID(record.ID)
	record.Kind = strings.TrimSpace(record.Kind)
	if record.Kind == "" {
		record.Kind = "container"
	}
	record.Name = strings.TrimSpace(record.Name)
	record.Status = normalizeDeployContainerStatus(record.Status)
	record.Runtime = normalizeSwarmLocalContainerRuntime(record.Runtime)
	record.ContainerName = normalizeContainerSlug(record.ContainerName)
	record.ContainerID = strings.TrimSpace(record.ContainerID)
	record.HostAPIBaseURL = strings.TrimSpace(record.HostAPIBaseURL)
	record.Image = strings.TrimSpace(record.Image)
	record.SyncMode = strings.TrimSpace(record.SyncMode)
	record.SyncModules = normalizeWorkspaceReplicationSyncModules(record.SyncModules)
	record.SyncOwnerSwarmID = strings.TrimSpace(record.SyncOwnerSwarmID)
	record.SyncCredentialURL = strings.TrimSpace(record.SyncCredentialURL)
	record.SyncAgentURL = strings.TrimSpace(record.SyncAgentURL)
	record.SyncBundlePassword = strings.TrimSpace(record.SyncBundlePassword)
	record.AttachStatus = normalizeDeployAttachStatus(record.AttachStatus)
	record.VerificationCode = strings.ToUpper(strings.TrimSpace(record.VerificationCode))
	record.BootstrapSecret = strings.TrimSpace(record.BootstrapSecret)
	record.ChildSwarmID = strings.TrimSpace(record.ChildSwarmID)
	record.ChildDisplayName = strings.TrimSpace(record.ChildDisplayName)
	record.ChildBackendURL = strings.TrimSpace(record.ChildBackendURL)
	record.ChildDesktopURL = strings.TrimSpace(record.ChildDesktopURL)
	record.ChildPublicKey = strings.TrimSpace(record.ChildPublicKey)
	record.ChildFingerprint = strings.TrimSpace(record.ChildFingerprint)
	record.HostSwarmID = strings.TrimSpace(record.HostSwarmID)
	record.HostDisplayName = strings.TrimSpace(record.HostDisplayName)
	record.HostPublicKey = strings.TrimSpace(record.HostPublicKey)
	record.HostFingerprint = strings.TrimSpace(record.HostFingerprint)
	record.HostBackendURL = strings.TrimSpace(record.HostBackendURL)
	record.HostDesktopURL = strings.TrimSpace(record.HostDesktopURL)
	record.GroupID = strings.TrimSpace(record.GroupID)
	record.GroupName = strings.TrimSpace(record.GroupName)
	record.GroupNetworkName = normalizeContainerSlug(record.GroupNetworkName)
	record.WorkspaceBootstrap = normalizeDeployContainerWorkspaceBootstrapList(record.WorkspaceBootstrap)
	record.ContainerPackages = normalizeContainerPackageManifestRecord(record.ContainerPackages)
	record.LastAttachError = strings.TrimSpace(record.LastAttachError)
	if record.BackendHostPort < 0 {
		record.BackendHostPort = 0
	}
	if record.DesktopHostPort < 0 {
		record.DesktopHostPort = 0
	}
	if record.BootstrapExpiresAt < 0 {
		record.BootstrapExpiresAt = 0
	}
	if record.BootstrapSecretUsedAt < 0 {
		record.BootstrapSecretUsedAt = 0
	}
	if record.DecidedAt < 0 {
		record.DecidedAt = 0
	}
	if record.SyncBundleExportedAt < 0 {
		record.SyncBundleExportedAt = 0
	}
	if record.SyncBundleExportCount < 0 {
		record.SyncBundleExportCount = 0
	}
	if record.CreatedAt < 0 {
		record.CreatedAt = 0
	}
	if record.UpdatedAt < 0 {
		record.UpdatedAt = 0
	}
	return record
}

func normalizeContainerPackageManifestRecord(record ContainerPackageManifestRecord) ContainerPackageManifestRecord {
	record.BaseImage = strings.TrimSpace(record.BaseImage)
	record.PackageManager = strings.TrimSpace(record.PackageManager)
	if len(record.Packages) == 0 {
		record.Packages = nil
		return record
	}
	out := make([]ContainerPackageSelectionRecord, 0, len(record.Packages))
	seen := make(map[string]struct{}, len(record.Packages))
	for _, pkg := range record.Packages {
		item := ContainerPackageSelectionRecord{
			Name:   strings.TrimSpace(pkg.Name),
			Source: strings.TrimSpace(pkg.Source),
			Reason: strings.TrimSpace(pkg.Reason),
		}
		if item.Name == "" {
			continue
		}
		key := strings.ToLower(item.Name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	if len(out) == 0 {
		record.Packages = nil
	} else {
		record.Packages = out
	}
	return record
}

func normalizeDeployContainerWorkspaceBootstrapList(items []DeployContainerWorkspaceBootstrap) []DeployContainerWorkspaceBootstrap {
	if len(items) == 0 {
		return nil
	}
	out := make([]DeployContainerWorkspaceBootstrap, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, raw := range items {
		item := normalizeDeployContainerWorkspaceBootstrap(raw)
		if item.TargetWorkspacePath == "" {
			continue
		}
		key := strings.ToLower(item.TargetWorkspacePath)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeDeployContainerWorkspaceBootstrap(item DeployContainerWorkspaceBootstrap) DeployContainerWorkspaceBootstrap {
	item.SourceWorkspacePath = strings.TrimSpace(item.SourceWorkspacePath)
	item.SourceWorkspaceName = strings.TrimSpace(item.SourceWorkspaceName)
	item.TargetWorkspacePath = strings.TrimSpace(item.TargetWorkspacePath)
	item.ThemeID = normalizeWorkspaceThemeID(item.ThemeID)
	item.ReplicationMode = strings.TrimSpace(strings.ToLower(item.ReplicationMode))
	item.Sync = normalizeWorkspaceReplicationSync(item.Sync)
	item.Directories = normalizeDeployContainerWorkspaceBootstrapDirectories(item.Directories)
	return item
}

func normalizeDeployContainerWorkspaceBootstrapDirectories(items []DeployContainerWorkspaceBootstrapDirectory) []DeployContainerWorkspaceBootstrapDirectory {
	if len(items) == 0 {
		return nil
	}
	out := make([]DeployContainerWorkspaceBootstrapDirectory, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, raw := range items {
		item := DeployContainerWorkspaceBootstrapDirectory{
			SourcePath: strings.TrimSpace(raw.SourcePath),
			TargetPath: strings.TrimSpace(raw.TargetPath),
		}
		if item.TargetPath == "" {
			continue
		}
		key := strings.ToLower(item.TargetPath)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeDeployContainerID(value string) string {
	return normalizeContainerSlug(value)
}

func normalizeDeployContainerStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "running":
		return "running"
	case "attached":
		return "attached"
	case "stopped":
		return "stopped"
	case "failed":
		return "failed"
	default:
		return "creating"
	}
}

func normalizeDeployAttachStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "launching":
		return "launching"
	case "attach_requested":
		return "attach_requested"
	case "attached":
		return "attached"
	case "rejected":
		return "rejected"
	case "failed":
		return "failed"
	default:
		return "pending"
	}
}

func decodeDeployContainerIDFromKey(key string) string {
	if !strings.HasPrefix(key, DeployContainerPrefix()) {
		return ""
	}
	raw := strings.TrimPrefix(key, DeployContainerPrefix())
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return ""
	}
	return normalizeDeployContainerID(decoded)
}
