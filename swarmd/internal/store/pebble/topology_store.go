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

const (
	DefaultTopologyMigrationStatusID = "default"
	TopologySnapshotVersion          = "checkpoint1-v1"
)

type TopologyRuntimeRecord struct {
	SwarmID              string   `json:"swarm_id"`
	UserID               string   `json:"user_id,omitempty"`
	AccountScopeID       string   `json:"account_scope_id,omitempty"`
	Name                 string   `json:"name"`
	Role                 string   `json:"role,omitempty"`
	Relationship         string   `json:"relationship,omitempty"`
	BackendURL           string   `json:"backend_url,omitempty"`
	DesktopURL           string   `json:"desktop_url,omitempty"`
	Status               string   `json:"status,omitempty"`
	Transport            string   `json:"transport,omitempty"`
	OwnerHostSwarmID     string   `json:"owner_host_swarm_id,omitempty"`
	OwnerHostContainerID string   `json:"owner_host_container_id,omitempty"`
	GroupIDs             []string `json:"group_ids,omitempty"`
	ObservedSources      []string `json:"observed_sources,omitempty"`
	CreatedAt            int64    `json:"created_at"`
	UpdatedAt            int64    `json:"updated_at"`
}

type TopologyHostContainerRecord struct {
	HostContainerID     string                     `json:"host_container_id"`
	UserID              string                     `json:"user_id,omitempty"`
	AccountScopeID      string                     `json:"account_scope_id,omitempty"`
	HostSwarmID         string                     `json:"host_swarm_id"`
	RuntimeContainerRef string                     `json:"runtime_container_ref"`
	Name                string                     `json:"name"`
	ContainerName       string                     `json:"container_name,omitempty"`
	ContainerID         string                     `json:"container_id,omitempty"`
	Runtime             string                     `json:"runtime,omitempty"`
	Image               string                     `json:"image,omitempty"`
	Status              string                     `json:"status,omitempty"`
	HostAPIBaseURL      string                     `json:"host_api_base_url,omitempty"`
	HostPort            int                        `json:"host_port,omitempty"`
	RuntimePort         int                        `json:"runtime_port,omitempty"`
	Mounts              []SwarmLocalContainerMount `json:"mounts,omitempty"`
	ObservedSources     []string                   `json:"observed_sources,omitempty"`
	CreatedAt           int64                      `json:"created_at"`
	UpdatedAt           int64                      `json:"updated_at"`
}

type TopologyAttachmentRecord struct {
	AttachmentID          string `json:"attachment_id"`
	UserID                string `json:"user_id,omitempty"`
	AccountScopeID        string `json:"account_scope_id,omitempty"`
	HostContainerID       string `json:"host_container_id"`
	RuntimeSwarmID        string `json:"runtime_swarm_id"`
	State                 string `json:"state,omitempty"`
	DeploymentID          string `json:"deployment_id,omitempty"`
	RemoteDeploySessionID string `json:"remote_deploy_session_id,omitempty"`
	LastError             string `json:"last_error,omitempty"`
	CreatedAt             int64  `json:"created_at"`
	UpdatedAt             int64  `json:"updated_at"`
}

type TopologyWorkspaceBindingRecord struct {
	BindingID                 string                   `json:"binding_id"`
	UserID                    string                   `json:"user_id,omitempty"`
	AccountScopeID            string                   `json:"account_scope_id,omitempty"`
	SourceWorkspacePath       string                   `json:"source_workspace_path"`
	SourceWorkspaceName       string                   `json:"source_workspace_name,omitempty"`
	DestinationRuntimeSwarmID string                   `json:"destination_runtime_swarm_id,omitempty"`
	DestinationHostSwarmID    string                   `json:"destination_host_swarm_id,omitempty"`
	DestinationContainerID    string                   `json:"destination_container_id,omitempty"`
	DestinationWorkspacePath  string                   `json:"destination_workspace_path,omitempty"`
	ReplicationMode           string                   `json:"replication_mode,omitempty"`
	Writable                  bool                     `json:"writable"`
	Sync                      WorkspaceReplicationSync `json:"sync,omitempty"`
	LegacyTargetKind          string                   `json:"legacy_target_kind,omitempty"`
	CreatedAt                 int64                    `json:"created_at"`
	UpdatedAt                 int64                    `json:"updated_at"`
}

type TopologySessionRouteRecord struct {
	SessionID            string `json:"session_id"`
	UserID               string `json:"user_id,omitempty"`
	AccountScopeID       string `json:"account_scope_id,omitempty"`
	RuntimeSwarmID       string `json:"runtime_swarm_id,omitempty"`
	HostSwarmID          string `json:"host_swarm_id,omitempty"`
	HostContainerID      string `json:"host_container_id,omitempty"`
	WorkspaceBindingID   string `json:"workspace_binding_id,omitempty"`
	BackendURL           string `json:"backend_url,omitempty"`
	HostWorkspacePath    string `json:"host_workspace_path,omitempty"`
	RuntimeWorkspacePath string `json:"runtime_workspace_path,omitempty"`
	CreatedAt            int64  `json:"created_at"`
	UpdatedAt            int64  `json:"updated_at"`
}

type TopologyMigrationStatusRecord struct {
	ID                    string `json:"id"`
	Version               string `json:"version"`
	RebuiltAt             int64  `json:"rebuilt_at"`
	RuntimeCount          int    `json:"runtime_count"`
	HostContainerCount    int    `json:"host_container_count"`
	AttachmentCount       int    `json:"attachment_count"`
	WorkspaceBindingCount int    `json:"workspace_binding_count"`
	SessionRouteCount     int    `json:"session_route_count"`
}

type TopologySnapshot struct {
	Runtimes          []TopologyRuntimeRecord          `json:"runtimes,omitempty"`
	RuntimePlacements []TopologyRuntimePlacementRecord `json:"runtime_placements,omitempty"`
	HostContainers    []TopologyHostContainerRecord    `json:"host_containers,omitempty"`
	Attachments       []TopologyAttachmentRecord       `json:"attachments,omitempty"`
	WorkspaceBindings []TopologyWorkspaceBindingRecord `json:"workspace_bindings,omitempty"`
	SessionRoutes     []TopologySessionRouteRecord     `json:"session_routes,omitempty"`
	MigrationStatus   TopologyMigrationStatusRecord    `json:"migration_status"`
}

type TopologyStore struct {
	store *Store
}

func NewTopologyStore(store *Store) *TopologyStore {
	return &TopologyStore{store: store}
}

// ReplaceSnapshot rewrites legacy/global topology keys for explicit migration/internal rebuilds only.
// Account-owned product topology must use ReplaceSnapshotForAccount so an account update
// cannot delete another account's topology records or fall back to global mutable keys.
func (s *TopologyStore) ReplaceSnapshot(snapshot TopologySnapshot) error {
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	snapshot.Runtimes = normalizeTopologyRuntimeRecords(snapshot.Runtimes)
	snapshot.RuntimePlacements = normalizeTopologyRuntimePlacementRecords(snapshot.RuntimePlacements)
	snapshot.HostContainers = normalizeTopologyHostContainerRecords(snapshot.HostContainers)
	snapshot.Attachments = normalizeTopologyAttachmentRecords(snapshot.Attachments)
	snapshot.WorkspaceBindings = normalizeTopologyWorkspaceBindingRecords(snapshot.WorkspaceBindings)
	snapshot.SessionRoutes = normalizeTopologySessionRouteRecords(snapshot.SessionRoutes)
	snapshot.MigrationStatus = normalizeTopologyMigrationStatusRecord(snapshot.MigrationStatus)
	batch := s.store.NewBatch()
	defer batch.Close()
	for _, prefix := range []string{
		TopologyRuntimePrefix(),
		TopologyRuntimePlacementPrefix(),
		TopologyHostContainerPrefix(),
		TopologyAttachmentPrefix(),
		TopologyWorkspaceBindingPrefix(),
		TopologySessionRoutePrefix(),
		TopologyMigrationStatusPrefix(),
	} {
		if err := s.deletePrefixWithBatch(batch, prefix); err != nil {
			return err
		}
	}
	for _, record := range snapshot.Runtimes {
		payload, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("marshal topology runtime %q: %w", record.SwarmID, err)
		}
		if err := batch.Set([]byte(KeyTopologyRuntime(record.SwarmID)), payload, nil); err != nil {
			return err
		}
	}
	for _, record := range snapshot.RuntimePlacements {
		payload, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("marshal topology runtime placement %q: %w", record.RuntimeSwarmID, err)
		}
		if err := batch.Set([]byte(KeyTopologyRuntimePlacement(record.RuntimeSwarmID)), payload, nil); err != nil {
			return err
		}
	}
	for _, record := range snapshot.HostContainers {
		payload, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("marshal topology host container %q: %w", record.HostContainerID, err)
		}
		if err := batch.Set([]byte(KeyTopologyHostContainer(record.HostContainerID)), payload, nil); err != nil {
			return err
		}
	}
	for _, record := range snapshot.Attachments {
		payload, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("marshal topology attachment %q: %w", record.AttachmentID, err)
		}
		if err := batch.Set([]byte(KeyTopologyAttachment(record.AttachmentID)), payload, nil); err != nil {
			return err
		}
	}
	for _, record := range snapshot.WorkspaceBindings {
		payload, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("marshal topology workspace binding %q: %w", record.BindingID, err)
		}
		if err := batch.Set([]byte(KeyTopologyWorkspaceBinding(record.BindingID)), payload, nil); err != nil {
			return err
		}
	}
	for _, record := range snapshot.SessionRoutes {
		payload, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("marshal topology session route %q: %w", record.SessionID, err)
		}
		if err := batch.Set([]byte(KeyTopologySessionRoute(record.SessionID)), payload, nil); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(snapshot.MigrationStatus)
	if err != nil {
		return fmt.Errorf("marshal topology migration status %q: %w", snapshot.MigrationStatus.ID, err)
	}
	if err := batch.Set([]byte(KeyTopologyMigrationStatus(snapshot.MigrationStatus.ID)), payload, nil); err != nil {
		return err
	}
	return batch.Commit(nil)
}

func (s *TopologyStore) Snapshot() (TopologySnapshot, error) {
	if s == nil || s.store == nil {
		return TopologySnapshot{}, errors.New("topology store is not configured")
	}
	runtimes, err := s.ListRuntimes(100000)
	if err != nil {
		return TopologySnapshot{}, err
	}
	runtimePlacements, err := s.listTopologyRuntimePlacementRecords(100000)
	if err != nil {
		return TopologySnapshot{}, err
	}
	hostContainers, err := s.ListHostContainers(100000)
	if err != nil {
		return TopologySnapshot{}, err
	}
	attachments, err := s.ListAttachments(100000)
	if err != nil {
		return TopologySnapshot{}, err
	}
	workspaceBindings, err := s.ListWorkspaceBindings(100000)
	if err != nil {
		return TopologySnapshot{}, err
	}
	sessionRoutes, err := s.ListSessionRoutes(100000)
	if err != nil {
		return TopologySnapshot{}, err
	}
	migrationStatus, _, err := s.GetMigrationStatus(DefaultTopologyMigrationStatusID)
	if err != nil {
		return TopologySnapshot{}, err
	}
	return TopologySnapshot{
		Runtimes:          runtimes,
		RuntimePlacements: runtimePlacements,
		HostContainers:    hostContainers,
		Attachments:       attachments,
		WorkspaceBindings: workspaceBindings,
		SessionRoutes:     sessionRoutes,
		MigrationStatus:   migrationStatus,
	}, nil
}

func (s *TopologyStore) GetRuntime(swarmID string) (TopologyRuntimeRecord, bool, error) {
	if s == nil || s.store == nil {
		return TopologyRuntimeRecord{}, false, nil
	}
	swarmID = normalizeTopologyKeyValue(swarmID)
	if swarmID == "" {
		return TopologyRuntimeRecord{}, false, errors.New("topology runtime swarm id is required")
	}
	var record TopologyRuntimeRecord
	ok, err := s.store.GetJSON(KeyTopologyRuntime(swarmID), &record)
	if err != nil {
		return TopologyRuntimeRecord{}, false, err
	}
	if !ok {
		return TopologyRuntimeRecord{}, false, nil
	}
	record = normalizeTopologyRuntimeRecord(record)
	if record.SwarmID == "" {
		record.SwarmID = swarmID
	}
	return record, true, nil
}

func (s *TopologyStore) PutRuntime(record TopologyRuntimeRecord) (TopologyRuntimeRecord, error) {
	if s == nil || s.store == nil {
		return TopologyRuntimeRecord{}, errors.New("topology store is not configured")
	}
	record = normalizeTopologyRuntimeRecord(record)
	if record.SwarmID == "" {
		return TopologyRuntimeRecord{}, errors.New("topology runtime swarm id is required")
	}
	if record.Name == "" {
		record.Name = record.SwarmID
	}
	now := time.Now().UnixMilli()
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeyTopologyRuntime(record.SwarmID), record); err != nil {
		return TopologyRuntimeRecord{}, err
	}
	if _, err := s.refreshMigrationStatus(); err != nil {
		return TopologyRuntimeRecord{}, err
	}
	return record, nil
}

func (s *TopologyStore) DeleteRuntime(swarmID string) error {
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	swarmID = normalizeTopologyKeyValue(swarmID)
	if swarmID == "" {
		return errors.New("topology runtime swarm id is required")
	}
	if err := s.store.Delete(KeyTopologyRuntime(swarmID)); err != nil {
		return err
	}
	if err := s.DeleteRuntimePlacement(swarmID); err != nil {
		return err
	}
	_, err := s.refreshMigrationStatus()
	return err
}

func (s *TopologyStore) ListRuntimes(limit int) ([]TopologyRuntimeRecord, error) {
	return s.listTopologyRuntimeRecords(limit)
}

func (s *TopologyStore) ListHostContainers(limit int) ([]TopologyHostContainerRecord, error) {
	return s.listTopologyHostContainerRecords(limit)
}

func (s *TopologyStore) ListHostContainersByHost(hostSwarmID string, limit int) ([]TopologyHostContainerRecord, error) {
	hostSwarmID = normalizeTopologyKeyValue(hostSwarmID)
	if hostSwarmID == "" {
		return nil, errors.New("topology host swarm id is required")
	}
	records, err := s.listTopologyHostContainerRecords(limit)
	if err != nil {
		return nil, err
	}
	out := make([]TopologyHostContainerRecord, 0, len(records))
	for _, record := range records {
		if strings.EqualFold(strings.TrimSpace(record.HostSwarmID), hostSwarmID) {
			out = append(out, record)
		}
	}
	return out, nil
}

func (s *TopologyStore) GetHostContainer(hostContainerID string) (TopologyHostContainerRecord, bool, error) {
	if s == nil || s.store == nil {
		return TopologyHostContainerRecord{}, false, nil
	}
	hostContainerID = normalizeTopologyKeyValue(hostContainerID)
	if hostContainerID == "" {
		return TopologyHostContainerRecord{}, false, errors.New("topology host container id is required")
	}
	var record TopologyHostContainerRecord
	ok, err := s.store.GetJSON(KeyTopologyHostContainer(hostContainerID), &record)
	if err != nil {
		return TopologyHostContainerRecord{}, false, err
	}
	if !ok {
		return TopologyHostContainerRecord{}, false, nil
	}
	record = normalizeTopologyHostContainerRecord(record)
	if record.HostContainerID == "" {
		record.HostContainerID = hostContainerID
	}
	return record, true, nil
}

func (s *TopologyStore) PutHostContainer(record TopologyHostContainerRecord) (TopologyHostContainerRecord, error) {
	if s == nil || s.store == nil {
		return TopologyHostContainerRecord{}, errors.New("topology store is not configured")
	}
	record = normalizeTopologyHostContainerRecord(record)
	if record.HostContainerID == "" {
		return TopologyHostContainerRecord{}, errors.New("topology host container id is required")
	}
	if record.HostSwarmID == "" {
		return TopologyHostContainerRecord{}, errors.New("topology host container host swarm id is required")
	}
	if record.RuntimeContainerRef == "" {
		return TopologyHostContainerRecord{}, errors.New("topology host container runtime container ref is required")
	}
	if record.Name == "" {
		record.Name = firstNonEmpty(record.ContainerName, record.ContainerID, record.HostContainerID)
	}
	now := time.Now().UnixMilli()
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeyTopologyHostContainer(record.HostContainerID), record); err != nil {
		return TopologyHostContainerRecord{}, err
	}
	if _, err := s.refreshMigrationStatus(); err != nil {
		return TopologyHostContainerRecord{}, err
	}
	return record, nil
}

func (s *TopologyStore) DeleteHostContainer(hostContainerID string) error {
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	hostContainerID = normalizeTopologyKeyValue(hostContainerID)
	if hostContainerID == "" {
		return errors.New("topology host container id is required")
	}
	if err := s.store.Delete(KeyTopologyHostContainer(hostContainerID)); err != nil {
		return err
	}
	_, err := s.refreshMigrationStatus()
	return err
}

func (s *TopologyStore) GetAttachment(attachmentID string) (TopologyAttachmentRecord, bool, error) {
	if s == nil || s.store == nil {
		return TopologyAttachmentRecord{}, false, nil
	}
	attachmentID = normalizeTopologyKeyValue(attachmentID)
	if attachmentID == "" {
		return TopologyAttachmentRecord{}, false, errors.New("topology attachment id is required")
	}
	var record TopologyAttachmentRecord
	ok, err := s.store.GetJSON(KeyTopologyAttachment(attachmentID), &record)
	if err != nil {
		return TopologyAttachmentRecord{}, false, err
	}
	if !ok {
		return TopologyAttachmentRecord{}, false, nil
	}
	record = normalizeTopologyAttachmentRecord(record)
	if record.AttachmentID == "" {
		record.AttachmentID = attachmentID
	}
	return record, true, nil
}

func (s *TopologyStore) PutAttachment(record TopologyAttachmentRecord) (TopologyAttachmentRecord, error) {
	if s == nil || s.store == nil {
		return TopologyAttachmentRecord{}, errors.New("topology store is not configured")
	}
	record = normalizeTopologyAttachmentRecord(record)
	if record.AttachmentID == "" {
		return TopologyAttachmentRecord{}, errors.New("topology attachment id is required")
	}
	if record.HostContainerID == "" {
		return TopologyAttachmentRecord{}, errors.New("topology attachment host container id is required")
	}
	if record.RuntimeSwarmID == "" {
		return TopologyAttachmentRecord{}, errors.New("topology attachment runtime swarm id is required")
	}
	now := time.Now().UnixMilli()
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeyTopologyAttachment(record.AttachmentID), record); err != nil {
		return TopologyAttachmentRecord{}, err
	}
	if _, err := s.refreshMigrationStatus(); err != nil {
		return TopologyAttachmentRecord{}, err
	}
	return record, nil
}

func (s *TopologyStore) DeleteAttachment(attachmentID string) error {
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	attachmentID = normalizeTopologyKeyValue(attachmentID)
	if attachmentID == "" {
		return errors.New("topology attachment id is required")
	}
	if err := s.store.Delete(KeyTopologyAttachment(attachmentID)); err != nil {
		return err
	}
	_, err := s.refreshMigrationStatus()
	return err
}

func (s *TopologyStore) ListAttachments(limit int) ([]TopologyAttachmentRecord, error) {
	return s.listTopologyAttachmentRecords(limit)
}

func (s *TopologyStore) ListAttachmentsByHostContainer(hostContainerID string, limit int) ([]TopologyAttachmentRecord, error) {
	hostContainerID = normalizeTopologyKeyValue(hostContainerID)
	if hostContainerID == "" {
		return nil, errors.New("topology host container id is required")
	}
	records, err := s.listTopologyAttachmentRecords(limit)
	if err != nil {
		return nil, err
	}
	out := make([]TopologyAttachmentRecord, 0, len(records))
	for _, record := range records {
		if strings.EqualFold(strings.TrimSpace(record.HostContainerID), hostContainerID) {
			out = append(out, record)
		}
	}
	return out, nil
}

func (s *TopologyStore) FindAttachmentByRuntime(runtimeSwarmID string) (TopologyAttachmentRecord, bool, error) {
	runtimeSwarmID = normalizeTopologyKeyValue(runtimeSwarmID)
	if runtimeSwarmID == "" {
		return TopologyAttachmentRecord{}, false, errors.New("topology runtime swarm id is required")
	}
	records, err := s.listTopologyAttachmentRecords(100000)
	if err != nil {
		return TopologyAttachmentRecord{}, false, err
	}
	for _, record := range records {
		if strings.EqualFold(strings.TrimSpace(record.RuntimeSwarmID), runtimeSwarmID) {
			return record, true, nil
		}
	}
	return TopologyAttachmentRecord{}, false, nil
}

func (s *TopologyStore) ListWorkspaceBindings(limit int) ([]TopologyWorkspaceBindingRecord, error) {
	return s.listTopologyWorkspaceBindingRecords(limit)
}

func (s *TopologyStore) ListWorkspaceBindingsBySourcePath(sourceWorkspacePath string, limit int) ([]TopologyWorkspaceBindingRecord, error) {
	sourceWorkspacePath = strings.TrimSpace(sourceWorkspacePath)
	if sourceWorkspacePath == "" {
		return nil, errors.New("topology source workspace path is required")
	}
	records, err := s.listTopologyWorkspaceBindingRecords(limit)
	if err != nil {
		return nil, err
	}
	out := make([]TopologyWorkspaceBindingRecord, 0, len(records))
	for _, record := range records {
		if strings.EqualFold(strings.TrimSpace(record.SourceWorkspacePath), sourceWorkspacePath) {
			out = append(out, record)
		}
	}
	return out, nil
}

func (s *TopologyStore) GetWorkspaceBinding(bindingID string) (TopologyWorkspaceBindingRecord, bool, error) {
	if s == nil || s.store == nil {
		return TopologyWorkspaceBindingRecord{}, false, nil
	}
	bindingID = normalizeTopologyKeyValue(bindingID)
	if bindingID == "" {
		return TopologyWorkspaceBindingRecord{}, false, errors.New("topology workspace binding id is required")
	}
	var record TopologyWorkspaceBindingRecord
	ok, err := s.store.GetJSON(KeyTopologyWorkspaceBinding(bindingID), &record)
	if err != nil {
		return TopologyWorkspaceBindingRecord{}, false, err
	}
	if !ok {
		return TopologyWorkspaceBindingRecord{}, false, nil
	}
	record = normalizeTopologyWorkspaceBindingRecord(record)
	if record.BindingID == "" {
		record.BindingID = bindingID
	}
	return record, true, nil
}

func (s *TopologyStore) PutWorkspaceBinding(record TopologyWorkspaceBindingRecord) (TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.store == nil {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology store is not configured")
	}
	record = normalizeTopologyWorkspaceBindingRecord(record)
	if record.BindingID == "" {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology workspace binding id is required")
	}
	if record.SourceWorkspacePath == "" {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology source workspace path is required")
	}
	now := time.Now().UnixMilli()
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeyTopologyWorkspaceBinding(record.BindingID), record); err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	if _, err := s.refreshMigrationStatus(); err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	return record, nil
}

func (s *TopologyStore) DeleteWorkspaceBinding(bindingID string) error {
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	bindingID = normalizeTopologyKeyValue(bindingID)
	if bindingID == "" {
		return errors.New("topology workspace binding id is required")
	}
	if err := s.store.Delete(KeyTopologyWorkspaceBinding(bindingID)); err != nil {
		return err
	}
	_, err := s.refreshMigrationStatus()
	return err
}

func (s *TopologyStore) ListSessionRoutes(limit int) ([]TopologySessionRouteRecord, error) {
	return s.listTopologySessionRouteRecords(limit)
}

func (s *TopologyStore) GetSessionRoute(sessionID string) (TopologySessionRouteRecord, bool, error) {
	if s == nil || s.store == nil {
		return TopologySessionRouteRecord{}, false, nil
	}
	sessionID = normalizeTopologyKeyValue(sessionID)
	if sessionID == "" {
		return TopologySessionRouteRecord{}, false, errors.New("topology session id is required")
	}
	var record TopologySessionRouteRecord
	ok, err := s.store.GetJSON(KeyTopologySessionRoute(sessionID), &record)
	if err != nil {
		return TopologySessionRouteRecord{}, false, err
	}
	if !ok {
		return TopologySessionRouteRecord{}, false, nil
	}
	record = normalizeTopologySessionRouteRecord(record)
	if record.SessionID == "" {
		record.SessionID = sessionID
	}
	return record, true, nil
}

func (s *TopologyStore) PutSessionRoute(record TopologySessionRouteRecord) (TopologySessionRouteRecord, error) {
	if s == nil || s.store == nil {
		return TopologySessionRouteRecord{}, errors.New("topology store is not configured")
	}
	record = normalizeTopologySessionRouteRecord(record)
	if record.SessionID == "" {
		return TopologySessionRouteRecord{}, errors.New("topology session id is required")
	}
	now := time.Now().UnixMilli()
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeyTopologySessionRoute(record.SessionID), record); err != nil {
		return TopologySessionRouteRecord{}, err
	}
	if _, err := s.refreshMigrationStatus(); err != nil {
		return TopologySessionRouteRecord{}, err
	}
	return record, nil
}

func (s *TopologyStore) DeleteSessionRoute(sessionID string) error {
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	sessionID = normalizeTopologyKeyValue(sessionID)
	if sessionID == "" {
		return errors.New("topology session id is required")
	}
	if err := s.store.Delete(KeyTopologySessionRoute(sessionID)); err != nil {
		return err
	}
	_, err := s.refreshMigrationStatus()
	return err
}

func (s *TopologyStore) GetMigrationStatus(id string) (TopologyMigrationStatusRecord, bool, error) {
	if s == nil || s.store == nil {
		return TopologyMigrationStatusRecord{}, false, nil
	}
	id = normalizeTopologyKeyValue(id)
	if id == "" {
		id = DefaultTopologyMigrationStatusID
	}
	var record TopologyMigrationStatusRecord
	ok, err := s.store.GetJSON(KeyTopologyMigrationStatus(id), &record)
	if err != nil {
		return TopologyMigrationStatusRecord{}, false, err
	}
	if !ok {
		return TopologyMigrationStatusRecord{}, false, nil
	}
	record = normalizeTopologyMigrationStatusRecord(record)
	if record.ID == "" {
		record.ID = id
	}
	return record, true, nil
}

func (s *TopologyStore) PutMigrationStatus(record TopologyMigrationStatusRecord) (TopologyMigrationStatusRecord, error) {
	if s == nil || s.store == nil {
		return TopologyMigrationStatusRecord{}, errors.New("topology store is not configured")
	}
	record = normalizeTopologyMigrationStatusRecord(record)
	if record.RebuiltAt <= 0 {
		record.RebuiltAt = time.Now().UnixMilli()
	}
	if err := s.store.PutJSON(KeyTopologyMigrationStatus(record.ID), record); err != nil {
		return TopologyMigrationStatusRecord{}, err
	}
	return record, nil
}

func (s *TopologyStore) RefreshMigrationStatus() (TopologyMigrationStatusRecord, error) {
	if s == nil || s.store == nil {
		return TopologyMigrationStatusRecord{}, errors.New("topology store is not configured")
	}
	return s.refreshMigrationStatus()
}

func (s *TopologyStore) refreshMigrationStatus() (TopologyMigrationStatusRecord, error) {
	runtimes, err := s.ListRuntimes(100000)
	if err != nil {
		return TopologyMigrationStatusRecord{}, err
	}
	hostContainers, err := s.ListHostContainers(100000)
	if err != nil {
		return TopologyMigrationStatusRecord{}, err
	}
	attachments, err := s.ListAttachments(100000)
	if err != nil {
		return TopologyMigrationStatusRecord{}, err
	}
	workspaceBindings, err := s.ListWorkspaceBindings(100000)
	if err != nil {
		return TopologyMigrationStatusRecord{}, err
	}
	sessionRoutes, err := s.ListSessionRoutes(100000)
	if err != nil {
		return TopologyMigrationStatusRecord{}, err
	}
	return s.PutMigrationStatus(TopologyMigrationStatusRecord{
		ID:                    DefaultTopologyMigrationStatusID,
		Version:               TopologySnapshotVersion,
		RebuiltAt:             time.Now().UnixMilli(),
		RuntimeCount:          len(runtimes),
		HostContainerCount:    len(hostContainers),
		AttachmentCount:       len(attachments),
		WorkspaceBindingCount: len(workspaceBindings),
		SessionRouteCount:     len(sessionRoutes),
	})
}

func (s *TopologyStore) listTopologyRuntimeRecords(limit int) ([]TopologyRuntimeRecord, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]TopologyRuntimeRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(TopologyRuntimePrefix(), limit, func(key string, value []byte) error {
		var record TopologyRuntimeRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode topology runtime: %w", err)
		}
		record = normalizeTopologyRuntimeRecord(record)
		if record.SwarmID == "" {
			record.SwarmID = decodeTopologyKeyValue(key, TopologyRuntimePrefix())
		}
		if record.SwarmID == "" {
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
			return strings.ToLower(out[i].SwarmID) < strings.ToLower(out[j].SwarmID)
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

func (s *TopologyStore) listTopologyHostContainerRecords(limit int) ([]TopologyHostContainerRecord, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]TopologyHostContainerRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(TopologyHostContainerPrefix(), limit, func(key string, value []byte) error {
		var record TopologyHostContainerRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode topology host container: %w", err)
		}
		record = normalizeTopologyHostContainerRecord(record)
		if record.HostContainerID == "" {
			record.HostContainerID = decodeTopologyKeyValue(key, TopologyHostContainerPrefix())
		}
		if record.HostContainerID == "" {
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
			return strings.ToLower(out[i].HostContainerID) < strings.ToLower(out[j].HostContainerID)
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

func (s *TopologyStore) listTopologyAttachmentRecords(limit int) ([]TopologyAttachmentRecord, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]TopologyAttachmentRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(TopologyAttachmentPrefix(), limit, func(key string, value []byte) error {
		var record TopologyAttachmentRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode topology attachment: %w", err)
		}
		record = normalizeTopologyAttachmentRecord(record)
		if record.AttachmentID == "" {
			record.AttachmentID = decodeTopologyKeyValue(key, TopologyAttachmentPrefix())
		}
		if record.AttachmentID == "" {
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
			return strings.ToLower(out[i].AttachmentID) < strings.ToLower(out[j].AttachmentID)
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

func (s *TopologyStore) listTopologyWorkspaceBindingRecords(limit int) ([]TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]TopologyWorkspaceBindingRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(TopologyWorkspaceBindingPrefix(), limit, func(key string, value []byte) error {
		var record TopologyWorkspaceBindingRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode topology workspace binding: %w", err)
		}
		record = normalizeTopologyWorkspaceBindingRecord(record)
		if record.BindingID == "" {
			record.BindingID = decodeTopologyKeyValue(key, TopologyWorkspaceBindingPrefix())
		}
		if record.BindingID == "" {
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
			return strings.ToLower(out[i].BindingID) < strings.ToLower(out[j].BindingID)
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

func (s *TopologyStore) listTopologySessionRouteRecords(limit int) ([]TopologySessionRouteRecord, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]TopologySessionRouteRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(TopologySessionRoutePrefix(), limit, func(key string, value []byte) error {
		var record TopologySessionRouteRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode topology session route: %w", err)
		}
		record = normalizeTopologySessionRouteRecord(record)
		if record.SessionID == "" {
			record.SessionID = decodeTopologyKeyValue(key, TopologySessionRoutePrefix())
		}
		if record.SessionID == "" {
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
			return strings.ToLower(out[i].SessionID) < strings.ToLower(out[j].SessionID)
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

func (s *TopologyStore) deletePrefixWithBatch(batch *pebble.Batch, prefix string) error {
	iter, err := s.store.db.NewIter(nil)
	if err != nil {
		return fmt.Errorf("create topology prefix iterator: %w", err)
	}
	defer iter.Close()
	for iter.SeekGE([]byte(prefix)); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if !strings.HasPrefix(key, prefix) {
			break
		}
		if err := batch.Delete(iter.Key(), nil); err != nil {
			return err
		}
	}
	return iter.Error()
}

func normalizeTopologyRuntimeRecords(records []TopologyRuntimeRecord) []TopologyRuntimeRecord {
	if len(records) == 0 {
		return nil
	}
	out := make([]TopologyRuntimeRecord, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, raw := range records {
		record := normalizeTopologyRuntimeRecord(raw)
		if record.SwarmID == "" {
			continue
		}
		if _, ok := seen[record.SwarmID]; ok {
			continue
		}
		seen[record.SwarmID] = struct{}{}
		out = append(out, record)
	}
	return out
}

func normalizeTopologyRuntimeRecord(record TopologyRuntimeRecord) TopologyRuntimeRecord {
	record.SwarmID = normalizeTopologyKeyValue(record.SwarmID)
	record.UserID = strings.TrimSpace(record.UserID)
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.Name = strings.TrimSpace(record.Name)
	record.Role = strings.ToLower(strings.TrimSpace(record.Role))
	record.Relationship = strings.ToLower(strings.TrimSpace(record.Relationship))
	record.BackendURL = strings.TrimSpace(record.BackendURL)
	record.DesktopURL = strings.TrimSpace(record.DesktopURL)
	record.Status = strings.ToLower(strings.TrimSpace(record.Status))
	record.Transport = strings.ToLower(strings.TrimSpace(record.Transport))
	record.OwnerHostSwarmID = strings.TrimSpace(record.OwnerHostSwarmID)
	record.OwnerHostContainerID = strings.TrimSpace(record.OwnerHostContainerID)
	record.GroupIDs = normalizeTopologyStringList(record.GroupIDs)
	record.ObservedSources = normalizeTopologyStringList(record.ObservedSources)
	record.CreatedAt, record.UpdatedAt = normalizeTopologyTimestamps(record.CreatedAt, record.UpdatedAt)
	return record
}

func normalizeTopologyHostContainerRecords(records []TopologyHostContainerRecord) []TopologyHostContainerRecord {
	if len(records) == 0 {
		return nil
	}
	out := make([]TopologyHostContainerRecord, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, raw := range records {
		record := normalizeTopologyHostContainerRecord(raw)
		if record.HostContainerID == "" {
			continue
		}
		if _, ok := seen[record.HostContainerID]; ok {
			continue
		}
		seen[record.HostContainerID] = struct{}{}
		out = append(out, record)
	}
	return out
}

func normalizeTopologyHostContainerRecord(record TopologyHostContainerRecord) TopologyHostContainerRecord {
	record.HostContainerID = normalizeTopologyKeyValue(record.HostContainerID)
	record.UserID = strings.TrimSpace(record.UserID)
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.HostSwarmID = strings.TrimSpace(record.HostSwarmID)
	record.RuntimeContainerRef = strings.TrimSpace(record.RuntimeContainerRef)
	record.Name = strings.TrimSpace(record.Name)
	record.ContainerName = normalizeContainerSlug(record.ContainerName)
	record.ContainerID = strings.TrimSpace(record.ContainerID)
	record.Runtime = normalizeSwarmLocalContainerRuntime(record.Runtime)
	record.Image = strings.TrimSpace(record.Image)
	record.Status = strings.ToLower(strings.TrimSpace(record.Status))
	record.HostAPIBaseURL = strings.TrimSpace(record.HostAPIBaseURL)
	if record.HostPort < 0 {
		record.HostPort = 0
	}
	if record.RuntimePort < 0 {
		record.RuntimePort = 0
	}
	record.Mounts = normalizeSwarmLocalContainerMounts(record.Mounts)
	record.ObservedSources = normalizeTopologyStringList(record.ObservedSources)
	record.CreatedAt, record.UpdatedAt = normalizeTopologyTimestamps(record.CreatedAt, record.UpdatedAt)
	return record
}

func normalizeTopologyAttachmentRecords(records []TopologyAttachmentRecord) []TopologyAttachmentRecord {
	if len(records) == 0 {
		return nil
	}
	out := make([]TopologyAttachmentRecord, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, raw := range records {
		record := normalizeTopologyAttachmentRecord(raw)
		if record.AttachmentID == "" {
			continue
		}
		if _, ok := seen[record.AttachmentID]; ok {
			continue
		}
		seen[record.AttachmentID] = struct{}{}
		out = append(out, record)
	}
	return out
}

func normalizeTopologyAttachmentRecord(record TopologyAttachmentRecord) TopologyAttachmentRecord {
	record.AttachmentID = normalizeTopologyKeyValue(record.AttachmentID)
	record.UserID = strings.TrimSpace(record.UserID)
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.HostContainerID = strings.TrimSpace(record.HostContainerID)
	record.RuntimeSwarmID = strings.TrimSpace(record.RuntimeSwarmID)
	record.State = strings.ToLower(strings.TrimSpace(record.State))
	record.DeploymentID = strings.TrimSpace(record.DeploymentID)
	record.RemoteDeploySessionID = strings.TrimSpace(record.RemoteDeploySessionID)
	record.LastError = strings.TrimSpace(record.LastError)
	record.CreatedAt, record.UpdatedAt = normalizeTopologyTimestamps(record.CreatedAt, record.UpdatedAt)
	return record
}

func normalizeTopologyWorkspaceBindingRecords(records []TopologyWorkspaceBindingRecord) []TopologyWorkspaceBindingRecord {
	if len(records) == 0 {
		return nil
	}
	out := make([]TopologyWorkspaceBindingRecord, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, raw := range records {
		record := normalizeTopologyWorkspaceBindingRecord(raw)
		if record.BindingID == "" {
			continue
		}
		if _, ok := seen[record.BindingID]; ok {
			continue
		}
		seen[record.BindingID] = struct{}{}
		out = append(out, record)
	}
	return out
}

func normalizeTopologyWorkspaceBindingRecord(record TopologyWorkspaceBindingRecord) TopologyWorkspaceBindingRecord {
	record.BindingID = normalizeTopologyKeyValue(record.BindingID)
	record.UserID = strings.TrimSpace(record.UserID)
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.SourceWorkspacePath = strings.TrimSpace(record.SourceWorkspacePath)
	record.SourceWorkspaceName = strings.TrimSpace(record.SourceWorkspaceName)
	record.DestinationRuntimeSwarmID = strings.TrimSpace(record.DestinationRuntimeSwarmID)
	record.DestinationHostSwarmID = strings.TrimSpace(record.DestinationHostSwarmID)
	record.DestinationContainerID = strings.TrimSpace(record.DestinationContainerID)
	record.DestinationWorkspacePath = strings.TrimSpace(record.DestinationWorkspacePath)
	record.ReplicationMode = strings.TrimSpace(record.ReplicationMode)
	record.Sync = normalizeWorkspaceReplicationSync(record.Sync)
	record.LegacyTargetKind = strings.TrimSpace(record.LegacyTargetKind)
	record.CreatedAt, record.UpdatedAt = normalizeTopologyTimestamps(record.CreatedAt, record.UpdatedAt)
	return record
}

func normalizeTopologySessionRouteRecords(records []TopologySessionRouteRecord) []TopologySessionRouteRecord {
	if len(records) == 0 {
		return nil
	}
	out := make([]TopologySessionRouteRecord, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, raw := range records {
		record := normalizeTopologySessionRouteRecord(raw)
		if record.SessionID == "" {
			continue
		}
		if _, ok := seen[record.SessionID]; ok {
			continue
		}
		seen[record.SessionID] = struct{}{}
		out = append(out, record)
	}
	return out
}

func normalizeTopologySessionRouteRecord(record TopologySessionRouteRecord) TopologySessionRouteRecord {
	record.SessionID = normalizeTopologyKeyValue(record.SessionID)
	record.UserID = strings.TrimSpace(record.UserID)
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.RuntimeSwarmID = strings.TrimSpace(record.RuntimeSwarmID)
	record.HostSwarmID = strings.TrimSpace(record.HostSwarmID)
	record.HostContainerID = strings.TrimSpace(record.HostContainerID)
	record.WorkspaceBindingID = strings.TrimSpace(record.WorkspaceBindingID)
	record.BackendURL = strings.TrimSpace(record.BackendURL)
	record.HostWorkspacePath = strings.TrimSpace(record.HostWorkspacePath)
	record.RuntimeWorkspacePath = strings.TrimSpace(record.RuntimeWorkspacePath)
	record.CreatedAt, record.UpdatedAt = normalizeTopologyTimestamps(record.CreatedAt, record.UpdatedAt)
	return record
}

func normalizeTopologyMigrationStatusRecord(record TopologyMigrationStatusRecord) TopologyMigrationStatusRecord {
	record.ID = normalizeTopologyKeyValue(record.ID)
	if record.ID == "" {
		record.ID = DefaultTopologyMigrationStatusID
	}
	record.Version = strings.TrimSpace(record.Version)
	if record.RebuiltAt < 0 {
		record.RebuiltAt = 0
	}
	if record.RuntimeCount < 0 {
		record.RuntimeCount = 0
	}
	if record.HostContainerCount < 0 {
		record.HostContainerCount = 0
	}
	if record.AttachmentCount < 0 {
		record.AttachmentCount = 0
	}
	if record.WorkspaceBindingCount < 0 {
		record.WorkspaceBindingCount = 0
	}
	if record.SessionRouteCount < 0 {
		record.SessionRouteCount = 0
	}
	return record
}

func normalizeTopologyKeyValue(value string) string {
	return strings.TrimSpace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func normalizeTopologyStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
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
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeTopologyTimestamps(createdAt, updatedAt int64) (int64, int64) {
	now := time.Now().UnixMilli()
	if createdAt <= 0 {
		createdAt = now
	}
	if updatedAt <= 0 {
		updatedAt = createdAt
	}
	if createdAt < 0 {
		createdAt = 0
	}
	if updatedAt < 0 {
		updatedAt = createdAt
	}
	return createdAt, updatedAt
}

func decodeTopologyKeyValue(key, prefix string) string {
	if !strings.HasPrefix(key, prefix) {
		return ""
	}
	decoded, err := urlPathUnescape(strings.TrimPrefix(key, prefix))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(decoded)
}

func urlPathUnescape(value string) (string, error) {
	return url.PathUnescape(value)
}
