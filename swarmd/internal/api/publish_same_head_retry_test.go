package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: publishJob and Queue.EnqueueDelivery must consume a completed follow-up
// exactly once even without a commit. The real durable queue and session receipt
// boundary prove replay/restart rejection without invoking Git or a provider.
func TestPublishSameHeadCompletedFollowup(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "same-head-session"
	if err := s.sessions.Store().CreateSessionForAccount(store.SessionSnapshot{ID: sessionID}, actor.UserID, actor.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	s.controlMCPRoutesOnce.Do(func() {})
	s.controlMCPRoutes = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v3/workers/"):
			writeJSON(w, 200, map[string]any{"run": map[string]any{"status": "succeeded", "session_id": sessionID}})
		case strings.HasPrefix(r.URL.Path, "/v3/sessions/"):
			writeJSON(w, 200, map[string]any{"session": map[string]any{"metadata": map[string]any{"project_id": "project", "task_id": "task"}}})
		case strings.HasPrefix(r.URL.Path, "/v3/projects/"):
			writeJSON(w, 200, map[string]any{"task": map[string]any{"title": "fix"}})
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	c, err := s.pipelineCall(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	db := s.sessions.Store().Underlying()
	s.landQueue = publish.NewQueue(db)
	id := controlMCPJobID(controlMCPJobWorker, "worker", "run")
	req := publish.Request{JobID: id, Folder: "folder", Branch: "branch", Head: "original"}
	if _, err := s.landQueue.Enqueue(publish.Entry{Account: actor.AccountScopeID, Request: req}); err != nil {
		t.Fatal(err)
	}
	hooks := publish.QueueHooks{Prepare: func(context.Context, *publish.Entry) error { return &publish.SendBackError{Reason: "checks failed"} }, SendBack: func(context.Context, publish.Entry) error { return nil }}
	fail := func() {
		t.Helper()
		if err := s.landQueue.Process(context.Background(), actor.AccountScopeID, "folder", hooks); err != nil {
			t.Fatal(err)
		}
	}
	deliver := func(run, status, head, want string) {
		t.Helper()
		refs := controlMCPJobRefs{projectID: "project", taskID: "task", sessionID: sessionID, run: map[string]any{"followup_status": status, "followup_run_id": run}}
		receipt, err := c.publishJob(controlMCPJobWorker, "worker", "run", refs, publish.Rule{Mode: "land", Folder: "folder"}, map[string]any{"head": head, "worktree_branch": "branch"}, false)
		if err != nil || receipt["delivery_state"] != want {
			t.Fatalf("run=%s receipt=%v err=%v want=%s", run, receipt, err, want)
		}
	}
	fail()
	deliver("followup-1", "running", "original", "sent_back")
	deliver("", "completed", "original", "sent_back")
	deliver("followup-1", "completed", "original", "queued")
	fail()
	// A reconstructed queue must not replay the completed follow-up after failure.
	s.landQueue = publish.NewQueue(db)
	deliver("followup-1", "completed", "original", "landing_stopped")
	deliver("followup-2", "completed", "original", "queued")
	fail()
	deliver("followup-2", "completed", "original", "landing_stopped")
}
