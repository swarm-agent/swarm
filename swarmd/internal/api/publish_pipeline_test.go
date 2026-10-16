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

// Purpose: advancePipeline must durably exhaust a job's BLOCK budget before
// sending another follow-up. Real review/worker/signal stores exercise the
// narrow transition boundary, including replay and isolation from another job.
func TestReviewPipelineExhaustion(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db := s.sessions.Store().Underlying()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	account, user := actor.AccountScopeID, actor.UserID
	ws := s.sessions.Store().WorkerStore()
	coder, err := ws.CreateWorker(account, user, store.CreateWorkerRequest{Name: "coder"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := ws.CreateWorker(account, user, store.CreateWorkerRequest{Name: "reviewer", Role: store.WorkerRoleReviewer}, nil)
	if err != nil {
		t.Fatal(err)
	}
	target, err := ws.RecordWorkerRun(account, store.WorkerRunRecord{WorkerID: coder.ID, UserID: user, WorkspaceID: "workspace", SessionID: "pipeline-session", Status: "succeeded", RequestSource: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.sessions.Store().CreateSessionForAccount(store.SessionSnapshot{ID: target.SessionID}, user, account); err != nil {
		t.Fatal(err)
	}
	job := "worker/" + coder.ID + "/" + target.ID
	r, err := ws.RecordWorkerRun(account, store.WorkerRunRecord{WorkerID: reviewer.ID, Role: store.WorkerRoleReviewer, UserID: user, WorkspaceID: "workspace", Status: "succeeded", RequestSource: "direct", Input: map[string]any{"review_job": job}})
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("a", 40)
	if err := ws.RecordJobReview(account, coder.ID, target.ID, store.JobReview{Head: head, Verdict: "block", ReviewerJobID: "worker/" + reviewer.ID + "/" + r.ID, RecordedAt: 1, Reason: "defect in diff"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(pipelineKey(account, job), ReviewPipeline{Round: 1}); err != nil {
		t.Fatal(err)
	}
	s.signals = store.NewSignalStore(db)
	req := httptest.NewRequest("POST", "/mcp", nil)
	req = req.WithContext(context.WithValue(req.Context(), productPrincipalRequestContextKey, identity.Principal{Type: "user", AccountScopeID: account, UserID: user}))
	for i := 0; i < 2; i++ {
		c := &controlMCPCall{server: s, request: req, next: s.apiMux()}
		ready, err := c.advancePipeline(coder.ID, target.ID, controlMCPJobRefs{sessionID: target.SessionID}, publish.Rule{}, map[string]any{"head": head})
		if err != nil || ready {
			t.Fatalf("advance ready=%v err=%v", ready, err)
		}
	}
	var chain ReviewPipeline
	if _, err := db.GetJSON(pipelineKey(account, job), &chain); err != nil {
		t.Fatal(err)
	}
	if chain.Round != 2 || chain.Next != "review_exhausted" || chain.Findings != "defect in diff" {
		t.Fatalf("chain: %+v", chain)
	}
	if found, err := db.GetJSON(pipelineKey("other", job), &ReviewPipeline{}); err != nil || found {
		t.Fatalf("cross-account state: %v %v", found, err)
	}
	page, err := s.signals.ListAfter(0, 10, store.SignalFilter{Account: account})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, sig := range page.Signals {
		if sig.Kind == "job.review_exhausted" {
			n++
			if sig.Attrs["findings"] != "defect in diff" {
				t.Fatal(sig)
			}
		}
	}
	if n != 1 {
		t.Fatalf("exhaustion signals=%d", n)
	}
	// A rebase BLOCK at the exhausted ordinary budget must reach follow-up,
	// not exhaustion. This fixture has no follow-up executor: its explicit
	// error must leave the durable pending transition and budget unchanged.
	chain.Next, chain.Rebase = "review_started", true
	if err := db.PutJSON(pipelineKey(account, job), chain); err != nil {
		t.Fatal(err)
	}
	c := &controlMCPCall{server: s, request: req, next: s.apiMux()}
	if ready, err := c.advancePipeline(coder.ID, target.ID, controlMCPJobRefs{sessionID: target.SessionID}, publish.Rule{}, map[string]any{"head": head}); ready || err == nil {
		t.Fatalf("expected unavailable follow-up, got %v %v", ready, err)
	}
	if _, err := db.GetJSON(pipelineKey(account, job), &chain); err != nil {
		t.Fatal(err)
	}
	if chain.Round != 2 || chain.Next != "review_fix_pending" {
		t.Fatalf("rebase consumed budget: %+v", chain)
	}
}

// Purpose: the daemon's actual scoped principal must cross the direct-worker
// HTTP authorization boundary; removing automations:write must reject it.
// No handler is replaced: an invalid worker reaches the real not-found path.
func TestReviewPipelinePrincipal(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	c, err := s.pipelineCall(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.dispatch("POST", "/v3/workers/missing/direct", nil, map[string]any{"prompt": "review"})
	if err == nil || strings.Contains(err.Error(), "403") {
		t.Fatalf("pipeline authorization: %v", err)
	}
	c, err = s.internalCall(context.Background(), "sessions:write")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.dispatch("POST", "/v3/workers/missing/direct", nil, map[string]any{"prompt": "review"})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("missing scope accepted: %v", err)
	}
}

// Purpose: replay must repair state persisted before its receipt even on the
// already-started early-return path. Real activity and signal stores must emit
// exactly once, without attempting a second reviewer admission.
func TestReviewPipelineReceiptReplay(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db := s.sessions.Store().Underlying()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.sessions.Store().CreateSessionForAccount(store.SessionSnapshot{ID: "receipt-session"}, actor.UserID, actor.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	s.signals = store.NewSignalStore(db)
	state := ReviewPipeline{Head: strings.Repeat("a", 40), Next: "review_started", ReviewerJobID: "worker/reviewer/review"}
	if err := db.PutJSON(pipelineKey(actor.AccountScopeID, "worker/coder/run"), state); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		c, err := s.pipelineCall(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ready, err := c.advancePipeline("coder", "run", controlMCPJobRefs{sessionID: "receipt-session"}, publish.Rule{}, map[string]any{"head": state.Head})
		if err != nil || ready {
			t.Fatalf("replay: %v %v", ready, err)
		}
	}
	page, err := s.signals.ListAfter(0, 10, store.SignalFilter{Account: actor.AccountScopeID})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, sig := range page.Signals {
		if sig.Kind == "job.review_started" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("receipts=%d", n)
	}
}

// Purpose: advancePipeline must stop failed admission for the recorded head
// across repeated sweeps, repairing exactly one durable job.review_failed
// receipt without attempting another reviewer. Real stores and receipt ACLs
// are the narrowest layer proving replay does not duplicate the failure.
func TestReviewPipelineFailedAdmissionReplay(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db := s.sessions.Store().Underlying()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.sessions.Store().CreateSessionForAccount(store.SessionSnapshot{ID: "failed-review-session"}, actor.UserID, actor.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	s.signals = store.NewSignalStore(db)
	state := ReviewPipeline{Head: strings.Repeat("a", 40), Next: "review_failed", Findings: "review checkout missing"}
	key := pipelineKey(actor.AccountScopeID, "worker/coder/run")
	if err := db.PutJSON(key, state); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		c, err := s.pipelineCall(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ready, err := c.advancePipeline("coder", "run", controlMCPJobRefs{sessionID: "failed-review-session"}, publish.Rule{}, map[string]any{"head": state.Head})
		if err != nil || ready {
			t.Fatalf("failed head retried: %v %v", ready, err)
		}
	}
	var after ReviewPipeline
	if _, err := db.GetJSON(key, &after); err != nil || after != state {
		t.Fatalf("failure state changed: %+v %v", after, err)
	}
	page, err := s.signals.ListAfter(0, 20, store.SignalFilter{Account: actor.AccountScopeID})
	if err != nil {
		t.Fatal(err)
	}
	failures := 0
	for _, sig := range page.Signals {
		if sig.Kind == "job.review_started" {
			t.Fatal("failed head restarted")
		}
		if sig.Kind == "job.review_failed" {
			failures++
			if sig.Attrs["findings"] != state.Findings {
				t.Fatal("failure reason lost")
			}
		}
	}
	if failures != 1 {
		t.Fatalf("failure receipts=%d", failures)
	}
}
