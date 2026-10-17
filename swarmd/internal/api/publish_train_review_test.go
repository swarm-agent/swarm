package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: advancePipeline must use a stacked candidate's exact queue base in
// every delivery state, excluding predecessor commits from reviewer input.
// Real queue/pipeline stores and captured reviewer admission are the narrowest
// boundary proving the range sent to review; unrelated heads must not inherit it.
func TestTrainReviewPipelineBase(t *testing.T) {
	for _, state := range []string{"land_failed", "landing", "review_pending", "unrelated_head", "invalid_base"} {
		t.Run(state, func(t *testing.T) {
			s, _, _, cleanup := setupScopedAuthTestServer(t)
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			actor, err := s.identitySessions.ActorForCurrentSelection()
			if err != nil {
				t.Fatal(err)
			}
			ws := s.sessions.Store().WorkerStore()
			coder, err := ws.CreateWorker(actor.AccountScopeID, actor.UserID, store.CreateWorkerRequest{Name: "coder"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			reviewer, err := ws.CreateWorker(actor.AccountScopeID, actor.UserID, store.CreateWorkerRequest{Name: "reviewer", Role: store.WorkerRoleReviewer}, nil)
			if err != nil {
				t.Fatal(err)
			}
			target, err := ws.RecordWorkerRun(actor.AccountScopeID, store.WorkerRunRecord{WorkerID: coder.ID, UserID: actor.UserID, WorkspaceID: "workspace", SessionID: "train-review-session", Status: "succeeded", RequestSource: "direct"})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.sessions.Store().CreateSessionForAccount(store.SessionSnapshot{ID: target.SessionID}, actor.UserID, actor.AccountScopeID); err != nil {
				t.Fatal(err)
			}
			db := s.sessions.Store().Underlying()
			s.signals = store.NewSignalStore(db)
			job := controlMCPJobID(controlMCPJobWorker, coder.ID, target.ID)
			oldBase, base, head := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
			folder := t.TempDir()
			q, err := s.landingQueue()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := q.Enqueue(publish.Entry{Account: actor.AccountScopeID, Request: publish.Request{JobID: job, Folder: folder, Branch: "agent/job", BaseBranch: "dev", BaseCommit: oldBase, Head: head}}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			wantBase := base
			if state == "unrelated_head" || state == "invalid_base" {
				wantBase = oldBase
			}
			s.controlMCPRoutesOnce.Do(func() {})
			s.controlMCPRoutes = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v3/workers":
					writeJSON(w, 200, map[string]any{"workers": []any{map[string]any{"id": reviewer.ID, "name": "reviewer"}}})
				case "/v3/projects":
					writeJSON(w, 200, map[string]any{"projects": []any{map[string]any{"id": "project", "workspaces": []any{map[string]any{"path": folder}}}}})
				case "/v3/workers/" + reviewer.ID + "/direct":
					var body struct {
						Prompt string `json:"prompt"`
						Input  struct {
							Commits []string `json:"review_commits"`
						} `json:"input"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					calls++
					if len(body.Input.Commits) != 1 || body.Input.Commits[0] != wantBase {
						t.Errorf("review bases=%v want [%s]", body.Input.Commits, wantBase)
					}
					if !strings.Contains(body.Prompt, pipelineBaseBrief(wantBase, head)) {
						t.Error("review brief includes wrong base")
					}
					http.Error(w, "admission unavailable", http.StatusForbidden)
				default:
					if !strings.Contains(r.URL.Path, "environment") {
						t.Errorf("unexpected route %s", r.URL.Path)
					}
					http.NotFound(w, r)
				}
			})
			advance := func() {
				t.Helper()
				c, err := s.pipelineCall(ctx)
				if err != nil {
					t.Fatal(err)
				}
				ready, err := c.advancePipeline(coder.ID, target.ID, controlMCPJobRefs{sessionID: target.SessionID, projectID: "project"}, publish.Rule{Folder: folder}, map[string]any{"head": head, "base_commit": oldBase})
				if ready || err != nil {
					t.Fatalf("advance ready=%v err=%v", ready, err)
				}
			}
			if err := q.Process(ctx, actor.AccountScopeID, folder, publish.QueueHooks{
				Prepare: func(_ context.Context, e *publish.Entry) error {
					e.Request.BaseCommit = base
					if state == "invalid_base" {
						e.Request.BaseCommit = "dev"
					}
					if state == "unrelated_head" {
						e.Request.Head = strings.Repeat("d", 40)
					}
					if state == "review_pending" {
						return &publish.RebaseReviewError{Reason: "review required"}
					}
					return nil
				},
				Land: func(_ context.Context, e publish.Entry) (publish.LandResult, error) {
					if state == "landing" {
						advance()
					}
					return publish.LandResult{}, errors.New("push failed")
				},
			}); err != nil {
				t.Fatal(err)
			}
			if state != "landing" {
				advance()
			}
			if calls != 1 {
				t.Fatalf("review admissions=%d want 1", calls)
			}
		})
	}
}

// Purpose: requireLandAdmission must preserve durable, job-scoped approval for
// the exact equivalent head, but reject a changed head, revoked approval, or a
// BLOCK. This store-backed boundary proves admission, not Git push execution;
// TestLandRebaseIdentity separately exercises real Git equivalence verification.
func TestTrainReviewAdmission(t *testing.T) {
	for _, mode := range []string{"equivalent", "changed_head", "blocked", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _, cleanup := setupScopedAuthTestServer(t)
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			actor, err := s.identitySessions.ActorForCurrentSelection()
			if err != nil {
				t.Fatal(err)
			}
			ws := s.sessions.Store().WorkerStore()
			coder, err := ws.CreateWorker(actor.AccountScopeID, actor.UserID, store.CreateWorkerRequest{Name: "coder"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			reviewer, err := ws.CreateWorker(actor.AccountScopeID, actor.UserID, store.CreateWorkerRequest{Name: "reviewer", Role: store.WorkerRoleReviewer}, nil)
			if err != nil {
				t.Fatal(err)
			}
			target, err := ws.RecordWorkerRun(actor.AccountScopeID, store.WorkerRunRecord{WorkerID: coder.ID, UserID: actor.UserID, WorkspaceID: "workspace", Status: "succeeded", RequestSource: "direct"})
			if err != nil {
				t.Fatal(err)
			}
			job := controlMCPJobID(controlMCPJobWorker, coder.ID, target.ID)
			r, err := ws.RecordWorkerRun(actor.AccountScopeID, store.WorkerRunRecord{WorkerID: reviewer.ID, Role: store.WorkerRoleReviewer, UserID: actor.UserID, WorkspaceID: "workspace", Status: "succeeded", RequestSource: "direct", Input: map[string]any{"review_job": job}})
			if err != nil {
				t.Fatal(err)
			}
			reviewID := controlMCPJobID(controlMCPJobWorker, reviewer.ID, r.ID)
			approved, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
			record := func(head, verdict string, at int64) {
				t.Helper()
				id := reviewID
				if at > 1 {
					run, err := ws.RecordWorkerRun(actor.AccountScopeID, store.WorkerRunRecord{WorkerID: reviewer.ID, Role: store.WorkerRoleReviewer, UserID: actor.UserID, WorkspaceID: "workspace", Status: "succeeded", RequestSource: "direct", Input: map[string]any{"review_job": job}})
					if err != nil {
						t.Fatal(err)
					}
					id = controlMCPJobID(controlMCPJobWorker, reviewer.ID, run.ID)
				}
				if err := ws.RecordJobReview(actor.AccountScopeID, coder.ID, target.ID, store.JobReview{Head: head, Verdict: verdict, ReviewerJobID: id, RecordedAt: at}); err != nil {
					t.Fatal(err)
				}
			}
			record(approved, "ok", 1)
			folder := t.TempDir()
			rule := publish.Rule{Folder: folder, Mode: "land", RequireReview: true}
			q, err := s.landingQueue()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := q.Enqueue(publish.Entry{Account: actor.AccountScopeID, Request: publish.Request{JobID: job, Folder: folder, Branch: "agent/job", BaseBranch: "dev", Head: head}, Review: &publish.ReviewEquivalence{ReviewID: reviewID, ReviewedHead: approved, RebasedHead: head, PatchID: strings.Repeat("c", 40), PatchIdentical: true}}); err != nil {
				t.Fatal(err)
			}
			if mode == "changed_head" {
				head = strings.Repeat("d", 40)
			}
			if mode == "blocked" {
				record(head, "block", 2)
			}
			if mode == "revoked" {
				record(approved, "block", 2)
			}
			c, err := s.pipelineCall(ctx)
			if err != nil {
				t.Fatal(err)
			}
			err = c.requireLandAdmission(controlMCPJobWorker, coder.ID, target.ID, rule, map[string]any{"head": head, "worktree_branch": "agent/job", "base_branch": "dev"})
			if mode == "equivalent" {
				if err != nil {
					t.Fatalf("equivalent approval rejected: %v", err)
				}
			} else {
				var missing *missingLandReview
				if !errors.As(err, &missing) {
					t.Fatalf("unsafe admission: %v", err)
				}
			}
			entry, _, err := q.Get(actor.AccountScopeID, job)
			if err != nil || entry.State != "queued" || entry.Request.Head != strings.Repeat("b", 40) {
				t.Fatalf("admission mutated queue: %+v err=%v", entry, err)
			}
		})
	}
}
