package pebblestore

import (
	"reflect"
	"testing"
)

// Purpose: task outcome hydration must select only a terminal handoff for the
// current session/run, never an older completed checkpoint in the same plan.
// hydrateTaskAttemptOutcome owns this provenance boundary; a real temporary
// store is the narrowest layer proving persistence and historical retention.
func TestTaskFollowupPlanSummaryProvenance(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	session := SessionSnapshot{ID: "session", UserID: "user", AccountScopeID: "account"}
	create := V3SessionMutationInput{SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, ClientRequestID: "create", IdempotencyKey: "create", PayloadHash: "create", RequestHash: "create", Kind: V3SessionMutationCreateSession, Session: &session}
	if _, err := s.ApplyV3SessionMutation(create); err != nil {
		t.Fatal(err)
	}
	run := V3SessionRunIntent{SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, RunID: "current", Status: V3RunIntentCompleted}
	pending := run
	pending.Status = V3RunIntentPendingExecutor
	if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, ClientRequestID: "pending", IdempotencyKey: "pending", PayloadHash: "pending", RequestHash: "pending", Kind: V3SessionMutationRecordRunIntent, RunIntent: &pending}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, ClientRequestID: "run", IdempotencyKey: "run", PayloadHash: "run", RequestHash: "run", Kind: V3SessionMutationRecordRunIntent, RunIntent: &run}); err != nil {
		t.Fatal(err)
	}
	plan := SessionPlanSnapshot{ID: "plan", SessionID: session.ID, UserID: "user", AccountScopeID: "account", Status: "approved", ApprovalState: "approved", Document: &SessionPlanDocument{Checkpoints: []SessionPlanCheckpoint{
		{ID: "old", Status: "completed", SessionID: session.ID, RunID: "previous", Handoff: &SessionPlanCheckpointHandoff{Overview: "stale outcome"}},
	}}}
	if err := s.PutPlan(plan); err != nil {
		t.Fatal(err)
	}
	task := &ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account", Title: "Task", Agent: "swarm", Status: "needs_review", SessionID: session.ID, Revision: 1, PlanBinding: &ProjectTaskPlanBinding{SessionID: session.ID, PlanID: plan.ID}}
	if err := s.PutProjectTask("account", task); err != nil {
		t.Fatal(err)
	}
	stored, _, err := s.GetProjectTask("account", "project", "task")
	if err != nil || stored.ActiveAttempt().Summary != "" {
		t.Fatalf("stale checkpoint summary leaked: %+v %v", stored, err)
	}
	plan.Document.Checkpoints = append(plan.Document.Checkpoints, SessionPlanCheckpoint{ID: "current", Status: "completed", SessionID: session.ID, RunID: run.RunID, Handoff: &SessionPlanCheckpointHandoff{Overview: "Current outcome; validation pending"}})
	if err := s.PutPlan(plan); err != nil {
		t.Fatal(err)
	}
	stored, _, err = s.GetProjectTask("account", "project", "task")
	if err != nil || stored.ActiveAttempt().SummaryRunID != run.RunID || stored.ActiveAttempt().Summary != "Current outcome; validation pending" {
		t.Fatalf("matching outcome missing: %+v %v", stored, err)
	}
	if _, err := s.ReserveTaskFollowup("account", "project", "task", "user", "next", "Next request", 1, 1000); err != nil {
		t.Fatal(err)
	}
	stored, _, err = s.GetProjectTask("account", "project", "task")
	if err != nil || stored.Attempts[0].Summary != "Current outcome; validation pending" || stored.ActiveAttempt().Summary != "" {
		t.Fatal("follow-up lost earlier outcome or inherited stale summary")
	}
}

// Purpose: accepted historical plan metadata must survive follow-up while a
// pending/rejected plan cannot authorize new execution. ReserveTaskFollowup is
// the narrow durable admission layer; rejection must leave all task data intact.
func TestTaskFollowupPlanReviewGate(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	for _, approval := range []string{"pending", "rejected", "approved"} {
		plan := SessionPlanSnapshot{ID: "plan-" + approval, SessionID: "session-" + approval, AccountScopeID: "account", UserID: "user", ApprovalState: approval, Document: &SessionPlanDocument{Title: "Original plan"}}
		if err := s.PutPlan(plan); err != nil {
			t.Fatal(err)
		}
		task := &ProjectTaskRecord{ID: approval, ProjectID: "project", Title: "Task", Agent: "swarm", Status: "needs_review", SessionID: plan.SessionID, Revision: 1, PlanBinding: &ProjectTaskPlanBinding{PlanID: plan.ID, SessionID: plan.SessionID, DefinitionRevision: 1}}
		if err := s.PutProjectTask("account", task); err != nil {
			t.Fatal(err)
		}
		before, _, _ := s.GetProjectTask("account", "project", task.ID)
		after, err := s.ReserveTaskFollowup("account", "project", task.ID, "user", "key", "New scope", 1, 1000)
		if approval != "approved" {
			if err == nil {
				t.Fatal("unreviewed plan bypassed")
			}
			current, _, _ := s.GetProjectTask("account", "project", task.ID)
			if !reflect.DeepEqual(before, current) {
				t.Fatal("review rejection changed task")
			}
		} else {
			if err != nil || after.PlanBinding != nil || after.Attempts[0].PlanBinding == nil || after.Attempts[0].PlanBinding.PlanID != plan.ID {
				t.Fatal("approved historical plan blocked or erased")
			}
			retained, found, err := s.GetPlan(plan.SessionID, plan.ID)
			if err != nil || !found || retained.ApprovalState != "approved" {
				t.Fatal("original approval changed")
			}
		}
	}
}
