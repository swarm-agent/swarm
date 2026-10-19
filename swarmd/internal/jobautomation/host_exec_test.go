package jobautomation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/publish"
)

// These tests run real scripts through HostExecutor (and the real Runner)
// with real Git. The only stub is the root-owner check: the test's own files
// pass it through publish.AllowOwnerForTests, and the refusal tests turn that
// off (or make the file fail it another way).

// notRootOwned makes path fail the root-owner check with the override off:
// a file the test user owns, or, when the tests run as root, one owned by
// nobody.
func notRootOwned(t *testing.T, path string) {
	t.Helper()
	if os.Getuid() == 0 {
		if err := os.Lchown(path, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
}

func refusedReason(t *testing.T, err error) string {
	t.Helper()
	var refused *RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	return refused.Reason
}

// Requirement: a run step runs on the host as the daemon's user, so it can
// write the folder's repository: a script merges SWARM_JOB_REF (the job's
// head, imported as refs/swarm/jobs/<run-id> before the step) into a branch
// of the folder, and that commit is in the folder afterwards. Swarm itself
// has no merge logic: the owner's script does it.
// Threat: run steps that cannot deliver locally because the folder's .git is
// read-only to them (the retired sandbox executor), so owners reach for
// built-in merge paths.
// Owners: HostExecutor.Run and Runner.runScript (head import, SWARM_JOB_REF).
// Real Git in a real folder is the narrowest layer that proves the write.
func TestRunStepMergesTheJobRefIntoAFolderBranch(t *testing.T) {
	folder := t.TempDir()
	repo := newGitRepo(t, folder)
	base := repo.git("rev-parse", "HEAD")
	jobDir := filepath.Join(t.TempDir(), "job")
	repo.git("clone", "-q", folder, jobDir)
	job := &gitRepo{t: t, dir: jobDir}
	job.git("checkout", "-q", "-b", "agent/run-1")
	job.write("logo.txt", "a logo\n")
	head := job.commitFiles("add logo")

	h := newHarness(t)
	h.head = func(ctx context.Context, folder string, r Result) (string, error) {
		ref := "refs/swarm/jobs/" + r.RunID
		cmd := exec.CommandContext(ctx, "git", "fetch", "-q", jobDir, "+refs/heads/"+r.Git.Branch+":"+ref)
		cmd.Dir = folder
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", errors.New(string(out))
		}
		return ref, nil
	}
	h.build()
	// The owner's script lives outside the folder, by absolute path.
	scripts := t.TempDir()
	writeScript(t, scripts, "merge.sh", `set -e
git checkout -q -b delivered main
git -c user.name=Owner -c user.email=owner@example.invalid -c commit.gpgsign=false merge -q --no-ff -m "deliver $SWARM_JOB_ID" "$SWARM_JOB_REF"
git rev-parse HEAD
`)
	h.process()
	h.automation(Automation{Name: "merge", Scope: Scope{Folder: folder}, Enabled: true,
		Steps: []Step{{ID: "merge", Type: StepRun, Command: filepath.Join(scripts, "merge.sh")}}})
	result := workerResult(folder, "run-1")
	result.Git = &ResultGit{Branch: "agent/run-1", BaseCommit: base, Head: head}
	h.results.results["run-1"] = result
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()

	d := h.store.WithLogs(h.deliveries("worker/coder-1/run-1")[0])
	step := d.Steps[0]
	if d.State != StateSucceeded || step.State != StateSucceeded || step.ExitCode == nil || *step.ExitCode != 0 {
		t.Fatalf("delivery = %s step %+v\n%s", d.State, step, step.LogTail)
	}
	merged := repo.git("rev-parse", "delivered")
	if step.Output != merged {
		t.Fatalf("step output %q, want the merge commit %s", step.Output, merged)
	}
	if parents := strings.Fields(repo.git("rev-list", "--parents", "-n", "1", merged)); len(parents) != 3 || parents[1] != base || parents[2] != head {
		t.Fatalf("merge commit parents = %v, want %s and the job head %s", parents, base, head)
	}
	repo.git("merge-base", "--is-ancestor", head, "delivered")
	if got := readFile(t, filepath.Join(folder, "logo.txt")); got != "a logo\n" {
		t.Fatalf("the job's file is not in the folder: %q", got)
	}
}

// Requirement: a run step's script runs only when the final file (symlinks
// resolved) is owned by root and not writable by group or others, for a
// path relative to the folder and for an absolute one; otherwise the step is
// refused with the reason and the install command, and the script does not
// run.
// Threat: anyone who can write the folder or the script (an agent, another
// user) running code as the daemon's user.
// Owners: ResolveScript and HostExecutor.Run, over publish.OwnedByRoot.
func TestRunStepRefusesScriptsThatAreNotRootOwnedOrAreWritable(t *testing.T) {
	restore := publish.AllowOwnerForTests(os.Getuid())
	t.Cleanup(restore)
	type tc struct {
		name, mode string
		perm       os.FileMode
		absolute   bool
		why        string
	}
	cases := []tc{
		{name: "relative not root-owned", mode: "owner", perm: 0o755, why: "it is not owned by root"},
		{name: "absolute not root-owned", mode: "owner", perm: 0o755, absolute: true, why: "it is not owned by root"},
		{name: "relative group-writable", perm: 0o775, why: "group or others can write it"},
		{name: "absolute group-writable", perm: 0o775, absolute: true, why: "group or others can write it"},
		{name: "relative other-writable", perm: 0o757, why: "group or others can write it"},
		{name: "absolute other-writable", perm: 0o757, absolute: true, why: "group or others can write it"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			folder := t.TempDir()
			dir := folder
			if c.absolute {
				dir = t.TempDir()
			}
			writeScript(t, dir, "deliver/push.sh", "touch \"$PWD/ran\"\n")
			path := filepath.Join(dir, "deliver", "push.sh")
			if err := os.Chmod(path, c.perm); err != nil {
				t.Fatal(err)
			}
			command := "deliver/push.sh"
			if c.absolute {
				command = path
			}
			if c.mode == "owner" {
				notRootOwned(t, path)
				defer publish.AllowOwnerForTests(-1)()
			}
			_, err := HostExecutor{}.Run(context.Background(), RunRequest{Folder: folder, Command: command, Timeout: 10 * time.Second})
			reason := refusedReason(t, err)
			resolved, evalErr := filepath.EvalSymlinks(path)
			if evalErr != nil {
				t.Fatal(evalErr)
			}
			for _, want := range []string{command + " must be owned by root and not writable by group or others", c.why, "sudo install -o root -m 755", resolved} {
				if !strings.Contains(reason, want) {
					t.Fatalf("reason %q lacks %q", reason, want)
				}
			}
			if _, err := os.Stat(filepath.Join(folder, "ran")); !os.IsNotExist(err) {
				t.Fatal("a refused script ran")
			}
		})
	}

	// Through the runner: the step is recorded as refused and nothing ran.
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "touch \"$PWD/ran\"\n")
	if err := os.Chmod(filepath.Join(folder, "push.sh"), 0o775); err != nil {
		t.Fatal(err)
	}
	h.process()
	h.automation(Automation{Name: "push", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{{ID: "push", Type: StepRun, Command: "push.sh"}}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	d := h.deliveries("worker/coder-1/run-1")[0]
	if d.State != StateFailed || d.Steps[0].ExitCode != nil || !strings.Contains(d.Steps[0].Reason, "must be owned by root and not writable by group or others") {
		t.Fatalf("refused step = %+v", d.Steps[0])
	}
	if _, err := os.Stat(filepath.Join(folder, "ran")); !os.IsNotExist(err) {
		t.Fatal("a refused script ran through the runner")
	}
}

// Requirement: the root-owner check applies to the file a symlink resolves
// to, and the step runs the resolved path, not the name the step gave.
// Threat: a root-owned-looking name in the folder that links to a file
// anyone can change.
// Owners: ResolveScript and HostExecutor.Run.
func TestRunStepResolvesSymlinksBeforeTheOwnerCheck(t *testing.T) {
	t.Cleanup(publish.AllowOwnerForTests(os.Getuid()))
	folder, elsewhere := t.TempDir(), t.TempDir()
	writeScript(t, elsewhere, "real.sh", "echo \"$0\"\ntouch \"$PWD/ran\"\n")
	real := filepath.Join(elsewhere, "real.sh")
	if err := os.MkdirAll(filepath.Join(folder, "deliver"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(folder, "deliver", "push.sh")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	resolvedReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}

	// A link to a file that passes runs that file, by its resolved path.
	out, err := HostExecutor{}.Run(context.Background(), RunRequest{Folder: folder, Command: "deliver/push.sh", Timeout: 10 * time.Second})
	if err != nil || out.ExitCode != 0 || lastLine(out.Output) != resolvedReal {
		t.Fatalf("link to a passing script: %+v %v (ran as %q, want %q)", out, err, lastLine(out.Output), resolvedReal)
	}
	if err := os.Remove(filepath.Join(folder, "ran")); err != nil {
		t.Fatal(err)
	}

	// A link to a file that is not root-owned is refused and nothing runs.
	notRootOwned(t, real)
	restore := publish.AllowOwnerForTests(-1)
	_, err = HostExecutor{}.Run(context.Background(), RunRequest{Folder: folder, Command: "deliver/push.sh", Timeout: 10 * time.Second})
	restore()
	reason := refusedReason(t, err)
	if !strings.Contains(reason, "a link to "+resolvedReal) || !strings.Contains(reason, "it is not owned by root") {
		t.Fatalf("reason %q does not name the link's target and why", reason)
	}
	// A link to a group-writable file is refused too.
	if err := os.Chmod(real, 0o775); err != nil {
		t.Fatal(err)
	}
	_, err = HostExecutor{}.Run(context.Background(), RunRequest{Folder: folder, Command: "deliver/push.sh", Timeout: 10 * time.Second})
	if reason := refusedReason(t, err); !strings.Contains(reason, "group or others can write it") {
		t.Fatalf("reason %q", reason)
	}
	if _, err := os.Stat(filepath.Join(folder, "ran")); !os.IsNotExist(err) {
		t.Fatal("a refused link's target ran")
	}

	// A link into a .git directory is refused.
	gitDir := filepath.Join(folder, ".git", "hooks")
	writeScript(t, gitDir, "push.sh", "touch \"$PWD/ran\"\n")
	if err := os.Symlink(filepath.Join(gitDir, "push.sh"), filepath.Join(folder, "hook.sh")); err != nil {
		t.Fatal(err)
	}
	_, err = HostExecutor{}.Run(context.Background(), RunRequest{Folder: folder, Command: "hook.sh", Timeout: 10 * time.Second})
	if reason := refusedReason(t, err); !strings.Contains(reason, ".git") {
		t.Fatalf("reason %q", reason)
	}
}

// processGone reports whether pid no longer runs (gone, or a zombie no one
// has reaped yet).
func processGone(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	stat := string(data)
	if i := strings.LastIndexByte(stat, ')'); i >= 0 && i+2 < len(stat) {
		return stat[i+2] == 'Z' || stat[i+2] == 'X'
	}
	return false
}

func waitGone(t *testing.T, folder, name string) {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(readFile(t, filepath.Join(folder, name))))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !processGone(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %s (%d) still runs after the step was killed", name, pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Requirement: a step's time limit, or the daemon cancelling it, kills the
// script and every process it started (the whole process group), and the
// step does not wait for them. A timeout is the step's outcome (exit code
// 124 and a line saying so); a cancel is not recorded as an outcome.
// Threat: a hung script, or a child it left running, holding the delivery
// (and the machine) forever.
// Owners: HostExecutor.Run over publish.RunScript (Setpgid, process-group
// kill).
func TestRunStepTimeoutKillsTheScriptAndItsChildren(t *testing.T) {
	t.Cleanup(publish.AllowOwnerForTests(os.Getuid()))
	script := "sleep 60 &\necho $! > \"$PWD/child.pid\"\necho $$ > \"$PWD/parent.pid\"\necho started\nsleep 60\n"

	folder := t.TempDir()
	writeScript(t, folder, "hang.sh", script)
	start := time.Now()
	out, err := HostExecutor{}.Run(context.Background(), RunRequest{Folder: folder, Command: "hang.sh", Timeout: time.Second})
	if err != nil {
		t.Fatalf("timeout: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("the step took %s to time out", elapsed)
	}
	if out.ExitCode != timeoutExitCode || !strings.Contains(string(out.Output), "started") || !strings.Contains(lastLine(out.Output), "timed out after 1s") {
		t.Fatalf("timed-out outcome = %d %q", out.ExitCode, out.Output)
	}
	waitGone(t, folder, "parent.pid")
	waitGone(t, folder, "child.pid")

	folder = t.TempDir()
	writeScript(t, folder, "hang.sh", script)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for i := 0; i < 500; i++ {
			if _, err := os.Stat(filepath.Join(folder, "parent.pid")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	if _, err := (HostExecutor{}).Run(ctx, RunRequest{Folder: folder, Command: "hang.sh", Timeout: time.Minute}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled step err = %v", err)
	}
	waitGone(t, folder, "parent.pid")
	waitGone(t, folder, "child.pid")
}

// Requirement: secrets are no longer a run step option; a step that still
// sends secrets (even an empty list) is refused with that reason when the
// definition is saved, and a stored step with secrets is refused before it
// runs.
// Threat: an owner believing a script gets only named secrets when it runs
// with the machine's own credentials.
// Owners: NormalizeSteps (secretsRemoved) and Runner.runScript.
func TestRunStepWithSecretsIsRefused(t *testing.T) {
	for _, body := range []string{
		`[{"type":"run","command":"deliver/push.sh","secrets":["FORGE_TOKEN"]}]`,
		`[{"type":"run","command":"deliver/push.sh","secrets":[]}]`,
		`[{"type":"agent","worker":"reviewer","brief":"x","secrets":["A"]}]`,
	} {
		var steps []Step
		if err := json.Unmarshal([]byte(body), &steps); err != nil {
			t.Fatal(err)
		}
		err := NormalizeSteps(steps)
		if !IsValidationError(err) || !strings.Contains(err.Error(), "secrets are no longer a step option") || !strings.Contains(err.Error(), "machine's own credentials") {
			t.Fatalf("%s: err = %v", body, err)
		}
	}
	var steps []Step
	if err := json.Unmarshal([]byte(`[{"type":"run","command":"deliver/push.sh"}]`), &steps); err != nil || NormalizeSteps(steps) != nil {
		t.Fatal("a run step without secrets is refused")
	}
	if encoded, _ := json.Marshal(steps[0]); strings.Contains(string(encoded), "secrets") {
		t.Fatalf("a step without secrets encodes them: %s", encoded)
	}

	// A step stored before secrets were removed is refused before it runs.
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "touch \"$PWD/ran\"\n")
	h.process()
	saved := h.automation(Automation{Name: "push", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{{ID: "push", Type: StepRun, Command: "push.sh"}}})
	step := saved.Steps[0]
	step.Secrets = json.RawMessage(`["FORGE_TOKEN"]`)
	record := StepRecord{}
	failure := h.runner.runScript(context.Background(), &Delivery{Folder: folder, Result: *workerResult(folder, "run-1")}, &record, step)
	if !failure.refused || !strings.Contains(failure.reason, "secrets are no longer a step option") || len(h.host.ran) != 0 {
		t.Fatalf("stored step with secrets: %+v (ran %d)", failure, len(h.host.ran))
	}
	if _, err := os.Stat(filepath.Join(folder, "ran")); !os.IsNotExist(err) {
		t.Fatal("a step with secrets ran")
	}
}

// Requirement: a run step's command is an absolute path or a clean path
// relative to the folder, never under .git and never leaving the folder
// with .. (an absolute path names a script elsewhere).
// Owner: ValidCommandPath.
func TestValidCommandPathAcceptsAbsolutePaths(t *testing.T) {
	for _, ok := range []string{"deliver/push.sh", "push.sh", "/usr/local/libexec/swarm/push.sh", "/srv/site/deliver/push.sh"} {
		if err := ValidCommandPath(ok); err != nil {
			t.Fatalf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "/", "..", "../push.sh", "a/../b.sh", "./push.sh", "/srv//push.sh", ".git/hooks/push", ".git", "/srv/site/.git/hooks/push", "sub/.git/x", "a\\b.sh", "a\x00b"} {
		if err := ValidCommandPath(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}
