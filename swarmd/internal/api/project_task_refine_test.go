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
)

// A summary revision must never leave an old executable plan approvable.
func TestBoundProjectTaskRefineInvalidatesApproval(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
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
