package pebblestore

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble"
)

// Purpose: canonical session/plan/program batches must publish compact related
// data whose warm read cost does not grow with prose or history payloads. This
// temporary-store test exercises the write boundary and snapshot reader, not a
// source-string check or a synthetic latency benchmark.
func TestProjectTaskRelatedProjectionScalingAndFreshness(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	task := ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account", Agent: "swarm", Title: "Task", SessionID: "session", TaskProgramID: "program", PlanBinding: &ProjectTaskPlanBinding{SessionID: "session", PlanID: "plan", Receipt: "receipt"}}
	if err := s.PutProjectTask("account", &task); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BackfillProjectTaskSummaries("account", "project"); err != nil {
		t.Fatal(err)
	}
	var baseline int64
	for n, size := range []int{16, 1 << 20} {
		body := strings.Repeat("x", size)
		session := SessionSnapshot{ID: "session", AccountScopeID: "account", UserID: "user", Metadata: map[string]any{"full_plan_markdown": body, "base_commit": "base", "lifecycle_signal": "blocked", "lifecycle_signal_run_id": "run", "blocker_reason": "input required"}}
		if err := s.UpdateSession(session); err != nil {
			t.Fatal(err)
		}
		plan := SessionPlanSnapshot{ID: "plan", SessionID: "session", AccountScopeID: "account", ApprovalState: "approved", AcceptedDefinitionReceipt: "receipt", Plan: body, Document: &SessionPlanDocument{Checkpoints: []SessionPlanCheckpoint{{ID: "cp", Status: "completed", Report: body, Tasks: []string{body}, RunID: "run", SessionID: "session", Handoff: &SessionPlanCheckpointHandoff{Overview: "done"}, Attempts: []SessionPlanCheckpointAttempt{{Report: body}}}}}}
		if err := s.PutPlan(plan); err != nil {
			t.Fatal(err)
		}
		program := TaskProgramRecord{ParentSessionID: "session", ProgramID: "program", State: TaskProgramStateBlocked, Revision: n + 1, Blocker: &TaskProgramBlocker{Message: "input required", Evidence: []string{body}}, Definition: TaskProgramDefinition{Jobs: []TaskProgramJobSpec{{MetaPrompt: body}}}, Jobs: []TaskProgramJobRecord{{JobID: "job", CurrentSessionID: "session", CurrentRunID: "run", GenerationHistory: []TaskProgramJobGeneration{{SessionID: "old", State: body}, {SessionID: "old", State: body}}}}}
		if err := s.putTaskProgramHistory(program); err != nil {
			t.Fatal(err)
		}
		_, stats, err := s.ReadProjectTaskBoard("account", "project", false, func(rows []ProjectTaskRecord, r *ProjectTaskBoardReader) {
			r.BindTask(&rows[0])
			v, ok, err := r.GetSession("session")
			if err != nil || !ok || v.Metadata["blocker_reason"] != "input required" || v.Metadata["full_plan_markdown"] != nil {
				t.Fatalf("session: %+v %v", v, err)
			}
			v.Metadata["base_commit"] = "mutated"
			v, _, _ = r.GetSession("session")
			if v.Metadata["base_commit"] != "base" {
				t.Fatal("cache alias")
			}
			p, ok, err := r.GetPlan("session", "plan")
			if err != nil || !ok || p.Plan != "" || p.Document.Checkpoints[0].Report != "" || p.Document.Checkpoints[0].Handoff.Overview != "done" {
				t.Fatalf("plan: %+v %v", p, err)
			}
			p.Document.Checkpoints[0].Status = "mutated"
			p, _, _ = r.GetPlan("session", "plan")
			if p.Document.Checkpoints[0].Status != "completed" {
				t.Fatal("plan cache alias")
			}
			g, ok, err := r.GetTaskProgram("session", "program")
			if err != nil || !ok || g.Revision != n+1 || len(g.Definition.Jobs) != 0 || len(g.Jobs[0].GenerationHistory) != 1 || g.Jobs[0].GenerationHistory[0].State != "" {
				t.Fatalf("program: %+v %v", g, err)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		if stats.BackfillBytes != 0 || stats.RelatedDecodedBytes > 10000 {
			t.Fatalf("read amplification: %+v", stats)
		}
		if n == 0 {
			baseline = stats.RelatedDecodedBytes
		} else if stats.RelatedDecodedBytes != baseline {
			t.Fatalf("body growth changed compact read bytes: %d -> %d", baseline, stats.RelatedDecodedBytes)
		}
		full, ok, err := s.GetPlan("session", "plan")
		if err != nil || !ok || full.Plan != body || full.Document.Checkpoints[0].Report != body {
			t.Fatal("detail was truncated")
		}
	}
}

// Purpose: legacy preparation must make bounded durable progress, return no
// partial board, and survive reopen without decoding already migrated bodies.
// Direct legacy fixtures model the absent derived keys; production board reads
// and actual Pebble reopen are the narrow migration/restart boundary.
func TestProjectTaskRelatedProjectionMigrationRestart(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessionStore(db)
	for i := 0; i < 35; i++ {
		id := fmt.Sprintf("session-%02d", i)
		task := ProjectTaskRecord{ID: id, ProjectID: "project", AccountID: "account", Agent: "swarm", Title: "Task", SessionID: id}
		if err := s.PutProjectTask("account", &task); err != nil {
			t.Fatal(err)
		}
		if err := db.PutJSON(KeySession(id), SessionSnapshot{ID: id, AccountScopeID: "account", Metadata: map[string]any{"body": strings.Repeat("x", 1024)}}); err != nil {
			t.Fatal(err)
		}
	}
	// Finish only the independent task-row migration first.
	for i := 0; i < 2; i++ {
		_, err := s.BackfillProjectTaskSummaries("account", "project")
		if err != nil && !errors.Is(err, ErrProjectTaskSummariesNotReady) {
			t.Fatal(err)
		}
	}
	called := false
	rows, stats, err := s.ReadProjectTaskBoard("account", "project", false, func([]ProjectTaskRecord, *ProjectTaskBoardReader) { called = true })
	if !errors.Is(err, ErrProjectTaskSummariesNotReady) || rows != nil || called || stats.BackfillRows != 32 {
		t.Fatalf("partial board: rows=%d called=%v stats=%+v err=%v", len(rows), called, stats, err)
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
	_, stats, err = s.ReadProjectTaskBoard("account", "project", false, nil)
	if !errors.Is(err, ErrProjectTaskSummariesNotReady) || stats.BackfillRows != 3 {
		t.Fatalf("restart progress: %+v %v", stats, err)
	}
	rows, stats, err = s.ReadProjectTaskBoard("account", "project", false, nil)
	if err != nil || len(rows) != 35 || stats.BackfillBytes != 0 {
		t.Fatalf("ready: %d %+v %v", len(rows), stats, err)
	}
}

// Purpose: batch rejection must not publish compact evidence without canonical
// state. setV3PlanSaveInBatch rejects a mismatched archived identity; abandoning
// that real batch must preserve BOTH keys byte-for-byte. Successful save and
// acceptance then exercise both canonical plan write paths and delete cleanup.
func TestProjectTaskRelatedProjectionAtomicPlan(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	plan := SessionPlanSnapshot{ID: "plan", SessionID: "session", AccountScopeID: "account", Version: 1, ApprovalState: "pending"}
	if err := s.PutPlan(plan); err != nil {
		t.Fatal(err)
	}
	key := KeySessionPlan("session", "plan")
	before, _, _ := db.GetBytes(key)
	derived, _, _ := db.GetBytes(taskRelatedKey(key))
	plan.Version = 2
	batch := db.NewBatch()
	err = setV3PlanSaveInBatch(batch, "session", V3PlanSaveMutation{Plan: plan, ArchivedRevision: &SessionPlanSnapshot{ID: "foreign", SessionID: "other"}})
	batch.Close()
	if err == nil {
		t.Fatal("invalid archive accepted")
	}
	after, _, _ := db.GetBytes(key)
	afterDerived, _, _ := db.GetBytes(taskRelatedKey(key))
	if !bytes.Equal(before, after) || !bytes.Equal(derived, afterDerived) {
		t.Fatal("rejected batch changed state")
	}
	batch = db.NewBatch()
	if err := setV3PlanSaveInBatch(batch, "session", V3PlanSaveMutation{Plan: plan}); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		t.Fatal(err)
	}
	batch.Close()
	plan.ApprovalState, plan.AcceptedDefinitionReceipt = "approved", "exact-receipt"
	batch = db.NewBatch()
	if err := setPlanAcceptancePlanInBatch(batch, plan, nil); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		t.Fatal(err)
	}
	batch.Close()
	var got SessionPlanSnapshot
	if ok, err := db.GetJSON(taskRelatedKey(key), &got); err != nil || !ok || got.Version != 2 || got.AcceptedDefinitionReceipt != "exact-receipt" {
		t.Fatalf("stale acceptance: %+v %v", got, err)
	}
	batch = db.NewBatch()
	if err := deleteTaskRelatedSessionInBatch(batch, "session", true); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		t.Fatal(err)
	}
	batch.Close()
	if ok, err := db.GetJSON(taskRelatedKey(key), &got); err != nil || ok {
		t.Fatalf("delete retained plan: %v %v", ok, err)
	}
}

// Purpose: exact receipt, session/account and run bindings are security evidence,
// not advisory labels. ProjectTaskBoardReader must reject stale/foreign rows
// without changing persisted task or plan state; direct reader checks isolate
// identity policy from API serialization and UI state.
func TestProjectTaskRelatedProjectionIdentity(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	if err := s.UpdateSession(SessionSnapshot{ID: "session", AccountScopeID: "account"}); err != nil {
		t.Fatal(err)
	}
	plan := SessionPlanSnapshot{ID: "plan", SessionID: "session", AccountScopeID: "account", Version: 2, ApprovalState: "approved", AcceptedDefinitionReceipt: "new"}
	if err := s.PutPlan(plan); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(KeyV3SessionRunIntentActive("session"), V3SessionRunState{SessionID: "session", AccountScopeID: "account", RunID: "old", Status: V3RunIntentCompleted}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := db.GetBytes(KeySessionPlan("session", "plan"))
	var stats ProjectTaskReadStats
	r := newProjectTaskBoardReader(db.db, "account", nil, &stats)
	task := ProjectTaskRecord{SessionID: "session", PlanBinding: &ProjectTaskPlanBinding{SessionID: "session", PlanID: "plan", Receipt: "old"}, Attempts: []ProjectTaskAttempt{{ID: "attempt", SessionID: "session", RunID: "new"}}, ActiveAttemptID: "attempt"}
	r.BindTask(&task)
	if _, ok, err := r.GetPlan("session", "plan"); err != nil || ok {
		t.Fatalf("stale receipt accepted: %v", err)
	}
	if _, ok, err := r.GetV3SessionRunState("session"); err != nil || ok {
		t.Fatalf("stale run accepted: %v", err)
	}
	r = newProjectTaskBoardReader(db.db, "foreign", nil, &stats)
	r.BindTask(&task)
	if _, ok, err := r.GetSession("session"); err != nil || ok {
		t.Fatalf("foreign session accepted: %v", err)
	}
	after, _, _ := db.GetBytes(KeySessionPlan("session", "plan"))
	if !bytes.Equal(before, after) {
		t.Fatal("identity rejection mutated plan")
	}
}

// Purpose: one legacy body larger than a chunk must still progress, whereas
// corrupt key/record identity must fail without publishing a derived row. This
// directly exercises bounded backfill with actual Pebble values and retry.
func TestProjectTaskRelatedProjectionLargeLegacyAndRejection(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	key := KeySessionPlan("session", "plan")
	plan := SessionPlanSnapshot{ID: "wrong", SessionID: "session", AccountScopeID: "account", Plan: strings.Repeat("x", (16<<20)+1)}
	if err := db.PutJSON(key, plan); err != nil {
		t.Fatal(err)
	}
	var stats ProjectTaskReadStats
	if err := s.backfillTaskRelatedKey(key, &stats); !errors.Is(err, ErrProjectTaskSummaryCorrupt) {
		t.Fatalf("wrong identity accepted: %v", err)
	}
	var derived SessionPlanSnapshot
	if ok, err := db.GetJSON(taskRelatedKey(key), &derived); err != nil || ok {
		t.Fatalf("rejection published projection: %v %v", ok, err)
	}
	plan.ID = "plan"
	if err := db.PutJSON(key, plan); err != nil {
		t.Fatal(err)
	}
	stats = ProjectTaskReadStats{}
	if err := s.backfillTaskRelatedKey(key, &stats); err != nil {
		t.Fatal(err)
	}
	if stats.BackfillRows != 1 || stats.BackfillBytes <= taskSummaryBackfillBytes {
		t.Fatalf("no oversized progress: %+v", stats)
	}
	if ok, err := db.GetJSON(taskRelatedKey(key), &derived); err != nil || !ok || derived.Plan != "" {
		t.Fatalf("bad projection: %+v %v", derived, err)
	}
	stats = ProjectTaskReadStats{}
	if err := s.backfillTaskRelatedKey(key, &stats); err != nil || stats.BackfillBytes != 0 {
		t.Fatalf("retry decoded body: %+v %v", stats, err)
	}
}

// Purpose: board handoffs must follow exact active-attempt session/run/receipt
// evidence and must not inherit a stale checkpoint outcome. The compact reader
// is the narrowest layer that can assert both fidelity and no full plan reads.
func TestProjectTaskRelatedProjectionAttemptHandoff(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	if err := s.UpdateSession(SessionSnapshot{ID: "session", AccountScopeID: "account"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(KeyV3SessionRunIntentActive("session"), V3SessionRunState{SessionID: "session", AccountScopeID: "account", RunID: "run", Status: V3RunIntentCompleted}); err != nil {
		t.Fatal(err)
	}
	plan := SessionPlanSnapshot{ID: "plan", SessionID: "session", AccountScopeID: "account", ApprovalState: "approved", AcceptedDefinitionReceipt: "receipt", Document: &SessionPlanDocument{Checkpoints: []SessionPlanCheckpoint{{ID: "cp", Status: "completed", SessionID: "session", RunID: "old", Handoff: &SessionPlanCheckpointHandoff{Overview: "stale"}}}}}
	for _, current := range []bool{false, true} {
		if current {
			plan.Document.Checkpoints[0].RunID = "run"
			plan.Document.Checkpoints[0].Handoff.Overview = "current"
		}
		if err := s.PutPlan(plan); err != nil {
			t.Fatal(err)
		}
		task := ProjectTaskRecord{SessionID: "session", AccountID: "account", ActiveAttemptID: "attempt", Attempts: []ProjectTaskAttempt{{ID: "attempt", SessionID: "session", RunID: "run"}}, PlanBinding: &ProjectTaskPlanBinding{SessionID: "session", PlanID: "plan", Receipt: "receipt"}}
		var stats ProjectTaskReadStats
		r := newProjectTaskBoardReader(db.db, "account", nil, &stats)
		r.hydrateAttempt(&task)
		if current {
			if task.ActiveAttempt().Summary != "current" || task.ActiveAttempt().SummaryRunID != "run" {
				t.Fatalf("current handoff missing: %+v", task.ActiveAttempt())
			}
		} else if task.ActiveAttempt().Summary != "" {
			t.Fatal("stale handoff inherited")
		}
	}
}

// Purpose: ApplyV3SessionMutation owns session creation and authorization. Its
// compact projection must commit with the canonical session, and foreign
// mutation rejection must leave both unchanged. This uses the real mutation
// service with an isolated store rather than only testing the projection helper.
func TestProjectTaskRelatedProjectionV3SessionBoundary(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	session := SessionSnapshot{ID: "session", AccountScopeID: "account", UserID: "user", Metadata: map[string]any{"base_commit": "base", "full_plan_markdown": strings.Repeat("x", 10000)}}
	input := V3SessionMutationInput{SessionID: session.ID, AccountScopeID: "account", UserID: "user", Kind: V3SessionMutationCreateSession, ClientRequestID: "create", PayloadHash: "create", Session: &session}
	if _, err := s.ApplyV3SessionMutation(input); err != nil {
		t.Fatal(err)
	}
	key := KeySession("session")
	before, _, _ := db.GetBytes(key)
	derived, _, _ := db.GetBytes(taskRelatedKey(key))
	if len(derived) == 0 || len(derived) >= 2000 {
		t.Fatalf("missing/large V3 projection: %d", len(derived))
	}
	input.AccountScopeID, input.UserID, input.ClientRequestID, input.PayloadHash = "foreign", "foreign-user", "foreign", "foreign"
	if _, err := s.ApplyV3SessionMutation(input); err == nil {
		t.Fatal("foreign mutation accepted")
	}
	after, _, _ := db.GetBytes(key)
	afterDerived, _, _ := db.GetBytes(taskRelatedKey(key))
	if !bytes.Equal(before, after) || !bytes.Equal(derived, afterDerived) {
		t.Fatal("foreign rejection changed state")
	}
}
