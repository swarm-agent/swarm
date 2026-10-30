package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
)

// catalogParityAgent is the Orchestrator's agent name in the parity
// harness: the owner's manage_jobs grants name it.
const catalogParityAgent = "swarm"

// seedJobs gives the home and the linked machine the same worker
// ("parity-worker") with one running job ("job:parity", its run
// parity-run) recorded in its own worker-execution session, as admission
// records it, and grants the home's Orchestrator every manage_jobs change
// (manage_jobs.swarm.<action>), as the owner does on the Permissions page.
func (h *catalogParityHarness) seedJobs(t *testing.T) {
	t.Helper()
	h.jobs = map[string]map[string]string{}
	for _, machine := range []struct {
		name          string
		s             *Server
		account, user string
	}{{"", h.home.s, h.home.account, h.home.user}, {"box", h.remote.s, h.remote.account, h.remote.user}} {
		worker, err := machine.s.sessions.CreateWorker(context.Background(), machine.account, machine.user, pebblestore.CreateWorkerRequest{Name: "parity-worker", Role: "coder"})
		if err != nil {
			t.Fatal(err)
		}
		session := pebblestore.SessionSnapshot{ID: "worker-execution-parity-run", WorkspacePath: t.TempDir(), AccountScopeID: machine.account, UserID: machine.user,
			Metadata: map[string]any{"worker_id": worker.ID, "worker_execution_run_id": "parity-run"}}
		if err := machine.s.sessions.Store().CreateSessionForAccount(session, machine.user, machine.account); err != nil {
			t.Fatal(err)
		}
		run := pebblestore.WorkerRunRecord{ID: "parity-run", WorkerID: worker.ID, UserID: machine.user, Role: pebblestore.WorkerRoleCoder, SessionID: session.ID,
			Status: "running", RequestSource: "direct", Input: map[string]any{"prompt": "Parity job."}, CreatedAt: 1, StartedAt: 1}
		if _, err := machine.s.sessions.Store().WorkerStore().RecordWorkerRun(machine.account, run); err != nil {
			t.Fatal(err)
		}
		h.jobs[machine.name] = map[string]string{"parity": controlMCPJobID(controlMCPJobWorker, worker.ID, "parity-run"), "worker": worker.ID}
	}
	perms := permission.NewService(pebblestore.NewPermissionStore(h.home.s.sessions.Store().Underlying()), nil, nil)
	for action := range nativeJobMutations {
		if _, err := perms.UpsertRuleForAccount(h.home.account, permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "manage_jobs." + catalogParityAgent + "." + action}); err != nil {
			t.Fatal(err)
		}
	}
	h.home.s.perm = perms
}

// resolveJob puts a seeded job's id where a case names it as "job:NAME".
func (h *catalogParityHarness) resolveJob(machine, text string) string {
	name, ok := strings.CutPrefix(text, "job:")
	if !ok || h.jobs == nil {
		return text
	}
	if id, ok := h.jobs[machine][name]; ok {
		return id
	}
	return text
}

// manageJobsParityCases holds a case for every manage_jobs action, here and
// on the linked machine box. Reads run on the seeded job and the seeded
// workflow; changes run on jobs and workers that refuse them the same way on
// both surfaces (an unknown job, a worker that does not exist, a running job
// with no question, no delivery and nothing to merge, a stop this harness's
// daemons cannot carry out), so neither call changes what the other reads.
// The end-to-end flow on a real worker is TestManageJobsEndToEndFromBothSurfaces.
func manageJobsParityCases() map[string][]catalogParityCase {
	at := func(machine string, args ...any) map[string]any {
		out := parityArgs(args...)
		if machine != "" {
			out["machine"] = machine
		}
		return out
	}
	everywhere := func(args ...any) []catalogParityCase {
		var out []catalogParityCase
		for _, machine := range []string{"", "box"} {
			call := at(machine, args...)
			out = append(out, catalogParityCase{orchestrator: call, control: call, compare: parityExact})
		}
		return out
	}
	project := map[string]string{"": "home-project", "box": "remote-project"}
	projectCases := func(args ...any) []catalogParityCase {
		var out []catalogParityCase
		for _, machine := range []string{"", "box"} {
			call := at(machine, append(append([]any(nil), args...), "project", project[machine])...)
			out = append(out, catalogParityCase{orchestrator: call, control: call, compare: parityExact})
		}
		return out
	}
	return map[string][]catalogParityCase{
		"list_runs":          everywhere("action", "list_runs", "limit", float64(5)),
		"get_job":            everywhere("action", "get_job", "job_id", "job:parity"),
		"deliveries":         everywhere("action", "deliveries", "id", "wf:Parity"),
		"rerun_failed_steps": everywhere("action", "rerun_failed_steps", "job_id", "job:parity"),
		"give_job":           projectCases("action", "give_job", "worker", "no-such-worker", "brief", "Parity"),
		"message_job":        everywhere("action", "message_job", "job_id", "worker/no-such-worker/no-such-run", "message", "more"),
		"answer_job":         everywhere("action", "answer_job", "job_id", "job:parity", "answer", "yes"),
		"deliver_job":        everywhere("action", "deliver_job", "job_id", "job:parity"),
		"stop_job":           everywhere("action", "stop_job", "job_id", "job:parity"),
		"schedule_job":       projectCases("action", "schedule_job", "worker", "no-such-worker", "brief", "Parity", "schedule", map[string]any{"kind": "interval", "interval_seconds": float64(3600)}),
	}
}

// jobsEndToEnd is one daemon with a real active Coder worker (deployed as
// swarm_deploy_worker does), its worker execution service, a project with a
// folder, the home Orchestrator's conversation with the owner's manage_jobs
// grants, and Swarm Control served by the same tool runtime.
type jobsEndToEnd struct {
	*jobCorrectionFixture
	runtime *tool.Runtime
	perms   *permission.Service
	folder  string
}

func newJobsEndToEnd(t *testing.T) *jobsEndToEnd {
	t.Helper()
	f := newJobCorrectionFixture(t)
	p := testPrincipal()
	perms, ok := f.server.perm.(*permission.Service)
	if !ok {
		t.Fatalf("permission service %T", f.server.perm)
	}
	// The project's folder is a real repository in the daemon's workspace
	// catalog, as swarm_create_project leaves it.
	f.server.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(f.sessions.Store().Underlying()))
	f.server.runner.(*runruntime.Service).SetWorkspaceService(f.server.workspace)
	f.server.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: jobautomation.NewStore(f.sessions.Store().Underlying())}))
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
	added, err := f.server.workspace.AddForPrincipal(p, folder, "project", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.sessions.Store().PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "jobs-project", AccountID: p.AccountScopeID, Name: "Jobs project", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: added.WorkspaceID, Path: folder, Role: "primary_code"}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.sessions.Store().CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "jobs-orch", AccountScopeID: p.AccountScopeID, UserID: p.UserID}, p.UserID, p.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	for action := range nativeJobMutations {
		if _, err := perms.UpsertRuleForAccount(p.AccountScopeID, permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "manage_jobs." + catalogParityAgent + "." + action}); err != nil {
			t.Fatal(err)
		}
	}
	runtime := tool.NewRuntime(1)
	t.Cleanup(func() { _ = runtime.Close() })
	runtime.SetManageSessionService(f.sessions)
	runtime.SetWorkflowLifecycleService(f.server)
	f.server.SetToolCatalogRuntime(runtime)
	e := &jobsEndToEnd{jobCorrectionFixture: f, runtime: runtime, perms: perms, folder: folder}
	// Admission (POST /v3/workers/{id}/direct) prepares the job's worktree
	// and resolves its model, which needs the agent and model-settings
	// authorities this daemon does not run: it is recorded as admission
	// records it (jobCorrectionFixture.job), from the request the handler
	// sent. Every other route is the daemon's own.
	routes := f.server.internalRouteHandler()
	admitted := 0
	f.server.controlMCPRoutes = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v3/workers/"+f.workerID+"/direct" {
			routes.ServeHTTP(w, r)
			return
		}
		var body struct {
			Prompt        string         `json:"prompt"`
			WorkspacePath string         `json:"workspace_path"`
			Input         map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.WorkspacePath != folder || body.Input["project_id"] != "jobs-project" {
			writeError(w, http.StatusBadRequest, errors.New("unexpected admission request"))
			return
		}
		admitted++
		runID := fmt.Sprintf("e2e-run-%d", admitted)
		_, sessionID := f.job(t, runID, func(run *pebblestore.WorkerRunRecord) { run.Input = map[string]any{"prompt": body.Prompt} })
		writeJSON(w, http.StatusOK, map[string]any{"run": map[string]any{"id": runID, "session_id": sessionID, "status": "running"}})
	})
	return e
}

// orchestrator runs a manage_jobs call as the Orchestrator's conversation
// and returns the envelope's state and data.
func (e *jobsEndToEnd) orchestrator(t *testing.T, args map[string]any) (string, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(args)
	ctx, cancel := context.WithTimeout(tool.WithCatalogAgent(context.Background(), catalogParityAgent), 20*time.Second)
	defer cancel()
	out, err := e.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: "jobs-orch", Principal: testPrincipal()}, tool.Call{Name: "manage_jobs", Arguments: string(raw)})
	if err != nil {
		t.Fatalf("Orchestrator manage_jobs %v: %v", args["action"], err)
	}
	return jobsEnvelope(t, out)
}

// control runs a manage_jobs call through Swarm Control's tools/call with a
// key holding scopes and returns the envelope's state and data.
func (e *jobsEndToEnd) control(t *testing.T, scopes []string, args map[string]any) (string, map[string]any) {
	t.Helper()
	out, refused := e.controlCall(t, scopes, args)
	if refused != "" {
		t.Fatalf("Swarm Control manage_jobs %v refused: %s", args["action"], refused)
	}
	return jobsEnvelope(t, out)
}

func (e *jobsEndToEnd) controlCall(t *testing.T, scopes []string, args map[string]any) (string, string) {
	t.Helper()
	req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)), &pebblestore.ScopedTokenRecord{ID: "key-jobs", Scopes: scopes})
	ctx, cancel := context.WithTimeout(req.Context(), 20*time.Second)
	defer cancel()
	c := &controlMCPCall{request: req.WithContext(ctx), server: e.server}
	params, _ := json.Marshal(map[string]any{"name": "manage_jobs", "arguments": args})
	result, rpcErr := c.callTool(params)
	if rpcErr != nil {
		t.Fatalf("tools/call: %+v", rpcErr)
	}
	text, _ := result["content"].([]map[string]any)[0]["text"].(string)
	if result["isError"] == true {
		return "", text
	}
	return text, ""
}

func jobsEnvelope(t *testing.T, out string) (string, map[string]any) {
	t.Helper()
	if err := tool.CheckEnvelope(out); err != nil {
		t.Fatalf("not a catalog envelope: %v\n%s", err, out)
	}
	var envelope struct {
		Tool  string         `json:"tool"`
		State string         `json:"state"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil || envelope.Tool != "manage_jobs" {
		t.Fatalf("envelope %s: %v", out, err)
	}
	return envelope.State, envelope.Data
}

// ask records a question the job's worker asked (ask_user), pending in the
// job's session, as the permission service records it during a run.
func (e *jobsEndToEnd) ask(t *testing.T, sessionID, callID, question string) string {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"question": question})
	record, err := e.perms.CreatePending(permission.CreateInput{SessionID: sessionID, RunID: "job-run", CallID: callID, ToolName: "ask_user", ToolArguments: string(args), Requirement: "tool", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	return record.ID
}

// Purpose:
//   - Requirement: the Orchestrator and an MCP client run worker jobs from the
//     catalog's manage_jobs, with the same actions and answers, including the
//     actions that were MCP-only (answer_job, stop_job, schedule_job): give a
//     job to a deployed worker (Swarm Control), read it (Orchestrator), message
//     it (Orchestrator), see it wait on its question (state waiting, both),
//     answer it (Orchestrator; a second question from Swarm Control), stop it
//     (Swarm Control) and schedule a job (both). A read key is refused every
//     change before anything runs.
//   - Threat/regression: a job action that one surface cannot reach, a
//     waiting job reported as ok, an answer that does not resolve the
//     worker's question, a stop that leaves the job running, a schedule that
//     is not enabled, or a read key starting work.
//   - Boundary/authority: tool.Runtime.executeManageJobs (both surfaces) ->
//     api.Server.ManageJobs -> the swarm_* job handlers over the daemon's
//     own routes (POST /v3/workers/{id}/direct, /v3/sessions/{id}/messages,
//     permission resolve, /v3/workers/{id}/runs/{run}/cancel, /v3/workers and
//     its activate and automation enable routes). Real Pebble store, real
//     Coder worker, real worker execution service; the model provider is
//     never reached. Admission itself (worktree preparation and model
//     resolution) is recorded as admission records it; it is not under test.
//   - Narrowest layer: the flow crosses the catalog handler, the job service
//     and the worker and session routes, so only routed calls through both
//     surfaces prove what each caller sees.
func TestManageJobsEndToEndFromBothSurfaces(t *testing.T) {
	e := newJobsEndToEnd(t)
	p := testPrincipal()
	full := []string{"swarm:read", "swarm:write", "sessions:read", "sessions:write", "projects:read", "automations:read", "automations:write", "signals:read"}
	readKey := []string{"swarm:read", "sessions:read", "sessions:write", "projects:read", "automations:read", "signals:read"}

	// A read key is refused a job before anything runs.
	if _, refused := e.controlCall(t, readKey, map[string]any{"action": "give_job", "worker": e.workerID, "brief": "Nothing", "project": "jobs-project"}); !strings.Contains(refused, "needs swarm:write") {
		t.Fatalf("read key give_job: %q", refused)
	}
	if runs, _, err := e.sessions.Store().WorkerStore().ListWorkerRuns(p.AccountScopeID, e.workerID, 10, ""); err != nil || len(runs) != 0 {
		t.Fatalf("a refused give_job started work: %+v %v", runs, err)
	}

	// Give a job (Swarm Control).
	state, given := e.control(t, full, map[string]any{"action": "give_job", "worker": e.workerID, "brief": "Add a rate limit to the upload route.", "project": "jobs-project", "idempotency_key": "e2e-give"})
	jobID, _ := given["job_id"].(string)
	if state != tool.EnvelopeStateOK || !strings.HasPrefix(jobID, "worker/"+e.workerID+"/") {
		t.Fatalf("give_job: %s %v", state, given)
	}
	runID := strings.TrimPrefix(jobID, "worker/"+e.workerID+"/")
	run, found, err := e.sessions.Store().WorkerStore().GetWorkerRun(p.AccountScopeID, e.workerID, runID)
	if err != nil || !found || run.SessionID == "" {
		t.Fatalf("given job's run %+v %v %v", run, found, err)
	}

	// Read it (Orchestrator): the same job.
	if _, read := e.orchestrator(t, map[string]any{"action": "get_job", "job_id": jobID}); read["job_id"] != jobID {
		t.Fatalf("get_job: %v", read)
	}

	// Message it (Orchestrator): the message lands in the job's session.
	if _, sent := e.orchestrator(t, map[string]any{"action": "message_job", "job_id": jobID, "message": "Also cap uploads at 10 MB.", "client_request_id": "e2e-message"}); sent["job_id"] != jobID {
		t.Fatalf("message_job: %v", sent)
	}
	landed := false
	for _, m := range e.userMessages(t, run.SessionID) {
		landed = landed || m.Content == "Also cap uploads at 10 MB."
	}
	if !landed {
		t.Fatalf("the message did not reach the job's session: %+v", e.userMessages(t, run.SessionID))
	}

	// The worker asks: both surfaces see the job waiting on it.
	first := e.ask(t, run.SessionID, "ask-1", "Which limit should apply to admins?")
	for _, read := range []func() (string, map[string]any){
		func() (string, map[string]any) {
			return e.control(t, full, map[string]any{"action": "get_job", "job_id": jobID})
		},
		func() (string, map[string]any) {
			return e.orchestrator(t, map[string]any{"action": "get_job", "job_id": jobID})
		},
	} {
		if state, job := read(); state != tool.EnvelopeStateWaiting || job["status"] != "needs_input" {
			t.Fatalf("a job waiting on its question: state %s, status %v", state, job["status"])
		}
	}
	// Answer it (Orchestrator): the question is resolved with the answer.
	if _, answered := e.orchestrator(t, map[string]any{"action": "answer_job", "job_id": jobID, "answer": "The same limit."}); answered["answered"] != first {
		t.Fatalf("answer_job: %v", answered)
	}
	if record, ok, err := e.perms.GetPermission(run.SessionID, first); err != nil || !ok || record.Status == pebblestore.PermissionStatusPending {
		t.Fatalf("the answered question is still pending: %+v %v %v", record, ok, err)
	}
	// A second question, answered from Swarm Control.
	second := e.ask(t, run.SessionID, "ask-2", "Should the limit be configurable?")
	if _, answered := e.control(t, full, map[string]any{"action": "answer_job", "job_id": jobID, "answer": "No."}); answered["answered"] != second {
		t.Fatalf("Swarm Control answer_job: %v", answered)
	}

	// Stop it (Swarm Control): the job is cancelled for good.
	if _, stopped := e.control(t, full, map[string]any{"action": "stop_job", "job_id": jobID, "reason": "Done for today."}); stopped["job_id"] != jobID || stopped["status"] != "cancelled" {
		t.Fatalf("stop_job: %v", stopped)
	}

	// Schedule one from each surface: each is a new active worker with its
	// schedule enabled.
	schedule := map[string]any{"kind": "interval", "interval_seconds": float64(3600)}
	for i, call := range []func(map[string]any) (string, map[string]any){
		func(args map[string]any) (string, map[string]any) { return e.orchestrator(t, args) },
		func(args map[string]any) (string, map[string]any) { return e.control(t, full, args) },
	} {
		name := []string{"Nightly from the Orchestrator", "Nightly from Swarm Control"}[i]
		state, scheduled := call(map[string]any{"action": "schedule_job", "worker": e.workerID, "brief": "Check the upload route.", "schedule": schedule, "project": "jobs-project", "name": name})
		worker, _ := scheduled["worker"].(map[string]any)
		id, _ := worker["id"].(string)
		if state != tool.EnvelopeStateOK || id == "" || scheduled["schedule_id"] == "" {
			t.Fatalf("schedule_job %s: %s %v", name, state, scheduled)
		}
		record, found, err := e.sessions.Store().WorkerStore().GetWorker(p.AccountScopeID, id)
		if err != nil || !found || record.LifecycleState != pebblestore.WorkerLifecycleStateActive || len(record.Automations) != 1 || !record.Automations[0].Enabled {
			t.Fatalf("scheduled worker %s: %+v %v %v", name, record, found, err)
		}
	}
}

// Purpose:
//   - Requirement: each manage_jobs action is listed and callable only for a
//     key holding its level and one of its route's API scopes: a read key
//     lists only the reads and is refused every change before anything runs;
//     a key without signals:read cannot list runs; a key without
//     sessions:write cannot change a job. On a linked machine, the machine's
//     POST /v3/jobs/{action} route holds a scoped key to the same scopes.
//     Under linked.{name}.work ask, the Orchestrator's job start on that
//     machine is refused at home with the reason (it has no approval prompt
//     there) and nothing reaches the machine, while reads still run; a
//     Swarm Control caller (whose person approves in their AI's chat)
//     reaches the machine.
//   - Threat/regression: a key level or route scope bypassed through the
//     catalog tool, a scoped key acting above itself on a linked machine,
//     or the Orchestrator starting work on an ask machine without the
//     person's approval.
//   - Boundary/authority: controlMCPCall.catalogTools/callCatalogTool,
//     api.Server.ManageJobs (manageJobsLinked), handleJobs, over the two
//     linked in-process daemons of the parity harness. No provider runs.
//   - Narrowest layer: the listing, the call gate, the home's linked rule and
//     the machine's route are separate checks; only calls through both
//     daemons prove each.
func TestManageJobsLevelsScopesAndLinkedRule(t *testing.T) {
	h := newCatalogParityHarness(t)
	listed := func(scopes []string) map[string]bool {
		t.Helper()
		c := &controlMCPCall{request: h.controlRequest(scopes, ""), server: h.home.s}
		for _, candidate := range c.visibleTools() {
			if candidate.Name != "manage_jobs" {
				continue
			}
			actions := map[string]bool{}
			for _, action := range candidate.InputSchema["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]string) {
				actions[action] = true
			}
			return actions
		}
		return nil
	}
	full := listed(catalogParityFullKey)
	for _, want := range []string{"list_runs", "get_job", "deliveries", "rerun_failed_steps", "give_job", "message_job", "answer_job", "deliver_job", "stop_job", "schedule_job"} {
		if !full[want] {
			t.Fatalf("the default listing lacks manage_jobs %s: %v", want, full)
		}
	}
	readKey := []string{"swarm:read", "sessions:read", "sessions:write", "signals:read", "automations:read"}
	read := listed(readKey)
	if !read["get_job"] || !read["list_runs"] || !read["deliveries"] || read["give_job"] || read["answer_job"] || read["stop_job"] || read["schedule_job"] {
		t.Fatalf("read key listing: %v", read)
	}
	if noSignals := listed([]string{"swarm:read", "sessions:read"}); noSignals["list_runs"] || !noSignals["get_job"] {
		t.Fatalf("a key without signals:read lists list_runs: %v", noSignals)
	}
	jobID := h.jobs[""]["parity"]
	store := h.home.s.sessions.Store()
	before, _, _ := store.WorkerStore().ListWorkerRuns(h.home.account, h.jobs[""]["worker"], 10, "")
	for _, args := range []map[string]any{
		{"action": "give_job", "worker": h.jobs[""]["worker"], "brief": "x", "project": "home-project"},
		{"action": "stop_job", "job_id": jobID},
		{"action": "answer_job", "job_id": jobID, "answer": "x"},
		{"action": "schedule_job", "worker": h.jobs[""]["worker"], "brief": "x", "schedule": map[string]any{"kind": "interval", "interval_seconds": float64(3600)}},
	} {
		if _, refused := h.controlTool(t, readKey, "manage_jobs", args); !strings.Contains(refused, "needs swarm:write") {
			t.Fatalf("read key %v: %q", args["action"], refused)
		}
		if _, refused := h.controlTool(t, []string{"swarm:read", "swarm:write", "sessions:read"}, "manage_jobs", args); !strings.Contains(refused, "needs sessions:write") {
			t.Fatalf("key without sessions:write %v: %q", args["action"], refused)
		}
	}
	after, _, _ := store.WorkerStore().ListWorkerRuns(h.home.account, h.jobs[""]["worker"], 10, "")
	if len(after) != len(before) || after[0].Status != "running" {
		t.Fatalf("a refused change touched the jobs: %+v", after)
	}

	// The linked machine's route holds a scoped key to its scopes.
	remoteGive := map[string]any{"worker": "no-such-worker", "brief": "x", "project": "remote-project"}
	body, _ := json.Marshal(remoteGive)
	req := requestWithScopedToken(requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, JobsPath+"/give_job", strings.NewReader(string(body))), h.remote.user, h.remote.account), &pebblestore.ScopedTokenRecord{ID: "remote-read", Scopes: []string{"sessions:read"}})
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.remote.s.handleJobs(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a sessions:read key gave a job on the linked machine's route: %d %s", rec.Code, rec.Body.String())
	}

	// Under ask, the Orchestrator's job start is refused at home and never
	// reaches the machine; a read still runs; Swarm Control reaches it.
	fleetActivityTestSetWork(t, h.home, "box", pebblestore.RuleAsk)
	give := map[string]any{"action": "give_job", "machine": "box", "worker": "no-such-worker", "brief": "x", "project": "remote-project"}
	if _, err := h.orchestratorTool(t, "manage_jobs", give); err == nil || !strings.Contains(err.Error(), "linked.box.work is ask") {
		t.Fatalf("Orchestrator give_job on an ask machine: %v", err)
	}
	if _, err := h.orchestratorTool(t, "manage_jobs", map[string]any{"action": "list_runs", "machine": "box"}); err != nil {
		t.Fatalf("Orchestrator read on an ask machine: %v", err)
	}
	if _, refused := h.controlTool(t, catalogParityFullKey, "manage_jobs", give); !strings.Contains(refused, "linked machine box refused give_job") || !strings.Contains(refused, "no worker") {
		t.Fatalf("Swarm Control give_job on an ask machine did not reach it: %q", refused)
	}
	// Off refuses every linked job action, both surfaces.
	fleetActivityTestSetWork(t, h.home, "box", pebblestore.RuleOff)
	if _, err := h.orchestratorTool(t, "manage_jobs", map[string]any{"action": "list_runs", "machine": "box"}); err == nil || !strings.Contains(err.Error(), "linked.box.work is off") {
		t.Fatalf("Orchestrator read on an off machine: %v", err)
	}
	if _, refused := h.controlTool(t, catalogParityFullKey, "manage_jobs", map[string]any{"action": "list_runs", "machine": "box"}); !strings.Contains(refused, "linked.box.work is off") {
		t.Fatalf("Swarm Control read on an off machine: %q", refused)
	}
}
