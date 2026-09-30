package pebblestore

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Purpose: completedTaskRunSummary and hydrateTaskAttemptOutcome must use only
// the exact completed run's final event. Threat: running commentary, failed or
// cancelled turns, mismatched message provenance, and cross-account reads can
// masquerade as successful outcomes. Canonical mutation + task read is the
// narrow hermetic durable boundary; no provider or lifecycle summary is seeded.
func TestTaskAutoOutcomeTerminalProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, status, messageRun string
		want                     bool
	}{
		{"completed", V3RunIntentCompleted, "run", true},
		{"running", V3RunIntentRunning, "run", false},
		{"failed", V3RunIntentFailed, "run", false},
		{"cancelled", V3RunIntentCancelled, "run", false},
		{"stale-message", V3RunIntentCompleted, "previous", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s := NewSessionStore(db)
			session := SessionSnapshot{ID: "session", UserID: "user", AccountScopeID: "account", Mode: "auto"}
			apply := func(key, kind, event string, snapshot *SessionSnapshot, run *V3SessionRunIntent, message *MessageSnapshot) {
				t.Helper()
				if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: kind, EventType: event, Session: snapshot, RunIntent: run, Message: message}); err != nil {
					t.Fatal(err)
				}
			}
			apply("create", V3SessionMutationCreateSession, "", &session, nil, nil)
			run := V3SessionRunIntent{SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, RunID: "run", Status: V3RunIntentPendingExecutor}
			apply("pending", V3SessionMutationRecordRunIntent, "", nil, &run, nil)
			run.Status = tc.status
			message := MessageSnapshot{ID: "final", Role: "assistant", Content: strings.Repeat("界", 2000), Metadata: map[string]any{"run_id": tc.messageRun}}
			apply("terminal", V3SessionMutationAppendMessage, "session.assistant.completed", nil, &run, &message)
			task := &ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Task", Agent: "swarm", Status: "in_progress", SessionID: session.ID}
			if err := s.PutProjectTask("account", task); err != nil {
				t.Fatal(err)
			}
			stored, found, err := s.GetProjectTask("account", "project", "task")
			if err != nil || !found {
				t.Fatal(err)
			}
			a := stored.ActiveAttempt()
			if tc.want {
				if a.SummaryRunID != "run" || len(a.Summary) > 4000 || !utf8.ValidString(a.Summary) || !strings.HasSuffix(a.Summary, " [truncated; retrieve completed run]") {
					t.Fatalf("invalid bounded outcome: %+v", a)
				}
			} else if a.Summary != "" || a.SummaryRunID != "" {
				t.Fatalf("non-final outcome exposed: %+v", a)
			}
			if _, found, err := s.GetProjectTask("other-account", "project", "task"); err != nil || found {
				t.Fatal("cross-account outcome exposed")
			}
			if tc.status == V3RunIntentRunning {
				return
			}
			// A new running run cannot inherit the prior final event or summary.
			run.RunID, run.Status = "next", V3RunIntentPendingExecutor
			apply("next", V3SessionMutationRecordRunIntent, "", nil, &run, nil)
			stored, _, err = s.GetProjectTask("account", "project", "task")
			if err != nil || stored.ActiveAttempt().Summary != "" || stored.ActiveAttempt().SummaryRunID != "" {
				t.Fatal("previous run summary leaked into new run")
			}
		})
	}
}
