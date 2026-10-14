package run

import (
	"context"
	"os/exec"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: resolveReviewTarget must reject missing, empty and cross-project
// jobs, capture committed Git HEAD rather than dirty output, and provide the
// landing ref and report. Real durable workers/tasks and Git are the narrowest
// admission source-authority proof, without invoking a provider.
func TestReviewerTargetValidationAndContext(t *testing.T) {
	_, ss, s, workspace := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	ws := ss.Store().WorkerStore()
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "Coder", Role: store.WorkerRoleCoder}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Activate("account", "owner", w.ID, w.Revision, nil)
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.Dispatch(context.Background(), "account", "owner", store.WorkerRunAdmission{WorkerID: w.ID, WorkspaceID: workspace, RequestSource: "direct", Input: map[string]any{"prompt": "implement brief"}, IdempotencyKey: "source"})
	if err != nil {
		t.Fatal(err)
	}
	r := store.WorkerRunRecord{AccountScopeID: "account", UserID: "owner", WorkspaceID: workspace, Input: map[string]any{"project_id": "proj_fixture", "review_job": "worker/" + w.ID + "/" + job.ID}}
	reviewer := store.WorkerRecord{Role: store.WorkerRoleReviewer}
	if _, err := s.resolveReviewTarget(context.Background(), r, reviewer); err == nil {
		t.Fatal("empty job accepted")
	}
	snapshot, _, err := ss.GetSession(job.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	git := func(path string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
		return string(out)
	}
	git(snapshot.WorktreeRootPath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "implementation")
	git(snapshot.WorkspacePath, "update-ref", "refs/swarm/publish/dev", snapshot.Metadata["base_commit"].(string))
	target, err := s.resolveReviewTarget(context.Background(), r, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	env := reviewEnvironment(map[string]any{"worker_review_target": target})
	if env["SWARM_REVIEW_HEAD"] == "" || env["SWARM_REVIEW_BASE"] != "refs/swarm/publish/dev" || env["SWARM_REVIEW_JOB"] != r.Input["review_job"] || target["brief"] != "implement brief" {
		t.Fatal(target, env)
	}
	if err := ss.Store().PutProject("account", &store.ProjectRecord{ID: "proj_other", Name: "other", Workspaces: []store.ProjectWorkspaceRef{{WorkspaceID: workspace, Path: snapshot.WorkspacePath}}}); err != nil {
		t.Fatal(err)
	}
	r.Input["project_id"] = "proj_other"
	if _, err := s.resolveReviewTarget(context.Background(), r, reviewer); err == nil {
		t.Fatal("cross-project job accepted")
	}
	r.Input["project_id"] = "proj_fixture"
	r.Input["review_job"] = "worker/missing/missing"
	if _, err := s.resolveReviewTarget(context.Background(), r, reviewer); err == nil {
		t.Fatal("missing job accepted")
	}
	r.AccountScopeID = "foreign"
	if _, err := s.resolveReviewTarget(context.Background(), r, reviewer); err == nil {
		t.Fatal("foreign account accepted")
	}
}
