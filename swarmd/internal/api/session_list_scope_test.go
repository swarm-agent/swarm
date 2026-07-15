package api

import (
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func TestListSessionsForCWDUsesExactPathWhenNoWorkspaceMatches(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions-unmatched-cwd.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"}
	workspaceStore := pebblestore.NewWorkspaceStore(store)
	workspaceSvc := workspaceruntime.NewService(workspaceStore)
	savedWorkspace := t.TempDir()
	if _, err := workspaceSvc.AddForPrincipal(principal, savedWorkspace, "saved", "", true); err != nil {
		t.Fatalf("add saved workspace: %v", err)
	}

	nonWorkspaceCWD := t.TempDir()
	nestedCWD := filepath.Join(nonWorkspaceCWD, "nested")
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	nonWorkspaceSession := createSessionForListScopeTest(t, sessionSvc, principal, nonWorkspaceCWD)
	_ = createSessionForListScopeTest(t, sessionSvc, principal, nestedCWD)
	_ = createSessionForListScopeTest(t, sessionSvc, principal, savedWorkspace)

	sessions, err := listSessionsForCWD(sessionSvc, workspaceSvc, principal, nonWorkspaceCWD, 100, false)
	if err != nil {
		t.Fatalf("list sessions for cwd: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 exact-CWD session for unmatched cwd, got %d: %#v", len(sessions), sessions)
	}
	if sessions[0].ID != nonWorkspaceSession.ID {
		t.Fatalf("session id = %q, want %q", sessions[0].ID, nonWorkspaceSession.ID)
	}
}

func createSessionForListScopeTest(t *testing.T, svc *sessionruntime.Service, principal identity.Principal, workspacePath string) pebblestore.SessionSnapshot {
	t.Helper()
	session, _, err := svc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		Title:          "List scope",
		WorkspacePath:  workspacePath,
		WorkspaceName:  filepath.Base(workspacePath),
		Preference: &pebblestore.ModelPreference{
			Provider: "test-provider",
			Model:    "test-model",
			Thinking: "medium",
		},
	})
	if err != nil {
		t.Fatalf("create session for %q: %v", workspacePath, err)
	}
	return session
}
