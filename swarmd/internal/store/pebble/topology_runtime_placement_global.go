package pebblestore

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func (s *TopologyStore) listTopologyRuntimePlacementRecords(limit int) ([]TopologyRuntimePlacementRecord, error) {
	out, err := listTopologyRecordsForAccount(s, TopologyRuntimePlacementPrefix(), func(key string, value []byte) (TopologyRuntimePlacementRecord, bool, error) {
		var record TopologyRuntimePlacementRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return TopologyRuntimePlacementRecord{}, false, fmt.Errorf("decode topology runtime placement: %w", err)
		}
		record = normalizeTopologyRuntimePlacementRecord(record)
		if record.RuntimeSwarmID == "" {
			record.RuntimeSwarmID = decodeTopologyKeyValue(key, TopologyRuntimePlacementPrefix())
		}
		if record.RuntimeSwarmID == "" {
			return TopologyRuntimePlacementRecord{}, false, nil
		}
		if record.PlacementID == "" {
			record.PlacementID = legacyTopologyRuntimePlacementID(record.AccountScopeID, record.RuntimeSwarmID)
		}
		if strings.TrimSpace(record.AccountScopeID) != "" {
			if err := validateTopologyRuntimePlacement(record); err != nil {
				return TopologyRuntimePlacementRecord{}, false, err
			}
		} else if strings.TrimSpace(record.RuntimeKind) == "" {
			return TopologyRuntimePlacementRecord{}, false, nil
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
