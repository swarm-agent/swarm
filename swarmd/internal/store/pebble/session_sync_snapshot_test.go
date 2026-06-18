package pebblestore

import (
	"fmt"
	"testing"
)

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

func TestBuildV3SyncSnapshotHydrateReturnsRequestedDeletedTombstoneBeyondAccountPage(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	for i := 0; i < 1005; i++ {
		sessionID := "session-tombstone-page-filler-" + string(rune('a'+(i%26))) + "-" + string(rune('a'+((i/26)%26))) + "-" + string(rune('a'+((i/676)%26)))
		createV3WorksetSessionForTest(t, sessions, sessionID, "/workspace/tombstone-other", int64(1000+i))
		if err := sessions.DeleteSession(sessionID); err != nil {
			t.Fatalf("delete filler session %s: %v", sessionID, err)
		}
	}
	createV3WorksetSessionForTest(t, sessions, "zzzz-session-tombstone-target", "/workspace/tombstone-target", 5000)
	if err := sessions.DeleteSession("zzzz-session-tombstone-target"); err != nil {
		t.Fatalf("delete target session: %v", err)
	}

	snapshot, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID: "account-1",
		UserID:         "user-1",
		SessionIDs:     []string{"zzzz-session-tombstone-target"},
		History:        V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build hydrate tombstone snapshot: %v", err)
	}
	tombstone := snapshot.TombstonesBySession["zzzz-session-tombstone-target"]
	if tombstone.SessionID != "zzzz-session-tombstone-target" || !tombstone.Deleted {
		t.Fatalf("hydrate snapshot missed requested deleted target tombstone beyond account page: %+v", snapshot.TombstonesBySession)
	}
}

func TestBuildV3SyncSnapshotTombstonesArePrincipalAndWorkspaceScoped(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-tombstone-workspace-a", "user-1", "account-1", "/workspace/tombstone-a", 1000)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-tombstone-workspace-b", "user-1", "account-1", "/workspace/tombstone-b", 1001)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-tombstone-other-user", "user-2", "account-1", "/workspace/tombstone-a", 1002)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-tombstone-legacy-empty-user", "", "account-1", "/workspace/tombstone-a", 1003)
	for _, sessionID := range []string{"session-tombstone-workspace-a", "session-tombstone-workspace-b", "session-tombstone-other-user", "session-tombstone-legacy-empty-user"} {
		if err := sessions.DeleteSession(sessionID); err != nil {
			t.Fatalf("delete %s: %v", sessionID, err)
		}
	}

	snapshot, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID: "account-1",
		UserID:         "user-1",
		WorkspacePath:  "/workspace/tombstone-a",
		RecentLimit:    10,
		History:        V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build workspace tombstone snapshot: %v", err)
	}
	if snapshot.TombstonesBySession["session-tombstone-workspace-a"].SessionID != "session-tombstone-workspace-a" {
		t.Fatalf("workspace snapshot missing same-user same-workspace tombstone: %+v", snapshot.TombstonesBySession)
	}
	for _, leaked := range []string{"session-tombstone-workspace-b", "session-tombstone-other-user", "session-tombstone-legacy-empty-user"} {
		if _, ok := snapshot.TombstonesBySession[leaked]; ok {
			t.Fatalf("workspace tombstone snapshot leaked %s: %+v", leaked, snapshot.TombstonesBySession)
		}
	}
}

func createV3SyncSnapshotSessionForUserTest(t *testing.T, sessions *SessionStore, sessionID, userID, accountScopeID, workspacePath string, updatedAt int64) {
	t.Helper()
	_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      sessionID,
		UserID:         userID,
		AccountScopeID: accountScopeID,
		IdempotencyKey: "create-" + sessionID,
		PayloadHash:    "hash-create-" + sessionID,
		Kind:           V3SessionMutationCreateSession,
		Session: &SessionSnapshot{
			ID:             sessionID,
			UserID:         userID,
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

func TestBuildV3SyncSnapshotGlobalRecentTombstonesExcludeOldUnrelatedHistory(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	for i := 0; i < 5; i++ {
		sessionID := fmt.Sprintf("session-global-recent-old-tombstone-%02d", i)
		createV3SyncSnapshotSessionForUserTest(t, sessions, sessionID, "user-1", "account-1", "/workspace/old-tombstones", int64(1000+i))
		if err := sessions.DeleteSession(sessionID); err != nil {
			t.Fatalf("delete old tombstone %s: %v", sessionID, err)
		}
	}
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-global-recent-live", "user-1", "account-1", "/workspace/live", 5000)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-global-recent-current-tombstone", "user-1", "account-1", "/workspace/live", 6000)
	if err := sessions.DeleteSession("session-global-recent-current-tombstone"); err != nil {
		t.Fatalf("delete current tombstone: %v", err)
	}

	snapshot, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID: "account-1",
		UserID:         "user-1",
		RecentLimit:    1,
		History:        V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build global recent snapshot: %v", err)
	}
	if len(snapshot.TombstonesBySession) > 1 {
		t.Fatalf("global recent returned historical account tombstones: %+v", snapshot.TombstonesBySession)
	}
}

func TestBuildV3SyncSnapshotWorkspaceRecentTombstonesAreLimitBounded(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	for i := 0; i < 5; i++ {
		sessionID := fmt.Sprintf("session-workspace-recent-tombstone-%02d", i)
		createV3SyncSnapshotSessionForUserTest(t, sessions, sessionID, "user-1", "account-1", "/workspace/recent-tombstones", int64(2000+i))
		if err := sessions.DeleteSession(sessionID); err != nil {
			t.Fatalf("delete workspace tombstone %s: %v", sessionID, err)
		}
	}

	snapshot, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID: "account-1",
		UserID:         "user-1",
		WorkspacePath:  "/workspace/recent-tombstones",
		RecentLimit:    2,
		History:        V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build workspace recent tombstone snapshot: %v", err)
	}
	if len(snapshot.TombstonesBySession) != 2 {
		t.Fatalf("workspace recent tombstones len=%d, want 2: %+v", len(snapshot.TombstonesBySession), snapshot.TombstonesBySession)
	}
	if !snapshot.Pagination.HasMore || snapshot.Pagination.NextBeforeUpdatedAt == nil || snapshot.Pagination.NextBeforeSessionID == "" {
		t.Fatalf("workspace recent tombstones missing deterministic pagination: %+v", snapshot.Pagination)
	}
}

func TestBuildV3SyncSnapshotRecentTombstoneSecondPageUsesBeforeCursor(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	for i := 0; i < 4; i++ {
		sessionID := fmt.Sprintf("session-recent-tombstone-page-%02d", i)
		createV3SyncSnapshotSessionForUserTest(t, sessions, sessionID, "user-1", "account-1", "/workspace/tombstone-pages", int64(3000+i))
		if err := sessions.DeleteSession(sessionID); err != nil {
			t.Fatalf("delete tombstone page %s: %v", sessionID, err)
		}
	}

	first, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID: "account-1",
		UserID:         "user-1",
		WorkspacePath:  "/workspace/tombstone-pages",
		RecentLimit:    2,
		History:        V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build first tombstone page: %v", err)
	}
	if !first.Pagination.HasMore || first.Pagination.NextBeforeUpdatedAt == nil {
		t.Fatalf("first page missing pagination: %+v", first.Pagination)
	}
	second, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID:        "account-1",
		UserID:                "user-1",
		WorkspacePath:         "/workspace/tombstone-pages",
		RecentLimit:           2,
		RecentBeforeUpdatedAt: first.Pagination.NextBeforeUpdatedAt,
		RecentBeforeSessionID: first.Pagination.NextBeforeSessionID,
		History:               V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build second tombstone page: %v", err)
	}
	if len(second.TombstonesBySession) != 2 {
		t.Fatalf("second page tombstones len=%d, want 2: %+v", len(second.TombstonesBySession), second.TombstonesBySession)
	}
	for sessionID := range second.TombstonesBySession {
		if _, duplicated := first.TombstonesBySession[sessionID]; duplicated {
			t.Fatalf("second tombstone page duplicated first page session %s: first=%+v second=%+v", sessionID, first.TombstonesBySession, second.TombstonesBySession)
		}
	}
}

func TestBuildV3SyncSnapshotLargeAccountTombstonesRemainBounded(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	for i := 0; i < 1005; i++ {
		sessionID := fmt.Sprintf("session-large-account-tombstone-%04d", i)
		createV3SyncSnapshotSessionForUserTest(t, sessions, sessionID, "user-1", "account-1", "/workspace/large-account", int64(4000+i))
		if err := sessions.DeleteSession(sessionID); err != nil {
			t.Fatalf("delete large account tombstone %s: %v", sessionID, err)
		}
	}

	snapshot, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID: "account-1",
		UserID:         "user-1",
		Global:         true,
		History:        V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build large account snapshot: %v", err)
	}
	if len(snapshot.TombstonesBySession) > 1000 {
		t.Fatalf("large account snapshot loaded unbounded tombstones: %d", len(snapshot.TombstonesBySession))
	}
	if len(snapshot.Omissions) == 0 {
		t.Fatalf("large account bounded tombstone scan did not report deterministic omission metadata")
	}
}

func TestBuildV3SyncSnapshotTargetedEmptyUserTombstoneReturnsOmission(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-targeted-empty-user-tombstone", "", "account-1", "/workspace/legacy", 5000)
	if err := sessions.DeleteSession("session-targeted-empty-user-tombstone"); err != nil {
		t.Fatalf("delete empty-user tombstone: %v", err)
	}

	snapshot, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID: "account-1",
		UserID:         "user-1",
		SessionIDs:     []string{"session-targeted-empty-user-tombstone"},
		History:        V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build targeted empty-user tombstone snapshot: %v", err)
	}
	if len(snapshot.TombstonesBySession) != 0 {
		t.Fatalf("empty-user tombstone leaked: %+v", snapshot.TombstonesBySession)
	}
	if len(snapshot.Omissions) != 1 || snapshot.Omissions[0].SessionID != "session-targeted-empty-user-tombstone" || snapshot.Omissions[0].Resource != "tombstones" || snapshot.Omissions[0].Reason != "bootstrap_required" {
		t.Fatalf("empty-user tombstone omission = %+v, want bootstrap_required tombstone omission", snapshot.Omissions)
	}
}
