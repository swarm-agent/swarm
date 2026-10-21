package api

import (
	"errors"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type failingCatalogBinding struct{}

func (failingCatalogBinding) EnsureLocalSelfPlacementForPrincipal(string, string) (pebblestore.TopologyRuntimePlacementRecord, error) {
	return pebblestore.TopologyRuntimePlacementRecord{}, nil
}
func (failingCatalogBinding) EnsureLocalWorkspaceSelfBindingForPrincipal(string, string, pebblestore.WorkspaceEntry) (pebblestore.TopologyWorkspaceBindingRecord, error) {
	return pebblestore.TopologyWorkspaceBindingRecord{}, errors.New("injected binding failure")
}
