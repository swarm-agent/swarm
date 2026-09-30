package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Budget policy has no AI tool ingress. Scoped/trigger principals cannot edit,
// disable, or reset it; the account is never accepted from the request body.
func (s *Server) handleWorkerBudget(w http.ResponseWriter, r *http.Request) {
	p, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if _, scoped := ScopedTokenFromRequest(r); scoped || p.Type != identity.PrincipalTypeUser {
		writeError(w, http.StatusForbidden, errors.New("worker budget requires authenticated user authority"))
		return
	}
	worker := r.URL.Query().Get("worker_id")
	policy, err := s.sessions.Store().GetWorkerBudget(p.AccountScopeID, worker)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrWorkerNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, policy)
	case http.MethodPut:
		var request struct {
			ExpectedRevision  *uint64 `json:"expected_revision"`
			DailyCostLimitUSD float64 `json:"daily_cost_limit_usd"`
			DailyTokensLimit  int64   `json:"daily_tokens_limit"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if request.ExpectedRevision == nil {
			writeError(w, http.StatusBadRequest, errors.New("expected_revision required"))
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			writeError(w, http.StatusBadRequest, errors.New("exactly one budget request required"))
			return
		}
		policy, err = s.sessions.Store().SetWorkerBudget(p.AccountScopeID, worker, *request.ExpectedRevision, request.DailyCostLimitUSD, request.DailyTokensLimit)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, store.ErrWorkerConflict) {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusOK, policy)
	default:
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}
