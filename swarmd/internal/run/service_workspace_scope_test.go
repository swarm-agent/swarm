package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/discovery"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
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
	if !scope.RejectScopeExpansion {
		t.Fatal("Coder worktree scope permits user-approved expansion")
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
	if !errors.Is(err, tool.ErrWorkspaceScopeExpansionRejected) || needed {
		t.Fatalf("unrelated Git admin path expansion = %t err=%v, want fail-closed rejection", needed, err)
	}
}

func TestRunWorkspaceScopeCoderSourceWorkspaceReadDoesNotRequestPermission(t *testing.T) {
	repo := t.TempDir()
	runTestGit(t, repo, "init", "-b", "dev")
	runTestGit(t, repo, "config", "user.email", "test@example.com")
	runTestGit(t, repo, "config", "user.name", "Test User")
	sourceFile := filepath.Join(repo, "source-only.txt")
	writeTestFile(t, sourceFile, "source content")
	runTestGit(t, repo, "add", "source-only.txt")
	runTestGit(t, repo, "commit", "-m", "base")

	worktree := filepath.Join(t.TempDir(), "child")
	runTestGit(t, repo, "worktree", "add", "-b", "agent/test-source-read", worktree)

	principal := testRunPrincipal()
	runSvc := NewService(nil, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	scope, err := runSvc.resolveRunWorkspaceScope(pebblestore.SessionSnapshot{
		ID:                 "session-coder-source-read",
		UserID:             principal.UserID,
		AccountScopeID:     principal.AccountScopeID,
		WorkspacePath:      worktree,
		WorktreeEnabled:    true,
		WorktreeRootPath:   worktree,
		WorktreeBranch:     "agent/test-source-read",
		WorktreeBaseBranch: "dev",
		Metadata: map[string]any{
			"requested_subagent":              "coder",
			"owned_scope":                     []string{"src/**"},
			"swarm_v3_source_workspace_path":  repo,
			"swarm_v3_runtime_workspace_path": worktree,
		},
	}, principal)
	if err != nil {
		t.Fatalf("resolve Coder source-workspace scope: %v", err)
	}
	if scope.SourceWorkspacePath != repo {
		t.Fatalf("Coder source workspace = %q, want %q", scope.SourceWorkspacePath, repo)
	}
	assertStringSliceContains(t, scope.ReadOnlyRoots, repo)
	assertStringSliceNotContains(t, scope.Roots, repo)

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "read", args: map[string]any{"path": sourceFile}},
		{name: "list", args: map[string]any{"path": repo}},
		{name: "search", args: map[string]any{"query": "source content", "path": repo}},
		{name: "find", args: map[string]any{"query": "source-only", "path": repo}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, needed, err := tool.ScopeExpansionForCall(scope, tool.Call{
				CallID:    tc.name + "-source-workspace",
				Name:      tc.name,
				Arguments: mustJSON(t, tc.args),
			})
			if err != nil || needed {
				t.Fatalf("Coder source-workspace %s requested permission: needed=%t request=%+v err=%v scope=%#v", tc.name, needed, request, err, scope)
			}
		})
	}

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "write", args: map[string]any{"path": sourceFile, "content": "changed"}},
		{name: "edit", args: map[string]any{"path": sourceFile, "old_string": "source", "new_string": "changed"}},
		{name: "webdownload", args: map[string]any{"url": "https://example.com", "output_dir": repo}},
	} {
		t.Run(tc.name+"-fails-closed", func(t *testing.T) {
			request, needed, err := tool.ScopeExpansionForCall(scope, tool.Call{
				CallID:    tc.name + "-source-workspace",
				Name:      tc.name,
				Arguments: mustJSON(t, tc.args),
			})
			if !errors.Is(err, tool.ErrWorkspaceScopeExpansionRejected) || needed {
				t.Fatalf("Coder source-workspace %s expansion = %t request=%+v err=%v, want fail-closed rejection", tc.name, needed, request, err)
			}
		})
	}

	outside := t.TempDir()
	for _, name := range []string{"read", "list", "search", "find"} {
		request, needed, err := tool.ScopeExpansionForCall(scope, tool.Call{
			CallID:    name + "-outside",
			Name:      name,
			Arguments: mustJSON(t, map[string]any{"path": outside, "query": "outside"}),
		})
		if !errors.Is(err, tool.ErrWorkspaceScopeExpansionRejected) || needed {
			t.Fatalf("unrelated %s expansion = %t request=%+v err=%v, want fail-closed rejection", name, needed, request, err)
		}
	}
}

func TestWorkspaceScopeGateRejectsCoderEscapeBeforePermissionCreation(t *testing.T) {
	worktree := t.TempDir()
	outside := t.TempDir()
	principal := testRunPrincipal()
	workspaceCtx := runWorkspaceContext{
		WorkspacePath:        worktree,
		WorkspaceRoots:       []string{worktree},
		OriginWorkspacePath:  worktree,
		OriginWorkspaceRoots: []string{worktree},
		Scope: tool.WorkspaceScope{
			PrimaryPath: worktree, Roots: []string{worktree}, WorktreeEnabled: true,
			WorktreeRootPath: worktree, RejectScopeExpansion: true,
		},
	}
	permissionStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "permissions.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer permissionStore.Close()
	permissions := permission.NewService(pebblestore.NewPermissionStore(permissionStore), nil, nil)
	runSvc := NewService(nil, nil, nil, nil, permissions, nil, discovery.NewService(), nil)

	results, approved, indexes, changed, waitMS, err := runSvc.gateWorkspaceScopeCalls(
		context.Background(), "coder-child", "coder-parent", "coder-run", 1, sessionruntime.ModeAuto,
		worktree, "coder", principal, &workspaceCtx, []tool.Call{{
			CallID: "outside-edit", Name: "edit", Arguments: mustJSON(t, map[string]any{"path": filepath.Join(outside, "README.md"), "old_string": "a", "new_string": "b"}),
		}}, nil,
	)
	if err != nil || len(approved) != 0 || len(indexes) != 0 || changed || waitMS != 0 {
		t.Fatalf("Coder escape gate result: approved=%d indexes=%v changed=%t wait=%d err=%v", len(approved), indexes, changed, waitMS, err)
	}
	if len(results) != 1 || !strings.Contains(results[0].Error, "workspace scope expansion rejected") {
		t.Fatalf("Coder escape result = %#v", results)
	}
	pending, err := permissions.ListPending("coder-parent", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("Coder escape created user-facing permissions: %#v", pending)
	}
}

func TestRunExecutionContextPreservesCoderLinkedWorkspaceReadAuthorization(t *testing.T) {
	repo := t.TempDir()
	runTestGit(t, repo, "init", "-b", "dev")
	runTestGit(t, repo, "config", "user.email", "test@example.com")
	runTestGit(t, repo, "config", "user.name", "Test User")
	writeTestFile(t, filepath.Join(repo, "tracked.txt"), "tracked")
	runTestGit(t, repo, "add", "tracked.txt")
	runTestGit(t, repo, "commit", "-m", "base")

	linked := t.TempDir()
	linkedFile := filepath.Join(linked, "web", "models.tsx")
	writeTestFile(t, linkedFile, "Agent Setup Models serviceTier")
	unrelated := t.TempDir()
	worktree := filepath.Join(t.TempDir(), "child")
	runTestGit(t, repo, "worktree", "add", "-b", "agent/test-live-linked-read", worktree)

	principal := testRunPrincipal()
	workspaceSvc, cleanup := newTestRunWorkspaceService(t)
	defer cleanup()
	if _, err := workspaceSvc.AddForPrincipal(principal, repo, "source", "", true); err != nil {
		t.Fatalf("save source workspace: %v", err)
	}
	if _, err := workspaceSvc.AddDirectoryForPrincipal(principal, repo, linked); err != nil {
		t.Fatalf("link read-only workspace: %v", err)
	}

	runSvc := NewService(nil, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	session := pebblestore.SessionSnapshot{
		ID:                 "session-coder-live-linked-read",
		UserID:             principal.UserID,
		AccountScopeID:     principal.AccountScopeID,
		WorkspacePath:      worktree,
		WorktreeEnabled:    true,
		WorktreeRootPath:   worktree,
		WorktreeBranch:     "agent/test-live-linked-read",
		WorktreeBaseBranch: "dev",
		Metadata: map[string]any{
			"requested_subagent":              "coder",
			"owned_scope":                     []string{"reports/**"},
			"swarm_v3_source_workspace_path":  repo,
			"swarm_v3_runtime_workspace_path": worktree,
		},
	}

	resolved, err := runSvc.resolveRunExecutionContext(session, RunExecutionContext{WorktreeMode: RunWorktreeModeInherit}, principal)
	if err != nil {
		t.Fatalf("resolve live linked-workspace Coder context: %v", err)
	}
	assertStringSliceContains(t, resolved.Scope.ReadOnlyRoots, repo)
	assertStringSliceContains(t, resolved.Scope.ReadOnlyRoots, linked)
	assertStringSliceNotContains(t, resolved.Scope.Roots, repo)
	assertStringSliceNotContains(t, resolved.Scope.Roots, linked)

	workspaceCtx := resolveRunWorkspaceContext(resolved)
	gateScope := workspaceScopeForGate(&workspaceCtx, principal, session.ID)
	for _, call := range []tool.Call{
		{Name: "list", Arguments: mustJSON(t, map[string]any{"path": linked})},
		{Name: "read", Arguments: mustJSON(t, map[string]any{"path": linkedFile})},
		{Name: "search", Arguments: mustJSON(t, map[string]any{"queries": []string{"Agent Setup", "Models"}, "path": linked, "include": "*.tsx"})},
		{Name: "find", Arguments: mustJSON(t, map[string]any{"query": "models", "path": linked})},
	} {
		request, needed, err := tool.ScopeExpansionForCall(gateScope, call)
		if err != nil || needed {
			t.Fatalf("live provider %s requested linked-workspace permission: needed=%t request=%+v err=%v scope=%#v", call.Name, needed, request, err, gateScope)
		}
	}
	for _, call := range []tool.Call{
		{Name: "write", Arguments: mustJSON(t, map[string]any{"path": linkedFile, "content": "changed"})},
		{Name: "search", Arguments: mustJSON(t, map[string]any{"query": "outside", "path": unrelated})},
	} {
		request, needed, err := tool.ScopeExpansionForCall(gateScope, call)
		if !errors.Is(err, tool.ErrWorkspaceScopeExpansionRejected) || needed {
			t.Fatalf("live provider protected %s expansion = %t request=%+v err=%v, want fail-closed rejection", call.Name, needed, request, err)
		}
	}
}

// Requirement: sessions admitted with the mandatory worktree contract must be
// revalidated immediately before provider execution. This prevents stale or
// client-overridden execution contexts from redirecting a write-capable run to
// the shared source checkout after durable admission.
func TestMandatorySessionWorktreeRunRevalidation(t *testing.T) {
	source := t.TempDir()
	worktree := t.TempDir()
	principal := testRunPrincipal()
	state := worktreeruntime.TaskWorkspaceState{WorkspacePath: worktree, BranchName: "agent/session-owned", Clean: true}
	runSvc := NewService(nil, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	runSvc.SetSessionWorktreeInspector(func(path string) (worktreeruntime.TaskWorkspaceState, error) {
		if path != worktree {
			return worktreeruntime.TaskWorkspaceState{}, fmt.Errorf("unexpected worktree path %q", path)
		}
		return state, nil
	})
	session := pebblestore.SessionSnapshot{
		ID: "session-owned", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		WorkspacePath: source, WorktreeEnabled: true, WorktreeRootPath: worktree, WorktreeBranch: state.BranchName, WorktreeBaseBranch: "dev",
		Metadata: map[string]any{
			"swarm_v3_source_workspace_path": source, "swarm_v3_runtime_workspace_path": worktree,
			"swarm_v3_mandatory_worktree": true, "swarm_v3_worktree_owner_session_id": "session-owned",
		},
	}

	resolved, err := runSvc.resolveRunExecutionContext(session, RunExecutionContext{WorktreeMode: RunWorktreeModeInherit}, principal)
	if err != nil {
		t.Fatalf("resolve mandatory worktree context: %v", err)
	}
	if resolved.Scope.PrimaryPath != worktree || !resolved.Scope.WorktreeEnabled || len(resolved.Scope.MutationScopes) != 1 || resolved.Scope.MutationScopes[0] != "." {
		t.Fatalf("mandatory worktree scope = %+v", resolved.Scope)
	}

	for _, test := range []struct {
		name    string
		request RunExecutionContext
		mutate  func(*pebblestore.SessionSnapshot)
		want    string
	}{
		{name: "off prohibited", request: RunExecutionContext{WorktreeMode: RunWorktreeModeOff}, want: "worktree_mode=off is prohibited"},
		{name: "source override prohibited", request: RunExecutionContext{WorktreeMode: RunWorktreeModeInherit, WorkspacePath: source}, want: "workspace_path must identify"},
		{name: "foreign owner rejected", request: RunExecutionContext{WorktreeMode: RunWorktreeModeInherit}, mutate: func(s *pebblestore.SessionSnapshot) {
			s.Metadata["swarm_v3_worktree_owner_session_id"] = "other-session"
		}, want: "ownership or lineage is incomplete"},
		{name: "branch drift rejected", request: RunExecutionContext{WorktreeMode: RunWorktreeModeInherit}, mutate: func(*pebblestore.SessionSnapshot) { state.BranchName = "dev" }, want: "branch changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := session
			candidate.Metadata = cloneGenericMap(session.Metadata)
			state.BranchName = "agent/session-owned"
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			_, err := runSvc.resolveRunExecutionContext(candidate, test.request, principal)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestRunExecutionContextPreservesCoderSourceWorkspaceReadAuthorization(t *testing.T) {
	repo := t.TempDir()
	runTestGit(t, repo, "init", "-b", "dev")
	runTestGit(t, repo, "config", "user.email", "test@example.com")
	runTestGit(t, repo, "config", "user.name", "Test User")
	sourceFile := filepath.Join(repo, "projects", "source-only.txt")
	writeTestFile(t, sourceFile, "source content")
	runTestGit(t, repo, "add", "projects/source-only.txt")
	runTestGit(t, repo, "commit", "-m", "base")

	worktree := filepath.Join(t.TempDir(), "child")
	runTestGit(t, repo, "worktree", "add", "-b", "agent/test-live-source-read", worktree)

	principal := testRunPrincipal()
	runSvc := NewService(nil, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	session := pebblestore.SessionSnapshot{
		ID:                 "session-coder-live-source-read",
		UserID:             principal.UserID,
		AccountScopeID:     principal.AccountScopeID,
		WorkspacePath:      worktree,
		WorktreeEnabled:    true,
		WorktreeRootPath:   worktree,
		WorktreeBranch:     "agent/test-live-source-read",
		WorktreeBaseBranch: "dev",
		Metadata: map[string]any{
			"requested_subagent":              "coder",
			"owned_scope":                     []string{"projects/output/**"},
			"swarm_v3_source_workspace_path":  repo,
			"swarm_v3_runtime_workspace_path": worktree,
		},
	}

	resolved, err := runSvc.resolveRunExecutionContext(session, RunExecutionContext{WorktreeMode: RunWorktreeModeInherit}, principal)
	if err != nil {
		t.Fatalf("resolve live Coder execution context: %v", err)
	}
	assertStringSliceContains(t, resolved.Scope.ReadOnlyRoots, repo)
	assertStringSliceNotContains(t, resolved.Scope.Roots, repo)
	if len(resolved.Scope.MutationScopes) != 1 || resolved.Scope.MutationScopes[0] != "projects/output/**" {
		t.Fatalf("live Coder mutation scopes = %#v", resolved.Scope.MutationScopes)
	}

	workspaceCtx := resolveRunWorkspaceContext(resolved)
	gateScope := workspaceScopeForGate(&workspaceCtx, principal, session.ID)
	for _, call := range []tool.Call{
		{Name: "list", Arguments: mustJSON(t, map[string]any{"path": filepath.Dir(sourceFile)})},
		{Name: "read", Arguments: mustJSON(t, map[string]any{"path": sourceFile})},
	} {
		request, needed, err := tool.ScopeExpansionForCall(gateScope, call)
		if err != nil || needed {
			t.Fatalf("live provider %s requested source-workspace permission: needed=%t request=%+v err=%v scope=%#v", call.Name, needed, request, err, gateScope)
		}
	}

	request, needed, err := tool.ScopeExpansionForCall(gateScope, tool.Call{
		Name:      "write",
		Arguments: mustJSON(t, map[string]any{"path": sourceFile, "content": "changed"}),
	})
	if !errors.Is(err, tool.ErrWorkspaceScopeExpansionRejected) || needed {
		t.Fatalf("live provider source-workspace write expansion = %t request=%+v err=%v, want fail-closed rejection", needed, request, err)
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
			PrimaryPath:          worktree,
			Roots:                []string{worktree},
			ReadOnlyRoots:        []string{gitAdmin},
			MutationScopes:       []string{"src/**"},
			RejectScopeExpansion: true,
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
	if _, needed, err := tool.ScopeExpansionForCall(scope, tool.Call{Name: "list", Arguments: mustJSON(t, map[string]any{"path": outside})}); !errors.Is(err, tool.ErrWorkspaceScopeExpansionRejected) || needed {
		t.Fatalf("unrelated list expansion = %t err=%v, want fail-closed rejection", needed, err)
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

// Requirement: every active account-saved workspace is already user-authorized
// filesystem scope. The regression threat is a provider call under a different
// saved root entering requestWorkspaceScopePermission and interrupting the user.
// This run-scope/gate test is the narrowest layer that proves every path-bearing
// tool classification and the unsaved-path negative case before tool execution.
func TestResolveRunWorkspaceScopeIncludesEveryAccountSavedWorkspaceWithoutSwitch(t *testing.T) {
	primary := t.TempDir()
	secondary := t.TempDir()
	external := t.TempDir()
	principal := testRunPrincipal()
	workspaceSvc, _, _, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	primaryResolution, err := workspaceSvc.AddForPrincipal(principal, primary, "primary", "", true)
	if err != nil {
		t.Fatalf("save primary workspace: %v", err)
	}
	secondaryResolution, err := workspaceSvc.AddForPrincipal(principal, secondary, "secondary", "", false)
	if err != nil {
		t.Fatalf("save secondary workspace: %v", err)
	}

	runSvc := NewService(nil, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	scope, err := runSvc.resolveRunWorkspaceScope(pebblestore.SessionSnapshot{
		ID: "session-global-saved-workspaces", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		WorkspacePath: primary, WorkspaceName: "primary",
		Metadata: map[string]any{"swarm_v3_source_workspace_id": primaryResolution.WorkspaceID, "swarm_v3_source_workspace_generation": primaryResolution.WorkspaceGeneration},
	}, principal)
	if err != nil {
		t.Fatalf("resolve global saved workspace scope: %v", err)
	}
	if scope.PrimaryPath != primary {
		t.Fatalf("primary path switched: got %q want %q", scope.PrimaryPath, primary)
	}
	assertStringSliceContains(t, scope.Roots, primary)
	assertStringSliceContains(t, scope.Roots, secondary)
	secondaryFile := filepath.Join(secondary, "allowed.txt")
	if err := os.WriteFile(secondaryFile, []byte("saved workspace"), 0o600); err != nil {
		t.Fatalf("seed saved secondary workspace: %v", err)
	}
	calls := []tool.Call{
		{CallID: "read-saved", Name: "read", Arguments: mustJSON(t, map[string]any{"path": secondaryFile})},
		{CallID: "list-saved", Name: "list", Arguments: mustJSON(t, map[string]any{"path": secondary})},
		{CallID: "search-saved", Name: "search", Arguments: mustJSON(t, map[string]any{"path": secondary, "query": "saved workspace"})},
		{CallID: "find-saved", Name: "find", Arguments: mustJSON(t, map[string]any{"path": secondary, "query": "allowed.txt"})},
		{CallID: "search-saved-paths", Name: "search", Arguments: mustJSON(t, map[string]any{"paths": []string{secondary}, "query": "saved workspace"})},
		{CallID: "agentic-search-saved", Name: "agentic_search", Arguments: mustJSON(t, map[string]any{"path": secondary, "query": "saved workspace"})},
		{CallID: "task-saved", Name: "task", Arguments: mustJSON(t, map[string]any{"workspace_path": secondary, "prompt": "inspect"})},
		{CallID: "write-saved", Name: "write", Arguments: mustJSON(t, map[string]any{"path": filepath.Join(secondary, "new.txt"), "content": "new"})},
		{CallID: "edit-saved", Name: "edit", Arguments: mustJSON(t, map[string]any{"path": secondaryFile, "old_string": "saved", "new_string": "pooled"})},
		{CallID: "download-saved", Name: "webdownload", Arguments: mustJSON(t, map[string]any{"url": "https://example.com", "output_dir": secondary})},
	}
	for _, call := range calls {
		if request, needsApproval, err := tool.ScopeExpansionForCall(scope, call); err != nil || needsApproval {
			t.Fatalf("saved secondary workspace %s required expansion: approval=%t request=%+v err=%v", call.Name, needsApproval, request, err)
		}
	}
	workspaceCtx := runWorkspaceContext{
		WorkspacePath: primary, WorkspaceRoots: append([]string(nil), scope.Roots...),
		OriginWorkspacePath: primary, OriginWorkspaceRoots: append([]string(nil), scope.Roots...), Scope: scope,
	}
	_, approved, indexes, changed, waitMS, err := runSvc.gateWorkspaceScopeCalls(
		context.Background(), "session-global-saved-workspaces", "session-global-saved-workspaces", "run-global-saved-workspaces", 1, sessionruntime.ModeAuto,
		primary, "primary", principal, &workspaceCtx, calls, nil,
	)
	if err != nil || len(approved) != len(calls) || len(indexes) != len(calls) || changed || waitMS != 0 {
		t.Fatalf("saved workspace tools prompted for permission: approved=%d indexes=%v changed=%t wait_ms=%d err=%v", len(approved), indexes, changed, waitMS, err)
	}
	for _, call := range []tool.Call{
		{Name: "read", Arguments: mustJSON(t, map[string]any{"path": filepath.Join(external, "blocked.txt")})},
		{Name: "list", Arguments: mustJSON(t, map[string]any{"path": external})},
		{Name: "search", Arguments: mustJSON(t, map[string]any{"path": external, "query": "blocked"})},
		{Name: "find", Arguments: mustJSON(t, map[string]any{"path": external, "query": "blocked.txt"})},
		{Name: "agentic_search", Arguments: mustJSON(t, map[string]any{"path": external, "query": "blocked"})},
		{Name: "task", Arguments: mustJSON(t, map[string]any{"workspace_path": external, "prompt": "inspect"})},
		{Name: "write", Arguments: mustJSON(t, map[string]any{"path": filepath.Join(external, "blocked.txt"), "content": "blocked"})},
		{Name: "edit", Arguments: mustJSON(t, map[string]any{"path": filepath.Join(external, "blocked.txt"), "old_string": "a", "new_string": "b"})},
		{Name: "webdownload", Arguments: mustJSON(t, map[string]any{"url": "https://example.com", "output_dir": external})},
	} {
		request, needsApproval, err := tool.ScopeExpansionForCall(scope, call)
		if err != nil || !needsApproval || request.DirectoryPath != external {
			t.Fatalf("unsaved external %s decision: approval=%t request=%+v err=%v", call.Name, needsApproval, request, err)
		}
	}
	if secondaryResolution.WorkspaceID == "" {
		t.Fatal("secondary saved workspace lost identity")
	}
}

func TestWorkspaceScopeApprovalAddsSessionTemporaryGrantWithoutMutatingWorkspaceCatalog(t *testing.T) {
	primary := t.TempDir()
	linked := t.TempDir()
	principal := testRunPrincipal()

	workspaceSvc, workspaceStore, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	if _, err := workspaceSvc.AddForPrincipal(principal, primary, "primary", "", true); err != nil {
		t.Fatalf("save primary workspace: %v", err)
	}

	sessionID := "session-temporary-workspace-scope"
	if err := pebblestore.NewSessionStore(rawStore).CreateSessionForAccount(pebblestore.SessionSnapshot{
		ID:            sessionID,
		WorkspacePath: primary,
		WorkspaceName: "primary",
		Title:         "Temporary workspace scope",
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

	changed, err := runSvc.applyWorkspaceScopeApproval(
		sessionID,
		primary,
		"primary",
		principal,
		workspaceScopeDecisionSessionAllow,
		tool.ScopeExpansionRequest{DirectoryPath: linked},
		&workspaceCtx,
	)
	if err != nil {
		t.Fatalf("apply session workspace scope approval: %v", err)
	}
	if !changed {
		t.Fatalf("session workspace scope approval did not report a scope change")
	}

	session, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("session missing after workspace scope approval: ok=%t err=%v", ok, err)
	}
	assertStringSliceContains(t, session.TemporaryWorkspaceRoots, linked)
	if len(session.WorkspaceGrants) != 1 || session.WorkspaceGrants[0].Kind != pebblestore.WorkspaceGrantTemporary || session.WorkspaceGrants[0].Path != linked {
		t.Fatalf("session workspace grants = %+v, want one temporary grant for %q", session.WorkspaceGrants, linked)
	}
	assertStringSliceContains(t, workspaceCtx.OriginWorkspaceRoots, linked)
	assertStringSliceContains(t, workspaceCtx.WorkspaceRoots, linked)

	accountEntry, ok, err := workspaceStore.GetForAccount(principal.AccountScopeID, primary)
	if err != nil || !ok {
		t.Fatalf("account workspace entry missing after approval: ok=%t err=%v", ok, err)
	}
	assertStringSliceNotContains(t, accountEntry.Directories, linked)
	if legacyEntry, ok, err := workspaceStore.GetLegacy(primary); err != nil || ok || len(legacyEntry.Directories) != 0 {
		t.Fatalf("session approval mutated legacy workspace entry: entry=%+v ok=%t err=%v", legacyEntry, ok, err)
	}
}

func TestWorkspaceScopeApprovalDoesNotShareTemporaryGrantAcrossWorkspaces(t *testing.T) {
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

	sessionID := "session-shared-temporary-scope"
	if err := pebblestore.NewSessionStore(rawStore).CreateSessionForAccount(pebblestore.SessionSnapshot{
		ID:            sessionID,
		WorkspacePath: right,
		WorkspaceName: "right",
		Title:         "Shared temporary scope",
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

	changed, err := runSvc.applyWorkspaceScopeApproval(
		sessionID,
		right,
		"right",
		principal,
		workspaceScopeDecisionSessionAllow,
		tool.ScopeExpansionRequest{DirectoryPath: shared},
		&workspaceCtx,
	)
	if err != nil {
		t.Fatalf("apply temporary shared workspace scope: %v", err)
	}
	if !changed {
		t.Fatalf("temporary shared workspace scope did not report a scope change")
	}
	assertStringSliceContains(t, workspaceCtx.OriginWorkspaceRoots, shared)

	leftEntry, ok, err := workspaceStore.GetForAccount(principal.AccountScopeID, left)
	if err != nil || !ok {
		t.Fatalf("left workspace missing after approval: ok=%t err=%v", ok, err)
	}
	assertStringSliceContains(t, leftEntry.Directories, shared)
	rightEntry, ok, err := workspaceStore.GetForAccount(principal.AccountScopeID, right)
	if err != nil || !ok {
		t.Fatalf("right workspace missing after approval: ok=%t err=%v", ok, err)
	}
	assertStringSliceNotContains(t, rightEntry.Directories, shared)
}

func TestResolveRunWorkspaceScopeRejectsStaleCapturedWorkspaceGeneration(t *testing.T) {
	primary := t.TempDir()
	principal := testRunPrincipal()
	workspaceSvc, _, _, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	created, err := workspaceSvc.AddForPrincipal(principal, primary, "primary", "", true)
	if err != nil {
		t.Fatalf("save primary workspace: %v", err)
	}
	runSvc := NewService(nil, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	session := pebblestore.SessionSnapshot{
		ID: "session-stale-generation", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: primary,
		Metadata: map[string]any{
			"swarm_v3_source_workspace_id":         created.WorkspaceID,
			"swarm_v3_source_workspace_generation": "2",
		},
	}
	_, err = runSvc.resolveRunWorkspaceScope(session, principal)
	if err == nil || !strings.Contains(err.Error(), "workspace generation is stale") {
		t.Fatalf("resolve stale generation error = %v", err)
	}
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
