package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: advancePipeline must retry infrastructure admission failures on a
// later completed follow-up, but not on replay or while the worker is active.
// The real pipeline/store plus a rejecting admission route proves a new attempt
// crosses the admission boundary without requiring a provider.
func TestReviewAdmissionFollowupRetry(t *testing.T) {
	for _, tc := range []struct {
		name, status, previous string
		want                   bool
	}{
		{"new completed", "completed", "", true},
		{"running", "running", "", false},
		{"same completed replay", "completed", "followup-new", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, cleanup := setupScopedAuthTestServer(t)
			defer cleanup()
			actor, err := s.identitySessions.ActorForCurrentSelection()
			if err != nil {
				t.Fatal(err)
			}
			if err := s.sessions.Store().CreateSessionForAccount(store.SessionSnapshot{ID: "retry-review-session"}, actor.UserID, actor.AccountScopeID); err != nil {
				t.Fatal(err)
			}
			db := s.sessions.Store().Underlying()
			s.signals = store.NewSignalStore(db)
			head := strings.Repeat("a", 40)
			if err := db.PutJSON(pipelineKey(actor.AccountScopeID, "worker/coder/run"), ReviewPipeline{Head: head, Next: "review_failed", Findings: "admission unavailable", FollowupRunID: tc.previous}); err != nil {
				t.Fatal(err)
			}
			attempts := 0
			s.controlMCPRoutesOnce.Do(func() {})
			s.controlMCPRoutes = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { attempts++; http.Error(w, "admission unavailable", 503) })
			c, err := s.pipelineCall(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.advancePipeline("coder", "run", controlMCPJobRefs{sessionID: "retry-review-session", run: map[string]any{"followup_status": tc.status, "followup_run_id": "followup-new"}}, publish.Rule{}, map[string]any{"head": head})
			if tc.want && (err == nil || attempts == 0) {
				t.Fatalf("follow-up did not retry admission: attempts=%d err=%v", attempts, err)
			}
			if !tc.want && (err != nil || attempts != 0) {
				t.Fatalf("active follow-up retried: attempts=%d err=%v", attempts, err)
			}
		})
	}
}
