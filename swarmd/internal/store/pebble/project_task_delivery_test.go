package pebblestore

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func pendingTaskUpdates(t *testing.T, s *SessionStore, run string) []ProjectTaskUpdate {
	t.Helper()
	updates, _, err := s.PendingProjectTaskUpdates("account", "user", "parent", run, "")
	if err != nil {
		t.Fatal(err)
	}
	return updates
}
func taskDeliveryInput(run string, updates []ProjectTaskUpdate) V3SessionMutationInput {
	return V3SessionMutationInput{SessionID: "parent", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationDeliverTasks, ClientRequestID: "delivery", PayloadHash: "derived", TaskDelivery: &V3ProjectTaskDeliveryMutation{RunID: run, Updates: updates}}
}
func taskDeliveryClaim(t *testing.T, s *SessionStore, run string) {
	t.Helper()
	_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "claim-" + run, PayloadHash: run, RunIntent: &V3SessionRunIntent{RunID: run, Status: V3RunIntentRunning}})
	if err != nil {
		t.Fatal(err)
	}
}

// Purpose: the V3 batch must keep reports pending until a successful provider
// receipt, deduplicate receipts and bind exact event bytes. Store assertions
// prove rejection without partial writes, unlike an enqueue/status-only test.
func TestProjectTaskDeliveryReceipt(t *testing.T) {
	s := taskReportFixture(t)
	in := taskReportInput(ProjectTaskUpdateProgress)
	if _, err := s.ApplyV3SessionMutation(in); err != nil {
		t.Fatal(err)
	}
	updates := pendingTaskUpdates(t, s, "goal")
	if len(updates) != 1 || updates[0].ParentRunID != "goal" || updates[0].QueueSeq == 0 {
		t.Fatalf("updates=%+v", updates)
	}
	if len(pendingTaskUpdates(t, s, "goal")) != 1 {
		t.Fatal("read acknowledged delivery")
	}
	forged := append([]ProjectTaskUpdate(nil), updates...)
	forged[0].Summary = "forged"
	before, _ := s.readV3SessionSequence("parent")
	if _, err := s.ApplyV3SessionMutation(taskDeliveryInput("goal", forged)); err == nil {
		t.Fatal("forged receipt accepted")
	}
	after, _ := s.readV3SessionSequence("parent")
	if before != after || len(pendingTaskUpdates(t, s, "goal")) != 1 {
		t.Fatal("rejection changed state")
	}
	receipt, err := s.ApplyV3SessionMutation(taskDeliveryInput("goal", updates))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.RealtimeOutbox.Event.EventType != "session.task.delivered" || len(pendingTaskUpdates(t, s, "goal")) != 0 {
		t.Fatal("receipt did not atomically acknowledge")
	}
	replay, err := s.ApplyV3SessionMutation(taskDeliveryInput("goal", updates))
	if err != nil || !replay.Replayed || replay.PrimarySeq != receipt.PrimarySeq {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	if _, err := s.ApplyV3SessionMutation(in); err != nil {
		t.Fatal(err)
	}
	if len(pendingTaskUpdates(t, s, "goal")) != 0 {
		t.Fatal("report retry requeued consumed event")
	}
}

// Purpose: one wait reconciler handles reports before/after registration and
// concurrent wake requests; progress cannot wake and enqueue cannot acknowledge.
func TestProjectTaskDeliveryWaitPolicy(t *testing.T) {
	for _, kind := range []ProjectTaskUpdateKind{ProjectTaskUpdateProgress, ProjectTaskUpdateAttention, ProjectTaskUpdateWakeRequest} {
		for _, before := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", kind, before), func(t *testing.T) {
				s := taskReportFixture(t)
				report := func() {
					t.Helper()
					if _, err := s.ApplyV3SessionMutation(taskReportInput(kind)); err != nil {
						t.Fatal(err)
					}
				}
				if before {
					report()
				}
				if err := registerTaskWait(s, "account", "user", "project", "task"); err != nil {
					t.Fatal(err)
				}
				if !before {
					report()
				}
				var wg sync.WaitGroup
				for i := 0; i < 4; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						if err := s.ReconcileProjectTaskWaits("account", "project", nil); err != nil {
							t.Error(err)
						}
					}()
				}
				wg.Wait()
				wake := kind != ProjectTaskUpdateProgress
				assertTaskWaitWake(t, s, wake)
				run := "goal"
				if wake {
					run = ProjectTaskWaitResumeID(run)
					taskDeliveryClaim(t, s, run)
				}
				updates := pendingTaskUpdates(t, s, run)
				if len(updates) != 1 {
					t.Fatalf("queued update lost: %+v", updates)
				}
				if _, err := s.ApplyV3SessionMutation(taskDeliveryInput(run, updates)); err != nil {
					t.Fatal(err)
				}
				if wake {
					_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "wait-again", PayloadHash: "wait-again", TaskWait: &V3ProjectTaskWaitMutation{RunID: run, ProjectID: "project", TaskIDs: []string{"task"}}})
					if err != nil {
						t.Fatal(err)
					}
					if err := s.ReconcileProjectTaskWaits("account", "project", nil); err != nil {
						t.Fatal(err)
					}
					if _, found, _ := s.GetV3SessionRunIntent("parent", ProjectTaskWaitResumeID(run)); found {
						t.Fatal("consumed event repeatedly woke parent")
					}
				}
			})
		}
	}
}

// Purpose: late provider responses and late task reports must not cross a user
// message, stop, archive, or attempt replacement. Assert retained queue bytes and
// unchanged parent sequence on rejection at the transaction authority.
func TestProjectTaskDeliveryStaleGuards(t *testing.T) {
	for _, change := range []string{"user", "stop", "archive", "attempt", "principal", "run"} {
		t.Run(change, func(t *testing.T) {
			s := taskReportFixture(t)
			if _, err := s.ApplyV3SessionMutation(taskReportInput(ProjectTaskUpdateWakeRequest)); err != nil {
				t.Fatal(err)
			}
			updates := pendingTaskUpdates(t, s, "goal")
			in := taskDeliveryInput("goal", updates)
			switch change {
			case "user":
				_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationAppendMessage, ClientRequestID: "new-user", PayloadHash: "new-user", Message: &MessageSnapshot{ID: "new-user", Role: "user", Content: "different objective"}})
				if err != nil {
					t.Fatal(err)
				}
			case "stop", "archive":
				_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "stop", PayloadHash: "stop", RunIntent: &V3SessionRunIntent{RunID: "goal", Status: V3RunIntentCancelled}})
				if err != nil {
					t.Fatal(err)
				}
				if change == "archive" {
					if err := s.ArchiveSessions([]string{"parent"}); err != nil {
						t.Fatal(err)
					}
				}
			case "attempt":
				_, err := s.UpdateProjectTask("account", "project", "task", func(task *ProjectTaskRecord) error {
					task.ActiveAttemptID = "new"
					task.SessionID = "new"
					task.Attempts = []ProjectTaskAttempt{{ID: "new", SessionID: "new"}}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			case "principal":
				in.UserID = "foreign"
			case "run":
				in.TaskDelivery.RunID = "foreign"
			}
			before, _ := s.readV3SessionSequence("parent")
			if _, err := s.ApplyV3SessionMutation(in); err == nil {
				t.Fatal("stale receipt accepted")
			}
			after, _ := s.readV3SessionSequence("parent")
			if before != after {
				t.Fatal("rejected receipt mutated parent")
			}
			if _, err := s.taskUpdateRaw(taskUpdateKey(updates[0])); err != nil {
				t.Fatal("unconsumed update lost", err)
			}
		})
	}
}

// Purpose: bounded queue pages survive Pebble restart and a final provider/run
// boundary without triggering another run. New goals cannot acquire delayed old
// task reports; captured deployment generation, not arrival time, owns delivery.
func TestProjectTaskDeliveryRestartAndFinalBoundary(t *testing.T) {
	s := taskReportFixture(t)
	for i := 0; i < 18; i++ {
		in := taskReportInput(ProjectTaskUpdateProgress)
		in.ClientRequestID = fmt.Sprint("report-", i)
		if _, err := s.ApplyV3SessionMutation(in); err != nil {
			t.Fatal(err)
		}
	}
	updates, next, err := s.PendingProjectTaskUpdates("account", "user", "parent", "goal", "")
	if err != nil || len(updates) != 16 || next == "" {
		t.Fatalf("page=%d %q %v", len(updates), next, err)
	}
	tail, _, err := s.PendingProjectTaskUpdates("account", "user", "parent", "goal", next)
	if err != nil || len(tail) != 2 {
		t.Fatalf("tail=%d %v", len(tail), err)
	}
	if _, err := s.ApplyV3SessionMutation(taskDeliveryInput("goal", updates)); err != nil {
		t.Fatal(err)
	}
	for _, run := range []struct{ id, status string }{{"goal", V3RunIntentCompleted}, {"new-goal", V3RunIntentPendingExecutor}, {"new-goal", V3RunIntentRunning}} {
		_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: run.id + run.status, PayloadHash: run.status, RunIntent: &V3SessionRunIntent{RunID: run.id, Status: run.status}})
		if err != nil {
			t.Fatal(err)
		}
	}
	in := taskReportInput(ProjectTaskUpdateWakeRequest)
	in.ClientRequestID = "late"
	if _, err := s.ApplyV3SessionMutation(in); err != nil {
		t.Fatal(err)
	}
	if len(pendingTaskUpdates(t, s, "new-goal")) != 0 {
		t.Fatal("late reports crossed goal generation")
	}
	if _, err := s.taskUpdateRaw(taskUpdateKey(tail[0])); err != nil {
		t.Fatal("final boundary lost pending report")
	}
	// Copy no fixtures or private state: reopen a separate real Pebble database.
	path := filepath.Join(t.TempDir(), "restart.pebble")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rs := NewSessionStore(db)
	taskWaitFixture(t, rs)
	for _, status := range []string{V3RunIntentPendingExecutor, V3RunIntentRunning} {
		_, err = rs.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "child", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: status, PayloadHash: status, RunIntent: &V3SessionRunIntent{RunID: "child-run", ParentSessionID: "parent", Status: status}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = rs.ApplyV3SessionMutation(taskReportInput(ProjectTaskUpdateAttention)); err != nil {
		t.Fatal(err)
	}
	if err = registerTaskWait(rs, "account", "user", "project", "task"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rs = NewSessionStore(db)
	if err = rs.ReconcileProjectTaskWaits("", "", nil); err != nil {
		t.Fatal(err)
	}
	run := ProjectTaskWaitResumeID("goal")
	taskDeliveryClaim(t, rs, run)
	got := pendingTaskUpdates(t, rs, run)
	if len(got) != 1 {
		t.Fatal("restart lost queued report")
	}
	raw, _ := json.Marshal(got[0])
	if len(raw) > 6000 {
		t.Fatal("unbounded delivery record")
	}
}

// Purpose: a wait committed inside the consuming provider step must not lose its
// receipt if wake reconciliation wins first. Conversely a new user message must
// fence wait registration and a late receipt. The store is the race authority.
func TestProjectTaskDeliveryReceiptVersusWake(t *testing.T) {
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
	if _, err := s.ApplyV3SessionMutation(taskDeliveryInput("goal", updates)); err != nil {
		t.Fatal("successful original response not acknowledged", err)
	}
	run := ProjectTaskWaitResumeID("goal")
	taskDeliveryClaim(t, s, run)
	if len(pendingTaskUpdates(t, s, run)) != 0 {
		t.Fatal("wake duplicated consumed update")
	}
}

// Purpose: user message append can leave the old run ID current; run identity
// alone must not authorize a stale tool to pend or consume reports afterward.
func TestProjectTaskDeliveryUserMessageFencesWait(t *testing.T) {
	s := taskReportFixture(t)
	_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationAppendMessage, ClientRequestID: "new-user", PayloadHash: "new-user", Message: &MessageSnapshot{ID: "new-user", Role: "user", Content: "new objective"}})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.readV3SessionSequence("parent")
	if err := registerTaskWait(s, "account", "user", "project", "task"); err == nil {
		t.Fatal("old provider registered wait after new message")
	}
	after, _ := s.readV3SessionSequence("parent")
	if before != after {
		t.Fatal("stale registration wrote state")
	}
}

// Purpose: a sealed execution epoch is not consumption authority even if its
// run ID remains current. Exercise the real epoch API, rejecting receipt/wait
// mutations without deleting queued data or changing the parent sequence.
func TestProjectTaskDeliveryEpochFence(t *testing.T) {
	s := taskReportFixture(t)
	epoch, found, err := s.GetActiveExecutionEpoch("parent")
	if err != nil || !found {
		t.Fatal("missing epoch", err)
	}
	_, err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "bind-epoch", PayloadHash: "bind-epoch", RunIntent: &V3SessionRunIntent{RunID: "goal", EpochID: epoch.EpochID, Status: V3RunIntentRunning}})
	if err != nil {
		t.Fatal(err)
	}
	// Explicit selection captures this run/epoch for the task attempt.
	if err := registerTaskWait(s, "account", "user", "project", "task"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyV3SessionMutation(taskReportInput(ProjectTaskUpdateAttention)); err != nil {
		t.Fatal(err)
	}
	updates := pendingTaskUpdates(t, s, "goal")
	if len(updates) != 1 {
		t.Fatal("missing bound update")
	}
	if _, err := s.SealExecutionEpoch(SealExecutionEpochInput{SessionID: "parent", EpochID: epoch.EpochID}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.readV3SessionSequence("parent")
	if _, err := s.ApplyV3SessionMutation(taskDeliveryInput("goal", updates)); err == nil {
		t.Fatal("sealed epoch consumed report")
	}
	if err := s.ReconcileProjectTaskWaits("account", "project", nil); err != nil {
		t.Fatal(err)
	}
	after, _ := s.readV3SessionSequence("parent")
	if before != after {
		t.Fatal("sealed epoch mutated parent")
	}
	if _, err := s.taskUpdateRaw(taskUpdateKey(updates[0])); err != nil {
		t.Fatal("sealed epoch lost report")
	}
}

// Purpose: either report publication or wait registration may commit first;
// their shared project/session transaction locks plus reconciliation must retain
// exactly one update and one continuation. This is an actual concurrent store
// test, not timing/sleep-based simulated execution.
func TestProjectTaskDeliveryConcurrentRegistration(t *testing.T) {
	for i := 0; i < 8; i++ {
		s := taskReportFixture(t)
		start := make(chan struct{})
		errs := make(chan error, 2)
		go func() {
			<-start
			_, err := s.ApplyV3SessionMutation(taskReportInput(ProjectTaskUpdateAttention))
			if err == nil {
				err = s.ReconcileProjectTaskWaits("account", "project", nil)
			}
			errs <- err
		}()
		go func() {
			<-start
			err := registerTaskWait(s, "account", "user", "project", "task")
			if err == nil {
				err = s.ReconcileProjectTaskWaits("account", "project", nil)
			}
			errs <- err
		}()
		close(start)
		for n := 0; n < 2; n++ {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		assertTaskWaitWake(t, s, true)
		run := ProjectTaskWaitResumeID("goal")
		taskDeliveryClaim(t, s, run)
		if len(pendingTaskUpdates(t, s, run)) != 1 {
			t.Fatal("race lost/duplicated update")
		}
	}
}
