package permission

import (
	"path/filepath"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestPermissionSummaryPendingIndexTracksNonzeroAndRepair(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "summary-index.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	perms := NewService(pebblestore.NewPermissionStore(store), events, nil)
	perms.SetSessionResolver(sessions)

	session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         "user-summary-index",
		AccountScopeID: "account-summary-index",
		Title:          "Summary index",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "workspace",
		Preference:     &pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	record, err := perms.CreatePending(CreateInput{
		SessionID:     session.ID,
		RunID:         "run-summary-index",
		CallID:        "call-summary-index",
		ToolName:      "bash",
		ToolArguments: "{}",
		Requirement:   "bash",
		Mode:          sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("create pending: %v", err)
	}

	summaries, err := perms.ListPendingSummaries(session.AccountScopeID, session.UserID, 10)
	if err != nil {
		t.Fatalf("list pending summaries: %v", err)
	}
	if len(summaries) != 1 || summaries[0].SessionID != session.ID || summaries[0].PendingCount != 1 {
		t.Fatalf("pending summaries after create = %+v", summaries)
	}

	if err := pebblestore.NewPermissionStore(store).ClearSummaryPendingIndex(session.AccountScopeID, session.UserID); err != nil {
		t.Fatalf("clear summary pending index: %v", err)
	}
	if summaries, err := perms.ListPendingSummaries(session.AccountScopeID, session.UserID, 10); err != nil {
		t.Fatalf("list after clear: %v", err)
	} else if len(summaries) != 0 {
		t.Fatalf("expected cleared index, got %+v", summaries)
	}

	if err := perms.RepairSummaryPendingIndex(session.AccountScopeID, session.UserID); err != nil {
		t.Fatalf("repair summary pending index: %v", err)
	}
	if summaries, err := perms.ListPendingSummaries(session.AccountScopeID, session.UserID, 10); err != nil {
		t.Fatalf("list after repair: %v", err)
	} else if len(summaries) != 1 || summaries[0].SessionID != session.ID || summaries[0].PendingCount != 1 {
		t.Fatalf("pending summaries after repair = %+v", summaries)
	}

	if _, err := perms.Resolve(session.ID, record.ID, DecisionDeny, "done"); err != nil {
		t.Fatalf("resolve pending: %v", err)
	}
	if summaries, err := perms.ListPendingSummaries(session.AccountScopeID, session.UserID, 10); err != nil {
		t.Fatalf("list after resolve: %v", err)
	} else if len(summaries) != 0 {
		t.Fatalf("expected no pending summaries after resolve, got %+v", summaries)
	}
}
