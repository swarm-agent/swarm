package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: swarm_get_job's error describes the job's latest run even when
// the receipt has no follow-up tracking. With the sandbox off (or Desktop with
// no sandbox backend) the executor never calls ReserveWorkerFollowup, so the
// receipt keeps its first run's error and no followup_status. A run in the
// job's session created after the receipt completed is a follow-up: while it
// runs and after it completed the view shows no error; when it failed it shows
// its own reason. Without such a run the receipt's own error stays.
// Threat (socials machine, 2026-10-30): after a successful follow-up the view
// still returned error "blocked: Narration..." next to status needs_review
// until the daemon restarted.
// Owners: controlMCPCall.jobState and untrackedFollowupRun over the real
// session run state (ApplySessionMutation on a real Pebble store); the worker
// receipt route is stubbed because only its JSON shape is read. This is the
// narrowest layer that joins the receipt with the session's durable run state.
func TestJobViewErrorAfterUntrackedFollowup(t *testing.T) {
	const blocker = "blocked: Narration can't be generated"
	for _, tc := range []struct {
		name            string
		followup        bool
		followupOutcome string
		followupReason  string
		wantError       string
	}{
		{name: "follow-up completed", followup: true, followupOutcome: sessionruntime.RunIntentCompleted},
		{name: "follow-up running", followup: true},
		{name: "follow-up failed", followup: true, followupOutcome: sessionruntime.RunIntentFailed, followupReason: "provider error", wantError: "provider error"},
		{name: "no follow-up keeps the run's own blocker", wantError: blocker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "store"), nil)
			defer f.close()
			p := testPrincipal()
			sid := f.createSession("job")
			apply := func(in sessionruntime.SessionMutationInput) {
				t.Helper()
				in.SessionID, in.AccountScopeID, in.UserID, in.PayloadHash = sid, p.AccountScopeID, p.UserID, in.ClientRequestID
				if _, err := f.sessions.ApplySessionMutation(in); err != nil {
					t.Fatal(err)
				}
			}
			// The job's own run, which ended blocked.
			apply(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationRecordRunIntent, ClientRequestID: "own", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "own", Status: sessionruntime.RunIntentPendingExecutor}})
			apply(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationRecordRunIntent, ClientRequestID: "own-terminal", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "own", Status: sessionruntime.RunIntentFailed, BlockedReason: blocker}})
			own, ok, err := f.sessions.GetSessionRunState(sid)
			if err != nil || !ok || own.RunID != "own" {
				t.Fatalf("own run state: %+v %v %v", own, ok, err)
			}
			completedAt := own.CreatedAt
			if tc.followup {
				// The follow-up is created after the receipt completed.
				deadline := time.Now().Add(time.Second)
				for time.Now().UnixMilli() <= completedAt && time.Now().Before(deadline) {
				}
				apply(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationAppendMessage, ClientRequestID: "followup-1",
					Message: &pebblestore.MessageSnapshot{ID: "msg-followup-1", Role: "user", Content: "fix it"}, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "followup-1", Status: sessionruntime.RunIntentPendingExecutor}})
				if tc.followupOutcome != "" {
					apply(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationRecordRunIntent, ClientRequestID: "followup-1-terminal",
						RunIntent: &pebblestore.V3SessionRunIntent{RunID: "followup-1", Status: tc.followupOutcome, BlockedReason: tc.followupReason}})
				}
				latest, _, _ := f.sessions.GetSessionRunState(sid)
				if latest.RunID != "followup-1" || latest.CreatedAt <= completedAt {
					t.Fatalf("fixture: follow-up not after the receipt: %+v (completed %d)", latest, completedAt)
				}
			}

			sessionRead := false
			handler := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v3/workers/worker-test/runs/run-test":
					// No followup_status: the sandbox never tracked the follow-up.
					writeJSON(rw, 200, map[string]any{"run": map[string]any{"status": "failed", "error": blocker, "session_id": sid, "completed_at": completedAt}})
				case r.URL.Path == "/v3/workers/worker-test":
					writeJSON(rw, 200, map[string]any{"worker": map[string]any{"role": "coder"}})
				case strings.HasPrefix(r.URL.Path, "/v3/sessions/"):
					sessionRead = true
					writeJSON(rw, 200, map[string]any{"session": map[string]any{"id": sid}})
				default:
					http.NotFound(rw, r)
				}
			})
			request := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodGet, "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read", "swarm:read"}})
			call := &controlMCPCall{request: request, next: handler, server: f.server}
			state, _, err := call.jobState(controlMCPJobWorker, "worker-test", "run-test")
			if err != nil {
				t.Fatal(err)
			}
			if !sessionRead {
				t.Fatalf("job view never read the job session: %+v", state)
			}
			if got, _ := state["error"].(string); got != tc.wantError {
				t.Fatalf("view error = %q, want %q (state %+v)", got, tc.wantError, state)
			}
		})
	}
}
