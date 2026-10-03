package pebblestore

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Purpose: ApplyV3SessionMutation/ReconcileProjectTaskWaits must serialize task
// wake claims against new user intent and archive. Both legal commit orderings
// are exercised concurrently; no late retry may revive the retired generation.
// Real store transactions are the narrowest layer proving durable postconditions.
func TestProjectTaskDeliveryWakeLifecycleRace(t *testing.T) {
	for _, action := range []string{"message", "archive"} {
		for iteration := 0; iteration < 6; iteration++ {
			t.Run(fmt.Sprintf("%s/%d", action, iteration), func(t *testing.T) {
				s := taskReportFixture(t)
				if err := registerTaskWait(s, "account", "user", "project", "task"); err != nil {
					t.Fatal(err)
				}
				if _, err := s.ApplyV3SessionMutation(taskReportInput(ProjectTaskUpdateWakeRequest)); err != nil {
					t.Fatal(err)
				}
				start, results := make(chan struct{}), make(chan error, 2)
				go func() { <-start; results <- s.ReconcileProjectTaskWaits("account", "project", nil) }()
				go func() {
					<-start
					if action == "archive" {
						results <- s.ArchiveSessions([]string{"parent"})
						return
					}
					_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationAppendMessage, ClientRequestID: "new-goal", PayloadHash: "new-goal", Message: &MessageSnapshot{ID: "new-goal", Role: "user", Content: "A different objective"}, RunIntent: &V3SessionRunIntent{RunID: "new-goal", Status: V3RunIntentPendingExecutor}})
					results <- err
				}()
				close(start)
				var raceErr error
				for i := 0; i < 2; i++ {
					if err := <-results; err != nil {
						raceErr = err
					}
				}
				if raceErr != nil {
					if action != "archive" || !strings.Contains(raceErr.Error(), "project session has active work") {
						t.Fatal(raceErr)
					}
					// Wake won: archive must fail closed, not partially archive an active continuation.
					assertTaskWaitWake(t, s, true)
					tomb, found, err := s.GetV3SessionTombstone("parent")
					if err != nil || (found && tomb.Archived) {
						t.Fatal("rejected archive changed tombstone", err)
					}
					return
				}
				if err := s.ReconcileProjectTaskWaits("", "", nil); err != nil {
					t.Fatal(err)
				}
				owner, _, err := s.GetV3SessionRunIntent("parent", "goal")
				if err != nil || (owner.Status != V3RunIntentCancelled && owner.Status != V3RunIntentCompleted) {
					t.Fatalf("owner=%+v %v", owner, err)
				}
				wake, found, err := s.GetV3SessionRunIntent("parent", ProjectTaskWaitResumeID("goal"))
				if err != nil || (found && wake.Status != V3RunIntentCancelled) {
					t.Fatalf("late wake=%+v found=%v %v", wake, found, err)
				}
				before, _ := s.readV3SessionSequence("parent")
				if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "stale-wake", PayloadHash: "stale-wake", TaskWait: &V3ProjectTaskWaitMutation{RunID: "goal", Wake: true}}); err == nil {
					t.Fatal("retired wait accepted another wake")
				}
				after, _ := s.readV3SessionSequence("parent")
				if before != after {
					t.Fatal("rejected wake changed parent")
				}
				if action == "message" {
					state, _, _ := s.GetV3SessionRunState("parent")
					if state.RunID != "new-goal" {
						t.Fatalf("overwrote user goal: %+v", state)
					}
					taskDeliveryClaim(t, s, "new-goal")
					if len(pendingTaskUpdates(t, s, "new-goal")) != 0 {
						t.Fatal("old report crossed user fence")
					}
				}
			})
		}
	}
}

// Purpose: a provider response from the original waiter can acknowledge only
// before the exact continuation starts. projectTaskReceiptOwner must reject the
// late response without deleting data that the successor still needs to consume.
func TestProjectTaskDeliveryReceiptAfterSuccessorStarts(t *testing.T) {
	s := taskReportFixture(t)
	if _, err := s.ApplyV3SessionMutation(taskReportInput(ProjectTaskUpdateAttention)); err != nil {
		t.Fatal(err)
	}
	updates := pendingTaskUpdates(t, s, "goal")
	if err := registerTaskWait(s, "account", "user", "project", "task"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileProjectTaskWaits("account", "project", nil); err != nil {
		t.Fatal(err)
	}
	run := ProjectTaskWaitResumeID("goal")
	taskDeliveryClaim(t, s, run)
	before, _ := s.readV3SessionSequence("parent")
	if _, err := s.ApplyV3SessionMutation(taskDeliveryInput("goal", updates)); err == nil {
		t.Fatal("previous provider consumed successor input")
	}
	after, _ := s.readV3SessionSequence("parent")
	if before != after || len(pendingTaskUpdates(t, s, run)) != 1 {
		t.Fatal("late receipt changed successor state")
	}
	if _, err := s.ApplyV3SessionMutation(taskDeliveryInput(run, updates)); err != nil {
		t.Fatal(err)
	}
	if len(pendingTaskUpdates(t, s, run)) != 0 {
		t.Fatal("successor did not consume update")
	}
}

// Purpose: the all-ready policy must wait for every selected attempt, but wake
// early for blockers or attempt replacement. Inspect the canonical result bytes
// and task postconditions, including bounded readable titles used by Desktop.
func TestProjectTaskDeliveryMultiTaskPolicy(t *testing.T) {
	for _, outcome := range []string{"completed", "blocked", "replacement"} {
		t.Run(outcome, func(t *testing.T) {
			s := taskReportFixture(t)
			_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "second-child", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationCreateSession, ClientRequestID: "create", PayloadHash: "create", Session: &SessionSnapshot{ID: "second-child", Metadata: map[string]any{"project_id": "project", "task_id": "second"}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.PutProjectTask("account", &ProjectTaskRecord{ID: "second", ProjectID: "project", Title: strings.Repeat("界", 250), Agent: "finder", SessionID: "second-child", Status: "in_progress"}); err != nil {
				t.Fatal(err)
			}
			if err := registerTaskWait(s, "account", "user", "project", "task", "second"); err != nil {
				t.Fatal(err)
			}
			if outcome == "completed" {
				taskWaitStatus(t, s, "needs_review")
			}
			if err := s.ReconcileProjectTaskWaits("account", "project", nil); err != nil {
				t.Fatal(err)
			}
			assertTaskWaitWake(t, s, false)
			_, err = s.UpdateProjectTask("account", "project", "second", func(task *ProjectTaskRecord) error {
				if outcome == "replacement" {
					task.ActiveAttemptID = "replacement"
					task.Attempts = append(task.Attempts, ProjectTaskAttempt{ID: "replacement", SessionID: "second-child", Role: "finder", Status: "in_progress"})
				} else {
					task.Status = outcome
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.ReconcileProjectTaskWaits("account", "project", nil); err != nil {
				t.Fatal(err)
			}
			assertTaskWaitWake(t, s, true)
			messages, err := s.ListV3SessionMessages("parent", 0, 20)
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				AllReady bool                `json:"all_ready"`
				Tasks    []map[string]string `json:"tasks"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(messages[0].Content, "Delegated project task outcomes:\n")), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.AllReady != (outcome == "completed") || len(payload.Tasks) != 2 {
				t.Fatalf("payload=%+v", payload)
			}
			if len([]rune(payload.Tasks[1]["title"])) != 200 {
				t.Fatal("title unbounded or missing")
			}
			if outcome == "replacement" && (payload.Tasks[1]["status"] != "superseded" || payload.Tasks[1]["attempt_id"] != "initial") {
				t.Fatal("wake retargeted new attempt", payload)
			}
			first, _, _ := s.GetProjectTask("account", "project", "task")
			want := "in_progress"
			if outcome == "completed" {
				want = "needs_review"
			}
			if first.Status != want {
				t.Fatal("wake changed sibling/accepted review", first.Status)
			}
		})
	}
}

// Purpose: ReserveTaskFollowup is the real attempt-replacement authority. Racing
// its revision-guarded reservation with wake reconciliation must retain the old
// wait target and never bind the new follow-up attempt to an old parent goal.
func TestProjectTaskDeliveryReopenVersusWake(t *testing.T) {
	for iteration := 0; iteration < 6; iteration++ {
		s := taskReportFixture(t)
		if _, err := s.ApplyV3SessionMutation(taskReportInput(ProjectTaskUpdateProgress)); err != nil {
			t.Fatal(err)
		}
		updates := pendingTaskUpdates(t, s, "goal")
		if err := registerTaskWait(s, "account", "user", "project", "task"); err != nil {
			t.Fatal(err)
		}
		taskWaitStatus(t, s, "completed")
		if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "child", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "child-completed", PayloadHash: "child-completed", RunIntent: &V3SessionRunIntent{RunID: "child-run", Status: V3RunIntentCompleted}}); err != nil {
			t.Fatal(err)
		}
		previous, _, err := s.GetProjectTask("account", "project", "task")
		if err != nil {
			t.Fatal(err)
		}
		start, results := make(chan struct{}), make(chan error, 2)
		go func() { <-start; results <- s.ReconcileProjectTaskWaits("account", "project", nil) }()
		go func() {
			<-start
			_, err := s.ReserveTaskFollowup("account", "project", "task", "user", "reopen", "New follow-up", previous.Revision, 100)
			results <- err
		}()
		close(start)
		var raceErr error
		for i := 0; i < 2; i++ {
			if err := <-results; err != nil {
				raceErr = err
			}
		}
		if raceErr != nil {
			t.Fatal(raceErr)
		}
		if err := s.ReconcileProjectTaskWaits("account", "project", nil); err != nil {
			t.Fatal(err)
		}
		assertTaskWaitWake(t, s, true)
		next, _, err := s.GetProjectTask("account", "project", "task")
		if err != nil || next.ActiveAttemptID == previous.ActiveAttemptID || len(next.Attempts) != 2 {
			t.Fatal("follow-up missing", err)
		}
		messages, err := s.ListV3SessionMessages("parent", 0, 20)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Tasks []map[string]string `json:"tasks"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(messages[0].Content, "Delegated project task outcomes:\n")), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Tasks) != 1 || payload.Tasks[0]["attempt_id"] != previous.ActiveAttemptID || payload.Tasks[0]["session_id"] != "child" {
			t.Fatal("wait retargeted follow-up", payload)
		}
		run := ProjectTaskWaitResumeID("goal")
		taskDeliveryClaim(t, s, run)
		if len(pendingTaskUpdates(t, s, run)) != 0 {
			t.Fatal("superseded report delivered to new attempt")
		}
		before, _ := s.readV3SessionSequence("parent")
		if _, err := s.ApplyV3SessionMutation(taskDeliveryInput(run, updates)); err == nil {
			t.Fatal("superseded receipt accepted")
		}
		after, _ := s.readV3SessionSequence("parent")
		if before != after {
			t.Fatal("rejected receipt changed parent")
		}
		if _, err := s.taskUpdateRaw(taskUpdateKey(updates[0])); err != nil {
			t.Fatal("unconsumed old data deleted", err)
		}
	}
}
