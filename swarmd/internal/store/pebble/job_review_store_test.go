package pebblestore

import (
	"encoding/json"
	"testing"
)

// Purpose: RecordJobReview owns durable per-head evidence. Store-level tests
// prove idempotent completion, latest-per-head ordering, and cross-account
// rejection without unauthorized changes, not provider review accuracy. The same
// transaction must also retain metadata-only worker/review outbox receipts.
func TestJobReviewDurableLatestPerHead(t *testing.T) {
	s, ws := openTestStore(t)
	create := func(role string) WorkerRecord {
		w, err := ws.CreateWorker("account", "user", CreateWorkerRequest{Name: role, Role: role}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	coder, reviewer := create(WorkerRoleCoder), create(WorkerRoleReviewer)
	target, err := ws.RecordWorkerRun("account", WorkerRunRecord{WorkerID: coder.ID, UserID: "user", WorkspaceID: "workspace", Status: "succeeded", RequestSource: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	s.SetJobReviewObserver(func(_, _, _ string, _ JobReview) { count++ })
	record := func(head string, at int64) JobReview {
		r, err := ws.RecordWorkerRun("account", WorkerRunRecord{WorkerID: reviewer.ID, Role: WorkerRoleReviewer, UserID: "user", WorkspaceID: "workspace", Input: map[string]any{"review_job": "worker/" + coder.ID + "/" + target.ID}, Status: "succeeded", RequestSource: "direct"})
		if err != nil {
			t.Fatal(err)
		}
		v := ParseJobReview("VERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: test", head, "worker/"+reviewer.ID+"/"+r.ID, "model", at)
		if err := ws.RecordJobReview("account", coder.ID, target.ID, v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	first := record("head", 10)
	second := record("head", 20)
	record("other", 30)
	if err := ws.RecordJobReview("account", coder.ID, target.ID, first); err != nil {
		t.Fatal(err)
	}
	valid, err := ws.CurrentValidJobReview("account", coder.ID, target.ID, "head")
	if err != nil || valid == nil || valid.ReviewerJobID != second.ReviewerJobID {
		t.Fatal(valid, err)
	}
	latest, err := NewWorkerStore(s).GetJobReview("account", coder.ID, target.ID)
	if err != nil || latest.Head != "other" {
		t.Fatal(latest, err)
	}
	if err := ws.RecordJobReview("foreign", coder.ID, target.ID, second); err == nil {
		t.Fatal("foreign review accepted")
	}
	rows, err := NewSessionStore(s).ListV3RealtimeOutboxAfter(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	reviews, finished := 0, 0
	for _, row := range rows {
		var metadata WorkerRealtimePayload
		if err := json.Unmarshal(row.Event.Payload, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.ActivityKind == "review.recorded" {
			reviews++
			if metadata.WorkerID != coder.ID || metadata.RunID != target.ID || metadata.State != "ok" {
				t.Fatalf("bad review receipt %+v", metadata)
			}
		}
		if metadata.ActivityKind == "job.finished" {
			finished++
		}
	}
	if reviews != 3 || finished != 4 {
		t.Fatalf("receipts reviews=%d finished=%d", reviews, finished)
	}
	if count != 3 {
		t.Fatal("duplicate or rejected review notified", count)
	}
	if valid, err := ws.CurrentValidJobReview("account", coder.ID, target.ID, "new"); err != nil || valid != nil {
		t.Fatal(valid, err)
	}
}
