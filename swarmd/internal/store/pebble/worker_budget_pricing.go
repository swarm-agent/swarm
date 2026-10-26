package pebblestore

import (
	"errors"
	"strings"
	"time"
)

// Bound legacy repricing under the account lock. If history exceeds this limit,
// retain the original unresolved projection so USD admission fails closed.
const workerBudgetPricingReceiptLimit = 100

// workerBudgetUsageDay rejudges unresolved model pricing without changing the
// receipt ledger or its projections. Preserve media charges, coverage and known
// provider costs; only replace an unknown model price when current rules resolve it.
func (s *SessionStore) workerBudgetUsageDay(account, worker, date string) (UsageScopeTotal, error) {
	total, _, err := s.GetUsageScopeDay(account, "worker", "", worker, date)
	if err != nil || total.UnknownReceipts == 0 {
		return total, err
	}
	prefix := SessionTurnUsageByAccountPrefix(account, "")
	original := total
	visited := 0
	err = s.store.IteratePrefix(prefix, workerBudgetPricingReceiptLimit+1, func(key string, value []byte) error {
		visited++
		if visited > workerBudgetPricingReceiptLimit {
			return nil
		}
		parts := strings.Split(strings.TrimPrefix(key, prefix), "/")
		if len(parts) != 2 {
			return errors.New("invalid worker budget receipt index")
		}
		var receipt SessionTurnUsageSnapshot
		found, err := s.store.GetJSON("session_turn_usage/"+parts[0]+"/"+parts[1], &receipt)
		if err != nil {
			return err
		}
		if !found || receipt.AccountScopeID != account || KeySessionTurnUsageByAccount(account, receipt.SessionID, receipt.RunID) != key || receipt.RunID != string(value) {
			return errors.New("worker budget receipt index mismatch")
		}
		if receipt.ScopeProjectionVersion < 3 || time.UnixMilli(receipt.CreatedAt).UTC().Format("2006-01-02") != date || unknownBudgetReceipt(receipt.PriceStatus) == 0 {
			return nil
		}
		for _, scope := range receipt.ScopeTotals {
			if scope.Kind != "worker" || scope.ID != worker {
				continue
			}
			current := receipt
			current.EstimatedCostUSD, current.PriceStatus = s.calculateReceiptCost(receipt)
			if unknownBudgetReceipt(current.PriceStatus) != 0 {
				return nil
			}
			current.CostProvenance = "catalog"
			// A plan has no separately billed service tier. Other unresolved
			// tier pricing must continue to fail closed.
			if current.PriceStatus == "subscription" {
				current.ServiceTierStatus = ""
			}
			applyScopeReceipt(&total, receipt, -1)
			applyScopeReceipt(&total, current, 1)
			break
		}
		return nil
	})
	if visited > workerBudgetPricingReceiptLimit {
		return original, err
	}
	return total, err
}
