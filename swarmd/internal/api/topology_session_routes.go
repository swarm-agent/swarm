package api

import (
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) upsertTopologySessionRoute(record pebblestore.SessionRouteRecord) error {
	if s == nil || s.topology == nil {
		return nil
	}
	route, err := s.topology.UpsertSessionRoute(record)
	if err != nil {
		return err
	}
	if s.sessionRoutes == nil {
		return nil
	}
	if strings.TrimSpace(route.SessionID) == "" || strings.TrimSpace(route.RuntimeSwarmID) == "" || strings.TrimSpace(route.BackendURL) == "" {
		return nil
	}
	_, err = s.sessionRoutes.Put(pebblestore.SessionRouteRecord{
		SessionID:            route.SessionID,
		ChildSwarmID:         route.RuntimeSwarmID,
		ChildBackendURL:      route.BackendURL,
		HostSwarmID:          route.HostSwarmID,
		HostContainerID:      route.HostContainerID,
		HostWorkspacePath:    route.HostWorkspacePath,
		RuntimeWorkspacePath: route.RuntimeWorkspacePath,
		WorkspaceBindingID:   route.WorkspaceBindingID,
		CreatedAt:            route.CreatedAt,
		UpdatedAt:            route.UpdatedAt,
	})
	return err
}

func (s *Server) deleteTopologySessionRoute(sessionID string) error {
	if s == nil || s.topology == nil {
		return nil
	}
	return s.topology.DeleteSessionRoute(sessionID)
}
