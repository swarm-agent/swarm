package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"swarm/packages/swarmd/internal/longsessiondiag"
)

const (
	LongSessionDiagnosticsConfigPath  = "/v3/diagnostics/long-session/config"
	LongSessionDiagnosticsSamplePath  = "/v3/diagnostics/long-session/samples"
	LongSessionDiagnosticsCapturePath = "/v3/diagnostics/long-session/captures"
	longSessionDiagnosticsMaxBody     = 32 << 10
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
		"artifact_location":  s.longSessionDiagnostics.Directory(),
	})
}

func (s *Server) handleLongSessionDiagnosticsSample(w http.ResponseWriter, r *http.Request) {
	s.handleLongSessionDiagnosticsDesktopSample(w, r, false)
}

func (s *Server) handleLongSessionDiagnosticsCapture(w http.ResponseWriter, r *http.Request) {
	s.handleLongSessionDiagnosticsDesktopSample(w, r, true)
}

func (s *Server) handleLongSessionDiagnosticsDesktopSample(w http.ResponseWriter, r *http.Request, captureDaemon bool) {
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
	var artifacts []string
	if captureDaemon {
		var err error
		artifacts, err = s.longSessionDiagnostics.CaptureNow()
		if err != nil {
			writeError(w, http.StatusInternalServerError, errors.New("capture long-session daemon diagnostics"))
			return
		}
	}
	s.longSessionDesktopSampleLogOnce.Do(func() {
		log.Printf("long-session diagnostics accepted first desktop sample artifact=%q", "desktop-samples.jsonl")
	})
	response := map[string]any{
		"ok":                true,
		"artifact_location": s.longSessionDiagnostics.Directory(),
	}
	if captureDaemon {
		response["artifacts"] = artifacts
	}
	writeJSON(w, http.StatusAccepted, response)
}
