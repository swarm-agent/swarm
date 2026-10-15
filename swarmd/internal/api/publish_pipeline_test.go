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
}
