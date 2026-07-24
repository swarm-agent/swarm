package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"swarm/packages/swarmd/internal/longsessiondiag"
)

const (
	LongSessionDiagnosticsConfigPath = "/v3/diagnostics/long-session/config"
	LongSessionDiagnosticsSamplePath = "/v3/diagnostics/long-session/samples"
	longSessionDiagnosticsMaxBody    = 32 << 10
)

func (s *Server) handleLongSessionDiagnosticsConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s == nil || s.longSessionDiagnostics == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "enabled": false, "error": "long-session diagnostics disabled"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"enabled":            true,
		"sample_interval_ms": longsessiondiag.DefaultSampleInterval.Milliseconds(),
		"max_sample_bytes":   longSessionDiagnosticsMaxBody,
	})
}

func (s *Server) handleLongSessionDiagnosticsSample(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s == nil || s.longSessionDiagnostics == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "enabled": false, "error": "long-session diagnostics disabled"})
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, longSessionDiagnosticsMaxBody))
	decoder.DisallowUnknownFields()
	var sample longsessiondiag.DesktopSample
	if err := decodeJSONObject(decoder, &sample); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.longSessionDiagnostics.RecordDesktopSample(sample); err != nil {
		if errors.Is(err, longsessiondiag.ErrDesktopSampleTooFrequent) {
			writeError(w, http.StatusTooManyRequests, err)
			return
		}
		if errors.Is(err, longsessiondiag.ErrInvalidDesktopSample) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeError(w, http.StatusInternalServerError, errors.New("record long-session diagnostic sample"))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}
