package pebblestore

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Purpose: ApplyV3SessionMutation owns wait registration and atomic wake claims.
// These store tests prove durability, ownership rejection and no partial writes
// at the narrowest transaction boundary, including publication/registration races.
func taskWaitFixture(t *testing.T, s *SessionStore) {
	t.Helper()
	if err := s.PutProject("account", &ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"parent", "child"} {
		metadata := map[string]any{"project_id": "project", "task_id": "task"}
		if id == "parent" {
			metadata = map[string]any{"swarm_v3_project_id": "project", "project_id": "project", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator"}
		}
		_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: id, UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationCreateSession, ClientRequestID: "create", PayloadHash: "create", Session: &SessionSnapshot{ID: id, Metadata: metadata}, NowUnixMs: 10})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutProjectTask("account", &ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Audit", Agent: "finder", Status: "in_progress", SessionID: "child"}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{V3RunIntentPendingExecutor, V3RunIntentRunning} {
		_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: status, PayloadHash: status, RunIntent: &V3SessionRunIntent{RunID: "goal", Status: status}, NowUnixMs: 20})
		if err != nil {
			t.Fatal(err)
		}
	}
}
func registerTaskWait(s *SessionStore, account, user, project string, ids ...string) error {
	_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: user, AccountScopeID: account, Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "wait", PayloadHash: project + strings.Join(ids, ","), TaskWait: &V3ProjectTaskWaitMutation{RunID: "goal", ProjectID: project, TaskIDs: ids}, NowUnixMs: 30})
	return err
}
func taskWaitStatus(t *testing.T, s *SessionStore, status string) {
	t.Helper()
	_, err := s.UpdateProjectTask("account", "project", "task", func(task *ProjectTaskRecord) error {
		task.Status = status
		task.LastError = "bounded outcome"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func assertTaskWaitWake(t *testing.T, s *SessionStore, want bool) {
	t.Helper()
	intent, ok, err := s.GetV3SessionRunIntent("parent", ProjectTaskWaitResumeID("goal"))
	if err != nil || ok != want {
		t.Fatalf("wake found=%v want=%v error=%v", ok, want, err)
	}
	if want && intent.Status != V3RunIntentPendingExecutor {
		t.Fatalf("wake=%+v", intent)
	}
	messages, err := s.ListV3SessionMessages("parent", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if want && (len(messages) != 1 || !strings.Contains(messages[0].Content, `"task_id":"task"`) || !strings.Contains(messages[0].Content, `"attempt_id":"initial"`)) {
		t.Fatalf("messages=%+v", messages)
	}
	if !want && len(messages) != 0 {
		t.Fatalf("unexpected messages=%+v", messages)
	}
}

// Purpose: no time-only reconciliation can create a provider intent; actionable
// outcomes wake exactly once under concurrency, and needs_review is not completed.
func TestProjectTaskWaitOutcomesAndNoTimeOnlyWake(t *testing.T) {
	for _, status := range []string{"in_progress", "planning", "needs_review", "completed", "failed", "blocked", "needs_input", "cancelled", "pending_approval"} {
		t.Run(status, func(t *testing.T) {
			s := NewSessionStore(openV3SessionEventTestStore(t))
			taskWaitFixture(t, s)
			if err := registerTaskWait(s, "account", "user", "project", "task"); err != nil {
				t.Fatal(err)
			}
			state, _, _ := s.GetV3SessionRunState("parent")
			if state.Active || state.Status != V3RunIntentWaitingTasks || state.CompletedAt != 0 {
				t.Fatalf("waiting projection=%+v", state)
			}
			for i := 0; i < 3; i++ {
				if err := s.ReconcileProjectTaskWaits("", "", nil); err != nil {
					t.Fatal(err)
				}
			}
			assertTaskWaitWake(t, s, false)
			taskWaitStatus(t, s, status)
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := s.ReconcileProjectTaskWaits("account", "project", nil); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			want := status != "in_progress" && status != "planning"
			assertTaskWaitWake(t, s, want)
			if want {
				messages, _ := s.ListV3SessionMessages("parent", 0, 20)
				if !strings.Contains(messages[0].Content, fmt.Sprintf(`"status":%q`, status)) {
					t.Fatal(messages)
				}
			}
		})
	}
}

// Purpose: completion before/during registration and restart cannot lose wakeups.
// Reopening Pebble, rather than an in-memory notification mock, proves persistence.
func TestProjectTaskWaitRegistrationRaceAndRestart(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(fmt.Sprint(before), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wait.pebble")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			s := NewSessionStore(db)
			taskWaitFixture(t, s)
			if before {
				taskWaitStatus(t, s, "needs_review")
			}
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				if err := registerTaskWait(s, "account", "user", "project", "task"); err != nil {
					t.Error(err)
				}
			}()
			go func() {
				defer wg.Done()
				if !before {
					_, err := s.UpdateProjectTask("account", "project", "task", func(task *ProjectTaskRecord) error { task.Status = "needs_review"; return nil })
					if err != nil {
						t.Error(err)
					}
				}
			}()
			wg.Wait()
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s = NewSessionStore(db)
			if err := s.ReconcileProjectTaskWaits("", "", nil); err != nil {
				t.Fatal(err)
			}
			assertTaskWaitWake(t, s, true)
			if err := s.ReconcileProjectTaskWaits("", "", nil); err != nil {
				t.Fatal(err)
			}
			assertTaskWaitWake(t, s, true)
		})
	}
}

// Purpose: reject cross-account/user/project, unrelated and undeployed waits
// before mutating parent state; cancellation and user supersession retire waits.
func TestProjectTaskWaitOwnershipAndSupersession(t *testing.T) {
	for _, wrong := range []string{"account", "user", "project", "task"} {
		t.Run(wrong, func(t *testing.T) {
			s := NewSessionStore(openV3SessionEventTestStore(t))
			taskWaitFixture(t, s)
			a, u, p, id := "account", "user", "project", "task"
			switch wrong {
			case "account":
				a = "foreign"
			case "user":
				u = "foreign"
			case "project":
				p = "foreign"
			case "task":
				id = "foreign"
			}
			if err := registerTaskWait(s, a, u, p, id); err == nil {
				t.Fatal("unauthorized wait accepted")
			}
			state, _, _ := s.GetV3SessionRunState("parent")
			if state.Status != V3RunIntentRunning {
				t.Fatal(state)
			}
			assertTaskWaitWake(t, s, false)
		})
	}
	for _, action := range []string{"cancel", "message"} {
		t.Run(action, func(t *testing.T) {
			s := NewSessionStore(openV3SessionEventTestStore(t))
			taskWaitFixture(t, s)
			if err := registerTaskWait(s, "account", "user", "project", "task"); err != nil {
				t.Fatal(err)
			}
			input := V3SessionMutationInput{SessionID: "parent", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: action, PayloadHash: action, RunIntent: &V3SessionRunIntent{RunID: "goal", Status: V3RunIntentCancelled}, NowUnixMs: 40}
			if action == "message" {
				input.Kind = V3SessionMutationAppendMessage
				input.RunIntent = nil
				input.Message = &MessageSnapshot{ID: "new-goal", Role: "user", Content: "Supersede the old goal"}
			}
			if _, err := s.ApplyV3SessionMutation(input); err != nil {
				t.Fatal(err)
			}
			taskWaitStatus(t, s, "completed")
			if err := s.ReconcileProjectTaskWaits("", "", nil); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := s.GetV3SessionRunIntent("parent", ProjectTaskWaitResumeID("goal")); err != nil || ok {
				t.Fatalf("stale wake=%v %v", ok, err)
			}
			owner, _, _ := s.GetV3SessionRunIntent("parent", "goal")
			if owner.Status != V3RunIntentCancelled {
				t.Fatal(owner)
			}
		})
	}
}

// Purpose: archive restoration must never resurrect a wait; a user message racing
// an already-claimed queued wake cancels that wake atomically with the new goal.
func TestProjectTaskWaitArchiveAndQueuedWakeSupersession(t *testing.T) {
	for _, action := range []string{"archive", "queued_message"} {
		t.Run(action, func(t *testing.T) {
			s := NewSessionStore(openV3SessionEventTestStore(t))
			taskWaitFixture(t, s)
			if err := registerTaskWait(s, "account", "user", "project", "task"); err != nil {
				t.Fatal(err)
			}
			if action == "archive" {
				if err := s.ArchiveSessions([]string{"parent"}); err != nil {
					t.Fatal(err)
				}
				taskWaitStatus(t, s, "needs_review")
				if err := s.ReconcileProjectTaskWaits("", "", nil); err != nil {
					t.Fatal(err)
				}
				owner, _, _ := s.GetV3SessionRunIntent("parent", "goal")
				if owner.Status != V3RunIntentCancelled {
					t.Fatal(owner)
				}
				assertTaskWaitWake(t, s, false)
			} else {
				taskWaitStatus(t, s, "needs_review")
				if err := s.ReconcileProjectTaskWaits("", "", nil); err != nil {
					t.Fatal(err)
				}
				_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationAppendMessage, ClientRequestID: "new-goal", PayloadHash: "new-goal", Message: &MessageSnapshot{Role: "user", Content: "Do something else"}, RunIntent: &V3SessionRunIntent{RunID: "new-goal", Status: V3RunIntentPendingExecutor}})
				if err != nil {
					t.Fatal(err)
				}
				wake, _, _ := s.GetV3SessionRunIntent("parent", ProjectTaskWaitResumeID("goal"))
				if wake.Status != V3RunIntentCancelled {
					t.Fatal(wake)
				}
				if err := s.ReconcileProjectTaskWaits("", "", nil); err != nil {
					t.Fatal(err)
				}
				state, _, _ := s.GetV3SessionRunState("parent")
				if state.RunID != "new-goal" {
					t.Fatal(state)
				}
			}
		})
	}
}

// Purpose: a failed atomic wake commit must retain the wait and leave no message
// or pending intent; retry then creates precisely one usable continuation.
func TestProjectTaskWaitAtomicFailure(t *testing.T) {
	db := openV3SessionEventTestStore(t)
	s := NewSessionStore(db)
	taskWaitFixture(t, s)
	if err := registerTaskWait(s, "account", "user", "project", "task"); err != nil {
		t.Fatal(err)
	}
	taskWaitStatus(t, s, "failed")
	db.sessionMutations.beforeTaskWaitCommit = func(string) error { return fmt.Errorf("injected commit failure") }
	if err := s.ReconcileProjectTaskWaits("", "", nil); err == nil {
		t.Fatal("expected failure")
	}
	db.sessionMutations.beforeTaskWaitCommit = nil
	assertTaskWaitWake(t, s, false)
	state, _, _ := s.GetV3SessionRunState("parent")
	if state.Status != V3RunIntentWaitingTasks {
		t.Fatal(state)
	}
	if err := s.ReconcileProjectTaskWaits("", "", nil); err != nil {
		t.Fatal(err)
	}
	assertTaskWaitWake(t, s, true)
}

// Purpose: selected attempts, not mutable task IDs, define eligibility. A blocker
// wakes an all-of wait early while nonterminal siblings stay explicitly in progress.
func TestProjectTaskWaitMultipleAndAttemptChange(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	taskWaitFixture(t, s)
	task, _, _ := s.GetProjectTask("account", "project", "task")
	task.ID = "second"
	task.SessionID = "second-child"
	task.Attempts = nil
	task.ActiveAttemptID = ""
	if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "second-child", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationCreateSession, ClientRequestID: "create", PayloadHash: "create", Session: &SessionSnapshot{ID: "second-child", Metadata: map[string]any{"project_id": "project", "task_id": "second"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProjectTask("account", task); err != nil {
		t.Fatal(err)
	}
	if err := registerTaskWait(s, "account", "user", "project", "task", "second"); err != nil {
		t.Fatal(err)
	}
	taskWaitStatus(t, s, "needs_review")
	if err := s.ReconcileProjectTaskWaits("", "", nil); err != nil {
		t.Fatal(err)
	}
	assertTaskWaitWake(t, s, false)
	_, err := s.UpdateProjectTask("account", "project", "second", func(task *ProjectTaskRecord) error {
		task.ActiveAttemptID = "replacement"
		task.Attempts = append(task.Attempts, ProjectTaskAttempt{ID: "replacement", SessionID: "second-child", Role: "finder", Status: "in_progress"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileProjectTaskWaits("", "", nil); err != nil {
		t.Fatal(err)
	}
	assertTaskWaitWake(t, s, true)
	messages, _ := s.ListV3SessionMessages("parent", 0, 20)
	if !strings.Contains(messages[0].Content, `"status":"superseded"`) {
		t.Fatal(messages)
	}
}

// Purpose: generic run-intent writes cannot forge a wait or bypass its ownership
// checks. Undeployed cards remain approval-gated and cannot be used as wait targets.
func TestProjectTaskWaitRejectForgedAndUndeployed(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	taskWaitFixture(t, s)
	_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "forged", PayloadHash: "forged", RunIntent: &V3SessionRunIntent{RunID: "goal", Status: V3RunIntentWaitingTasks, TaskWait: &V3ProjectTaskWait{ProjectID: "foreign"}}})
	if err == nil {
		t.Fatal("forged wait accepted")
	}
	if err := s.PutProjectTask("account", &ProjectTaskRecord{ID: "unapproved", ProjectID: "project", Title: "Needs approval", Agent: "finder", Status: "pending_approval"}); err != nil {
		t.Fatal(err)
	}
	if err := registerTaskWait(s, "account", "user", "project", "unapproved"); err == nil {
		t.Fatal("undeployed task accepted")
	}
	state, _, _ := s.GetV3SessionRunState("parent")
	if state.Status != V3RunIntentRunning {
		t.Fatal(state)
	}
	task, _, _ := s.GetProjectTask("account", "project", "unapproved")
	if task.Status != "pending_approval" || task.SessionID != "" {
		t.Fatal(task)
	}
	assertTaskWaitWake(t, s, false)
}
