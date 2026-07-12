package pebblestore

import (
	"encoding/json"
	"testing"
)

func TestBuildV3SessionWorksetPaginatesRecentSessions(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3WorksetSessionForTest(t, sessions, "session-a", "/workspace/a", 1000)
	createV3WorksetSessionForTest(t, sessions, "session-b", "/workspace/a", 3000)
	createV3WorksetSessionForTest(t, sessions, "session-c", "/workspace/a", 2000)
	createV3WorksetSessionForTest(t, sessions, "session-d", "/workspace/other", 4000)

	first, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID: "account-1",
		WorkspacePath:  "/workspace/a",
		RecentLimit:    2,
		History:        V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build first page: %v", err)
	}
	if got := first.SessionOrder; len(got) != 2 || got[0] != "session-b" || got[1] != "session-c" {
		t.Fatalf("first page order = %+v", got)
	}
	if !first.Pagination.HasMore || first.Pagination.NextBeforeUpdatedAt == nil || *first.Pagination.NextBeforeUpdatedAt != 2000 || first.Pagination.NextBeforeSessionID != "session-c" {
		t.Fatalf("first page pagination = %+v", first.Pagination)
	}

	second, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID:        "account-1",
		WorkspacePath:         "/workspace/a",
		RecentLimit:           2,
		RecentBeforeUpdatedAt: first.Pagination.NextBeforeUpdatedAt,
		RecentBeforeSessionID: first.Pagination.NextBeforeSessionID,
		History:               V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build second page: %v", err)
	}
	if got := second.SessionOrder; len(got) != 1 || got[0] != "session-a" {
		t.Fatalf("second page order = %+v", got)
	}
	if second.Pagination.HasMore || second.Pagination.NextBeforeUpdatedAt != nil {
		t.Fatalf("second page pagination = %+v", second.Pagination)
	}
}

func TestBuildV3SessionWorksetExcludesNavigationHiddenSystemSidechats(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3WorksetSessionForTest(t, sessions, "parent", "/workspace/sidechat", 1000)
	createV3WorksetSessionForTest(t, sessions, "system-sidechat-plan", "/workspace/sidechat", 2000)

	sidechat, ok, err := sessions.GetSession("system-sidechat-plan")
	if err != nil || !ok {
		t.Fatalf("load sidechat: ok=%t err=%v", ok, err)
	}
	sidechat.Metadata = map[string]any{
		"navigation_hidden": true,
		"system_sidechat":   true,
		"lineage_kind":      "system_sidechat",
		"parent_session_id": "parent",
	}
	if err := sessions.UpdateSession(sidechat); err != nil {
		t.Fatalf("mark sidechat hidden: %v", err)
	}

	workset, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID: "account-1",
		UserID:         "user-1",
		WorkspacePath:  "/workspace/sidechat",
		RecentLimit:    10,
		History:        V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build workset: %v", err)
	}
	if got := workset.SessionOrder; len(got) != 1 || got[0] != "parent" {
		t.Fatalf("hidden sidechat leaked into workset: %+v", got)
	}

	search, err := sessions.SearchV3Sessions(V3SessionSearchOptions{AccountScopeID: "account-1", UserID: "user-1", Global: true, Limit: 10})
	if err != nil {
		t.Fatalf("search sessions: %v", err)
	}
	if len(search.Items) != 1 || search.Items[0].ID != "parent" {
		t.Fatalf("hidden sidechat leaked into search: %+v", search.Items)
	}
}

func TestBuildV3SessionWorksetIncludesPinnedSessionsOutsideRecentLimit(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3WorksetSessionForTest(t, sessions, "session-pinned-old", "/workspace/pinned", 1000)
	createV3WorksetSessionForTest(t, sessions, "session-recent-new", "/workspace/pinned", 4000)
	createV3WorksetSessionForTest(t, sessions, "session-recent-mid", "/workspace/pinned", 3000)
	createV3WorksetSessionForTest(t, sessions, "session-other-workspace-pinned", "/workspace/other", 2000)
	createV3WorksetSessionForTest(t, sessions, "session-other-user-pinned", "/workspace/pinned", 5000)

	for _, sessionID := range []string{"session-pinned-old", "session-other-workspace-pinned", "session-other-user-pinned"} {
		session, ok, err := sessions.GetSession(sessionID)
		if err != nil || !ok {
			t.Fatalf("load %s: ok=%t err=%v", sessionID, ok, err)
		}
		session.Metadata = map[string]any{V3SessionDesktopSidebarPinnedMetadataKey: true}
		if sessionID == "session-other-user-pinned" {
			session.UserID = "user-2"
		}
		if err := sessions.UpdateSession(session); err != nil {
			t.Fatalf("pin %s: %v", sessionID, err)
		}
	}

	workset, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID:               "account-1",
		UserID:                       "user-1",
		WorkspacePath:                "/workspace/pinned",
		RecentLimit:                  2,
		IncludePinnedSidebarSessions: true,
		History:                      V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build pinned workset: %v", err)
	}
	if got := workset.SessionOrder; len(got) != 3 || got[0] != "session-recent-new" || got[1] != "session-recent-mid" || got[2] != "session-pinned-old" {
		t.Fatalf("pinned workset order = %+v", got)
	}
	if workset.SessionsByID["session-pinned-old"].Metadata[V3SessionDesktopSidebarPinnedMetadataKey] != true {
		t.Fatalf("pinned metadata not preserved: %+v", workset.SessionsByID["session-pinned-old"].Metadata)
	}
	if _, ok := workset.SessionsByID["session-other-workspace-pinned"]; ok {
		t.Fatalf("pinned workset leaked other workspace session")
	}
	if _, ok := workset.SessionsByID["session-other-user-pinned"]; ok {
		t.Fatalf("pinned workset leaked other user session")
	}
}

func TestBuildV3SessionWorksetUsesSessionIDTieBreaker(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3WorksetSessionForTest(t, sessions, "session-a", "/workspace/tie", 1000)
	createV3WorksetSessionForTest(t, sessions, "session-c", "/workspace/tie", 1000)
	createV3WorksetSessionForTest(t, sessions, "session-b", "/workspace/tie", 1000)

	page, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID: "account-1",
		WorkspacePath:  "/workspace/tie",
		RecentLimit:    2,
		History:        V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build tie page: %v", err)
	}
	if got := page.SessionOrder; len(got) != 2 || got[0] != "session-c" || got[1] != "session-b" {
		t.Fatalf("tie page order = %+v", got)
	}
	if !page.Pagination.HasMore || page.Pagination.NextBeforeUpdatedAt == nil || *page.Pagination.NextBeforeUpdatedAt != 1000 || page.Pagination.NextBeforeSessionID != "session-b" {
		t.Fatalf("tie pagination = %+v", page.Pagination)
	}

	next, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID:        "account-1",
		WorkspacePath:         "/workspace/tie",
		RecentLimit:           2,
		RecentBeforeUpdatedAt: page.Pagination.NextBeforeUpdatedAt,
		RecentBeforeSessionID: page.Pagination.NextBeforeSessionID,
		History:               V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build tie next page: %v", err)
	}
	if got := next.SessionOrder; len(got) != 1 || got[0] != "session-a" {
		t.Fatalf("tie next order = %+v", got)
	}
}

func TestBuildV3SessionWorksetResolvesWorktreeSessionBySourceWorkspace(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3WorksetSessionForTest(t, sessions, "session-worktree", "/data/swarm/worktrees/swarm-go/ws_session", 2000)
	worktree, ok, err := sessions.GetSession("session-worktree")
	if err != nil || !ok {
		t.Fatalf("load worktree session: ok=%t err=%v", ok, err)
	}
	worktree.WorktreeEnabled = true
	worktree.WorktreeRootPath = worktree.WorkspacePath
	worktree.WorktreeBranch = "agent/session-worktree"
	worktree.Metadata = map[string]any{
		"swarm_v3_source_workspace_path":  "/host/swarm-go",
		"swarm_v3_runtime_workspace_path": worktree.WorkspacePath,
	}
	if err := sessions.UpdateSession(worktree); err != nil {
		t.Fatalf("update worktree session metadata: %v", err)
	}

	workset, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID: "account-1",
		WorkspacePath:  "/host/swarm-go",
		RecentLimit:    10,
		History:        V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build source workspace workset: %v", err)
	}
	if got := workset.SessionOrder; len(got) != 1 || got[0] != "session-worktree" {
		t.Fatalf("source workspace workset order = %+v", got)
	}
	if _, ok, err := store.GetBytes(KeySessionRecentForAccountWorkspace("account-1", "/host/swarm-go", worktree.UpdatedAt, worktree.ID)); err != nil || !ok {
		t.Fatalf("source workspace recent index missing: ok=%t err=%v", ok, err)
	}
}

func TestBuildV3SessionWorksetRecentIndexTracksUpdatesAndDeletes(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3WorksetSessionForTest(t, sessions, "session-old", "/workspace/index", 1000)
	createV3WorksetSessionForTest(t, sessions, "session-delete", "/workspace/index", 3000)
	createV3WorksetSessionForTest(t, sessions, "session-move", "/workspace/index", 2000)

	updated, ok, err := sessions.GetSession("session-old")
	if err != nil || !ok {
		t.Fatalf("load session-old: ok=%t err=%v", ok, err)
	}
	updated.UpdatedAt = 5000
	if err := sessions.UpdateSession(updated); err != nil {
		t.Fatalf("update recent index session: %v", err)
	}
	moved, ok, err := sessions.GetSession("session-move")
	if err != nil || !ok {
		t.Fatalf("load session-move: ok=%t err=%v", ok, err)
	}
	moved.WorkspacePath = "/workspace/other"
	moved.UpdatedAt = 6000
	if err := sessions.UpdateSession(moved); err != nil {
		t.Fatalf("move recent index session: %v", err)
	}
	if err := sessions.DeleteSession("session-delete"); err != nil {
		t.Fatalf("delete recent index session: %v", err)
	}

	workset, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID: "account-1",
		WorkspacePath:  "/workspace/index",
		RecentLimit:    10,
		History:        V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build updated indexed workset: %v", err)
	}
	if got := workset.SessionOrder; len(got) != 1 || got[0] != "session-old" {
		t.Fatalf("indexed update/delete order = %+v", got)
	}
}

func TestBuildV3SessionWorksetBackfillsRecentIndexForExistingSessions(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	legacy := SessionSnapshot{
		ID:             "legacy-session",
		UserID:         "user-1",
		AccountScopeID: "account-1",
		WorkspacePath:  "/workspace/legacy",
		WorkspaceName:  "workspace",
		Title:          "legacy",
		CreatedAt:      1000,
		UpdatedAt:      1000,
	}
	if err := store.PutJSON(KeySession(legacy.ID), legacy); err != nil {
		t.Fatalf("seed legacy session: %v", err)
	}

	workset, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID: "account-1",
		WorkspacePath:  "/workspace/legacy",
		RecentLimit:    10,
		History:        V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build backfilled indexed workset: %v", err)
	}
	if got := workset.SessionOrder; len(got) != 1 || got[0] != "legacy-session" {
		t.Fatalf("backfilled order = %+v", got)
	}
	if _, ok, err := store.GetBytes(KeySessionRecentForAccountWorkspace("account-1", "/workspace/legacy", legacy.UpdatedAt, legacy.ID)); err != nil || !ok {
		t.Fatalf("recent index was not backfilled: ok=%t err=%v", ok, err)
	}
}

func TestBuildV3SessionWorksetGlobalRecentUsesAccountIndex(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3WorksetSessionForTest(t, sessions, "session-account", "/workspace/account", 2000)
	foreign := SessionSnapshot{ID: "session-foreign", AccountScopeID: "account-2", WorkspacePath: "/workspace/account", Title: "foreign", CreatedAt: 3000, UpdatedAt: 3000}
	if err := sessions.CreateSession(foreign); err != nil {
		t.Fatalf("create foreign session: %v", err)
	}

	workset, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID: "account-1",
		RecentLimit:    10,
		History:        V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build account global indexed workset: %v", err)
	}
	if got := workset.SessionOrder; len(got) != 1 || got[0] != "session-account" {
		t.Fatalf("account global order = %+v", got)
	}
}

func TestBuildV3SessionWorksetCappedHistoryWithManifest(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3WorksetSessionForTest(t, sessions, "session-budget-manifest", "/workspace/budget", 1000)
	appendV3WorksetMessageForTest(t, sessions, "session-budget-manifest", "first message", 2000)
	appendV3WorksetMessageForTest(t, sessions, "session-budget-manifest", "second message", 3000)

	workset, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID: "account-1",
		SessionIDs:     []string{"session-budget-manifest"},
		History:        V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeFull, MaxMessagesPerSession: 1, ManifestPolicy: V3SessionWorksetManifestPolicyManifest},
	})
	if err != nil {
		t.Fatalf("build capped manifest workset: %v", err)
	}
	if len(workset.MessagesBySession["session-budget-manifest"]) != 1 {
		t.Fatalf("messages should remain inline for local switching: %+v", workset.MessagesBySession["session-budget-manifest"])
	}
	if len(workset.HistoryChunksByID) != 0 {
		t.Fatalf("manifest should not inline history chunk bodies: %+v", workset.HistoryChunksByID)
	}
	if len(workset.HistoryManifestsBySession["session-budget-manifest"]) == 0 || len(workset.Omissions) == 0 {
		t.Fatalf("manifest/omissions missing: manifests=%+v omissions=%+v", workset.HistoryManifestsBySession, workset.Omissions)
	}
	if workset.Omissions[0].ManifestRef == "" || workset.Omissions[0].Reason != V3SessionWorksetOmissionRequiresManifest {
		t.Fatalf("omission = %+v", workset.Omissions[0])
	}
}

func TestBuildV3SessionWorksetPerSessionOmittedHistory(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3WorksetSessionForTest(t, sessions, "session-omit", "/workspace/omit", 1000)
	appendV3WorksetMessageForTest(t, sessions, "session-omit", "one", 2000)
	appendV3WorksetMessageForTest(t, sessions, "session-omit", "two", 3000)

	workset, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID: "account-1",
		SessionIDs:     []string{"session-omit"},
		History:        V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeFull, MaxMessagesPerSession: 1, ManifestPolicy: V3SessionWorksetManifestPolicyOmit},
	})
	if err != nil {
		t.Fatalf("build omitted workset: %v", err)
	}
	if len(workset.Omissions) == 0 || workset.Omissions[0].Reason != V3SessionWorksetOmissionRequiresManifest || workset.Omissions[0].NextCursor == "" {
		t.Fatalf("omissions = %+v", workset.Omissions)
	}
	if len(workset.HistoryManifestsBySession["session-omit"]) != 0 {
		t.Fatalf("omit policy should not return manifest: %+v", workset.HistoryManifestsBySession["session-omit"])
	}
}

func TestBuildV3SessionWorksetOmitsEventsByDefault(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3WorksetSessionForTest(t, sessions, "session-diagnostics", "/workspace/diagnostics", 1000)
	appendV3WorksetDiagnosticForTest(t, sessions, "session-diagnostics", "session.diagnostic.store.result", 2000)
	appendV3WorksetDiagnosticForTest(t, sessions, "session-diagnostics", "session.diagnostic.provider.response", 3000)
	appendV3WorksetMessageForTest(t, sessions, "session-diagnostics", "visible", 4000)

	workset, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID: "account-1",
		SessionIDs:     []string{"session-diagnostics"},
		History:        V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeFull, ManifestPolicy: V3SessionWorksetManifestPolicyManifest},
	})
	if err != nil {
		t.Fatalf("build event-omitted workset: %v", err)
	}
	if len(workset.EventsBySession["session-diagnostics"]) != 0 {
		t.Fatalf("events should be omitted by default: %+v", workset.EventsBySession["session-diagnostics"])
	}
}

func TestBuildV3SessionWorksetIncludesNonDiagnosticEventsWhenRequested(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3WorksetSessionForTest(t, sessions, "session-events", "/workspace/events", 1000)
	appendV3WorksetDiagnosticForTest(t, sessions, "session-events", "session.diagnostic.store.result", 2000)
	appendV3WorksetMessageForTest(t, sessions, "session-events", "visible", 4000)

	workset, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID: "account-1",
		SessionIDs:     []string{"session-events"},
		History:        V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeFull, ManifestPolicy: V3SessionWorksetManifestPolicyManifest, IncludeEvents: true},
	})
	if err != nil {
		t.Fatalf("build event-included workset: %v", err)
	}
	for _, event := range workset.EventsBySession["session-events"] {
		if v3SessionWorksetEventOmitted(event) {
			t.Fatalf("diagnostic event leaked into workset: %+v", event)
		}
	}
	if len(workset.EventsBySession["session-events"]) != 2 {
		t.Fatalf("events = %+v, want create + message events only", workset.EventsBySession["session-events"])
	}
}

func TestBuildV3SessionWorksetGlobalNoRecentReturnsPrincipalSessionsDeterministically(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-global-workset-old", "user-1", "account-1", "/workspace/global-old", 1000)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-global-workset-new", "user-1", "account-1", "/workspace/global-new", 3000)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-global-workset-mid", "user-1", "account-1", "/workspace/global-mid", 2000)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-global-workset-other-user", "user-2", "account-1", "/workspace/global-other-user", 4000)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-global-workset-blank-user", "", "account-1", "/workspace/global-blank-user", 5000)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-global-workset-other-account", "user-1", "account-2", "/workspace/global-other-account", 6000)

	workset, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID: "account-1",
		UserID:         "user-1",
		Global:         true,
		History:        V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeNone},
	})
	if err != nil {
		t.Fatalf("build principal-global workset: %v", err)
	}
	want := []string{"session-global-workset-new", "session-global-workset-mid", "session-global-workset-old"}
	if !stringSlicesEqual(workset.SessionOrder, want) {
		t.Fatalf("principal-global workset order = %+v, want %+v", workset.SessionOrder, want)
	}
	for _, leaked := range []string{"session-global-workset-other-user", "session-global-workset-blank-user", "session-global-workset-other-account"} {
		if _, ok := workset.SessionsByID[leaked]; ok {
			t.Fatalf("principal-global workset leaked %s: %+v", leaked, workset.SessionOrder)
		}
	}
}

func TestBuildV3SessionWorksetGlobalMatchesBootstrapAndKeepsInactiveWithIncludeActive(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-global-parity-a", "user-1", "account-1", "/workspace/global-parity-a", 1000)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-global-parity-b", "user-1", "account-1", "/workspace/global-parity-b", 3000)
	createV3SyncSnapshotSessionForUserTest(t, sessions, "session-global-parity-c", "user-1", "account-1", "/workspace/global-parity-c", 2000)

	bootstrap, err := sessions.BuildV3SyncSnapshot(V3SyncSnapshotOptions{
		AccountScopeID:        "account-1",
		UserID:                "user-1",
		Global:                true,
		History:               V3SyncSnapshotHistoryOptions{Mode: V3SyncSnapshotHistoryModeNone},
		IncludeActiveSessions: true,
	})
	if err != nil {
		t.Fatalf("build principal-global bootstrap snapshot: %v", err)
	}
	workset, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID:        "account-1",
		UserID:                "user-1",
		Global:                true,
		History:               V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeNone},
		IncludeActiveSessions: true,
	})
	if err != nil {
		t.Fatalf("build principal-global reconnect workset: %v", err)
	}
	if !stringSlicesEqual(workset.SessionOrder, bootstrap.SessionOrder) {
		t.Fatalf("reconnect global order = %+v, bootstrap order = %+v", workset.SessionOrder, bootstrap.SessionOrder)
	}
	want := []string{"session-global-parity-b", "session-global-parity-c", "session-global-parity-a"}
	if !stringSlicesEqual(workset.SessionOrder, want) {
		t.Fatalf("principal-global workset reduced or reordered inactive sessions: got %+v want %+v", workset.SessionOrder, want)
	}
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func createV3WorksetSessionForTest(t *testing.T, sessions *SessionStore, sessionID, workspacePath string, updatedAt int64) {
	t.Helper()
	_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      sessionID,
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "create-" + sessionID,
		PayloadHash:    "hash-create-" + sessionID,
		Kind:           V3SessionMutationCreateSession,
		Session: &SessionSnapshot{
			ID:             sessionID,
			UserID:         "user-1",
			AccountScopeID: "account-1",
			WorkspacePath:  workspacePath,
			WorkspaceName:  "workspace",
			Title:          sessionID,
			CreatedAt:      updatedAt,
			UpdatedAt:      updatedAt,
		},
		NowUnixMs: updatedAt,
	})
	if err != nil {
		t.Fatalf("create workset session %s: %v", sessionID, err)
	}
}

func appendV3WorksetMessageForTest(t *testing.T, sessions *SessionStore, sessionID, content string, now int64) {
	t.Helper()
	_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      sessionID,
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "message-" + sessionID + content,
		PayloadHash:    "hash-message-" + sessionID + content,
		Kind:           V3SessionMutationAppendMessage,
		Message:        &MessageSnapshot{Role: "user", Content: content},
		NowUnixMs:      now,
	})
	if err != nil {
		t.Fatalf("append workset message %s: %v", sessionID, err)
	}
}
func appendV3WorksetDiagnosticForTest(t *testing.T, sessions *SessionStore, sessionID, eventType string, now int64) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"diagnostic": "large payload should not be sent to desktop workset",
		"value":      string(make([]byte, 1024)),
	})
	if err != nil {
		t.Fatalf("marshal diagnostic payload: %v", err)
	}
	_, err = sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:      sessionID,
		UserID:         "user-1",
		AccountScopeID: "account-1",
		IdempotencyKey: "diagnostic-" + sessionID + eventType,
		PayloadHash:    "hash-diagnostic-" + sessionID + eventType,
		Kind:           V3SessionMutationRecordDiagnostic,
		EventType:      eventType,
		EventPayload:   payload,
		NowUnixMs:      now,
	})
	if err != nil {
		t.Fatalf("append diagnostic event %s: %v", sessionID, err)
	}
}
