package pebblestore

import (
	"errors"
	"math"
	"time"
)

// WorkerBudgetStatus is an indexed UTC-day view, not a second charge ledger.
// Nil remaining means unset; observed coverage is never historical completeness.
type WorkerBudgetStatus struct {
	WorkerBudgetPolicy
	Date string `json:"date"`
	Usage UsageScopeTotal `json:"usage"`
	RemainingCostUSD *float64 `json:"remaining_cost_usd"`
	RemainingTokens *int64 `json:"remaining_tokens"`
	Blocked bool `json:"blocked"`
	BlockedReason string `json:"blocked_reason,omitempty"`
	Inflight bool `json:"inflight"`
	AccountPolicy UsageLimitRecord `json:"account_policy"`
	AccountUsage DailyUsageAccumulator `json:"account_usage"`
	AccountRemainingCostUSD *float64 `json:"account_remaining_cost_usd"`
	AccountRemainingTokens *int64 `json:"account_remaining_tokens"`
	AccountInflight bool `json:"account_inflight"`
	AccountCoverage string `json:"account_coverage"`
	Limitations string `json:"limitations"`
}

func remainingBudget(cost float64, tokens int64, usedCost float64, usedTokens int64) (*float64, *int64) {
	var c *float64
	var n *int64
	if cost > 0 { v := math.Max(0, cost-usedCost); c = &v }
	if tokens > 0 { v := tokens-usedTokens; if v < 0 { v = 0 }; n = &v }
	return c, n
}

func (s *SessionStore) GetWorkerBudgetStatus(account, worker string) (WorkerBudgetStatus, error) {
	var status WorkerBudgetStatus
	if s == nil || s.store == nil { return status, errors.New("store is not configured") }
	unlock := s.store.sessionMutations.lockSessions("account:"+account)
	defer unlock()
	policy, err := s.GetWorkerBudget(account, worker)
	if err != nil { return status, err }
	status.WorkerBudgetPolicy = policy
	status.Date = time.Now().UTC().Format("2006-01-02")
	status.Usage, _, err = s.GetUsageScopeDay(account, "worker", "", worker, status.Date)
	if err != nil { return status, err }
	status.RemainingCostUSD, status.RemainingTokens = remainingBudget(policy.DailyCostLimitUSD, policy.DailyTokensLimit, status.Usage.CatalogCostUSD+status.Usage.ProviderCostUSD+status.Usage.ProviderEstimateCostUSD, status.Usage.TotalTokens)
	status.AccountPolicy, _, err = s.GetUsageLimit(account)
	if err != nil { return status, err }
	if status.AccountPolicy.AccountScopeID == "" { status.AccountPolicy.AccountScopeID = account }
	var accountRecorded bool
	status.AccountUsage, accountRecorded, err = s.GetDailyUsageAccumulator(account, status.Date)
	if err != nil { return status, err }
	status.AccountCoverage = "no_records"
	if accountRecorded { status.AccountCoverage = "observed_receipts_only" }
	if status.AccountUsage.AccountScopeID == "" { status.AccountUsage.AccountScopeID, status.AccountUsage.Date = account, status.Date }
	if status.AccountPolicy.Enabled {
		status.AccountRemainingCostUSD, status.AccountRemainingTokens = remainingBudget(status.AccountPolicy.DailyCostLimitUSD, status.AccountPolicy.DailyTokensLimit, status.AccountUsage.TotalCostUSD, status.AccountUsage.TotalTokens)
	}
	var reservation workerBudgetReservation
	status.Inflight, err = s.store.GetJSON(workerBudgetKey(account, worker)+"/reservation", &reservation)
	if err != nil { return status, err }
	status.AccountInflight, err = s.store.GetJSON(accountBudgetReservationKey(account), &reservation)
	if err != nil { return status, err }
	if err := s.checkWorkerBudgetAdmissionLocked(account, worker); err != nil {
		if !errors.Is(err, ErrWorkerBudget) { return status, err }
		status.Blocked, status.BlockedReason = true, err.Error()
	}
	status.Limitations = "Stop-before-next-call, not an invoice-hard cap; admitted work may overshoot. Unsettled or unidentified operations survive restart and UTC rollover. Totals reflect observed receipts only; unknown pricing is not free."
	return status, nil
}
