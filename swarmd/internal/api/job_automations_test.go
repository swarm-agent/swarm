package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

// These tests drive job automations from real job ends: a media job's
// media.job.* signal (signalMediaSettled) and a design job's design.job.*
// signal (DesignJobEndSignal), through the daemon's result source
// (JobAutomationResults over the shared job result builder and
// designJobView) into the real Runner and store. The sandbox is the stubbed
// host boundary (automationHostSandbox runs the folder's script on the host
// with only the step's environment); this machine has neither bubblewrap
// nor docker.

type automationHostSandbox struct{ ran []jobautomation.RunRequest }

func (h *automationHostSandbox) Run(ctx context.Context, req jobautomation.RunRequest) (jobautomation.RunOutcome, error) {
	h.ran = append(h.ran, req)
	if err := jobautomation.ValidCommandPath(req.Command); err != nil {
		return jobautomation.RunOutcome{}, &jobautomation.RefusedError{Reason: err.Error()}
	}
	cmd := exec.CommandContext(ctx, filepath.Join(req.Folder, req.Command), req.Args...)
	cmd.Dir = req.Folder
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = bytes.NewReader(req.Stdin)
	out, err := cmd.CombinedOutput()
	if req.OnOutput != nil {
		req.OnOutput(out)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return jobautomation.RunOutcome{ExitCode: exit.ExitCode(), Output: out}, nil
	}
	return jobautomation.RunOutcome{Output: out}, err
}

func writeAutomationScript(t *testing.T, folder, name string) {
	t.Helper()
	body := "#!/bin/sh\ncat > \"$PWD/result.json\"\necho \"done $SWARM_JOB_ID\"\n"
	if err := os.WriteFile(filepath.Join(folder, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func newAutomationRunner(t *testing.T, server *Server, store *pebblestore.Store, feed *pebblestore.SignalStore, account string, sandbox jobautomation.RunExecutor) *jobautomation.Runner {
	t.Helper()
	runner := jobautomation.NewRunner(jobautomation.Options{
		Store: jobautomation.NewStore(store), Feed: feed, Account: func() string { return account }, Results: server.JobAutomationResults(),
		Run: sandbox, Agent: server.JobAutomationAgents(), Emit: signals.NewEmitter(feed).Emit,
	})
	server.SetJobAutomations(runner)
	if err := runner.ProcessOnce(context.Background()); err != nil { // first start: cursor at the feed's end
		t.Fatal(err)
	}
	return runner
}

// Requirement: a media job's end produces one result (its files with path,
// sha256 and type, its brief and folder) and triggers the automation set for
// its folder, whose run step receives that result.
// Threat: media jobs silently never reaching their automations (no result,
// wrong folder), or a result whose file hashes do not match the files.
// Owners: signalMediaSettled, JobAutomationResults (mediaJobAutomationResult,
// mediaJobFiles) and jobautomation.Runner.
func TestMediaJobEndProducesAResultAndTriggersItsAutomation(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "media-automation.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	feed := pebblestore.NewSignalStore(store)
	server := &Server{sessions: sessionruntime.NewService(pebblestore.NewSessionStore(store), events), signals: feed}
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-a", AccountScopeID: "account-a"}
	folder := t.TempDir()
	writeAutomationScript(t, folder, "upload.sh")
	sandbox := &automationHostSandbox{}
	runner := newAutomationRunner(t, server, store, feed, p.AccountScopeID, sandbox)
	if _, _, err := runner.Store().Put(p.AccountScopeID, jobautomation.Automation{Name: "upload", Scope: jobautomation.Scope{Folder: folder}, Enabled: true,
		Steps: []jobautomation.Step{{ID: "upload", Type: jobautomation.StepRun, Command: "upload.sh"}}}); err != nil {
		t.Fatal(err)
	}
	project := &pebblestore.ProjectRecord{ID: "proj-media", Name: "Site"}
	if err := server.sessions.Store().PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\nautomation-media")
	task := &pebblestore.ProjectTaskRecord{ID: "task-media-1", ProjectID: project.ID, AccountID: p.AccountScopeID, Title: "Logo", Description: "a round logo",
		Status: "needs_review", Agent: "image", Model: "image-model", WorkspacePath: folder, SourceWorkspace: pebblestore.ProjectTaskSource{WorkspaceID: "ws-1", WorkspaceGeneration: 1, Path: folder, Provenance: "registered"},
		Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "d1", Title: "Logo 1", Kind: "artifact", Status: "ready", MediaURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)}}}
	if err := server.sessions.Store().PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	server.signalMediaSettled(p, project.ID, task.ID)
	if err := runner.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	list, err := runner.Store().JobDeliveries(p.AccountScopeID, "media/proj-media/task-media-1")
	if err != nil || len(list) != 1 {
		t.Fatalf("deliveries = %d %v", len(list), err)
	}
	d := list[0]
	if d.State != jobautomation.StateSucceeded || d.Steps[0].Output != "done media/proj-media/task-media-1" {
		t.Fatalf("delivery = %+v", d)
	}
	var result jobautomation.Result
	data, err := os.ReadFile(filepath.Join(folder, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(png)
	if result.Kind != jobautomation.KindMedia || result.Status != jobautomation.StatusSucceeded || result.Folder != folder || result.Brief != "a round logo" ||
		len(result.Files) != 1 || result.Files[0].SHA256 != hex.EncodeToString(sum[:]) || result.Files[0].Type != "image/png" || result.Files[0].Path != "logo-1.png" {
		t.Fatalf("media result = %+v", result)
	}
}

func newDesignAutomationFixture(t *testing.T) (*Server, *pebblestore.Store, *controlMCPCall) {
	t.Helper()
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	store := sessions.Store().Underlying()
	server.SetSignalStore(pebblestore.NewSignalStore(store))
	req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)),
		&pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read", "sessions:write", "automations:read", "automations:write", "swarm:read"}})
	call := &controlMCPCall{request: req, next: server.internalRouteHandler(), server: server}
	server.jobAutomationCallFn = func(context.Context) (*controlMCPCall, error) { return call, nil }
	return server, store, call
}

// Requirement: a design job's end (design.job.completed) produces one result
// read through designJobView (status, cost) with the files it wrote (path and
// sha256 from the job record) and triggers the folder's automation.
// Threat: design jobs that end without a signal or a result never reaching
// the person's steps.
// Owners: DesignJobEndSignal, JobAutomationResults (designJobAutomationResult
// over designJobView) and jobautomation.Runner.
func TestDesignJobEndProducesAResultAndTriggersItsAutomation(t *testing.T) {
	server, store, _ := newDesignAutomationFixture(t)
	p := testPrincipal()
	folder := t.TempDir()
	writeAutomationScript(t, folder, "publish.sh")
	runner := newAutomationRunner(t, server, store, server.signals, p.AccountScopeID, &automationHostSandbox{})
	if _, _, err := runner.Store().Put(p.AccountScopeID, jobautomation.Automation{Name: "publish designs", Scope: jobautomation.Scope{Folder: folder}, Enabled: true,
		Steps: []jobautomation.Step{{ID: "publish", Type: jobautomation.StepRun, Command: "publish.sh"}}}); err != nil {
		t.Fatal(err)
	}
	project := &pebblestore.ProjectRecord{ID: "proj-design", Name: "Designs"}
	if err := server.sessions.Store().PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("c", 64)
	job := &pebblestore.ProjectTaskDesignJob{Mode: "create", Brief: "a pricing page", Count: 1, Folder: "designs/pricing-abc123", ResultsPath: "designs/pricing-abc123/results.json",
		State: pebblestore.DesignJobCompleted, Provider: "fixture", Model: "designer-model", Tokens: 900, CostUSD: 0.01, StartedAt: 1000, FinishedAt: 2000,
		Items: []pebblestore.ProjectTaskDesignItem{{N: 1, Path: "designs/pricing-abc123/01.html", SHA256: sha, Status: "passed"}}}
	task := &pebblestore.ProjectTaskRecord{ID: "task_design01", ProjectID: project.ID, AccountID: p.AccountScopeID, Title: "Designs: pricing", Description: "a pricing page",
		Status: "completed", Agent: "designer", OutcomeType: "ui_design", WorkspacePath: folder, SourceWorkspace: pebblestore.ProjectTaskSource{WorkspaceID: "ws-1", WorkspaceGeneration: 1, Path: folder, Provenance: "registered"}, Design: job}
	if err := server.sessions.Store().PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	stored, _, err := server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	sig, ok := DesignJobEndSignal(*stored)
	if !ok || sig.Kind != "design.job.completed" {
		t.Fatalf("design signal = %+v %v", sig, ok)
	}
	if _, err := server.signals.AppendOnce(sig); err != nil {
		t.Fatal(err)
	}
	if err := runner.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	list, err := runner.Store().JobDeliveries(p.AccountScopeID, "design/proj-design/task_design01")
	if err != nil || len(list) != 1 || list[0].State != jobautomation.StateSucceeded {
		t.Fatalf("deliveries = %+v %v", list, err)
	}
	result := list[0].Result
	if result.Kind != jobautomation.KindDesign || result.Status != jobautomation.StatusSucceeded || result.Folder != folder || result.Brief != "a pricing page" ||
		len(result.Files) != 1 || result.Files[0].Path != "designs/pricing-abc123/01.html" || result.Files[0].SHA256 != sha || result.Cost == nil || result.Cost.Tokens != 900 {
		t.Fatalf("design result = %+v", result)
	}
}

// Requirement: with no automation, swarm_get_job shows no automations and
// swarm_deliver_job merges exactly as before (the same routes, no automation
// state touched); only automation: true re-runs, and it refuses a job
// without deliveries without merging anything.
// Threat: automations changing the hand-integration default, or a re-run
// request falling through to a merge.
// Owners: controlMCPGetJob, controlMCPDeliverJob and rerunJobAutomations.
func TestDeliverJobWithoutAutomationsIsUnchanged(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "deliver.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := &Server{}
	server.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: jobautomation.NewStore(store)}))
	var routes []string
	c := &controlMCPCall{server: server, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)),
		&pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read", "sessions:write", "automations:read", "automations:write"}})}
	c.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routes = append(routes, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/v3/workers/wkr-1/runs/run-1":
			writeJSON(w, 200, map[string]any{"run": map[string]any{"id": "run-1", "status": "succeeded", "role": "coder", "review": map[string]any{"project_id": "proj-1", "task_id": "task-1"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects/proj-1/tasks":
			writeJSON(w, 200, map[string]any{"tasks": []any{map[string]any{"id": "task-1", "session_id": "sess-1", "worktree_branch": "agent/run-1", "base_branch": "main"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects/proj-1/tasks/task-1":
			writeJSON(w, 200, map[string]any{"task": map[string]any{"id": "task-1", "status": "needs_review"}})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v3/projects/proj-1/tasks/task-1/"):
			writeJSON(w, 200, map[string]any{"task": map[string]any{"id": "task-1"}, "integration": map[string]any{"state": "integrated", "resulting_target_head": strings.Repeat("d", 40)}})
		default:
			writeJSON(w, 200, map[string]any{})
		}
	})

	if _, err := controlMCPDeliverJob(c, map[string]any{"job_id": "worker/wkr-1/run-1", "automation": true}); err == nil || !strings.Contains(err.Error(), "no automation deliveries") {
		t.Fatalf("automation re-run without deliveries = %v", err)
	}
	if len(routes) != 0 {
		t.Fatalf("a refused automation re-run reached Swarm: %v", routes)
	}
	if deliveries, ok := c.jobDeliveries("worker/wkr-1/run-1"); ok || deliveries != nil {
		t.Fatalf("a job without automations lists deliveries: %v", deliveries)
	}
	// The fixture has no session store, so recording the delivered job's
	// activity receipt after the merge fails; the merge itself is what this
	// test checks.
	if _, err := controlMCPDeliverJob(c, map[string]any{"job_id": "worker/wkr-1/run-1"}); err != nil && !strings.Contains(err.Error(), "activity receipt requires an owned session") {
		t.Fatalf("deliver = %v", err)
	}
	merged := false
	for _, route := range routes {
		if strings.HasPrefix(route, "POST /v3/projects/proj-1/tasks/task-1/") {
			merged = true
		}
		if strings.Contains(route, "job-automations") || strings.Contains(route, "job-deliveries") {
			t.Fatalf("a hand delivery touched automations: %v", routes)
		}
	}
	if !merged {
		t.Fatalf("swarm_deliver_job without automation did not reach the merge route: %v", routes)
	}
	if list, _ := server.jobAutomations.Store().List(testPrincipal().AccountScopeID); len(list) != 0 {
		t.Fatalf("a hand delivery created automations: %v", list)
	}
}

// Requirement: an agent step starts its job through
// WorkerExecutionService.Dispatch (the workspace and worker checks) with
// request source "automation", the step's brief with the triggering job's
// result attached, the job's project and the folder's workspace; a reviewer
// worker gets the triggering job as its review_job. The automation's job is
// what the loop guard recognises (its worker.run signal carries source
// automation; Runner.consume ignores it, see the jobautomation tests).
// Threat: an agent step that bypasses admission checks, or a job that cannot
// be told apart from a person's and so re-triggers automations.
// Owners: jobAutomationAgents.Start, WorkerExecutionService.Dispatch and
// WorkerStore.AdmitWorkerRun (which must accept the automation source).
func TestAgentStepAdmitsAnAutomationJobThroughDispatch(t *testing.T) {
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	store := sessions.Store().Underlying()
	principal := testPrincipal()
	ids := pebblestore.NewIdentityStore(store)
	if _, err := ids.PutUser(principal.User); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.PutAccountScope(principal.AccountScope); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.PutAccountUser(principal.AccountUser); err != nil {
		t.Fatal(err)
	}
	// The catalog holds the models the seeded agent-model settings name, so
	// the worker's role model resolves at admission.
	catalog := pebblestore.NewModelCatalogStore(store)
	for _, model := range []string{"action", "plan", "compact", "finder", "coder", "designer", "router"} {
		if err := catalog.SetRecord(pebblestore.ModelCatalogRecord{Provider: "codex", Model: model, ThinkingOptions: []string{"high"}}); err != nil {
			t.Fatal(err)
		}
	}
	workspaces := workspace.NewService(pebblestore.NewWorkspaceStore(store))
	server.workspace = workspaces
	runner := server.runner.(*runruntime.Service)
	runner.SetWorkspaceService(workspaces)
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "main")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
	added, err := workspaces.AddForPrincipal(principal, folder, "site", "", false)
	if err != nil {
		t.Fatal(err)
	}
	project := &pebblestore.ProjectRecord{ID: "proj-agent", Name: "Site", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: added.WorkspaceID, Path: added.WorkspacePath, Role: "primary_code"}}}
	if err := sessions.Store().PutProject(principal.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	execution, err := runruntime.NewWorkerExecutionService(runner, sessions.Store(), &worktree.Service{}, func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		return sessionruntime.SessionMutationResult{}, errors.New("this test stops at admission")
	}, func(identity.Principal, pebblestore.V3SessionRunIntent) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	runner.SetWorkerExecutionService(execution)
	req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)),
		&pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read", "sessions:write", "automations:read", "automations:write"}})
	call := &controlMCPCall{request: req, next: server.internalRouteHandler(), server: server}
	server.jobAutomationCallFn = func(context.Context) (*controlMCPCall, error) { return call, nil }
	if _, err := controlMCPDeployWorker(call, map[string]any{"preset": "finder"}); err != nil {
		t.Fatal(err)
	}

	triggering := jobautomation.Result{V: 1, JobID: "worker/coder-1/run-1", Kind: jobautomation.KindWorker, ProjectID: project.ID, Folder: added.WorkspacePath,
		Status: jobautomation.StatusSucceeded, Report: "Added the logo."}
	agents := server.JobAutomationAgents()
	jobID, startErr := agents.Start(context.Background(), jobautomation.AgentRequest{Account: principal.AccountScopeID, Folder: added.WorkspacePath, Worker: "finder",
		Brief: "Check the new logo is used everywhere.", Result: triggering, IdempotencyKey: "jdel_test.check.0"})

	workers, err := sessions.Store().WorkerStore().ListWorkers(principal.AccountScopeID, pebblestore.ListWorkersQuery{Limit: 10})
	if err != nil || len(workers.Workers) != 1 {
		t.Fatalf("workers = %v %v", workers, err)
	}
	runs, _, err := sessions.Store().WorkerStore().ListWorkerRuns(principal.AccountScopeID, workers.Workers[0].ID, 10, "")
	if err != nil || len(runs) != 1 {
		t.Fatalf("admitted runs = %d %v (start: %q %v)", len(runs), err, jobID, startErr)
	}
	run := runs[0]
	prompt, _ := run.Input["prompt"].(string)
	if run.RequestSource != jobautomation.RequestSourceAutomation || run.Input["project_id"] != project.ID || run.WorkspaceID != added.WorkspaceID ||
		!strings.HasPrefix(prompt, "Check the new logo is used everywhere.") || !strings.Contains(prompt, `"job_id": "worker/coder-1/run-1"`) {
		t.Fatalf("admitted run = source %q project %v workspace %q prompt %q", run.RequestSource, run.Input["project_id"], run.WorkspaceID, prompt)
	}
	if _, ok := run.Input["review_job"]; ok {
		t.Fatal("a non-reviewer job got a review_job")
	}
	if startErr == nil && jobID != controlMCPJobID(controlMCPJobWorker, run.WorkerID, run.ID) {
		t.Fatalf("job id = %q", jobID)
	}
	// The same step again (a restart) reuses the admitted job.
	if _, err := agents.Start(context.Background(), jobautomation.AgentRequest{Account: principal.AccountScopeID, Folder: added.WorkspacePath, Worker: "finder",
		Brief: "Check the new logo is used everywhere.", Result: triggering, IdempotencyKey: "jdel_test.check.0"}); err != nil && startErr == nil {
		t.Fatalf("replayed start = %v", err)
	}
	if again, _, _ := sessions.Store().WorkerStore().ListWorkerRuns(principal.AccountScopeID, workers.Workers[0].ID, 10, ""); len(again) != 1 {
		t.Fatalf("a replayed agent step admitted %d jobs", len(again))
	}
	// Another account's automation cannot start a job as the owner.
	if _, err := agents.Start(context.Background(), jobautomation.AgentRequest{Account: "other-account", Folder: added.WorkspacePath, Worker: "finder", Brief: "x",
		Result: triggering, IdempotencyKey: "jdel_other.check.0"}); err == nil {
		t.Fatal("an agent step ran for another account")
	}
}

// Requirement: /v3/job-automations needs automations:write to change and
// automations:read to read; definitions are the caller's account's only;
// an invalid definition is refused with 400 and nothing stored.
// Threat: a read-only key setting a script to run, or one account reading or
// deleting another's automations.
// Owners: handleJobAutomations, handleJobDeliveries and jobautomation.Store.
func TestJobAutomationRoutesEnforceScopesAndAccounts(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "routes.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := &Server{}
	server.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: jobautomation.NewStore(store)}))
	do := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		if strings.HasPrefix(r.URL.Path, JobDeliveriesPath) {
			server.handleJobDeliveries(w, r)
		} else {
			server.handleJobAutomations(w, r)
		}
		return w
	}
	body := `{"name":"push","scope":{"folder":"/srv/site"},"steps":[{"type":"run","command":"deliver/push.sh"}]}`
	owner := func(method, path, body string, scopes ...string) *http.Request {
		r := withTestPrincipal(httptest.NewRequest(method, path, strings.NewReader(body)))
		return requestWithScopedToken(r, &pebblestore.ScopedTokenRecord{Scopes: scopes})
	}
	if w := do(owner(http.MethodPost, JobAutomationsPath, body, "automations:read")); w.Code != http.StatusForbidden {
		t.Fatalf("read-only create = %d %s", w.Code, w.Body)
	}
	if w := do(owner(http.MethodPost, JobAutomationsPath, `{"name":"bad","scope":{"folder":"/srv/site"},"steps":[{"type":"run","command":"../x.sh"}]}`, "automations:write")); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid create = %d %s", w.Code, w.Body)
	}
	w := do(owner(http.MethodPost, JobAutomationsPath, body, "automations:write"))
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body)
	}
	var created struct {
		Automation struct {
			ID       string `json:"id"`
			Revision uint64 `json:"revision"`
		} `json:"automation"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Automation.ID == "" || created.Automation.Revision != 1 {
		t.Fatalf("created = %s %v", w.Body, err)
	}
	other := requestWithScopedToken(requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, JobAutomationsPath, nil), "other-user", "other-account"),
		&pebblestore.ScopedTokenRecord{Scopes: []string{"automations:read", "automations:write"}})
	if w := do(other); w.Code != http.StatusOK || strings.Contains(w.Body.String(), created.Automation.ID) {
		t.Fatalf("other account list = %d %s", w.Code, w.Body)
	}
	otherDelete := requestWithScopedToken(requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodDelete, JobAutomationsPath+"/"+created.Automation.ID, nil), "other-user", "other-account"),
		&pebblestore.ScopedTokenRecord{Scopes: []string{"automations:write"}})
	if w := do(otherDelete); w.Code != http.StatusNotFound {
		t.Fatalf("other account delete = %d %s", w.Code, w.Body)
	}
	if w := do(owner(http.MethodPost, JobAutomationsPath+"/stop", `{"stopped":true}`, "automations:read")); w.Code != http.StatusForbidden {
		t.Fatalf("read-only stop = %d", w.Code)
	}
	if w := do(owner(http.MethodPost, JobAutomationsPath+"/stop", `{"stopped":true}`, "automations:write")); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"stopped":true`) {
		t.Fatalf("stop = %d %s", w.Code, w.Body)
	}
	if w := do(owner(http.MethodGet, JobDeliveriesPath+"?job_id=worker/w/r", "", "automations:read")); w.Code != http.StatusOK {
		t.Fatalf("deliveries = %d %s", w.Code, w.Body)
	}
	if w := do(owner(http.MethodDelete, JobAutomationsPath+"/"+created.Automation.ID, "", "automations:write")); w.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", w.Code, w.Body)
	}
}
