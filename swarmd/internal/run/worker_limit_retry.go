package run

import (
	"context"
	"errors"
	"strings"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// LimitRetryFilter names which of an account's worker runs waiting for a
// provider's limit retry now: one run (WorkerID and RunID), or every waiting
// run, optionally only those whose session uses Provider.
type LimitRetryFilter struct {
	WorkerID string
	RunID    string
	Provider string
}

// LimitRetry is one waiting worker run a retry-now reached.
type LimitRetry struct {
	WorkerID string `json:"worker_id"`
	RunID    string `json:"run_id"`
	// Status is the run's status after the retry: running when it reran,
	// waiting when the worker's budget (or capacity) still holds it, or
	// cancelled when its admission had closed.
	Status string `json:"status"`
}

// RetryLimitWaitsNow makes worker runs waiting for a provider's limit retry
// now instead of at the reset the old provider stated, for when the person
// fixed the provider (a new sign-in, a new key, a different model). Each run
// matched has its RetryAt moved to now and then goes through the one rerun
// path (rerunAfterProviderLimit / startLimitRerun) under the dispatch lock,
// so budget admission, capacity, LimitWaits accounting and the rerun's
// idempotency stay as they are. A rerun that hits the limit again simply
// waits again. A run that is not waiting is left alone (a second call is a
// no-op); a named run that does not exist is ErrWorkerNotFound.
func (s *WorkerExecutionService) RetryLimitWaitsNow(ctx context.Context, account string, filter LimitRetryFilter) ([]LimitRetry, error) {
	account = strings.TrimSpace(account)
	if account == "" {
		return nil, store.ErrWorkerInvalid
	}
	if (filter.WorkerID == "") != (filter.RunID == "") {
		return nil, errors.New("a worker run is named by both worker and run")
	}
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	ws, err := s.workerStore()
	if err != nil {
		return nil, err
	}
	var candidates []store.WorkerRunRecord
	if filter.RunID != "" {
		r, found, err := ws.GetWorkerRun(account, filter.WorkerID, filter.RunID)
		if err != nil {
			return nil, err
		}
		if !found || r.AccountScopeID != account {
			return nil, store.ErrWorkerNotFound
		}
		candidates = append(candidates, r)
	} else {
		candidates, err = ws.UnfinishedWorkerRuns(account, "", "")
		if err != nil {
			return nil, err
		}
	}
	out := []LimitRetry{}
	var failures []error
	for _, r := range candidates {
		if r.Status != store.WorkerRunStatusWaiting || r.Placement != nil {
			continue
		}
		if filter.Provider != "" && !s.runUsesProvider(r, filter.Provider) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		status, err := s.retryLimitWaitNow(ctx, ws, account, r)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		out = append(out, LimitRetry{WorkerID: r.WorkerID, RunID: r.ID, Status: status})
	}
	return out, errors.Join(failures...)
}

// retryLimitWaitNow moves one waiting run's RetryAt to now and settles it
// through rerunAfterProviderLimit, the path the scheduler takes at the reset.
// It runs under dispatchMu.
func (s *WorkerExecutionService) retryLimitWaitNow(ctx context.Context, ws *store.WorkerStore, account string, r store.WorkerRunRecord) (string, error) {
	w, found, err := ws.GetWorker(account, r.WorkerID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", store.ErrWorkerNotFound
	}
	now := time.Now()
	if r.RetryAt > now.UnixMilli() {
		r.RetryAt = now.UnixMilli()
		r.QueueReason = "the provider was fixed; the job runs again now"
		if r, err = ws.RecordWorkerRun(account, r); err != nil {
			return "", err
		}
	}
	budget, err := s.host.runs.sessions.Store().GetWorkerBudgetStatus(account, r.WorkerID)
	if err != nil {
		return "", err
	}
	if err := s.rerunAfterProviderLimit(ctx, ws, w, r, budget.Hold != nil, now); err != nil {
		return "", err
	}
	current, found, err := ws.GetWorkerRun(account, r.WorkerID, r.ID)
	if err != nil || !found {
		return "", errors.Join(err, store.ErrWorkerNotFound)
	}
	return current.Status, nil
}

// runUsesProvider reports whether the run's session uses provider.
func (s *WorkerExecutionService) runUsesProvider(r store.WorkerRunRecord, provider string) bool {
	if r.SessionID == "" {
		return false
	}
	snapshot, found, err := s.host.runs.sessions.GetSession(r.SessionID)
	if err != nil || !found {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(snapshot.Preference.Provider), strings.TrimSpace(provider))
}
