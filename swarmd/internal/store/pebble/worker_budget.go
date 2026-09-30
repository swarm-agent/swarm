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
	AccountScopeID    string  `json:"account_scope_id"`
	WorkerID          string  `json:"worker_id"`
	Revision          uint64  `json:"revision"`
	DailyCostLimitUSD float64 `json:"daily_cost_limit_usd"`
	DailyTokensLimit  int64   `json:"daily_tokens_limit"`
	UpdatedAt         int64   `json:"updated_at"`
}

type workerBudgetReservation struct {
	SessionID string `json:"session_id"`
	Date string `json:"date"`
	OperationID string `json:"operation_id,omitempty"`
}

func accountBudgetReservationKey(account string) string {
	return "worker_budget_account/" + keyPart(account) + "/reservation"
}

func (s *SessionStore) accountBudgetActive(account string) (UsageLimitRecord, bool, error) {
	policy, found, err := s.GetUsageLimit(account)
	return policy, found && policy.Enabled && (policy.DailyCostLimitUSD > 0 || policy.DailyTokensLimit > 0), err
}

func workerBudgetKey(account, worker string) string {
	return "worker_budget/" + keyPart(account) + "/" + keyPart(worker)
}

func (s *SessionStore) GetWorkerBudget(account, worker string) (WorkerBudgetPolicy, error) {
	policy := WorkerBudgetPolicy{AccountScopeID: account, WorkerID: worker}
	if s == nil || s.store == nil || strings.TrimSpace(account) == "" || !workerIDRegexp.MatchString(worker) {
		return policy, errors.New("invalid worker budget identity")
	}
	if record, found, err := s.WorkerStore().GetWorker(account, worker); err != nil {
		return policy, err
	} else if !found || record.ID != worker || record.AccountScopeID != account {
		return policy, ErrWorkerNotFound
	}
	_, err := s.store.GetJSON(workerBudgetKey(account, worker), &policy)
	return policy, err
}

// SetWorkerBudget is an internal persistence boundary; authenticated user-only
// ingress must authorize it. Neither AI definitions nor triggers call this API.
func (s *SessionStore) SetWorkerBudget(account, worker string, expected uint64, cost float64, tokens int64, users ...string) (WorkerBudgetPolicy, error) {
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 || tokens < 0 {
		return WorkerBudgetPolicy{}, errors.New("invalid worker budget limits")
	}
	if s == nil || s.store == nil {
		return WorkerBudgetPolicy{}, errors.New("store is not configured")
	}
	unlock := s.store.sessionMutations.lockSessions("account:" + account)
	defer unlock()
	policy, err := s.GetWorkerBudget(account, worker)
	if err != nil {
		return policy, err
	}
	if policy.Revision != expected {
		return policy, ErrWorkerConflict
	}
	policy.Revision++
	policy.DailyCostLimitUSD, policy.DailyTokensLimit, policy.UpdatedAt = cost, tokens, time.Now().UnixMilli()
	user := ""
	if len(users) == 1 { user = users[0] }
	mutation := &workerRealtimeMutation{accountScopeID: account, userID: user, workerID: worker}
	if err := mutation.put(workerBudgetKey(account, worker), policy); err != nil { return policy, err }
	if err := mutation.setPayload(WorkerRealtimePayload{WorkerID: worker, BudgetRevision: policy.Revision, ChangeSummary: "budget policy updated"}); err != nil { return policy, err }
	if err := s.store.commitWorkerRealtime(mutation); err != nil { return policy, err }
	s.store.publishWorkerRealtime(mutation)
	return policy, nil
}

// checkWorkerBudgetLocked reads the canonical indexed UTC day, not context
// occupancy. Unknown receipts are not zero-dollar receipts under a cost cap.
func (s *SessionStore) checkWorkerBudgetLocked(account, worker, date string) (WorkerBudgetPolicy, UsageScopeTotal, error) {
	policy, err := s.GetWorkerBudget(account, worker)
	if err != nil {
		return policy, UsageScopeTotal{}, err
	}
	total, _, err := s.GetUsageScopeDay(account, "worker", "", worker, date)
	if err != nil {
		return policy, total, err
	}
	if policy.DailyCostLimitUSD > 0 {
		if total.UnknownReceipts > 0 || total.Coverage == "repaired_receipts_incomplete" {
			return policy, total, fmt.Errorf("%w: unresolved worker pricing or coverage", ErrWorkerBudget)
		}
		cost := total.CatalogCostUSD + total.ProviderCostUSD + total.ProviderEstimateCostUSD
		if cost >= policy.DailyCostLimitUSD {
			return policy, total, fmt.Errorf("%w: daily worker USD limit exceeded", ErrWorkerBudget)
		}
	}
	if policy.DailyTokensLimit > 0 && total.TotalTokens >= policy.DailyTokensLimit {
		return policy, total, fmt.Errorf("%w: daily worker token limit exceeded", ErrWorkerBudget)
	}
	// Account policy is additive; a worker allowance never supersedes it.
	accountPolicy, found, err := s.GetUsageLimit(account)
	if err != nil {
		return policy, total, err
	}
	if found && accountPolicy.Enabled {
		usage, _, err := s.GetDailyUsageAccumulator(account, date)
		if err != nil {
			return policy, total, err
		}
		if accountPolicy.DailyCostLimitUSD > 0 && usage.UnknownReceipts > 0 { return policy, total, fmt.Errorf("%w: unresolved account pricing", ErrWorkerBudget) }
		if (accountPolicy.DailyCostLimitUSD > 0 && usage.TotalCostUSD >= accountPolicy.DailyCostLimitUSD) || (accountPolicy.DailyTokensLimit > 0 && usage.TotalTokens >= accountPolicy.DailyTokensLimit) {
			return policy, total, fmt.Errorf("%w: daily account usage limit exceeded", ErrWorkerBudget)
		}
	}
	return policy, total, nil
}

func (s *SessionStore) CheckWorkerBudgetAdmission(account, worker string) error {
	if s == nil || s.store == nil {
		return errors.New("store is not configured")
	}
	unlock := s.store.sessionMutations.lockSessions("account:" + account)
	defer unlock()
	return s.checkWorkerBudgetAdmissionLocked(account, worker)
}

func (s *SessionStore) checkWorkerBudgetAdmissionLocked(account, worker string) error {
	_, _, err := s.checkWorkerBudgetLocked(account, worker, time.Now().UTC().Format("2006-01-02"))
	if err != nil {
		return err
	}
	var reservation workerBudgetReservation
	found, err := s.store.GetJSON(workerBudgetKey(account, worker)+"/reservation", &reservation)
	if err != nil { return err }
	if found { return fmt.Errorf("%w: worker allowance reserved by an unsettled operation", ErrWorkerBudget) }
	var accountReservation workerBudgetReservation
	if found, err := s.store.GetJSON(accountBudgetReservationKey(account), &accountReservation); err != nil { return err } else if found {
		return fmt.Errorf("%w: account allowance reserved by an unsettled operation", ErrWorkerBudget)
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
func (s *SessionStore) CheckWorkerSessionBudget(account, sessionID, provider, model string, operationIDs ...string) error {
	_, status := s.CalculateCostWithStatus(provider, model, 1, 1, 0, 0)
	return s.CheckWorkerSessionBudgetWithPrice(account, sessionID, status, operationIDs...)
}

// Media callers supply the canonical dimension-specific pricing status.
func (s *SessionStore) CheckWorkerSessionBudgetWithPrice(account, sessionID, priceStatus string, operationIDs ...string) error {
	if s == nil || s.store == nil {
		return errors.New("store is not configured")
	}
	unlock := s.store.sessionMutations.lockSessions("account:" + account)
	defer unlock()
	scopes, err := s.resolveUsageScopes(account, sessionID)
	if err != nil {
		return err
	}
	date := time.Now().UTC().Format("2006-01-02")
	keys := []string{}
	var existing workerBudgetReservation
	if found, err := s.store.GetJSON(accountBudgetReservationKey(account), &existing); err != nil { return err } else if found { return fmt.Errorf("%w: account operation remains unsettled", ErrWorkerBudget) }
	accountPolicy, accountActive, err := s.accountBudgetActive(account)
	if err != nil { return err }
	if accountActive {
		usage, _, err := s.GetDailyUsageAccumulator(account, date)
		if err != nil { return err }
		if (accountPolicy.DailyCostLimitUSD > 0 && usage.TotalCostUSD >= accountPolicy.DailyCostLimitUSD) || (accountPolicy.DailyTokensLimit > 0 && usage.TotalTokens >= accountPolicy.DailyTokensLimit) {
			return fmt.Errorf("%w: daily account usage limit exceeded", ErrWorkerBudget)
		}
		if accountPolicy.DailyCostLimitUSD > 0 && usage.UnknownReceipts > 0 { return fmt.Errorf("%w: unresolved account pricing", ErrWorkerBudget) }
		if accountPolicy.DailyCostLimitUSD > 0 && priceStatus != "known" && priceStatus != "free" && priceStatus != "subscription" {
			return fmt.Errorf("%w: account-capped provider pricing is unknown", ErrWorkerBudget)
		}
		keys = append(keys, accountBudgetReservationKey(account))
	}
	for _, scope := range scopes {
		if scope.Kind != "worker" {
			continue
		}
		policy, total, err := s.checkWorkerBudgetLocked(account, scope.ID, date)
		if err != nil {
			return err
		}
		if found, err := s.store.GetJSON(workerBudgetKey(account, scope.ID)+"/reservation", &existing); err != nil { return err } else if found { return fmt.Errorf("%w: worker operation remains unsettled", ErrWorkerBudget) }
		if policy.DailyCostLimitUSD == 0 && policy.DailyTokensLimit == 0 {
			continue
		}
		if total.Coverage == "repaired_receipts_incomplete" {
			return fmt.Errorf("%w: worker receipt coverage is incomplete", ErrWorkerBudget)
		}
		if policy.DailyCostLimitUSD > 0 {
			if priceStatus != "known" && priceStatus != "free" && priceStatus != "subscription" {
				return fmt.Errorf("%w: provider pricing is unknown", ErrWorkerBudget)
			}
		}
		keys = append(keys, workerBudgetKey(account, scope.ID)+"/reservation")
	}
	if len(keys) == 0 { return nil }
	operation := ""
	if len(operationIDs) == 1 { operation = strings.TrimSpace(operationIDs[0]) }
	if operation != "" {
		var received bool
		if found, err := s.store.GetJSON(workerBudgetOperationReceiptKey(account, sessionID, operation), &received); err != nil { return err } else if found { return fmt.Errorf("%w: operation identity already has a receipt", ErrWorkerBudget) }
	}
	payload, err := json.Marshal(workerBudgetReservation{SessionID: sessionID, Date: date, OperationID: operation})
	if err != nil { return err }
	batch := s.store.NewBatch()
	defer batch.Close()
	for _, key := range keys {
		if err := batch.Set([]byte(key), payload, nil); err != nil { return err }
	}
	return batch.Commit(pebble.Sync)
}

// ReleaseWorkerBudgetReservation is used only after a run has terminated and
// genuine receipts have been persisted. A run without observed receipts retains
// its reservation, including cancellation and restart; there is no timed reset.
func (s *SessionStore) ReleaseWorkerBudgetReservation(account, sessionID string, operationIDs ...string) error {
	if s == nil || s.store == nil {
		return errors.New("store is not configured")
	}
	unlock := s.store.sessionMutations.lockSessions("account:" + account)
	defer unlock()
	scopes, err := s.resolveUsageScopes(account, sessionID)
	if err != nil {
		return err
	}
	keys := []string{accountBudgetReservationKey(account)}
	for _, scope := range scopes {
		if scope.Kind == "worker" { keys = append(keys, workerBudgetKey(account, scope.ID)+"/reservation") }
	}
	operation := ""
	if len(operationIDs) == 1 { operation = strings.TrimSpace(operationIDs[0]) }
	batch := s.store.NewBatch()
	defer batch.Close()
	for _, key := range keys {
		var reservation workerBudgetReservation
		found, err := s.store.GetJSON(key, &reservation)
		if err != nil {
			return err
		}
		if !found || reservation.SessionID != sessionID {
			continue
		}
		// Legacy/unidentified calls stay blocked: a newer unrelated receipt is not settlement.
		if operation == "" || reservation.OperationID != operation { continue }
		if err := batch.Set([]byte(workerBudgetOperationReceiptKey(account, sessionID, operation)+"/terminated"), []byte("true"), nil); err != nil { return err }
		var received bool
		if _, err := s.store.GetJSON(workerBudgetOperationReceiptKey(account, sessionID, operation), &received); err != nil { return err }
		if !received { continue }
		if err := batch.Delete([]byte(key), nil); err != nil { return err }
	}
	return batch.Commit(pebble.Sync)
}

func workerBudgetOperationReceiptKey(account, session, operation string) string {
	return "worker_budget_operation_receipt/" + keyPart(account) + "/" + keyPart(session) + "/" + keyPart(operation)
}

func (s *SessionStore) setWorkerBudgetOperationReceipt(batch *pebble.Batch, account, session, operation string) error {
	if operation == "" { return nil }
	if err := batch.Set([]byte(workerBudgetOperationReceiptKey(account, session, operation)), []byte("true"), nil); err != nil { return err }
	var terminated bool
	if _, err := s.store.GetJSON(workerBudgetOperationReceiptKey(account, session, operation)+"/terminated", &terminated); err != nil { return err }
	if !terminated { return nil }
	scopes, err := s.resolveUsageScopes(account, session)
	if err != nil { return err }
	keys := []string{accountBudgetReservationKey(account)}
	for _, scope := range scopes { if scope.Kind == "worker" { keys = append(keys, workerBudgetKey(account, scope.ID)+"/reservation") } }
	for _, key := range keys {
		var reservation workerBudgetReservation
		found, err := s.store.GetJSON(key, &reservation)
		if err != nil { return err }
		if found && reservation.SessionID == session && reservation.OperationID == operation {
			if err := batch.Delete([]byte(key), nil); err != nil { return err }
		}
	}
	return nil
}

// CheckWorkerUnmeteredOperation rejects optional internal operations whose
// existing caller cannot persist a genuine receipt. Do not reserve/guess a cost
// or silently spend outside the worker budget (e.g. background title generation).
func (s *SessionStore) CheckWorkerUnmeteredOperation(account, session string) error {
	if s == nil || s.store == nil {
		return errors.New("store is not configured")
	}
	unlock := s.store.sessionMutations.lockSessions("account:" + account)
	defer unlock()
	scopes, err := s.resolveUsageScopes(account, session)
	if err != nil {
		return err
	}
	_, active, err := s.accountBudgetActive(account)
	if err != nil { return err }
	if active { return fmt.Errorf("%w: account-capped internal operation has no canonical receipt boundary", ErrWorkerBudget) }
	for _, scope := range scopes {
		if scope.Kind != "worker" {
			continue
		}
		policy, err := s.GetWorkerBudget(account, scope.ID)
		if err != nil {
			return err
		}
		if policy.DailyCostLimitUSD > 0 || policy.DailyTokensLimit > 0 {
			return fmt.Errorf("%w: internal operation has no canonical receipt boundary", ErrWorkerBudget)
		}
	}
	return nil
}

func unknownBudgetReceipt(status string) int64 {
	switch status { case "known", "free", "subscription": return 0 }
	return 1
}
