package jobautomation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/publish"
	"swarm/packages/swarmd/internal/sandbox"
	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// These tests drive the real consumer (Runner) over a real Pebble store and
// the real signal feed (SignalStore + signals.Emitter). Run steps run through
// the real HostExecutor (real processes on the host); its root-owner check
// accepts the test's own files through publish.AllowOwnerForTests. The job's
// swarm_get_job view (staticResults returns the result a job end produced)
// is stubbed, and the agent step's job start is stubbed by recordingAgents
// (the model provider); the real Dispatch path is tested in api.

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

// recordingHost is the real HostExecutor, recording each request; before
// may stop a request (a daemon stopping as the step starts).
type recordingHost struct {
	before func(RunRequest) error
	ran    []RunRequest
}

func (h *recordingHost) Run(ctx context.Context, req RunRequest) (RunOutcome, error) {
	if h.before != nil {
		if err := h.before(req); err != nil {
			return RunOutcome{}, err
		}
	}
	h.ran = append(h.ran, req)
	return HostExecutor{}.Run(ctx, req)
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
	host    *recordingHost
	agents  *recordingAgents
	runner  *Runner
	// head imports the job's head into the folder; nil names the ref only.
	head func(context.Context, string, Result) (string, error)
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, dbPath: filepath.Join(t.TempDir(), "automations.pebble"), results: &staticResults{results: map[string]*Result{}},
		host: &recordingHost{}, agents: &recordingAgents{status: "succeeded"}}
	t.Cleanup(publish.AllowOwnerForTests(os.Getuid()))
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
	h.build()
}

// build makes a fresh runner over the open database.
func (h *harness) build() {
	head := h.head
	if head == nil {
		head = func(_ context.Context, _ string, r Result) (string, error) { return "refs/swarm/jobs/" + r.RunID, nil }
	}
	h.runner = NewRunner(Options{Store: h.store, Feed: h.feed, Account: func() string { return testAccount }, Results: h.results,
		Run: h.host, Agent: h.agents, Emit: signals.NewEmitter(h.feed).Emit, Head: head})
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
	// Not group- or other-writable whatever the umask: the root-owner check
	// refuses that.
	if err := os.Chmod(path, 0o755); err != nil {
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

// Requirement: a run step runs on the host, as the daemon's user, in the
// folder, with no sandbox manager configured. It gets the job's result JSON
// on stdin and the SWARM_* environment over a minimal environment (PATH,
// HOME, a per-step TMPDIR removed afterwards) and nothing else of the
// daemon's environment; its last output line is recorded as the step's
// output with the log tail, and its exit code as the outcome.
// Threat: a step refused because the sandbox is off, a script that cannot
// tell which job ended, a daemon variable leaking into a script, a scratch
// folder left behind, or a lost output.
// Owners: HostExecutor.Run over publish.RunScript, and Runner.runScript
// (stepEnv, stdin, tail, lastLine) and Store. A real process run through the
// real runner is the narrowest layer that proves what the script sees.
func TestRunStepRunsOnTheHostWithResultAndOnlyItsEnv(t *testing.T) {
	t.Setenv("SWARM_DAEMON_ONLY_SECRET", "daemon-value")
	if sandbox.Default() != nil {
		t.Fatal("a sandbox manager is configured; this test proves run steps need none")
	}
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "deliver/push.sh", `cat > "$PWD/stdin.json"
env | sort > "$PWD/env.txt"
echo "$TMPDIR" > "$PWD/tmpdir.txt"
touch "$TMPDIR/scratch-file"
id -u > "$PWD/uid.txt"
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
		"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + os.Getenv("HOME")} {
		if !strings.Contains(env, want+"\n") {
			t.Fatalf("env lacks %s:\n%s", want, env)
		}
	}
	// Only the minimal environment, the SWARM_* variables and what the
	// shell itself sets.
	shell := map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true, "PWD": true, "OLDPWD": true, "SHLVL": true, "_": true}
	for _, line := range strings.Split(strings.TrimSpace(env), "\n") {
		name, _, _ := strings.Cut(line, "=")
		if !shell[name] && !strings.HasPrefix(name, "SWARM_") || name == "SWARM_DAEMON_ONLY_SECRET" {
			t.Fatalf("env has %s from outside the step:\n%s", name, env)
		}
	}
	scratch := strings.TrimSpace(readFile(t, filepath.Join(folder, "tmpdir.txt")))
	if scratch == "" || scratch == os.TempDir() || !strings.Contains(env, "TMPDIR="+scratch+"\n") {
		t.Fatalf("TMPDIR %q is not a per-step scratch folder", scratch)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("the step's scratch folder %s was not removed: %v", scratch, err)
	}
	if uid := strings.TrimSpace(readFile(t, filepath.Join(folder, "uid.txt"))); uid != strconv.Itoa(os.Getuid()) {
		t.Fatalf("the step ran as uid %s, not the daemon's user %d", uid, os.Getuid())
	}
	if len(h.host.ran) != 1 || h.host.ran[0].Folder != folder || h.host.ran[0].Args[0] != "--draft" {
		t.Fatalf("run requests = %+v", h.host.ran)
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
	h.host.before = func(req RunRequest) error {
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

	h.host.before = nil
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

	// A follow-up that committed a new head delivers again (one delivery per
	// automation, job and head: see delivery_rounds_test.go).
	h.results.results["run-1"].Git.Head = strings.Repeat("c", 40)
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
	if d.State != StateStopped || d.Steps[0].State != StateStopped || d.Steps[0].Attempts != 0 || len(h.host.ran) != 0 {
		t.Fatalf("stopped delivery = %+v (ran %d)", d, len(h.host.ran))
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
	if h.results.calls != 0 || len(h.deliveries("worker/coder-1/run-1")) != 0 || len(h.host.ran) != 0 || len(h.agents.started) != 0 {
		t.Fatalf("a job end without automations did something: results %d runs %d agents %d", h.results.calls, len(h.host.ran), len(h.agents.started))
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
