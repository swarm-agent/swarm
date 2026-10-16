package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: publishJob is AutoPublisher's admission boundary. A completed fix
// must cross both its stopped-state guard and the durable queue guard; unfinished,
// unchanged and exhausted fixes must leave the stored entry unchanged. Read-model
// HTTP fixtures isolate this boundary while session receipts use the real store.
func TestPublishLandingStoppedCompletedFix(t *testing.T) {
	for _, tc := range []struct {
		name, status, head string
		rounds             int
		want               string
	}{
		{"completed", "completed", "new-commit", 2, "queued"},
		{"running", "running", "new-commit", 2, "landing_stopped"},
		{"unchanged", "completed", "old-commit", 2, "landing_stopped"},
		{"exhausted", "completed", "new-commit", 3, "landing_stopped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, cleanup := setupScopedAuthTestServer(t)
			defer cleanup()
			actor, err := s.identitySessions.ActorForCurrentSelection()
			if err != nil {
				t.Fatal(err)
			}
			const sessionID = "fix-round-session"
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
			id := controlMCPJobID(controlMCPJobWorker, "worker", "run")
			mem := &landMemoryStore{}
			old := publish.Entry{Account: actor.AccountScopeID, State: "landing_stopped", SendBacks: tc.rounds, FollowUpID: "fix-round", Request: publish.Request{JobID: id, Folder: "folder", Branch: "branch", Head: "old-commit"}}
			if err := mem.PutJSON("", []publish.Entry{old}); err != nil {
				t.Fatal(err)
			}
			s.landQueue = publish.NewQueue(mem)
			refs := controlMCPJobRefs{projectID: "project", taskID: "task", sessionID: sessionID, run: map[string]any{"followup_status": tc.status}}
			receipt, err := c.publishJob(controlMCPJobWorker, "worker", "run", refs, publish.Rule{Mode: "land", Folder: "folder"}, map[string]any{"head": tc.head, "worktree_branch": "branch"}, false)
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := s.landQueue.Get(actor.AccountScopeID, id)
			if err != nil || got.State != tc.want || receipt["delivery_state"] != tc.want || got.SendBacks != tc.rounds {
				t.Fatalf("receipt=%v entry=%+v err=%v", receipt, got, err)
			}
			if tc.want != "queued" && got.Request.Head != old.Request.Head {
				t.Fatal("rejected fix changed head")
			}
		})
	}
}
