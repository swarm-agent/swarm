package api

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: consume must resolve headless completions and historical recovery
// must preserve that authorized reopening. Real durable queue, pipeline and
// signal records exercise consume/recoverReviews/deliver across reconstruction;
// routed assessment responses keep the new review pending without Git/providers.
func TestPublishRetireCompletionRecovery(t *testing.T) {
	for _, landed := range []bool{false, true} {
		t.Run(fmt.Sprint(landed), func(t *testing.T) {
			s, _, _, cleanup := setupScopedAuthTestServer(t)
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			db := s.sessions.Store().Underlying()
			account := s.OutboxAccount()
			feed := store.NewSignalStore(db)
			s.publishConfig = "configured"
			folder := t.TempDir()
			s.loadPublishConfig = func(string) ([]publish.Rule, error) {
				return []publish.Rule{{Folder: folder, Mode: "land", Auto: true, RequireReview: true}}, nil
			}
			p := s.NewAutoPublisher(feed, db)
			sig := store.Signal{Account: account, Kind: "worker.run.succeeded", Summary: "completion", Source: store.SignalSourceSwarmd, Severity: store.SignalSeverityInfo, Refs: map[string]string{"worker_id": "coder", "run_id": "run"}}
			job := controlMCPJobID(controlMCPJobWorker, "coder", "run")
			old, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
			if err := db.PutJSON(pipelineKey(account, job), ReviewPipeline{Head: old, Next: "review_exhausted"}); err != nil {
				t.Fatal(err)
			}
			if landed {
				if err := p.retire(sig, old); err != nil {
					t.Fatal(err)
				}
				if err := db.PutJSON(fmt.Sprintf("publish_queue/%x", sha256.Sum256([]byte(account))), []publish.Entry{{Account: account, State: "landed", Request: publish.Request{JobID: job, Head: old}}}); err != nil {
					t.Fatal(err)
				}
			}
			views := 0
			unavailable := false
			s.controlMCPRoutesOnce.Do(func() {})
			s.controlMCPRoutes = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v3/workers/coder/runs/run" {
					views++
					if unavailable {
						http.Error(w, "assessment unavailable", 503)
						return
					}
					writeJSON(w, 200, map[string]any{"run": map[string]any{"status": "succeeded", "review": map[string]any{"project_id": "project", "task_id": "task"}}})
					return
				}
				if r.URL.Path == "/v3/projects/project/tasks/task" {
					writeJSON(w, 200, map[string]any{"task": map[string]any{"status": "needs_review", "source_workspace": map[string]any{"path": folder}, "delivery_assessment": map[string]any{"source_oid": head, "state": "ready"}}})
					return
				}
				http.NotFound(w, r)
			})
			if err := p.consume(ctx, p.deliver); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if _, err := feed.Append(sig); err != nil {
					t.Fatal(err)
				}
			}
			admitted := 0
			if err := p.consume(ctx, func(ctx context.Context, got store.Signal) error {
				if got.Attrs["head"] != head {
					t.Errorf("completion head=%q want %q", got.Attrs["head"], head)
				}
				admitted++
				return db.PutJSON(pipelineKey(account, job), ReviewPipeline{Head: head, Next: "review_started"})
			}); err != nil {
				t.Fatal(err)
			}
			if admitted == 0 {
				t.Fatal("fresh completion was not assessed")
			}
			// The historical review, not the pending new head, is what recovery emits.
			if err := db.PutJSON("worker_review/"+url.PathEscape(account)+"/coder/run", store.JobReviewRecord{ByHead: map[string]store.JobReview{old: {Head: old, Verdict: "ok"}}}); err != nil {
				t.Fatal(err)
			}
			p = s.NewAutoPublisher(feed, db)
			recoveryViews := views
			if err := p.recoverReviews(ctx, p.deliver); err != nil {
				t.Fatal(err)
			}
			if views == recoveryViews {
				t.Fatal("historical recovery did not reach current-head assessment")
			}
			var pending ReviewPipeline
			if _, err := db.GetJSON(pipelineKey(account, job), &pending); err != nil {
				t.Fatal(err)
			}
			if pending.Head != head || pending.Next != "review_started" {
				t.Fatalf("recovery changed pending review: %+v", pending)
			}
			var reopened autoPublishRetirement
			if _, err := db.GetJSON(publishRetiredKey(sig), &reopened); err != nil {
				t.Fatal(err)
			}
			if reopened.Retired || reopened.Head != head {
				t.Fatalf("recovery revoked reopening: %+v", reopened)
			}
			unavailable = true
			before := views
			sig.Kind = "job.reviewed"
			sig.Attrs = map[string]string{"verdict": "ok", "head": head}
			if err := p.deliver(ctx, sig); err == nil || views == before {
				t.Fatal("new-head review skipped instead of reaching assessment")
			}
			if retired, _ := p.isRetired(sig); retired {
				t.Fatal("failed assessment retired new head")
			}
		})
	}
}

// Purpose: terminal pipeline heads retire before assessment and remain excluded
// by recoverReviews after reconstruction. Pebble plus callback counts isolate
// the durable scheduling boundary without a Git assessment.
func TestPublishRetireRecovery(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db := s.sessions.Store().Underlying()
	account := s.OutboxAccount()
	sig := store.Signal{Account: account, Refs: map[string]string{"worker_id": "coder", "run_id": "run"}, Attrs: map[string]string{"head": "head"}}
	if err := db.PutJSON("worker_review/"+url.PathEscape(account)+"/coder/run", store.JobReviewRecord{ByHead: map[string]store.JobReview{"head": {Head: "head", Verdict: "ok"}}}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(pipelineKey(account, controlMCPJobID(controlMCPJobWorker, "coder", "run")), ReviewPipeline{Head: "head", Next: "review_exhausted"}); err != nil {
		t.Fatal(err)
	}
	p := s.NewAutoPublisher(store.NewSignalStore(db), db)
	if err := p.deliver(context.Background(), sig); err != nil {
		t.Fatal(err)
	}
	p = s.NewAutoPublisher(store.NewSignalStore(db), db)
	calls := 0
	if err := p.recoverReviews(context.Background(), func(context.Context, store.Signal) error { calls++; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("retired job assessed %d times", calls)
	}
}

// Purpose: recoverReviews limits pending recovery to one pass per minute.
// Callback counts and an explicit previous timestamp avoid sleeps.
func TestPublishRetireRecoveryCadence(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db := s.sessions.Store().Underlying()
	if err := db.PutJSON("worker_review/"+url.PathEscape(s.OutboxAccount())+"/coder/run", store.JobReviewRecord{ByHead: map[string]store.JobReview{"head": {Head: "head", Verdict: "ok"}}}); err != nil {
		t.Fatal(err)
	}
	p := s.NewAutoPublisher(store.NewSignalStore(db), db)
	calls := 0
	deliver := func(context.Context, store.Signal) error { calls++; return nil }
	for i := 0; i < 3; i++ {
		if err := p.recoverReviews(context.Background(), deliver); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("recovery calls=%d", calls)
	}
	p.lastRecovery = time.Now().Add(-time.Minute)
	if err := p.recoverReviews(context.Background(), deliver); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("recovery did not resume: %d", calls)
	}
}

// Purpose: unretireCompletion alone authorizes a changed-head reopening;
// same-head completions and review replay must leave durable retirement intact.
// Helper-level durable state is the narrowest ingestion authority test.
func TestPublishRetireNewHead(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db := s.sessions.Store().Underlying()
	p := s.NewAutoPublisher(store.NewSignalStore(db), db)
	sig := store.Signal{Account: s.OutboxAccount(), Refs: map[string]string{"worker_id": "coder", "run_id": "run"}, Attrs: map[string]string{"head": "old"}}
	if err := p.retire(sig, "old"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"job.reviewed", "worker.run.succeeded"} {
		sig.Kind = kind
		if err := p.unretireCompletion(context.Background(), &sig); err != nil {
			t.Fatal(err)
		}
		if retired, err := p.isRetired(sig); err != nil || !retired {
			t.Fatalf("same head reopened: %v", err)
		}
	}
	sig.Kind = "job.reviewed"
	sig.Attrs["head"] = "new"
	if err := p.unretireCompletion(context.Background(), &sig); err != nil {
		t.Fatal(err)
	}
	if retired, _ := p.isRetired(sig); !retired {
		t.Fatal("review reopened retirement")
	}
	sig.Kind = "worker.run.succeeded"
	if err := p.unretireCompletion(context.Background(), &sig); err != nil {
		t.Fatal(err)
	}
	p = s.NewAutoPublisher(store.NewSignalStore(db), db)
	if retired, err := p.isRetired(sig); err != nil || retired {
		t.Fatalf("new head remains retired: %v", err)
	}
}

// Purpose: settleEndedBlocked recognizes ended inactive workers even without
// phase metadata; autoPublishNext must still wait for active/unfinished workers.
// The pure state boundary isolates this lifecycle decision.
func TestPublishRetireBlockedEnded(t *testing.T) {
	for _, tc := range []struct {
		active bool
		end    int64
		want   int
	}{{false, 1, autoPublishSkip}, {true, 1, autoPublishWait}, {false, 0, autoPublishWait}} {
		run := &autoPublishRunState{active: tc.active}
		run.settleEndedBlocked("blocked", tc.end)
		if got := autoPublishNext("blocked", nil, run); got != tc.want {
			t.Fatalf("%+v got %d", tc, got)
		}
	}
}

// Purpose: deliver retires stored reviewer roles before routed job assessment.
// A rejecting route plus real WorkerStore records proves the cheap read boundary.
func TestPublishRetireReviewer(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db := s.sessions.Store().Underlying()
	account := s.OutboxAccount()
	ws := store.NewWorkerStore(db)
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	worker, err := ws.CreateWorker(account, actor.UserID, store.CreateWorkerRequest{Name: "reviewer", Role: store.WorkerRoleReviewer}, nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := ws.RecordWorkerRun(account, store.WorkerRunRecord{WorkerID: worker.ID, Role: store.WorkerRoleReviewer, Status: "succeeded", RequestSource: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.controlMCPRoutesOnce.Do(func() {})
	s.controlMCPRoutes = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "unexpected assessment", 500) })
	p := s.NewAutoPublisher(store.NewSignalStore(db), db)
	sig := store.Signal{Account: account, Refs: map[string]string{"worker_id": worker.ID, "run_id": run.ID}}
	if err := p.deliver(context.Background(), sig); err != nil {
		t.Fatal(err)
	}
	if retired, err := p.isRetired(sig); err != nil || !retired || calls != 0 {
		t.Fatalf("retired=%v calls=%d err=%v", retired, calls, err)
	}
}
