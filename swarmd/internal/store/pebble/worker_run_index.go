package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble"
)

// Runs are indexed by admission time and by being unfinished, so a machine can
// answer "what was admitted between T1 and T2" and "what is running now" by
// reading only those runs, never every run it ever had. Every worker write
// commits through setWorkerRealtimeMutationInBatch, which keeps both indexes in
// the same batch as the run record; runs written before the index existed are
// backfilled once at startup (RunWorkerRunIndexMigration). Reads check every
// index entry against its run record, so an entry can never make a count wrong.

const (
	keyWorkerRunTimePrefix   = "worker/runidx/time/"
	keyWorkerRunActivePrefix = "worker/runidx/active/"
	keyWorkerRunIndexVersion = "worker/runidx/version"
	// Version 3 adds capacity reservations to the version 2 session index.
	workerRunIndexVersion = 3
)

type workerRunIndexEntry struct {
	WorkerID  string `json:"w"`
	RunID     string `json:"r"`
	CreatedAt int64  `json:"t"`
}

func keyWorkerRunTime(account string, createdAt int64, workerID, runID string) string {
	if createdAt < 0 {
		createdAt = 0
	}
	return fmt.Sprintf("%s%s/%020d/%s/%s", keyWorkerRunTimePrefix, keyPart(account), createdAt, keyPart(workerID), keyPart(runID))
}

func keyWorkerRunTimeAccountPrefix(account string) string {
	return keyWorkerRunTimePrefix + keyPart(account) + "/"
}

func keyWorkerRunActive(account, workerID, runID string) string {
	return fmt.Sprintf("%s%s/%s/%s", keyWorkerRunActivePrefix, keyPart(account), keyPart(workerID), keyPart(runID))
}

// indexWorkerRunInBatch adds the index entries of one run record being written.
// A run's admission time never changes, so its time entry is written once and
// rewritten identically; its active entry follows its status.
func indexWorkerRunInBatch(batch *pebble.Batch, account string, data []byte) error {
	var run struct {
		SessionID      string `json:"session_id"`
		ID             string `json:"id"`
		WorkerID       string `json:"worker_id"`
		Status         string `json:"status"`
		CreatedAt      int64  `json:"created_at"`
		FollowupStatus string `json:"followup_status"`
	}
	if err := json.Unmarshal(data, &run); err != nil {
		return fmt.Errorf("index worker run: %w", err)
	}
	if strings.TrimSpace(run.ID) == "" || strings.TrimSpace(run.WorkerID) == "" {
		return fmt.Errorf("%w: worker run without id or worker", ErrWorkerInvalid)
	}
	entry, err := json.Marshal(workerRunIndexEntry{WorkerID: run.WorkerID, RunID: run.ID, CreatedAt: run.CreatedAt})
	if err != nil {
		return err
	}
	if err := batch.Set([]byte(keyWorkerRunTime(account, run.CreatedAt, run.WorkerID, run.ID)), entry, nil); err != nil {
		return err
	}
	if run.SessionID != "" {
		if err := batch.Set([]byte(keyWorkerRunSession(account, run.SessionID)), entry, nil); err != nil {
			return err
		}
	}
	reservation := keyWorkerReservation(account, run.WorkerID, run.ID)
	if run.Status == "preparing" || run.Status == "running" || run.FollowupStatus == "running" {
		if err := batch.Set([]byte(reservation), entry, nil); err != nil {
			return err
		}
	} else if err := batch.Delete([]byte(reservation), nil); err != nil {
		return err
	}
	active := keyWorkerRunActive(account, run.WorkerID, run.ID)
	if AutomationV2Terminal(run.Status) && run.FollowupStatus != "running" && run.FollowupStatus != "queued" {
		return batch.Delete([]byte(active), nil)
	}
	return batch.Set([]byte(active), entry, nil)
}

// isWorkerRunRecordKey reports whether key is a run record of account
// (worker/run/<account>/<worker>/<run>), not another worker key.
func isWorkerRunRecordKey(account, key string) bool {
	rest, ok := strings.CutPrefix(key, KeyWorkerRunAccountPrefix+keyPart(account)+"/")
	return ok && strings.Count(rest, "/") == 1
}

// RunWorkerRunIndexMigration indexes the runs written before the index existed.
// It is idempotent: entries are derived from the records and rewritten as is.
func RunWorkerRunIndexMigration(store *Store) (int, error) {
	if store == nil || store.db == nil {
		return 0, errors.New("store is not configured")
	}
	var marker struct {
		Version int `json:"version"`
	}
	if found, err := store.GetJSON(keyWorkerRunIndexVersion, &marker); err != nil {
		return 0, err
	} else if found && marker.Version >= workerRunIndexVersion {
		return 0, nil
	}
	const iterateAll = int(^uint(0) >> 1)
	indexed := 0
	batch := store.NewBatch()
	flush := func() error {
		if batch.Count() == 0 {
			return nil
		}
		err := batch.Commit(pebble.Sync)
		_ = batch.Close()
		batch = store.NewBatch()
		return err
	}
	err := store.IteratePrefix(KeyWorkerRunAccountPrefix, iterateAll, func(key string, value []byte) error {
		var run struct {
			AccountScopeID string `json:"account_scope_id"`
		}
		if json.Unmarshal(value, &run) != nil || !isWorkerRunRecordKey(run.AccountScopeID, key) {
			return nil
		}
		if err := indexWorkerRunInBatch(batch, run.AccountScopeID, value); err != nil {
			return err
		}
		indexed++
		if batch.Count() >= 2000 {
			return flush()
		}
		return nil
	})
	if err == nil {
		err = flush()
	}
	_ = batch.Close()
	if err != nil {
		return indexed, fmt.Errorf("index worker runs: %w", err)
	}
	return indexed, store.PutJSON(keyWorkerRunIndexVersion, map[string]int{"version": workerRunIndexVersion})
}

// WorkerRunTimeQuery selects runs by admission time, newest first.
type WorkerRunTimeQuery struct {
	SinceMs  int64    // inclusive; 0 is the beginning
	UntilMs  int64    // exclusive; 0 is now and later
	Statuses []string // empty is every status
	WorkerID string   // empty is every worker
	Limit    int      // 1..500
	Cursor   string   // next_cursor of the previous page
}

// ListWorkerRunsByTime lists one account's runs across all workers, newest
// admission first, one consistent snapshot per page.
func (ws *WorkerStore) ListWorkerRunsByTime(account string, q WorkerRunTimeQuery) ([]WorkerRunRecord, string, error) {
	if ws == nil || ws.store == nil || strings.TrimSpace(account) == "" {
		return nil, "", ErrWorkerInvalid
	}
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	prefix := keyWorkerRunTimeAccountPrefix(account)
	lower := prefix + fmt.Sprintf("%020d", max(q.SinceMs, 0))
	upper := prefix + "~"
	if q.UntilMs > 0 {
		upper = prefix + fmt.Sprintf("%020d", q.UntilMs)
	}
	if q.Cursor != "" {
		if !strings.HasPrefix(q.Cursor, prefix) || q.Cursor < lower || q.Cursor >= upper {
			return nil, "", fmt.Errorf("%w: cursor does not belong to this query", ErrWorkerInvalid)
		}
		upper = q.Cursor
	}
	want := map[string]bool{}
	for _, status := range q.Statuses {
		want[strings.TrimSpace(status)] = true
	}
	snap := ws.store.db.NewSnapshot()
	defer snap.Close()
	iter, err := snap.NewIter(&pebble.IterOptions{LowerBound: []byte(lower), UpperBound: []byte(upper)})
	if err != nil {
		return nil, "", err
	}
	defer iter.Close()
	runs := []WorkerRunRecord{}
	next := ""
	for valid := iter.Last(); valid; valid = iter.Prev() {
		run, ok, err := workerRunFromTimeEntry(snap, account, string(iter.Key()), iter.Value())
		if err != nil {
			return nil, "", err
		}
		if !ok || (q.WorkerID != "" && run.WorkerID != q.WorkerID) || (len(want) > 0 && !want[run.Status]) {
			continue
		}
		if len(runs) == limit {
			next = keyAfterLast(runs, account)
			break
		}
		runs = append(runs, run)
	}
	if err := iter.Error(); err != nil {
		return nil, "", err
	}
	return runs, next, nil
}

func keyAfterLast(runs []WorkerRunRecord, account string) string {
	last := runs[len(runs)-1]
	return keyWorkerRunTime(account, last.CreatedAt, last.WorkerID, last.ID)
}

// workerRunFromTimeEntry reads the run a time entry names and keeps it only if
// the record still says that admission time.
func workerRunFromTimeEntry(reader pebble.Reader, account, key string, value []byte) (WorkerRunRecord, bool, error) {
	var entry workerRunIndexEntry
	if err := json.Unmarshal(value, &entry); err != nil {
		return WorkerRunRecord{}, false, nil
	}
	var run WorkerRunRecord
	found, err := getJSONFromReader(reader, KeyWorkerRun(account, entry.WorkerID, entry.RunID), &run)
	if err != nil || !found {
		return WorkerRunRecord{}, false, err
	}
	if keyWorkerRunTime(account, run.CreatedAt, run.WorkerID, run.ID) != key {
		return WorkerRunRecord{}, false, nil
	}
	return run, true, nil
}

// WorkerRunCounts are exact counts of the runs admitted in a time window, and
// of everything unfinished now, from one consistent snapshot.
type WorkerRunCounts struct {
	SinceMs     int64            `json:"since_ms"`
	UntilMs     int64            `json:"until_ms"`
	Total       int              `json:"total"`
	ByStatus    map[string]int   `json:"by_status"`
	BySource    map[string]int   `json:"by_source"`
	Failed      []WorkerRunBrief `json:"failed"`
	Running     int              `json:"running"`
	RunningRuns []WorkerRunBrief `json:"running_runs"`
	// ByWorker tallies the window's runs and the unfinished runs per worker.
	ByWorker map[string]WorkerRunTally `json:"by_worker"`
}

// WorkerRunTally is one worker's share of WorkerRunCounts.
type WorkerRunTally struct {
	Runs    int `json:"runs"`
	Failed  int `json:"failed"`
	Running int `json:"running"`
}

// WorkerRunBrief names one run for an overview.
type WorkerRunBrief struct {
	WorkerID      string `json:"worker_id"`
	WorkerName    string `json:"worker_name,omitempty"`
	RunID         string `json:"run_id"`
	AutomationID  string `json:"automation_id,omitempty"`
	RequestSource string `json:"request_source"`
	Status        string `json:"status"`
	CreatedAt     int64  `json:"created_at"`
	CompletedAt   int64  `json:"completed_at,omitempty"`
}

const workerRunBriefLimit = 50

// A brief holds ids, names, status and times only: a run's input, output and
// error text stay on the machine (swarm_get_job reads them).
func briefWorkerRun(run WorkerRunRecord) WorkerRunBrief {
	return WorkerRunBrief{WorkerID: run.WorkerID, RunID: run.ID, AutomationID: run.AutomationID, RequestSource: run.RequestSource, Status: run.Status, CreatedAt: run.CreatedAt, CompletedAt: run.CompletedAt}
}

// CountWorkerRuns counts the runs admitted in [sinceMs, untilMs) by status and
// source, lists the failed ones (newest first, at most 50 listed, all counted),
// and counts every unfinished run whenever it was admitted.
func (ws *WorkerStore) CountWorkerRuns(account string, sinceMs, untilMs int64) (WorkerRunCounts, error) {
	if ws == nil || ws.store == nil || strings.TrimSpace(account) == "" || (untilMs > 0 && untilMs <= sinceMs) {
		return WorkerRunCounts{}, ErrWorkerInvalid
	}
	counts := WorkerRunCounts{SinceMs: sinceMs, UntilMs: untilMs, ByStatus: map[string]int{}, BySource: map[string]int{}, Failed: []WorkerRunBrief{}, RunningRuns: []WorkerRunBrief{}, ByWorker: map[string]WorkerRunTally{}}
	snap := ws.store.db.NewSnapshot()
	defer snap.Close()
	prefix := keyWorkerRunTimeAccountPrefix(account)
	upper := prefix + "~"
	if untilMs > 0 {
		upper = prefix + fmt.Sprintf("%020d", untilMs)
	}
	iter, err := snap.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix + fmt.Sprintf("%020d", max(sinceMs, 0))), UpperBound: []byte(upper)})
	if err != nil {
		return counts, err
	}
	for valid := iter.Last(); valid; valid = iter.Prev() {
		run, ok, err := workerRunFromTimeEntry(snap, account, string(iter.Key()), iter.Value())
		if err != nil {
			iter.Close()
			return counts, err
		}
		if !ok {
			continue
		}
		counts.Total++
		counts.ByStatus[run.Status]++
		counts.BySource[run.RequestSource]++
		tally := counts.ByWorker[run.WorkerID]
		tally.Runs++
		if run.Status == "failed" {
			tally.Failed++
		}
		counts.ByWorker[run.WorkerID] = tally
		if run.Status == "failed" && len(counts.Failed) < workerRunBriefLimit {
			counts.Failed = append(counts.Failed, briefWorkerRun(run))
		}
	}
	err = iter.Error()
	iter.Close()
	if err != nil {
		return counts, err
	}
	activePrefix := keyWorkerRunActivePrefix + keyPart(account) + "/"
	active := []WorkerRunRecord{}
	err = iteratePrefixFromReader(snap, activePrefix, int(^uint(0)>>1), func(_ string, value []byte) error {
		var entry workerRunIndexEntry
		if json.Unmarshal(value, &entry) != nil {
			return nil
		}
		var run WorkerRunRecord
		found, err := getJSONFromReader(snap, KeyWorkerRun(account, entry.WorkerID, entry.RunID), &run)
		if err != nil {
			return err
		}
		if found && !AutomationV2Terminal(run.Status) {
			active = append(active, run)
			tally := counts.ByWorker[run.WorkerID]
			tally.Running++
			counts.ByWorker[run.WorkerID] = tally
		}
		return nil
	})
	if err != nil {
		return counts, err
	}
	sort.Slice(active, func(i, j int) bool { return active[i].CreatedAt > active[j].CreatedAt })
	counts.Running = len(active)
	for _, run := range active {
		if len(counts.RunningRuns) == workerRunBriefLimit {
			break
		}
		counts.RunningRuns = append(counts.RunningRuns, briefWorkerRun(run))
	}
	return counts, nil
}

// CountUnfinishedWorkerRuns counts every account's unfinished runs on this
// machine, each checked against its run record. A restart of the daemon
// interrupts them, so an update waits until this is zero.
func (ws *WorkerStore) CountUnfinishedWorkerRuns() (int, error) {
	if ws == nil || ws.store == nil {
		return 0, ErrWorkerInvalid
	}
	snap := ws.store.db.NewSnapshot()
	defer snap.Close()
	count := 0
	err := iteratePrefixFromReader(snap, keyWorkerRunActivePrefix, int(^uint(0)>>1), func(key string, value []byte) error {
		var entry workerRunIndexEntry
		account, _, ok := strings.Cut(strings.TrimPrefix(key, keyWorkerRunActivePrefix), "/")
		if !ok || json.Unmarshal(value, &entry) != nil {
			return nil
		}
		// The key holds the account already escaped, as the record key does.
		var run WorkerRunRecord
		found, err := getJSONFromReader(snap, KeyWorkerRunAccountPrefix+account+"/"+keyPart(entry.WorkerID)+"/"+keyPart(entry.RunID), &run)
		if err != nil {
			return err
		}
		if found && !AutomationV2Terminal(run.Status) {
			count++
		}
		return nil
	})
	return count, err
}
