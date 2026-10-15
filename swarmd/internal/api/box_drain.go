package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"swarm/packages/swarmd/internal/executioncapacity"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// The daemon fences execution only. Installation and privileged restart remain
// the installer's responsibility. Owner authority is required across accounts.
func (s *Server) handleBoxDrain(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if !s.requireScope(w, r, "admin") {
		return
	}
	manager := s.ExecutionCapacity()
	if manager == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("execution admission unavailable"))
		return
	}
	manager.SetDrainEndObserver(func(status executioncapacity.DrainStatus) {
		if s.signals != nil {
			_, err := s.signals.Append(pebblestore.Signal{Source: pebblestore.SignalSourceSwarmd, Kind: "update.drain." + status.EndReason, Severity: pebblestore.SignalSeverityInfo, Summary: "Update drain ended: " + status.EndReason, DedupKey: "update.drain.end:" + status.Since.Format(time.RFC3339Nano)})
			if err != nil {
				log.Printf("record drain end: %v", err)
			}
		}
	})
	switch r.Method {
	case http.MethodPut:
		var body struct {
			Generation string `json:"generation"`
			Claim      string `json:"claim"`
			Release    bool   `json:"release"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			writeError(w, 400, err)
			return
		}
		if body.Release {
			writeJSON(w, 200, manager.ReleaseRestart(body.Generation, body.Claim))
			return
		}
		s.envBuildMu.Lock()
		defer s.envBuildMu.Unlock()
		status := manager.DrainStatus()
		if !s.addDrainBuildBlockers(w, &status) {
			return
		}
		if !status.Ready {
			writeError(w, 409, errors.New("drain not ready"))
			return
		}
		status, err := manager.ClaimRestart(body.Generation, body.Claim)
		if err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, 200, status)
	case http.MethodDelete:
		writeJSON(w, http.StatusOK, manager.CancelDrain(r.URL.Query().Get("generation")))
	case http.MethodGet:
		status := manager.DrainStatus()
		if !s.addDrainBuildBlockers(w, &status) {
			return
		}
		writeJSON(w, http.StatusOK, status)
	case http.MethodPost:
		var body struct {
			MaxSeconds int `json:"max_seconds"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if body.MaxSeconds < 0 || body.MaxSeconds > 86400 {
			writeError(w, http.StatusBadRequest, errors.New("max_seconds must be between 0 (default 7200) and 86400"))
			return
		}
		s.envBuildMu.Lock()
		status, err := manager.BeginDrain(time.Duration(body.MaxSeconds) * time.Second)
		s.envBuildMu.Unlock()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err)
			return
		}
		if !s.addDrainBuildBlockers(w, &status) {
			return
		}
		if s.signals != nil {
			if _, err := s.signals.Append(pebblestore.Signal{Source: pebblestore.SignalSourceSwarmd, Kind: "update.drain.started", Severity: pebblestore.SignalSeverityInfo, Summary: "New executions held for installer update", DedupKey: "update.drain:" + status.Since.Format(time.RFC3339Nano)}); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, status)
	default:
		methodNotAllowed(w)
	}
}

func controlMCPRequestUpdate(c *controlMCPCall, args map[string]any) (any, error) {
	return c.dispatch(http.MethodPost, "/v3/box/drain", nil, args)
}

func (s *Server) addDrainBuildBlockers(w http.ResponseWriter, status *executioncapacity.DrainStatus) bool {
	if s.sandboxEnvironments != nil {
		count, err := s.sandboxEnvironments.CountBuilding()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err)
			return false
		}
		status.EnvironmentBuilds = count
		status.Ready = status.Ready && count == 0
	}
	return true
}

func controlMCPCancelUpdate(c *controlMCPCall, args map[string]any) (any, error) {
	return c.dispatch(http.MethodDelete, "/v3/box/drain", nil, nil)
}
