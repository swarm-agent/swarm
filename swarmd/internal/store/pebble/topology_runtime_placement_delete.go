package pebblestore

import (
	"errors"

	"github.com/cockroachdb/pebble"
)

func (s *TopologyStore) DeleteRuntimePlacementForAccount(accountScopeID, runtimeSwarmID string) error {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	runtimeSwarmID = normalizeTopologyKeyValue(runtimeSwarmID)
	if runtimeSwarmID == "" {
		return errors.New("topology runtime swarm id is required")
	}
	if err := s.store.Delete(KeyTopologyRuntimePlacementForAccount(accountScopeID, runtimeSwarmID)); err != nil && !errors.Is(err, pebble.ErrNotFound) {
		return err
	}
	return nil
}

func (s *TopologyStore) DeleteRuntimePlacement(runtimeSwarmID string) error {
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	runtimeSwarmID = normalizeTopologyKeyValue(runtimeSwarmID)
	if runtimeSwarmID == "" {
		return errors.New("topology runtime swarm id is required")
	}
	if err := s.store.Delete(KeyTopologyRuntimePlacement(runtimeSwarmID)); err != nil && !errors.Is(err, pebble.ErrNotFound) {
		return err
	}
	return nil
}
