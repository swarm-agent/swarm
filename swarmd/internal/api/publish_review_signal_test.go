package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: AutoPublisher.consume routes BLOCK for policy evaluation but must
// never admit it to landing. Durable OK review signals flow into
// publishJob and the durable queue without replaying them after restart. Real
// signal/review/queue stores prove the integration; job discovery and the
// external landing command are supplied at their boundaries, not providers.
// Real worker routing and an owned session also exercise activity receipt ACLs.
func TestAutoPublishReviewSignalQueuesLanding(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db := s.sessions.Store().Underlying()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	account, user := actor.AccountScopeID, actor.UserID
	handler := s.apiMux()
	s.SetPublishConfig("configured")
	ws := s.sessions.Store().WorkerStore()
	coder, err := ws.CreateWorker(account, user, store.CreateWorkerRequest{Name: "coder"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := ws.CreateWorker(account, user, store.CreateWorkerRequest{Name: "reviewer", Role: store.WorkerRoleReviewer}, nil)
	if err != nil {
		t.Fatal(err)
	}
	target, err := ws.RecordWorkerRun(account, store.WorkerRunRecord{WorkerID: coder.ID, UserID: user, SessionID: "review-signal-session", WorkspaceID: "workspace", Status: "succeeded", RequestSource: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	jobID := "worker/" + coder.ID + "/" + target.ID
	head := strings.Repeat("a", 40)
	req := httptest.NewRequest("POST", "/mcp", nil)
	req = req.WithContext(context.WithValue(req.Context(), productPrincipalRequestContextKey, identity.Principal{Type: "user", AccountScopeID: account, UserID: user}))
	if err := s.sessions.Store().CreateSessionForAccount(store.SessionSnapshot{ID: target.SessionID}, user, account); err != nil {
		t.Fatal(err)
	}
	c := &controlMCPCall{server: s, request: req, next: handler}
	feed := store.NewSignalStore(db)
	appendSignal := func(kind, verdict string) {
		t.Helper()
		_, err := feed.Append(store.Signal{Kind: kind, Severity: store.SignalSeverityInfo, Source: store.SignalSourceSwarmd, Account: account, Summary: "review test", Refs: map[string]string{"worker_id": coder.ID, "run_id": target.ID}, Attrs: map[string]string{"verdict": verdict}})
		if err != nil {
			t.Fatal(err)
		}
	}
	appendSignal("test.started", "")
	p := s.NewAutoPublisher(feed, db)
	calls := 0
	deliver := func(ctx context.Context, sig store.Signal) error {
		calls++
		if sig.Refs["worker_id"] != coder.ID || sig.Refs["run_id"] != target.ID {
			t.Fatal("wrong signal target")
		}
		_, err := c.publishJob(controlMCPJobWorker, coder.ID, target.ID, controlMCPJobRefs{}, publish.Rule{Folder: "/project", Mode: "land", Auto: true, RequireReview: true}, map[string]any{"head": head, "worktree_branch": "agent/job", "base_branch": "dev"}, false)
		return err
	}
	consume := func() {
		t.Helper()
		if err := p.consume(context.Background(), deliver); err != nil {
			t.Fatal(err)
		}
	}
	consume()
	appendSignal("worker.run.succeeded", "")
	consume()
	q, err := s.landingQueue()
	if err != nil {
		t.Fatal(err)
	}
	assertEmpty := func() {
		t.Helper()
		e, _, err := q.Get(account, jobID)
		if err != nil || e.State != "" {
			t.Fatalf("unreviewed job queued: %+v %v", e, err)
		}
	}
	assertEmpty()
	appendSignal("job.reviewed", "block")
	consume()
	assertEmpty()
	if calls != 2 {
		t.Fatalf("BLOCK policy dispatch count: %d", calls)
	}
	r, err := ws.RecordWorkerRun(account, store.WorkerRunRecord{WorkerID: reviewer.ID, Role: store.WorkerRoleReviewer, UserID: user, WorkspaceID: "workspace", Status: "succeeded", RequestSource: "direct", Input: map[string]any{"review_job": jobID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.RecordJobReview(account, coder.ID, target.ID, store.JobReview{Head: head, Verdict: "ok", ReviewerJobID: "worker/" + reviewer.ID + "/" + r.ID, RecordedAt: 1}); err != nil {
		t.Fatal(err)
	}
	// Deliberately omit the OK signal: durable recovery must still enqueue.
	if err := p.recoverReviews(context.Background(), deliver); err != nil {
		t.Fatal(err)
	}
	consume()
	e, _, err := q.Get(account, jobID)
	if err != nil || e.State != "queued" || e.Request.Head != head || calls != 3 {
		t.Fatalf("OK failed to queue landing: %+v calls=%d err=%v", e, calls, err)
	}
	land := 0
	if err := q.Process(context.Background(), account, "/project", publish.QueueHooks{Prepare: func(context.Context, *publish.Entry) error {
		return c.requireLandReview(controlMCPJobWorker, coder.ID, target.ID, head)
	}, Land: func(_ context.Context, e publish.Entry) (publish.LandResult, error) {
		land++
		return publish.LandResult{Commit: e.Request.Head}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	e, _, err = q.Get(account, jobID)
	if err != nil || e.State != "landed" || land != 1 {
		t.Fatalf("landing not fired: %+v %v", e, err)
	}
	p = s.NewAutoPublisher(feed, db)
	consume()
	if calls != 3 || land != 1 {
		t.Fatal("review signal replayed after restart")
	}
}
