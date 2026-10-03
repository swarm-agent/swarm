package api

import (
	"context"
	"net/http"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: CreateProjectTask/deployProjectTaskExecution must preserve big-feature
// Swarm auto routing (including intent defaults), small Coder routing and explicit
// Plan mode. The hermetic API/store layer proves real session mode and absence of
// unapproved run intents without a provider or permission-setting mutation.
func TestProjectFeatureRoutingUsesSwarmAuto(t *testing.T) {
	for _, tc := range []struct{ name, agent, intent, size, wantAgent, wantMode, wantStatus string }{
		{"big-explicit", "swarm", "code", "big", "swarm", "auto", "pending_approval"},
		{"big-default", "", "code", "big", "swarm", "auto", "pending_approval"},
		{"small", "coder", "code", "small", "coder", "auto", "pending_approval"},
		{"explicit-plan", "plan", "", "big", "plan", "plan", "planning"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupMatrixTestFixture(t)
			defer f.db.Close()
			f.server.v3SessionExecutor = nil
			p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
			project := f.createProject(t)
			if err := f.server.sessions.Store().CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
				t.Fatal(err)
			}
			created := requireMatrixTaskResponse(t, f.callAPI(http.MethodPost, "/"+project+"/tasks", map[string]any{"title": "Feature", "prompt": "Implement feature", "agent": tc.agent, "intent": tc.intent, "feature_size": tc.size}, p), http.StatusCreated)
			task, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, project, created["id"].(string))
			if err != nil || !found {
				t.Fatalf("created task missing: %v", err)
			}
			if task.Agent != tc.wantAgent || task.Status != tc.wantStatus {
				t.Fatalf("route: %+v", task)
			}
			sess, found, err := f.server.sessions.Store().GetSession(task.SessionID)
			if err != nil || !found || sess.Mode != tc.wantMode || !sess.WorktreeEnabled {
				t.Fatalf("session: %+v %v", sess, err)
			}
			if tc.wantAgent == "swarm" {
				if sess.Preference.Model != "gemini-2.5-action" {
					t.Fatalf("Swarm did not use configured action model: %+v", sess.Preference)
				}
				preview, _, err := f.server.resolveTaskModelPreference(p, task)
				if err != nil || preview.Model != sess.Preference.Model {
					t.Fatalf("preview/launch mismatch: %+v %v", preview, err)
				}
			}
			if tc.wantMode == "auto" {
				if task.PlanBinding != nil || task.OutcomeType != "code_pr" {
					t.Fatalf("unexpected planning contract: %+v", task)
				}
				intents, err := f.server.sessions.Store().ListRunIntents(task.SessionID, 10)
				if err != nil || len(intents) != 0 {
					t.Fatalf("unapproved execution: %+v %v", intents, err)
				}
			}
		})
	}
}

// Purpose: the Orchestrator's structured refinement must publish its own replacement
// through SubmitProjectTaskStructuredPlan and the atomic V3 plan/task publication.
// Stale, cross-account, changed-plan-ID and missing guards must leave the plan,
// card and run intents unchanged. This API/store fixture is narrower than live AI.
func TestOrchestratorStructuredRefinementKeepsCardPending(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	f.server.v3SessionExecutor = nil
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	project := f.createProject(t)
	if err := f.server.sessions.Store().CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	doc := &pebblestore.SessionPlanDocument{ID: "card-plan", Title: "Feature plan", Info: pebblestore.SessionPlanInfo{Goal: "Implement feature"}, Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: "Feature", Tasks: []string{"Implement old behavior"}, AcceptanceCriteria: []string{"Behavior works"}}}}
	task, err := f.server.CreateProjectTask(context.Background(), p, project, tool.ProjectTaskCreateInput{Title: "Feature", Prompt: "Implement feature", Agent: "swarm", FeatureSize: "big", Document: doc})
	if err != nil {
		t.Fatal(err)
	}
	binding := *task.PlanBinding
	doc.Checkpoints[0].Tasks = []string{"Implement revised behavior"}
	input := sessionruntime.ProjectTaskPlanSubmissionInput{AccountScopeID: f.accountID, UserID: f.userID, ProjectID: project, TaskID: task.ID, SessionID: task.SessionID, ExpectedPlanID: binding.PlanID, ExpectedDefinitionRevision: binding.DefinitionRevision, Document: doc, Feedback: "Revise behavior"}
	for _, kind := range []string{"stale", "missing", "foreign", "wrong-id"} {
		bad := input
		switch kind {
		case "stale":
			bad.ExpectedDefinitionRevision++
		case "missing":
			bad.ExpectedDefinitionRevision = 0
		case "foreign":
			bad.AccountScopeID = "foreign-account"
		case "wrong-id":
			copyDoc := *doc
			copyDoc.ID = "another-plan"
			bad.Document = &copyDoc
		}
		if _, err := f.server.SubmitProjectTaskPlan(context.Background(), bad); err == nil {
			t.Fatalf("accepted %s", kind)
		}
		unchanged, _, err := f.server.sessions.Store().GetProjectTask(f.accountID, project, task.ID)
		if err != nil || *unchanged.PlanBinding != binding || unchanged.Status != "pending_approval" {
			t.Fatalf("rejection changed card: %+v %v", unchanged, err)
		}
	}
	revised, err := f.server.SubmitProjectTaskPlan(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if revised.Task.ID != task.ID || revised.Task.SessionID != task.SessionID || revised.Task.Status != "pending_approval" || revised.Plan.Version != binding.DefinitionRevision+1 || revised.Plan.ApprovalState != "pending" || revised.Plan.AcceptedDefinitionReceipt != "" || revised.Plan.Document.Checkpoints[0].Tasks[0] != "Implement revised behavior" {
		t.Fatalf("revision contract: %+v", revised)
	}
	if len(revised.Task.FeedbackHistory) != 1 || revised.Task.FeedbackHistory[0] != input.Feedback {
		t.Fatal("feedback not retained")
	}
	if _, err := f.server.SubmitProjectTaskPlan(context.Background(), input); err == nil {
		t.Fatal("stale revision overwrote replacement")
	}
	guards := tool.ProjectTaskApprovalGuards{SessionID: task.SessionID, PlanID: binding.PlanID, DefinitionRevision: binding.DefinitionRevision}
	if _, err := f.server.ApproveProjectTask(context.Background(), p, project, task.ID, guards); err == nil {
		t.Fatal("stale approval started execution")
	}
	intents, err := f.server.sessions.Store().ListRunIntents(task.SessionID, 10)
	if err != nil || len(intents) != 0 {
		t.Fatalf("revision launched an agent: %+v %v", intents, err)
	}
	// Exact current card approval remains the only implementation transition.
	guards.DefinitionRevision = revised.Plan.Version
	w := f.callAPI(http.MethodPost, "/"+project+"/tasks/"+task.ID+"/approve", guards, p)
	if w.Code != http.StatusOK {
		t.Fatalf("approve revised card: %d %s", w.Code, w.Body.String())
	}
	sess, _, err := f.server.sessions.Store().GetSession(task.SessionID)
	if err != nil || sess.Mode != sessionruntime.ModeAuto {
		t.Fatalf("approved session: %+v %v", sess, err)
	}
	approvedTask, _, _ := f.server.sessions.Store().GetProjectTask(f.accountID, project, task.ID)
	approvedBinding := *approvedTask.PlanBinding
	input.ExpectedDefinitionRevision = approvedBinding.DefinitionRevision
	if _, err := f.server.SubmitProjectTaskPlan(context.Background(), input); err == nil {
		t.Fatal("structured refinement reopened approved execution")
	}
	unchanged, _, _ := f.server.sessions.Store().GetProjectTask(f.accountID, project, task.ID)
	if unchanged.Status != approvedTask.Status || *unchanged.PlanBinding != approvedBinding {
		t.Fatal("rejected refinement mutated approved card")
	}
}
