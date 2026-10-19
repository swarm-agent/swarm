package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/design"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type designFactoryCounter struct{ calls int }

func (f *designFactoryCounter) DesignModel(identity.Principal, string, string) (design.Model, error) {
	f.calls++
	return nil, errors.New("not used")
}

type designNoopChecker struct{}

func (designNoopChecker) Check(context.Context, []byte) design.CheckReport {
	return design.CheckReport{Status: design.CheckOK}
}

func newDesignJobTestServer(t *testing.T) (*Server, *pebblestore.SessionStore, *designFactoryCounter, identity.Principal) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "design-jobs-api.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{sessions: sessionruntime.NewService(pebblestore.NewSessionStore(store), events)}
	factory := &designFactoryCounter{}
	s.SetDesignJobs(designNoopChecker{}, factory)
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-a", AccountScopeID: "account-a"}
	return s, s.sessions.Store(), factory, p
}

// Requirement: the design-jobs route refuses a bad count, a path outside the
// workspace and an invalid edit_path before it resolves a model, creates a
// receipt session or stores a task.
// Threat: a malformed or escaping request spends money, writes outside the
// workspace or leaves a half-created job.
// Owners: handleProjectDesignJobs, createDesignJob and
// normalizeDesignJobRequest (design.Validate, pebblestore.ValidDesignRelativePath).
// The handler with a real store is the narrowest layer that proves both the
// rejection and that nothing was created; a counting model factory proves no
// model was resolved.
func TestDesignJobCreateRejectsInvalidRequestsBeforeAnyWrite(t *testing.T) {
	s, store, factory, p := newDesignJobTestServer(t)
	cases := map[string]string{
		"count too high":      `{"brief":"pricing page","count":51}`,
		"negative count":      `{"brief":"pricing page","count":-1}`,
		"empty brief":         `{"brief":"  "}`,
		"folder escape":       `{"brief":"pricing page","folder":"../outside"}`,
		"folder nested up":    `{"brief":"pricing page","folder":"a/../../outside"}`,
		"absolute folder":     `{"brief":"pricing page","folder":"/etc"}`,
		"root folder":         `{"brief":"pricing page","folder":"/"}`,
		"git folder":          `{"brief":"pricing page","folder":".git/hooks"}`,
		"edit escape":         `{"brief":"make it blue","edit_path":"../page.html"}`,
		"edit absolute":       `{"brief":"make it blue","edit_path":"/srv/page.html"}`,
		"edit not html":       `{"brief":"make it blue","edit_path":"notes.txt"}`,
		"edit count":          `{"brief":"make it blue","edit_path":"page.html","count":2}`,
		"edit with folder":    `{"brief":"make it blue","edit_path":"page.html","folder":"designs"}`,
		"unknown field":       `{"brief":"pricing page","model":"any"}`,
		"too many variations": `{"brief":"pricing page","variations":[` + strings.TrimSuffix(strings.Repeat(`"v",`, 51), ",") + `]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v3/projects/project-a/design-jobs", strings.NewReader(body))
			s.handleProjectDesignJobs(rec, req, p, "project-a", nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
	tasks, err := store.ListProjectTasks(p.AccountScopeID, "project-a", 100)
	if err != nil || len(tasks) != 0 || factory.calls != 0 {
		t.Fatalf("a rejected request created state: tasks=%d model resolutions=%d err=%v", len(tasks), factory.calls, err)
	}

	// A read-only key is refused before the body is read.
	rec := httptest.NewRecorder()
	req := requestWithScopedToken(httptest.NewRequest(http.MethodPost, "/v3/projects/project-a/design-jobs", strings.NewReader(`{"brief":"x"}`)), &pebblestore.ScopedTokenRecord{Scopes: []string{"projects:read", "sessions:read"}})
	s.handleProjectDesignJobs(rec, req, p, "project-a", nil)
	if rec.Code != http.StatusForbidden || factory.calls != 0 {
		t.Fatalf("read-only key: status %d", rec.Code)
	}
}

func TestNormalizeDesignJobRequestDefaults(t *testing.T) {
	// Requirement: an omitted count is one design and an omitted folder is the
	// designs folder; an edit is one file with no batch folder. Threat: a
	// default that spends more than asked or writes elsewhere. Owner:
	// normalizeDesignJobRequest; a pure function is the narrowest layer.
	in, req, err := normalizeDesignJobRequest(designJobCreateRequest{Brief: " pricing page "})
	if err != nil || req.Count != 1 || in.Folder != design.DefaultFolder || req.Mode() != design.ModeCreate || req.Brief != "pricing page" {
		t.Fatalf("create defaults: %#v %v", req, err)
	}
	_, req, err = normalizeDesignJobRequest(designJobCreateRequest{Brief: "blue", EditPath: "site/index.html"})
	if err != nil || req.Count != 1 || req.Folder != "" || req.Mode() != design.ModeEdit {
		t.Fatalf("edit defaults: %#v %v", req, err)
	}
	if got := designJobSlug("  50 Pricing pages / dark!  "); got != "50-pricing-pages-dark" {
		t.Fatalf("slug %q", got)
	}
	if got := designJobSlug("日本語"); got != "design" {
		t.Fatalf("non-ASCII slug %q", got)
	}
}

func putDesignTask(t *testing.T, store *pebblestore.SessionStore, p identity.Principal, state string) *pebblestore.ProjectTaskRecord {
	t.Helper()
	job := &pebblestore.ProjectTaskDesignJob{Mode: "create", Brief: "pricing page", Count: 2, Folder: "designs/pricing-abc123", ResultsPath: "designs/pricing-abc123/results.json", State: state, StartedAt: 1000}
	status := "in_progress"
	if state != pebblestore.DesignJobRunning {
		job.FinishedAt, status = 2000, state
	}
	task := &pebblestore.ProjectTaskRecord{ID: "task_design01", ProjectID: "project-a", Title: "Designs", Agent: "designer", OutcomeType: "ui_design", Status: status, SessionID: "session-design01", Design: job}
	if err := store.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	return task
}

// Requirement: agent-task lifecycle routes (approve, deploy, edit, reopen,
// refine, integrate, programs) refuse a design job; a running one can only be
// read or cancelled, a settled one may also be archived or deleted.
// Threat: approving or redeploying a design job starts an agent session on it
// or rewrites its durable outcome. Owners: refuseDesignTaskMutation (the
// project task route guard), ApproveProjectTask and DeployProjectTask. The
// guard with a real store is the narrowest layer for the route decision.
func TestDesignTaskRoutesRefuseAgentLifecycle(t *testing.T) {
	s, store, _, p := newDesignJobTestServer(t)
	putDesignTask(t, store, p, pebblestore.DesignJobRunning)
	check := func(method, path string, wantRefused bool) {
		t.Helper()
		segments := strings.Split(strings.Trim(strings.TrimPrefix(path, ProjectsPath), "/"), "/")
		rec := httptest.NewRecorder()
		refused := s.refuseDesignTaskMutation(rec, httptest.NewRequest(method, path, nil), p, "project-a", segments)
		if refused != wantRefused || (refused && rec.Code != http.StatusConflict) {
			t.Fatalf("%s %s: refused=%v code=%d", method, path, refused, rec.Code)
		}
	}
	base := ProjectsPath + "/project-a/tasks/task_design01"
	for _, op := range []string{"approve", "accept", "reject", "reopen", "refine", "complete", "integrate", "program:deploy", "archive"} {
		check(http.MethodPost, base+"/"+op, true)
	}
	check(http.MethodPatch, base, true)
	check(http.MethodDelete, base, true)
	check(http.MethodGet, base, false)
	if _, err := s.ApproveProjectTask(context.Background(), p, "project-a", "task_design01"); !errors.Is(err, errDesignJobNotAgentTask) {
		t.Fatalf("approve: %v", err)
	}

	if _, err := store.UpdateProjectTask(p.AccountScopeID, "project-a", "task_design01", func(task *pebblestore.ProjectTaskRecord) error {
		task.Status, task.Design.State, task.Design.FinishedAt = "completed", pebblestore.DesignJobCompleted, 3000
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	check(http.MethodPost, base+"/archive", false)
	check(http.MethodDelete, base, false)
	check(http.MethodPost, base+"/approve", true)
	check(http.MethodPost, base+"/reopen", true)
	// An ordinary agent task is not affected.
	if err := store.PutProjectTask(p.AccountScopeID, &pebblestore.ProjectTaskRecord{ID: "task_coder01", ProjectID: "project-a", Title: "Fix", Agent: "coder", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	check(http.MethodPost, ProjectsPath+"/project-a/tasks/task_coder01/approve", false)
}

// Requirement: a design job is completed when at least one design was written
// (create) or the file was replaced (edit), failed otherwise, and cancelled
// when stopped. Threat: a job with no output reported as done, or a stopped
// job reported as failed or complete. Owner: finishDesignJob; a pure function
// over the record is the narrowest layer.
func TestFinishDesignJobOutcome(t *testing.T) {
	newTask := func(mode string) *pebblestore.ProjectTaskRecord {
		job := &pebblestore.ProjectTaskDesignJob{Mode: mode, Brief: "pricing page", Count: 2, Folder: "designs/x-abc123", ResultsPath: "designs/x-abc123/results.json", State: pebblestore.DesignJobRunning, StartedAt: 10}
		if mode == "edit" {
			job.Count, job.Folder, job.ResultsPath, job.EditPath = 1, "", "", "page.html"
		}
		return &pebblestore.ProjectTaskRecord{Status: "in_progress", Design: job}
	}
	batch := func(items ...design.ItemResult) design.BatchResult {
		counts := map[design.Status]int{}
		for _, item := range items {
			counts[item.Status]++
		}
		return design.BatchResult{Items: items, Counts: counts}
	}
	cases := []struct {
		name, mode, want string
		result           design.BatchResult
		runErr           error
		stopped          bool
	}{
		{"one written", "create", "completed", batch(design.ItemResult{Index: 1, Status: design.StatusPassed, Path: "designs/x-abc123/01.html"}, design.ItemResult{Index: 2, Status: design.StatusGenerationFailed, Error: "model failed"}), nil, false},
		{"none written", "create", "failed", batch(design.ItemResult{Index: 1, Status: design.StatusGenerationFailed}, design.ItemResult{Index: 2, Status: design.StatusSkippedBudget}), nil, false},
		{"stopped", "create", "cancelled", batch(design.ItemResult{Index: 1, Status: design.StatusPassed, Path: "designs/x-abc123/01.html"}, design.ItemResult{Index: 2, Status: design.StatusCancelled}), nil, true},
		{"run error", "edit", "failed", design.BatchResult{}, errors.New("read page.html: missing"), false},
		{"edit replaced", "edit", "completed", batch(design.ItemResult{Index: 1, Status: design.StatusRepaired, Path: "page.html", SHA256: strings.Repeat("a", 64)}), nil, false},
		{"edit stale", "edit", "failed", batch(design.ItemResult{Index: 1, Status: design.StatusStaleBase, Path: "page.html", Error: "file changed"}), nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := newTask(tc.mode)
			finishDesignJob(task, tc.result, tc.runErr, tc.stopped, 99)
			if task.Status != tc.want || task.Design.State != tc.want || task.Design.FinishedAt != 99 {
				t.Fatalf("got status=%q state=%q", task.Status, task.Design.State)
			}
			if (tc.want == "completed") != (task.Design.Error == "") || (tc.want != "completed" && task.LastError == "") {
				t.Fatalf("error text: %q", task.Design.Error)
			}
			if err := task.Design.Validate(); err != nil {
				t.Fatalf("settled record invalid: %v", err)
			}
		})
	}
}

// Requirement: design jobs are first-class Swarm Control jobs: design/<project>/<task>
// parses, get_job shows each design's status, check findings (bounded) and
// cost and says there is nothing to merge, stop cancels through the design
// route, and message/answer/deliver refuse without touching Swarm.
// Threat: an AI cannot read or stop a design job, is told to merge files that
// are already in the folder, or a follow-up/merge acts on a design job.
// Owners: controlMCPParseJobID, controlMCPGetJob/designJobView, stopDesignJob,
// controlMCPMessageJob, controlMCPAnswerJob and controlMCPDeliverJob. An HTTP
// adapter capture is the narrowest layer for exact routing and views.
func TestControlMCPDesignJobLifecycle(t *testing.T) {
	for _, bad := range []string{"design/project-a", "design/../task", "designs/project-a/task_1", "design/project-a/accept"} {
		if _, _, _, err := controlMCPParseJobID(map[string]any{"job_id": bad}); err == nil {
			t.Errorf("parsed %q", bad)
		}
	}
	kind, a, b, err := controlMCPParseJobID(map[string]any{"job_id": "design/project-a/task_design01"})
	if err != nil || kind != controlMCPJobDesign || a != "project-a" || b != "task_design01" {
		t.Fatalf("parse: %s %s %s %v", kind, a, b, err)
	}

	long := strings.Repeat("é", 300)
	task := map[string]any{"id": "task_design01", "status": "completed", "session_id": "session-design01", "design": map[string]any{
		"mode": "create", "count": 2, "folder": "designs/pricing-abc123", "results_path": "designs/pricing-abc123/results.json", "state": "completed",
		"provider": "fixture", "model": "designer-model", "thinking": "low", "counts": map[string]any{"passed": 1, "failed": 1},
		"calls": 3, "tokens": 900, "cost_usd": 0.01, "cost_complete": true,
		"items": []any{
			map[string]any{"n": 1, "path": "designs/pricing-abc123/01.html", "status": "passed", "check": "ok", "tokens": 300, "cost_usd": 0.003},
			map[string]any{"n": 2, "status": "failed", "check": "errors", "diagnostics": []any{map[string]any{"kind": "js_exception", "message": long}}, "check_after_repair": "errors", "diagnostics_after_repair": []any{map[string]any{"kind": "js_exception", "message": "still broken"}}, "tokens": 600},
		},
	}}
	var writes []string
	c := &controlMCPCall{request: requestWithScopedToken(httptest.NewRequest(http.MethodPost, "/mcp", nil), &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read", "sessions:write"}})}
	c.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects/project-a/tasks/task_design01":
			writeJSON(w, 200, map[string]any{"task": task})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/sessions/session-design01":
			writeJSON(w, 200, map[string]any{"pending_permissions": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/v3/projects/project-a/design-jobs/task_design01/cancel":
			writes = append(writes, r.URL.Path)
			writeJSON(w, http.StatusConflict, map[string]any{"error": "the design job already ended (completed)"})
		default:
			writes = append(writes, r.Method+" "+r.URL.Path)
			writeJSON(w, 404, map[string]any{"error": "unexpected"})
		}
	})
	out, err := controlMCPGetJob(c, map[string]any{"job_id": "design/project-a/task_design01"})
	if err != nil {
		t.Fatal(err)
	}
	view := out.(map[string]any)
	if view["status"] != "completed" || view["next"] != controlMCPDesignNext || view["folder"] != "designs/pricing-abc123" || view["results_path"] != "designs/pricing-abc123/results.json" || view["job_id"] != "design/project-a/task_design01" {
		t.Fatalf("view: %#v", view)
	}
	items := view["items"].([]map[string]any)
	if len(items) != 2 || items[0]["path"] != "designs/pricing-abc123/01.html" {
		t.Fatalf("items: %#v", items)
	}
	check := items[1]["check"].(map[string]any)
	if check["status"] != "errors" || len([]rune(check["message"].(string))) > 200 || items[1]["check_after_repair"].(map[string]any)["message"] != "still broken" {
		t.Fatalf("diagnostics: %#v", items[1])
	}
	if totals := view["totals"].(map[string]any); totals["cost_usd"] != 0.01 || totals["tokens"] != float64(900) {
		t.Fatalf("totals: %#v", totals)
	}

	for name, call := range map[string]func(*controlMCPCall, map[string]any) (any, error){
		"message": controlMCPMessageJob, "answer": controlMCPAnswerJob, "deliver": controlMCPDeliverJob,
	} {
		args := map[string]any{"job_id": "design/project-a/task_design01", "message": "more", "answer": "yes"}
		if _, err := call(c, args); err == nil || !strings.Contains(err.Error(), "design job") {
			t.Fatalf("%s did not refuse: %v", name, err)
		}
	}
	if len(writes) != 0 {
		t.Fatalf("refused tools reached Swarm: %v", writes)
	}
	stopped, err := controlMCPStopJob(c, map[string]any{"job_id": "design/project-a/task_design01"})
	if err != nil || stopped.(map[string]any)["stopped"] != false || len(writes) != 1 || writes[0] != "/v3/projects/project-a/design-jobs/task_design01/cancel" {
		t.Fatalf("stop: %#v %v %v", stopped, err, writes)
	}
}

// Requirement: swarm_design sends exactly the design request to the project's
// folder and returns the design job. Threat: the tool reaches another route,
// drops the person's options, or works in an unnamed folder. Owners:
// controlMCPDesign, place and the route allowlist (controlMCPRouteAllowed).
func TestControlMCPDesignCreatesDesignJob(t *testing.T) {
	var captured map[string]any
	c := &controlMCPCall{request: requestWithScopedToken(httptest.NewRequest(http.MethodPost, "/mcp", nil), &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read", "sessions:write"}})}
	c.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects":
			writeJSON(w, 200, map[string]any{"projects": []any{map[string]any{"id": "project-a", "name": "Site", "workspaces": []any{map[string]any{"path": "/work/site", "workspace_id": "ws-1"}}}}})
		case r.Method == http.MethodPost && r.URL.Path == "/v3/projects/project-a/design-jobs":
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Fatal(err)
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"task": map[string]any{"id": "task_design01"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects/project-a/tasks/task_design01":
			writeJSON(w, 200, map[string]any{"task": map[string]any{"id": "task_design01", "status": "in_progress", "design": map[string]any{"mode": "create", "count": 3, "state": "running"}}})
		default:
			t.Fatalf("unexpected route %s %s", r.Method, r.URL.Path)
		}
	})
	args := map[string]any{"brief": "pricing page", "count": float64(3), "style": "dark", "variations": []any{"minimal", "bold"}, "batch_name": "Pricing"}
	var definition controlMCPTool
	for _, tool := range controlMCPTools() {
		if tool.Name == "swarm_design" {
			definition = tool
		}
	}
	if definition.call == nil || !controlMCPCoreTools["swarm_design"] {
		t.Fatal("swarm_design is not a registered core tool")
	}
	if err := controlMCPValidateArgs(definition.InputSchema, args); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string]any{{"brief": "x", "count": float64(51)}, {"brief": "x", "count": float64(0)}, {"count": float64(2)}} {
		if err := controlMCPValidateArgs(definition.InputSchema, bad); err == nil {
			t.Fatalf("schema accepted %v", bad)
		}
	}
	out, err := controlMCPDesign(c, args)
	if err != nil {
		t.Fatal(err)
	}
	if captured["workspace"] != "/work/site" || captured["brief"] != "pricing page" || captured["count"] != float64(3) || captured["style"] != "dark" || captured["batch_name"] != "Pricing" || len(captured["variations"].([]any)) != 2 {
		t.Fatalf("request: %#v", captured)
	}
	view := out.(map[string]any)
	if view["job_id"] != "design/project-a/task_design01" || view["status"] != "in_progress" {
		t.Fatalf("view: %#v", view)
	}
	for _, route := range []string{"/v3/projects/project-a/design-jobs", "/v3/projects/project-a/design-jobs/task_1/cancel"} {
		if !controlMCPRouteAllowed(http.MethodPost, route) {
			t.Fatalf("route %s not allowed", route)
		}
	}
	if controlMCPRouteAllowed(http.MethodPost, "/v3/projects/project-a/design-jobs/accept/cancel") {
		t.Fatal("reserved id accepted")
	}
	c.request = requestWithScopedToken(c.request, &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read"}})
	captured = nil
	if _, err := controlMCPDesign(c, map[string]any{"brief": "x"}); err == nil || captured != nil {
		t.Fatal("read-only key made a design job")
	}
}

type designScriptedModel struct{}

func (designScriptedModel) Info() design.ModelInfo {
	return design.ModelInfo{Provider: "fixture", Model: "designer-model", Thinking: "low"}
}

func (designScriptedModel) Complete(_ context.Context, call design.ModelCall) (design.ModelReply, error) {
	cost := 0.002
	usage := design.CallUsage{Reported: true, Input: 100, Output: 50, Total: 150, CostUSD: &cost, PriceStatus: "known"}
	if call.Item == 2 {
		return design.ModelReply{}, errors.New("provider refused the request")
	}
	return design.ModelReply{Text: "```html\n<!doctype html><html><head><title>t</title></head><body><main>ok</main></body></html>\n```", Usage: usage}, nil
}

// Requirement: a running design job writes its designs and results.json into
// its own folder, records each design's outcome and real usage on the task,
// and settles the task (completed with one design written of two) so the
// running-job index is cleared. Threat: a job whose files and durable record
// disagree, that stays running after the pipeline ends, or that reports cost
// the model did not report. Owners: executeDesignJob, designJobProgress,
// applyDesignBatch, finishDesignJob with tool.DesignFiles and the real store.
// A scripted model and an always-OK checker stand in for the provider and
// browser; this proves the wiring, not model or browser behavior.
func TestExecuteDesignJobRecordsOutcome(t *testing.T) {
	s, store, _, p := newDesignJobTestServer(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	task := putDesignTask(t, store, p, pebblestore.DesignJobRunning)
	files, err := tool.NewDesignFiles(root, task.Design.ResultsPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	handle := &designJobHandle{cancel: cancel}
	s.executeDesignJob(ctx, p, "project-a", task.ID, task.Design.Request(), designScriptedModel{}, files, s.designJobs, "key", handle)

	got, _, err := store.GetProjectTask(p.AccountScopeID, "project-a", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	job := got.Design
	if got.Status != "completed" || job.State != pebblestore.DesignJobCompleted || job.FinishedAt == 0 || len(job.Items) != 2 {
		t.Fatalf("job not settled: status=%q design=%#v", got.Status, job)
	}
	if job.Items[0].Status != string(design.StatusPassed) || job.Items[0].Path != "designs/pricing-abc123/01.html" || job.Items[0].CostUSD == nil || *job.Items[0].CostUSD != 0.002 || !job.Items[0].UsageComplete {
		t.Fatalf("item 1: %#v", job.Items[0])
	}
	if job.Items[1].Status != string(design.StatusGenerationFailed) || job.Items[1].Path != "" || job.Items[1].CostUSD != nil || job.Items[1].Error == "" {
		t.Fatalf("item 2: %#v", job.Items[1])
	}
	if job.Calls != 1 || job.Tokens != 150 || job.Model != "designer-model" {
		t.Fatalf("totals: %#v", job)
	}
	if _, err := os.Stat(filepath.Join(root, "designs/pricing-abc123/01.html")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "designs/pricing-abc123/02.html")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a failed design left a file")
	}
	raw, err := os.ReadFile(filepath.Join(root, "designs/pricing-abc123/results.json"))
	if err != nil || !strings.Contains(string(raw), `"generation_failed"`) {
		t.Fatalf("results.json: %v %s", err, raw)
	}
	if marked, err := store.ReconcileInterruptedDesignJobs(1); err != nil || marked != 0 {
		t.Fatalf("running index left after settling: %d %v", marked, err)
	}
}
