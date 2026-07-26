package run

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/discovery"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func TestRunWorkspaceScopeInjectsAccountScopedLinkedAgentsInstructions(t *testing.T) {
	primary := t.TempDir()
	linked := t.TempDir()
	writeTestFile(t, filepath.Join(primary, "AGENTS.md"), "primary_runtime_rule: yes")
	writeTestFile(t, filepath.Join(linked, "AGENTS.md"), "linked_root: "+linked)

	principal := testRunPrincipal()
	workspaceSvc, cleanup := newTestRunWorkspaceService(t)
	defer cleanup()
	if _, err := workspaceSvc.AddForPrincipal(principal, primary, "primary", "", true); err != nil {
		t.Fatalf("save primary workspace: %v", err)
	}
	if _, err := workspaceSvc.AddDirectoryForPrincipal(principal, primary, linked); err != nil {
		t.Fatalf("add linked directory: %v", err)
	}

	runSvc := NewService(nil, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	session := pebblestore.SessionSnapshot{
		ID:             "session-linked-agents",
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		WorkspacePath:  primary,
	}

	scope, err := runSvc.resolveRunWorkspaceScope(session, principal)
	if err != nil {
		t.Fatalf("resolve run workspace scope: %v", err)
	}
	assertStringSliceContains(t, scope.Roots, primary)
	assertStringSliceContains(t, scope.Roots, linked)

	instructions := runSvc.composeInstructionsForScope(scope, pebblestore.AgentProfile{}, "")
	for _, want := range []string{
		"primary_runtime_rule: yes",
		"linked_root: " + linked,
		filepath.Join(primary, "AGENTS.md"),
		filepath.Join(linked, "AGENTS.md"),
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions missing %q\n--- instructions ---\n%s", want, instructions)
		}
	}
}

func TestRunWorkspaceScopeIncludesFullAgentsInstructions(t *testing.T) {
	primary := t.TempDir()
	const tailRule = "tail_runtime_rule_after_four_kilobytes: must_load"
	largeRule := "# Large rule\n" + strings.Repeat("padding instruction line to exceed prior runtime prompt cap\n", 100) + tailRule
	if len(largeRule) <= 4000 {
		t.Fatalf("test rule length = %d, want over prior 4000-byte cap", len(largeRule))
	}
	writeTestFile(t, filepath.Join(primary, "AGENTS.md"), largeRule)

	runSvc := NewService(nil, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	instructions := runSvc.composeInstructionsForScope(tool.WorkspaceScope{
		PrimaryPath: primary,
		Roots:       []string{primary},
	}, pebblestore.AgentProfile{}, "")
	if !strings.Contains(instructions, tailRule) {
		t.Fatalf("instructions missing AGENTS.md content beyond prior 4000-byte cap")
	}
	if strings.Contains(instructions, "...[truncated]") {
		t.Fatalf("instructions unexpectedly truncated AGENTS.md content")
	}
}

func TestRunWorkspaceScopeIgnoresLegacyOnlyEntriesForPrincipalBackedRun(t *testing.T) {
	primary := t.TempDir()
	linked := t.TempDir()
	writeTestFile(t, filepath.Join(primary, "AGENTS.md"), "primary_runtime_rule: yes")
	writeTestFile(t, filepath.Join(linked, "AGENTS.md"), "legacy_linked_rule: should_not_load")

	workspaceSvc, _, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	legacyEntry := pebblestore.WorkspaceEntry{
		Path:        primary,
		Name:        "legacy-only",
		Directories: []string{primary, linked},
	}
	if err := rawStore.PutJSON(pebblestore.KeyWorkspaceEntry(primary), legacyEntry); err != nil {
		t.Fatalf("write legacy workspace entry: %v", err)
	}

	principal := testRunPrincipal()
	runSvc := NewService(nil, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	session := pebblestore.SessionSnapshot{
		ID:             "session-ignore-legacy",
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		WorkspacePath:  primary,
	}

	scope, err := runSvc.resolveRunWorkspaceScope(session, principal)
	if err != nil {
		t.Fatalf("resolve run workspace scope: %v", err)
	}
	assertStringSliceContains(t, scope.Roots, primary)
	assertStringSliceNotContains(t, scope.Roots, linked)

	instructions := runSvc.composeInstructionsForScope(scope, pebblestore.AgentProfile{}, "")
	if strings.Contains(instructions, "legacy_linked_rule: should_not_load") || strings.Contains(instructions, filepath.Join(linked, "AGENTS.md")) {
		t.Fatalf("principal-backed run loaded legacy-only linked instructions\n--- instructions ---\n%s", instructions)
	}
}

func TestRunWorkspaceScopeRequiresPrincipalForPrincipalBackedRuntime(t *testing.T) {
	primary := t.TempDir()
	runSvc := NewService(nil, nil, nil, nil, nil, nil, discovery.NewService(), nil)

	_, err := runSvc.resolveRunWorkspaceScope(pebblestore.SessionSnapshot{
		ID:            "session-missing-principal",
		WorkspacePath: primary,
	}, identity.Principal{})
	if err == nil {
		t.Fatalf("expected missing principal error")
	}
	if !errors.Is(err, identity.ErrPrincipalRequired) || !strings.Contains(err.Error(), "run workspace scope requires principal") {
		t.Fatalf("missing principal error = %v, want clear principal-required error", err)
	}
}

func TestPersistentWorkspaceScopeApprovalAddsAccountScopedDirectory(t *testing.T) {
	primary := t.TempDir()
	linked := t.TempDir()
	principal := testRunPrincipal()

	workspaceSvc, workspaceStore, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	if _, err := workspaceSvc.AddForPrincipal(principal, primary, "primary", "", true); err != nil {
		t.Fatalf("save primary workspace: %v", err)
	}

	sessionID := "session-persistent-add-dir"
	if err := pebblestore.NewSessionStore(rawStore).CreateSessionForAccount(pebblestore.SessionSnapshot{
		ID:            sessionID,
		WorkspacePath: primary,
		WorkspaceName: "primary",
		Title:         "Persistent add-dir",
	}, principal.UserID, principal.AccountScopeID); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(rawStore), nil)

	runSvc := NewService(sessionSvc, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	workspaceCtx := runWorkspaceContext{
		WorkspacePath:        primary,
		WorkspaceRoots:       []string{primary},
		OriginWorkspacePath:  primary,
		OriginWorkspaceRoots: []string{primary},
	}
	changed, err := runSvc.applyPersistentWorkspaceScopeAccess(
		sessionID,
		primary,
		"primary",
		principal,
		tool.ScopeExpansionRequest{DirectoryPath: linked},
		&workspaceCtx,
	)
	if err != nil {
		t.Fatalf("apply persistent workspace scope access: %v", err)
	}
	if !changed {
		t.Fatalf("persistent add-dir did not report a workspace scope change")
	}

	accountEntry, ok, err := workspaceStore.GetForAccount(principal.AccountScopeID, primary)
	if err != nil || !ok {
		t.Fatalf("account workspace entry missing after add-dir: ok=%t err=%v", ok, err)
	}
	assertStringSliceContains(t, accountEntry.Directories, linked)
	if legacyEntry, ok, err := workspaceStore.GetLegacy(primary); err != nil || ok || len(legacyEntry.Directories) != 0 {
		t.Fatalf("persistent add-dir wrote legacy workspace entry: entry=%+v ok=%t err=%v", legacyEntry, ok, err)
	}
	assertStringSliceContains(t, workspaceCtx.OriginWorkspaceRoots, linked)
	assertStringSliceContains(t, workspaceCtx.WorkspaceRoots, linked)
}

func TestResolveRunWorkspaceScopeRejectsTemporaryRootChangedToSymlink(t *testing.T) {
	primary := t.TempDir()
	temporaryParent := t.TempDir()
	temporaryRoot := filepath.Join(temporaryParent, "approved")
	if err := os.Mkdir(temporaryRoot, 0o755); err != nil {
		t.Fatalf("create temporary root: %v", err)
	}
	if err := os.Remove(temporaryRoot); err != nil {
		t.Fatalf("remove temporary root: %v", err)
	}
	if err := os.Symlink(t.TempDir(), temporaryRoot); err != nil {
		t.Fatalf("replace temporary root with symlink: %v", err)
	}

	runSvc := NewService(nil, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	_, err := runSvc.resolveRunWorkspaceScope(pebblestore.SessionSnapshot{
		ID:                      "session-stale-temporary-root",
		UserID:                  "user-1",
		AccountScopeID:          "account-1",
		WorkspacePath:           primary,
		TemporaryWorkspaceRoots: []string{temporaryRoot},
	}, testRunPrincipal())
	if err == nil || !strings.Contains(err.Error(), "temporary workspace root must not be a symlink") {
		t.Fatalf("resolve stale temporary root error = %v, want symlink rejection", err)
	}
}

func newTestRunWorkspaceService(t *testing.T) (*workspaceruntime.Service, func()) {
	t.Helper()
	workspaceSvc, _, _, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	return workspaceSvc, cleanup
}

func newTestRunWorkspaceServiceWithRawStore(t *testing.T) (*workspaceruntime.Service, *pebblestore.WorkspaceStore, *pebblestore.Store, func()) {
	t.Helper()
	rawStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open pebble store: %v", err)
	}
	workspaceStore := pebblestore.NewWorkspaceStore(rawStore)
	workspaceSvc := workspaceruntime.NewService(workspaceStore)
	return workspaceSvc, workspaceStore, rawStore, func() { _ = rawStore.Close() }
}

func testRunPrincipal() identity.Principal {
	return identity.Principal{
		Type:           identity.PrincipalTypeUser,
		UserID:         "user-1",
		AccountScopeID: "account-1",
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertStringSliceContains(t *testing.T, values []string, want string) {
	t.Helper()
	want = strings.TrimSpace(want)
	for _, value := range values {
		if strings.TrimSpace(value) == want {
			return
		}
	}
	t.Fatalf("%q not found in %v", want, values)
}

func assertStringSliceNotContains(t *testing.T, values []string, unwanted string) {
	t.Helper()
	unwanted = strings.TrimSpace(unwanted)
	for _, value := range values {
		if strings.TrimSpace(value) == unwanted {
			t.Fatalf("%q unexpectedly found in %v", unwanted, values)
		}
	}
}
