package api

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Purpose: the registered integrate -> reopen path must retain inspected Git
// provenance when preparation conflicts or equivalent patches lack source ancestry.
// The temporary store/repository layer is the narrowest proof of receipt durability, isolated
// exact-source allocation and promotability; provider execution is not needed.
// Threat: missing receipts block repair, current dev replaces unintegrated work,
// stale/foreign/dirty sources allocate unauthorized lanes, or retries lose history.
func TestProjectTaskRepairPrepareConflictReceipt(t *testing.T) {
	for _, scenario := range []string{"repair", "ancestor", "equivalent-repair", "equivalent-source-head", "equivalent-target-head", "equivalent-foreign-owner", "equivalent-forged-head", "equivalent-stale-revision", "legacy", "source-head", "target-head", "dirty-source", "dirty-target", "wrong-target", "wrong-source", "foreign-owner", "foreign-account", "missing-base"} {
		t.Run(scenario, func(t *testing.T) {
			equivalent := strings.HasPrefix(scenario, "equivalent-")
			scenario = strings.TrimPrefix(scenario, "equivalent-")
			f := setupMatrixTestFixture(t)
			defer func() { f.db.Close() }()
			root := t.TempDir()
			t.Setenv("HOME", root)
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			repo := filepath.Join(root, "repo")
			if err := os.Mkdir(repo, 0700); err != nil {
				t.Fatal(err)
			}
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
			write := func(path, text string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(path, "feature.txt"), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			commit := func(path, message string) {
				git(path, "add", "feature.txt")
				git(path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", message)
			}
			git(repo, "init", "-b", "dev")
			// Preflight and repair merge commits also need hermetic Git identity.
			git(repo, "config", "user.name", "Fixture")
			git(repo, "config", "user.email", "fixture@example.invalid")
			write(repo, "base\n")
			commit(repo, "base")
			base := git(repo, "rev-parse", "HEAD")
			entry, err := pebblestore.NewWorkspaceStore(f.db).AddForAccount(f.accountID, repo, "Repo")
			if err != nil {
				t.Fatal(err)
			}
			wire := func() *worktreeruntime.Service {
				ss := pebblestore.NewSessionStore(f.db)
				el, err := pebblestore.NewEventLog(f.db)
				if err != nil {
					t.Fatal(err)
				}
				f.server.sessions = sessionruntime.NewService(ss, el)
				f.server.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(f.db))
				f.server.agentModelSettings = agentmodelsettings.NewService(pebblestore.NewAgentModelSettingsStore(f.db))
				f.server.agents = agentruntime.NewService(pebblestore.NewAgentStore(f.db), el)
				f.server.model = model.NewService(pebblestore.NewModelStore(f.db), el, nil)
				service := worktreeruntime.NewService(pebblestore.NewWorktreeStore(f.db), f.server.workspace, nil)
				f.server.worktrees = service
				return service
			}
			service := wire()
			p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
			alloc, err := service.AllocateProjectTaskFollowup(p, repo, "origin", "agent/receipt-origin", base, "dev")
			if err != nil {
				t.Fatal(err)
			}
			write(alloc.WorkspacePath, "original feature\n")
			commit(alloc.WorkspacePath, "feature")
			head := git(alloc.WorkspacePath, "rev-parse", "HEAD")
			if equivalent {
				git(repo, "commit", "--allow-empty", "-m", "target advance")
				git(repo, "cherry-pick", head)
			} else if scenario == "ancestor" {
				git(repo, "merge", "--ff-only", alloc.BranchName)
			} else {
				write(repo, "target change\n")
				commit(repo, "target")
			}
			target := git(repo, "rev-parse", "HEAD")
			binding := pebblestore.ProjectTaskSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: repo, Provenance: "explicit"}
			db := f.server.sessions.Store()
			project := &pebblestore.ProjectRecord{ID: "project", Name: "Project", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: entry.WorkspaceID, Path: repo}}}
			if err := db.PutProject(f.accountID, project); err != nil {
				t.Fatal(err)
			}
			task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: project.ID, AccountID: f.accountID, Title: "Repair feature", Status: "needs_review", SessionID: "origin", Agent: "coder", WorkspacePath: alloc.WorkspacePath, WorktreeBranch: alloc.BranchName, BaseBranch: "dev", BaseCommit: base, SourceWorkspace: binding}
			if err := db.PutProjectTask(f.accountID, task); err != nil {
				t.Fatal(err)
			}
			snapshot := pebblestore.SessionSnapshot{ID: "origin", UserID: p.UserID, AccountScopeID: p.AccountScopeID, WorkspacePath: alloc.WorkspacePath, WorktreeEnabled: true, WorktreeRootPath: alloc.WorkspacePath, WorktreeBranch: alloc.BranchName, WorktreeBaseBranch: "dev", Mode: "auto", Metadata: map[string]any{"project_id": project.ID, "task_id": task.ID, "base_commit": base, "swarm_v3_source_workspace_path": repo, "swarm_v3_source_workspace_id": binding.WorkspaceID, "swarm_v3_source_workspace_generation": binding.WorkspaceGeneration, "swarm_v3_worktree_owner_session_id": "origin", "swarm_v3_runtime_workspace_path": alloc.WorkspacePath}}
			if _, err := applyProjectLifecycleFixture(f.server, sessionruntime.SessionMutationInput{SessionID: snapshot.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: "create-origin", IdempotencyKey: "create-origin", PayloadHash: "create-origin", RequestHash: "create-origin", Kind: sessionruntime.SessionMutationCreateSession, Session: &snapshot, WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "allocated", Path: alloc.WorkspacePath, SourcePath: repo, OwnerSessionID: snapshot.ID, Branch: alloc.BranchName, AllocatedRuntimeRoot: true}}); err != nil {
				t.Fatal(err)
			}
			read := func() *pebblestore.ProjectTaskRecord {
				t.Helper()
				row, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, project.ID, task.ID)
				if err != nil || !found {
					t.Fatalf("read task: %v", err)
				}
				return row
			}
			if scenario == "ancestor" {
				response := f.callAPI(http.MethodPost, "/project/tasks/task/integrate", map[string]any{"session_id": "origin", "source_branch": alloc.BranchName, "target_branch": "dev"}, p)
				row := read()
				if response.Code != 200 || row.Integration == nil || row.Integration.State != "already_integrated" || !row.IsIntegrated || row.Status != "completed" || git(repo, "rev-parse", "HEAD") != target {
					t.Fatalf("true ancestor not reconciled: %d %s %+v", response.Code, response.Body, row)
				}
				return
			}
			expectedError, expectedStatus := "prepare integration failed", http.StatusBadRequest
			if equivalent {
				expectedError, expectedStatus = "equivalent patches", http.StatusConflict
				state := inspectTaskGitState(*read(), db)
				if state.isIntegrated || state.unintegratedCommits != 1 {
					t.Fatalf("equivalent history hidden before integrate: %+v", state)
				}
			}
			integrate := func() {
				t.Helper()
				response := f.callAPI(http.MethodPost, "/project/tasks/task/integrate", map[string]any{"session_id": "origin", "source_branch": alloc.BranchName, "target_branch": "dev"}, p)
				if response.Code != expectedStatus || !strings.Contains(response.Body.String(), expectedError) {
					t.Fatalf("expected real prepare conflict: %d %s", response.Code, response.Body)
				}
			}
			integrate()
			failed := read()
			if failed.Integration == nil || failed.Integration.State != "conflict" || failed.Integration.SessionID != "origin" || failed.Integration.SourceHead != head || failed.Integration.PreviousTargetHead != target || failed.Integration.SourceBranch != alloc.BranchName || failed.Integration.TargetBranch != "dev" || !strings.Contains(failed.Integration.Error, expectedError) || failed.IsIntegrated || failed.Status == "completed" {
				t.Fatalf("lost authentic prepare receipt: %+v", failed.Integration)
			}
			if git(repo, "rev-parse", "HEAD") != target || git(repo, "status", "--porcelain") != "" {
				t.Fatal("failed preparation modified captured target")
			}
			// Reopen the real store before consuming the receipt, not a fabricated row.
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			f.db, err = pebblestore.Open(f.dir)
			if err != nil {
				t.Fatal(err)
			}
			service = wire()
			if !reflect.DeepEqual(failed.Integration, read().Integration) {
				t.Fatal("receipt changed across restart")
			}
			if scenario == "legacy" {
				if _, err := f.server.sessions.Store().UpdateProjectTask(f.accountID, project.ID, task.ID, func(row *pebblestore.ProjectTaskRecord) error {
					row.Integration.SourceHead, row.Integration.PreviousTargetHead = "", ""
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "source-head":
				write(alloc.WorkspacePath, "changed source\n")
				commit(alloc.WorkspacePath, "stale source")
			case "target-head":
				write(repo, "changed target\n")
				commit(repo, "stale target")
			case "dirty-source":
				write(alloc.WorkspacePath, "dirty\n")
			case "dirty-target":
				write(repo, "dirty\n")
			case "wrong-target":
				git(repo, "checkout", "-b", "other")
			case "wrong-source":
				git(alloc.WorkspacePath, "checkout", "-b", "agent/other")
			case "foreign-account":
				p.AccountScopeID = "foreign"
			case "foreign-owner":
				p.UserID = "foreign"
			case "forged-head":
				if _, err := f.server.sessions.Store().UpdateProjectTask(f.accountID, project.ID, task.ID, func(row *pebblestore.ProjectTaskRecord) error {
					row.Integration.SourceHead = target
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			case "missing-base":
				if _, err := f.server.sessions.Store().UpdateProjectTask(f.accountID, project.ID, task.ID, func(row *pebblestore.ProjectTaskRecord) error {
					row.BaseCommit = ""
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			before, lanes := read(), git(repo, "worktree", "list", "--porcelain")
			body := map[string]any{"client_request_id": "repair", "revision": before.Revision, "feedback": "Repair retained feature without promotion", "repair": true}
			if scenario == "stale-revision" {
				body["revision"] = before.Revision - 1
			}
			response := f.callAPI(http.MethodPost, "/project/tasks/task/reopen", body, p)
			if scenario != "repair" {
				if response.Code != 403 && response.Code != 409 && response.Code != 404 {
					t.Fatalf("unsafe repair admitted: %d %s", response.Code, response.Body)
				}
				if !reflect.DeepEqual(before, read()) || lanes != git(repo, "worktree", "list", "--porcelain") {
					t.Fatal("rejection mutated task or allocated worktree")
				}
				if scenario != "legacy" {
					return
				}
				if !strings.Contains(response.Body.String(), "retry integration") {
					t.Fatal("legacy receipt has no actionable recovery")
				}
				integrate() // Explicitly reissue verified provenance; never fill it on reopen.
				body["revision"] = read().Revision
				response = f.callAPI(http.MethodPost, "/project/tasks/task/reopen", body, p)
			}
			if response.Code != 503 || !strings.Contains(response.Body.String(), "executor") {
				t.Fatalf("expected durable allocation, absent executor: %d %s", response.Code, response.Body)
			}
			reserved := read()
			if len(reserved.Attempts) != 2 || reserved.ActiveAttempt().Recovery == nil || reserved.ActiveAttempt().Recovery.HeadCommit != head || reserved.ActiveAttempt().Recovery.TargetHead != target {
				t.Fatal("repair reservation lost exact source")
			}
			// A real store reopen models retry after failed wake without starting providers.
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			f.db, err = pebblestore.Open(f.dir)
			if err != nil {
				t.Fatal(err)
			}
			service = wire()
			f.server.v3SessionExecutor = newSessionV3Executor(f.server)
			f.server.v3SessionExecutor.inFlightRuns[sessionV3ExecutorRunKey(reserved.SessionID, reserved.ExecutionRunID())] = true
			for i := 0; i < 2; i++ {
				response = f.callAPI(http.MethodPost, "/project/tasks/task/reopen", body, p)
				if response.Code != 200 {
					t.Fatalf("repair retry: %d %s", response.Code, response.Body)
				}
			}
			repaired := read()
			session, found, err := f.server.sessions.Store().GetSession(repaired.SessionID)
			if err != nil || !found || repaired.SessionID != reserved.SessionID || len(repaired.Attempts) != 2 || !session.WorktreeEnabled || session.WorktreeRootPath == repo || session.WorktreeRootPath == alloc.WorkspacePath || session.Metadata["swarm_v3_worktree_owner_session_id"] != session.ID || session.Metadata["swarm_v3_source_workspace_path"] != repo || session.WorktreeBaseBranch != "dev" || session.Metadata["base_commit"] != base {
				t.Fatalf("invalid isolated repair ownership: %+v %v", session, err)
			}
			claims, err := f.server.sessions.Store().InspectWorktreeOwnership(p.AccountScopeID, p.UserID, []string{session.WorktreeRootPath})
			if err != nil || len(claims) != 1 || claims[0].OwnerSessionID != session.ID || claims[0].ClaimantSessionID != "" {
				t.Fatal("repair has no canonical ownership")
			}
			if git(session.WorktreeRootPath, "rev-parse", "HEAD") != head || git(session.WorktreeRootPath, "show", "HEAD:feature.txt") != "original feature" || git(repo, "rev-parse", "HEAD") != target {
				t.Fatal("repair was allocated from bare target instead of originating feature")
			}
			intents, err := f.server.sessions.Store().ListRunIntents(session.ID, 10)
			if err != nil || len(intents) != 1 {
				t.Fatal("duplicate repair created multiple run intents")
			}
			git(repo, "merge-base", "--is-ancestor", base, head)
			if equivalent {
				// Exercise the normal history reconciliation path without changing target.
				git(session.WorktreeRootPath, "merge", "--no-edit", "dev")
				repairHead := git(session.WorktreeRootPath, "rev-parse", "HEAD")
				git(repo, "merge-base", "--is-ancestor", head, repairHead)
				plan, err := service.PrepareTaskIntegration(repo, "dev", target, []worktreeruntime.TaskIntegrationChild{{SessionID: session.ID, BaseCommit: base, HeadCommit: repairHead}})
				if err != nil || plan.FastForwardHead != repairHead || git(repo, "rev-parse", "HEAD") != target {
					t.Fatalf("equivalent-history repair lost ancestry or target: %+v %v", plan, err)
				}
				return
			}
			// Simulate an explicit conflict-resolution merge in the new lane. Both
			// original feature ancestry and captured target survive; no promotion occurs.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			out, mergeErr := exec.CommandContext(ctx, "git", "-C", session.WorktreeRootPath, "merge", "--no-commit", "dev").CombinedOutput()
			cancel()
			if mergeErr == nil || !strings.Contains(string(out), "CONFLICT") {
				t.Fatalf("expected repair merge conflict: %v %s", mergeErr, out)
			}
			write(session.WorktreeRootPath, "original feature\ntarget change\nrepair resolution\n")
			commit(session.WorktreeRootPath, "repair resolution")
			repairHead := git(session.WorktreeRootPath, "rev-parse", "HEAD")
			git(repo, "merge-base", "--is-ancestor", head, repairHead)
			plan, err := service.PrepareTaskIntegration(repo, "dev", target, []worktreeruntime.TaskIntegrationChild{{SessionID: session.ID, BaseCommit: base, HeadCommit: repairHead}})
			if err != nil || plan.FastForwardHead != repairHead || git(repo, "rev-parse", "HEAD") != target || git(alloc.WorkspacePath, "rev-parse", "HEAD") != head {
				t.Fatalf("repair not promotable without losing original source: %+v %v", plan, err)
			}
		})
	}
}
