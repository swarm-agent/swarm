package tool

import (
	"errors"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type observingPromotionWorktrees struct {
	*coderLineageWorktreeService
	beforePrepare func()
	applyErr      error
}

func (s *observingPromotionWorktrees) PrepareTaskIntegration(path, branch, head string, children []worktreeruntime.TaskIntegrationChild) (worktreeruntime.TaskIntegrationPlan, error) {
	for _, child := range children {
		if !child.PreserveAncestry {
			return worktreeruntime.TaskIntegrationPlan{}, errors.New("promotion must preserve original source ancestry")
		}
	}
	s.beforePrepare()
	return s.coderLineageWorktreeService.PrepareTaskIntegration(path, branch, head, children)
}

func (s *observingPromotionWorktrees) ApplyTaskIntegration(path string, plan worktreeruntime.TaskIntegrationPlan) (worktreeruntime.TaskIntegrationResult, error) {
	if s.applyErr != nil {
		return worktreeruntime.TaskIntegrationResult{}, s.applyErr
	}
	return s.coderLineageWorktreeService.ApplyTaskIntegration(path, plan)
}

// Purpose: manageWorktreePromote must persist progress before Git preparation,
// and verified terminal receipts for a single source, rejecting unsupported
// batches before Git or task mutations. Real temp-store
// publication proves the tool uses durable task/outbox authority. Git doubles
// isolate orchestration failure paths; existing real-Git tests own Git validity.
func TestManageWorktreePromoteTaskLifecycle(t *testing.T) {
	for _, mode := range []string{"single", "batch", "prepare-failure", "apply-failure", "stale-source", "persistence-failure"} {
		t.Run(mode, func(t *testing.T) {
			runtime, scope, worktrees, _ := newCoderLineageRuntime(t)
			scope.Roots = append(scope.Roots, "/captured")
			sessions := runtime.sessions.(*coderLineageSessionService)
			source := sessions.parent
			source.Metadata["swarm_v3_source_workspace_path"] = "/captured"
			source.Metadata["base_commit"] = "captured-base"
			source.Metadata["project_id"] = "project"
			source.Metadata["task_id"] = "task"
			sessions.parent = source
			worktrees.states["/captured"] = worktreeruntime.TaskWorkspaceState{BranchName: "dev", HeadCommit: "target-head", Clean: true}
			db, err := pebblestore.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := pebblestore.NewSessionStore(db)
			runtime.projects = store
			ids := []any{source.ID}
			taskIDs := []string{"task"}
			if mode == "batch" {
				second := source
				second.ID = "second-source"
				second.Metadata = map[string]any{"swarm_v3_source_workspace_path": "/captured", "base_commit": "captured-base", "project_id": "project", "task_id": "second-task"}
				sessions.children[second.ID] = second
				ids = append(ids, second.ID)
				taskIDs = append(taskIDs, "second-task")
			}
			for i, id := range taskIDs {
				sessionID := ids[i].(string)
				if mode == "stale-source" {
					sessionID = "replacement"
				}
				if err := store.PutProjectTask(scope.Principal.AccountScopeID, &pebblestore.ProjectTaskRecord{ID: id, ProjectID: "project", Title: "Task", SessionID: sessionID, Status: "needs_review", Revision: 1}); err != nil {
					t.Fatal(err)
				}
			}
			wakeups := 0
			db.SetProjectPublisher(func(_ pebblestore.V3RealtimeOutboxRecord) { wakeups++ })
			observed := &observingPromotionWorktrees{coderLineageWorktreeService: worktrees, beforePrepare: func() {
				for _, id := range taskIDs {
					task, _, err := store.GetProjectTask(scope.Principal.AccountScopeID, "project", id)
					if err != nil || task.Integration == nil || task.Integration.State != "in_progress" || task.Integration.SourceHead != "parent-head" || task.IsIntegrated {
						t.Fatalf("Git started without progress: %+v %v", task, err)
					}
				}
				if mode == "persistence-failure" {
					store.SetProjectTaskUpdateHookForTest(func(string) error { return errors.New("injected write failure") })
				}
			}}
			if mode == "prepare-failure" {
				worktrees.prepareErr = errors.New("merge conflict")
			}
			if mode == "apply-failure" {
				observed.applyErr = errors.New("apply failed")
			}
			runtime.worktrees = observed
			_, err = runtime.manageWorktreePromote(scope, map[string]any{"source_session_ids": ids, "target_branch": "dev"})
			success := mode == "single"
			if success && err != nil {
				t.Fatal(err)
			}
			if !success && err == nil {
				t.Fatal("failure reported success")
			}
			for _, id := range taskIDs {
				task, _, readErr := store.GetProjectTask(scope.Principal.AccountScopeID, "project", id)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if task.IsIntegrated != success || (task.Status == "completed") != success {
					t.Fatalf("wrong completion: %+v", task)
				}
				if success && (task.Integration.State != "integrated" || task.Integration.ResultingTargetHead == "") {
					t.Fatalf("missing verification receipt: %+v", task)
				}
				if mode == "prepare-failure" && (task.Integration.State != "conflict" || task.Integration.Error == "") {
					t.Fatalf("lost repair receipt: %+v", task)
				}
			}
			if success && wakeups != 2*len(taskIDs) {
				t.Fatalf("wakeups = %d", wakeups)
			}
			if (mode == "stale-source" || mode == "batch") && (wakeups != 0 || worktrees.applyCalls != 0) {
				t.Fatal("stale source mutated state")
			}
		})
	}
}
