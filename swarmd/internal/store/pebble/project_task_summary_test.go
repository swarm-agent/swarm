package pebblestore

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble"
)

// Purpose: ListProjectTaskSummaries and the canonical realtime batch must keep
// board bytes independent of archived media/history, without losing exact review
// identity. The temporary-store layer proves persistence, partition movement,
// rejection postconditions and authorized media references, not API permissions.
func TestProjectTaskSummariesLifecycle(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	media := "data:image/png;base64," + strings.Repeat("a", 1<<20)
	task := ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Task", Agent: "swarm", Revision: 3, Status: "pending_approval", FullPlanMarkdown: strings.Repeat("plan", 1<<18), PlanBinding: &ProjectTaskPlanBinding{PlanID: "plan", DefinitionRevision: 7, Receipt: "receipt"}, Deliverables: []ProjectTaskDeliverable{{ID: "image", MediaURL: media, Thumbnail: media}}}
	if err := s.PutProjectTask("account", &task); err != nil {
		t.Fatal(err)
	}
	archived := ProjectTaskRecord{ID: "archived", ProjectID: "project", Title: "Archived", Agent: "swarm", Archived: true, Deliverables: task.Deliverables}
	if err := s.PutProjectTask("account", &archived); err != nil {
		t.Fatal(err)
	}
	rows, stats, err := s.ListProjectTaskSummaries("account", "project", false)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	if stats.ScannedRows != 1 || stats.DecodedBytes > 10000 {
		t.Fatalf("read amplification: %+v", stats)
	}
	row := rows[0]
	want := fmt.Sprintf("/v3/projects/project/tasks/task/deliverables/image?field=media&sha256=%x", sha256.Sum256([]byte(media)))
	if row.Deliverables[0].MediaURL != want || row.Deliverables[0].Thumbnail != want || row.FullPlanMarkdown != "" || row.PlanBinding.Receipt != "receipt" || row.Revision != 3 || row.Status != "pending_approval" {
		t.Fatalf("lost compact identity: %+v", row)
	}
	// Poison only canonical payload after migration: warm reads must not decode it.
	if err := db.PutBytes(KeyProjectTask("account", "project", "archived"), []byte("not json")); err != nil {
		t.Fatal(err)
	}
	if _, stats, err = s.ListProjectTaskSummaries("account", "project", false); err != nil || stats.BackfillRows != 0 || stats.ScannedRows != 1 {
		t.Fatalf("warm read touched canonical/archive: %+v %v", stats, err)
	}
	foreign, _, err := s.ListProjectTaskSummaries("foreign", "project", false)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("foreign rows: %v %v", foreign, err)
	}
	task.Archived = true
	if err := s.PutProjectTask("account", &task); err != nil {
		t.Fatal(err)
	}
	rows, _, err = s.ListProjectTaskSummaries("account", "project", false)
	if err != nil || len(rows) != 0 {
		t.Fatalf("archive move: %v %v", rows, err)
	}
	task.Archived = false
	if err := s.PutProjectTask("account", &task); err != nil {
		t.Fatal(err)
	}
	bad := task
	bad.AccountID = "foreign"
	if err := s.PutProjectTask("account", &bad); err == nil {
		t.Fatal("cross-account write accepted")
	}
	rows, _, err = s.ListProjectTaskSummaries("account", "project", false)
	if err != nil || len(rows) != 1 || rows[0].AccountID != "account" {
		t.Fatalf("rejection changed rows: %v %v", rows, err)
	}
	if err := s.DeleteProjectTask("account", "project", "task"); err != nil {
		t.Fatal(err)
	}
	rows, _, err = s.ListProjectTaskSummaries("account", "project", false)
	if err != nil || len(rows) != 0 {
		t.Fatalf("delete left row: %v %v", rows, err)
	}
}

// Purpose: the migration's cursor and projection must survive interruption and
// reopen without partial-success boards. Missing rows or corrupt legacy identity
// must fail closed. BackfillProjectTaskSummaries is the narrowest durable boundary.
func TestProjectTaskSummariesBackfillRestart(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessionStore(db)
	for i := 0; i < 35; i++ {
		task := ProjectTaskRecord{ID: fmt.Sprintf("task-%02d", i), AccountID: "account", ProjectID: "project", Title: "Legacy", Revision: i + 1, CreatedAt: int64(i + 1)}
		if err := db.PutJSON(KeyProjectTask("account", "project", task.ID), task); err != nil {
			t.Fatal(err)
		}
	}
	rows, stats, err := s.ListProjectTaskSummaries("account", "project", false)
	if !errors.Is(err, ErrProjectTaskSummariesNotReady) || rows != nil || stats.BackfillRows != 32 {
		t.Fatalf("partial success: %v %+v %v", rows, stats, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = NewSessionStore(db)
	rows, stats, err = s.ListProjectTaskSummaries("account", "project", false)
	if err != nil || len(rows) != 35 || stats.BackfillRows != 3 || rows[34].Revision != 35 {
		t.Fatalf("restart: %d %+v %v", len(rows), stats, err)
	}
	if err := db.db.Delete([]byte(taskSummaryRowKey(rows[0])), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if rows, _, err := s.ListProjectTaskSummaries("account", "project", false); !errors.Is(err, ErrProjectTaskSummaryCorrupt) || rows != nil {
		t.Fatalf("missing row accepted: %v %v", rows, err)
	}
	bad := ProjectTaskRecord{ID: "bad", AccountID: "foreign", ProjectID: "other"}
	if err := db.PutJSON(KeyProjectTask("account", "other", "bad"), bad); err != nil {
		t.Fatal(err)
	}
	if rows, _, err := s.ListProjectTaskSummaries("account", "other", false); !errors.Is(err, ErrProjectTaskSummaryCorrupt) || rows != nil {
		t.Fatalf("foreign legacy identity accepted: %v %v", rows, err)
	}
	var state taskSummaryState
	if ok, err := db.GetJSON(taskSummaryPrefix("account", "other")+"state", &state); err != nil || ok {
		t.Fatalf("failed migration advanced state: %v %v", ok, err)
	}
}

// Purpose: related reads share a snapshot and deduplicate exact keys; foreign run
// state and stale plan receipts cannot affect review. This tests the store reader
// directly so no session hydration or unrelated active plan can mask a violation.
func TestProjectTaskSummariesRelatedIdentity(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PutJSON(KeyV3SessionRunIntentActive("session"), V3SessionRunState{SessionID: "session", AccountScopeID: "foreign", RunID: "run", Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := NewSessionStore(db).PutPlan(SessionPlanSnapshot{ID: "plan", SessionID: "session", AccountScopeID: "account", Version: 2, ApprovalState: "approved", AcceptedDefinitionReceipt: "accepted"}); err != nil {
		t.Fatal(err)
	}
	rows := []ProjectTaskRecord{
		{ID: "one", SessionID: "session", PlanBinding: &ProjectTaskPlanBinding{PlanID: "plan", DefinitionRevision: 2, Receipt: "accepted"}},
		{ID: "two", SessionID: "session", PlanBinding: &ProjectTaskPlanBinding{PlanID: "plan", DefinitionRevision: 2, Receipt: "stale"}},
	}
	snapshot := db.db.NewSnapshot()
	defer snapshot.Close()
	// A later update must not alter the captured related view.
	if err := NewSessionStore(db).PutPlan(SessionPlanSnapshot{ID: "plan", SessionID: "session", AccountScopeID: "foreign"}); err != nil {
		t.Fatal(err)
	}
	var stats ProjectTaskReadStats
	related, err := readProjectTaskRelated(snapshot, "account", rows, &stats)
	if err != nil {
		t.Fatal(err)
	}
	if stats.RelatedRecordReads != 2 || related["one"].RunState != nil || related["one"].Plan == nil || related["two"].Plan != nil || !related["two"].PlanBindingStale {
		t.Fatalf("identity/dedup: %+v %+v", related, stats)
	}
}

// Purpose: projection errors must abort the same canonical batch as task writes,
// never leaving a new task without its index. Oversize rejection is deterministic
// fault injection at setTaskSummariesInBatch, avoiding filesystem-dependent faults.
func TestProjectTaskSummariesAtomicFailure(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	task := ProjectTaskRecord{ID: "task", ProjectID: "project", Agent: "swarm", Title: strings.Repeat("x", taskSummaryMaxBytes+1)}
	if err := s.PutProjectTask("account", &task); err == nil {
		t.Fatal("oversize accepted")
	}
	if _, found, err := s.GetProjectTask("account", "project", "task"); err != nil || found {
		t.Fatalf("partial task persisted: %v %v", found, err)
	}
	rows, _, err := s.ListProjectTaskSummaries("account", "project", false)
	if err != nil || len(rows) != 0 {
		t.Fatalf("partial projection: %v %v", rows, err)
	}
}

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
