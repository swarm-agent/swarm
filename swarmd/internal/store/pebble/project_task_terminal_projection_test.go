package pebblestore

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cockroachdb/pebble"
)

// Purpose: canonical V3 mutations must publish an exact bounded terminal summary
// before any subsequent task write. ReadProjectTaskBoard must not decode large
// final messages, leak stale/foreign outcomes, or repeatedly migrate negatives.
// This store integration layer exercises atomic publication and restart repair
// without providers, and asserts data and read budgets rather than timing.
func TestTaskBoardTerminalProjection(t *testing.T) {
	for _, tc := range []struct {
		name, event, role, messageRun, content string
		want                                   bool
	}{
		{"unicode", "session.assistant.completed", "assistant", "run", strings.Repeat("界", 400000), true},
		{"wrong-event", "session.assistant.failed", "assistant", "run", "not final", false},
		{"wrong-role", "session.assistant.completed", "user", "run", "not assistant", false},
		{"wrong-run", "session.assistant.completed", "assistant", "old", "stale", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir()
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db.Close() }()
			s := NewSessionStore(db)
			session := SessionSnapshot{ID: "session", UserID: "user", AccountScopeID: "account", Mode: "auto"}
			apply := func(key, kind, event string, snapshot *SessionSnapshot, run *V3SessionRunIntent, message *MessageSnapshot) {
				t.Helper()
				_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: kind, EventType: event, Session: snapshot, RunIntent: run, Message: message})
				if err != nil {
					t.Fatal(err)
				}
			}
			apply("create", V3SessionMutationCreateSession, "", &session, nil, nil)
			run := V3SessionRunIntent{SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, RunID: "run", Status: V3RunIntentPendingExecutor}
			apply("pending", V3SessionMutationRecordRunIntent, "", nil, &run, nil)
			task := &ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Task", Agent: "swarm", Status: "in_progress", SessionID: session.ID}
			if err := s.PutProjectTask("account", task); err != nil {
				t.Fatal(err)
			}
			if _, err := s.BackfillProjectTaskSummaries("account", "project"); err != nil {
				t.Fatal(err)
			}
			run.Status = V3RunIntentCompleted
			message := MessageSnapshot{ID: "final", Role: tc.role, Content: tc.content, Metadata: map[string]any{"run_id": tc.messageRun}}
			apply("terminal", V3SessionMutationAppendMessage, tc.event, nil, &run, &message)
			state, ok, err := s.GetV3SessionRunState(session.ID)
			if err != nil || !ok {
				t.Fatalf("state: %v %v", ok, err)
			}
			key := taskRelatedKey(KeyV3SessionEvent(session.ID, state.EventSeq))
			check := func() {
				t.Helper()
				rows, stats, err := s.ReadProjectTaskBoard("account", "project", false, func([]ProjectTaskRecord, *ProjectTaskBoardReader) {})
				if err != nil || len(rows) != 1 {
					t.Fatalf("board: %v %v", rows, err)
				}
				a := rows[0].ActiveAttempt()
				if a == nil {
					t.Fatal("missing attempt")
				}
				if tc.want {
					if a.SummaryRunID != "run" || len(a.Summary) > 4000 || !utf8.ValidString(a.Summary) || !strings.HasSuffix(a.Summary, " [truncated; retrieve completed run]") {
						t.Fatalf("summary: %+v", a)
					}
				} else if a.Summary != "" {
					t.Fatalf("invalid summary: %+v", a)
				}
				if stats.BackfillRows != 0 || stats.RelatedDecodedBytes > 16000 {
					t.Fatalf("warm amplification: %+v", stats)
				}
			}
			check()
			if tc.want {
				owned, found, err := s.GetSession(session.ID)
				if err != nil || !found {
					t.Fatal("missing session", err)
				}
				if owned.Metadata == nil {
					owned.Metadata = make(map[string]any)
				}
				owned.Metadata["lifecycle_summary_run_id"] = "run"
				owned.Metadata["lifecycle_signal"] = "needs_review"
				owned.Metadata["lifecycle_summary"] = "lower precedence"
				if err := s.UpdateSession(owned); err != nil {
					t.Fatal(err)
				}
				check()
			}
			// ApplyV3SessionMutation is an internal trusted mutation surface, not
			// principal authorization. Cross-account HTTP rejection is asserted by
			// TestProjectDeliverableContentLazyAccountBound; here prove that the
			// compact reader rejects foreign/stale provenance without mutation.
			before, _, err := db.GetBytes(key)
			if err != nil {
				t.Fatal(err)
			}
			for _, change := range []func(*V3SessionRunState){
				func(v *V3SessionRunState) { v.AccountScopeID = "other" },
				func(v *V3SessionRunState) { v.SessionID = "other" },
				func(v *V3SessionRunState) { v.RunID = "old" },
				func(v *V3SessionRunState) { v.EventSeq-- },
				func(v *V3SessionRunState) { v.Active = true },
			} {
				candidate := state
				change(&candidate)
				r := newProjectTaskBoardReader(db.db, "account", nil, &ProjectTaskReadStats{})
				if summary, ok := r.completedRunSummary(candidate); ok || summary != "" {
					t.Fatal("foreign/stale summary exposed")
				}
			}
			after, _, err := db.GetBytes(key)
			if err != nil || string(before) != string(after) {
				t.Fatal("rejected reads changed terminal projection", err)
			}
			// Simulate a pre-projection database. Each repaired key commits before
			// returning retry; reopening must retain positives and negatives alike.
			if err := db.db.Delete([]byte(key), pebble.Sync); err != nil {
				t.Fatal(err)
			}
			_, stats, err := s.ReadProjectTaskBoard("account", "project", false, nil)
			if !errors.Is(err, ErrProjectTaskSummariesNotReady) || stats.BackfillRows != 1 {
				t.Fatalf("repair: %+v %v", stats, err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			s = NewSessionStore(db)
			check()
			rows, _, err := s.ReadProjectTaskBoard("other", "project", false, nil)
			if err != nil || len(rows) != 0 {
				t.Fatal("foreign board exposed", err)
			}
			// Deletion must not leave even negative terminal projections behind.
			batch := db.db.NewBatch()
			if err := deleteTaskRelatedSessionInBatch(batch, session.ID, true); err != nil {
				t.Fatal(err)
			}
			if err := batch.Commit(pebble.Sync); err != nil {
				t.Fatal(err)
			}
			batch.Close()
			if _, closer, err := db.db.Get([]byte(key)); !errors.Is(err, pebble.ErrNotFound) {
				if closer != nil {
					closer.Close()
				}
				t.Fatal("terminal projection survived deletion", err)
			}
		})
	}
}
