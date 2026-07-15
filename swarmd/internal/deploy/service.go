package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
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
	PathContainerSettings           = "deploy.container.settings.v1"
	PathContainerWorkspaceBootstrap = "deploy.container.workspace-bootstrap.v1"
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
	UserID              string                        `json:"user_id,omitempty"`
	AccountScopeID      string                        `json:"account_scope_id,omitempty"`
	Kind                string                        `json:"kind"`
	Name                string                        `json:"name"`
	Status              string                        `json:"status"`
	Runtime             string                        `json:"runtime"`
	GroupID             string                        `json:"group_id,omitempty"`
	GroupName           string                        `json:"group_name,omitempty"`
	GroupNetworkName    string                        `json:"group_network_name,omitempty"`
	ContainerName       string                        `json:"container_name,omitempty"`
	ContainerID         string                        `json:"container_id,omitempty"`
	HostAPIBaseURL      string                        `json:"host_api_base_url,omitempty"`
	HostSwarmID         string                        `json:"host_swarm_id,omitempty"`
	HostContainerID     string                        `json:"host_container_id,omitempty"`
	HostDisplayName     string                        `json:"host_display_name,omitempty"`
	HostBackendURL      string                        `json:"host_backend_url,omitempty"`
	HostDesktopURL      string                        `json:"host_desktop_url,omitempty"`
	AttachmentID        string                        `json:"attachment_id,omitempty"`
	BackendHostPort     int                           `json:"backend_host_port"`
	DesktopHostPort     int                           `json:"desktop_host_port"`
	Image               string                        `json:"image,omitempty"`
	AttachStatus        string                        `json:"attach_status,omitempty"`
	LastAttachError     string                        `json:"last_attach_error,omitempty"`
	BootstrapSecretSent bool                          `json:"bootstrap_secret_sent"`
	BypassPermissions   bool                          `json:"bypass_permissions,omitempty"`
	AlwaysOn            bool                          `json:"always_on,omitempty"`
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
	DeploymentID       string
	Name               string
	Runtime            string
	Image              string
	Mounts             []pebblestore.ContainerMount
	WorkspaceBootstrap []ContainerWorkspaceBootstrap
	ContainerPackages  ContainerPackageManifest
	GroupID            string
	GroupName          string
	GroupNetworkName   string
	BypassPermissions  bool
	AlwaysOn           bool
	UserID             string
	AccountScopeID     string
}

type ContainerActionInput struct {
	ID     string
	Action string
}

type ContainerSettingsUpdateInput struct {
	ID                string
	BypassPermissions *bool
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
}

type ContainerAttachFinalizeInput struct {
	DeploymentID             string                        `json:"deployment_id"`
	BootstrapSecret          string                        `json:"bootstrap_secret"`
	UserID                   string                        `json:"user_id,omitempty"`
	AccountScopeID           string                        `json:"account_scope_id,omitempty"`
	HostSwarmID              string                        `json:"host_swarm_id"`
	HostContainerID          string                        `json:"host_container_id,omitempty"`
	ChildSwarmID             string                        `json:"child_swarm_id,omitempty"`
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
	WorkspaceBootstrap       []ContainerWorkspaceBootstrap `json:"workspace_bootstrap,omitempty"`
}

type ContainerWorkspaceBootstrapRequestInput struct {
	DeploymentID    string `json:"deployment_id"`
	BootstrapSecret string `json:"bootstrap_secret"`
}

type ContainerAttachState struct {
	DeploymentID             string `json:"deployment_id"`
	AttachStatus             string `json:"attach_status"`
	UserID                   string `json:"user_id,omitempty"`
	AccountScopeID           string `json:"account_scope_id,omitempty"`
	ChildSwarmID             string `json:"child_swarm_id,omitempty"`
	ChildDisplayName         string `json:"child_display_name,omitempty"`
	ChildBackendURL          string `json:"child_backend_url,omitempty"`
	ChildDesktopURL          string `json:"child_desktop_url,omitempty"`
	ChildFingerprint         string `json:"child_fingerprint,omitempty"`
	HostSwarmID              string `json:"host_swarm_id,omitempty"`
	HostContainerID          string `json:"host_container_id,omitempty"`
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
	BootstrapSecretExpires   int64  `json:"bootstrap_secret_expires_at,omitempty"`
	LastError                string `json:"last_error,omitempty"`
	DecidedAt                int64  `json:"decided_at,omitempty"`
	UpdatedAt                int64  `json:"updated_at"`
}

type ContainerWorkspaceBootstrapDirectory = pebblestore.DeployContainerWorkspaceBootstrapDirectory
type ContainerWorkspaceBootstrap = pebblestore.DeployContainerWorkspaceBootstrap

type Service struct {
	store       *pebblestore.DeployContainerStore
	swarmStore  *pebblestore.SwarmStore
	topology    *pebblestore.TopologyStore
	workspace   *workspaceruntime.Service
}

func NewService(store *pebblestore.DeployContainerStore, swarms *swarmruntime.Service, swarmStore *pebblestore.SwarmStore, args ...any) *Service {
	service := &Service{store: store, swarmStore: swarmStore}
	_ = swarms
	for _, arg := range args {
		switch value := arg.(type) {
		case *workspaceruntime.Service:
			service.workspace = value
		case *pebblestore.TopologyStore:
			service.topology = value
		}
	}
	return service
}

func (s *Service) RuntimeStatus(context.Context) (ContainerRuntimeStatus, error) {
	return ContainerRuntimeStatus{}, fmt.Errorf("deploy container runtime is no longer supported")
}

func (s *Service) List(ctx context.Context) ([]ContainerDeployment, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("deploy container service is not configured")
	}
	records, err := s.listRecordsForContext(ctx, 500)
	if err != nil {
		return nil, err
	}
	out := make([]ContainerDeployment, 0, len(records))
	for _, record := range records {
		if err := s.syncCanonicalFields(&record); err != nil {
			return nil, err
		}
		out = append(out, mapContainerRecord(record))
	}
	return out, nil
}

func (s *Service) Create(context.Context, ContainerCreateInput) (ContainerDeployment, error) {
	return ContainerDeployment{}, fmt.Errorf("deploy container creation is no longer supported")
}

func (s *Service) Act(context.Context, ContainerActionInput) (ContainerDeployment, error) {
	return ContainerDeployment{}, fmt.Errorf("deploy container actions are no longer supported")
}

func (s *Service) UpdateSettings(ctx context.Context, input ContainerSettingsUpdateInput) (ContainerDeployment, error) {
	if s == nil || s.store == nil {
		return ContainerDeployment{}, fmt.Errorf("deploy container service is not configured")
	}
	record, ok, err := s.getRecordForContext(ctx, input.ID)
	if err != nil {
		return ContainerDeployment{}, err
	}
	if !ok {
		return ContainerDeployment{}, fmt.Errorf("deploy container not found")
	}
	if input.BypassPermissions != nil {
		record.BypassPermissions = *input.BypassPermissions
	}
	saved, err := s.persistRecordForContext(ctx, record)
	if err != nil {
		return ContainerDeployment{}, err
	}
	if err := s.syncCanonicalDeploymentState(saved); err != nil {
		return ContainerDeployment{}, err
	}
	return mapContainerRecord(saved), nil
}

func (s *Service) Delete(ctx context.Context, deploymentIDs []string) (DeleteResult, error) {
	if s == nil || s.store == nil {
		return DeleteResult{}, fmt.Errorf("deploy container service is not configured")
	}
	ids := normalizeDeploymentDeleteIDs(deploymentIDs)
	if len(ids) == 0 {
		return DeleteResult{}, errors.New("at least one deploy container id is required")
	}
	items := make([]DeleteItemResult, len(ids))
	for index, deploymentID := range ids {
		items[index] = s.deleteDeployment(ctx, deploymentID)
	}
	result := DeleteResult{Deleted: make([]string, 0, len(items)), Items: items}
	for _, item := range items {
		if item.Deleted {
			result.Count++
			result.Deleted = append(result.Deleted, item.ID)
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

func (s *Service) deleteDeployment(ctx context.Context, deploymentID string) DeleteItemResult {
	record, ok, err := s.getRecordForContext(ctx, deploymentID)
	if err != nil {
		return DeleteItemResult{ID: strings.TrimSpace(deploymentID), Error: err.Error()}
	}
	if !ok {
		return DeleteItemResult{ID: strings.TrimSpace(deploymentID), Error: "deploy container not found"}
	}
	item := DeleteItemResult{ID: record.ID, Name: record.Name, ContainerName: record.ContainerName, ChildSwarmID: record.ChildSwarmID, ChildDisplayName: firstNonEmpty(record.ChildDisplayName, record.ChildSwarmID)}
	principal, ok := principalFromContext(ctx)
	if !ok {
		item.Error = identity.ErrPrincipalRequired.Error()
		return item
	}
	if err := s.store.DeleteForAccount(principal.AccountScopeID, record.ID); err != nil {
		item.Error = err.Error()
		return item
	}
	if err := s.deleteCanonicalDeploymentState(record); err != nil {
		item.Error = err.Error()
		return item
	}
	item.Deleted = true
	item.RemovedDeployment = true
	return item
}

func (s *Service) WorkspaceBootstrap(_ context.Context, input ContainerWorkspaceBootstrapRequestInput) ([]ContainerWorkspaceBootstrap, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("deploy container service is not configured")
	}
	record, ok, err := s.store.Get(input.DeploymentID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("deploy container not found")
	}
	if subtleTrim(record.BootstrapSecret) == "" || subtleTrim(record.BootstrapSecret) != subtleTrim(input.BootstrapSecret) {
		return nil, fmt.Errorf("bootstrap secret mismatch")
	}
	return append([]ContainerWorkspaceBootstrap(nil), record.WorkspaceBootstrap...), nil
}

func (s *Service) FinalizeLocalBootstrap(ctx context.Context, cfg startupconfig.FileConfig, state swarmruntime.LocalState, status ContainerAttachState, input ContainerAttachFinalizeInput) error {
	if s == nil || s.swarmStore == nil {
		return fmt.Errorf("deploy container service is not configured")
	}
	principalUserID := ""
	principalAccountScopeID := ""
	if principal, ok := identity.PrincipalFromContext(ctx); ok {
		principalUserID = strings.TrimSpace(principal.UserID)
		principalAccountScopeID = strings.TrimSpace(principal.AccountScopeID)
	}
	userID, accountScopeID, err := requirePairingIdentity(firstNonEmpty(input.UserID, status.UserID, principalUserID), firstNonEmpty(input.AccountScopeID, status.AccountScopeID, principalAccountScopeID))
	if err != nil {
		return err
	}
	hostSwarmID := firstNonEmpty(input.HostSwarmID, status.HostSwarmID, cfg.ParentSwarmID)
	if hostSwarmID == "" {
		return fmt.Errorf("approved attach is missing host swarm id")
	}
	pairing, _, err := s.swarmStore.GetLocalPairing()
	if err != nil {
		return err
	}
	pairing.PairingState = startupconfig.PairingStatePaired
	pairing.ParentSwarmID = hostSwarmID
	pairing.UserID = userID
	pairing.AccountScopeID = accountScopeID
	pairing.LastDecision = "approved"
	pairing.LastDecisionReason = ""
	pairing.LastUpdatedByRole = "child"
	if _, err := s.swarmStore.PutLocalPairing(pairing); err != nil {
		return err
	}
	if _, err := s.swarmStore.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{
		SwarmID:               hostSwarmID,
		Name:                  firstNonEmpty(input.HostDisplayName, status.HostDisplayName, "Primary"),
		Role:                  "primary",
		PublicKey:             firstNonEmpty(input.HostPublicKey, status.HostPublicKey),
		Fingerprint:           firstNonEmpty(input.HostFingerprint, status.HostFingerprint),
		Relationship:          swarmruntime.RelationshipParent,
		OutgoingPeerAuthToken: strings.TrimSpace(input.ChildToHostPeerAuthToken),
		IncomingPeerAuthHash:  swarmruntime.HashPeerAuthToken(input.HostToChildPeerAuthToken),
		ApprovedAt:            time.Now().UnixMilli(),
	}); err != nil {
		return err
	}
	if cfg.DeployContainer.HostDriven {
		if err := s.ensureChildContainerPlacementForBootstrap(accountScopeID, userID, state, status, input); err != nil {
			return err
		}
	} else {
		if err := s.ensureChildSelfPlacementForBootstrap(accountScopeID, userID, state); err != nil {
			return err
		}
	}
	if len(input.WorkspaceBootstrap) == 0 {
		return nil
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: userID, AccountScopeID: accountScopeID, AccountScopeSource: identity.AccountScopeSourceServerState}
	return s.applyBootstrapWorkspaces(principal, cfg, state, status, input.WorkspaceBootstrap)
}

func (s *Service) applyBootstrapWorkspaces(principal identity.Principal, cfg startupconfig.FileConfig, state swarmruntime.LocalState, status ContainerAttachState, items []ContainerWorkspaceBootstrap) error {
	if !principal.Valid() {
		return identity.ErrPrincipalRequired
	}
	deploymentID := strings.TrimSpace(cfg.DeployContainer.DeploymentID)
	if deploymentID == "" {
		deploymentID = strings.TrimSpace(status.DeploymentID)
	}
	for _, item := range items {
		workspacePath := strings.TrimSpace(item.TargetWorkspacePath)
		if workspacePath == "" {
			continue
		}
		if strings.TrimSpace(item.SourceWorkspaceID) == "" {
			return fmt.Errorf("deploy bootstrap workspace binding for %q is missing source workspace id", strings.TrimSpace(item.SourceWorkspacePath))
		}
		if item.SourceWorkspaceGeneration <= 0 {
			return fmt.Errorf("deploy bootstrap workspace binding for %q is missing source workspace generation", strings.TrimSpace(item.SourceWorkspacePath))
		}
		if s.workspace != nil {
			if _, err := s.workspace.AddForPrincipal(principal, workspacePath, strings.TrimSpace(item.SourceWorkspaceName), strings.TrimSpace(item.ThemeID), false); err != nil {
				return err
			}
			for _, directory := range item.Directories {
				if targetPath := strings.TrimSpace(directory.TargetPath); targetPath != "" {
					if _, err := s.workspace.AddDirectoryForPrincipal(principal, workspacePath, targetPath); err != nil {
						return err
					}
				}
			}
			if item.MakeCurrent {
				if _, err := s.workspace.SelectForPrincipal(principal, workspacePath); err != nil {
					return err
				}
			}
		}
		if s.topology == nil {
			continue
		}
		runtimeSwarmID := firstNonEmpty(inputChildSwarmID(state, status), strings.TrimSpace(state.Node.SwarmID))
		placement, ok, err := s.topology.GetRuntimePlacementForAccount(principal.AccountScopeID, runtimeSwarmID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("deploy bootstrap workspace binding requires runtime placement for %s", runtimeSwarmID)
		}
		if _, err := pebblestore.UpsertTopologyWorkspaceBindingForAccount(s.topology, principal.AccountScopeID, pebblestore.TopologyWorkspaceBindingRecord{
			BindingID:                       pebblestore.CanonicalTopologyWorkspaceBindingID(deploymentID, strings.TrimSpace(item.SourceWorkspacePath)),
			UserID:                          principal.UserID,
			AccountScopeID:                  principal.AccountScopeID,
			SourceWorkspaceID:               strings.TrimSpace(item.SourceWorkspaceID),
			SourceWorkspaceGeneration:       item.SourceWorkspaceGeneration,
			SourceWorkspacePath:             strings.TrimSpace(item.SourceWorkspacePath),
			SourceWorkspaceName:             strings.TrimSpace(item.SourceWorkspaceName),
			DestinationRuntimeSwarmID:       runtimeSwarmID,
			DestinationAuthorityHostSwarmID: placement.AuthorityHostSwarmID,
			DestinationHostSwarmID:          placement.AuthorityHostSwarmID,
			DestinationContainerID:          placement.AuthorityContainerID,
			DestinationRuntimeKind:          placement.RuntimeKind,
			DestinationWorkspacePath:        workspacePath,
			PlacementGeneration:             placement.PlacementGeneration,
			BindingGeneration:               1,
			State:                           pebblestore.TopologyWorkspaceBindingStateBound,
			AccessMode:                      pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
			MaterializationKind:             pebblestore.TopologyWorkspaceBindingMaterializationSource,
			AttestedByHostSwarmID:           placement.AuthorityHostSwarmID,
			AttestedAt:                      time.Now().UnixMilli(),
			ReplicationMode:                 strings.TrimSpace(item.ReplicationMode),
			Writable:                        item.Writable,
			Sync:                            item.Sync,
			LegacyTargetKind:                "local",
		}); err != nil {
			return err
		}
	}
	return nil
}

func inputChildSwarmID(state swarmruntime.LocalState, status ContainerAttachState) string {
	return firstNonEmpty(status.ChildSwarmID, state.Node.SwarmID)
}

func (s *Service) listRecordsForContext(ctx context.Context, limit int) ([]pebblestore.DeployContainerRecord, error) {
	if principal, ok := principalFromContext(ctx); ok {
		return s.store.ListForAccount(principal.AccountScopeID, limit)
	}
	return s.store.List(limit)
}

func (s *Service) getRecordForContext(ctx context.Context, deploymentID string) (pebblestore.DeployContainerRecord, bool, error) {
	if principal, ok := principalFromContext(ctx); ok {
		return s.store.GetForAccount(principal.AccountScopeID, deploymentID)
	}
	return s.store.Get(deploymentID)
}

func (s *Service) persistRecordForContext(ctx context.Context, record pebblestore.DeployContainerRecord) (pebblestore.DeployContainerRecord, error) {
	if principal, ok := principalFromContext(ctx); ok {
		if strings.TrimSpace(record.UserID) == "" {
			record.UserID = principal.UserID
		}
		if strings.TrimSpace(record.AccountScopeID) == "" {
			record.AccountScopeID = principal.AccountScopeID
		}
		if err := s.syncCanonicalFields(&record); err != nil {
			return pebblestore.DeployContainerRecord{}, err
		}
		return s.store.PutForAccount(record, record.UserID, record.AccountScopeID)
	}
	return s.persistRecord(record)
}

func (s *Service) persistRecord(record pebblestore.DeployContainerRecord) (pebblestore.DeployContainerRecord, error) {
	if err := s.syncCanonicalFields(&record); err != nil {
		return pebblestore.DeployContainerRecord{}, err
	}
	return s.store.Put(record)
}

func (s *Service) syncCanonicalFields(record *pebblestore.DeployContainerRecord) error {
	if s == nil || record == nil {
		return nil
	}
	hostSwarmID := firstNonEmpty(record.HostSwarmID, s.localSwarmID())
	if strings.TrimSpace(record.HostSwarmID) == "" {
		record.HostSwarmID = hostSwarmID
	}
	if strings.TrimSpace(record.HostContainerID) == "" {
		record.HostContainerID = pebblestore.CanonicalTopologyHostContainerID(hostSwarmID, firstNonEmpty(record.ContainerID, record.ContainerName, record.ID))
	}
	if strings.TrimSpace(record.AttachmentID) == "" && strings.TrimSpace(record.HostContainerID) != "" && strings.TrimSpace(record.ChildSwarmID) != "" {
		record.AttachmentID = pebblestore.CanonicalTopologyAttachmentID(record.HostContainerID, record.ChildSwarmID)
	}
	return nil
}

func (s *Service) localSwarmID() string {
	if s == nil || s.swarmStore == nil {
		return ""
	}
	node, ok, err := s.swarmStore.GetLocalNode()
	if err != nil || !ok {
		return ""
	}
	return strings.TrimSpace(node.SwarmID)
}

func mapContainerRecord(record pebblestore.DeployContainerRecord) ContainerDeployment {
	return ContainerDeployment{
		ID: record.ID, UserID: record.UserID, AccountScopeID: record.AccountScopeID, Kind: record.Kind,
		Name: record.Name, Status: record.Status, Runtime: record.Runtime, GroupID: record.GroupID,
		GroupName: record.GroupName, GroupNetworkName: record.GroupNetworkName, ContainerName: record.ContainerName,
		ContainerID: record.ContainerID, HostAPIBaseURL: record.HostAPIBaseURL, HostSwarmID: record.HostSwarmID,
		HostContainerID: record.HostContainerID, HostDisplayName: record.HostDisplayName, HostBackendURL: record.HostBackendURL,
		HostDesktopURL: record.HostDesktopURL, AttachmentID: record.AttachmentID, BackendHostPort: record.BackendHostPort,
		DesktopHostPort: record.DesktopHostPort, Image: record.Image, AttachStatus: record.AttachStatus,
		LastAttachError: record.LastAttachError, BootstrapSecretSent: record.BootstrapSecretSent,
		BypassPermissions: record.BypassPermissions, AlwaysOn: record.AlwaysOn, ChildSwarmID: record.ChildSwarmID,
		ChildDisplayName: record.ChildDisplayName, ChildBackendURL: record.ChildBackendURL, ChildDesktopURL: record.ChildDesktopURL,
		WorkspaceBootstrap: append([]ContainerWorkspaceBootstrap(nil), record.WorkspaceBootstrap...),
		ContainerPackages: mapStoredContainerPackageManifest(record.ContainerPackages), CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

func mapStoredContainerPackageManifest(input pebblestore.ContainerPackageManifestRecord) ContainerPackageManifest {
	packages := make([]ContainerPackageSelection, 0, len(input.Packages))
	for _, item := range input.Packages {
		packages = append(packages, ContainerPackageSelection{Name: strings.TrimSpace(item.Name), Source: strings.TrimSpace(item.Source), Reason: strings.TrimSpace(item.Reason)})
	}
	return ContainerPackageManifest{BaseImage: strings.TrimSpace(input.BaseImage), PackageManager: strings.TrimSpace(input.PackageManager), Packages: packages}
}

func normalizeDeploymentDeleteIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
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

func principalFromContext(ctx context.Context) (identity.Principal, bool) {
	principal, ok := identity.PrincipalFromContext(ctx)
	return principal, ok && principal.Valid()
}

func requirePairingIdentity(userID, accountScopeID string) (string, string, error) {
	userID = strings.TrimSpace(userID)
	accountScopeID = strings.TrimSpace(accountScopeID)
	if userID == "" || accountScopeID == "" {
		return "", "", fmt.Errorf("local pairing user id and account scope id are required")
	}
	return userID, accountScopeID, nil
}

func subtleTrim(value string) string { return strings.TrimSpace(value) }

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
