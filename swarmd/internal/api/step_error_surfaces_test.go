package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
)

// Purpose: a delivery stopped by a run step whose script never started
// reads as an infrastructure error on every task surface: the outcome is
// "error <step> (infrastructure: <kind>)", the task state is blocked_infra
// (never needs_you: nothing waits on the person) and its reason says it is
// an infrastructure problem and not the work's and what to do next; a
// failed check is workflow_failed, never named infrastructure; the summary carries the
// delivery's error reason, and steps_result carries status error with
// error_kind. Threat: a checkout failure shown as a failed check the person
// or a coder must fix (the production review loop). Owners:
// computeTaskProgress, taskProgressOutcome, deliveryStepsResult and
// pebblestore.ProjectTaskState; pure functions over real records, the
// narrowest layer that holds the task state together.
func TestTaskProgressNamesAnInfrastructureError(t *testing.T) {
	d := jobautomation.Delivery{ID: "jdel-err", AutomationID: "auto-1", AutomationName: "review loop", State: jobautomation.StateError, Round: 1,
		Steps: []jobautomation.StepRecord{
			{ID: "console", Type: jobautomation.StepRun, State: jobautomation.StateError, ErrorKind: jobautomation.ErrorKindCheckout, StartedAt: 1000, FinishedAt: 2000,
				Reason: "the script could not run: could not make the step's checkout in the environment: object d1d5 could not be read: permission denied"},
			{ID: "fix-console", Type: jobautomation.StepAgent, State: jobautomation.StateSkipped, Reason: "condition not met: step console is error, not failed"},
		},
		Definition: []jobautomation.Step{{ID: "console", Type: jobautomation.StepRun, In: jobautomation.RunInEnvironment}, {ID: "fix-console", Type: jobautomation.StepAgent}},
	}
	task := pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", Status: "needs_review", Agent: "coder"}
	p := computeTaskProgress(task, []jobautomation.Delivery{d}, nil, 30000)
	if !strings.HasPrefix(p.Outcome, "error ") || !strings.Contains(p.Outcome, "(infrastructure: checkout)") {
		t.Fatalf("outcome = %q", p.Outcome)
	}
	if p.State != progressFailed || !strings.Contains(p.Summary, "infrastructure error, not the work") || !strings.Contains(p.Summary, "console could not run (checkout)") {
		t.Fatalf("progress state %q summary %q", p.State, p.Summary)
	}
	state, reason := pebblestore.ProjectTaskState(task, p.Outcome, false, false)
	if state != pebblestore.ProjectTaskStateBlockedInfra || !strings.Contains(reason, "an infrastructure problem, not the work") || !strings.Contains(reason, "nothing for a coder to fix") || !strings.Contains(reason, "rerun the failed steps") {
		t.Fatalf("task state %q reason %q", state, reason)
	}
	if !pebblestore.ProjectTaskNeedsAttention(task, p.Outcome, false, false) || pebblestore.ProjectTaskBucket(task, p.Outcome, false, false) != pebblestore.ProjectTaskCountBlockedInfra {
		t.Fatalf("infrastructure error bucket %q", pebblestore.ProjectTaskBucket(task, p.Outcome, false, false))
	}
	steps := deliveryStepsResult(d)
	if len(steps) != 2 || steps[0].Status != jobautomation.StateError || steps[0].ErrorKind != jobautomation.ErrorKindCheckout || steps[1].ErrorKind != "" {
		t.Fatalf("steps_result = %+v", steps)
	}
	// A failed check (the script ran) keeps its failed outcome and reason.
	failed := d
	failed.State = jobautomation.StateFailed
	failed.Steps = []jobautomation.StepRecord{{ID: "console", Type: jobautomation.StepRun, State: jobautomation.StateFailed, Reason: "exited with code 1"}}
	fp := computeTaskProgress(task, []jobautomation.Delivery{failed}, nil, 30000)
	if !strings.HasPrefix(fp.Outcome, "failed ") {
		t.Fatalf("failed outcome = %q", fp.Outcome)
	}
	if state, reason := pebblestore.ProjectTaskState(task, fp.Outcome, false, false); state != pebblestore.ProjectTaskStateWorkflowFailed || strings.Contains(reason, "infrastructure") || !strings.Contains(reason, "send the task back") {
		t.Fatalf("a failed check reads %q: %q", state, reason)
	}
}

// landedAfterInfraErrorDelivery is the production shape of a delivery whose
// environment check could not run while its push still ran: gate passed,
// console (in the folder's environment, allowed to fail) ended error with
// error_kind checkout, review passed with verdict ok and the host push step
// succeeded, so the runner ended the delivery error.
func landedAfterInfraErrorDelivery(push string) jobautomation.Delivery {
	head := strings.Repeat("ab", 20)
	return jobautomation.Delivery{ID: "jdel-landed", AutomationID: "auto-1", AutomationName: "review loop", State: jobautomation.StateError, Round: 1, Head: head, FinishedAt: 9000,
		Steps: []jobautomation.StepRecord{
			{ID: "gate", Type: jobautomation.StepRun, State: jobautomation.StateSucceeded, StartedAt: 1000, FinishedAt: 2000},
			{ID: "console", Type: jobautomation.StepRun, State: jobautomation.StateError, ErrorKind: jobautomation.ErrorKindCheckout, StartedAt: 2000, FinishedAt: 3000,
				Reason: "the script could not run: could not make the step's checkout in the environment"},
			{ID: "review", Type: jobautomation.StepAgent, State: jobautomation.StateSucceeded, StartedAt: 3000, FinishedAt: 6000, Outcome: map[string]any{"verdict": "ok"}},
			{ID: "push-clean", Type: jobautomation.StepRun, State: push, StartedAt: 6000, FinishedAt: 8000, Output: "PUSHED " + head},
		},
		Definition: []jobautomation.Step{{ID: "gate", Type: jobautomation.StepRun}, {ID: "console", Type: jobautomation.StepRun, In: jobautomation.RunInEnvironment, ContinueOnFailure: true}, {ID: "review", Type: jobautomation.StepAgent}, {ID: "push-clean", Type: jobautomation.StepRun}},
	}
}

// Purpose: a delivery whose push step succeeded landed the work even though
// an earlier environment step could not run (error, error_kind checkout):
// task_state is landed with a state_reason naming the pushed sha and, after
// it, the step that could not run; the workflow state is done (not failed),
// the count bucket done and nothing asks for attention; a wait's milestone
// reads landed. Every description of task states (the manage_projects
// help, docs/task-progress.md) states this same rule.
// Threat: a delivery that really landed reported blocked_infra / failed with
// "rerun the failed steps", so an AI reruns work already on the target, and
// descriptions of task_state that drift apart. Owners:
// deliveryLandedAfterStepError (taskProgressOutcome, deliveryWorkflowState),
// pebblestore.ProjectTaskState and pebblestore.ProjectTaskLandedOverStepError;
// computeTaskProgress over a real delivery record is the narrowest layer
// that derives outcome, workflow state and task state together, the same
// values get_task, list_tasks, wait rows and linked machines read.
func TestTaskStateLandedWhenPushSucceededAfterAnInfraError(t *testing.T) {
	d := landedAfterInfraErrorDelivery(jobautomation.StateSucceeded)
	task := pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", Status: "needs_review", Agent: "coder"}
	p := computeTaskProgress(task, []jobautomation.Delivery{d}, nil, 30000)
	sha := shortCommit(d.Head)
	if !strings.HasPrefix(p.Outcome, "pushed "+sha) || !strings.Contains(p.Outcome, "; run console could not run (infrastructure: checkout)") {
		t.Fatalf("outcome = %q", p.Outcome)
	}
	if p.Workflow == nil || p.Workflow.State != workflowDone || len(p.Workflow.Deliveries) != 1 || p.Workflow.Deliveries[0].State != workflowDone {
		t.Fatalf("workflow = %+v", p.Workflow)
	}
	if p.State == progressFailed {
		t.Fatalf("progress state failed for a landed delivery: %q", p.Summary)
	}
	state, reason := pebblestore.ProjectTaskState(task, p.Outcome, false, false)
	if state != pebblestore.ProjectTaskStateLanded || !strings.HasPrefix(reason, "pushed "+sha) || !strings.Contains(reason, "; run console could not run (infrastructure: checkout)") || strings.Contains(reason, "rerun the failed steps") {
		t.Fatalf("task state %q reason %q", state, reason)
	}
	if bucket := pebblestore.ProjectTaskBucket(task, p.Outcome, false, false); bucket != pebblestore.ProjectTaskCountDone || pebblestore.ProjectTaskNeedsAttention(task, p.Outcome, false, false) {
		t.Fatalf("landed delivery bucket %q", bucket)
	}
	// The step that could not run stays visible on the delivery's steps.
	steps := deliveryStepsResult(d)
	if len(steps) != 4 || steps[1].Status != jobautomation.StateError || steps[1].ErrorKind != jobautomation.ErrorKindCheckout {
		t.Fatalf("steps_result = %+v", steps)
	}

	// The same rule, in the same words, on every description of task_state:
	// the MCP/Orchestrator manage_projects help and the documented API
	// (docs/task-progress.md). A wait's guidance is built from the same
	// constant.
	rule := pebblestore.ProjectTaskLandedOverStepError
	manageProjects, ok := tool.CatalogToolNamed("manage_projects")
	if !ok || !strings.Contains(manageProjects.Description, rule) {
		t.Fatalf("manage_projects description lacks the landed rule %q", rule)
	}
	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "task-progress.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(strings.Fields(string(doc)), " "), rule) {
		t.Fatalf("docs/task-progress.md lacks the landed rule %q", rule)
	}
}

// Purpose: the same environment step error with no push (the push step did
// not run, or the step that could not run is the push itself) hands nothing
// over: task_state stays blocked_infra with "rerun the failed steps", the
// workflow state failed. Threat: the landed rule widening into calling an
// unpushed delivery landed. Owners and layer as above.
func TestTaskStateBlockedInfraWhenNothingLanded(t *testing.T) {
	task := pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", Status: "needs_review", Agent: "coder"}
	notPushed := landedAfterInfraErrorDelivery(jobautomation.StateSkipped)
	pushErrored := landedAfterInfraErrorDelivery(jobautomation.StateSucceeded)
	pushErrored.Steps[1].State, pushErrored.Steps[1].ErrorKind = jobautomation.StateSucceeded, ""
	pushErrored.Steps[3].State, pushErrored.Steps[3].ErrorKind = jobautomation.StateError, jobautomation.ErrorKindCheckout
	for name, d := range map[string]jobautomation.Delivery{"no push": notPushed, "push could not run": pushErrored} {
		p := computeTaskProgress(task, []jobautomation.Delivery{d}, nil, 30000)
		if !strings.HasPrefix(p.Outcome, "error ") || !strings.Contains(p.Outcome, "(infrastructure: checkout)") {
			t.Fatalf("%s: outcome = %q", name, p.Outcome)
		}
		if p.Workflow == nil || p.Workflow.State != workflowFailed {
			t.Fatalf("%s: workflow = %+v", name, p.Workflow)
		}
		state, reason := pebblestore.ProjectTaskState(task, p.Outcome, false, false)
		if state != pebblestore.ProjectTaskStateBlockedInfra || !strings.Contains(reason, "rerun the failed steps") {
			t.Fatalf("%s: task state %q reason %q", name, state, reason)
		}
		if !pebblestore.ProjectTaskNeedsAttention(task, p.Outcome, false, false) {
			t.Fatalf("%s: blocked_infra does not need attention", name)
		}
	}
}

// Purpose: a folder repository problem a run step's checkout recorded shows
// on the machine status (swarm_status, whose box summary a linked machine's
// machine_status reads) as "folder repository <folder> has a corrupt
// object <id>" and on get_environment as repository_health, only to callers
// who can see the folder, and both clear once a checkout reads every object.
// Threat: a folder repository problem invisible outside one step's log, a
// warning that never clears, or a folder path disclosed across accounts.
// Owners: handleBoxSummary (visibleRepositoryHealth), putRepositoryHealth
// (GetSandboxEnvironmentLog) and jobautomation.Store.RecordRepositoryCheck;
// real routes over a temporary store are the narrowest boundary proving the
// surfaces and the account filter.
func TestFolderRepositoryHealthOnStatusAndEnvironment(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "health"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := jobautomation.NewStore(db)
	s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: store}))
	s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(db))
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	scopes, _ := aiKeyScopes("full")
	_, token, err := s.security.CreateScopedTokenUnder(nil, "repository-health", scopes, actor.AccountScopeID, actor.UserID, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	req := requestWithScopedToken(requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/mcp", nil), actor.UserID, actor.AccountScopeID), &token)
	principal, _ := PrincipalFromRequest(req)
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "base")
	if _, err := s.workspace.AddForPrincipal(principal, folder, "folder", "", false); err != nil {
		t.Fatal(err)
	}
	object := strings.Repeat("d1", 20)
	if err := store.RecordRepositoryCheck(folder, &jobautomation.RepositoryCheck{Object: object, Problem: jobautomation.RepositoryObjectCorrupt, Detail: "its bytes do not inflate to the object (corrupt)"}); err != nil {
		t.Fatal(err)
	}
	c := &controlMCPCall{request: req, next: s.internalRouteHandler(), server: s}
	status := func() string {
		t.Helper()
		value, err := controlMCPStatus(c, map[string]any{})
		result := controlMCPToolResult(value, err)
		if err != nil || result["isError"] == true {
			t.Fatalf("status: %v %v", result, err)
		}
		return result["content"].([]map[string]any)[0]["text"].(string)
	}
	want := "folder repository " + folder + " has a corrupt object " + object
	if got := status(); !strings.Contains(got, want) || strings.Contains(got, `"status":"ok"`) {
		t.Fatalf("status lacks %q:\n%s", want, got)
	}
	view := map[string]any{}
	s.putRepositoryHealth(view, folder)
	health, _ := view["repository_health"].(map[string]any)
	if health == nil || health["object"] != object || health["problem"] != jobautomation.RepositoryObjectCorrupt || !strings.HasPrefix(health["message"].(string), want) {
		t.Fatalf("environment view = %+v", view)
	}

	// Another account sees neither the folder nor the object.
	readScopes, _ := aiKeyScopes("read")
	_, otherToken, err := s.security.CreateScopedTokenUnder(nil, "other-account", readScopes, "other-account", "other-user", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	own := c.request
	c.request = requestWithScopedToken(requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/mcp", nil), "other-user", "other-account"), &otherToken)
	if got := status(); strings.Contains(got, folder) || strings.Contains(got, object) {
		t.Fatalf("cross-account disclosure: %s", got)
	}
	c.request = own

	// A clean checkout clears it everywhere.
	if err := store.RecordRepositoryCheck(folder, &jobautomation.RepositoryCheck{Clean: true}); err != nil {
		t.Fatal(err)
	}
	if got := status(); strings.Contains(got, object) {
		t.Fatalf("status still names the object after a clean checkout: %s", got)
	}
	cleared := map[string]any{}
	s.putRepositoryHealth(cleared, folder)
	if _, ok := cleared["repository_health"]; ok {
		t.Fatalf("environment view still has repository_health: %+v", cleared)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(status()), &decoded); err != nil {
		t.Fatal(err)
	}

	// A checkout git refused without naming an object: the failing git
	// command and git's whole error, on both surfaces.
	gitErr := "fatal: Could not read from remote repository. | Please make sure you have the correct access rights | and the repository exists."
	if err := store.RecordRepositoryCheck(folder, &jobautomation.RepositoryCheck{Problem: jobautomation.RepositoryCheckoutFailed, Command: "git clone --no-checkout --shared " + folder, Detail: gitErr}); err != nil {
		t.Fatal(err)
	}
	if got := status(); !strings.Contains(got, "could not be checked out by a run step: git clone --no-checkout --shared") || !strings.Contains(got, "and the repository exists.") {
		t.Fatalf("status lacks the checkout failure:\n%s", got)
	}
	failedView := map[string]any{}
	s.putRepositoryHealth(failedView, folder)
	health, _ = failedView["repository_health"].(map[string]any)
	if health == nil || health["problem"] != jobautomation.RepositoryCheckoutFailed || health["git_command"] != "git clone --no-checkout --shared "+folder || health["detail"] != gitErr {
		t.Fatalf("environment view = %+v", failedView)
	}
	if _, ok := health["object"]; ok {
		t.Fatalf("a checkout failure names an object: %+v", health)
	}
	if err := store.RecordRepositoryCheck(folder, &jobautomation.RepositoryCheck{Clean: true}); err != nil {
		t.Fatal(err)
	}

	// The object store permission repair: one that made entries readable
	// shows as repository_health.permissions; one that left entries it
	// could not repair is a status reason too, only for this account.
	if err := store.RecordObjectStoreRepair(jobautomation.ObjectStoreRepair{Folder: folder, Checked: 9, Repaired: 4}); err != nil {
		t.Fatal(err)
	}
	repairView := map[string]any{}
	s.putRepositoryHealth(repairView, folder)
	health, _ = repairView["repository_health"].(map[string]any)
	permissions, _ := health["permissions"].(map[string]any)
	if permissions == nil || permissions["repaired"] != 4 || permissions["unrepaired"] != 0 {
		t.Fatalf("environment view = %+v", repairView)
	}
	if got := status(); strings.Contains(got, "could not repair") {
		t.Fatalf("a successful repair is a status reason: %s", got)
	}
	if err := store.RecordObjectStoreRepair(jobautomation.ObjectStoreRepair{Folder: folder, Checked: 9, Unrepaired: 2, Sample: "objects/ab/cd: mode -r--r----- owner 1:2, not Swarm's to change"}); err != nil {
		t.Fatal(err)
	}
	wantRepair := "folder repository " + folder + " has 2 object store entries run steps cannot read that Swarm could not repair"
	if got := status(); !strings.Contains(got, wantRepair) {
		t.Fatalf("status lacks %q:\n%s", wantRepair, got)
	}
	problemView := map[string]any{}
	s.putRepositoryHealth(problemView, folder)
	if health, _ := problemView["repository_health"].(map[string]any); health == nil || !strings.HasPrefix(health["message"].(string), wantRepair) {
		t.Fatalf("environment view = %+v", problemView)
	}
	c.request = requestWithScopedToken(requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/mcp", nil), "other-user", "other-account"), &otherToken)
	if got := status(); strings.Contains(got, folder) {
		t.Fatalf("cross-account disclosure of the repair: %s", got)
	}
	c.request = own
}
