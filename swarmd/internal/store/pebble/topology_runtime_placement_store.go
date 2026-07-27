package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

const (
	TopologyRuntimeKindHost = "host"

	// TopologyRuntimeKindContainer remains part of protected V3 and generic topology data.
	// Runtime placement validation treats it like every other non-empty kind.
	TopologyRuntimeKindContainer = "container"

	TopologyRuntimePlacementStateActive = "active"
)

type TopologyRuntimePlacementRecord struct {
	PlacementID          string `json:"placement_id"`
	RuntimeSwarmID       string `json:"runtime_swarm_id"`
	AccountScopeID       string `json:"account_scope_id"`
	AuthorityHostSwarmID string `json:"authority_host_swarm_id"`
	AuthorityContainerID string `json:"authority_container_id"`
	RuntimeKind          string `json:"runtime_kind"`
	PlacementGeneration  int    `json:"placement_generation"`
	State                string `json:"state"`
	CreatedAt            int64  `json:"created_at"`
	UpdatedAt            int64  `json:"updated_at"`
}

func (s *TopologyStore) ListRuntimePlacementsForAccount(accountScopeID string, limit int) ([]TopologyRuntimePlacementRecord, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return nil, err
	}
	if s == nil || s.store == nil {
		return nil, errors.New("topology store is not configured")
	}
	return s.listTopologyRuntimePlacementRecordsForAccount(accountScopeID, limit)
}

func (s *TopologyStore) GetRuntimePlacementForAccount(accountScopeID, runtimeSwarmID string) (TopologyRuntimePlacementRecord, bool, error) {
	record, ok, err := s.getRuntimePlacementForAccountRaw(accountScopeID, runtimeSwarmID)
	if err != nil || !ok {
		return record, ok, err
	}
	if err := validateTopologyRuntimePlacement(record); err != nil {
		return TopologyRuntimePlacementRecord{}, false, err
	}
	return record, true, nil
}

func (s *TopologyStore) getRuntimePlacementForAccountRaw(accountScopeID, runtimeSwarmID string) (TopologyRuntimePlacementRecord, bool, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologyRuntimePlacementRecord{}, false, err
	}
	if s == nil || s.store == nil {
		return TopologyRuntimePlacementRecord{}, false, errors.New("topology store is not configured")
	}
	runtimeSwarmID = normalizeTopologyKeyValue(runtimeSwarmID)
	if runtimeSwarmID == "" {
		return TopologyRuntimePlacementRecord{}, false, errors.New("topology runtime swarm id is required")
	}
	var record TopologyRuntimePlacementRecord
	ok, err := s.store.GetJSON(KeyTopologyRuntimePlacementForAccount(accountScopeID, runtimeSwarmID), &record)
	if err != nil || !ok {
		return TopologyRuntimePlacementRecord{}, ok, err
	}
	record = normalizeTopologyRuntimePlacementRecord(record)
	if record.RuntimeSwarmID == "" {
		record.RuntimeSwarmID = runtimeSwarmID
	}
	record, err = enforceTopologyRuntimePlacementAccount(accountScopeID, record)
	if err != nil {
		return TopologyRuntimePlacementRecord{}, false, err
	}
	if record.PlacementID == "" {
		record.PlacementID = legacyTopologyRuntimePlacementID(record.AccountScopeID, record.RuntimeSwarmID)
	}
	if record.PlacementGeneration <= 0 {
		record.PlacementGeneration = 1
	}
	if strings.TrimSpace(record.State) == "" {
		record.State = TopologyRuntimePlacementStateActive
	}
	return record, true, nil
}

func (s *TopologyStore) PutRuntimePlacementForAccount(accountScopeID string, record TopologyRuntimePlacementRecord) (TopologyRuntimePlacementRecord, error) {
	if s == nil || s.store == nil {
		return TopologyRuntimePlacementRecord{}, errors.New("topology store is not configured")
	}
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologyRuntimePlacementRecord{}, err
	}
	inputCreatedAt := record.CreatedAt
	inputPlacementGeneration := record.PlacementGeneration
	inputState := record.State
	record = normalizeTopologyRuntimePlacementRecord(record)
	if record.RuntimeSwarmID == "" {
		return TopologyRuntimePlacementRecord{}, errors.New("topology runtime swarm id is required")
	}
	record, err = enforceTopologyRuntimePlacementAccount(accountScopeID, record)
	if err != nil {
		return TopologyRuntimePlacementRecord{}, err
	}
	existing, ok, err := s.getRuntimePlacementForAccountRaw(accountScopeID, record.RuntimeSwarmID)
	if err != nil {
		return TopologyRuntimePlacementRecord{}, err
	}
	if !ok {
		if strings.TrimSpace(inputState) == "" {
			record.State = ""
		}
		if record.PlacementID == "" {
			record.PlacementID = newTopologyRuntimePlacementID()
		}
	} else {
		if existing.PlacementGeneration <= 0 {
			existing.PlacementGeneration = 1
		}
		if strings.TrimSpace(existing.State) == "" {
			existing.State = TopologyRuntimePlacementStateActive
		}
		if err := validateTopologyRuntimePlacement(existing); err != nil {
			return TopologyRuntimePlacementRecord{}, err
		}
		if inputCreatedAt <= 0 {
			record.CreatedAt = existing.CreatedAt
		}
		if inputPlacementGeneration <= 0 {
			record.PlacementGeneration = existing.PlacementGeneration
		}
		if strings.TrimSpace(inputState) == "" {
			record.State = existing.State
		}
		if record.PlacementID == "" {
			record.PlacementID = existing.PlacementID
		}
	}
	if record.PlacementGeneration <= 0 {
		record.PlacementGeneration = 1
	}
	if strings.TrimSpace(record.State) == "" {
		record.State = TopologyRuntimePlacementStateActive
	}
	if err := validateTopologyRuntimePlacement(record); err != nil {
		return TopologyRuntimePlacementRecord{}, err
	}
	record.CreatedAt, record.UpdatedAt = nextTopologyWriteTimestamps(record.CreatedAt)
	if err := s.store.PutJSON(KeyTopologyRuntimePlacementForAccount(accountScopeID, record.RuntimeSwarmID), record); err != nil {
		return TopologyRuntimePlacementRecord{}, err
	}
	return record, nil
}

func (s *TopologyStore) listTopologyRuntimePlacementRecordsForAccount(accountScopeID string, limit int) ([]TopologyRuntimePlacementRecord, error) {
	out, err := listTopologyRecordsForAccount(s, TopologyRuntimePlacementPrefixForAccount(accountScopeID), func(key string, value []byte) (TopologyRuntimePlacementRecord, bool, error) {
		var record TopologyRuntimePlacementRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return TopologyRuntimePlacementRecord{}, false, fmt.Errorf("decode topology runtime placement: %w", err)
		}
		record = normalizeTopologyRuntimePlacementRecord(record)
		if record.RuntimeSwarmID == "" {
			record.RuntimeSwarmID = decodeTopologyKeyValue(key, TopologyRuntimePlacementPrefixForAccount(accountScopeID))
		}
		if record.RuntimeSwarmID == "" {
			return TopologyRuntimePlacementRecord{}, false, nil
		}
		var err error
		record, err = enforceTopologyRuntimePlacementAccount(accountScopeID, record)
		if err != nil {
			return TopologyRuntimePlacementRecord{}, false, err
		}
		if record.PlacementID == "" {
			record.PlacementID = legacyTopologyRuntimePlacementID(record.AccountScopeID, record.RuntimeSwarmID)
		}
		if err := validateTopologyRuntimePlacement(record); err != nil {
			return TopologyRuntimePlacementRecord{}, false, err
		}
		return record, true, nil
	}, limit)
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return strings.ToLower(out[i].RuntimeSwarmID) < strings.ToLower(out[j].RuntimeSwarmID)
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

func normalizeTopologyRuntimePlacementRecords(records []TopologyRuntimePlacementRecord) []TopologyRuntimePlacementRecord {
	if len(records) == 0 {
		return nil
	}
	out := make([]TopologyRuntimePlacementRecord, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, raw := range records {
		record := normalizeTopologyRuntimePlacementRecord(raw)
		if record.RuntimeSwarmID == "" {
			continue
		}
		if _, ok := seen[record.RuntimeSwarmID]; ok {
			continue
		}
		seen[record.RuntimeSwarmID] = struct{}{}
		out = append(out, record)
	}
	return out
}

func normalizeTopologyRuntimePlacementRecord(record TopologyRuntimePlacementRecord) TopologyRuntimePlacementRecord {
	record.PlacementID = normalizeTopologyKeyValue(record.PlacementID)
	record.RuntimeSwarmID = normalizeTopologyKeyValue(record.RuntimeSwarmID)
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.AuthorityHostSwarmID = normalizeTopologyKeyValue(record.AuthorityHostSwarmID)
	record.AuthorityContainerID = normalizeTopologyKeyValue(record.AuthorityContainerID)
	record.RuntimeKind = strings.ToLower(strings.TrimSpace(record.RuntimeKind))
	record.State = strings.ToLower(strings.TrimSpace(record.State))
	if record.State == "" {
		record.State = TopologyRuntimePlacementStateActive
	}
	if record.CreatedAt < 0 {
		record.CreatedAt = 0
	}
	if record.UpdatedAt < 0 {
		record.UpdatedAt = 0
	}
	return record
}

func enforceTopologyRuntimePlacementAccount(accountScopeID string, record TopologyRuntimePlacementRecord) (TopologyRuntimePlacementRecord, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return TopologyRuntimePlacementRecord{}, errors.New("account scope id is required")
	}
	recordAccountScopeID := strings.TrimSpace(record.AccountScopeID)
	if recordAccountScopeID != "" && recordAccountScopeID != accountScopeID {
		return TopologyRuntimePlacementRecord{}, errors.New("topology runtime placement account scope id does not match account key")
	}
	record.AccountScopeID = accountScopeID
	return record, nil
}

func validateTopologyRuntimePlacement(record TopologyRuntimePlacementRecord) error {
	if strings.TrimSpace(record.PlacementID) == "" {
		return errors.New("topology runtime placement id is required")
	}
	if strings.TrimSpace(record.AccountScopeID) == "" {
		return errors.New("topology runtime placement account scope id is required")
	}
	if strings.TrimSpace(record.RuntimeSwarmID) == "" {
		return errors.New("topology runtime placement runtime swarm id is required")
	}
	if record.PlacementGeneration <= 0 {
		return errors.New("topology runtime placement generation is required")
	}
	state := strings.ToLower(strings.TrimSpace(record.State))
	if state == "" {
		return errors.New("topology runtime placement state is required")
	}
	if strings.TrimSpace(record.RuntimeKind) == "" {
		return errors.New("topology runtime placement runtime kind is required")
	}
	return nil
}

func newTopologyRuntimePlacementID() string {
	return "rtp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func legacyTopologyRuntimePlacementID(accountScopeID, runtimeSwarmID string) string {
	replacer := strings.NewReplacer("%", "", "/", "_")
	accountPart := replacer.Replace(keyPart(accountScopeID))
	runtimePart := replacer.Replace(keyPart(runtimeSwarmID))
	return "rtp_legacy_" + accountPart + "_" + runtimePart
}
