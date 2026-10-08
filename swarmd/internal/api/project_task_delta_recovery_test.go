package api

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

// Purpose: the authenticated recover-integrate route must persist authentic
// delta provenance and task events, survive store reload/retry without duplicate
// commits, and reject stale/foreign/dirty requests without target changes. Real
// Git plus the temporary canonical store is the narrowest layer proving these
// API and persistence postconditions; this is not provider/runtime validation.
func TestProjectTaskDeltaRecovery(t *testing.T) {
	for _, scenario := range []string{"recover", "equivalent", "conflict", "stale-revision", "stale-target", "foreign-account", "dirty-source", "dirty-target", "wrong-source", "target-moves-during-prepare"} {
		t.Run(scenario, func(t *testing.T) {
			f := setupMatrixTestFixture(t)
			defer func() { f.db.Close() }()
			t.Setenv("HOME", t.TempDir())
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			repo := t.TempDir()
			git := func(path string, args ...string) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				out, err := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			write := func(path, name, value string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(path, name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			commit := func(path string) { git(path, "add", "."); git(path, "commit", "-m", "fixture") }
			git(repo, "init", "-b", "dev")
			git(repo, "config", "user.name", "Fixture")
			git(repo, "config", "user.email", "fixture@example.invalid")
			write(repo, "feature", "base\n")
			commit(repo)
			tree, base := git(repo, "rev-parse", "HEAD^{tree}"), git(repo, "rev-parse", "HEAD")
			for i := 0; i < 12; i++ {
				base = git(repo, "commit-tree", tree, "-p", base, "-m", "shared history")
			}
			git(repo, "update-ref", "refs/heads/dev", base)
			entry, err := pebblestore.NewWorkspaceStore(f.db).AddForAccount(f.accountID, repo, "Repo")
			if err != nil {
				t.Fatal(err)
			}
			binding := pebblestore.ProjectTaskSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: repo, Provenance: "explicit"}
			wire := func() *worktree.Service {
				el, err := pebblestore.NewEventLog(f.db)
				if err != nil {
					t.Fatal(err)
				}
				f.server.sessions = sessionruntime.NewService(pebblestore.NewSessionStore(f.db), el)
				f.server.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(f.db))
				svc := worktree.NewService(pebblestore.NewWorktreeStore(f.db), f.server.workspace, nil)
				f.server.worktrees = svc
				seedTaskSessionBinding(t, f, binding)
				return svc
			}
			svc := wire()
			p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
			alloc, err := svc.AllocateProjectTaskFollowup(p, repo, "origin", "agent/recovery-origin", base, "dev")
			if err != nil {
				t.Fatal(err)
			}
			write(alloc.WorkspacePath, "feature", "task edit\n")
			commit(alloc.WorkspacePath)
			source := git(alloc.WorkspacePath, "rev-parse", "HEAD")
			rewritten := git(repo, "commit-tree", tree, "-m", "rewritten shared history")
			git(repo, "update-ref", "refs/heads/dev", rewritten)
			write(repo, "target-only", "unrelated target edit")
			if scenario == "equivalent" {
				write(repo, "feature", "task edit\n")
			}
			if scenario == "conflict" {
				write(repo, "feature", "target overlap\n")
			}
			commit(repo)
			target := git(repo, "rev-parse", "HEAD")
			db := f.server.sessions.Store()
			project := &pebblestore.ProjectRecord{ID: "project", Name: "Project", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: entry.WorkspaceID, Path: repo}}}
			if err := db.PutProject(f.accountID, project); err != nil {
				t.Fatal(err)
			}
			task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: project.ID, AccountID: f.accountID, Title: "Recover task", Revision: 1, Status: "needs_review", SessionID: "origin", Agent: "coder", WorkspacePath: alloc.WorkspacePath, WorktreeBranch: alloc.BranchName, BaseBranch: "dev", BaseCommit: base, SourceWorkspace: binding}
			if err := db.PutProjectTask(f.accountID, task); err != nil {
				t.Fatal(err)
			}
			snap := pebblestore.SessionSnapshot{ID: "origin", UserID: p.UserID, AccountScopeID: p.AccountScopeID, WorkspacePath: alloc.WorkspacePath, WorktreeEnabled: true, WorktreeRootPath: alloc.WorkspacePath, WorktreeBranch: alloc.BranchName, WorktreeBaseBranch: "dev", Mode: "auto", Metadata: map[string]any{"project_id": project.ID, "task_id": task.ID, "base_commit": base, "swarm_v3_source_workspace_path": repo, "swarm_v3_source_workspace_id": binding.WorkspaceID, "swarm_v3_source_workspace_generation": binding.WorkspaceGeneration, "swarm_v3_worktree_owner_session_id": "origin", "swarm_v3_runtime_workspace_path": alloc.WorkspacePath}}
			if _, err := applyProjectLifecycleFixture(f.server, sessionruntime.SessionMutationInput{SessionID: snap.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: "origin", IdempotencyKey: "origin", PayloadHash: "origin", RequestHash: "origin", Kind: sessionruntime.SessionMutationCreateSession, Session: &snap, WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "allocated", Path: alloc.WorkspacePath, SourcePath: repo, OwnerSessionID: snap.ID, Branch: alloc.BranchName, AllocatedRuntimeRoot: true}}); err != nil {
				t.Fatal(err)
			}
			read := func() *pebblestore.ProjectTaskRecord {
				t.Helper()
				row, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, "project", "task")
				if err != nil || !found {
					t.Fatalf("task: %v", err)
				}
				return row
			}
			row := read()
			body := map[string]any{"session_id": "origin", "source_branch": alloc.BranchName, "target_branch": "dev", "revision": row.Revision, "attempt_id": row.ActiveAttemptID, "source_head": source, "target_head": target}
			switch scenario {
			case "stale-revision":
				body["revision"] = row.Revision - 1
			case "stale-target":
				body["target_head"] = rewritten
			case "foreign-account":
				p.AccountScopeID = "foreign"
			case "dirty-source":
				write(alloc.WorkspacePath, "dirty", "preserve")
			case "dirty-target":
				write(repo, "dirty", "preserve")
			case "wrong-source":
				body["source_head"] = target
			}
			if scenario == "target-moves-during-prepare" {
				updates := 0
				restore := db.SetProjectTaskUpdateHookForTest(func(string) error {
					updates++
					if updates == 2 {
						git(repo, "commit", "--allow-empty", "-m", "concurrent target advance")
						target = git(repo, "rev-parse", "HEAD")
					}
					return nil
				})
				defer restore()
			}
			response := f.callAPI(http.MethodPost, "/project/tasks/task/recover-integrate", body, p)
			if scenario != "recover" && scenario != "equivalent" {
				if response.Code < 400 {
					t.Fatalf("unsafe success %d %s", response.Code, response.Body)
				}
				if git(repo, "rev-parse", "HEAD") != target || git(alloc.WorkspacePath, "rev-parse", "HEAD") != source {
					t.Fatal("rejected request changed target/source")
				}
				if scenario == "conflict" {
					r := read().Integration
					if r == nil || r.State != "conflict" || r.RecoveredHead == "" || !strings.Contains(r.Error, "feature") {
						t.Fatalf("missing retained repair evidence: %+v", r)
					}
					if git(repo, "rev-parse", r.RecoveredHead+"^") != target {
						t.Fatal("repair imports old history")
					}
					recovery := &pebblestore.ProjectTaskRecoverySource{SessionID: "origin", WorkspacePath: alloc.WorkspacePath, Branch: alloc.BranchName, BaseCommit: base, HeadCommit: source, TargetBranch: "dev", TargetHead: target, PreparedHead: r.RecoveredHead, PreparedRef: r.RecoveryRef}
					if err := f.server.validateProjectTaskRecovery(p, read(), recovery); err != nil {
						t.Fatalf("same-card repair source unavailable: %v", err)
					}
					if projectTaskRepairBase(recovery) != target {
						t.Fatal("repair base includes old history")
					}
				} else if scenario != "target-moves-during-prepare" && read().Integration != nil {
					t.Fatal("rejected selection created receipt")
				}
				return
			}
			if response.Code != 200 {
				t.Fatalf("recovery: %d %s", response.Code, response.Body)
			}
			got := read()
			want := "recovered"
			if scenario == "equivalent" {
				want = "equivalent"
			}
			if got.Integration == nil || got.Integration.State != want || got.IsIntegrated || got.Integration.SourceHead != source || got.Integration.RecoveryBase != base || got.Status != "completed" {
				t.Fatalf("false receipt: %+v", got)
			}
			landed := git(repo, "rev-parse", "HEAD")
			if git(repo, "show", "HEAD:target-only") != "unrelated target edit" || git(repo, "show", "HEAD:feature") != "task edit" {
				t.Fatal("wrong delivered edits")
			}
			count := "1"
			if scenario == "equivalent" {
				count = "0"
			}
			if git(repo, "rev-list", "--count", target+".."+landed) != count || git(alloc.WorkspacePath, "rev-parse", "HEAD") != source {
				t.Fatal("history replayed or original changed")
			}
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			f.db, err = pebblestore.Open(f.dir)
			if err != nil {
				t.Fatal(err)
			}
			wire()
			reloaded := read()
			if err := reconcileTaskGitState(f.server.sessions.Store(), reloaded); err != nil {
				t.Fatal(err)
			}
			if reloaded.DeliveryAssessment.State != want || reloaded.IsIntegrated {
				t.Fatalf("reload loses truthful delivery: %+v", reloaded)
			}
			retry := f.callAPI(http.MethodPost, "/project/tasks/task/recover-integrate", body, p)
			if retry.Code != 200 || git(repo, "rev-parse", "HEAD") != landed {
				t.Fatalf("retry duplicates or fails: %d %s", retry.Code, retry.Body)
			}
		})
	}
}
