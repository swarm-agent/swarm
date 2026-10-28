package run

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const descendantTestParent = "parent-swarm"

type descendantScopeFixture struct {
	service   *Service
	principal identity.Principal
	parentDir string
	roots     map[string]string
}

func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// newDescendantScopeFixture seeds real worktree sessions with durable
// ownership claims (the same legacy-admission path the API tests use) in an
// isolated Pebble store. Lineage comes only from parent_session_id metadata.
func newDescendantScopeFixture(t *testing.T) descendantScopeFixture {
	t.Helper()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := pebblestore.NewSessionStore(db)
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	type seed struct{ id, parent, account string }
	seeds := []seed{
		{"child", descendantTestParent, "account"},
		{"grandchild", "child", "account"},
		{"sibling", "other-parent", "account"},
		{"unrelated", "", "account"},
		{"foreign", descendantTestParent, "other-account"},
	}
	fx := descendantScopeFixture{principal: p, parentDir: resolvedTempDir(t), roots: map[string]string{}}
	snapshots := make([]pebblestore.SessionSnapshot, 0, len(seeds))
	for _, s := range seeds {
		root := resolvedTempDir(t)
		if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "pkg", "result.go"), []byte("package pkg // "+s.id+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		fx.roots[s.id] = root
		metadata := map[string]any{"swarm_v3_worktree_owner_session_id": s.id, "swarm_v3_runtime_workspace_path": root, "lineage_kind": "delegated_subagent"}
		if s.parent != "" {
			metadata["parent_session_id"] = s.parent
		}
		snapshot := pebblestore.SessionSnapshot{ID: s.id, WorktreeEnabled: true, WorktreeRootPath: root, WorktreeBranch: "agent/" + s.id, WorkspacePath: root, Metadata: metadata, AccountScopeID: s.account, UserID: p.UserID}
		if err := store.CreateSessionForAccount(snapshot, p.UserID, s.account); err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := store.CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := range snapshots {
		snapshot := snapshots[i]
		if _, err := store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{Kind: pebblestore.V3SessionMutationUpdateMetadata, SessionID: snapshot.ID, UserID: snapshot.UserID, AccountScopeID: snapshot.AccountScopeID, Session: &snapshot, ClientRequestID: "admit-" + snapshot.ID, PayloadHash: "admit-" + snapshot.ID, WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "legacy", Path: snapshot.WorktreeRootPath, SourcePath: snapshot.WorktreeRootPath, OwnerSessionID: snapshot.ID, Branch: snapshot.WorktreeBranch}}); err != nil {
			t.Fatalf("admit %s: %v", snapshot.ID, err)
		}
	}
	// No permission service: a call that reaches the prompt path fails with
	// "permission system is not configured" instead of waiting on a person.
	fx.service = &Service{sessions: sessionruntime.NewService(store, nil)}
	return fx
}

func (fx descendantScopeFixture) workspaceCtx() *runWorkspaceContext {
	return &runWorkspaceContext{
		WorkspacePath: fx.parentDir, WorkspaceRoots: []string{fx.parentDir},
		OriginWorkspacePath: fx.parentDir, OriginWorkspaceRoots: []string{fx.parentDir},
		Scope: tool.WorkspaceScope{PrimaryPath: fx.parentDir, Roots: []string{fx.parentDir}},
	}
}

func (fx descendantScopeFixture) gate(t *testing.T, sessionID string, ctx *runWorkspaceContext, call tool.Call) (tool.Result, bool) {
	t.Helper()
	results, approved, _, _, _, err := fx.service.gateWorkspaceScopeCalls(context.Background(), sessionID, sessionID, "run", 1, "auto", fx.parentDir, "workspace", fx.principal, ctx, []tool.Call{call}, nil)
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	return results[0], len(approved) == 1
}

// Purpose: a delegated task must read its own descendants' worktrees without
// stopping on a person (live bug: a Swarm task blocked 40 minutes on a
// permission.workspace_scope.v2 card for its child's result file). Threat: the
// allowance leaking to siblings, unrelated or foreign-account tasks, or
// letting writes/task launches into a child's worktree bypass existing rules.
// Owners: Service.gateWorkspaceScopeCalls, resolveDescendantReadRoots and the
// durable WorktreeOwnership/parent_session_id records; the tool runtime then
// executes the admitted read. A real temp Pebble store and real files are the
// narrowest layer that proves both the gate decision and the executed read.
func TestGateWorkspaceScopeReadsDescendantWorktreesWithoutPrompt(t *testing.T) {
	fx := newDescendantScopeFixture(t)
	read := func(id string) tool.Call {
		return tool.Call{CallID: "read-" + id, Name: "read", Arguments: mustJSON(t, map[string]any{"path": filepath.Join(fx.roots[id], "pkg", "result.go")})}
	}

	for _, tc := range []struct {
		name, session string
		call          tool.Call
		admitted      bool
	}{
		{"parent reads child", descendantTestParent, read("child"), true},
		{"grandparent reads grandchild", descendantTestParent, read("grandchild"), true},
		{"child reads its child", "child", read("grandchild"), true},
		{"parent lists child root", descendantTestParent, tool.Call{CallID: "list", Name: "list", Arguments: mustJSON(t, map[string]any{"path": fx.roots["child"]})}, true},
		{"parent searches child", descendantTestParent, tool.Call{CallID: "search", Name: "search", Arguments: mustJSON(t, map[string]any{"path": filepath.Join(fx.roots["child"], "pkg"), "query": "package"})}, true},
		{"sibling task still prompts", descendantTestParent, read("sibling"), false},
		{"unrelated task still prompts", descendantTestParent, read("unrelated"), false},
		{"foreign account child still prompts", descendantTestParent, read("foreign"), false},
		{"child cannot read its parent's worktree", "grandchild", read("child"), false},
		{"write into child still prompts", descendantTestParent, tool.Call{CallID: "write", Name: "write", Arguments: mustJSON(t, map[string]any{"path": filepath.Join(fx.roots["child"], "pkg", "result.go"), "content": "x"})}, false},
		{"edit in child still prompts", descendantTestParent, tool.Call{CallID: "edit", Name: "edit", Arguments: mustJSON(t, map[string]any{"path": filepath.Join(fx.roots["child"], "pkg", "result.go"), "old_string": "package", "new_string": "x"})}, false},
		{"task launch into child still prompts", descendantTestParent, tool.Call{CallID: "task", Name: "task", Arguments: mustJSON(t, map[string]any{"workspace_path": fx.roots["child"]})}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := fx.workspaceCtx()
			result, admitted := fx.gate(t, tc.session, ctx, tc.call)
			if admitted != tc.admitted {
				t.Fatalf("admitted=%v want %v result=%+v", admitted, tc.admitted, result)
			}
			if tc.admitted {
				if result.Error != "" || len(ctx.Scope.DescendantReadRoots) == 0 {
					t.Fatalf("admitted read left error or no read root: %+v %v", result, ctx.Scope.DescendantReadRoots)
				}
				if len(ctx.Scope.Roots) != 1 || len(ctx.Scope.MutationScopes) != 0 {
					t.Fatalf("descendant read widened writable roots: %+v", ctx.Scope)
				}
				return
			}
			if !strings.Contains(result.Error, "permission system is not configured") {
				t.Fatalf("expected the prompt path, got %+v", result)
			}
			for _, id := range []string{"sibling", "unrelated", "foreign"} {
				if slices.Contains(ctx.Scope.DescendantReadRoots, fx.roots[id]) {
					t.Fatalf("non-descendant root %s admitted: %v", id, ctx.Scope.DescendantReadRoots)
				}
			}
		})
	}

	// The admitted scope executes the read, while writes into the same child
	// worktree remain rejected by the runtime and leave the file unchanged.
	ctx := fx.workspaceCtx()
	if _, ok := fx.gate(t, descendantTestParent, ctx, read("child")); !ok {
		t.Fatal("child read not admitted")
	}
	output, err := tool.ExecuteForWorkspaceScope(context.Background(), ctx.Scope, read("child"))
	if err != nil || !strings.Contains(output, "package pkg // child") {
		t.Fatalf("read did not execute: %v %s", err, output)
	}
	target := filepath.Join(fx.roots["child"], "pkg", "result.go")
	if _, err := tool.ExecuteForWorkspaceScope(context.Background(), ctx.Scope, tool.Call{CallID: "w", Name: "write", Arguments: mustJSON(t, map[string]any{"path": target, "content": "overwritten"})}); err == nil {
		t.Fatal("write into descendant worktree succeeded")
	}
	if data, _ := os.ReadFile(target); string(data) != "package pkg // child\n" {
		t.Fatalf("descendant file changed: %q", data)
	}
}

type fakeDescendantLineage struct {
	sessions map[string]pebblestore.SessionSnapshot
	claims   map[string]pebblestore.WorktreeOwnership
}

func (f fakeDescendantLineage) GetSession(id string) (pebblestore.SessionSnapshot, bool, error) {
	s, ok := f.sessions[id]
	return s, ok, nil
}

func (f fakeDescendantLineage) InspectWorktreeOwnership(account, user string, paths []string) ([]pebblestore.WorktreeOwnership, error) {
	c, ok := f.claims[paths[0]]
	if !ok || c.AccountScopeID != account || c.UserID != user {
		return nil, pebblestore.ErrWorktreeRecoveryConflict
	}
	return []pebblestore.WorktreeOwnership{c}, nil
}

// Purpose: lineage proof must fail closed on corrupt or ambiguous records.
// Threat: a cycle, a recovery-reserved claim, an owner whose recorded root
// differs from the claim, or a session claiming its own worktree being treated
// as a descendant grant. resolveDescendantWorktreeReadRoot owns this; a fake
// lineage is the narrowest way to inject states the store's own admission
// rules refuse to create.
func TestResolveDescendantWorktreeReadRootFailsClosed(t *testing.T) {
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "a", UserID: "u"}
	session := func(id, root, parent string) pebblestore.SessionSnapshot {
		return pebblestore.SessionSnapshot{ID: id, AccountScopeID: "a", UserID: "u", WorktreeEnabled: root != "", WorktreeRootPath: root, Metadata: map[string]any{"parent_session_id": parent}}
	}
	claim := func(path, owner, claimant string) pebblestore.WorktreeOwnership {
		return pebblestore.WorktreeOwnership{Path: path, AccountScopeID: "a", UserID: "u", OwnerSessionID: owner, ClaimantSessionID: claimant}
	}
	base := "/srv/worktrees"
	lineage := fakeDescendantLineage{
		sessions: map[string]pebblestore.SessionSnapshot{
			"child":    session("child", base+"/child", "root"),
			"cycle-a":  session("cycle-a", base+"/cycle", "cycle-b"),
			"cycle-b":  session("cycle-b", "", "cycle-a"),
			"reserved": session("reserved", base+"/reserved", "root"),
			"moved":    session("moved", base+"/elsewhere", "root"),
			"root":     session("root", base+"/root", ""),
		},
		claims: map[string]pebblestore.WorktreeOwnership{
			base + "/child":    claim(base+"/child", "child", ""),
			base + "/cycle":    claim(base+"/cycle", "cycle-a", ""),
			base + "/reserved": claim(base+"/reserved", "reserved", "recovery"),
			base + "/moved":    claim(base+"/moved", "moved", ""),
			base + "/root":     claim(base+"/root", "root", ""),
		},
	}
	if root, ok := resolveDescendantWorktreeReadRoot(lineage, p, "root", base+"/child/deep/dir"); !ok || root != base+"/child" {
		t.Fatalf("child not proven: %q %v", root, ok)
	}
	for name, path := range map[string]string{
		"cycle":         base + "/cycle/x",
		"reserved":      base + "/reserved/x",
		"root mismatch": base + "/moved/x",
		"own worktree":  base + "/root/x",
		"no claim":      base + "/none/x",
	} {
		if root, ok := resolveDescendantWorktreeReadRoot(lineage, p, "root", path); ok {
			t.Fatalf("%s admitted %q", name, root)
		}
	}
	other := p
	other.AccountScopeID = "b"
	if _, ok := resolveDescendantWorktreeReadRoot(lineage, other, "root", base+"/child/x"); ok {
		t.Fatal("cross-account principal admitted")
	}
}

// Purpose: every action a permission.workspace_scope.v2 card offers must be
// accepted by the canonical resolver (live bug: "session_allow" returned
// `unsupported decision`). Threat: offering a dead action, or mapping it to a
// persistent policy rule instead of this-session-only approval. Owners:
// workspaceScopePermissionArguments (the offered actions) and
// permission.Service Resolve/ResolveWithPolicy (normalizeResolveAction). A temp
// permission store proves the durable record transition and its decision.
func TestWorkspaceScopeCardActionsResolve(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "permissions"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := permission.NewService(pebblestore.NewPermissionStore(db), nil, nil)
	call := tool.Call{CallID: "call", Name: "read", Arguments: `{"path":"/outside/file.go"}`}
	request := tool.ScopeExpansionRequest{ToolName: "read", ArgumentName: "path", RequestedPath: "/outside/file.go", TargetPath: "/outside/file.go", DirectoryPath: "/outside"}
	args := workspaceScopePermissionArguments(workspaceScopePermissionTarget{Path: "/workspace", Name: "workspace"}, call, request)
	var card struct {
		Actions map[string]struct {
			Decision string `json:"decision"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(args), &card); err != nil || len(card.Actions) == 0 {
		t.Fatalf("card offers no actions: %v %s", err, args)
	}
	offered := []string{}
	for key, action := range card.Actions {
		offered = append(offered, key, action.Decision)
	}
	for i, action := range offered {
		for _, resolve := range []string{"resolve", "resolve_with_policy"} {
			id := resolve + "-" + action + "-" + string(rune('a'+i))
			pending, err := svc.CreatePending(permission.CreateInput{SessionID: "session", RunID: "run", CallID: id, ToolName: "read", ToolArguments: args, Requirement: workspaceScopePermissionRequirement, Mode: "auto"})
			if err != nil {
				t.Fatal(err)
			}
			var record pebblestore.PermissionRecord
			if resolve == "resolve" {
				record, err = svc.Resolve("session", pending.ID, action, "")
			} else {
				var rule *permission.PolicyRule
				record, rule, err = svc.ResolveWithPolicy("session", pending.ID, action, "")
				if rule != nil {
					t.Fatalf("%s saved a policy rule: %+v", action, rule)
				}
			}
			if err != nil {
				t.Fatalf("%s %q rejected: %v", resolve, action, err)
			}
			if record.Status != pebblestore.PermissionStatusApproved || workspaceScopeDecisionFromReason(record.Reason) != workspaceScopeDecisionSessionAllow {
				t.Fatalf("%s %q resolved to %+v", resolve, action, record)
			}
			if permission.IsPersistentAction(action) {
				t.Fatalf("%q is persistent", action)
			}
		}
	}
	// Unknown actions remain rejected and leave the request pending.
	pending, err := svc.CreatePending(permission.CreateInput{SessionID: "session", RunID: "run", CallID: "bogus", ToolName: "read", ToolArguments: args, Requirement: workspaceScopePermissionRequirement, Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resolve("session", pending.ID, "bogus", ""); err == nil || !strings.Contains(err.Error(), "unsupported decision") {
		t.Fatalf("bogus action accepted: %v", err)
	}
	if count, err := svc.PendingCount("session"); err != nil || count != 1 {
		t.Fatalf("pending=%d err=%v", count, err)
	}
}
