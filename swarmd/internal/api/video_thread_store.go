package api

import pebblestore "swarm/packages/swarmd/internal/store/pebble"

func (s *Server) SetVideoThreadStore(store *pebblestore.VideoThreadStore) {
	if s == nil {
		return
	}
	s.videoThreads = store
}
