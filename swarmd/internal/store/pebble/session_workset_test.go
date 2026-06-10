package pebblestore

import "testing"

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

func TestBuildV3SessionWorksetBudgetExceededWithoutManifestFails(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3WorksetSessionForTest(t, sessions, "session-budget-error", "/workspace/budget", 1000)
	appendV3WorksetMessageForTest(t, sessions, "session-budget-error", "large message that cannot fit into a tiny response budget", 2000)

	_, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID: "account-1",
		SessionIDs:     []string{"session-budget-error"},
		History:        V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeFull, ManifestPolicy: V3SessionWorksetManifestPolicyError},
		ResponseBudget: V3SessionWorksetResponseBudget{MaxBytes: 20},
	})
	if err == nil {
		t.Fatalf("expected budget error")
	}
}

func TestBuildV3SessionWorksetBudgetExceededWithManifest(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3WorksetSessionForTest(t, sessions, "session-budget-manifest", "/workspace/budget", 1000)
	appendV3WorksetMessageForTest(t, sessions, "session-budget-manifest", "first message", 2000)
	appendV3WorksetMessageForTest(t, sessions, "session-budget-manifest", "second message", 3000)

	workset, err := sessions.BuildV3SessionWorkset(V3SessionWorksetOptions{
		AccountScopeID: "account-1",
		SessionIDs:     []string{"session-budget-manifest"},
		History:        V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeFull, MaxMessagesPerSession: 1, ManifestPolicy: V3SessionWorksetManifestPolicyManifest},
		ResponseBudget: V3SessionWorksetResponseBudget{AllowManifest: true},
	})
	if err != nil {
		t.Fatalf("build manifest workset: %v", err)
	}
	if len(workset.MessagesBySession["session-budget-manifest"]) != 0 {
		t.Fatalf("messages should be omitted when manifest is required: %+v", workset.MessagesBySession["session-budget-manifest"])
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
