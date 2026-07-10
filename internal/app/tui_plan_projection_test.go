package app

import (
	"encoding/json"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
)

func TestTUISessionStoreProjectsHydratedAndRealtimePlanExecution(t *testing.T) {
	store := newTUISessionStore()
	store.MergeHydrated(client.SessionV3Hydrated{Session: client.SessionSummary{ID: "s1"}, Projection: client.SessionV3Projection{SessionID: "s1"}, ActivePlan: &client.SessionPlan{ID: "p1", Active: true, Version: 1, Document: &client.SessionPlanDocument{ExecutionPolicy: client.SessionPlanExecutionPolicy{Mode: "automatic"}}}, PlanRevisions: []client.SessionPlan{{ID: "p1", Version: 1}}, ActiveRunIntent: &client.SessionV3RunIntent{SessionID: "s1", RunID: "r1", Status: "accepted"}})
	snapshot, ok := store.ChatSnapshot("s1")
	if !ok || len(snapshot.Plans) != 1 || snapshot.ActiveRunIntent == nil || snapshot.ActiveRunIntent.RunID != "r1" {
		t.Fatalf("hydrated execution projection missing: %#v", snapshot)
	}
	payload, _ := json.Marshal(map[string]any{"plan": client.SessionPlan{ID: "p1", Active: true, Version: 2, Document: &client.SessionPlanDocument{ActiveCheckpointID: "cp-2"}}, "run_intent": client.SessionV3RunIntent{SessionID: "s1", RunID: "r2", Status: "pending_executor"}})
	result := store.ApplyRealtimeFrame(client.V3RealtimeFrame{Kind: "event", SessionID: "s1", Event: &client.SessionV3Event{ID: "e1", SessionID: "s1", Seq: 2, EventType: "plan.saved", Payload: payload}})
	if !result.Changed {
		t.Fatal("realtime plan event did not change store")
	}
	snapshot, _ = store.ChatSnapshot("s1")
	if snapshot.Plans[0].Version != 2 || snapshot.Plans[0].Document.ActiveCheckpointID != "cp-2" || snapshot.ActiveRunIntent.RunID != "r2" {
		t.Fatalf("realtime execution state stale: %#v", snapshot)
	}
	if len(snapshot.PlanRevisions) == 0 || snapshot.PlanRevisions[0].Version != 1 {
		t.Fatalf("prior revision not retained: %#v", snapshot.PlanRevisions)
	}
}
