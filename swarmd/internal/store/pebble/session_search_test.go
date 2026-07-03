package pebblestore

import "testing"

func TestSearchV3SessionsRecentPaginationAndFilters(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	createSearchTestSession(t, sessions, SessionSnapshot{ID: "recent-old", UserID: "user-1", AccountScopeID: "acct-1", WorkspacePath: workspaceA, Title: "Old", CreatedAt: 1000, UpdatedAt: 1000})
	createSearchTestSession(t, sessions, SessionSnapshot{ID: "recent-mid", UserID: "user-1", AccountScopeID: "acct-1", WorkspacePath: workspaceA, Title: "Mid", CreatedAt: 2000, UpdatedAt: 2000})
	createSearchTestSession(t, sessions, SessionSnapshot{ID: "recent-other-workspace", UserID: "user-1", AccountScopeID: "acct-1", WorkspacePath: workspaceB, Title: "Other", CreatedAt: 2500, UpdatedAt: 2500})
	createSearchTestSession(t, sessions, SessionSnapshot{ID: "recent-new", UserID: "user-1", AccountScopeID: "acct-1", WorkspacePath: workspaceA, Title: "New", CreatedAt: 3000, UpdatedAt: 3000})

	from := int64(1500)
	result, err := sessions.SearchV3Sessions(V3SessionSearchOptions{AccountScopeID: "acct-1", UserID: "user-1", WorkspacePath: workspaceA, FromUpdatedAt: &from, Limit: 1})
	if err != nil {
		t.Fatalf("search recent: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "recent-new" || result.Items[0].MessageCount != 0 {
		t.Fatalf("first page = %+v", result.Items)
	}
	if !result.Pagination.HasMore || result.Pagination.NextCursor == "" {
		t.Fatalf("pagination = %+v", result.Pagination)
	}
	result, err = sessions.SearchV3Sessions(V3SessionSearchOptions{AccountScopeID: "acct-1", UserID: "user-1", WorkspacePath: workspaceA, FromUpdatedAt: &from, BeforeUpdatedAt: result.Pagination.NextBeforeUpdatedAt, BeforeSessionID: result.Pagination.NextBeforeSessionID, Limit: 1})
	if err != nil {
		t.Fatalf("search recent page 2: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "recent-mid" || result.Pagination.HasMore {
		t.Fatalf("second page = %+v pagination=%+v", result.Items, result.Pagination)
	}
}

func TestSearchV3SessionsQueryMessageCountAndArchived(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	workspaceA := t.TempDir()
	createSearchTestSession(t, sessions, SessionSnapshot{ID: "query-active", UserID: "user-1", AccountScopeID: "acct-1", WorkspacePath: workspaceA, Title: "Active Project", CreatedAt: 1000, UpdatedAt: 1000})
	appendSearchTestMessage(t, sessions, "query-active", "user-1", "acct-1", "needle in a message", 2000)
	createSearchTestSession(t, sessions, SessionSnapshot{ID: "query-archived", UserID: "user-1", AccountScopeID: "acct-1", WorkspacePath: workspaceA, Title: "Archived Needle", CreatedAt: 1500, UpdatedAt: 1500})
	appendSearchTestMessage(t, sessions, "query-archived", "user-1", "acct-1", "archived needle body", 2500)
	if err := sessions.ArchiveSession("query-archived"); err != nil {
		t.Fatalf("archive: %v", err)
	}

	result, err := sessions.SearchV3Sessions(V3SessionSearchOptions{AccountScopeID: "acct-1", UserID: "user-1", Global: true, Query: "needle", Limit: 50})
	if err != nil {
		t.Fatalf("query active: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "query-active" || result.Items[0].MessageCount != 1 || len(result.Items[0].Snippets) == 0 || result.Items[0].Archived {
		t.Fatalf("active query result = %+v", result.Items)
	}

	result, err = sessions.SearchV3Sessions(V3SessionSearchOptions{AccountScopeID: "acct-1", UserID: "user-1", Global: true, Query: "needle", ArchivedMode: "only", Limit: 50})
	if err != nil {
		t.Fatalf("query archived: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "query-archived" || result.Items[0].MessageCount != 1 || !result.Items[0].Archived {
		t.Fatalf("archived query result = %+v", result.Items)
	}
}

func createSearchTestSession(t *testing.T, sessions *SessionStore, session SessionSnapshot) {
	t.Helper()
	if err := sessions.CreateSession(session); err != nil {
		t.Fatalf("create session %s: %v", session.ID, err)
	}
}

func appendSearchTestMessage(t *testing.T, sessions *SessionStore, sessionID, userID, accountScopeID, content string, now int64) {
	t.Helper()
	_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{Kind: V3SessionMutationAppendMessage, SessionID: sessionID, UserID: userID, AccountScopeID: accountScopeID, ClientRequestID: sessionID + content, PayloadHash: "hash-" + sessionID + content, NowUnixMs: now, Message: &MessageSnapshot{Role: "user", Content: content}})
	if err != nil {
		t.Fatalf("append message %s: %v", sessionID, err)
	}
}
