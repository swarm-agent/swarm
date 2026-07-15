package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

const (
	PathContainerRuntime  = "deploy.container.runtime.v1"
	PathContainerList     = "deploy.container.list.v1"
	PathContainerCreate   = "deploy.container.create.v1"
	PathContainerAction   = "deploy.container.action.v1"
	PathContainerDelete   = "deploy.container.delete.v1"
	PathContainerSettings = "deploy.container.settings.v1"
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

type ContainerWorkspaceBootstrapDirectory = pebblestore.DeployContainerWorkspaceBootstrapDirectory
type ContainerWorkspaceBootstrap = pebblestore.DeployContainerWorkspaceBootstrap

type Service struct {
	store      *pebblestore.DeployContainerStore
	swarmStore *pebblestore.SwarmStore
	topology   *pebblestore.TopologyStore
}

func NewService(store *pebblestore.DeployContainerStore, swarms *swarmruntime.Service, swarmStore *pebblestore.SwarmStore, args ...any) *Service {
	service := &Service{store: store, swarmStore: swarmStore}
	_ = swarms
	for _, arg := range args {
		if value, ok := arg.(*pebblestore.TopologyStore); ok {
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
		ContainerPackages:  mapStoredContainerPackageManifest(record.ContainerPackages), CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
