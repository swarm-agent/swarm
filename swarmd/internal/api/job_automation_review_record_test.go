package api

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const reviewLoopHead = "0123456789abcdef0123456789abcdef01234567"

// reviewLoopFindings is a FINDINGS section shaped like the forge reviewer's:
// five numbered findings, the fifth a security finding, together far longer
// than the 4,000-character task summary.
func reviewLoopFindings() string {
	var b strings.Builder
	for i := 1; i <= 5; i++ {
		severity := "medium"
		if i == 5 {
			severity = "high security"
		}
		fmt.Fprintf(&b, "%d. %s internal/upload/upload.go:%d finding %d starts here.\n", i, severity, 10*i, i)
		for j := 0; j < 16; j++ {
			fmt.Fprintf(&b, "   detail %d.%d: the change does not cover this case and the report claims it does.\n", i, j)
		}
		fmt.Fprintf(&b, "   end of finding %d\n", i)
	}
	return strings.TrimSpace(b.String())
}

// reviewLoopMessage is a reviewer's final assistant message as the forge run
// produced it: sixteen lines of progress notes, then the structured report.
func reviewLoopMessage(verdict, findings string) string {
	var b strings.Builder
	for i := 1; i <= 16; i++ {
		fmt.Fprintf(&b, "I'm checking whether step %d of the brief is covered by the diff.\n", i)
	}
	b.WriteString("VERDICT: " + verdict + "\nSUMMARY: the change is reviewed against its brief.\nFINDINGS: " + findings + "\nTESTS RUN: none (review is read-only)\n")
	return b.String()
}

// reviewLoopAgents starts no model run: each agent step's job is the reviewer
// job recorded below with the final message a model would have written (the
// model provider is the only stubbed boundary). Wait is the daemon's own
// jobAutomationAgents.Wait, which reads the verdict and findings from the
// review record.
type reviewLoopAgents struct {
	real     jobautomation.AgentRunner
	reviewer string
	mu       sync.Mutex
	started  []string
}

func (a *reviewLoopAgents) Start(_ context.Context, req jobautomation.AgentRequest) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.started = append(a.started, req.Worker)
	return a.reviewer, nil
}

func (a *reviewLoopAgents) Wait(ctx context.Context, account, jobID string) (jobautomation.AgentOutcome, error) {
	return a.real.Wait(ctx, account, jobID)
}

type reviewLoopResults struct{ result *jobautomation.Result }

func (r reviewLoopResults) Result(context.Context, pebblestore.Signal, bool) (*jobautomation.Result, error) {
	copy := *r.result
	return &copy, nil
}

type reviewLoop struct {
	f        reopenFixture
	coderJob string
	agents   *reviewLoopAgents
	runner   *jobautomation.Runner
	feed     *pebblestore.SignalStore
}

// newReviewLoop records a coder job with a real session, and a reviewer job
// that reviewed it and ended with message, recorded through
// ParseJobReview/RecordJobReview as recordReviewResult does. Its automation
// is the docs' review loop with an agent step after an ok.
func newReviewLoop(t *testing.T, message string) reviewLoop {
	t.Helper()
	f := newReopenFixture(t)
	p := testPrincipal()
	ctx := context.Background()
	coderJob := f.job(t, "run-coder", "succeeded", f.session.ID)
	reviewer, err := f.server.sessions.CreateWorker(ctx, p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleReviewer, Name: "Reviewer", IdempotencyKey: "loop-reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	ws := f.server.sessions.Store().WorkerStore()
	if _, err := ws.RecordWorkerRun(p.AccountScopeID, pebblestore.WorkerRunRecord{ID: "run-review", WorkerID: reviewer.ID, UserID: p.UserID, Role: pebblestore.WorkerRoleReviewer,
		Status: "succeeded", RequestSource: jobautomation.RequestSourceAutomation, Input: map[string]any{"prompt": "Review.", "review_job": coderJob}, CreatedAt: 3, StartedAt: 3, CompletedAt: 4}); err != nil {
		t.Fatal(err)
	}
	reviewerJob := controlMCPJobID(controlMCPJobWorker, reviewer.ID, "run-review")
	if err := ws.RecordJobReview(p.AccountScopeID, f.workerID, "run-coder", pebblestore.ParseJobReview(message, reviewLoopHead, reviewerJob, "test/model", 4)); err != nil {
		t.Fatal(err)
	}

	db := f.server.sessions.Store().Underlying()
	feed := pebblestore.NewSignalStore(db)
	store := jobautomation.NewStore(db)
	folder := t.TempDir()
	// The coder job's result as the task summary carries it: its report is
	// cut at 4,000 characters, so it must never be where findings come from.
	report := strings.Repeat("The coder's summary. ", 400)[:4000] + " [truncated; retrieve completed run]"
	result := &jobautomation.Result{V: jobautomation.ResultVersion, JobID: coderJob, Kind: jobautomation.KindWorker, Role: "coder", Worker: &jobautomation.ResultWorker{ID: f.workerID, Name: "Coder"},
		RunID: "run-coder", Folder: folder, Status: jobautomation.StatusSucceeded, Report: report,
		Git: &jobautomation.ResultGit{BaseCommit: strings.Repeat("a", 40), Head: reviewLoopHead, Commits: []jobautomation.ResultCommit{{SHA: reviewLoopHead, Subject: "add the limit"}}}}
	agents := &reviewLoopAgents{real: f.server.JobAutomationAgents(), reviewer: reviewerJob}
	runner := jobautomation.NewRunner(jobautomation.Options{Store: store, Feed: feed, Account: func() string { return p.AccountScopeID }, Results: reviewLoopResults{result: result},
		Agent: agents, Reopen: f.server.JobAutomationReopener()})
	// A first pass sets the runner's cursor at the feed's end, as a daemon
	// start does; the job ends after it.
	if err := runner.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Put(p.AccountScopeID, jobautomation.Automation{Name: "review loop", Scope: jobautomation.Scope{Folder: folder, Worker: f.workerID}, When: jobautomation.WhenSucceeded, Enabled: true,
		Steps: []jobautomation.Step{
			{ID: "review", Type: jobautomation.StepAgent, Worker: reviewer.ID, Brief: "Review this job's exact head against its brief."},
			{ID: "fix", Type: jobautomation.StepReopen, From: "review", MaxRounds: 2, Message: "The reviewer blocked your work. Its findings:", If: []jobautomation.Condition{{Step: "review", Verdict: jobautomation.VerdictBlock}}},
			{ID: "after", Type: jobautomation.StepAgent, Worker: "finder", Brief: "Runs only after an ok.", If: []jobautomation.Condition{{Step: "review", Verdict: jobautomation.VerdictOK}}},
		}}); err != nil {
		t.Fatal(err)
	}
	if _, err := feed.Append(pebblestore.Signal{Kind: "worker.run.succeeded", Severity: pebblestore.SignalSeverityInfo, Source: pebblestore.SignalSourceSwarmd, Account: p.AccountScopeID,
		Summary: "Worker run succeeded", DedupKey: "worker.run:run-coder:", Refs: map[string]string{"worker_id": f.workerID, "run_id": "run-coder"}, Attrs: map[string]string{"source": "direct"}}); err != nil {
		t.Fatal(err)
	}
	return reviewLoop{f: f, coderJob: coderJob, agents: agents, runner: runner, feed: feed}
}

func (l reviewLoop) process(t *testing.T) jobautomation.Delivery {
	t.Helper()
	if err := l.runner.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	deliveries, err := l.runner.Store().JobDeliveries(testPrincipal().AccountScopeID, l.coderJob)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("deliveries = %d %v", len(deliveries), err)
	}
	return deliveries[0]
}

// Requirement: a reviewer's BLOCK whose final message starts with progress
// notes is recorded as block with its full FINDINGS section, the agent
// step's outcome carries those findings from the review record, and the
// reopen step sends all of them (over 4,000 characters, five findings) into
// the coder job's own session.
// Threat (forge run): the verdict was never read (every review "malformed"),
// and the reopen sent the 4,000-character task summary's first half of
// finding 1, dropping findings 2-5 including a security finding.
// Owners: pebblestore.ParseJobReview, WorkerStore.RecordJobReview,
// Server.automationAgentReview via jobAutomationAgents.Wait, Runner.runAgent
// and runReopen, jobAutomationReopener.Reopen over the real session routes.
// Only the model provider is replaced (the reviewer's final message is
// recorded directly).
func TestReviewLoopReopenSendsTheRecordedFindingsInFull(t *testing.T) {
	findings := reviewLoopFindings()
	message := reviewLoopMessage("BLOCK", findings)
	if len(findings) <= 4000 || len(message) <= 7000 {
		t.Fatalf("fixture: findings %d bytes, message %d bytes", len(findings), len(message))
	}
	loop := newReviewLoop(t, message)
	before := len(loop.f.userMessages(t, loop.f.session.ID))
	d := loop.process(t)

	review := d.Steps[0]
	if review.Outcome["verdict"] != jobautomation.VerdictBlock || review.Outcome["findings"] != findings {
		got, _ := review.Outcome["findings"].(string)
		t.Fatalf("review step outcome: verdict %v, findings %d bytes (want %d):\n%.300s", review.Outcome["verdict"], len(got), len(findings), got)
	}
	got := loop.f.userMessages(t, loop.f.session.ID)
	if len(got) != before+1 {
		t.Fatalf("reopen sent %d messages (steps %+v)", len(got)-before, d.Steps)
	}
	want := "The reviewer blocked your work. Its findings:\n\n" + findings + "\n\n" + jobautomation.ReopenClosing
	if sent := got[len(got)-1]; sent != want {
		t.Fatalf("reopen message is %d bytes, want %d; has finding 5: %v", len(sent), len(want), strings.Contains(sent, "5. high security"))
	}
	if strings.Contains(got[len(got)-1], "truncated") || strings.Contains(got[len(got)-1], "The coder's summary") {
		t.Fatal("the reopen sent the cut task summary")
	}
	if d.Steps[2].State != jobautomation.StateSkipped {
		t.Fatalf("the ok-only step ran after a block: %+v", d.Steps[2])
	}
}

// Requirement: a reviewer's OK after progress notes is recorded as ok, so an
// agent step's later `verdict: ok` condition holds and nothing is reopened.
// Threat (forge run): an OK review could never be recorded, so a loop waiting
// for an ok never passed.
// Owners: as above.
func TestReviewLoopOkAfterProgressNotesSatisfiesAnOkCondition(t *testing.T) {
	loop := newReviewLoop(t, reviewLoopMessage("OK", "None"))
	before := len(loop.f.userMessages(t, loop.f.session.ID))
	d := loop.process(t)
	if d.Steps[0].Outcome["verdict"] != jobautomation.VerdictOK {
		t.Fatalf("review step outcome = %+v", d.Steps[0].Outcome)
	}
	if _, ok := d.Steps[0].Outcome["findings"]; ok {
		t.Fatalf("an ok review with no findings carried findings: %+v", d.Steps[0].Outcome)
	}
	if d.Steps[1].State != jobautomation.StateSkipped {
		t.Fatalf("an ok review reopened the job: %+v", d.Steps[1])
	}
	if d.Steps[2].State == jobautomation.StateSkipped || len(loop.agents.started) != 2 || loop.agents.started[1] != "finder" {
		t.Fatalf("the verdict: ok condition did not hold: step %+v, started %v", d.Steps[2], loop.agents.started)
	}
	if n := len(loop.f.userMessages(t, loop.f.session.ID)); n != before {
		t.Fatalf("an ok review sent %d follow-ups", n-before)
	}
}

// Requirement: an agent step's findings come from the review record only,
// never the reviewer job's cut task summary. A malformed record (no verdict
// could be read) gives no findings and is marked unreadable, so the reopen
// sends nothing (the readable BLOCK without findings, which sends its
// reason, is TestReviewLoopReadableBlockWithoutFindingsSendsTheReason).
// Threat: the 4,000-character summary (result.Report), or the malformed
// reason, reaching the coder as findings.
// Owner: Server.automationAgentReview.
func TestAutomationAgentReviewNeverReadsTheTaskSummary(t *testing.T) {
	loop := newReviewLoop(t, "I'm checking whether the limit is enforced.\nSUMMARY: early\nVERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: none")
	p := testPrincipal()
	run, found, err := loop.f.server.sessions.Store().WorkerStore().GetWorkerRun(p.AccountScopeID, strings.Split(loop.agents.reviewer, "/")[1], "run-review")
	if err != nil || !found {
		t.Fatalf("reviewer run = %v %v", found, err)
	}
	verdict, findings, unreadable := loop.f.server.automationAgentReview(p.AccountScopeID, run.WorkerID, run.ID)
	if verdict != jobautomation.VerdictBlock || findings != "" || !unreadable {
		t.Fatalf("malformed review = %q %q unreadable %v", verdict, findings, unreadable)
	}
	// Through the runner: nothing is sent, neither the record's reason nor
	// the cut task summary the step's job result carries.
	before := len(loop.f.userMessages(t, loop.f.session.ID))
	d := loop.process(t)
	if _, ok := d.Steps[0].Outcome["findings"]; ok || d.Steps[0].Outcome["review_unreadable"] != true {
		t.Fatalf("review step outcome = %+v", d.Steps[0].Outcome)
	}
	if got := loop.f.userMessages(t, loop.f.session.ID); len(got) != before {
		t.Fatalf("reopen sent %d messages (steps %+v)", len(got)-before, d.Steps)
	}
}
