package run

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// A folder whose publish rule keeps up with its forge starts each worker job
// from the commit the rule fetched, records the forge branch as the job's base,
// and does not start the job when the rule cannot bring the base.
func TestWorkerJobStartsFromThePublishRulesForgeBase(t *testing.T) {
	runs, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	canonical, err := runs.sessionDeployCanonicalize(SessionDeployCanonicalizeInput{})
	if err != nil {
		t.Fatal(err)
	}
	repo := canonical.SourceWorkspacePath
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// The forge's main moved on: a merged pull request the folder's dev lacks.
	forge := git("commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "merged on the forge")
	git("update-ref", "refs/swarm/publish/main", forge)

	var seenFolder, seenCurrent string
	fail := false
	runs.SetPublishBaseResolver(func(_ context.Context, folder, current string) (string, string, bool, error) {
		seenFolder, seenCurrent = folder, current
		if fail {
			return "", "", false, errors.New("fetch refused")
		}
		return forge, "main", true, nil
	})

	plan := store.SessionPlanDocument{Title: "Refresh", Info: store.SessionPlanInfo{Goal: "Refresh"}, Checkpoints: []store.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: "Refresh", Tasks: []string{"Refresh"}, AcceptanceCriteria: []string{"Done"}, Status: "pending"}}}
	ws := ss.Store().WorkerStore()
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "Curator", WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}, Automations: []store.WorkerAutomationDefinition{{Name: "daily", ActivationMode: "manual", Enabled: true, PlanDocument: plan}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w, err = execution.Activate("account", "owner", w.ID, w.Revision, map[string]string{"primary": workspaceID}); err != nil {
		t.Fatal(err)
	}
	r, err := execution.Dispatch(context.Background(), "account", "owner", store.WorkerRunAdmission{WorkerID: w.ID, AutomationID: w.Automations[0].ID, RequestSource: "test_run", Input: map[string]any{"prompt": "Refresh"}, IdempotencyKey: "forge-base-1"})
	if err != nil {
		t.Fatal(err)
	}
	if seenFolder != repo || seenCurrent != "dev" {
		t.Fatalf("resolver saw folder %q branch %q", seenFolder, seenCurrent)
	}
	task, found, err := ss.Store().GetProjectTask("account", "proj_fixture", "task_"+r.ID)
	if err != nil || !found {
		t.Fatalf("task: %v %v", found, err)
	}
	if task.BaseCommit != forge || task.BaseBranch != "main" {
		t.Fatalf("job base = %s on %q, want the forge's %s on main", task.BaseCommit, task.BaseBranch, forge)
	}
	if head := strings.TrimSpace(gitOut(t, task.WorkspacePath, "rev-parse", "HEAD")); head != forge {
		t.Fatalf("job worktree starts at %s, want %s", head, forge)
	}

	fail = true
	r2, err := execution.Dispatch(context.Background(), "account", "owner", store.WorkerRunAdmission{WorkerID: w.ID, AutomationID: w.Automations[0].ID, RequestSource: "test_run", Input: map[string]any{"prompt": "Refresh"}, IdempotencyKey: "forge-base-2"})
	if err == nil || !strings.Contains(err.Error(), "fetch refused") {
		t.Fatalf("a job started without its forge base: %v", err)
	}
	// The next reconcile fails the run visibly (worker.run.failed), with the reason.
	_ = execution.ReconcileWorker(context.Background(), "account", w.ID)
	failed, found, err := ws.GetWorkerRun("account", w.ID, r2.ID)
	if err != nil || !found || failed.Status != "failed" || !strings.Contains(failed.Error, "fetch refused") {
		t.Fatalf("run = %+v %v %v, want failed naming the refusal", failed, found, err)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return string(out)
}
