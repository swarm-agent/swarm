package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

// Purpose: a delivery stopped by a run step whose script never started
// reads as an infrastructure error on every task surface: the outcome is
// "error <step> (infrastructure: <kind>)", the task state reason says it is
// an infrastructure problem and not the work's, the summary carries the
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
	if state != pebblestore.ProjectTaskStateNeedsYou || !strings.Contains(reason, "an infrastructure problem, not the work") || !strings.Contains(reason, "nothing for a coder to fix") {
		t.Fatalf("task state %q reason %q", state, reason)
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
	if _, reason := pebblestore.ProjectTaskState(task, fp.Outcome, false, false); strings.Contains(reason, "infrastructure") {
		t.Fatalf("a failed check is named an infrastructure problem: %q", reason)
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
}
