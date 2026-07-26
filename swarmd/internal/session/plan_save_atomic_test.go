package session

import (
	"errors"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestCommitPreparedPlanSaveFailureLeavesLegacyPlanStateUnchanged(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()
	sessionID := createPlanTestSession(t, svc)

	prepared, err := svc.PreparePlanSaveWithMetadata(sessionID, "plan-atomic", "Atomic", "# Atomic", "approved", "approved", true, PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{Title: "Atomic"}})
	if err != nil {
		t.Fatalf("prepare plan save: %v", err)
	}
	_, err = svc.CommitPreparedPlanSave(prepared, func(SessionMutationInput) (SessionMutationResult, error) {
		return SessionMutationResult{}, errors.New("injected mutation failure")
	})
	if err == nil {
		t.Fatal("commit prepared plan save succeeded, want injected failure")
	}
	if _, ok, getErr := svc.GetPlan(sessionID, prepared.Plan.ID); getErr != nil || ok {
		t.Fatalf("legacy plan changed after failed mutation: ok=%v err=%v", ok, getErr)
	}
	if _, ok, getErr := svc.GetActivePlan(sessionID); getErr != nil || ok {
		t.Fatalf("active plan changed after failed mutation: ok=%v err=%v", ok, getErr)
	}
}

func TestCommitPreparedPlanSaveCommitsPlanRevisionEventAndOutboxTogether(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()
	sessionID := createPlanTestSession(t, svc)

	first, err := svc.PreparePlanSaveWithMetadata(sessionID, "plan-atomic", "Atomic", "# Atomic", "approved", "approved", true, PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{Title: "Atomic"}})
	if err != nil {
		t.Fatalf("prepare initial plan save: %v", err)
	}
	firstResult, err := svc.CommitPreparedPlanSave(first, svc.ApplySessionMutation)
	if err != nil {
		t.Fatalf("commit initial plan save: %v", err)
	}
	replayedFirstResult, err := svc.CommitPreparedPlanSave(first, svc.ApplySessionMutation)
	if err != nil {
		t.Fatalf("replay initial plan save: %v", err)
	}
	if !replayedFirstResult.Replayed || replayedFirstResult.Plan == nil || replayedFirstResult.Plan.ID != first.Plan.ID || replayedFirstResult.Plan.Version != first.Plan.Version {
		t.Fatalf("replayed committed plan = %#v, replayed=%v", replayedFirstResult.Plan, replayedFirstResult.Replayed)
	}
	second, err := svc.PreparePlanSaveWithMetadata(sessionID, first.Plan.ID, "Atomic", "# Atomic\n\nUpdated", "approved", "approved", true, PlanSaveMetadata{UpdateSummary: "updated", Document: &pebblestore.SessionPlanDocument{Title: "Atomic"}})
	if err != nil {
		t.Fatalf("prepare revised plan save: %v", err)
	}
	result, err := svc.CommitPreparedPlanSave(second, svc.ApplySessionMutation)
	if err != nil {
		t.Fatalf("commit revised plan save: %v", err)
	}
	if result.Plan == nil || result.Plan.Version != 2 || result.Plan.ParentRevision != 1 {
		t.Fatalf("committed plan = %#v", result.Plan)
	}
	if result.Event.EventType != "session.plan.saved" || result.RealtimeOutbox == nil || result.RealtimeOutbox.EndpointSeq == 0 {
		t.Fatalf("missing committed event/outbox: event=%#v outbox=%#v", result.Event, result.RealtimeOutbox)
	}
	if firstResult.RealtimeOutbox == nil {
		t.Fatal("initial mutation did not return committed realtime outbox")
	}
	if firstResult.RealtimeOutbox.EndpointSeq >= result.RealtimeOutbox.EndpointSeq {
		t.Fatalf("outbox did not advance: first=%d second=%d", firstResult.RealtimeOutbox.EndpointSeq, result.RealtimeOutbox.EndpointSeq)
	}
	revision, ok, err := svc.GetPlanRevision(sessionID, first.Plan.ID, 1)
	if err != nil || !ok || revision.Version != 1 {
		t.Fatalf("archived revision missing: revision=%#v ok=%v err=%v", revision, ok, err)
	}
	active, ok, err := svc.GetActivePlan(sessionID)
	if err != nil || !ok || active.Version != 2 {
		t.Fatalf("active plan not committed with revision: active=%#v ok=%v err=%v", active, ok, err)
	}
}
