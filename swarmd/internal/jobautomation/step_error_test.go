package jobautomation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// originRepo is a jobRepo whose folder has an origin remote (a bare
// repository) carrying its dev branch; pushHead also pushes the job's head.
func originRepo(t *testing.T, pushHead bool) (jobRepo, string) {
	t.Helper()
	r := newJobRepo(t)
	origin := filepath.Join(filepath.Dir(r.Folder), "origin.git")
	runGit(t, filepath.Dir(r.Folder), "init", "-q", "--bare", origin)
	runGit(t, r.Folder, "remote", "add", "origin", origin)
	runGit(t, r.Folder, "push", "-q", "origin", "dev")
	if pushHead {
		runGit(t, r.Worktree, "push", "-q", "origin", "agent/run-1")
	}
	return r, origin
}

// looseObject is the path of object id in the folder's own store.
func looseObject(t *testing.T, r jobRepo, id string) string {
	t.Helper()
	path := filepath.Join(r.Folder, ".git", "objects", id[:2], id[2:])
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("object %s is not loose in the folder: %v", id, err)
	}
	return path
}

// corrupt overwrites a loose object's bytes (it no longer inflates) and
// returns its original bytes.
func corrupt(t *testing.T, path string) []byte {
	t.Helper()
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a zlib stream"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	return original
}

func restoreObject(t *testing.T, path string, original []byte) {
	t.Helper()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
}

// Requirement: when a step's checkout cannot read an object from the
// folder's repository (corrupt bytes, or a loose object the lease's user may
// not read, which Git also calls "corrupt"), the step names the object and
// git's underlying cause, fetches from the folder's origin remote (the head
// commit when origin has it, else origin's branches), checks out the head
// and runs its script, saying which fallback it used; the folder's
// repository is never written. With no fallback the step is a checkout start
// error naming the cause, not a script failure.
// Threat: a delivery failing (and dispatching a coder) on a production
// checkout that read an unreadable 0440 object as "corrupt"; a fallback that
// repairs or rewrites the folder's own repository.
// Owners: EnvironmentExecutor.Run (prepareScript fallback, parseCheckout,
// prepareFailure) over a fake lease runner with real Git repositories, the
// narrowest layer that runs the real checkout script.
func TestEnvironmentCheckoutFallsBackToOriginOnUnreadableObject(t *testing.T) {
	t.Run("corrupt object, head on origin", func(t *testing.T) {
		r, _ := originRepo(t, true)
		blob := runGit(t, r.Folder, "rev-parse", r.Head+":app.txt")
		path := looseObject(t, r, blob)
		corrupt(t, path)
		bad, _ := os.ReadFile(path)
		before := repoState(t, r)
		out, err := EnvironmentExecutor{Leases: newFakeLeases(t)}.Run(context.Background(), envRequest(r, "scripts/check.sh", 60*time.Second))
		if err != nil {
			t.Fatalf("run: %v\n%s", err, out.Output)
		}
		got := string(out.Output)
		if out.ExitCode != 3 || !strings.Contains(got, "app=head content") || !strings.Contains(got, "head="+r.Head) {
			t.Fatalf("the script did not run on the head: exit %d\n%s", out.ExitCode, got)
		}
		if !strings.Contains(got, "object "+blob) || !strings.Contains(got, "corrupt") || !strings.Contains(got, "fallback origin-head") {
			t.Fatalf("the output does not name the object, the cause and the fallback:\n%s", got)
		}
		if out.Repository == nil || out.Repository.Object != blob || out.Repository.Problem != RepositoryObjectCorrupt || out.Repository.Fallback != "origin-head" || out.Repository.Clean {
			t.Fatalf("repository check = %+v", out.Repository)
		}
		if after, _ := os.ReadFile(path); string(after) != string(bad) {
			t.Fatal("the step rewrote the folder's object")
		}
		if after := repoState(t, r); after != before {
			t.Fatalf("the step changed the folder:\n%s\n%s", before, after)
		}
	})

	t.Run("corrupt object, head only in the folder", func(t *testing.T) {
		// The bad object is dev's (on origin); the head is not pushed.
		r, _ := originRepo(t, false)
		devBlob := runGit(t, r.Folder, "rev-parse", r.Dev+":app.txt")
		// A head that keeps dev's app.txt reads the bad blob.
		runGit(t, r.Worktree, "checkout", "-q", r.Dev, "--", "app.txt")
		runGit(t, r.Worktree, "commit", "-qam", "keep dev's app.txt")
		r.Head = runGit(t, r.Worktree, "rev-parse", "HEAD")
		corrupt(t, looseObject(t, r, devBlob))
		out, err := EnvironmentExecutor{Leases: newFakeLeases(t)}.Run(context.Background(), envRequest(r, "scripts/ok.sh", 60*time.Second))
		if err != nil || out.ExitCode != 0 {
			t.Fatalf("run: %v exit %d\n%s", err, out.ExitCode, out.Output)
		}
		if !strings.Contains(string(out.Output), "fallback origin-branches") || !strings.Contains(string(out.Output), "all good") {
			t.Fatalf("output:\n%s", out.Output)
		}
	})

	t.Run("unreadable object, head on origin", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a mode 0000 file: an unreadable object needs a non-root test user")
		}
		r, _ := originRepo(t, true)
		blob := runGit(t, r.Folder, "rev-parse", r.Head+":app.txt")
		path := looseObject(t, r, blob)
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o444) })
		out, err := EnvironmentExecutor{Leases: newFakeLeases(t)}.Run(context.Background(), envRequest(r, "scripts/ok.sh", 60*time.Second))
		if err != nil || out.ExitCode != 0 {
			t.Fatalf("run: %v exit %d\n%s", err, out.ExitCode, out.Output)
		}
		got := string(out.Output)
		if !strings.Contains(got, "object "+blob) || !strings.Contains(got, "permission denied") || !strings.Contains(got, "----------") || !strings.Contains(got, "fallback origin-head") {
			t.Fatalf("the output does not name the permission problem and the fallback:\n%s", got)
		}
		if out.Repository == nil || out.Repository.Problem != RepositoryObjectUnreadable || out.Repository.Object != blob {
			t.Fatalf("repository check = %+v", out.Repository)
		}
		if info, _ := os.Stat(path); info.Mode().Perm() != 0 {
			t.Fatalf("the step changed the folder's object mode to %v", info.Mode().Perm())
		}
	})

	t.Run("no origin: a checkout error naming the cause", func(t *testing.T) {
		r := newJobRepo(t)
		blob := runGit(t, r.Folder, "rev-parse", r.Head+":app.txt")
		corrupt(t, looseObject(t, r, blob))
		out, err := EnvironmentExecutor{Leases: newFakeLeases(t)}.Run(context.Background(), envRequest(r, "scripts/ok.sh", 60*time.Second))
		var start *StartError
		if !errors.As(err, &start) || start.Kind != ErrorKindCheckout || !strings.Contains(start.Reason, blob) || !strings.Contains(start.Reason, "corrupt") || !strings.Contains(start.Reason, "no origin remote") {
			t.Fatalf("err = %v, want a checkout start error naming the object and the missing fallback", err)
		}
		if strings.Contains(string(out.Output), "all good") || out.Repository == nil || out.Repository.Object != blob {
			t.Fatalf("output %q, repository %+v", out.Output, out.Repository)
		}
	})
}

// Requirement: a run step whose script never started (here the checkout:
// the head is not in the folder's repository) ends status error with
// error_kind checkout, not failed: a later step on if status failed is
// skipped (no coder is sent to fix a check that never ran), one on if status
// error runs, and the delivery ends error with a reason naming an
// infrastructure problem. Without continue_on_failure such a step stops the
// delivery as error. A script that ran and exited non-zero stays failed
// (TestRunnerEnvironmentStepFailureContinuesToConditionalStep).
// Threat: the production review loop dispatching fix-console for 7 minutes
// because a checkout failure was reported as the check failing.
// Owners: Runner.runScript (couldNotStart), Runner.runStep (StateError,
// ErrorKind), Runner.process (delivery state), conditionsHold and
// DeliveryErrorReason, through the real runner, store and executor.
func TestRunnerCheckoutErrorIsErrorNotFailed(t *testing.T) {
	r := newJobRepo(t)
	h := newHarness(t)
	h.env = EnvironmentExecutor{Leases: newFakeLeases(t)}
	h.build()
	writeScript(t, r.Folder, "fix.sh", "echo fixing\n")
	writeScript(t, r.Folder, "infra.sh", "echo infrastructure\n")
	h.process()
	h.automation(Automation{Name: "review loop", Scope: Scope{Folder: r.Folder}, Enabled: true,
		Steps: []Step{
			{ID: "console", Type: StepRun, In: RunInEnvironment, Command: "scripts/check.sh", ContinueOnFailure: true},
			{ID: "fix-console", Type: StepRun, Command: "fix.sh", If: []Condition{{Step: "console", Status: StateFailed}}},
			{ID: "infra", Type: StepRun, Command: "infra.sh", If: []Condition{{Step: "console", Status: StateError}}},
		}})
	missing := strings.Repeat("0123456789", 4)
	result := workerResult(r.Folder, "run-1")
	result.Git.Head, result.Git.BaseCommit, result.Git.Worktree = missing, r.Dev, r.Worktree
	h.results.results["run-1"] = result
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	d := h.deliveries("worker/coder-1/run-1")[0]
	console, fix, infra := d.Steps[0], d.Steps[1], d.Steps[2]
	if console.State != StateError || console.ErrorKind != ErrorKindCheckout || console.ExitCode != nil || !strings.Contains(console.Reason, "is not in the folder's repository") || !strings.Contains(console.Next, "nothing to fix in the work") {
		t.Fatalf("console step = %+v", console)
	}
	if fix.State != StateSkipped || !strings.Contains(fix.Reason, "is error, not failed") {
		t.Fatalf("fix-console ran on a check that never ran: %+v", fix)
	}
	if infra.State != StateSucceeded || infra.Output != "infrastructure" {
		t.Fatalf("infra step = %+v", infra)
	}
	if d.State != StateError || !strings.Contains(DeliveryErrorReason(d), "infrastructure error, not the work: step console could not run (checkout)") {
		t.Fatalf("delivery state %s, reason %q", d.State, DeliveryErrorReason(d))
	}
	for _, ran := range h.host.ran {
		if ran.Command == "fix.sh" {
			t.Fatal("the fix script ran")
		}
	}
	if len(h.signalsOf("automation.step.error")) != 1 || len(h.signalsOf("automation.step.failed")) != 0 {
		t.Fatalf("signals: error %d failed %d", len(h.signalsOf("automation.step.error")), len(h.signalsOf("automation.step.failed")))
	}

	// Without continue_on_failure the error stops the delivery as error.
	h2 := newHarness(t)
	h2.env = EnvironmentExecutor{Leases: newFakeLeases(t)}
	h2.build()
	writeScript(t, r.Folder, "after.sh", "echo after\n")
	h2.process()
	h2.automation(Automation{Name: "gate", Scope: Scope{Folder: r.Folder}, Enabled: true,
		Steps: []Step{
			{ID: "console", Type: StepRun, In: RunInEnvironment, Command: "scripts/check.sh"},
			{ID: "after", Type: StepRun, Command: "after.sh"},
		}})
	h2.results.results["run-1"] = result
	h2.jobEnd("run-1", "", "succeeded", "direct")
	h2.process()
	d = h2.deliveries("worker/coder-1/run-1")[0]
	if d.State != StateError || d.Steps[0].State != StateError || d.Steps[1].State != StateSkipped || !strings.Contains(d.Steps[1].Reason, "could not run") {
		t.Fatalf("delivery %s steps %+v", d.State, d.Steps)
	}
}

// Requirement: a host run step whose script cannot start is classified like
// an environment one: a missing script ends error with error_kind
// script_missing, one that is not executable script_unusable; neither is
// failed.
// Threat: a host check that never ran sending a coder to "fix" it.
// Owners: HostExecutor.Run (RefusedError.Kind, missingScriptError) and
// Runner.runScript, through the real runner and host executor.
func TestRunnerHostStepThatCannotStartIsError(t *testing.T) {
	folder := t.TempDir()
	h := newHarness(t)
	h.process()
	writeScript(t, folder, "plain.sh", "echo plain\n")
	if err := os.Chmod(filepath.Join(folder, "plain.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.automation(Automation{Name: "host", Scope: Scope{Folder: folder}, Enabled: true,
		Steps: []Step{
			{ID: "missing", Type: StepRun, Command: "missing.sh", ContinueOnFailure: true},
			{ID: "plain", Type: StepRun, Command: "plain.sh", ContinueOnFailure: true},
		}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	d := h.deliveries("worker/coder-1/run-1")[0]
	if d.Steps[0].State != StateError || d.Steps[0].ErrorKind != ErrorKindScriptMissing {
		t.Fatalf("missing step = %+v", d.Steps[0])
	}
	if d.Steps[1].State != StateError || d.Steps[1].ErrorKind != ErrorKindScriptUnusable {
		t.Fatalf("not executable step = %+v", d.Steps[1])
	}
	if d.State != StateError {
		t.Fatalf("delivery = %s", d.State)
	}
}

// Requirement: a step's checkout that could not read an object of the
// folder's repository records the folder's repository health (object,
// problem, git's cause; message "folder repository <folder> has a corrupt
// object <id>"), also when the origin fallback made the checkout; a later
// checkout that reads every object from the folder clears it.
// Threat: a folder repository problem that only shows as one step's log
// line, or a health warning that never clears once fixed.
// Owners: Runner.runScript (Store.RecordRepositoryCheck from
// RunOutcome.Repository), Store.RepositoryHealthOf/RepositoryHealthProblems
// and RepositoryHealth.Message, through the real runner, store and executor.
func TestRunnerRecordsAndClearsFolderRepositoryHealth(t *testing.T) {
	r, _ := originRepo(t, true)
	blob := runGit(t, r.Folder, "rev-parse", r.Head+":app.txt")
	path := looseObject(t, r, blob)
	original := corrupt(t, path)
	h := newHarness(t)
	h.env = EnvironmentExecutor{Leases: newFakeLeases(t)}
	h.build()
	h.process()
	h.automation(Automation{Name: "console", Scope: Scope{Folder: r.Folder}, Enabled: true,
		Steps: []Step{{ID: "console", Type: StepRun, In: RunInEnvironment, Command: "scripts/ok.sh"}}})
	result := workerResult(r.Folder, "run-1")
	result.Git.Head, result.Git.BaseCommit, result.Git.Worktree = r.Head, r.Dev, r.Worktree
	h.results.results["run-1"] = result
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	d := h.deliveries("worker/coder-1/run-1")[0]
	if d.State != StateSucceeded || !strings.Contains(h.store.WithLogs(d).Steps[0].LogTail, "fallback origin-head") {
		t.Fatalf("delivery %s, step %+v", d.State, d.Steps[0])
	}
	health, found, err := h.store.RepositoryHealthOf(r.Folder)
	if err != nil || !found || health.Object != blob || health.Problem != RepositoryObjectCorrupt {
		t.Fatalf("health = %+v found %v err %v", health, found, err)
	}
	if want := "folder repository " + r.Folder + " has a corrupt object " + blob; !strings.HasPrefix(health.Message(), want) {
		t.Fatalf("message %q, want prefix %q", health.Message(), want)
	}
	if all, err := h.store.RepositoryHealthProblems(); err != nil || len(all) != 1 || all[0].Folder != r.Folder {
		t.Fatalf("problems = %+v, %v", all, err)
	}

	// The object fixed, the next job's checkout reads every object from the
	// folder: cleared.
	restoreObject(t, path, original)
	writeRepoFile(t, r.Worktree, "next.txt", "next\n", 0o644)
	runGit(t, r.Worktree, "add", "next.txt", "scripts/check.sh", "app.txt")
	runGit(t, r.Worktree, "commit", "-qm", "next job")
	next := workerResult(r.Folder, "run-2")
	next.Git.Head, next.Git.BaseCommit, next.Git.Worktree = runGit(t, r.Worktree, "rev-parse", "HEAD"), r.Dev, r.Worktree
	h.results.results["run-2"] = next
	h.env = EnvironmentExecutor{Leases: newFakeLeases(t)} // a fake runs one step
	h.build()
	h.jobEnd("run-2", "", "succeeded", "direct")
	h.process()
	if d2 := h.deliveries("worker/coder-1/run-2"); len(d2) != 1 || d2[0].State != StateSucceeded || strings.Contains(h.store.WithLogs(d2[0]).Steps[0].LogTail, "fallback") {
		t.Fatalf("second delivery = %+v", d2)
	}
	if _, found, err := h.store.RepositoryHealthOf(r.Folder); err != nil || found {
		t.Fatalf("health not cleared after a clean checkout: found %v err %v", found, err)
	}
}
