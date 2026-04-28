package deploy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	agentruntime "swarm/packages/swarmd/internal/agent"
	auth "swarm/packages/swarmd/internal/auth"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	"swarm/packages/swarmd/internal/tailscalehttp"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

const (
	PathContainerRuntime            = "deploy.container.runtime.v1"
	PathContainerList               = "deploy.container.list.v1"
	PathContainerCreate             = "deploy.container.create.v1"
	PathContainerAction             = "deploy.container.action.v1"
	PathContainerDelete             = "deploy.container.delete.v1"
	PathContainerAttachChildState   = "deploy.container.attach.child_state.v1"
	PathContainerAttachRequest      = "deploy.container.attach.request.v1"
	PathContainerAttachStatus       = "deploy.container.attach.status.v1"
	PathContainerAttachApprove      = "deploy.container.attach.approve.v1"
	PathContainerAttachFinalize     = "deploy.container.attach.finalize.v1"
	PathContainerSyncCredentials    = "deploy.container.sync.credentials.v1"
	PathContainerSyncAgents         = "deploy.container.sync.agents.v1"
	PathContainerWorkspaceBootstrap = "deploy.container.workspace-bootstrap.v1"

	childLocalTransportMountTargetDir = "/run/swarm-parent-transport"
	childLocalTransportSocketPath     = childLocalTransportMountTargetDir + "/api.sock"
	childLocalTransportBaseURL        = "http://swarm-local-transport"
	managedCredentialSyncPollInterval = 5 * time.Second
	peerAuthSwarmIDHeader             = "X-Swarm-Peer-ID"
	peerAuthTokenHeader               = "X-Swarm-Peer-Token"
	remoteSyncVaultPasswordEnvKey     = "SWARM_REMOTE_SYNC_VAULT_PASSWORD"
	syncManagedVaultKeyHeader         = "X-Swarm-Sync-Managed-Vault-Key"
)

type ContainerRuntimeStatus struct {
	Recommended string   `json:"recommended"`
	Available   []string `json:"available"`
	Warning     string   `json:"warning,omitempty"`
	PathID      string   `json:"path_id"`
}

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

type ContainerDeployment struct {
	ID                  string                        `json:"id"`
	Kind                string                        `json:"kind"`
	Name                string                        `json:"name"`
	Status              string                        `json:"status"`
	Runtime             string                        `json:"runtime"`
	GroupID             string                        `json:"group_id,omitempty"`
	GroupName           string                        `json:"group_name,omitempty"`
	GroupNetworkName    string                        `json:"group_network_name,omitempty"`
	SyncEnabled         bool                          `json:"sync_enabled,omitempty"`
	SyncMode            string                        `json:"sync_mode,omitempty"`
	SyncModules         []string                      `json:"sync_modules,omitempty"`
	SyncOwnerSwarmID    string                        `json:"sync_owner_swarm_id,omitempty"`
	ContainerName       string                        `json:"container_name,omitempty"`
	ContainerID         string                        `json:"container_id,omitempty"`
	HostAPIBaseURL      string                        `json:"host_api_base_url,omitempty"`
	BackendHostPort     int                           `json:"backend_host_port"`
	DesktopHostPort     int                           `json:"desktop_host_port"`
	Image               string                        `json:"image,omitempty"`
	AttachStatus        string                        `json:"attach_status,omitempty"`
	LastAttachError     string                        `json:"last_attach_error,omitempty"`
	BootstrapSecretSent bool                          `json:"bootstrap_secret_sent"`
	BypassPermissions   bool                          `json:"bypass_permissions,omitempty"`
	ChildSwarmID        string                        `json:"child_swarm_id,omitempty"`
	ChildDisplayName    string                        `json:"child_display_name,omitempty"`
	ChildBackendURL     string                        `json:"child_backend_url,omitempty"`
	ChildDesktopURL     string                        `json:"child_desktop_url,omitempty"`
	WorkspaceBootstrap  []ContainerWorkspaceBootstrap `json:"workspace_bootstrap,omitempty"`
	ContainerPackages   ContainerPackageManifest      `json:"container_packages,omitempty"`
	CreatedAt           int64                         `json:"created_at"`
	UpdatedAt           int64                         `json:"updated_at"`
}

type ContainerCreateInput struct {
	Name               string
	Runtime            string
	Image              string
	Mounts             []localcontainers.Mount
	WorkspaceBootstrap []ContainerWorkspaceBootstrap
	ContainerPackages  ContainerPackageManifest
	GroupID            string
	GroupName          string
	GroupNetworkName   string
	SyncEnabled        bool
	SyncMode           string
	SyncModules        []string
	SyncVaultPassword  string
	BypassPermissions  bool
}

type ContainerActionInput struct {
	ID     string
	Action string
}

type ContainerAttachRequestInput struct {
	DeploymentID      string
	BootstrapSecret   string
	ChildSwarmID      string
	ChildDisplayName  string
	ChildBackendURL   string
	ChildDesktopURL   string
	ChildPublicKey    string
	ChildFingerprint  string
	RequestedAtMillis int64
}

type ContainerAttachStatusInput struct {
	DeploymentID    string
	BootstrapSecret string
	ChildSwarmID    string
}

type ContainerAttachApproveInput struct {
	DeploymentID             string
	BootstrapSecret          string
	HostSwarmID              string
	HostDisplayName          string
	HostPublicKey            string
	HostFingerprint          string
	HostBackendURL           string
	HostDesktopURL           string
	HostToChildPeerAuthToken string
	ChildToHostPeerAuthToken string
	GroupID                  string
	GroupName                string
	GroupNetworkName         string
	SyncVaultPassword        string
}

type ContainerAttachFinalizeInput struct {
	DeploymentID             string                        `json:"deployment_id"`
	BootstrapSecret          string                        `json:"bootstrap_secret"`
	HostSwarmID              string                        `json:"host_swarm_id"`
	HostDisplayName          string                        `json:"host_display_name"`
	HostPublicKey            string                        `json:"host_public_key"`
	HostFingerprint          string                        `json:"host_fingerprint"`
	HostBackendURL           string                        `json:"host_backend_url"`
	HostDesktopURL           string                        `json:"host_desktop_url"`
	GroupID                  string                        `json:"group_id"`
	GroupName                string                        `json:"group_name"`
	GroupNetworkName         string                        `json:"group_network_name"`
	HostToChildPeerAuthToken string                        `json:"host_to_child_peer_auth_token,omitempty"`
	ChildToHostPeerAuthToken string                        `json:"child_to_host_peer_auth_token,omitempty"`
	SyncMode                 string                        `json:"sync_mode,omitempty"`
	SyncModules              []string                      `json:"sync_modules,omitempty"`
	SyncOwnerSwarmID         string                        `json:"sync_owner_swarm_id,omitempty"`
	SyncBundlePassword       string                        `json:"sync_bundle_password,omitempty"`
	SyncBundle               []byte                        `json:"sync_bundle,omitempty"`
	SyncVaultPassword        string                        `json:"sync_vault_password,omitempty"`
	SyncManagedVaultKey      string                        `json:"-"`
	WorkspaceBootstrap       []ContainerWorkspaceBootstrap `json:"workspace_bootstrap,omitempty"`
}

type ContainerSyncCredentialRequestInput struct {
	DeploymentID    string `json:"deployment_id"`
	BootstrapSecret string `json:"bootstrap_secret"`
	VaultPassword   string `json:"vault_password,omitempty"`
}

type ContainerWorkspaceBootstrapRequestInput struct {
	DeploymentID    string `json:"deployment_id"`
	BootstrapSecret string `json:"bootstrap_secret"`
}

type ContainerSyncCredentialBundle struct {
	OwnerSwarmID   string `json:"owner_swarm_id"`
	BundlePassword string `json:"bundle_password"`
	Bundle         []byte `json:"bundle"`
	Exported       int    `json:"exported"`
	ExportedAt     int64  `json:"exported_at,omitempty"`
}

type ContainerSyncAgentBundle struct {
	State        agentruntime.State `json:"state"`
	SnapshotHash string             `json:"snapshot_hash"`
	ExportedAt   int64              `json:"exported_at,omitempty"`
}

type ContainerAttachState struct {
	DeploymentID             string `json:"deployment_id"`
	AttachStatus             string `json:"attach_status"`
	ChildSwarmID             string `json:"child_swarm_id,omitempty"`
	ChildDisplayName         string `json:"child_display_name,omitempty"`
	ChildBackendURL          string `json:"child_backend_url,omitempty"`
	ChildDesktopURL          string `json:"child_desktop_url,omitempty"`
	ChildFingerprint         string `json:"child_fingerprint,omitempty"`
	HostSwarmID              string `json:"host_swarm_id,omitempty"`
	HostDisplayName          string `json:"host_display_name,omitempty"`
	HostPublicKey            string `json:"host_public_key,omitempty"`
	HostFingerprint          string `json:"host_fingerprint,omitempty"`
	HostBackendURL           string `json:"host_backend_url,omitempty"`
	HostDesktopURL           string `json:"host_desktop_url,omitempty"`
	GroupID                  string `json:"group_id,omitempty"`
	GroupName                string `json:"group_name,omitempty"`
	GroupNetworkName         string `json:"group_network_name,omitempty"`
	HostToChildPeerAuthToken string `json:"host_to_child_peer_auth_token,omitempty"`
	ChildToHostPeerAuthToken string `json:"child_to_host_peer_auth_token,omitempty"`
	SyncVaultPassword        string `json:"sync_vault_password,omitempty"`
	SyncManagedVaultKey      string `json:"sync_managed_vault_key,omitempty"`
	BootstrapSecretExpires   int64  `json:"bootstrap_secret_expires_at,omitempty"`
	LastError                string `json:"last_error,omitempty"`
	DecidedAt                int64  `json:"decided_at,omitempty"`
	UpdatedAt                int64  `json:"updated_at"`
}

type pendingSyncVaultPassword struct {
	Password  string
	ExpiresAt int64
}

type ContainerWorkspaceBootstrapDirectory = pebblestore.DeployContainerWorkspaceBootstrapDirectory

type ContainerWorkspaceBootstrap = pebblestore.DeployContainerWorkspaceBootstrap

type Service struct {
	store                        *pebblestore.DeployContainerStore
	containers                   *localcontainers.Service
	swarms                       *swarmruntime.Service
	swarmStore                   *pebblestore.SwarmStore
	auth                         *auth.Service
	agents                       *agentruntime.Service
	workspace                    *workspaceruntime.Service
	startupPath                  string
	client                       *http.Client
	hostCallbackURLFunc          func(string) (string, bool)
	localTransportHostSocketPath string
	pendingMu                    sync.Mutex
	pendingSyncVaultPasswords    map[string]pendingSyncVaultPassword
	agentSnapshotMu              sync.Mutex
	agentSnapshotHash            string
}

func NewService(store *pebblestore.DeployContainerStore, containers *localcontainers.Service, swarms *swarmruntime.Service, swarmStore *pebblestore.SwarmStore, authSvc *auth.Service, agentSvc *agentruntime.Service, workspaceSvc *workspaceruntime.Service, startupPath string) *Service {
	return &Service{
		store:                     store,
		containers:                containers,
		swarms:                    swarms,
		swarmStore:                swarmStore,
		auth:                      authSvc,
		agents:                    agentSvc,
		workspace:                 workspaceSvc,
		startupPath:               strings.TrimSpace(startupPath),
		client:                    newBootstrapClient(),
		pendingSyncVaultPasswords: make(map[string]pendingSyncVaultPassword),
	}
}

func (s *Service) SetHostCallbackURLResolver(resolver func(string) (string, bool)) {
	if s == nil {
		return
	}
	s.hostCallbackURLFunc = resolver
}

func (s *Service) SetLocalTransportSocketPath(path string) {
	if s == nil {
		return
	}
	s.localTransportHostSocketPath = strings.TrimSpace(path)
}

func (s *Service) configuredChildLocalTransportSocketPath() string {
	if s == nil || strings.TrimSpace(s.localTransportHostSocketPath) == "" {
		return ""
	}
	return childLocalTransportSocketPath
}

func (s *Service) rememberPendingSyncVaultPassword(deploymentID, password string, expiresAt int64) {
	if s == nil {
		return
	}
	deploymentID = strings.TrimSpace(deploymentID)
	password = strings.TrimSpace(password)
	if deploymentID == "" || password == "" {
		return
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if s.pendingSyncVaultPasswords == nil {
		s.pendingSyncVaultPasswords = make(map[string]pendingSyncVaultPassword)
	}
	s.pendingSyncVaultPasswords[deploymentID] = pendingSyncVaultPassword{
		Password:  password,
		ExpiresAt: expiresAt,
	}
}

func (s *Service) resolvePendingSyncVaultPassword(deploymentID string) string {
	if s == nil {
		return ""
	}
	deploymentID = strings.TrimSpace(deploymentID)
	if deploymentID == "" {
		return ""
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	entry, ok := s.pendingSyncVaultPasswords[deploymentID]
	if !ok {
		return ""
	}
	if entry.ExpiresAt > 0 && time.Now().UnixMilli() > entry.ExpiresAt {
		delete(s.pendingSyncVaultPasswords, deploymentID)
		return ""
	}
	return strings.TrimSpace(entry.Password)
}

func (s *Service) clearPendingSyncVaultPassword(deploymentID string) {
	if s == nil {
		return
	}
	deploymentID = strings.TrimSpace(deploymentID)
	if deploymentID == "" {
		return
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	delete(s.pendingSyncVaultPasswords, deploymentID)
}

func (s *Service) localTransportMountArgs() []string {
	if s == nil {
		return nil
	}
	socketPath := strings.TrimSpace(s.localTransportHostSocketPath)
	if socketPath == "" {
		return nil
	}
	return []string{"-v", fmt.Sprintf("%s:%s", filepath.Dir(socketPath), childLocalTransportMountTargetDir)}
}

func (s *Service) RuntimeStatus(ctx context.Context) (ContainerRuntimeStatus, error) {
	if s == nil || s.containers == nil {
		return ContainerRuntimeStatus{}, fmt.Errorf("deploy container service is not configured")
	}
	status, err := s.containers.RuntimeStatus(ctx)
	if err != nil {
		return ContainerRuntimeStatus{}, err
	}
	return ContainerRuntimeStatus{
		Recommended: status.Recommended,
		Available:   append([]string(nil), status.Available...),
		Warning:     status.Warning,
		PathID:      PathContainerRuntime,
	}, nil
}

func (s *Service) List(ctx context.Context) ([]ContainerDeployment, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("deploy container service is not configured")
	}
	records, err := s.store.List(500)
	if err != nil {
		return nil, err
	}
	out := make([]ContainerDeployment, 0, len(records))
	for _, record := range records {
		out = append(out, mapContainerRecord(record))
	}
	return out, nil
}

func (s *Service) Create(ctx context.Context, input ContainerCreateInput) (ContainerDeployment, error) {
	if s == nil || s.store == nil || s.containers == nil || s.swarms == nil || s.swarmStore == nil {
		return ContainerDeployment{}, fmt.Errorf("deploy container service is not configured")
	}
	startupCfg, hostState, err := s.resolveBootstrapContext()
	if err != nil {
		return ContainerDeployment{}, err
	}
	bootstrapSecret, err := generateSecretToken(24)
	if err != nil {
		return ContainerDeployment{}, err
	}
	runtimeName := strings.TrimSpace(input.Runtime)
	runtimeStatus, err := s.containers.RuntimeStatus(ctx)
	if err != nil {
		return ContainerDeployment{}, err
	}
	if runtimeName == "" {
		runtimeName = strings.TrimSpace(runtimeStatus.Recommended)
	}
	localTransportSocketPath := s.configuredChildLocalTransportSocketPath()
	hostRuntimeHost, hostAPIBaseURL, hostDesktopURL, hostDrivenAttach, err := resolveLocalContainerBootstrapTargets(startupCfg, hostState, runtimeName, localTransportSocketPath)
	if err != nil {
		return ContainerDeployment{}, err
	}
	log.Printf("deploy create bootstrap resolved runtime=%q host_runtime_host=%q host_api_base_url=%q host_desktop_url=%q host_driven=%t", strings.TrimSpace(runtimeName), hostRuntimeHost, hostAPIBaseURL, hostDesktopURL, hostDrivenAttach)
	hostPort, err := s.containers.ResolveCreateHostPort(hostAPIBaseURL, 0)
	if err != nil {
		return ContainerDeployment{}, err
	}
	group, err := s.resolveTargetGroupForCreate(hostState, input)
	if err != nil {
		return ContainerDeployment{}, err
	}
	groupID := strings.TrimSpace(group.ID)
	groupName := firstNonEmpty(group.Name, groupID)
	groupNetworkName := firstNonEmpty(group.NetworkName, swarmruntime.SuggestedGroupNetworkName(groupName, groupID))
	deploymentID := suggestedDeploymentID(input.Name)
	syncConfig := workspaceruntime.NormalizeReplicationSync(workspaceruntime.ReplicationSyncInput{
		Enabled: input.SyncEnabled,
		Mode:    input.SyncMode,
		Modules: input.SyncModules,
	})
	syncEnabled := syncConfig.Enabled
	syncVaultPassword := strings.TrimSpace(input.SyncVaultPassword)
	if syncEnabled {
		if err := s.requireManagedSyncVaultPassword(syncVaultPassword); err != nil {
			return ContainerDeployment{}, err
		}
	}
	syncMode := ""
	syncModules := []string(nil)
	syncOwnerSwarmID := ""
	syncCredentialURL := ""
	syncAgentURL := ""
	if syncEnabled {
		syncMode = syncConfig.Mode
		syncModules = append([]string(nil), syncConfig.Modules...)
		syncOwnerSwarmID = strings.TrimSpace(hostState.Node.SwarmID)
		if workspaceruntime.ReplicationSyncModuleEnabled(syncModules, workspaceruntime.ReplicationSyncModuleCredentials) {
			syncCredentialURL = buildDeploymentSyncCredentialURL(hostAPIBaseURL)
		}
		if workspaceruntime.ReplicationSyncModuleEnabled(syncModules, workspaceruntime.ReplicationSyncModuleAgents) || workspaceruntime.ReplicationSyncModuleEnabled(syncModules, workspaceruntime.ReplicationSyncModuleCustomTools) {
			syncAgentURL = buildDeploymentSyncAgentURL(hostAPIBaseURL)
		}
	}
	extraRunArgs := []string(nil)
	if localTransportSocketPath != "" {
		extraRunArgs = s.localTransportMountArgs()
	}
	runtimeMount := localcontainers.CurrentRuntimeMount()
	container, createErr := s.containers.Create(ctx, localcontainers.CreateInput{
		Name:              input.Name,
		Runtime:           input.Runtime,
		NetworkName:       groupNetworkName,
		HostAPIBaseURL:    hostAPIBaseURL,
		HostPort:          hostPort,
		Image:             input.Image,
		ContainerPackages: localcontainers.ContainerPackageManifest(mapContainerPackageManifest(input.ContainerPackages)),
		Mounts:            input.Mounts,
		ExtraRunArgs:      extraRunArgs,
		RuntimeMount:      runtimeMount,
		Env: buildChildContainerEnv(containerBootstrapEnvInput{
			HostState:                hostState,
			ChildName:                strings.TrimSpace(input.Name),
			DeploymentID:             deploymentID,
			BootstrapSecret:          bootstrapSecret,
			HostAPIBaseURL:           hostAPIBaseURL,
			HostDesktopURL:           hostDesktopURL,
			LocalTransportSocketPath: localTransportSocketPath,
			ChildAdvertiseHost:       hostRuntimeHost,
			ChildAdvertisePort:       hostPort,
			HostDriven:               hostDrivenAttach,
			SyncEnabled:              syncEnabled,
			SyncMode:                 syncMode,
			SyncModules:              append([]string(nil), syncModules...),
			SyncOwnerSwarmID:         syncOwnerSwarmID,
			SyncCredentialURL:        syncCredentialURL,
			SyncAgentURL:             syncAgentURL,
			BypassPermissions:        input.BypassPermissions,
		}),
	})
	if createErr != nil && !createResultCanBePersisted(container) {
		return ContainerDeployment{}, createErr
	}
	log.Printf("deploy create launched runtime=%q deployment_id=%q group_id=%q group_network_name=%q host_port=%d create_err=%v", strings.TrimSpace(input.Runtime), deploymentID, groupID, groupNetworkName, hostPort, createErr)
	resolvedRuntimeName := firstNonEmpty(container.Runtime, strings.TrimSpace(input.Runtime), runtimeStatus.Recommended)
	if deploymentID == "" {
		deploymentID = strings.TrimSpace(container.ID)
	}
	now := time.Now()
	record := pebblestore.DeployContainerRecord{
		ID:                  firstNonEmpty(deploymentID, container.ID),
		Kind:                "container",
		Name:                createResultDisplayName(input, container),
		Status:              normalizeDeploymentStatus(container.Status),
		Runtime:             resolvedRuntimeName,
		GroupNetworkName:    groupNetworkName,
		ContainerName:       container.ContainerName,
		ContainerID:         container.ContainerID,
		HostAPIBaseURL:      container.HostAPIBaseURL,
		HostBackendURL:      hostAPIBaseURL,
		HostDesktopURL:      hostDesktopURL,
		BackendHostPort:     container.HostPort,
		DesktopHostPort:     container.HostPort + 1,
		Image:               container.Image,
		SyncEnabled:         syncEnabled,
		SyncMode:            syncMode,
		SyncModules:         append([]string(nil), syncModules...),
		SyncOwnerSwarmID:    syncOwnerSwarmID,
		SyncCredentialURL:   syncCredentialURL,
		SyncAgentURL:        syncAgentURL,
		GroupID:             groupID,
		GroupName:           groupName,
		WorkspaceBootstrap:  append([]pebblestore.DeployContainerWorkspaceBootstrap(nil), input.WorkspaceBootstrap...),
		ContainerPackages:   mapContainerPackageManifest(input.ContainerPackages),
		AttachStatus:        "launching",
		BootstrapSecret:     bootstrapSecret,
		BootstrapExpiresAt:  now.Add(10 * time.Minute).UnixMilli(),
		BootstrapSecretSent: true,
		BypassPermissions:   input.BypassPermissions,
		LastAttachError:     strings.TrimSpace(container.Warning),
		CreatedAt:           container.CreatedAt,
		UpdatedAt:           container.UpdatedAt,
	}
	saved, saveErr := s.store.Put(record)
	if saveErr != nil {
		return ContainerDeployment{}, saveErr
	}
	if syncEnabled && syncVaultPassword != "" {
		s.rememberPendingSyncVaultPassword(saved.ID, syncVaultPassword, saved.BootstrapExpiresAt)
	}
	if createErr == nil && hostDrivenAttach {
		attached, attachErr := s.completeHostDrivenLocalAttach(ctx, startupCfg, hostState, saved, syncVaultPassword)
		if attachErr != nil {
			log.Printf("deploy host-driven local attach failed deployment_id=%q err=%v", attached.ID, attachErr)
		}
		return mapContainerRecord(attached), nil
	}
	return mapContainerRecord(saved), createErr
}

func (s *Service) requireManagedSyncVaultPassword(syncVaultPassword string) error {
	if s == nil || s.auth == nil {
		return nil
	}
	vaultStatus, err := s.auth.VaultStatus()
	if err != nil {
		return err
	}
	if vaultStatus.Enabled && !vaultStatus.Unlocked && strings.TrimSpace(syncVaultPassword) == "" {
		return fmt.Errorf("vault password is required to sync from a vaulted host")
	}
	return nil
}

func (s *Service) Act(ctx context.Context, input ContainerActionInput) (ContainerDeployment, error) {
	if s == nil || s.store == nil || s.containers == nil {
		return ContainerDeployment{}, fmt.Errorf("deploy container service is not configured")
	}
	container, err := s.containers.Act(ctx, localcontainers.ActionInput{ID: input.ID, Action: input.Action})
	if err != nil {
		return ContainerDeployment{}, err
	}
	record, ok, getErr := s.store.Get(input.ID)
	if getErr != nil {
		return ContainerDeployment{}, getErr
	}
	if !ok {
		return ContainerDeployment{}, fmt.Errorf("deploy container not found")
	}
	record.Status = normalizeDeploymentStatus(container.Status)
	record.Runtime = container.Runtime
	record.ContainerName = container.ContainerName
	record.ContainerID = container.ContainerID
	record.HostAPIBaseURL = container.HostAPIBaseURL
	record.BackendHostPort = container.HostPort
	record.DesktopHostPort = container.HostPort + 1
	record.Image = container.Image
	record.LastAttachError = strings.TrimSpace(container.Warning)
	if record.Status == "running" && record.AttachStatus == "" {
		record.AttachStatus = "launching"
	}
	saved, saveErr := s.store.Put(record)
	if saveErr != nil {
		return ContainerDeployment{}, saveErr
	}
	if strings.EqualFold(strings.TrimSpace(input.Action), "start") {
		if err := s.unlockManagedLocalChildVaultIfNeeded(ctx, saved); err != nil {
			return mapContainerRecord(saved), err
		}
	}
	return mapContainerRecord(saved), nil
}

func (s *Service) Delete(ctx context.Context, deploymentIDs []string) (localcontainers.DeleteResult, error) {
	if s == nil || s.store == nil {
		return localcontainers.DeleteResult{}, fmt.Errorf("deploy container service is not configured")
	}
	ids := normalizeDeploymentDeleteIDs(deploymentIDs)
	if len(ids) == 0 {
		return localcontainers.DeleteResult{}, errors.New("at least one deploy container id is required")
	}

	items := make([]localcontainers.DeleteItemResult, len(ids))
	var wg sync.WaitGroup
	for i, deploymentID := range ids {
		wg.Add(1)
		go func(index int, id string) {
			defer wg.Done()
			items[index] = s.deleteDeployment(ctx, id)
		}(i, deploymentID)
	}
	wg.Wait()

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
		return result, fmt.Errorf("failed to delete %d deploy container(s)", result.Failed)
	}
	return result, nil
}

func (s *Service) AttachRequest(ctx context.Context, input ContainerAttachRequestInput) (ContainerAttachState, error) {
	if s == nil || s.store == nil {
		return ContainerAttachState{}, fmt.Errorf("deploy container service is not configured")
	}
	log.Printf("deploy service attach request deployment_id=%q child_swarm_id=%q child_backend_url=%q", strings.TrimSpace(input.DeploymentID), strings.TrimSpace(input.ChildSwarmID), strings.TrimSpace(input.ChildBackendURL))
	record, ok, err := s.store.Get(input.DeploymentID)
	if err != nil {
		return ContainerAttachState{}, err
	}
	if !ok {
		return ContainerAttachState{}, fmt.Errorf("deploy container not found")
	}
	if record.AttachStatus == "attached" {
		return mapAttachState(record), fmt.Errorf("deploy container already attached")
	}
	if strings.TrimSpace(record.BootstrapSecret) == "" {
		return ContainerAttachState{}, fmt.Errorf("bootstrap secret is not configured")
	}
	if record.BootstrapSecretUsedAt > 0 {
		record.AttachStatus = "failed"
		record.LastAttachError = "bootstrap secret already used"
		saved, saveErr := s.store.Put(record)
		if saveErr != nil {
			return ContainerAttachState{}, saveErr
		}
		return mapAttachState(saved), fmt.Errorf("bootstrap secret already used")
	}
	if time.Now().UnixMilli() > record.BootstrapExpiresAt {
		record.AttachStatus = "failed"
		record.LastAttachError = "bootstrap secret expired"
		saved, saveErr := s.store.Put(record)
		if saveErr != nil {
			return ContainerAttachState{}, saveErr
		}
		return mapAttachState(saved), fmt.Errorf("bootstrap secret expired")
	}
	if subtleTrim(record.BootstrapSecret) != subtleTrim(input.BootstrapSecret) {
		record.AttachStatus = "failed"
		record.LastAttachError = "bootstrap secret mismatch"
		saved, saveErr := s.store.Put(record)
		if saveErr != nil {
			return ContainerAttachState{}, saveErr
		}
		return mapAttachState(saved), fmt.Errorf("bootstrap secret mismatch")
	}
	if err := validateChildIdentity(input); err != nil {
		record.AttachStatus = "failed"
		record.LastAttachError = err.Error()
		saved, saveErr := s.store.Put(record)
		if saveErr != nil {
			return ContainerAttachState{}, saveErr
		}
		return mapAttachState(saved), err
	}
	record.ChildSwarmID = strings.TrimSpace(input.ChildSwarmID)
	record.ChildDisplayName = strings.TrimSpace(input.ChildDisplayName)
	record.ChildBackendURL = strings.TrimSpace(input.ChildBackendURL)
	record.ChildDesktopURL = strings.TrimSpace(input.ChildDesktopURL)
	record.ChildPublicKey = strings.TrimSpace(input.ChildPublicKey)
	record.ChildFingerprint = strings.TrimSpace(input.ChildFingerprint)
	record.BootstrapSecretUsedAt = time.Now().UnixMilli()
	record.VerificationCode = ""
	record.AttachStatus = "attach_requested"
	record.Status = "running"
	record.LastAttachError = ""
	saved, saveErr := s.store.Put(record)
	if saveErr != nil {
		return ContainerAttachState{}, saveErr
	}
	log.Printf("deploy service attach request stored deployment_id=%q attach_status=%q child_swarm_id=%q", saved.ID, saved.AttachStatus, saved.ChildSwarmID)
	return mapAttachState(saved), nil
}

func (s *Service) deleteDeployment(ctx context.Context, deploymentID string) localcontainers.DeleteItemResult {
	record, ok, err := s.store.Get(deploymentID)
	if err != nil {
		return localcontainers.DeleteItemResult{ID: strings.TrimSpace(deploymentID), Error: err.Error()}
	}
	if !ok {
		return localcontainers.DeleteItemResult{ID: strings.TrimSpace(deploymentID), Error: "deploy container not found"}
	}

	item := localcontainers.DeleteItemResult{
		ID:               record.ID,
		Name:             record.Name,
		ContainerName:    record.ContainerName,
		ChildSwarmID:     record.ChildSwarmID,
		ChildDisplayName: firstNonEmpty(record.ChildDisplayName, record.ChildSwarmID),
	}
	if item.ChildSwarmID != "" || item.ChildDisplayName != "" {
		item.ChildInfoDetected = true
	}

	if record.Runtime != "" && record.ContainerName != "" {
		if err := localcontainers.RemoveRuntimeContainer(ctx, record.Runtime, record.ContainerName); err != nil && !localcontainers.IsMissingRuntimeContainerError(err) {
			item.Error = err.Error()
			return item
		}
	}
	if s.containers != nil {
		if _, err := s.containers.RemoveStoredRecordForDeployment(record); err != nil {
			item.Error = err.Error()
			return item
		}
	}
	if err := s.store.Delete(record.ID); err != nil {
		item.Error = err.Error()
		return item
	}
	s.clearPendingSyncVaultPassword(record.ID)
	if s.auth != nil {
		if err := s.auth.DeleteManagedVaultKey(record.ID); err != nil && !errors.Is(err, pebblestore.ErrVaultLocked) {
			item.Error = err.Error()
			return item
		}
	}
	item.Deleted = true
	item.RemovedDeployment = true

	childSwarmID := strings.TrimSpace(record.ChildSwarmID)
	if childSwarmID == "" {
		return item
	}
	if s.swarmStore != nil {
		memberships, err := s.swarmStore.ListGroupMembershipsBySwarm(childSwarmID, 500)
		if err != nil {
			item.Error = err.Error()
			return item
		}
		for _, membership := range memberships {
			if err := s.swarmStore.DeleteGroupMembership(membership.GroupID, membership.SwarmID); err != nil {
				item.Error = err.Error()
				return item
			}
			item.RemovedGroupMemberships++
		}
		if err := s.swarmStore.DeleteTrustedPeer(childSwarmID); err != nil {
			item.Error = err.Error()
			return item
		}
		item.RemovedTrustedPeer = true
	}
	if s.auth != nil {
		if _, err := s.auth.DeleteManagedCredentialsByOwnerSwarmID(childSwarmID); err != nil && !errors.Is(err, pebblestore.ErrVaultLocked) {
			item.Error = err.Error()
			return item
		}
	}
	if s.workspace != nil {
		if err := s.workspace.RemoveReplicationLinksByTargetSwarmID(childSwarmID); err != nil {
			item.Error = err.Error()
			return item
		}
	}
	return item
}

func (s *Service) AttachStatus(ctx context.Context, input ContainerAttachStatusInput) (ContainerAttachState, error) {
	if s == nil || s.store == nil {
		return ContainerAttachState{}, fmt.Errorf("deploy container service is not configured")
	}
	record, ok, err := s.store.Get(input.DeploymentID)
	if err != nil {
		return ContainerAttachState{}, err
	}
	if !ok {
		return ContainerAttachState{}, fmt.Errorf("deploy container not found")
	}
	if subtleTrim(record.BootstrapSecret) == "" {
		return ContainerAttachState{}, fmt.Errorf("bootstrap secret is not configured")
	}
	if subtleTrim(record.BootstrapSecret) != subtleTrim(input.BootstrapSecret) {
		return ContainerAttachState{}, fmt.Errorf("bootstrap secret mismatch")
	}
	if childSwarmID := strings.TrimSpace(input.ChildSwarmID); childSwarmID != "" && strings.TrimSpace(record.ChildSwarmID) != "" && !strings.EqualFold(childSwarmID, record.ChildSwarmID) {
		return ContainerAttachState{}, fmt.Errorf("child swarm mismatch")
	}
	return mapAttachState(record), nil
}

func (s *Service) ChildAttachState(ctx context.Context, input ContainerAttachStatusInput) (swarmruntime.LocalState, error) {
	if s == nil || s.swarms == nil {
		return swarmruntime.LocalState{}, fmt.Errorf("deploy container service is not configured")
	}
	_ = ctx
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return swarmruntime.LocalState{}, err
	}
	if !cfg.Child || !cfg.DeployContainer.Enabled {
		return swarmruntime.LocalState{}, fmt.Errorf("child deploy bootstrap is not configured")
	}
	if subtleTrim(cfg.DeployContainer.DeploymentID) != subtleTrim(input.DeploymentID) {
		return swarmruntime.LocalState{}, fmt.Errorf("deployment id mismatch")
	}
	if subtleTrim(cfg.DeployContainer.BootstrapSecret) == "" {
		return swarmruntime.LocalState{}, fmt.Errorf("bootstrap secret is not configured")
	}
	if subtleTrim(cfg.DeployContainer.BootstrapSecret) != subtleTrim(input.BootstrapSecret) {
		return swarmruntime.LocalState{}, fmt.Errorf("bootstrap secret mismatch")
	}
	return s.prepareChildAttachState(cfg)
}

func (s *Service) AttachApprove(ctx context.Context, input ContainerAttachApproveInput) (ContainerAttachState, error) {
	if s == nil || s.store == nil {
		return ContainerAttachState{}, fmt.Errorf("deploy container service is not configured")
	}
	_ = ctx
	log.Printf("deploy service attach approve deployment_id=%q host_swarm_id=%q group_id=%q group_network_name=%q", strings.TrimSpace(input.DeploymentID), strings.TrimSpace(input.HostSwarmID), strings.TrimSpace(input.GroupID), strings.TrimSpace(input.GroupNetworkName))
	record, ok, err := s.store.Get(input.DeploymentID)
	if err != nil {
		return ContainerAttachState{}, err
	}
	if !ok {
		return ContainerAttachState{}, fmt.Errorf("deploy container not found")
	}
	if strings.TrimSpace(record.ChildSwarmID) == "" {
		return ContainerAttachState{}, fmt.Errorf("child attach request has not been received")
	}
	if record.AttachStatus == "attached" {
		return mapAttachState(record), nil
	}
	if subtleTrim(record.BootstrapSecret) == "" {
		return ContainerAttachState{}, fmt.Errorf("bootstrap secret is not configured")
	}
	if subtleTrim(record.BootstrapSecret) != subtleTrim(input.BootstrapSecret) {
		return ContainerAttachState{}, fmt.Errorf("bootstrap secret mismatch")
	}
	record.HostSwarmID = strings.TrimSpace(input.HostSwarmID)
	record.HostDisplayName = strings.TrimSpace(input.HostDisplayName)
	record.HostPublicKey = strings.TrimSpace(input.HostPublicKey)
	record.HostFingerprint = strings.TrimSpace(input.HostFingerprint)
	record.HostAPIBaseURL = strings.TrimSpace(input.HostBackendURL)
	record.HostBackendURL = strings.TrimSpace(input.HostBackendURL)
	record.HostDesktopURL = strings.TrimSpace(input.HostDesktopURL)
	record.GroupID = strings.TrimSpace(input.GroupID)
	record.GroupName = strings.TrimSpace(input.GroupName)
	record.GroupNetworkName = strings.TrimSpace(input.GroupNetworkName)
	syncVaultPassword := strings.TrimSpace(input.SyncVaultPassword)
	if syncVaultPassword == "" {
		syncVaultPassword = s.resolvePendingSyncVaultPassword(record.ID)
	}
	hostToChildPeerAuthToken, childToHostPeerAuthToken, err := generateAttachPeerAuthTokens()
	if err != nil {
		return ContainerAttachState{}, err
	}
	if err := s.finalizeApprovedAttach(&record, hostToChildPeerAuthToken, childToHostPeerAuthToken, syncVaultPassword); err != nil {
		record.AttachStatus = "failed"
		record.LastAttachError = err.Error()
		saved, saveErr := s.store.Put(record)
		if saveErr != nil {
			return ContainerAttachState{}, saveErr
		}
		return mapAttachState(saved), err
	}
	record.AttachStatus = "attached"
	record.Status = "attached"
	record.DecidedAt = time.Now().UnixMilli()
	record.LastAttachError = ""
	saved, saveErr := s.store.Put(record)
	if saveErr != nil {
		return ContainerAttachState{}, saveErr
	}
	log.Printf("deploy service attach approve stored deployment_id=%q attach_status=%q child_swarm_id=%q host_swarm_id=%q", saved.ID, saved.AttachStatus, saved.ChildSwarmID, saved.HostSwarmID)
	state := mapAttachState(saved)
	state.HostToChildPeerAuthToken = hostToChildPeerAuthToken
	state.ChildToHostPeerAuthToken = childToHostPeerAuthToken
	state.SyncVaultPassword = syncVaultPassword
	if managedKey, ok, err := s.managedLocalChildVaultKey(record.ID); err == nil && ok {
		state.SyncManagedVaultKey = managedKey
	} else if err != nil {
		return state, err
	}
	log.Printf("deploy service attach approve managed vault key deployment_id=%q present=%t", saved.ID, strings.TrimSpace(state.SyncManagedVaultKey) != "")
	return state, nil
}

func (s *Service) SyncCredentialBundle(ctx context.Context, input ContainerSyncCredentialRequestInput) (ContainerSyncCredentialBundle, error) {
	if s == nil || s.store == nil || s.auth == nil {
		return ContainerSyncCredentialBundle{}, fmt.Errorf("deploy container service is not configured")
	}
	_ = ctx
	record, ok, err := s.store.Get(input.DeploymentID)
	if err != nil {
		return ContainerSyncCredentialBundle{}, err
	}
	if !ok {
		return ContainerSyncCredentialBundle{}, fmt.Errorf("deploy container not found")
	}
	if subtleTrim(record.BootstrapSecret) == "" {
		return ContainerSyncCredentialBundle{}, fmt.Errorf("bootstrap secret is not configured")
	}
	if subtleTrim(record.BootstrapSecret) != subtleTrim(input.BootstrapSecret) {
		return ContainerSyncCredentialBundle{}, fmt.Errorf("bootstrap secret mismatch")
	}
	if !record.SyncEnabled {
		return ContainerSyncCredentialBundle{}, fmt.Errorf("swarm sync is not enabled for this deployment")
	}
	record.SyncModules = workspaceruntime.NormalizeReplicationSyncModules(record.SyncModules)
	if len(record.SyncModules) == 0 {
		record.SyncModules = workspaceruntime.DefaultReplicationSyncModules()
	}
	if !workspaceruntime.ReplicationSyncModuleEnabled(record.SyncModules, workspaceruntime.ReplicationSyncModuleCredentials) {
		return ContainerSyncCredentialBundle{}, fmt.Errorf("credential sync module is not enabled for this deployment")
	}
	if strings.TrimSpace(record.SyncOwnerSwarmID) == "" {
		return ContainerSyncCredentialBundle{}, fmt.Errorf("sync owner swarm id is not configured")
	}
	if strings.TrimSpace(record.SyncBundlePassword) == "" {
		return ContainerSyncCredentialBundle{}, fmt.Errorf("sync bundle password is not configured")
	}
	payload, exported, err := s.auth.ExportCredentials(record.SyncBundlePassword, strings.TrimSpace(input.VaultPassword))
	if err != nil {
		return ContainerSyncCredentialBundle{}, err
	}
	record.SyncBundleExportCount = exported
	record.SyncBundleExportedAt = time.Now().UnixMilli()
	if _, err := s.store.Put(record); err != nil {
		return ContainerSyncCredentialBundle{}, err
	}
	return ContainerSyncCredentialBundle{
		OwnerSwarmID:   record.SyncOwnerSwarmID,
		BundlePassword: record.SyncBundlePassword,
		Bundle:         payload,
		Exported:       exported,
		ExportedAt:     record.SyncBundleExportedAt,
	}, nil
}

func (s *Service) SyncAgentBundle(ctx context.Context, input ContainerSyncCredentialRequestInput) (ContainerSyncAgentBundle, error) {
	if s == nil || s.store == nil || s.agents == nil {
		return ContainerSyncAgentBundle{}, fmt.Errorf("deploy container service is not configured")
	}
	_ = ctx
	record, ok, err := s.store.Get(input.DeploymentID)
	if err != nil {
		return ContainerSyncAgentBundle{}, err
	}
	if !ok {
		return ContainerSyncAgentBundle{}, fmt.Errorf("deploy container not found")
	}
	if subtleTrim(record.BootstrapSecret) == "" {
		return ContainerSyncAgentBundle{}, fmt.Errorf("bootstrap secret is not configured")
	}
	if subtleTrim(record.BootstrapSecret) != subtleTrim(input.BootstrapSecret) {
		return ContainerSyncAgentBundle{}, fmt.Errorf("bootstrap secret mismatch")
	}
	if !record.SyncEnabled {
		return ContainerSyncAgentBundle{}, fmt.Errorf("swarm sync is not enabled for this deployment")
	}
	record.SyncModules = workspaceruntime.NormalizeReplicationSyncModules(record.SyncModules)
	if len(record.SyncModules) == 0 {
		record.SyncModules = workspaceruntime.DefaultReplicationSyncModules()
	}
	syncProfiles := workspaceruntime.ReplicationSyncModuleEnabled(record.SyncModules, workspaceruntime.ReplicationSyncModuleAgents)
	syncCustomTools := workspaceruntime.ReplicationSyncModuleEnabled(record.SyncModules, workspaceruntime.ReplicationSyncModuleCustomTools)
	if !syncProfiles && !syncCustomTools {
		return ContainerSyncAgentBundle{}, fmt.Errorf("agent sync modules are not enabled for this deployment")
	}
	state, err := s.agents.ListState(2000)
	if err != nil {
		return ContainerSyncAgentBundle{}, err
	}
	filtered := state
	if !syncProfiles {
		filtered.Profiles = nil
		filtered.ActivePrimary = ""
		filtered.ActiveSubagent = nil
	}
	if !syncCustomTools {
		filtered.CustomTools = nil
	}
	payload, err := json.Marshal(filtered)
	if err != nil {
		return ContainerSyncAgentBundle{}, err
	}
	s.agentSnapshotMu.Lock()
	defer s.agentSnapshotMu.Unlock()
	sum := sha256.Sum256(payload)
	s.agentSnapshotHash = hex.EncodeToString(sum[:])
	return ContainerSyncAgentBundle{
		State:        filtered,
		SnapshotHash: s.agentSnapshotHash,
		ExportedAt:   time.Now().UnixMilli(),
	}, nil
}

func (s *Service) WorkspaceBootstrap(ctx context.Context, input ContainerWorkspaceBootstrapRequestInput) ([]ContainerWorkspaceBootstrap, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("deploy container service is not configured")
	}
	_ = ctx
	record, ok, err := s.store.Get(input.DeploymentID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("deploy container not found")
	}
	if subtleTrim(record.BootstrapSecret) == "" {
		return nil, fmt.Errorf("bootstrap secret is not configured")
	}
	if subtleTrim(record.BootstrapSecret) != subtleTrim(input.BootstrapSecret) {
		return nil, fmt.Errorf("bootstrap secret mismatch")
	}
	return append([]ContainerWorkspaceBootstrap(nil), record.WorkspaceBootstrap...), nil
}

func (s *Service) FinalizeAttachFromHost(ctx context.Context, input ContainerAttachFinalizeInput) error {
	if s == nil || s.swarms == nil || s.swarmStore == nil {
		return fmt.Errorf("deploy container service is not configured")
	}
	_ = ctx
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return err
	}
	if !cfg.Child || !cfg.DeployContainer.Enabled {
		return fmt.Errorf("child deploy bootstrap is not configured")
	}
	if !cfg.DeployContainer.HostDriven {
		return fmt.Errorf("child deploy bootstrap is not waiting for host-driven finalization")
	}
	if subtleTrim(cfg.DeployContainer.DeploymentID) != subtleTrim(input.DeploymentID) {
		return fmt.Errorf("deployment id mismatch")
	}
	if subtleTrim(cfg.DeployContainer.BootstrapSecret) == "" {
		return fmt.Errorf("bootstrap secret is not configured")
	}
	if subtleTrim(cfg.DeployContainer.BootstrapSecret) != subtleTrim(input.BootstrapSecret) {
		return fmt.Errorf("bootstrap secret mismatch")
	}
	state, err := s.prepareChildAttachState(cfg)
	if err != nil {
		return err
	}
	return s.finalizeChildAttach(cfg, state, ContainerAttachState{
		DeploymentID:     strings.TrimSpace(input.DeploymentID),
		AttachStatus:     "attached",
		HostSwarmID:      strings.TrimSpace(input.HostSwarmID),
		HostDisplayName:  strings.TrimSpace(input.HostDisplayName),
		HostPublicKey:    strings.TrimSpace(input.HostPublicKey),
		HostFingerprint:  strings.TrimSpace(input.HostFingerprint),
		HostBackendURL:   strings.TrimSpace(input.HostBackendURL),
		HostDesktopURL:   strings.TrimSpace(input.HostDesktopURL),
		GroupID:          strings.TrimSpace(input.GroupID),
		GroupName:        strings.TrimSpace(input.GroupName),
		GroupNetworkName: strings.TrimSpace(input.GroupNetworkName),
	}, input)
}

func (s *Service) AutoAttachChild(ctx context.Context) error {
	if s == nil || s.swarms == nil {
		return fmt.Errorf("deploy container service is not configured")
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return err
	}
	log.Printf("deploy auto attach child startup child=%t deploy_enabled=%t deployment_id=%q swarm_name=%q advertise_host=%q advertise_port=%d", cfg.Child, cfg.DeployContainer.Enabled, strings.TrimSpace(cfg.DeployContainer.DeploymentID), strings.TrimSpace(cfg.SwarmName), strings.TrimSpace(cfg.AdvertiseHost), cfg.AdvertisePort)
	if !cfg.Child || !cfg.DeployContainer.Enabled {
		return nil
	}
	state, err := s.prepareChildAttachState(cfg)
	if err != nil {
		return err
	}
	if cfg.DeployContainer.HostDriven {
		log.Printf("deploy auto attach child waiting for host-driven finalization deployment_id=%q swarm_name=%q", strings.TrimSpace(cfg.DeployContainer.DeploymentID), strings.TrimSpace(cfg.SwarmName))
		return nil
	}

	attachState, err := s.requestLocalAttach(ctx, cfg, state)
	if err != nil {
		return err
	}
	log.Printf("deploy auto attach child received attach state deployment_id=%q attach_status=%q host_swarm_id=%q group_id=%q", attachState.DeploymentID, attachState.AttachStatus, attachState.HostSwarmID, attachState.GroupID)
	if strings.TrimSpace(cfg.RemoteDeploy.SessionID) != "" && strings.TrimSpace(cfg.RemoteDeploy.InviteToken) != "" {
		return nil
	}
	return s.finalizeChildAttach(cfg, state, attachState, ContainerAttachFinalizeInput{
		HostToChildPeerAuthToken: strings.TrimSpace(attachState.HostToChildPeerAuthToken),
		ChildToHostPeerAuthToken: strings.TrimSpace(attachState.ChildToHostPeerAuthToken),
		SyncVaultPassword:        strings.TrimSpace(attachState.SyncVaultPassword),
		SyncManagedVaultKey:      strings.TrimSpace(attachState.SyncManagedVaultKey),
	})
}

func (s *Service) RecoverLocalDeployments(ctx context.Context) error {
	if s == nil || s.store == nil || s.containers == nil {
		return fmt.Errorf("deploy container service is not configured")
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return err
	}
	if cfg.Child {
		return nil
	}
	records, err := s.store.List(500)
	if err != nil {
		return err
	}
	for _, record := range records {
		if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.Runtime) == "" || strings.TrimSpace(record.ContainerName) == "" {
			continue
		}
		if strings.TrimSpace(record.AttachStatus) != "attached" {
			continue
		}
		if strings.TrimSpace(record.Status) == "running" {
			continue
		}
		log.Printf("deploy startup recovery starting local child deployment_id=%q status=%q attach_status=%q runtime=%q container=%q", record.ID, record.Status, record.AttachStatus, record.Runtime, record.ContainerName)
		if _, err := s.Act(ctx, ContainerActionInput{ID: record.ID, Action: "start"}); err != nil {
			log.Printf("deploy startup recovery failed deployment_id=%q container=%q err=%v", record.ID, record.ContainerName, err)
			continue
		}
		log.Printf("deploy startup recovery started local child deployment_id=%q container=%q", record.ID, record.ContainerName)
	}
	return nil
}

func (s *Service) finalizeApprovedAttach(record *pebblestore.DeployContainerRecord, hostToChildPeerAuthToken, childToHostPeerAuthToken, syncVaultPassword string) error {
	if s == nil || s.swarms == nil || s.swarmStore == nil {
		return fmt.Errorf("deploy container service is not configured")
	}
	if record == nil {
		return fmt.Errorf("deploy container record is required")
	}
	if strings.TrimSpace(record.ChildSwarmID) == "" {
		return fmt.Errorf("child swarm id is required for approval")
	}
	if strings.TrimSpace(record.ChildPublicKey) == "" || strings.TrimSpace(record.ChildFingerprint) == "" {
		return fmt.Errorf("child identity proof is required for approval")
	}
	startupCfg, hostState, err := s.resolveBootstrapContext()
	if err != nil {
		return err
	}
	groupID := strings.TrimSpace(record.GroupID)
	if groupID == "" {
		return fmt.Errorf("attach approval is missing target group id")
	}
	group, ok, err := s.swarmStore.GetGroup(groupID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("target group %q does not exist", groupID)
	}
	if err := requireHostedGroupForLocalSwarm(hostState, groupID); err != nil {
		return err
	}
	groupName := firstNonEmpty(group.Name, record.GroupName, groupID)
	groupNetworkName := firstNonEmpty(group.NetworkName, record.GroupNetworkName, swarmruntime.SuggestedGroupNetworkName(groupName, groupID))
	if group.Name != groupName || group.NetworkName != groupNetworkName || group.HostSwarmID == "" {
		group.Name = groupName
		group.NetworkName = groupNetworkName
		group.HostSwarmID = firstNonEmpty(group.HostSwarmID, strings.TrimSpace(hostState.Node.SwarmID))
		if _, err := s.swarmStore.PutGroup(group); err != nil {
			return err
		}
	}
	if currentGroupID := strings.TrimSpace(hostState.CurrentGroupID); currentGroupID != "" && !strings.EqualFold(currentGroupID, groupID) {
		log.Printf("deploy finalize approved attach honoring requested group deployment_id=%q requested_group_id=%q host_current_group_id=%q", record.ID, groupID, currentGroupID)
	}
	if _, err := s.swarms.UpsertGroupMember(swarmruntime.UpsertGroupMemberInput{
		GroupID:        groupID,
		SwarmID:        hostState.Node.SwarmID,
		Name:           firstNonEmpty(hostState.Node.Name, "Primary"),
		SwarmRole:      "master",
		MembershipRole: "host",
	}); err != nil {
		return err
	}
	if _, err := s.swarms.UpsertGroupMember(swarmruntime.UpsertGroupMemberInput{
		GroupID:        groupID,
		SwarmID:        record.ChildSwarmID,
		Name:           firstNonEmpty(record.ChildDisplayName, record.Name),
		SwarmRole:      "child",
		MembershipRole: "member",
	}); err != nil {
		return err
	}
	if _, err := s.swarmStore.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{
		SwarmID:               record.ChildSwarmID,
		Name:                  firstNonEmpty(record.ChildDisplayName, record.Name),
		Role:                  "child",
		PublicKey:             record.ChildPublicKey,
		Fingerprint:           record.ChildFingerprint,
		Relationship:          swarmruntime.RelationshipChild,
		ParentSwarmID:         hostState.Node.SwarmID,
		TransportMode:         firstNonEmpty(startupCfg.NetworkMode, startupconfig.NetworkModeLAN),
		OutgoingPeerAuthToken: strings.TrimSpace(hostToChildPeerAuthToken),
		IncomingPeerAuthHash:  swarmruntime.HashPeerAuthToken(childToHostPeerAuthToken),
		ApprovedAt:            time.Now().UnixMilli(),
	}); err != nil {
		return err
	}
	record.HostSwarmID = strings.TrimSpace(hostState.Node.SwarmID)
	record.HostDisplayName = firstNonEmpty(hostState.Node.Name, strings.TrimSpace(startupCfg.SwarmName), "Primary")
	record.HostPublicKey = strings.TrimSpace(hostState.Node.PublicKey)
	record.HostFingerprint = strings.TrimSpace(hostState.Node.Fingerprint)
	if strings.TrimSpace(record.HostBackendURL) == "" || strings.TrimSpace(record.HostAPIBaseURL) == "" {
		runtimeName := firstNonEmpty(record.Runtime)
		_, hostBackendURL, hostDesktopURL, err := resolveContainerReachableHostEndpoints(startupCfg, hostState, runtimeName)
		if err != nil {
			return err
		}
		record.HostAPIBaseURL = hostBackendURL
		record.HostBackendURL = hostBackendURL
		if strings.TrimSpace(record.HostDesktopURL) == "" {
			record.HostDesktopURL = hostDesktopURL
		}
		childBackendURL, childDesktopURL, urlErr := childContainerReachableURLs(startupCfg)
		if urlErr != nil {
			return urlErr
		}
		record.ChildBackendURL = childBackendURL
		record.ChildDesktopURL = childDesktopURL
	}
	record.GroupID = groupID
	record.GroupName = groupName
	record.GroupNetworkName = groupNetworkName
	if record.SyncEnabled {
		record.SyncMode = firstNonEmpty(record.SyncMode, workspaceruntime.ReplicationSyncModeManaged)
		record.SyncModules = workspaceruntime.NormalizeReplicationSyncModules(record.SyncModules)
		if len(record.SyncModules) == 0 {
			record.SyncModules = workspaceruntime.DefaultReplicationSyncModules()
		}
		record.SyncOwnerSwarmID = firstNonEmpty(record.SyncOwnerSwarmID, strings.TrimSpace(hostState.Node.SwarmID))
		if workspaceruntime.ReplicationSyncModuleEnabled(record.SyncModules, workspaceruntime.ReplicationSyncModuleCredentials) {
			record.SyncCredentialURL = firstNonEmpty(record.SyncCredentialURL, buildDeploymentSyncCredentialURL(record.HostBackendURL))
		} else {
			record.SyncCredentialURL = ""
		}
		if workspaceruntime.ReplicationSyncModuleEnabled(record.SyncModules, workspaceruntime.ReplicationSyncModuleAgents) || workspaceruntime.ReplicationSyncModuleEnabled(record.SyncModules, workspaceruntime.ReplicationSyncModuleCustomTools) {
			record.SyncAgentURL = firstNonEmpty(record.SyncAgentURL, buildDeploymentSyncAgentURL(record.HostBackendURL))
		} else {
			record.SyncAgentURL = ""
		}
		if workspaceruntime.ReplicationSyncModuleEnabled(record.SyncModules, workspaceruntime.ReplicationSyncModuleCredentials) {
			if err := s.requireManagedSyncVaultPassword(syncVaultPassword); err != nil {
				return err
			}
			if strings.TrimSpace(record.SyncBundlePassword) == "" {
				bundlePassword, err := generateSecretToken(32)
				if err != nil {
					return err
				}
				record.SyncBundlePassword = bundlePassword
			}
			if s.auth == nil {
				return fmt.Errorf("auth service is not configured")
			}
			_, exported, err := s.auth.ExportCredentials(record.SyncBundlePassword, strings.TrimSpace(syncVaultPassword))
			if err != nil {
				return err
			}
			if vaultStatus, err := s.auth.VaultStatus(); err != nil {
				return err
			} else if vaultStatus.Enabled {
				if _, err := s.ensureManagedLocalChildVaultKey(record.ID); err != nil {
					return err
				}
			}
			record.SyncBundleExportCount = exported
			record.SyncBundleExportedAt = time.Now().UnixMilli()
		} else {
			record.SyncBundlePassword = ""
			record.SyncBundleExportCount = 0
			record.SyncBundleExportedAt = 0
		}
	}
	return nil
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

type containerBootstrapEnvInput struct {
	HostState                swarmruntime.LocalState
	ChildName                string
	DeploymentID             string
	BootstrapSecret          string
	HostAPIBaseURL           string
	HostDesktopURL           string
	LocalTransportSocketPath string
	ChildAdvertiseHost       string
	ChildAdvertisePort       int
	HostDriven               bool
	SyncEnabled              bool
	SyncMode                 string
	SyncModules              []string
	SyncOwnerSwarmID         string
	SyncCredentialURL        string
	SyncAgentURL             string
	BypassPermissions        bool
}

func buildChildContainerEnv(input containerBootstrapEnvInput) []string {
	childConfigPath := filepath.Join(os.TempDir(), "swarm-child.conf")
	cfg := startupconfig.Default(childConfigPath)
	cfg.Mode = startupconfig.ModeBox
	cfg.Host = startupconfig.DefaultHost
	cfg.Port = startupconfig.DefaultPort
	cfg.AdvertiseHost = strings.TrimSpace(input.ChildAdvertiseHost)
	cfg.AdvertisePort = input.ChildAdvertisePort
	cfg.DesktopPort = startupconfig.DefaultDesktopPort
	cfg.BypassPermissions = input.BypassPermissions
	cfg.SwarmMode = true
	cfg.Child = true
	cfg.NetworkMode = startupconfig.NetworkModeLAN
	cfg.SwarmName = strings.TrimSpace(input.ChildName)
	cfg.ParentSwarmID = strings.TrimSpace(input.HostState.Node.SwarmID)
	cfg.PairingState = startupconfig.PairingStateBootstrapReady
	cfg.DeployContainer = startupconfig.DeployContainerBootstrap{
		Enabled:                  true,
		HostDriven:               input.HostDriven,
		SyncEnabled:              input.SyncEnabled,
		SyncMode:                 strings.TrimSpace(input.SyncMode),
		SyncModules:              append([]string(nil), input.SyncModules...),
		SyncOwnerSwarmID:         strings.TrimSpace(input.SyncOwnerSwarmID),
		SyncCredentialURL:        strings.TrimSpace(input.SyncCredentialURL),
		SyncAgentURL:             strings.TrimSpace(input.SyncAgentURL),
		DeploymentID:             strings.TrimSpace(input.DeploymentID),
		HostAPIBaseURL:           strings.TrimSpace(input.HostAPIBaseURL),
		HostDesktopURL:           strings.TrimSpace(input.HostDesktopURL),
		LocalTransportSocketPath: strings.TrimSpace(input.LocalTransportSocketPath),
		BootstrapSecret:          strings.TrimSpace(input.BootstrapSecret),
	}
	env := []string{
		"SWARM_STARTUP_MODE=box",
		"SWARM_CONTAINER_OFFLINE=true",
		// Keep the child listener on all container interfaces so the host-published
		// loopback ports stay reachable from the parent, even when child->parent
		// bootstrap uses the mounted local transport socket.
		fmt.Sprintf("SWARMD_LISTEN=0.0.0.0:%d", startupconfig.DefaultPort),
		fmt.Sprintf("SWARM_DESKTOP_PORT=%d", startupconfig.DefaultDesktopPort),
		fmt.Sprintf("SWARM_CHILD_STARTUP_CONFIG=%s", encodeEnvMultiline(startupconfig.Format(cfg))),
	}
	return appendInheritedChildDebugEnv(env)
}

func appendInheritedChildDebugEnv(env []string) []string {
	return localcontainers.AppendInheritedChildDebugEnv(env)
}

func mapContainerPackageManifest(input ContainerPackageManifest) pebblestore.ContainerPackageManifestRecord {
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

func mapStoredContainerPackageManifest(input pebblestore.ContainerPackageManifestRecord) ContainerPackageManifest {
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

func mapContainerRecord(record pebblestore.DeployContainerRecord) ContainerDeployment {
	return ContainerDeployment{
		ID:                  record.ID,
		Kind:                record.Kind,
		Name:                record.Name,
		Status:              record.Status,
		Runtime:             record.Runtime,
		GroupID:             record.GroupID,
		GroupName:           record.GroupName,
		ContainerName:       record.ContainerName,
		ContainerID:         record.ContainerID,
		HostAPIBaseURL:      record.HostAPIBaseURL,
		GroupNetworkName:    record.GroupNetworkName,
		SyncEnabled:         record.SyncEnabled,
		SyncMode:            record.SyncMode,
		SyncModules:         append([]string(nil), record.SyncModules...),
		SyncOwnerSwarmID:    record.SyncOwnerSwarmID,
		BackendHostPort:     record.BackendHostPort,
		DesktopHostPort:     record.DesktopHostPort,
		Image:               record.Image,
		AttachStatus:        record.AttachStatus,
		LastAttachError:     record.LastAttachError,
		BootstrapSecretSent: record.BootstrapSecretSent,
		BypassPermissions:   record.BypassPermissions,
		ChildSwarmID:        record.ChildSwarmID,
		ChildDisplayName:    record.ChildDisplayName,
		ChildBackendURL:     record.ChildBackendURL,
		ChildDesktopURL:     record.ChildDesktopURL,
		WorkspaceBootstrap:  append([]ContainerWorkspaceBootstrap(nil), record.WorkspaceBootstrap...),
		ContainerPackages:   mapStoredContainerPackageManifest(record.ContainerPackages),
		CreatedAt:           record.CreatedAt,
		UpdatedAt:           record.UpdatedAt,
	}
}

func normalizeDeploymentDeleteIDs(deploymentIDs []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(deploymentIDs))
	for _, value := range deploymentIDs {
		normalized := strings.TrimSpace(value)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func mapAttachState(record pebblestore.DeployContainerRecord) ContainerAttachState {
	return ContainerAttachState{
		DeploymentID:           record.ID,
		AttachStatus:           record.AttachStatus,
		ChildSwarmID:           record.ChildSwarmID,
		ChildDisplayName:       record.ChildDisplayName,
		ChildBackendURL:        record.ChildBackendURL,
		ChildDesktopURL:        record.ChildDesktopURL,
		ChildFingerprint:       record.ChildFingerprint,
		HostSwarmID:            record.HostSwarmID,
		HostDisplayName:        record.HostDisplayName,
		HostPublicKey:          record.HostPublicKey,
		HostFingerprint:        record.HostFingerprint,
		HostBackendURL:         record.HostBackendURL,
		HostDesktopURL:         record.HostDesktopURL,
		GroupID:                record.GroupID,
		GroupName:              record.GroupName,
		GroupNetworkName:       record.GroupNetworkName,
		BootstrapSecretExpires: record.BootstrapExpiresAt,
		LastError:              record.LastAttachError,
		DecidedAt:              record.DecidedAt,
		UpdatedAt:              record.UpdatedAt,
	}
}

func normalizeDeploymentStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "running":
		return "running"
	case "attached":
		return "attached"
	case "exited", "stopped":
		return "stopped"
	case "missing", "failed":
		return "failed"
	default:
		return "creating"
	}
}

func createResultCanBePersisted(container localcontainers.Container) bool {
	return strings.TrimSpace(container.ID) != "" ||
		strings.TrimSpace(container.Name) != "" ||
		strings.TrimSpace(container.ContainerName) != ""
}

func createResultDisplayName(input ContainerCreateInput, container localcontainers.Container) string {
	return firstNonEmpty(container.Name, strings.TrimSpace(input.Name))
}

func generateSecretToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate secret token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func generateAttachPeerAuthTokens() (string, string, error) {
	hostToChildPeerAuthToken, err := swarmruntime.GeneratePeerAuthToken()
	if err != nil {
		return "", "", err
	}
	childToHostPeerAuthToken, err := swarmruntime.GeneratePeerAuthToken()
	if err != nil {
		return "", "", err
	}
	return hostToChildPeerAuthToken, childToHostPeerAuthToken, nil
}

func suggestedDeploymentID(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range name {
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

func validateChildIdentity(input ContainerAttachRequestInput) error {
	if strings.TrimSpace(input.ChildSwarmID) == "" {
		return fmt.Errorf("child swarm id is required")
	}
	if strings.TrimSpace(input.ChildPublicKey) == "" {
		return fmt.Errorf("child public key is required")
	}
	fingerprint := strings.TrimSpace(input.ChildFingerprint)
	if fingerprint == "" {
		return fmt.Errorf("child fingerprint is required")
	}
	if swarmruntime.FingerprintPublicKey(strings.TrimSpace(input.ChildPublicKey)) != fingerprint {
		return fmt.Errorf("child fingerprint mismatch")
	}
	return nil
}

type localChildSwarmStateResponse struct {
	OK    bool                    `json:"ok"`
	State swarmruntime.LocalState `json:"state"`
}

func (s *Service) prepareChildAttachState(cfg startupconfig.FileConfig) (swarmruntime.LocalState, error) {
	state, err := s.swarms.EnsureLocalState(swarmruntime.EnsureLocalStateInput{
		Name:          strings.TrimSpace(cfg.SwarmName),
		Role:          "child",
		SwarmMode:     true,
		AdvertiseMode: cfg.NetworkMode,
		AdvertiseAddr: strings.TrimSpace(cfg.AdvertiseHost),
	})
	if err != nil {
		return swarmruntime.LocalState{}, err
	}
	if s.swarmStore != nil {
		pairing, ok, err := s.swarmStore.GetLocalPairing()
		if err != nil {
			return swarmruntime.LocalState{}, err
		}
		if !ok {
			pairing = pebblestore.SwarmLocalPairingRecord{}
		}
		pairing.PairingState = startupconfig.PairingStatePendingApproval
		pairing.ParentSwarmID = strings.TrimSpace(cfg.ParentSwarmID)
		pairing.LastUpdatedByRole = "child"
		if _, err := s.swarmStore.PutLocalPairing(pairing); err != nil {
			return swarmruntime.LocalState{}, err
		}
	}
	return state, nil
}

func shouldUseHostDrivenLocalAttach(cfg startupconfig.FileConfig, state swarmruntime.LocalState) bool {
	_ = cfg
	_ = state
	return true
}

func resolveLocalContainerBootstrapTargets(cfg startupconfig.FileConfig, state swarmruntime.LocalState, runtimeName, localTransportSocketPath string) (string, string, string, bool, error) {
	_ = runtimeName
	_ = localTransportSocketPath
	if shouldUseHostDrivenLocalAttach(cfg, state) {
		apiBaseURL := runtimeHTTPURL(startupconfig.DefaultHost, cfg.Port)
		desktopURL := ""
		if cfg.DesktopPort >= 1 && cfg.DesktopPort <= 65535 {
			desktopURL = runtimeHTTPURL(startupconfig.DefaultHost, cfg.DesktopPort)
		}
		return localcontainers.HostnameFromBaseURL(apiBaseURL), apiBaseURL, desktopURL, true, nil
	}
	hostRuntimeHost, hostAPIBaseURL, hostDesktopURL, err := resolveContainerReachableHostEndpoints(cfg, state, runtimeName)
	if err != nil {
		return "", "", "", false, err
	}
	return hostRuntimeHost, hostAPIBaseURL, hostDesktopURL, false, nil
}

func (s *Service) resolveHostDrivenParentCallbackURLs(runtimeName string, cfg startupconfig.FileConfig) (string, string, error) {
	_ = s
	_ = runtimeName
	apiBaseURL := runtimeHTTPURL(startupconfig.DefaultHost, cfg.Port)
	desktopURL := ""
	if cfg.DesktopPort >= 1 && cfg.DesktopPort <= 65535 {
		desktopURL = runtimeHTTPURL(startupconfig.DefaultHost, cfg.DesktopPort)
	}
	return apiBaseURL, desktopURL, nil
}

func (s *Service) completeHostDrivenLocalAttach(ctx context.Context, startupCfg startupconfig.FileConfig, hostState swarmruntime.LocalState, record pebblestore.DeployContainerRecord, syncVaultPassword string) (pebblestore.DeployContainerRecord, error) {
	childBackendURL := runtimeHTTPURL(startupconfig.DefaultHost, record.BackendHostPort)
	childDesktopURL := ""
	if record.DesktopHostPort > 0 {
		childDesktopURL = runtimeHTTPURL(startupconfig.DefaultHost, record.DesktopHostPort)
	}
	childState, err := s.waitForHostReachableChildState(ctx, childBackendURL, record.ID, record.BootstrapSecret)
	if err != nil {
		return s.failDeploymentAttach(record.ID, err)
	}
	if _, err := s.AttachRequest(ctx, ContainerAttachRequestInput{
		DeploymentID:      record.ID,
		BootstrapSecret:   record.BootstrapSecret,
		ChildSwarmID:      strings.TrimSpace(childState.Node.SwarmID),
		ChildDisplayName:  firstNonEmpty(strings.TrimSpace(childState.Node.Name), record.Name),
		ChildBackendURL:   childBackendURL,
		ChildDesktopURL:   childDesktopURL,
		ChildPublicKey:    strings.TrimSpace(childState.Node.PublicKey),
		ChildFingerprint:  strings.TrimSpace(childState.Node.Fingerprint),
		RequestedAtMillis: time.Now().UnixMilli(),
	}); err != nil {
		current, loadErr := s.loadDeploymentRecord(record.ID)
		if loadErr != nil {
			return pebblestore.DeployContainerRecord{}, loadErr
		}
		return current, err
	}
	fallbackBackendURL, fallbackDesktopURL, fallbackErr := s.resolveHostDrivenParentCallbackURLs(strings.TrimSpace(record.Runtime), startupCfg)
	if fallbackErr != nil {
		return s.failDeploymentAttach(record.ID, fallbackErr)
	}
	attachState, err := s.AttachApprove(ctx, ContainerAttachApproveInput{
		DeploymentID:      record.ID,
		BootstrapSecret:   record.BootstrapSecret,
		HostSwarmID:       strings.TrimSpace(hostState.Node.SwarmID),
		HostDisplayName:   firstNonEmpty(hostState.Node.Name, strings.TrimSpace(startupCfg.SwarmName), "Primary"),
		HostPublicKey:     strings.TrimSpace(hostState.Node.PublicKey),
		HostFingerprint:   strings.TrimSpace(hostState.Node.Fingerprint),
		HostBackendURL:    firstNonEmpty(strings.TrimSpace(record.HostBackendURL), strings.TrimSpace(record.HostAPIBaseURL), fallbackBackendURL),
		HostDesktopURL:    firstNonEmpty(strings.TrimSpace(record.HostDesktopURL), fallbackDesktopURL),
		GroupID:           strings.TrimSpace(record.GroupID),
		GroupName:         strings.TrimSpace(record.GroupName),
		GroupNetworkName:  strings.TrimSpace(record.GroupNetworkName),
		SyncVaultPassword: strings.TrimSpace(syncVaultPassword),
	})
	if err != nil {
		current, loadErr := s.loadDeploymentRecord(record.ID)
		if loadErr != nil {
			return pebblestore.DeployContainerRecord{}, loadErr
		}
		return current, err
	}
	currentRecord, loadErr := s.loadDeploymentRecord(record.ID)
	if loadErr != nil {
		return pebblestore.DeployContainerRecord{}, loadErr
	}
	finalizeInput := ContainerAttachFinalizeInput{
		DeploymentID:      record.ID,
		BootstrapSecret:   record.BootstrapSecret,
		HostSwarmID:       attachState.HostSwarmID,
		HostDisplayName:   attachState.HostDisplayName,
		HostPublicKey:     attachState.HostPublicKey,
		HostFingerprint:   attachState.HostFingerprint,
		HostBackendURL:    attachState.HostBackendURL,
		HostDesktopURL:    attachState.HostDesktopURL,
		GroupID:           attachState.GroupID,
		GroupName:         attachState.GroupName,
		GroupNetworkName:  attachState.GroupNetworkName,
		SyncMode:          currentRecord.SyncMode,
		SyncModules:       append([]string(nil), currentRecord.SyncModules...),
		SyncVaultPassword: strings.TrimSpace(syncVaultPassword),
	}
	if currentRecord.SyncEnabled && workspaceruntime.ReplicationSyncModuleEnabled(currentRecord.SyncModules, workspaceruntime.ReplicationSyncModuleCredentials) {
		if vaultStatus, err := s.auth.VaultStatus(); err != nil {
			return s.failDeploymentAttach(record.ID, err)
		} else if vaultStatus.Enabled && strings.TrimSpace(attachState.SyncManagedVaultKey) == "" {
			return s.failDeploymentAttach(record.ID, fmt.Errorf("managed child vault key was not attached to host-driven finalize"))
		}
		bundle, err := s.SyncCredentialBundle(ctx, ContainerSyncCredentialRequestInput{
			DeploymentID:    currentRecord.ID,
			BootstrapSecret: currentRecord.BootstrapSecret,
			VaultPassword:   strings.TrimSpace(syncVaultPassword),
		})
		if err != nil {
			return s.failDeploymentAttach(record.ID, err)
		}
		finalizeInput.SyncOwnerSwarmID = bundle.OwnerSwarmID
		finalizeInput.SyncBundlePassword = bundle.BundlePassword
		finalizeInput.SyncBundle = bundle.Bundle
	}
	finalizeInput.WorkspaceBootstrap = append([]ContainerWorkspaceBootstrap(nil), currentRecord.WorkspaceBootstrap...)
	finalizeInput.HostToChildPeerAuthToken = strings.TrimSpace(attachState.HostToChildPeerAuthToken)
	finalizeInput.ChildToHostPeerAuthToken = strings.TrimSpace(attachState.ChildToHostPeerAuthToken)
	finalizeInput.SyncManagedVaultKey = strings.TrimSpace(attachState.SyncManagedVaultKey)
	if err := s.postLocalAttachFinalize(ctx, strings.TrimRight(childBackendURL, "/")+"/v1/deploy/container/attach/finalize", "", finalizeInput); err != nil {
		return s.failDeploymentAttach(record.ID, err)
	}
	return s.loadDeploymentRecord(record.ID)
}

func (s *Service) waitForHostReachableChildState(ctx context.Context, childBackendURL, deploymentID, bootstrapSecret string) (swarmruntime.LocalState, error) {
	deadline := time.Now().Add(20 * time.Second)
	endpoint := strings.TrimRight(strings.TrimSpace(childBackendURL), "/") + "/v1/deploy/container/attach/child-state"
	for {
		state, err := s.fetchLocalChildAttachState(ctx, endpoint, deploymentID, bootstrapSecret)
		if err == nil {
			if strings.TrimSpace(state.Node.SwarmID) != "" && strings.TrimSpace(state.Node.PublicKey) != "" && strings.TrimSpace(state.Node.Fingerprint) != "" {
				return state, nil
			}
			err = fmt.Errorf("child swarm state is not ready")
		}
		if time.Now().After(deadline) {
			return swarmruntime.LocalState{}, fmt.Errorf("wait for child swarm state at %q: %w", endpoint, err)
		}
		select {
		case <-ctx.Done():
			return swarmruntime.LocalState{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *Service) fetchLocalChildAttachState(ctx context.Context, endpoint, deploymentID, bootstrapSecret string) (swarmruntime.LocalState, error) {
	payload := map[string]string{
		"deployment_id":    strings.TrimSpace(deploymentID),
		"bootstrap_secret": strings.TrimSpace(bootstrapSecret),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return swarmruntime.LocalState{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return swarmruntime.LocalState{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return swarmruntime.LocalState{}, err
	}
	defer resp.Body.Close()
	var decoded struct {
		OK    bool                    `json:"ok"`
		State swarmruntime.LocalState `json:"state"`
		Error string                  `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return swarmruntime.LocalState{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return swarmruntime.LocalState{}, errors.New(firstNonEmpty(decoded.Error, fmt.Sprintf("child state request failed with status %d", resp.StatusCode)))
	}
	return decoded.State, nil
}

func (s *Service) postLocalAttachFinalize(ctx context.Context, endpoint, token string, payload ContainerAttachFinalizeInput) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	log.Printf("deploy post local attach finalize endpoint=%q managed_vault_key_present=%t payload_bytes=%d", endpoint, strings.TrimSpace(payload.SyncManagedVaultKey) != "", len(body))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		req.Header.Set("X-Swarm-Token", strings.TrimSpace(token))
	}
	if strings.TrimSpace(payload.SyncManagedVaultKey) != "" {
		req.Header.Set(syncManagedVaultKeyHeader, strings.TrimSpace(payload.SyncManagedVaultKey))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var decoded struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New(firstNonEmpty(decoded.Error, fmt.Sprintf("child attach finalize failed with status %d", resp.StatusCode)))
	}
	return nil
}

func (s *Service) loadDeploymentRecord(id string) (pebblestore.DeployContainerRecord, error) {
	record, ok, err := s.store.Get(id)
	if err != nil {
		return pebblestore.DeployContainerRecord{}, err
	}
	if !ok {
		return pebblestore.DeployContainerRecord{}, fmt.Errorf("deploy container not found")
	}
	return record, nil
}

func (s *Service) failDeploymentAttach(id string, err error) (pebblestore.DeployContainerRecord, error) {
	record, ok, getErr := s.store.Get(id)
	if getErr != nil {
		return pebblestore.DeployContainerRecord{}, getErr
	}
	if !ok {
		return pebblestore.DeployContainerRecord{}, fmt.Errorf("deploy container not found")
	}
	record.AttachStatus = "failed"
	record.Status = "running"
	record.LastAttachError = strings.TrimSpace(err.Error())
	record.UpdatedAt = time.Now().UnixMilli()
	saved, saveErr := s.store.Put(record)
	if saveErr != nil {
		return pebblestore.DeployContainerRecord{}, saveErr
	}
	return saved, err
}

func hostRole(cfg startupconfig.FileConfig) string {
	if cfg.Child {
		return "child"
	}
	return "master"
}

func (s *Service) requestLocalAttach(ctx context.Context, cfg startupconfig.FileConfig, state swarmruntime.LocalState) (ContainerAttachState, error) {
	childBackendURL, childDesktopURL, err := childContainerReachableURLs(cfg)
	if err != nil {
		return ContainerAttachState{}, err
	}
	socketPath := strings.TrimSpace(cfg.DeployContainer.LocalTransportSocketPath)
	parentAPIBaseURL := strings.TrimSpace(cfg.DeployContainer.HostAPIBaseURL)
	if socketPath != "" {
		parentAPIBaseURL = childLocalTransportBaseURL
	}
	if parentAPIBaseURL == "" {
		return ContainerAttachState{}, fmt.Errorf("child startup config is missing a local parent transport")
	}
	log.Printf("deploy request local attach deployment_id=%q parent_api_base_url=%q child_backend_url=%q child_desktop_url=%q parent_swarm_id=%q", strings.TrimSpace(cfg.DeployContainer.DeploymentID), parentAPIBaseURL, childBackendURL, childDesktopURL, strings.TrimSpace(cfg.ParentSwarmID))
	attachEndpoint := strings.TrimRight(parentAPIBaseURL, "/") + "/v1/deploy/container/attach/request"
	attachPayload := map[string]any{
		"deployment_id":      strings.TrimSpace(cfg.DeployContainer.DeploymentID),
		"bootstrap_secret":   strings.TrimSpace(cfg.DeployContainer.BootstrapSecret),
		"child_swarm_id":     strings.TrimSpace(state.Node.SwarmID),
		"child_display_name": firstNonEmpty(strings.TrimSpace(cfg.SwarmName), state.Node.Name),
		"child_backend_url":  childBackendURL,
		"requested_at":       time.Now().UnixMilli(),
		"child_public_key":   strings.TrimSpace(state.Node.PublicKey),
		"child_fingerprint":  strings.TrimSpace(state.Node.Fingerprint),
	}
	if childDesktopURL != "" {
		attachPayload["child_desktop_url"] = childDesktopURL
	}
	attachState, err := s.postLocalAttachRequest(ctx, attachEndpoint, socketPath, attachPayload)
	if err != nil {
		return ContainerAttachState{}, err
	}
	log.Printf("deploy request local attach child acknowledged deployment_id=%q attach_status=%q host_swarm_id=%q", attachState.DeploymentID, attachState.AttachStatus, attachState.HostSwarmID)
	approvePayload, err := buildLocalAttachApprovePayload(cfg, state, attachState)
	if err != nil {
		return ContainerAttachState{}, err
	}
	approveEndpoint := strings.TrimRight(parentAPIBaseURL, "/") + "/v1/deploy/container/attach/approve"
	return s.postLocalAttachApprove(ctx, approveEndpoint, socketPath, approvePayload)
}

func newBootstrapClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: transport,
	}
}

func newUnixSocketBootstrapClient(socketPath string) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", strings.TrimSpace(socketPath))
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: transport,
	}
}

func (s *Service) bootstrapHTTPClient(socketPath string) *http.Client {
	if strings.TrimSpace(socketPath) == "" {
		if s != nil && s.client != nil {
			return s.client
		}
		return newBootstrapClient()
	}
	return newUnixSocketBootstrapClient(socketPath)
}

func (s *Service) bootstrapHTTPClientForEndpoint(socketPath, endpoint string) (*http.Client, error) {
	client := s.bootstrapHTTPClient(socketPath)
	if strings.TrimSpace(socketPath) != "" {
		return client, nil
	}
	return tailscalehttp.ClientForEndpoint(endpoint, client)
}

func (s *Service) postLocalAttachRequest(ctx context.Context, endpoint, socketPath string, payload map[string]any) (ContainerAttachState, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return ContainerAttachState{}, err
	}
	log.Printf("deploy post local attach request endpoint=%q payload_bytes=%d", endpoint, len(body))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ContainerAttachState{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client, err := s.bootstrapHTTPClientForEndpoint(socketPath, endpoint)
	if err != nil {
		return ContainerAttachState{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return ContainerAttachState{}, err
	}
	defer resp.Body.Close()
	var decoded struct {
		OK     bool                 `json:"ok"`
		Attach ContainerAttachState `json:"attach"`
		Error  string               `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return ContainerAttachState{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decoded.Attach, errors.New(firstNonEmpty(decoded.Error, fmt.Sprintf("child attach request failed with status %d", resp.StatusCode)))
	}
	log.Printf("deploy post local attach request response endpoint=%q status=%d attach_status=%q", endpoint, resp.StatusCode, decoded.Attach.AttachStatus)
	return decoded.Attach, nil
}

func buildLocalAttachApprovePayload(cfg startupconfig.FileConfig, state swarmruntime.LocalState, attachState ContainerAttachState) (map[string]any, error) {
	groupID := strings.TrimSpace(attachState.GroupID)
	if groupID == "" {
		if groupID = strings.TrimSpace(cfg.ParentSwarmID); groupID == "" {
			groupID = strings.TrimSpace(attachState.HostSwarmID)
		}
	}
	if groupID == "" {
		return nil, fmt.Errorf("attach approval is missing group id")
	}
	groupName := firstNonEmpty(attachState.GroupName, groupID)
	groupNetworkName := firstNonEmpty(attachState.GroupNetworkName, swarmruntime.SuggestedGroupNetworkName(groupName, groupID))
	payload := map[string]any{
		"deployment_id":                 strings.TrimSpace(cfg.DeployContainer.DeploymentID),
		"bootstrap_secret":              strings.TrimSpace(cfg.DeployContainer.BootstrapSecret),
		"host_swarm_id":                 strings.TrimSpace(attachState.HostSwarmID),
		"host_display_name":             firstNonEmpty(attachState.HostDisplayName, state.Node.Name, "Primary"),
		"host_public_key":               strings.TrimSpace(attachState.HostPublicKey),
		"host_fingerprint":              strings.TrimSpace(attachState.HostFingerprint),
		"host_backend_url":              strings.TrimSpace(attachState.HostBackendURL),
		"host_to_child_peer_auth_token": strings.TrimSpace(attachState.HostToChildPeerAuthToken),
		"child_to_host_peer_auth_token": strings.TrimSpace(attachState.ChildToHostPeerAuthToken),
		"group_id":                      groupID,
		"group_name":                    groupName,
		"group_network_name":            groupNetworkName,
	}
	if desktopURL := strings.TrimSpace(attachState.HostDesktopURL); desktopURL != "" {
		payload["host_desktop_url"] = desktopURL
	}
	return payload, nil
}

func (s *Service) postLocalAttachApprove(ctx context.Context, endpoint, socketPath string, payload map[string]any) (ContainerAttachState, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return ContainerAttachState{}, err
	}
	log.Printf("deploy post local attach approve endpoint=%q payload_bytes=%d", endpoint, len(body))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ContainerAttachState{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client, err := s.bootstrapHTTPClientForEndpoint(socketPath, endpoint)
	if err != nil {
		return ContainerAttachState{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return ContainerAttachState{}, err
	}
	defer resp.Body.Close()
	var decoded struct {
		OK     bool                 `json:"ok"`
		Attach ContainerAttachState `json:"attach"`
		Error  string               `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return ContainerAttachState{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decoded.Attach, errors.New(firstNonEmpty(decoded.Error, fmt.Sprintf("child attach approve failed with status %d", resp.StatusCode)))
	}
	if strings.ToLower(strings.TrimSpace(decoded.Attach.AttachStatus)) != "attached" {
		return decoded.Attach, errors.New(firstNonEmpty(decoded.Attach.LastError, "child attach approval did not complete"))
	}
	log.Printf("deploy post local attach approve response endpoint=%q status=%d attach_status=%q", endpoint, resp.StatusCode, decoded.Attach.AttachStatus)
	return decoded.Attach, nil
}

func (s *Service) finalizeChildAttach(cfg startupconfig.FileConfig, state swarmruntime.LocalState, status ContainerAttachState, finalizeInput ContainerAttachFinalizeInput) error {
	if s == nil || s.swarmStore == nil {
		return fmt.Errorf("deploy container service is not configured")
	}
	pairing, ok, err := s.swarmStore.GetLocalPairing()
	if err != nil {
		return err
	}
	if !ok {
		pairing = pebblestore.SwarmLocalPairingRecord{}
	}
	pairing.PairingState = startupconfig.PairingStatePaired
	pairing.ParentSwarmID = firstNonEmpty(status.HostSwarmID, strings.TrimSpace(cfg.ParentSwarmID))
	pairing.LastDecision = "approved"
	pairing.LastDecisionReason = ""
	pairing.LastUpdatedByRole = "child"
	if _, err := s.swarmStore.PutLocalPairing(pairing); err != nil {
		return err
	}
	hostSwarmID := firstNonEmpty(status.HostSwarmID, strings.TrimSpace(cfg.ParentSwarmID))
	if hostSwarmID == "" {
		return fmt.Errorf("approved attach is missing host swarm id")
	}
	existingPeer, peerExists, err := s.swarmStore.GetTrustedPeer(hostSwarmID)
	if err != nil {
		return err
	}
	outgoingPeerAuthToken := strings.TrimSpace(finalizeInput.ChildToHostPeerAuthToken)
	if outgoingPeerAuthToken == "" && peerExists {
		outgoingPeerAuthToken = strings.TrimSpace(existingPeer.OutgoingPeerAuthToken)
	}
	incomingPeerAuthHash := ""
	if token := strings.TrimSpace(finalizeInput.HostToChildPeerAuthToken); token != "" {
		incomingPeerAuthHash = swarmruntime.HashPeerAuthToken(token)
	} else if peerExists {
		incomingPeerAuthHash = strings.TrimSpace(existingPeer.IncomingPeerAuthHash)
	}
	if _, err := s.swarmStore.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{
		SwarmID:               hostSwarmID,
		Name:                  firstNonEmpty(status.HostDisplayName, existingPeer.Name, "Primary"),
		Role:                  "master",
		PublicKey:             firstNonEmpty(strings.TrimSpace(status.HostPublicKey), existingPeer.PublicKey),
		Fingerprint:           firstNonEmpty(strings.TrimSpace(status.HostFingerprint), existingPeer.Fingerprint),
		Relationship:          swarmruntime.RelationshipParent,
		ParentSwarmID:         "",
		TransportMode:         firstNonEmpty(cfg.NetworkMode, existingPeer.TransportMode, startupconfig.NetworkModeLAN),
		OutgoingPeerAuthToken: outgoingPeerAuthToken,
		IncomingPeerAuthHash:  incomingPeerAuthHash,
		ApprovedAt:            time.Now().UnixMilli(),
	}); err != nil {
		return err
	}
	groupID := strings.TrimSpace(status.GroupID)
	if groupID != "" {
		groupName := firstNonEmpty(status.GroupName, groupID)
		groupNetworkName := firstNonEmpty(status.GroupNetworkName, swarmruntime.SuggestedGroupNetworkName(groupName, groupID))
		groupRecord, ok, err := s.swarmStore.GetGroup(groupID)
		if err != nil {
			return err
		}
		if !ok {
			if _, err := s.swarmStore.PutGroup(pebblestore.SwarmGroupRecord{
				ID:          groupID,
				Name:        groupName,
				NetworkName: groupNetworkName,
				HostSwarmID: hostSwarmID,
			}); err != nil {
				return err
			}
		} else if groupRecord.Name == "" || groupRecord.HostSwarmID == "" || groupRecord.NetworkName == "" {
			groupRecord.Name = groupName
			groupRecord.NetworkName = firstNonEmpty(groupRecord.NetworkName, groupNetworkName)
			groupRecord.HostSwarmID = firstNonEmpty(groupRecord.HostSwarmID, hostSwarmID)
			if _, err := s.swarmStore.PutGroup(groupRecord); err != nil {
				return err
			}
		}
		if _, err := s.swarmStore.PutGroupMembership(pebblestore.SwarmGroupMembershipRecord{
			GroupID:        groupID,
			SwarmID:        hostSwarmID,
			Name:           firstNonEmpty(status.HostDisplayName, groupName, "Primary"),
			SwarmRole:      "master",
			MembershipRole: swarmruntime.GroupMembershipRoleHost,
		}); err != nil {
			return err
		}
		if _, err := s.swarmStore.PutGroupMembership(pebblestore.SwarmGroupMembershipRecord{
			GroupID:        groupID,
			SwarmID:        strings.TrimSpace(state.Node.SwarmID),
			Name:           firstNonEmpty(state.Node.Name, strings.TrimSpace(cfg.SwarmName), "Child"),
			SwarmRole:      "child",
			MembershipRole: swarmruntime.GroupMembershipRoleMember,
		}); err != nil {
			return err
		}
		if err := s.swarmStore.PutCurrentGroupID(groupID); err != nil {
			return err
		}
	}
	if cfg.DeployContainer.SyncEnabled {
		cfg.DeployContainer.SyncModules = workspaceruntime.NormalizeReplicationSyncModules(firstNonEmptyStringSlice(finalizeInput.SyncModules, cfg.DeployContainer.SyncModules))
		if len(cfg.DeployContainer.SyncModules) == 0 {
			cfg.DeployContainer.SyncModules = workspaceruntime.DefaultReplicationSyncModules()
		}
		cfg.DeployContainer.SyncMode = firstNonEmpty(strings.TrimSpace(finalizeInput.SyncMode), strings.TrimSpace(cfg.DeployContainer.SyncMode), workspaceruntime.ReplicationSyncModeManaged)
		if workspaceruntime.ReplicationSyncModuleEnabled(cfg.DeployContainer.SyncModules, workspaceruntime.ReplicationSyncModuleCredentials) {
			if s.auth == nil {
				return fmt.Errorf("auth service is not configured")
			}
			bundle := ContainerSyncCredentialBundle{
				OwnerSwarmID:   strings.TrimSpace(finalizeInput.SyncOwnerSwarmID),
				BundlePassword: strings.TrimSpace(finalizeInput.SyncBundlePassword),
				Bundle:         append([]byte(nil), finalizeInput.SyncBundle...),
			}
			if len(bundle.Bundle) == 0 || strings.TrimSpace(bundle.BundlePassword) == "" {
				var err error
				bundle, err = s.fetchSyncCredentialBundle(context.Background(), cfg, status)
				if err != nil {
					return err
				}
			}
			ownerSwarmID := firstNonEmpty(bundle.OwnerSwarmID, strings.TrimSpace(cfg.DeployContainer.SyncOwnerSwarmID), hostSwarmID)
			updatedPairing, err := s.applyManagedCredentialBundle(pairing, ownerSwarmID, bundle, strings.TrimSpace(finalizeInput.SyncVaultPassword), strings.TrimSpace(finalizeInput.SyncManagedVaultKey))
			if err != nil {
				return err
			}
			pairing = updatedPairing
		}
		if workspaceruntime.ReplicationSyncModuleEnabled(cfg.DeployContainer.SyncModules, workspaceruntime.ReplicationSyncModuleAgents) || workspaceruntime.ReplicationSyncModuleEnabled(cfg.DeployContainer.SyncModules, workspaceruntime.ReplicationSyncModuleCustomTools) {
			bundle, err := s.fetchSyncAgentBundle(context.Background(), cfg, status)
			if err != nil {
				return err
			}
			if err := s.applyManagedAgentBundle(bundle, cfg.DeployContainer.SyncModules); err != nil {
				return err
			}
		}
	}
	if cfg.DeployContainer.HostDriven {
		if err := s.applyBootstrapWorkspaces(cfg, status, pairing, finalizeInput.WorkspaceBootstrap); err != nil {
			return err
		}
	} else {
		if err := s.provisionBootstrapWorkspaces(context.Background(), cfg, state, status, pairing); err != nil {
			return err
		}
	}
	return nil
}

func buildDeploymentSyncCredentialURL(hostAPIBaseURL string) string {
	hostAPIBaseURL = strings.TrimRight(strings.TrimSpace(hostAPIBaseURL), "/")
	if hostAPIBaseURL == "" {
		return ""
	}
	return hostAPIBaseURL + "/v1/deploy/container/sync/credentials"
}

func buildDeploymentSyncAgentURL(hostAPIBaseURL string) string {
	hostAPIBaseURL = strings.TrimRight(strings.TrimSpace(hostAPIBaseURL), "/")
	if hostAPIBaseURL == "" {
		return ""
	}
	return hostAPIBaseURL + "/v1/deploy/container/sync/agents"
}

func buildDeploymentWorkspaceBootstrapURL(hostAPIBaseURL string) string {
	hostAPIBaseURL = strings.TrimRight(strings.TrimSpace(hostAPIBaseURL), "/")
	if hostAPIBaseURL == "" {
		return ""
	}
	return hostAPIBaseURL + "/v1/deploy/container/workspaces/bootstrap"
}

func (s *Service) fetchSyncAgentBundle(ctx context.Context, cfg startupconfig.FileConfig, status ContainerAttachState) (ContainerSyncAgentBundle, error) {
	socketPath := strings.TrimSpace(cfg.DeployContainer.LocalTransportSocketPath)
	endpoint := strings.TrimSpace(cfg.DeployContainer.SyncAgentURL)
	if socketPath != "" {
		endpoint = buildDeploymentSyncAgentURL(childLocalTransportBaseURL)
	} else if endpoint == "" {
		hostAPIBaseURL := firstNonEmpty(status.HostBackendURL, strings.TrimSpace(cfg.DeployContainer.HostAPIBaseURL))
		endpoint = buildDeploymentSyncAgentURL(hostAPIBaseURL)
	}
	if endpoint == "" {
		return ContainerSyncAgentBundle{}, fmt.Errorf("sync agent url is not configured")
	}
	payload, err := json.Marshal(ContainerSyncCredentialRequestInput{
		DeploymentID:    strings.TrimSpace(cfg.DeployContainer.DeploymentID),
		BootstrapSecret: strings.TrimSpace(cfg.DeployContainer.BootstrapSecret),
	})
	if err != nil {
		return ContainerSyncAgentBundle{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ContainerSyncAgentBundle{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	s.addPeerAuthHeaders(req, firstNonEmpty(strings.TrimSpace(status.HostSwarmID), strings.TrimSpace(cfg.ParentSwarmID)))
	client, err := s.bootstrapHTTPClientForEndpoint(socketPath, endpoint)
	if err != nil {
		return ContainerSyncAgentBundle{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return ContainerSyncAgentBundle{}, err
	}
	defer resp.Body.Close()
	var decoded struct {
		OK     bool                     `json:"ok"`
		Bundle ContainerSyncAgentBundle `json:"bundle"`
		Error  string                   `json:"error"`
		PathID string                   `json:"path_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return ContainerSyncAgentBundle{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ContainerSyncAgentBundle{}, errors.New(firstNonEmpty(decoded.Error, fmt.Sprintf("sync agent fetch failed with status %d", resp.StatusCode)))
	}
	return decoded.Bundle, nil
}

func (s *Service) applyManagedAgentBundle(bundle ContainerSyncAgentBundle, modules []string) error {
	if s == nil || s.agents == nil {
		return fmt.Errorf("deploy container service is not configured")
	}
	syncProfiles := workspaceruntime.ReplicationSyncModuleEnabled(modules, workspaceruntime.ReplicationSyncModuleAgents)
	syncCustomTools := workspaceruntime.ReplicationSyncModuleEnabled(modules, workspaceruntime.ReplicationSyncModuleCustomTools)
	if !syncProfiles && !syncCustomTools {
		return nil
	}
	_, _, _, err := s.agents.ReplaceManagedState(bundle.State, syncProfiles, syncCustomTools)
	return err
}

func firstNonEmptyStringSlice(values ...[]string) []string {
	for _, value := range values {
		if len(value) == 0 {
			continue
		}
		return append([]string(nil), value...)
	}
	return nil
}

func (s *Service) RunManagedCredentialSyncLoop(ctx context.Context) {
	if s == nil {
		return
	}
	if err := s.SyncManagedCredentialsOnce(ctx); err != nil {
		log.Printf("warning: managed credential sync failed: %v", err)
	}
	ticker := time.NewTicker(managedCredentialSyncPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.SyncManagedCredentialsOnce(ctx); err != nil {
				log.Printf("warning: managed credential sync failed: %v", err)
			}
		}
	}
}

func (s *Service) SyncManagedCredentialsOnce(ctx context.Context) error {
	if s == nil || s.auth == nil || s.swarmStore == nil {
		return fmt.Errorf("deploy container service is not configured")
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return err
	}
	if !cfg.Child {
		return nil
	}
	pairing, ok, err := s.swarmStore.GetLocalPairing()
	if err != nil {
		return err
	}
	if !ok {
		pairing = pebblestore.SwarmLocalPairingRecord{}
	}
	if !strings.EqualFold(strings.TrimSpace(pairing.PairingState), startupconfig.PairingStatePaired) {
		return nil
	}
	switch {
	case cfg.DeployContainer.Enabled && cfg.DeployContainer.SyncEnabled:
		cfg.DeployContainer.SyncModules = workspaceruntime.NormalizeReplicationSyncModules(cfg.DeployContainer.SyncModules)
		if len(cfg.DeployContainer.SyncModules) == 0 {
			cfg.DeployContainer.SyncModules = workspaceruntime.DefaultReplicationSyncModules()
		}
		ownerSwarmID := firstNonEmpty(strings.TrimSpace(cfg.DeployContainer.SyncOwnerSwarmID), strings.TrimSpace(pairing.ParentSwarmID), strings.TrimSpace(cfg.ParentSwarmID))
		if ownerSwarmID == "" {
			return nil
		}
		if workspaceruntime.ReplicationSyncModuleEnabled(cfg.DeployContainer.SyncModules, workspaceruntime.ReplicationSyncModuleCredentials) {
			bundle, err := s.fetchSyncCredentialBundle(ctx, cfg, ContainerAttachState{HostSwarmID: ownerSwarmID})
			if err != nil {
				return s.recordManagedCredentialSyncFailure(pairing, err)
			}
			updatedPairing, err := s.applyManagedCredentialBundle(pairing, ownerSwarmID, bundle, "", "")
			if err != nil {
				return s.recordManagedCredentialSyncFailure(pairing, err)
			}
			pairing = updatedPairing
		}
		if workspaceruntime.ReplicationSyncModuleEnabled(cfg.DeployContainer.SyncModules, workspaceruntime.ReplicationSyncModuleAgents) || workspaceruntime.ReplicationSyncModuleEnabled(cfg.DeployContainer.SyncModules, workspaceruntime.ReplicationSyncModuleCustomTools) {
			bundle, err := s.fetchSyncAgentBundle(ctx, cfg, ContainerAttachState{HostSwarmID: ownerSwarmID})
			if err != nil {
				return s.recordManagedCredentialSyncFailure(pairing, err)
			}
			if err := s.applyManagedAgentBundle(bundle, cfg.DeployContainer.SyncModules); err != nil {
				return s.recordManagedCredentialSyncFailure(pairing, err)
			}
		}
		return nil
	case cfg.RemoteDeploy.Enabled && cfg.RemoteDeploy.SyncEnabled:
		ownerSwarmID := firstNonEmpty(strings.TrimSpace(cfg.RemoteDeploy.SyncOwnerSwarmID), strings.TrimSpace(pairing.ParentSwarmID), strings.TrimSpace(cfg.ParentSwarmID))
		if ownerSwarmID == "" {
			return nil
		}
		vaultPassword := currentRemoteSyncVaultPassword()
		bundle, err := s.fetchRemoteDeploySyncCredentialBundle(ctx, cfg, ownerSwarmID, vaultPassword)
		if err != nil {
			return s.recordManagedCredentialSyncFailure(pairing, err)
		}
		updatedPairing, err := s.applyManagedCredentialBundle(pairing, ownerSwarmID, bundle, vaultPassword, "")
		if err != nil {
			return s.recordManagedCredentialSyncFailure(pairing, err)
		}
		if strings.TrimSpace(vaultPassword) != "" && updatedPairing.ManagedAuthAppliedAt > 0 {
			_ = os.Unsetenv(remoteSyncVaultPasswordEnvKey)
		}
		return nil
	default:
		return nil
	}
}

func (s *Service) applyManagedCredentialBundle(pairing pebblestore.SwarmLocalPairingRecord, ownerSwarmID string, bundle ContainerSyncCredentialBundle, vaultPassword, managedVaultKey string) (pebblestore.SwarmLocalPairingRecord, error) {
	if s == nil || s.auth == nil || s.swarmStore == nil {
		return pairing, fmt.Errorf("deploy container service is not configured")
	}
	ownerSwarmID = strings.TrimSpace(ownerSwarmID)
	if ownerSwarmID == "" {
		return pairing, fmt.Errorf("sync owner swarm id is required")
	}
	metadata, err := s.auth.CredentialBundleMetadata(bundle.BundlePassword, bundle.Bundle)
	if err != nil {
		return pairing, err
	}
	pairing.ManagedAuthOwnerSwarmID = ownerSwarmID
	pairing.ManagedAuthLastAttemptAt = time.Now().UnixMilli()
	if metadata.SnapshotHash != "" && strings.EqualFold(strings.TrimSpace(pairing.ManagedAuthSnapshotHash), metadata.SnapshotHash) {
		pairing.ManagedAuthLastError = ""
		saved, err := s.swarmStore.PutLocalPairing(pairing)
		if err != nil {
			return pairing, err
		}
		return saved, nil
	}
	result, err := s.auth.ImportManagedCredentialsWithVaultAccess(ownerSwarmID, bundle.BundlePassword, strings.TrimSpace(vaultPassword), strings.TrimSpace(managedVaultKey), bundle.Bundle)
	if err != nil {
		return pairing, err
	}
	pairing.ManagedAuthSnapshotHash = firstNonEmpty(strings.TrimSpace(result.SnapshotHash), strings.TrimSpace(metadata.SnapshotHash))
	pairing.ManagedAuthAppliedAt = time.Now().UnixMilli()
	pairing.ManagedAuthLastAttemptAt = pairing.ManagedAuthAppliedAt
	pairing.ManagedAuthLastError = ""
	saved, err := s.swarmStore.PutLocalPairing(pairing)
	if err != nil {
		return pairing, err
	}
	return saved, nil
}

func (s *Service) recordManagedCredentialSyncFailure(pairing pebblestore.SwarmLocalPairingRecord, syncErr error) error {
	if s == nil || s.swarmStore == nil {
		return syncErr
	}
	pairing.ManagedAuthLastAttemptAt = time.Now().UnixMilli()
	pairing.ManagedAuthLastError = strings.TrimSpace(syncErr.Error())
	if _, err := s.swarmStore.PutLocalPairing(pairing); err != nil {
		return errors.Join(syncErr, err)
	}
	return syncErr
}

func (s *Service) provisionBootstrapWorkspaces(ctx context.Context, cfg startupconfig.FileConfig, state swarmruntime.LocalState, status ContainerAttachState, pairing pebblestore.SwarmLocalPairingRecord) error {
	_ = state
	items, err := s.fetchWorkspaceBootstrap(ctx, cfg, status)
	if err != nil {
		return err
	}
	return s.applyBootstrapWorkspaces(cfg, status, pairing, items)
}

func (s *Service) applyBootstrapWorkspaces(cfg startupconfig.FileConfig, status ContainerAttachState, pairing pebblestore.SwarmLocalPairingRecord, items []ContainerWorkspaceBootstrap) error {
	if s == nil || s.workspace == nil || s.swarmStore == nil {
		return fmt.Errorf("deploy container service is not configured")
	}
	deploymentID := strings.TrimSpace(cfg.DeployContainer.DeploymentID)
	if deploymentID == "" {
		return fmt.Errorf("child deploy bootstrap is missing deployment id")
	}
	if strings.EqualFold(strings.TrimSpace(pairing.WorkspaceBootstrapDeploymentID), deploymentID) && pairing.WorkspaceBootstrapAt > 0 {
		return nil
	}
	if len(items) == 0 {
		updated := pairing
		updated.WorkspaceBootstrapDeploymentID = deploymentID
		updated.WorkspaceBootstrapAt = time.Now().UnixMilli()
		if _, err := s.swarmStore.PutLocalPairing(updated); err != nil {
			return err
		}
		return nil
	}
	known, err := s.workspace.ListKnown(100000)
	if err != nil {
		return err
	}
	knownByPath := make(map[string]workspaceruntime.Entry, len(known))
	for _, entry := range known {
		knownByPath[strings.TrimSpace(entry.Path)] = entry
	}
	current, hasCurrent, err := s.workspace.CurrentBinding()
	if err != nil {
		return err
	}
	currentAssigned := hasCurrent && strings.TrimSpace(current.WorkspacePath) != ""
	hostSwarmID := firstNonEmpty(status.HostSwarmID, strings.TrimSpace(cfg.ParentSwarmID))
	hostSwarmName := firstNonEmpty(status.HostDisplayName, "Primary")
	for _, item := range items {
		workspacePath := strings.TrimSpace(item.TargetWorkspacePath)
		if workspacePath == "" {
			continue
		}
		entry, exists := knownByPath[workspacePath]
		if !exists {
			if _, err := s.workspace.Add(workspacePath, strings.TrimSpace(item.SourceWorkspaceName), strings.TrimSpace(item.ThemeID), false); err != nil {
				return err
			}
			known, err := s.workspace.ListKnown(100000)
			if err != nil {
				return err
			}
			knownByPath = make(map[string]workspaceruntime.Entry, len(known))
			for _, refreshed := range known {
				knownByPath[strings.TrimSpace(refreshed.Path)] = refreshed
			}
			entry = knownByPath[workspacePath]
		}
		for _, directory := range item.Directories {
			targetPath := strings.TrimSpace(directory.TargetPath)
			if targetPath == "" {
				continue
			}
			if containsTrimmedString(entry.Directories, targetPath) {
				continue
			}
			if _, err := s.workspace.AddDirectory(workspacePath, targetPath); err != nil {
				return err
			}
			entry.Directories = append(entry.Directories, targetPath)
		}
		_, err := s.workspace.AddReplicationLink(workspacePath, pebblestore.WorkspaceReplicationLink{
			ID:                  fmt.Sprintf("bootstrap:%s:%s", deploymentID, strings.TrimSpace(item.SourceWorkspacePath)),
			TargetKind:          "local",
			TargetSwarmID:       hostSwarmID,
			TargetSwarmName:     hostSwarmName,
			TargetWorkspacePath: strings.TrimSpace(item.SourceWorkspacePath),
			ReplicationMode:     strings.TrimSpace(item.ReplicationMode),
			Writable:            item.Writable,
			Sync:                item.Sync,
		})
		if err != nil {
			return err
		}
		if item.MakeCurrent && !currentAssigned {
			if _, err := s.workspace.Select(workspacePath); err != nil {
				return err
			}
			currentAssigned = true
		}
	}
	updated := pairing
	updated.WorkspaceBootstrapDeploymentID = deploymentID
	updated.WorkspaceBootstrapAt = time.Now().UnixMilli()
	if _, err := s.swarmStore.PutLocalPairing(updated); err != nil {
		return err
	}
	return nil
}

func (s *Service) fetchSyncCredentialBundle(ctx context.Context, cfg startupconfig.FileConfig, status ContainerAttachState) (ContainerSyncCredentialBundle, error) {
	socketPath := strings.TrimSpace(cfg.DeployContainer.LocalTransportSocketPath)
	endpoint := strings.TrimSpace(cfg.DeployContainer.SyncCredentialURL)
	if socketPath != "" {
		endpoint = buildDeploymentSyncCredentialURL(childLocalTransportBaseURL)
	} else if endpoint == "" {
		hostAPIBaseURL := firstNonEmpty(status.HostBackendURL, strings.TrimSpace(cfg.DeployContainer.HostAPIBaseURL))
		endpoint = buildDeploymentSyncCredentialURL(hostAPIBaseURL)
	}
	if endpoint == "" {
		return ContainerSyncCredentialBundle{}, fmt.Errorf("sync credential url is not configured")
	}
	payload, err := json.Marshal(ContainerSyncCredentialRequestInput{
		DeploymentID:    strings.TrimSpace(cfg.DeployContainer.DeploymentID),
		BootstrapSecret: strings.TrimSpace(cfg.DeployContainer.BootstrapSecret),
	})
	if err != nil {
		return ContainerSyncCredentialBundle{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ContainerSyncCredentialBundle{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	s.addPeerAuthHeaders(req, firstNonEmpty(strings.TrimSpace(status.HostSwarmID), strings.TrimSpace(cfg.ParentSwarmID)))
	client, err := s.bootstrapHTTPClientForEndpoint(socketPath, endpoint)
	if err != nil {
		return ContainerSyncCredentialBundle{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return ContainerSyncCredentialBundle{}, err
	}
	defer resp.Body.Close()
	var decoded struct {
		OK     bool                          `json:"ok"`
		Bundle ContainerSyncCredentialBundle `json:"bundle"`
		Error  string                        `json:"error"`
		PathID string                        `json:"path_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return ContainerSyncCredentialBundle{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ContainerSyncCredentialBundle{}, errors.New(firstNonEmpty(decoded.Error, fmt.Sprintf("sync credential fetch failed with status %d", resp.StatusCode)))
	}
	if len(decoded.Bundle.Bundle) == 0 {
		return ContainerSyncCredentialBundle{}, fmt.Errorf("sync credential bundle was empty")
	}
	if strings.TrimSpace(decoded.Bundle.BundlePassword) == "" {
		return ContainerSyncCredentialBundle{}, fmt.Errorf("sync credential bundle password was empty")
	}
	return decoded.Bundle, nil
}

func (s *Service) fetchRemoteDeploySyncCredentialBundle(ctx context.Context, cfg startupconfig.FileConfig, ownerSwarmID, vaultPassword string) (ContainerSyncCredentialBundle, error) {
	endpoint := strings.TrimSpace(cfg.RemoteDeploy.SyncCredentialURL)
	if endpoint == "" {
		hostAPIBaseURL := strings.TrimSpace(cfg.RemoteDeploy.HostAPIBaseURL)
		if hostAPIBaseURL != "" {
			endpoint = strings.TrimRight(hostAPIBaseURL, "/") + "/v1/deploy/remote/session/sync/credentials"
		}
	}
	if endpoint == "" {
		return ContainerSyncCredentialBundle{}, fmt.Errorf("remote sync credential url is not configured")
	}
	payload, err := json.Marshal(map[string]string{
		"session_id":     strings.TrimSpace(cfg.RemoteDeploy.SessionID),
		"session_token":  strings.TrimSpace(cfg.RemoteDeploy.SessionToken),
		"vault_password": strings.TrimSpace(vaultPassword),
	})
	if err != nil {
		return ContainerSyncCredentialBundle{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ContainerSyncCredentialBundle{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	s.addPeerAuthHeaders(req, ownerSwarmID)
	client, err := s.bootstrapHTTPClientForEndpoint("", endpoint)
	if err != nil {
		return ContainerSyncCredentialBundle{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return ContainerSyncCredentialBundle{}, err
	}
	defer resp.Body.Close()
	var decoded struct {
		OK     bool                          `json:"ok"`
		Bundle ContainerSyncCredentialBundle `json:"bundle"`
		Error  string                        `json:"error"`
		PathID string                        `json:"path_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return ContainerSyncCredentialBundle{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ContainerSyncCredentialBundle{}, errors.New(firstNonEmpty(decoded.Error, fmt.Sprintf("remote sync credential fetch failed with status %d", resp.StatusCode)))
	}
	if len(decoded.Bundle.Bundle) == 0 {
		return ContainerSyncCredentialBundle{}, fmt.Errorf("remote sync credential bundle was empty")
	}
	if strings.TrimSpace(decoded.Bundle.BundlePassword) == "" {
		return ContainerSyncCredentialBundle{}, fmt.Errorf("remote sync credential bundle password was empty")
	}
	return decoded.Bundle, nil
}

func currentRemoteSyncVaultPassword() string {
	return strings.TrimSpace(os.Getenv(remoteSyncVaultPasswordEnvKey))
}

func (s *Service) addPeerAuthHeaders(req *http.Request, peerSwarmID string) {
	if s == nil || s.swarmStore == nil || req == nil {
		return
	}
	peerSwarmID = strings.TrimSpace(peerSwarmID)
	if peerSwarmID == "" {
		return
	}
	node, ok, err := s.swarmStore.GetLocalNode()
	if err != nil || !ok || strings.TrimSpace(node.SwarmID) == "" {
		return
	}
	peer, ok, err := s.swarmStore.GetTrustedPeer(peerSwarmID)
	if err != nil || !ok {
		return
	}
	peerToken := strings.TrimSpace(peer.OutgoingPeerAuthToken)
	if peerToken == "" {
		return
	}
	req.Header.Set(peerAuthSwarmIDHeader, strings.TrimSpace(node.SwarmID))
	req.Header.Set(peerAuthTokenHeader, peerToken)
}

func (s *Service) managedLocalChildVaultKey(deploymentID string) (string, bool, error) {
	if s == nil || s.auth == nil {
		return "", false, nil
	}
	return s.auth.ManagedVaultKey(strings.TrimSpace(deploymentID))
}

func (s *Service) ensureManagedLocalChildVaultKey(deploymentID string) (string, error) {
	if s == nil || s.auth == nil {
		return "", fmt.Errorf("auth service is not configured")
	}
	deploymentID = strings.TrimSpace(deploymentID)
	if deploymentID == "" {
		return "", fmt.Errorf("deployment id is required")
	}
	if managedKey, ok, err := s.auth.ManagedVaultKey(deploymentID); err != nil {
		return "", err
	} else if ok {
		return managedKey, nil
	}
	managedKey, err := generateSecretToken(32)
	if err != nil {
		return "", err
	}
	if err := s.auth.PutManagedVaultKey(deploymentID, managedKey); err != nil {
		return "", err
	}
	return managedKey, nil
}

func (s *Service) UnlockManagedLocalChildVaults(ctx context.Context) error {
	if s == nil || s.store == nil {
		return nil
	}
	vaultStatus, err := s.auth.VaultStatus()
	if err != nil {
		return err
	}
	if !vaultStatus.Enabled || !vaultStatus.Unlocked {
		return nil
	}
	records, err := s.store.List(500)
	if err != nil {
		return err
	}
	var errs []error
	for _, record := range records {
		if err := s.unlockManagedLocalChildVaultIfNeeded(ctx, record); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", record.ID, err))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

func (s *Service) unlockManagedLocalChildVaultIfNeeded(ctx context.Context, record pebblestore.DeployContainerRecord) error {
	if s == nil || s.auth == nil {
		return nil
	}
	if strings.TrimSpace(record.AttachStatus) != "attached" {
		return nil
	}
	if strings.TrimSpace(record.Status) != "running" {
		return nil
	}
	if strings.TrimSpace(record.ChildSwarmID) == "" || strings.TrimSpace(record.ChildBackendURL) == "" {
		return nil
	}
	vaultStatus, err := s.auth.VaultStatus()
	if err != nil {
		return err
	}
	if !vaultStatus.Enabled || !vaultStatus.Unlocked {
		return nil
	}
	managedKey, ok, err := s.managedLocalChildVaultKey(record.ID)
	if err != nil || !ok {
		return err
	}
	if err := s.waitForChildReady(ctx, record.ChildBackendURL, 20*time.Second); err != nil {
		return err
	}
	childVaultStatus, err := s.fetchChildVaultStatus(ctx, record)
	if err != nil {
		return err
	}
	if !childVaultStatus.Enabled || childVaultStatus.Unlocked {
		if !childVaultStatus.Enabled {
			return fmt.Errorf("child vault is not enabled")
		}
		return nil
	}
	return s.unlockChildVault(ctx, record, managedKey)
}

func (s *Service) waitForChildReady(ctx context.Context, childBackendURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	endpoint := strings.TrimRight(strings.TrimSpace(childBackendURL), "/") + "/readyz"
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err == nil {
			resp, err := s.bootstrapHTTPClient("").Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for child readiness")
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (s *Service) fetchChildVaultStatus(ctx context.Context, record pebblestore.DeployContainerRecord) (auth.VaultStatus, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(record.ChildBackendURL), "/") + "/v1/vault"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return auth.VaultStatus{}, err
	}
	s.addPeerAuthHeaders(req, record.ChildSwarmID)
	resp, err := s.bootstrapHTTPClient("").Do(req)
	if err != nil {
		return auth.VaultStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return auth.VaultStatus{}, fmt.Errorf("child vault status failed with status %d", resp.StatusCode)
	}
	var status auth.VaultStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return auth.VaultStatus{}, err
	}
	return status, nil
}

func (s *Service) unlockChildVault(ctx context.Context, record pebblestore.DeployContainerRecord, managedKey string) error {
	endpoint := strings.TrimRight(strings.TrimSpace(record.ChildBackendURL), "/") + "/v1/vault/unlock"
	payload, err := json.Marshal(map[string]string{"password": strings.TrimSpace(managedKey)})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	s.addPeerAuthHeaders(req, record.ChildSwarmID)
	resp, err := s.bootstrapHTTPClient("").Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var decoded struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			return fmt.Errorf("child vault unlock failed with status %d", resp.StatusCode)
		}
		return errors.New(firstNonEmpty(strings.TrimSpace(decoded.Error), fmt.Sprintf("child vault unlock failed with status %d", resp.StatusCode)))
	}
	return nil
}

func (s *Service) fetchWorkspaceBootstrap(ctx context.Context, cfg startupconfig.FileConfig, status ContainerAttachState) ([]ContainerWorkspaceBootstrap, error) {
	socketPath := strings.TrimSpace(cfg.DeployContainer.LocalTransportSocketPath)
	endpoint := buildDeploymentWorkspaceBootstrapURL(firstNonEmpty(status.HostBackendURL, strings.TrimSpace(cfg.DeployContainer.HostAPIBaseURL)))
	if socketPath != "" {
		endpoint = buildDeploymentWorkspaceBootstrapURL(childLocalTransportBaseURL)
	}
	if endpoint == "" {
		return nil, fmt.Errorf("workspace bootstrap url is not configured")
	}
	payload, err := json.Marshal(ContainerWorkspaceBootstrapRequestInput{
		DeploymentID:    strings.TrimSpace(cfg.DeployContainer.DeploymentID),
		BootstrapSecret: strings.TrimSpace(cfg.DeployContainer.BootstrapSecret),
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client, err := s.bootstrapHTTPClientForEndpoint(socketPath, endpoint)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var decoded struct {
		OK         bool                          `json:"ok"`
		Workspaces []ContainerWorkspaceBootstrap `json:"workspaces"`
		Error      string                        `json:"error"`
		PathID     string                        `json:"path_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.New(firstNonEmpty(decoded.Error, fmt.Sprintf("workspace bootstrap fetch failed with status %d", resp.StatusCode)))
	}
	return append([]ContainerWorkspaceBootstrap(nil), decoded.Workspaces...), nil
}

func encodeEnvMultiline(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\n", `\n`)
}

func subtleTrim(value string) string {
	return strings.TrimSpace(value)
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

func containsTrimmedString(values []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

func (s *Service) resolveTargetGroupForCreate(hostState swarmruntime.LocalState, input ContainerCreateInput) (pebblestore.SwarmGroupRecord, error) {
	groupID := strings.TrimSpace(input.GroupID)
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
	groupName := firstNonEmpty(group.Name, strings.TrimSpace(input.GroupName), groupID)
	groupNetworkName := firstNonEmpty(group.NetworkName, strings.TrimSpace(input.GroupNetworkName), swarmruntime.SuggestedGroupNetworkName(groupName, groupID))
	if strings.TrimSpace(group.HostSwarmID) != "" && !strings.EqualFold(strings.TrimSpace(group.HostSwarmID), strings.TrimSpace(hostState.Node.SwarmID)) {
		return pebblestore.SwarmGroupRecord{}, fmt.Errorf("target group %q is hosted by another swarm", groupID)
	}
	if group.Name != groupName || group.NetworkName != groupNetworkName || group.HostSwarmID == "" {
		group.Name = groupName
		group.NetworkName = groupNetworkName
		group.HostSwarmID = strings.TrimSpace(hostState.Node.SwarmID)
		saved, err := s.swarmStore.PutGroup(group)
		if err != nil {
			return pebblestore.SwarmGroupRecord{}, err
		}
		group = saved
	}
	return group, nil
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

func resolveContainerReachableHostEndpoints(cfg startupconfig.FileConfig, state swarmruntime.LocalState, runtimeName string) (string, string, string, error) {
	host, err := resolveContainerReachableCallbackHost(cfg, state, runtimeName)
	if err != nil {
		return "", "", "", err
	}
	apiPort := canonicalAdvertisePort(cfg)
	log.Printf("deploy resolve callback endpoints runtime=%q host=%q api_port=%d desktop_port=%d desktop_assets_on_api=%t desktop_assets_on_desktop=%t", strings.TrimSpace(runtimeName), host, apiPort, cfg.DesktopPort, false, cfg.DesktopPort > 0)
	if err := verifyRuntimeCallbackEndpoint(cfg, host, apiPort); err != nil {
		return "", "", "", err
	}
	apiBaseURL := runtimeHTTPURL(host, apiPort)
	desktopBaseURL := ""
	if cfg.DesktopPort > 0 {
		desktopBaseURL = runtimeHTTPURL(host, cfg.DesktopPort)
	}
	log.Printf("deploy resolve callback endpoints runtime=%q api_base_url=%q desktop_base_url=%q", strings.TrimSpace(runtimeName), apiBaseURL, desktopBaseURL)
	return host, apiBaseURL, desktopBaseURL, nil
}

func childContainerReachableURLs(cfg startupconfig.FileConfig) (string, string, error) {
	hostPort := canonicalAdvertisePort(cfg)
	if hostPort < 1 || hostPort > 65535 {
		return "", "", fmt.Errorf("child startup config is missing a valid published host port")
	}
	// The child calls the master over the configured host API URL, but the master reaches
	// the child over the host-published container ports on the same machine.
	const hostLoopback = "127.0.0.1"
	backendURL := runtimeHTTPURL(hostLoopback, hostPort)
	desktopURL := ""
	if cfg.DesktopPort > 0 {
		desktopURL = runtimeHTTPURL(hostLoopback, hostPort+1)
	}
	log.Printf("deploy child reachable urls strategy=%q published_host_port=%d backend_url=%q desktop_url=%q desktop_assets_on_backend=%t desktop_assets_on_desktop=%t", "host-loopback-published-port", hostPort, backendURL, desktopURL, false, cfg.DesktopPort > 0)
	return backendURL, desktopURL, nil
}

func resolveContainerReachableCallbackHost(cfg startupconfig.FileConfig, state swarmruntime.LocalState, runtimeName string) (string, error) {
	runtimeName = strings.TrimSpace(strings.ToLower(runtimeName))
	bindHost := normalizeHostLiteral(cfg.Host)
	if bindHost == "" {
		return "", fmt.Errorf("local Add Swarm requires swarmd to bind a container-reachable host; current swarm.conf host is empty")
	}
	callbackHost := normalizeHostLiteral(cfg.AdvertiseHost)
	log.Printf("deploy resolve callback host runtime=%q bind_host=%q advertise_host=%q", runtimeName, bindHost, callbackHost)
	if isLocalOnlyHost(bindHost) {
		if callbackHost != "" {
			if isWildcardHost(callbackHost) {
				return "", fmt.Errorf("local Add Swarm cannot use advertise_host %q while swarmd host is %q; set advertise_host to the concrete address child swarms should use to reach the master, restart swarmd, then try again", callbackHost, bindHost)
			}
			if !isLocalOnlyHost(callbackHost) {
				log.Printf("deploy resolve callback host runtime=%q resolved_host=%q strategy=%q", runtimeName, callbackHost, "loopback-advertise_host")
				return callbackHost, nil
			}
		}
		if transportHost := firstContainerReachableTransportHost(state); transportHost != "" {
			log.Printf("deploy resolve callback host runtime=%q resolved_host=%q strategy=%q", runtimeName, transportHost, "loopback-transport-host")
			return transportHost, nil
		}
		return "", fmt.Errorf("local Add Swarm cannot derive a concrete callback host while swarmd host is %q; set advertise_host to a LAN or container-reachable address, restart swarmd, then try again", bindHost)
	}
	if isWildcardHost(bindHost) {
		if callbackHost == "" {
			return "", fmt.Errorf("local Add Swarm requires advertise_host when swarm.conf host is %q; set advertise_host to the LAN or container-reachable address that child containers should call", bindHost)
		}
		if isLocalOnlyHost(callbackHost) || isWildcardHost(callbackHost) {
			return "", fmt.Errorf("local Add Swarm cannot use advertise_host %q as a child callback target; set advertise_host to a concrete LAN or container-reachable host", callbackHost)
		}
		log.Printf("deploy resolve callback host runtime=%q resolved_host=%q strategy=%q", runtimeName, callbackHost, "wildcard-advertise_host")
		return callbackHost, nil
	}
	if callbackHost == "" {
		log.Printf("deploy resolve callback host runtime=%q resolved_host=%q strategy=%q", runtimeName, bindHost, "bind_host")
		return bindHost, nil
	}
	if isLocalOnlyHost(callbackHost) || isWildcardHost(callbackHost) {
		return "", fmt.Errorf("local Add Swarm cannot use advertise_host %q as a child callback target; set advertise_host to a concrete LAN or container-reachable host", callbackHost)
	}
	if bindIP := net.ParseIP(bindHost); bindIP != nil {
		if callbackIP := net.ParseIP(callbackHost); callbackIP != nil && !bindIP.Equal(callbackIP) {
			return "", fmt.Errorf("local Add Swarm requires advertise_host to match the swarmd bind address when host is a specific IP; current host=%q advertise_host=%q", bindHost, callbackHost)
		}
	}
	log.Printf("deploy resolve callback host runtime=%q resolved_host=%q strategy=%q", runtimeName, callbackHost, "advertise_host")
	return callbackHost, nil
}

func isContainerHostAlias(host string) bool {
	host = normalizeHostLiteral(host)
	switch strings.ToLower(host) {
	case "host.containers.internal", "host.docker.internal":
		return true
	default:
		return false
	}
}

func firstContainerReachableTransportHost(state swarmruntime.LocalState) string {
	for _, kind := range []string{startupconfig.NetworkModeLAN, startupconfig.NetworkModeTailscale} {
		if host := firstTransportHostForKind(state.Node.Transports, kind); host != "" {
			return host
		}
	}
	if host := normalizeCallbackTargetHost(state.Node.AdvertiseAddr); host != "" {
		return host
	}
	return ""
}

func firstTransportHostForKind(transports []swarmruntime.TransportSummary, kind string) string {
	kind = strings.TrimSpace(kind)
	for _, transport := range transports {
		if strings.TrimSpace(transport.Kind) != kind {
			continue
		}
		if host := normalizeCallbackTargetHost(transport.Primary); host != "" {
			return host
		}
		for _, value := range transport.All {
			if host := normalizeCallbackTargetHost(value); host != "" {
				return host
			}
		}
	}
	return ""
}

func normalizeCallbackTargetHost(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		value = parsed.Hostname()
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = normalizeHostLiteral(value)
	if value == "" || isLocalOnlyHost(value) || isWildcardHost(value) || isContainerHostAlias(value) {
		return ""
	}
	return value
}

func normalizeHostLiteral(host string) string {
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(host), "[]"))
}

func isLocalOnlyHost(host string) bool {
	host = normalizeHostLiteral(host)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if parsedIP := net.ParseIP(host); parsedIP != nil {
		return parsedIP.IsLoopback()
	}
	return false
}

func isWildcardHost(host string) bool {
	host = normalizeHostLiteral(host)
	if host == "" {
		return false
	}
	if parsedIP := net.ParseIP(host); parsedIP != nil {
		return parsedIP.IsUnspecified()
	}
	return false
}

func verifyRuntimeCallbackEndpoint(cfg startupconfig.FileConfig, host string, port int) error {
	verifyHost := strings.TrimSpace(host)
	if isContainerHostAlias(host) {
		if bindHost := normalizeHostLiteral(cfg.Host); bindHost != "" {
			verifyHost = bindHost
		}
		log.Printf("deploy verify callback endpoint alias=%q via_host=%q", strings.TrimSpace(host), verifyHost)
	}
	address := net.JoinHostPort(verifyHost, strconv.Itoa(port))
	log.Printf("deploy verify callback endpoint address=%q callback_host=%q", address, strings.TrimSpace(host))
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		callbackAddress := net.JoinHostPort(strings.TrimSpace(host), strconv.Itoa(port))
		return fmt.Errorf("local Add Swarm cannot reach the master callback endpoint %q at runtime; verified via %q on the host; update swarm.conf host/advertise_host so swarmd is actually serving there, restart swarmd, then try again: %w", callbackAddress, address, err)
	}
	_ = conn.Close()
	log.Printf("deploy verify callback endpoint success address=%q callback_host=%q", address, strings.TrimSpace(host))
	return nil
}

func canonicalAdvertisePort(cfg startupconfig.FileConfig) int {
	if cfg.AdvertisePort >= 1 && cfg.AdvertisePort <= 65535 {
		return cfg.AdvertisePort
	}
	return cfg.Port
}

func runtimeHTTPURL(host string, port int) string {
	return "http://" + net.JoinHostPort(strings.TrimSpace(host), strconv.Itoa(port))
}
