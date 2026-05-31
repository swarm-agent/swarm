package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// SnapshotForAccount reads only account-scoped topology keys. It intentionally
// does not fall back to legacy/global topology prefixes; those prefixes are
// migration inputs only after Checkpoint 1.2.
func (s *TopologyStore) SnapshotForAccount(accountScopeID string) (TopologySnapshot, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologySnapshot{}, err
	}
	runtimes, err := s.ListRuntimesForAccount(accountScopeID, 100000)
	if err != nil {
		return TopologySnapshot{}, err
	}
	runtimePlacements, err := s.ListRuntimePlacementsForAccount(accountScopeID, 100000)
	if err != nil {
		return TopologySnapshot{}, err
	}
	hostContainers, err := s.ListHostContainersForAccount(accountScopeID, 100000)
	if err != nil {
		return TopologySnapshot{}, err
	}
	attachments, err := s.ListAttachmentsForAccount(accountScopeID, 100000)
	if err != nil {
		return TopologySnapshot{}, err
	}
	workspaceBindings, err := s.ListWorkspaceBindingsForAccount(accountScopeID, 100000)
	if err != nil {
		return TopologySnapshot{}, err
	}
	sessionRoutes, err := s.ListSessionRoutesForAccount(accountScopeID, 100000)
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
		MigrationStatus: TopologyMigrationStatusRecord{
			ID:                    DefaultTopologyMigrationStatusID,
			Version:               TopologySnapshotVersion,
			RebuiltAt:             time.Now().UnixMilli(),
			RuntimeCount:          len(runtimes),
			HostContainerCount:    len(hostContainers),
			AttachmentCount:       len(attachments),
			WorkspaceBindingCount: len(workspaceBindings),
			SessionRouteCount:     len(sessionRoutes),
		},
	}, nil
}

// ReplaceSnapshotForAccount replaces only one account's topology records. It
// never deletes or reads legacy/global topology keys and never touches another
// account's keyspace.
func (s *TopologyStore) ReplaceSnapshotForAccount(accountScopeID string, snapshot TopologySnapshot) error {
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	snapshot.Runtimes = normalizeTopologyRuntimeRecords(snapshot.Runtimes)
	snapshot.RuntimePlacements = normalizeTopologyRuntimePlacementRecords(snapshot.RuntimePlacements)
	snapshot.HostContainers = normalizeTopologyHostContainerRecords(snapshot.HostContainers)
	snapshot.Attachments = normalizeTopologyAttachmentRecords(snapshot.Attachments)
	snapshot.WorkspaceBindings = normalizeTopologyWorkspaceBindingRecords(snapshot.WorkspaceBindings)
	snapshot.SessionRoutes = normalizeTopologySessionRouteRecords(snapshot.SessionRoutes)
	for i := range snapshot.Runtimes {
		if snapshot.Runtimes[i], err = enforceTopologyRuntimeAccount(accountScopeID, snapshot.Runtimes[i]); err != nil {
			return err
		}
	}
	for i := range snapshot.RuntimePlacements {
		if snapshot.RuntimePlacements[i], err = enforceTopologyRuntimePlacementAccount(accountScopeID, snapshot.RuntimePlacements[i]); err != nil {
			return err
		}
		if snapshot.RuntimePlacements[i].PlacementID == "" {
			snapshot.RuntimePlacements[i].PlacementID = legacyTopologyRuntimePlacementID(snapshot.RuntimePlacements[i].AccountScopeID, snapshot.RuntimePlacements[i].RuntimeSwarmID)
		}
		if err := validateTopologyRuntimePlacement(snapshot.RuntimePlacements[i]); err != nil {
			return err
		}
	}
	for i := range snapshot.HostContainers {
		if snapshot.HostContainers[i], err = enforceTopologyHostContainerAccount(accountScopeID, snapshot.HostContainers[i]); err != nil {
			return err
		}
	}
	for i := range snapshot.Attachments {
		if snapshot.Attachments[i], err = enforceTopologyAttachmentAccount(accountScopeID, snapshot.Attachments[i]); err != nil {
			return err
		}
	}
	for i := range snapshot.WorkspaceBindings {
		if snapshot.WorkspaceBindings[i], err = enforceTopologyWorkspaceBindingAccount(accountScopeID, snapshot.WorkspaceBindings[i]); err != nil {
			return err
		}
	}
	for i := range snapshot.SessionRoutes {
		if snapshot.SessionRoutes[i], err = enforceTopologySessionRouteAccount(accountScopeID, snapshot.SessionRoutes[i]); err != nil {
			return err
		}
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	for _, prefix := range []string{
		TopologyRuntimePrefixForAccount(accountScopeID),
		TopologyRuntimePlacementPrefixForAccount(accountScopeID),
		TopologyHostContainerPrefixForAccount(accountScopeID),
		TopologyAttachmentPrefixForAccount(accountScopeID),
		TopologyWorkspaceBindingPrefixForAccount(accountScopeID),
		TopologySessionRoutePrefixForAccount(accountScopeID),
	} {
		if err := s.deletePrefixWithBatch(batch, prefix); err != nil {
			return err
		}
	}
	for _, record := range snapshot.Runtimes {
		if err := setTopologyBatchJSON(batch, KeyTopologyRuntimeForAccount(accountScopeID, record.SwarmID), record); err != nil {
			return fmt.Errorf("marshal topology runtime %q: %w", record.SwarmID, err)
		}
	}
	for _, record := range snapshot.RuntimePlacements {
		if err := setTopologyBatchJSON(batch, KeyTopologyRuntimePlacementForAccount(accountScopeID, record.RuntimeSwarmID), record); err != nil {
			return fmt.Errorf("marshal topology runtime placement %q: %w", record.RuntimeSwarmID, err)
		}
	}
	for _, record := range snapshot.HostContainers {
		if err := setTopologyBatchJSON(batch, KeyTopologyHostContainerForAccount(accountScopeID, record.HostContainerID), record); err != nil {
			return fmt.Errorf("marshal topology host container %q: %w", record.HostContainerID, err)
		}
	}
	for _, record := range snapshot.Attachments {
		if err := setTopologyBatchJSON(batch, KeyTopologyAttachmentForAccount(accountScopeID, record.AttachmentID), record); err != nil {
			return fmt.Errorf("marshal topology attachment %q: %w", record.AttachmentID, err)
		}
	}
	for _, record := range snapshot.WorkspaceBindings {
		if err := setTopologyBatchJSON(batch, KeyTopologyWorkspaceBindingForAccount(accountScopeID, record.BindingID), record); err != nil {
			return fmt.Errorf("marshal topology workspace binding %q: %w", record.BindingID, err)
		}
	}
	for _, record := range snapshot.SessionRoutes {
		if err := setTopologyBatchJSON(batch, KeyTopologySessionRouteForAccount(accountScopeID, record.SessionID), record); err != nil {
			return fmt.Errorf("marshal topology session route %q: %w", record.SessionID, err)
		}
	}
	return batch.Commit(nil)
}

func (s *TopologyStore) ListRuntimesForAccount(accountScopeID string, limit int) ([]TopologyRuntimeRecord, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return nil, err
	}
	return s.listTopologyRuntimeRecordsForAccount(accountScopeID, limit)
}

func (s *TopologyStore) GetRuntimeForAccount(accountScopeID, swarmID string) (TopologyRuntimeRecord, bool, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologyRuntimeRecord{}, false, err
	}
	if s == nil || s.store == nil {
		return TopologyRuntimeRecord{}, false, errors.New("topology store is not configured")
	}
	swarmID = normalizeTopologyKeyValue(swarmID)
	if swarmID == "" {
		return TopologyRuntimeRecord{}, false, errors.New("topology runtime swarm id is required")
	}
	var record TopologyRuntimeRecord
	ok, err := s.store.GetJSON(KeyTopologyRuntimeForAccount(accountScopeID, swarmID), &record)
	if err != nil || !ok {
		return TopologyRuntimeRecord{}, ok, err
	}
	record = normalizeTopologyRuntimeRecord(record)
	if record.SwarmID == "" {
		record.SwarmID = swarmID
	}
	record, err = enforceTopologyRuntimeAccount(accountScopeID, record)
	if err != nil {
		return TopologyRuntimeRecord{}, false, err
	}
	return record, true, nil
}

func (s *TopologyStore) PutRuntimeForAccount(accountScopeID string, record TopologyRuntimeRecord) (TopologyRuntimeRecord, error) {
	if s == nil || s.store == nil {
		return TopologyRuntimeRecord{}, errors.New("topology store is not configured")
	}
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologyRuntimeRecord{}, err
	}
	record = normalizeTopologyRuntimeRecord(record)
	if record.SwarmID == "" {
		return TopologyRuntimeRecord{}, errors.New("topology runtime swarm id is required")
	}
	if record.Name == "" {
		record.Name = record.SwarmID
	}
	if record, err = enforceTopologyRuntimeAccount(accountScopeID, record); err != nil {
		return TopologyRuntimeRecord{}, err
	}
	record.CreatedAt, record.UpdatedAt = nextTopologyWriteTimestamps(record.CreatedAt)
	if err := s.store.PutJSON(KeyTopologyRuntimeForAccount(accountScopeID, record.SwarmID), record); err != nil {
		return TopologyRuntimeRecord{}, err
	}
	return record, nil
}

func (s *TopologyStore) DeleteRuntimeForAccount(accountScopeID, swarmID string) error {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	swarmID = normalizeTopologyKeyValue(swarmID)
	if swarmID == "" {
		return errors.New("topology runtime swarm id is required")
	}
	if err := s.store.Delete(KeyTopologyRuntimeForAccount(accountScopeID, swarmID)); err != nil {
		return err
	}
	return s.DeleteRuntimePlacementForAccount(accountScopeID, swarmID)
}

func (s *TopologyStore) ListHostContainersForAccount(accountScopeID string, limit int) ([]TopologyHostContainerRecord, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return nil, err
	}
	return s.listTopologyHostContainerRecordsForAccount(accountScopeID, limit)
}

func (s *TopologyStore) ListHostContainersByHostForAccount(accountScopeID, hostSwarmID string, limit int) ([]TopologyHostContainerRecord, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return nil, err
	}
	hostSwarmID = normalizeTopologyKeyValue(hostSwarmID)
	if hostSwarmID == "" {
		return nil, errors.New("topology host swarm id is required")
	}
	records, err := s.listTopologyHostContainerRecordsForAccount(accountScopeID, limit)
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

func (s *TopologyStore) GetHostContainerForAccount(accountScopeID, hostContainerID string) (TopologyHostContainerRecord, bool, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologyHostContainerRecord{}, false, err
	}
	if s == nil || s.store == nil {
		return TopologyHostContainerRecord{}, false, errors.New("topology store is not configured")
	}
	hostContainerID = normalizeTopologyKeyValue(hostContainerID)
	if hostContainerID == "" {
		return TopologyHostContainerRecord{}, false, errors.New("topology host container id is required")
	}
	var record TopologyHostContainerRecord
	ok, err := s.store.GetJSON(KeyTopologyHostContainerForAccount(accountScopeID, hostContainerID), &record)
	if err != nil || !ok {
		return TopologyHostContainerRecord{}, ok, err
	}
	record = normalizeTopologyHostContainerRecord(record)
	if record.HostContainerID == "" {
		record.HostContainerID = hostContainerID
	}
	record, err = enforceTopologyHostContainerAccount(accountScopeID, record)
	if err != nil {
		return TopologyHostContainerRecord{}, false, err
	}
	return record, true, nil
}

func (s *TopologyStore) PutHostContainerForAccount(accountScopeID string, record TopologyHostContainerRecord) (TopologyHostContainerRecord, error) {
	if s == nil || s.store == nil {
		return TopologyHostContainerRecord{}, errors.New("topology store is not configured")
	}
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologyHostContainerRecord{}, err
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
	if record, err = enforceTopologyHostContainerAccount(accountScopeID, record); err != nil {
		return TopologyHostContainerRecord{}, err
	}
	record.CreatedAt, record.UpdatedAt = nextTopologyWriteTimestamps(record.CreatedAt)
	if err := s.store.PutJSON(KeyTopologyHostContainerForAccount(accountScopeID, record.HostContainerID), record); err != nil {
		return TopologyHostContainerRecord{}, err
	}
	return record, nil
}

func (s *TopologyStore) DeleteHostContainerForAccount(accountScopeID, hostContainerID string) error {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	hostContainerID = normalizeTopologyKeyValue(hostContainerID)
	if hostContainerID == "" {
		return errors.New("topology host container id is required")
	}
	return s.store.Delete(KeyTopologyHostContainerForAccount(accountScopeID, hostContainerID))
}

func (s *TopologyStore) ListAttachmentsForAccount(accountScopeID string, limit int) ([]TopologyAttachmentRecord, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return nil, err
	}
	return s.listTopologyAttachmentRecordsForAccount(accountScopeID, limit)
}

func (s *TopologyStore) ListAttachmentsByHostContainerForAccount(accountScopeID, hostContainerID string, limit int) ([]TopologyAttachmentRecord, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return nil, err
	}
	hostContainerID = normalizeTopologyKeyValue(hostContainerID)
	if hostContainerID == "" {
		return nil, errors.New("topology host container id is required")
	}
	records, err := s.listTopologyAttachmentRecordsForAccount(accountScopeID, limit)
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

func (s *TopologyStore) FindAttachmentByRuntimeForAccount(accountScopeID, runtimeSwarmID string) (TopologyAttachmentRecord, bool, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologyAttachmentRecord{}, false, err
	}
	runtimeSwarmID = normalizeTopologyKeyValue(runtimeSwarmID)
	if runtimeSwarmID == "" {
		return TopologyAttachmentRecord{}, false, errors.New("topology runtime swarm id is required")
	}
	records, err := s.listTopologyAttachmentRecordsForAccount(accountScopeID, 100000)
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

func (s *TopologyStore) GetAttachmentForAccount(accountScopeID, attachmentID string) (TopologyAttachmentRecord, bool, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologyAttachmentRecord{}, false, err
	}
	if s == nil || s.store == nil {
		return TopologyAttachmentRecord{}, false, errors.New("topology store is not configured")
	}
	attachmentID = normalizeTopologyKeyValue(attachmentID)
	if attachmentID == "" {
		return TopologyAttachmentRecord{}, false, errors.New("topology attachment id is required")
	}
	var record TopologyAttachmentRecord
	ok, err := s.store.GetJSON(KeyTopologyAttachmentForAccount(accountScopeID, attachmentID), &record)
	if err != nil || !ok {
		return TopologyAttachmentRecord{}, ok, err
	}
	record = normalizeTopologyAttachmentRecord(record)
	if record.AttachmentID == "" {
		record.AttachmentID = attachmentID
	}
	record, err = enforceTopologyAttachmentAccount(accountScopeID, record)
	if err != nil {
		return TopologyAttachmentRecord{}, false, err
	}
	return record, true, nil
}

func (s *TopologyStore) PutAttachmentForAccount(accountScopeID string, record TopologyAttachmentRecord) (TopologyAttachmentRecord, error) {
	if s == nil || s.store == nil {
		return TopologyAttachmentRecord{}, errors.New("topology store is not configured")
	}
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologyAttachmentRecord{}, err
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
	if record, err = enforceTopologyAttachmentAccount(accountScopeID, record); err != nil {
		return TopologyAttachmentRecord{}, err
	}
	record.CreatedAt, record.UpdatedAt = nextTopologyWriteTimestamps(record.CreatedAt)
	if err := s.store.PutJSON(KeyTopologyAttachmentForAccount(accountScopeID, record.AttachmentID), record); err != nil {
		return TopologyAttachmentRecord{}, err
	}
	return record, nil
}

func (s *TopologyStore) DeleteAttachmentForAccount(accountScopeID, attachmentID string) error {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	attachmentID = normalizeTopologyKeyValue(attachmentID)
	if attachmentID == "" {
		return errors.New("topology attachment id is required")
	}
	return s.store.Delete(KeyTopologyAttachmentForAccount(accountScopeID, attachmentID))
}

func (s *TopologyStore) ListWorkspaceBindingsForAccount(accountScopeID string, limit int) ([]TopologyWorkspaceBindingRecord, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return nil, err
	}
	return s.listTopologyWorkspaceBindingRecordsForAccount(accountScopeID, limit)
}

func (s *TopologyStore) ListWorkspaceBindingsBySourcePathForAccount(accountScopeID, sourceWorkspacePath string, limit int) ([]TopologyWorkspaceBindingRecord, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return nil, err
	}
	sourceWorkspacePath = strings.TrimSpace(sourceWorkspacePath)
	if sourceWorkspacePath == "" {
		return nil, errors.New("topology source workspace path is required")
	}
	records, err := s.listTopologyWorkspaceBindingRecordsForAccount(accountScopeID, limit)
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

func (s *TopologyStore) GetWorkspaceBindingForAccount(accountScopeID, bindingID string) (TopologyWorkspaceBindingRecord, bool, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologyWorkspaceBindingRecord{}, false, err
	}
	if s == nil || s.store == nil {
		return TopologyWorkspaceBindingRecord{}, false, errors.New("topology store is not configured")
	}
	bindingID = normalizeTopologyKeyValue(bindingID)
	if bindingID == "" {
		return TopologyWorkspaceBindingRecord{}, false, errors.New("topology workspace binding id is required")
	}
	var record TopologyWorkspaceBindingRecord
	ok, err := s.store.GetJSON(KeyTopologyWorkspaceBindingForAccount(accountScopeID, bindingID), &record)
	if err != nil || !ok {
		return TopologyWorkspaceBindingRecord{}, ok, err
	}
	record = normalizeTopologyWorkspaceBindingRecord(record)
	if record.BindingID == "" {
		record.BindingID = bindingID
	}
	record, err = enforceTopologyWorkspaceBindingAccount(accountScopeID, record)
	if err != nil {
		return TopologyWorkspaceBindingRecord{}, false, err
	}
	return record, true, nil
}

func (s *TopologyStore) PutWorkspaceBindingForAccount(accountScopeID string, record TopologyWorkspaceBindingRecord) (TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.store == nil {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology store is not configured")
	}
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	record = normalizeTopologyWorkspaceBindingRecord(record)
	if record.BindingID == "" {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology workspace binding id is required")
	}
	if record.SourceWorkspacePath == "" {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology source workspace path is required")
	}
	if record, err = enforceTopologyWorkspaceBindingAccount(accountScopeID, record); err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	record.CreatedAt, record.UpdatedAt = nextTopologyWriteTimestamps(record.CreatedAt)
	if err := s.store.PutJSON(KeyTopologyWorkspaceBindingForAccount(accountScopeID, record.BindingID), record); err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	return record, nil
}

func (s *TopologyStore) DeleteWorkspaceBindingForAccount(accountScopeID, bindingID string) error {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	bindingID = normalizeTopologyKeyValue(bindingID)
	if bindingID == "" {
		return errors.New("topology workspace binding id is required")
	}
	return s.store.Delete(KeyTopologyWorkspaceBindingForAccount(accountScopeID, bindingID))
}

func (s *TopologyStore) ListSessionRoutesForAccount(accountScopeID string, limit int) ([]TopologySessionRouteRecord, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return nil, err
	}
	return s.listTopologySessionRouteRecordsForAccount(accountScopeID, limit)
}

func (s *TopologyStore) GetSessionRouteForAccount(accountScopeID, sessionID string) (TopologySessionRouteRecord, bool, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologySessionRouteRecord{}, false, err
	}
	if s == nil || s.store == nil {
		return TopologySessionRouteRecord{}, false, errors.New("topology store is not configured")
	}
	sessionID = normalizeTopologyKeyValue(sessionID)
	if sessionID == "" {
		return TopologySessionRouteRecord{}, false, errors.New("topology session id is required")
	}
	var record TopologySessionRouteRecord
	ok, err := s.store.GetJSON(KeyTopologySessionRouteForAccount(accountScopeID, sessionID), &record)
	if err != nil || !ok {
		return TopologySessionRouteRecord{}, ok, err
	}
	record = normalizeTopologySessionRouteRecord(record)
	if record.SessionID == "" {
		record.SessionID = sessionID
	}
	record, err = enforceTopologySessionRouteAccount(accountScopeID, record)
	if err != nil {
		return TopologySessionRouteRecord{}, false, err
	}
	return record, true, nil
}

func (s *TopologyStore) PutSessionRouteForAccount(accountScopeID string, record TopologySessionRouteRecord) (TopologySessionRouteRecord, error) {
	if s == nil || s.store == nil {
		return TopologySessionRouteRecord{}, errors.New("topology store is not configured")
	}
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologySessionRouteRecord{}, err
	}
	record = normalizeTopologySessionRouteRecord(record)
	if record.SessionID == "" {
		return TopologySessionRouteRecord{}, errors.New("topology session id is required")
	}
	if record, err = enforceTopologySessionRouteAccount(accountScopeID, record); err != nil {
		return TopologySessionRouteRecord{}, err
	}
	record.CreatedAt, record.UpdatedAt = nextTopologyWriteTimestamps(record.CreatedAt)
	if err := s.store.PutJSON(KeyTopologySessionRouteForAccount(accountScopeID, record.SessionID), record); err != nil {
		return TopologySessionRouteRecord{}, err
	}
	return record, nil
}

func (s *TopologyStore) DeleteSessionRouteForAccount(accountScopeID, sessionID string) error {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	sessionID = normalizeTopologyKeyValue(sessionID)
	if sessionID == "" {
		return errors.New("topology session id is required")
	}
	return s.store.Delete(KeyTopologySessionRouteForAccount(accountScopeID, sessionID))
}

func (s *TopologyStore) listTopologyRuntimeRecordsForAccount(accountScopeID string, limit int) ([]TopologyRuntimeRecord, error) {
	out, err := listTopologyRecordsForAccount(s, TopologyRuntimePrefixForAccount(accountScopeID), func(key string, value []byte) (TopologyRuntimeRecord, bool, error) {
		var record TopologyRuntimeRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return TopologyRuntimeRecord{}, false, fmt.Errorf("decode topology runtime: %w", err)
		}
		record = normalizeTopologyRuntimeRecord(record)
		if record.SwarmID == "" {
			record.SwarmID = decodeTopologyKeyValue(key, TopologyRuntimePrefixForAccount(accountScopeID))
		}
		if record.SwarmID == "" {
			return TopologyRuntimeRecord{}, false, nil
		}
		var err error
		record, err = enforceTopologyRuntimeAccount(accountScopeID, record)
		return record, err == nil, err
	}, limit)
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

func (s *TopologyStore) listTopologyHostContainerRecordsForAccount(accountScopeID string, limit int) ([]TopologyHostContainerRecord, error) {
	out, err := listTopologyRecordsForAccount(s, TopologyHostContainerPrefixForAccount(accountScopeID), func(key string, value []byte) (TopologyHostContainerRecord, bool, error) {
		var record TopologyHostContainerRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return TopologyHostContainerRecord{}, false, fmt.Errorf("decode topology host container: %w", err)
		}
		record = normalizeTopologyHostContainerRecord(record)
		if record.HostContainerID == "" {
			record.HostContainerID = decodeTopologyKeyValue(key, TopologyHostContainerPrefixForAccount(accountScopeID))
		}
		if record.HostContainerID == "" {
			return TopologyHostContainerRecord{}, false, nil
		}
		var err error
		record, err = enforceTopologyHostContainerAccount(accountScopeID, record)
		return record, err == nil, err
	}, limit)
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

func (s *TopologyStore) listTopologyAttachmentRecordsForAccount(accountScopeID string, limit int) ([]TopologyAttachmentRecord, error) {
	out, err := listTopologyRecordsForAccount(s, TopologyAttachmentPrefixForAccount(accountScopeID), func(key string, value []byte) (TopologyAttachmentRecord, bool, error) {
		var record TopologyAttachmentRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return TopologyAttachmentRecord{}, false, fmt.Errorf("decode topology attachment: %w", err)
		}
		record = normalizeTopologyAttachmentRecord(record)
		if record.AttachmentID == "" {
			record.AttachmentID = decodeTopologyKeyValue(key, TopologyAttachmentPrefixForAccount(accountScopeID))
		}
		if record.AttachmentID == "" {
			return TopologyAttachmentRecord{}, false, nil
		}
		var err error
		record, err = enforceTopologyAttachmentAccount(accountScopeID, record)
		return record, err == nil, err
	}, limit)
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

func (s *TopologyStore) listTopologyWorkspaceBindingRecordsForAccount(accountScopeID string, limit int) ([]TopologyWorkspaceBindingRecord, error) {
	out, err := listTopologyRecordsForAccount(s, TopologyWorkspaceBindingPrefixForAccount(accountScopeID), func(key string, value []byte) (TopologyWorkspaceBindingRecord, bool, error) {
		var record TopologyWorkspaceBindingRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return TopologyWorkspaceBindingRecord{}, false, fmt.Errorf("decode topology workspace binding: %w", err)
		}
		record = normalizeTopologyWorkspaceBindingRecord(record)
		if record.BindingID == "" {
			record.BindingID = decodeTopologyKeyValue(key, TopologyWorkspaceBindingPrefixForAccount(accountScopeID))
		}
		if record.BindingID == "" {
			return TopologyWorkspaceBindingRecord{}, false, nil
		}
		var err error
		record, err = enforceTopologyWorkspaceBindingAccount(accountScopeID, record)
		return record, err == nil, err
	}, limit)
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

func (s *TopologyStore) listTopologySessionRouteRecordsForAccount(accountScopeID string, limit int) ([]TopologySessionRouteRecord, error) {
	out, err := listTopologyRecordsForAccount(s, TopologySessionRoutePrefixForAccount(accountScopeID), func(key string, value []byte) (TopologySessionRouteRecord, bool, error) {
		var record TopologySessionRouteRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return TopologySessionRouteRecord{}, false, fmt.Errorf("decode topology session route: %w", err)
		}
		record = normalizeTopologySessionRouteRecord(record)
		if record.SessionID == "" {
			record.SessionID = decodeTopologyKeyValue(key, TopologySessionRoutePrefixForAccount(accountScopeID))
		}
		if record.SessionID == "" {
			return TopologySessionRouteRecord{}, false, nil
		}
		var err error
		record, err = enforceTopologySessionRouteAccount(accountScopeID, record)
		return record, err == nil, err
	}, limit)
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

func listTopologyRecordsForAccount[T any](s *TopologyStore, prefix string, decode func(key string, value []byte) (T, bool, error), limit int) ([]T, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("topology store is not configured")
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]T, 0, min(limit, 16))
	err := s.store.IteratePrefix(prefix, limit, func(key string, value []byte) error {
		record, ok, err := decode(key, value)
		if err != nil {
			return err
		}
		if ok {
			out = append(out, record)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func setTopologyBatchJSON(batch interface {
	Set([]byte, []byte, *pebble.WriteOptions) error
}, key string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return batch.Set([]byte(key), payload, nil)
}

func requireTopologyAccountScopeID(accountScopeID string) (string, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return "", errors.New("account scope id is required")
	}
	return accountScopeID, nil
}

func nextTopologyWriteTimestamps(createdAt int64) (int64, int64) {
	now := time.Now().UnixMilli()
	if createdAt <= 0 {
		createdAt = now
	}
	return createdAt, now
}

func enforceTopologyRuntimeAccount(accountScopeID string, record TopologyRuntimeRecord) (TopologyRuntimeRecord, error) {
	if err := validateTopologyRecordAccount(accountScopeID, record.UserID, record.AccountScopeID); err != nil {
		return TopologyRuntimeRecord{}, err
	}
	record.AccountScopeID = strings.TrimSpace(accountScopeID)
	record.UserID = strings.TrimSpace(record.UserID)
	return record, nil
}

func enforceTopologyHostContainerAccount(accountScopeID string, record TopologyHostContainerRecord) (TopologyHostContainerRecord, error) {
	if err := validateTopologyRecordAccount(accountScopeID, record.UserID, record.AccountScopeID); err != nil {
		return TopologyHostContainerRecord{}, err
	}
	record.AccountScopeID = strings.TrimSpace(accountScopeID)
	record.UserID = strings.TrimSpace(record.UserID)
	return record, nil
}

func enforceTopologyAttachmentAccount(accountScopeID string, record TopologyAttachmentRecord) (TopologyAttachmentRecord, error) {
	if err := validateTopologyRecordAccount(accountScopeID, record.UserID, record.AccountScopeID); err != nil {
		return TopologyAttachmentRecord{}, err
	}
	record.AccountScopeID = strings.TrimSpace(accountScopeID)
	record.UserID = strings.TrimSpace(record.UserID)
	return record, nil
}

func enforceTopologyWorkspaceBindingAccount(accountScopeID string, record TopologyWorkspaceBindingRecord) (TopologyWorkspaceBindingRecord, error) {
	if err := validateTopologyRecordAccount(accountScopeID, record.UserID, record.AccountScopeID); err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	record.AccountScopeID = strings.TrimSpace(accountScopeID)
	record.UserID = strings.TrimSpace(record.UserID)
	return record, nil
}

func enforceTopologySessionRouteAccount(accountScopeID string, record TopologySessionRouteRecord) (TopologySessionRouteRecord, error) {
	if err := validateTopologyRecordAccount(accountScopeID, record.UserID, record.AccountScopeID); err != nil {
		return TopologySessionRouteRecord{}, err
	}
	record.AccountScopeID = strings.TrimSpace(accountScopeID)
	record.UserID = strings.TrimSpace(record.UserID)
	return record, nil
}

func validateTopologyRecordAccount(accountScopeID, userID, recordAccountScopeID string) error {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return errors.New("account scope id is required")
	}
	if strings.TrimSpace(userID) == "" {
		return errors.New("topology record user id is required")
	}
	recordAccountScopeID = strings.TrimSpace(recordAccountScopeID)
	if recordAccountScopeID == "" {
		return errors.New("topology record account scope id is required")
	}
	if recordAccountScopeID != accountScopeID {
		return errors.New("topology record account scope id does not match account key")
	}
	return nil
}
