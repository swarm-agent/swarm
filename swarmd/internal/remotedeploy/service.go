package remotedeploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/buildinfo"
	"swarm-refactor/swarmtui/pkg/startupconfig"
	authruntime "swarm/packages/swarmd/internal/auth"
	deployruntime "swarm/packages/swarmd/internal/deploy"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

const (
	PathSessionList                 = "deploy.remote.list.v1"
	PathSessionCreate               = "deploy.remote.create.v1"
	PathSessionDelete               = "deploy.remote.delete.v1"
	PathSessionStart                = "deploy.remote.start.v1"
	PathSessionUpdateJob            = "deploy.remote.update-job.v1"
	PathSessionApprove              = "deploy.remote.approve.v1"
	PathSessionChildStatus          = "deploy.remote.child_status.v1"
	PathSessionPreflight            = "deploy.remote.preflight.v1"
	remoteImageNamePrefix           = "localhost/swarm-remote-child"
	remoteContainerPrefix           = "swarm-remote-child"
	remoteImagePrefixEnv            = "SWARM_REMOTE_DEPLOY_IMAGE_PREFIX"
	remotePackageManager            = "apt"
	remotePackageBaseImage          = "ubuntu:24.04"
	remoteImageDeliveryArchive      = "archive"
	remoteImageDeliveryRegistry     = "registry"
	legacyRemoteCredentialsFileName = "remote-child.credentials.env"
)

type ContainerPackageSelection struct {
	Name   string `json:"name"`
	Source string `json:"source,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type ContainerPackageManifest struct {
	BaseImage      string                      `json:"base_image,omitempty"`
	PackageManager string                      `json:"package_manager,omitempty"`
	Packages       []ContainerPackageSelection `json:"packages,omitempty"`
}

type PayloadSelection struct {
	SourcePath    string
	WorkspacePath string
	WorkspaceName string
	TargetPath    string
	Mode          string
	Directories   []PayloadDirectorySelection
}

type PayloadDirectorySelection struct {
	SourcePath string
	TargetPath string
}

type CreateSessionInput struct {
	Name                string
	SSHSessionTarget    string
	TransportMode       string
	RemoteAdvertiseHost string
	GroupID             string
	GroupName           string
	RemoteRuntime       string
	ImageDeliveryMode   string
	SyncEnabled         bool
	BypassPermissions   bool
	ContainerPackages   ContainerPackageManifest
	Payloads            []PayloadSelection
}

type StartSessionInput struct {
	SessionID         string
	TailscaleAuthKey  string
	SyncVaultPassword string
}

type UpdateJobInput struct {
	DevMode          *bool
	PostRebuildCheck bool
}

type UpdateJobResult struct {
	PathID          string           `json:"path_id"`
	Mode            string           `json:"mode"`
	DevMode         bool             `json:"dev_mode"`
	Summary         UpdateJobSummary `json:"summary"`
	Items           []UpdateJobItem  `json:"items"`
	StartedAtUnix   int64            `json:"started_at_unix_ms,omitempty"`
	UpdatedAtUnix   int64            `json:"updated_at_unix_ms,omitempty"`
	CompletedAtUnix int64            `json:"completed_at_unix_ms,omitempty"`
}

type UpdateJobSummary struct {
	Total          int `json:"total"`
	Replaced       int `json:"replaced"`
	Skipped        int `json:"skipped"`
	Failed         int `json:"failed"`
	AlreadyCurrent int `json:"already_current"`
	Unknown        int `json:"unknown"`
}

type UpdateJobItem struct {
	ID               string `json:"id"`
	Name             string `json:"name,omitempty"`
	SSHSessionTarget string `json:"ssh_session_target,omitempty"`
	Status           string `json:"status,omitempty"`
	State            string `json:"state"`
	Reason           string `json:"reason,omitempty"`
	PreviousImageRef string `json:"previous_image_ref,omitempty"`
	TargetImageRef   string `json:"target_image_ref,omitempty"`
	ImageSignature   string `json:"image_signature,omitempty"`
	Error            string `json:"error,omitempty"`
}

type ApproveSessionInput struct {
	SessionID string
}

type ChildStatusInput struct {
	SessionID    string
	SessionToken string
}

type DeleteSessionInput struct {
	SessionIDs     []string
	ChildSwarmIDs  []string
	TeardownRemote bool
}

type SessionPayload struct {
	ID            string                    `json:"id"`
	SourcePath    string                    `json:"source_path,omitempty"`
	WorkspacePath string                    `json:"workspace_path,omitempty"`
	WorkspaceName string                    `json:"workspace_name,omitempty"`
	TargetPath    string                    `json:"target_path,omitempty"`
	Mode          string                    `json:"mode,omitempty"`
	Directories   []SessionPayloadDirectory `json:"directories,omitempty"`
	GitRoot       string                    `json:"git_root,omitempty"`
	ArchiveName   string                    `json:"archive_name,omitempty"`
	IncludedFiles int                       `json:"included_files"`
	IncludedBytes int64                     `json:"included_bytes"`
	ExcludedNote  string                    `json:"excluded_note,omitempty"`
}

type SessionPayloadDirectory struct {
	SourcePath    string `json:"source_path,omitempty"`
	TargetPath    string `json:"target_path,omitempty"`
	GitRoot       string `json:"git_root,omitempty"`
	ArchiveName   string `json:"archive_name,omitempty"`
	IncludedFiles int    `json:"included_files"`
	IncludedBytes int64  `json:"included_bytes"`
	ExcludedNote  string `json:"excluded_note,omitempty"`
}

type RemoteDiskInfo struct {
	Path           string `json:"path,omitempty"`
	AvailableBytes int64  `json:"available_bytes,omitempty"`
	RequiredBytes  int64  `json:"required_bytes,omitempty"`
}

type SessionPreflight struct {
	PathID                  string           `json:"path_id"`
	BuilderRuntime          string           `json:"builder_runtime,omitempty"`
	RemoteRuntime           string           `json:"remote_runtime,omitempty"`
	ImageDeliveryMode       string           `json:"image_delivery_mode,omitempty"`
	ImagePrefix             string           `json:"image_prefix,omitempty"`
	SSHReachable            bool             `json:"ssh_reachable"`
	SystemdAvailable        bool             `json:"systemd_available"`
	SystemdUnit             string           `json:"systemd_unit,omitempty"`
	RemoteRoot              string           `json:"remote_root,omitempty"`
	RemoteNetworkCandidates []string         `json:"remote_network_candidates,omitempty"`
	RemoteDisk              RemoteDiskInfo   `json:"remote_disk,omitempty"`
	FilesToCopy             []string         `json:"files_to_copy,omitempty"`
	Payloads                []SessionPayload `json:"payloads,omitempty"`
	Summary                 string           `json:"summary,omitempty"`
	Checks                  []string         `json:"checks,omitempty"`
}

type Session struct {
	ID                  string                   `json:"id"`
	Name                string                   `json:"name"`
	Status              string                   `json:"status"`
	SSHSessionTarget    string                   `json:"ssh_session_target,omitempty"`
	TransportMode       string                   `json:"transport_mode,omitempty"`
	MasterEndpoint      string                   `json:"master_endpoint,omitempty"`
	RemoteEndpoint      string                   `json:"remote_endpoint,omitempty"`
	RemoteAdvertiseHost string                   `json:"remote_advertise_host,omitempty"`
	GroupID             string                   `json:"group_id,omitempty"`
	GroupName           string                   `json:"group_name,omitempty"`
	BuilderRuntime      string                   `json:"builder_runtime,omitempty"`
	RemoteRuntime       string                   `json:"remote_runtime,omitempty"`
	ImageDeliveryMode   string                   `json:"image_delivery_mode,omitempty"`
	ImagePrefix         string                   `json:"image_prefix,omitempty"`
	MasterTailscaleURL  string                   `json:"master_tailscale_url,omitempty"`
	RemoteAuthURL       string                   `json:"remote_auth_url,omitempty"`
	RemoteTailnetURL    string                   `json:"remote_tailnet_url,omitempty"`
	ImageRef            string                   `json:"image_ref,omitempty"`
	ImageSignature      string                   `json:"image_signature,omitempty"`
	ImageArchiveBytes   int64                    `json:"image_archive_bytes,omitempty"`
	EnrollmentID        string                   `json:"enrollment_id,omitempty"`
	EnrollmentStatus    string                   `json:"enrollment_status,omitempty"`
	ChildSwarmID        string                   `json:"child_swarm_id,omitempty"`
	ChildName           string                   `json:"child_name,omitempty"`
	HostSwarmID         string                   `json:"host_swarm_id,omitempty"`
	HostName            string                   `json:"host_name,omitempty"`
	HostPublicKey       string                   `json:"host_public_key,omitempty"`
	HostFingerprint     string                   `json:"host_fingerprint,omitempty"`
	HostAPIBaseURL      string                   `json:"host_api_base_url,omitempty"`
	HostDesktopURL      string                   `json:"host_desktop_url,omitempty"`
	BypassPermissions   bool                     `json:"bypass_permissions,omitempty"`
	ContainerPackages   ContainerPackageManifest `json:"container_packages,omitempty"`
	LastError           string                   `json:"last_error,omitempty"`
	LastRemoteOutput    string                   `json:"last_remote_output,omitempty"`
	SyncEnabled         bool                     `json:"sync_enabled,omitempty"`
	SyncMode            string                   `json:"sync_mode,omitempty"`
	SyncOwnerSwarmID    string                   `json:"sync_owner_swarm_id,omitempty"`
	Preflight           SessionPreflight         `json:"preflight"`
	CreatedAt           int64                    `json:"created_at"`
	UpdatedAt           int64                    `json:"updated_at"`
	ApprovedAt          int64                    `json:"approved_at,omitempty"`
	AttachedAt          int64                    `json:"attached_at,omitempty"`
}

type Service struct {
	store       *pebblestore.RemoteDeploySessionStore
	swarms      *swarmruntime.Service
	swarmStore  *pebblestore.SwarmStore
	containers  *localcontainers.Service
	auth        *authruntime.Service
	workspace   *workspaceruntime.Service
	startupPath string
	startupCWD  string
}

type remoteRuntimeArtifact struct {
	Signature          string
	ImageRef           string
	ArchiveName        string
	ArchivePath        string
	ArchiveBytes       int64
	RequiredDiskBytes  int64
	ArchiveHit         bool
	RemoteImagePresent bool
}

type remotePairingTransport struct {
	Kind    string   `json:"kind,omitempty"`
	Primary string   `json:"primary,omitempty"`
	All     []string `json:"all,omitempty"`
}

type remotePairingRequest struct {
	InviteToken          string                   `json:"invite_token"`
	PrimarySwarmID       string                   `json:"primary_swarm_id"`
	PrimaryName          string                   `json:"primary_name,omitempty"`
	PrimaryEndpoint      string                   `json:"primary_endpoint"`
	TransportMode        string                   `json:"transport_mode,omitempty"`
	RendezvousTransports []remotePairingTransport `json:"rendezvous_transports,omitempty"`
}

type remotePairingResponse struct {
	OK           bool   `json:"ok"`
	ChildSwarmID string `json:"child_swarm_id"`
	ChildName    string `json:"child_name"`
	AuthCode     string `json:"auth_code"`
}

type remotePairingFinalizeRequest struct {
	PrimarySwarmID       string                   `json:"primary_swarm_id"`
	PrimaryName          string                   `json:"primary_name,omitempty"`
	PrimaryPublicKey     string                   `json:"primary_public_key,omitempty"`
	PrimaryFingerprint   string                   `json:"primary_fingerprint,omitempty"`
	TransportMode        string                   `json:"transport_mode,omitempty"`
	RendezvousTransports []remotePairingTransport `json:"rendezvous_transports,omitempty"`
}

func NewService(store *pebblestore.RemoteDeploySessionStore, swarms *swarmruntime.Service, swarmStore *pebblestore.SwarmStore, containers *localcontainers.Service, authSvc *authruntime.Service, workspaceSvc *workspaceruntime.Service, startupPath, startupCWD string) *Service {
	return &Service{
		store:       store,
		swarms:      swarms,
		swarmStore:  swarmStore,
		containers:  containers,
		auth:        authSvc,
		workspace:   workspaceSvc,
		startupPath: strings.TrimSpace(startupPath),
		startupCWD:  strings.TrimSpace(startupCWD),
	}
}

func remoteDeployImagePrefix() string {
	prefix := strings.TrimSpace(os.Getenv(remoteImagePrefixEnv))
	if prefix == "" {
		prefix = remoteImageNamePrefix
	}
	return strings.TrimRight(prefix, "/")
}

func normalizeRemoteImageDeliveryMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case remoteImageDeliveryArchive:
		return remoteImageDeliveryArchive
	case remoteImageDeliveryRegistry:
		return remoteImageDeliveryRegistry
	default:
		return remoteImageDeliveryArchive
	}
}

func resolveRemoteImagePrefix(mode string) (string, error) {
	switch normalizeRemoteImageDeliveryMode(mode) {
	case remoteImageDeliveryRegistry:
		prefix := strings.TrimRight(strings.TrimSpace(os.Getenv(remoteImagePrefixEnv)), "/")
		if prefix != "" && prefix != localcontainers.ProductionImagePrefix {
			return "", fmt.Errorf("Remote preflight failed on the master.\n\nWhat failed\n- Published remote image download now uses the verified GHCR app image %s, but %s is set to %s.\n\nWhat to do\n- Unset %s or set it to %s.\n- Then rerun preflight.", localcontainers.ProductionImagePrefix, remoteImagePrefixEnv, prefix, remoteImagePrefixEnv, localcontainers.ProductionImagePrefix)
		}
		return localcontainers.ProductionImagePrefix, nil
	default:
		return remoteImageNamePrefix, nil
	}
}

func remoteImageUsesArchive(imageRef string) bool {
	imageRef = strings.TrimSpace(imageRef)
	return imageRef == "" || strings.HasPrefix(imageRef, "localhost/")
}

func (s *Service) List(ctx context.Context) ([]Session, error) {
	return s.list(ctx, true)
}

func (s *Service) ListCached(ctx context.Context) ([]Session, error) {
	return s.list(ctx, false)
}

func (s *Service) list(ctx context.Context, refresh bool) ([]Session, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("remote deploy service is not configured")
	}
	records, err := s.store.List(500)
	if err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(records))
	for _, record := range records {
		if refresh {
			refreshed, _ := s.refreshRemoteSessionState(ctx, record)
			record = refreshed
		}
		if err := s.ensureWorkspaceReplicationLinks(record); err != nil {
			return nil, err
		}
		out = append(out, mapSession(record))
	}
	return out, nil
}

func (s *Service) Create(ctx context.Context, input CreateSessionInput) (Session, error) {
	if s == nil || s.store == nil || s.swarms == nil || s.swarmStore == nil {
		return Session{}, fmt.Errorf("remote deploy service is not configured")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Session{}, fmt.Errorf("name is required")
	}
	sshTarget := strings.TrimSpace(input.SSHSessionTarget)
	if sshTarget == "" {
		return Session{}, fmt.Errorf("ssh_session_target is required")
	}
	transportMode := normalizeRemoteTransportMode(input.TransportMode)
	imageDeliveryMode := normalizeRemoteImageDeliveryMode(input.ImageDeliveryMode)
	imagePrefix, err := resolveRemoteImagePrefix(imageDeliveryMode)
	if err != nil {
		return Session{}, err
	}
	stepStartedAt := time.Now()
	startupCfg, hostState, err := s.resolveBootstrapContext()
	logRemoteDeployTiming("create.resolve_bootstrap_context", stepStartedAt, err)
	if err != nil {
		return Session{}, err
	}
	stepStartedAt = time.Now()
	group, err := s.resolveTargetGroupForSession(hostState, strings.TrimSpace(input.GroupID), strings.TrimSpace(input.GroupName))
	logRemoteDeployTiming("create.resolve_target_group", stepStartedAt, err, "group_id", strings.TrimSpace(input.GroupID))
	if err != nil {
		return Session{}, err
	}
	sessionID := suggestedSessionID(name)
	if sessionID == "" {
		sessionID = "remote-child"
	}
	sessionID = sessionID + "-" + shortToken(4)
	stepStartedAt = time.Now()
	remoteRuntime, systemdAvailable, sudoMode, remoteHome, remoteNetworkCandidates, remoteAvailableBytes, err := s.inspectRemoteHost(ctx, sshTarget, input.RemoteRuntime)
	logRemoteDeployTiming("create.inspect_remote_host", stepStartedAt, err, "session_id", sessionID, "ssh_target", sshTarget, "systemd", strconv.FormatBool(systemdAvailable), "sudo_mode", sudoMode)
	if err != nil {
		return Session{}, formatCreatePreflightError(sshTarget, err)
	}
	remoteRoot := remoteRootForHome(remoteHome, sessionID)
	stepStartedAt = time.Now()
	if err := s.checkRemoteInstallCollision(ctx, sshTarget, remoteRoot); err != nil {
		logRemoteDeployTiming("create.check_remote_install_collision", stepStartedAt, err, "session_id", sessionID, "ssh_target", sshTarget)
		return Session{}, formatCreatePreflightError(sshTarget, err)
	}
	logRemoteDeployTiming("create.check_remote_install_collision", stepStartedAt, nil, "session_id", sessionID, "ssh_target", sshTarget)
	stepStartedAt = time.Now()
	masterEndpoint, err := resolveMasterRemoteDeployEndpoint(startupCfg, transportMode)
	logRemoteDeployTiming("create.resolve_master_endpoint", stepStartedAt, err, "session_id", sessionID, "transport_mode", transportMode)
	if err != nil {
		return Session{}, formatCreatePreflightError(sshTarget, err)
	}
	remoteAdvertiseHost, err := normalizeRemoteAdvertiseHost(firstNonEmpty(
		strings.TrimSpace(input.RemoteAdvertiseHost),
		firstRemoteNetworkCandidate(remoteNetworkCandidates),
		defaultReachableSSHHostCandidate(sshTarget),
	))
	if err != nil {
		return Session{}, formatCreatePreflightError(sshTarget, err)
	}
	if transportMode == startupconfig.NetworkModeLAN && remoteAdvertiseHost == "" {
		return Session{}, formatCreatePreflightError(sshTarget, fmt.Errorf("remote reachable host is required for LAN/WireGuard deploy"))
	}
	sessionToken, err := generateSecretToken(16)
	if err != nil {
		return Session{}, err
	}
	stepStartedAt = time.Now()
	builderRuntime, err := s.detectBuilderRuntime(ctx, remoteRuntime)
	logRemoteDeployTiming("create.detect_builder_runtime", stepStartedAt, err, "session_id", sessionID, "builder_runtime", builderRuntime, "requested_remote_runtime", remoteRuntime)
	if err != nil {
		return Session{}, formatCreatePreflightError(sshTarget, err)
	}
	stepStartedAt = time.Now()
	payloads, err := s.buildPayloads(input.Payloads)
	logRemoteDeployTiming(
		"create.build_payloads",
		stepStartedAt,
		err,
		"session_id",
		sessionID,
		"payloads",
		strconv.Itoa(len(payloads)),
		"payload_archives",
		strconv.Itoa(remotePayloadArchiveCount(payloads)),
		"payload_bytes",
		strconv.FormatInt(remotePayloadIncludedBytes(payloads), 10),
	)
	if err != nil {
		return Session{}, err
	}
	stepStartedAt = time.Now()
	remoteRequiredBytes, err := remotePreflightRequiredDiskBytes(ctx, imageDeliveryMode, payloads)
	logRemoteDeployTiming("create.calculate_remote_disk_required", stepStartedAt, err, "session_id", sessionID, "required_bytes", strconv.FormatInt(remoteRequiredBytes, 10))
	if err != nil {
		return Session{}, formatCreatePreflightError(sshTarget, err)
	}
	stepStartedAt = time.Now()
	checkedAvailableBytes, err := s.checkRemoteDiskCapacity(ctx, sshTarget, remoteRoot, remoteRequiredBytes)
	logRemoteDeployTiming("create.check_remote_disk_capacity", stepStartedAt, err, "session_id", sessionID, "ssh_target", sshTarget, "available_bytes", strconv.FormatInt(checkedAvailableBytes, 10), "required_bytes", strconv.FormatInt(remoteRequiredBytes, 10))
	if checkedAvailableBytes > 0 {
		remoteAvailableBytes = checkedAvailableBytes
	}
	if err != nil {
		return Session{}, formatCreatePreflightError(sshTarget, err)
	}
	filesToCopy := []string(nil)
	if remoteImageUsesArchive(remoteImageRef(imagePrefix, "preflight")) {
		filesToCopy = append(filesToCopy, filepath.ToSlash(filepath.Join("remote", remoteImageArchiveName(transportMode))))
	}
	for _, payload := range payloads {
		if payload.ArchiveName != "" {
			filesToCopy = append(filesToCopy, filepath.ToSlash(filepath.Join("remote", payload.ArchiveName)))
		}
		for _, directory := range payload.Directories {
			if directory.ArchiveName == "" {
				continue
			}
			filesToCopy = append(filesToCopy, filepath.ToSlash(filepath.Join("remote", directory.ArchiveName)))
		}
	}
	record := pebblestore.RemoteDeploySessionRecord{
		ID:                      sessionID,
		Name:                    name,
		Status:                  "preflight_ready",
		SSHSessionTarget:        sshTarget,
		TransportMode:           transportMode,
		MasterEndpoint:          masterEndpoint,
		RemoteAdvertiseHost:     remoteAdvertiseHost,
		GroupID:                 group.ID,
		GroupName:               firstNonEmpty(group.Name, input.GroupName, group.ID),
		BuilderRuntime:          builderRuntime,
		RemoteRuntime:           remoteRuntime,
		ImageDeliveryMode:       imageDeliveryMode,
		ImagePrefix:             imagePrefix,
		RemoteRoot:              remoteRoot,
		MasterTailscaleURL:      firstNonEmpty(map[bool]string{true: masterEndpoint}[transportMode == startupconfig.NetworkModeTailscale]),
		MasterSwarmID:           strings.TrimSpace(hostState.Node.SwarmID),
		SyncEnabled:             input.SyncEnabled,
		SyncMode:                firstNonEmpty(map[bool]string{true: "managed"}[input.SyncEnabled]),
		SyncOwnerSwarmID:        firstNonEmpty(map[bool]string{true: strings.TrimSpace(hostState.Node.SwarmID)}[input.SyncEnabled]),
		BypassPermissions:       input.BypassPermissions,
		ContainerPackages:       mapRemoteContainerPackageManifest(input.ContainerPackages),
		SyncCredentialURL:       firstNonEmpty(map[bool]string{true: buildRemoteSyncCredentialURL(masterEndpoint, sessionID)}[input.SyncEnabled]),
		SessionToken:            sessionToken,
		SSHReachable:            true,
		SystemdAvailable:        systemdAvailable,
		SudoMode:                sudoMode,
		RemoteNetworkCandidates: remoteNetworkCandidates,
		RemoteDisk: pebblestore.RemoteDeployDiskRecord{
			Path:           remoteRoot,
			AvailableBytes: remoteAvailableBytes,
			RequiredBytes:  remoteRequiredBytes,
		},
		FilesToCopy: filesToCopy,
		Payloads:    payloads,
	}
	stepStartedAt = time.Now()
	saved, err := s.store.Put(record)
	logRemoteDeployTiming("create.store_put", stepStartedAt, err, "session_id", sessionID)
	if err != nil {
		return Session{}, err
	}
	return mapSession(saved), nil
}

func (s *Service) Delete(ctx context.Context, input DeleteSessionInput) (localcontainers.DeleteResult, error) {
	if s == nil || s.store == nil {
		return localcontainers.DeleteResult{}, fmt.Errorf("remote deploy service is not configured")
	}
	sessionIDs := normalizeRemoteSessionDeleteIDs(input.SessionIDs)
	childSwarmIDs := normalizeRemoteSessionDeleteIDs(input.ChildSwarmIDs)
	if len(sessionIDs) == 0 && len(childSwarmIDs) == 0 {
		return localcontainers.DeleteResult{}, errors.New("at least one remote session id or child swarm id is required")
	}

	records, err := s.store.List(500)
	if err != nil {
		return localcontainers.DeleteResult{}, err
	}
	recordsByID := make(map[string]pebblestore.RemoteDeploySessionRecord, len(records))
	recordsByChildSwarmID := make(map[string][]pebblestore.RemoteDeploySessionRecord)
	for _, record := range records {
		recordsByID[record.ID] = record
		childSwarmID := strings.TrimSpace(record.ChildSwarmID)
		if childSwarmID != "" {
			recordsByChildSwarmID[childSwarmID] = append(recordsByChildSwarmID[childSwarmID], record)
		}
	}

	items := make([]localcontainers.DeleteItemResult, 0, len(sessionIDs)+len(childSwarmIDs))
	deletedSessions := make(map[string]struct{}, len(sessionIDs))

	for _, sessionID := range sessionIDs {
		record, ok := recordsByID[sessionID]
		if !ok {
			items = append(items, localcontainers.DeleteItemResult{
				ID:    sessionID,
				Error: "remote deploy session not found",
			})
			continue
		}
		item := localcontainers.DeleteItemResult{
			ID:               record.ID,
			Name:             firstNonEmpty(record.ChildName, record.Name, record.ID),
			ContainerName:    record.SSHSessionTarget,
			ChildSwarmID:     strings.TrimSpace(record.ChildSwarmID),
			ChildDisplayName: firstNonEmpty(record.ChildName, record.Name, record.ChildSwarmID),
		}
		if item.ChildSwarmID != "" || item.ChildDisplayName != "" {
			item.ChildInfoDetected = true
		}
		if input.TeardownRemote {
			if err := s.teardownRemoteChildInstall(ctx, record); err != nil {
				item.Error = err.Error()
				items = append(items, item)
				continue
			}
		}
		if err := s.store.Delete(record.ID); err != nil {
			item.Error = err.Error()
			items = append(items, item)
			continue
		}
		deletedSessions[record.ID] = struct{}{}
		item.Deleted = true
		item.RemovedDeployment = true
		if err := s.cleanupRemoteChildState(strings.TrimSpace(record.ChildSwarmID), &item); err != nil {
			item.Error = err.Error()
		}
		items = append(items, item)
	}

	for _, childSwarmID := range childSwarmIDs {
		item := localcontainers.DeleteItemResult{
			ID:                childSwarmID,
			Name:              childSwarmID,
			ChildSwarmID:      childSwarmID,
			ChildDisplayName:  childSwarmID,
			ChildInfoDetected: true,
		}
		matchedRecords := recordsByChildSwarmID[childSwarmID]
		if input.TeardownRemote && len(matchedRecords) == 0 {
			item.Error = "remote deploy session not found for SSH teardown"
			items = append(items, item)
			continue
		}
		for _, record := range matchedRecords {
			if _, ok := deletedSessions[record.ID]; ok {
				continue
			}
			if input.TeardownRemote {
				if err := s.teardownRemoteChildInstall(ctx, record); err != nil {
					item.Error = err.Error()
					break
				}
			}
			if err := s.store.Delete(record.ID); err != nil {
				item.Error = err.Error()
				break
			}
			deletedSessions[record.ID] = struct{}{}
			item.RemovedDeployment = true
			if item.Name == childSwarmID {
				item.Name = firstNonEmpty(record.ChildName, record.Name, childSwarmID)
				item.ChildDisplayName = firstNonEmpty(record.ChildName, record.Name, childSwarmID)
				item.ContainerName = record.SSHSessionTarget
			}
		}
		if item.Error == "" {
			if err := s.cleanupRemoteChildState(childSwarmID, &item); err != nil {
				item.Error = err.Error()
			}
		}
		if item.Error == "" {
			item.Deleted = item.RemovedDeployment || item.RemovedTrustedPeer || item.RemovedGroupMemberships > 0
			if !item.Deleted {
				item.Error = "remote child state not found"
			}
		}
		items = append(items, item)
	}

	result := localcontainers.DeleteResult{
		Deleted: make([]string, 0, len(items)),
		Items:   make([]localcontainers.DeleteItemResult, 0, len(items)),
	}
	for _, item := range items {
		result.Items = append(result.Items, item)
		if item.Deleted {
			result.Deleted = append(result.Deleted, item.ID)
			result.Count++
		}
		if item.Error != "" {
			result.Failed++
		}
		if item.RemovedDeployment || item.RemovedTrustedPeer || item.RemovedGroupMemberships > 0 {
			result.ChildInfoRemoved++
		}
	}
	if result.Failed > 0 {
		return result, fmt.Errorf("failed to delete %d remote swarm record(s)", result.Failed)
	}
	return result, nil
}

func (s *Service) Start(ctx context.Context, input StartSessionInput) (Session, error) {
	if s == nil || s.store == nil || s.swarms == nil || s.swarmStore == nil {
		return Session{}, fmt.Errorf("remote deploy service is not configured")
	}
	record, ok, err := s.store.Get(input.SessionID)
	if err != nil {
		return Session{}, err
	}
	if !ok {
		return Session{}, fmt.Errorf("remote deploy session not found")
	}
	startupCfg, hostState, err := s.resolveBootstrapContext()
	if err != nil {
		return Session{}, err
	}
	if record.SyncEnabled {
		stepStartedAt := time.Now()
		if err := s.requireManagedSyncVaultPassword(strings.TrimSpace(input.SyncVaultPassword)); err != nil {
			logRemoteDeployTiming("start.require_sync_vault_password", stepStartedAt, err, "session_id", strings.TrimSpace(input.SessionID))
			return Session{}, err
		}
		logRemoteDeployTiming("start.require_sync_vault_password", stepStartedAt, nil, "session_id", strings.TrimSpace(input.SessionID))
		if strings.TrimSpace(record.SyncBundlePassword) == "" {
			bundlePassword, err := generateSecretToken(32)
			if err != nil {
				return Session{}, err
			}
			record.SyncBundlePassword = bundlePassword
		}
		if s.auth == nil {
			return Session{}, fmt.Errorf("auth service is not configured")
		}
		stepStartedAt = time.Now()
		_, exported, err := s.auth.ExportCredentials(record.SyncBundlePassword, strings.TrimSpace(input.SyncVaultPassword))
		logRemoteDeployTiming("start.export_sync_credentials", stepStartedAt, err, "session_id", strings.TrimSpace(input.SessionID), "exported", strconv.Itoa(exported))
		if err != nil {
			return Session{}, err
		}
		record.SyncBundleExportCount = exported
		record.SyncBundleExportedAt = time.Now().UnixMilli()
		record.SyncMode = firstNonEmpty(record.SyncMode, "managed")
		record.SyncOwnerSwarmID = firstNonEmpty(record.SyncOwnerSwarmID, strings.TrimSpace(hostState.Node.SwarmID))
		record.SyncCredentialURL = firstNonEmpty(record.SyncCredentialURL, buildRemoteSyncCredentialURL(strings.TrimSpace(record.MasterEndpoint), record.ID))
	}
	record.TransportMode = normalizeRemoteTransportMode(record.TransportMode)
	stepStartedAt := time.Now()
	invite, err := s.swarms.CreateInvite(swarmruntime.CreateInviteInput{
		PrimarySwarmID:       strings.TrimSpace(hostState.Node.SwarmID),
		PrimaryName:          firstNonEmpty(strings.TrimSpace(startupCfg.SwarmName), hostState.Node.Name, "Primary"),
		GroupID:              strings.TrimSpace(record.GroupID),
		TransportMode:        record.TransportMode,
		RendezvousTransports: []swarmruntime.TransportSummary{{Kind: record.TransportMode, Primary: strings.TrimSpace(record.MasterEndpoint), All: []string{strings.TrimSpace(record.MasterEndpoint)}}},
		TTL:                  30 * time.Minute,
	})
	logRemoteDeployTiming("start.create_invite", stepStartedAt, err, "session_id", record.ID, "transport_mode", record.TransportMode)
	if err != nil {
		return Session{}, err
	}
	record.InviteToken = invite.Token
	childCfgText := s.renderChildStartupConfig(record, startupCfg, hostState)
	workDir, err := os.MkdirTemp("", "swarm-remote-deploy-")
	if err != nil {
		return Session{}, err
	}
	defer os.RemoveAll(workDir)
	stepStartedAt = time.Now()
	runtimeArtifact, err := s.prepareRemoteRuntimeArtifact(ctx, record.BuilderRuntime, record.TransportMode, record.ImagePrefix, mapRemoteStoredContainerPackageManifest(record.ContainerPackages))
	logRemoteDeployTiming(
		"start.prepare_runtime_artifact",
		stepStartedAt,
		err,
		"session_id",
		record.ID,
		"archive_hit",
		strconv.FormatBool(runtimeArtifact.ArchiveHit),
		"archive_bytes",
		strconv.FormatInt(runtimeArtifact.ArchiveBytes, 10),
		"image_ref",
		runtimeArtifact.ImageRef,
		"signature",
		runtimeArtifact.Signature,
	)
	if err != nil {
		record.Status = "failed"
		record.LastError = err.Error()
		saved, saveErr := s.store.Put(record)
		if saveErr != nil {
			return Session{}, saveErr
		}
		return mapSession(saved), err
	}
	record.ImageRef = runtimeArtifact.ImageRef
	record.ImageSignature = runtimeArtifact.Signature
	record.ImageArchiveBytes = runtimeArtifact.ArchiveBytes
	record.RemoteDisk.RequiredBytes = remoteRequiredDiskBytes(runtimeArtifact.RequiredDiskBytes, record.Payloads)
	stepStartedAt = time.Now()
	remoteImagePresent, err := remoteImageExists(ctx, record.SSHSessionTarget, record.RemoteRuntime, runtimeArtifact.ImageRef, record.SudoMode)
	logRemoteDeployTiming("start.check_remote_image", stepStartedAt, err, "session_id", record.ID, "image_ref", runtimeArtifact.ImageRef, "remote_image_present", strconv.FormatBool(remoteImagePresent))
	if err != nil {
		record.Status = "failed"
		record.LastError = err.Error()
		saved, saveErr := s.store.Put(record)
		if saveErr != nil {
			return Session{}, saveErr
		}
		return mapSession(saved), err
	}
	runtimeArtifact.RemoteImagePresent = remoteImagePresent
	if remoteImagePresent {
		record.RemoteDisk.RequiredBytes = remoteRequiredDiskBytes(0, record.Payloads)
	}
	stepStartedAt = time.Now()
	availableBytes, err := s.checkRemoteDiskCapacity(ctx, record.SSHSessionTarget, record.RemoteRoot, record.RemoteDisk.RequiredBytes)
	logRemoteDeployTiming("start.check_remote_disk_capacity", stepStartedAt, err, "session_id", record.ID, "available_bytes", strconv.FormatInt(availableBytes, 10), "required_bytes", strconv.FormatInt(record.RemoteDisk.RequiredBytes, 10))
	if availableBytes > 0 {
		record.RemoteDisk.AvailableBytes = availableBytes
	}
	if err != nil {
		record.Status = "failed"
		record.LastError = err.Error()
		saved, saveErr := s.store.Put(record)
		if saveErr != nil {
			return Session{}, saveErr
		}
		return mapSession(saved), err
	}
	stepStartedAt = time.Now()
	if err := s.prepareRemoteBundle(ctx, workDir, &record); err != nil {
		logRemoteDeployTiming("start.prepare_remote_bundle", stepStartedAt, err, "session_id", record.ID, "payload_archives", strconv.Itoa(remotePayloadArchiveCount(record.Payloads)))
		record.Status = "failed"
		record.LastError = err.Error()
		saved, saveErr := s.store.Put(record)
		if saveErr != nil {
			return Session{}, saveErr
		}
		return mapSession(saved), err
	}
	logRemoteDeployTiming("start.prepare_remote_bundle", stepStartedAt, nil, "session_id", record.ID, "payload_archives", strconv.Itoa(remotePayloadArchiveCount(record.Payloads)))
	stepStartedAt = time.Now()
	if err := s.copyRemoteBundle(ctx, workDir, runtimeArtifact, &record); err != nil {
		logRemoteDeployTiming("start.copy_remote_bundle", stepStartedAt, err, "session_id", record.ID, "ssh_target", record.SSHSessionTarget, "image_archive_bytes", strconv.FormatInt(runtimeArtifact.ArchiveBytes, 10))
		record.Status = "failed"
		record.LastError = err.Error()
		saved, saveErr := s.store.Put(record)
		if saveErr != nil {
			return Session{}, saveErr
		}
		return mapSession(saved), err
	}
	logRemoteDeployTiming("start.copy_remote_bundle", stepStartedAt, nil, "session_id", record.ID, "ssh_target", record.SSHSessionTarget, "image_archive_bytes", strconv.FormatInt(runtimeArtifact.ArchiveBytes, 10))
	stepStartedAt = time.Now()
	output, authURL, remoteEndpoint, err := s.startRemoteBundle(ctx, &record, childCfgText, strings.TrimSpace(input.TailscaleAuthKey), strings.TrimSpace(input.SyncVaultPassword))
	logRemoteDeployTiming("start.start_remote_bundle", stepStartedAt, err, "session_id", record.ID, "ssh_target", record.SSHSessionTarget, "auth_required", strconv.FormatBool(strings.TrimSpace(authURL) != ""), "remote_endpoint_present", strconv.FormatBool(strings.TrimSpace(remoteEndpoint) != ""))
	record.InviteToken = invite.Token
	record.LastRemoteOutput = strings.TrimSpace(output)
	record.RemoteAuthURL = strings.TrimSpace(authURL)
	record.RemoteEndpoint = strings.TrimSpace(remoteEndpoint)
	record.RemoteTailnetURL = firstNonEmpty(map[bool]string{true: strings.TrimSpace(remoteEndpoint)}[strings.EqualFold(strings.TrimSpace(record.TransportMode), startupconfig.NetworkModeTailscale)])
	if err != nil {
		record.Status = "failed"
		record.LastError = err.Error()
		saved, saveErr := s.store.Put(record)
		if saveErr != nil {
			return Session{}, saveErr
		}
		return mapSession(saved), err
	}
	record.Status = "waiting_for_approval"
	record.EnrollmentStatus = "pending"
	record.LastError = ""
	stepStartedAt = time.Now()
	saved, err := s.store.Put(record)
	logRemoteDeployTiming("start.store_waiting_for_approval", stepStartedAt, err, "session_id", record.ID)
	if err != nil {
		return Session{}, err
	}
	return mapSession(saved), nil
}

type SyncCredentialRequestInput struct {
	SessionID     string
	SessionToken  string
	VaultPassword string
}

func (s *Service) RunUpdateJob(ctx context.Context, input UpdateJobInput) (UpdateJobResult, error) {
	startedAt := time.Now().UnixMilli()
	result := UpdateJobResult{PathID: PathSessionUpdateJob, Mode: "dev", DevMode: true, Items: []UpdateJobItem{}, StartedAtUnix: startedAt, UpdatedAtUnix: startedAt}
	if s == nil || s.store == nil {
		return result, fmt.Errorf("remote deploy service is not configured")
	}
	devMode := true
	if input.DevMode != nil {
		devMode = *input.DevMode
	}
	result.DevMode = devMode
	if !devMode {
		result.Mode = "release"
		return result, fmt.Errorf("remote SSH dev update only supports dev mode")
	}
	records, err := s.store.List(500)
	if err != nil {
		return result, err
	}
	var cachedArtifacts map[string]remoteRuntimeArtifact
	for _, record := range records {
		if !remoteUpdateRecordEligible(record) {
			continue
		}
		active, activeErr := remoteSessionContainerActive(ctx, record)
		if activeErr != nil {
			item := remoteUpdateItemForRecord(record)
			item.State = "failed"
			item.Reason = "active_check_failed"
			item.Error = activeErr.Error()
			result.addUpdateJobItem(item)
			continue
		}
		if !active {
			continue
		}
		item := remoteUpdateItemForRecord(record)
		cacheKey := remoteRuntimeArtifactCacheKey(record)
		if cachedArtifacts == nil {
			cachedArtifacts = map[string]remoteRuntimeArtifact{}
		}
		artifact, cached := cachedArtifacts[cacheKey]
		var prepareErr error
		if !cached {
			artifact, prepareErr = s.prepareRemoteRuntimeArtifact(ctx, record.BuilderRuntime, record.TransportMode, record.ImagePrefix, mapRemoteStoredContainerPackageManifest(record.ContainerPackages))
			if prepareErr == nil {
				cachedArtifacts[cacheKey] = artifact
			}
		}
		if prepareErr != nil {
			item.State = "failed"
			item.Reason = "prepare_runtime_artifact_failed"
			item.Error = prepareErr.Error()
			result.addUpdateJobItem(item)
			continue
		}
		item.TargetImageRef = artifact.ImageRef
		item.ImageSignature = artifact.Signature
		remoteImagePresent, imageErr := remoteImageExists(ctx, record.SSHSessionTarget, record.RemoteRuntime, artifact.ImageRef, record.SudoMode)
		if imageErr != nil {
			item.State = "failed"
			item.Reason = "remote_image_check_failed"
			item.Error = imageErr.Error()
			result.addUpdateJobItem(item)
			continue
		}
		artifact.RemoteImagePresent = remoteImagePresent
		if remoteImagePresent {
			record.RemoteDisk.RequiredBytes = remoteRequiredDiskBytes(0, nil)
		} else if artifact.RequiredDiskBytes > 0 {
			requiredBytes := remoteRequiredDiskBytes(artifact.RequiredDiskBytes, nil)
			availableBytes, diskErr := s.checkRemoteDiskCapacity(ctx, record.SSHSessionTarget, record.RemoteRoot, requiredBytes)
			if availableBytes > 0 {
				record.RemoteDisk.AvailableBytes = availableBytes
			}
			record.RemoteDisk.RequiredBytes = requiredBytes
			if diskErr != nil {
				item.State = "failed"
				item.Reason = "remote_disk_capacity_failed"
				item.Error = diskErr.Error()
				result.addUpdateJobItem(item)
				continue
			}
		}
		if copyErr := copyRemoteRuntimeArtifact(ctx, artifact, record); copyErr != nil {
			item.State = "failed"
			item.Reason = "copy_remote_image_failed"
			item.Error = copyErr.Error()
			result.addUpdateJobItem(item)
			continue
		}
		replaceResult, replaceErr := runRemoteDevReplacement(ctx, record, artifact)
		item.State = firstNonEmpty(replaceResult.State, "replaced")
		item.Reason = replaceResult.Reason
		if replaceResult.PreviousImageRef != "" {
			item.PreviousImageRef = replaceResult.PreviousImageRef
		}
		if replaceErr != nil {
			item.State = "failed"
			item.Error = firstNonEmpty(replaceResult.Error, replaceErr.Error())
			record.LastError = item.Error
			if saved, saveErr := s.store.Put(record); saveErr == nil {
				record = saved
			}
			result.addUpdateJobItem(item)
			continue
		}
		if item.State == "" {
			item.State = "replaced"
		}
		if item.State == "replaced" || item.State == "skipped" {
			record.ImageRef = artifact.ImageRef
			record.ImageSignature = artifact.Signature
			record.ImageArchiveBytes = artifact.ArchiveBytes
			record.Status = "attached"
			record.LastError = ""
			if replaceResult.RemoteEndpoint != "" {
				record.RemoteEndpoint = replaceResult.RemoteEndpoint
				if strings.EqualFold(strings.TrimSpace(record.TransportMode), startupconfig.NetworkModeTailscale) {
					record.RemoteTailnetURL = replaceResult.RemoteEndpoint
				}
			}
			if saved, saveErr := s.store.Put(record); saveErr != nil {
				item.State = "failed"
				item.Reason = "store_update_failed"
				item.Error = saveErr.Error()
			} else {
				record = saved
			}
		}
		result.addUpdateJobItem(item)
	}
	result.CompletedAtUnix = time.Now().UnixMilli()
	result.UpdatedAtUnix = result.CompletedAtUnix
	if result.Summary.Failed > 0 {
		return result, fmt.Errorf("remote SSH dev update failed for %d session(s)", result.Summary.Failed)
	}
	if result.Summary.Total == 0 {
		return result, nil
	}
	return result, nil
}

func remoteUpdateRecordEligible(record pebblestore.RemoteDeploySessionRecord) bool {
	return strings.EqualFold(strings.TrimSpace(record.Status), "attached") && strings.TrimSpace(record.SSHSessionTarget) != "" && strings.TrimSpace(record.RemoteRoot) != ""
}

func remoteRuntimeArtifactCacheKey(record pebblestore.RemoteDeploySessionRecord) string {
	manifest := mapRemoteStoredContainerPackageManifest(record.ContainerPackages)
	return strings.Join([]string{
		normalizeRemoteDeployRuntime(record.BuilderRuntime),
		normalizeRemoteTransportMode(record.TransportMode),
		strings.TrimRight(firstNonEmpty(strings.TrimSpace(record.ImagePrefix), remoteDeployImagePrefix()), "/"),
		remoteContainerPackageSignaturePayload(manifest),
	}, "\x00")
}

func remoteUpdateItemForRecord(record pebblestore.RemoteDeploySessionRecord) UpdateJobItem {
	return UpdateJobItem{ID: record.ID, Name: record.Name, SSHSessionTarget: record.SSHSessionTarget, Status: record.Status, PreviousImageRef: record.ImageRef}
}

func (r *UpdateJobResult) addUpdateJobItem(item UpdateJobItem) {
	if r == nil {
		return
	}
	if item.State == "" {
		item.State = "skipped"
	}
	r.Items = append(r.Items, item)
	r.Summary.Total++
	switch item.State {
	case "replaced":
		r.Summary.Replaced++
	case "failed":
		r.Summary.Failed++
	case "skipped":
		r.Summary.Skipped++
		if strings.EqualFold(strings.TrimSpace(item.Reason), "already-current") {
			r.Summary.AlreadyCurrent++
		}
	default:
		r.Summary.Unknown++
		r.Summary.Skipped++
	}
	r.UpdatedAtUnix = time.Now().UnixMilli()
}

func (s *Service) SyncCredentialBundle(ctx context.Context, input SyncCredentialRequestInput) (deployruntime.ContainerSyncCredentialBundle, error) {
	if s == nil || s.store == nil || s.auth == nil {
		return deployruntime.ContainerSyncCredentialBundle{}, fmt.Errorf("remote deploy service is not configured")
	}
	record, ok, err := s.store.Get(input.SessionID)
	if err != nil {
		return deployruntime.ContainerSyncCredentialBundle{}, err
	}
	if !ok {
		return deployruntime.ContainerSyncCredentialBundle{}, fmt.Errorf("remote deploy session not found")
	}
	if strings.TrimSpace(record.SessionToken) == "" || strings.TrimSpace(record.SessionToken) != strings.TrimSpace(input.SessionToken) {
		return deployruntime.ContainerSyncCredentialBundle{}, fmt.Errorf("remote deploy session token mismatch")
	}
	if !record.SyncEnabled {
		return deployruntime.ContainerSyncCredentialBundle{}, fmt.Errorf("swarm sync is not enabled for this remote deploy session")
	}
	if strings.TrimSpace(record.SyncOwnerSwarmID) == "" {
		return deployruntime.ContainerSyncCredentialBundle{}, fmt.Errorf("sync owner swarm id is not configured")
	}
	if strings.TrimSpace(record.SyncBundlePassword) == "" {
		return deployruntime.ContainerSyncCredentialBundle{}, fmt.Errorf("sync bundle password is not configured")
	}
	payload, exported, err := s.auth.ExportCredentials(record.SyncBundlePassword, strings.TrimSpace(input.VaultPassword))
	if err != nil {
		return deployruntime.ContainerSyncCredentialBundle{}, err
	}
	record.SyncBundleExportCount = exported
	record.SyncBundleExportedAt = time.Now().UnixMilli()
	if saved, saveErr := s.store.Put(record); saveErr == nil {
		record = saved
	}
	_ = ctx
	return deployruntime.ContainerSyncCredentialBundle{
		OwnerSwarmID:   record.SyncOwnerSwarmID,
		BundlePassword: record.SyncBundlePassword,
		Bundle:         payload,
	}, nil
}

func (s *Service) Approve(ctx context.Context, input ApproveSessionInput) (Session, error) {
	if s == nil || s.store == nil || s.swarms == nil {
		return Session{}, fmt.Errorf("remote deploy service is not configured")
	}
	record, ok, err := s.store.Get(input.SessionID)
	if err != nil {
		return Session{}, err
	}
	if !ok {
		return Session{}, fmt.Errorf("remote deploy session not found")
	}
	if strings.TrimSpace(record.InviteToken) == "" {
		return Session{}, fmt.Errorf("remote deploy session has not started")
	}
	if refreshed, err := s.refreshRemoteSessionState(ctx, record); err == nil {
		record = refreshed
	}
	if strings.TrimSpace(record.EnrollmentID) == "" {
		return Session{}, fmt.Errorf("remote child has not enrolled yet")
	}
	startupCfg, hostState, err := s.resolveBootstrapContext()
	if err != nil {
		return Session{}, err
	}
	enrollment, _, err := s.swarms.DecideEnrollment(swarmruntime.DecideEnrollmentInput{EnrollmentID: record.EnrollmentID, Approve: true})
	if err != nil {
		return Session{}, err
	}
	record.EnrollmentStatus = strings.TrimSpace(enrollment.Status)
	record.ApprovedAt = time.Now().UnixMilli()
	record.Status = "approved"
	record.ChildSwarmID = strings.TrimSpace(enrollment.ChildSwarmID)
	record.ChildName = strings.TrimSpace(enrollment.ChildName)
	record.HostSwarmID = strings.TrimSpace(hostState.Node.SwarmID)
	record.HostName = firstNonEmpty(hostState.Node.Name, strings.TrimSpace(startupCfg.SwarmName), "Primary")
	record.HostPublicKey = strings.TrimSpace(hostState.Node.PublicKey)
	record.HostFingerprint = strings.TrimSpace(hostState.Node.Fingerprint)
	record.HostAPIBaseURL = strings.TrimSpace(record.MasterEndpoint)
	record.HostDesktopURL = strings.TrimSpace(record.MasterEndpoint)
	record.AttachedAt = time.Now().UnixMilli()
	record.Status = "approved"
	record.LastError = ""
	if err := s.applyApprovedRemotePeerAuth(record); err != nil {
		record.Status = "failed"
		record.LastError = err.Error()
		saved, saveErr := s.store.Put(record)
		if saveErr != nil {
			return Session{}, saveErr
		}
		return mapSession(saved), err
	}
	if err := s.finalizeApprovedRemotePairing(ctx, record, hostState); err != nil {
		record.Status = "failed"
		record.LastError = err.Error()
		saved, saveErr := s.store.Put(record)
		if saveErr != nil {
			return Session{}, saveErr
		}
		return mapSession(saved), err
	}
	record.Status = "attached"
	if err := s.ensureWorkspaceReplicationLinks(record); err != nil {
		record.Status = "failed"
		record.LastError = err.Error()
		saved, saveErr := s.store.Put(record)
		if saveErr != nil {
			return Session{}, saveErr
		}
		return mapSession(saved), err
	}
	saved, err := s.store.Put(record)
	if err != nil {
		return Session{}, err
	}
	_ = ctx
	return mapSession(saved), nil
}

func (s *Service) ensureWorkspaceReplicationLinks(record pebblestore.RemoteDeploySessionRecord) error {
	if s == nil || s.workspace == nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(record.Status), "attached") {
		return nil
	}
	childSwarmID := strings.TrimSpace(record.ChildSwarmID)
	if childSwarmID == "" {
		return nil
	}
	knownEntries, err := s.workspace.ListKnown(100000)
	if err != nil {
		return err
	}
	knownByPath := make(map[string]workspaceruntime.Entry, len(knownEntries))
	for _, entry := range knownEntries {
		knownByPath[strings.TrimSpace(entry.Path)] = entry
	}
	childSwarmName := firstNonEmpty(strings.TrimSpace(record.ChildName), strings.TrimSpace(record.Name), childSwarmID)
	syncMode := strings.TrimSpace(record.SyncMode)
	if record.SyncEnabled && syncMode == "" {
		syncMode = workspaceruntime.ReplicationSyncModeManaged
	}
	for idx, payload := range record.Payloads {
		workspacePath := firstNonEmpty(strings.TrimSpace(payload.WorkspacePath), strings.TrimSpace(payload.SourcePath))
		if workspacePath == "" {
			continue
		}
		entry, ok := knownByPath[workspacePath]
		if !ok {
			workspaceName := firstNonEmpty(strings.TrimSpace(payload.WorkspaceName), filepath.Base(workspacePath))
			resolution, err := s.workspace.Add(workspacePath, workspaceName, "", false)
			if err != nil {
				return err
			}
			workspacePath = strings.TrimSpace(resolution.WorkspacePath)
			knownEntries, err = s.workspace.ListKnown(100000)
			if err != nil {
				return err
			}
			knownByPath = make(map[string]workspaceruntime.Entry, len(knownEntries))
			for _, refreshed := range knownEntries {
				knownByPath[strings.TrimSpace(refreshed.Path)] = refreshed
			}
			entry = knownByPath[workspacePath]
		} else {
			workspacePath = strings.TrimSpace(entry.Path)
		}
		linkID := strings.TrimSpace(payload.ID)
		if linkID == "" {
			linkID = fmt.Sprintf("payload-%02d", idx+1)
		}
		linkID = fmt.Sprintf("remote:%s:%s", strings.TrimSpace(record.ID), linkID)
		writable := !strings.EqualFold(strings.TrimSpace(payload.Mode), "ro")
		targetWorkspacePath := firstNonEmpty(strings.TrimSpace(payload.TargetPath), "/workspaces")
		desired := pebblestore.WorkspaceReplicationLink{
			ID:                  linkID,
			TargetKind:          workspaceruntime.ReplicationTargetModeRemote,
			TargetSwarmID:       childSwarmID,
			TargetSwarmName:     childSwarmName,
			TargetWorkspacePath: targetWorkspacePath,
			ReplicationMode:     workspaceruntime.ReplicationModeBundle,
			Writable:            writable,
			Sync: pebblestore.WorkspaceReplicationSync{
				Enabled: record.SyncEnabled,
				Mode:    syncMode,
			},
		}
		if existing, ok := findWorkspaceReplicationLink(entry.ReplicationLinks, linkID); ok && workspaceReplicationLinksEqual(existing, desired) {
			continue
		}
		if _, err := s.workspace.AddReplicationLink(workspacePath, desired); err != nil {
			return err
		}
		entry.ReplicationLinks = upsertWorkspaceReplicationLink(entry.ReplicationLinks, desired)
		knownByPath[workspacePath] = entry
	}
	return nil
}

func findWorkspaceReplicationLink(links []pebblestore.WorkspaceReplicationLink, linkID string) (pebblestore.WorkspaceReplicationLink, bool) {
	for _, link := range links {
		if strings.EqualFold(strings.TrimSpace(link.ID), strings.TrimSpace(linkID)) {
			return link, true
		}
	}
	return pebblestore.WorkspaceReplicationLink{}, false
}

func workspaceReplicationLinksEqual(left, right pebblestore.WorkspaceReplicationLink) bool {
	return strings.EqualFold(strings.TrimSpace(left.ID), strings.TrimSpace(right.ID)) &&
		strings.EqualFold(strings.TrimSpace(left.TargetKind), strings.TrimSpace(right.TargetKind)) &&
		strings.EqualFold(strings.TrimSpace(left.TargetSwarmID), strings.TrimSpace(right.TargetSwarmID)) &&
		strings.TrimSpace(left.TargetSwarmName) == strings.TrimSpace(right.TargetSwarmName) &&
		strings.TrimSpace(left.TargetWorkspacePath) == strings.TrimSpace(right.TargetWorkspacePath) &&
		strings.EqualFold(strings.TrimSpace(left.ReplicationMode), strings.TrimSpace(right.ReplicationMode)) &&
		left.Writable == right.Writable &&
		left.Sync.Enabled == right.Sync.Enabled &&
		strings.EqualFold(strings.TrimSpace(left.Sync.Mode), strings.TrimSpace(right.Sync.Mode))
}

func upsertWorkspaceReplicationLink(links []pebblestore.WorkspaceReplicationLink, desired pebblestore.WorkspaceReplicationLink) []pebblestore.WorkspaceReplicationLink {
	out := make([]pebblestore.WorkspaceReplicationLink, 0, len(links)+1)
	replaced := false
	for _, link := range links {
		if strings.EqualFold(strings.TrimSpace(link.ID), strings.TrimSpace(desired.ID)) {
			out = append(out, desired)
			replaced = true
			continue
		}
		out = append(out, link)
	}
	if !replaced {
		out = append(out, desired)
	}
	return out
}

func (s *Service) finalizeApprovedRemotePairing(ctx context.Context, record pebblestore.RemoteDeploySessionRecord, hostState swarmruntime.LocalState) error {
	remoteEndpoint := remoteSessionEndpoint(record)
	if remoteEndpoint == "" {
		return fmt.Errorf("remote child endpoint is not available yet")
	}
	stepStartedAt := time.Now()
	if err := waitForRemoteSwarmReady(ctx, remoteEndpoint, 45*time.Second); err != nil {
		logRemoteDeployTiming("approve.wait_for_remote_ready", stepStartedAt, err, "session_id", record.ID)
		return err
	}
	logRemoteDeployTiming("approve.wait_for_remote_ready", stepStartedAt, nil, "session_id", record.ID)
	peerSwarmID := strings.TrimSpace(record.HostSwarmID)
	if peerSwarmID == "" {
		return fmt.Errorf("host swarm id is not available yet")
	}
	peerToken := strings.TrimSpace(record.InviteToken)
	if peerToken == "" {
		return fmt.Errorf("remote deploy invite token is not available yet")
	}
	transports := remotePairingTransportsForMode(strings.TrimSpace(record.TransportMode), hostState.Node.Transports, strings.TrimSpace(record.MasterEndpoint))
	payload := remotePairingFinalizeRequest{
		PrimarySwarmID:       peerSwarmID,
		PrimaryName:          strings.TrimSpace(record.HostName),
		PrimaryPublicKey:     strings.TrimSpace(record.HostPublicKey),
		PrimaryFingerprint:   strings.TrimSpace(record.HostFingerprint),
		TransportMode:        strings.TrimSpace(record.TransportMode),
		RendezvousTransports: transports,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	requestURL := strings.TrimRight(remoteEndpoint, "/") + "/v1/swarm/remote-pairing/finalize"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Swarm-Peer-ID", peerSwarmID)
	req.Header.Set("X-Swarm-Peer-Token", peerToken)
	stepStartedAt = time.Now()
	resp, err := (&http.Client{Timeout: remotePairingHTTPTimeout}).Do(req)
	logRemoteDeployTiming("approve.remote_pairing_finalize_request", stepStartedAt, err, "session_id", record.ID, "endpoint", requestURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message, readErr := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		if readErr != nil {
			return fmt.Errorf("remote request failed with status %d", resp.StatusCode)
		}
		text := strings.TrimSpace(string(message))
		if text == "" {
			return fmt.Errorf("remote request failed with status %d", resp.StatusCode)
		}
		return fmt.Errorf("remote request failed with status %d: %s", resp.StatusCode, text)
	}
	return nil
}

func (s *Service) applyApprovedRemotePeerAuth(record pebblestore.RemoteDeploySessionRecord) error {
	if s == nil || s.swarmStore == nil {
		return fmt.Errorf("remote deploy service is not configured")
	}
	childSwarmID := strings.TrimSpace(record.ChildSwarmID)
	if childSwarmID == "" {
		return fmt.Errorf("approved remote deploy is missing child swarm id")
	}
	existing, _, err := s.swarmStore.GetTrustedPeer(childSwarmID)
	if err != nil {
		return err
	}
	transports := existing.RendezvousTransports
	if len(transports) == 0 && remoteSessionEndpoint(record) != "" {
		transports = []pebblestore.SwarmTransportRecord{{
			Kind:    firstNonEmpty(strings.TrimSpace(record.TransportMode), startupconfig.NetworkModeTailscale),
			Primary: remoteSessionEndpoint(record),
			All:     []string{remoteSessionEndpoint(record)},
		}}
	}
	approvedAt := existing.ApprovedAt
	if approvedAt <= 0 {
		approvedAt = time.Now().UnixMilli()
	}
	_, err = s.swarmStore.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{
		SwarmID:               childSwarmID,
		Name:                  firstNonEmpty(strings.TrimSpace(record.ChildName), existing.Name, childSwarmID),
		Role:                  firstNonEmpty(existing.Role, "child"),
		PublicKey:             firstNonEmpty(strings.TrimSpace(record.ChildPublicKey), existing.PublicKey),
		Fingerprint:           firstNonEmpty(strings.TrimSpace(record.ChildFingerprint), existing.Fingerprint),
		Relationship:          swarmruntime.RelationshipChild,
		ParentSwarmID:         firstNonEmpty(strings.TrimSpace(record.HostSwarmID), existing.ParentSwarmID),
		TransportMode:         firstNonEmpty(existing.TransportMode, strings.TrimSpace(record.TransportMode), startupconfig.NetworkModeTailscale),
		RendezvousTransports:  transports,
		OutgoingPeerAuthToken: strings.TrimSpace(record.InviteToken),
		IncomingPeerAuthHash:  swarmruntime.HashPeerAuthToken(strings.TrimSpace(record.SessionToken)),
		ApprovedAt:            approvedAt,
	})
	return err
}

func (s *Service) ChildStatus(ctx context.Context, input ChildStatusInput) (Session, error) {
	if s == nil || s.store == nil {
		return Session{}, fmt.Errorf("remote deploy service is not configured")
	}
	record, ok, err := s.store.Get(input.SessionID)
	if err != nil {
		return Session{}, err
	}
	if !ok {
		return Session{}, fmt.Errorf("remote deploy session not found")
	}
	if strings.TrimSpace(record.SessionToken) == "" || strings.TrimSpace(record.SessionToken) != strings.TrimSpace(input.SessionToken) {
		return Session{}, fmt.Errorf("remote deploy session token mismatch")
	}
	if strings.TrimSpace(record.EnrollmentID) == "" && strings.TrimSpace(record.InviteToken) != "" && s.swarms != nil {
		pending, err := s.swarms.ListPendingEnrollments(500)
		if err == nil {
			for _, item := range pending {
				if strings.EqualFold(strings.TrimSpace(item.InviteToken), strings.TrimSpace(record.InviteToken)) {
					record.EnrollmentID = strings.TrimSpace(item.ID)
					record.EnrollmentStatus = strings.TrimSpace(item.Status)
					record.ChildSwarmID = firstNonEmpty(record.ChildSwarmID, strings.TrimSpace(item.ChildSwarmID))
					record.ChildName = firstNonEmpty(record.ChildName, strings.TrimSpace(item.ChildName))
					record.ChildPublicKey = firstNonEmpty(record.ChildPublicKey, strings.TrimSpace(item.ChildPublicKey))
					record.ChildFingerprint = firstNonEmpty(record.ChildFingerprint, strings.TrimSpace(item.ChildFingerprint))
					if saved, saveErr := s.store.Put(record); saveErr == nil {
						record = saved
					}
					break
				}
			}
		}
	}
	_ = ctx
	return mapSession(record), nil
}

func (s *Service) refreshRemoteSessionState(ctx context.Context, record pebblestore.RemoteDeploySessionRecord) (pebblestore.RemoteDeploySessionRecord, error) {
	if s == nil || s.store == nil {
		return record, fmt.Errorf("remote deploy service is not configured")
	}
	changed := false
	var refreshErr error
	status := strings.TrimSpace(record.Status)
	if status == "waiting_for_approval" || status == "approved" || status == "attached" {
		if runtimeChanged, err := s.refreshRemoteRuntimeSignals(ctx, &record); err != nil {
			if firstNonEmpty(strings.TrimSpace(record.LastError), "") != strings.TrimSpace(err.Error()) {
				record.LastError = strings.TrimSpace(err.Error())
				changed = true
			}
			refreshErr = err
		} else {
			if runtimeChanged {
				changed = true
			}
			if strings.TrimSpace(record.LastError) != "" {
				record.LastError = ""
				changed = true
			}
		}
		if inviteChanged, err := s.ensurePendingInvite(&record); err != nil {
			if refreshErr == nil {
				refreshErr = err
			}
		} else if inviteChanged {
			changed = true
		}
		if enrollmentChanged, err := s.syncPendingEnrollment(&record); err != nil {
			if refreshErr == nil {
				refreshErr = err
			}
		} else if enrollmentChanged {
			changed = true
		}
		if shouldRequestRemotePairing(record) {
			if pairingChanged, err := s.requestRemotePairing(ctx, &record); err != nil {
				if strings.TrimSpace(record.LastError) != strings.TrimSpace(err.Error()) {
					record.LastError = strings.TrimSpace(err.Error())
					changed = true
				}
				if refreshErr == nil {
					refreshErr = err
				}
			} else {
				if pairingChanged {
					changed = true
				}
				if strings.TrimSpace(record.LastError) != "" {
					record.LastError = ""
					changed = true
				}
				if enrollmentChanged, err := s.syncPendingEnrollment(&record); err != nil {
					if refreshErr == nil {
						refreshErr = err
					}
				} else if enrollmentChanged {
					changed = true
				}
			}
		}
	}
	if changed {
		saved, err := s.store.Put(record)
		if err != nil {
			return record, err
		}
		record = saved
	}
	return record, refreshErr
}

func (s *Service) refreshRemoteRuntimeSignals(ctx context.Context, record *pebblestore.RemoteDeploySessionRecord) (bool, error) {
	if record == nil {
		return false, fmt.Errorf("remote deploy record is required")
	}
	if strings.TrimSpace(record.SSHSessionTarget) == "" {
		return false, fmt.Errorf("remote ssh target is required")
	}
	sudoPrefix := ""
	if strings.TrimSpace(record.SudoMode) == "sudo" {
		sudoPrefix = "sudo "
	}
	cmd := fmt.Sprintf(`set -eu
log_file=%s
runtime=%s
if [ -s "$log_file" ]; then
  logs="$(tail -n 200 "$log_file" 2>&1 || true)"
elif [ "$runtime" = "podman" ]; then
  logs="$(%spodman logs --tail 200 %s 2>&1 || true)"
else
  logs="$(%sdocker logs --tail 200 %s 2>&1 || true)"
fi
printf '%%s\n' "$logs"
`, shellQuote(remoteServiceLogPath(*record)), shellQuote(normalizeRemoteDeployRuntime(record.RemoteRuntime)), sudoPrefix, shellQuote(remoteContainerNameForSession(record.ID)), sudoPrefix, shellQuote(remoteContainerNameForSession(record.ID)))
	output, err := runSSHCommand(ctx, record.SSHSessionTarget, cmd)
	if err != nil {
		return false, err
	}
	trimmedOutput := strings.TrimSpace(output)
	changed := false
	if trimmedOutput != "" && trimmedOutput != strings.TrimSpace(record.LastRemoteOutput) {
		record.LastRemoteOutput = trimmedOutput
		changed = true
	}
	authURL, remoteEndpoint := parseRemoteBootstrapURLs(output)
	if authURL != "" && authURL != strings.TrimSpace(record.RemoteAuthURL) {
		record.RemoteAuthURL = authURL
		changed = true
	}
	if remoteEndpoint != "" && remoteEndpoint != remoteSessionEndpoint(*record) {
		record.RemoteEndpoint = remoteEndpoint
		if strings.EqualFold(strings.TrimSpace(record.TransportMode), startupconfig.NetworkModeTailscale) {
			record.RemoteTailnetURL = remoteEndpoint
		}
		changed = true
	}
	return changed, nil
}

func (s *Service) ensurePendingInvite(record *pebblestore.RemoteDeploySessionRecord) (bool, error) {
	if s == nil || s.swarms == nil || record == nil {
		return false, nil
	}
	if strings.TrimSpace(record.InviteToken) == "" || strings.TrimSpace(record.EnrollmentID) != "" {
		return false, nil
	}
	startupCfg, hostState, err := s.resolveBootstrapContext()
	if err != nil {
		return false, err
	}
	hostName := firstNonEmpty(strings.TrimSpace(startupCfg.SwarmName), strings.TrimSpace(hostState.Node.Name), "Primary")
	transports := remotePairingTransportsForMode(strings.TrimSpace(record.TransportMode), hostState.Node.Transports, strings.TrimSpace(record.MasterEndpoint))
	rendezvous := make([]swarmruntime.TransportSummary, 0, len(transports))
	for _, item := range transports {
		rendezvous = append(rendezvous, swarmruntime.TransportSummary{
			Kind:    strings.TrimSpace(item.Kind),
			Primary: strings.TrimSpace(item.Primary),
			All:     append([]string(nil), item.All...),
		})
	}
	invite, err := s.swarms.EnsureInvite(swarmruntime.EnsureInviteInput{
		Token:                strings.TrimSpace(record.InviteToken),
		PrimarySwarmID:       strings.TrimSpace(hostState.Node.SwarmID),
		PrimaryName:          hostName,
		GroupID:              strings.TrimSpace(record.GroupID),
		TransportMode:        strings.TrimSpace(record.TransportMode),
		RendezvousTransports: rendezvous,
		TTL:                  30 * time.Minute,
	})
	if err != nil {
		return false, err
	}
	changed := false
	if strings.TrimSpace(record.GroupID) == "" && strings.TrimSpace(invite.GroupID) != "" {
		record.GroupID = strings.TrimSpace(invite.GroupID)
		changed = true
	}
	if strings.TrimSpace(record.HostSwarmID) != strings.TrimSpace(hostState.Node.SwarmID) {
		record.HostSwarmID = strings.TrimSpace(hostState.Node.SwarmID)
		changed = true
	}
	if strings.TrimSpace(record.HostName) != hostName {
		record.HostName = hostName
		changed = true
	}
	return changed, nil
}

func (s *Service) syncPendingEnrollment(record *pebblestore.RemoteDeploySessionRecord) (bool, error) {
	if s == nil || s.swarms == nil || record == nil {
		return false, nil
	}
	inviteToken := strings.TrimSpace(record.InviteToken)
	if inviteToken == "" {
		return false, nil
	}
	pending, err := s.swarms.ListPendingEnrollments(500)
	if err != nil {
		return false, err
	}
	for _, item := range pending {
		if !strings.EqualFold(strings.TrimSpace(item.InviteToken), inviteToken) {
			continue
		}
		changed := false
		if strings.TrimSpace(record.EnrollmentID) != strings.TrimSpace(item.ID) {
			record.EnrollmentID = strings.TrimSpace(item.ID)
			changed = true
		}
		if strings.TrimSpace(record.EnrollmentStatus) != strings.TrimSpace(item.Status) {
			record.EnrollmentStatus = strings.TrimSpace(item.Status)
			changed = true
		}
		if strings.TrimSpace(record.ChildSwarmID) != strings.TrimSpace(item.ChildSwarmID) {
			record.ChildSwarmID = strings.TrimSpace(item.ChildSwarmID)
			changed = true
		}
		if strings.TrimSpace(record.ChildName) != strings.TrimSpace(item.ChildName) {
			record.ChildName = strings.TrimSpace(item.ChildName)
			changed = true
		}
		if strings.TrimSpace(record.ChildPublicKey) != strings.TrimSpace(item.ChildPublicKey) {
			record.ChildPublicKey = strings.TrimSpace(item.ChildPublicKey)
			changed = true
		}
		if strings.TrimSpace(record.ChildFingerprint) != strings.TrimSpace(item.ChildFingerprint) {
			record.ChildFingerprint = strings.TrimSpace(item.ChildFingerprint)
			changed = true
		}
		return changed, nil
	}
	return false, nil
}

func (s *Service) requestRemotePairing(ctx context.Context, record *pebblestore.RemoteDeploySessionRecord) (bool, error) {
	if s == nil || s.swarms == nil || record == nil {
		return false, fmt.Errorf("remote deploy service is not configured")
	}
	remoteEndpoint := remoteSessionEndpoint(*record)
	if remoteEndpoint == "" {
		return false, fmt.Errorf("remote child endpoint is not available yet")
	}
	stepStartedAt := time.Now()
	if err := waitForRemoteSwarmReady(ctx, remoteEndpoint, 45*time.Second); err != nil {
		logRemoteDeployTiming("refresh.wait_for_remote_ready", stepStartedAt, err, "session_id", record.ID)
		return false, err
	}
	logRemoteDeployTiming("refresh.wait_for_remote_ready", stepStartedAt, nil, "session_id", record.ID)
	startupCfg, hostState, err := s.resolveBootstrapContext()
	if err != nil {
		return false, err
	}
	transports := remotePairingTransportsForMode(strings.TrimSpace(record.TransportMode), hostState.Node.Transports, strings.TrimSpace(record.MasterEndpoint))
	primaryEndpoint := firstNonEmpty(
		strings.TrimSpace(record.MasterEndpoint),
		firstTransportPrimary(transports),
	)
	payload := remotePairingRequest{
		InviteToken:          strings.TrimSpace(record.InviteToken),
		PrimarySwarmID:       strings.TrimSpace(hostState.Node.SwarmID),
		PrimaryName:          firstNonEmpty(strings.TrimSpace(startupCfg.SwarmName), strings.TrimSpace(hostState.Node.Name), "Primary"),
		PrimaryEndpoint:      primaryEndpoint,
		TransportMode:        strings.TrimSpace(record.TransportMode),
		RendezvousTransports: transports,
	}
	var response remotePairingResponse
	requestURL := strings.TrimRight(remoteEndpoint, "/") + "/v1/swarm/remote-pairing/request"
	stepStartedAt = time.Now()
	client := &http.Client{Timeout: remotePairingHTTPTimeout}
	if err := remoteSwarmJSONRequestWithClient(http.MethodPost, requestURL, payload, &response, client); err != nil {
		logRemoteDeployTiming("refresh.remote_pairing_request", stepStartedAt, err, "session_id", record.ID, "endpoint", requestURL)
		return false, err
	}
	logRemoteDeployTiming("refresh.remote_pairing_request", stepStartedAt, nil, "session_id", record.ID, "endpoint", requestURL)
	changed := false
	if strings.TrimSpace(record.LastPairingURL) != remoteEndpoint {
		record.LastPairingURL = remoteEndpoint
		changed = true
	}
	if strings.TrimSpace(record.EnrollmentStatus) != "pairing_requested" {
		record.EnrollmentStatus = "pairing_requested"
		changed = true
	}
	if childSwarmID := strings.TrimSpace(response.ChildSwarmID); childSwarmID != "" && childSwarmID != strings.TrimSpace(record.ChildSwarmID) {
		record.ChildSwarmID = childSwarmID
		changed = true
	}
	if childName := strings.TrimSpace(response.ChildName); childName != "" && childName != strings.TrimSpace(record.ChildName) {
		record.ChildName = childName
		changed = true
	}
	return changed, nil
}

func shouldRequestRemotePairing(record pebblestore.RemoteDeploySessionRecord) bool {
	if strings.TrimSpace(record.EnrollmentID) != "" {
		return false
	}
	remoteEndpoint := remoteSessionEndpoint(record)
	if remoteEndpoint == "" || strings.TrimSpace(record.InviteToken) == "" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(record.EnrollmentStatus), "pairing_requested") {
		return true
	}
	return !strings.EqualFold(strings.TrimSpace(record.LastPairingURL), remoteEndpoint)
}

const remotePairingHTTPTimeout = 30 * time.Second

func waitForRemoteSwarmReady(ctx context.Context, endpoint string, timeout time.Duration) error {
	return waitForRemoteSwarmReadyWithClient(ctx, endpoint, nil, timeout, 2*time.Second)
}

func waitForRemoteSwarmReadyWithClient(ctx context.Context, endpoint string, client *http.Client, timeout, interval time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return fmt.Errorf("remote child tailnet url is not available yet")
	}
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	readyEndpoint := strings.TrimRight(endpoint, "/") + "/readyz"
	healthEndpoint := strings.TrimRight(endpoint, "/") + "/healthz"
	deadline := time.Now().Add(timeout)
	startedAt := time.Now()
	probes := 0
	var lastErr error
	for {
		probes++
		if err := probeRemoteSwarmReady(ctx, readyEndpoint, client); err == nil {
			logRemoteDeployTiming("remote.wait_ready", startedAt, nil, "endpoint", endpoint, "probes", strconv.Itoa(probes))
			return nil
		} else {
			lastErr = err
		}
		probes++
		if err := probeRemoteSwarmReady(ctx, healthEndpoint, client); err == nil {
			logRemoteDeployTiming("remote.wait_ready", startedAt, nil, "endpoint", endpoint, "probes", strconv.Itoa(probes))
			return nil
		} else {
			lastErr = err
		}
		if err := ctx.Err(); err != nil {
			logRemoteDeployTiming("remote.wait_ready", startedAt, err, "endpoint", endpoint, "probes", strconv.Itoa(probes))
			if lastErr != nil {
				return fmt.Errorf("remote child at %s was not ready before context ended: %w", endpoint, lastErr)
			}
			return err
		}
		if time.Now().After(deadline) {
			logRemoteDeployTiming("remote.wait_ready", startedAt, lastErr, "endpoint", endpoint, "probes", strconv.Itoa(probes))
			if lastErr == nil {
				lastErr = fmt.Errorf("timed out waiting for readyz")
			}
			return fmt.Errorf("remote child at %s was not ready within %s: %w", endpoint, timeout, lastErr)
		}
		timer := time.NewTimer(interval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			if lastErr != nil {
				return fmt.Errorf("remote child at %s was not ready before context ended: %w", endpoint, lastErr)
			}
			return ctx.Err()
		}
	}
}

func probeRemoteSwarmReady(ctx context.Context, endpoint string, client *http.Client) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = resp.Status
	}
	return fmt.Errorf("%s returned %s: %s", endpoint, resp.Status, message)
}

func parseRemoteBootstrapURLs(output string) (authURL, remoteEndpoint string) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "TAILSCALE_AUTH_URL="):
			authURL = strings.TrimSpace(strings.TrimPrefix(line, "TAILSCALE_AUTH_URL="))
		case strings.HasPrefix(line, "SWARM_REMOTE_URL="):
			remoteEndpoint = strings.TrimSpace(strings.TrimPrefix(line, "SWARM_REMOTE_URL="))
		case strings.HasPrefix(line, "SWARM_TAILNET_URL="):
			remoteEndpoint = strings.TrimSpace(strings.TrimPrefix(line, "SWARM_TAILNET_URL="))
		}
	}
	return authURL, remoteEndpoint
}

func (s *Service) resolveBootstrapContext() (startupconfig.FileConfig, swarmruntime.LocalState, error) {
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return startupconfig.FileConfig{}, swarmruntime.LocalState{}, err
	}
	state, err := s.swarms.EnsureLocalState(swarmruntime.EnsureLocalStateInput{
		Name:          strings.TrimSpace(cfg.SwarmName),
		Role:          hostRole(cfg),
		SwarmMode:     true,
		AdvertiseMode: cfg.NetworkMode,
		AdvertiseAddr: strings.TrimSpace(cfg.AdvertiseHost),
	})
	if err != nil {
		return startupconfig.FileConfig{}, swarmruntime.LocalState{}, err
	}
	return cfg, state, nil
}

func (s *Service) loadStartupConfig() (startupconfig.FileConfig, error) {
	path := strings.TrimSpace(s.startupPath)
	if path == "" {
		resolved, err := startupconfig.ResolvePath()
		if err != nil {
			return startupconfig.FileConfig{}, err
		}
		path = resolved
	}
	return startupconfig.Load(path)
}

func (s *Service) detectBuilderRuntime(ctx context.Context, preferredRuntime string) (string, error) {
	preferredRuntime = normalizeRemoteDeployRuntime(preferredRuntime)
	if s.containers != nil {
		status, err := s.containers.RuntimeStatus(ctx)
		if err == nil {
			if preferredRuntime != "" {
				for _, available := range status.Available {
					if normalizeRemoteDeployRuntime(available) == preferredRuntime {
						return preferredRuntime, nil
					}
				}
			}
			if strings.TrimSpace(status.Recommended) != "" {
				return strings.TrimSpace(status.Recommended), nil
			}
		}
	}
	candidates := []string{"podman", "docker"}
	if preferredRuntime != "" {
		candidates = append([]string{preferredRuntime}, candidates...)
	}
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		candidate = normalizeRemoteDeployRuntime(candidate)
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("install Podman or Docker to build remote child artifacts")
}

func (s *Service) inspectRemoteHost(ctx context.Context, sshTarget, preferredRuntime string) (runtimeName string, systemdAvailable bool, sudoMode string, remoteHome string, remoteNetworkCandidates []string, remoteAvailableBytes int64, err error) {
	checkCmd := fmt.Sprintf(`set -eu
runtime=%s
if command -v systemctl >/dev/null 2>&1; then systemd=1; else systemd=0; fi
sudo_mode="none"
if command -v sudo >/dev/null 2>&1; then sudo_mode="sudo"; fi
remote_home="${HOME:-}"
if [ -z "$remote_home" ]; then remote_home="$(cd && pwd)"; fi
if [ -z "$remote_home" ] || [ "${remote_home#/}" = "$remote_home" ]; then echo "remote home directory missing" >&2; exit 41; fi
remote_available_bytes=""
if command -v df >/dev/null 2>&1; then
  remote_available_kb="$(df -Pk "$remote_home" 2>/dev/null | awk 'NR==2 {print $4}')"
  case "$remote_available_kb" in
    ''|*[!0-9]*) remote_available_bytes="" ;;
    *) remote_available_bytes="$((remote_available_kb * 1024))" ;;
  esac
fi
printf 'REMOTE_RUNTIME=%%s\n' "$runtime"
printf 'SYSTEMD_AVAILABLE=%%s\n' "$systemd"
printf 'SUDO_MODE=%%s\n' "$sudo_mode"
printf 'REMOTE_HOME=%%s\n' "$remote_home"
printf 'REMOTE_AVAILABLE_BYTES=%%s\n' "$remote_available_bytes"
if command -v ip >/dev/null 2>&1; then
  ip -o -4 addr show up 2>/dev/null | while read -r _ iface _ addr _; do
    case "$iface" in
      lo|docker*|br-*|veth*|cni*|flannel*|tailscale*|zt*)
        continue
        ;;
    esac
    addr="${addr%%/*}"
    if [ -n "$addr" ]; then
      printf 'REMOTE_NETWORK_CANDIDATE=%%s\n' "$addr"
    fi
  done
fi
`, shellQuote(firstNonEmpty(strings.TrimSpace(preferredRuntime), "native")))
	out, err := runSSHCommand(ctx, sshTarget, checkCmd)
	if err != nil {
		return "", false, "", "", nil, 0, err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "REMOTE_RUNTIME="):
			runtimeName = strings.TrimSpace(strings.TrimPrefix(line, "REMOTE_RUNTIME="))
		case strings.HasPrefix(line, "SYSTEMD_AVAILABLE="):
			systemdAvailable = strings.TrimSpace(strings.TrimPrefix(line, "SYSTEMD_AVAILABLE=")) == "1"
		case strings.HasPrefix(line, "SUDO_MODE="):
			sudoMode = strings.TrimSpace(strings.TrimPrefix(line, "SUDO_MODE="))
		case strings.HasPrefix(line, "REMOTE_HOME="):
			remoteHome = strings.TrimSpace(strings.TrimPrefix(line, "REMOTE_HOME="))
		case strings.HasPrefix(line, "REMOTE_AVAILABLE_BYTES="):
			available, parseErr := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "REMOTE_AVAILABLE_BYTES=")), 10, 64)
			if parseErr == nil && available > 0 {
				remoteAvailableBytes = available
			}
		case strings.HasPrefix(line, "REMOTE_NETWORK_CANDIDATE="):
			remoteNetworkCandidates = append(remoteNetworkCandidates, strings.TrimSpace(strings.TrimPrefix(line, "REMOTE_NETWORK_CANDIDATE=")))
		}
	}
	if runtimeName == "" {
		return "", false, "", "", nil, 0, fmt.Errorf("remote runtime detection failed")
	}
	if remoteHome == "" || !strings.HasPrefix(remoteHome, "/") {
		return "", false, "", "", nil, 0, fmt.Errorf("remote home directory detection failed")
	}
	return runtimeName, systemdAvailable, sudoMode, remoteHome, sortRemoteNetworkCandidates(remoteNetworkCandidates), remoteAvailableBytes, nil
}

func (s *Service) checkRemoteInstallCollision(ctx context.Context, sshTarget, remoteRoot string) error {
	remoteRoot = strings.TrimSpace(remoteRoot)
	checkCmd := fmt.Sprintf(`set -eu
remote_root=%s
if [ -e "$remote_root" ]; then
  echo "REMOTE_ROOT_EXISTS=$remote_root"
  exit 42
fi
printf 'REMOTE_INSTALL_PATH_CLEAR=1\n'
`, shellQuote(remoteRoot))
	out, err := runSSHCommand(ctx, sshTarget, checkCmd)
	if err != nil {
		trimmed := strings.TrimSpace(out)
		switch {
		case strings.Contains(trimmed, "REMOTE_ROOT_EXISTS="):
			path := strings.TrimSpace(strings.TrimPrefix(lastMatchingLine(trimmed, "REMOTE_ROOT_EXISTS="), "REMOTE_ROOT_EXISTS="))
			if path == "" {
				path = remoteRoot
			}
			return fmt.Errorf("remote preflight failed: target remote path already exists: %s", path)
		default:
			return err
		}
	}
	return nil
}

func (s *Service) checkRemoteDiskCapacity(ctx context.Context, sshTarget, remoteRoot string, requiredBytes int64) (availableBytes int64, err error) {
	remoteRoot = strings.TrimSpace(remoteRoot)
	if remoteRoot == "" {
		return 0, fmt.Errorf("remote root is required for disk capacity check")
	}
	if requiredBytes <= 0 {
		requiredBytes = remoteRequiredDiskBytes(0, nil)
	}
	checkCmd := fmt.Sprintf(`set -eu
remote_root=%s
required_bytes=%s
parent="$remote_root"
while [ ! -e "$parent" ] && [ "$parent" != "/" ]; do
  parent="$(dirname "$parent")"
done
available_bytes=""
if command -v df >/dev/null 2>&1; then
  available_kb="$(df -Pk "$parent" 2>/dev/null | awk 'NR==2 {print $4}')"
  case "$available_kb" in
    ''|*[!0-9]*) available_bytes="" ;;
    *) available_bytes="$((available_kb * 1024))" ;;
  esac
fi
case "$available_bytes" in
  ''|*[!0-9]*)
    echo "REMOTE_DISK_AVAILABLE_UNKNOWN=$parent"
    exit 44
    ;;
esac
printf 'REMOTE_DISK_PATH=%%s\n' "$parent"
printf 'REMOTE_DISK_AVAILABLE_BYTES=%%s\n' "$available_bytes"
printf 'REMOTE_DISK_REQUIRED_BYTES=%%s\n' "$required_bytes"
if [ "$available_bytes" -lt "$required_bytes" ]; then
  echo "REMOTE_DISK_INSUFFICIENT=$parent:$available_bytes:$required_bytes"
  exit 43
fi
`, shellQuote(remoteRoot), shellQuote(strconv.FormatInt(requiredBytes, 10)))
	out, err := runSSHCommand(ctx, sshTarget, checkCmd)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "REMOTE_DISK_AVAILABLE_BYTES=") {
			available, parseErr := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "REMOTE_DISK_AVAILABLE_BYTES=")), 10, 64)
			if parseErr == nil && available > 0 {
				availableBytes = available
			}
		}
	}
	if err != nil {
		trimmed := strings.TrimSpace(out)
		switch {
		case strings.Contains(trimmed, "REMOTE_DISK_INSUFFICIENT="):
			parts := strings.Split(strings.TrimSpace(strings.TrimPrefix(lastMatchingLine(trimmed, "REMOTE_DISK_INSUFFICIENT="), "REMOTE_DISK_INSUFFICIENT=")), ":")
			path := remoteRoot
			if len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
				path = strings.TrimSpace(parts[0])
			}
			available := availableBytes
			if len(parts) > 1 {
				if parsed, parseErr := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64); parseErr == nil {
					available = parsed
				}
			}
			required := requiredBytes
			if len(parts) > 2 {
				if parsed, parseErr := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64); parseErr == nil {
					required = parsed
				}
			}
			return available, fmt.Errorf("remote preflight failed: insufficient disk space at %s: available %d bytes, required %d bytes", path, available, required)
		case strings.Contains(trimmed, "REMOTE_DISK_AVAILABLE_UNKNOWN="):
			path := strings.TrimSpace(strings.TrimPrefix(lastMatchingLine(trimmed, "REMOTE_DISK_AVAILABLE_UNKNOWN="), "REMOTE_DISK_AVAILABLE_UNKNOWN="))
			if path == "" {
				path = remoteRoot
			}
			return availableBytes, fmt.Errorf("remote preflight failed: remote disk capacity unavailable at %s", path)
		default:
			return availableBytes, err
		}
	}
	return availableBytes, nil
}

func formatCreatePreflightError(sshTarget string, err error) error {
	message := strings.TrimSpace(err.Error())
	lower := strings.ToLower(message)
	target := strings.TrimSpace(sshTarget)
	if target == "" {
		target = "remote host"
	}
	switch {
	case strings.Contains(lower, "remote runtime missing:docker"):
		return fmt.Errorf("Remote preflight failed for %s.\n\nWhat failed\n- Docker was selected for the remote host, but Docker is not installed there.\n\nWhat to do on the remote host\n- Install Docker.\n- Then rerun preflight.\n\nSuggested commands (Debian/Ubuntu)\n- ssh %s\n- apt update\n- apt install -y docker.io\n- systemctl enable --now docker\n- docker --version", target, target)
	case strings.Contains(lower, "remote runtime missing:podman"):
		return fmt.Errorf("Remote preflight failed for %s.\n\nWhat failed\n- Podman was selected for the remote host, but Podman is not installed there.\n\nWhat to do on the remote host\n- Install Podman.\n- Then rerun preflight.\n\nSuggested commands (Debian/Ubuntu)\n- ssh %s\n- apt update\n- apt install -y podman\n- podman --version", target, target)
	case strings.Contains(lower, "remote runtime missing"):
		return fmt.Errorf("Remote preflight failed for %s.\n\nWhat failed\n- No supported container runtime was found on the remote host.\n\nWhat to do on the remote host\n- Install Docker or Podman.\n- Then rerun preflight.\n\nSuggested commands (Debian/Ubuntu)\n- ssh %s\n- apt update\n- apt install -y docker.io\n- systemctl enable --now docker\n- docker --version\n\nAlternative\n- apt install -y podman\n- podman --version", target, target)
	case strings.Contains(lower, "target remote path already exists:"):
		path := strings.TrimSpace(strings.TrimPrefix(message, "remote preflight failed: target remote path already exists:"))
		if path == "" {
			path = "remote deploy path"
		}
		return fmt.Errorf("Remote preflight failed for %s.\n\nWhat failed\n- The target remote deploy path already exists: %s\n\nWhat to do\n- Remove or rename that path on the remote host, or choose a different swarm name and rerun preflight.\n\nSuggested commands\n- ssh %s\n- ls -la %s\n- rm -rf %s   # only if this old deploy can be safely removed", target, path, target, path, path)
	case strings.Contains(lower, "insufficient disk space at"):
		return fmt.Errorf("Remote preflight failed for %s.\n\nWhat failed\n- The remote host does not have enough available disk space for the image and selected payloads.\n- %s\n\nWhat to do\n- Free disk space on the remote host or select fewer workspace payloads.\n- Then rerun preflight.\n\nSuggested commands\n- ssh %s\n- df -h\n- docker system df || true\n- docker system prune   # only if old Docker cache can be safely removed", target, message, target)
	case strings.Contains(lower, "remote disk capacity unavailable"):
		return fmt.Errorf("Remote preflight failed for %s.\n\nWhat failed\n- Swarm could not read available disk capacity on the remote host.\n- %s\n\nWhat to do\n- Make sure df is installed and the remote home filesystem is readable.\n- Then rerun preflight.\n\nSuggested commands\n- ssh %s\n- df -Pk ~", target, message, target)
	case strings.Contains(lower, "systemd unit already exists:"):
		unit := strings.TrimSpace(strings.TrimPrefix(message, "remote preflight failed: systemd unit already exists:"))
		if unit == "" {
			unit = "existing systemd unit"
		}
		return fmt.Errorf("Remote preflight failed for %s.\n\nWhat failed\n- The target systemd unit already exists: %s\n\nWhat to do\n- Remove or rename the old unit, or choose a different swarm name and rerun preflight.\n\nSuggested commands\n- ssh %s\n- systemctl status %s\n- systemctl disable --now %s   # if it is an old test unit you want to replace\n- unit_path=$(systemctl show -p FragmentPath --value %s)\n- rm -f \"$unit_path\"\n- systemctl daemon-reload", target, unit, target, unit, unit, unit)
	case strings.Contains(lower, "master swarm.conf tailscale_url is required"):
		return fmt.Errorf("Remote preflight failed on the master.\n\nWhat failed\n- The master startup config is missing tailscale_url.\n\nWhat to do\n- Set tailscale_url in the master swarm.conf so remote children know where to call back.\n- Then rerun preflight.")
	case strings.Contains(lower, "master swarm.conf host is still loopback-only for lan/wireguard"):
		return fmt.Errorf("Remote preflight failed on the master.\n\nWhat failed\n- LAN/WireGuard remote deploy cannot use this master while swarmd is still bound to localhost.\n\nWhy this matters\n- SSH is only used to install the child.\n- After launch, the child calls back to the master over the master's real listener.\n- advertise_host only changes the announced endpoint. It does not move the master listener off 127.0.0.1.\n\nWhat to do\n- Set host in the master swarm.conf to a reachable LAN, WireGuard, or tunnel address.\n- Keep advertise_host aligned to the same reachable address unless you have a separate forwarded endpoint.\n- Restart Swarm so the master actually listens there.\n- Then rerun preflight.\n\nAlternative\n- Switch this deploy to Tailscale if both machines are already on the tailnet.")
	case strings.Contains(lower, "advertise_host is required for lan/wireguard"):
		return fmt.Errorf("Remote preflight failed on the master.\n\nWhat failed\n- LAN/WireGuard remote deploy needs a master callback endpoint, but this master is not advertising one.\n\nWhy this matters\n- SSH is only used to install the child.\n- After launch, the child calls back to the master over advertise_host:advertise_port.\n\nWhat to do\n- Set advertise_host and advertise_port in the master swarm.conf to a host and port the remote child can reach.\n- Or switch this deploy to Tailscale if both machines are already on the tailnet.\n- Then rerun preflight.")
	case strings.Contains(lower, "remote reachable host is required for lan/wireguard"):
		return fmt.Errorf("Remote preflight failed for %s.\n\nWhat failed\n- LAN/WireGuard remote deploy needs a reachable host or IP for the remote child.\n\nWhat to do\n- Enter the remote machine's reachable LAN, WireGuard, or tunnel IP/hostname.\n- Then rerun preflight.", target)
	case strings.Contains(lower, "remote reachable host must not include a url scheme"):
		return fmt.Errorf("Remote preflight failed for %s.\n\nWhat failed\n- The remote reachable host must be a host or IP only.\n\nWhat to do\n- Remove http:// or https:// and enter just the host or IP.\n- Then rerun preflight.", target)
	case strings.Contains(lower, "remote reachable host must not contain path separators"):
		return fmt.Errorf("Remote preflight failed for %s.\n\nWhat failed\n- The remote reachable host must not contain a path.\n\nWhat to do\n- Enter only the host or IP for the remote child endpoint.\n- Then rerun preflight.", target)
	default:
		return err
	}
}

func (s *Service) resolveTargetGroupForSession(hostState swarmruntime.LocalState, groupID, groupName string) (pebblestore.SwarmGroupRecord, error) {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return pebblestore.SwarmGroupRecord{}, fmt.Errorf("group_id is required")
	}
	if err := requireHostedGroupForLocalSwarm(hostState, groupID); err != nil {
		return pebblestore.SwarmGroupRecord{}, err
	}
	group, ok, err := s.swarmStore.GetGroup(groupID)
	if err != nil {
		return pebblestore.SwarmGroupRecord{}, err
	}
	if !ok {
		return pebblestore.SwarmGroupRecord{}, fmt.Errorf("target group %q does not exist", groupID)
	}
	if strings.TrimSpace(group.Name) == "" {
		group.Name = firstNonEmpty(groupName, groupID)
		if _, err := s.swarmStore.PutGroup(group); err != nil {
			return pebblestore.SwarmGroupRecord{}, err
		}
	}
	return group, nil
}

func (s *Service) buildPayloads(payloads []PayloadSelection) ([]pebblestore.RemoteDeployPayloadRecord, error) {
	out := make([]pebblestore.RemoteDeployPayloadRecord, 0, len(payloads))
	for idx, payload := range payloads {
		record, err := buildPayloadRecord(idx, payload)
		if err != nil {
			return nil, err
		}
		if record.SourcePath == "" {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

func buildPayloadRecord(index int, payload PayloadSelection) (pebblestore.RemoteDeployPayloadRecord, error) {
	sourcePath := strings.TrimSpace(payload.SourcePath)
	if sourcePath == "" {
		return pebblestore.RemoteDeployPayloadRecord{}, nil
	}
	rootRecord, err := buildPayloadDirectoryRecord(
		sourcePath,
		firstNonEmpty(strings.TrimSpace(payload.TargetPath), "/workspaces"),
		fmt.Sprintf("payload-%02d", index+1),
	)
	if err != nil {
		return pebblestore.RemoteDeployPayloadRecord{}, err
	}
	directories := make([]pebblestore.RemoteDeployPayloadDirectoryRecord, 0, len(payload.Directories))
	totalFiles := rootRecord.IncludedFiles
	totalBytes := rootRecord.IncludedBytes
	seenDirectories := map[string]struct{}{
		sourcePath: {},
	}
	for directoryIndex, directory := range payload.Directories {
		sourcePath := strings.TrimSpace(directory.SourcePath)
		if sourcePath == "" {
			continue
		}
		if _, ok := seenDirectories[sourcePath]; ok {
			continue
		}
		seenDirectories[sourcePath] = struct{}{}
		directoryRecord, err := buildPayloadDirectoryRecord(
			sourcePath,
			firstNonEmpty(strings.TrimSpace(directory.TargetPath), firstNonEmpty(strings.TrimSpace(payload.TargetPath), "/workspaces")),
			fmt.Sprintf("payload-%02d-dir-%02d", index+1, directoryIndex+1),
		)
		if err != nil {
			return pebblestore.RemoteDeployPayloadRecord{}, err
		}
		directories = append(directories, directoryRecord)
		totalFiles += directoryRecord.IncludedFiles
		totalBytes += directoryRecord.IncludedBytes
	}
	record := pebblestore.RemoteDeployPayloadRecord{
		ID:            fmt.Sprintf("payload-%02d", index+1),
		SourcePath:    rootRecord.SourcePath,
		WorkspacePath: strings.TrimSpace(payload.WorkspacePath),
		WorkspaceName: strings.TrimSpace(payload.WorkspaceName),
		TargetPath:    rootRecord.TargetPath,
		Mode:          firstNonEmpty(strings.TrimSpace(payload.Mode), "rw"),
		Directories:   directories,
		GitRoot:       rootRecord.GitRoot,
		ArchiveName:   rootRecord.ArchiveName,
		IncludedFiles: totalFiles,
		IncludedBytes: totalBytes,
		ExcludedNote:  rootRecord.ExcludedNote,
	}
	return record, nil
}

func buildPayloadDirectoryRecord(sourcePath, targetPath, archivePrefix string) (pebblestore.RemoteDeployPayloadDirectoryRecord, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return pebblestore.RemoteDeployPayloadDirectoryRecord{}, nil
	}
	includedFiles, includedBytes, gitRoot, err := gitTrackedStats(sourcePath)
	if err != nil {
		return pebblestore.RemoteDeployPayloadDirectoryRecord{}, err
	}
	archiveBase := sanitizeSlug(filepath.Base(sourcePath))
	if archiveBase == "" {
		archiveBase = "workspace"
	}
	return pebblestore.RemoteDeployPayloadDirectoryRecord{
		SourcePath:    sourcePath,
		TargetPath:    firstNonEmpty(strings.TrimSpace(targetPath), "/workspaces"),
		GitRoot:       gitRoot,
		ArchiveName:   fmt.Sprintf("%s-%s.tar.gz", archivePrefix, archiveBase),
		IncludedFiles: includedFiles,
		IncludedBytes: includedBytes,
		ExcludedNote:  "Only Git-tracked files are included in remote payload archives.",
	}, nil
}

func (s *Service) renderChildStartupConfig(record pebblestore.RemoteDeploySessionRecord, startupCfg startupconfig.FileConfig, hostState swarmruntime.LocalState) string {
	transportMode := normalizeRemoteTransportMode(record.TransportMode)
	remoteAdvertiseHost := strings.TrimSpace(record.RemoteAdvertiseHost)
	cfg := startupconfig.Default(remoteStartupConfigPath(record))
	cfg.Mode = startupconfig.ModeBox
	cfg.Host = firstNonEmpty(remoteAdvertiseHost, startupconfig.DefaultHost)
	cfg.Port = startupconfig.DefaultPort
	cfg.AdvertiseHost = firstNonEmpty(remoteAdvertiseHost, startupconfig.DefaultHost)
	cfg.AdvertisePort = startupconfig.DefaultPort
	cfg.DesktopPort = startupconfig.DefaultDesktopPort
	cfg.SwarmMode = true
	cfg.Child = true
	cfg.NetworkMode = transportMode
	cfg.TailscaleURL = firstNonEmpty(map[bool]string{true: strings.TrimSpace(record.MasterEndpoint)}[transportMode == startupconfig.NetworkModeTailscale])
	cfg.BypassPermissions = record.BypassPermissions
	cfg.SwarmName = strings.TrimSpace(record.Name)
	cfg.ParentSwarmID = strings.TrimSpace(hostState.Node.SwarmID)
	cfg.PairingState = startupconfig.PairingStateBootstrapReady
	cfg.RemoteDeploy = startupconfig.RemoteDeployBootstrap{
		Enabled:           true,
		SessionID:         strings.TrimSpace(record.ID),
		HostAPIBaseURL:    strings.TrimSpace(record.MasterEndpoint),
		HostDesktopURL:    strings.TrimSpace(record.MasterEndpoint),
		SyncEnabled:       record.SyncEnabled,
		SyncMode:          strings.TrimSpace(record.SyncMode),
		SyncOwnerSwarmID:  strings.TrimSpace(record.SyncOwnerSwarmID),
		SyncCredentialURL: firstNonEmpty(strings.TrimSpace(record.SyncCredentialURL), buildRemoteSyncCredentialURL(strings.TrimSpace(record.MasterEndpoint), strings.TrimSpace(record.ID))),
	}
	return startupconfig.Format(cfg)
}

func (s *Service) prepareRemoteBundle(ctx context.Context, workDir string, record *pebblestore.RemoteDeploySessionRecord) error {
	_ = ctx
	if record == nil {
		return fmt.Errorf("remote deploy record is required")
	}
	bundleDir := filepath.Join(workDir, "bundle")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		return err
	}
	remoteBundleDir := filepath.Join(bundleDir, "remote")
	if err := os.MkdirAll(remoteBundleDir, 0o755); err != nil {
		return err
	}
	for _, payload := range record.Payloads {
		if payload.ArchiveName == "" || payload.SourcePath == "" {
			continue
		}
		archivePath := filepath.Join(remoteBundleDir, payload.ArchiveName)
		if err := createGitTrackedArchive(payload.SourcePath, archivePath); err != nil {
			return err
		}
		for _, directory := range payload.Directories {
			if directory.ArchiveName == "" || directory.SourcePath == "" {
				continue
			}
			archivePath := filepath.Join(remoteBundleDir, directory.ArchiveName)
			if err := createGitTrackedArchive(directory.SourcePath, archivePath); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) copyRemoteBundle(ctx context.Context, workDir string, runtimeArtifact remoteRuntimeArtifact, record *pebblestore.RemoteDeploySessionRecord) error {
	if record == nil {
		return fmt.Errorf("remote deploy record is required")
	}
	remoteDir := firstNonEmpty(strings.TrimSpace(record.RemoteRoot), remoteRoot(record.ID))
	if _, err := runSSHCommand(ctx, record.SSHSessionTarget, fmt.Sprintf("mkdir -p %s", shellQuote(remoteDir))); err != nil {
		return err
	}
	sourceRoot := filepath.Join(workDir, "bundle", "remote")
	entries, err := os.ReadDir(sourceRoot)
	if err != nil {
		return fmt.Errorf("read remote bundle: %w", err)
	}
	if len(entries) > 0 {
		sourceDir := sourceRoot + string(filepath.Separator) + "."
		dest := fmt.Sprintf("%s:%s/", record.SSHSessionTarget, remoteDir)
		cmd := exec.CommandContext(ctx, "scp", "-r", sourceDir, dest)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("scp remote bundle: %w: %s", err, strings.TrimSpace(string(output)))
		}
	}
	if runtimeArtifact.RemoteImagePresent || !remoteImageUsesArchive(runtimeArtifact.ImageRef) {
		return nil
	}
	if strings.TrimSpace(runtimeArtifact.ArchivePath) == "" {
		return fmt.Errorf("remote image archive path is required")
	}
	archiveName := firstNonEmpty(strings.TrimSpace(runtimeArtifact.ArchiveName), remoteImageArchiveName(record.TransportMode))
	archiveDest := fmt.Sprintf("%s:%s", record.SSHSessionTarget, filepath.ToSlash(filepath.Join(remoteDir, archiveName)))
	cmd := exec.CommandContext(ctx, "scp", runtimeArtifact.ArchivePath, archiveDest)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp remote image archive: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (s *Service) startRemoteBundle(ctx context.Context, record *pebblestore.RemoteDeploySessionRecord, childCfgText string, tailscaleAuthKey string, syncVaultPassword string) (output, authURL, remoteEndpoint string, err error) {
	if record == nil {
		return "", "", "", fmt.Errorf("remote deploy record is required")
	}
	cmd, err := remoteBundleStartScript(record, childCfgText, tailscaleAuthKey, syncVaultPassword)
	if err != nil {
		return "", "", "", err
	}
	output, err = runSSHCommand(ctx, record.SSHSessionTarget, cmd)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "TAILSCALE_AUTH_URL="):
			authURL = strings.TrimSpace(strings.TrimPrefix(line, "TAILSCALE_AUTH_URL="))
		case strings.HasPrefix(line, "SWARM_REMOTE_URL="):
			remoteEndpoint = strings.TrimSpace(strings.TrimPrefix(line, "SWARM_REMOTE_URL="))
		case strings.HasPrefix(line, "SWARM_TAILNET_URL="):
			remoteEndpoint = strings.TrimSpace(strings.TrimPrefix(line, "SWARM_TAILNET_URL="))
		}
	}
	return output, authURL, remoteEndpoint, err
}

func mapRemoteContainerPackageManifest(input ContainerPackageManifest) pebblestore.ContainerPackageManifestRecord {
	packages := make([]pebblestore.ContainerPackageSelectionRecord, 0, len(input.Packages))
	for _, pkg := range input.Packages {
		packages = append(packages, pebblestore.ContainerPackageSelectionRecord{
			Name:   strings.TrimSpace(pkg.Name),
			Source: strings.TrimSpace(pkg.Source),
			Reason: strings.TrimSpace(pkg.Reason),
		})
	}
	return pebblestore.ContainerPackageManifestRecord{
		BaseImage:      strings.TrimSpace(input.BaseImage),
		PackageManager: strings.TrimSpace(input.PackageManager),
		Packages:       packages,
	}
}

func mapRemoteStoredContainerPackageManifest(input pebblestore.ContainerPackageManifestRecord) ContainerPackageManifest {
	packages := make([]ContainerPackageSelection, 0, len(input.Packages))
	for _, pkg := range input.Packages {
		packages = append(packages, ContainerPackageSelection{
			Name:   strings.TrimSpace(pkg.Name),
			Source: strings.TrimSpace(pkg.Source),
			Reason: strings.TrimSpace(pkg.Reason),
		})
	}
	return ContainerPackageManifest{
		BaseImage:      strings.TrimSpace(input.BaseImage),
		PackageManager: strings.TrimSpace(input.PackageManager),
		Packages:       packages,
	}
}

func mapSession(record pebblestore.RemoteDeploySessionRecord) Session {
	payloads := make([]SessionPayload, 0, len(record.Payloads))
	for _, payload := range record.Payloads {
		directories := make([]SessionPayloadDirectory, 0, len(payload.Directories))
		for _, directory := range payload.Directories {
			directories = append(directories, SessionPayloadDirectory{
				SourcePath:    directory.SourcePath,
				TargetPath:    directory.TargetPath,
				GitRoot:       directory.GitRoot,
				ArchiveName:   directory.ArchiveName,
				IncludedFiles: directory.IncludedFiles,
				IncludedBytes: directory.IncludedBytes,
				ExcludedNote:  directory.ExcludedNote,
			})
		}
		payloads = append(payloads, SessionPayload{
			ID:            payload.ID,
			SourcePath:    payload.SourcePath,
			WorkspacePath: payload.WorkspacePath,
			WorkspaceName: payload.WorkspaceName,
			TargetPath:    payload.TargetPath,
			Mode:          payload.Mode,
			Directories:   directories,
			GitRoot:       payload.GitRoot,
			ArchiveName:   payload.ArchiveName,
			IncludedFiles: payload.IncludedFiles,
			IncludedBytes: payload.IncludedBytes,
			ExcludedNote:  payload.ExcludedNote,
		})
	}
	preflight := SessionPreflight{
		PathID:                  PathSessionPreflight,
		BuilderRuntime:          record.BuilderRuntime,
		RemoteRuntime:           record.RemoteRuntime,
		ImageDeliveryMode:       record.ImageDeliveryMode,
		ImagePrefix:             record.ImagePrefix,
		SSHReachable:            record.SSHReachable,
		SystemdAvailable:        record.SystemdAvailable,
		SystemdUnit:             record.SystemdUnit,
		RemoteRoot:              record.RemoteRoot,
		RemoteNetworkCandidates: append([]string(nil), record.RemoteNetworkCandidates...),
		RemoteDisk: RemoteDiskInfo{
			Path:           record.RemoteDisk.Path,
			AvailableBytes: record.RemoteDisk.AvailableBytes,
			RequiredBytes:  record.RemoteDisk.RequiredBytes,
		},
		FilesToCopy: append([]string(nil), record.FilesToCopy...),
		Payloads:    payloads,
		Summary:     remotePreflightSummary(record),
		Checks: []string{
			"local runtime bundle assets available",
			"remote SSH reachable",
			"remote home directory resolved",
			"target remote install path does not already exist",
			"remote container runtime is available",
		},
	}
	return Session{
		ID:                  record.ID,
		Name:                record.Name,
		Status:              record.Status,
		SSHSessionTarget:    record.SSHSessionTarget,
		TransportMode:       record.TransportMode,
		MasterEndpoint:      record.MasterEndpoint,
		RemoteEndpoint:      remoteSessionEndpoint(record),
		RemoteAdvertiseHost: record.RemoteAdvertiseHost,
		GroupID:             record.GroupID,
		GroupName:           record.GroupName,
		BuilderRuntime:      record.BuilderRuntime,
		RemoteRuntime:       record.RemoteRuntime,
		ImageDeliveryMode:   record.ImageDeliveryMode,
		ImagePrefix:         record.ImagePrefix,
		MasterTailscaleURL:  record.MasterTailscaleURL,
		RemoteAuthURL:       record.RemoteAuthURL,
		RemoteTailnetURL:    record.RemoteTailnetURL,
		ImageRef:            record.ImageRef,
		ImageSignature:      record.ImageSignature,
		ImageArchiveBytes:   record.ImageArchiveBytes,
		EnrollmentID:        record.EnrollmentID,
		EnrollmentStatus:    record.EnrollmentStatus,
		ChildSwarmID:        record.ChildSwarmID,
		ChildName:           record.ChildName,
		HostSwarmID:         record.HostSwarmID,
		HostName:            record.HostName,
		HostPublicKey:       record.HostPublicKey,
		HostFingerprint:     record.HostFingerprint,
		HostAPIBaseURL:      record.HostAPIBaseURL,
		HostDesktopURL:      record.HostDesktopURL,
		BypassPermissions:   record.BypassPermissions,
		ContainerPackages:   mapRemoteStoredContainerPackageManifest(record.ContainerPackages),
		LastError:           record.LastError,
		LastRemoteOutput:    record.LastRemoteOutput,
		SyncEnabled:         record.SyncEnabled,
		SyncMode:            record.SyncMode,
		SyncOwnerSwarmID:    record.SyncOwnerSwarmID,
		Preflight:           preflight,
		CreatedAt:           record.CreatedAt,
		UpdatedAt:           record.UpdatedAt,
		ApprovedAt:          record.ApprovedAt,
		AttachedAt:          record.AttachedAt,
	}
}

func remotePreflightSummary(record pebblestore.RemoteDeploySessionRecord) string {
	target := firstNonEmpty(record.SSHSessionTarget, "remote host")
	transport := firstNonEmpty(strings.TrimSpace(record.TransportMode), startupconfig.NetworkModeTailscale)
	workspaceSummary := "selected workspace payloads"
	if len(record.Payloads) == 0 {
		workspaceSummary = "no workspace payloads"
	}
	diskSummary := ""
	if record.RemoteDisk.AvailableBytes > 0 && record.RemoteDisk.RequiredBytes > 0 {
		diskSummary = fmt.Sprintf(" Remote disk check: %s available, %s required.", formatByteCount(record.RemoteDisk.AvailableBytes), formatByteCount(record.RemoteDisk.RequiredBytes))
	}
	if normalizeRemoteImageDeliveryMode(record.ImageDeliveryMode) == remoteImageDeliveryRegistry {
		imageRef := firstNonEmpty(strings.TrimSpace(record.ImageRef), remoteImageRef(record.ImagePrefix, "published"))
		return fmt.Sprintf("Prepare %s locally, copy only payload archives over SSH to %s, have the remote host pull %s there, launch the remote child container, and wait for child approval over %s.%s", workspaceSummary, target, imageRef, transport, diskSummary)
	}
	return fmt.Sprintf("Prepare the prepackaged Swarm remote image locally, copy it over SSH to %s when needed, launch the remote child container there now, and wait for child approval over %s.%s", target, transport, diskSummary)
}

func formatByteCount(bytes int64) string {
	if bytes < 0 {
		bytes = 0
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	value := float64(bytes)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", bytes, units[unit])
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

func (s *Service) cleanupRemoteChildState(childSwarmID string, item *localcontainers.DeleteItemResult) error {
	childSwarmID = strings.TrimSpace(childSwarmID)
	if childSwarmID == "" {
		return nil
	}
	if s.swarmStore != nil {
		memberships, err := s.swarmStore.ListGroupMembershipsBySwarm(childSwarmID, 500)
		if err != nil {
			return err
		}
		for _, membership := range memberships {
			if err := s.swarmStore.DeleteGroupMembership(membership.GroupID, membership.SwarmID); err != nil {
				return err
			}
			item.RemovedGroupMemberships++
		}
		if err := s.swarmStore.DeleteTrustedPeer(childSwarmID); err != nil {
			return err
		}
		item.RemovedTrustedPeer = true
	}
	if s.auth != nil {
		if _, err := s.auth.DeleteManagedCredentialsByOwnerSwarmID(childSwarmID); err != nil {
			return err
		}
	}
	if s.workspace != nil {
		if err := s.workspace.RemoveReplicationLinksByTargetSwarmID(childSwarmID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) teardownRemoteChildInstall(ctx context.Context, record pebblestore.RemoteDeploySessionRecord) error {
	target := strings.TrimSpace(record.SSHSessionTarget)
	if target == "" {
		return fmt.Errorf("remote ssh target is required for SSH teardown")
	}
	runtimeName := normalizeRemoteDeployRuntime(record.RemoteRuntime)
	if runtimeName == "" {
		runtimeName = "docker"
	}
	remoteRoot := strings.TrimSpace(record.RemoteRoot)
	containerName := remoteContainerNameForSession(record.ID)
	sudoPrefix := ""
	if strings.TrimSpace(record.SudoMode) == "sudo" {
		sudoPrefix = "sudo "
	}
	cmd := fmt.Sprintf(`set -eu
remote_root=%s
runtime=%s
if [ "$runtime" = "podman" ]; then
  %spodman rm -f %s >/dev/null 2>&1 || true
else
  %sdocker rm -f %s >/dev/null 2>&1 || true
fi
if [ -n "$remote_root" ]; then
  %srm -rf "$remote_root" >/dev/null 2>&1 || true
fi
printf 'REMOTE_DELETE_OK=1\n'
`, shellQuote(remoteRoot), shellQuote(runtimeName), sudoPrefix, shellQuote(containerName), sudoPrefix, shellQuote(containerName), sudoPrefix)
	output, err := runSSHCommand(ctx, target, cmd)
	if err != nil {
		return err
	}
	if !strings.Contains(output, "REMOTE_DELETE_OK=1") {
		return fmt.Errorf("remote SSH teardown on %s did not complete", target)
	}
	return nil
}

func buildRemoteSyncCredentialURL(hostAPIBaseURL, sessionID string) string {
	hostAPIBaseURL = strings.TrimRight(strings.TrimSpace(hostAPIBaseURL), "/")
	sessionID = strings.TrimSpace(sessionID)
	if hostAPIBaseURL == "" || sessionID == "" {
		return ""
	}
	return hostAPIBaseURL + "/v1/deploy/remote/session/sync/credentials"
}

func (s *Service) requireManagedSyncVaultPassword(syncVaultPassword string) error {
	if s == nil || s.auth == nil {
		return nil
	}
	vaultStatus, err := s.auth.VaultStatus()
	if err != nil {
		return err
	}
	if vaultStatus.Enabled && strings.TrimSpace(syncVaultPassword) == "" {
		return fmt.Errorf("vault password is required to sync from a vaulted host")
	}
	return nil
}

func systemdUnitName(sessionID string) string {
	return fmt.Sprintf("swarm-remote-child-%s.service", sanitizeSlug(sessionID))
}

func remoteContainerNameForSession(sessionID string) string {
	slug := sanitizeSlug(sessionID)
	if slug == "" {
		return remoteContainerPrefix
	}
	return fmt.Sprintf("%s-%s", remoteContainerPrefix, slug)
}

func remoteRoot(sessionID string) string {
	// Keep the managed remote root short enough for swarmd's Unix socket paths.
	return filepath.ToSlash(filepath.Join("~/.local/share/swarm/rd", sanitizeSlug(sessionID)))
}

func remoteRootForHome(homeDir, sessionID string) string {
	homeDir = strings.TrimSpace(homeDir)
	if homeDir == "" {
		return remoteRoot(sessionID)
	}
	if filepath.Clean(homeDir) == "/root" {
		return filepath.ToSlash(filepath.Join("/var/lib/swarm/rd", sanitizeSlug(sessionID)))
	}
	return filepath.ToSlash(filepath.Join(homeDir, ".local", "share", "swarm", "rd", sanitizeSlug(sessionID)))
}

func copyRemoteRuntimeArtifact(ctx context.Context, runtimeArtifact remoteRuntimeArtifact, record pebblestore.RemoteDeploySessionRecord) error {
	if runtimeArtifact.RemoteImagePresent || !remoteImageUsesArchive(runtimeArtifact.ImageRef) {
		return nil
	}
	if strings.TrimSpace(runtimeArtifact.ArchivePath) == "" {
		return fmt.Errorf("remote image archive path is required")
	}
	remoteDir := firstNonEmpty(strings.TrimSpace(record.RemoteRoot), remoteRoot(record.ID))
	if _, err := runSSHCommand(ctx, record.SSHSessionTarget, fmt.Sprintf("mkdir -p %s", shellQuote(remoteDir))); err != nil {
		return err
	}
	archiveName := firstNonEmpty(strings.TrimSpace(runtimeArtifact.ArchiveName), remoteImageArchiveName(record.TransportMode))
	archiveDest := fmt.Sprintf("%s:%s", record.SSHSessionTarget, filepath.ToSlash(filepath.Join(remoteDir, archiveName)))
	cmd := exec.CommandContext(ctx, "scp", runtimeArtifact.ArchivePath, archiveDest)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp remote image archive: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

type remoteDevReplacementResult struct {
	State            string
	Reason           string
	PreviousImageRef string
	RemoteEndpoint   string
	Error            string
}

func remoteSessionContainerActive(ctx context.Context, record pebblestore.RemoteDeploySessionRecord) (bool, error) {
	containerName := remoteContainerNameForSession(record.ID)
	cmd := fmt.Sprintf(`set -eu
runtime=%s
container_name=%s
use_sudo=%s
as_root() {
  if [ "$use_sudo" = "1" ]; then
    sudo "$@"
  else
    "$@"
  fi
}
runtime_cmd() {
  if [ "$runtime" = "podman" ]; then
    as_root podman "$@"
  else
    as_root docker "$@"
  fi
}
if running="$(runtime_cmd inspect -f '{{.State.Running}}' "$container_name" 2>/dev/null)" && [ "$running" = "true" ]; then
  printf 'REMOTE_ACTIVE=1\n'
else
  printf 'REMOTE_ACTIVE=0\n'
fi
`, shellQuote(normalizeRemoteDeployRuntime(record.RemoteRuntime)), shellQuote(containerName), shellQuote(map[bool]string{true: "1", false: "0"}[strings.TrimSpace(record.SudoMode) == "sudo"]))
	output, err := runSSHCommand(ctx, record.SSHSessionTarget, cmd)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "REMOTE_ACTIVE=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "REMOTE_ACTIVE=")) == "1", nil
		}
	}
	return false, nil
}

func runRemoteDevReplacement(ctx context.Context, record pebblestore.RemoteDeploySessionRecord, runtimeArtifact remoteRuntimeArtifact) (remoteDevReplacementResult, error) {
	cmd := remoteDevReplacementScript(record, runtimeArtifact)
	output, err := runSSHCommand(ctx, record.SSHSessionTarget, cmd)
	result := parseRemoteDevReplacementOutput(output)
	if err != nil {
		if result.Error == "" {
			result.Error = err.Error()
		}
		return result, err
	}
	return result, nil
}

func parseRemoteDevReplacementOutput(output string) remoteDevReplacementResult {
	var result remoteDevReplacementResult
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "REMOTE_UPDATE_STATE="):
			result.State = strings.TrimSpace(strings.TrimPrefix(line, "REMOTE_UPDATE_STATE="))
		case strings.HasPrefix(line, "REMOTE_UPDATE_REASON="):
			result.Reason = strings.TrimSpace(strings.TrimPrefix(line, "REMOTE_UPDATE_REASON="))
		case strings.HasPrefix(line, "REMOTE_UPDATE_PREVIOUS_IMAGE="):
			result.PreviousImageRef = strings.TrimSpace(strings.TrimPrefix(line, "REMOTE_UPDATE_PREVIOUS_IMAGE="))
		case strings.HasPrefix(line, "REMOTE_UPDATE_ENDPOINT="):
			result.RemoteEndpoint = strings.TrimSpace(strings.TrimPrefix(line, "REMOTE_UPDATE_ENDPOINT="))
		case strings.HasPrefix(line, "REMOTE_UPDATE_ERROR="):
			result.Error = strings.TrimSpace(strings.TrimPrefix(line, "REMOTE_UPDATE_ERROR="))
		}
	}
	return result
}

func remoteDevReplacementScript(record pebblestore.RemoteDeploySessionRecord, runtimeArtifact remoteRuntimeArtifact) string {
	remoteRoot := firstNonEmpty(strings.TrimSpace(record.RemoteRoot), remoteRoot(record.ID))
	remoteStateRoot := filepath.ToSlash(filepath.Join(remoteRoot, "state"))
	tailscaleStateDir := filepath.ToSlash(filepath.Join(remoteStateRoot, "tailscale"))
	swarmdStateDir := filepath.ToSlash(filepath.Join(remoteStateRoot, "swarmd"))
	configHome := filepath.ToSlash(filepath.Join(remoteRoot, "config"))
	logDir := filepath.ToSlash(filepath.Join(remoteRoot, "logs"))
	logFile := remoteServiceLogPath(record)
	startScriptPath := filepath.ToSlash(filepath.Join(remoteRoot, "run-remote-child.sh"))
	backupStartScriptPath := filepath.ToSlash(filepath.Join(remoteRoot, "run-remote-child.sh.swarm-update-old"))
	pidFile := filepath.ToSlash(filepath.Join(remoteRoot, "run-remote-child.pid"))
	transportMode := normalizeRemoteTransportMode(record.TransportMode)
	runtimeName := normalizeRemoteDeployRuntime(record.RemoteRuntime)
	remoteAdvertiseHost := firstNonEmpty(strings.TrimSpace(record.RemoteAdvertiseHost), "127.0.0.1")
	listenAddr := firstNonEmpty(map[bool]string{true: net.JoinHostPort(remoteAdvertiseHost, strconv.Itoa(startupconfig.DefaultPort))}[transportMode == startupconfig.NetworkModeLAN], "127.0.0.1:7781")
	offlineMode := "0"
	bootstrapOutputPrefix := "SWARM_TAILNET_URL"
	if transportMode == startupconfig.NetworkModeLAN {
		offlineMode = "1"
		bootstrapOutputPrefix = "SWARM_REMOTE_URL"
	}
	useSudo := "0"
	if strings.TrimSpace(record.SudoMode) == "sudo" {
		useSudo = "1"
	}
	containerName := remoteContainerNameForSession(record.ID)
	backupName := containerName + "-swarm-update-old"
	imageArchiveName := remoteImageArchiveName(transportMode)
	useArchiveImage := "0"
	if remoteImageUsesArchive(runtimeArtifact.ImageRef) {
		useArchiveImage = "1"
	}
	mountTargets := []string{remoteRoot}
	seen := map[string]struct{}{remoteRoot: {}}
	appendMount := func(path string) {
		path = firstNonEmpty(strings.TrimSpace(path), "/workspaces")
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		mountTargets = append(mountTargets, path)
	}
	for _, payload := range record.Payloads {
		appendMount(payload.TargetPath)
		for _, directory := range payload.Directories {
			appendMount(directory.TargetPath)
		}
	}
	mountArgs := ""
	for _, targetPath := range mountTargets {
		mountArgs += fmt.Sprintf("run_args+=(--volume %s)\n", shellQuote(targetPath+":"+targetPath))
	}
	return fmt.Sprintf(`set -euo pipefail
remote_root=%s
config_home=%s
tailscale_state_dir=%s
swarmd_state_dir=%s
log_dir=%s
log_file=%s
start_script=%s
backup_start_script=%s
pid_file=%s
use_sudo=%s
runtime=%s
image_ref=%s
image_archive=%s
use_archive_image=%s
container_name=%s
backup_name=%s
transport_mode=%s
remote_advertise_host=%s
listen_addr=%s
offline_mode=%s
ts_hostname=%s
mkdir -p "$remote_root" "$config_home/swarm" "$tailscale_state_dir" "$swarmd_state_dir" "$log_dir" "$remote_root/xdg/data" "$remote_root/xdg/state"
cd "$remote_root"
as_root() {
  if [ "$use_sudo" = "1" ]; then
    sudo "$@"
  else
    "$@"
  fi
}
runtime_cmd() {
  if [ "$runtime" = "podman" ]; then
    as_root podman "$@"
  else
    as_root docker "$@"
  fi
}
if ! runtime_cmd inspect -f '{{.State.Running}}' "$container_name" 2>/dev/null | grep -qx true; then
  printf 'REMOTE_UPDATE_STATE=skipped\nREMOTE_UPDATE_REASON=not-active\n'
  exit 0
fi
previous_image="$(runtime_cmd inspect -f '{{.Config.Image}}' "$container_name" 2>/dev/null || true)"
printf 'REMOTE_UPDATE_PREVIOUS_IMAGE=%%s\n' "$previous_image"
if [ "$previous_image" = "$image_ref" ]; then
  printf 'REMOTE_UPDATE_STATE=skipped\nREMOTE_UPDATE_REASON=already-current\n'
  exit 0
fi
if ! runtime_cmd image inspect "$image_ref" >/dev/null 2>&1; then
  if [ "$use_archive_image" = "1" ] && [ -f "$image_archive" ]; then
    runtime_cmd load -i "$image_archive" >/dev/null
  else
    runtime_cmd pull "$image_ref" >/dev/null
  fi
fi
cp "$start_script" "$backup_start_script" 2>/dev/null || true
cat > "$start_script" <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
remote_root=%s
config_home=%s
tailscale_state_dir=%s
swarmd_state_dir=%s
runtime=%s
image_ref=%s
container_name=%s
listen_addr=%s
offline_mode=%s
ts_hostname=%s
mkdir -p "$tailscale_state_dir" "$swarmd_state_dir" "$remote_root/xdg/data" "$remote_root/xdg/state"
export XDG_CONFIG_HOME="$config_home"
export XDG_DATA_HOME="$remote_root/xdg/data"
export XDG_STATE_HOME="$remote_root/xdg/state"
export TS_SOCKET="$tailscale_state_dir/tailscaled.sock"
export TS_STATE_DIR="$tailscale_state_dir"
export TS_OUTBOUND_HTTP_PROXY_LISTEN="127.0.0.1:1055"
export TS_TUN_MODE=userspace-networking
export SWARM_TAILSCALE_OUTBOUND_PROXY="http://127.0.0.1:1055"
export SWARMD_DATA_DIR="$swarmd_state_dir"
export SWARMD_LOCK_PATH="$swarmd_state_dir/swarmd.lock"
export SWARMD_LISTEN="$listen_addr"
export SWARM_DESKTOP_PORT="5555"
export SWARM_CONTAINER_OFFLINE="$offline_mode"
export TS_HOSTNAME="$ts_hostname"
if [ "$runtime" = "podman" ]; then
  runtime_bin=podman
else
  runtime_bin=docker
fi
run_args=(run --rm --name "$container_name" --network host
  -e "XDG_CONFIG_HOME=$config_home"
  -e "XDG_DATA_HOME=$remote_root/xdg/data"
  -e "XDG_STATE_HOME=$remote_root/xdg/state"
  -e "TS_SOCKET=$tailscale_state_dir/tailscaled.sock"
  -e "TS_STATE_DIR=$tailscale_state_dir"
  -e "TS_OUTBOUND_HTTP_PROXY_LISTEN=127.0.0.1:1055"
  -e "TS_TUN_MODE=userspace-networking"
  -e "SWARM_TAILSCALE_OUTBOUND_PROXY=http://127.0.0.1:1055"
  -e "SWARMD_DATA_DIR=$swarmd_state_dir"
  -e "SWARMD_LOCK_PATH=$swarmd_state_dir/swarmd.lock"
  -e "SWARMD_LISTEN=$listen_addr"
  -e "SWARM_DESKTOP_PORT=5555"
  -e "SWARM_CONTAINER_OFFLINE=$offline_mode"
  -e "TS_HOSTNAME=$ts_hostname"
)
run_args+=(--volume "$remote_root:$remote_root")
%srun_args+=("$image_ref")
exec "$runtime_bin" "${run_args[@]}"
SCRIPT
chmod 0755 "$start_script"
runtime_cmd rm -f "$backup_name" >/dev/null 2>&1 || true
runtime_cmd stop "$container_name" >/dev/null 2>&1 || true
if runtime_cmd inspect -f '{{.State.Running}}' "$container_name" 2>/dev/null | grep -qx true; then
  cp "$backup_start_script" "$start_script" 2>/dev/null || true
  printf 'REMOTE_UPDATE_ERROR=stop-old-failed\n'
  exit 1
fi
if ! runtime_cmd rename "$container_name" "$backup_name" >/dev/null 2>&1; then
  cp "$backup_start_script" "$start_script" 2>/dev/null || true
  runtime_cmd start "$container_name" >/dev/null 2>&1 || true
  printf 'REMOTE_UPDATE_ERROR=rename-old-failed\n'
  exit 1
fi
rm -f "$pid_file"
if [ "$use_sudo" = "1" ]; then
  nohup sudo -E /bin/bash "$start_script" >"$log_file" 2>&1 < /dev/null &
else
  nohup /bin/bash "$start_script" >"$log_file" 2>&1 < /dev/null &
fi
echo $! >"$pid_file"
remote_url=""
deadline=$((SECONDS + 90))
while :; do
  if [ "$transport_mode" = "lan" ]; then
    if runtime_cmd exec "$container_name" sh -lc "curl -fsS http://${remote_advertise_host}:7781/readyz >/dev/null 2>&1 || curl -fsS http://${remote_advertise_host}:7781/healthz >/dev/null 2>&1" >/dev/null 2>&1; then
      remote_url="http://${remote_advertise_host}:7781"
      break
    fi
  elif [ -s "$log_file" ]; then
    remote_url="$(sed -n 's/^SWARM_TAILNET_URL=//p' "$log_file" | tail -n 1)"
    if [ -n "$remote_url" ]; then
      break
    fi
  fi
  if ! runtime_cmd inspect -f '{{.State.Running}}' "$container_name" 2>/dev/null | grep -qx true; then
    break
  fi
  if [ "${SECONDS}" -ge "$deadline" ]; then
    break
  fi
  sleep 1
done
if ! runtime_cmd inspect -f '{{.State.Running}}' "$container_name" 2>/dev/null | grep -qx true; then
  runtime_cmd rm -f "$container_name" >/dev/null 2>&1 || true
  runtime_cmd rename "$backup_name" "$container_name" >/dev/null 2>&1 || true
  cp "$backup_start_script" "$start_script" 2>/dev/null || true
  runtime_cmd start "$container_name" >/dev/null 2>&1 || true
  printf 'REMOTE_UPDATE_ERROR=replacement-not-running\n'
  exit 1
fi
if [ -z "$remote_url" ]; then
  runtime_cmd rm -f "$container_name" >/dev/null 2>&1 || true
  runtime_cmd rename "$backup_name" "$container_name" >/dev/null 2>&1 || true
  cp "$backup_start_script" "$start_script" 2>/dev/null || true
  runtime_cmd start "$container_name" >/dev/null 2>&1 || true
  printf 'REMOTE_UPDATE_ERROR=replacement-not-ready\n'
  exit 1
fi
runtime_cmd rm -f "$backup_name" >/dev/null 2>&1 || true
rm -f "$backup_start_script"
printf 'REMOTE_UPDATE_STATE=replaced\n'
printf '%s=%%s\n' "$remote_url"
printf 'REMOTE_UPDATE_ENDPOINT=%%s\n' "$remote_url"
`, shellQuote(remoteRoot), shellQuote(configHome), shellQuote(tailscaleStateDir), shellQuote(swarmdStateDir), shellQuote(logDir), shellQuote(logFile), shellQuote(startScriptPath), shellQuote(backupStartScriptPath), shellQuote(pidFile), shellQuote(useSudo), shellQuote(runtimeName), shellQuote(strings.TrimSpace(runtimeArtifact.ImageRef)), shellQuote(imageArchiveName), shellQuote(useArchiveImage), shellQuote(containerName), shellQuote(backupName), shellQuote(transportMode), shellQuote(remoteAdvertiseHost), shellQuote(listenAddr), shellQuote(offlineMode), shellQuote(firstNonEmpty(strings.TrimSpace(record.Name), "swarm-box")), shellQuote(remoteRoot), shellQuote(configHome), shellQuote(tailscaleStateDir), shellQuote(swarmdStateDir), shellQuote(runtimeName), shellQuote(strings.TrimSpace(runtimeArtifact.ImageRef)), shellQuote(containerName), shellQuote(listenAddr), shellQuote(offlineMode), shellQuote(firstNonEmpty(strings.TrimSpace(record.Name), "swarm-box")), mountArgs, bootstrapOutputPrefix)
}

func remoteStartupConfigPath(record pebblestore.RemoteDeploySessionRecord) string {
	return filepath.ToSlash(filepath.Join(firstNonEmpty(strings.TrimSpace(record.RemoteRoot), remoteRoot(record.ID)), "config", "swarm", "swarm.conf"))
}

func remoteBootstrapSecretPath(record pebblestore.RemoteDeploySessionRecord) string {
	return filepath.ToSlash(startupconfig.RemoteDeployBootstrapSecretPath(remoteStartupConfigPath(record)))
}

func remoteServiceLogPath(record pebblestore.RemoteDeploySessionRecord) string {
	return filepath.ToSlash(filepath.Join(firstNonEmpty(strings.TrimSpace(record.RemoteRoot), remoteRoot(record.ID)), "logs", "remote-child.log"))
}

func normalizeRemoteDeployRuntime(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "podman":
		return "podman"
	default:
		return "docker"
	}
}

func normalizeRemoteTransportMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case startupconfig.NetworkModeLAN:
		return startupconfig.NetworkModeLAN
	default:
		return startupconfig.NetworkModeTailscale
	}
}

func resolveMasterRemoteDeployEndpoint(cfg startupconfig.FileConfig, transportMode string) (string, error) {
	switch normalizeRemoteTransportMode(transportMode) {
	case startupconfig.NetworkModeLAN:
		if isRemoteDeployLocalOnlyHost(cfg.Host) {
			return "", fmt.Errorf("master swarm.conf host is still loopback-only for LAN/WireGuard remote child deploy")
		}
		host := firstNonEmpty(strings.TrimSpace(cfg.AdvertiseHost), usableLANHost(strings.TrimSpace(cfg.Host)))
		if host == "" {
			return "", fmt.Errorf("master swarm.conf advertise_host is required for LAN/WireGuard remote child deploy")
		}
		port := cfg.AdvertisePort
		if port < 1 || port > 65535 {
			port = cfg.Port
		}
		return "http://" + net.JoinHostPort(host, strconv.Itoa(port)), nil
	default:
		endpoint := strings.TrimSpace(cfg.TailscaleURL)
		if endpoint == "" {
			return "", fmt.Errorf("master swarm.conf tailscale_url is required for remote child deploy")
		}
		return normalizeRemoteSwarmURL(endpoint, "https"), nil
	}
}

func usableLANHost(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	if lower == "127.0.0.1" || lower == "localhost" || lower == "0.0.0.0" || lower == "::1" {
		return ""
	}
	return value
}

func isRemoteDeployLocalOnlyHost(value string) bool {
	value = strings.TrimSpace(strings.Trim(value, "[]"))
	if value == "" {
		return false
	}
	if strings.EqualFold(value, "localhost") {
		return true
	}
	if parsed := net.ParseIP(value); parsed != nil {
		return parsed.IsLoopback()
	}
	return false
}

func normalizeRemoteAdvertiseHost(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.Contains(value, "://") {
		return "", fmt.Errorf("remote reachable host must not include a URL scheme")
	}
	if strings.Contains(value, "/") {
		return "", fmt.Errorf("remote reachable host must not contain path separators")
	}
	return value, nil
}

func defaultReachableSSHHostCandidate(target string) string {
	candidate := strings.TrimSpace(sshTargetHostCandidate(target))
	if candidate == "" {
		return ""
	}
	if strings.Contains(candidate, ":") || strings.Contains(candidate, ".") {
		return candidate
	}
	return ""
}

func sshTargetHostCandidate(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if at := strings.LastIndex(target, "@"); at >= 0 {
		target = strings.TrimSpace(target[at+1:])
	}
	if strings.HasPrefix(target, "[") {
		if end := strings.Index(target, "]"); end > 0 {
			return strings.TrimSpace(target[1:end])
		}
	}
	if colon := strings.LastIndex(target, ":"); colon > 0 && strings.Count(target, ":") == 1 {
		target = strings.TrimSpace(target[:colon])
	}
	return strings.TrimSpace(target)
}

func firstRemoteNetworkCandidate(values []string) string {
	for _, value := range sortRemoteNetworkCandidates(values) {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func sortRemoteNetworkCandidates(values []string) []string {
	normalized := make([]string, 0, len(values))
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
		normalized = append(normalized, trimmed)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		return remoteNetworkCandidateRank(normalized[i]) < remoteNetworkCandidateRank(normalized[j])
	})
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func remoteNetworkCandidateRank(value string) int {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return 3
	}
	if ip.IsPrivate() {
		return 0
	}
	if ip.IsLoopback() {
		return 4
	}
	return 1
}

func normalizeRemoteSwarmURL(value, defaultScheme string) string {
	value = strings.TrimSpace(strings.TrimSuffix(value, "/"))
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://") {
		return value
	}
	if defaultScheme == "" {
		defaultScheme = "https"
	}
	return defaultScheme + "://" + value
}

func remoteSessionEndpoint(record pebblestore.RemoteDeploySessionRecord) string {
	if endpoint := strings.TrimSpace(record.RemoteEndpoint); endpoint != "" {
		return endpoint
	}
	if endpoint := strings.TrimSpace(record.RemoteTailnetURL); endpoint != "" {
		return endpoint
	}
	return ""
}

func suggestedSessionID(name string) string {
	return sanitizeSlug(name)
}

func sanitizeSlug(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func hostRole(cfg startupconfig.FileConfig) string {
	if cfg.Child {
		return "child"
	}
	return "master"
}

func firstTransportPrimary(transports []remotePairingTransport) string {
	for _, transport := range transports {
		if primary := strings.TrimSpace(transport.Primary); primary != "" {
			return primary
		}
		for _, value := range transport.All {
			value = strings.TrimSpace(value)
			if value != "" {
				return value
			}
		}
	}
	return ""
}

func normalizeRemoteSessionDeleteIDs(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func remotePairingTransportsForMode(kind string, transports []swarmruntime.TransportSummary, fallbackEndpoint string) []remotePairingTransport {
	kind = strings.TrimSpace(kind)
	out := make([]remotePairingTransport, 0, len(transports))
	for _, transport := range transports {
		transportKind := strings.TrimSpace(transport.Kind)
		if kind != "" && !strings.EqualFold(transportKind, kind) {
			continue
		}
		values := dedupeRemotePairingTransportValues(append([]string{strings.TrimSpace(transport.Primary)}, transport.All...))
		if len(values) == 0 {
			continue
		}
		out = append(out, remotePairingTransport{
			Kind:    transportKind,
			Primary: firstNonEmpty(strings.TrimSpace(transport.Primary), values[0]),
			All:     values,
		})
	}
	if len(out) > 0 {
		return out
	}
	fallbackEndpoint = strings.TrimSpace(fallbackEndpoint)
	if fallbackEndpoint == "" {
		return nil
	}
	return []remotePairingTransport{{
		Kind:    firstNonEmpty(kind, startupconfig.NetworkModeTailscale),
		Primary: fallbackEndpoint,
		All:     []string{fallbackEndpoint},
	}}
}

func dedupeRemotePairingTransportValues(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func requireHostedGroupForLocalSwarm(state swarmruntime.LocalState, groupID string) error {
	groupID = strings.TrimSpace(groupID)
	localSwarmID := strings.TrimSpace(state.Node.SwarmID)
	if groupID == "" {
		return fmt.Errorf("group_id is required")
	}
	if localSwarmID == "" {
		return fmt.Errorf("local swarm id is not configured")
	}
	for _, group := range state.Groups {
		if !strings.EqualFold(strings.TrimSpace(group.Group.ID), groupID) {
			continue
		}
		for _, member := range group.Members {
			if !strings.EqualFold(strings.TrimSpace(member.SwarmID), localSwarmID) {
				continue
			}
			if strings.TrimSpace(member.MembershipRole) != swarmruntime.GroupMembershipRoleHost {
				return fmt.Errorf("only the host swarm can manage group %q", groupID)
			}
			return nil
		}
		return fmt.Errorf("group %q is not hosted by the local swarm", groupID)
	}
	return fmt.Errorf("group %q is not part of the current local swarm state", groupID)
}

func (s *Service) prepareRemoteRuntimeArtifact(ctx context.Context, builderRuntime, transportMode, imagePrefix string, manifest ContainerPackageManifest) (remoteRuntimeArtifact, error) {
	if !remoteImageUsesArchive(remoteImageRef(imagePrefix, "preflight")) {
		return prepareRemoteProductionRegistryArtifact(ctx)
	}
	buildRoot, err := resolveRemoteDeployBuildRoot(s.startupCWD)
	if err != nil {
		return remoteRuntimeArtifact{}, err
	}
	stepStartedAt := time.Now()
	if err := ensureRemoteDeployBackendBinaries(ctx, buildRoot); err != nil {
		logRemoteDeployTiming("start.prepare_runtime.build_backend_binaries", stepStartedAt, err)
		return remoteRuntimeArtifact{}, err
	}
	logRemoteDeployTiming("start.prepare_runtime.build_backend_binaries", stepStartedAt, nil)
	stepStartedAt = time.Now()
	signature, err := remoteImageSignature(buildRoot, transportMode, manifest)
	logRemoteDeployTiming("start.prepare_runtime.signature", stepStartedAt, err, "signature", signature)
	if err != nil {
		return remoteRuntimeArtifact{}, err
	}
	imageRef := remoteImageRef(imagePrefix, signature)
	if !remoteImageUsesArchive(imageRef) {
		return remoteRuntimeArtifact{
			Signature: signature,
			ImageRef:  imageRef,
		}, nil
	}
	builderRuntime = normalizeRemoteDeployRuntime(builderRuntime)
	if builderRuntime == "" {
		builderRuntime = "docker"
	}
	stepStartedAt = time.Now()
	imageRef, err = ensureRemoteDeployImageCurrent(ctx, buildRoot, builderRuntime, imagePrefix, signature, manifest)
	logRemoteDeployTiming("start.prepare_runtime.ensure_image", stepStartedAt, err, "builder_runtime", builderRuntime, "image_ref", imageRef, "signature", signature)
	if err != nil {
		return remoteRuntimeArtifact{}, err
	}
	cacheRoot, err := remoteDeployCacheRoot()
	if err != nil {
		return remoteRuntimeArtifact{}, err
	}
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return remoteRuntimeArtifact{}, err
	}
	archiveName := remoteImageArchiveName(transportMode)
	archivePath := filepath.Join(cacheRoot, fmt.Sprintf("swarm-image-%s-%s.tar", normalizeRemoteTransportMode(transportMode), signature))
	archiveHit := false
	if _, err := os.Stat(archivePath); err == nil {
		stepStartedAt = time.Now()
		if err := validateTarArchive(archivePath); err == nil {
			archiveHit = true
			logRemoteDeployTiming("start.prepare_runtime.validate_cached_archive", stepStartedAt, nil, "signature", signature)
		} else {
			logRemoteDeployTiming("start.prepare_runtime.validate_cached_archive", stepStartedAt, err, "signature", signature)
			_ = os.Remove(archivePath)
		}
	} else if !os.IsNotExist(err) {
		return remoteRuntimeArtifact{}, err
	} else {
		stepStartedAt = time.Now()
		if err := exportRemoteImageArchive(ctx, builderRuntime, imageRef, archivePath); err != nil {
			logRemoteDeployTiming("start.prepare_runtime.export_archive", stepStartedAt, err, "signature", signature, "image_ref", imageRef)
			return remoteRuntimeArtifact{}, err
		}
		logRemoteDeployTiming("start.prepare_runtime.export_archive", stepStartedAt, nil, "signature", signature, "image_ref", imageRef)
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		return remoteRuntimeArtifact{}, err
	}
	archiveBytes := info.Size()
	return remoteRuntimeArtifact{
		Signature:         signature,
		ImageRef:          imageRef,
		ArchiveName:       archiveName,
		ArchivePath:       archivePath,
		ArchiveBytes:      archiveBytes,
		RequiredDiskBytes: archiveBytes,
		ArchiveHit:        archiveHit,
	}, nil
}

func resolveRemoteDeployBuildRoot(startupCWD string) (string, error) {
	buildRoot := strings.TrimSpace(startupCWD)
	if buildRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve build root: %w", err)
		}
		buildRoot = cwd
	}
	buildRoot, err := filepath.Abs(buildRoot)
	if err != nil {
		return "", fmt.Errorf("resolve build root: %w", err)
	}
	containerfilePath := filepath.Join(buildRoot, "deploy", "container-mvp", "Containerfile")
	if _, err := os.Stat(containerfilePath); err != nil {
		return "", fmt.Errorf("container build assets missing: %w", err)
	}
	return buildRoot, nil
}

func remoteDeployCacheRoot() (string, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache dir: %w", err)
	}
	return filepath.Join(cacheRoot, "swarm", "remote-deploy"), nil
}

func exportRemoteImageArchive(ctx context.Context, runtimeName, imageRef, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	tempPath := fmt.Sprintf("%s.%d.tmp", destPath, os.Getpid())
	_ = os.Remove(tempPath)
	if err := createRemoteImageArchive(ctx, runtimeName, imageRef, tempPath); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := validateTarArchive(tempPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("validate image archive: %w", err)
	}
	if err := os.Rename(tempPath, destPath); err != nil {
		if _, statErr := os.Stat(destPath); statErr == nil {
			_ = os.Remove(tempPath)
			return nil
		}
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

func createRemoteImageArchive(ctx context.Context, runtimeName, imageRef, destPath string) error {
	runtimeName = normalizeRemoteDeployRuntime(runtimeName)
	if runtimeName == "" {
		return fmt.Errorf("container runtime is required to export remote image")
	}
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return fmt.Errorf("image ref is required to export remote image")
	}
	cmd := exec.CommandContext(ctx, runtimeName, "save", "-o", destPath, imageRef)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("export remote image archive %q with %s: %s", imageRef, runtimeName, message)
	}
	return nil
}

func validateTarArchive(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := tar.NewReader(file)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if header == nil || header.Size <= 0 {
			continue
		}
		if _, err := io.Copy(io.Discard, reader); err != nil {
			return err
		}
	}
}

func validateTarGzArchive(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if header == nil || header.Size <= 0 {
			continue
		}
		if _, err := io.Copy(io.Discard, reader); err != nil {
			return err
		}
	}
}

func prepareRemoteProductionRegistryArtifact(ctx context.Context) (remoteRuntimeArtifact, error) {
	metadata, err := localcontainers.FetchProductionImageMetadata(ctx)
	if err != nil {
		return remoteRuntimeArtifact{}, fmt.Errorf("fetch production swarm image metadata: %w", err)
	}
	imageRef := strings.TrimSpace(metadata.ImageDigestRef)
	if imageRef == "" {
		return remoteRuntimeArtifact{}, fmt.Errorf("release image metadata missing image digest ref")
	}
	return remoteRuntimeArtifact{
		Signature:         strings.TrimPrefix(imageRef, localcontainers.ProductionImagePrefix+"@"),
		ImageRef:          imageRef,
		RequiredDiskBytes: metadata.ImageSizeBytes,
	}, nil
}

func remoteProductionImageLabelChecks() []struct {
	label    string
	expected string
} {
	expectedVersion := strings.TrimSpace(buildinfo.DisplayVersion())
	expectedCommit := strings.TrimSpace(buildinfo.DisplayCommit())
	checks := []struct {
		label    string
		expected string
	}{
		{label: "org.opencontainers.image.source", expected: localcontainers.OfficialSourceRepository},
		{label: "org.opencontainers.image.version", expected: expectedVersion},
		{label: "swarmagent.image.contract", expected: localcontainers.OfficialImageContract},
		{label: "swarmagent.image.role", expected: "app"},
		{label: "swarmagent.version", expected: expectedVersion},
	}
	if expectedCommit != "" && expectedCommit != "unknown" {
		checks = append(checks,
			struct {
				label    string
				expected string
			}{label: "org.opencontainers.image.revision", expected: expectedCommit},
			struct {
				label    string
				expected string
			}{label: "swarmagent.commit", expected: expectedCommit},
		)
	}
	return checks
}

func remoteProductionImageVerificationScript() string {
	var builder strings.Builder
	for _, check := range remoteProductionImageLabelChecks() {
		if strings.TrimSpace(check.expected) == "" {
			continue
		}
		builder.WriteString(fmt.Sprintf("expected_label=%s\n", shellQuote(check.expected)))
		builder.WriteString(fmt.Sprintf("actual_label=$(runtime_cmd image inspect \"$image_ref\" --format '{{ index .Config.Labels %q }}' 2>/dev/null || true)\n", check.label))
		builder.WriteString("actual_label=$(printf '%s' \"$actual_label\" | tr -d '\\r' | sed -e 's/[[:space:]]*$//')\n")
		builder.WriteString(fmt.Sprintf("if [ \"$actual_label\" != \"$expected_label\" ]; then printf 'remote image label verification failed: %%s=%%s, expected %%s\\n' %s \"$actual_label\" \"$expected_label\" >&2; exit 1; fi\n", shellQuote(check.label)))
	}
	return builder.String()
}

func ensureRemoteDeployImageCurrent(ctx context.Context, buildRoot, builderRuntime, imagePrefix, signature string, manifest ContainerPackageManifest) (string, error) {
	builderRuntime = normalizeRemoteDeployRuntime(builderRuntime)
	if builderRuntime == "" {
		return "", fmt.Errorf("builder runtime is required")
	}
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return "", fmt.Errorf("remote image signature is required")
	}
	manifest = normalizeRemoteContainerPackageManifest(manifest)
	imageRef := remoteImageRef(imagePrefix, signature)
	exists, err := runtimeImageExistsLocal(ctx, builderRuntime, imageRef)
	if err != nil {
		return "", fmt.Errorf("check local remote image %q: %w", imageRef, err)
	}
	if exists {
		return imageRef, nil
	}
	if len(manifest.Packages) == 0 {
		if err := rebuildRemoteBaseImage(ctx, buildRoot, builderRuntime, imageRef); err != nil {
			return "", err
		}
		return imageRef, nil
	}
	baseSignature, err := remoteRuntimeSignature(buildRoot)
	if err != nil {
		return "", err
	}
	baseImageRef := remoteImageRef(imagePrefix, baseSignature)
	baseExists, err := runtimeImageExistsLocal(ctx, builderRuntime, baseImageRef)
	if err != nil {
		return "", fmt.Errorf("check local base remote image %q: %w", baseImageRef, err)
	}
	if !baseExists {
		if err := rebuildRemoteBaseImage(ctx, buildRoot, builderRuntime, baseImageRef); err != nil {
			return "", err
		}
	}
	if err := buildRemotePackageAwareImage(ctx, builderRuntime, buildRoot, imageRef, baseImageRef, manifest); err != nil {
		return "", err
	}
	return imageRef, nil
}

func rebuildRemoteBaseImage(ctx context.Context, buildRoot, builderRuntime, imageRef string) error {
	cmd := exec.CommandContext(ctx, "bash", filepath.Join("scripts", "rebuild-container.sh"), "--image-only")
	cmd.Dir = buildRoot
	cmd.Env = append(os.Environ(),
		"BUILD_RUNTIME="+builderRuntime,
		"IMAGE_NAME="+strings.TrimSpace(imageRef),
		"SWARM_REBUILD_REASON=remote-deploy-image-build",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("rebuild remote base image %q with %s: %s", imageRef, builderRuntime, message)
	}
	return nil
}

func buildRemotePackageAwareImage(ctx context.Context, runtimeName, buildRoot, imageRef, baseImageRef string, manifest ContainerPackageManifest) error {
	runtimeName = normalizeRemoteDeployRuntime(runtimeName)
	if runtimeName == "" {
		return fmt.Errorf("builder runtime is required")
	}
	manifest = normalizeRemoteContainerPackageManifest(manifest)
	if len(manifest.Packages) == 0 {
		return fmt.Errorf("package-aware image build requires at least one package")
	}
	packageNames := make([]string, 0, len(manifest.Packages))
	for _, pkg := range manifest.Packages {
		packageNames = append(packageNames, pkg.Name)
	}
	containerfile := fmt.Sprintf("FROM %s\nRUN apt-get update && apt-get install -y --no-install-recommends %s && rm -rf /var/lib/apt/lists/*\n", strings.TrimSpace(baseImageRef), strings.Join(packageNames, " "))
	cmd := exec.CommandContext(ctx, runtimeName, "build", "-t", strings.TrimSpace(imageRef), "-")
	cmd.Dir = buildRoot
	cmd.Stdin = strings.NewReader(containerfile)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("build remote package image %q: %s", imageRef, message)
	}
	return nil
}

func runtimeImageExistsLocal(parent context.Context, runtimeName, imageRef string) (bool, error) {
	runtimeName = normalizeRemoteDeployRuntime(runtimeName)
	imageRef = strings.TrimSpace(imageRef)
	if runtimeName == "" || imageRef == "" {
		return false, fmt.Errorf("runtime and image ref are required")
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	args := []string{"image", "inspect", imageRef}
	if runtimeName == "podman" {
		args = []string{"image", "exists", imageRef}
	}
	cmd := exec.CommandContext(ctx, runtimeName, args...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	return false, errors.New(message)
}

func ensureRemoteDeployBackendBinaries(ctx context.Context, buildRoot string) error {
	buildRoot = strings.TrimSpace(buildRoot)
	if buildRoot == "" {
		return fmt.Errorf("build root is required")
	}
	stageDir := filepath.Join(buildRoot, ".bin", "main")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return fmt.Errorf("prepare staged backend binary dir: %w", err)
	}
	cmd := exec.CommandContext(ctx, "bash", "swarmd/scripts/dev-build.sh")
	cmd.Dir = buildRoot
	cmd.Env = append(os.Environ(),
		"SWARMD_BUILD_HARD_RESTART=0",
		fmt.Sprintf("BIN_DIR=%s", stageDir),
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build remote deploy backend binaries: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func remoteRuntimeSignature(buildRoot string) (string, error) {
	h := sha256.New()
	inputs := []string{
		filepath.Join(buildRoot, "deploy", "container-mvp", "entrypoint.sh"),
		filepath.Join(buildRoot, ".bin", "main", "swarmd"),
		filepath.Join(buildRoot, ".bin", "main", "swarmctl"),
		filepath.Join(buildRoot, "swarmd", "internal", "fff", "lib", "linux-amd64-gnu", "libfff_c.so"),
		filepath.Join(buildRoot, ".tools", "go", "VERSION"),
		filepath.Join(buildRoot, ".tools", "go", "go.env"),
	}
	for _, path := range inputs {
		if err := hashFileWithPath(h, path, buildRoot); err != nil {
			return "", err
		}
	}
	if err := hashDirWithPath(h, filepath.Join(buildRoot, "web", "dist"), buildRoot); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

func remoteImageSignature(buildRoot, transportMode string, manifest ContainerPackageManifest) (string, error) {
	baseSignature, err := remoteRuntimeSignature(buildRoot)
	if err != nil {
		return "", err
	}
	payload := baseSignature + "\n" + normalizeRemoteTransportMode(transportMode) + "\n" + remoteContainerPackageSignaturePayload(manifest)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])[:16], nil
}

func remoteContainerPackageSignaturePayload(manifest ContainerPackageManifest) string {
	manifest = normalizeRemoteContainerPackageManifest(manifest)
	parts := []string{
		strings.TrimSpace(manifest.BaseImage),
		strings.TrimSpace(manifest.PackageManager),
	}
	for _, pkg := range manifest.Packages {
		parts = append(parts, pkg.Name)
	}
	return strings.Join(parts, "\n")
}

func normalizeRemoteContainerPackageManifest(manifest ContainerPackageManifest) ContainerPackageManifest {
	normalized := ContainerPackageManifest{
		BaseImage:      firstNonEmpty(strings.TrimSpace(manifest.BaseImage), remotePackageBaseImage),
		PackageManager: firstNonEmpty(strings.TrimSpace(manifest.PackageManager), remotePackageManager),
	}
	if len(manifest.Packages) == 0 {
		return normalized
	}
	seen := map[string]struct{}{}
	packages := make([]ContainerPackageSelection, 0, len(manifest.Packages))
	for _, pkg := range manifest.Packages {
		name := strings.ToLower(strings.TrimSpace(pkg.Name))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		packages = append(packages, ContainerPackageSelection{
			Name:   name,
			Source: strings.TrimSpace(pkg.Source),
			Reason: strings.TrimSpace(pkg.Reason),
		})
	}
	sort.Slice(packages, func(i, j int) bool {
		return packages[i].Name < packages[j].Name
	})
	normalized.Packages = packages
	return normalized
}

func remoteImageArchiveName(transportMode string) string {
	switch normalizeRemoteTransportMode(transportMode) {
	case startupconfig.NetworkModeLAN:
		return "swarm-remote-lan-wireguard-image.tar"
	default:
		return "swarm-remote-tailscale-image.tar"
	}
}

func remoteImageRef(imagePrefix, signature string) string {
	prefix := strings.TrimRight(strings.TrimSpace(imagePrefix), "/")
	if prefix == "" {
		prefix = remoteDeployImagePrefix()
	}
	return fmt.Sprintf("%s:%s", prefix, strings.TrimSpace(signature))
}

func hashDirWithPath(h io.Writer, rootPath, buildRoot string) error {
	rootPath = filepath.Clean(rootPath)
	entries := make([]string, 0, 32)
	err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		entries = append(entries, path)
		return nil
	})
	if err != nil {
		return fmt.Errorf("hash remote deploy dir %q: %w", rootPath, err)
	}
	sort.Strings(entries)
	for _, path := range entries {
		if err := hashFileWithPath(h, path, buildRoot); err != nil {
			return err
		}
	}
	return nil
}

func hashFileWithPath(h io.Writer, path, buildRoot string) error {
	rel, err := filepath.Rel(buildRoot, path)
	if err != nil {
		return fmt.Errorf("hash remote deploy file %q: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("hash remote deploy file %q: %w", path, err)
	}
	if _, err := io.WriteString(h, filepath.ToSlash(rel)); err != nil {
		return err
	}
	if _, err := io.WriteString(h, "\n"); err != nil {
		return err
	}
	if _, err := io.WriteString(h, fmt.Sprintf("%d\n", info.Size())); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open remote deploy file %q: %w", path, err)
	}
	defer file.Close()
	if _, err := io.Copy(h, file); err != nil {
		return fmt.Errorf("hash remote deploy file %q: %w", path, err)
	}
	if _, err := io.WriteString(h, "\n"); err != nil {
		return err
	}
	return nil
}

func addPathToArchive(tw *tar.Writer, sourcePath, archivePath string) error {
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		entries, err := os.ReadDir(sourcePath)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if err := addPathToArchive(tw, filepath.Join(sourcePath, entry.Name()), filepath.ToSlash(filepath.Join(archivePath, entry.Name()))); err != nil {
				return err
			}
		}
		return nil
	}
	linkTarget := ""
	if info.Mode()&os.ModeSymlink != 0 {
		linkTarget, err = os.Readlink(sourcePath)
		if err != nil {
			return err
		}
	}
	header, err := tar.FileInfoHeader(info, linkTarget)
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(archivePath)
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := io.Copy(tw, file); err != nil {
		return err
	}
	return nil
}

func createGitTrackedArchive(sourcePath, destArchive string) error {
	startedAt := time.Now()
	gitRoot, files, err := gitTrackedFiles(sourcePath)
	if err != nil {
		logRemoteDeployTiming("start.prepare_bundle.git_tracked_archive", startedAt, err, "archive", filepath.Base(destArchive))
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destArchive), 0o755); err != nil {
		return err
	}
	file, err := os.Create(destArchive)
	if err != nil {
		return err
	}
	defer file.Close()
	gz := gzip.NewWriter(file)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	for _, rel := range files {
		abs := filepath.Join(gitRoot, rel)
		info, err := os.Stat(abs)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		f, err := os.Open(abs)
		if err != nil {
			return err
		}
		if _, err := io.Copy(tw, f); err != nil {
			f.Close()
			return err
		}
		f.Close()
	}
	logRemoteDeployTiming("start.prepare_bundle.git_tracked_archive", startedAt, nil, "archive", filepath.Base(destArchive), "files", strconv.Itoa(len(files)))
	return nil
}

func remotePayloadArchiveCount(payloads []pebblestore.RemoteDeployPayloadRecord) int {
	count := 0
	for _, payload := range payloads {
		if strings.TrimSpace(payload.ArchiveName) != "" {
			count++
		}
		for _, directory := range payload.Directories {
			if strings.TrimSpace(directory.ArchiveName) != "" {
				count++
			}
		}
	}
	return count
}

func remotePayloadIncludedBytes(payloads []pebblestore.RemoteDeployPayloadRecord) int64 {
	var total int64
	for _, payload := range payloads {
		total += payload.IncludedBytes
	}
	return total
}

func remotePreflightRequiredDiskBytes(ctx context.Context, imageDeliveryMode string, payloads []pebblestore.RemoteDeployPayloadRecord) (int64, error) {
	var imageBytes int64
	if normalizeRemoteImageDeliveryMode(imageDeliveryMode) == remoteImageDeliveryRegistry {
		metadata, err := localcontainers.FetchProductionImageMetadata(ctx)
		if err != nil {
			return 0, fmt.Errorf("fetch production swarm image metadata for disk preflight: %w", err)
		}
		imageBytes = metadata.ImageSizeBytes
	}
	return remoteRequiredDiskBytes(imageBytes, payloads), nil
}

func remoteRequiredDiskBytes(imageBytes int64, payloads []pebblestore.RemoteDeployPayloadRecord) int64 {
	var total int64
	if imageBytes > 0 {
		total += imageBytes
	}
	payloadBytes := remotePayloadIncludedBytes(payloads)
	if payloadBytes > 0 {
		// Payload archives are staged on disk and then extracted into the target
		// workspace mounts, so require enough room for both compressed input and
		// unpacked files. The included byte count is the safest local estimate.
		total += payloadBytes * 2
	}
	if total <= 0 {
		// Keep zero-payload registry deploys from reporting an unknown/no-op check.
		return 256 * 1024 * 1024
	}
	return total
}

func logRemoteDeployTiming(step string, startedAt time.Time, err error, fields ...string) {
	parts := []string{
		fmt.Sprintf("remote deploy timing step=%q", strings.TrimSpace(step)),
		fmt.Sprintf("elapsed_ms=%d", time.Since(startedAt).Milliseconds()),
	}
	if err != nil {
		parts = append(parts, fmt.Sprintf("status=%q", "error"), fmt.Sprintf("err=%q", strings.TrimSpace(err.Error())))
	} else {
		parts = append(parts, fmt.Sprintf("status=%q", "ok"))
	}
	for idx := 0; idx+1 < len(fields); idx += 2 {
		key := strings.TrimSpace(fields[idx])
		value := strings.TrimSpace(fields[idx+1])
		if key == "" || value == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%q", key, value))
	}
	log.Print(strings.Join(parts, " "))
}

func gitTrackedStats(sourcePath string) (int, int64, string, error) {
	gitRoot, files, err := gitTrackedFiles(sourcePath)
	if err != nil {
		return 0, 0, "", err
	}
	var total int64
	for _, rel := range files {
		info, err := os.Stat(filepath.Join(gitRoot, rel))
		if err != nil {
			return 0, 0, "", err
		}
		total += info.Size()
	}
	return len(files), total, gitRoot, nil
}

func gitTrackedFiles(sourcePath string) (gitRoot string, files []string, err error) {
	abs, err := filepath.Abs(sourcePath)
	if err != nil {
		return "", nil, err
	}
	cmd := exec.Command("git", "-C", abs, "rev-parse", "--show-toplevel")
	rootBytes, err := cmd.Output()
	if err != nil {
		return "", nil, fmt.Errorf("resolve git root for %q: %w", sourcePath, err)
	}
	gitRoot = strings.TrimSpace(string(rootBytes))
	lsCmd := exec.Command("git", "-C", gitRoot, "ls-files", "-z", "--", abs)
	output, err := lsCmd.Output()
	if err != nil {
		return "", nil, fmt.Errorf("list git-tracked files for %q: %w", sourcePath, err)
	}
	parts := bytes.Split(output, []byte{0})
	files = make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		files = append(files, string(part))
	}
	sort.Strings(files)
	return gitRoot, files, nil
}

func runSSHCommand(ctx context.Context, target, script string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", fmt.Errorf("ssh target is required")
	}
	cmd := exec.CommandContext(ctx, "ssh", target, "bash", "-se")
	cmd.Stdin = strings.NewReader(script + "\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("ssh %s: %w: %s", target, err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func remoteSwarmJSONRequestWithClient(method, endpoint string, payload any, out any, client *http.Client) error {
	var bodyReader io.Reader
	if payload != nil {
		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(body)
	}
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	req, err := http.NewRequest(method, endpoint, bodyReader)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return fmt.Errorf("remote request %s failed: %s", endpoint, message)
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return err
	}
	return nil
}

func remoteImageExists(ctx context.Context, target, runtimeName, imageRef, sudoMode string) (bool, error) {
	target = strings.TrimSpace(target)
	runtimeName = normalizeRemoteDeployRuntime(runtimeName)
	imageRef = strings.TrimSpace(imageRef)
	if target == "" {
		return false, fmt.Errorf("ssh target is required")
	}
	if imageRef == "" {
		return false, fmt.Errorf("remote image ref is required")
	}
	sudoPrefix := ""
	if strings.TrimSpace(sudoMode) == "sudo" {
		sudoPrefix = "sudo "
	}
	cmd := fmt.Sprintf(`set -eu
runtime=%s
image_ref=%s
if [ "$runtime" = "podman" ]; then
  if %spodman image inspect "$image_ref" >/dev/null 2>&1; then
    printf 'REMOTE_IMAGE_PRESENT=1\n'
  else
    printf 'REMOTE_IMAGE_PRESENT=0\n'
  fi
else
  if %sdocker image inspect "$image_ref" >/dev/null 2>&1; then
    printf 'REMOTE_IMAGE_PRESENT=1\n'
  else
    printf 'REMOTE_IMAGE_PRESENT=0\n'
  fi
fi
`, shellQuote(runtimeName), shellQuote(imageRef), sudoPrefix, sudoPrefix)
	output, err := runSSHCommand(ctx, target, cmd)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "REMOTE_IMAGE_PRESENT=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "REMOTE_IMAGE_PRESENT=")) == "1", nil
		}
	}
	return false, nil
}

func generateSecretToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func shortToken(size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "rand"
	}
	return hex.EncodeToString(buf)
}

func remoteInstallerScript(record pebblestore.RemoteDeploySessionRecord) string {
	remoteRoot := firstNonEmpty(strings.TrimSpace(record.RemoteRoot), remoteRoot(record.ID))
	legacyCredentialsFile := filepath.ToSlash(filepath.Join(remoteRoot, legacyRemoteCredentialsFileName))
	bootstrapSecretFile := remoteBootstrapSecretPath(record)
	remoteStateRoot := filepath.ToSlash(filepath.Join(remoteRoot, "state"))
	transportMode := normalizeRemoteTransportMode(record.TransportMode)
	runtimeName := normalizeRemoteDeployRuntime(record.RemoteRuntime)
	if runtimeName == "" {
		runtimeName = "docker"
	}
	remoteAdvertiseHost := firstNonEmpty(strings.TrimSpace(record.RemoteAdvertiseHost), "127.0.0.1")
	listenAddr := firstNonEmpty(map[bool]string{true: net.JoinHostPort(remoteAdvertiseHost, strconv.Itoa(startupconfig.DefaultPort))}[transportMode == startupconfig.NetworkModeLAN], "127.0.0.1:7781")
	offlineMode := "0"
	bootstrapOutputPrefix := "SWARM_TAILNET_URL"
	if transportMode == startupconfig.NetworkModeLAN {
		offlineMode = "1"
		bootstrapOutputPrefix = "SWARM_REMOTE_URL"
	}
	tailscaleStateDir := filepath.ToSlash(filepath.Join(remoteStateRoot, "tailscale"))
	swarmdStateDir := filepath.ToSlash(filepath.Join(remoteStateRoot, "swarmd"))
	configHome := filepath.ToSlash(filepath.Join(remoteRoot, "config"))
	logDir := filepath.ToSlash(filepath.Join(remoteRoot, "logs"))
	logFile := filepath.ToSlash(filepath.Join(logDir, "remote-child.log"))
	startScriptPath := filepath.ToSlash(filepath.Join(remoteRoot, "run-remote-child.sh"))
	pidFile := filepath.ToSlash(filepath.Join(remoteRoot, "run-remote-child.pid"))
	imageArchiveName := remoteImageArchiveName(transportMode)
	useArchiveImage := "0"
	imageVerification := ""
	if remoteImageUsesArchive(record.ImageRef) {
		useArchiveImage = "1"
	} else if strings.HasPrefix(strings.TrimSpace(record.ImageRef), localcontainers.ProductionImagePrefix+"@sha256:") {
		imageVerification = remoteProductionImageVerificationScript()
	}
	containerName := remoteContainerNameForSession(record.ID)
	useSudo := "0"
	if strings.TrimSpace(record.SudoMode) == "sudo" {
		useSudo = "1"
	}
	payloadExtract := ""
	mountTargets := []string{remoteRoot}
	mountSeen := map[string]struct{}{remoteRoot: {}}
	appendMountTarget := func(path string) {
		path = firstNonEmpty(strings.TrimSpace(path), "/workspaces")
		if path == "" {
			return
		}
		if _, ok := mountSeen[path]; ok {
			return
		}
		mountSeen[path] = struct{}{}
		mountTargets = append(mountTargets, path)
	}
	for _, payload := range record.Payloads {
		if payload.ArchiveName == "" {
			continue
		}
		targetPath := firstNonEmpty(strings.TrimSpace(payload.TargetPath), "/workspaces")
		appendMountTarget(targetPath)
		payloadExtract += fmt.Sprintf("as_root mkdir -p %s\nas_root chown \"${remote_user}:${remote_group}\" %s >/dev/null 2>&1 || true\ntar -xzf %s -C %s\n", shellQuote(targetPath), shellQuote(targetPath), shellQuote(payload.ArchiveName), shellQuote(targetPath))
		for _, directory := range payload.Directories {
			if directory.ArchiveName == "" {
				continue
			}
			directoryTargetPath := firstNonEmpty(strings.TrimSpace(directory.TargetPath), targetPath)
			appendMountTarget(directoryTargetPath)
			payloadExtract += fmt.Sprintf("as_root mkdir -p %s\nas_root chown \"${remote_user}:${remote_group}\" %s >/dev/null 2>&1 || true\ntar -xzf %s -C %s\n", shellQuote(directoryTargetPath), shellQuote(directoryTargetPath), shellQuote(directory.ArchiveName), shellQuote(directoryTargetPath))
		}
	}
	mountArgs := ""
	for _, targetPath := range mountTargets {
		mountArgs += fmt.Sprintf("run_args+=(--volume %s)\n", shellQuote(targetPath+":"+targetPath))
	}
	return fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

remote_root=%s
config_home=%s
legacy_credentials_file=%s
bootstrap_secret_file=%s
state_root=%s
tailscale_state_dir=%s
swarmd_state_dir=%s
log_dir=%s
log_file=%s
start_script=%s
pid_file=%s
use_sudo=%s
runtime=%s
image_ref=%s
image_archive=%s
use_archive_image=%s
container_name=%s
transport_mode=%s
remote_advertise_host=%s
listen_addr=%s
offline_mode=%s
remote_user="$(id -un)"
remote_group="$(id -gn)"
cd "$remote_root"

as_root() {
  if [ "$use_sudo" = "1" ]; then
    sudo "$@"
  else
    "$@"
  fi
}

runtime_cmd() {
  if [ "$runtime" = "podman" ]; then
    as_root podman "$@"
  else
    as_root docker "$@"
  fi
}

now_ms() {
  date +%%s%%3N
}

log_timer_step() {
  local step="${1:-}"
  local started_ms="${2:-0}"
  local ended_ms elapsed_ms
  ended_ms="$(now_ms)"
  elapsed_ms=0
  if [[ "$started_ms" =~ ^[0-9]+$ ]] && [[ "$ended_ms" =~ ^[0-9]+$ ]]; then
    elapsed_ms=$((ended_ms - started_ms))
  fi
  printf 'SWARM_REMOTE_TIMER step=%%s elapsed_ms=%%s\n' "$step" "$elapsed_ms"
}

step_started_ms="$(now_ms)"
mkdir -p "$remote_root" "$config_home/swarm" "$state_root" "$tailscale_state_dir" "$swarmd_state_dir" "$log_dir" "$remote_root/xdg/data" "$remote_root/xdg/state"
chmod 0700 "$config_home" "$config_home/swarm"
if [ ! -f "$config_home/swarm/swarm.conf" ]; then
  echo "remote startup config missing: $config_home/swarm/swarm.conf" >&2
  exit 1
fi
chmod 0600 "$config_home/swarm/swarm.conf"
if [ -f "$bootstrap_secret_file" ]; then
  chmod 0600 "$bootstrap_secret_file"
fi
as_root chmod 0755 "$remote_root" "$state_root" "$remote_root/xdg" "$remote_root/xdg/data" "$remote_root/xdg/state" >/dev/null 2>&1 || true
as_root chown -R 65534:65534 "$config_home" "$remote_root/xdg" "$swarmd_state_dir" >/dev/null 2>&1 || true
rm -f "$legacy_credentials_file"
: > "$log_file"
log_timer_step "prepare_remote_root" "$step_started_ms"
step_started_ms="$(now_ms)"
if runtime_cmd image inspect "$image_ref" >/dev/null 2>&1; then
  :
elif [ "$use_archive_image" != "1" ]; then
  runtime_cmd pull "$image_ref" >/dev/null
elif [ -f "$image_archive" ]; then
  runtime_cmd load -i "$image_archive" >/dev/null
else
  echo "remote image archive missing and image is not present: $image_ref" >&2
  exit 1
fi
%slog_timer_step "ensure_remote_image" "$step_started_ms"
step_started_ms="$(now_ms)"
%s
log_timer_step "extract_payloads" "$step_started_ms"

step_started_ms="$(now_ms)"
cat > "$start_script" <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
remote_root=%s
config_home=%s
tailscale_state_dir=%s
swarmd_state_dir=%s
runtime=%s
image_ref=%s
container_name=%s
listen_addr=%s
offline_mode=%s
ts_hostname=%s
mkdir -p "$tailscale_state_dir" "$swarmd_state_dir" "$remote_root/xdg/data" "$remote_root/xdg/state"
export XDG_CONFIG_HOME="$config_home"
export XDG_DATA_HOME="$remote_root/xdg/data"
export XDG_STATE_HOME="$remote_root/xdg/state"
export TS_SOCKET="$tailscale_state_dir/tailscaled.sock"
export TS_STATE_DIR="$tailscale_state_dir"
export TS_OUTBOUND_HTTP_PROXY_LISTEN="127.0.0.1:1055"
export TS_TUN_MODE=userspace-networking
export SWARM_TAILSCALE_OUTBOUND_PROXY="http://127.0.0.1:1055"
export SWARMD_DATA_DIR="$swarmd_state_dir"
export SWARMD_LOCK_PATH="$swarmd_state_dir/swarmd.lock"
export SWARMD_LISTEN="$listen_addr"
export SWARM_DESKTOP_PORT="5555"
export SWARM_CONTAINER_OFFLINE="$offline_mode"
export TS_HOSTNAME="$ts_hostname"
if [ "$runtime" = "podman" ]; then
  runtime_bin=podman
else
  runtime_bin=docker
fi
"$runtime_bin" rm -f "$container_name" >/dev/null 2>&1 || true
run_args=(
  run
  --rm
  --name "$container_name"
  --network host
  -e "XDG_CONFIG_HOME=$config_home"
  -e "XDG_DATA_HOME=$remote_root/xdg/data"
  -e "XDG_STATE_HOME=$remote_root/xdg/state"
  -e "TS_SOCKET=$tailscale_state_dir/tailscaled.sock"
  -e "TS_STATE_DIR=$tailscale_state_dir"
  -e "TS_OUTBOUND_HTTP_PROXY_LISTEN=127.0.0.1:1055"
  -e "TS_TUN_MODE=userspace-networking"
  -e "SWARM_TAILSCALE_OUTBOUND_PROXY=http://127.0.0.1:1055"
  -e "SWARMD_DATA_DIR=$swarmd_state_dir"
  -e "SWARMD_LOCK_PATH=$swarmd_state_dir/swarmd.lock"
  -e "SWARMD_LISTEN=$listen_addr"
  -e "SWARM_DESKTOP_PORT=5555"
  -e "SWARM_CONTAINER_OFFLINE=$offline_mode"
  -e "TS_HOSTNAME=$ts_hostname"
)
if [ -n "${TS_AUTHKEY:-}" ]; then
  run_args+=(-e TS_AUTHKEY)
fi
if [ -n "${SWARM_REMOTE_SYNC_VAULT_PASSWORD:-}" ]; then
  run_args+=(-e SWARM_REMOTE_SYNC_VAULT_PASSWORD)
fi
run_args+=(--volume "$remote_root:$remote_root")
%s
run_args+=("$image_ref")
exec "$runtime_bin" "${run_args[@]}"
SCRIPT
chmod 0755 "$start_script"
log_timer_step "write_start_script" "$step_started_ms"

step_started_ms="$(now_ms)"
rm -f "$pid_file"
if [ "$use_sudo" = "1" ]; then
  nohup sudo -E /bin/bash "$start_script" >"$log_file" 2>&1 < /dev/null &
else
  nohup /bin/bash "$start_script" >"$log_file" 2>&1 < /dev/null &
fi
echo $! >"$pid_file"
log_timer_step "start_remote_container" "$step_started_ms"

log_output=""
auth_url=""
remote_url=""
service_state=""
deadline=$((SECONDS + 90))
step_started_ms="$(now_ms)"
while :; do
  if [ -s "$log_file" ]; then
    log_output="$(tail -n 200 "$log_file" 2>&1 || true)"
  elif runtime_cmd logs --tail 200 "$container_name" >/dev/null 2>&1; then
    log_output="$(runtime_cmd logs --tail 200 "$container_name" 2>&1 || true)"
  else
    log_output=""
  fi
  auth_url="$(printf '%%s\n' "$log_output" | sed -n 's/^TAILSCALE_AUTH_URL=//p' | tail -n 1)"
  if [ "$transport_mode" = "lan" ]; then
    if runtime_cmd exec "$container_name" sh -lc "curl -fsS http://${remote_advertise_host}:7781/readyz >/dev/null 2>&1 || curl -fsS http://${remote_advertise_host}:7781/healthz >/dev/null 2>&1" >/dev/null 2>&1; then
      remote_url="http://${remote_advertise_host}:7781"
    fi
  else
    remote_url="$(printf '%%s\n' "$log_output" | sed -n 's/^SWARM_TAILNET_URL=//p' | tail -n 1)"
  fi
  if [ -n "$auth_url" ] || [ -n "$remote_url" ]; then
    break
  fi
  if runtime_cmd inspect -f '{{.State.Running}}' "$container_name" >/dev/null 2>&1; then
    service_state="$(runtime_cmd inspect -f '{{.State.Running}}' "$container_name" 2>/dev/null || true)"
    if [ "$service_state" = "true" ]; then
      service_state="active"
    else
      service_state="inactive"
    fi
  elif [ -f "$pid_file" ]; then
    child_pid="$(cat "$pid_file" 2>/dev/null || true)"
    if [ -n "$child_pid" ] && kill -0 "$child_pid" 2>/dev/null; then
      service_state="active"
    else
      service_state="inactive"
    fi
  else
    service_state="inactive"
  fi
  if [ "$service_state" != "active" ] && [ "${SECONDS}" -ge "${deadline}" ]; then
    break
  fi
  if [ "${SECONDS}" -ge "${deadline}" ]; then
    break
  fi
  sleep 1
done
log_timer_step "wait_for_bootstrap_signal" "$step_started_ms"
printf '%%s\n' "$log_output"
printf 'TAILSCALE_AUTH_URL=%%s\n' "$auth_url"
printf '%s=%%s\n' "$remote_url"
`, shellQuote(remoteRoot), shellQuote(configHome), shellQuote(legacyCredentialsFile), shellQuote(bootstrapSecretFile), shellQuote(remoteStateRoot), shellQuote(tailscaleStateDir), shellQuote(swarmdStateDir), shellQuote(logDir), shellQuote(logFile), shellQuote(startScriptPath), shellQuote(pidFile), shellQuote(useSudo), shellQuote(runtimeName), shellQuote(strings.TrimSpace(record.ImageRef)), shellQuote(imageArchiveName), shellQuote(useArchiveImage), shellQuote(containerName), shellQuote(transportMode), shellQuote(remoteAdvertiseHost), shellQuote(listenAddr), shellQuote(offlineMode), imageVerification, payloadExtract, shellQuote(remoteRoot), shellQuote(configHome), shellQuote(tailscaleStateDir), shellQuote(swarmdStateDir), shellQuote(runtimeName), shellQuote(strings.TrimSpace(record.ImageRef)), shellQuote(containerName), shellQuote(listenAddr), shellQuote(offlineMode), shellQuote(firstNonEmpty(strings.TrimSpace(record.Name), "swarm-box")), mountArgs, bootstrapOutputPrefix)
}

func remoteBundleStartScript(record *pebblestore.RemoteDeploySessionRecord, childCfgText string, tailscaleAuthKey string, syncVaultPassword string) (string, error) {
	if record == nil {
		return "", fmt.Errorf("remote deploy record is required")
	}
	remoteDir := firstNonEmpty(strings.TrimSpace(record.RemoteRoot), remoteRoot(record.ID))
	childCfgText = strings.ReplaceAll(strings.TrimSpace(childCfgText), "\r\n", "\n")
	if childCfgText == "" {
		return "", fmt.Errorf("remote child startup config is required")
	}
	secretCfgPath := remoteStartupConfigPath(*record)
	secretCfg := startupconfig.Default(secretCfgPath)
	secretCfg.RemoteDeploy.Enabled = true
	secretCfg.RemoteDeploy.SessionToken = strings.TrimSpace(record.SessionToken)
	secretCfg.RemoteDeploy.InviteToken = strings.TrimSpace(record.InviteToken)
	bootstrapSecretText := strings.TrimSpace(startupconfig.FormatRemoteDeployBootstrapSecrets(secretCfg))
	installerScript := remoteInstallerScript(*record)
	legacyCredentialsPath := filepath.ToSlash(filepath.Join(remoteDir, legacyRemoteCredentialsFileName))
	var builder strings.Builder
	builder.WriteString("set -euo pipefail\n")
	builder.WriteString("umask 077\n")
	builder.WriteString("trap 'rm -f \"$installer_path\" \"$legacy_credentials_file\"' EXIT\n")
	builder.WriteString(fmt.Sprintf("remote_dir=%s\n", shellQuote(remoteDir)))
	builder.WriteString(fmt.Sprintf("config_path=%s\n", shellQuote(remoteStartupConfigPath(*record))))
	builder.WriteString(fmt.Sprintf("bootstrap_secret_path=%s\n", shellQuote(remoteBootstrapSecretPath(*record))))
	builder.WriteString(fmt.Sprintf("installer_path=%s\n", shellQuote(filepath.ToSlash(filepath.Join(remoteDir, "install-remote-child.sh")))))
	builder.WriteString(fmt.Sprintf("legacy_credentials_file=%s\n", shellQuote(legacyCredentialsPath)))
	builder.WriteString("mkdir -p \"$remote_dir\" \"$(dirname \"$config_path\")\"\n")
	builder.WriteString("rm -f \"$legacy_credentials_file\"\n")
	builder.WriteString("cat > \"$config_path\" <<'SWARM_REMOTE_CONFIG_EOF'\n")
	builder.WriteString(childCfgText)
	builder.WriteString("\nSWARM_REMOTE_CONFIG_EOF\n")
	builder.WriteString("chmod 0600 \"$config_path\"\n")
	builder.WriteString("cat > \"$bootstrap_secret_path\" <<'SWARM_REMOTE_SECRET_EOF'\n")
	builder.WriteString(bootstrapSecretText)
	builder.WriteString("\nSWARM_REMOTE_SECRET_EOF\n")
	builder.WriteString("chmod 0600 \"$bootstrap_secret_path\"\n")
	builder.WriteString("cat > \"$installer_path\" <<'SWARM_REMOTE_INSTALL_EOF'\n")
	builder.WriteString(installerScript)
	builder.WriteString("\nSWARM_REMOTE_INSTALL_EOF\n")
	builder.WriteString("chmod 0700 \"$installer_path\"\n")
	if value := strings.TrimSpace(tailscaleAuthKey); value != "" {
		builder.WriteString(fmt.Sprintf("TS_AUTHKEY=%s ", shellQuote(value)))
	}
	if value := strings.TrimSpace(syncVaultPassword); value != "" {
		builder.WriteString(fmt.Sprintf("SWARM_REMOTE_SYNC_VAULT_PASSWORD=%s ", shellQuote(value)))
	}
	builder.WriteString("\"$installer_path\"\n")
	builder.WriteString("rm -f \"$installer_path\" \"$legacy_credentials_file\"\n")
	return builder.String(), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func lastMatchingLine(output, prefix string) string {
	var match string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			match = line
		}
	}
	return match
}

func encodeStartupConfigEnv(value string) string {
	return value
}
