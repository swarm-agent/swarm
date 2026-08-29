package run

import (
	"context"
	"os"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/discovery"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// These post-codegen tests are Lane B evidence candidates for the frozen
// flat-global-workspaces scenario manifest. Each test names the E2E scenarios
// whose runtime contract it exercises through production entry points.

func TestLaneB_E2E014_E2E015_SavedWorkspaceScopeNeedsNoExpansion(t *testing.T) {
	primary := t.TempDir()
	principal := testRunPrincipal()
	workspaceSvc, cleanup := newTestRunWorkspaceService(t)
	defer cleanup()

	created, err := workspaceSvc.AddForPrincipal(principal, primary, "saved", "", true)
	if err != nil {
		t.Fatalf("save account workspace: %v", err)
	}
	runSvc := NewService(nil, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	scope, err := runSvc.ResolveRuntimeWorkspaceScope(pebblestore.SessionSnapshot{
		ID: "lane-b-auto-manage", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		WorkspacePath: primary,
		Metadata: map[string]any{
			"swarm_v3_source_workspace_id":         created.WorkspaceID,
			"swarm_v3_source_workspace_generation": "1",
		},
	}, principal)
	if err != nil {
		t.Fatalf("resolve saved workspace scope: %v", err)
	}
	for _, call := range []tool.Call{
		{Name: "read", Arguments: `{"path":"inside.txt"}`},
		{Name: "write", Arguments: `{"path":"nested/new.txt"}`},
	} {
		request, needsApproval, err := tool.ScopeExpansionForCall(scope, call)
		if err != nil || needsApproval {
			t.Fatalf("%s saved-path scope: approval=%t request=%+v err=%v", call.Name, needsApproval, request, err)
		}
	}
}

func TestLaneB_E2E016_E2E017_E2E018_TemporaryGrantIsSessionOnlyAndReusable(t *testing.T) {
	primary := t.TempDir()
	external := t.TempDir()
	principal := testRunPrincipal()
	workspaceSvc, _, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	if _, err := workspaceSvc.AddForPrincipal(principal, primary, "primary", "", true); err != nil {
		t.Fatalf("save primary workspace: %v", err)
	}

	sessionStore := pebblestore.NewSessionStore(rawStore)
	for _, id := range []string{"lane-b-granted", "lane-b-ungranted"} {
		if err := sessionStore.CreateSessionForAccount(pebblestore.SessionSnapshot{
			ID: id, WorkspacePath: primary, WorkspaceName: "primary", Title: id,
		}, principal.UserID, principal.AccountScopeID); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	sessionSvc := sessionruntime.NewService(sessionStore, nil)
	runSvc := NewService(sessionSvc, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	runSvc.SetWorkspaceService(workspaceSvc)
	workspaceCtx := runWorkspaceContext{
		WorkspacePath: primary, WorkspaceRoots: []string{primary},
		OriginWorkspacePath: primary, OriginWorkspaceRoots: []string{primary},
	}

	changed, err := runSvc.applyWorkspaceScopeApproval(
		"lane-b-granted", primary, "primary", principal,
		workspaceScopeDecisionSessionAllow, tool.ScopeExpansionRequest{DirectoryPath: external}, &workspaceCtx,
	)
	if err != nil || !changed {
		t.Fatalf("apply temporary grant: changed=%t err=%v", changed, err)
	}
	granted, ok, err := sessionSvc.GetSession("lane-b-granted")
	if err != nil || !ok {
		t.Fatalf("load granted session: ok=%t err=%v", ok, err)
	}
	ungranted, ok, err := sessionSvc.GetSession("lane-b-ungranted")
	if err != nil || !ok {
		t.Fatalf("load ungranted session: ok=%t err=%v", ok, err)
	}
	if len(granted.WorkspaceGrants) != 1 || granted.WorkspaceGrants[0].Kind != pebblestore.WorkspaceGrantTemporary || granted.WorkspaceGrants[0].Path != external {
		t.Fatalf("granted session grants = %+v", granted.WorkspaceGrants)
	}
	if len(ungranted.WorkspaceGrants) != 0 || len(ungranted.TemporaryWorkspaceRoots) != 0 {
		t.Fatalf("temporary grant leaked to second session: grants=%+v roots=%v", ungranted.WorkspaceGrants, ungranted.TemporaryWorkspaceRoots)
	}

	grantedScope, err := runSvc.ResolveRuntimeWorkspaceScope(granted, principal)
	if err != nil {
		t.Fatalf("resolve granted scope: %v", err)
	}
	request, needsApproval, err := tool.ScopeExpansionForCall(grantedScope, tool.Call{Name: "write", Arguments: `{"path":"` + external + `/again.txt"}`})
	if err != nil || needsApproval {
		t.Fatalf("reuse temporary grant: approval=%t request=%+v err=%v", needsApproval, request, err)
	}
	ungrantedScope, err := runSvc.ResolveRuntimeWorkspaceScope(ungranted, principal)
	if err != nil {
		t.Fatalf("resolve ungranted scope: %v", err)
	}
	request, needsApproval, err = tool.ScopeExpansionForCall(ungrantedScope, tool.Call{Name: "read", Arguments: `{"path":"` + external + `"}`})
	if err != nil || !needsApproval || request.DirectoryPath != external {
		t.Fatalf("external path in second session: approval=%t request=%+v err=%v", needsApproval, request, err)
	}
}

func TestLaneB_E2E027_E2E028_RejectsStaleWorkspaceIdentityBeforeScope(t *testing.T) {
	primary := t.TempDir()
	principal := testRunPrincipal()
	workspaceSvc, cleanup := newTestRunWorkspaceService(t)
	defer cleanup()
	created, err := workspaceSvc.AddForPrincipal(principal, primary, "primary", "", true)
	if err != nil {
		t.Fatalf("save primary workspace: %v", err)
	}
	runSvc := NewService(nil, nil, nil, nil, nil, nil, discovery.NewService(), nil)
	runSvc.SetWorkspaceService(workspaceSvc)

	cases := []struct {
		name, id, generation, want string
	}{
		{name: "stale id", id: "workspace-other", generation: "1", want: "workspace identity is stale"},
		{name: "stale generation", id: created.WorkspaceID, generation: "2", want: "workspace generation is stale"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runSvc.ResolveRuntimeWorkspaceScope(pebblestore.SessionSnapshot{
				ID: "lane-b-stale", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
				WorkspacePath: primary,
				Metadata: map[string]any{
					"swarm_v3_source_workspace_id":         tc.id,
					"swarm_v3_source_workspace_generation": tc.generation,
				},
			}, principal)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("resolve error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLaneB_E2E023_E2E024_E2E025_ExternalOutputAndContainment(t *testing.T) {
	primary := t.TempDir()
	external := t.TempDir()

	request, needsApproval, err := tool.ScopeExpansionForCall(
		tool.WorkspaceScope{PrimaryPath: primary, Roots: []string{primary}},
		tool.Call{Name: "webdownload", Arguments: `{"output_dir":"` + external + `"}`},
	)
	if err != nil || !needsApproval || request.ArgumentName != "output_dir" || request.DirectoryPath != external {
		t.Fatalf("external webdownload scope: approval=%t request=%+v err=%v", needsApproval, request, err)
	}

	_, needsApproval, err = tool.ScopeExpansionForCall(
		tool.WorkspaceScope{PrimaryPath: primary, Roots: []string{primary}},
		tool.Call{Name: "read", Arguments: `{"path":"/"}`},
	)
	if err == nil || needsApproval || !strings.Contains(err.Error(), "refusing to add filesystem root") {
		t.Fatalf("filesystem-root scope: approval=%t err=%v", needsApproval, err)
	}

	link := primary + "-escape"
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	request, needsApproval, err = tool.ScopeExpansionForCall(
		tool.WorkspaceScope{PrimaryPath: primary, Roots: []string{primary}},
		tool.Call{Name: "read", Arguments: `{"path":"` + link + `"}`},
	)
	if err != nil || !needsApproval || request.DirectoryPath != external {
		t.Fatalf("symlink escape scope: approval=%t request=%+v err=%v", needsApproval, request, err)
	}
}

func TestLaneB_E2E031_E2E032_E2E033_E2E034_WorkspaceScopeDoesNotDecideOperationPolicy(t *testing.T) {
	primary := t.TempDir()
	scope := tool.WorkspaceScope{PrimaryPath: primary, Roots: []string{primary}}
	for _, name := range []string{"bash", "git", "task", "manage_sessions"} {
		request, needsApproval, err := tool.ScopeExpansionForCall(scope, tool.Call{Name: name, Arguments: `{}`})
		if err != nil || needsApproval || request != (tool.ScopeExpansionRequest{}) {
			t.Fatalf("%s workspace phase unexpectedly decided operation policy: approval=%t request=%+v err=%v", name, needsApproval, request, err)
		}
	}

	permissionPayload := workspaceScopePermissionArguments(
		workspaceScopePermissionTarget{Exists: true, Path: primary, Name: "saved"},
		tool.Call{Name: "write", Arguments: `{"path":"outside.txt"}`},
		tool.ScopeExpansionRequest{RequestedPath: "outside.txt", TargetPath: "/outside.txt", DirectoryPath: "/outside"},
	)
	if !strings.Contains(permissionPayload, `"session_allow"`) || strings.Contains(permissionPayload, `"operation_allow"`) || strings.Contains(permissionPayload, `"workspace_add_dir"`) {
		t.Fatalf("workspace permission actions conflate authorities: %s", permissionPayload)
	}
	for _, want := range []string{"for this chat session only", "add this folder as a new workspace from the workspace picker"} {
		if !strings.Contains(permissionPayload, want) {
			t.Fatalf("workspace permission language missing %q: %s", want, permissionPayload)
		}
	}
	for _, retired := range []string{"add to your workspace", "link to your workspace", "workspace group"} {
		if strings.Contains(strings.ToLower(permissionPayload), retired) {
			t.Fatalf("workspace permission language retains retired wording %q: %s", retired, permissionPayload)
		}
	}
}

// TestLaneE_E2E035AutoManagedWorkspaceCannotDowngradeDestructiveBash covers
// E2E-035/REQ-OP-001/REQ-OP-003 across both authorities. An in-scope path
// needs no workspace expansion, but the backend must still promote an
// under-declared destructive command and require operation approval.
func TestLaneE_E2E035AutoManagedWorkspaceCannotDowngradeDestructiveBash(t *testing.T) {
	primary := t.TempDir()
	arguments := `{"command":"rm inside.txt","explanation":["Remove inside.txt."],"category":"update","critical":false}`
	call := tool.Call{CallID: "lane-e-destructive", Name: "bash", Arguments: arguments}

	request, needsApproval, err := tool.ScopeExpansionForCall(tool.WorkspaceScope{PrimaryPath: primary, Roots: []string{primary}}, call)
	if err != nil || needsApproval || request != (tool.ScopeExpansionRequest{}) {
		t.Fatalf("authorized workspace unexpectedly decided destructive operation policy: approval=%t request=%+v err=%v", needsApproval, request, err)
	}

	policy := permission.DefaultPolicy()
	policy.BashProfile = permission.BashApprovalProfileOnlyCriticalPrompts
	explain := permission.ExplainPolicy("auto", "bash", arguments, policy)
	if explain.BashEffect == nil {
		t.Fatal("destructive bash had no backend effect assessment")
	}
	effect := *explain.BashEffect
	if !effect.Valid || !effect.Promoted || effect.DeclaredCategory != permission.BashEffectUpdate || effect.Category != permission.BashEffectDelete || !effect.Critical {
		t.Fatalf("destructive bash was not promoted to critical delete: %+v", effect)
	}
	if explain.Decision != permission.PolicyDecisionAsk || explain.Source != "bash_profile" || !strings.Contains(explain.Reason, "delete") {
		t.Fatalf("authorized workspace downgraded destructive operation gate: %+v", explain)
	}
}

func TestLaneB_E2E029_AccountWorkspaceIdentityCannotCrossAccounts(t *testing.T) {
	primary := t.TempDir()
	owner := testRunPrincipal()
	other := owner
	other.UserID = "user-2"
	other.AccountScopeID = "account-2"
	workspaceSvc, cleanup := newTestRunWorkspaceService(t)
	defer cleanup()
	created, err := workspaceSvc.AddForPrincipal(owner, primary, "owner-only", "", true)
	if err != nil {
		t.Fatalf("save owner workspace: %v", err)
	}
	if _, ok, err := workspaceSvc.GetByWorkspaceIDForPrincipal(other, created.WorkspaceID); err != nil || ok {
		t.Fatalf("cross-account lookup: ok=%t err=%v", ok, err)
	}
	scope, err := workspaceSvc.ScopeForPathForPrincipal(other, primary)
	if err != nil {
		t.Fatalf("cross-account scope lookup: %v", err)
	}
	if scope.Matched || scope.WorkspaceID != "" || scope.WorkspaceGeneration != 0 || scope.WorkspaceState != "" {
		t.Fatalf("cross-account workspace authority leaked: %+v", scope)
	}
	if scope.ResolvedPath != primary || scope.WorkspacePath != primary || len(scope.Directories) != 1 || scope.Directories[0] != primary {
		t.Fatalf("cross-account unmatched scope did not preserve the requested path: %+v", scope)
	}
}

func TestLaneE_E2E030WorkspaceAndOperationPolicyChangesAffectOnlyLaterCalls(t *testing.T) {
	primary := t.TempDir()
	external := t.TempDir()
	call := tool.Call{Name: "write", Arguments: `{"path":"` + external + `/change.txt","content":"blocked"}`}

	beforeRequest, beforeNeedsApproval, err := tool.ScopeExpansionForCall(tool.WorkspaceScope{PrimaryPath: primary, Roots: []string{primary}}, call)
	if err != nil || !beforeNeedsApproval || beforeRequest.DirectoryPath != external {
		t.Fatalf("pre-change workspace decision = approval=%t request=%+v err=%v", beforeNeedsApproval, beforeRequest, err)
	}
	_, afterNeedsApproval, err := tool.ScopeExpansionForCall(tool.WorkspaceScope{PrimaryPath: primary, Roots: []string{primary, external}}, call)
	if err != nil || afterNeedsApproval {
		t.Fatalf("post-change workspace decision = approval=%t err=%v", afterNeedsApproval, err)
	}
	if !beforeNeedsApproval {
		t.Fatal("later workspace policy evaluation mutated the earlier decision")
	}

	allowWrite := permission.NormalizePolicy(permission.Policy{Version: 1, Rules: []permission.PolicyRule{{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "write"}}})
	denyWrite := permission.NormalizePolicy(permission.Policy{Version: 1, Rules: []permission.PolicyRule{{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionDeny, Tool: "write"}}})
	first := permission.ExplainPolicy("auto", "write", call.Arguments, allowWrite)
	second := permission.ExplainPolicy("auto", "write", call.Arguments, denyWrite)
	if first.Decision != permission.PolicyDecisionAllow || second.Decision != permission.PolicyDecisionDeny {
		t.Fatalf("operation policy changes did not affect only the later call: first=%+v second=%+v", first, second)
	}
}

func TestLaneB_E2E031_E2E034_SavedWorkspaceDoesNotBypassOperationPolicy(t *testing.T) {
	svc, sessionID, _, cleanup := newTaskLaunchPermissionServiceWithPermissions(t)
	defer cleanup()

	denyWrite := permission.NormalizePolicy(permission.Policy{Version: 1, Rules: []permission.PolicyRule{{
		Kind:     permission.PolicyRuleKindTool,
		Decision: permission.PolicyDecisionDeny,
		Tool:     "write",
	}}})

	primary := t.TempDir()
	scope := tool.WorkspaceScope{PrimaryPath: primary, Roots: []string{primary}}
	call := tool.Call{CallID: "lane-b-policy-deny", Name: "write", Arguments: `{"path":"inside.txt","content":"blocked"}`}
	request, needsScopeApproval, err := tool.ScopeExpansionForCall(scope, call)
	if err != nil || needsScopeApproval || request != (tool.ScopeExpansionRequest{}) {
		t.Fatalf("saved workspace scope unexpectedly blocked call before operation policy: approval=%t request=%+v err=%v", needsScopeApproval, request, err)
	}

	results, approved, _, _, _, err := svc.gateToolCalls(context.Background(), sessionID, "lane-b-policy-run", 1, "auto", []tool.Call{call}, nil, &denyWrite)
	if err != nil {
		t.Fatalf("evaluate operation policy: %v", err)
	}
	if len(approved) != 0 || len(results) != 1 || !strings.Contains(strings.ToLower(results[0].Error), "denied by deny tool: write") {
		t.Fatalf("operation policy was bypassed after workspace authorization: approved=%+v results=%+v", approved, results)
	}
	if _, statErr := os.Stat(primary + "/inside.txt"); !os.IsNotExist(statErr) {
		t.Fatalf("denied operation produced a filesystem side effect: %v", statErr)
	}
}
