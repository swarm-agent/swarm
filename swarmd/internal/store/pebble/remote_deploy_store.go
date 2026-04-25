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

type RemoteDeployPayloadRecord struct {
	ID            string                               `json:"id"`
	SourcePath    string                               `json:"source_path,omitempty"`
	WorkspacePath string                               `json:"workspace_path,omitempty"`
	WorkspaceName string                               `json:"workspace_name,omitempty"`
	TargetPath    string                               `json:"target_path,omitempty"`
	Mode          string                               `json:"mode,omitempty"`
	Directories   []RemoteDeployPayloadDirectoryRecord `json:"directories,omitempty"`
	GitRoot       string                               `json:"git_root,omitempty"`
	ArchiveName   string                               `json:"archive_name,omitempty"`
	IncludedFiles int                                  `json:"included_files,omitempty"`
	IncludedBytes int64                                `json:"included_bytes,omitempty"`
	ExcludedNote  string                               `json:"excluded_note,omitempty"`
}

type RemoteDeployPayloadDirectoryRecord struct {
	SourcePath    string `json:"source_path,omitempty"`
	TargetPath    string `json:"target_path,omitempty"`
	GitRoot       string `json:"git_root,omitempty"`
	ArchiveName   string `json:"archive_name,omitempty"`
	IncludedFiles int    `json:"included_files,omitempty"`
	IncludedBytes int64  `json:"included_bytes,omitempty"`
	ExcludedNote  string `json:"excluded_note,omitempty"`
}

type RemoteDeployDiskRecord struct {
	Path           string `json:"path,omitempty"`
	AvailableBytes int64  `json:"available_bytes,omitempty"`
	RequiredBytes  int64  `json:"required_bytes,omitempty"`
}

type RemoteDeploySessionRecord struct {
	ID                      string                         `json:"id"`
	Name                    string                         `json:"name"`
	Status                  string                         `json:"status"`
	SSHSessionTarget        string                         `json:"ssh_session_target,omitempty"`
	TransportMode           string                         `json:"transport_mode,omitempty"`
	MasterEndpoint          string                         `json:"master_endpoint,omitempty"`
	RemoteEndpoint          string                         `json:"remote_endpoint,omitempty"`
	RemoteAdvertiseHost     string                         `json:"remote_advertise_host,omitempty"`
	GroupID                 string                         `json:"group_id,omitempty"`
	GroupName               string                         `json:"group_name,omitempty"`
	BuilderRuntime          string                         `json:"builder_runtime,omitempty"`
	RemoteRuntime           string                         `json:"remote_runtime,omitempty"`
	ImageDeliveryMode       string                         `json:"image_delivery_mode,omitempty"`
	ImagePrefix             string                         `json:"image_prefix,omitempty"`
	SystemdUnit             string                         `json:"systemd_unit,omitempty"`
	RemoteRoot              string                         `json:"remote_root,omitempty"`
	MasterTailscaleURL      string                         `json:"master_tailscale_url,omitempty"`
	MasterSwarmID           string                         `json:"master_swarm_id,omitempty"`
	SessionToken            string                         `json:"session_token,omitempty"`
	InviteToken             string                         `json:"invite_token,omitempty"`
	EnrollmentID            string                         `json:"enrollment_id,omitempty"`
	EnrollmentStatus        string                         `json:"enrollment_status,omitempty"`
	ChildSwarmID            string                         `json:"child_swarm_id,omitempty"`
	ChildName               string                         `json:"child_name,omitempty"`
	ChildPublicKey          string                         `json:"child_public_key,omitempty"`
	ChildFingerprint        string                         `json:"child_fingerprint,omitempty"`
	HostSwarmID             string                         `json:"host_swarm_id,omitempty"`
	HostName                string                         `json:"host_name,omitempty"`
	HostPublicKey           string                         `json:"host_public_key,omitempty"`
	HostFingerprint         string                         `json:"host_fingerprint,omitempty"`
	HostAPIBaseURL          string                         `json:"host_api_base_url,omitempty"`
	HostDesktopURL          string                         `json:"host_desktop_url,omitempty"`
	RemoteAuthURL           string                         `json:"remote_auth_url,omitempty"`
	RemoteTailnetURL        string                         `json:"remote_tailnet_url,omitempty"`
	LastPairingURL          string                         `json:"last_pairing_url,omitempty"`
	ImageRef                string                         `json:"image_ref,omitempty"`
	ImageSignature          string                         `json:"image_signature,omitempty"`
	ImageArchiveBytes       int64                          `json:"image_archive_bytes,omitempty"`
	LastRemoteOutput        string                         `json:"last_remote_output,omitempty"`
	LastError               string                         `json:"last_error,omitempty"`
	SSHReachable            bool                           `json:"ssh_reachable,omitempty"`
	SystemdAvailable        bool                           `json:"systemd_available,omitempty"`
	SudoMode                string                         `json:"sudo_mode,omitempty"`
	SyncEnabled             bool                           `json:"sync_enabled,omitempty"`
	SyncMode                string                         `json:"sync_mode,omitempty"`
	SyncOwnerSwarmID        string                         `json:"sync_owner_swarm_id,omitempty"`
	BypassPermissions       bool                           `json:"bypass_permissions,omitempty"`
	ContainerPackages       ContainerPackageManifestRecord `json:"container_packages,omitempty"`
	SyncCredentialURL       string                         `json:"sync_credential_url,omitempty"`
	SyncBundlePassword      string                         `json:"sync_bundle_password,omitempty"`
	SyncBundleExportedAt    int64                          `json:"sync_bundle_exported_at,omitempty"`
	SyncBundleExportCount   int                            `json:"sync_bundle_export_count,omitempty"`
	RemoteNetworkCandidates []string                       `json:"remote_network_candidates,omitempty"`
	RemoteDisk              RemoteDeployDiskRecord         `json:"remote_disk,omitempty"`
	FilesToCopy             []string                       `json:"files_to_copy,omitempty"`
	Payloads                []RemoteDeployPayloadRecord    `json:"payloads,omitempty"`
	ApprovedAt              int64                          `json:"approved_at,omitempty"`
	AttachedAt              int64                          `json:"attached_at,omitempty"`
	CreatedAt               int64                          `json:"created_at"`
	UpdatedAt               int64                          `json:"updated_at"`
}

type RemoteDeploySessionStore struct {
	store *Store
}

func NewRemoteDeploySessionStore(store *Store) *RemoteDeploySessionStore {
	return &RemoteDeploySessionStore{store: store}
}

func (s *RemoteDeploySessionStore) Get(sessionID string) (RemoteDeploySessionRecord, bool, error) {
	if s == nil || s.store == nil {
		return RemoteDeploySessionRecord{}, false, nil
	}
	sessionID = normalizeRemoteDeploySessionID(sessionID)
	if sessionID == "" {
		return RemoteDeploySessionRecord{}, false, errors.New("remote deploy session id is required")
	}
	var record RemoteDeploySessionRecord
	ok, err := s.store.GetJSON(KeyRemoteDeploySession(sessionID), &record)
	if err != nil {
		return RemoteDeploySessionRecord{}, false, err
	}
	if !ok {
		return RemoteDeploySessionRecord{}, false, nil
	}
	record = normalizeRemoteDeploySessionRecord(record)
	if record.ID == "" {
		record.ID = sessionID
	}
	return record, true, nil
}

func (s *RemoteDeploySessionStore) Put(record RemoteDeploySessionRecord) (RemoteDeploySessionRecord, error) {
	if s == nil || s.store == nil {
		return RemoteDeploySessionRecord{}, errors.New("remote deploy session store is not configured")
	}
	record = normalizeRemoteDeploySessionRecord(record)
	if record.ID == "" {
		return RemoteDeploySessionRecord{}, errors.New("remote deploy session id is required")
	}
	if record.Name == "" {
		return RemoteDeploySessionRecord{}, errors.New("remote deploy session name is required")
	}
	now := time.Now().UnixMilli()
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeyRemoteDeploySession(record.ID), record); err != nil {
		return RemoteDeploySessionRecord{}, err
	}
	return record, nil
}

func (s *RemoteDeploySessionStore) Delete(sessionID string) error {
	if s == nil || s.store == nil {
		return errors.New("remote deploy session store is not configured")
	}
	sessionID = normalizeRemoteDeploySessionID(sessionID)
	if sessionID == "" {
		return errors.New("remote deploy session id is required")
	}
	return s.store.Delete(KeyRemoteDeploySession(sessionID))
}

func (s *RemoteDeploySessionStore) List(limit int) ([]RemoteDeploySessionRecord, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]RemoteDeploySessionRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(RemoteDeploySessionPrefix(), limit, func(key string, value []byte) error {
		var record RemoteDeploySessionRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode remote deploy session: %w", err)
		}
		record = normalizeRemoteDeploySessionRecord(record)
		if record.ID == "" {
			record.ID = decodeRemoteDeploySessionIDFromKey(key)
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

func normalizeRemoteDeploySessionRecord(record RemoteDeploySessionRecord) RemoteDeploySessionRecord {
	record.ID = normalizeRemoteDeploySessionID(record.ID)
	record.Name = strings.TrimSpace(record.Name)
	record.Status = normalizeRemoteDeploySessionStatus(record.Status)
	record.SSHSessionTarget = strings.TrimSpace(record.SSHSessionTarget)
	record.TransportMode = strings.ToLower(strings.TrimSpace(record.TransportMode))
	if record.TransportMode == "" {
		record.TransportMode = "tailscale"
	}
	record.MasterEndpoint = strings.TrimSpace(record.MasterEndpoint)
	record.RemoteEndpoint = strings.TrimSpace(record.RemoteEndpoint)
	record.RemoteAdvertiseHost = strings.TrimSpace(record.RemoteAdvertiseHost)
	record.GroupID = strings.TrimSpace(record.GroupID)
	record.GroupName = strings.TrimSpace(record.GroupName)
	record.BuilderRuntime = strings.TrimSpace(record.BuilderRuntime)
	record.RemoteRuntime = normalizeSwarmLocalContainerRuntime(record.RemoteRuntime)
	record.ImageDeliveryMode = strings.ToLower(strings.TrimSpace(record.ImageDeliveryMode))
	record.ImagePrefix = strings.TrimRight(strings.TrimSpace(record.ImagePrefix), "/")
	record.SystemdUnit = strings.TrimSpace(record.SystemdUnit)
	record.RemoteRoot = strings.TrimSpace(record.RemoteRoot)
	record.MasterTailscaleURL = strings.TrimSpace(record.MasterTailscaleURL)
	if record.MasterEndpoint == "" {
		record.MasterEndpoint = record.MasterTailscaleURL
	}
	record.MasterSwarmID = strings.TrimSpace(record.MasterSwarmID)
	record.SessionToken = strings.TrimSpace(record.SessionToken)
	record.InviteToken = strings.TrimSpace(record.InviteToken)
	record.EnrollmentID = strings.TrimSpace(record.EnrollmentID)
	record.EnrollmentStatus = strings.TrimSpace(record.EnrollmentStatus)
	record.ChildSwarmID = strings.TrimSpace(record.ChildSwarmID)
	record.ChildName = strings.TrimSpace(record.ChildName)
	record.ChildPublicKey = strings.TrimSpace(record.ChildPublicKey)
	record.ChildFingerprint = strings.TrimSpace(record.ChildFingerprint)
	record.HostSwarmID = strings.TrimSpace(record.HostSwarmID)
	record.HostName = strings.TrimSpace(record.HostName)
	record.HostPublicKey = strings.TrimSpace(record.HostPublicKey)
	record.HostFingerprint = strings.TrimSpace(record.HostFingerprint)
	record.HostAPIBaseURL = strings.TrimSpace(record.HostAPIBaseURL)
	record.HostDesktopURL = strings.TrimSpace(record.HostDesktopURL)
	record.RemoteAuthURL = strings.TrimSpace(record.RemoteAuthURL)
	record.RemoteTailnetURL = strings.TrimSpace(record.RemoteTailnetURL)
	if record.RemoteEndpoint == "" {
		record.RemoteEndpoint = record.RemoteTailnetURL
	}
	record.LastPairingURL = strings.TrimSpace(record.LastPairingURL)
	record.ImageRef = strings.TrimSpace(record.ImageRef)
	record.ImageSignature = strings.TrimSpace(record.ImageSignature)
	record.LastRemoteOutput = strings.TrimSpace(record.LastRemoteOutput)
	record.LastError = strings.TrimSpace(record.LastError)
	record.SudoMode = strings.TrimSpace(record.SudoMode)
	record.SyncMode = strings.TrimSpace(record.SyncMode)
	record.SyncOwnerSwarmID = strings.TrimSpace(record.SyncOwnerSwarmID)
	record.ContainerPackages = normalizeContainerPackageManifestRecord(record.ContainerPackages)
	record.SyncCredentialURL = strings.TrimSpace(record.SyncCredentialURL)
	record.SyncBundlePassword = strings.TrimSpace(record.SyncBundlePassword)
	if record.SyncBundleExportedAt < 0 {
		record.SyncBundleExportedAt = 0
	}
	if record.SyncBundleExportCount < 0 {
		record.SyncBundleExportCount = 0
	}
	record.RemoteNetworkCandidates = normalizeRemoteDeployCandidateList(record.RemoteNetworkCandidates)
	record.RemoteDisk.Path = strings.TrimSpace(record.RemoteDisk.Path)
	if record.RemoteDisk.AvailableBytes < 0 {
		record.RemoteDisk.AvailableBytes = 0
	}
	if record.RemoteDisk.RequiredBytes < 0 {
		record.RemoteDisk.RequiredBytes = 0
	}
	for i := range record.FilesToCopy {
		record.FilesToCopy[i] = strings.TrimSpace(record.FilesToCopy[i])
	}
	for i := range record.Payloads {
		record.Payloads[i].ID = strings.TrimSpace(record.Payloads[i].ID)
		record.Payloads[i].SourcePath = strings.TrimSpace(record.Payloads[i].SourcePath)
		record.Payloads[i].WorkspacePath = strings.TrimSpace(record.Payloads[i].WorkspacePath)
		record.Payloads[i].WorkspaceName = strings.TrimSpace(record.Payloads[i].WorkspaceName)
		record.Payloads[i].TargetPath = strings.TrimSpace(record.Payloads[i].TargetPath)
		record.Payloads[i].Mode = strings.TrimSpace(record.Payloads[i].Mode)
		for j := range record.Payloads[i].Directories {
			record.Payloads[i].Directories[j].SourcePath = strings.TrimSpace(record.Payloads[i].Directories[j].SourcePath)
			record.Payloads[i].Directories[j].TargetPath = strings.TrimSpace(record.Payloads[i].Directories[j].TargetPath)
			record.Payloads[i].Directories[j].GitRoot = strings.TrimSpace(record.Payloads[i].Directories[j].GitRoot)
			record.Payloads[i].Directories[j].ArchiveName = strings.TrimSpace(record.Payloads[i].Directories[j].ArchiveName)
			record.Payloads[i].Directories[j].ExcludedNote = strings.TrimSpace(record.Payloads[i].Directories[j].ExcludedNote)
			if record.Payloads[i].Directories[j].IncludedFiles < 0 {
				record.Payloads[i].Directories[j].IncludedFiles = 0
			}
			if record.Payloads[i].Directories[j].IncludedBytes < 0 {
				record.Payloads[i].Directories[j].IncludedBytes = 0
			}
		}
		record.Payloads[i].GitRoot = strings.TrimSpace(record.Payloads[i].GitRoot)
		record.Payloads[i].ArchiveName = strings.TrimSpace(record.Payloads[i].ArchiveName)
		record.Payloads[i].ExcludedNote = strings.TrimSpace(record.Payloads[i].ExcludedNote)
		if record.Payloads[i].IncludedFiles < 0 {
			record.Payloads[i].IncludedFiles = 0
		}
		if record.Payloads[i].IncludedBytes < 0 {
			record.Payloads[i].IncludedBytes = 0
		}
	}
	if record.ApprovedAt < 0 {
		record.ApprovedAt = 0
	}
	if record.AttachedAt < 0 {
		record.AttachedAt = 0
	}
	if record.ImageArchiveBytes < 0 {
		record.ImageArchiveBytes = 0
	}
	if record.CreatedAt < 0 {
		record.CreatedAt = 0
	}
	if record.UpdatedAt < 0 {
		record.UpdatedAt = 0
	}
	return record
}

func normalizeRemoteDeployCandidateList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeRemoteDeploySessionID(value string) string {
	return normalizeContainerSlug(value)
}

func normalizeRemoteDeploySessionStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "preflight_ready":
		return "preflight_ready"
	case "starting":
		return "starting"
	case "waiting_for_child":
		return "waiting_for_child"
	case "waiting_for_approval":
		return "waiting_for_approval"
	case "approved":
		return "approved"
	case "attached":
		return "attached"
	case "failed":
		return "failed"
	default:
		return "preflight_ready"
	}
}

func decodeRemoteDeploySessionIDFromKey(key string) string {
	if !strings.HasPrefix(key, RemoteDeploySessionPrefix()) {
		return ""
	}
	raw := strings.TrimPrefix(key, RemoteDeploySessionPrefix())
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return ""
	}
	return normalizeRemoteDeploySessionID(decoded)
}
