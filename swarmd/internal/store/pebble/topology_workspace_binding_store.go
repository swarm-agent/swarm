package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func newTopologyWorkspaceBindingID() string {
	return "wsb_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func DeterministicTopologyWorkspaceSelfBindingID(accountScopeID, workspaceID, runtimeSwarmID string) string {
	material := strings.TrimSpace(accountScopeID) + "\x00" + strings.TrimSpace(workspaceID) + "\x00" + strings.TrimSpace(runtimeSwarmID)
	sum := sha256.Sum256([]byte(material))
	return "wsb_self_" + hex.EncodeToString(sum[:16])
}

// deterministicTopologyWorkspaceSelfBindingIDForRecord binds local self-binding identity
// to immutable workspace identity and placement generations. It intentionally excludes
// mutable source/destination paths and names; those remain metadata checked on reuse.
func deterministicTopologyWorkspaceSelfBindingIDForRecord(accountScopeID string, record TopologyWorkspaceBindingRecord) string {
	material := strings.Join([]string{
		strings.TrimSpace(accountScopeID),
		strings.TrimSpace(record.SourceWorkspaceID),
		fmt.Sprintf("%d", record.SourceWorkspaceGeneration),
		strings.TrimSpace(record.DestinationRuntimeSwarmID),
		strings.ToLower(strings.TrimSpace(record.DestinationRuntimeKind)),
		strings.TrimSpace(record.DestinationAuthorityHostSwarmID),
		strings.TrimSpace(record.DestinationHostSwarmID),
		strings.TrimSpace(record.DestinationContainerID),
		fmt.Sprintf("%d", record.PlacementGeneration),
		fmt.Sprintf("%d", record.BindingGeneration),
		strings.ToLower(strings.TrimSpace(record.State)),
		strings.ToLower(strings.TrimSpace(record.AccessMode)),
		strings.ToLower(strings.TrimSpace(record.MaterializationKind)),
	}, "\x00")
	sum := sha256.Sum256([]byte(material))
	return "wsb_self_" + hex.EncodeToString(sum[:16])
}

func (s *TopologyStore) EnsureLocalWorkspaceSelfBindingForAccount(accountScopeID string, record TopologyWorkspaceBindingRecord) (TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.store == nil {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology store is not configured")
	}
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	record = normalizeTopologyWorkspaceBindingRecord(record)
	if record.SourceWorkspaceID == "" {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology source workspace id is required")
	}
	if record.SourceWorkspacePath == "" {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology source workspace path is required")
	}
	if record.SourceWorkspaceGeneration <= 0 {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology source workspace generation is required")
	}
	if record.DestinationRuntimeSwarmID == "" {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology destination runtime swarm id is required")
	}
	if record.DestinationRuntimeKind != TopologyRuntimeKindHost {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology local self workspace binding destination runtime kind must be host")
	}
	if record.DestinationAuthorityHostSwarmID == "" {
		record.DestinationAuthorityHostSwarmID = record.DestinationHostSwarmID
	}
	if record.DestinationHostSwarmID == "" {
		record.DestinationHostSwarmID = record.DestinationAuthorityHostSwarmID
	}
	if record.DestinationAuthorityHostSwarmID != record.DestinationRuntimeSwarmID || record.DestinationHostSwarmID != record.DestinationRuntimeSwarmID {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology local self workspace binding authority host must equal destination runtime swarm id")
	}
	if record.DestinationContainerID != "" {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology local self workspace binding destination container id must be empty")
	}
	if record.DestinationWorkspacePath == "" {
		record.DestinationWorkspacePath = record.SourceWorkspacePath
	}
	if record.PlacementGeneration <= 0 {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology workspace binding placement generation is required")
	}
	placement, ok, err := s.GetRuntimePlacementForAccount(accountScopeID, record.DestinationRuntimeSwarmID)
	if err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	if !ok {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology local self workspace binding runtime placement is required")
	}
	if placement.RuntimeKind != TopologyRuntimeKindHost || placement.RuntimeSwarmID != record.DestinationRuntimeSwarmID || placement.AuthorityHostSwarmID != record.DestinationRuntimeSwarmID || placement.AuthorityContainerID != "" || placement.PlacementGeneration != record.PlacementGeneration || placement.State != TopologyRuntimePlacementStateActive {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology local self workspace binding runtime placement does not match")
	}
	if record.BindingGeneration <= 0 {
		record.BindingGeneration = 1
	}
	if record.State == "" {
		record.State = TopologyWorkspaceBindingStateBound
	}
	if record.State != TopologyWorkspaceBindingStateBound {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology local self workspace binding state must be bound")
	}
	if record.AccessMode == "" {
		record.AccessMode = TopologyWorkspaceBindingAccessModeLocal
	}
	if record.AccessMode != TopologyWorkspaceBindingAccessModeLocal {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology local self workspace binding access mode must be local")
	}
	if record.MaterializationKind == "" {
		record.MaterializationKind = TopologyWorkspaceBindingMaterializationSource
	}
	if record.MaterializationKind != TopologyWorkspaceBindingMaterializationSource {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology local self workspace binding materialization kind must be source")
	}
	if record.AttestedByHostSwarmID == "" {
		record.AttestedByHostSwarmID = record.DestinationRuntimeSwarmID
	}
	if record.AttestedByHostSwarmID != record.DestinationRuntimeSwarmID {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology local self workspace binding attesting host must equal destination runtime swarm id")
	}
	if record.Writable != true {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology local self workspace binding must be writable")
	}
	if record.AttestedAt <= 0 {
		record.AttestedAt = time.Now().UnixMilli()
	}
	record.BindingID = deterministicTopologyWorkspaceSelfBindingIDForRecord(accountScopeID, record)
	record, err = enforceTopologyWorkspaceBindingAccount(accountScopeID, record)
	if err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}

	matches, err := s.activeWorkspaceBindingsForWorkspaceAndRuntime(accountScopeID, record.SourceWorkspaceID, record.DestinationRuntimeSwarmID)
	if err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	if len(matches) > 1 {
		return TopologyWorkspaceBindingRecord{}, errors.New("ambiguous active local workspace self bindings")
	}
	if len(matches) == 1 {
		existing := matches[0]
		if err := validateExistingLocalWorkspaceSelfBinding(existing, record); err != nil {
			return TopologyWorkspaceBindingRecord{}, err
		}
		return existing, nil
	}

	return s.putStrictWorkspaceBindingForAccount(accountScopeID, record)
}

func (s *TopologyStore) activeWorkspaceBindingsForWorkspaceAndRuntime(accountScopeID, workspaceID, runtimeSwarmID string) ([]TopologyWorkspaceBindingRecord, error) {
	records, err := s.ListWorkspaceBindingsForAccount(accountScopeID, 100000)
	if err != nil {
		return nil, err
	}
	out := make([]TopologyWorkspaceBindingRecord, 0, 1)
	for _, record := range records {
		if record.SourceWorkspaceID != workspaceID || record.DestinationRuntimeSwarmID != runtimeSwarmID {
			continue
		}
		if record.State == "" || record.State == TopologyWorkspaceBindingStateBound {
			out = append(out, record)
		}
	}
	return out, nil
}

func validateExistingLocalWorkspaceSelfBinding(existing, desired TopologyWorkspaceBindingRecord) error {
	existing = normalizeTopologyWorkspaceBindingRecord(existing)
	desired = normalizeTopologyWorkspaceBindingRecord(desired)
	if existing.SourceWorkspaceID != desired.SourceWorkspaceID {
		return fmt.Errorf("workspace binding %q source workspace id mismatch", existing.BindingID)
	}
	if existing.SourceWorkspaceGeneration != desired.SourceWorkspaceGeneration {
		return fmt.Errorf("workspace binding %q source workspace generation mismatch", existing.BindingID)
	}
	if existing.SourceWorkspacePath != desired.SourceWorkspacePath {
		return fmt.Errorf("workspace binding %q source workspace path mismatch", existing.BindingID)
	}
	if existing.DestinationRuntimeSwarmID != desired.DestinationRuntimeSwarmID {
		return fmt.Errorf("workspace binding %q destination runtime mismatch", existing.BindingID)
	}
	if existing.DestinationRuntimeKind != desired.DestinationRuntimeKind {
		return fmt.Errorf("workspace binding %q destination runtime kind mismatch", existing.BindingID)
	}
	if existing.DestinationAuthorityHostSwarmID != desired.DestinationAuthorityHostSwarmID {
		return fmt.Errorf("workspace binding %q destination authority host mismatch", existing.BindingID)
	}
	if existing.DestinationHostSwarmID != desired.DestinationHostSwarmID {
		return fmt.Errorf("workspace binding %q destination host mismatch", existing.BindingID)
	}
	if existing.DestinationContainerID != desired.DestinationContainerID {
		return fmt.Errorf("workspace binding %q destination container id mismatch", existing.BindingID)
	}
	if existing.DestinationWorkspacePath != desired.DestinationWorkspacePath {
		return fmt.Errorf("workspace binding %q destination workspace path mismatch", existing.BindingID)
	}
	if existing.PlacementGeneration != desired.PlacementGeneration {
		return fmt.Errorf("workspace binding %q placement generation mismatch", existing.BindingID)
	}
	if existing.BindingGeneration != desired.BindingGeneration {
		return fmt.Errorf("workspace binding %q binding generation mismatch", existing.BindingID)
	}
	if existing.State != desired.State {
		return fmt.Errorf("workspace binding %q state mismatch", existing.BindingID)
	}
	if existing.AccessMode != desired.AccessMode {
		return fmt.Errorf("workspace binding %q access mode mismatch", existing.BindingID)
	}
	if existing.MaterializationKind != desired.MaterializationKind {
		return fmt.Errorf("workspace binding %q materialization kind mismatch", existing.BindingID)
	}
	if existing.AttestedByHostSwarmID != desired.AttestedByHostSwarmID {
		return fmt.Errorf("workspace binding %q attesting host mismatch", existing.BindingID)
	}
	if existing.Writable != desired.Writable {
		return fmt.Errorf("workspace binding %q writable mismatch", existing.BindingID)
	}
	if existing.BindingID != desired.BindingID {
		return fmt.Errorf("workspace binding %q deterministic binding id mismatch", existing.BindingID)
	}
	return nil
}
