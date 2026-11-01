package jobautomation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Requirement: a run step may say where its script runs with in: host (the
// default, stored as "") or in: environment. With in: environment the
// command is a clean path of a script in the job's head, relative to the
// repository root; an absolute path, one with .., one under .git, an
// unknown in, and in on any other step type are refused when the workflow is
// saved. Host run steps keep their shape and validation, and only they need
// the machine's owner (HasRunStep).
// Threat: an environment step that names a host file or leaves the
// checkout, a typo that silently runs on the host, or a sandboxed check that
// needlessly needs the machine owner's admin key.
// Owners: NormalizeSteps / Step.normalizeRun, ValidEnvironmentCommand and
// HasRunStep. Definition validation is the narrowest layer.
func TestEnvironmentRunStepValidation(t *testing.T) {
	accepted := []Step{
		{ID: "console", Type: "run", In: "environment", Command: "scripts/check-console.sh", ContinueOnFailure: true},
		{ID: "args", Type: "run", In: " Environment ", Command: "check.sh", Args: []string{"--fast"}},
	}
	for _, step := range accepted {
		steps := []Step{step}
		if err := NormalizeSteps(steps); err != nil {
			t.Fatalf("%+v refused: %v", step, err)
		}
		if steps[0].In != RunInEnvironment {
			t.Fatalf("in normalized to %q", steps[0].In)
		}
		if HasRunStep(steps) {
			t.Fatalf("an environment step counts as a host run step: %+v", steps[0])
		}
	}
	host := []Step{{ID: "push", Type: "run", In: "host", Command: "deliver/push.sh"}}
	if err := NormalizeSteps(host); err != nil || host[0].In != "" {
		t.Fatalf("in: host = %q, %v; want the default stored as \"\"", host[0].In, err)
	}
	if !HasRunStep(host) || !HasRunStep([]Step{{ID: "abs", Type: "run", Command: "/usr/local/bin/push"}}) {
		t.Fatal("a host run step no longer needs the machine's owner")
	}
	refused := []struct {
		step Step
		want string
	}{
		{Step{ID: "a", Type: "run", In: "environment", Command: "/usr/local/bin/check.sh"}, "is absolute"},
		{Step{ID: "b", Type: "run", In: "environment", Command: "../outside.sh"}, "leaves the repository"},
		{Step{ID: "c", Type: "run", In: "environment", Command: "scripts/../../x.sh"}, "clean path"},
		{Step{ID: "d", Type: "run", In: "environment", Command: ".git/hooks/pre-commit"}, "under .git"},
		{Step{ID: "e", Type: "run", In: "environment", Command: "./check.sh"}, "clean path"},
		{Step{ID: "f", Type: "run", In: "environment"}, "command is required"},
		{Step{ID: "g", Type: "run", In: "container", Command: "check.sh"}, "in must be host"},
		{Step{ID: "h", Type: "agent", In: "environment", Worker: "coder", Brief: "fix it"}, "in is only for a run step"},
		{Step{ID: "i", Type: "reopen", In: "host", Message: "again"}, "in is only for a run step"},
	}
	for _, c := range refused {
		err := NormalizeSteps([]Step{c.step})
		if err == nil || !IsValidationError(err) || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%+v: err = %v, want a validation error with %q", c.step, err, c.want)
		}
	}
}

// fakeLeases is a lease runner for EnvironmentExecutor tests: each lease is
// a home directory of its own, and commands run as local processes with only
// PATH, HOME and the command's own variables (the real lease is a container
// of the folder's environment; sandbox tests own its isolation). It records
// every take, command and release.
type fakeLeases struct {
	t        *testing.T
	mu       sync.Mutex
	busy     int   // takes answered busy before one succeeds; -1 forever
	takeErr  error // a final refusal (no environment)
	takes    int
	homes    map[string]string
	commands []LeaseCommand
	released []string
	// releaseCtxErr is the release context's error when it was called.
	releaseCtxErr []error
	// started is closed when the step's script (not the prepare) starts.
	started chan struct{}
	// env, when set, adds variables to every command, given the lease's home.
	env func(home string) []string
}

func newFakeLeases(t *testing.T) *fakeLeases {
	return &fakeLeases{t: t, homes: map[string]string{}, started: make(chan struct{})}
}

func (f *fakeLeases) TakeStepLease(_ context.Context, folder, owner string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.takes++
	if f.takeErr != nil {
		return "", f.takeErr
	}
	if f.busy < 0 || f.takes <= f.busy {
		return "", fmt.Errorf("%w (2 of 2 in use)", ErrEnvironmentBusy)
	}
	id := fmt.Sprintf("lease-%d", f.takes)
	home := filepath.Join(f.t.TempDir(), id)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	f.homes[id] = home
	return id, nil
}

func (f *fakeLeases) ExecLease(ctx context.Context, lease, owner string, cmd LeaseCommand) (LeaseResult, error) {
	f.mu.Lock()
	home, ok := f.homes[lease]
	f.commands = append(f.commands, cmd)
	script := len(cmd.Argv) > 3 && cmd.Argv[3] == "swarm-step"
	f.mu.Unlock()
	if !ok {
		return LeaseResult{}, errors.New("no such lease " + lease)
	}
	if script {
		close(f.started)
	}
	runCtx, cancel := context.WithTimeout(ctx, cmd.Timeout)
	defer cancel()
	c := exec.CommandContext(runCtx, cmd.Argv[0], cmd.Argv[1:]...)
	c.Dir = cmd.Dir
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1"}
	if f.env != nil {
		c.Env = append(c.Env, f.env(home)...)
	}
	for name, value := range cmd.Env {
		c.Env = append(c.Env, name+"="+value)
	}
	c.Stdin = cmd.Stdin
	w := writerFunc(func(p []byte) (int, error) {
		if cmd.OnOutput != nil {
			cmd.OnOutput(append([]byte(nil), p...))
		}
		return len(p), nil
	})
	c.Stdout, c.Stderr = w, w
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	c.WaitDelay = 2 * time.Second
	err := c.Run()
	if ctx.Err() != nil {
		return LeaseResult{}, ctx.Err()
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return LeaseResult{ExitCode: 124, TimedOut: true}, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return LeaseResult{ExitCode: exitErr.ExitCode()}, nil
	}
	return LeaseResult{}, err
}

func (f *fakeLeases) ReleaseLease(ctx context.Context, lease, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, lease)
	f.releaseCtxErr = append(f.releaseCtxErr, ctx.Err())
	if home, ok := f.homes[lease]; ok {
		delete(f.homes, lease)
		return os.RemoveAll(home)
	}
	return nil
}

type writerFunc func([]byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

// jobRepo is a folder whose repository has a dev branch and a job worktree
// on its own branch: Head is the job's committed head; the worktree also
// holds uncommitted changes that are not the head.
type jobRepo struct {
	Folder, Worktree, Head, Dev string
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Swarm Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeRepoFile(t *testing.T, dir, name, body string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// checkScript prints what the step sees, tries to change its checkout, and
// exits 3.
const checkScript = `#!/bin/sh
echo "head=$(git rev-parse HEAD)"
echo "app=$(cat app.txt)"
echo "base=$(git merge-base origin/dev HEAD)"
echo "job=$SWARM_JOB_ID delivery=$SWARM_DELIVERY_ID round=$SWARM_ROUND"
echo "daemon=${DAEMON_ONLY:-unset}"
echo "args=$*"
echo "stdin=$(cat)"
echo "changed by the step" > app.txt
touch step-made-this
echo "last line"
exit 3
`

func newJobRepo(t *testing.T) jobRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	root := t.TempDir()
	folder := filepath.Join(root, "folder")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, folder, "init", "-q", "-b", "dev")
	writeRepoFile(t, folder, "app.txt", "dev content\n", 0o644)
	runGit(t, folder, "add", "-A")
	runGit(t, folder, "commit", "-qm", "base")
	dev := runGit(t, folder, "rev-parse", "HEAD")
	worktree := filepath.Join(root, "worktrees", "run-1")
	runGit(t, folder, "worktree", "add", "-q", "-b", "agent/run-1", worktree)
	writeRepoFile(t, worktree, "app.txt", "head content\n", 0o644)
	writeRepoFile(t, worktree, "scripts/check.sh", checkScript, 0o755)
	writeRepoFile(t, worktree, "scripts/plain.sh", "#!/bin/sh\necho plain\n", 0o644)
	writeRepoFile(t, worktree, "scripts/slow.sh", "#!/bin/sh\necho started\nsleep 30\n", 0o755)
	writeRepoFile(t, worktree, "scripts/ok.sh", "#!/bin/sh\necho all good\n", 0o755)
	runGit(t, worktree, "add", "-A")
	runGit(t, worktree, "commit", "-qm", "job work")
	head := runGit(t, worktree, "rev-parse", "HEAD")
	// The live worktree differs from the head: the step must not see it.
	writeRepoFile(t, worktree, "app.txt", "uncommitted worktree content\n", 0o644)
	writeRepoFile(t, worktree, "scripts/check.sh", "#!/bin/sh\necho worktree script\n", 0o755)
	return jobRepo{Folder: folder, Worktree: worktree, Head: head, Dev: dev}
}

// repoState is everything a step must not change: the folder's refs and
// status, and the worktree's files and status.
func repoState(t *testing.T, r jobRepo) string {
	t.Helper()
	files := []string{}
	for _, dir := range []string{r.Folder, r.Worktree} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			files = append(files, filepath.Join(filepath.Base(dir), e.Name()))
		}
	}
	sort.Strings(files)
	return strings.Join([]string{
		runGit(t, r.Folder, "for-each-ref"),
		runGit(t, r.Folder, "status", "--porcelain"),
		runGit(t, r.Worktree, "status", "--porcelain"),
		runGit(t, r.Worktree, "rev-parse", "HEAD"),
		readFile(t, filepath.Join(r.Worktree, "app.txt")),
		strings.Join(files, ","),
	}, "\n")
}

func envRequest(r jobRepo, command string, timeout time.Duration) RunRequest {
	return RunRequest{
		Folder: r.Folder, Command: command, Args: []string{"--fast", "two words"},
		Env: map[string]string{
			"SWARM_HEAD": r.Head, "SWARM_JOB_ID": "worker/coder-1/run-1", "SWARM_DELIVERY_ID": "dlv-1", "SWARM_ROUND": "1",
			"DAEMON_ONLY": "must not pass",
		},
		Stdin: []byte(`{"job_id":"worker/coder-1/run-1"}`), Timeout: timeout, Owner: "workflow-step/dlv-1/console/1",
	}
}

// Requirement: a run step with in: environment runs its script from a fresh
// checkout of exactly the job's head (SWARM_HEAD) in a lease of the folder's
// environment, never the live job worktree or the folder, with the folder's
// branches as origin/* (so git merge-base origin/dev HEAD works), the
// step's SWARM_* variables (no other variable of the request), its args and
// the job's result on stdin. Its output and exit code are the step's; the
// lease is released afterwards; nothing it does changes the folder's
// repository or the job's worktree.
// Threat: a check that passes on uncommitted worktree files, a script from
// an agent-written branch that changes the folder or the job's worktree, a
// leaked daemon variable, or a lease never released.
// Owners: EnvironmentExecutor.Run (prepareScript, runStepScript,
// stepVariables) over the EnvironmentLeases contract. A fake lease runner
// with a real Git repository is the narrowest layer that proves what the
// checkout is; the sandbox package owns the lease's container isolation.
func TestEnvironmentExecutorRunsTheHeadInAFreshCheckout(t *testing.T) {
	r := newJobRepo(t)
	before := repoState(t, r)
	leases := newFakeLeases(t)
	var streamed strings.Builder
	req := envRequest(r, "scripts/check.sh", 30*time.Second)
	req.OnOutput = func(p []byte) { streamed.Write(p) }
	out, err := EnvironmentExecutor{Leases: leases}.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out.Output)
	}
	got := string(out.Output)
	if out.ExitCode != 3 {
		t.Fatalf("exit code = %d, want the script's 3\n%s", out.ExitCode, got)
	}
	for _, want := range []string{
		"head=" + r.Head, "app=head content", "base=" + r.Dev,
		"job=worker/coder-1/run-1 delivery=dlv-1 round=1", "daemon=unset", "args=--fast two words",
		`stdin={"job_id":"worker/coder-1/run-1"}`, "last line",
	} {
		if !strings.Contains(got, want+"\n") {
			t.Fatalf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "worktree script") || strings.Contains(got, "uncommitted") {
		t.Fatalf("the step saw the live worktree:\n%s", got)
	}
	if streamed.String() != got {
		t.Fatalf("streamed output differs from the outcome:\n%q\n%q", streamed.String(), got)
	}
	if after := repoState(t, r); after != before {
		t.Fatalf("the step changed the folder or the worktree:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if leases.takes != 1 || len(leases.released) != 1 || leases.released[0] != "lease-1" || leases.releaseCtxErr[0] != nil {
		t.Fatalf("takes = %d, released = %v (%v)", leases.takes, leases.released, leases.releaseCtxErr)
	}
	if len(leases.homes) != 0 {
		t.Fatalf("lease homes left: %v", leases.homes)
	}
	for _, cmd := range leases.commands {
		if cmd.Dir != r.Folder {
			t.Fatalf("command ran in %q, want the folder (read-only in the lease)", cmd.Dir)
		}
		for name := range cmd.Env {
			if !strings.HasPrefix(name, "SWARM_") {
				t.Fatalf("variable %s passed into the lease", name)
			}
		}
		if cmd.Env["SWARM_HEAD"] != r.Head {
			t.Fatalf("SWARM_HEAD = %q", cmd.Env["SWARM_HEAD"])
		}
	}
}

// Requirement: an environment step has the host step's time limit: a script
// still running at timeout_seconds is killed, the step exits 124 with a
// line saying so, and the lease is released; a daemon stopping mid-script
// returns the cancellation (the step is resumed, not recorded) and still
// releases the lease, with a context that is not cancelled.
// Threat: a hung check holding a lease forever, or a shutdown leaking one.
// Owners: EnvironmentExecutor.Run (deadline, timedOut, stopped, release).
func TestEnvironmentExecutorTimeoutAndShutdownReleaseTheLease(t *testing.T) {
	r := newJobRepo(t)
	leases := newFakeLeases(t)
	start := time.Now()
	out, err := EnvironmentExecutor{Leases: leases}.Run(context.Background(), envRequest(r, "scripts/slow.sh", 2*time.Second))
	if err != nil {
		t.Fatalf("timeout: %v", err)
	}
	if out.ExitCode != timeoutExitCode || !strings.Contains(string(out.Output), "started") || !strings.Contains(string(out.Output), "timed out after 2s") {
		t.Fatalf("timeout outcome = %d\n%s", out.ExitCode, out.Output)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("the timeout took %s", elapsed)
	}
	if len(leases.released) != 1 {
		t.Fatalf("released after timeout = %v", leases.released)
	}

	leases = newFakeLeases(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := EnvironmentExecutor{Leases: leases}.Run(ctx, envRequest(r, "scripts/slow.sh", time.Minute))
		done <- err
	}()
	select {
	case <-leases.started:
	case <-time.After(30 * time.Second):
		t.Fatal("the script never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown err = %v, want context.Canceled", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the step did not stop with the daemon")
	}
	if len(leases.released) != 1 || leases.releaseCtxErr[0] != nil {
		t.Fatalf("released after shutdown = %v (%v)", leases.released, leases.releaseCtxErr)
	}
}

// Requirement: while every lease of the folder's environment is in use, an
// environment step waits for one within its time limit (saying so in its
// output), and if none frees up ends as a start timeout (its script never
// started: StartError start_timeout, exit 124); a folder with no
// environment, or one that is not ready, is refused at once with the reason
// (kind lease: nothing ran); a script missing from the head or committed
// without its executable bit is refused with the fix (kinds script_missing,
// script_unusable), and its lease is released.
// Threat: a step hanging on a folder without an environment, a busy folder
// failing a check at once, or a confusing failure for a missing script.
// Owners: EnvironmentExecutor.takeLease and prepareFailure.
func TestEnvironmentExecutorLeaseWaitAndRefusals(t *testing.T) {
	r := newJobRepo(t)

	leases := newFakeLeases(t)
	leases.busy = 2
	out, err := EnvironmentExecutor{Leases: leases, Poll: 10 * time.Millisecond}.Run(context.Background(), envRequest(r, "scripts/ok.sh", 30*time.Second))
	if err != nil || out.ExitCode != 0 || !strings.Contains(string(out.Output), "waiting for a free lease") || !strings.Contains(string(out.Output), "all good") || leases.takes != 3 {
		t.Fatalf("busy then free: %v, exit %d, takes %d\n%s", err, out.ExitCode, leases.takes, out.Output)
	}

	leases = newFakeLeases(t)
	leases.busy = -1
	out, err = EnvironmentExecutor{Leases: leases, Poll: 10 * time.Millisecond}.Run(context.Background(), envRequest(r, "scripts/ok.sh", time.Second))
	var start *StartError
	if !errors.As(err, &start) || start.Kind != ErrorKindStartTimeout || out.ExitCode != timeoutExitCode || !strings.Contains(string(out.Output), "waiting for a free lease") || !strings.Contains(string(out.Output), "never started") || len(leases.commands) != 0 || len(leases.released) != 0 {
		t.Fatalf("busy forever: %v, exit %d, commands %d, released %v\n%s", err, out.ExitCode, len(leases.commands), leases.released, out.Output)
	}

	leases = newFakeLeases(t)
	leases.takeErr = errors.New("the environment of the folder is failed, not ready: build failed")
	_, err = EnvironmentExecutor{Leases: leases}.Run(context.Background(), envRequest(r, "scripts/ok.sh", 30*time.Second))
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Kind != ErrorKindLease || !strings.Contains(refused.Reason, "cannot be leased") || !strings.Contains(refused.Reason, "build failed") || len(leases.commands) != 0 {
		t.Fatalf("no environment: %v, commands %d", err, len(leases.commands))
	}

	for _, c := range []struct{ command, want, kind string }{
		{"scripts/missing.sh", "does not exist in the job's head", ErrorKindScriptMissing},
		{"scripts/plain.sh", "is not executable in the job's head", ErrorKindScriptUnusable},
		{"scripts", "is not a regular file", ErrorKindScriptUnusable},
	} {
		leases = newFakeLeases(t)
		_, err = EnvironmentExecutor{Leases: leases}.Run(context.Background(), envRequest(r, c.command, 30*time.Second))
		if !errors.As(err, &refused) || refused.Kind != c.kind || !strings.Contains(refused.Reason, c.want) {
			t.Fatalf("%s: err = %v, want refusal %q", c.command, err, c.want)
		}
		if len(leases.commands) != 1 || len(leases.released) != 1 {
			t.Fatalf("%s: commands %d (want only the prepare), released %v", c.command, len(leases.commands), leases.released)
		}
	}

	for _, req := range []RunRequest{
		func() RunRequest { q := envRequest(r, "/bin/sh", time.Second); return q }(),
		func() RunRequest {
			q := envRequest(r, "scripts/ok.sh", time.Second)
			q.Env["SWARM_HEAD"] = ""
			return q
		}(),
	} {
		leases = newFakeLeases(t)
		if _, err := (EnvironmentExecutor{Leases: leases}).Run(context.Background(), req); !errors.As(err, &refused) || leases.takes != 0 {
			t.Fatalf("%q head %q: err = %v, takes %d", req.Command, req.Env["SWARM_HEAD"], err, leases.takes)
		}
	}
}

// Requirement: in a workflow, a failing environment step with
// continue_on_failure records failed (its exit code and last output line as
// the step's output), and a later step with if: {step, status: failed}
// runs while one with if: {step, status: error} is skipped (the script ran:
// it is not an infrastructure error); a host run step in the same workflow
// still runs on the host.
// Threat: an environment check whose failure stops the workflow, or whose
// result conditions cannot see.
// Owners: Runner.runScript (dispatch by Step.In, stepOwner) and
// Runner.runStep, through the real runner and store.
func TestRunnerEnvironmentStepFailureContinuesToConditionalStep(t *testing.T) {
	r := newJobRepo(t)
	leases := newFakeLeases(t)
	h := newHarness(t)
	h.env = EnvironmentExecutor{Leases: leases}
	h.build()
	writeScript(t, r.Folder, "notify.sh", "echo notified\n")
	h.process()
	h.automation(Automation{Name: "console check", Scope: Scope{Folder: r.Folder}, Enabled: true,
		Steps: []Step{
			{ID: "console", Type: StepRun, In: RunInEnvironment, Command: "scripts/check.sh", ContinueOnFailure: true},
			{ID: "notify", Type: StepRun, Command: "notify.sh", If: []Condition{{Step: "console", Status: StateFailed}}},
			{ID: "infra", Type: StepRun, Command: "notify.sh", If: []Condition{{Step: "console", Status: StateError}}},
		}})
	result := workerResult(r.Folder, "run-1")
	result.Git.Head, result.Git.BaseCommit, result.Git.Worktree = r.Head, r.Dev, r.Worktree
	h.results.results["run-1"] = result
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	d := h.deliveries("worker/coder-1/run-1")[0]
	console, notify := d.Steps[0], d.Steps[1]
	if console.State != StateFailed || console.ErrorKind != "" || console.ExitCode == nil || *console.ExitCode != 3 || console.Output != "last line" {
		t.Fatalf("console step = %+v", console)
	}
	if infra := d.Steps[2]; infra.State != StateSkipped {
		t.Fatalf("a status error step ran on a script that ran and failed: %+v", infra)
	}
	if d.State != StateFailed {
		t.Fatalf("delivery = %s, want failed", d.State)
	}
	if notify.State != StateSucceeded || notify.Output != "notified" {
		t.Fatalf("notify step = %+v", notify)
	}
	if len(h.host.ran) != 1 || h.host.ran[0].Command != "notify.sh" {
		t.Fatalf("host ran %+v, want only notify.sh", h.host.ran)
	}
	if leases.takes != 1 || len(leases.released) != 1 {
		t.Fatalf("leases: takes %d released %v", leases.takes, leases.released)
	}
	if !d.EnvironmentRunStep("console") || d.EnvironmentRunStep("notify") {
		t.Fatal("EnvironmentRunStep does not tell the environment check from the host step")
	}
	if logTail := h.store.WithLogs(d).Steps[0].LogTail; !strings.Contains(logTail, "head="+r.Head) {
		t.Fatalf("console log = %q", logTail)
	}
}
