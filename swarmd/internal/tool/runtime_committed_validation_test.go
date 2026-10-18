package tool_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type committedValidationFixture struct {
	runtime                           *tool.Runtime
	sessions                          *sessionruntime.Service
	store                             *pebblestore.SessionStore
	workspace                         *workspaceruntime.Service
	worktrees                         *worktreeruntime.Service
	scope                             tool.WorkspaceScope
	request                           tool.CommittedSourceRequest
	source, child, base, head, marker string
}

func validationGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	argv := append([]string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func validationWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newCommittedValidationFixture(t *testing.T) *committedValidationFixture {
	t.Helper()
	root := t.TempDir()
	isolatedHome := filepath.Join(root, "home")
	if err := os.Mkdir(isolatedHome, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", isolatedHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	f := &committedValidationFixture{source: filepath.Join(root, "source"), marker: filepath.Join(root, "command-ran")}
	if err := os.Mkdir(f.source, 0o700); err != nil {
		t.Fatal(err)
	}
	validationGit(t, f.source, "init", "-b", "dev")
	validationWrite(t, filepath.Join(f.source, "tracked.txt"), "base\n")
	validationGit(t, f.source, "add", "tracked.txt")
	validationGit(t, f.source, "commit", "-m", "base")
	f.base = validationGit(t, f.source, "rev-parse", "HEAD")
	db, err := pebblestore.Open(filepath.Join(root, "state", "fixture.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	f.store = pebblestore.NewSessionStore(db)
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	f.sessions = sessionruntime.NewService(f.store, events)
	workspaceStore := pebblestore.NewWorkspaceStore(db)
	workspace := workspaceruntime.NewService(workspaceStore)
	worktrees := worktreeruntime.NewService(pebblestore.NewWorktreeStore(db), workspace, events)
	f.workspace, f.worktrees = workspace, worktrees
	t.Cleanup(worktrees.Close)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "fixture-user", AccountScopeID: "fixture-account", SessionID: "fixture-parent"}
	if _, _, err := workspaceStore.CreateForAccountIfAbsent(principal.AccountScopeID, f.source, "fixture", ""); err != nil {
		t.Fatal(err)
	}
	base, err := worktrees.ResolveTaskBase(f.source)
	if err != nil {
		t.Fatal(err)
	}
	allocation, err := worktrees.AllocateTaskWorkspace(f.source, base, "fixture-child", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.child = allocation.WorkspacePath
	validationWrite(t, filepath.Join(f.child, "tracked.txt"), "committed-child\n")
	validationGit(t, f.child, "add", "tracked.txt")
	validationGit(t, f.child, "commit", "-m", "child")
	f.head = validationGit(t, f.child, "rev-parse", "HEAD")
	f.request = tool.CommittedSourceRequest{TaskCallID: "fixture-call", ChildSessionID: "fixture-child", HeadCommit: f.head}
	row := map[string]any{"subagent": "system-coder", "child_session_id": f.request.ChildSessionID, "child_run_id": "fixture-child-run", "phase": "completed", "head_commit": f.head, "base_commit": f.base, "worktree_root_path": f.child, "worktree_branch": allocation.BranchName, "parent_branch": "dev", "parent_workspace_path": f.source}
	parent := pebblestore.SessionSnapshot{ID: principal.SessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: f.source, Metadata: map[string]any{"task_launches": map[string]any{f.request.TaskCallID: map[string]any{"launches": []any{row}}}}}
	child := pebblestore.SessionSnapshot{ID: f.request.ChildSessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: f.child, WorktreeEnabled: true, WorktreeRootPath: f.child, WorktreeBranch: allocation.BranchName, WorktreeBaseBranch: "dev", Metadata: map[string]any{"parent_session_id": parent.ID, "parent_task_call_id": f.request.TaskCallID, "lineage_kind": "delegated_subagent", "subagent": "system-coder", "base_commit": f.base, "head_commit": f.head, "target_workspace_path": f.source, "swarm_v3_source_workspace_path": f.source, "parent_branch": "dev"}}
	for _, snapshot := range []pebblestore.SessionSnapshot{parent, child} {
		if err := f.store.CreateSession(snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := f.store.CreateDelegatedChildLineage(pebblestore.DelegatedChildLineageRecord{AccountScopeID: principal.AccountScopeID, LogicalTaskID: "fixture-logical-task"}, pebblestore.DelegatedChildGenerationRecord{SessionID: child.ID, ParentSessionID: parent.ID, RunID: "fixture-child-run", WorkspacePath: f.child, WorktreeBranch: allocation.BranchName, ParentBranch: "dev", ImmutableBaseCommit: f.base}, "fixture-lineage-create"); err != nil {
		t.Fatal(err)
	}
	if err := f.sessions.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{SessionID: child.ID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, RunID: "fixture-child-run", Phase: "needs_review", EndedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	f.scope = tool.WorkspaceScope{PrimaryPath: f.source, Roots: []string{f.source}, SessionID: parent.ID, Principal: principal}
	f.runtime = tool.NewRuntime(1)
	f.runtime.SetManageWorktreeServices(f.sessions, workspace, worktrees)
	t.Cleanup(func() {
		if err := f.runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := f.runtime.ResolveCommittedSource(f.scope, f.request); err != nil {
		t.Fatalf("real committed fixture preflight: %v", err)
	}
	return f
}

func (f *committedValidationFixture) execute(t *testing.T, command string, modify func(map[string]any)) (map[string]any, error) {
	t.Helper()
	args := map[string]any{"command": command, "explanation": []string{"Execute the bounded fixture validation command."}, "category": "read", "critical": false, "timeout_ms": 5000, "committed_source": map[string]any{"task_call_id": f.request.TaskCallID, "child_session_id": f.request.ChildSessionID, "head_commit": f.request.HeadCommit}}
	if modify != nil {
		modify(args)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := f.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, f.scope, tool.Call{CallID: "fixture-validation", Name: "bash", Arguments: string(raw)})
	payload := map[string]any{}
	if strings.HasPrefix(out, "{") {
		if decodeErr := json.Unmarshal([]byte(out), &payload); decodeErr != nil {
			t.Fatalf("decode result: %v", decodeErr)
		}
	}
	return payload, err
}

func validationReceipt(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	receipt, ok := payload["validation_receipt"].(map[string]any)
	if !ok {
		t.Fatalf("missing validation receipt: %#v", payload)
	}
	return receipt
}

func TestCommittedValidationRecallExecutesExactSuccessfulChild(t *testing.T) {
	// Requirement: Swarm validates the completed Coder commit in its exact tree.
	// Threat: shell cwd silently uses the parent or grants an arbitrary path.
	// Owners: Runtime.executeOne/executeCommittedSourceBash and ResolveCommittedSource.
	// Real Git, Pebble, public recall and shell execution is the narrowest complete route.
	for _, phase := range []string{"completed", "needs_review"} {
		t.Run(phase, func(t *testing.T) {
			f := newCommittedValidationFixture(t)
			if err := f.sessions.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{SessionID: f.request.ChildSessionID, RunID: "fixture-child-run", Phase: phase, EndedAt: 2, UpdatedAt: 2}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			recalled, err := f.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, f.scope, tool.Call{CallID: "fixture-recall", Name: "manage-worktree", Arguments: `{"action":"recall","task_call_id":"fixture-call"}`})
			if err != nil {
				t.Fatal(err)
			}
			var recall struct {
				Children []struct {
					Source tool.CommittedSourceRequest `json:"committed_source"`
				} `json:"children"`
			}
			if err := json.Unmarshal([]byte(recalled), &recall); err != nil {
				t.Fatal(err)
			}
			if len(recall.Children) != 1 || recall.Children[0].Source != f.request {
				t.Fatalf("recall failed to expose exact eligible tuple: %s", recalled)
			}
			f.request = recall.Children[0].Source
			// The same resolver authenticates committed-source correction allocation.
			binding, err := f.runtime.ResolveCommittedSource(f.scope, f.request)
			if err != nil || binding.SourceHeadCommit != f.head || binding.SourceBaseCommit != f.base {
				t.Fatalf("correction source binding: %#v, %v", binding, err)
			}
			payload, err := f.execute(t, "pwd; cat tracked.txt", nil)
			if err != nil {
				t.Fatal(err)
			}
			receipt := validationReceipt(t, payload)
			if receipt["verified"] != true || receipt["head_commit"] != f.head || receipt["child_session_id"] != f.request.ChildSessionID || receipt["task_call_id"] != f.request.TaskCallID || receipt["workspace_path"] != f.child {
				t.Fatalf("incorrect receipt: %#v", receipt)
			}
			if !strings.Contains(payload["output"].(string), f.child) || !strings.Contains(payload["output"].(string), "committed-child") {
				t.Fatalf("command did not inspect child: %#v", payload)
			}
			if got := validationGit(t, f.source, "rev-parse", "HEAD"); got != f.base {
				t.Fatal("parent HEAD changed")
			}
			if got := validationGit(t, f.child, "status", "--porcelain"); got != "" {
				t.Fatalf("child changed: %s", got)
			}
		})
	}
}

func TestCommittedValidationFailureAndPostcondition(t *testing.T) {
	// Requirement: unsuccessful commands or changed source never produce proof.
	// Threat: exit failures or validation mutations are reported as verified commits.
	// Owners: executeCommittedSourceBash receipt and postflight ResolveCommittedSource;
	// the real shell plus durable source check proves the evidence boundary.
	for _, tc := range []struct {
		name, command string
		exit          float64
	}{
		{"failure", "exit 7", 7},
		{"tracked mutation", "printf changed > tracked.txt", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCommittedValidationFixture(t)
			payload, executionErr := f.execute(t, tc.command, func(args map[string]any) {
				if tc.exit == 0 {
					args["category"] = "write"
				}
			})
			if executionErr == nil {
				t.Fatal("failed validation returned no error")
			}
			if summary, ok := payload["summary"].(string); !ok || !strings.Contains(summary, "Child validation failed") {
				t.Fatalf("failed receipt missing failure summary: %#v", payload)
			}
			receipt := validationReceipt(t, payload)
			if receipt["verified"] != false || strings.TrimSpace(receipt["reason"].(string)) == "" || payload["exit_code"] != tc.exit {
				t.Fatalf("failed validation reported success: %#v", payload)
			}
			if got := validationGit(t, f.source, "status", "--porcelain"); got != "" {
				t.Fatal("parent tree changed")
			}
		})
	}
}

func TestCommittedValidationRejectsBeforeExecuting(t *testing.T) {
	// Requirement: only authenticated current completed clean child lineage runs.
	// Threat: cross-account, stale or restricted contexts execute before rejection.
	// Owners: public Bash dispatch, executeCommittedSourceBash and ResolveCommittedSource;
	// real durable services and a marker postcondition prove rejection has no command side effect.
	for _, name := range []string{"account", "child", "stale request", "stale head", "wrong task", "running", "paused", "failed", "dirty", "symlink", "workspace override", "restricted", "task history", "readonly", "reviewer", "read roots", "mutation scope", "rootless readonly", "rootless reviewer"} {
		t.Run(name, func(t *testing.T) {
			f := newCommittedValidationFixture(t)
			var modify func(map[string]any)
			switch name {
			case "account":
				f.scope.Principal.AccountScopeID = "other-account"
			case "child":
				f.request.ChildSessionID = f.scope.SessionID
			case "stale request":
				f.request.HeadCommit = f.base
			case "stale head":
				validationWrite(t, filepath.Join(f.child, "tracked.txt"), "newer head\n")
				validationGit(t, f.child, "commit", "-am", "newer head")
			case "wrong task":
				f.request.TaskCallID = "unrelated-call"
			case "running", "paused", "failed":
				if err := f.sessions.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{SessionID: f.request.ChildSessionID, RunID: "fixture-child-run", Active: name == "running", Phase: name, EndedAt: 2, UpdatedAt: 2}); err != nil {
					t.Fatal(err)
				}
			case "dirty":
				validationWrite(t, filepath.Join(f.child, "tracked.txt"), "dirty\n")
			case "symlink":
				moved := f.child + "-moved"
				if err := os.Rename(f.child, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, f.child); err != nil {
					t.Fatal(err)
				}
			case "workspace override":
				modify = func(args map[string]any) { args["workspace_path"] = f.source }
			case "restricted":
				f.scope.RejectScopeExpansion = true
			case "task history":
				f.scope.TaskHistoryOnly = true
			case "readonly":
				f.scope.ReadOnlyBash = true
			case "reviewer":
				f.scope.ReviewCheckout = f.source
			case "read roots":
				f.scope.ReadOnlyRoots = []string{f.child}
			case "mutation scope":
				f.scope.MutationScopes = []string{"tracked.txt"}
			case "rootless readonly":
				f.scope.PrimaryPath, f.scope.Roots, f.scope.ReadOnlyBash = "", nil, true
			case "rootless reviewer":
				f.scope.PrimaryPath, f.scope.Roots, f.scope.ReviewCheckout = "", nil, f.source
			}
			command := "printf ran > '" + strings.ReplaceAll(f.marker, "'", "'\\''") + "'"
			_, err := f.execute(t, command, modify)
			if err == nil {
				t.Fatal("invalid source/scope accepted")
			}
			if _, statErr := os.Stat(f.marker); !os.IsNotExist(statErr) {
				t.Fatalf("rejected command executed: %v", statErr)
			}
			if got := validationGit(t, f.source, "rev-parse", "HEAD"); got != f.base {
				t.Fatal("rejection changed parent HEAD")
			}
		})
	}
}
