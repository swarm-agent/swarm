package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/client"
)

func TestSessionManagerMapsGlobalV3SyncOrderAndActivity(t *testing.T) {
	snapshot := client.SessionV3SyncSnapshot{
		SessionsByID: map[string]client.SessionSummary{
			"session-a": {ID: "session-a", WorkspacePath: "/workspace-a", Title: "A", UpdatedAt: 1000},
			"session-b": {ID: "session-b", WorkspacePath: "/workspace-b", Title: "B", UpdatedAt: 2000},
		},
		ProjectionsBySession: map[string]client.SessionV3Projection{
			"session-a": {SessionID: "session-a", LastEventSeq: 3, ProjectionHighWatermarkSeq: 4},
			"session-b": {SessionID: "session-b", LastEventSeq: 5, ProjectionHighWatermarkSeq: 6},
		},
		CurrentRunStateBySession: map[string]client.SessionV3RunState{
			"session-a": {SessionID: "session-a", RunID: "run-a", Active: true, Status: "running", StartedAt: 900, UpdatedAt: 1000},
		},
		PermissionSummariesBySession: map[string]client.SessionV3SyncPermissionSummary{
			"session-b": {SessionID: "session-b", PendingApprovalCount: 2},
		},
		ActiveSessionIDs: []string{"session-a"},
		SessionOrder:     []string{"session-b", "session-a"},
	}

	summaries := modelSessionSummariesFromV3SyncSnapshot(snapshot)
	if len(summaries) != 2 || summaries[0].ID != "session-b" || summaries[1].ID != "session-a" {
		t.Fatalf("summaries = %#v", summaries)
	}
	if summaries[0].WorkspacePath != "/workspace-b" || summaries[0].PendingPermissionCount != 2 {
		t.Fatalf("global session summary was filtered or lost attention state: %#v", summaries[0])
	}
	if summaries[1].Lifecycle == nil || !summaries[1].Lifecycle.Active || summaries[1].ActiveRunIntent == nil || summaries[1].ActiveRunIntent.RunID != "run-a" {
		t.Fatalf("active session state was not mapped: %#v", summaries[1])
	}
	if summaries[1].SessionAPI != "v3" || summaries[1].LastEventSeq != 3 || summaries[1].ProjectionHighWatermarkSeq != 4 {
		t.Fatalf("projection state was not mapped: %#v", summaries[1])
	}
}
