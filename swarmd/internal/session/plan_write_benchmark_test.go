package session

import (
	"fmt"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// BenchmarkCheckpointTaskUpdateLongSession guards the plan write path against
// accidentally reintroducing transcript-length work. The large message count is
// metadata only: checkpoint progress must remain a direct plan-key update.
func BenchmarkCheckpointTaskUpdateLongSession(b *testing.B) {
	store, err := pebblestore.Open(filepath.Join(b.TempDir(), "sessions.pebble"))
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	sessions := pebblestore.NewSessionStore(store)
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		b.Fatal(err)
	}
	svc := NewService(sessions, events)
	sessionID := "session-plan-benchmark"
	if err := sessions.CreateSession(pebblestore.SessionSnapshot{ID: sessionID, UserID: "user", AccountScopeID: "account", Title: "Plan benchmark", MessageCount: 1_000_000, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		b.Fatal(err)
	}
	plan, _, err := svc.SavePlanWithMetadata(sessionID, "plan-bench", "Checkpoint benchmark", "# Benchmark", "approved", "approved", true, PlanSaveMetadata{Checkpoint: true, RevisionKind: PlanRevisionKindExecution, Document: &pebblestore.SessionPlanDocument{
		ID: "plan-bench", Title: "Checkpoint benchmark", ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: "automatic", Shape: "checkpointed"}, Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Update", Status: PlanCheckpointStatusInProgress, Tasks: []string{"update task"}}}, ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		b.Fatalf("seed plan: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		doc := *plan.Document
		doc.Checkpoints = append([]pebblestore.SessionPlanCheckpoint(nil), plan.Document.Checkpoints...)
		doc.Checkpoints[0].Notes = fmt.Sprintf("progress-%d", i)
		plan, _, err = svc.SavePlanWithMetadata(sessionID, plan.ID, plan.Title, plan.Plan, plan.Status, plan.ApprovalState, true, PlanSaveMetadata{Checkpoint: true, RevisionKind: PlanRevisionKindExecution, UpdateKind: "update_checkpoint", Document: &doc})
		if err != nil {
			b.Fatal(err)
		}
	}
}
