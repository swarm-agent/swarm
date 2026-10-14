package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: the direct V3 integration endpoint (used by deliver_job) must refuse
// reviewer tasks before lineage selection or Git mutation. HTTP over the real
// task store proves rejection and unchanged durable task state; Git is not
// needed because this policy must reject before reaching Git.
func TestReviewerV3DeliveryRefused(t *testing.T) {
	server, _, raw := newWorkspaceOverviewTopologyTestServer(t)
	db := store.NewSessionStore(raw)
	account := testPrincipal().AccountScopeID
	if err := db.PutProject(account, &store.ProjectRecord{ID: "project", Name: "review"}); err != nil {
		t.Fatal(err)
	}
	task := &store.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: account, Title: "review", Agent: store.WorkerRoleReviewer, Status: "needs_review"}
	if err := db.PutProjectTask(account, task); err != nil {
		t.Fatal(err)
	}
	before, _, _ := db.GetProjectTask(account, "project", "task")
	r := httptest.NewRequest(http.MethodPost, ProjectsPath+"/project/tasks/task/integrate", strings.NewReader(`{}`))
	r = r.WithContext(context.WithValue(r.Context(), productPrincipalRequestContextKey, testPrincipal()))
	w := httptest.NewRecorder()
	server.handleProjects(w, r)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "report-only") {
		t.Fatal(w.Code, w.Body.String())
	}
	after, _, _ := db.GetProjectTask(account, "project", "task")
	if after.Revision != before.Revision || after.Integration != nil || after.IsIntegrated {
		t.Fatal("refusal mutated state", after)
	}
	assessment := inspectTaskGitStateContext(context.Background(), *after, db).deliveryAssessment
	if assessment.State != "report_only" || len(assessment.AllowedActions) != 0 {
		t.Fatal(assessment)
	}
}
