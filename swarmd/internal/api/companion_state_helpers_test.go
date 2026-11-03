package api

import "testing"

// waitCompanionMaintenance waits until no background Companion state pass is
// running. Every commit's nudge starts (or re-arms) its pass before the
// commit returns, so after the caller's last commit this waits for all of
// them.
func (s *Server) waitCompanionMaintenance() {
	m := &s.companionMaintenance
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.running) > 0 {
		m.idle.Wait()
	}
}

// claimSessionV3ExecutorRunForTest marks job's run in flight in the daemon's
// executor, for a test that writes the run's lifecycle itself: the
// executor's start and its refill pass skip an in-flight run, so the test
// stays the run's only writer. It must be called before the run is recorded
// pending. The returned release ends the claim.
func claimSessionV3ExecutorRunForTest(t *testing.T, e *sessionV3Executor, job sessionV3ExecutorJob) (release func()) {
	t.Helper()
	if e == nil {
		t.Fatal("the daemon has no V3 executor to claim the run in")
	}
	runKey := sessionV3ExecutorRunKey(job.SessionID, job.RunID)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.inFlightRuns[runKey] {
		t.Fatalf("run %s is already in flight", job.RunID)
	}
	e.inFlightRuns[runKey] = true
	return func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		delete(e.inFlightRuns, runKey)
	}
}

// sessionV3ExecutorStartedRunForTest reports whether the executor itself
// started job's run (its start records the run's state; a test's claim does
// not).
func sessionV3ExecutorStartedRunForTest(e *sessionV3Executor, job sessionV3ExecutorJob) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runStates[sessionV3ExecutorRunKey(job.SessionID, job.RunID)] != nil
}
