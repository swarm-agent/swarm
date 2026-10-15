package pebblestore

import (
	"errors"
	"path/filepath"
	"testing"
)

// Purpose: SetLimits CAS and checkJobLimits must read live durable policy, reject
// unsafe edits without mutation, and signal refusals. Real Pebble is the narrowest
// layer proving persisted revisions and receipt-index enforcement, not providers.
func TestLimitsLiveRevisionCeilingSignal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "limits")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessionStore(db)
	signals := NewSignalStore(db)
	db.SetLimitSignalStore(signals)
	l, err := s.GetLimits("a")
	if err != nil || l != DefaultLimits() {
		t.Fatalf("defaults %+v %v", l, err)
	}
	scope := UsageScopeTotal{Kind: "worker_run", ProjectID: "worker", ID: "job", TotalTokens: 10}
	if err := db.PutJSON(usageScopeKey("a", scope), scope); err != nil {
		t.Fatal(err)
	}
	if err := s.checkJobLimits("a", []UsageScopeTotal{scope}); err != nil {
		t.Fatal(err)
	}
	l.JobTokens = 10
	l, err = s.SetLimits("a", 0, l)
	if err != nil {
		t.Fatal(err)
	}
	var reached *LimitReached
	if err := s.checkJobLimits("a", []UsageScopeTotal{scope}); !errors.As(err, &reached) || reached.Limit != "job_tokens" {
		t.Fatalf("refusal %v", err)
	}
	page, err := signals.ListAfter(0, 10, SignalFilter{Account: "a"})
	if err != nil || len(page.Signals) != 1 || page.Signals[0].Kind != "limit.reached" || page.Signals[0].Attrs["value"] != "10" {
		t.Fatalf("signal %+v %v", page, err)
	}
	if err := s.checkJobLimits("b", []UsageScopeTotal{scope}); err != nil {
		t.Fatalf("cross account %v", err)
	}
	if _, err = s.SetLimits("a", 0, l); !errors.Is(err, ErrLimitsConflict) {
		t.Fatalf("stale %v", err)
	}
	bad := l
	bad.ActiveChildren = 257
	if _, err = s.SetLimits("a", l.Revision, bad); err == nil {
		t.Fatal("ceiling accepted")
	}
	got, _ := s.GetLimits("a")
	if got != l {
		t.Fatal("failed update mutated state")
	}
	l.JobTokens = 11
	l, err = s.SetLimits("a", l.Revision, l)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.checkJobLimits("a", []UsageScopeTotal{scope}); err != nil {
		t.Fatalf("live raise %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err = db.GetLimits("a")
	if err != nil || got != l {
		t.Fatalf("restart %+v %v", got, err)
	}
}

// Purpose: AdmitWorkerRun must apply edits without restart, reject excess work
// without creating a receipt, and preserve idempotent replay at capacity.
func TestLimitsWorkerAdmission(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws := NewWorkerStore(db)
	signals := NewSignalStore(db)
	db.SetLimitSignalStore(signals)
	w, err := ws.CreateWorker("a", "owner", CreateWorkerRequest{Name: "review", Instructions: "Review", WorkspaceRequirements: []WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.ActivateWorker("a", "owner", w.ID, w.Revision, map[string]string{"primary": "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	l := DefaultLimits()
	l.WorkerJobConcurrency = 1
	l, err = db.SetLimits("a", 0, l)
	if err != nil {
		t.Fatal(err)
	}
	req := WorkerRunAdmission{WorkerID: w.ID, UserID: "owner", RequestSource: "direct", IdempotencyKey: "one", Input: map[string]any{"prompt": "review"}}
	first, err := ws.AdmitWorkerRun("a", req)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := ws.AdmitWorkerRun("a", req)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay %v", err)
	}
	req.IdempotencyKey = "two"
	var reached *LimitReached
	if _, err = ws.AdmitWorkerRun("a", req); !errors.As(err, &reached) {
		t.Fatalf("over cap %v", err)
	}
	active, err := ws.UnfinishedWorkerRuns("a", w.ID, "")
	if err != nil || len(active) != 1 {
		t.Fatalf("partial admission %+v %v", active, err)
	}
	page, err := signals.ListAfter(0, 10, SignalFilter{Account: "a"})
	if err != nil || len(page.Signals) != 1 || page.Signals[0].Attrs["limit"] != "worker_job_concurrency" {
		t.Fatalf("signal %+v %v", page, err)
	}
	l.WorkerJobConcurrency = 2
	if _, err = db.SetLimits("a", l.Revision, l); err != nil {
		t.Fatal(err)
	}
	if _, err = ws.AdmitWorkerRun("a", req); err != nil {
		t.Fatalf("live raise %v", err)
	}
}
