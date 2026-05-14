package api

import pebblestore "swarm/packages/swarmd/internal/store/pebble"

func (s *Server) upsertTopologySessionRoute(record pebblestore.SessionRouteRecord) error {
	if s == nil || s.topology == nil {
		return nil
	}
	_, err := s.topology.UpsertSessionRoute(record)
	return err
}

func (s *Server) deleteTopologySessionRoute(sessionID string) error {
	if s == nil || s.topology == nil {
		return nil
	}
	return s.topology.DeleteSessionRoute(sessionID)
}
