package jobautomation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/sandbox"
	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// These tests drive the real consumer (Runner) over a real Pebble store and
// the real signal feed (SignalStore + signals.Emitter). Two host boundaries
// are stubbed: the job's swarm_get_job view (staticResults returns the
// result a job end produced) and the sandbox (hostSandbox runs the folder's
// script on the host with only the step's environment plus the folder's
// secret stand-ins, which is what the folder's sandbox gives a script; this
// machine has neither bubblewrap nor docker). The agent step's job start is
// stubbed by recordingAgents; the real Dispatch path is tested in api.

const testAccount = "acct-a"

type staticResults struct {
	mu      sync.Mutex
	results map[string]*Result // by run id
	calls   int
}

func (s *staticResults) Result(_ context.Context, sig pebblestore.Signal, _ bool) (*Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	id := sig.Refs["run_id"]
	if id == "" {
		id = sig.Refs["task_id"]
	}
	r, ok := s.results[id]
	if !ok {
		return nil, nil
	}
	copy := *r
	return &copy, nil
}

// hostSandbox stands in for the folder's sandbox: the script runs with a
// cleared environment, the step's variables and the stand-ins of the
// folder's secret grants only.
type hostSandbox struct {
	grants map[string]map[string]string // folder -> NAME -> stand-in
	before func(RunRequest) error
	ran    []RunRequest
}

func (h *hostSandbox) Run(ctx context.Context, req RunRequest) (RunOutcome, error) {
	if h.before != nil {
		if err := h.before(req); err != nil {
			return RunOutcome{}, err
		}
	}
	h.ran = append(h.ran, req)
	script, err := scriptPath(req.Folder, req.Command)
	if err != nil {
		return RunOutcome{}, &RefusedError{Reason: err.Error()}
	}
	cmd := exec.CommandContext(ctx, script, req.Args...)
	cmd.Dir = req.Folder
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	for k, v := range h.grants[req.Folder] {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = bytes.NewReader(req.Stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	if req.OnOutput != nil {
		req.OnOutput(out.Bytes())
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return RunOutcome{ExitCode: exit.ExitCode(), Output: out.Bytes()}, nil
	}
	return RunOutcome{Output: out.Bytes()}, err
}

type recordingAgents struct {
	started []AgentRequest
	status  string
	// outcomes, when set, script each started job's end in order (the
	// verdict its review recorded, its head); the job id is filled in.
	outcomes []AgentOutcome
}

func (a *recordingAgents) Start(_ context.Context, req AgentRequest) (string, error) {
	a.started = append(a.started, req)
	return "worker/reviewer-1/run-review-" + strconv.Itoa(len(a.started)), nil
}

func (a *recordingAgents) Wait(_ context.Context, _, jobID string) (AgentOutcome, error) {
	if len(a.outcomes) > 0 {
		n, _ := strconv.Atoi(strings.TrimPrefix(jobID, "worker/reviewer-1/run-review-"))
		if n < 1 || n > len(a.outcomes) {
			return AgentOutcome{}, errors.New("no scripted outcome for " + jobID)
		}
		out := a.outcomes[n-1]
		out.JobID = jobID
		if out.Status == "" {
			out.Status = "succeeded"
		}
		return out, nil
	}
	return AgentOutcome{JobID: jobID, Status: a.status, Result: &Result{JobID: jobID, Report: "looks fine", Git: &ResultGit{Head: "abc"},
		Review: map[string]any{"verdict": "approve", "findings": []any{"none"}}}}, nil
}

type harness struct {
	t       *testing.T
	dbPath  string
	db      *pebblestore.Store
	feed    *pebblestore.SignalStore
	store   *Store
	results *staticResults
	sandbox *hostSandbox
	agents  *recordingAgents
	runner  *Runner
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, dbPath: filepath.Join(t.TempDir(), "automations.pebble"), results: &staticResults{results: map[string]*Result{}},
		sandbox: &hostSandbox{grants: map[string]map[string]string{}}, agents: &recordingAgents{status: "succeeded"}}
	h.open()
	t.Cleanup(func() { _ = h.db.Close() })
	return h
}

// open (re)opens the database and builds a fresh runner, as a daemon start
// does.
func (h *harness) open() {
	h.t.Helper()
	db, err := pebblestore.Open(h.dbPath)
	if err != nil {
		h.t.Fatal(err)
	}
	h.db, h.feed, h.store = db, pebblestore.NewSignalStore(db), NewStore(db)
	h.runner = NewRunner(Options{Store: h.store, Feed: h.feed, Account: func() string { return testAccount }, Results: h.results,
		Run: h.sandbox, Agent: h.agents, Emit: signals.NewEmitter(h.feed).Emit,
		Head: func(_ context.Context, _ string, r Result) (string, error) { return "refs/swarm/jobs/" + r.RunID, nil }})
	h.runner.backoff = func(int) time.Duration { return 0 }
}

func (h *harness) restart() {
	h.t.Helper()
	if err := h.db.Close(); err != nil {
		h.t.Fatal(err)
	}
	h.open()
}

func (h *harness) process() {
	h.t.Helper()
	if err := h.runner.ProcessOnce(context.Background()); err != nil {
		h.t.Fatal(err)
	}
}

// jobEnd records a worker.run.<status> signal as the daemon does.
func (h *harness) jobEnd(runID, followup, status, source string) {
	h.t.Helper()
	if _, err := h.feed.Append(pebblestore.Signal{Kind: "worker.run." + status, Severity: pebblestore.SignalSeverityInfo, Source: pebblestore.SignalSourceSwarmd,
		Account: testAccount, Summary: "Worker run " + status, DedupKey: "worker.run:" + runID + ":" + followup,
		Refs: map[string]string{"worker_id": "coder-1", "run_id": runID}, Attrs: map[string]string{"source": source}}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) automation(a Automation) Automation {
	h.t.Helper()
	saved, _, err := h.store.Put(testAccount, a)
	if err != nil {
		h.t.Fatal(err)
	}
	return saved
}

func (h *harness) signalsOf(prefix string) []pebblestore.Signal {
	h.t.Helper()
	page, err := h.feed.ListAfter(0, 1000, pebblestore.SignalFilter{Account: testAccount, KindPrefixes: []string{prefix}})
	if err != nil {
		h.t.Fatal(err)
	}
	return page.Signals
}

func (h *harness) deliveries(jobID string) []Delivery {
	h.t.Helper()
	list, err := h.store.JobDeliveries(testAccount, jobID)
	if err != nil {
		h.t.Fatal(err)
	}
	return list
}

func writeScript(t *testing.T, folder, name, body string) {
	t.Helper()
	path := filepath.Join(folder, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func workerResult(folder, runID string) *Result {
	return &Result{V: ResultVersion, JobID: "worker/coder-1/" + runID, Kind: KindWorker, Role: "coder", Worker: &ResultWorker{ID: "coder-1", Name: "Coder"},
		RunID: runID, ProjectID: "proj-1", Folder: folder, Status: StatusSucceeded, JobStatus: "needs_review", Brief: "add a logo", Report: "Added it.",
		Git:  &ResultGit{Branch: "agent/" + runID, BaseCommit: strings.Repeat("a", 40), Head: strings.Repeat("b", 40), Commits: []ResultCommit{{SHA: strings.Repeat("b", 40), Subject: "add logo"}}, Files: []string{"logo.png"}},
		Cost: &ResultCost{Tokens: 1200, Model: "model-x"}}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Requirement: a run step gets the job's result JSON on stdin and the SWARM_*
// environment, sees its own folder's secret grants and nothing of another
// folder's or the daemon's environment, and its last output line is recorded
// as the step's output with the log tail.
// Threat: a script that cannot tell which job ended, a secret of another
// folder (or of the daemon) leaking into a folder's script, or a lost output.
// Owners: Runner.runScript (stepEnv, stdin, tail, lastLine) and Store.
func TestRunStepGetsResultOnStdinEnvAndOnlyTheFolderGrants(t *testing.T) {
	t.Setenv("SWARM_DAEMON_ONLY_SECRET", "daemon-value")
	h := newHarness(t)
	folder, other := t.TempDir(), t.TempDir()
	h.sandbox.grants[folder] = map[string]string{"FORGE_TOKEN": "swarm-secret://FORGE_TOKEN"}
	h.sandbox.grants[other] = map[string]string{"OTHER_TOKEN": "swarm-secret://OTHER_TOKEN"}
	writeScript(t, folder, "deliver/push.sh", `cat > "$PWD/stdin.json"
env | sort > "$PWD/env.txt"
echo "pushing $SWARM_HEAD"
echo "https://forge.example.invalid/pr/7"
echo ""
`)
	h.process() // first start: the cursor begins at the feed's end
	h.automation(Automation{Name: "push", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{{ID: "push", Type: StepRun, Command: "deliver/push.sh", Args: []string{"--draft"}}}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()

	list := h.deliveries("worker/coder-1/run-1")
	if len(list) != 1 {
		t.Fatalf("deliveries = %d", len(list))
	}
	d := h.store.WithLogs(list[0])
	step := d.Steps[0]
	if d.State != StateSucceeded || step.State != StateSucceeded || step.Attempts != 1 || step.ExitCode == nil || *step.ExitCode != 0 {
		t.Fatalf("delivery = %+v", d)
	}
	if step.Output != "https://forge.example.invalid/pr/7" || !strings.Contains(step.LogTail, "pushing "+strings.Repeat("b", 40)) {
		t.Fatalf("output %q tail %q", step.Output, step.LogTail)
	}
	var got Result
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(folder, "stdin.json"))), &got); err != nil {
		t.Fatal(err)
	}
	if got.JobID != "worker/coder-1/run-1" || got.Git == nil || got.Git.Head != strings.Repeat("b", 40) || got.Brief != "add a logo" || got.Report != "Added it." || got.Cost.Tokens != 1200 {
		t.Fatalf("stdin result = %+v", got)
	}
	env := readFile(t, filepath.Join(folder, "env.txt"))
	for _, want := range []string{"SWARM_JOB_ID=worker/coder-1/run-1", "SWARM_JOB_STATUS=succeeded", "SWARM_HEAD=" + strings.Repeat("b", 40),
		"SWARM_BASE=" + strings.Repeat("a", 40), "SWARM_BRANCH=agent/run-1", "SWARM_JOB_REF=refs/swarm/jobs/run-1", "SWARM_DELIVERY_ID=" + d.ID,
		"FORGE_TOKEN=swarm-secret://FORGE_TOKEN"} {
		if !strings.Contains(env, want+"\n") {
			t.Fatalf("env lacks %s:\n%s", want, env)
		}
	}
	for _, leak := range []string{"OTHER_TOKEN", "SWARM_DAEMON_ONLY_SECRET", "daemon-value"} {
		if strings.Contains(env, leak) {
			t.Fatalf("env leaks %s:\n%s", leak, env)
		}
	}
	if len(h.sandbox.ran) != 1 || h.sandbox.ran[0].Folder != folder || h.sandbox.ran[0].Args[0] != "--draft" {
		t.Fatalf("sandbox requests = %+v", h.sandbox.ran)
	}
	if sigs := h.signalsOf("automation.step.succeeded"); len(sigs) != 1 || sigs[0].Attrs["output"] != "https://forge.example.invalid/pr/7" {
		t.Fatalf("succeeded signals = %+v", sigs)
	}
	if sigs := h.signalsOf("automation.delivery.finished"); len(sigs) != 1 || sigs[0].Attrs["state"] != StateSucceeded {
		t.Fatalf("finished signals = %+v", sigs)
	}
}

// Requirement: a failed step stops the steps after it, the delivery fails,
// and automation.step.failed (warning) names the job, delivery and step in
// its refs (never automation_id), with a short reason and the next action.
// Threat: later steps (a deploy) running after a failed one (a check), or a
// failure an operator's monitor cannot see.
// Owners: Runner.runDelivery, runStep, skipRest and signal.
func TestFailingStepStopsLaterStepsAndSignalsFailure(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "check.sh", "echo 'tests failed: 3'\nexit 3\n")
	writeScript(t, folder, "deploy.sh", "touch \"$PWD/deployed\"\n")
	h.process()
	h.automation(Automation{Name: "check then deploy", Scope: Scope{Folder: folder}, Enabled: true,
		Steps: []Step{{ID: "check", Type: StepRun, Command: "check.sh"}, {ID: "deploy", Type: StepRun, Command: "deploy.sh"}}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()

	d := h.deliveries("worker/coder-1/run-1")[0]
	if d.State != StateFailed || d.Steps[0].State != StateFailed || d.Steps[1].State != StateSkipped {
		t.Fatalf("states = %s %s %s", d.State, d.Steps[0].State, d.Steps[1].State)
	}
	if *d.Steps[0].ExitCode != 3 || d.Steps[0].Output != "tests failed: 3" || d.Steps[0].Next != "fix the script" {
		t.Fatalf("failed step = %+v", d.Steps[0])
	}
	if _, err := os.Stat(filepath.Join(folder, "deployed")); !os.IsNotExist(err) {
		t.Fatal("the step after a failed step ran")
	}
	failed := h.signalsOf("automation.step.failed")
	if len(failed) != 1 {
		t.Fatalf("failed signals = %d", len(failed))
	}
	sig := failed[0]
	if sig.Severity != pebblestore.SignalSeverityWarning || sig.Refs["job_id"] != d.JobID || sig.Refs["delivery_id"] != d.ID || sig.Refs["step_id"] != "check" {
		t.Fatalf("failed signal = %+v", sig)
	}
	if _, ok := sig.Refs["automation_id"]; ok {
		t.Fatal("automation signal uses automation_id, which names a schedule on worker signals")
	}
	if sig.Attrs["step"] != "check" || !strings.Contains(sig.Attrs["reason"], "exited with code 3") || sig.Attrs["next"] != "fix the script" {
		t.Fatalf("failed attrs = %+v", sig.Attrs)
	}
	if started := h.signalsOf("automation.step.started"); len(started) != 1 {
		t.Fatalf("started signals = %d, want only the first step", len(started))
	}
}

// Requirement: continue_on_failure lets the next steps run after a failed
// step; the delivery still reports failed.
// Owner: Runner.runDelivery.
func TestContinueOnFailureRunsLaterSteps(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "notify.sh", "exit 1\n")
	writeScript(t, folder, "upload.sh", "echo uploaded\n")
	h.process()
	h.automation(Automation{Name: "notify and upload", Scope: Scope{Folder: folder}, When: WhenAlways, Enabled: true,
		Steps: []Step{{ID: "notify", Type: StepRun, Command: "notify.sh", ContinueOnFailure: true}, {ID: "upload", Type: StepRun, Command: "upload.sh"}}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	d := h.deliveries("worker/coder-1/run-1")[0]
	if d.Steps[0].State != StateFailed || d.Steps[1].State != StateSucceeded || d.Steps[1].Output != "uploaded" || d.State != StateFailed {
		t.Fatalf("states = %s %s %s", d.State, d.Steps[0].State, d.Steps[1].State)
	}
}

// Requirement: a daemon restart in the middle of a delivery resumes it at
// the first step that is not finished and never re-runs a finished step.
// Threat: a push or upload done twice after a crash.
// Owners: Store (durable step states, the unfinished index) and
// Runner.runDelivery over a reopened database.
func TestRestartResumesWithoutRerunningAFinishedStep(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "one.sh", "echo x >> \"$PWD/one.count\"\n")
	writeScript(t, folder, "two.sh", "echo x >> \"$PWD/two.count\"\necho done\n")
	h.process()
	h.automation(Automation{Name: "two steps", Scope: Scope{Folder: folder}, Enabled: true,
		Steps: []Step{{ID: "one", Type: StepRun, Command: "one.sh"}, {ID: "two", Type: StepRun, Command: "two.sh"}}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")

	// The daemon stops while step two is starting.
	ctx, cancel := context.WithCancel(context.Background())
	h.sandbox.before = func(req RunRequest) error {
		if req.Command == "two.sh" {
			cancel()
			return context.Canceled
		}
		return nil
	}
	if err := h.runner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	h.runner.dispatch(ctx, true)
	d := h.deliveries("worker/coder-1/run-1")[0]
	if d.Steps[0].State != StateSucceeded || d.Steps[1].State != StateRunning || Terminal(d.State) {
		t.Fatalf("before restart: %s %s %s", d.State, d.Steps[0].State, d.Steps[1].State)
	}

	h.sandbox.before = nil
	h.restart()
	h.process()
	d = h.deliveries("worker/coder-1/run-1")[0]
	if d.State != StateSucceeded || d.Steps[0].Attempts != 1 || d.Steps[1].State != StateSucceeded {
		t.Fatalf("after restart: %+v", d)
	}
	if got := readFile(t, filepath.Join(folder, "one.count")); got != "x\n" {
		t.Fatalf("step one ran %d times", strings.Count(got, "x"))
	}
	if got := readFile(t, filepath.Join(folder, "two.count")); got != "x\n" {
		t.Fatalf("step two ran %d times", strings.Count(got, "x"))
	}
}

// Requirement: the same job end delivers once, even when its signal is seen
// again (a replayed signal, the cursor read again); a follow-up of the same
// job is a new delivery that runs again.
// Threat: one job pushed or deployed twice.
// Owners: Runner.consume/Scan and Store.CreateDelivery (stable delivery id).
func TestSameJobEndDeliversOnceAndAFollowupDeliversAgain(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "count.sh", "echo \"$SWARM_DELIVERY_ID\" >> \"$PWD/runs\"\n")
	h.process()
	h.automation(Automation{Name: "count", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{{ID: "count", Type: StepRun, Command: "count.sh"}}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.jobEnd("run-1", "", "succeeded", "direct") // the same end, recorded again
	h.process()
	if err := h.store.SetCursor(0); err != nil { // and the whole feed read again
		t.Fatal(err)
	}
	h.process()
	if list := h.deliveries("worker/coder-1/run-1"); len(list) != 1 {
		t.Fatalf("deliveries after replay = %d", len(list))
	}
	if runs := strings.Count(readFile(t, filepath.Join(folder, "runs")), "\n"); runs != 1 {
		t.Fatalf("the step ran %d times for one job end", runs)
	}

	h.jobEnd("run-1", "followup-run-9", "succeeded", "direct")
	h.process()
	list := h.deliveries("worker/coder-1/run-1")
	if len(list) != 2 || list[0].Followup == list[1].Followup {
		t.Fatalf("deliveries after follow-up = %+v", list)
	}
	if runs := strings.Count(readFile(t, filepath.Join(folder, "runs")), "\n"); runs != 2 {
		t.Fatalf("the follow-up ran the step %d times in total, want 2", runs)
	}
}

// Requirement: an agent step starts a job (with the result attached and an
// idempotency key of delivery and step), waits for it and records its result
// and review verdict; a job started by an automation (request source
// automation) triggers no automations.
// Threat: an automation loop where a job's automation starts a job that
// triggers it again.
// Owners: Runner.runAgent and the loop guard in Runner.consume.
func TestAgentStepRecordsTheJobAndAutomationJobsTriggerNothing(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	h.process()
	h.automation(Automation{Name: "review", Scope: Scope{Folder: folder}, When: WhenAlways, Enabled: true,
		Steps: []Step{{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "review the job"}}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	if len(h.agents.started) != 1 {
		t.Fatalf("agent jobs started = %d", len(h.agents.started))
	}
	req := h.agents.started[0]
	d := h.deliveries("worker/coder-1/run-1")[0]
	if req.Worker != "reviewer" || req.Folder != folder || req.Result.JobID != "worker/coder-1/run-1" || req.IdempotencyKey != d.ID+".review" {
		t.Fatalf("agent request = %+v", req)
	}
	step := d.Steps[0]
	if step.State != StateSucceeded || step.Outcome["job_id"] != "worker/reviewer-1/run-review-1" || step.Outcome["head"] != "abc" {
		t.Fatalf("agent step = %+v", step)
	}
	if review, _ := step.Outcome["review"].(map[string]any); review["verdict"] != "approve" {
		t.Fatalf("review outcome = %+v", step.Outcome)
	}

	// The reviewer's own end: request source automation, no automation runs.
	h.results.results["run-review-1"] = workerResult(folder, "run-review-1")
	h.results.results["run-review-1"].RequestSource = RequestSourceAutomation
	calls := h.results.calls
	h.jobEnd("run-review-1", "", "succeeded", RequestSourceAutomation)
	h.process()
	if list := h.deliveries("worker/coder-1/run-review-1"); len(list) != 0 {
		t.Fatalf("an automation's job triggered %d deliveries", len(list))
	}
	if h.results.calls != calls || len(h.agents.started) != 1 {
		t.Fatal("an automation's job was considered for automations")
	}
}

// Requirement: while the machine-wide stop switch is on no new step starts:
// the next step is recorded stopped (nothing runs) and the delivery ends
// stopped; with the switch off an explicit re-run runs the stopped steps.
// Threat: automations that cannot be halted during an incident.
// Owners: Store.SetStopped / Rerun and Runner.runDelivery.
func TestStopSwitchPreventsNewSteps(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "touch \"$PWD/pushed\"\n")
	h.process()
	h.automation(Automation{Name: "push", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{{ID: "push", Type: StepRun, Command: "push.sh"}}})
	if _, err := h.store.SetStopped(true, "user-a"); err != nil {
		t.Fatal(err)
	}
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	d := h.deliveries("worker/coder-1/run-1")[0]
	if d.State != StateStopped || d.Steps[0].State != StateStopped || d.Steps[0].Attempts != 0 || len(h.sandbox.ran) != 0 {
		t.Fatalf("stopped delivery = %+v (ran %d)", d, len(h.sandbox.ran))
	}
	if _, err := os.Stat(filepath.Join(folder, "pushed")); !os.IsNotExist(err) {
		t.Fatal("a step ran while the stop switch was on")
	}
	if sigs := h.signalsOf("automation.delivery.finished"); len(sigs) != 1 || sigs[0].Attrs["state"] != StateStopped {
		t.Fatalf("finished signals = %+v", sigs)
	}
	if _, err := h.store.SetStopped(false, "user-a"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := h.store.Rerun(testAccount, d.ID); err != nil || !ok {
		t.Fatal(ok, err)
	}
	h.process()
	d = h.deliveries("worker/coder-1/run-1")[0]
	if d.State != StateSucceeded || d.Reruns != 1 {
		t.Fatalf("after re-run = %+v", d)
	}
	if _, err := os.Stat(filepath.Join(folder, "pushed")); err != nil {
		t.Fatal("the re-run did not run the stopped step")
	}
}

// Requirement: a failed step is retried automatically only up to the
// automation's retries; with retries 0 it is not retried.
// Owner: Runner.runStep.
func TestRetriesAreBoundedByTheAutomation(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "flaky.sh", "echo x >> \"$PWD/tries\"\n[ \"$(wc -l < \"$PWD/tries\")\" -ge 3 ]\n")
	h.process()
	h.automation(Automation{Name: "flaky", Scope: Scope{Folder: folder}, Enabled: true, Retries: 2, Steps: []Step{{ID: "flaky", Type: StepRun, Command: "flaky.sh"}}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	for i := 0; i < 5; i++ {
		h.process()
	}
	d := h.deliveries("worker/coder-1/run-1")[0]
	if d.State != StateSucceeded || d.Steps[0].Attempts != 3 {
		t.Fatalf("retried delivery = %s attempts %d", d.State, d.Steps[0].Attempts)
	}

	folder2 := t.TempDir()
	writeScript(t, folder2, "fail.sh", "echo x >> \"$PWD/tries\"\nexit 1\n")
	h.automation(Automation{Name: "no retries", Scope: Scope{Folder: folder2}, Enabled: true, Steps: []Step{{ID: "fail", Type: StepRun, Command: "fail.sh"}}})
	h.results.results["run-2"] = workerResult(folder2, "run-2")
	h.jobEnd("run-2", "", "succeeded", "direct")
	for i := 0; i < 3; i++ {
		h.process()
	}
	if got := strings.Count(readFile(t, filepath.Join(folder2, "tries")), "x"); got != 1 {
		t.Fatalf("retries 0 ran the step %d times", got)
	}
}

// Requirement: with no automation a job's end changes nothing: no result is
// built, no delivery recorded, no step or automation signal emitted.
// Threat: a built-in path (a merge, a push) appearing without the person
// setting one up.
// Owner: Runner.consume.
func TestNoAutomationMeansNothingRuns(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	h.process()
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	if h.results.calls != 0 || len(h.deliveries("worker/coder-1/run-1")) != 0 || len(h.sandbox.ran) != 0 || len(h.agents.started) != 0 {
		t.Fatalf("a job end without automations did something: results %d sandbox %d agents %d", h.results.calls, len(h.sandbox.ran), len(h.agents.started))
	}
	if sigs := h.signalsOf("automation"); len(sigs) != 0 {
		t.Fatalf("automation signals without automations: %+v", sigs)
	}
	// An automation for another folder does not apply either.
	h.automation(Automation{Name: "elsewhere", Scope: Scope{Folder: t.TempDir()}, Enabled: true, Steps: []Step{{Type: StepRun, Command: "x.sh"}}})
	h.jobEnd("run-1", "followup-1", "succeeded", "direct")
	h.process()
	if len(h.deliveries("worker/coder-1/run-1")) != 0 {
		t.Fatal("an automation of another folder delivered")
	}
}

// Requirement: a run step is refused, with a clear reason, when the sandbox
// is not active; nothing runs on the host.
// Owner: SandboxExecutor.Run.
func TestSandboxExecutorRefusesWithoutAnActiveSandbox(t *testing.T) {
	_, err := SandboxExecutor{Manager: func() *sandbox.Manager { return nil }}.Run(context.Background(), RunRequest{Folder: t.TempDir(), Command: "push.sh"})
	var refused *RefusedError
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "sandbox is not active") {
		t.Fatalf("err = %v", err)
	}
}
