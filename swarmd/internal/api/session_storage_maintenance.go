package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const SessionStorageMaintenancePath = "/v3/maintenance/session-storage"

type sessionStorageMaintenanceHTTPRequest struct {
	Mode                                 string `json:"mode"`
	RealtimeReplayRetentionSeconds       int64  `json:"realtime_replay_retention_seconds,omitempty"`
	CompletedIdempotencyRetentionSeconds int64  `json:"completed_idempotency_retention_seconds,omitempty"`
	RealtimeMinimumRecords               uint64 `json:"realtime_minimum_records,omitempty"`
	BatchRecords                         int    `json:"batch_records,omitempty"`
	RunSearchMigration                   bool   `json:"run_search_migration,omitempty"`
	SearchMigrationMaxSessions           int    `json:"search_migration_max_sessions,omitempty"`
}

func (s *Server) handleSessionStorageMaintenance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	// This operator-only endpoint is deliberately unavailable over network and
	// desktop HTTP transports. The local Unix-socket marker is set by the daemon
	// before authentication middleware runs.
	if !isLocalTransportRequest(r) {
		writeError(w, http.StatusNotFound, errors.New("not found"))
		return
	}
	if s == nil || s.sessions == nil || s.sessions.Store() == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("session storage maintenance is not configured"))
		return
	}
	var request sessionStorageMaintenanceHTTPRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	mode := strings.ToLower(strings.TrimSpace(request.Mode))
	if mode == "" {
		mode = "dry_run"
	}
	if mode != "dry_run" && mode != "apply" {
		writeError(w, http.StatusBadRequest, errors.New("mode must be dry_run or apply"))
		return
	}
	policy := pebblestore.DefaultV3SessionRetentionPolicy()
	if request.RealtimeReplayRetentionSeconds > 0 {
		policy.RealtimeReplayRetention = time.Duration(request.RealtimeReplayRetentionSeconds) * time.Second
	}
	if request.CompletedIdempotencyRetentionSeconds > 0 {
		policy.CompletedIdempotencyRetention = time.Duration(request.CompletedIdempotencyRetentionSeconds) * time.Second
	}
	if request.RealtimeMinimumRecords > 0 {
		policy.RealtimeMinimumRecords = request.RealtimeMinimumRecords
	}
	if request.BatchRecords > 0 {
		policy.BatchRecords = request.BatchRecords
	}
	report, err := s.sessions.Store().RunSessionStorageMaintenance(r.Context(), pebblestore.SessionStorageMaintenanceRequest{
		Apply:                      mode == "apply",
		RetentionPolicy:            policy,
		RunSearchMigration:         request.RunSearchMigration,
		SearchMigrationMaxSessions: request.SearchMigrationMaxSessions,
	})
	if err != nil {
		// Return the aggregate report accumulated before the failure, but never
		// return the raw store error because it may contain a key or identifier.
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":     false,
			"error":  "session storage maintenance failed",
			"report": report,
		})
		return
	}
	writeJSON(w, http.StatusOK, report)
}
