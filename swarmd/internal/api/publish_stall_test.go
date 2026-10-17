package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: advancePipeline must review a completed fix explanation even when
// HEAD is unchanged, without replaying old findings or admitting duplicate reviews.
// Real pipeline/review stores and captured admission isolate this orchestration boundary.
func TestPublishStallNoChangeFix(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	db := s.sessions.Store().Underlying()
	ws := s.sessions.Store().WorkerStore()
	coder, err := ws.CreateWorker(actor.AccountScopeID, actor.UserID, store.CreateWorkerRequest{Name: "coder"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := ws.CreateWorker(actor.AccountScopeID, actor.UserID, store.CreateWorkerRequest{Name: "reviewer", Role: store.WorkerRoleReviewer}, nil)
	if err != nil {
		t.Fatal(err)
	}
	target, err := ws.RecordWorkerRun(actor.AccountScopeID, store.WorkerRunRecord{WorkerID: coder.ID, SessionID: "stall-session", UserID: actor.UserID, Status: "succeeded", RequestSource: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.sessions.Store().CreateSessionForAccount(store.SessionSnapshot{ID: target.SessionID}, actor.UserID, actor.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	job := controlMCPJobID(controlMCPJobWorker, coder.ID, target.ID)
	old, err := ws.RecordWorkerRun(actor.AccountScopeID, store.WorkerRunRecord{WorkerID: reviewer.ID, Role: store.WorkerRoleReviewer, UserID: actor.UserID, Status: "succeeded", RequestSource: "direct", Input: map[string]any{"review_job": job}})
	if err != nil {
		t.Fatal(err)
	}
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	oldJob := controlMCPJobID(controlMCPJobWorker, reviewer.ID, old.ID)
	if err := ws.RecordJobReview(actor.AccountScopeID, coder.ID, target.ID, store.JobReview{Head: head, Verdict: "block", Reason: "check invariant", ReviewerJobID: oldJob, RecordedAt: 1}); err != nil {
		t.Fatal(err)
	}
	key := pipelineKey(actor.AccountScopeID, job)
	if err := db.PutJSON(key, ReviewPipeline{Head: head, Base: base, Round: 1, Next: "review_fix_sent", ReviewerJobID: oldJob, Findings: "check invariant"}); err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	admissions := 0
	s.signals = store.NewSignalStore(db)
	s.controlMCPRoutesOnce.Do(func() {})
	s.controlMCPRoutes = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/workers":
			writeJSON(w, 200, map[string]any{"workers": []any{map[string]any{"id": reviewer.ID, "name": "reviewer"}}})
		case "/v3/projects":
			writeJSON(w, 200, map[string]any{"projects": []any{map[string]any{"id": "project", "workspaces": []any{map[string]any{"path": folder}}}}})
		case "/v3/workers/" + reviewer.ID + "/direct":
			admissions++
			var body struct {
				Prompt string `json:"prompt"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(body.Prompt, "not a code issue") || !strings.Contains(body.Prompt, "check invariant") {
				t.Errorf("missing review context: %s", body.Prompt)
			}
			writeJSON(w, 200, map[string]any{"run": map[string]any{"id": "review-next", "status": "running"}})
		case "/v3/workers/" + reviewer.ID + "/runs/review-next":
			writeJSON(w, 200, map[string]any{"run": map[string]any{"id": "review-next", "status": "running"}})
		default:
			http.NotFound(w, r)
		}
	})
	c, err := s.pipelineCall(ctx)
	if err != nil {
		t.Fatal(err)
	}
	refs := controlMCPJobRefs{sessionID: target.SessionID, projectID: "project", run: map[string]any{"followup_status": "running", "followup_run_id": "fix-one"}}
	result := map[string]any{"head": head, "base_commit": base, "report": "not a code issue"}
	advance := func() {
		t.Helper()
		ready, err := c.advancePipeline(coder.ID, target.ID, refs, publish.Rule{Folder: folder}, result)
		if ready || err != nil {
			t.Fatalf("advance: %v %v", ready, err)
		}
	}
	advance()
	if admissions != 0 {
		t.Fatal("reviewed unfinished fix")
	}
	refs.run["followup_status"] = "completed"
	advance()
	if admissions != 1 {
		t.Fatalf("admissions=%d want 1", admissions)
	}
	advance()
	if admissions != 1 {
		t.Fatal("duplicate review")
	}
	var state ReviewPipeline
	if _, err := db.GetJSON(key, &state); err != nil {
		t.Fatal(err)
	}
	if state.Next != "review_started" || state.Round != 1 || state.FollowupRunID != "fix-one" {
		t.Fatalf("state: %+v", state)
	}
	// A second blocking verdict on the same head consumes the final round.
	next, err := ws.RecordWorkerRun(actor.AccountScopeID, store.WorkerRunRecord{WorkerID: reviewer.ID, Role: store.WorkerRoleReviewer, UserID: actor.UserID, Status: "succeeded", RequestSource: "direct", Input: map[string]any{"review_job": job}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.RecordJobReview(actor.AccountScopeID, coder.ID, target.ID, store.JobReview{Head: head, Verdict: "block", Reason: "still unresolved", ReviewerJobID: controlMCPJobID(controlMCPJobWorker, reviewer.ID, next.ID), RecordedAt: 2}); err != nil {
		t.Fatal(err)
	}
	advance()
	if _, err := db.GetJSON(key, &state); err != nil {
		t.Fatal(err)
	}
	if state.Next != "review_exhausted" || state.Round != 2 {
		t.Fatalf("round limit: %+v", state)
	}
	advance()
	if admissions != 1 {
		t.Fatal("exhausted review admitted")
	}
}

// Purpose: autoPublishNext must distinguish terminal blocked workers from live
// or recovering work even when the job's derived status is stale. This pure
// decision boundary proves terminal work does not enter the retry path.
func TestPublishStallSettledBlocked(t *testing.T) {
	for _, status := range []string{"blocked", "in_progress", "needs_review"} {
		run := &autoPublishRunState{settled: true, workerBlocked: true}
		if got := autoPublishNext(status, nil, run); got != autoPublishSkip {
			t.Errorf("%s: got %d", status, got)
		}
	}
	for _, run := range []*autoPublishRunState{{active: true, settled: true, workerBlocked: true}, {recovering: true, settled: true, workerBlocked: true}, {workerBlocked: true}} {
		if got := autoPublishNext("blocked", nil, run); got != autoPublishWait {
			t.Errorf("live run skipped: %+v", run)
		}
	}
}

// Purpose: deliver must acknowledge terminal durable landing/pipeline records
// before expensive job assessment or retry. Real stores with a rejecting route
// prove no assessment is attempted and no retry budget remains.
func TestPublishStallTerminalDelivery(t *testing.T) {
	for _, mode := range []string{"landed", "review_exhausted"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _, cleanup := setupScopedAuthTestServer(t)
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			db := s.sessions.Store().Underlying()
			account := s.OutboxAccount()
			job := controlMCPJobID(controlMCPJobWorker, "coder", "run")
			if mode == "review_exhausted" {
				if err := db.PutJSON(pipelineKey(account, job), ReviewPipeline{Next: mode}); err != nil {
					t.Fatal(err)
				}
			} else {
				q, err := s.landingQueue()
				if err != nil {
					t.Fatal(err)
				}
				folder := t.TempDir()
				if _, err := q.Enqueue(publish.Entry{Account: account, Request: publish.Request{JobID: job, Folder: folder, Branch: "branch", Head: "head"}}); err != nil {
					t.Fatal(err)
				}
				if err := q.Process(ctx, account, folder, publish.QueueHooks{Prepare: func(context.Context, *publish.Entry) error { return nil }, Land: func(context.Context, publish.Entry) (publish.LandResult, error) { return publish.LandResult{}, nil }}); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			s.controlMCPRoutesOnce.Do(func() {})
			s.controlMCPRoutes = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "must not assess", 500) })
			p := s.NewAutoPublisher(store.NewSignalStore(db), db)
			sig := store.Signal{Seq: 2, Account: account, Refs: map[string]string{"worker_id": "coder", "run_id": "run"}}
			p.attempts[2] = 3
			if err := p.deliver(ctx, sig); err != nil {
				t.Errorf("terminal delivery retried: %v", err)
			}
			if calls != 0 || p.attempts[2] != 0 {
				t.Errorf("calls=%d attempts=%d", calls, p.attempts[2])
			}
		})
	}
}

// Purpose: consume must interrupt a stale retry backlog for fresh signals,
// without reversing signals for the same job. A real durable feed and callback
// append exercise the scheduling boundary deterministically, without sleeps.
func TestPublishStallFreshBeforeRetries(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.SetPublishConfig("configured")
	db := s.sessions.Store().Underlying()
	feed := store.NewSignalStore(db)
	p := s.NewAutoPublisher(feed, db)
	account := s.OutboxAccount()
	appendSignal := func(worker string) store.Signal {
		t.Helper()
		sig, err := feed.Append(store.Signal{Account: account, Kind: "worker.run.succeeded", Summary: "Job completed", Source: store.SignalSourceSwarmd, Severity: store.SignalSeverityInfo, Refs: map[string]string{"worker_id": worker, "run_id": "run"}})
		if err != nil {
			t.Fatal(err)
		}
		return sig
	}
	first, second := appendSignal("first"), appendSignal("second")
	if err := db.PutJSON(autoPublishCursorKey, second.Seq); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(autoPublishPendingKey, []autoPublishPending{{Signal: first}, {Signal: second}}); err != nil {
		t.Fatal(err)
	}
	var order []string
	if err := p.consume(ctx, func(_ context.Context, sig store.Signal) error {
		worker := sig.Refs["worker_id"]
		order = append(order, worker)
		if worker == "first" {
			appendSignal("second")
			appendSignal("fresh")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ","); got != "first,fresh,second,second" {
		t.Fatalf("delivery order: %s", got)
	}
	var pending []autoPublishPending
	if _, err := db.GetJSON(autoPublishPendingKey, &pending); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending: %+v", pending)
	}
}

// Purpose: nextRetryDelay must not wake continuously for an order-blocked
// successor while consume preserves its predecessor's backoff. Real durable
// pending state exercises both scheduler boundaries without timers or sleeps.
func TestPublishStallBlockedSuccessorDeadline(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	s.SetPublishConfig("configured")
	db := s.sessions.Store().Underlying()
	feed := store.NewSignalStore(db)
	p := s.NewAutoPublisher(feed, db)
	account := s.OutboxAccount()
	signal := func(seq uint64, worker string) store.Signal {
		return store.Signal{Seq: seq, Account: account, Refs: map[string]string{"worker_id": worker, "run_id": "run"}}
	}
	pending := []autoPublishPending{
		{Signal: signal(1, "first"), NextAttempt: time.Now().Add(time.Hour)},
		{Signal: signal(2, "first")},
	}
	if err := db.PutJSON(autoPublishCursorKey, uint64(2)); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(autoPublishPendingKey, pending); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.consume(ctx, func(context.Context, store.Signal) error { t.Fatal("delivered order-blocked work"); return nil }); err != nil {
		t.Fatal(err)
	}
	if delay, err := p.nextRetryDelay(); err != nil || delay != 30*time.Second {
		t.Fatalf("blocked successor delay=%v err=%v; want idle cap", delay, err)
	}
	// An unrelated ready job must still wake immediately.
	pending = append(pending, autoPublishPending{Signal: signal(3, "other")})
	if err := db.PutJSON(autoPublishPendingKey, pending); err != nil {
		t.Fatal(err)
	}
	if delay, err := p.nextRetryDelay(); err != nil || delay != 0 {
		t.Fatalf("unrelated ready delay=%v err=%v", delay, err)
	}
	// Once the predecessor is removed, its successor becomes eligible.
	if err := db.PutJSON(autoPublishPendingKey, pending[1:2]); err != nil {
		t.Fatal(err)
	}
	if delay, err := p.nextRetryDelay(); err != nil || delay != 0 {
		t.Fatalf("unblocked successor delay=%v err=%v", delay, err)
	}
}
