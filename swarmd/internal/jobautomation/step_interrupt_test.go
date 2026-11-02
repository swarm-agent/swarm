package jobautomation

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/signals"
)

// killingHost is the real HostExecutor; while kill is set, the first time a
// script prints "pid=<n>" it sends that process SIGTERM, as a service stop
// or an operator would from outside the step. It records what the runner
// counted and saved at that moment.
type killingHost struct {
	runner *Runner
	store  *Store
	kill   bool

	mu      sync.Mutex
	killed  int
	running int
	saved   string
}

var pidLine = regexp.MustCompile(`pid=(\d+)`)

func (k *killingHost) Run(ctx context.Context, req RunRequest) (RunOutcome, error) {
	next := req.OnOutput
	var seen strings.Builder
	req.OnOutput = func(p []byte) {
		next(p)
		k.mu.Lock()
		defer k.mu.Unlock()
		seen.Write(p)
		m := pidLine.FindStringSubmatch(seen.String())
		if !k.kill || m == nil || k.killed > 0 {
			return
		}
		k.running = k.runner.RunningScripts()
		k.saved = k.store.StepLog(req.Env["SWARM_DELIVERY_ID"], "gate")
		pid, _ := strconv.Atoi(m[1])
		if err := syscall.Kill(pid, syscall.SIGTERM); err == nil {
			k.killed++
		}
	}
	return HostExecutor{}.Run(ctx, req)
}

// useExecutor rebuilds the harness runner around run, as build does.
func (h *harness) useExecutor(run RunExecutor) {
	head := func(_ context.Context, _ string, r Result) (string, error) { return "refs/swarm/jobs/" + r.RunID, nil }
	h.runner = NewRunner(Options{Store: h.store, Feed: h.feed, Account: func() string { return testAccount }, Results: h.results,
		Run: run, Agent: h.agents, Emit: signals.NewEmitter(h.feed).Emit, Head: head})
	h.runner.backoff = func(int) time.Duration { return 0 }
}

const gateScript = "echo \"pid=$$\"\necho \"running the suite\"\nexec sleep 30\n"

// Requirement: a run step whose script Swarm saw killed by a stop signal
// (SIGTERM here) that its time limit did not send ends error with
// error_kind interrupted: the exit code 143 is not the script's verdict on
// the work. The delivery stops on it (ends error, a later push never runs),
// the reason names the signal, the exit code and the script's last output,
// the log tail keeps what the script printed (saved while it ran, before
// the kill) with Swarm's note, and the step was counted as a running
// script while it ran.
// Threat (seen on a forge: a gate ended "exited with code 143" with no
// output): an infrastructure stop read as a failed test, so `if status
// failed` fix steps send a coder to fix code that did not fail, or a
// continue-on-failure path pushes over a check that never finished.
// Owners: HostExecutor.Run (RunOutcome.Signal), Runner.runScript
// (interruptSignal, stepLogWriter, RunningScripts) and Runner.runStep.
// Layer: a real script killed by a real signal through the real runner
// and store: the narrowest layer where the wait status is observed.
func TestRunnerHostStepKilledBySignalIsInterruptedNotFailed(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "gate.sh", gateScript)
	writeScript(t, folder, "push.sh", "echo pushed > \"$PWD/push.ran\"\n")
	killer := &killingHost{store: h.store, kill: true}
	h.useExecutor(killer)
	killer.runner = h.runner
	h.process()
	h.automation(Automation{Name: "gate then push", Scope: Scope{Folder: folder}, Enabled: true,
		Steps: []Step{{ID: "gate", Type: StepRun, Command: "gate.sh", TimeoutSeconds: 20}, {ID: "push", Type: StepRun, Command: "push.sh"}}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()

	if killer.killed != 1 {
		t.Fatalf("the gate was not killed (killed=%d)", killer.killed)
	}
	if killer.running != 1 {
		t.Fatalf("RunningScripts while the gate ran = %d, want 1", killer.running)
	}
	if got := h.runner.RunningScripts(); got != 0 {
		t.Fatalf("RunningScripts after the gate = %d, want 0", got)
	}
	if !strings.Contains(killer.saved, "pid=") {
		t.Fatalf("the running gate's log was not saved before it ended: %q", killer.saved)
	}
	d := h.deliveries("worker/coder-1/run-1")[0]
	gate, push := d.Steps[0], d.Steps[1]
	if gate.State != StateError || gate.ErrorKind != ErrorKindInterrupted {
		t.Fatalf("gate = %s/%s (%s), want error/interrupted", gate.State, gate.ErrorKind, gate.Reason)
	}
	if gate.ExitCode == nil || *gate.ExitCode != 143 {
		t.Fatalf("gate exit code = %v, want 143", gate.ExitCode)
	}
	for _, want := range []string{"killed by SIGTERM (exit code 143)", "not by its time limit", "last output: running the suite"} {
		if !strings.Contains(gate.Reason, want) {
			t.Fatalf("gate reason %q lacks %q", gate.Reason, want)
		}
	}
	if gate.Next != interruptedNext || strings.Contains(gate.Next, "fix the script") {
		t.Fatalf("gate next = %q", gate.Next)
	}
	if d.State != StateError || push.State != StateSkipped {
		t.Fatalf("delivery %s, push %s: an interrupted gate must stop the delivery as error", d.State, push.State)
	}
	if _, err := os.Stat(filepath.Join(folder, "push.ran")); !os.IsNotExist(err) {
		t.Fatalf("push ran after an interrupted gate (%v)", err)
	}
	logTail := h.store.StepLog(d.ID, "gate")
	for _, want := range []string{"running the suite", "swarm: the script was killed by SIGTERM (exit code 143)"} {
		if !strings.Contains(logTail, want) {
			t.Fatalf("gate log %q lacks %q", logTail, want)
		}
	}
	if errs := h.signalsOf("automation.step.error"); len(errs) != 1 || errs[0].Attrs["error_kind"] != ErrorKindInterrupted {
		t.Fatalf("step error signals = %+v", errs)
	}
	if failed := h.signalsOf("automation.step.failed"); len(failed) != 0 {
		t.Fatalf("an interrupted gate signalled failed: %+v", failed)
	}
}

// Requirement: an interrupted run step is retried by the workflow's own
// configured retries, like any infrastructure error: the next attempt runs
// the script once more, and the later push runs exactly once.
// Threat: no recovery from a transient stop, or a duplicate push.
// Owners: Runner.runStep (retry of a StateError without refused).
// Layer: real scripts through the real runner.
func TestRunnerInterruptedStepUsesConfiguredRetriesWithoutDuplicates(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "gate.sh", "echo x >> \"$PWD/gate.count\"\nif [ \"$(wc -l < \"$PWD/gate.count\")\" -eq 1 ]; then\n"+gateScript+"fi\necho suite passed\n")
	writeScript(t, folder, "push.sh", "echo x >> \"$PWD/push.count\"\n")
	killer := &killingHost{store: h.store, kill: true}
	h.useExecutor(killer)
	killer.runner = h.runner
	h.process()
	h.automation(Automation{Name: "gate then push", Scope: Scope{Folder: folder}, Enabled: true, Retries: 1,
		Steps: []Step{{ID: "gate", Type: StepRun, Command: "gate.sh", TimeoutSeconds: 20}, {ID: "push", Type: StepRun, Command: "push.sh"}}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	h.process()

	d := h.deliveries("worker/coder-1/run-1")[0]
	if d.State != StateSucceeded || d.Steps[0].State != StateSucceeded || d.Steps[0].Attempts != 2 {
		t.Fatalf("delivery %s gate %s attempts %d", d.State, d.Steps[0].State, d.Steps[0].Attempts)
	}
	if len(d.Steps[0].Failures) != 1 || !strings.Contains(d.Steps[0].Failures[0].Reason, "SIGTERM") {
		t.Fatalf("gate failures = %+v", d.Steps[0].Failures)
	}
	if got := readFile(t, filepath.Join(folder, "gate.count")); got != "x\nx\n" {
		t.Fatalf("gate ran %d times, want 2", strings.Count(got, "x"))
	}
	if got := readFile(t, filepath.Join(folder, "push.count")); got != "x\n" {
		t.Fatalf("push ran %d times, want 1", strings.Count(got, "x"))
	}
}

// Requirement: a script that exits by itself stays failed, whatever its
// code: exit 1 keeps its reason unchanged; exit 143 (no signal observed)
// stays failed with "fix the script" but says Swarm cannot tell whether
// the script chose 143 or something stopped it; a script that crashes on
// its own signal (SIGSEGV) stays failed and names the signal.
// Threat: every 143 classified as infrastructure (a real test failure
// hidden from its fix step), or the ordinary failure text changed.
// Owners: Runner.runScript (signalExitHint, interruptSignal).
// Layer: real scripts through the real runner.
func TestRunnerOrdinaryScriptExitsStayFailed(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "one.sh", "echo boom\nexit 1\n")
	writeScript(t, folder, "own143.sh", "echo suite failed\nexit 143\n")
	writeScript(t, folder, "crash.sh", "ulimit -c 0\necho crashing\nkill -SEGV $$\n")
	h.process()
	h.automation(Automation{Name: "checks", Scope: Scope{Folder: folder}, Enabled: true,
		Steps: []Step{
			{ID: "one", Type: StepRun, Command: "one.sh", ContinueOnFailure: true},
			{ID: "own143", Type: StepRun, Command: "own143.sh", ContinueOnFailure: true},
			{ID: "crash", Type: StepRun, Command: "crash.sh", ContinueOnFailure: true},
		}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()

	d := h.deliveries("worker/coder-1/run-1")[0]
	for _, step := range d.Steps {
		if step.State != StateFailed || step.ErrorKind != "" || step.Next != "fix the script" {
			t.Fatalf("step %s = %s/%q next %q (%s), want failed", step.ID, step.State, step.ErrorKind, step.Next, step.Reason)
		}
	}
	if got := d.Steps[0].Reason; got != "exited with code 1: boom" {
		t.Fatalf("exit 1 reason = %q", got)
	}
	if got := d.Steps[1].Reason; !strings.HasPrefix(got, "exited with code 143 (143 is also what a process killed by SIGTERM returns") ||
		!strings.Contains(got, "cannot tell") || !strings.HasSuffix(got, ": suite failed") {
		t.Fatalf("exit 143 reason = %q", got)
	}
	if got := d.Steps[2].Reason; got != "killed by SIGSEGV (exit code 139): crashing" {
		t.Fatalf("crash reason = %q", got)
	}
	if d.State != StateFailed {
		t.Fatalf("delivery = %s", d.State)
	}
}

// Requirement: when the daemon stops (or crashes) during a run step, the
// output the script streamed is already saved; after the restart the step
// runs again once, its history says the attempt before was interrupted
// (with its last saved output), and its log keeps the interrupted
// attempt's output before the new one's.
// Threat: a restart erasing the only evidence of why a gate stopped, or an
// interrupted attempt hidden from the step's history.
// Owners: stepLogWriter (saved while running), Runner.runStep (resume of a
// run step left running: interruptedResumeReason, resumedLog).
// Layer: the real runner over a real store reopened as a daemon start does.
func TestRunnerKeepsInterruptedAttemptLogAcrossRestart(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "gate.sh", "echo x >> \"$PWD/gate.count\"\necho second attempt passed\n")
	h.process()
	h.automation(Automation{Name: "gate", Scope: Scope{Folder: folder}, Enabled: true,
		Steps: []Step{{ID: "gate", Type: StepRun, Command: "gate.sh"}}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")

	// The first attempt streams output, then the daemon stops under it.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var savedWhileRunning string
	h.host.before = func(req RunRequest) error {
		req.OnOutput([]byte("compiling package one\n"))
		savedWhileRunning = h.store.StepLog(req.Env["SWARM_DELIVERY_ID"], "gate")
		cancel()
		return context.Canceled
	}
	if err := h.runner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	h.runner.dispatch(ctx, true)
	if !strings.Contains(savedWhileRunning, "compiling package one") {
		t.Fatalf("the log was not saved while the step ran: %q", savedWhileRunning)
	}
	d := h.deliveries("worker/coder-1/run-1")[0]
	if d.Steps[0].State != StateRunning || Terminal(d.State) {
		t.Fatalf("before restart: %s %s", d.State, d.Steps[0].State)
	}

	h.host.before = nil
	h.restart()
	h.process()
	d = h.deliveries("worker/coder-1/run-1")[0]
	gate := d.Steps[0]
	if d.State != StateSucceeded || gate.State != StateSucceeded || gate.Attempts != 2 {
		t.Fatalf("after restart: delivery %s gate %s attempts %d", d.State, gate.State, gate.Attempts)
	}
	if len(gate.Failures) != 1 || gate.Failures[0].Attempt != 1 ||
		!strings.Contains(gate.Failures[0].Reason, "attempt 1 did not finish") ||
		!strings.Contains(gate.Failures[0].Reason, "last saved output: compiling package one") {
		t.Fatalf("gate failures = %+v", gate.Failures)
	}
	logTail := h.store.StepLog(d.ID, "gate")
	first := strings.Index(logTail, "compiling package one")
	marker := strings.Index(logTail, "swarm: attempt 1 did not finish (the daemon stopped while it ran); attempt 2 starts here")
	second := strings.Index(logTail, "second attempt passed")
	if first < 0 || marker < first || second < marker {
		t.Fatalf("gate log = %q", logTail)
	}
	if got := readFile(t, filepath.Join(folder, "gate.count")); got != "x\n" {
		t.Fatalf("the script ran %d times after the restart, want 1", strings.Count(got, "x"))
	}
}

// Requirement: a running step's log is saved on its first output and then
// at most once per stepLogFlush, on write (no timer), always with the
// whole bounded tail.
// Threat: a write per output line to the store, or a quiet interval whose
// output is never saved before a crash.
// Owner: stepLogWriter. Layer: the writer with a fixed clock.
func TestStepLogWriterSavesWhileRunningAtMostEveryFlush(t *testing.T) {
	now := time.Unix(1000, 0)
	var saves []string
	w := &stepLogWriter{tail: &tailBuffer{max: MaxLogTailBytes}, now: func() time.Time { return now },
		save: func(p []byte) error { saves = append(saves, string(p)); return nil }}
	w.write([]byte("a\n"))
	now = now.Add(stepLogFlush / 2)
	w.write([]byte("b\n"))
	now = now.Add(stepLogFlush / 2)
	w.write([]byte("c\n"))
	if len(saves) != 2 || saves[0] != "a\n" || saves[1] != "a\nb\nc\n" {
		t.Fatalf("saves = %q", saves)
	}
	if got := string(w.tail.bytes()); got != "a\nb\nc\n" {
		t.Fatalf("tail = %q", got)
	}
}

// Requirement: a resumed run step whose earlier attempt's saved log fills
// the whole tail (MaxLogTailBytes) still logs the new attempt's output
// exactly once, and when the new attempt is killed by a stop signal its
// reason keeps "last output: ..." from the new attempt's own output.
// Threat (review of the resume change): runScript compared the tail's
// length with its length after the resumed log; a full tail never changes
// length, so the executor's copy of the output was written again (output
// twice in the log) and the interrupted reason lost its last output — the
// case of a long gate interrupted twice.
// Owners: Runner.runScript (stepLogWriter.written, scriptLast) and
// Runner.runStep (resumedLog).
// Layer: the real runner and store, reopened as a daemon start does, with
// a real script killed by a real signal.
func TestRunnerResumedStepWithFullSavedLogLogsOutputOnceAndKeepsLastOutput(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "gate.sh", "echo \"second attempt line\"\necho \"pid=$$\"\nexec sleep 30\n")
	h.process()
	h.automation(Automation{Name: "gate", Scope: Scope{Folder: folder}, Enabled: true,
		Steps: []Step{{ID: "gate", Type: StepRun, Command: "gate.sh", TimeoutSeconds: 20}}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")

	// The first attempt streams more than the whole tail, then the daemon
	// stops under it.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	big := strings.Repeat("first attempt filler line\n", MaxLogTailBytes/26+100)
	h.host.before = func(req RunRequest) error {
		req.OnOutput([]byte(big))
		cancel()
		return context.Canceled
	}
	if err := h.runner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	h.runner.dispatch(ctx, true)
	d := h.deliveries("worker/coder-1/run-1")[0]
	if saved := h.store.StepLog(d.ID, "gate"); len(saved) != MaxLogTailBytes {
		t.Fatalf("the first attempt's saved log is %d bytes, want the full tail %d", len(saved), MaxLogTailBytes)
	}

	h.host.before = nil
	h.restart()
	killer := &killingHost{store: h.store, kill: true}
	h.useExecutor(killer)
	killer.runner = h.runner
	h.process()

	if killer.killed != 1 {
		t.Fatalf("the resumed gate was not killed (killed=%d)", killer.killed)
	}
	d = h.deliveries("worker/coder-1/run-1")[0]
	gate := d.Steps[0]
	if gate.Attempts != 2 || gate.State != StateError || gate.ErrorKind != ErrorKindInterrupted {
		t.Fatalf("gate = %s/%s attempts %d (%s), want error/interrupted on attempt 2", gate.State, gate.ErrorKind, gate.Attempts, gate.Reason)
	}
	if !strings.Contains(gate.Reason, "last output: pid=") {
		t.Fatalf("the resumed attempt's reason lost its last output: %q", gate.Reason)
	}
	logTail := h.store.StepLog(d.ID, "gate")
	if n := strings.Count(logTail, "second attempt line"); n != 1 {
		t.Fatalf("the resumed attempt's output is in its log %d times, want 1: %q", n, logTail[max(0, len(logTail)-400):])
	}
	marker := strings.Index(logTail, "attempt 2 starts here")
	line := strings.Index(logTail, "second attempt line")
	note := strings.Index(logTail, "swarm: the script was killed by SIGTERM")
	if marker < 0 || line < marker || note < line {
		t.Fatalf("gate log order wrong (marker %d, output %d, note %d)", marker, line, note)
	}
	if len(logTail) > MaxLogTailBytes {
		t.Fatalf("gate log is %d bytes, over the tail bound %d", len(logTail), MaxLogTailBytes)
	}
}
