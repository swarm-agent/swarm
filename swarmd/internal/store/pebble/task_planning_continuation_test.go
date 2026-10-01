package pebblestore

import (
	"path/filepath"
	"reflect"
	"testing"
)

// Purpose: ApplyV3SessionMutation is the narrowest atomic boundary for chat,
// run admission and task ownership. Rejected stale/foreign/terminal resumes must
// leave the task, run and event history unchanged; valid resumes retain the
// original session, attempt, workspace and feedback and are idempotent.
func TestTaskPlanningChatContinuation(t *testing.T) {
	for _, scenario := range []string{"paused", "failed", "quota", "stale", "session", "attempt", "account", "user", "terminal", "archived"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sessions.pebble")
			store, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			sessions := NewSessionStore(store)
			current := SessionSnapshot{ID: "planning", Mode: "plan", WorkspacePath: t.TempDir(), Metadata: map[string]any{"project_id": "project", "task_id": "task", "task_attempt_id": "initial"}}
			if scenario == "attempt" {
				current.Metadata["task_attempt_id"] = "other"
			}
			apply := func(key, kind string, session *SessionSnapshot, message *MessageSnapshot, intent *V3SessionRunIntent) (V3SessionMutationResult, error) {
				return sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: current.ID, UserID: "user", AccountScopeID: "account", IdempotencyKey: key, RequestHash: key, Kind: kind, Session: session, Message: message, RunIntent: intent})
			}
			if _, err := apply("create", V3SessionMutationCreateSession, &current, nil, nil); err != nil {
				t.Fatal(err)
			}
			task := &ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account", Title: "Plan", Agent: "plan", Status: "planning", SessionID: current.ID, WorkspacePath: current.WorkspacePath}
			if err := sessions.PutProjectTask("account", task); err != nil {
				t.Fatal(err)
			}
			oldRun := task.ExecutionRunID()
			if _, err := apply("initial", V3SessionMutationRecordRunIntent, nil, nil, &V3SessionRunIntent{RunID: oldRun, Status: V3RunIntentPendingExecutor}); err != nil {
				t.Fatal(err)
			}
			status, reason := V3RunIntentCancelled, V3RunStoppedByUser
			if scenario == "failed" {
				status, reason = V3RunIntentFailed, "provider error"
			}
			if scenario == "quota" {
				reason = "usage limit exceeded"
			}
			if _, err := apply("pause", V3SessionMutationRecordRunIntent, nil, nil, &V3SessionRunIntent{RunID: oldRun, Status: status, BlockedReason: reason}); err != nil {
				t.Fatal(err)
			}
			if _, err := sessions.UpdateProjectTask("account", "project", "task", func(task *ProjectTaskRecord) error {
				switch scenario {
				case "stale":
					task.EnsureTaskAttempts()
					task.ActiveAttempt().RunID = "other-run"
				case "session":
					task.SessionID = "other-session"
				case "terminal":
					task.Status = "failed"
				case "archived":
					task.Archived = true
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			before, _, _ := sessions.GetProjectTask("account", "project", "task")
			eventsBefore, err := sessions.ListV3SessionEvents(current.ID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			// Reopen Pebble: continuation must survive losing all runtime state.
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			sessions = NewSessionStore(store)
			input := V3SessionMutationInput{SessionID: current.ID, UserID: "user", AccountScopeID: "account", IdempotencyKey: "resume", RequestHash: "resume", Kind: V3SessionMutationAppendMessage, Message: &MessageSnapshot{Role: "user", Content: "Retain previous requirements; add keyboard support."}, RunIntent: &V3SessionRunIntent{RunID: "resumed", Status: V3RunIntentPendingExecutor}}
			if scenario == "account" {
				input.AccountScopeID = "foreign"
			}
			if scenario == "user" {
				input.UserID = "foreign-user"
			}
			result, err := sessions.ApplyV3SessionMutation(input)
			after, _, _ := sessions.GetProjectTask("account", "project", "task")
			if scenario != "paused" {
				if err == nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("unauthorized mutation: err=%v before=%+v after=%+v", err, before, after)
				}
				if _, found, _ := sessions.GetV3SessionRunIntent(current.ID, "resumed"); found {
					t.Fatal("rejected resume left a run")
				}
				eventsAfter, _ := sessions.ListV3SessionEvents(current.ID, 0, 100)
				if !reflect.DeepEqual(eventsBefore, eventsAfter) {
					t.Fatal("rejected resume changed session history")
				}
				return
			}
			if err != nil || result.Message == nil || result.Message.Content != input.Message.Content || after.ExecutionRunID() != "resumed" || after.SessionID != current.ID || after.ActiveAttemptID != "initial" || len(after.Attempts) != 1 || after.WorkspacePath != before.WorkspacePath {
				t.Fatalf("continuation: result=%+v task=%+v err=%v", result, after, err)
			}
			replay, err := sessions.ApplyV3SessionMutation(input)
			replayedTask, _, _ := sessions.GetProjectTask("account", "project", "task")
			if err != nil || !replay.Replayed || !reflect.DeepEqual(after, replayedTask) {
				t.Fatalf("resume replay changed ownership: %+v %v", replayedTask, err)
			}
		})
	}
}
