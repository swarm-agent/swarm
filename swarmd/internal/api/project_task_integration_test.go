package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/sandbox"
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
// rejection without a receipt or target mutation. Detached-tip cases exercise
// delivery-only recovery and durable warnings; stale detached commits and wrong
// branches and target mismatches must remain unchanged with lane_branch_mismatch
// and the HTTP delivery assessment, preserving MCP's unobserved-state handling. Git HEAD lock
// conflicts must surface through IntegrateProjectTask without changing delivery.
// The inspection-timeout case injects only the assessment result, checking the
// mutation's effective sandbox allowance and real HTTP/MCP refusal reason on
// all three workflow attempts (one assessment each), with no receipt or target
// mutation; an interactive call then retries once in-call and really merges.
// The workflow case crosses toBranch and the real HTTP handler to prove the
// recovery warning reaches the recorded outcome, not just the stored receipt.
func TestProjectTaskIntegrateNativeAndHTTP(t *testing.T) {
	for _, entry := range []string{"inspection-timeout", "native", "http", "detached-tip", "detached-tip-workflow", "detached-lock", "detached-stale", "detached-dirty", "wrong-branch", "target-mismatch"} {
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
			if entry == "inspection-timeout" {
				manager, err := sandbox.NewManager(context.Background(), sandbox.Config{Mode: sandbox.ModeRequired, UID: 10001, GID: 10001, StateDir: t.TempDir()}, boxLeaseRunner{})
				if err != nil {
					t.Fatal(err)
				}
				previous := sandbox.Default()
				sandbox.SetDefault(manager)
				defer sandbox.SetDefault(previous)
				original := inspectIntegrationGitState
				defer func() { inspectIntegrationGitState = original }()
				calls := 0
				inspectIntegrationGitState = func(ctx context.Context, task pebblestore.ProjectTaskRecord, db *pebblestore.SessionStore) taskGitState {
					calls++
					if got := worktreeruntime.DeliveryAssessmentBudget(ctx); got != 30*time.Second {
						t.Fatalf("mutation budget %s", got)
					}
					a := &pebblestore.TaskDeliveryAssessment{State: "unavailable", Freshness: "unknown", ReasonCode: "inspection_timeout", Reason: "Git inspection exceeded its time budget"}
					return taskGitState{deliveryAssessment: a, actionNeeded: taskAssessmentAction(a)}
				}
				if got := worktreeruntime.DeliveryAssessmentBudget(context.Background()); got != 15*time.Second {
					t.Fatalf("status budget %s", got)
				}
				call := &controlMCPCall{request: withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)), server: f.server, workflowDelivery: true}
				handler := f.server.internalRouteHandler()
				call.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet {
						writeJSON(w, 200, map[string]any{"task": task})
						return
					}
					handler.ServeHTTP(w, r)
				})
				for i := 0; i < 3; i++ {
					_, err := controlMCPIntegrate(call, map[string]any{"project_id": proj.ID, "task_id": task.ID}, "merge", "get")
					if err == nil || !strings.Contains(err.Error(), "Git inspection exceeded its time budget (inspection_timeout)") || strings.Contains(err.Error(), "will retry") {
						t.Fatalf("attempt %d: %v", i+1, err)
					}
				}
				sandbox.SetDefault(previous)
				unchanged, _, _ := db.GetProjectTask(p.AccountScopeID, proj.ID, task.ID)
				if calls != 3 || unchanged.Integration != nil || integrationGit(t, source, "rev-parse", "HEAD") != base {
					t.Fatal("timeout mutated delivery or lost attempts")
				}
				// Interactive caller (swarm_integrate_task / swarm_deliver_job):
				// one timed-out assessment, then the real one, merges in one call.
				interactive := 0
				inspectIntegrationGitState = func(ctx context.Context, task pebblestore.ProjectTaskRecord, db *pebblestore.SessionStore) taskGitState {
					interactive++
					if interactive == 1 {
						a := &pebblestore.TaskDeliveryAssessment{State: "unavailable", Freshness: "unknown", ReasonCode: "inspection_timeout", Reason: "Git inspection exceeded its time budget"}
						return taskGitState{deliveryAssessment: a, actionNeeded: taskAssessmentAction(a)}
					}
					return original(ctx, task, db)
				}
				call.workflowDelivery = false
				out, err := controlMCPIntegrate(call, map[string]any{"project_id": proj.ID, "task_id": task.ID}, "swarm_integrate_task", "swarm_list_tasks")
				if err != nil || interactive != 2 {
					t.Fatalf("interactive retry: assessments %d err %v", interactive, err)
				}
				receipt, _ := out.(map[string]any)["integration"].(map[string]any)
				if receipt["resulting_target_head"] != head {
					t.Fatalf("interactive result %#v", out)
				}
				integrationGit(t, source, "merge-base", "--is-ancestor", head, "HEAD")
				return
			}
			if strings.HasPrefix(entry, "detached-") {
				commit := head
				if entry == "detached-stale" {
					commit = base
				}
				integrationGit(t, child, "checkout", "--detach", commit)
			}
			if entry == "detached-lock" {
				lock := filepath.Join(integrationGit(t, child, "rev-parse", "--absolute-git-dir"), "HEAD.lock")
				if err := os.WriteFile(lock, []byte("held"), 0600); err != nil {
					t.Fatal(err)
				}
				_, err := f.server.IntegrateProjectTask(context.Background(), p, proj.ID, task.ID)
				if err == nil || !strings.Contains(err.Error(), "HEAD.lock") {
					t.Fatalf("lost lock failure: %v", err)
				}
				unchanged, _, _ := db.GetProjectTask(p.AccountScopeID, proj.ID, task.ID)
				if unchanged.Integration != nil || integrationGit(t, child, "rev-parse", "--symbolic-full-name", "HEAD") != "HEAD" || integrationGit(t, source, "rev-parse", "HEAD") != base {
					t.Fatal("lock failure mutated delivery")
				}
				return
			}
			if entry == "detached-dirty" {
				if err := os.WriteFile(filepath.Join(child, "result.txt"), []byte("uncommitted\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if entry == "wrong-branch" {
				integrationGit(t, child, "checkout", "-b", "other")
			}
			if entry == "target-mismatch" {
				integrationGit(t, source, "checkout", "--detach", base)
			}
			if entry == "detached-stale" || entry == "detached-dirty" || entry == "wrong-branch" || entry == "target-mismatch" {
				before := integrationGit(t, child, "rev-parse", "HEAD", "--symbolic-full-name", "HEAD")
				_, err := f.server.IntegrateProjectTask(context.Background(), p, proj.ID, task.ID)
				if err == nil || !strings.Contains(err.Error(), "lane_branch_mismatch") {
					t.Fatalf("mismatch: %v", err)
				}
				args, _ := json.Marshal(tool.ProjectTaskIntegrationSelection{SessionID: session.ID, SourceBranch: "agent/task", TargetBranch: "dev"})
				code, out := f.do(http.MethodPost, "/v3/projects/"+proj.ID+"/tasks/"+task.ID+"/integrate", string(args))
				var body map[string]any
				if err := json.Unmarshal([]byte(out), &body); err != nil {
					t.Fatal(err)
				}
				delivery, ok := body["delivery"].(map[string]any)
				if code != http.StatusConflict || !ok || delivery["reason_code"] != "lane_branch_mismatch" {
					t.Fatalf("lost delivery assessment: %d %s", code, out)
				}
				unchanged, _, _ := db.GetProjectTask(p.AccountScopeID, proj.ID, task.ID)
				if unchanged.Integration != nil || integrationGit(t, source, "rev-parse", "HEAD") != base || integrationGit(t, child, "rev-parse", "HEAD", "--symbolic-full-name", "HEAD") != before {
					t.Fatal("rejection mutated lane, receipt or target")
				}
				return
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
			} else if entry == "detached-tip-workflow" {
				// Exercise toBranch through the real HTTP routes and real Git:
				// a fabricated integration response cannot prove warning delivery.
				integrationGit(t, child, "checkout", "--detach", head)
				handler := f.server.internalRouteHandler()
				call := &controlMCPCall{request: withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)), server: f.server}
				call.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, r)
					if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/integrate") && rec.Code == http.StatusOK {
						if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
							t.Fatal(err)
						}
					}
					for k, values := range rec.Header() {
						w.Header()[k] = values
					}
					w.WriteHeader(rec.Code)
					_, _ = w.Write(rec.Body.Bytes())
				})
				f.server.jobAutomationCallFn = func(context.Context) (*controlMCPCall, error) { return call, nil }
				outcome, err := (jobAutomationDeliverer{s: f.server}).toBranch(context.Background(), jobautomation.DeliverRequest{Folder: source, To: jobautomation.DeliverMerge, Result: jobautomation.Result{JobID: "task/" + proj.ID + "/" + task.ID, Git: &jobautomation.ResultGit{Head: head}}})
				if err != nil {
					t.Fatal(err)
				}
				warning, _ := result["integration"].(map[string]any)["warning"].(string)
				if warning == "" || !strings.Contains(outcome.Where, warning) || len(outcome.Delivered) != 1 || !strings.Contains(outcome.Delivered[0].Where, warning) {
					t.Fatalf("warning lost: response=%#v outcome=%#v", result, outcome)
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
			if entry == "detached-tip" && receipt["warning"] == "" {
				t.Fatal("HTTP response lost recovery warning")
			}
			updated, _, _ := db.GetProjectTask(p.AccountScopeID, proj.ID, task.ID)
			if strings.HasPrefix(entry, "detached-tip") && (updated.Integration.Warning == "" || integrationGit(t, child, "symbolic-ref", "--short", "HEAD") != "agent/task") {
				t.Fatal("recovery did not attach and record warning")
			}
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
