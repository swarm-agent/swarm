package pebblestore

import "testing"

func TestBuildV3SyncSnapshotKeepsExtraResourcesPointInTime(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	permissions := NewPermissionStore(store)
	createV3WorksetSessionForTest(t, sessions, "session-sync-snapshot", "/workspace/sync-snapshot", 1000)
	if err := sessions.PutUsageSummary(SessionUsageSummary{SessionID: "session-sync-snapshot", UserID: "user-1", AccountScopeID: "account-1", Provider: "before", Model: "before", InputTokens: 1, OutputTokens: 2}); err != nil {
		t.Fatalf("put initial usage: %v", err)
	}
	if err := permissions.PutPermission(PermissionRecord{ID: "permission-before", SessionID: "session-sync-snapshot", Status: PermissionStatusPending, CreatedAt: 10, UpdatedAt: 10}, nil); err != nil {
		t.Fatalf("put initial permission: %v", err)
	}
	if err := sessions.PutPlan(SessionPlanSnapshot{ID: "plan-sync", SessionID: "session-sync-snapshot", UserID: "user-1", AccountScopeID: "account-1", Title: "Plan Before", Plan: "# Before", Status: "draft", ApprovalState: "draft", Version: 1, CreatedAt: 10, UpdatedAt: 10}); err != nil {
		t.Fatalf("put initial plan: %v", err)
	}
	if err := sessions.SetActivePlan("session-sync-snapshot", "plan-sync", 10); err != nil {
		t.Fatalf("set initial active plan: %v", err)
	}

	var hookErr error
	restore := setV3SyncSnapshotAfterSnapshotHookForTest(func() {
		if err := sessions.PutUsageSummary(SessionUsageSummary{SessionID: "session-sync-snapshot", UserID: "user-1", AccountScopeID: "account-1", Provider: "after", Model: "after", InputTokens: 99, OutputTokens: 100}); err != nil {
			hookErr = err
			return
		}
		if err := permissions.PutPermission(PermissionRecord{ID: "permission-after", SessionID: "session-sync-snapshot", Status: PermissionStatusPending, CreatedAt: 20, UpdatedAt: 20}, nil); err != nil {
			hookErr = err
			return
		}
		if err := sessions.PutPlan(SessionPlanSnapshot{ID: "plan-sync", SessionID: "session-sync-snapshot", UserID: "user-1", AccountScopeID: "account-1", Title: "Plan After", Plan: "# After", Status: "draft", ApprovalState: "draft", Version: 2, CreatedAt: 10, UpdatedAt: 20}); err != nil {
			hookErr = err
			return
		}
		if err := sessions.DeleteSession("session-sync-snapshot"); err != nil {
			hookErr = err
			return
		}
	})
	defer restore()

	snapshot, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID:        "account-1",
		SessionIDs:            []string{"session-sync-snapshot"},
		History:               V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
		IncludeActivePlan:     true,
		IncludePlanRevisions:  true,
		IncludeActiveSessions: true,
	})
	if err != nil {
		t.Fatalf("build sync snapshot: %v", err)
	}
	if hookErr != nil {
		t.Fatalf("after snapshot hook mutation: %v", hookErr)
	}
	if snapshot.SessionsByID["session-sync-snapshot"].ID != "session-sync-snapshot" {
		t.Fatalf("snapshot lost session after concurrent delete: %+v", snapshot.SessionsByID)
	}
	if _, ok := snapshot.TombstonesBySession["session-sync-snapshot"]; ok {
		t.Fatalf("snapshot included post-snapshot tombstone: %+v", snapshot.TombstonesBySession["session-sync-snapshot"])
	}
	usage := snapshot.UsageBySession["session-sync-snapshot"]
	if usage.Provider != "before" || usage.InputTokens != 1 || usage.OutputTokens != 2 {
		t.Fatalf("snapshot usage = %+v, want pre-hook usage", usage)
	}
	pending := snapshot.PermissionsBySession["session-sync-snapshot"]
	if len(pending) != 1 || pending[0].ID != "permission-before" {
		t.Fatalf("snapshot permissions = %+v, want only pre-hook permission", pending)
	}
	plan := snapshot.PlansBySession["session-sync-snapshot"]
	if plan.ID != "plan-sync" || plan.Title != "Plan Before" || !plan.Active || plan.Version != 1 {
		t.Fatalf("snapshot active plan = %+v, want pre-hook active plan", plan)
	}
	revisions := snapshot.PlanRevisionsBySession["session-sync-snapshot"]
	if len(revisions) != 1 || revisions[0].Version != 1 || revisions[0].Title != "Plan Before" {
		t.Fatalf("snapshot plan revisions = %+v, want only pre-hook revision", revisions)
	}
}

func TestBuildV3SyncSnapshotGlobalSelectorWithoutRecentIncludesAccountSessions(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3WorksetSessionForTest(t, sessions, "session-global-a", "/workspace/global-a", 1000)
	createV3WorksetSessionForTest(t, sessions, "session-global-b", "/workspace/global-b", 2000)
	createV3SyncSnapshotSessionForAccountTest(t, sessions, "session-global-other", "account-other", "/workspace/global-other", 3000)

	snapshot, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID: "account-1",
		Global:         true,
		History:        V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build global sync snapshot: %v", err)
	}
	if snapshot.SessionsByID["session-global-a"].ID != "session-global-a" || snapshot.SessionsByID["session-global-b"].ID != "session-global-b" {
		t.Fatalf("global snapshot missing account sessions: order=%+v sessions=%+v", snapshot.SessionOrder, snapshot.SessionsByID)
	}
	if _, ok := snapshot.SessionsByID["session-global-other"]; ok {
		t.Fatalf("global snapshot leaked other account session: %+v", snapshot.SessionsByID["session-global-other"])
	}
	if len(snapshot.SessionOrder) != 2 || snapshot.SessionOrder[0] != "session-global-b" || snapshot.SessionOrder[1] != "session-global-a" {
		t.Fatalf("global snapshot order = %+v, want updated_at desc", snapshot.SessionOrder)
	}
}

func createV3SyncSnapshotSessionForAccountTest(t *testing.T, sessions *SessionStore, sessionID, accountScopeID, workspacePath string, updatedAt int64) {
	t.Helper()
	_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      sessionID,
		UserID:         "user-1",
		AccountScopeID: accountScopeID,
		IdempotencyKey: "create-" + sessionID,
		PayloadHash:    "hash-create-" + sessionID,
		Kind:           V3SessionMutationCreateSession,
		Session: &SessionSnapshot{
			ID:             sessionID,
			UserID:         "user-1",
			AccountScopeID: accountScopeID,
			WorkspacePath:  workspacePath,
			WorkspaceName:  "workspace",
			Title:          sessionID,
			CreatedAt:      updatedAt,
			UpdatedAt:      updatedAt,
		},
		NowUnixMs: updatedAt,
	})
	if err != nil {
		t.Fatalf("create sync snapshot session %s: %v", sessionID, err)
	}
}
