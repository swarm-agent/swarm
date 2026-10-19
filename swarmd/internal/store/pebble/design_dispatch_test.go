package pebblestore

import (
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: manage_design was replaced by swarm_design and its queue is no
// longer dispatched, so the daemon's one-shot startup pass must end every
// still-pending request: queued candidates fail with DesignReplacedReason and
// an alert naming swarm_design, running attempts end interrupted and
// cancel-requested attempts end cancelled, across owners, and the pending
// index is empty afterwards. Threat: requests stay "queued" or "running"
// forever with no dispatcher, a terminal request is rewritten, or a second
// start mutates history again. Owners: Store.RetirePendingDesignRequests
// (called from runtime/daemon.go), FailQueuedDesign and RecordDesignAttempt.
// A temporary Pebble store is the narrowest layer that holds the durable
// request records, receipts and pending index the daemon pass changes.
func TestRetirePendingDesignRequestsEndsEveryPendingCandidate(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "design-retire.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	owner := DesignPrincipal{AccountID: "account-a", PrincipalID: "user-a"}
	other := DesignPrincipal{AccountID: "account-b", PrincipalID: "user-b"}
	spec := func(id string) DesignCandidateSpec {
		return DesignCandidateSpec{ArtifactID: id, Kind: DesignHTML, Operation: DesignGenerate, Brief: "a landing page"}
	}
	r, err := db.SubmitDesignRequest(owner, DesignSubmit{RequestID: "req-1", IdempotencyKey: "submit-1", ParentSessionID: "parent-1", ParentRunID: "run-1", Candidates: []DesignCandidateSpec{spec("art-1"), spec("art-2"), spec("art-3")}})
	if err != nil {
		t.Fatal(err)
	}
	// Candidate 1 is running; candidate 2 is running with a cancel request.
	if r, err = db.RecordDesignAttempt(owner, "req-1", DesignAttemptMutation{IdempotencyKey: "start-1", ExpectedRevision: r.Revision, Candidate: 1, State: DesignRunning, ChildSessionID: "child-1", RunID: "child-run-1"}); err != nil {
		t.Fatal(err)
	}
	if r, err = db.RecordDesignAttempt(owner, "req-1", DesignAttemptMutation{IdempotencyKey: "start-2", ExpectedRevision: r.Revision, Candidate: 2, State: DesignRunning, ChildSessionID: "child-2", RunID: "child-run-2"}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.RecordDesignAttempt(owner, "req-1", DesignAttemptMutation{IdempotencyKey: "cancel-2", ExpectedRevision: r.Revision, Candidate: 2, State: DesignCancelRequested, ChildSessionID: "child-2", RunID: "child-run-2"}); err != nil {
		t.Fatal(err)
	}
	// Another owner's queued request is retired too.
	if _, err = db.SubmitDesignRequest(other, DesignSubmit{RequestID: "req-2", IdempotencyKey: "submit-2", ParentSessionID: "parent-2", ParentRunID: "run-2", Candidates: []DesignCandidateSpec{spec("art-4")}}); err != nil {
		t.Fatal(err)
	}
	// A request that already ended must not change.
	done, err := db.SubmitDesignRequest(owner, DesignSubmit{RequestID: "req-3", IdempotencyKey: "submit-3", ParentSessionID: "parent-1", ParentRunID: "run-1", Candidates: []DesignCandidateSpec{spec("art-5")}})
	if err != nil {
		t.Fatal(err)
	}
	if done, err = db.RecordDesignAttempt(owner, "req-3", DesignAttemptMutation{IdempotencyKey: "cancel-3", ExpectedRevision: done.Revision, Candidate: 0, State: DesignCancelRequested}); err != nil {
		t.Fatal(err)
	}

	ended, err := db.RetirePendingDesignRequests()
	if err != nil {
		t.Fatal(err)
	}
	if ended != 4 {
		t.Fatalf("ended %d candidates, want 4", ended)
	}
	got, err := db.GetDesignRequest(owner, "req-1")
	if err != nil {
		t.Fatal(err)
	}
	queued := got.Candidates[0]
	if queued.State != DesignFailed || queued.FailureReason != DesignReplacedReason || !strings.Contains(queued.RouterAlert, "swarm_design") || len(queued.Attempts) != 0 {
		t.Fatalf("queued candidate not failed as replaced: %+v", queued)
	}
	running := got.Candidates[1]
	if running.State != DesignInterrupted || len(running.Attempts) != 1 || running.Attempts[0].State != DesignInterrupted || running.Attempts[0].ReasonCode != DesignReplacedReason || running.Attempts[0].Result != nil || running.Attempts[0].Output != nil {
		t.Fatalf("running candidate not interrupted as replaced: %+v", running)
	}
	cancelled := got.Candidates[2]
	if cancelled.State != DesignCancelled || cancelled.Attempts[0].State != DesignCancelled || cancelled.Attempts[0].ReasonCode != DesignReplacedReason {
		t.Fatalf("cancel-requested candidate not cancelled: %+v", cancelled)
	}
	if got.State == DesignQueued || got.State == DesignRunning || got.State == DesignCancelRequested {
		t.Fatalf("request still pending: %s", got.State)
	}
	if otherGot, err := db.GetDesignRequest(other, "req-2"); err != nil || otherGot.Candidates[0].State != DesignFailed || otherGot.Candidates[0].FailureReason != DesignReplacedReason {
		t.Fatalf("other owner's request not retired: %+v %v", otherGot, err)
	}
	if after, err := db.GetDesignRequest(owner, "req-3"); err != nil || after.Revision != done.Revision {
		t.Fatalf("an ended request was rewritten: revision %d -> %d (%v)", done.Revision, after.Revision, err)
	}
	if rows, _, err := db.ScanDesignPending(""); err != nil || len(rows) != 0 {
		t.Fatalf("pending index not empty: %d rows, err %v", len(rows), err)
	}
	// A second daemon start changes nothing.
	before := got.Revision
	if again, err := db.RetirePendingDesignRequests(); err != nil || again != 0 {
		t.Fatalf("second pass ended %d (err %v), want 0", again, err)
	}
	if after, _ := db.GetDesignRequest(owner, "req-1"); after.Revision != before {
		t.Fatalf("second pass rewrote the request: %d -> %d", before, after.Revision)
	}
	// The retirement reason is accepted only for queued candidates.
	if _, err := db.FailQueuedDesign(owner, "req-1", got.Revision, 1, DesignReplacedReason); err == nil {
		t.Fatal("FailQueuedDesign accepted an interrupted candidate")
	}
}
