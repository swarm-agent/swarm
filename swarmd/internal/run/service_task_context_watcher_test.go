package run

import (
	"errors"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type fakeTaskContextUsageAuthority struct {
	summary    pebblestore.SessionUsageSummary
	turn       pebblestore.SessionTurnUsageSnapshot
	lineage    pebblestore.DelegatedChildLineageRecord
	generation pebblestore.DelegatedChildGenerationRecord
}

func (f *fakeTaskContextUsageAuthority) GetUsageSummary(string) (pebblestore.SessionUsageSummary, bool, error) {
	return f.summary, true, nil
}
func (f *fakeTaskContextUsageAuthority) GetTurnUsage(string, string) (pebblestore.SessionTurnUsageSnapshot, bool, error) {
	return f.turn, true, nil
}
func (f *fakeTaskContextUsageAuthority) GetDelegatedChildLineage(string, string) (pebblestore.DelegatedChildLineageRecord, bool, error) {
	return f.lineage, true, nil
}
func (f *fakeTaskContextUsageAuthority) GetDelegatedChildGeneration(string, string, int) (pebblestore.DelegatedChildGenerationRecord, bool, error) {
	return f.generation, true, nil
}
func (f *fakeTaskContextUsageAuthority) UpdateDelegatedChildRun(input pebblestore.UpdateDelegatedChildRunInput) (pebblestore.DelegatedChildLineageRecord, bool, error) {
	f.lineage.CurrentRunID = input.RunID
	f.generation.RunID = input.RunID
	return f.lineage, true, nil
}

func taskContextWatcherFixture(tokens int64) (*taskContextWatcher, *fakeTaskContextUsageAuthority) {
	summary := pebblestore.SessionUsageSummary{
		SessionID: "child-1", AccountScopeID: "account-1", Provider: "codex", Model: "gpt-test",
		Source: "codex_api_usage", ContextWindow: 1000, TotalTokens: tokens, LastRunID: "run-1", UpdatedAt: 10,
	}
	authority := &fakeTaskContextUsageAuthority{
		summary: summary,
		turn: pebblestore.SessionTurnUsageSnapshot{
			SessionID: "child-1", RunID: "run-1", Provider: "codex", Model: "gpt-test", Source: "codex_api_usage",
			APIUsageRawPath: "response.usage", ContextWindow: 1000, TotalTokens: tokens, UpdatedAt: 10,
		},
		lineage:    pebblestore.DelegatedChildLineageRecord{AccountScopeID: "account-1", LogicalTaskID: "logical-1", CurrentGeneration: 1, CurrentSessionID: "child-1", Revision: 1},
		generation: pebblestore.DelegatedChildGenerationRecord{AccountScopeID: "account-1", LogicalTaskID: "logical-1", Generation: 1, Revision: 1, State: pebblestore.DelegatedChildGenerationActive, SessionID: "child-1"},
	}
	watcher := newTaskContextWatcher(authority, taskLaunchPrepared{
		LogicalTaskID: "logical-1", ChildSession: pebblestore.SessionSnapshot{ID: "child-1", AccountScopeID: "account-1"},
		SubagentProvider: "codex", SubagentModel: "gpt-test",
	})
	return watcher, authority
}

func TestTaskContextWatcherUsesExactEightyPercentBoundary(t *testing.T) {
	watcher, authority := taskContextWatcherFixture(799)
	decision, err := watcher.Boundary(RunContinuationBoundaryInput{SessionID: "child-1", RunID: "run-1", Provider: "codex", Model: "gpt-test"})
	if err != nil || decision.Kind != "" {
		t.Fatalf("799/1000 decision=%+v err=%v, want wait", decision, err)
	}
	authority.summary.TotalTokens, authority.turn.TotalTokens = 800, 800
	decision, err = watcher.Boundary(RunContinuationBoundaryInput{SessionID: "child-1", RunID: "run-1", Provider: "codex", Model: "gpt-test"})
	if err != nil || decision.Kind != RunContinuationBoundaryTaskRotation {
		t.Fatalf("800/1000 decision=%+v err=%v, want rotation", decision, err)
	}
}

func TestTaskContextWatcherRejectsUntrustedOrStaleFacts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*fakeTaskContextUsageAuthority)
	}{
		{"zero tokens", func(a *fakeTaskContextUsageAuthority) { a.summary.TotalTokens, a.turn.TotalTokens = 0, 0 }},
		{"wrong source", func(a *fakeTaskContextUsageAuthority) { a.summary.Source, a.turn.Source = "estimated", "estimated" }},
		{"wrong path", func(a *fakeTaskContextUsageAuthority) { a.turn.APIUsageRawPath = "request.bytes" }},
		{"wrong session", func(a *fakeTaskContextUsageAuthority) { a.summary.SessionID = "other" }},
		{"wrong run", func(a *fakeTaskContextUsageAuthority) { a.summary.LastRunID = "old-run" }},
		{"stale fact", func(a *fakeTaskContextUsageAuthority) { a.turn.UpdatedAt = 9 }},
		{"wrong model", func(a *fakeTaskContextUsageAuthority) { a.summary.Model = "other-model" }},
		{"wrong provider", func(a *fakeTaskContextUsageAuthority) { a.summary.Provider, a.turn.Provider = "google", "google" }},
		{"retired generation", func(a *fakeTaskContextUsageAuthority) {
			a.generation.State = pebblestore.DelegatedChildGenerationRetired
		}},
		{"stale lineage run", func(a *fakeTaskContextUsageAuthority) {
			a.lineage.CurrentRunID, a.generation.RunID = "old-run", "old-run"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			watcher, authority := taskContextWatcherFixture(900)
			tc.mutate(authority)
			decision, err := watcher.Boundary(RunContinuationBoundaryInput{SessionID: "child-1", RunID: "run-1", Provider: "codex", Model: "gpt-test"})
			if err != nil || decision.Kind != "" {
				t.Fatalf("decision=%+v err=%v, want wait", decision, err)
			}
		})
	}
}

func TestTaskContextWatcherRejectsBoundaryRuntimeMismatch(t *testing.T) {
	for _, input := range []RunContinuationBoundaryInput{
		{SessionID: "child-1", RunID: "run-1", Provider: "google", Model: "gpt-test"},
		{SessionID: "child-1", RunID: "run-1", Provider: "codex", Model: "other-model"},
	} {
		watcher, _ := taskContextWatcherFixture(900)
		decision, err := watcher.Boundary(input)
		if err != nil || decision.Kind != "" {
			t.Fatalf("input=%+v decision=%+v err=%v, want wait", input, decision, err)
		}
	}
}

func TestTaskContextWatcherIgnoresNonUsageInputs(t *testing.T) {
	summary := pebblestore.SessionUsageSummary{ContextWindow: 1000, TotalTokens: 799, Source: "codex_api_usage"}
	if taskUsageReachesRotationThreshold(summary) {
		t.Fatal("799 tokens unexpectedly reached threshold")
	}
	// Request bytes, MaxOutputTokens, and Compact state are intentionally absent
	// from both the summary and threshold function and cannot affect the result.
	summary.TotalTokens = 800
	if !taskUsageReachesRotationThreshold(summary) {
		t.Fatal("800 tokens did not reach threshold")
	}
}

func TestTaskRotationBoundaryErrorIsTyped(t *testing.T) {
	err := &TaskRotationBoundaryError{Decision: RunContinuationBoundaryDecision{Kind: RunContinuationBoundaryTaskRotation}}
	if !IsTaskRotationBoundary(err) || !errors.Is(err, err) {
		t.Fatal("typed Task rotation boundary was not detectable")
	}
}
