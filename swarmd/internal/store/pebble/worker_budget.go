package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

var ErrWorkerBudget = errors.New("worker budget blocked")

// WorkerBudgetPolicy is deliberately separate from AI-editable worker definitions.
// Zero means unset. Updates never clear usage or outstanding reservations.
type WorkerBudgetPolicy struct {
	AccountScopeID string `json:"account_scope_id"`
	WorkerID string `json:"worker_id"`
	Revision uint64 `json:"revision"`
	DailyCostLimitUSD float64 `json:"daily_cost_limit_usd"`
	DailyTokensLimit int64 `json:"daily_tokens_limit"`
	UpdatedAt int64 `json:"updated_at"`
}

type workerBudgetReservation struct {
	SessionID string `json:"session_id"`
	Date string `json:"date"`
	UsageRevision uint64 `json:"usage_revision"`
	ReceiptRevision uint64 `json:"receipt_revision"`
}

func workerBudgetKey(account, worker string) string {
	return "worker_budget/" + keyPart(account) + "/" + keyPart(worker)
}

func (s *SessionStore) GetWorkerBudget(account, worker string) (WorkerBudgetPolicy, error) {
	policy := WorkerBudgetPolicy{AccountScopeID: account, WorkerID: worker}
	if s == nil || s.store == nil || strings.TrimSpace(account) == "" || !workerIDRegexp.MatchString(worker) {
		return policy, errors.New("invalid worker budget identity")
	}
	if record, found, err := s.WorkerStore().GetWorker(account, worker); err != nil { return policy, err } else if !found || record.ID != worker || record.AccountScopeID != account { return policy, ErrWorkerNotFound }
	_, err := s.store.GetJSON(workerBudgetKey(account, worker), &policy)
	return policy, err
}

// SetWorkerBudget is an internal persistence boundary; authenticated user-only
// ingress must authorize it. Neither AI definitions nor triggers call this API.
func (s *SessionStore) SetWorkerBudget(account, worker string, expected uint64, cost float64, tokens int64) (WorkerBudgetPolicy, error) {
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 || tokens < 0 { return WorkerBudgetPolicy{}, errors.New("invalid worker budget limits") }
	if s == nil || s.store == nil { return WorkerBudgetPolicy{}, errors.New("store is not configured") }
	unlock := s.store.sessionMutations.lockSessions("account:" + account)
	defer unlock()
	policy, err := s.GetWorkerBudget(account, worker)
	if err != nil { return policy, err }
	if policy.Revision != expected { return policy, ErrWorkerConflict }
	policy.Revision++
	policy.DailyCostLimitUSD, policy.DailyTokensLimit, policy.UpdatedAt = cost, tokens, time.Now().UnixMilli()
	payload, err := json.Marshal(policy)
	if err != nil { return policy, err }
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(workerBudgetKey(account, worker)), payload, nil); err != nil { return policy, err }
	return policy, batch.Commit(pebble.Sync)
}

// checkWorkerBudgetLocked reads the canonical indexed UTC day, not context
// occupancy. Unknown receipts are not zero-dollar receipts under a cost cap.
func (s *SessionStore) checkWorkerBudgetLocked(account, worker, date string) (WorkerBudgetPolicy, UsageScopeTotal, error) {
	policy, err := s.GetWorkerBudget(account, worker)
	if err != nil { return policy, UsageScopeTotal{}, err }
	total, _, err := s.GetUsageScopeDay(account, "worker", "", worker, date)
	if err != nil { return policy, total, err }
	if policy.DailyCostLimitUSD > 0 {
		if total.UnknownReceipts > 0 || total.Coverage == "repaired_receipts_incomplete" { return policy, total, fmt.Errorf("%w: unresolved worker pricing or coverage", ErrWorkerBudget) }
		cost := total.CatalogCostUSD + total.ProviderCostUSD + total.ProviderEstimateCostUSD
		if cost >= policy.DailyCostLimitUSD { return policy, total, fmt.Errorf("%w: daily worker USD limit exceeded", ErrWorkerBudget) }
	}
	if policy.DailyTokensLimit > 0 && total.TotalTokens >= policy.DailyTokensLimit { return policy, total, fmt.Errorf("%w: daily worker token limit exceeded", ErrWorkerBudget) }
	// Account policy is additive; a worker allowance never supersedes it.
	accountPolicy, found, err := s.GetUsageLimit(account)
	if err != nil { return policy, total, err }
	if found && accountPolicy.Enabled {
		usage, _, err := s.GetDailyUsageAccumulator(account, date)
		if err != nil { return policy, total, err }
		if (accountPolicy.DailyCostLimitUSD > 0 && usage.TotalCostUSD >= accountPolicy.DailyCostLimitUSD) || (accountPolicy.DailyTokensLimit > 0 && usage.TotalTokens >= accountPolicy.DailyTokensLimit) { return policy, total, fmt.Errorf("%w: daily account usage limit exceeded", ErrWorkerBudget) }
	}
	return policy, total, nil
}

func (s *SessionStore) CheckWorkerBudgetAdmission(account, worker string) error {
	if s == nil || s.store == nil { return errors.New("store is not configured") }
	unlock := s.store.sessionMutations.lockSessions("account:" + account)
	defer unlock()
	policy, _, err := s.checkWorkerBudgetLocked(account, worker, time.Now().UTC().Format("2006-01-02"))
	if err != nil { return err }
	if policy.DailyCostLimitUSD > 0 || policy.DailyTokensLimit > 0 {
		var reservation workerBudgetReservation
		found, err := s.store.GetJSON(workerBudgetKey(account, worker) + "/reservation", &reservation)
		if err != nil { return err }
		if found {
			return fmt.Errorf("%w: worker allowance reserved by an unsettled operation", ErrWorkerBudget)
		}
	}
	return nil
}

// CheckWorkerSessionBudget resolves trusted durable descendant lineage on every
// new billable operation. A capped worker reserves its entire remaining allowance
// exclusively for one session: no guessed output-token or price upper bounds.
// Reservations survive restart and UTC rollover. An unresolved prior call blocks
// another session rather than silently expiring its potentially billable work.
// This is a stop-before-next-call control, not an invoice-hard cap: an already
// admitted provider operation can overshoot before its genuine receipt arrives.
func (s *SessionStore) CheckWorkerSessionBudget(account, sessionID, provider, model string) error {
	_, status := s.CalculateCostWithStatus(provider, model, 1, 1, 0, 0)
	return s.CheckWorkerSessionBudgetWithPrice(account, sessionID, status)
}

// Media callers supply the canonical dimension-specific pricing status.
func (s *SessionStore) CheckWorkerSessionBudgetWithPrice(account, sessionID, priceStatus string) error {
	if s == nil || s.store == nil { return errors.New("store is not configured") }
	unlock := s.store.sessionMutations.lockSessions("account:" + account)
	defer unlock()
	scopes, err := s.resolveUsageScopes(account, sessionID)
	if err != nil { return err }
	date := time.Now().UTC().Format("2006-01-02")
	for _, scope := range scopes {
		if scope.Kind != "worker" { continue }
		policy, total, err := s.checkWorkerBudgetLocked(account, scope.ID, date)
		if err != nil { return err }
		if policy.DailyCostLimitUSD == 0 && policy.DailyTokensLimit == 0 { continue }
		if total.Coverage == "repaired_receipts_incomplete" { return fmt.Errorf("%w: worker receipt coverage is incomplete", ErrWorkerBudget) }
		if policy.DailyCostLimitUSD > 0 {
			if priceStatus != "known" && priceStatus != "free" && priceStatus != "subscription" { return fmt.Errorf("%w: provider pricing is unknown", ErrWorkerBudget) }
		}
		key := workerBudgetKey(account, scope.ID) + "/reservation"
		var reservation workerBudgetReservation
		found, err := s.store.GetJSON(key, &reservation)
		if err != nil { return err }
		if found {
			return fmt.Errorf("%w: worker allowance reserved by an unsettled operation", ErrWorkerBudget)
		}
		if !found {
			revision, err := s.workerBudgetReceiptRevision(account, sessionID)
			if err != nil { return err }
			payload, err := json.Marshal(workerBudgetReservation{SessionID: sessionID, Date: date, UsageRevision: total.Revision, ReceiptRevision: revision})
			if err != nil { return err }
			batch := s.store.NewBatch()
			if err := batch.Set([]byte(key), payload, nil); err != nil { batch.Close(); return err }
			err = batch.Commit(pebble.Sync)
			batch.Close()
			if err != nil { return err }
		}
	}
	return nil
}

// ReleaseWorkerBudgetReservation is used only after a run has terminated and
// genuine receipts have been persisted. A run without observed receipts retains
// its reservation, including cancellation and restart; there is no timed reset.
func (s *SessionStore) ReleaseWorkerBudgetReservation(account, sessionID string) error {
	if s == nil || s.store == nil { return errors.New("store is not configured") }
	unlock := s.store.sessionMutations.lockSessions("account:" + account)
	defer unlock()
	scopes, err := s.resolveUsageScopes(account, sessionID)
	if err != nil { return err }
	for _, scope := range scopes {
		if scope.Kind != "worker" { continue }
		key := workerBudgetKey(account, scope.ID) + "/reservation"
		var reservation workerBudgetReservation
		found, err := s.store.GetJSON(key, &reservation)
		if err != nil { return err }
		if !found || reservation.SessionID != sessionID { continue }
		total, _, err := s.GetUsageScopeDay(account, "worker", "", scope.ID, reservation.Date)
		if err != nil { return err }
		revision, err := s.workerBudgetReceiptRevision(account, sessionID)
		if err != nil { return err }
		if total.Revision <= reservation.UsageRevision || revision <= reservation.ReceiptRevision { continue }
		batch := s.store.NewBatch()
		if err := batch.Delete([]byte(key), nil); err != nil { batch.Close(); return err }
		err = batch.Commit(pebble.Sync)
		batch.Close()
		if err != nil { return err }
	}
	return nil
}

func workerBudgetReceiptKey(account, session string) string {
	return "worker_budget_receipt_revision/" + keyPart(account) + "/" + keyPart(session)
}

func (s *SessionStore) workerBudgetReceiptRevision(account, session string) (uint64, error) {
	var revision uint64
	_, err := s.store.GetJSON(workerBudgetReceiptKey(account, session), &revision)
	return revision, err
}

// Canonical receipt batches own this settlement marker, under the same account
// lock as reservations. Context resets, metadata and AI operations cannot set it.
func (s *SessionStore) setWorkerBudgetReceiptRevision(batch *pebble.Batch, account, session string) error {
	revision, err := s.workerBudgetReceiptRevision(account, session)
	if err != nil { return err }
	payload, err := json.Marshal(revision + 1)
	if err != nil { return err }
	return batch.Set([]byte(workerBudgetReceiptKey(account, session)), payload, nil)
}

// CheckWorkerUnmeteredOperation rejects optional internal operations whose
// existing caller cannot persist a genuine receipt. Do not reserve/guess a cost
// or silently spend outside the worker budget (e.g. background title generation).
func (s *SessionStore) CheckWorkerUnmeteredOperation(account, session string) error {
	if s == nil || s.store == nil { return errors.New("store is not configured") }
	unlock := s.store.sessionMutations.lockSessions("account:" + account)
	defer unlock()
	scopes, err := s.resolveUsageScopes(account, session)
	if err != nil { return err }
	for _, scope := range scopes {
		if scope.Kind != "worker" { continue }
		policy, err := s.GetWorkerBudget(account, scope.ID)
		if err != nil { return err }
		if policy.DailyCostLimitUSD > 0 || policy.DailyTokensLimit > 0 { return fmt.Errorf("%w: internal operation has no canonical receipt boundary", ErrWorkerBudget) }
	}
	return nil
}
