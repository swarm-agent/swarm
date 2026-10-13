package pebblestore

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// groundTruth counts runs by reading every run record of every worker, the
// slow way the index replaces.
func groundTruth(t *testing.T, ws *WorkerStore, workers []string, since, until int64) (total int, byStatus, bySource map[string]int, failed, running []string, window []string) {
	t.Helper()
	byStatus, bySource = map[string]int{}, map[string]int{}
	type stamped struct {
		id string
		at int64
	}
	var inWindow, failedRuns []stamped
	for _, worker := range workers {
		after := ""
		for {
			runs, next, err := ws.ListWorkerRuns("account", worker, 100, after)
			if err != nil {
				t.Fatal(err)
			}
			for _, run := range runs {
				if !AutomationV2Terminal(run.Status) {
					running = append(running, run.ID)
				}
				if run.CreatedAt < since || (until > 0 && run.CreatedAt >= until) {
					continue
				}
				total++
				byStatus[run.Status]++
				bySource[run.RequestSource]++
				inWindow = append(inWindow, stamped{run.ID, run.CreatedAt})
				if run.Status == "failed" {
					failedRuns = append(failedRuns, stamped{run.ID, run.CreatedAt})
				}
			}
			if next == "" {
				break
			}
			after = next
		}
	}
	newest := func(list []stamped) []string {
		sort.SliceStable(list, func(i, j int) bool { return list[i].at > list[j].at })
		ids := []string{}
		for _, s := range list {
			ids = append(ids, s.id)
		}
		return ids
	}
	sort.Strings(running)
	return total, byStatus, bySource, newest(failedRuns), running, newest(inWindow)
}

func briefIDs(list []WorkerRunBrief) []string {
	ids := []string{}
	for _, b := range list {
		ids = append(ids, b.RunID)
	}
	return ids
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Requirement: a machine's run counts for a time window, its unfinished runs and
// its newest-first run list are exactly what its run records say, across
// workers, through status changes and windows, without reading every run.
func TestWorkerRunIndexMatchesTheRecordsExactly(t *testing.T) {
	_, ws := openTestStore(t)
	rng := rand.New(rand.NewSource(7))
	workers := []string{}
	for i := 0; i < 4; i++ {
		w, err := ws.CreateWorker("account", "user", CreateWorkerRequest{Name: fmt.Sprintf("Worker %d", i), Instructions: "Do the assigned task"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		workers = append(workers, w.ID)
	}
	statuses := []string{"admitted", "running", "succeeded", "failed", "cancelled"}
	sources := []string{"direct", "schedule", "trigger"}
	day := int64(86_400_000)
	base := int64(1_790_000_000_000)
	for i := 0; i < 300; i++ {
		// Distinct admission times over three days, so window edges are exact.
		at := base + int64(i)*day*3/300 + 1
		run, err := ws.RecordWorkerRun("account", WorkerRunRecord{WorkerID: workers[rng.Intn(len(workers))], Status: "admitted", RequestSource: sources[rng.Intn(len(sources))], CreatedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		// Move most runs on: running, then a final outcome, like real runs.
		switch next := statuses[rng.Intn(len(statuses))]; next {
		case "admitted":
		case "running":
			run.Status = "running"
			if run, err = ws.RecordWorkerRun("account", run); err != nil {
				t.Fatal(err)
			}
		default:
			run.Status = "running"
			if run, err = ws.RecordWorkerRun("account", run); err != nil {
				t.Fatal(err)
			}
			run.Status, run.Error = next, ""
			if next == "failed" {
				run.Error = "provider refused"
			}
			if _, err = ws.RecordWorkerRun("account", run); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Another account's runs never count.
	other, err := ws.CreateWorker("other", "user", CreateWorkerRequest{Name: "Other", Instructions: "Do the assigned task"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.RecordWorkerRun("other", WorkerRunRecord{WorkerID: other.ID, Status: "failed", RequestSource: "direct", CreatedAt: base + day}); err != nil {
		t.Fatal(err)
	}

	windows := [][2]int64{{0, 0}, {base + day, base + 2*day}, {base + 2*day, 0}, {base + day + 12345, base + day + 9_000_000}, {base + 5*day, 0}}
	for _, win := range windows {
		counts, err := ws.CountWorkerRuns("account", win[0], win[1])
		if err != nil {
			t.Fatal(err)
		}
		total, byStatus, bySource, failed, running, window := groundTruth(t, ws, workers, win[0], win[1])
		if counts.Total != total || fmt.Sprint(counts.ByStatus) != fmt.Sprint(byStatus) || fmt.Sprint(counts.BySource) != fmt.Sprint(bySource) {
			t.Fatalf("window %v: index says %d %v %v, records say %d %v %v", win, counts.Total, counts.ByStatus, counts.BySource, total, byStatus, bySource)
		}
		if len(failed) > workerRunBriefLimit {
			failed = failed[:workerRunBriefLimit]
		}
		if !sameStrings(briefIDs(counts.Failed), failed) {
			t.Fatalf("window %v: failed runs differ", win)
		}
		got := briefIDs(counts.RunningRuns)
		sort.Strings(got)
		if counts.Running != len(running) || (len(running) <= workerRunBriefLimit && !sameStrings(got, running)) {
			t.Fatalf("window %v: running %d %v, records say %d", win, counts.Running, got, len(running))
		}
		// Paging newest first returns every run in the window exactly once.
		listed, cursor := []string{}, ""
		for {
			page, next, err := ws.ListWorkerRunsByTime("account", WorkerRunTimeQuery{SinceMs: win[0], UntilMs: win[1], Limit: 7, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			for _, run := range page {
				listed = append(listed, run.ID)
			}
			if next == "" {
				break
			}
			cursor = next
		}
		if !sameStrings(listed, window) {
			t.Fatalf("window %v: listed %d runs, records have %d (or out of order)", win, len(listed), len(window))
		}
	}
	// Status and worker filters.
	page, _, err := ws.ListWorkerRunsByTime("account", WorkerRunTimeQuery{Statuses: []string{"failed"}, WorkerID: workers[0], Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range page {
		if run.Status != "failed" || run.WorkerID != workers[0] {
			t.Fatalf("filter let through %s/%s", run.WorkerID, run.Status)
		}
	}
}

// Runs written before the index existed are counted after the startup
// migration, and the migration is idempotent.
func TestWorkerRunIndexMigrationBackfills(t *testing.T) {
	s, ws := openTestStore(t)
	w, err := ws.CreateWorker("account", "user", CreateWorkerRequest{Name: "Worker", Instructions: "Do the assigned task"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Records as an older daemon wrote them: no index entries.
	for i, status := range []string{"succeeded", "failed", "running", "admitted"} {
		run := WorkerRunRecord{ID: fmt.Sprintf("wrun_old%d", i), AccountScopeID: "account", WorkerID: w.ID, Status: status, RequestSource: "schedule", CreatedAt: int64(1000 + i)}
		if err := s.PutJSON(KeyWorkerRun("account", w.ID, run.ID), run); err != nil {
			t.Fatal(err)
		}
	}
	if counts, _ := ws.CountWorkerRuns("account", 0, 0); counts.Total != 0 {
		t.Fatalf("unindexed runs counted before the migration: %d", counts.Total)
	}
	if err := s.Delete(keyWorkerRunIndexVersion); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := RunWorkerRunIndexMigration(s); err != nil {
			t.Fatal(err)
		}
		_ = s.Delete(keyWorkerRunIndexVersion)
	}
	counts, err := ws.CountWorkerRuns("account", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Total != 4 || counts.ByStatus["failed"] != 1 || counts.Running != 2 || len(counts.Failed) != 1 || counts.Failed[0].RunID != "wrun_old1" {
		t.Fatalf("after migration: %+v", counts)
	}
}

// Per-worker tallies agree with each worker's own records.
func TestWorkerRunIndexTalliesPerWorker(t *testing.T) {
	_, ws := openTestStore(t)
	want := map[string]WorkerRunTally{}
	for i := 0; i < 3; i++ {
		w, err := ws.CreateWorker("account", "user", CreateWorkerRequest{Name: fmt.Sprintf("W%d", i), Instructions: "Do the assigned task"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		for j, status := range []string{"succeeded", "failed", "running", "failed", "admitted"}[:2+i] {
			run, err := ws.RecordWorkerRun("account", WorkerRunRecord{WorkerID: w.ID, Status: "running", RequestSource: "schedule", CreatedAt: int64(5000 + 10*i + j)})
			if err != nil {
				t.Fatal(err)
			}
			if status != "running" {
				run.Status = status
				if _, err := ws.RecordWorkerRun("account", run); err != nil && status != "admitted" {
					t.Fatal(err)
				} else if status == "admitted" {
					status = "running" // running cannot return to admitted
				}
			}
			tally := want[w.ID]
			tally.Runs++
			if status == "failed" {
				tally.Failed++
			}
			if status == "running" {
				tally.Running++
			}
			want[w.ID] = tally
		}
	}
	counts, err := ws.CountWorkerRuns("account", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(counts.ByWorker) != fmt.Sprint(want) {
		t.Fatalf("by worker = %v, want %v", counts.ByWorker, want)
	}
}

// The machine-wide unfinished count spans every account and follows status
// changes, so an update never restarts Swarm under a run of any account.
func TestCountUnfinishedWorkerRunsSpansAccounts(t *testing.T) {
	_, ws := openTestStore(t)
	runs := map[string]WorkerRunRecord{}
	for _, account := range []string{"account-a", "Account B"} {
		w, err := ws.CreateWorker(account, "user", CreateWorkerRequest{Name: "Worker", Instructions: "Do the assigned task"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		for i, status := range []string{"admitted", "running", "succeeded"} {
			run, err := ws.RecordWorkerRun(account, WorkerRunRecord{WorkerID: w.ID, Status: "running", RequestSource: "direct", CreatedAt: int64(1000 + i)})
			if err != nil {
				t.Fatal(err)
			}
			if status == "succeeded" {
				run.Status = status
				if run, err = ws.RecordWorkerRun(account, run); err != nil {
					t.Fatal(err)
				}
			}
			runs[account+fmt.Sprint(i)] = run
		}
	}
	if got, err := ws.CountUnfinishedWorkerRuns(); err != nil || got != 4 {
		t.Fatalf("unfinished = %d, %v; want 4", got, err)
	}
	for _, key := range []string{"account-a0", "Account B1"} {
		run := runs[key]
		run.Status = "cancelled"
		if _, err := ws.RecordWorkerRun(strings.TrimSuffix(strings.TrimSuffix(key, "0"), "1"), run); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := ws.CountUnfinishedWorkerRuns(); err != nil || got != 2 {
		t.Fatalf("unfinished after cancelling two = %d, %v; want 2", got, err)
	}
}
