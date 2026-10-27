package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func integrationGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Purpose: IntegrateProjectTask must preserve unrelated untracked files through
// both public entry points. Real Git and the task store are the narrowest layer
// proving ancestry, completion and file preservation, including stale selection
// rejection without a receipt or target mutation.
func TestProjectTaskIntegrateNativeAndHTTP(t *testing.T) {
	for _, entry := range []string{"native", "http"} {
		t.Run(entry, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			root := t.TempDir()
			source := filepath.Join(root, "source")
			child := filepath.Join(root, "child")
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			integrationGit(t, source, "init", "-b", "dev")
			integrationGit(t, source, "commit", "--allow-empty", "-m", "base")
			base := integrationGit(t, source, "rev-parse", "HEAD")
			integrationGit(t, source, "worktree", "add", "-b", "agent/task", child)
			if err := os.WriteFile(filepath.Join(child, "result.txt"), []byte("result\n"), 0600); err != nil {
				t.Fatal(err)
			}
			integrationGit(t, child, "add", "result.txt")
			integrationGit(t, child, "commit", "-m", "result")
			head := integrationGit(t, child, "rev-parse", "HEAD")
			untouched := filepath.Join(source, "local-note")
			if err := os.WriteFile(untouched, []byte("keep\n"), 0600); err != nil {
				t.Fatal(err)
			}
			f := newConfirmationFixture(t, filepath.Join(root, "store"), nil)
			defer f.close()
			p := testPrincipal()
			db := f.sessions.Store()
			catalogDB, err := pebblestore.Open(filepath.Join(root, "catalog"))
			if err != nil {
				t.Fatal(err)
			}
			defer catalogDB.Close()
			f.server.workspace = workspaceruntime.NewService(pebblestore.NewWorkspaceStore(catalogDB))
			if _, _, err := pebblestore.NewWorkspaceStore(catalogDB).CreateForAccountIfAbsent(p.AccountScopeID, source, "source", ""); err != nil {
				t.Fatal(err)
			}
			proj := &pebblestore.ProjectRecord{ID: "integration-project", AccountID: p.AccountScopeID, Name: "Integration", Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: source}}}
			if err := db.PutProject(p.AccountScopeID, proj); err != nil {
				t.Fatal(err)
			}
			bound, err := f.server.resolveProjectTaskSource(p, proj, source, "", 0, true)
			if err != nil {
				t.Fatal(err)
			}
			session := pebblestore.SessionSnapshot{ID: "integration-child", AccountScopeID: p.AccountScopeID, UserID: p.UserID, WorkspacePath: child, WorktreeEnabled: true, WorktreeRootPath: child, WorktreeBranch: "agent/task", WorktreeBaseBranch: "dev", Metadata: map[string]any{"swarm_v3_source_workspace_path": source, "base_commit": base, "project_id": proj.ID, "task_id": "integration-task", "swarm_v3_source_workspace_id": bound.WorkspaceID, "swarm_v3_source_workspace_generation": bound.WorkspaceGeneration, "swarm_v3_worktree_owner_session_id": "integration-child", "swarm_v3_runtime_workspace_path": child}}
			if err := db.CreateSessionForAccount(session, p.UserID, p.AccountScopeID); err != nil {
				t.Fatal(err)
			}
			if err := db.CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := f.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "legacy", AllocatedRuntimeRoot: true, Path: child, SourcePath: source, OwnerSessionID: session.ID, Branch: session.WorktreeBranch}, Kind: sessionruntime.SessionMutationUpdateMetadata, SessionID: session.ID, AccountScopeID: p.AccountScopeID, UserID: p.UserID, ClientRequestID: "create-child", PayloadHash: "create-child", Session: &session}); err != nil {
				t.Fatal(err)
			}
			task := &pebblestore.ProjectTaskRecord{ID: "integration-task", ProjectID: proj.ID, AccountID: p.AccountScopeID, Title: "Integrate", Agent: "coder", Status: "needs_review", SessionID: session.ID, WorkspacePath: child, WorktreeBranch: "agent/task", BaseBranch: "dev", BaseCommit: base, SourceWorkspace: bound}
			if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
				t.Fatal(err)
			}
			f.server.worktrees = &worktreeruntime.Service{}
			_, err = f.server.IntegrateProjectTask(context.Background(), p, proj.ID, task.ID, tool.ProjectTaskIntegrationSelection{SessionID: "stale", SourceBranch: "agent/task", TargetBranch: "dev"})
			if err == nil {
				t.Fatal("stale selection accepted")
			}
			unchanged, _, _ := db.GetProjectTask(p.AccountScopeID, proj.ID, task.ID)
			if unchanged.Integration != nil || integrationGit(t, source, "rev-parse", "HEAD") != base {
				t.Fatal("rejection mutated task or target")
			}
			var result map[string]any
			if entry == "native" {
				runtime := tool.NewRuntime(1)
				defer runtime.Close()
				runtime.SetManageProjectStore(db)
				runtime.SetProjectTaskLifecycleService(f.server)
				args, _ := json.Marshal(map[string]any{"action": "integrate_task", "project_id": proj.ID, "task_id": task.ID})
				out, err := runtime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), tool.WorkspaceScope{PrimaryPath: source, Roots: []string{source}, Principal: p}, tool.Call{Name: "manage_projects", Arguments: string(args)})
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(out), &result); err != nil {
					t.Fatal(err)
				}
			} else {
				args, _ := json.Marshal(tool.ProjectTaskIntegrationSelection{SessionID: session.ID, SourceBranch: "agent/task", TargetBranch: "dev"})
				code, out := f.do(http.MethodPost, "/v3/projects/"+proj.ID+"/tasks/"+task.ID+"/integrate", string(args))
				if code != 200 {
					t.Fatalf("%d %s", code, out)
				}
				if err := json.Unmarshal([]byte(out), &result); err != nil {
					t.Fatal(err)
				}
			}
			if result["status"] != "integrated" {
				t.Fatalf("result: %#v", result)
			}
			receipt := result["integration"].(map[string]any)
			if receipt["target_branch"] != "dev" || receipt["resulting_target_head"] != head {
				t.Fatalf("receipt: %#v", receipt)
			}
			updated, _, _ := db.GetProjectTask(p.AccountScopeID, proj.ID, task.ID)
			if updated.Status != "completed" {
				t.Fatalf("status %s", updated.Status)
			}
			integrationGit(t, source, "merge-base", "--is-ancestor", head, "HEAD")
			contents, err := os.ReadFile(untouched)
			if err != nil || string(contents) != "keep\n" {
				t.Fatalf("untracked file changed: %q %v", contents, err)
			}
		})
	}
}

// Purpose: ApproveProjectTask rejects finished/running tasks before deployment or
// run admission. A real store with deliberately absent deployment prerequisites
// proves the early boundary and unchanged task state without provider execution.
func TestApproveProjectTaskRefusesNonPending(t *testing.T) {
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "store"), nil)
	defer f.close()
	p := testPrincipal()
	db := f.sessions.Store()
	for _, state := range []string{"needs_review", "in_progress", "completed", "launched", "running"} {
		task := &pebblestore.ProjectTaskRecord{ID: "refuse-" + state, ProjectID: "project", AccountID: p.AccountScopeID, Title: "Refuse", Agent: "coder", Status: state}
		if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
			t.Fatal(err)
		}
		before, _, _ := db.GetProjectTask(p.AccountScopeID, task.ProjectID, task.ID)
		_, err := f.server.ApproveProjectTask(context.Background(), p, task.ProjectID, task.ID)
		if err == nil || !strings.Contains(err.Error(), state) || (state == "needs_review" && !strings.Contains(err.Error(), "integrate_task")) {
			t.Fatalf("state %s: %v", state, err)
		}
		after, _, _ := db.GetProjectTask(p.AccountScopeID, task.ProjectID, task.ID)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("refusal changed task")
		}
	}
}
