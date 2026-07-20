package pebblestore

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestPutPlanWithArchivedRevisionAndEventCommitsOneAtomicPlanState(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sessions.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	sessions := NewSessionStore(store)
	if err := sessions.CreateSession(SessionSnapshot{ID: "session-plan-write", UserID: "user", AccountScopeID: "account", Title: "Plan write", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	events, err := NewEventLog(store)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}

	archived := SessionPlanSnapshot{ID: "plan-1", SessionID: "session-plan-write", UserID: "user", AccountScopeID: "account", Title: "Plan", Plan: "old", Version: 1, Active: false}
	plan := archived
	plan.Plan = "new"
	plan.Version = 2
	plan.ParentRevision = 1
	plan.Active = true
	eventPayload, err := json.Marshal(map[string]any{"session_id": plan.SessionID, "active_plan": plan, "version": plan.Version})
	if err != nil {
		t.Fatal(err)
	}

	envelope, err := sessions.PutPlanWithArchivedRevisionAndEvent(plan, &archived, true, 42, events, EventAppend{Stream: "session:" + plan.SessionID, EventType: "session.plan.saved", EntityID: plan.SessionID, Payload: eventPayload})
	if err != nil {
		t.Fatalf("atomic plan write: %v", err)
	}
	if envelope.GlobalSeq != 1 || envelope.EventType != "session.plan.saved" {
		t.Fatalf("event envelope = %#v", envelope)
	}
	stored, ok, err := sessions.GetPlan(plan.SessionID, plan.ID)
	if err != nil || !ok {
		t.Fatalf("get current plan: ok=%v err=%v", ok, err)
	}
	if stored.Version != 2 || stored.Plan != "new" {
		t.Fatalf("current plan = %#v", stored)
	}
	revision, ok, err := sessions.GetPlanRevision(plan.SessionID, plan.ID, 1)
	if err != nil || !ok {
		t.Fatalf("get archived revision: ok=%v err=%v", ok, err)
	}
	if revision.Plan != "old" || revision.Version != 1 {
		t.Fatalf("archived revision = %#v", revision)
	}
	active, ok, err := sessions.GetActivePlan(plan.SessionID)
	if err != nil || !ok {
		t.Fatalf("get active plan: ok=%v err=%v", ok, err)
	}
	if active.PlanID != plan.ID || active.UpdatedAt != 42 {
		t.Fatalf("active plan = %#v", active)
	}
	storedEvents, err := events.ReadFrom(envelope.GlobalSeq, 1)
	if err != nil || len(storedEvents) != 1 {
		t.Fatalf("read event: count=%d err=%v", len(storedEvents), err)
	}
	if storedEvents[0].EventType != "session.plan.saved" || string(storedEvents[0].Payload) != string(eventPayload) {
		t.Fatalf("stored event = %#v", storedEvents[0])
	}
}
