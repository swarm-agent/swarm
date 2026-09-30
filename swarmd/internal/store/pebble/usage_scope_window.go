package pebblestore

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/cockroachdb/pebble"
)

// UTC day projections are indexed alongside lifetime projections; reads never
// scan receipts. Windows have the same observed-only historical coverage.
func usageScopeDayKey(account string, scope UsageScopeTotal, date string) string {
	return usageScopeKey(account, scope) + "/day/" + date
}

func (s *SessionStore) GetUsageScopeDay(account, kind, project, id, date string) (UsageScopeTotal, bool, error) {
	total := UsageScopeTotal{Kind: kind, ProjectID: project, ID: id, Coverage: "no_records"}
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil || parsed.Format("2006-01-02") != date || account == "" || id == "" {
		return total, false, errors.New("invalid usage day scope")
	}
	found, err := s.store.GetJSON(usageScopeDayKey(account, total, date), &total)
	return total, found, err
}

func (s *SessionStore) setUsageDays(batch *pebble.Batch, current, previous SessionTurnUsageSnapshot) error {
	date := time.UnixMilli(current.CreatedAt).UTC().Format("2006-01-02")
	for _, scope := range current.ScopeTotals {
		total, _, err := s.GetUsageScopeDay(current.AccountScopeID, scope.Kind, scope.ProjectID, scope.ID, date)
		if err != nil {
			return err
		}
		if previous.ScopeTotals != nil && previous.ScopeProjectionVersion >= 2 {
			applyScopeReceipt(&total, previous, -1)
		}
		applyScopeReceipt(&total, current, 1)
		if total.Coverage != "repaired_receipts_incomplete" {
			total.Coverage = scope.Coverage
			if total.Coverage != "repaired_receipts_incomplete" {
				total.Coverage = "observed_receipts_only"
			}
		}
		total.Revision++
		payload, err := json.Marshal(total)
		if err != nil {
			return err
		}
		if err := batch.Set([]byte(usageScopeDayKey(current.AccountScopeID, total, date)), payload, nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *SessionStore) setMediaUsageDays(batch *pebble.Batch, media SessionMediaUsageRecord) error {
	date := time.UnixMilli(media.CreatedAt).UTC().Format("2006-01-02")
	for _, scope := range media.ScopeTotals {
		total, _, err := s.GetUsageScopeDay(media.AccountScopeID, scope.Kind, scope.ProjectID, scope.ID, date)
		if err != nil {
			return err
		}
		total.MediaCostUSD += media.CostUSD
		total.MediaReceipts++
		total.ReceiptCount++
		switch media.PriceStatus {
		case "known":
			total.CatalogCostUSD += media.CostUSD
		case "free":
			total.FreeReceipts++
		case "subscription":
			total.SubscriptionReceipts++
		default:
			total.UnknownReceipts++
		}
		if total.Coverage != "repaired_receipts_incomplete" {
			total.Coverage = scope.Coverage
			if total.Coverage != "repaired_receipts_incomplete" {
				total.Coverage = "observed_receipts_only"
			}
		}
		total.Revision++
		payload, err := json.Marshal(total)
		if err != nil {
			return err
		}
		if err := batch.Set([]byte(usageScopeDayKey(media.AccountScopeID, total, date)), payload, nil); err != nil {
			return err
		}
	}
	return nil
}
