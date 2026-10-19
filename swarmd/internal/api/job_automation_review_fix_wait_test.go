package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: when an agent step's reviewer was allowed to fix, the outcome
// Wait hands the runner carries the reviewer job's own head, branch, base
// (the reviewed head) and fix commits, read with Git from its recorded
// checkout; when that checkout cannot be read (no recorded fix checkout, a
// branch rewritten below the reviewed head) the outcome carries ResultError
// and no head, so the runner fails an OK verdict closed. A reviewer without
// fixes is never read this way.
// Threat (review finding): an OK verdict whose fix commits were dropped by a
// swallowed result error or a failed view read, delivering the unfixed head.
// Owners: jobAutomationAgents.Wait and Server.reviewFixCheckout, over the
// daemon's real worker run route, worker/session store and real Git. No
// model provider or sandbox is involved.
func TestWaitReadsTheFixingReviewersCheckout(t *testing.T) {
	f := newReopenFixture(t)
	p := testPrincipal()
	ctx := context.Background()
	reviewer, err := f.server.sessions.CreateWorker(ctx, p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleReviewer, Name: "Security reviewer", IdempotencyKey: "wait-fix-reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "main")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "base")
	base := outboxPinGit(t, folder, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(folder, "upload.go"), []byte("package upload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outboxPinGit(t, folder, "add", ".")
	outboxPinGit(t, folder, "commit", "-q", "-m", "coder work")
	reviewed := outboxPinGit(t, folder, "rev-parse", "HEAD")

	store := f.server.sessions.Store()
	checkout := func(runID string, fix bool) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), runID)
		outboxPinGit(t, filepath.Dir(dir), "clone", "-q", folder, dir)
		outboxPinGit(t, dir, "checkout", "-q", "-b", "agent/review-"+runID, reviewed)
		if fix {
			if err := os.WriteFile(filepath.Join(dir, "upload.go"), []byte("package upload\n\n// escaped\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			outboxPinGit(t, dir, "commit", "-q", "-am", "escape the upload name")
		}
		return dir
	}
	record := func(runID string, fixes bool, session pebblestore.SessionSnapshot) string {
		t.Helper()
		if session.ID != "" {
			if err := store.CreateSession(session); err != nil {
				t.Fatal(err)
			}
		}
		input := map[string]any{"prompt": "Review.", "review_job": "worker/" + f.workerID + "/run-coder",
			"captured_review_target": map[string]any{"head": reviewed, "base": base, "job": "worker/" + f.workerID + "/run-coder"}}
		if fixes {
			input["review_fixes"] = jobautomation.FixesSecurity
		}
		if _, err := store.WorkerStore().RecordWorkerRun(p.AccountScopeID, pebblestore.WorkerRunRecord{ID: runID, WorkerID: reviewer.ID, UserID: p.UserID,
			Role: pebblestore.WorkerRoleReviewer, SessionID: session.ID, Status: "succeeded", RequestSource: jobautomation.RequestSourceAutomation,
			Input: input, CreatedAt: 1, StartedAt: 1, CompletedAt: 2}); err != nil {
			t.Fatal(err)
		}
		return controlMCPJobID(controlMCPJobWorker, reviewer.ID, runID)
	}
	session := func(id, root, branch string) pebblestore.SessionSnapshot {
		return pebblestore.SessionSnapshot{ID: id, UserID: p.UserID, AccountScopeID: p.AccountScopeID, WorkspacePath: folder, Title: "Reviewer",
			WorktreeEnabled: root != "", WorktreeRootPath: root, WorktreeBranch: branch, WorktreeBaseBranch: reviewed, CreatedAt: 1, UpdatedAt: 1}
	}
	wait := func(jobID string) jobautomation.AgentOutcome {
		t.Helper()
		out, err := f.server.JobAutomationAgents().Wait(ctx, p.AccountScopeID, jobID)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// A fix commit on the reviewer's branch: its head and commit are read.
	fixDir := checkout("runfix1", true)
	fixHead := outboxPinGit(t, fixDir, "rev-parse", "HEAD")
	out := wait(record("runfix1", true, session("sess-review-fix-1", fixDir, "agent/review-runfix1")))
	if out.ResultError != "" || out.Result == nil || out.Result.Git == nil {
		t.Fatalf("fixing reviewer outcome = %+v (error %q)", out, out.ResultError)
	}
	g := out.Result.Git
	if out.Head != fixHead || g.Head != fixHead || g.BaseCommit != reviewed || g.Branch != "agent/review-runfix1" || g.Worktree != fixDir ||
		len(g.Commits) != 1 || g.Commits[0].SHA != fixHead || g.Commits[0].Subject != "escape the upload name" {
		t.Fatalf("fixing reviewer git = %+v head %s", g, out.Head)
	}

	// No fix commits: the head is the reviewed head, read, no commits.
	cleanDir := checkout("runfix2", false)
	out = wait(record("runfix2", true, session("sess-review-fix-2", cleanDir, "agent/review-runfix2")))
	if out.ResultError != "" || out.Head != "" || out.Result == nil || out.Result.Git == nil || out.Result.Git.Head != reviewed || len(out.Result.Git.Commits) != 0 {
		t.Fatalf("reviewer without fix commits = %+v", out)
	}

	// Unreadable: no recorded fix checkout, and a branch rewritten below
	// the reviewed head. Both report ResultError and no head.
	out = wait(record("runfix3", true, session("sess-review-fix-3", "", "")))
	if out.ResultError == "" || out.Head != "" {
		t.Fatalf("a fixing reviewer without a recorded checkout = %+v", out)
	}
	rewritten := checkout("runfix4", true)
	outboxPinGit(t, rewritten, "reset", "-q", "--hard", base)
	out = wait(record("runfix4", true, session("sess-review-fix-4", rewritten, "agent/review-runfix4")))
	if !strings.Contains(out.ResultError, "does not descend from the reviewed head") || out.Head != "" {
		t.Fatalf("a rewritten fix branch = %+v", out)
	}

	// A reviewer without fixes is not read from its checkout.
	readOnlyDir := checkout("runro", true)
	if git, fixes, err := f.server.reviewFixCheckout(ctx, p.AccountScopeID, reviewer.ID, "runro"); fixes || git != nil || err != nil {
		t.Fatalf("unrecorded run read as a fixing reviewer: %+v %v %v", git, fixes, err)
	}
	record("runro", false, session("sess-review-ro", readOnlyDir, "agent/review-runro"))
	if git, fixes, err := f.server.reviewFixCheckout(ctx, p.AccountScopeID, reviewer.ID, "runro"); fixes || git != nil || err != nil {
		t.Fatalf("a reviewer without fixes was read as fixing: %+v %v %v", git, fixes, err)
	}
}

// stubStartAgents stubs only the start of an agent step's job (the model
// provider): it hands out reviewer jobs already recorded in the store. Wait
// is the daemon's own jobAutomationAgents.Wait.
type stubStartAgents struct {
	jobs    []string
	started []jobautomation.AgentRequest
	wait    jobautomation.AgentRunner
}

func (a *stubStartAgents) Start(_ context.Context, req jobautomation.AgentRequest) (string, error) {
	a.started = append(a.started, req)
	job := a.jobs[0]
	a.jobs = a.jobs[1:]
	return job, nil
}

func (a *stubStartAgents) Wait(ctx context.Context, account, jobID string) (jobautomation.AgentOutcome, error) {
	return a.wait.Wait(ctx, account, jobID)
}

type fixedJobResults map[string]*jobautomation.Result

func (f fixedJobResults) Result(_ context.Context, sig pebblestore.Signal, _ bool) (*jobautomation.Result, error) {
	r, ok := f[sig.Refs["run_id"]]
	if !ok {
		return nil, errors.New("no result for " + sig.Refs["run_id"])
	}
	copied := *r
	g := *r.Git
	copied.Git = &g
	return &copied, nil
}

// Requirement (brief item 5 and 6, review finding: production path): an agent
// step with fixes "security" gets the fixing reviewer's head and fix commits
// from the daemon's own Wait, which reads them with Git from the reviewer's
// recorded agent/review-<run> clone. Its OK verdict comes from the recorded
// job review. The daemon's VerifyJobHead and JobHeadChangedLines then decide,
// in the folder. A 4-line fix moves the delivery head, and the later run step
// gets the reviewer's head as SWARM_HEAD. An 81-line fix fails the step with
// "reviewer fixes too large", keeps the head and does not run the push.
// Threat: the daemon path never carrying the reviewer clone's head and
// commits (fixes silently never applied), or the line limit counted somewhere
// other than the daemon's own counter.
// Owners: jobautomation.Runner (runAgent, applyReviewerFixes) over
// jobAutomationAgents.Wait, Server.reviewFixCheckout,
// Server.automationAgentReview, VerifyJobHead, JobHeadChangedLines and
// ImportJobRef, with real Git clones, the real worker store and signal feed.
// Stubbed: the reviewer job's start (stubStartAgents: the model provider),
// the coder job's result (fixedJobResults). The run step runs through the
// real host executor (automationHost).
func TestFixingReviewerStepAppliesFixesThroughTheDaemonPath(t *testing.T) {
	f := newReopenFixture(t)
	p := testPrincipal()
	ctx := context.Background()
	account := p.AccountScopeID
	store := f.server.sessions.Store()
	ws := store.WorkerStore()
	reviewer, err := f.server.sessions.CreateWorker(ctx, account, p.UserID, pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleReviewer, Name: "Security reviewer", IdempotencyKey: "e2e-fix-reviewer"})
	if err != nil {
		t.Fatal(err)
	}

	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(folder, "upload.go"), []byte("package upload\n\nfunc Name(s string) string { return s }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outboxPinGit(t, folder, "add", "upload.go")
	outboxPinGit(t, folder, "commit", "-q", "-m", "base")
	base := outboxPinGit(t, folder, "rev-parse", "HEAD")
	// The coder commits in its own clone, so the reviewed head is not in the folder.
	coderClone := filepath.Join(t.TempDir(), "coder")
	outboxPinGit(t, filepath.Dir(coderClone), "clone", "-q", folder, coderClone)
	if err := os.WriteFile(filepath.Join(coderClone, "upload.go"), []byte("package upload\n\nfunc Name(s string) string { return s + \"\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outboxPinGit(t, coderClone, "commit", "-q", "-am", "coder work")
	reviewed := outboxPinGit(t, coderClone, "rev-parse", "HEAD")
	if err := os.MkdirAll(filepath.Join(folder, "deliver"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "deliver", "push.sh"), []byte("#!/bin/sh\necho \"$SWARM_HEAD\" >> \"$PWD/pushed-heads\"\necho pushed\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A fixing reviewer job, recorded as the worker execution service
	// records one: its run (input review_fixes, captured target), its
	// session on agent/review-<run> in its own clone, and its OK review.
	reviewerJob := func(coderRun, runID string, edit func(dir string)) (string, string) {
		t.Helper()
		dir := filepath.Join(t.TempDir(), runID)
		outboxPinGit(t, filepath.Dir(dir), "clone", "-q", coderClone, dir)
		outboxPinGit(t, dir, "checkout", "-q", "-b", "agent/review-"+runID, reviewed)
		edit(dir)
		outboxPinGit(t, dir, "add", ".")
		outboxPinGit(t, dir, "commit", "-q", "-m", "reviewer fix "+runID)
		head := outboxPinGit(t, dir, "rev-parse", "HEAD")
		sessionID := "sess-" + runID
		if err := store.CreateSession(pebblestore.SessionSnapshot{ID: sessionID, UserID: p.UserID, AccountScopeID: account, WorkspacePath: folder, Title: "Reviewer",
			WorktreeEnabled: true, WorktreeRootPath: dir, WorktreeBranch: "agent/review-" + runID, WorktreeBaseBranch: reviewed, CreatedAt: 1, UpdatedAt: 1}); err != nil {
			t.Fatal(err)
		}
		reviewedJob := "worker/" + f.workerID + "/" + coderRun
		if _, err := ws.RecordWorkerRun(account, pebblestore.WorkerRunRecord{ID: runID, WorkerID: reviewer.ID, UserID: p.UserID, Role: pebblestore.WorkerRoleReviewer,
			SessionID: sessionID, Status: "succeeded", RequestSource: jobautomation.RequestSourceAutomation, CreatedAt: 1, StartedAt: 1, CompletedAt: 2,
			Input: map[string]any{"prompt": "Review.", "review_job": reviewedJob, "review_fixes": jobautomation.FixesSecurity,
				"captured_review_target": map[string]any{"head": reviewed, "base": base, "job": reviewedJob, "checkout": coderClone}}}); err != nil {
			t.Fatal(err)
		}
		jobID := controlMCPJobID(controlMCPJobWorker, reviewer.ID, runID)
		if err := ws.RecordJobReview(account, f.workerID, coderRun, pebblestore.JobReview{Verdict: jobautomation.VerdictOK, Head: reviewed, ReviewerJobID: jobID, Model: "m", RecordedAt: 10}); err != nil {
			t.Fatal(err)
		}
		return jobID, head
	}
	small := func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "upload.go"), []byte("package upload\n\nimport \"html\"\n\nfunc Name(s string) string { return html.EscapeString(s) }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	large := func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte("package upload\n"+strings.Repeat("// line\n", 80)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	coderRuns := []string{"coderok", "coderbig"}
	for _, run := range coderRuns {
		f.job(t, run, "succeeded", "")
	}
	okJob, okHead := reviewerJob("coderok", "reviewok", small)
	bigJob, bigHead := reviewerJob("coderbig", "reviewbig", large)

	results := fixedJobResults{}
	for _, run := range coderRuns {
		results[run] = &jobautomation.Result{V: jobautomation.ResultVersion, JobID: "worker/" + f.workerID + "/" + run, Kind: jobautomation.KindWorker, Role: pebblestore.WorkerRoleCoder,
			RunID: run, Folder: folder, Status: jobautomation.StatusSucceeded, Brief: "upload names",
			Git: &jobautomation.ResultGit{Branch: "agent/worker-" + run, BaseCommit: base, Head: reviewed, Worktree: coderClone, Commits: []jobautomation.ResultCommit{{SHA: reviewed, Subject: "coder work"}}}}
	}
	agents := &stubStartAgents{jobs: []string{okJob, bigJob}, wait: f.server.JobAutomationAgents()}
	feed := pebblestore.NewSignalStore(store.Underlying())
	sandbox := newAutomationHost(t)
	runner := jobautomation.NewRunner(jobautomation.Options{Store: jobautomation.NewStore(store.Underlying()), Feed: feed, Account: func() string { return account },
		Results: results, Run: sandbox, Agent: agents, Head: ImportJobRef, Verify: VerifyJobHead, Lines: JobHeadChangedLines})
	if err := runner.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runner.Store().Put(account, jobautomation.Automation{Name: "review then push", Scope: jobautomation.Scope{Folder: folder}, Enabled: true, Steps: []jobautomation.Step{
		{ID: "review", Type: jobautomation.StepAgent, Worker: reviewer.ID, Brief: "Review.", Fixes: jobautomation.FixesSecurity},
		{ID: "push", Type: jobautomation.StepRun, Command: "deliver/push.sh", If: []jobautomation.Condition{{Step: "review", Verdict: jobautomation.VerdictOK}}},
	}}); err != nil {
		t.Fatal(err)
	}
	end := func(run string) jobautomation.Delivery {
		t.Helper()
		if _, err := feed.Append(pebblestore.Signal{Kind: "worker.run.succeeded", Severity: pebblestore.SignalSeverityInfo, Source: pebblestore.SignalSourceSwarmd,
			Account: account, Summary: "Worker run succeeded", DedupKey: "worker.run:" + run + ":",
			Refs: map[string]string{"worker_id": f.workerID, "run_id": run}, Attrs: map[string]string{"source": "direct"}}); err != nil {
			t.Fatal(err)
		}
		if err := runner.ProcessOnce(ctx); err != nil {
			t.Fatal(err)
		}
		list, err := runner.Store().JobDeliveries(account, "worker/"+f.workerID+"/"+run)
		if err != nil || len(list) != 1 {
			t.Fatalf("deliveries for %s = %d %v", run, len(list), err)
		}
		return list[0]
	}

	// OK, 4 changed lines: the head moves to the reviewer's head.
	d := end("coderok")
	if len(agents.started) != 1 || agents.started[0].Fixes != jobautomation.FixesSecurity {
		t.Fatalf("started = %+v", agents.started)
	}
	if d.State != jobautomation.StateSucceeded || d.Head != okHead || d.Steps[0].State != jobautomation.StateSucceeded || d.Steps[1].State != jobautomation.StateSucceeded {
		t.Fatalf("ok delivery = state %s head %s (want %s) steps %+v", d.State, d.Head, okHead, d.Steps)
	}
	out := d.Steps[0].Outcome
	if out["fixes_applied"] != true || fmt.Sprint(out["fix_lines"]) != "4" {
		t.Fatalf("ok outcome = %+v", out)
	}
	commits, _ := out["fix_commits"].([]any)
	if len(commits) != 1 || commits[0].(map[string]any)["sha"] != okHead || commits[0].(map[string]any)["subject"] != "reviewer fix reviewok" {
		t.Fatalf("fix commits = %+v", out["fix_commits"])
	}
	pushed, err := os.ReadFile(filepath.Join(folder, "pushed-heads"))
	if err != nil || strings.TrimSpace(string(pushed)) != okHead {
		t.Fatalf("the run step got %q (%v), want the reviewer's head %s", pushed, err, okHead)
	}
	// The delivery's job ref in the folder names the moved (reviewer's) head.
	if outboxPinGit(t, folder, "rev-parse", "refs/swarm/jobs/coderok") != okHead {
		t.Fatal("the delivery's job ref does not name the reviewer's head")
	}
	view := jobDeliveryView(d)
	if view["head"] != okHead || len(view["fix_commits"].([]any)) != 1 {
		t.Fatalf("delivery view = %+v", view)
	}

	// OK, 81 changed lines counted by the daemon: the step fails, the head stays.
	d = end("coderbig")
	if d.State != jobautomation.StateFailed || d.Head != reviewed || d.Steps[0].Reason != jobautomation.ReasonFixesTooLarge || d.Steps[1].State != jobautomation.StateSkipped {
		t.Fatalf("large delivery = state %s head %s step %+v push %s", d.State, d.Head, d.Steps[0], d.Steps[1].State)
	}
	if fmt.Sprint(d.Steps[0].Outcome["fix_lines"]) != "81" || d.Steps[0].Outcome["fixes_applied"] != false {
		t.Fatalf("large outcome = %+v (reviewer head %s)", d.Steps[0].Outcome, bigHead)
	}
	if pushed, _ := os.ReadFile(filepath.Join(folder, "pushed-heads")); strings.Count(string(pushed), "\n") != 1 {
		t.Fatalf("pushes after the too-large fix = %q", pushed)
	}
}
