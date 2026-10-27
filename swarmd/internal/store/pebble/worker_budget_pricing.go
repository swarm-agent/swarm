package pebblestore

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Bound matching worker-day receipts, not unrelated account history. Discovery
// uses today's rollups and frozen session bindings, including delegated children.
const workerBudgetPricingReceiptLimit = 100

// workerBudgetUsageDay rejudges unresolved model pricing without changing the
// receipt ledger or its projections. Preserve media charges, coverage and known
// provider costs; only replace an unknown model price when current rules resolve it.
func (s *SessionStore) workerBudgetUsageDay(account, worker, date string) (UsageScopeTotal, error) {
	total, _, err := s.GetUsageScopeDay(account, "worker", "", worker, date)
	if err != nil || total.UnknownReceipts == 0 {
		return total, err
	}
	sessions, err := s.workerBudgetDaySessions(account, worker, date)
	if err != nil {
		return total, err
	}
	original := total
	visited, scanned := 0, 0
	exhausted := errors.New("worker budget pricing read bound")
	for session := range sessions {
		prefix := SessionTurnUsageByAccountPrefix(account, session)
		err = s.store.IteratePrefix(prefix, workerBudgetPricingDiscoveryLimit+1, func(key string, value []byte) error {
			scanned++
			if scanned > workerBudgetPricingDiscoveryLimit {
				return exhausted
			}
			parts := strings.Split(strings.TrimPrefix(key, SessionTurnUsageByAccountPrefix(account, "")), "/")
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
				visited++
				if visited > workerBudgetPricingReceiptLimit {
					return exhausted
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
		if errors.Is(err, exhausted) {
			return original, nil
		}
		if err != nil {
			return original, err
		}
	}
	return total, nil
}

// Rollups are written atomically with receipts. They locate today's sessions;
// immutable usage bindings, rather than mutable session metadata, select workers.
// A separate discovery bound protects the account lock without charging unrelated
// receipts against the worker-day repricing limit. No receipt or index is rewritten.
const workerBudgetPricingDiscoveryLimit = 10000

func (s *SessionStore) workerBudgetDaySessions(account, worker, date string) (map[string]bool, error) {
	sessions := map[string]bool{}
	seen := map[string]bool{}
	count := 0
	prefix := AccountUsageRollupPrefix(account) + keyPart(date) + "/"
	err := s.store.IteratePrefix(prefix, workerBudgetPricingDiscoveryLimit+1, func(key string, value []byte) error {
		count++
		if count > workerBudgetPricingDiscoveryLimit {
			return errors.New("worker budget pricing discovery bound exceeded")
		}
		var rollup AccountUsageRollup
		if err := json.Unmarshal(value, &rollup); err != nil {
			return err
		}
		if rollup.AccountScopeID != account || rollup.Date != date || KeyAccountUsageRollup(account, date, rollup.SessionID, rollup.Provider, rollup.Model) != key {
			return errors.New("worker budget rollup mismatch")
		}
		if seen[rollup.SessionID] {
			return nil
		}
		seen[rollup.SessionID] = true
		var scopes []UsageScopeTotal
		if _, err := s.store.GetJSON(usageBindingKey(account, rollup.SessionID), &scopes); err != nil {
			return err
		}
		for _, scope := range scopes {
			if scope.Kind == "worker" && scope.ID == worker {
				sessions[rollup.SessionID] = true
			}
		}
		return nil
	})
	return sessions, err
}
