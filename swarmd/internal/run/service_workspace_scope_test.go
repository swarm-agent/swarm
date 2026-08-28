package run

import (
	"context"
	"errors"
	"os"
	"os/exec"
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

func TestRunWorkspaceScopeUsesManagedWorktreeAsPrimaryAndPromptsToolRoot(t *testing.T) {
	source := t.TempDir()
	worktree := t.TempDir()
	linked := t.TempDir()
	writeTestFile(t, filepath.Join(worktree, "AGENTS.md"), "worktree_runtime_rule: yes")

	principal := testRunPrincipal()
	runSvc := NewService(nil, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	session := pebblestore.SessionSnapshot{
		ID:                      "session-worktree-runtime",
		UserID:                  principal.UserID,
		AccountScopeID:          principal.AccountScopeID,
		WorkspacePath:           source,
		TemporaryWorkspaceRoots: []string{linked},
		WorktreeEnabled:         true,
		WorktreeRootPath:        worktree,
		WorktreeBaseBranch:      "dev",
		WorktreeBranch:          "agent/plan-worktree",
		Metadata: map[string]any{
			"swarm_v3_source_workspace_path":  source,
			"swarm_v3_runtime_workspace_path": worktree,
			"base_commit":                     strings.Repeat("a", 40),
		},
	}

	scope, err := runSvc.resolveRunWorkspaceScope(session, principal)
	if err != nil {
		t.Fatalf("resolve managed worktree scope: %v", err)
	}
	if scope.PrimaryPath != worktree || !scope.WorktreeEnabled || scope.WorktreeRootPath != worktree || scope.WorktreeBranch != session.WorktreeBranch || scope.WorktreeBaseBranch != session.WorktreeBaseBranch || scope.WorktreeBaseCommit != strings.Repeat("a", 40) || scope.SourceWorkspacePath != source {
		t.Fatalf("managed worktree scope = %+v", scope)
	}
	assertStringSliceContains(t, scope.Roots, worktree)
	assertStringSliceContains(t, scope.Roots, linked)

	instructions := runSvc.ComposeRuntimeInstructions(scope, sessionruntime.ModePlan, false, pebblestore.NormalizeAgentProfile(pebblestore.AgentProfile{
		Name: "swarm", Mode: "primary", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true),
	}), "")
	for _, want := range []string{
		"- primary_root: " + worktree,
		"Tools run directly on the host workspace path: " + worktree,
		"Managed worktree context (authoritative for this run):",
		"- active_worktree: " + worktree,
		"project root and default path for list, search, read, find",
		"- worktree_branch: " + session.WorktreeBranch,
		"- source_workspace: " + source + " (reference identity only",
		"worktree_runtime_rule: yes",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("worktree instructions missing %q\n--- instructions ---\n%s", want, instructions)
		}
	}
}

func TestRunWorkspaceScopeCoderAllowsOnlyCanonicalLinkedWorktreeGitAdminRoot(t *testing.T) {
	repo := t.TempDir()
	runTestGit(t, repo, "init", "-b", "dev")
	runTestGit(t, repo, "config", "user.email", "test@example.com")
	runTestGit(t, repo, "config", "user.name", "Test User")
	writeTestFile(t, filepath.Join(repo, "tracked.txt"), "tracked")
	runTestGit(t, repo, "add", "tracked.txt")
	runTestGit(t, repo, "commit", "-m", "base")

	worktree := filepath.Join(t.TempDir(), "child")
	runTestGit(t, repo, "worktree", "add", "-b", "agent/test-linked-admin", worktree)
	gitAdminRoot := strings.TrimSpace(runTestGit(t, worktree, "rev-parse", "--path-format=absolute", "--git-dir"))

	principal := testRunPrincipal()
	runSvc := NewService(nil, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	scope, err := runSvc.resolveRunWorkspaceScope(pebblestore.SessionSnapshot{
		ID:                 "session-coder-linked-admin",
		UserID:             principal.UserID,
		AccountScopeID:     principal.AccountScopeID,
		WorkspacePath:      repo,
		WorktreeEnabled:    true,
		WorktreeRootPath:   worktree,
		WorktreeBranch:     "agent/test-linked-admin",
		WorktreeBaseBranch: "dev",
		Metadata: map[string]any{
			"requested_subagent": "coder",
			"owned_scope":        []string{"src/**"},
		},
	}, principal)
	if err != nil {
		t.Fatalf("resolve Coder linked-worktree scope: %v", err)
	}
	assertStringSliceNotContains(t, scope.Roots, gitAdminRoot)
	assertStringSliceContains(t, scope.ReadOnlyRoots, gitAdminRoot)
	if len(scope.MutationScopes) != 1 || scope.MutationScopes[0] != "src/**" {
		t.Fatalf("Coder mutation scopes = %#v, want declared owned scope", scope.MutationScopes)
	}
	request, needed, err := tool.ScopeExpansionForCall(scope, tool.Call{Name: "read", Arguments: mustJSON(t, map[string]any{"path": filepath.Join(gitAdminRoot, "gitdir")})})
	if err != nil || needed {
		t.Fatalf("own Git admin read requested expansion: needed=%t request=%+v err=%v", needed, request, err)
	}
	otherAdmin := filepath.Join(filepath.Dir(gitAdminRoot), "other-worktree", "gitdir")
	if err := os.MkdirAll(filepath.Dir(otherAdmin), 0o755); err != nil {
		t.Fatalf("create unrelated Git admin fixture: %v", err)
	}
	writeTestFile(t, otherAdmin, filepath.Join(t.TempDir(), ".git"))
	_, needed, err = tool.ScopeExpansionForCall(scope, tool.Call{Name: "read", Arguments: mustJSON(t, map[string]any{"path": otherAdmin})})
	if err != nil || !needed {
		t.Fatalf("unrelated Git admin path expansion = %t err=%v, want permission", needed, err)
	}
}

func TestWorkspaceScopeGatePreservesCoderGitAdminReadAuthorization(t *testing.T) {
	worktree := t.TempDir()
	gitAdmin := t.TempDir()
	principal := testRunPrincipal()
	workspaceCtx := runWorkspaceContext{
		WorkspacePath:        worktree,
		WorkspaceRoots:       []string{worktree},
		OriginWorkspacePath:  worktree,
		OriginWorkspaceRoots: []string{worktree},
		Scope: tool.WorkspaceScope{
			PrimaryPath:    worktree,
			Roots:          []string{worktree},
			ReadOnlyRoots:  []string{gitAdmin},
			MutationScopes: []string{"src/**"},
		},
	}

	scope := workspaceScopeForGate(&workspaceCtx, principal, "child-session")
	if scope.PrimaryPath != worktree || scope.SessionID != "child-session" || scope.Principal.AccountScopeID != principal.AccountScopeID {
		t.Fatalf("workspace gate scope identity = %#v", scope)
	}
	assertStringSliceContains(t, scope.ReadOnlyRoots, gitAdmin)
	if len(scope.MutationScopes) != 1 || scope.MutationScopes[0] != "src/**" {
		t.Fatalf("workspace gate mutation scopes = %#v", scope.MutationScopes)
	}
	calls := []tool.Call{
		{CallID: "read-gitdir", Name: "read", Arguments: mustJSON(t, map[string]any{"path": filepath.Join(gitAdmin, "gitdir")})},
		{CallID: "list-git-admin", Name: "list", Arguments: mustJSON(t, map[string]any{"path": gitAdmin})},
	}
	for _, call := range calls {
		request, needed, err := tool.ScopeExpansionForCall(scope, call)
		if err != nil || needed {
			t.Fatalf("%s inside authenticated Coder Git admin root requested expansion: needed=%t request=%+v err=%v", call.Name, needed, request, err)
		}
	}
	_, approved, indexes, changed, waitMS, err := (&Service{}).gateWorkspaceScopeCalls(
		context.Background(), "child-session", "parent-session", "child-run", 1, sessionruntime.ModeAuto,
		worktree, "child", principal, &workspaceCtx, calls, nil,
	)
	if err != nil || len(approved) != len(calls) || len(indexes) != len(calls) || changed || waitMS != 0 {
		t.Fatalf("workspace gate prompted for authenticated Coder reads: approved=%d indexes=%v changed=%t wait_ms=%d err=%v", len(approved), indexes, changed, waitMS, err)
	}
	outside := t.TempDir()
	if _, needed, err := tool.ScopeExpansionForCall(scope, tool.Call{Name: "list", Arguments: mustJSON(t, map[string]any{"path": outside})}); err != nil || !needed {
		t.Fatalf("unrelated list expansion = %t err=%v, want permission", needed, err)
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

func TestPersistentWorkspaceScopeApprovalAllowsDirectorySharedByWorkspaces(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	shared := t.TempDir()
	principal := testRunPrincipal()

	workspaceSvc, workspaceStore, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	if _, err := workspaceSvc.AddForPrincipal(principal, left, "left", "", false); err != nil {
		t.Fatalf("save left workspace: %v", err)
	}
	if _, err := workspaceSvc.AddForPrincipal(principal, right, "right", "", true); err != nil {
		t.Fatalf("save right workspace: %v", err)
	}
	if _, err := workspaceSvc.AddDirectoryForPrincipal(principal, left, shared); err != nil {
		t.Fatalf("link shared directory to left workspace: %v", err)
	}

	sessionID := "session-shared-persistent-add-dir"
	if err := pebblestore.NewSessionStore(rawStore).CreateSessionForAccount(pebblestore.SessionSnapshot{
		ID:            sessionID,
		WorkspacePath: right,
		WorkspaceName: "right",
		Title:         "Shared persistent add-dir",
	}, principal.UserID, principal.AccountScopeID); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(rawStore), nil)
	runSvc := NewService(sessionSvc, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	workspaceCtx := runWorkspaceContext{
		WorkspacePath:        right,
		WorkspaceRoots:       []string{right},
		OriginWorkspacePath:  right,
		OriginWorkspaceRoots: []string{right},
	}

	changed, err := runSvc.applyPersistentWorkspaceScopeAccess(
		sessionID,
		right,
		"right",
		principal,
		tool.ScopeExpansionRequest{DirectoryPath: shared},
		&workspaceCtx,
	)
	if err != nil {
		t.Fatalf("apply shared persistent workspace scope access: %v", err)
	}
	if !changed {
		t.Fatalf("shared persistent add-dir did not report a workspace scope change")
	}
	for _, workspacePath := range []string{left, right} {
		entry, ok, err := workspaceStore.GetForAccount(principal.AccountScopeID, workspacePath)
		if err != nil || !ok {
			t.Fatalf("workspace %q missing after shared add-dir: ok=%t err=%v", workspacePath, ok, err)
		}
		assertStringSliceContains(t, entry.Directories, shared)
	}
	assertStringSliceContains(t, workspaceCtx.OriginWorkspaceRoots, shared)
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

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
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
