package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: advancePipeline must use the landing queue's exact rebased base for
// both full and scoped reviews, without losing the approved range on admission
// failure, while enforcing retry delay, attempt limits and distinct identities.
// Real pipeline, review and queue stores plus captured HTTP admission
// requests are the narrowest boundary proving retry scope (not provider behavior).
func TestPipelineRebaseBase(t *testing.T) {
	for _, mode := range []string{"full", "scoped_retry", "conflict", "unrelated_head", "invalid_base"} {
		t.Run(mode, func(t *testing.T) {
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
			target, err := ws.RecordWorkerRun(actor.AccountScopeID, store.WorkerRunRecord{WorkerID: coder.ID, UserID: actor.UserID, WorkspaceID: "workspace", SessionID: "rebase-base-session", Status: "succeeded", RequestSource: "direct"})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.sessions.Store().CreateSessionForAccount(store.SessionSnapshot{ID: target.SessionID}, actor.UserID, actor.AccountScopeID); err != nil {
				t.Fatal(err)
			}
			job := controlMCPJobID(controlMCPJobWorker, coder.ID, target.ID)
			oldBase, oldHead := strings.Repeat("a", 40), strings.Repeat("b", 40)
			newBase, head := strings.Repeat("c", 40), strings.Repeat("d", 40)
			key := pipelineKey(actor.AccountScopeID, job)
			if mode == "scoped_retry" {
				r, err := ws.RecordWorkerRun(actor.AccountScopeID, store.WorkerRunRecord{WorkerID: reviewer.ID, Role: store.WorkerRoleReviewer, UserID: actor.UserID, WorkspaceID: "workspace", Status: "succeeded", RequestSource: "direct", Input: map[string]any{"review_job": job}})
				if err != nil {
					t.Fatal(err)
				}
				if err := ws.RecordJobReview(actor.AccountScopeID, coder.ID, target.ID, store.JobReview{Head: oldHead, Verdict: "ok", ReviewerJobID: controlMCPJobID(controlMCPJobWorker, reviewer.ID, r.ID), RecordedAt: 1}); err != nil {
					t.Fatal(err)
				}
				if err := db.PutJSON(key, ReviewPipeline{Head: oldHead, Base: oldBase, Next: "review_approved"}); err != nil {
					t.Fatal(err)
				}
			}
			folder := t.TempDir()
			s.landQueue = publish.NewQueue(db)
			if _, err := s.landQueue.Enqueue(publish.Entry{Account: actor.AccountScopeID, Request: publish.Request{JobID: job, Folder: folder, Branch: "agent/job", Head: oldHead}}); err != nil {
				t.Fatal(err)
			}
			if err := s.landQueue.Process(ctx, actor.AccountScopeID, folder, publish.QueueHooks{
				Prepare: func(_ context.Context, e *publish.Entry) error {
					e.Request.BaseCommit, e.Request.Head = newBase, head
					if mode == "invalid_base" {
						e.Request.BaseCommit = "dev"
					}
					if mode == "unrelated_head" {
						e.Request.Head = oldHead
					}
					if mode == "conflict" {
						return &publish.SendBackError{Reason: "Rebase conflict: resolve changes"}
					}
					return &publish.RebaseReviewError{Reason: "review conflict resolution"}
				},
				SendBack: func(context.Context, publish.Entry) error { return nil },
			}); err != nil {
				t.Fatal(err)
			}
			s.signals = store.NewSignalStore(db)
			var requestIDs []string
			var requests []struct {
				Prompt string `json:"prompt"`
				Input  struct {
					Commits []string `json:"review_commits"`
				} `json:"input"`
			}
			s.controlMCPRoutesOnce.Do(func() {})
			s.controlMCPRoutes = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v3/workers":
					writeJSON(w, 200, map[string]any{"workers": []any{map[string]any{"id": reviewer.ID, "name": "reviewer"}}})
				case "/v3/projects":
					writeJSON(w, 200, map[string]any{"projects": []any{map[string]any{"id": "project", "workspaces": []any{map[string]any{"path": folder}}}}})
				case "/v3/workers/" + reviewer.ID + "/direct":
					var body json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					var identity struct {
						Key string `json:"idempotency_key"`
					}
					if err := json.Unmarshal(body, &identity); err != nil {
						t.Fatal(err)
					}
					requestIDs = append(requestIDs, identity.Key)
					requests = append(requests, struct {
						Prompt string `json:"prompt"`
						Input  struct {
							Commits []string `json:"review_commits"`
						} `json:"input"`
					}{})
					if err := json.Unmarshal(body, &requests[len(requests)-1]); err != nil {
						t.Fatal(err)
					}
					http.Error(w, "admission unavailable", http.StatusForbidden)
				default:
					if !strings.Contains(r.URL.Path, "environment") {
						t.Errorf("unexpected route %s", r.URL.Path)
					}
					http.NotFound(w, r)
				}
			})
			attempts := 1
			if mode == "scoped_retry" {
				attempts = 3
			}
			wantBase := newBase
			if mode == "unrelated_head" || mode == "invalid_base" {
				wantBase = oldBase
			}
			want := []string{wantBase}
			if mode == "scoped_retry" {
				want = append(want, oldBase, oldHead)
			}
			for attempt := 0; attempt < attempts; attempt++ {
				c, err := s.pipelineCall(ctx)
				if err != nil {
					t.Fatal(err)
				}
				ready, err := c.advancePipeline(coder.ID, target.ID, controlMCPJobRefs{sessionID: target.SessionID, projectID: "project"}, publish.Rule{Folder: folder}, map[string]any{"head": head, "base_commit": oldBase})
				if ready || err != nil {
					t.Fatalf("advance: ready=%v err=%v", ready, err)
				}
				if len(requests) != attempt+1 {
					t.Fatalf("admissions=%d want %d", len(requests), attempt+1)
				}
				got := requests[attempt]
				if !reflect.DeepEqual(got.Input.Commits, want) {
					t.Errorf("attempt %d commits=%v want %v", attempt, got.Input.Commits, want)
				}
				if !strings.Contains(got.Prompt, pipelineBaseBrief(wantBase, head)) {
					t.Errorf("brief does not use landing base: %s", got.Prompt)
				}
				var state ReviewPipeline
				if _, err := db.GetJSON(key, &state); err != nil {
					t.Fatal(err)
				}
				if state.Next != "review_failed" || state.InfrastructureAttempts == 0 || !strings.Contains(state.Findings, "admission failed") {
					t.Fatalf("failure not recorded: %+v", state)
				}
				if mode == "scoped_retry" {
					if state.Head != oldHead || state.Base != oldBase {
						t.Errorf("admission overwrote reviewed pair: %+v", state)
					}
					if !strings.Contains(got.Prompt, "Rebase-scoped review:") {
						t.Error("lost rebase-scoped brief")
					}
					if state.InfrastructureAttempts != attempt+1 {
						t.Errorf("attempt count=%d want %d", state.InfrastructureAttempts, attempt+1)
					}
					wantID := "pipeline-review-" + target.ID + "-" + head
					if attempt > 0 {
						wantID += fmt.Sprintf("-retry-%d", attempt)
					}
					if requestIDs[attempt] != wantID {
						t.Errorf("request ID=%q want %q", requestIDs[attempt], wantID)
					}
					assertBlocked := func() {
						t.Helper()
						ready, err := c.advancePipeline(coder.ID, target.ID, controlMCPJobRefs{sessionID: target.SessionID, projectID: "project"}, publish.Rule{Folder: folder}, map[string]any{"head": head, "base_commit": oldBase})
						if ready || err != nil {
							t.Fatalf("blocked advance: ready=%v err=%v", ready, err)
						}
						var after ReviewPipeline
						if _, err := db.GetJSON(key, &after); err != nil {
							t.Fatal(err)
						}
						if len(requests) != attempt+1 || !reflect.DeepEqual(after, state) {
							t.Fatalf("retry guard admitted or mutated state: admissions=%d before=%+v after=%+v", len(requests), state, after)
						}
					}
					if state.RetryAfter <= time.Now().UnixMilli() {
						t.Fatal("missing retry delay")
					}
					assertBlocked()
					// Expire retry delay without sleeping; preserve all review evidence.
					state.RetryAfter = 0
					if err := db.PutJSON(key, state); err != nil {
						t.Fatal(err)
					}
					if attempt == attempts-1 {
						assertBlocked()
					}
				}
			}
		})
	}
}
