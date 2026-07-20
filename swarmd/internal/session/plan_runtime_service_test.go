package session

import (
	"encoding/json"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestPlanRuntimeCommandServiceReturnsCompactReceiptForPassiveProgress(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := pebblestore.NewSessionStore(store)
	if err = sessions.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "session", Title: "session"}, "user", "account"); err != nil {
		t.Fatal(err)
	}
	_, err = sessions.PutPlanDefinition(pebblestore.PlanDefinitionWrite{
		Definition:  pebblestore.PlanDefinition{SessionID: "session", PlanID: "plan", DefinitionRevision: 1, CheckpointOrder: []string{"cp-1"}},
		Checkpoints: []pebblestore.CheckpointDefinition{{CheckpointID: "cp-1", SubtaskOrder: []string{"task-1"}}},
		Subtasks:    []pebblestore.SubtaskDefinition{{CheckpointID: "cp-1", SubtaskID: "task-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewPlanRuntimeCommandService(sessions)
	activated, err := svc.Execute(PlanRuntimeExecutionInput{Action: PlanRuntimeActionActivate, SessionID: "session", PlanID: "plan", AccountScopeID: "account", DefinitionRevision: 1, ExpectedExecutionSeq: 0, ClientRequestID: "activate"})
	if err != nil {
		t.Fatal(err)
	}
	if activated.ExecutionSeq != 1 || activated.NextCheckpointID != "cp-1" {
		t.Fatalf("unexpected activation receipt: %#v", activated)
	}
	// Seed an in-progress checkpoint directly: start transitions require genuine
	// epoch/run linkage, which is tested by the epoch subsystem.
	_, err = sessions.AppendPlanExecution(pebblestore.PlanExecutionCommand{SessionID: "session", PlanID: "plan", AccountScopeID: "account", DefinitionRevision: 1, ExpectedExecutionSeq: 1, ClientRequestID: "seed", PayloadHash: "seed", EventType: "plan.checkpoint_started", CheckpointID: "cp-1", NextSummary: pebblestore.PlanExecutionSummary{Status: "in_progress", ActiveCheckpointID: "cp-1", ActiveAttemptID: "attempt-1"}, CheckpointChange: &pebblestore.CheckpointExecution{CheckpointID: "cp-1", Status: "in_progress", ActiveAttemptID: "attempt-1"}})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := svc.Execute(PlanRuntimeExecutionInput{Action: PlanRuntimeActionCompleteSubtasks, SessionID: "session", PlanID: "plan", AccountScopeID: "account", DefinitionRevision: 1, ExpectedExecutionSeq: 2, ClientRequestID: "complete", CheckpointID: "cp-1", SubtaskIDs: []string{"task-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ExecutionSeq != 3 || len(receipt.ChangedSubtasks) != 1 || receipt.ChangedSubtasks[0].Status != "completed" {
		t.Fatalf("unexpected compact receipt: %#v", receipt)
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"definition_revision", "checkpoint_order", "acceptance_criteria", "checkpoint_executions", "subtask_executions", "snapshot"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("compact receipt contains forbidden field %q: %s", forbidden, raw)
		}
	}
	if _, ok, err := sessions.GetV3SessionRunIntent("session", ""); err != nil || ok {
		t.Fatalf("passive progress created a run intent: ok=%v err=%v", ok, err)
	}
	if _, ok, err := sessions.GetActiveExecutionEpoch("session"); err != nil || ok {
		t.Fatalf("passive progress created an epoch: ok=%v err=%v", ok, err)
	}
}

func TestPlanRuntimeCommandServiceRejectsUnknownDefinitionTargets(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := pebblestore.NewSessionStore(store)
	if err = sessions.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "session", Title: "session"}, "user", "account"); err != nil {
		t.Fatal(err)
	}
	_, err = sessions.PutPlanDefinition(pebblestore.PlanDefinitionWrite{Definition: pebblestore.PlanDefinition{SessionID: "session", PlanID: "plan", DefinitionRevision: 1, CheckpointOrder: []string{"cp-1"}}, Checkpoints: []pebblestore.CheckpointDefinition{{CheckpointID: "cp-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewPlanRuntimeCommandService(sessions)
	_, err = svc.Execute(PlanRuntimeExecutionInput{Action: PlanRuntimeActionStartCheckpoint, SessionID: "session", PlanID: "plan", AccountScopeID: "account", DefinitionRevision: 1, ExpectedExecutionSeq: 0, ClientRequestID: "unknown", CheckpointID: "cp-missing", AttemptID: "attempt", RunID: "run", EpochID: "epoch"})
	if err == nil || !strings.Contains(err.Error(), "not found in immutable definition") {
		t.Fatalf("expected named-target validation error, got %v", err)
	}
}
