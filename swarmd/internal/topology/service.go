package topology

import (
	"fmt"
	"sort"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const SnapshotVersion = pebblestore.TopologySnapshotVersion

type Service struct {
	topologyStore   *pebblestore.TopologyStore
	swarmStore      *pebblestore.SwarmStore
	swarmNodes      *pebblestore.SwarmNodeStore
	localContainers *pebblestore.SwarmLocalContainerStore
	deployments     *pebblestore.DeployContainerStore
	remoteDeploys   *pebblestore.RemoteDeploySessionStore
	sessionRoutes   *pebblestore.SessionRouteStore
	workspaceStore  *pebblestore.WorkspaceStore
}

func NewService(
	topologyStore *pebblestore.TopologyStore,
	swarmStore *pebblestore.SwarmStore,
	swarmNodes *pebblestore.SwarmNodeStore,
	localContainers *pebblestore.SwarmLocalContainerStore,
	deployments *pebblestore.DeployContainerStore,
	remoteDeploys *pebblestore.RemoteDeploySessionStore,
	sessionRoutes *pebblestore.SessionRouteStore,
	workspaceStore *pebblestore.WorkspaceStore,
) *Service {
	return &Service{
		topologyStore:   topologyStore,
		swarmStore:      swarmStore,
		swarmNodes:      swarmNodes,
		localContainers: localContainers,
		deployments:     deployments,
		remoteDeploys:   remoteDeploys,
		sessionRoutes:   sessionRoutes,
		workspaceStore:  workspaceStore,
	}
}

func (s *Service) Rebuild() (pebblestore.TopologyMigrationStatusRecord, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyMigrationStatusRecord{}, fmt.Errorf("topology service is not configured")
	}
	snapshot, err := s.buildSnapshot()
	if err != nil {
		return pebblestore.TopologyMigrationStatusRecord{}, err
	}
	if err := s.topologyStore.ReplaceSnapshot(snapshot); err != nil {
		return pebblestore.TopologyMigrationStatusRecord{}, err
	}
	return snapshot.MigrationStatus, nil
}

func (s *Service) EnsureSnapshot() (pebblestore.TopologyMigrationStatusRecord, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyMigrationStatusRecord{}, fmt.Errorf("topology service is not configured")
	}
	_, ok, err := s.topologyStore.GetMigrationStatus(pebblestore.DefaultTopologyMigrationStatusID)
	if err != nil {
		return pebblestore.TopologyMigrationStatusRecord{}, err
	}
	if ok {
		return s.topologyStore.RefreshMigrationStatus()
	}
	return s.Rebuild()
}

func (s *Service) RefreshMigrationStatus() (pebblestore.TopologyMigrationStatusRecord, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyMigrationStatusRecord{}, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.RefreshMigrationStatus()
}

func (s *Service) Snapshot() (pebblestore.TopologySnapshot, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologySnapshot{}, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.Snapshot()
}

func (s *Service) SnapshotForAccount(accountScopeID string) (pebblestore.TopologySnapshot, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologySnapshot{}, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.SnapshotForAccount(accountScopeID)
}

func (s *Service) ReplaceSnapshotForAccount(accountScopeID string, snapshot pebblestore.TopologySnapshot) error {
	if s == nil || s.topologyStore == nil {
		return fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.ReplaceSnapshotForAccount(accountScopeID, snapshot)
}

func (s *Service) ListRuntimes(limit int) ([]pebblestore.TopologyRuntimeRecord, error) {
	if s == nil || s.topologyStore == nil {
		return nil, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.ListRuntimes(limit)
}

func (s *Service) ListRuntimesForAccount(accountScopeID string, limit int) ([]pebblestore.TopologyRuntimeRecord, error) {
	if s == nil || s.topologyStore == nil {
		return nil, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.ListRuntimesForAccount(accountScopeID, limit)
}

func (s *Service) ListRuntimePlacementsForAccount(accountScopeID string, limit int) ([]pebblestore.TopologyRuntimePlacementRecord, error) {
	if s == nil || s.topologyStore == nil {
		return nil, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.ListRuntimePlacementsForAccount(accountScopeID, limit)
}

func (s *Service) GetRuntimePlacementForAccount(accountScopeID, runtimeSwarmID string) (pebblestore.TopologyRuntimePlacementRecord, bool, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyRuntimePlacementRecord{}, false, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.GetRuntimePlacementForAccount(accountScopeID, runtimeSwarmID)
}

func (s *Service) PutRuntimePlacementForAccount(accountScopeID string, record pebblestore.TopologyRuntimePlacementRecord) (pebblestore.TopologyRuntimePlacementRecord, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyRuntimePlacementRecord{}, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.PutRuntimePlacementForAccount(accountScopeID, record)
}

func (s *Service) EnsureLocalSelfPlacementForAccount(accountScopeID string) (pebblestore.TopologyRuntimePlacementRecord, error) {
	return s.EnsureLocalSelfPlacementForPrincipal(accountScopeID, accountScopeID)
}

func (s *Service) EnsureLocalSelfPlacementForPrincipal(accountScopeID, userID string) (pebblestore.TopologyRuntimePlacementRecord, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyRuntimePlacementRecord{}, fmt.Errorf("topology service is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return pebblestore.TopologyRuntimePlacementRecord{}, fmt.Errorf("account scope id is required")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return pebblestore.TopologyRuntimePlacementRecord{}, fmt.Errorf("user id is required")
	}
	localSwarmID, localNode, err := s.loadLocalNode()
	if err != nil {
		return pebblestore.TopologyRuntimePlacementRecord{}, err
	}
	if strings.TrimSpace(localSwarmID) == "" {
		return pebblestore.TopologyRuntimePlacementRecord{}, fmt.Errorf("local swarm id is required for self placement")
	}
	if _, err := s.topologyStore.PutRuntimeForAccount(accountScopeID, pebblestore.TopologyRuntimeRecord{
		SwarmID:         localSwarmID,
		UserID:          userID,
		AccountScopeID:  accountScopeID,
		Name:            firstNonEmpty(localNode.Name, localSwarmID),
		Role:            localNode.Role,
		Relationship:    "self",
		Status:          "online",
		ObservedSources: []string{"swarm_local_node"},
		CreatedAt:       localNode.CreatedAt,
		UpdatedAt:       localNode.UpdatedAt,
	}); err != nil {
		return pebblestore.TopologyRuntimePlacementRecord{}, err
	}
	record := pebblestore.TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       localSwarmID,
		AccountScopeID:       accountScopeID,
		AuthorityHostSwarmID: localSwarmID,
		RuntimeKind:          pebblestore.TopologyRuntimeKindHost,
		State:                pebblestore.TopologyRuntimePlacementStateActive,
	}
	record.PlacementGeneration = 1
	return s.topologyStore.PutRuntimePlacementForAccount(accountScopeID, record)
}

func (s *Service) requireExistingLocalSelfPlacementForAccount(accountScopeID string) (pebblestore.TopologyRuntimePlacementRecord, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyRuntimePlacementRecord{}, fmt.Errorf("topology service is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return pebblestore.TopologyRuntimePlacementRecord{}, fmt.Errorf("account scope id is required")
	}
	localSwarmID, _, err := s.loadLocalNode()
	if err != nil {
		return pebblestore.TopologyRuntimePlacementRecord{}, err
	}
	if strings.TrimSpace(localSwarmID) == "" {
		return pebblestore.TopologyRuntimePlacementRecord{}, fmt.Errorf("local swarm id is required for self placement")
	}
	placement, ok, err := s.topologyStore.GetRuntimePlacementForAccount(accountScopeID, localSwarmID)
	if err != nil {
		return pebblestore.TopologyRuntimePlacementRecord{}, err
	}
	if !ok {
		return pebblestore.TopologyRuntimePlacementRecord{}, fmt.Errorf("local self runtime placement is required for workspace binding")
	}
	if !strings.EqualFold(placement.RuntimeSwarmID, localSwarmID) || placement.RuntimeKind != pebblestore.TopologyRuntimeKindHost || !strings.EqualFold(placement.AuthorityHostSwarmID, localSwarmID) || placement.AuthorityContainerID != "" || placement.PlacementGeneration <= 0 || placement.State != pebblestore.TopologyRuntimePlacementStateActive {
		return pebblestore.TopologyRuntimePlacementRecord{}, fmt.Errorf("local self placement is invalid for workspace binding")
	}
	return placement, nil
}

func (s *Service) EnsureLocalWorkspaceSelfBindingForPrincipal(accountScopeID, userID string, workspaceEntry pebblestore.WorkspaceEntry) (pebblestore.TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyWorkspaceBindingRecord{}, fmt.Errorf("topology service is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return pebblestore.TopologyWorkspaceBindingRecord{}, fmt.Errorf("account scope id is required")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return pebblestore.TopologyWorkspaceBindingRecord{}, fmt.Errorf("user id is required")
	}
	workspaceEntry = pebblestore.NormalizeWorkspaceEntryForAccount(accountScopeID, workspaceEntry)
	if strings.TrimSpace(workspaceEntry.WorkspaceID) == "" {
		return pebblestore.TopologyWorkspaceBindingRecord{}, fmt.Errorf("workspace id is required")
	}
	if workspaceEntry.WorkspaceGeneration <= 0 {
		return pebblestore.TopologyWorkspaceBindingRecord{}, fmt.Errorf("workspace generation is required")
	}
	if strings.TrimSpace(workspaceEntry.Path) == "" {
		return pebblestore.TopologyWorkspaceBindingRecord{}, fmt.Errorf("workspace path is required")
	}
	placement, err := s.requireExistingLocalSelfPlacementForAccount(accountScopeID)
	if err != nil {
		return pebblestore.TopologyWorkspaceBindingRecord{}, err
	}
	binding := pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       pebblestore.DeterministicTopologyWorkspaceSelfBindingID(accountScopeID, workspaceEntry.WorkspaceID, placement.RuntimeSwarmID),
		UserID:                          userID,
		AccountScopeID:                  accountScopeID,
		SourceWorkspaceID:               workspaceEntry.WorkspaceID,
		SourceWorkspaceGeneration:       workspaceEntry.WorkspaceGeneration,
		SourceWorkspacePath:             workspaceEntry.Path,
		SourceWorkspaceName:             workspaceEntry.Name,
		DestinationRuntimeSwarmID:       placement.RuntimeSwarmID,
		DestinationAuthorityHostSwarmID: placement.AuthorityHostSwarmID,
		DestinationRuntimeKind:          placement.RuntimeKind,
		DestinationHostSwarmID:          placement.AuthorityHostSwarmID,
		DestinationWorkspacePath:        workspaceEntry.Path,
		PlacementGeneration:             placement.PlacementGeneration,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AccessMode:                      pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind:             pebblestore.TopologyWorkspaceBindingMaterializationSource,
		AttestedByHostSwarmID:           placement.AuthorityHostSwarmID,
		AttestedAt:                      time.Now().UnixMilli(),
		Writable:                        true,
	}
	return s.topologyStore.EnsureLocalWorkspaceSelfBindingForAccount(accountScopeID, binding)
}

func (s *Service) ListHostContainersByHost(hostSwarmID string, limit int) ([]pebblestore.TopologyHostContainerRecord, error) {
	if s == nil || s.topologyStore == nil {
		return nil, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.ListHostContainersByHost(hostSwarmID, limit)
}

func (s *Service) ListHostContainersByHostForAccount(accountScopeID, hostSwarmID string, limit int) ([]pebblestore.TopologyHostContainerRecord, error) {
	if s == nil || s.topologyStore == nil {
		return nil, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.ListHostContainersByHostForAccount(accountScopeID, hostSwarmID, limit)
}

func (s *Service) GetHostContainer(hostContainerID string) (pebblestore.TopologyHostContainerRecord, bool, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyHostContainerRecord{}, false, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.GetHostContainer(hostContainerID)
}

func (s *Service) GetAttachment(attachmentID string) (pebblestore.TopologyAttachmentRecord, bool, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyAttachmentRecord{}, false, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.GetAttachment(attachmentID)
}

func (s *Service) FindHostContainer(hostSwarmID string, refs ...string) (pebblestore.TopologyHostContainerRecord, bool, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyHostContainerRecord{}, false, fmt.Errorf("topology service is not configured")
	}
	return pebblestore.FindTopologyHostContainerByRefs(s.topologyStore, hostSwarmID, refs...)
}

func (s *Service) PutRuntimeForAccount(accountScopeID string, record pebblestore.TopologyRuntimeRecord) (pebblestore.TopologyRuntimeRecord, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyRuntimeRecord{}, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.PutRuntimeForAccount(accountScopeID, record)
}

func (s *Service) UpsertRuntime(record pebblestore.TopologyRuntimeRecord) error {
	if s == nil || s.topologyStore == nil {
		return fmt.Errorf("topology service is not configured")
	}
	if strings.TrimSpace(record.AccountScopeID) != "" {
		return pebblestore.UpsertTopologyRuntimeRecordForAccount(s.topologyStore, record.AccountScopeID, record)
	}
	return pebblestore.UpsertTopologyRuntimeRecord(s.topologyStore, record)
}

func (s *Service) UpsertHostContainer(record pebblestore.TopologyHostContainerRecord) error {
	if s == nil || s.topologyStore == nil {
		return fmt.Errorf("topology service is not configured")
	}
	if strings.TrimSpace(record.AccountScopeID) != "" {
		return pebblestore.UpsertTopologyHostContainerForAccount(s.topologyStore, record.AccountScopeID, record)
	}
	return pebblestore.UpsertTopologyHostContainer(s.topologyStore, record)
}

func (s *Service) UpsertAttachment(record pebblestore.TopologyAttachmentRecord) error {
	if s == nil || s.topologyStore == nil {
		return fmt.Errorf("topology service is not configured")
	}
	if strings.TrimSpace(record.AccountScopeID) != "" {
		return pebblestore.UpsertTopologyAttachmentForAccount(s.topologyStore, record.AccountScopeID, record)
	}
	return pebblestore.UpsertTopologyAttachment(s.topologyStore, record)
}

func (s *Service) UpsertWorkspaceBinding(record pebblestore.TopologyWorkspaceBindingRecord) (pebblestore.TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyWorkspaceBindingRecord{}, fmt.Errorf("topology service is not configured")
	}
	if strings.TrimSpace(record.AccountScopeID) != "" {
		return pebblestore.UpsertTopologyWorkspaceBindingForAccount(s.topologyStore, record.AccountScopeID, record)
	}
	return pebblestore.UpsertTopologyWorkspaceBinding(s.topologyStore, record)
}

func (s *Service) PutWorkspaceBindingForAccount(accountScopeID string, record pebblestore.TopologyWorkspaceBindingRecord) (pebblestore.TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyWorkspaceBindingRecord{}, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.PutWorkspaceBindingForAccount(accountScopeID, record)
}

func (s *Service) UpsertSessionRoute(record pebblestore.SessionRouteRecord) (pebblestore.TopologySessionRouteRecord, error) {
	return s.UpsertSessionRouteForAccount(strings.TrimSpace(record.AccountScopeID), record)
}

func (s *Service) UpsertSessionRouteForAccount(accountScopeID string, record pebblestore.SessionRouteRecord) (pebblestore.TopologySessionRouteRecord, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologySessionRouteRecord{}, fmt.Errorf("topology service is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return pebblestore.TopologySessionRouteRecord{}, fmt.Errorf("account scope id is required")
	}
	bindings, err := s.buildWorkspaceBindingsForAccount(accountScopeID)
	if err != nil {
		return pebblestore.TopologySessionRouteRecord{}, err
	}
	bindingID := strings.TrimSpace(record.WorkspaceBindingID)
	route := pebblestore.TopologySessionRouteRecord{
		SessionID:            strings.TrimSpace(record.SessionID),
		UserID:               strings.TrimSpace(record.UserID),
		AccountScopeID:       accountScopeID,
		RuntimeSwarmID:       strings.TrimSpace(record.ChildSwarmID),
		HostSwarmID:          strings.TrimSpace(record.HostSwarmID),
		HostContainerID:      strings.TrimSpace(record.HostContainerID),
		WorkspaceBindingID:   bindingID,
		RuntimeWorkspacePath: strings.TrimSpace(record.RuntimeWorkspacePath),
		PlacementGeneration:  record.PlacementGeneration,
		BindingGeneration:    record.BindingGeneration,
		CreatedAt:            record.CreatedAt,
		UpdatedAt:            record.UpdatedAt,
	}
	runtime, err := s.lookupRuntimeForSessionRouteForAccount(accountScopeID, route.RuntimeSwarmID)
	if err != nil {
		return pebblestore.TopologySessionRouteRecord{}, err
	}
	enrichTopologySessionRouteFromBinding(&route, findWorkspaceBindingByID(bindings, bindingID), runtime)
	return s.topologyStore.PutSessionRouteForAccount(accountScopeID, route)
}

func (s *Service) DeleteSessionRoute(sessionID string) error {
	if s == nil || s.topologyStore == nil {
		return fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.DeleteSessionRoute(sessionID)
}

func (s *Service) DeleteSessionRouteForAccount(accountScopeID, sessionID string) error {
	if s == nil || s.topologyStore == nil {
		return fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.DeleteSessionRouteForAccount(accountScopeID, sessionID)
}

func (s *Service) DeleteWorkspaceBinding(bindingID string) error {
	if s == nil || s.topologyStore == nil {
		return fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.DeleteWorkspaceBinding(bindingID)
}

func (s *Service) DeleteWorkspaceBindingForAccount(accountScopeID, bindingID string) error {
	if s == nil || s.topologyStore == nil {
		return fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.DeleteWorkspaceBindingForAccount(accountScopeID, bindingID)
}

func (s *Service) ListAttachmentsByHostContainer(hostContainerID string, limit int) ([]pebblestore.TopologyAttachmentRecord, error) {
	if s == nil || s.topologyStore == nil {
		return nil, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.ListAttachmentsByHostContainer(hostContainerID, limit)
}

func (s *Service) ListAttachmentsByHostContainerForAccount(accountScopeID, hostContainerID string, limit int) ([]pebblestore.TopologyAttachmentRecord, error) {
	if s == nil || s.topologyStore == nil {
		return nil, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.ListAttachmentsByHostContainerForAccount(accountScopeID, hostContainerID, limit)
}

func (s *Service) DeleteHostContainer(hostContainerID string) error {
	if s == nil || s.topologyStore == nil {
		return fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.DeleteHostContainer(hostContainerID)
}

func (s *Service) DeleteAttachment(attachmentID string) error {
	if s == nil || s.topologyStore == nil {
		return fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.DeleteAttachment(attachmentID)
}

func (s *Service) DeleteHostContainerForAccount(accountScopeID, hostContainerID string) error {
	if s == nil || s.topologyStore == nil {
		return fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.DeleteHostContainerForAccount(accountScopeID, hostContainerID)
}

func (s *Service) DeleteAttachmentForAccount(accountScopeID, attachmentID string) error {
	if s == nil || s.topologyStore == nil {
		return fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.DeleteAttachmentForAccount(accountScopeID, attachmentID)
}

func (s *Service) RemoveRuntimeObservedSource(swarmID, source string) error {
	if s == nil || s.topologyStore == nil {
		return fmt.Errorf("topology service is not configured")
	}
	return pebblestore.RemoveTopologyRuntimeObservedSource(s.topologyStore, swarmID, source)
}

func (s *Service) ResolveRuntimeHostContainer(runtimeSwarmID string) (pebblestore.TopologyHostContainerRecord, pebblestore.TopologyAttachmentRecord, bool, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyHostContainerRecord{}, pebblestore.TopologyAttachmentRecord{}, false, fmt.Errorf("topology service is not configured")
	}
	attachment, ok, err := s.topologyStore.FindAttachmentByRuntime(runtimeSwarmID)
	if err != nil || !ok {
		return pebblestore.TopologyHostContainerRecord{}, attachment, ok, err
	}
	hostContainer, ok, err := s.topologyStore.GetHostContainer(attachment.HostContainerID)
	if err != nil || !ok {
		return pebblestore.TopologyHostContainerRecord{}, attachment, ok, err
	}
	return hostContainer, attachment, true, nil
}

func (s *Service) ResolveRuntimeHostContainerForAccount(accountScopeID, runtimeSwarmID string) (pebblestore.TopologyHostContainerRecord, pebblestore.TopologyAttachmentRecord, bool, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyHostContainerRecord{}, pebblestore.TopologyAttachmentRecord{}, false, fmt.Errorf("topology service is not configured")
	}
	attachment, ok, err := s.topologyStore.FindAttachmentByRuntimeForAccount(accountScopeID, runtimeSwarmID)
	if err != nil || !ok {
		return pebblestore.TopologyHostContainerRecord{}, attachment, ok, err
	}
	hostContainer, ok, err := s.topologyStore.GetHostContainerForAccount(accountScopeID, attachment.HostContainerID)
	if err != nil || !ok {
		return pebblestore.TopologyHostContainerRecord{}, attachment, ok, err
	}
	return hostContainer, attachment, true, nil
}

func (s *Service) ListWorkspaceBindings(limit int) ([]pebblestore.TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.topologyStore == nil {
		return nil, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.ListWorkspaceBindings(limit)
}

func (s *Service) ListWorkspaceBindingsForAccount(accountScopeID string, limit int) ([]pebblestore.TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.topologyStore == nil {
		return nil, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.ListWorkspaceBindingsForAccount(accountScopeID, limit)
}

func (s *Service) ListWorkspaceBindingsBySourcePath(sourceWorkspacePath string, limit int) ([]pebblestore.TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.topologyStore == nil {
		return nil, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.ListWorkspaceBindingsBySourcePath(sourceWorkspacePath, limit)
}

func (s *Service) ListWorkspaceBindingsBySourcePathForAccount(accountScopeID, sourceWorkspacePath string, limit int) ([]pebblestore.TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.topologyStore == nil {
		return nil, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.ListWorkspaceBindingsBySourcePathForAccount(accountScopeID, sourceWorkspacePath, limit)
}

func (s *Service) GetSessionRoute(sessionID string) (pebblestore.TopologySessionRouteRecord, bool, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologySessionRouteRecord{}, false, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.GetSessionRoute(sessionID)
}

func (s *Service) GetSessionRouteForAccount(accountScopeID, sessionID string) (pebblestore.TopologySessionRouteRecord, bool, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologySessionRouteRecord{}, false, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.GetSessionRouteForAccount(accountScopeID, sessionID)
}

func (s *Service) GetRuntime(swarmID string) (pebblestore.TopologyRuntimeRecord, bool, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyRuntimeRecord{}, false, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.GetRuntime(swarmID)
}

func (s *Service) GetRuntimeForAccount(accountScopeID, swarmID string) (pebblestore.TopologyRuntimeRecord, bool, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyRuntimeRecord{}, false, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.GetRuntimeForAccount(accountScopeID, swarmID)
}

func (s *Service) GetWorkspaceBinding(bindingID string) (pebblestore.TopologyWorkspaceBindingRecord, bool, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.GetWorkspaceBinding(bindingID)
}

func (s *Service) GetWorkspaceBindingForAccount(accountScopeID, bindingID string) (pebblestore.TopologyWorkspaceBindingRecord, bool, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.GetWorkspaceBindingForAccount(accountScopeID, bindingID)
}

func (s *Service) buildSnapshot() (pebblestore.TopologySnapshot, error) {
	now := time.Now().UnixMilli()
	localSwarmID, localNode, _ := s.loadLocalNode()
	groupIDsBySwarm, err := s.loadGroupIDsBySwarm()
	if err != nil {
		return pebblestore.TopologySnapshot{}, err
	}

	runtimeMap := map[string]pebblestore.TopologyRuntimeRecord{}
	runtimePlacementMap := map[string]pebblestore.TopologyRuntimePlacementRecord{}
	hostContainerMap := map[string]pebblestore.TopologyHostContainerRecord{}
	attachmentMap := map[string]pebblestore.TopologyAttachmentRecord{}

	if strings.TrimSpace(localNode.SwarmID) != "" {
		mergeRuntime(runtimeMap, pebblestore.TopologyRuntimeRecord{
			SwarmID:         localNode.SwarmID,
			Name:            firstNonEmpty(localNode.Name, localNode.SwarmID),
			Role:            localNode.Role,
			Relationship:    "self",
			Status:          "online",
			ObservedSources: []string{"swarm_local_node"},
			CreatedAt:       now,
			UpdatedAt:       now,
		})
		mergeRuntimePlacement(runtimePlacementMap, pebblestore.TopologyRuntimePlacementRecord{
			RuntimeSwarmID:       localNode.SwarmID,
			AuthorityHostSwarmID: localNode.SwarmID,
			RuntimeKind:          pebblestore.TopologyRuntimeKindHost,
			CreatedAt:            now,
			UpdatedAt:            now,
		})
	}

	if s.swarmStore != nil {
		trustedPeers, err := s.swarmStore.ListTrustedPeers(100000)
		if err != nil {
			return pebblestore.TopologySnapshot{}, err
		}
		for _, peer := range trustedPeers {
			mergeRuntime(runtimeMap, pebblestore.TopologyRuntimeRecord{
				SwarmID:         peer.SwarmID,
				Name:            firstNonEmpty(peer.Name, peer.SwarmID),
				Role:            peer.Role,
				Relationship:    peer.Relationship,
				Transport:       peer.TransportMode,
				Status:          "trusted",
				ObservedSources: []string{"swarm_trusted_peer"},
				CreatedAt:       peer.CreatedAt,
				UpdatedAt:       peer.UpdatedAt,
			})
		}
	}

	if s.swarmNodes != nil {
		nodes, err := s.swarmNodes.List(100000)
		if err != nil {
			return pebblestore.TopologySnapshot{}, err
		}
		for _, node := range nodes {
			mergeRuntime(runtimeMap, pebblestore.TopologyRuntimeRecord{
				SwarmID:         node.SwarmID,
				Name:            firstNonEmpty(node.Name, node.SwarmID),
				Role:            node.Role,
				Relationship:    normalizeNodeRelationship(node.Role),
				BackendURL:      node.BackendURL,
				DesktopURL:      node.DesktopURL,
				Status:          node.Status,
				Transport:       node.Transport,
				ObservedSources: []string{"swarm_node"},
				CreatedAt:       node.CreatedAt,
				UpdatedAt:       node.UpdatedAt,
			})
		}
	}

	if s.localContainers != nil {
		localContainers, err := s.localContainers.List(100000)
		if err != nil {
			return pebblestore.TopologySnapshot{}, err
		}
		for _, container := range localContainers {
			hostSwarmID := firstNonEmpty(localSwarmID, "local")
			runtimeContainerRef := firstNonEmpty(container.ContainerID, container.ContainerName, container.ID)
			hostContainerID := canonicalHostContainerID(hostSwarmID, runtimeContainerRef)
			mergeHostContainer(hostContainerMap, pebblestore.TopologyHostContainerRecord{
				HostContainerID:     hostContainerID,
				HostSwarmID:         hostSwarmID,
				RuntimeContainerRef: runtimeContainerRef,
				Name:                firstNonEmpty(container.Name, container.ContainerName, container.ID),
				ContainerName:       firstNonEmpty(container.ContainerName, container.ID),
				ContainerID:         container.ContainerID,
				Runtime:             container.Runtime,
				Image:               container.Image,
				Status:              container.Status,
				HostAPIBaseURL:      container.HostAPIBaseURL,
				HostPort:            container.HostPort,
				RuntimePort:         container.RuntimePort,
				Mounts:              container.Mounts,
				ObservedSources:     []string{"swarm_local_container"},
				CreatedAt:           container.CreatedAt,
				UpdatedAt:           container.UpdatedAt,
			})
		}
	}

	if s.deployments != nil {
		deployments, err := s.deployments.List(100000)
		if err != nil {
			return pebblestore.TopologySnapshot{}, err
		}
		for _, deployment := range deployments {
			hostSwarmID := firstNonEmpty(strings.TrimSpace(deployment.HostSwarmID), localSwarmID, "local")
			runtimeContainerRef := firstNonEmpty(strings.TrimSpace(deployment.ContainerID), strings.TrimSpace(deployment.ContainerName), strings.TrimSpace(deployment.ID))
			hostContainerID := firstNonEmpty(strings.TrimSpace(deployment.HostContainerID), canonicalHostContainerID(hostSwarmID, runtimeContainerRef))
			mergeHostContainer(hostContainerMap, pebblestore.TopologyHostContainerRecord{
				HostContainerID:     hostContainerID,
				HostSwarmID:         hostSwarmID,
				RuntimeContainerRef: runtimeContainerRef,
				Name:                firstNonEmpty(deployment.Name, deployment.ContainerName, deployment.ID),
				ContainerName:       firstNonEmpty(deployment.ContainerName, deployment.Name, deployment.ID),
				ContainerID:         deployment.ContainerID,
				Runtime:             deployment.Runtime,
				Image:               deployment.Image,
				Status:              firstNonEmpty(deployment.AttachStatus, deployment.Status),
				HostAPIBaseURL:      firstNonEmpty(deployment.HostAPIBaseURL, deployment.HostBackendURL),
				HostPort:            deployment.BackendHostPort,
				RuntimePort:         deployment.DesktopHostPort,
				ObservedSources:     []string{"deploy_container"},
				CreatedAt:           deployment.CreatedAt,
				UpdatedAt:           deployment.UpdatedAt,
			})
			if strings.TrimSpace(deployment.ChildSwarmID) != "" {
				mergeRuntimePlacement(runtimePlacementMap, pebblestore.TopologyRuntimePlacementRecord{
					RuntimeSwarmID:       deployment.ChildSwarmID,
					AuthorityHostSwarmID: hostSwarmID,
					AuthorityContainerID: hostContainerID,
					RuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
					CreatedAt:            deployment.CreatedAt,
					UpdatedAt:            deployment.UpdatedAt,
				})
				mergeRuntime(runtimeMap, pebblestore.TopologyRuntimeRecord{
					SwarmID:              deployment.ChildSwarmID,
					Name:                 firstNonEmpty(deployment.ChildDisplayName, deployment.Name, deployment.ChildSwarmID),
					Role:                 "child",
					Relationship:         "child",
					BackendURL:           deployment.ChildBackendURL,
					DesktopURL:           deployment.ChildDesktopURL,
					Status:               firstNonEmpty(deployment.AttachStatus, deployment.Status),
					OwnerHostSwarmID:     hostSwarmID,
					OwnerHostContainerID: hostContainerID,
					ObservedSources:      []string{"deploy_container"},
					CreatedAt:            deployment.CreatedAt,
					UpdatedAt:            deployment.UpdatedAt,
				})
				attachmentID := canonicalAttachmentID(hostContainerID, deployment.ChildSwarmID)
				mergeAttachment(attachmentMap, pebblestore.TopologyAttachmentRecord{
					AttachmentID:    attachmentID,
					HostContainerID: hostContainerID,
					RuntimeSwarmID:  deployment.ChildSwarmID,
					State:           firstNonEmpty(deployment.AttachStatus, deployment.Status),
					DeploymentID:    deployment.ID,
					CreatedAt:       deployment.CreatedAt,
					UpdatedAt:       deployment.UpdatedAt,
				})
			}
		}
	}

	if s.remoteDeploys != nil {
		sessions, err := s.remoteDeploys.List(100000)
		if err != nil {
			return pebblestore.TopologySnapshot{}, err
		}
		for _, session := range sessions {
			if strings.TrimSpace(session.ChildSwarmID) != "" {
				hostSwarmID := firstNonEmpty(strings.TrimSpace(session.HostSwarmID), strings.TrimSpace(session.MasterSwarmID))
				runtimeContainerRef := remoteContainerNameForSession(session.ID)
				hostContainerID := canonicalHostContainerID(hostSwarmID, runtimeContainerRef)
				if hostContainerID != "" {
					mergeHostContainer(hostContainerMap, pebblestore.TopologyHostContainerRecord{
						HostContainerID:     hostContainerID,
						HostSwarmID:         hostSwarmID,
						RuntimeContainerRef: runtimeContainerRef,
						Name:                firstNonEmpty(session.ChildName, session.Name, session.ID),
						ContainerName:       runtimeContainerRef,
						Runtime:             session.RemoteRuntime,
						Status:              session.Status,
						HostAPIBaseURL:      remoteSessionEndpoint(session),
						ObservedSources:     []string{"remote_deploy_session"},
						CreatedAt:           session.CreatedAt,
						UpdatedAt:           session.UpdatedAt,
					})
					mergeRuntimePlacement(runtimePlacementMap, pebblestore.TopologyRuntimePlacementRecord{
						RuntimeSwarmID:       session.ChildSwarmID,
						AuthorityHostSwarmID: hostSwarmID,
						AuthorityContainerID: hostContainerID,
						RuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
						CreatedAt:            session.CreatedAt,
						UpdatedAt:            session.UpdatedAt,
					})
				}
				mergeRuntime(runtimeMap, pebblestore.TopologyRuntimeRecord{
					SwarmID:              session.ChildSwarmID,
					Name:                 firstNonEmpty(session.ChildName, session.Name, session.ChildSwarmID),
					Role:                 "child",
					Relationship:         "child",
					BackendURL:           session.RemoteEndpoint,
					DesktopURL:           session.HostDesktopURL,
					Status:               session.Status,
					Transport:            session.TransportMode,
					OwnerHostSwarmID:     hostSwarmID,
					OwnerHostContainerID: hostContainerID,
					ObservedSources:      []string{"remote_deploy_session"},
					CreatedAt:            session.CreatedAt,
					UpdatedAt:            session.UpdatedAt,
				})
			}
		}
	}

	workspaceBindings, err := s.buildWorkspaceBindings()
	if err != nil {
		return pebblestore.TopologySnapshot{}, err
	}
	sessionRouteRecords, err := s.buildSessionRoutes(workspaceBindings, runtimeMap)
	if err != nil {
		return pebblestore.TopologySnapshot{}, err
	}

	for _, runtime := range runtimeMap {
		if _, ok := runtimePlacementMap[runtime.SwarmID]; ok {
			continue
		}
		if strings.TrimSpace(runtime.OwnerHostSwarmID) != "" && strings.TrimSpace(runtime.OwnerHostContainerID) != "" {
			mergeRuntimePlacement(runtimePlacementMap, pebblestore.TopologyRuntimePlacementRecord{
				RuntimeSwarmID:       runtime.SwarmID,
				AuthorityHostSwarmID: runtime.OwnerHostSwarmID,
				AuthorityContainerID: runtime.OwnerHostContainerID,
				RuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
				CreatedAt:            runtime.CreatedAt,
				UpdatedAt:            runtime.UpdatedAt,
			})
		}
	}
	runtimes := runtimeMapValues(runtimeMap)
	for i := range runtimes {
		runtimes[i].GroupIDs = normalizeGroupIDs(groupIDsBySwarm[runtimes[i].SwarmID])
	}
	runtimePlacements := runtimePlacementMapValues(runtimePlacementMap)
	hostContainers := hostContainerMapValues(hostContainerMap)
	attachments := attachmentMapValues(attachmentMap)
	sortTopologyRuntimes(runtimes)
	sortTopologyRuntimePlacements(runtimePlacements)
	sortTopologyHostContainers(hostContainers)
	sortTopologyAttachments(attachments)
	sortTopologyWorkspaceBindings(workspaceBindings)
	sortTopologySessionRoutes(sessionRouteRecords)

	migrationStatus := pebblestore.TopologyMigrationStatusRecord{
		ID:                    pebblestore.DefaultTopologyMigrationStatusID,
		Version:               SnapshotVersion,
		RebuiltAt:             now,
		RuntimeCount:          len(runtimes),
		HostContainerCount:    len(hostContainers),
		AttachmentCount:       len(attachments),
		WorkspaceBindingCount: len(workspaceBindings),
		SessionRouteCount:     len(sessionRouteRecords),
	}
	return pebblestore.TopologySnapshot{
		Runtimes:          runtimes,
		RuntimePlacements: runtimePlacements,
		HostContainers:    hostContainers,
		Attachments:       attachments,
		WorkspaceBindings: workspaceBindings,
		SessionRoutes:     sessionRouteRecords,
		MigrationStatus:   migrationStatus,
	}, nil
}

func (s *Service) loadLocalNode() (string, pebblestore.SwarmLocalNodeRecord, error) {
	if s == nil || s.swarmStore == nil {
		return "", pebblestore.SwarmLocalNodeRecord{}, nil
	}
	record, ok, err := s.swarmStore.GetLocalNode()
	if err != nil || !ok {
		return strings.TrimSpace(record.SwarmID), record, err
	}
	return strings.TrimSpace(record.SwarmID), record, nil
}

func (s *Service) loadGroupIDsBySwarm() (map[string][]string, error) {
	out := map[string][]string{}
	if s == nil || s.swarmStore == nil {
		return out, nil
	}
	groups, err := s.swarmStore.ListGroups(100000)
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		members, err := s.swarmStore.ListGroupMemberships(group.ID, 100000)
		if err != nil {
			return nil, err
		}
		for _, member := range members {
			swarmID := strings.TrimSpace(member.SwarmID)
			groupID := strings.TrimSpace(group.ID)
			if swarmID == "" || groupID == "" {
				continue
			}
			out[swarmID] = append(out[swarmID], groupID)
		}
	}
	for swarmID, groupIDs := range out {
		out[swarmID] = normalizeGroupIDs(groupIDs)
	}
	return out, nil
}

func (s *Service) buildWorkspaceBindings() ([]pebblestore.TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.topologyStore == nil {
		return nil, nil
	}
	return s.topologyStore.ListWorkspaceBindings(100000)
}

func (s *Service) buildWorkspaceBindingsForAccount(accountScopeID string) ([]pebblestore.TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.topologyStore == nil {
		return nil, nil
	}
	return s.topologyStore.ListWorkspaceBindingsForAccount(accountScopeID, 100000)
}

func (s *Service) buildSessionRoutes(bindings []pebblestore.TopologyWorkspaceBindingRecord, runtimes map[string]pebblestore.TopologyRuntimeRecord) ([]pebblestore.TopologySessionRouteRecord, error) {
	if s == nil || s.sessionRoutes == nil {
		return nil, nil
	}
	routes, err := s.sessionRoutes.List(100000)
	if err != nil {
		return nil, err
	}
	out := make([]pebblestore.TopologySessionRouteRecord, 0, len(routes))
	for _, route := range routes {
		bindingID := strings.TrimSpace(route.WorkspaceBindingID)
		record := pebblestore.TopologySessionRouteRecord{
			SessionID:            strings.TrimSpace(route.SessionID),
			RuntimeSwarmID:       strings.TrimSpace(route.ChildSwarmID),
			HostSwarmID:          strings.TrimSpace(route.HostSwarmID),
			HostContainerID:      strings.TrimSpace(route.HostContainerID),
			WorkspaceBindingID:   bindingID,
			RuntimeWorkspacePath: strings.TrimSpace(route.RuntimeWorkspacePath),
			PlacementGeneration:  route.PlacementGeneration,
			BindingGeneration:    route.BindingGeneration,
			CreatedAt:            route.CreatedAt,
			UpdatedAt:            route.UpdatedAt,
		}
		enrichTopologySessionRouteFromBinding(&record, findWorkspaceBindingByID(bindings, bindingID), findRuntimeBySwarmID(runtimes, record.RuntimeSwarmID))
		out = append(out, record)
	}
	return out, nil
}

func findWorkspaceBindingByID(bindings []pebblestore.TopologyWorkspaceBindingRecord, bindingID string) pebblestore.TopologyWorkspaceBindingRecord {
	bindingID = strings.TrimSpace(bindingID)
	if bindingID == "" {
		return pebblestore.TopologyWorkspaceBindingRecord{}
	}
	for _, binding := range bindings {
		if strings.EqualFold(strings.TrimSpace(binding.BindingID), bindingID) {
			return binding
		}
	}
	return pebblestore.TopologyWorkspaceBindingRecord{}
}

func findRuntimeBySwarmID(runtimes map[string]pebblestore.TopologyRuntimeRecord, swarmID string) pebblestore.TopologyRuntimeRecord {
	swarmID = strings.TrimSpace(swarmID)
	if swarmID == "" {
		return pebblestore.TopologyRuntimeRecord{}
	}
	for key, runtime := range runtimes {
		if strings.EqualFold(strings.TrimSpace(key), swarmID) || strings.EqualFold(strings.TrimSpace(runtime.SwarmID), swarmID) {
			return runtime
		}
	}
	return pebblestore.TopologyRuntimeRecord{}
}

func (s *Service) lookupRuntimeForSessionRoute(swarmID string) (pebblestore.TopologyRuntimeRecord, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyRuntimeRecord{}, nil
	}
	swarmID = strings.TrimSpace(swarmID)
	if swarmID == "" {
		return pebblestore.TopologyRuntimeRecord{}, nil
	}
	runtime, ok, err := s.topologyStore.GetRuntime(swarmID)
	if err != nil {
		return runtime, err
	}
	if !ok {
		return s.runtimeFromDeployments(swarmID)
	}
	if strings.TrimSpace(runtime.OwnerHostSwarmID) != "" && strings.TrimSpace(runtime.OwnerHostContainerID) != "" {
		return runtime, nil
	}
	fallback, err := s.runtimeFromDeployments(swarmID)
	if err != nil {
		return pebblestore.TopologyRuntimeRecord{}, err
	}
	runtime.OwnerHostSwarmID = firstNonEmpty(runtime.OwnerHostSwarmID, fallback.OwnerHostSwarmID)
	runtime.OwnerHostContainerID = firstNonEmpty(runtime.OwnerHostContainerID, fallback.OwnerHostContainerID)
	return runtime, nil
}

func (s *Service) lookupRuntimeForSessionRouteForAccount(accountScopeID, swarmID string) (pebblestore.TopologyRuntimeRecord, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyRuntimeRecord{}, nil
	}
	swarmID = strings.TrimSpace(swarmID)
	if swarmID == "" {
		return pebblestore.TopologyRuntimeRecord{}, nil
	}
	runtime, ok, err := s.topologyStore.GetRuntimeForAccount(accountScopeID, swarmID)
	if err != nil || !ok {
		return runtime, err
	}
	return runtime, nil
}

func (s *Service) runtimeFromDeployments(swarmID string) (pebblestore.TopologyRuntimeRecord, error) {
	if s == nil || s.deployments == nil {
		return pebblestore.TopologyRuntimeRecord{}, nil
	}
	swarmID = strings.TrimSpace(swarmID)
	if swarmID == "" {
		return pebblestore.TopologyRuntimeRecord{}, nil
	}
	deployments, err := s.deployments.List(100000)
	if err != nil {
		return pebblestore.TopologyRuntimeRecord{}, err
	}
	for _, deployment := range deployments {
		if !strings.EqualFold(strings.TrimSpace(deployment.ChildSwarmID), swarmID) {
			continue
		}
		hostSwarmID := strings.TrimSpace(deployment.HostSwarmID)
		hostContainerID := strings.TrimSpace(deployment.HostContainerID)
		if hostContainerID == "" {
			runtimeContainerRef := firstNonEmpty(strings.TrimSpace(deployment.ContainerID), strings.TrimSpace(deployment.ContainerName), strings.TrimSpace(deployment.ID))
			hostContainerID = canonicalHostContainerID(hostSwarmID, runtimeContainerRef)
		}
		return pebblestore.TopologyRuntimeRecord{
			SwarmID:              strings.TrimSpace(deployment.ChildSwarmID),
			Name:                 firstNonEmpty(deployment.ChildDisplayName, deployment.Name, deployment.ChildSwarmID),
			Role:                 "child",
			Relationship:         "child",
			BackendURL:           deployment.ChildBackendURL,
			DesktopURL:           deployment.ChildDesktopURL,
			Status:               firstNonEmpty(deployment.AttachStatus, deployment.Status),
			OwnerHostSwarmID:     hostSwarmID,
			OwnerHostContainerID: hostContainerID,
			ObservedSources:      []string{"deploy_container"},
			CreatedAt:            deployment.CreatedAt,
			UpdatedAt:            deployment.UpdatedAt,
		}, nil
	}
	return pebblestore.TopologyRuntimeRecord{}, nil
}

func enrichTopologySessionRouteFromBinding(route *pebblestore.TopologySessionRouteRecord, binding pebblestore.TopologyWorkspaceBindingRecord, runtime pebblestore.TopologyRuntimeRecord) {
	if route == nil {
		return
	}
	bindingPresent := strings.TrimSpace(binding.BindingID) != ""
	if strings.TrimSpace(route.HostSwarmID) == "" && bindingPresent {
		route.HostSwarmID = strings.TrimSpace(binding.DestinationHostSwarmID)
	}
	if strings.TrimSpace(route.HostSwarmID) == "" {
		route.HostSwarmID = strings.TrimSpace(runtime.OwnerHostSwarmID)
	}
	if strings.TrimSpace(route.HostContainerID) == "" && bindingPresent {
		route.HostContainerID = strings.TrimSpace(binding.DestinationContainerID)
	}
	if strings.TrimSpace(route.HostContainerID) == "" {
		route.HostContainerID = strings.TrimSpace(runtime.OwnerHostContainerID)
	}
	if strings.TrimSpace(route.RuntimeWorkspacePath) == "" && bindingPresent {
		route.RuntimeWorkspacePath = strings.TrimSpace(binding.DestinationWorkspacePath)
	}
	if route.PlacementGeneration <= 0 && bindingPresent {
		route.PlacementGeneration = binding.PlacementGeneration
	}
	if route.BindingGeneration <= 0 && bindingPresent {
		route.BindingGeneration = binding.BindingGeneration
	}
}

func mergeRuntime(dst map[string]pebblestore.TopologyRuntimeRecord, incoming pebblestore.TopologyRuntimeRecord) {
	incoming = normalizeRuntime(incoming)
	if incoming.SwarmID == "" {
		return
	}
	existing, ok := dst[incoming.SwarmID]
	if !ok {
		dst[incoming.SwarmID] = incoming
		return
	}
	existing.Name = firstNonEmpty(existing.Name, incoming.Name, incoming.SwarmID)
	existing.Role = firstNonEmpty(existing.Role, incoming.Role)
	existing.Relationship = chooseRelationship(existing.Relationship, incoming.Relationship)
	existing.BackendURL = firstNonEmpty(existing.BackendURL, incoming.BackendURL)
	existing.DesktopURL = firstNonEmpty(existing.DesktopURL, incoming.DesktopURL)
	existing.Status = firstNonEmpty(existing.Status, incoming.Status)
	existing.Transport = firstNonEmpty(existing.Transport, incoming.Transport)
	existing.OwnerHostSwarmID = firstNonEmpty(existing.OwnerHostSwarmID, incoming.OwnerHostSwarmID)
	existing.OwnerHostContainerID = firstNonEmpty(existing.OwnerHostContainerID, incoming.OwnerHostContainerID)
	existing.GroupIDs = appendDedup(existing.GroupIDs, incoming.GroupIDs...)
	existing.ObservedSources = appendDedup(existing.ObservedSources, incoming.ObservedSources...)
	existing.CreatedAt = minPositive(existing.CreatedAt, incoming.CreatedAt)
	existing.UpdatedAt = maxInt64(existing.UpdatedAt, incoming.UpdatedAt)
	dst[incoming.SwarmID] = existing
}

func mergeRuntimePlacement(dst map[string]pebblestore.TopologyRuntimePlacementRecord, incoming pebblestore.TopologyRuntimePlacementRecord) {
	incoming = normalizeRuntimePlacement(incoming)
	if incoming.RuntimeSwarmID == "" {
		return
	}
	existing, ok := dst[incoming.RuntimeSwarmID]
	if !ok {
		dst[incoming.RuntimeSwarmID] = incoming
		return
	}
	existing.AuthorityHostSwarmID = firstNonEmpty(existing.AuthorityHostSwarmID, incoming.AuthorityHostSwarmID)
	existing.AuthorityContainerID = firstNonEmpty(existing.AuthorityContainerID, incoming.AuthorityContainerID)
	existing.RuntimeKind = firstNonEmpty(existing.RuntimeKind, incoming.RuntimeKind)
	existing.State = firstNonEmpty(existing.State, incoming.State)
	existing.PlacementGeneration = maxInt(existing.PlacementGeneration, incoming.PlacementGeneration)
	existing.CreatedAt = minPositive(existing.CreatedAt, incoming.CreatedAt)
	existing.UpdatedAt = maxInt64(existing.UpdatedAt, incoming.UpdatedAt)
	dst[incoming.RuntimeSwarmID] = existing
}

func mergeHostContainer(dst map[string]pebblestore.TopologyHostContainerRecord, incoming pebblestore.TopologyHostContainerRecord) {
	incoming = normalizeHostContainer(incoming)
	if incoming.HostContainerID == "" {
		return
	}
	existing, ok := dst[incoming.HostContainerID]
	if !ok {
		dst[incoming.HostContainerID] = incoming
		return
	}
	existing.HostSwarmID = firstNonEmpty(existing.HostSwarmID, incoming.HostSwarmID)
	existing.RuntimeContainerRef = firstNonEmpty(existing.RuntimeContainerRef, incoming.RuntimeContainerRef)
	existing.Name = firstNonEmpty(existing.Name, incoming.Name, incoming.HostContainerID)
	existing.ContainerName = firstNonEmpty(existing.ContainerName, incoming.ContainerName)
	existing.ContainerID = firstNonEmpty(existing.ContainerID, incoming.ContainerID)
	existing.Runtime = firstNonEmpty(existing.Runtime, incoming.Runtime)
	existing.Image = firstNonEmpty(existing.Image, incoming.Image)
	existing.Status = firstNonEmpty(existing.Status, incoming.Status)
	existing.HostAPIBaseURL = firstNonEmpty(existing.HostAPIBaseURL, incoming.HostAPIBaseURL)
	existing.HostPort = maxInt(existing.HostPort, incoming.HostPort)
	existing.RuntimePort = maxInt(existing.RuntimePort, incoming.RuntimePort)
	if len(existing.Mounts) == 0 {
		existing.Mounts = incoming.Mounts
	}
	existing.ObservedSources = appendDedup(existing.ObservedSources, incoming.ObservedSources...)
	existing.CreatedAt = minPositive(existing.CreatedAt, incoming.CreatedAt)
	existing.UpdatedAt = maxInt64(existing.UpdatedAt, incoming.UpdatedAt)
	dst[incoming.HostContainerID] = existing
}

func mergeAttachment(dst map[string]pebblestore.TopologyAttachmentRecord, incoming pebblestore.TopologyAttachmentRecord) {
	incoming = normalizeAttachment(incoming)
	if incoming.AttachmentID == "" {
		return
	}
	existing, ok := dst[incoming.AttachmentID]
	if !ok {
		dst[incoming.AttachmentID] = incoming
		return
	}
	existing.HostContainerID = firstNonEmpty(existing.HostContainerID, incoming.HostContainerID)
	existing.RuntimeSwarmID = firstNonEmpty(existing.RuntimeSwarmID, incoming.RuntimeSwarmID)
	existing.State = firstNonEmpty(existing.State, incoming.State)
	existing.DeploymentID = firstNonEmpty(existing.DeploymentID, incoming.DeploymentID)
	existing.RemoteDeploySessionID = firstNonEmpty(existing.RemoteDeploySessionID, incoming.RemoteDeploySessionID)
	existing.LastError = firstNonEmpty(existing.LastError, incoming.LastError)
	existing.CreatedAt = minPositive(existing.CreatedAt, incoming.CreatedAt)
	existing.UpdatedAt = maxInt64(existing.UpdatedAt, incoming.UpdatedAt)
	dst[incoming.AttachmentID] = existing
}

func normalizeRuntime(record pebblestore.TopologyRuntimeRecord) pebblestore.TopologyRuntimeRecord {
	record.Name = firstNonEmpty(strings.TrimSpace(record.Name), strings.TrimSpace(record.SwarmID))
	record.Role = strings.ToLower(strings.TrimSpace(record.Role))
	record.Relationship = strings.ToLower(strings.TrimSpace(record.Relationship))
	record.Status = strings.ToLower(strings.TrimSpace(record.Status))
	record.Transport = strings.ToLower(strings.TrimSpace(record.Transport))
	record.BackendURL = ""
	record.DesktopURL = strings.TrimSpace(record.DesktopURL)
	record.OwnerHostSwarmID = strings.TrimSpace(record.OwnerHostSwarmID)
	record.OwnerHostContainerID = strings.TrimSpace(record.OwnerHostContainerID)
	record.ObservedSources = appendDedup(nil, record.ObservedSources...)
	if record.CreatedAt <= 0 {
		record.CreatedAt = time.Now().UnixMilli()
	}
	if record.UpdatedAt <= 0 {
		record.UpdatedAt = record.CreatedAt
	}
	return record
}

func normalizeRuntimePlacement(record pebblestore.TopologyRuntimePlacementRecord) pebblestore.TopologyRuntimePlacementRecord {
	record.RuntimeSwarmID = strings.TrimSpace(record.RuntimeSwarmID)
	record.AuthorityHostSwarmID = strings.TrimSpace(record.AuthorityHostSwarmID)
	record.AuthorityContainerID = strings.TrimSpace(record.AuthorityContainerID)
	record.RuntimeKind = strings.ToLower(strings.TrimSpace(record.RuntimeKind))
	record.State = strings.ToLower(strings.TrimSpace(record.State))
	if record.State == "" {
		record.State = pebblestore.TopologyRuntimePlacementStateActive
	}
	if record.PlacementGeneration <= 0 {
		record.PlacementGeneration = 1
	}
	if record.CreatedAt < 0 {
		record.CreatedAt = 0
	}
	if record.UpdatedAt < 0 {
		record.UpdatedAt = 0
	}
	return record
}

func normalizeHostContainer(record pebblestore.TopologyHostContainerRecord) pebblestore.TopologyHostContainerRecord {
	record.HostSwarmID = strings.TrimSpace(record.HostSwarmID)
	record.RuntimeContainerRef = strings.TrimSpace(record.RuntimeContainerRef)
	record.Name = firstNonEmpty(strings.TrimSpace(record.Name), strings.TrimSpace(record.ContainerName), strings.TrimSpace(record.ContainerID), strings.TrimSpace(record.HostContainerID))
	record.ContainerName = strings.TrimSpace(record.ContainerName)
	record.ContainerID = strings.TrimSpace(record.ContainerID)
	record.Runtime = strings.ToLower(strings.TrimSpace(record.Runtime))
	record.Image = strings.TrimSpace(record.Image)
	record.Status = strings.ToLower(strings.TrimSpace(record.Status))
	record.HostAPIBaseURL = strings.TrimSpace(record.HostAPIBaseURL)
	record.ObservedSources = appendDedup(nil, record.ObservedSources...)
	if record.CreatedAt <= 0 {
		record.CreatedAt = time.Now().UnixMilli()
	}
	if record.UpdatedAt <= 0 {
		record.UpdatedAt = record.CreatedAt
	}
	return record
}

func normalizeAttachment(record pebblestore.TopologyAttachmentRecord) pebblestore.TopologyAttachmentRecord {
	record.HostContainerID = strings.TrimSpace(record.HostContainerID)
	record.RuntimeSwarmID = strings.TrimSpace(record.RuntimeSwarmID)
	record.State = strings.ToLower(strings.TrimSpace(record.State))
	record.DeploymentID = strings.TrimSpace(record.DeploymentID)
	record.RemoteDeploySessionID = strings.TrimSpace(record.RemoteDeploySessionID)
	record.LastError = strings.TrimSpace(record.LastError)
	if record.CreatedAt <= 0 {
		record.CreatedAt = time.Now().UnixMilli()
	}
	if record.UpdatedAt <= 0 {
		record.UpdatedAt = record.CreatedAt
	}
	return record
}

func normalizeGroupIDs(groupIDs []string) []string {
	return appendDedup(nil, groupIDs...)
}

func appendDedup(existing []string, values ...string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(existing)+len(values))
	for _, raw := range existing {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func minPositive(values ...int64) int64 {
	var out int64
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if out == 0 || value < out {
			out = value
		}
	}
	return out
}

func maxInt64(values ...int64) int64 {
	var out int64
	for _, value := range values {
		if value > out {
			out = value
		}
	}
	return out
}

func maxInt(values ...int) int {
	out := 0
	for _, value := range values {
		if value > out {
			out = value
		}
	}
	return out
}

func chooseRelationship(existing, incoming string) string {
	existing = strings.ToLower(strings.TrimSpace(existing))
	incoming = strings.ToLower(strings.TrimSpace(incoming))
	if existing == "self" || incoming == "self" {
		if existing == "self" {
			return existing
		}
		return incoming
	}
	if existing != "" {
		return existing
	}
	return incoming
}

func normalizeNodeRelationship(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "managed", "manager", "child", "self":
		return strings.ToLower(strings.TrimSpace(role))
	default:
		return ""
	}
}

func remoteContainerNameForSession(sessionID string) string {
	slug := strings.ToLower(strings.TrimSpace(sessionID))
	slug = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, slug)
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "swarm-remote"
	}
	return "swarm-remote-" + slug
}

func remoteSessionEndpoint(session pebblestore.RemoteDeploySessionRecord) string {
	return firstNonEmpty(session.RemoteEndpoint, session.RemoteTailnetURL, session.HostAPIBaseURL)
}

func canonicalHostContainerID(hostSwarmID, runtimeContainerRef string) string {
	hostSwarmID = strings.TrimSpace(hostSwarmID)
	runtimeContainerRef = strings.TrimSpace(runtimeContainerRef)
	if hostSwarmID == "" || runtimeContainerRef == "" {
		return ""
	}
	return fmt.Sprintf("%s:%s", hostSwarmID, strings.ToLower(runtimeContainerRef))
}

func canonicalAttachmentID(hostContainerID, runtimeSwarmID string) string {
	hostContainerID = strings.TrimSpace(hostContainerID)
	runtimeSwarmID = strings.TrimSpace(runtimeSwarmID)
	if hostContainerID == "" || runtimeSwarmID == "" {
		return ""
	}
	return fmt.Sprintf("%s=>%s", hostContainerID, runtimeSwarmID)
}

func canonicalWorkspaceBindingID(sourceWorkspacePath, targetSwarmID, targetWorkspacePath, targetKind string) string {
	parts := []string{
		strings.ToLower(strings.TrimSpace(sourceWorkspacePath)),
		strings.ToLower(strings.TrimSpace(targetSwarmID)),
		strings.ToLower(strings.TrimSpace(targetWorkspacePath)),
		strings.ToLower(strings.TrimSpace(targetKind)),
	}
	return strings.Join(parts, "|")
}

func sortTopologyRuntimes(records []pebblestore.TopologyRuntimeRecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].UpdatedAt == records[j].UpdatedAt {
			return strings.ToLower(records[i].SwarmID) < strings.ToLower(records[j].SwarmID)
		}
		return records[i].UpdatedAt > records[j].UpdatedAt
	})
}

func sortTopologyRuntimePlacements(records []pebblestore.TopologyRuntimePlacementRecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].UpdatedAt == records[j].UpdatedAt {
			return strings.ToLower(records[i].RuntimeSwarmID) < strings.ToLower(records[j].RuntimeSwarmID)
		}
		return records[i].UpdatedAt > records[j].UpdatedAt
	})
}

func sortTopologyHostContainers(records []pebblestore.TopologyHostContainerRecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].UpdatedAt == records[j].UpdatedAt {
			return strings.ToLower(records[i].HostContainerID) < strings.ToLower(records[j].HostContainerID)
		}
		return records[i].UpdatedAt > records[j].UpdatedAt
	})
}

func sortTopologyAttachments(records []pebblestore.TopologyAttachmentRecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].UpdatedAt == records[j].UpdatedAt {
			return strings.ToLower(records[i].AttachmentID) < strings.ToLower(records[j].AttachmentID)
		}
		return records[i].UpdatedAt > records[j].UpdatedAt
	})
}

func sortTopologyWorkspaceBindings(records []pebblestore.TopologyWorkspaceBindingRecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].UpdatedAt == records[j].UpdatedAt {
			return strings.ToLower(records[i].BindingID) < strings.ToLower(records[j].BindingID)
		}
		return records[i].UpdatedAt > records[j].UpdatedAt
	})
}

func sortTopologySessionRoutes(records []pebblestore.TopologySessionRouteRecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].UpdatedAt == records[j].UpdatedAt {
			return strings.ToLower(records[i].SessionID) < strings.ToLower(records[j].SessionID)
		}
		return records[i].UpdatedAt > records[j].UpdatedAt
	})
}

func runtimeMapValues(input map[string]pebblestore.TopologyRuntimeRecord) []pebblestore.TopologyRuntimeRecord {
	out := make([]pebblestore.TopologyRuntimeRecord, 0, len(input))
	for _, value := range input {
		out = append(out, value)
	}
	return out
}

func runtimePlacementMapValues(input map[string]pebblestore.TopologyRuntimePlacementRecord) []pebblestore.TopologyRuntimePlacementRecord {
	out := make([]pebblestore.TopologyRuntimePlacementRecord, 0, len(input))
	for _, value := range input {
		out = append(out, value)
	}
	return out
}

func hostContainerMapValues(input map[string]pebblestore.TopologyHostContainerRecord) []pebblestore.TopologyHostContainerRecord {
	out := make([]pebblestore.TopologyHostContainerRecord, 0, len(input))
	for _, value := range input {
		out = append(out, value)
	}
	return out
}

func attachmentMapValues(input map[string]pebblestore.TopologyAttachmentRecord) []pebblestore.TopologyAttachmentRecord {
	out := make([]pebblestore.TopologyAttachmentRecord, 0, len(input))
	for _, value := range input {
		out = append(out, value)
	}
	return out
}
