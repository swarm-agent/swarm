package pebblestore

import "testing"

func TestBuildV3SessionWorksetUsesOneSnapshotForSessionsAndEndpointWatermark(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3WorksetSessionForTest(t, sessions, "snapshot-before", "/workspace/snapshot", 1000)

	cleanup := setV3SessionWorksetAfterSnapshotHookForTest(func() {
		createV3WorksetSessionForTest(t, sessions, "snapshot-racing", "/workspace/snapshot", 2000)
	})
	defer cleanup()

	workset, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID: "account-1",
		WorkspacePath:  "/workspace/snapshot",
		RecentLimit:    10,
		History:        V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build workset: %v", err)
	}
	if got := workset.SessionsByID["snapshot-before"].ID; got != "snapshot-before" {
		t.Fatalf("snapshot missing pre-existing session: %+v", workset.SessionsByID)
	}
	if got := workset.SessionsByID["snapshot-racing"].ID; got != "" {
		t.Fatalf("racing session leaked into pre-mutation snapshot: %+v", workset.SessionsByID)
	}
	if workset.Rev != 1 {
		t.Fatalf("workset rev = %d, want snapshot rev before racing mutation", workset.Rev)
	}
	current, err := sessions.CurrentV3RealtimeOutboxRevision()
	if err != nil {
		t.Fatalf("current outbox revision: %v", err)
	}
	if current <= workset.Rev {
		t.Fatalf("test setup failed: current=%d workset.rev=%d", current, workset.Rev)
	}
}
