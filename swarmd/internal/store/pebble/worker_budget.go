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
	SessionID   string  `json:"session_id"`
	Date        string  `json:"date"`
	OperationID string  `json:"operation_id,omitempty"`
	CostUSD     float64 `json:"cost_usd"`
	CreatedAt   int64   `json:"created_at"`
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

// WorkerBudgetRequest is a proposed policy change. A non-nil zero value clears
// limits; nil leaves the existing policy unchanged.
type WorkerBudgetRequest struct {
	DailyUSD    float64 `json:"daily_usd"`
	DailyTokens int64   `json:"daily_tokens,omitempty"`
}

func (b *WorkerBudgetRequest) Validate() error {
	if b != nil && (math.IsNaN(b.DailyUSD) || math.IsInf(b.DailyUSD, 0) || b.DailyUSD < 0 || b.DailyTokens < 0) {
		return errors.New("invalid worker budget limits")
	}
	return nil
}

// SetWorkerBudget is the authorized policy mutation service. Acceptance shares
// its preparation boundary to commit the policy and worker atomically.
func (s *SessionStore) SetWorkerBudget(account, worker string, expected uint64, cost float64, tokens int64, users ...string) (WorkerBudgetPolicy, error) {
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 || tokens < 0 {
		return WorkerBudgetPolicy{}, errors.New("invalid worker budget limits")
	}
	if s == nil || s.store == nil {
		return WorkerBudgetPolicy{}, errors.New("store is not configured")
	}
	unlock := s.store.sessionMutations.lockSessions("account:" + account)
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	policy, err := s.prepareWorkerBudget(account, worker, expected, cost, tokens)
	if err != nil {
		return policy, err
	}
	user := ""
	if len(users) == 1 {
		user = users[0]
	}
	mutation := &workerRealtimeMutation{accountScopeID: account, userID: user, workerID: worker}
	if err := mutation.put(workerBudgetKey(account, worker), policy); err != nil {
		return policy, err
	}
	if err := mutation.setPayload(WorkerRealtimePayload{WorkerID: worker, BudgetRevision: policy.Revision, ChangeSummary: "budget policy updated"}); err != nil {
		return policy, err
	}
	if err := s.store.commitWorkerRealtime(mutation); err != nil {
		return policy, err
	}
	unlock()
	locked = false
	s.store.publishWorkerRealtime(mutation)
	return policy, nil
}

// prepareWorkerBudget requires the account mutation lock; callers commit the
// returned policy with their own atomic realtime mutation.
func (s *SessionStore) prepareWorkerBudget(account, worker string, expected uint64, cost float64, tokens int64) (WorkerBudgetPolicy, error) {
	if err := (&WorkerBudgetRequest{DailyUSD: cost, DailyTokens: tokens}).Validate(); err != nil {
		return WorkerBudgetPolicy{}, err
	}
	policy, err := s.GetWorkerBudget(account, worker)
	if err != nil {
		return policy, err
	}
	if policy.Revision != expected {
		return policy, ErrWorkerConflict
	}
	if policy.Revision == ^uint64(0) {
		return policy, ErrWorkerConflict
	}
	accountPolicy, _, err := s.GetUsageLimit(account)
	if err != nil {
		return policy, err
	}
	if accountPolicy.Enabled && ((accountPolicy.DailyCostLimitUSD > 0 && !math.IsInf(accountPolicy.DailyCostLimitUSD, 0) && cost > accountPolicy.DailyCostLimitUSD) || (accountPolicy.DailyTokensLimit > 0 && tokens > accountPolicy.DailyTokensLimit)) {
		return policy, errors.New("worker daily limit cannot exceed the enabled overall daily limit; change the overall limit in Usage")
	}
	policy.Revision++
	policy.DailyCostLimitUSD, policy.DailyTokensLimit, policy.UpdatedAt = cost, tokens, time.Now().UnixMilli()
	return policy, nil
}

// checkWorkerBudgetLocked reads the canonical indexed UTC day, not context
// occupancy. Unknown receipts are not zero-dollar receipts under a cost cap:
// they count at their conservative estimate (unpriced_estimate.go).
func (s *SessionStore) checkWorkerBudgetLocked(account, worker, date string) (WorkerBudgetPolicy, UsageScopeTotal, error) {
	policy, err := s.GetWorkerBudget(account, worker)
	if err != nil {
		return policy, UsageScopeTotal{}, err
	}
	var total UsageScopeTotal
	if policy.DailyCostLimitUSD > 0 {
		total, err = s.workerBudgetUsageDay(account, worker, date)
	} else {
		total, _, err = s.GetUsageScopeDay(account, "worker", "", worker, date)
	}
	if err != nil {
		return policy, total, err
	}
	return s.checkWorkerBudgetTotalLocked(account, worker, date, policy, total)
}

// The caller holds the account lock and supplies one consistent usage read.
func (s *SessionStore) checkWorkerBudgetTotalLocked(account, worker, date string, policy WorkerBudgetPolicy, total UsageScopeTotal) (WorkerBudgetPolicy, UsageScopeTotal, error) {
	var hold WorkerBudgetHold
	if found, err := s.store.GetJSON(workerBudgetHoldKey(account, worker, date), &hold); err != nil {
		return policy, total, err
	} else if found {
		return policy, total, fmt.Errorf("%w: daily budget hold until %s", ErrWorkerBudget, time.UnixMilli(hold.ResetAt).UTC().Format(time.RFC3339))
	}
	if h := workerExhaustion(policy, total, date); h != nil {
		return policy, total, s.holdWorkerBudgetLocked(account, worker, h)
	}
	if policy.DailyCostLimitUSD > 0 {
		if total.Coverage == "repaired_receipts_incomplete" {
			return policy, total, fmt.Errorf("%w: unresolved worker receipt coverage", ErrWorkerBudget)
		}
		estimate := total.UnpricedEstimateUSD()
		cost := total.CatalogCostUSD + total.ProviderCostUSD + total.ProviderEstimateCostUSD + estimate
		if cost >= policy.DailyCostLimitUSD {
			return policy, total, fmt.Errorf("%w: daily worker USD limit exceeded%s", ErrWorkerBudget, unpricedLimitDetail(total.UnknownReceipts, estimate))
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
		if h := accountExhaustion(accountPolicy, usage, date); h != nil {
			return policy, total, s.holdWorkerBudgetLocked(account, worker, h)
		}
		if accountPolicy.DailyCostLimitUSD > 0 && usage.PricingCoverageIncomplete {
			return policy, total, fmt.Errorf("%w: unresolved account pricing coverage", ErrWorkerBudget)
		}
		estimate := usage.UnpricedEstimateUSD()
		if accountPolicy.DailyCostLimitUSD > 0 && usage.TotalCostUSD+estimate >= accountPolicy.DailyCostLimitUSD {
			return policy, total, fmt.Errorf("%w: daily account usage limit exceeded%s", ErrWorkerBudget, unpricedLimitDetail(usage.UnknownReceipts, estimate))
		}
		if accountPolicy.DailyTokensLimit > 0 && usage.TotalTokens >= accountPolicy.DailyTokensLimit {
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
	err := s.checkWorkerBudgetAdmissionLocked(account, worker)
	unlock()
	if errors.Is(err, ErrWorkerBudget) {
		s.publishWorkerBudgetHolds(account)
	}
	return err
}

func (s *SessionStore) checkWorkerBudgetAdmissionLocked(account, worker string) error {
	_, _, err := s.checkWorkerBudgetLocked(account, worker, time.Now().UTC().Format("2006-01-02"))
	if err != nil {
		return err
	}
	return s.checkWorkerBudgetReservationsLocked(account, worker)
}

func (s *SessionStore) checkWorkerBudgetReservationsLocked(account, worker string) error {
	if _, err := s.openWorkerBudgetCost(workerBudgetKey(account, worker) + "/reservation"); err != nil {
		return err
	}
	_, err := s.openWorkerBudgetCost(accountBudgetReservationKey(account))
	return err
}

// CheckWorkerSessionBudget resolves trusted durable descendant lineage on every
// new billable operation. Model calls have no output estimate (subscription calls
// cost zero); media reserves its canonical estimate. Reservations expire in an hour.
// This is a stop-before-next-call control, not an invoice-hard cap: an already
// admitted provider operation can overshoot before its genuine receipt arrives.
func (s *SessionStore) CheckWorkerSessionBudget(account, sessionID, provider, model string, operationIDs ...string) error {
	_, status := s.CalculateCostWithStatus(provider, model, 1, 1, 0, 0, 0)
	return s.CheckWorkerSessionBudgetWithPrice(account, sessionID, status, operationIDs...)
}

// Media callers supply the canonical dimension-specific pricing status.
func (s *SessionStore) CheckWorkerSessionBudgetWithPrice(account, sessionID, priceStatus string, operationIDs ...string) error {
	return s.checkWorkerSessionBudget(account, sessionID, priceStatus, nil, operationIDs...)
}

// CheckWorkerMediaAdmission reserves the estimate when an operation ID is supplied.
// Calls without an ID are read-only admission previews.
func (s *SessionStore) CheckWorkerMediaAdmission(account, sessionID string, estimate MediaCostEstimate, operationIDs ...string) error {
	if math.IsNaN(estimate.CostUSD) || math.IsInf(estimate.CostUSD, 0) || estimate.CostUSD < 0 {
		return errors.New("invalid media cost estimate")
	}
	return s.checkWorkerSessionBudget(account, sessionID, estimate.PriceStatus, &estimate.CostUSD, operationIDs...)
}

func (s *SessionStore) checkWorkerSessionBudget(account, sessionID, priceStatus string, estimatedCost *float64, operationIDs ...string) error {
	if s == nil || s.store == nil {
		return errors.New("store is not configured")
	}
	unlock := s.store.sessionMutations.lockSessions("account:" + account)
	defer unlock()
	scopes, err := s.resolveUsageScopes(account, sessionID)
	if err != nil {
		return err
	}
	// Account limits do not make ordinary sessions into worker runs. Resolve
	// corroborated worker ownership before reading or reserving any allowance.
	workerOwned := false
	for _, scope := range scopes {
		if scope.Kind == "worker" {
			workerOwned = true
			break
		}
	}
	if !workerOwned {
		return nil
	}
	limits, err := s.GetLimits(account)
	if err != nil {
		return err
	}
	for _, scope := range scopes {
		if scope.Kind != "worker_run" {
			continue
		}
		if err := s.store.observeLimit(account, scope.ProjectID+"/"+scope.ID+"/pricing", "job_cost_usd", limits.JobCostUSD, limits.JobCostUSD > 0 && priceStatus != "known" && priceStatus != "free" && priceStatus != "subscription"); err != nil {
			return err
		}
	}
	if err := s.checkJobLimits(account, scopes); err != nil {
		return err
	}
	date := time.Now().UTC().Format("2006-01-02")
	// Exhaustion precedes reservations so they cannot hide a genuine day stop.
	// Reuse these reads while the account lock keeps receipts/policies stable.
	type checkedBudget struct {
		policy WorkerBudgetPolicy
		total  UsageScopeTotal
	}
	checked := make(map[string]checkedBudget)
	for _, scope := range scopes {
		if scope.Kind == "worker" {
			if _, ok := checked[scope.ID]; ok {
				continue
			}
			policy, total, err := s.checkWorkerBudgetLocked(account, scope.ID, date)
			if err != nil {
				return err
			}
			checked[scope.ID] = checkedBudget{policy, total}
		}
	}
	keys := []string{}
	accountReserved, err := s.openWorkerBudgetCost(accountBudgetReservationKey(account))
	if err != nil {
		return err
	}
	cost := 0.0
	if estimatedCost != nil {
		cost = *estimatedCost
	}
	accountPolicy, accountActive, err := s.accountBudgetActive(account)
	if err != nil {
		return err
	}
	if accountActive {
		usage, _, err := s.GetDailyUsageAccumulator(account, date)
		if err != nil {
			return err
		}
		if (accountPolicy.DailyCostLimitUSD > 0 && usage.TotalCostUSD >= accountPolicy.DailyCostLimitUSD) || (accountPolicy.DailyTokensLimit > 0 && usage.TotalTokens >= accountPolicy.DailyTokensLimit) {
			return fmt.Errorf("%w: daily account usage limit exceeded", ErrWorkerBudget)
		}
		if accountPolicy.DailyCostLimitUSD > 0 && usage.PricingCoverageIncomplete {
			return fmt.Errorf("%w: unresolved account pricing coverage", ErrWorkerBudget)
		}
		if accountPolicy.DailyCostLimitUSD > 0 && priceStatus != "known" && priceStatus != "free" && priceStatus != "subscription" {
			return fmt.Errorf("%w: account-capped provider pricing is unknown", ErrWorkerBudget)
		}
		estimate := usage.UnpricedEstimateUSD()
		if accountPolicy.DailyCostLimitUSD > 0 && (usage.TotalCostUSD+estimate >= accountPolicy.DailyCostLimitUSD || usage.TotalCostUSD+estimate+accountReserved+cost > accountPolicy.DailyCostLimitUSD) {
			return fmt.Errorf("%w: daily account usage limit exceeded%s", ErrWorkerBudget, unpricedLimitDetail(usage.UnknownReceipts, estimate))
		}
		keys = append(keys, accountBudgetReservationKey(account))
	}
	for _, scope := range scopes {
		if scope.Kind != "worker" {
			continue
		}
		policy, total := checked[scope.ID].policy, checked[scope.ID].total
		reserved, err := s.openWorkerBudgetCost(workerBudgetKey(account, scope.ID) + "/reservation")
		if err != nil {
			return err
		}
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
		if policy.DailyCostLimitUSD > 0 && total.CatalogCostUSD+total.ProviderCostUSD+total.ProviderEstimateCostUSD+total.UnpricedEstimateUSD()+reserved+cost > policy.DailyCostLimitUSD {
			return fmt.Errorf("%w: daily worker usage limit exceeded%s", ErrWorkerBudget, unpricedLimitDetail(total.UnknownReceipts, total.UnpricedEstimateUSD()))
		}
		keys = append(keys, workerBudgetKey(account, scope.ID)+"/reservation")
	}
	if estimatedCost != nil {
		for _, scope := range scopes {
			if scope.Kind != "worker_run" || limits.JobCostUSD <= 0 {
				continue
			}
			total, _, err := s.GetUsageScope(account, scope.Kind, scope.ProjectID, scope.ID)
			if err != nil {
				return err
			}
			// An estimate is not incurred usage: refuse without recording a
			// crossing or emitting a limit.reached signal.
			if total.CatalogCostUSD+total.ProviderCostUSD+total.ProviderEstimateCostUSD+*estimatedCost > limits.JobCostUSD {
				return &LimitReached{"limit.reached", "job_cost_usd", limits.JobCostUSD}
			}
		}
		if len(operationIDs) == 0 {
			return nil
		}
	}
	if len(keys) == 0 {
		return nil
	}
	operation := ""
	if len(operationIDs) == 1 {
		operation = strings.TrimSpace(operationIDs[0])
	}
	if operation != "" {
		var received bool
		if found, err := s.store.GetJSON(workerBudgetOperationReceiptKey(account, sessionID, operation), &received); err != nil {
			return err
		} else if found {
			return fmt.Errorf("%w: operation identity already has a receipt", ErrWorkerBudget)
		}
	}
	payload, err := json.Marshal(workerBudgetReservation{SessionID: sessionID, Date: date, OperationID: operation, CostUSD: cost, CreatedAt: time.Now().UnixMilli()})
	if err != nil {
		return err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	for _, key := range keys {
		key = budgetOperationKey(key, sessionID, operation)
		var existing workerBudgetReservation
		if found, err := s.store.GetJSON(key, &existing); err != nil {
			return err
		} else if found {
			return fmt.Errorf("%w: operation already reserved", ErrWorkerBudget)
		}
		if err := batch.Set([]byte(key), payload, nil); err != nil {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
}

// ReleaseWorkerBudgetReservation releases only the completed operation, including
// failures without receipts. Durable usage remains authoritative for exhaustion.
func (s *SessionStore) ReleaseWorkerBudgetReservation(account, sessionID string, operationIDs ...string) error {
	session := sessionID
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
		if scope.Kind == "worker" {
			keys = append(keys, workerBudgetKey(account, scope.ID)+"/reservation")
		}
	}
	operation := ""
	if len(operationIDs) == 1 {
		operation = strings.TrimSpace(operationIDs[0])
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	for _, key := range keys {
		key = budgetOperationKey(key, session, operation)
		var reservation workerBudgetReservation
		found, err := s.store.GetJSON(key, &reservation)
		if err != nil {
			return err
		}
		if !found || reservation.SessionID != sessionID {
			continue
		}
		// Release only the matching operation's reservation; other operations remain
		// reserved until they finish or expire after one hour.
		if operation == "" || reservation.OperationID != operation {
			continue
		}
		if err := batch.Set([]byte(workerBudgetOperationReceiptKey(account, sessionID, operation)+"/terminated"), []byte("true"), nil); err != nil {
			return err
		}
		if err := batch.Delete([]byte(key), nil); err != nil {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
}

func workerBudgetOperationReceiptKey(account, session, operation string) string {
	return "worker_budget_operation_receipt/" + keyPart(account) + "/" + keyPart(session) + "/" + keyPart(operation)
}

func (s *SessionStore) setWorkerBudgetOperationReceipt(batch *pebble.Batch, account, session, operation string) error {
	if operation == "" {
		return nil
	}
	if err := batch.Set([]byte(workerBudgetOperationReceiptKey(account, session, operation)), []byte("true"), nil); err != nil {
		return err
	}
	var terminated bool
	if _, err := s.store.GetJSON(workerBudgetOperationReceiptKey(account, session, operation)+"/terminated", &terminated); err != nil {
		return err
	}
	if !terminated {
		return nil
	}
	scopes, err := s.resolveUsageScopes(account, session)
	if err != nil {
		return err
	}
	keys := []string{accountBudgetReservationKey(account)}
	for _, scope := range scopes {
		if scope.Kind == "worker" {
			keys = append(keys, workerBudgetKey(account, scope.ID)+"/reservation")
		}
	}
	for _, key := range keys {
		key = budgetOperationKey(key, session, operation)
		var reservation workerBudgetReservation
		found, err := s.store.GetJSON(key, &reservation)
		if err != nil {
			return err
		}
		if found && reservation.SessionID == session && reservation.OperationID == operation {
			if err := batch.Delete([]byte(key), nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// CheckWorkerUnmeteredOperation rejects optional internal operations whose
// existing caller cannot persist a genuine receipt, only for explicitly capped
// workers resolved through durable lineage. Account caps alone do not classify
// internal operations as worker work. Do not reserve/guess a cost
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
	for _, scope := range scopes {
		if scope.Kind != "worker" {
			continue
		}
		policy, _, err := s.checkWorkerBudgetLocked(account, scope.ID, time.Now().UTC().Format("2006-01-02"))
		if err != nil {
			return err
		}
		_, accountActive, err := s.accountBudgetActive(account)
		if err != nil {
			return err
		}
		if accountActive || policy.DailyCostLimitUSD > 0 || policy.DailyTokensLimit > 0 {
			return fmt.Errorf("%w: internal operation has no canonical receipt boundary", ErrWorkerBudget)
		}
	}
	return nil
}

// WorkerBudgetActive reports whether session is worker-owned (through durable
// lineage) and a worker or account budget applies to it, so each provider
// call must reserve and settle one operation before the next may start.
func (s *SessionStore) WorkerBudgetActive(account, session string) (bool, error) {
	if s == nil || s.store == nil {
		return false, errors.New("store is not configured")
	}
	unlock := s.store.sessionMutations.lockSessions("account:" + account)
	defer unlock()
	scopes, err := s.resolveUsageScopes(account, session)
	if err != nil {
		return false, err
	}
	for _, scope := range scopes {
		if scope.Kind != "worker" {
			continue
		}
		policy, err := s.GetWorkerBudget(account, scope.ID)
		if err != nil {
			return false, err
		}
		_, accountActive, err := s.accountBudgetActive(account)
		if err != nil {
			return false, err
		}
		if accountActive || policy.DailyCostLimitUSD > 0 || policy.DailyTokensLimit > 0 {
			return true, nil
		}
	}
	return false, nil
}

func unknownBudgetReceipt(status string) int64 {
	switch status {
	case "known", "free", "subscription":
		return 0
	}
	return 1
}
