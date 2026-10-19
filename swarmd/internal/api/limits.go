package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	store "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) handleLimits(w http.ResponseWriter, r *http.Request) {
	p, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, 401, errors.New("principal required"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !s.requireScopeAny(w, r, "usage:read", "usage:write") {
			return
		}
		l, err := s.sessions.Store().GetLimits(p.AccountScopeID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, l)
	case http.MethodPut:
		if !s.requireScope(w, r, "usage:write") {
			return
		}
		var req struct {
			ExpectedRevision *uint64      `json:"expected_revision"`
			Limits           store.Limits `json:"limits"`
		}
		defaults := store.DefaultLimits()
		req.Limits = store.Limits{
			HelperConcurrency: defaults.HelperConcurrency, HelpersPerJob: defaults.HelpersPerJob,
			HelperTimeoutSeconds: defaults.HelperTimeoutSeconds, HelperTokens: defaults.HelperTokens,
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if req.ExpectedRevision == nil || dec.Decode(new(any)) != io.EOF {
			writeError(w, 400, errors.New("expected_revision and exactly one request required"))
			return
		}
		l, err := s.sessions.Store().SetLimits(p.AccountScopeID, *req.ExpectedRevision, req.Limits)
		if err != nil {
			status := 400
			if errors.Is(err, store.ErrLimitsConflict) {
				status = 409
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, 200, l)
	default:
		writeError(w, 405, errors.New("method not allowed"))
	}
}

func controlMCPGetLimits(c *controlMCPCall, args map[string]any) (any, error) {
	return c.dispatch(http.MethodGet, "/v3/limits", nil, nil)
}
func controlMCPSetLimits(c *controlMCPCall, args map[string]any) (any, error) {
	return c.dispatch(http.MethodPut, "/v3/limits", nil, args)
}

func controlMCPLimitsSchema() map[string]any {
	return controlMCPObject([]string{"worker_job_concurrency", "job_tokens", "job_cost_usd", "delegation_waves", "active_children", "swarm_children"}, map[string]any{
		"project_cache_bytes":    map[string]any{"type": "integer", "minimum": 0, "description": "Per-project soft cache cap in bytes; zero selects 10 GiB."},
		"helper_concurrency":     map[string]any{"type": "integer", "minimum": 1, "maximum": 256},
		"helpers_per_job":        map[string]any{"type": "integer", "minimum": 1, "maximum": 10000},
		"helper_timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 3600},
		"helper_tokens":          map[string]any{"type": "integer", "minimum": 1, "maximum": 1000000000, "description": "Per-helper cumulative token budget, enforced after provider usage receipts. An over-budget helper is stopped and reported at the next step; no pre-dispatch estimate is used."},
		"revision":               map[string]any{"type": "integer", "minimum": 0},
		"worker_job_concurrency": map[string]any{"type": "integer", "minimum": 1, "maximum": 10000},
		"job_tokens":             map[string]any{"type": "integer", "minimum": 0, "maximum": 1000000000},
		"job_cost_usd":           map[string]any{"type": "number", "minimum": 0, "maximum": 1000000},
		"delegation_waves":       map[string]any{"type": "integer", "minimum": 0, "maximum": 256},
		"active_children":        map[string]any{"type": "integer", "minimum": 1, "maximum": 256},
		"swarm_children":         map[string]any{"type": "integer", "minimum": 1, "maximum": 256},
	})
}
