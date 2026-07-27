package session

import (
	"errors"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestCommitV3PlanAcceptanceIsAtomicOrderedAndIdempotent(t *testing.T) {
	svc, cleanup := newModeReentryTestService(t)
	defer cleanup()
	sessionID := createModeReentryTestSession(t, svc, ModePlan)
	created, ok, err := svc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	doc := &pebblestore.SessionPlanDocument{ID: "plan-atomic", Title: "Atomic", Status: "approved", Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "One", Status: PlanCheckpointStatusPending, Order: 1}}}
	apply := func(input SessionMutationInput) (SessionMutationResult, error) {
		return svc.ApplySessionMutation(input)
	}
	first, err := svc.CommitV3PlanAcceptance(PlanAcceptanceCommitInput{Session: created, PlanID: doc.ID, Title: doc.Title, Plan: "# Atomic", Document: doc, ApplySessionMutation: apply})
	if err != nil {
		t.Fatalf("commit acceptance: %v", err)
	}
	if first.Mutation.Replayed || len(first.Mutation.Events) != 2 || len(first.Mutation.RealtimeOutboxes) != 2 {
		t.Fatalf("unexpected fresh acceptance mutation: %+v", first.Mutation)
	}
	if first.Session.Title != doc.Title {
		t.Fatalf("accepted session title = %q, want plan title %q", first.Session.Title, doc.Title)
	}
	want := []string{"session.plan.saved", "session.mode.updated"}
	for i, event := range first.Mutation.Events {
		if event.EventType != want[i] || event.Seq != first.Mutation.FirstSeq+uint64(i) {
			t.Fatalf("event[%d] = %+v, want type %q ordered from %d", i, event, want[i], first.Mutation.FirstSeq)
		}
	}
	stored, ok, err := svc.GetActivePlan(created.ID)
	if err != nil || !ok || stored.ID != doc.ID || stored.Version != 1 {
		t.Fatalf("active plan = %+v ok=%v err=%v", stored, ok, err)
	}
	mode, err := svc.GetMode(created.ID)
	if err != nil || mode != ModeAuto {
		t.Fatalf("mode = %q err=%v", mode, err)
	}

	// Replay the exact canonical mutation input and verify no additional events.
	input := SessionMutationInput{SessionID: created.ID, UserID: created.UserID, AccountScopeID: created.AccountScopeID, ClientRequestID: first.Mutation.Idempotency.ClientRequestID, IdempotencyKey: first.Mutation.Idempotency.Key, PayloadHash: first.Mutation.PayloadHash, RequestHash: first.Mutation.Idempotency.RequestHash, Kind: SessionMutationAcceptPlan, PlanAcceptance: &pebblestore.V3PlanAcceptanceMutation{Plan: first.Plan, Session: first.Session, PlanEventPayload: first.Mutation.Events[0].Payload, ModeEventPayload: first.Mutation.Events[1].Payload}}
	replayed, err := svc.ApplySessionMutation(input)
	if err != nil || !replayed.Replayed || replayed.FirstSeq != first.Mutation.FirstSeq || replayed.LastSeq != first.Mutation.LastSeq {
		t.Fatalf("replay = %+v err=%v", replayed, err)
	}

	conflictInput := input
	conflictInput.PayloadHash = "different-plan-acceptance-payload"
	conflictInput.RequestHash = conflictInput.PayloadHash
	conflicted, err := svc.ApplySessionMutation(conflictInput)
	if !errors.Is(err, ErrSessionIdempotencyConflict) {
		t.Fatalf("idempotency conflict = %+v err=%v", conflicted, err)
	}
	if conflicted.ResponseStatus != pebblestore.V3SessionMutationStatusConflict || conflicted.Conflict == nil {
		t.Fatalf("idempotency conflict result = %+v", conflicted)
	}
}
