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

// Purpose: advancePipeline must distinguish reviewer infrastructure failures from
// source findings and stop reviewing landed work. Real review/queue/pipeline
// stores and captured admission routes prove retry timing, limits, and absence
// of coder messages at the narrow orchestration boundary (not provider behavior).
func TestPipelineInfra(t *testing.T) {
	for _, mode := range []string{"failed_review", "landed_head", "landed_delivery", "different_head"} {
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
			target, err := ws.RecordWorkerRun(actor.AccountScopeID, store.WorkerRunRecord{WorkerID: coder.ID, UserID: actor.UserID, WorkspaceID: "workspace", SessionID: "pipeline-infra-session", Status: "succeeded", RequestSource: "direct"})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.sessions.Store().CreateSessionForAccount(store.SessionSnapshot{ID: target.SessionID}, actor.UserID, actor.AccountScopeID); err != nil {
				t.Fatal(err)
			}
			job := controlMCPJobID(controlMCPJobWorker, coder.ID, target.ID)
			head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
			key := pipelineKey(actor.AccountScopeID, job)
			state := ReviewPipeline{Round: 1, Head: base, Next: "review_approved"}
			if err := db.PutJSON(key, state); err != nil {
				t.Fatal(err)
			}
			folder := t.TempDir()
			s.landQueue = publish.NewQueue(db)
			s.signals = store.NewSignalStore(db)
			result := map[string]any{"head": head, "base_commit": base}
			if mode != "failed_review" {
				if _, err := s.landQueue.Enqueue(publish.Entry{Account: actor.AccountScopeID, Request: publish.Request{JobID: job, Folder: folder, Branch: "agent/job", Head: head}}); err != nil {
					t.Fatal(err)
				}
				if err := s.landQueue.Process(ctx, actor.AccountScopeID, folder, publish.QueueHooks{Prepare: func(context.Context, *publish.Entry) error { return nil }, Land: func(context.Context, publish.Entry) (publish.LandResult, error) {
					return publish.LandResult{Commit: head}, nil
				}}); err != nil {
					t.Fatal(err)
				}
				entry, _, err := s.landQueue.Get(actor.AccountScopeID, job)
				if err != nil || entry.State != "landed" {
					t.Fatalf("landing fixture: %+v %v", entry, err)
				}
				if mode != "landed_head" {
					result["head"] = strings.Repeat("c", 40)
				}
				if mode == "landed_delivery" {
					result["delivery_state"] = "landed"
				}
			}
			var ids []string
			s.controlMCPRoutesOnce.Do(func() {})
			s.controlMCPRoutes = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v3/workers":
					writeJSON(w, 200, map[string]any{"workers": []any{map[string]any{"id": reviewer.ID, "name": "reviewer"}}})
				case "/v3/projects":
					writeJSON(w, 200, map[string]any{"projects": []any{map[string]any{"id": "project", "workspaces": []any{map[string]any{"path": folder}}}}})
				case "/v3/workers/" + reviewer.ID + "/direct":
					var body struct {
						Key string `json:"idempotency_key"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					ids = append(ids, body.Key)
					http.Error(w, "admission unavailable", http.StatusForbidden)
				default:
					if !strings.Contains(r.URL.Path, "environment") {
						t.Errorf("unexpected route (including coder messaging): %s", r.URL.Path)
					}
					http.NotFound(w, r)
				}
			})
			c, err := s.pipelineCall(ctx)
			if err != nil {
				t.Fatal(err)
			}
			advance := func() {
				t.Helper()
				ready, err := c.advancePipeline(coder.ID, target.ID, controlMCPJobRefs{sessionID: target.SessionID, projectID: "project"}, publish.Rule{Folder: folder}, result)
				if ready || err != nil {
					t.Fatalf("advance: ready=%v err=%v", ready, err)
				}
			}
			load := func() ReviewPipeline {
				t.Helper()
				var got ReviewPipeline
				if _, err := db.GetJSON(key, &got); err != nil {
					t.Fatal(err)
				}
				return got
			}
			if mode != "failed_review" {
				advance()
				if mode == "different_head" {
					if len(ids) != 1 {
						t.Fatal("different head should still be reviewed")
					}
					return
				}
				if len(ids) != 0 || !reflect.DeepEqual(load(), state) {
					t.Fatalf("landed job advanced: requests=%v state=%+v", ids, load())
				}
				return
			}
			recordFailure := func(n int) {
				t.Helper()
				r, err := ws.RecordWorkerRun(actor.AccountScopeID, store.WorkerRunRecord{WorkerID: reviewer.ID, Role: store.WorkerRoleReviewer, UserID: actor.UserID, WorkspaceID: "workspace", Status: "failed", RequestSource: "direct", Input: map[string]any{"review_job": job}})
				if err != nil {
					t.Fatal(err)
				}
				if err := ws.RecordJobReview(actor.AccountScopeID, coder.ID, target.ID, store.JobReview{Head: head, Verdict: "block", Reason: "reviewer run failed: x", ReviewerJobID: controlMCPJobID(controlMCPJobWorker, reviewer.ID, r.ID), RecordedAt: int64(n)}); err != nil {
					t.Fatal(err)
				}
			}
			recordFailure(1)
			advance()
			state = load()
			if state.Next != "review_failed" || state.Findings != "Review infrastructure failure: reviewer run failed: x" || state.InfrastructureAttempts != 1 || state.Round != 1 || len(ids) != 0 || state.RetryAfter <= time.Now().UnixMilli() {
				t.Fatalf("failed review mishandled: %+v requests=%v", state, ids)
			}
			advance()
			if !reflect.DeepEqual(load(), state) || len(ids) != 0 {
				t.Fatal("retry delay ignored")
			}
			// Admission failures consume the same infrastructure budget, not fix rounds.
			for attempt := 2; attempt <= 3; attempt++ {
				state.RetryAfter = 0
				if err := db.PutJSON(key, state); err != nil {
					t.Fatal(err)
				}
				advance()
				state = load()
				if state.Next != "review_failed" || state.InfrastructureAttempts != attempt || state.Round != 1 || len(ids) != attempt-1 {
					t.Fatalf("retry %d: %+v requests=%v", attempt, state, ids)
				}
				if ids[attempt-2] != fmt.Sprintf("pipeline-review-%s-%s-retry-%d", target.ID, head, attempt-1) {
					t.Fatalf("retry identity: %v", ids)
				}
			}
			recordFailure(4)
			state.RetryAfter = 0
			if err := db.PutJSON(key, state); err != nil {
				t.Fatal(err)
			}
			advance()
			if !reflect.DeepEqual(load(), state) || len(ids) != 2 {
				t.Fatal("fourth failure retried beyond budget")
			}
		})
	}
}
