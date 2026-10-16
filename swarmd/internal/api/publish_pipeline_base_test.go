package api

import (
	"context"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: advancePipeline must classify absent exact-base evidence as an
// infrastructure failure, never admit a reviewer or send findings to the coder.
// Real durable pipeline/signal stores are the narrowest transition boundary.
func TestPipelineMissingExactBase(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	db := s.sessions.Store().Underlying()
	if err := s.sessions.Store().CreateSessionForAccount(store.SessionSnapshot{ID: "base-review-session"}, actor.UserID, actor.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	s.signals = store.NewSignalStore(db)
	c, err := s.pipelineCall(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("a", 40)
	ready, err := c.advancePipeline("coder", "run", controlMCPJobRefs{sessionID: "base-review-session"}, publish.Rule{}, map[string]any{"head": head})
	if err != nil || ready {
		t.Fatalf("advance: ready=%v error=%v", ready, err)
	}
	var state ReviewPipeline
	if _, err := db.GetJSON(pipelineKey(actor.AccountScopeID, "worker/coder/run"), &state); err != nil {
		t.Fatal(err)
	}
	if state.Next != "review_failed" || state.Round != 0 || state.ReviewerJobID != "" || state.Head != head {
		t.Fatalf("missing base not infrastructure failure: %+v", state)
	}
	page, err := s.signals.ListAfter(0, 10, store.SignalFilter{Account: actor.AccountScopeID})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Signals) != 1 || page.Signals[0].Kind != "job.review_failed" {
		t.Fatalf("unexpected receipts: %+v", page.Signals)
	}
}

// Purpose: the pipeline brief must supply an immutable diff range instead of
// relying on a private ref absent from independent clones. This pure formatter
// boundary checks exact bytes without requiring provider admission.
func TestPipelineExactBaseBrief(t *testing.T) {
	base, head := strings.Repeat("b", 40), strings.Repeat("a", 40)
	brief := pipelineBaseBrief(base, head)
	if !strings.Contains(brief, "Exact landing base commit: "+base) || !strings.Contains(brief, "git diff "+base+"..."+head) {
		t.Fatal(brief)
	}
	for _, invalid := range []string{"", "refs/swarm/publish/dev", "--all", strings.Repeat("z", 40)} {
		if pipelineExactCommit(invalid) {
			t.Fatalf("accepted %q", invalid)
		}
	}
	if !pipelineExactCommit(base) {
		t.Fatal("exact SHA rejected")
	}
}
