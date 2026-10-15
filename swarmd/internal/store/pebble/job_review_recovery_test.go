package pebblestore

import "testing"

// Purpose: ReviewRecoveryPage must recover OK evidence after the process dies
// before notification. Closing/reopening real Pebble proves that review writes,
// not observer delivery or RecordedJobs replay, own recovery; account filtering
// and BLOCK prevent unrelated or rejected work from becoming recovery candidates.
func TestJobReviewRecoveryReopen(t *testing.T) {
	path := t.TempDir()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ws := NewWorkerStore(s)
	coder, err := ws.CreateWorker("account", "user", CreateWorkerRequest{Name: "coder"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := ws.CreateWorker("account", "user", CreateWorkerRequest{Name: "reviewer", Role: WorkerRoleReviewer}, nil)
	if err != nil {
		t.Fatal(err)
	}
	target, err := ws.RecordWorkerRun("account", WorkerRunRecord{WorkerID: coder.ID, UserID: "user", WorkspaceID: "workspace", Status: "succeeded", RequestSource: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := ws.RecordWorkerRun("account", WorkerRunRecord{WorkerID: reviewer.ID, Role: WorkerRoleReviewer, UserID: "user", WorkspaceID: "workspace", Status: "succeeded", RequestSource: "direct", Input: map[string]any{"review_job": "worker/" + coder.ID + "/" + target.ID}})
	if err != nil {
		t.Fatal(err)
	}
	review := JobReview{Verdict: "ok", Head: "head", ReviewerJobID: "worker/" + reviewer.ID + "/" + run.ID, RecordedAt: 1}
	// No observer: model the crash window between persistence and notification.
	if err := ws.RecordJobReview("account", coder.ID, target.ID, review); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ws = NewWorkerStore(s)
	if err := ws.RecordJobReview("account", coder.ID, target.ID, review); err != nil {
		t.Fatal(err)
	}
	signals, next, err := ws.ReviewRecoveryPage("account", "")
	if err != nil || next != "" || len(signals) != 1 || signals[0].Refs["run_id"] != target.ID {
		t.Fatalf("lost review: %+v %s %v", signals, next, err)
	}
	foreign, _, err := ws.ReviewRecoveryPage("foreign", "")
	if err != nil || len(foreign) != 0 {
		t.Fatal("cross-account recovery", foreign, err)
	}
	page, err := NewSignalStore(s).ListAfter(0, 10, SignalFilter{})
	if err != nil || len(page.Signals) != 0 {
		t.Fatal("test unexpectedly emitted signal", err)
	}
}
