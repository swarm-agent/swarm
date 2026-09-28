package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// A summary revision must never leave an old executable plan approvable.
func TestBoundProjectTaskRefineInvalidatesApproval(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	// These boundary tests inspect durable intents, not provider execution.
	f.server.v3SessionExecutor = nil
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	project := f.createProject(t)
	if err := f.server.sessions.Store().CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	w := f.callAPI(http.MethodPost, "/"+project+"/tasks", map[string]any{"title": "Plan output", "prompt": "Create old.json", "agent": "plan", "feature_size": "big"}, p)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Task pebblestore.ProjectTaskRecord `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	task := response.Task
	if intent, found, err := f.server.sessions.Store().GetV3SessionActiveRunIntent(task.SessionID); err != nil {
		t.Fatal(err)
	} else if found {
		intent.Status = pebblestore.V3RunIntentCompleted
		key := "fixture-planning-complete"
		if _, err := f.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: task.SessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: sessionruntime.SessionMutationRecordRunIntent, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, RunIntent: &intent}); err != nil {
			t.Fatal(err)
		}
	}
	doc := &pebblestore.SessionPlanDocument{ID: "revise-plan", Title: "Write output", Info: pebblestore.SessionPlanInfo{Goal: "Create old.json"}, Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: "Write file", Tasks: []string{"Create old.json"}, AcceptanceCriteria: []string{"old.json committed"}}}}
	_, err := f.server.planLifecycle.SubmitProjectTaskStructuredPlan(sessionruntime.ProjectTaskPlanSubmissionInput{AccountScopeID: f.accountID, UserID: f.userID, ProjectID: project, TaskID: task.ID, SessionID: task.SessionID, Document: doc})
	if err != nil {
		t.Fatal(err)
	}
	guards := tool.ProjectTaskApprovalGuards{SessionID: task.SessionID, PlanID: doc.ID, DefinitionRevision: 1}
	for _, body := range []map[string]any{{"feedback": "Use new.json"}, {"feedback": "Use new.json", "session_id": task.SessionID, "plan_id": doc.ID, "definition_revision": 99}} {
		w = f.callAPI(http.MethodPost, "/"+project+"/tasks/"+task.ID+"/refine", body, p)
		if w.Code != http.StatusConflict {
			t.Fatalf("missing/stale guards: %d %s", w.Code, w.Body.String())
		}
	}
	w = f.callAPI(http.MethodPost, "/"+project+"/tasks/"+task.ID+"/refine", map[string]any{"feedback": "Use new.json", "session_id": task.SessionID, "plan_id": doc.ID, "definition_revision": 1}, p)
	if w.Code != http.StatusOK {
		t.Fatalf("refine: %d %s", w.Code, w.Body.String())
	}
	if _, err = f.server.ApproveProjectTask(context.Background(), p, project, task.ID, guards); err == nil {
		t.Fatal("old approval executed after revision request")
	}
	updated, _, _ := f.server.sessions.Store().GetProjectTask(f.accountID, project, task.ID)
	if updated.Status != "planning" {
		t.Fatalf("status %s", updated.Status)
	}
	plan, _, _ := f.server.sessions.Store().GetPlan(task.SessionID, doc.ID)
	if plan.ApprovalState != "rejected" || plan.Version <= 1 {
		t.Fatalf("old definition still approvable: %#v", plan)
	}
	doc.Checkpoints[0].Tasks = []string{"Create new.json"}
	doc.Checkpoints[0].AcceptanceCriteria = []string{"new.json committed"}
	submitted, err := f.server.planLifecycle.SubmitProjectTaskStructuredPlan(sessionruntime.ProjectTaskPlanSubmissionInput{AccountScopeID: f.accountID, UserID: f.userID, ProjectID: project, TaskID: task.ID, SessionID: task.SessionID, Document: doc})
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Receipt == "" {
		t.Fatal("missing new receipt")
	}
	updated, _, _ = f.server.sessions.Store().GetProjectTask(f.accountID, project, task.ID)
	if updated.Status != "pending_approval" || updated.PlanBinding.DefinitionRevision <= 1 {
		t.Fatalf("replacement not pending approval: %#v", updated)
	}
	if _, err = f.server.ApproveProjectTask(context.Background(), p, project, task.ID, guards); err == nil {
		t.Fatal("old approval accepted replacement")
	}
}

// A supplied task plan must not execute in the captured source checkout.
func TestSuppliedProjectPlanHasOwnedLaneWithoutRun(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	// These boundary tests inspect durable intents, not provider execution.
	f.server.v3SessionExecutor = nil
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	project := f.createProject(t)
	if err := f.server.sessions.Store().CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	doc := &pebblestore.SessionPlanDocument{ID: "supplied-plan", Title: "Write output", Info: pebblestore.SessionPlanInfo{Goal: "Write file"}, Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: "Write file", Tasks: []string{"Create result.txt"}, AcceptanceCriteria: []string{"result.txt committed"}}}}
	task, err := f.server.CreateProjectTask(context.Background(), p, project, tool.ProjectTaskCreateInput{Title: "Supplied plan", Prompt: "Create result.txt", Agent: "swarm", FeatureSize: "big", Document: doc})
	if err != nil {
		t.Fatal(err)
	}
	sess, ok, err := f.server.sessions.Store().GetSession(task.SessionID)
	if err != nil || !ok || !sess.WorktreeEnabled || sess.WorktreeRootPath == "" || sess.Mode != sessionruntime.ModePlan {
		t.Fatalf("no isolated planning lane: %#v %v", sess, err)
	}
	if task.WorkspacePath != sess.WorktreeRootPath || task.Status != "pending_approval" {
		t.Fatalf("task not bound to pending lane: %#v", task)
	}
	if intent, found, _ := f.server.sessions.Store().GetV3SessionActiveRunIntent(task.SessionID); found {
		t.Fatalf("unapproved run started: %#v", intent)
	}
}

func TestIdenticalTaskPromptsGetDistinctOwnedWorktrees(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	f.server.v3SessionExecutor = nil
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	project := f.createProject(t)
	if err := f.server.sessions.Store().CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.server.worktrees = &distinctFixtureWorktrees{}
	input := tool.ProjectTaskCreateInput{ID: "task-one", Title: "Same title", Prompt: "Same small change", Agent: "coder", FeatureSize: "small"}
	first, err := f.server.CreateProjectTask(context.Background(), p, project, input)
	if err != nil {
		t.Fatal(err)
	}
	input.ID = "task-two"
	second, err := f.server.CreateProjectTask(context.Background(), p, project, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.WorkspacePath == second.WorkspacePath || first.WorktreeBranch == second.WorktreeBranch {
		t.Fatal("identical prompts shared a mutable worktree")
	}
	replayed, err := f.server.CreateProjectTask(context.Background(), p, project, input)
	if err != nil || replayed.SessionID != second.SessionID || replayed.WorkspacePath != second.WorkspacePath {
		t.Fatalf("retry changed owned lane: %#v %v", replayed, err)
	}
}

type distinctFixtureWorktrees struct{ testMockWorktreeService }

func (m *distinctFixtureWorktrees) AllocateDetachedWorkspaceRequestedForPrincipal(p identity.Principal, workspace, seed, base, branch string) (worktreeruntime.Allocation, error) {
	return worktreeruntime.Allocation{WorkspacePath: "/mock/worktrees/" + branch, BranchName: branch, BaseBranch: "dev", BaseCommit: "base-commit-sha-001", RepoRoot: workspace}, nil
}

func TestProgramOnlyTaskUsesCanonicalPlanSession(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	f.server.v3SessionExecutor = nil
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	project := f.createProject(t)
	if err := f.server.sessions.Store().CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	var prog pebblestore.TaskProgramDefinition
	if err := json.Unmarshal([]byte(`{"id":"files","stages":[{"id":"s1","dependency_evidence":"Ready"}],"jobs":[{"id":"file","stage_id":"s1","agent_type":"coder","title":"File","meta_prompt":"Write result.txt and commit","deliverable":"Committed result.txt","owned_scope":["result.txt"],"acceptance_criteria":["File committed"],"dependency_evidence":"Ready"}]}`), &prog); err != nil {
		t.Fatal(err)
	}
	task, err := f.server.CreateProjectTask(context.Background(), p, project, tool.ProjectTaskCreateInput{Title: "Program only", Prompt: "Write a file", Agent: "swarm", TaskProgram: &prog})
	if err != nil {
		t.Fatal(err)
	}
	sess, _, _ := f.server.sessions.Store().GetSession(task.SessionID)
	if task.PlanBinding == nil || task.PlanDocument == nil || !sess.WorktreeEnabled || sess.Metadata["agent_profile"] == nil || sess.Mode != sessionruntime.ModePlan {
		t.Fatalf("noncanonical program coordinator: task=%#v session=%#v", task, sess)
	}
}
