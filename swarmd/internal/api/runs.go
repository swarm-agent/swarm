package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// handleRuns lists the machine's worker runs across all workers, newest
// admission first (GET /v3/runs, scope signals:read): ?since=&until=
// (milliseconds, until exclusive), ?status=failed,running, ?worker_id=,
// ?limit= (1-500, default 100) and ?cursor= (next_cursor of the last page).
// It reads only the runs in the window, through the run index. Like the box
// summary it holds ids, names, status and times only: a run's error text
// stays on the machine (swarm_get_job reads it).
func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if !s.requireScope(w, r, signalsReadScope) {
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("runs are not available"))
		return
	}
	q := r.URL.Query()
	query := pebblestore.WorkerRunTimeQuery{WorkerID: strings.TrimSpace(q.Get("worker_id")), Cursor: strings.TrimSpace(q.Get("cursor"))}
	for name, target := range map[string]*int64{"since": &query.SinceMs, "until": &query.UntilMs} {
		if raw := strings.TrimSpace(q.Get(name)); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value < 0 {
				writeError(w, http.StatusBadRequest, errors.New(name+" must be milliseconds since the epoch"))
				return
			}
			*target = value
		}
	}
	if query.UntilMs > 0 && query.UntilMs <= query.SinceMs {
		writeError(w, http.StatusBadRequest, errors.New("until must be after since"))
		return
	}
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 500 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be 1-500"))
			return
		}
		query.Limit = limit
	}
	for _, status := range strings.Split(q.Get("status"), ",") {
		if status = strings.TrimSpace(status); status != "" {
			switch status {
			case "admitted", "running", "succeeded", "failed", "cancelled":
				query.Statuses = append(query.Statuses, status)
			default:
				writeError(w, http.StatusBadRequest, errors.New("status must be admitted, running, succeeded, failed or cancelled"))
				return
			}
		}
	}
	account := strings.TrimSpace(principal.AccountScopeID)
	runs, next, err := s.sessions.ListWorkerRunsByTime(account, query)
	if err != nil {
		if errors.Is(err, pebblestore.ErrWorkerInvalid) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	names := map[string]string{}
	if all, err := s.allWorkers(account); err == nil {
		for _, worker := range all {
			names[worker.ID] = worker.Name
		}
	}
	items := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		item := map[string]any{
			"job_id": "worker/" + run.WorkerID + "/" + run.ID, "run_id": run.ID, "worker_id": run.WorkerID, "worker_name": names[run.WorkerID],
			"automation_id": run.AutomationID, "request_source": run.RequestSource, "status": run.Status,
			"created_at": run.CreatedAt, "started_at": run.StartedAt, "completed_at": run.CompletedAt,
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": items, "next_cursor": next})
}
