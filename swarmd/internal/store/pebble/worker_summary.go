package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// WorkerActiveRun identifies a nonterminal durable receipt for hub navigation.
type WorkerActiveRun struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id,omitempty"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
}

// WorkerRunSummary counts durable receipts, not a page of run history. Daily
// counts use admission (created_at), while active is across the scanned history.
// When truncated, all counts are partial and must not be displayed as totals.
type WorkerRunSummary struct {
	Active          []WorkerActiveRun `json:"active"`
	ActiveTruncated bool              `json:"active_truncated"`
	Date            string            `json:"date"`
	Timezone        string            `json:"timezone"`
	DayStartAt      int64             `json:"day_start_at"`
	DayEndAt        int64             `json:"day_end_at"`
	DailyRuns       int               `json:"daily_runs"`
	DailySuccess    int               `json:"daily_success"`
	DailyFailed     int               `json:"daily_failed"`
	DailyCanceled   int               `json:"daily_cancelled"`
	ActiveRuns      int               `json:"active_runs"`
	ScannedRuns     int               `json:"scanned_runs"`
	Truncated       bool              `json:"truncated"`
}

// SummarizeWorkerRuns reads account/worker-scoped receipt keys in one bounded
// traversal. Unlike ListWorkerRuns it is independent of history page size and
// cursor order; it makes no writes and fails closed on corrupt index rows.
func (ws *WorkerStore) SummarizeWorkerRuns(account, workerID string, day time.Time, zone string) (WorkerRunSummary, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerRunSummary{}, errors.New("store is not open")
	}
	if strings.TrimSpace(account) == "" || !workerIDRegexp.MatchString(workerID) || zone == "" {
		return WorkerRunSummary{}, errors.New("account, worker and timezone required")
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return WorkerRunSummary{}, err
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	end := start.AddDate(0, 0, 1)
	result := WorkerRunSummary{Date: start.Format("2006-01-02"), Timezone: zone, DayStartAt: start.UnixMilli(), DayEndAt: end.UnixMilli(), Active: []WorkerActiveRun{}}
	prefix := WorkerRunPrefix(account, workerID)
	it, err := ws.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return WorkerRunSummary{}, err
	}
	defer it.Close()
	bytesRead := 0
	for valid := it.First(); valid; valid = it.Next() {
		if result.ScannedRuns >= 10000 || bytesRead+len(it.Value()) > 32*1024*1024 {
			result.Truncated = true
			break
		}
		bytesRead += len(it.Value())
		var r WorkerRunRecord
		if err := json.Unmarshal(it.Value(), &r); err != nil {
			return WorkerRunSummary{}, fmt.Errorf("corrupt worker run record: %w", err)
		}
		if r.AccountScopeID != account || r.WorkerID != workerID {
			return WorkerRunSummary{}, ErrWorkerConflict
		}
		result.ScannedRuns++
		if r.Status == "admitted" || r.Status == "running" {
			result.ActiveRuns++
			if len(result.Active) < 100 {
				result.Active = append(result.Active, WorkerActiveRun{ID: r.ID, SessionID: r.SessionID, Status: r.Status, CreatedAt: r.CreatedAt})
			} else {
				result.ActiveTruncated = true
			}
		}
		if r.CreatedAt >= result.DayStartAt && r.CreatedAt < result.DayEndAt {
			result.DailyRuns++
			switch r.Status {
			case "succeeded":
				result.DailySuccess++
			case "failed":
				result.DailyFailed++
			case "cancelled":
				result.DailyCanceled++
			}
		}
	}
	if err := it.Error(); err != nil {
		return WorkerRunSummary{}, err
	}
	return result, nil
}
