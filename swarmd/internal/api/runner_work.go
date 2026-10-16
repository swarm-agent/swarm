package api

import (
	"errors"
	"net/http"
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"time"
)

func (s *Server) SetRunnerStore(store *pebblestore.Store) { s.runnerStore = store }

func (s *Server) handleRunnerWork(w http.ResponseWriter, r *http.Request, rec *pebblestore.ScopedTokenRecord) {
	if s.runnerStore == nil {
		writeError(w, 503, errors.New("runner store unavailable"))
		return
	}
	now := time.Now()
	if r.URL.Path == "/v3/runners/claim" {
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		for {
			wake := s.runnerStore.RunnerLandingWake()
			landing, err := s.runnerStore.ClaimRunnerLanding(rec.AccountScopeID, rec.Name, time.Now())
			if err != nil {
				writeError(w, 409, err)
				return
			}
			if landing != nil {
				landing.SourceClonePath = ""
				writeJSON(w, 200, map[string]any{"landing": landing})
				return
			}
			select {
			case <-r.Context().Done():
				return
			case <-timer.C:
				writeJSON(w, 200, map[string]any{"landing": nil})
				return
			case <-wake:
			}
		}

	}
	if r.URL.Path == "/v3/runners/status" {
		var status pebblestore.RunnerStatus
		if err := decodeJSONLimited(w, r, &status, 4096); err != nil {
			writeError(w, 400, err)
			return
		}
		status.Name = rec.Name
		status.LastSeen = now.UnixMilli()
		if err := s.runnerStore.SetRunnerStatus(rec.AccountScopeID, status); err != nil {
			writeError(w, 400, err)
			return
		}
		if status.State == "unhealthy" {
			s.runnerSignal("runner.unhealthy", rec.AccountScopeID, rec.Name)
		}
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v3/runners/landings/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	if parts[1] == "bundle" {
		writeError(w, http.StatusGone, errors.New("runner bundles retired; use payload"))
		return
	}
	if parts[1] == "payload" {
		f, err := s.runnerStore.OpenRunnerPayload(rec.AccountScopeID, rec.Name, parts[0], now)
		if err != nil {
			writeError(w, 403, err)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			writeError(w, 500, err)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		http.ServeContent(w, r, "source.tar.gz", info.ModTime(), f)
		return
	}
	var req struct {
		LeaseID    string `json:"lease_id"`
		Output     string `json:"output"`
		ExitCodes  []int  `json:"exit_codes"`
		DurationMS int64  `json:"duration_ms"`
	}
	if err := decodeJSONLimited(w, r, &req, 128<<10); err != nil {
		writeError(w, 400, err)
		return
	}
	if err := s.runnerStore.UpdateRunnerLanding(rec.AccountScopeID, rec.Name, parts[0], req.LeaseID, parts[1], req.Output, req.ExitCodes, req.DurationMS, now); err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
