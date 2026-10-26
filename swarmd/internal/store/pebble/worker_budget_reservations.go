package pebblestore

import (
	"encoding/json"
	"github.com/cockroachdb/pebble"
	"strings"
	"time"
)

func budgetOperationKey(base, session, operation string) string {
	return base + "/" + keyPart(session) + "/" + keyPart(operation)
}

// Caller holds the account mutation lock. Legacy reservations lack a timestamp
// and are expired too, so upgrading cannot preserve an indefinite crash lock.
func (s *SessionStore) openWorkerBudgetCost(base string) (float64, error) {
	batch := s.store.NewBatch()
	defer batch.Close()
	cost := 0.0
	cutoff := time.Now().Add(-time.Hour).UnixMilli()
	err := s.store.IteratePrefix(base, 0, func(key string, value []byte) error {
		if key != base && !strings.HasPrefix(key, base+"/") {
			return nil
		}
		var r workerBudgetReservation
		if err := json.Unmarshal(value, &r); err != nil {
			return err
		}
		if r.CreatedAt <= cutoff {
			return batch.Delete([]byte(key), nil)
		}
		cost += r.CostUSD
		return nil
	})
	if err != nil {
		return 0, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, err
	}
	return cost, nil
}

// hasOpenWorkerBudgetReservation reports presence, not cost: subscription turns
// reserve zero dollars but are still in flight. Status reads do not prune records.
func (s *SessionStore) hasOpenWorkerBudgetReservation(base string) (bool, error) {
	cutoff := time.Now().Add(-time.Hour).UnixMilli()
	found := false
	err := s.store.IteratePrefix(base+"/", 0, func(_ string, value []byte) error {
		var r workerBudgetReservation
		if err := json.Unmarshal(value, &r); err != nil {
			return err
		}
		if r.CreatedAt > cutoff {
			found = true
		}
		return nil
	})
	return found, err
}
