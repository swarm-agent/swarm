package api

import (
	"context"
	"net/http"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: ApproveProjectTask must reject plan outcomes with missing durable
// bindings before dispatch. A damaged pending record must not become a direct
// Swarm run. The temporary API/store fixture is the narrowest admission layer;
// assert unchanged mode, task, and durable run owners as well as rejection.
func TestProjectPlanReviewMissingBindingDoesNotDispatch(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projectID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	response := f.callAPI(http.MethodPost, "/"+projectID+"/tasks", map[string]any{
		"title": "Review required", "prompt": "Author a plan", "agent": "plan", "feature_size": "big",
	}, p)
	task := requireMatrixTaskResponse(t, response, http.StatusCreated)
	taskID, sessionID := task["id"].(string), task["session_id"].(string)
	if _, err := f.server.sessions.Store().UpdateProjectTask(f.accountID, projectID, taskID, func(task *pebblestore.ProjectTaskRecord) error {
		task.Status = "pending_approval"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := f.server.sessions.Store().ListRunIntents(sessionID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.ApproveProjectTask(context.Background(), p, projectID, taskID); err == nil {
		t.Fatal("accepted plan task with missing binding")
	}
	after, err := f.server.sessions.Store().ListRunIntents(sessionID, 10)
	if err != nil || len(after) != len(before) {
		t.Fatalf("rejected approval changed durable owners: %+v %v", after, err)
	}
	for i := range before {
		if after[i].RunID != before[i].RunID || after[i].Status != before[i].Status {
			t.Fatalf("rejected approval changed run: %+v", after[i])
		}
	}
	session, found, err := f.server.sessions.Store().GetSession(sessionID)
	if err != nil || !found || session.Mode != "plan" {
		t.Fatalf("rejected approval changed mode: %+v %v", session, err)
	}
	stored, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, projectID, taskID)
	if err != nil || !found || stored.Status != "pending_approval" || stored.PlanBinding != nil {
		t.Fatalf("rejected approval changed task: %+v %v", stored, err)
	}
}

// Purpose: task detail hydration must not expose a foreign-account plan or a
// different session's document even when a forged binding names its exact ID.
// hydrateTaskPlanDocument with a temporary store proves the read boundary only.
func TestProjectPlanReviewHydrationIsolation(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	store := f.server.sessions.Store()
	plan := pebblestore.SessionPlanSnapshot{ID: "plan", SessionID: "owner-session", AccountScopeID: f.accountID, Version: 1,
		Document: &pebblestore.SessionPlanDocument{ID: "plan", Title: "Private definition"}}
	if err := store.PutPlan(plan); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ account, session string }{{"foreign-account", "owner-session"}, {f.accountID, "other-session"}} {
		task := &pebblestore.ProjectTaskRecord{AccountID: tc.account, SessionID: tc.session,
			PlanBinding: &pebblestore.ProjectTaskPlanBinding{PlanID: "plan", SessionID: "owner-session", DefinitionRevision: 1}}
		hydrateTaskPlanDocument(task, store)
		if task.PlanDocument != nil {
			t.Fatalf("foreign binding exposed plan: %+v", task.PlanDocument)
		}
	}
	owned := &pebblestore.ProjectTaskRecord{AccountID: f.accountID, SessionID: "owner-session",
		PlanBinding: &pebblestore.ProjectTaskPlanBinding{PlanID: "plan", SessionID: "owner-session", DefinitionRevision: 1}}
	hydrateTaskPlanDocument(owned, store)
	if owned.PlanDocument == nil || owned.PlanDocument.Title != "Private definition" {
		t.Fatal("owned exact definition was not hydrated")
	}
}
