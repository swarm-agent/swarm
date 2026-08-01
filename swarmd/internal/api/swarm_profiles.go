package api

import (
	"errors"
	"net/http"
)

const swarmProfilesPath = "/v1/swarm-profiles"

var errSwarmProfilesRetired = errors.New("swarm profiles are retired; configure Swarm Action and optional Plan favorites through mode settings")

// handleSwarmProfiles is retained only as an explicit hard-cut response for
// clients that still call the legacy route. Swarm model selection is no longer
// an agent-member split payload.
func (s *Server) handleSwarmProfiles(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusGone, errSwarmProfilesRetired)
}

func (s *Server) handleSwarmProfileByID(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusGone, errSwarmProfilesRetired)
}
