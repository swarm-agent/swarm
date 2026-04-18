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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	authruntime "swarm/packages/swarmd/internal/auth"
	deployruntime "swarm/packages/swarmd/internal/deploy"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

const (
	PathSessionList        = "deploy.remote.list.v1"
	PathSessionCreate      = "deploy.remote.create.v1"
	PathSessionDelete      = "deploy.remote.delete.v1"
	PathSessionStart       = "deploy.remote.start.v1"
	PathSessionApprove     = "deploy.remote.approve.v1"
	PathSessionChildStatus = "deploy.remote.child_status.v1"
	PathSessionPreflight   = "deploy.remote.preflight.v1"
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
}

type CreateSessionInput struct {
	Name              string
	SSHSessionTarget  string
	GroupID           string
	GroupName         string
	RemoteRuntime     string
	SyncEnabled       bool
	BypassPermissions bool
	ContainerPackages ContainerPackageManifest
	Payloads          []PayloadSelection
}

type StartSessionInput struct {
	SessionID         string
	TailscaleAuthKey  string
	SyncVaultPassword string
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
	ID            string `json:"id"`
	SourcePath    string `json:"source_path,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	TargetPath    string `json:"target_path,omitempty"`
	Mode          string `json:"mode,omitempty"`
	GitRoot       string `json:"git_root,omitempty"`
	ArchiveName   string `json:"archive_name,omitempty"`
	IncludedFiles int    `json:"included_files"`
	IncludedBytes int64  `json:"included_bytes"`
	ExcludedNote  string `json:"excluded_note,omitempty"`
}

type SessionPreflight struct {
	PathID           string           `json:"path_id"`
	BuilderRuntime   string           `json:"builder_runtime,omitempty"`
	RemoteRuntime    string           `json:"remote_runtime,omitempty"`
	SSHReachable     bool             `json:"ssh_reachable"`
	SystemdAvailable bool             `json:"systemd_available"`
	SystemdUnit      string           `json:"systemd_unit,omitempty"`
	RemoteRoot       string           `json:"remote_root,omitempty"`
	FilesToCopy      []string         `json:"files_to_copy,omitempty"`
	Payloads         []SessionPayload `json:"payloads,omitempty"`
	Summary          string           `json:"summary,omitempty"`
	Checks           []string         `json:"checks,omitempty"`
}

type Session struct {
	ID                 string                   `json:"id"`
	Name               string                   `json:"name"`
	Status             string                   `json:"status"`
	SSHSessionTarget   string                   `json:"ssh_session_target,omitempty"`
	GroupID            string                   `json:"group_id,omitempty"`
	GroupName          string                   `json:"group_name,omitempty"`
	BuilderRuntime     string                   `json:"builder_runtime,omitempty"`
	RemoteRuntime      string                   `json:"remote_runtime,omitempty"`
	MasterTailscaleURL string                   `json:"master_tailscale_url,omitempty"`
	RemoteAuthURL      string                   `json:"remote_auth_url,omitempty"`
	RemoteTailnetURL   string                   `json:"remote_tailnet_url,omitempty"`
	ImageRef           string                   `json:"image_ref,omitempty"`
	ImageSignature     string                   `json:"image_signature,omitempty"`
	ImageArchiveBytes  int64                    `json:"image_archive_bytes,omitempty"`
	EnrollmentID       string                   `json:"enrollment_id,omitempty"`
	EnrollmentStatus   string                   `json:"enrollment_status,omitempty"`
	ChildSwarmID       string                   `json:"child_swarm_id,omitempty"`
	ChildName          string                   `json:"child_name,omitempty"`
	HostSwarmID        string                   `json:"host_swarm_id,omitempty"`
	HostName           string                   `json:"host_name,omitempty"`
	HostPublicKey      string                   `json:"host_public_key,omitempty"`
	HostFingerprint    string                   `json:"host_fingerprint,omitempty"`
	HostAPIBaseURL     string                   `json:"host_api_base_url,omitempty"`
	HostDesktopURL     string                   `json:"host_desktop_url,omitempty"`
	BypassPermissions  bool                     `json:"bypass_permissions,omitempty"`
	ContainerPackages  ContainerPackageManifest `json:"container_packages,omitempty"`
	LastError          string                   `json:"last_error,omitempty"`
	LastRemoteOutput   string                   `json:"last_remote_output,omitempty"`
	SyncEnabled        bool                     `json:"sync_enabled,omitempty"`
	SyncMode           string                   `json:"sync_mode,omitempty"`
	SyncOwnerSwarmID   string                   `json:"sync_owner_swarm_id,omitempty"`
	Preflight          SessionPreflight         `json:"preflight"`
	CreatedAt          int64                    `json:"created_at"`
	UpdatedAt          int64                    `json:"updated_at"`
	ApprovedAt         int64                    `json:"approved_at,omitempty"`
	AttachedAt         int64                    `json:"attached_at,omitempty"`
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

type remoteImageArtifact struct {
	ImageRef      string
	Signature     string
	ArchivePath   string
	ArchiveBytes  int64
	LocalImageHit bool
	ArchiveHit    bool
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
	startupCfg, hostState, err := s.resolveBootstrapContext()
	if err != nil {
		return Session{}, err
	}
	group, err := s.resolveTargetGroupForSession(hostState, strings.TrimSpace(input.GroupID), strings.TrimSpace(input.GroupName))
	if err != nil {
		return Session{}, err
	}
	builderRuntime, err := s.detectBuilderRuntime(ctx)
	if err != nil {
		return Session{}, formatCreatePreflightError(sshTarget, err)
	}
	sessionID := suggestedSessionID(name)
	if sessionID == "" {
		sessionID = "remote-child"
	}
	sessionID = sessionID + "-" + shortToken(4)
	systemdUnit := systemdUnitName(sessionID)
	remoteRuntime, systemdAvailable, sudoMode, remoteHome, err := s.inspectRemoteHost(ctx, sshTarget, input.RemoteRuntime)
	if err != nil {
		return Session{}, formatCreatePreflightError(sshTarget, err)
	}
	remoteRoot := remoteRootForHome(remoteHome, sessionID)
	if err := s.checkRemoteInstallCollision(ctx, sshTarget, remoteRoot, systemdUnit); err != nil {
		return Session{}, formatCreatePreflightError(sshTarget, err)
	}
	masterTailscaleURL := strings.TrimSpace(startupCfg.TailscaleURL)
	if masterTailscaleURL == "" {
		return Session{}, formatCreatePreflightError(sshTarget, fmt.Errorf("master swarm.conf tailscale_url is required for remote child deploy"))
	}
	sessionToken, err := generateSecretToken(16)
	if err != nil {
		return Session{}, err
	}
	payloads, err := s.buildPayloads(input.Payloads)
	if err != nil {
		return Session{}, err
	}
	filesToCopy := []string{
		"remote/swarm.conf",
		"remote/install-remote-child.sh",
		"remote/swarm-container-mvp.tar.gz",
	}
	for _, payload := range payloads {
		if payload.ArchiveName != "" {
			filesToCopy = append(filesToCopy, filepath.ToSlash(filepath.Join("remote", payload.ArchiveName)))
		}
	}
	record := pebblestore.RemoteDeploySessionRecord{
		ID:                 sessionID,
		Name:               name,
		Status:             "preflight_ready",
		SSHSessionTarget:   sshTarget,
		GroupID:            group.ID,
		GroupName:          firstNonEmpty(group.Name, input.GroupName, group.ID),
		BuilderRuntime:     builderRuntime,
		RemoteRuntime:      remoteRuntime,
		SystemdUnit:        systemdUnit,
		RemoteRoot:         remoteRoot,
		MasterTailscaleURL: masterTailscaleURL,
		MasterSwarmID:      strings.TrimSpace(hostState.Node.SwarmID),
		SyncEnabled:        input.SyncEnabled,
		SyncMode:           firstNonEmpty(map[bool]string{true: "managed"}[input.SyncEnabled]),
		SyncOwnerSwarmID:   firstNonEmpty(map[bool]string{true: strings.TrimSpace(hostState.Node.SwarmID)}[input.SyncEnabled]),
		BypassPermissions:  input.BypassPermissions,
		ContainerPackages:  mapRemoteContainerPackageManifest(input.ContainerPackages),
		SyncCredentialURL:  firstNonEmpty(map[bool]string{true: buildRemoteSyncCredentialURL(strings.TrimSpace(masterTailscaleURL), sessionID)}[input.SyncEnabled]),
		SessionToken:       sessionToken,
		SSHReachable:       true,
		SystemdAvailable:   systemdAvailable,
		SudoMode:           sudoMode,
		FilesToCopy:        filesToCopy,
		Payloads:           payloads,
	}
	saved, err := s.store.Put(record)
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
		if err := s.requireManagedSyncVaultPassword(strings.TrimSpace(input.SyncVaultPassword)); err != nil {
			return Session{}, err
		}
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
		_, exported, err := s.auth.ExportCredentials(record.SyncBundlePassword, strings.TrimSpace(input.SyncVaultPassword))
		if err != nil {
			return Session{}, err
		}
		record.SyncBundleExportCount = exported
		record.SyncBundleExportedAt = time.Now().UnixMilli()
		record.SyncMode = firstNonEmpty(record.SyncMode, "managed")
		record.SyncOwnerSwarmID = firstNonEmpty(record.SyncOwnerSwarmID, strings.TrimSpace(hostState.Node.SwarmID))
		record.SyncCredentialURL = firstNonEmpty(record.SyncCredentialURL, buildRemoteSyncCredentialURL(strings.TrimSpace(record.MasterTailscaleURL), record.ID))
	}
	invite, err := s.swarms.CreateInvite(swarmruntime.CreateInviteInput{
		PrimarySwarmID:       strings.TrimSpace(hostState.Node.SwarmID),
		PrimaryName:          firstNonEmpty(strings.TrimSpace(startupCfg.SwarmName), hostState.Node.Name, "Primary"),
		GroupID:              strings.TrimSpace(record.GroupID),
		TransportMode:        startupconfig.NetworkModeTailscale,
		RendezvousTransports: []swarmruntime.TransportSummary{{Kind: startupconfig.NetworkModeTailscale, Primary: strings.TrimSpace(record.MasterTailscaleURL), All: []string{strings.TrimSpace(record.MasterTailscaleURL)}}},
		TTL:                  30 * time.Minute,
	})
	if err != nil {
		return Session{}, err
	}
	childCfgText := s.renderChildStartupConfig(record, startupCfg, hostState, invite.Token)
	workDir, err := os.MkdirTemp("", "swarm-remote-deploy-")
	if err != nil {
		return Session{}, err
	}
	defer os.RemoveAll(workDir)
	imageArtifact, err := s.prepareRemoteImageArtifact(ctx, record.BuilderRuntime)
	if err != nil {
		record.Status = "failed"
		record.LastError = err.Error()
		saved, saveErr := s.store.Put(record)
		if saveErr != nil {
			return Session{}, saveErr
		}
		return mapSession(saved), err
	}
	record.ImageRef = imageArtifact.ImageRef
	record.ImageSignature = imageArtifact.Signature
	record.ImageArchiveBytes = imageArtifact.ArchiveBytes
	if err := s.prepareRemoteBundle(ctx, workDir, &record, childCfgText); err != nil {
		record.Status = "failed"
		record.LastError = err.Error()
		saved, saveErr := s.store.Put(record)
		if saveErr != nil {
			return Session{}, saveErr
		}
		return mapSession(saved), err
	}
	if err := s.copyRemoteBundle(ctx, workDir, imageArtifact, &record); err != nil {
		record.Status = "failed"
		record.LastError = err.Error()
		saved, saveErr := s.store.Put(record)
		if saveErr != nil {
			return Session{}, saveErr
		}
		return mapSession(saved), err
	}
	output, authURL, tailnetURL, err := s.startRemoteBundle(ctx, &record, strings.TrimSpace(input.TailscaleAuthKey), strings.TrimSpace(input.SyncVaultPassword))
	record.InviteToken = invite.Token
	record.LastRemoteOutput = strings.TrimSpace(output)
	record.RemoteAuthURL = strings.TrimSpace(authURL)
	record.RemoteTailnetURL = strings.TrimSpace(tailnetURL)
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
	saved, err := s.store.Put(record)
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
	record.HostAPIBaseURL = strings.TrimSpace(record.MasterTailscaleURL)
	record.HostDesktopURL = strings.TrimSpace(record.MasterTailscaleURL)
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
	remoteEndpoint := strings.TrimSpace(record.RemoteTailnetURL)
	if remoteEndpoint == "" {
		return fmt.Errorf("remote child tailnet url is not available yet")
	}
	if err := waitForRemoteSwarmReady(ctx, remoteEndpoint, 45*time.Second); err != nil {
		return err
	}
	peerSwarmID := strings.TrimSpace(record.HostSwarmID)
	if peerSwarmID == "" {
		return fmt.Errorf("host swarm id is not available yet")
	}
	peerToken := strings.TrimSpace(record.InviteToken)
	if peerToken == "" {
		return fmt.Errorf("remote deploy invite token is not available yet")
	}
	transports := remotePairingTransportsForMode(startupconfig.NetworkModeTailscale, hostState.Node.Transports, strings.TrimSpace(record.MasterTailscaleURL))
	payload := remotePairingFinalizeRequest{
		PrimarySwarmID:       peerSwarmID,
		PrimaryName:          strings.TrimSpace(record.HostName),
		PrimaryPublicKey:     strings.TrimSpace(record.HostPublicKey),
		PrimaryFingerprint:   strings.TrimSpace(record.HostFingerprint),
		TransportMode:        startupconfig.NetworkModeTailscale,
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
	resp, err := (&http.Client{Timeout: 12 * time.Second}).Do(req)
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
	if len(transports) == 0 && strings.TrimSpace(record.RemoteTailnetURL) != "" {
		transports = []pebblestore.SwarmTransportRecord{{
			Kind:    startupconfig.NetworkModeTailscale,
			Primary: strings.TrimSpace(record.RemoteTailnetURL),
			All:     []string{strings.TrimSpace(record.RemoteTailnetURL)},
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
		TransportMode:         firstNonEmpty(existing.TransportMode, startupconfig.NetworkModeTailscale),
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
	runtimeName := normalizeRemoteDeployRuntime(record.RemoteRuntime)
	sudoPrefix := ""
	if strings.TrimSpace(record.SudoMode) == "sudo" {
		sudoPrefix = "sudo "
	}
	cmd := fmt.Sprintf(`set -eu
runtime=%s
if [ "$runtime" = "podman" ]; then
  logs="$(%spodman logs --tail 200 swarm-remote-child 2>&1 || true)"
else
  logs="$(%sdocker logs --tail 200 swarm-remote-child 2>&1 || true)"
fi
printf '%%s\n' "$logs"
`, shellQuote(runtimeName), sudoPrefix, sudoPrefix)
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
	authURL, tailnetURL := parseRemoteBootstrapURLs(output)
	if authURL != "" && authURL != strings.TrimSpace(record.RemoteAuthURL) {
		record.RemoteAuthURL = authURL
		changed = true
	}
	if tailnetURL != "" && tailnetURL != strings.TrimSpace(record.RemoteTailnetURL) {
		record.RemoteTailnetURL = tailnetURL
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
	remoteEndpoint := strings.TrimSpace(record.RemoteTailnetURL)
	if remoteEndpoint == "" {
		return false, fmt.Errorf("remote child tailnet url is not available yet")
	}
	if err := waitForRemoteSwarmReady(ctx, remoteEndpoint, 45*time.Second); err != nil {
		return false, err
	}
	startupCfg, hostState, err := s.resolveBootstrapContext()
	if err != nil {
		return false, err
	}
	transports := remotePairingTransportsForMode(startupconfig.NetworkModeTailscale, hostState.Node.Transports, strings.TrimSpace(record.MasterTailscaleURL))
	primaryEndpoint := firstNonEmpty(
		strings.TrimSpace(record.MasterTailscaleURL),
		firstTransportPrimary(transports),
	)
	payload := remotePairingRequest{
		InviteToken:          strings.TrimSpace(record.InviteToken),
		PrimarySwarmID:       strings.TrimSpace(hostState.Node.SwarmID),
		PrimaryName:          firstNonEmpty(strings.TrimSpace(startupCfg.SwarmName), strings.TrimSpace(hostState.Node.Name), "Primary"),
		PrimaryEndpoint:      primaryEndpoint,
		TransportMode:        startupconfig.NetworkModeTailscale,
		RendezvousTransports: transports,
	}
	var response remotePairingResponse
	if err := remoteSwarmJSONRequestWithClient(http.MethodPost, strings.TrimRight(remoteEndpoint, "/")+"/v1/swarm/remote-pairing/request", payload, &response, nil); err != nil {
		return false, err
	}
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
	remoteTailnetURL := strings.TrimSpace(record.RemoteTailnetURL)
	if remoteTailnetURL == "" || strings.TrimSpace(record.InviteToken) == "" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(record.EnrollmentStatus), "pairing_requested") {
		return true
	}
	return !strings.EqualFold(strings.TrimSpace(record.LastPairingURL), remoteTailnetURL)
}

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
	var lastErr error
	for {
		if err := probeRemoteSwarmReady(ctx, readyEndpoint, client); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if err := probeRemoteSwarmReady(ctx, healthEndpoint, client); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return fmt.Errorf("remote child at %s was not ready before context ended: %w", endpoint, lastErr)
			}
			return err
		}
		if time.Now().After(deadline) {
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

func parseRemoteBootstrapURLs(output string) (authURL, tailnetURL string) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "TAILSCALE_AUTH_URL="):
			authURL = strings.TrimSpace(strings.TrimPrefix(line, "TAILSCALE_AUTH_URL="))
		case strings.HasPrefix(line, "SWARM_TAILNET_URL="):
			tailnetURL = strings.TrimSpace(strings.TrimPrefix(line, "SWARM_TAILNET_URL="))
		}
	}
	return authURL, tailnetURL
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

func (s *Service) detectBuilderRuntime(ctx context.Context) (string, error) {
	if s.containers != nil {
		status, err := s.containers.RuntimeStatus(ctx)
		if err == nil && strings.TrimSpace(status.Recommended) != "" {
			return strings.TrimSpace(status.Recommended), nil
		}
	}
	for _, candidate := range []string{"podman", "docker"} {
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("install Podman or Docker to build remote child artifacts")
}

func (s *Service) inspectRemoteHost(ctx context.Context, sshTarget, preferredRuntime string) (runtimeName string, systemdAvailable bool, sudoMode string, remoteHome string, err error) {
	checkCmd := fmt.Sprintf(`set -eu
preferred_runtime=%s
runtime=""
if command -v "$preferred_runtime" >/dev/null 2>&1; then runtime="$preferred_runtime"; fi
if [ -z "$runtime" ]; then echo "remote runtime missing:$preferred_runtime" >&2; exit 40; fi
if command -v systemctl >/dev/null 2>&1; then systemd=1; else systemd=0; fi
sudo_mode="none"
if command -v sudo >/dev/null 2>&1; then sudo_mode="sudo"; fi
remote_home="${HOME:-}"
if [ -z "$remote_home" ]; then remote_home="$(cd && pwd)"; fi
if [ -z "$remote_home" ] || [ "${remote_home#/}" = "$remote_home" ]; then echo "remote home directory missing" >&2; exit 41; fi
printf 'REMOTE_RUNTIME=%%s\n' "$runtime"
printf 'SYSTEMD_AVAILABLE=%%s\n' "$systemd"
printf 'SUDO_MODE=%%s\n' "$sudo_mode"
printf 'REMOTE_HOME=%%s\n' "$remote_home"
`, shellQuote(normalizeRemoteDeployRuntime(preferredRuntime)))
	out, err := runSSHCommand(ctx, sshTarget, checkCmd)
	if err != nil {
		return "", false, "", "", err
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
		}
	}
	if runtimeName == "" {
		return "", false, "", "", fmt.Errorf("remote runtime detection failed")
	}
	if remoteHome == "" || !strings.HasPrefix(remoteHome, "/") {
		return "", false, "", "", fmt.Errorf("remote home directory detection failed")
	}
	return runtimeName, systemdAvailable, sudoMode, remoteHome, nil
}

func (s *Service) checkRemoteInstallCollision(ctx context.Context, sshTarget, remoteRoot, systemdUnit string) error {
	remoteRoot = strings.TrimSpace(remoteRoot)
	systemdUnit = strings.TrimSpace(systemdUnit)
	checkCmd := fmt.Sprintf(`set -eu
remote_root=%s
systemd_unit=%s
if [ -e "$remote_root" ]; then
  echo "REMOTE_ROOT_EXISTS=$remote_root"
  exit 42
fi
if command -v systemctl >/dev/null 2>&1 && [ -n "$systemd_unit" ]; then
  if systemctl cat "$systemd_unit" >/dev/null 2>&1 || systemctl status "$systemd_unit" >/dev/null 2>&1; then
    echo "SYSTEMD_UNIT_EXISTS=$systemd_unit"
    exit 43
  fi
fi
printf 'REMOTE_INSTALL_PATH_CLEAR=1\n'
`, shellQuote(remoteRoot), shellQuote(systemdUnit))
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
		case strings.Contains(trimmed, "SYSTEMD_UNIT_EXISTS="):
			unit := strings.TrimSpace(strings.TrimPrefix(lastMatchingLine(trimmed, "SYSTEMD_UNIT_EXISTS="), "SYSTEMD_UNIT_EXISTS="))
			if unit == "" {
				unit = systemdUnit
			}
			return fmt.Errorf("remote preflight failed: systemd unit already exists: %s", unit)
		default:
			return err
		}
	}
	return nil
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
	case strings.Contains(lower, "systemd unit already exists:"):
		unit := strings.TrimSpace(strings.TrimPrefix(message, "remote preflight failed: systemd unit already exists:"))
		if unit == "" {
			unit = "existing systemd unit"
		}
		return fmt.Errorf("Remote preflight failed for %s.\n\nWhat failed\n- The target systemd unit already exists: %s\n\nWhat to do\n- Remove or rename the old unit, or choose a different swarm name and rerun preflight.\n\nSuggested commands\n- ssh %s\n- systemctl status %s\n- systemctl disable --now %s   # if it is an old test unit you want to replace\n- unit_path=$(systemctl show -p FragmentPath --value %s)\n- rm -f \"$unit_path\"\n- systemctl daemon-reload", target, unit, target, unit, unit, unit)
	case strings.Contains(lower, "master swarm.conf tailscale_url is required"):
		return fmt.Errorf("Remote preflight failed on the master.\n\nWhat failed\n- The master startup config is missing tailscale_url.\n\nWhat to do\n- Set tailscale_url in the master swarm.conf so remote children know where to call back.\n- Then rerun preflight.")
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
		sourcePath := strings.TrimSpace(payload.SourcePath)
		if sourcePath == "" {
			continue
		}
		archiveName := fmt.Sprintf("payload-%02d-%s.tar.gz", idx+1, sanitizeSlug(filepath.Base(sourcePath)))
		includedFiles, includedBytes, gitRoot, err := gitTrackedStats(sourcePath)
		if err != nil {
			return nil, err
		}
		out = append(out, pebblestore.RemoteDeployPayloadRecord{
			ID:            fmt.Sprintf("payload-%02d", idx+1),
			SourcePath:    sourcePath,
			WorkspacePath: strings.TrimSpace(payload.WorkspacePath),
			WorkspaceName: strings.TrimSpace(payload.WorkspaceName),
			TargetPath:    firstNonEmpty(strings.TrimSpace(payload.TargetPath), "/workspaces"),
			Mode:          firstNonEmpty(strings.TrimSpace(payload.Mode), "rw"),
			GitRoot:       gitRoot,
			ArchiveName:   archiveName,
			IncludedFiles: includedFiles,
			IncludedBytes: includedBytes,
			ExcludedNote:  "Only Git-tracked files are included in remote payload archives.",
		})
	}
	return out, nil
}

func (s *Service) renderChildStartupConfig(record pebblestore.RemoteDeploySessionRecord, startupCfg startupconfig.FileConfig, hostState swarmruntime.LocalState, inviteToken string) string {
	cfg := startupconfig.Default(filepath.Join(firstNonEmpty(strings.TrimSpace(record.RemoteRoot), remoteRoot(record.ID)), "swarm.conf"))
	cfg.Mode = startupconfig.ModeBox
	cfg.Host = startupconfig.DefaultHost
	cfg.Port = startupconfig.DefaultPort
	cfg.AdvertiseHost = startupconfig.DefaultHost
	cfg.AdvertisePort = startupconfig.DefaultPort
	cfg.DesktopPort = startupconfig.DefaultDesktopPort
	cfg.SwarmMode = true
	cfg.Child = true
	cfg.NetworkMode = startupconfig.NetworkModeTailscale
	cfg.TailscaleURL = strings.TrimSpace(record.MasterTailscaleURL)
	cfg.BypassPermissions = record.BypassPermissions
	cfg.SwarmName = strings.TrimSpace(record.Name)
	cfg.ParentSwarmID = strings.TrimSpace(hostState.Node.SwarmID)
	cfg.PairingState = startupconfig.PairingStateBootstrapReady
	cfg.RemoteDeploy = startupconfig.RemoteDeployBootstrap{
		Enabled:           true,
		SessionID:         strings.TrimSpace(record.ID),
		SessionToken:      strings.TrimSpace(record.SessionToken),
		HostAPIBaseURL:    strings.TrimSpace(record.MasterTailscaleURL),
		HostDesktopURL:    strings.TrimSpace(startupCfg.TailscaleURL),
		InviteToken:       strings.TrimSpace(inviteToken),
		SyncEnabled:       record.SyncEnabled,
		SyncMode:          strings.TrimSpace(record.SyncMode),
		SyncOwnerSwarmID:  strings.TrimSpace(record.SyncOwnerSwarmID),
		SyncCredentialURL: firstNonEmpty(strings.TrimSpace(record.SyncCredentialURL), buildRemoteSyncCredentialURL(strings.TrimSpace(record.MasterTailscaleURL), strings.TrimSpace(record.ID))),
	}
	return startupconfig.Format(cfg)
}

func (s *Service) prepareRemoteBundle(ctx context.Context, workDir string, record *pebblestore.RemoteDeploySessionRecord, childCfgText string) error {
	_ = ctx
	if record == nil {
		return fmt.Errorf("remote deploy record is required")
	}
	bundleDir := filepath.Join(workDir, "bundle")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(bundleDir, "remote"), 0o755); err != nil {
		return err
	}
	cfgPath := filepath.Join(bundleDir, "remote", "swarm.conf")
	if err := os.WriteFile(cfgPath, []byte(childCfgText), 0o644); err != nil {
		return err
	}
	installerPath := filepath.Join(bundleDir, "remote", "install-remote-child.sh")
	if err := os.WriteFile(installerPath, []byte(remoteInstallerScript(*record)), 0o755); err != nil {
		return err
	}
	for _, payload := range record.Payloads {
		if payload.ArchiveName == "" || payload.SourcePath == "" {
			continue
		}
		archivePath := filepath.Join(bundleDir, "remote", payload.ArchiveName)
		if err := createGitTrackedArchive(payload.SourcePath, archivePath); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) copyRemoteBundle(ctx context.Context, workDir string, imageArtifact remoteImageArtifact, record *pebblestore.RemoteDeploySessionRecord) error {
	if record == nil {
		return fmt.Errorf("remote deploy record is required")
	}
	remoteDir := firstNonEmpty(strings.TrimSpace(record.RemoteRoot), remoteRoot(record.ID))
	if _, err := runSSHCommand(ctx, record.SSHSessionTarget, fmt.Sprintf("mkdir -p %s", shellQuote(remoteDir))); err != nil {
		return err
	}
	remoteHasImage, err := remoteImageExists(ctx, record.SSHSessionTarget, record.RemoteRuntime, firstNonEmpty(record.ImageRef, imageArtifact.ImageRef), record.SudoMode)
	if err != nil {
		return err
	}
	sourceDir := filepath.Join(workDir, "bundle", "remote") + string(filepath.Separator) + "."
	dest := fmt.Sprintf("%s:%s/", record.SSHSessionTarget, remoteDir)
	cmd := exec.CommandContext(ctx, "scp", "-r", sourceDir, dest)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp remote bundle: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if remoteHasImage {
		return nil
	}
	if strings.TrimSpace(imageArtifact.ArchivePath) == "" {
		return fmt.Errorf("remote image archive path is required")
	}
	imageDest := fmt.Sprintf("%s:%s", record.SSHSessionTarget, filepath.ToSlash(filepath.Join(remoteDir, "swarm-container-mvp.tar.gz")))
	cmd = exec.CommandContext(ctx, "scp", imageArtifact.ArchivePath, imageDest)
	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp remote image archive: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (s *Service) startRemoteBundle(ctx context.Context, record *pebblestore.RemoteDeploySessionRecord, tailscaleAuthKey string, syncVaultPassword string) (output, authURL, tailnetURL string, err error) {
	if record == nil {
		return "", "", "", fmt.Errorf("remote deploy record is required")
	}
	remoteDir := firstNonEmpty(strings.TrimSpace(record.RemoteRoot), remoteRoot(record.ID))
	cmd := fmt.Sprintf("cd %s && ./install-remote-child.sh", shellQuote(remoteDir))
	envVars := make([]string, 0, 2)
	if strings.TrimSpace(tailscaleAuthKey) != "" {
		envVars = append(envVars, fmt.Sprintf("TAILSCALE_AUTHKEY=%s", shellQuote(strings.TrimSpace(tailscaleAuthKey))))
	}
	if strings.TrimSpace(syncVaultPassword) != "" {
		envVars = append(envVars, fmt.Sprintf("SWARM_REMOTE_SYNC_VAULT_PASSWORD=%s", shellQuote(strings.TrimSpace(syncVaultPassword))))
	}
	if len(envVars) > 0 {
		cmd = fmt.Sprintf("cd %s && %s ./install-remote-child.sh", shellQuote(remoteDir), strings.Join(envVars, " "))
	}
	output, err = runSSHCommand(ctx, record.SSHSessionTarget, cmd)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "TAILSCALE_AUTH_URL="):
			authURL = strings.TrimSpace(strings.TrimPrefix(line, "TAILSCALE_AUTH_URL="))
		case strings.HasPrefix(line, "SWARM_TAILNET_URL="):
			tailnetURL = strings.TrimSpace(strings.TrimPrefix(line, "SWARM_TAILNET_URL="))
		}
	}
	return output, authURL, tailnetURL, err
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
		payloads = append(payloads, SessionPayload{
			ID:            payload.ID,
			SourcePath:    payload.SourcePath,
			WorkspacePath: payload.WorkspacePath,
			WorkspaceName: payload.WorkspaceName,
			TargetPath:    payload.TargetPath,
			Mode:          payload.Mode,
			GitRoot:       payload.GitRoot,
			ArchiveName:   payload.ArchiveName,
			IncludedFiles: payload.IncludedFiles,
			IncludedBytes: payload.IncludedBytes,
			ExcludedNote:  payload.ExcludedNote,
		})
	}
	preflight := SessionPreflight{
		PathID:           PathSessionPreflight,
		BuilderRuntime:   record.BuilderRuntime,
		RemoteRuntime:    record.RemoteRuntime,
		SSHReachable:     record.SSHReachable,
		SystemdAvailable: record.SystemdAvailable,
		SystemdUnit:      record.SystemdUnit,
		RemoteRoot:       record.RemoteRoot,
		FilesToCopy:      append([]string(nil), record.FilesToCopy...),
		Payloads:         payloads,
		Summary:          fmt.Sprintf("Build locally with %s, copy bundle over SSH to %s, install %s systemd unit, and wait for child approval.", firstNonEmpty(record.BuilderRuntime, "container runtime"), firstNonEmpty(record.SSHSessionTarget, "remote host"), firstNonEmpty(record.RemoteRuntime, "container runtime")),
		Checks: []string{
			"local builder runtime available",
			"remote SSH reachable",
			"remote container runtime available",
			"remote home directory resolved",
			"target remote install path does not already exist",
			"target systemd unit name does not already exist",
		},
	}
	return Session{
		ID:                 record.ID,
		Name:               record.Name,
		Status:             record.Status,
		SSHSessionTarget:   record.SSHSessionTarget,
		GroupID:            record.GroupID,
		GroupName:          record.GroupName,
		BuilderRuntime:     record.BuilderRuntime,
		RemoteRuntime:      record.RemoteRuntime,
		MasterTailscaleURL: record.MasterTailscaleURL,
		RemoteAuthURL:      record.RemoteAuthURL,
		RemoteTailnetURL:   record.RemoteTailnetURL,
		ImageRef:           record.ImageRef,
		ImageSignature:     record.ImageSignature,
		ImageArchiveBytes:  record.ImageArchiveBytes,
		EnrollmentID:       record.EnrollmentID,
		EnrollmentStatus:   record.EnrollmentStatus,
		ChildSwarmID:       record.ChildSwarmID,
		ChildName:          record.ChildName,
		HostSwarmID:        record.HostSwarmID,
		HostName:           record.HostName,
		HostPublicKey:      record.HostPublicKey,
		HostFingerprint:    record.HostFingerprint,
		HostAPIBaseURL:     record.HostAPIBaseURL,
		HostDesktopURL:     record.HostDesktopURL,
		BypassPermissions:  record.BypassPermissions,
		ContainerPackages:  mapRemoteStoredContainerPackageManifest(record.ContainerPackages),
		LastError:          record.LastError,
		LastRemoteOutput:   record.LastRemoteOutput,
		SyncEnabled:        record.SyncEnabled,
		SyncMode:           record.SyncMode,
		SyncOwnerSwarmID:   record.SyncOwnerSwarmID,
		Preflight:          preflight,
		CreatedAt:          record.CreatedAt,
		UpdatedAt:          record.UpdatedAt,
		ApprovedAt:         record.ApprovedAt,
		AttachedAt:         record.AttachedAt,
	}
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
	systemdUnit := strings.TrimSpace(record.SystemdUnit)
	if systemdUnit == "" {
		systemdUnit = systemdUnitName(record.ID)
	}
	remoteRoot := strings.TrimSpace(record.RemoteRoot)
	sudoPrefix := ""
	if strings.TrimSpace(record.SudoMode) == "sudo" {
		sudoPrefix = "sudo "
	}
	cmd := fmt.Sprintf(`set -eu
systemd_unit=%s
remote_root=%s
runtime=%s
if command -v systemctl >/dev/null 2>&1 && [ -n "$systemd_unit" ]; then
  unit_path="$(%ssystemctl show -p FragmentPath --value "$systemd_unit" 2>/dev/null || true)"
  %ssystemctl disable --now "$systemd_unit" >/dev/null 2>&1 || true
  if [ -n "$unit_path" ]; then
    %srm -f "$unit_path" >/dev/null 2>&1 || true
  fi
  %ssystemctl daemon-reload >/dev/null 2>&1 || true
fi
if [ "$runtime" = "podman" ]; then
  %spodman rm -f swarm-remote-child >/dev/null 2>&1 || true
else
  %sdocker rm -f swarm-remote-child >/dev/null 2>&1 || true
fi
if [ -n "$remote_root" ]; then
  %srm -rf "$remote_root" >/dev/null 2>&1 || true
fi
printf 'REMOTE_DELETE_OK=1\n'
`, shellQuote(systemdUnit), shellQuote(remoteRoot), shellQuote(runtimeName), sudoPrefix, sudoPrefix, sudoPrefix, sudoPrefix, sudoPrefix, sudoPrefix, sudoPrefix)
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

func remoteRoot(sessionID string) string {
	return filepath.ToSlash(filepath.Join("~/.local/share/swarm/remote-deploy", sanitizeSlug(sessionID)))
}

func remoteRootForHome(homeDir, sessionID string) string {
	homeDir = strings.TrimSpace(homeDir)
	if homeDir == "" {
		return remoteRoot(sessionID)
	}
	return filepath.ToSlash(filepath.Join(homeDir, ".local", "share", "swarm", "remote-deploy", sanitizeSlug(sessionID)))
}

func normalizeRemoteDeployRuntime(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "podman":
		return "podman"
	default:
		return "docker"
	}
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

func (s *Service) prepareRemoteImageArtifact(ctx context.Context, runtimeName string) (remoteImageArtifact, error) {
	buildRoot, err := resolveRemoteDeployBuildRoot(s.startupCWD)
	if err != nil {
		return remoteImageArtifact{}, err
	}
	if err := ensureRemoteDeployBackendBinaries(ctx, buildRoot); err != nil {
		return remoteImageArtifact{}, err
	}
	signature, err := remoteImageSignature(buildRoot)
	if err != nil {
		return remoteImageArtifact{}, err
	}
	imageRef := remoteDeployImageRef(signature)
	imageExists, err := runtimeImageExists(ctx, runtimeName, imageRef)
	if err != nil {
		return remoteImageArtifact{}, err
	}
	if !imageExists {
		if err := buildRemoteImage(ctx, runtimeName, imageRef, buildRoot); err != nil {
			return remoteImageArtifact{}, err
		}
	}
	cacheRoot, err := remoteDeployCacheRoot()
	if err != nil {
		return remoteImageArtifact{}, err
	}
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return remoteImageArtifact{}, err
	}
	archivePath := filepath.Join(cacheRoot, fmt.Sprintf("swarm-container-mvp-%s.tar.gz", signature))
	archiveHit := false
	if _, err := os.Stat(archivePath); err == nil {
		if err := validateImageArchive(archivePath); err == nil {
			archiveHit = true
		} else {
			_ = os.Remove(archivePath)
		}
	} else if !os.IsNotExist(err) {
		return remoteImageArtifact{}, err
	} else {
		if err := exportImageArchive(ctx, runtimeName, imageRef, archivePath); err != nil {
			return remoteImageArtifact{}, err
		}
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		return remoteImageArtifact{}, err
	}
	return remoteImageArtifact{
		ImageRef:      imageRef,
		Signature:     signature,
		ArchivePath:   archivePath,
		ArchiveBytes:  info.Size(),
		LocalImageHit: imageExists,
		ArchiveHit:    archiveHit,
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

func remoteDeployImageRef(signature string) string {
	signature = strings.TrimSpace(strings.ToLower(signature))
	if signature == "" {
		signature = "latest"
	}
	return fmt.Sprintf("localhost/swarm-container-mvp:remote-%s", signature)
}

func exportImageArchive(ctx context.Context, runtimeName, imageRef, destPath string) error {
	runtimeName = strings.TrimSpace(runtimeName)
	imageRef = strings.TrimSpace(imageRef)
	if runtimeName == "" {
		return fmt.Errorf("builder runtime is required")
	}
	if imageRef == "" {
		return fmt.Errorf("image ref is required")
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	tempPath := fmt.Sprintf("%s.%d.tmp", destPath, os.Getpid())
	plainTarPath := fmt.Sprintf("%s.%d.tar", destPath, os.Getpid())
	_ = os.Remove(tempPath)
	_ = os.Remove(plainTarPath)
	var saveCmd *exec.Cmd
	if runtimeName == "podman" {
		saveCmd = exec.CommandContext(ctx, "podman", "save", "--format", "docker-archive", "-o", plainTarPath, imageRef)
	} else {
		saveCmd = exec.CommandContext(ctx, "docker", "save", "-o", plainTarPath, imageRef)
	}
	if output, err := saveCmd.CombinedOutput(); err != nil {
		_ = os.Remove(plainTarPath)
		_ = os.Remove(tempPath)
		return fmt.Errorf("export image archive: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if err := gzipFile(tempPath, plainTarPath); err != nil {
		_ = os.Remove(plainTarPath)
		_ = os.Remove(tempPath)
		return fmt.Errorf("compress image archive: %w", err)
	}
	_ = os.Remove(plainTarPath)
	if err := validateImageArchive(tempPath); err != nil {
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

func gzipFile(destPath, sourcePath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	dest, err := os.Create(destPath)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(dest)
	if _, err := io.Copy(gz, source); err != nil {
		_ = gz.Close()
		_ = dest.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		_ = dest.Close()
		return err
	}
	return dest.Close()
}

func validateImageArchive(path string) error {
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

func buildRemoteImage(ctx context.Context, runtimeName, imageRef, buildRoot string) error {
	runtimeName = strings.TrimSpace(runtimeName)
	imageRef = strings.TrimSpace(imageRef)
	if runtimeName == "" {
		return fmt.Errorf("builder runtime is required")
	}
	if imageRef == "" {
		return fmt.Errorf("image ref is required")
	}
	buildCmd := exec.CommandContext(ctx, runtimeName, "build", "-f", "deploy/container-mvp/Containerfile", "-t", imageRef, ".")
	buildCmd.Dir = buildRoot
	if output, err := buildCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build remote image: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func runtimeImageExists(ctx context.Context, runtimeName, imageRef string) (bool, error) {
	runtimeName = strings.TrimSpace(runtimeName)
	imageRef = strings.TrimSpace(imageRef)
	if runtimeName == "" || imageRef == "" {
		return false, nil
	}
	cmd := exec.CommandContext(ctx, runtimeName, "image", "inspect", imageRef)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if _, ok := err.(*exec.ExitError); ok {
		return false, nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	return false, fmt.Errorf("inspect remote deploy image %q: %s", imageRef, message)
}

func remoteImageSignature(buildRoot string) (string, error) {
	h := sha256.New()
	inputs := []string{
		filepath.Join(buildRoot, "deploy", "container-mvp", "Containerfile"),
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

func createGitTrackedArchive(sourcePath, destArchive string) error {
	gitRoot, files, err := gitTrackedFiles(sourcePath)
	if err != nil {
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
	return nil
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
	cmd := exec.CommandContext(ctx, "ssh", target, "sh", "-se")
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
	sudoPrefix := ""
	if strings.TrimSpace(record.SudoMode) == "sudo" {
		sudoPrefix = "sudo "
	}
	imageRef := firstNonEmpty(strings.TrimSpace(record.ImageRef), "localhost/swarm-container-mvp:latest")
	remoteStateRoot := filepath.ToSlash(filepath.Join(record.RemoteRoot, "state"))
	tailscaleStateDir := filepath.ToSlash(filepath.Join(remoteStateRoot, "tailscale"))
	swarmdStateDir := filepath.ToSlash(filepath.Join(remoteStateRoot, "swarmd"))
	stateDirs := fmt.Sprintf(`%smkdir -p %s
%smkdir -p %s
`, sudoPrefix, shellQuote(tailscaleStateDir), sudoPrefix, shellQuote(swarmdStateDir))
	payloadExtract := ""
	for _, payload := range record.Payloads {
		if payload.ArchiveName == "" {
			continue
		}
		payloadExtract += fmt.Sprintf("%smkdir -p %s\n%star -xzf %s -C %s\n", sudoPrefix, shellQuote(payload.TargetPath), sudoPrefix, shellQuote(payload.ArchiveName), shellQuote(payload.TargetPath))
	}
	podmanPayloadVolumes := remotePayloadVolumeFlags(record.Payloads, "podman")
	dockerPayloadVolumes := remotePayloadVolumeFlags(record.Payloads, "docker")
	runtimeLoad := fmt.Sprintf(`image_ref=%s
if [ "$runtime" = "podman" ]; then
  if %spodman image inspect "$image_ref" >/dev/null 2>&1; then
    :
  elif [ -f swarm-container-mvp.tar.gz ]; then
    gunzip -c swarm-container-mvp.tar.gz | %spodman load
  else
    echo "[swarm-remote-child] required image archive missing for $image_ref" >&2
    exit 1
  fi
else
  if %sdocker image inspect "$image_ref" >/dev/null 2>&1; then
    :
  elif [ -f swarm-container-mvp.tar.gz ]; then
    gunzip -c swarm-container-mvp.tar.gz | %sdocker load
  else
    echo "[swarm-remote-child] required image archive missing for $image_ref" >&2
    exit 1
  fi
fi`, shellQuote(imageRef), sudoPrefix, sudoPrefix, sudoPrefix, sudoPrefix)
	containerConfigHome := "/var/lib/swarm-config"
	runCmd := fmt.Sprintf(`remote_root=%s
runtime=%s
container_config_home=%s
config_mount_target="$container_config_home/swarm/swarm.conf"
tailscale_state_dir=%s
swarmd_state_dir=%s
  if [ "$runtime" = "podman" ]; then
  podman rm -f swarm-remote-child > /dev/null 2>&1 || true
  if [ -n "${TAILSCALE_AUTHKEY:-}" ]; then
    podman run -d --name swarm-remote-child --restart=always \
      --cap-add=NET_ADMIN \
      --device=/dev/net/tun \
      -e XDG_CONFIG_HOME="$container_config_home" \
      -e TS_TUN_MODE=auto \
      -e TS_AUTHKEY="${TAILSCALE_AUTHKEY}" \
      -e SWARM_REMOTE_SYNC_VAULT_PASSWORD="${SWARM_REMOTE_SYNC_VAULT_PASSWORD:-}" \
      -v "$remote_root/swarm.conf:$config_mount_target:Z" \
      -v "$tailscale_state_dir:/var/lib/tailscale:Z" \
      -v "$swarmd_state_dir:/var/lib/swarmd:Z" \
%s      %s
  else
    podman run -d --name swarm-remote-child --restart=always \
      --cap-add=NET_ADMIN \
      --device=/dev/net/tun \
      -e XDG_CONFIG_HOME="$container_config_home" \
      -e TS_TUN_MODE=auto \
      -e SWARM_REMOTE_SYNC_VAULT_PASSWORD="${SWARM_REMOTE_SYNC_VAULT_PASSWORD:-}" \
      -v "$remote_root/swarm.conf:$config_mount_target:Z" \
      -v "$tailscale_state_dir:/var/lib/tailscale:Z" \
      -v "$swarmd_state_dir:/var/lib/swarmd:Z" \
%s      %s
  fi
else
  docker rm -f swarm-remote-child > /dev/null 2>&1 || true
  if [ -n "${TAILSCALE_AUTHKEY:-}" ]; then
    docker run -d --name swarm-remote-child --restart=always \
      --cap-add=NET_ADMIN \
      --device=/dev/net/tun \
      -e XDG_CONFIG_HOME="$container_config_home" \
      -e TS_TUN_MODE=auto \
      -e TS_AUTHKEY="${TAILSCALE_AUTHKEY}" \
      -e SWARM_REMOTE_SYNC_VAULT_PASSWORD="${SWARM_REMOTE_SYNC_VAULT_PASSWORD:-}" \
      -v "$remote_root/swarm.conf:$config_mount_target" \
      -v "$tailscale_state_dir:/var/lib/tailscale" \
      -v "$swarmd_state_dir:/var/lib/swarmd" \
%s      %s
  else
    docker run -d --name swarm-remote-child --restart=always \
      --cap-add=NET_ADMIN \
      --device=/dev/net/tun \
      -e XDG_CONFIG_HOME="$container_config_home" \
      -e TS_TUN_MODE=auto \
      -e SWARM_REMOTE_SYNC_VAULT_PASSWORD="${SWARM_REMOTE_SYNC_VAULT_PASSWORD:-}" \
      -v "$remote_root/swarm.conf:$config_mount_target" \
      -v "$tailscale_state_dir:/var/lib/tailscale" \
      -v "$swarmd_state_dir:/var/lib/swarmd" \
%s      %s
  fi
fi`, shellQuote(record.RemoteRoot), shellQuote(record.RemoteRuntime), shellQuote(containerConfigHome), shellQuote(tailscaleStateDir), shellQuote(swarmdStateDir), podmanPayloadVolumes, shellQuote(imageRef), podmanPayloadVolumes, shellQuote(imageRef), dockerPayloadVolumes, shellQuote(imageRef), dockerPayloadVolumes, shellQuote(imageRef))
	startScriptPath := filepath.ToSlash(filepath.Join(record.RemoteRoot, "run-remote-child.sh"))
	startScriptWrite := fmt.Sprintf(`cat > %s <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
%s
SCRIPT
chmod 0755 %s
`, shellQuote(startScriptPath), runCmd, shellQuote(startScriptPath))
	unitPath := string(os.PathSeparator) + filepath.ToSlash(filepath.Join("etc", "systemd", "system", strings.TrimSpace(record.SystemdUnit)))
	execStopCmd := `docker stop swarm-remote-child`
	if strings.TrimSpace(record.RemoteRuntime) == "podman" {
		execStopCmd = `podman stop swarm-remote-child`
	}
	unitWrite := fmt.Sprintf(`cat > %s <<'UNIT'
[Unit]
Description=Swarm remote child %s
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=%s
ExecStart=/bin/bash %s
ExecStop=/bin/sh -lc '%s'

[Install]
WantedBy=multi-user.target
UNIT`, shellQuote(unitPath), record.Name, record.RemoteRoot, startScriptPath, strings.ReplaceAll(execStopCmd, "'", `'"'"'`))
	if sudoPrefix != "" {
		unitWrite = fmt.Sprintf(`cat <<'UNIT' | sudo tee %s >/dev/null
[Unit]
Description=Swarm remote child %s
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=%s
ExecStart=/bin/bash %s
ExecStop=/bin/sh -lc '%s'

[Install]
WantedBy=multi-user.target
UNIT`, shellQuote(unitPath), record.Name, record.RemoteRoot, startScriptPath, strings.ReplaceAll(execStopCmd, "'", `'"'"'`))
	}
	unitInstall := fmt.Sprintf(`%s
%ssystemctl daemon-reload
if [ -n "${TAILSCALE_AUTHKEY:-}" ] || [ -n "${SWARM_REMOTE_SYNC_VAULT_PASSWORD:-}" ]; then
  %ssystemctl enable %s
else
  %ssystemctl enable --now %s
fi
`, unitWrite, sudoPrefix, sudoPrefix, shellQuote(record.SystemdUnit), sudoPrefix, shellQuote(record.SystemdUnit))
	fallbackRunCmd := fmt.Sprintf(`/bin/bash %s`, shellQuote(startScriptPath))
	if sudoPrefix != "" {
		fallbackRunCmd = fmt.Sprintf(`sudo --preserve-env=TAILSCALE_AUTHKEY,SWARM_REMOTE_SYNC_VAULT_PASSWORD /bin/bash %s`, shellQuote(startScriptPath))
	}
	authStartCmd := fmt.Sprintf("systemctl set-environment \"TAILSCALE_AUTHKEY=${TAILSCALE_AUTHKEY:-}\" \"SWARM_REMOTE_SYNC_VAULT_PASSWORD=${SWARM_REMOTE_SYNC_VAULT_PASSWORD:-}\"\nsystemctl start %s\nsystemctl unset-environment TAILSCALE_AUTHKEY SWARM_REMOTE_SYNC_VAULT_PASSWORD", shellQuote(record.SystemdUnit))
	if sudoPrefix != "" {
		authStartCmd = fmt.Sprintf("sudo systemctl set-environment \"TAILSCALE_AUTHKEY=${TAILSCALE_AUTHKEY:-}\" \"SWARM_REMOTE_SYNC_VAULT_PASSWORD=${SWARM_REMOTE_SYNC_VAULT_PASSWORD:-}\"\nsudo systemctl start %s\nsudo systemctl unset-environment TAILSCALE_AUTHKEY SWARM_REMOTE_SYNC_VAULT_PASSWORD", shellQuote(record.SystemdUnit))
	}
	logCmd := `docker logs --tail 200 swarm-remote-child 2>&1 || true`
	containerRunningCmd := `docker inspect -f '{{.State.Running}}' swarm-remote-child 2>/dev/null || true`
	if strings.TrimSpace(record.RemoteRuntime) == "podman" {
		logCmd = `podman logs --tail 200 swarm-remote-child 2>&1 || true`
		containerRunningCmd = `podman inspect -f '{{.State.Running}}' swarm-remote-child 2>/dev/null || true`
	}
	if sudoPrefix != "" {
		logCmd = sudoPrefix + logCmd
		containerRunningCmd = sudoPrefix + containerRunningCmd
	}
	return fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
remote_root=%s
cd "$remote_root"
runtime=%s
%s
%s
%s
%s
if command -v systemctl > /dev/null 2>&1; then
  %s
  if [ -n "${TAILSCALE_AUTHKEY:-}" ] || [ -n "${SWARM_REMOTE_SYNC_VAULT_PASSWORD:-}" ]; then
    %s
  fi
else
  %s
fi
log_output=""
auth_url=""
tailnet_url=""
container_running=""
deadline=$((SECONDS + 30))
while :; do
  if [ "$runtime" = "podman" ]; then
    log_output="$(%s)"
    container_running="$(%s)"
  else
    log_output="$(%s)"
    container_running="$(%s)"
  fi
  auth_url="$(printf '%%s\n' "$log_output" | sed -n 's/^TAILSCALE_AUTH_URL=//p' | tail -n 1)"
  tailnet_url="$(printf '%%s\n' "$log_output" | sed -n 's/^SWARM_TAILNET_URL=//p' | tail -n 1)"
  if [ -n "$auth_url" ] || [ -n "$tailnet_url" ]; then
    break
  fi
  if [ "${container_running}" != "true" ] && [ "${SECONDS}" -ge "${deadline}" ]; then
    break
  fi
  if [ "${SECONDS}" -ge "${deadline}" ]; then
    break
  fi
  sleep 1
done
printf '%%s\n' "$log_output"
printf 'TAILSCALE_AUTH_URL=%%s\n' "$auth_url"
printf 'SWARM_TAILNET_URL=%%s\n' "$tailnet_url"
`, shellQuote(record.RemoteRoot), shellQuote(record.RemoteRuntime), runtimeLoad, stateDirs, payloadExtract, startScriptWrite, unitInstall, authStartCmd, fallbackRunCmd, logCmd, containerRunningCmd, logCmd, containerRunningCmd)
}

func remotePayloadVolumeFlags(payloads []pebblestore.RemoteDeployPayloadRecord, runtimeName string) string {
	seen := make(map[string]struct{}, len(payloads))
	lines := make([]string, 0, len(payloads))
	for _, payload := range payloads {
		targetPath := strings.TrimSpace(payload.TargetPath)
		if targetPath == "" {
			continue
		}
		volumeSpec := targetPath + ":" + targetPath
		if strings.EqualFold(strings.TrimSpace(payload.Mode), "ro") {
			volumeSpec += ":ro"
		}
		if runtimeName == "podman" {
			if strings.Contains(volumeSpec, ":ro") {
				volumeSpec += ",Z"
			} else {
				volumeSpec += ":Z"
			}
		}
		if _, ok := seen[volumeSpec]; ok {
			continue
		}
		seen[volumeSpec] = struct{}{}
		lines = append(lines, fmt.Sprintf("    -v %s \\", shellQuote(volumeSpec)))
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
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
