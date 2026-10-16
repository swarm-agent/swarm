package api

import (
	"context"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: advancePipeline must not narrow a never-approved job to conflict-only
// review. Durable queue/pipeline state is the narrowest boundary that reproduces
// a job queued before reviews were enabled, then returned for a rebase conflict.
func TestUnreviewedRebaseUsesFullReview(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	db := s.sessions.Store().Underlying()
	if err := s.sessions.Store().CreateSessionForAccount(store.SessionSnapshot{ID: "rebase-recovery-session"}, actor.UserID, actor.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	s.signals = store.NewSignalStore(db)
	q, err := s.landingQueue()
	if err != nil {
		t.Fatal(err)
	}
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	folder := t.TempDir()
	_, err = q.Enqueue(publish.Entry{Account: actor.AccountScopeID, Request: publish.Request{Folder: folder, JobID: "worker/coder/run", Head: head, BaseCommit: base}})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Process(context.Background(), actor.AccountScopeID, folder, publish.QueueHooks{Prepare: func(context.Context, *publish.Entry) error {
		return &publish.SendBackError{Reason: "Rebase conflict: docs"}
	}, SendBack: func(context.Context, publish.Entry) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	c, err := s.pipelineCall(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.advancePipeline("coder", "run", controlMCPJobRefs{sessionID: "rebase-recovery-session"}, publish.Rule{}, map[string]any{"head": head, "base_commit": base})
	if err != nil {
		t.Fatal(err)
	}
	var state ReviewPipeline
	if _, err := db.GetJSON(pipelineKey(actor.AccountScopeID, "worker/coder/run"), &state); err != nil {
		t.Fatal(err)
	}
	if state.Rebase || state.Base != base || !strings.HasPrefix(state.Findings, "Review infrastructure admission failed:") || strings.Contains(state.Findings, "exact rebase comparison") {
		t.Fatalf("unreviewed job narrowed or stuck: %+v", state)
	}
}

// Purpose: infrastructure failure is retried without coder activity, but does
// not consume fix rounds or retry forever. advancePipeline and its durable
// state are the narrowest boundary proving restart-safe bounded admission.
func TestReviewInfrastructureRetryBound(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	db := s.sessions.Store().Underlying()
	if err := s.sessions.Store().CreateSessionForAccount(store.SessionSnapshot{ID: "infra-retry-session"}, actor.UserID, actor.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	s.signals = store.NewSignalStore(db)
	c, err := s.pipelineCall(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("c", 40)
	key := pipelineKey(actor.AccountScopeID, "worker/coder/run")
	state := ReviewPipeline{Head: head, Next: "review_failed", Findings: "Review infrastructure failure: exact landing base commit is unavailable"}
	if err := db.PutJSON(key, state); err != nil {
		t.Fatal(err)
	}
	_, err = c.advancePipeline("coder", "run", controlMCPJobRefs{sessionID: "infra-retry-session"}, publish.Rule{}, map[string]any{"head": head})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if _, err := db.GetJSON(key, &got); err != nil {
		t.Fatal(err)
	}
	if got["infrastructure_attempts"] != float64(1) {
		t.Fatalf("infrastructure not retried: %v", got)
	}
	// Immediate replay must honor the persisted backoff.
	_, err = c.advancePipeline("coder", "run", controlMCPJobRefs{sessionID: "infra-retry-session"}, publish.Rule{}, map[string]any{"head": head})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetJSON(key, &got); err != nil {
		t.Fatal(err)
	}
	if got["infrastructure_attempts"] != float64(1) {
		t.Fatalf("backoff bypassed: %v", got)
	}
	for i := 2; i <= 4; i++ {
		got["retry_after"] = float64(1)
		if err := db.PutJSON(key, got); err != nil {
			t.Fatal(err)
		}
		_, err = c.advancePipeline("coder", "run", controlMCPJobRefs{sessionID: "infra-retry-session"}, publish.Rule{}, map[string]any{"head": head})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.GetJSON(key, &got); err != nil {
			t.Fatal(err)
		}
		want := i
		if want > 3 {
			want = 3
		}
		if got["infrastructure_attempts"] != float64(want) || got["round"] != float64(0) {
			t.Fatalf("retry bound/fix rounds: %v", got)
		}
	}
}
