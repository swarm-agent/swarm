package jobautomation

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedAgents serialises the recording agent stub for deliveries that run
// concurrently.
type lockedAgents struct {
	mu    sync.Mutex
	inner *recordingAgents
}

func (a *lockedAgents) Start(ctx context.Context, req AgentRequest) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.inner.Start(ctx, req)
}

func (a *lockedAgents) Wait(ctx context.Context, account, jobID string) (AgentOutcome, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.inner.Wait(ctx, account, jobID)
}

// gatedReopener answers busy (the job is running) until reopen requests from
// `gate` distinct deliveries have arrived, so every one of them is waiting at
// once; after each follow-up it sends, the job is busy for one more call, as
// a session is while the follow-up runs.
type gatedReopener struct {
	mu        sync.Mutex
	gate      int
	seen      map[string]bool
	busyAfter bool
	sent      []ReopenRequest
}

func (g *gatedReopener) Reopen(_ context.Context, req ReopenRequest) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.seen[req.RequestID] = true
	if len(g.seen) < g.gate || g.busyAfter {
		g.busyAfter = false
		return "", stillRunning{}
	}
	g.sent = append(g.sent, req)
	g.busyAfter = true
	return "followup-" + req.RequestID, nil
}

func (g *gatedReopener) sentCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.sent)
}

// Requirement: a job gets at most max_rounds reopens in total from one
// automation, even when several of its deliveries (for different heads) reach
// their reopen step while the job is busy and wait for it together: the round
// limit is checked again under the send lock, right before each follow-up is
// sent, and a delivery that finds the limit used ends blocked
// (rounds_exhausted) without sending.
// Threat (review of the per-job round fix): runReopen read the round count
// once before waiting; both waiting deliveries passed that check and each
// sent once the job settled, one reopen past max_rounds per extra delivery.
// Owners: Runner.dispatch (concurrent deliveries), Runner.runReopen /
// sendReopen and Store.ReopenCount / PutReopen over the real store and signal
// feed; only the reviewer job (model provider) and the job's session are
// stubbed.
// Layer: the concurrent dispatcher over the real store is the only layer
// where two deliveries of one job wait on the same busy job.
func TestWaitingDeliveriesOfOneJobNeverExceedMaxRounds(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "echo pushed\n")
	h.agents.outcomes = []AgentOutcome{
		{Verdict: VerdictBlock, Findings: "1. high a.go:1 the limit is not checked"},
		{Verdict: VerdictBlock, Findings: "1. high a.go:1 the limit is not checked"},
	}
	h.runner.agent = &lockedAgents{inner: h.agents}
	reopener := &gatedReopener{gate: 2, seen: map[string]bool{}}
	h.runner.reopen = reopener
	h.runner.recheck = 5 * time.Millisecond
	h.process()
	h.automation(reviewLoop(folder, 1))

	// Two ends of the same job on different heads: two deliveries, both at
	// round 0 because no reopen has been sent yet.
	result := workerResult(folder, "run-1")
	h.results.results["run-1"] = result
	h.jobEnd("run-1", "", "succeeded", "direct")
	if err := h.runner.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	result.Git.Head = strings.Repeat("c", 40)
	h.jobEnd("run-1", "followup-manual", "succeeded", "direct")
	if err := h.runner.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if list := h.deliveries("worker/coder-1/run-1"); len(list) != 2 || list[0].Round != 0 || list[1].Round != 0 {
		t.Fatalf("deliveries before dispatch = %+v", list)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h.runner.dispatch(ctx, false)
	var list []Delivery
	for {
		list = h.deliveries("worker/coder-1/run-1")
		done := len(list) == 2
		for _, d := range list {
			done = done && Terminal(d.State)
		}
		h.runner.mu.Lock()
		idle := len(h.runner.active) == 0
		h.runner.mu.Unlock()
		if done && idle {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("deliveries did not finish: %+v", list)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if n := reopener.sentCount(); n != 1 {
		t.Fatalf("reopens sent = %d, want max_rounds 1", n)
	}
	if count, err := h.store.ReopenCount(testAccount, "worker/coder-1/run-1", list[0].AutomationID); err != nil || count != 1 {
		t.Fatalf("recorded reopens = %d %v, want 1", count, err)
	}
	reopened, blocked := 0, 0
	for _, d := range list {
		switch {
		case d.State == StateSentBack && d.Steps[1].State == StateSucceeded:
			reopened++
		case d.State == StateBlocked && d.BlockedReason == RoundsExhaustedReason("fix") && d.Steps[1].Outcome["followup"] == nil:
			blocked++
		}
	}
	if reopened != 1 || blocked != 1 {
		for _, d := range list {
			t.Logf("delivery %s state %s reason %q reopen step %+v", d.ID, d.State, d.BlockedReason, d.Steps[1])
		}
		t.Fatalf("reopened %d blocked %d, want one of each", reopened, blocked)
	}
}
