package run

import (
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a checkout-free Companion is a conversation with runtime identity
// only. Requirement: resolveRunWorkspaceScope gives the account user's current
// Companion an empty, non-expandable scope (no working directory, roots or
// repository recovery) and refuses another user's principal, a session that
// merely copies the Companion marker without being the current identity, and
// a marked session carrying filesystem grants; resolveRunExecutionContext
// refuses any requested cwd/workspace/worktree; resolveTaskTargetWorkspace
// refuses a direct source-bound child task with or without a requested path
// (repository work needs a project task with an explicit source).
// Threat: the Companion fix opening an implicit cwd or source authority, a
// forged marker borrowing the Companion's checkout-free admission, or a later
// delegated task silently running without an authorized source. Boundary:
// pebblestore.CheckoutFreeCompanion / ValidateCheckoutFreeCompanion and the
// run service's scope, execution-context and task-target resolvers.
// Narrowest layer: the run service over a real Pebble session store, since
// these resolvers are the run-time authority every turn and task launch uses.
func TestCheckoutFreeCompanionScopeIsEmptyAndDelegationFailsClosed(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "companion-scope.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(db), events)
	store := sessions.Store()
	const account, user = "acct-1", "user-1"
	owner := identity.Principal{Type: identity.PrincipalTypeUser, UserID: user, AccountScopeID: account, AccountScopeSource: identity.AccountScopeSourceServerState}
	other := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-2", AccountScopeID: account, AccountScopeSource: identity.AccountScopeSourceServerState}
	record, _, err := store.EnsureCompanion(account, user, 1)
	if err != nil {
		t.Fatal(err)
	}
	marker := map[string]any{pebblestore.SessionCompanionMetadataKey: map[string]any{"generation": record.Generation}, "agent_name": "system-orchestrator"}
	companion := pebblestore.SessionSnapshot{ID: record.SessionID, UserID: user, AccountScopeID: account, Title: "Companion", Mode: "auto", Metadata: marker, CreatedAt: 1, UpdatedAt: 1}
	forged := pebblestore.SessionSnapshot{ID: "forged-companion", UserID: user, AccountScopeID: account, Title: "Forged", Mode: "auto", Metadata: marker, CreatedAt: 1, UpdatedAt: 1}
	for _, session := range []pebblestore.SessionSnapshot{companion, forged} {
		if err := store.CreateSession(session); err != nil {
			t.Fatal(err)
		}
	}
	service := &Service{sessions: sessions}

	scope, err := service.resolveRunWorkspaceScope(companion, owner)
	if err != nil {
		t.Fatalf("companion scope: %v", err)
	}
	if scope.PrimaryPath != "" || len(scope.Roots) != 0 || !scope.RejectScopeExpansion || scope.ExplicitRepositoryRecovery || scope.SessionID != companion.ID {
		t.Fatalf("companion scope carries authority: %+v", scope)
	}
	if _, err := service.resolveRunWorkspaceScope(companion, other); err == nil {
		t.Fatal("another user's principal resolved the Companion's scope")
	}
	if _, err := service.resolveRunWorkspaceScope(forged, owner); err == nil {
		t.Fatal("a session copying the Companion marker was admitted checkout-free")
	}
	granted := companion
	available := true
	granted.WorkspaceGrants = []pebblestore.WorkspaceGrant{{Kind: pebblestore.WorkspaceGrantAdditional, WorkspaceID: "ws-1", WorkspaceGeneration: 1, Path: t.TempDir(), Available: &available}}
	if _, err := service.resolveRunWorkspaceScope(granted, owner); err == nil || !strings.Contains(err.Error(), "filesystem authority") {
		t.Fatalf("a Companion carrying grants was admitted checkout-free: %v", err)
	}

	resolved, err := service.resolveRunExecutionContext(companion, RunExecutionContext{WorktreeMode: RunWorktreeModeInherit}, owner)
	if err != nil || resolved.Scope.PrimaryPath != "" || resolved.WorktreeMode != RunWorktreeModeOff {
		t.Fatalf("companion execution context: %+v %v", resolved, err)
	}
	for _, requested := range []RunExecutionContext{
		{WorktreeMode: RunWorktreeModeInherit, CWD: t.TempDir()},
		{WorktreeMode: RunWorktreeModeInherit, WorkspacePath: t.TempDir()},
		{WorktreeMode: RunWorktreeModeOn},
	} {
		if _, err := service.resolveRunExecutionContext(companion, requested, owner); err == nil {
			t.Fatalf("companion accepted an execution workspace override %+v", requested)
		}
	}

	for _, launch := range []*taskLaunchSpec{
		{RequestedSubagentType: "coder"},
		{RequestedSubagentType: "coder", TargetWorkspacePath: t.TempDir()},
		{RequestedSubagentType: "finder"},
	} {
		path, _, err := service.resolveTaskTargetWorkspace(companion, owner, launch)
		if err == nil || path != "" || !strings.Contains(err.Error(), "no source checkout") {
			t.Fatalf("direct %s task from the Companion resolved %q: %v", launch.RequestedSubagentType, path, err)
		}
	}
	after, _, err := sessions.GetSession(companion.ID)
	if err != nil || after.WorkspacePath != "" || len(after.WorkspaceGrants) != 0 {
		t.Fatalf("resolution changed the Companion: %+v %v", after, err)
	}
}
