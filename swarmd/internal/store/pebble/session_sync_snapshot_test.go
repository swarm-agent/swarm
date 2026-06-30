package pebblestore

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble"
)

func TestBuildV3SyncSnapshotOmitsAllSessionResourceMaps(t *testing.T) {
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
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal sync snapshot: %v", err)
	}
	for _, forbidden := range []string{"permissions_by_session", "usage_by_session", "plans_by_session", "plan_revisions_by_session"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("sync snapshot emitted removed all-session resource %q: %s", forbidden, string(raw))
		}
	}
}

func TestBuildV3SyncSnapshotHistoryNoneOmitsPerSessionMessageAndEventKeys(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-sync-metadata-only")
	appendV3SessionMessageForStoreTest(t, sessions, "session-sync-metadata-only", "message-sync-metadata-only", "hello metadata", "user-1", "account-1")

	snapshot, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID: "account-1",
		UserID:         "user-1",
		SessionIDs:     []string{"session-sync-metadata-only"},
		History:        V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build metadata-only sync snapshot: %v", err)
	}
	if snapshot.MessagesBySession == nil || snapshot.EventsBySession == nil || snapshot.HistoryManifestsBySession == nil {
		t.Fatalf("top-level history maps must be non-nil: messages=%v events=%v manifests=%v", snapshot.MessagesBySession, snapshot.EventsBySession, snapshot.HistoryManifestsBySession)
	}
	if _, ok := snapshot.MessagesBySession["session-sync-metadata-only"]; ok {
		t.Fatalf("history none manufactured messages key: %+v", snapshot.MessagesBySession)
	}
	if _, ok := snapshot.EventsBySession["session-sync-metadata-only"]; ok {
		t.Fatalf("history none manufactured events key: %+v", snapshot.EventsBySession)
	}
	if _, ok := snapshot.HistoryManifestsBySession["session-sync-metadata-only"]; ok {
		t.Fatalf("history none manufactured history manifest key: %+v", snapshot.HistoryManifestsBySession)
	}
}

func TestBuildV3SyncSnapshotRequestedZeroMessageHistoryReturnsAuthoritativeEmptyKey(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-sync-zero-messages")

	snapshot, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID: "account-1",
		UserID:         "user-1",
		SessionIDs:     []string{"session-sync-zero-messages"},
		History: V3SyncSnapshotHistoryOptions{
			Mode:                  V3SyncSnapshotHistoryModeTail,
			IncludeMessages:       true,
			MaxMessagesPerSession: 10,
		},
	})
	if err != nil {
		t.Fatalf("build requested zero-message sync snapshot: %v", err)
	}
	messages, ok := snapshot.MessagesBySession["session-sync-zero-messages"]
	if !ok {
		t.Fatalf("requested message history omitted authoritative empty key: %+v", snapshot.MessagesBySession)
	}
	if len(messages) != 0 {
		t.Fatalf("requested zero-message history = %+v, want empty slice", messages)
	}
}

func TestBuildV3SyncSnapshotRunIntentsResourceAuthority(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-sync-run-intents")
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-sync-run-intents",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "run-intent-sync-1",
		PayloadHash:    "hash-run-intent-sync-1",
		Kind:           V3SessionMutationAppendMessage,
		Message:        &MessageSnapshot{Role: "user", Content: "run intent"},
		RunIntent:      &V3SessionRunIntent{RunID: "run-sync", Status: V3RunIntentPendingExecutor},
		NowUnixMs:      2000,
	}); err != nil {
		t.Fatalf("create run intent: %v", err)
	}

	metadataOnly, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID: "account-1",
		UserID:         "user-1",
		SessionIDs:     []string{"session-sync-run-intents"},
		History:        V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build metadata-only sync snapshot: %v", err)
	}
	if _, ok := metadataOnly.RunIntentsBySession["session-sync-run-intents"]; ok {
		t.Fatalf("metadata-only snapshot emitted run intents key: %+v", metadataOnly.RunIntentsBySession)
	}

	withIntents, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID:    "account-1",
		UserID:            "user-1",
		SessionIDs:        []string{"session-sync-run-intents"},
		History:           V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
		IncludeRunIntents: true,
	})
	if err != nil {
		t.Fatalf("build run-intents sync snapshot: %v", err)
	}
	if got := withIntents.RunIntentsBySession["session-sync-run-intents"]; len(got) != 1 || got[0].RunID != "run-sync" {
		t.Fatalf("requested run intents = %+v", got)
	}
}

func TestBuildV3SyncSnapshotRequestedZeroRunIntentsReturnsAuthoritativeEmptyKey(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-sync-zero-run-intents")

	snapshot, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID:    "account-1",
		UserID:            "user-1",
		SessionIDs:        []string{"session-sync-zero-run-intents"},
		History:           V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
		IncludeRunIntents: true,
	})
	if err != nil {
		t.Fatalf("build requested zero-run-intents sync snapshot: %v", err)
	}
	intents, ok := snapshot.RunIntentsBySession["session-sync-zero-run-intents"]
	if !ok {
		t.Fatalf("requested run intents omitted authoritative empty key: %+v", snapshot.RunIntentsBySession)
	}
	if len(intents) != 0 {
		t.Fatalf("requested zero-run-intents = %+v, want empty slice", intents)
	}
}

func TestBuildV3SyncSnapshotIncludeActiveDoesNotScanRunIntentHistory(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "session-sync-active-current-state")
	pending := V3SessionRunIntent{SessionID: "session-sync-active-current-state", UserID: "user-1", AccountScopeID: "account-1", RunID: "run-sync-active-current-state", Status: V3RunIntentPendingExecutor, CreatedAt: 2000, UpdatedAt: 2000}
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      "session-sync-active-current-state",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "run-intent-sync-active-current-state",
		PayloadHash:    "hash-run-intent-sync-active-current-state",
		Kind:           V3SessionMutationAppendMessage,
		RunIntent:      &pending,
		NowUnixMs:      2000,
	}); err != nil {
		t.Fatalf("create active run intent: %v", err)
	}
	if err := store.Delete(KeyV3SessionRunIntent(pending.SessionID, pending.RunID)); err != nil {
		t.Fatalf("delete historical run-intent record: %v", err)
	}
	if err := store.Delete(KeyV3SessionRunIntentStatus(pending.Status, pending.UpdatedAt, pending.AccountScopeID, pending.SessionID, pending.RunID)); err != nil {
		t.Fatalf("delete historical run-intent status record: %v", err)
	}

	snapshot, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID:        "account-1",
		UserID:                "user-1",
		SessionIDs:            []string{"session-sync-active-current-state"},
		History:               V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
		IncludeActiveSessions: true,
	})
	if err != nil {
		t.Fatalf("build include-active sync snapshot: %v", err)
	}
	if _, ok := snapshot.RunIntentsBySession["session-sync-active-current-state"]; ok {
		t.Fatalf("include active emitted historical run intents without IncludeRunIntents: %+v", snapshot.RunIntentsBySession)
	}
	state, ok := snapshot.CurrentRunStateBySession["session-sync-active-current-state"]
	if !ok || state.RunID != pending.RunID || !state.Active {
		t.Fatalf("current run state = %+v, ok=%v", state, ok)
	}
	if len(snapshot.ActiveSessionIDs) != 1 || snapshot.ActiveSessionIDs[0] != "session-sync-active-current-state" {
		t.Fatalf("active_session_ids = %+v", snapshot.ActiveSessionIDs)
	}

	withHistory, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID:        "account-1",
		UserID:                "user-1",
		SessionIDs:            []string{"session-sync-active-current-state"},
		History:               V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
		IncludeActiveSessions: true,
		IncludeRunIntents:     true,
	})
	if err != nil {
		t.Fatalf("build include-active run-intent sync snapshot: %v", err)
	}
	intents, ok := withHistory.RunIntentsBySession["session-sync-active-current-state"]
	if !ok || len(intents) != 0 {
		t.Fatalf("explicit run intents after historical records were removed = %+v, ok=%v", intents, ok)
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
		UserID:         "user-1",
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

func TestBuildV3SyncSnapshotIncludesPinnedSessionsOutsideRecentLimit(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-sync-pinned-old", "user-1", "account-1", "/workspace/sync-pinned", 1000)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-sync-recent-new", "user-1", "account-1", "/workspace/sync-pinned", 4000)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-sync-recent-mid", "user-1", "account-1", "/workspace/sync-pinned", 3000)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-sync-other-workspace-pinned", "user-1", "account-1", "/workspace/other", 2000)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-sync-other-user-pinned", "user-2", "account-1", "/workspace/sync-pinned", 5000)

	for _, sessionID := range []string{"session-sync-pinned-old", "session-sync-other-workspace-pinned", "session-sync-other-user-pinned"} {
		session, ok, err := sessions.GetSession(sessionID)
		if err != nil || !ok {
			t.Fatalf("load %s: ok=%t err=%v", sessionID, ok, err)
		}
		session.Metadata = map[string]any{V3SessionDesktopSidebarPinnedMetadataKey: true}
		if err := sessions.UpdateSession(session); err != nil {
			t.Fatalf("pin %s: %v", sessionID, err)
		}
	}

	snapshot, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID:               "account-1",
		UserID:                       "user-1",
		WorkspacePath:                "/workspace/sync-pinned",
		RecentLimit:                  2,
		IncludePinnedSidebarSessions: true,
		History:                      V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build pinned sync snapshot: %v", err)
	}
	if got := snapshot.SessionOrder; len(got) != 3 || got[0] != "session-sync-recent-new" || got[1] != "session-sync-recent-mid" || got[2] != "session-sync-pinned-old" {
		t.Fatalf("pinned sync snapshot order = %+v", got)
	}
	if snapshot.SessionsByID["session-sync-pinned-old"].Metadata[V3SessionDesktopSidebarPinnedMetadataKey] != true {
		t.Fatalf("pinned metadata not preserved: %+v", snapshot.SessionsByID["session-sync-pinned-old"].Metadata)
	}
	if _, ok := snapshot.SessionsByID["session-sync-other-workspace-pinned"]; ok {
		t.Fatalf("pinned sync snapshot leaked other workspace session")
	}
	if _, ok := snapshot.SessionsByID["session-sync-other-user-pinned"]; ok {
		t.Fatalf("pinned sync snapshot leaked other user session")
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
		seedLegacyV3SessionTombstoneForTest(t, sessions, V3SessionTombstone{SessionID: sessionID, UserID: "user-1", AccountScopeID: "account-1", WorkspacePath: "/workspace/old-tombstones", Kind: "deleted", Deleted: true, UpdatedAt: int64(1000 + i)})
	}
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-global-recent-live", "user-1", "account-1", "/workspace/live", 5000)
	seedLegacyV3SessionTombstoneForTest(t, sessions, V3SessionTombstone{SessionID: "session-global-recent-current-tombstone", UserID: "user-1", AccountScopeID: "account-1", WorkspacePath: "/workspace/live", Kind: "deleted", Deleted: true, UpdatedAt: 6000})

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

func TestBuildV3SyncSnapshotGlobalPrincipalReturnsAllTombstonesBeyondLegacyBound(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	for i := 0; i < 1005; i++ {
		sessionID := fmt.Sprintf("session-large-principal-tombstone-%04d", i)
		createV3SyncSnapshotSessionForUserTest(t, sessions, sessionID, "user-1", "account-1", "/workspace/large-principal", int64(4000+i))
		if err := sessions.DeleteSession(sessionID); err != nil {
			t.Fatalf("delete principal tombstone %s: %v", sessionID, err)
		}
	}
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-large-principal-other-user", "user-2", "account-1", "/workspace/large-principal", 7000)
	if err := sessions.DeleteSession("session-large-principal-other-user"); err != nil {
		t.Fatalf("delete other-user tombstone: %v", err)
	}
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-large-principal-blank-user", "", "account-1", "/workspace/large-principal", 7001)
	if err := sessions.DeleteSession("session-large-principal-blank-user"); err != nil {
		t.Fatalf("delete blank-user tombstone: %v", err)
	}

	snapshot, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID: "account-1",
		UserID:         "user-1",
		Global:         true,
		History:        V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build global principal tombstone snapshot: %v", err)
	}
	if len(snapshot.TombstonesBySession) != 1005 {
		t.Fatalf("global principal tombstones len=%d, want 1005", len(snapshot.TombstonesBySession))
	}
	for i := 0; i < 1005; i++ {
		sessionID := fmt.Sprintf("session-large-principal-tombstone-%04d", i)
		if tombstone := snapshot.TombstonesBySession[sessionID]; tombstone.SessionID != sessionID || !tombstone.Deleted {
			t.Fatalf("global principal snapshot missed %s: %+v", sessionID, tombstone)
		}
	}
	for _, leaked := range []string{"session-large-principal-other-user", "session-large-principal-blank-user"} {
		if _, ok := snapshot.TombstonesBySession[leaked]; ok {
			t.Fatalf("global principal snapshot leaked %s", leaked)
		}
	}
}

func TestBuildV3SyncSnapshotNonGlobalBoundedSelectorTombstonesStillFailClosed(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	for i := 0; i < 1005; i++ {
		sessionID := fmt.Sprintf("session-large-workspace-tombstone-%04d", i)
		createV3SyncSnapshotSessionForUserTest(t, sessions, sessionID, "user-1", "account-1", "/workspace/large-workspace", int64(5000+i))
		if err := sessions.DeleteSession(sessionID); err != nil {
			t.Fatalf("delete workspace tombstone %s: %v", sessionID, err)
		}
	}

	_, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID: "account-1",
		UserID:         "user-1",
		WorkspacePath:  "/workspace/large-workspace",
		History:        V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
	})
	if err == nil || !strings.Contains(err.Error(), "retry with recent.limit and pagination") {
		t.Fatalf("large non-global snapshot error=%v, want fail-closed pagination error", err)
	}
}

func TestBuildV3SyncSnapshotDoesNotBackfillLegacyTombstoneScopeIndexes(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	legacy := V3SessionTombstone{SessionID: "session-legacy-tombstone-backfill", UserID: "user-1", AccountScopeID: "account-1", WorkspacePath: "/workspace/legacy-tombstone", Kind: "deleted", Deleted: true, UpdatedAt: 7000}
	seedLegacyV3SessionTombstoneForTest(t, sessions, legacy)

	global, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID: "account-1",
		UserID:         "user-1",
		Global:         true,
		History:        V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build global legacy tombstone snapshot: %v", err)
	}
	if len(global.TombstonesBySession) != 0 {
		t.Fatalf("global snapshot returned legacy tombstone from request-path backfill: %+v", global.TombstonesBySession)
	}

	recent, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID: "account-1",
		UserID:         "user-1",
		RecentLimit:    10,
		History:        V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build recent legacy tombstone snapshot: %v", err)
	}
	if len(recent.TombstonesBySession) != 0 {
		t.Fatalf("recent snapshot returned legacy tombstone from request-path backfill: %+v", recent.TombstonesBySession)
	}
	if ok, err := store.GetJSON(KeyV3SessionTombstoneByAccountUser(legacy.AccountScopeID, legacy.UserID, legacy.UpdatedAt, legacy.SessionID), &V3SessionTombstone{}); err != nil || ok {
		t.Fatalf("request path backfilled account+user index ok=%v err=%v", ok, err)
	}
	if ok, err := store.GetJSON(KeyV3SessionTombstoneByAccountUserWorkspace(legacy.AccountScopeID, legacy.UserID, legacy.WorkspacePath, legacy.UpdatedAt, legacy.SessionID), &V3SessionTombstone{}); err != nil || ok {
		t.Fatalf("request path backfilled account+user+workspace index ok=%v err=%v", ok, err)
	}
}

func seedLegacyV3SessionTombstoneForTest(t *testing.T, sessions *SessionStore, tombstone V3SessionTombstone) {
	t.Helper()
	if sessions == nil || sessions.store == nil {
		t.Fatalf("session store is nil")
	}
	tombstone = normalizeV3SessionTombstone(tombstone)
	payload, err := json.Marshal(tombstone)
	if err != nil {
		t.Fatalf("marshal legacy tombstone: %v", err)
	}
	batch := sessions.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeyV3SessionTombstone(tombstone.SessionID)), payload, nil); err != nil {
		t.Fatalf("seed direct legacy tombstone: %v", err)
	}
	if tombstone.AccountScopeID != "" {
		if err := batch.Set([]byte(KeyV3SessionTombstoneByAccount(tombstone.AccountScopeID, tombstone.SessionID)), payload, nil); err != nil {
			t.Fatalf("seed old account legacy tombstone index: %v", err)
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		t.Fatalf("commit legacy tombstone seed: %v", err)
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
