package pebblestore

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/cockroachdb/pebble"
)

// WorkerBudgetHold is a UTC-day stop, independent of user-owned lifecycle state.
// Raising/unsetting a policy never resumes an exhausted worker within that day.
type WorkerBudgetHold struct {
	Date      string  `json:"date"`
	Reason    string  `json:"reason"`
	CapSource string  `json:"cap_source"`
	Dimension string  `json:"dimension"`
	Limit     float64 `json:"limit"`
	Usage     float64 `json:"usage"`
	ResetAt   int64   `json:"reset_at"`
}

func workerBudgetHoldKey(account, worker, date string) string {
	return workerBudgetKey(account, worker) + "/hold/" + date
}

func budgetExhaustion(date, source, dimension string, limit, usage float64) *WorkerBudgetHold {
	if limit <= 0 || math.IsNaN(limit) || math.IsInf(limit, 0) || usage < limit {
		return nil
	}
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		return nil
	}
	return &WorkerBudgetHold{Date: date, Reason: "daily_budget_exhausted", CapSource: source, Dimension: dimension, Limit: limit, Usage: usage, ResetAt: day.AddDate(0, 0, 1).UnixMilli()}
}

func workerExhaustion(policy WorkerBudgetPolicy, total UsageScopeTotal, date string) *WorkerBudgetHold {
	if h := budgetExhaustion(date, "worker", "usd", policy.DailyCostLimitUSD, total.CatalogCostUSD+total.ProviderCostUSD+total.ProviderEstimateCostUSD); h != nil {
		return h
	}
	return budgetExhaustion(date, "worker", "tokens", float64(policy.DailyTokensLimit), float64(total.TotalTokens))
}

func accountExhaustion(policy UsageLimitRecord, total DailyUsageAccumulator, date string) *WorkerBudgetHold {
	if !policy.Enabled {
		return nil
	}
	if h := budgetExhaustion(date, "account", "usd", policy.DailyCostLimitUSD, total.TotalCostUSD); h != nil {
		return h
	}
	return budgetExhaustion(date, "account", "tokens", float64(policy.DailyTokensLimit), float64(total.TotalTokens))
}

// Called under the account mutation lock. Hold and canonical notification/index
// share the receipt/admission batch: a crash cannot lose the alert after stopping.
// The day-key is also the idempotency marker, even if the user clears the inbox.
func (s *SessionStore) setWorkerBudgetHold(batch *pebble.Batch, account, worker string, hold *WorkerBudgetHold) error {
	if hold == nil {
		return nil
	}
	var existing WorkerBudgetHold
	if found, err := s.store.GetJSON(workerBudgetHoldKey(account, worker, hold.Date), &existing); err != nil {
		return err
	} else if found {
		return nil
	}
	if _, err := s.GetWorkerBudget(account, worker); err != nil {
		return err
	}
	raw, err := json.Marshal(hold)
	if err != nil {
		return err
	}
	if err = batch.Set([]byte(workerBudgetHoldKey(account, worker, hold.Date)), raw, nil); err != nil {
		return err
	}
	node, found, err := NewSwarmStore(s.store).GetLocalNode()
	if err != nil {
		return err
	}
	// Tests and uninitialized stores may lack runtime identity. Keep the durable
	// hold; initialized daemons always have the canonical local node identity.
	if !found || node.SwarmID == "" {
		return nil
	}
	day, err := time.Parse("2006-01-02", hold.Date)
	if err != nil {
		return err
	}
	// Stable day timestamp prevents two participants in the same receipt batch
	// from creating duplicate indexes for the same worker/day notification.
	now := day.UnixMilli()
	n := NotificationRecord{ID: "worker-budget-" + worker + "-" + hold.Date, AccountScopeID: account, SwarmID: node.SwarmID, WorkerID: worker, Category: NotificationCategorySystem, Kind: NotificationKindSystem, Severity: NotificationSeverityWarning, Status: NotificationStatusActive, SourceEventType: "worker.budget.exhausted", Title: "Worker stopped: daily budget reached", Body: fmt.Sprintf("Worker %s reached the %s daily %s limit (%g of %g). No new work until %s. Already dispatched calls may overshoot.", worker, hold.CapSource, hold.Dimension, hold.Usage, hold.Limit, time.UnixMilli(hold.ResetAt).UTC().Format(time.RFC3339)), CreatedAt: now, UpdatedAt: now, Payload: map[string]any{"worker_id": worker, "budget_hold": hold}}
	raw, err = json.Marshal(n)
	if err != nil {
		return err
	}
	key := notificationKeyForAccount(account, n.SwarmID, n.ID)
	if err = batch.Set([]byte(key), raw, nil); err != nil {
		return err
	}
	return batch.Set([]byte(notificationBySwarmKeyForAccount(account, n.SwarmID, now, n.ID)), []byte(key), nil)
}

func (s *SessionStore) holdWorkerBudgetLocked(account, worker string, h *WorkerBudgetHold) error {
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := s.setWorkerBudgetHold(batch, account, worker, h); err != nil {
		return err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s %s limit reached; held until %s", ErrWorkerBudget, h.CapSource, h.Dimension, time.UnixMilli(h.ResetAt).UTC().Format(time.RFC3339))
}

// Settlement of an account receipt also stops each worker whose next admission
// would fail. The bounded worker catalog is authoritative; ordinary sessions
// are still never classified or reserved as worker work.
func (s *SessionStore) setAccountWorkerBudgetHolds(batch *pebble.Batch, acc DailyUsageAccumulator) error {
	policy, _, err := s.GetUsageLimit(acc.AccountScopeID)
	if err != nil {
		return err
	}
	h := accountExhaustion(policy, acc, acc.Date)
	if h == nil {
		return nil
	}
	count := 0
	return s.store.IteratePrefix(KeyWorkerAccountPrefix+keyPart(acc.AccountScopeID)+"/", 10001, func(_ string, raw []byte) error {
		count++
		if count > 10000 {
			return fmt.Errorf("worker budget settlement exceeds bounded worker catalog")
		}
		var w WorkerRecord
		if err := json.Unmarshal(raw, &w); err != nil {
			return err
		}
		if w.AccountScopeID != acc.AccountScopeID {
			return ErrWorkerInvalid
		}
		if w.LifecycleState == WorkerLifecycleStateDeleted {
			return nil
		}
		return s.setWorkerBudgetHold(batch, acc.AccountScopeID, w.ID, h)
	})
}

// Wakeups are accelerators only: the notification and hold remain durable if
// delivery is missed. API publication uses stable notification idempotency.
func (s *Store) SetWorkerBudgetPublisher(publish func(NotificationRecord)) {
	s.workerPublisherMu.Lock()
	defer s.workerPublisherMu.Unlock()
	s.workerBudgetPublisher = publish
}

func (s *SessionStore) publishWorkerBudgetHolds(account string) {
	s.store.workerPublisherMu.RLock()
	publish := s.store.workerBudgetPublisher
	s.store.workerPublisherMu.RUnlock()
	if publish == nil {
		return
	}
	node, found, err := NewSwarmStore(s.store).GetLocalNode()
	if err != nil || !found {
		return
	}
	date := time.Now().UTC().Format("2006-01-02")
	_ = s.store.IteratePrefix(KeyWorkerAccountPrefix+keyPart(account)+"/", 10000, func(_ string, raw []byte) error {
		var w WorkerRecord
		if err := json.Unmarshal(raw, &w); err != nil {
			return err
		}
		n, found, err := NewNotificationStore(s.store).GetNotificationForAccount(account, node.SwarmID, "worker-budget-"+w.ID+"-"+date)
		if err != nil {
			return err
		}
		if found {
			publish(n)
		}
		return nil
	})
}
