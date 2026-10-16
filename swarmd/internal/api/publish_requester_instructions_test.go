package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: advancePipeline must pass requester amendments, not worker output or
// automated findings, to review admission. Real V3 mutations exercise persisted
// provenance and ordering; intercepting admission is the narrowest provider-free
// boundary exposing the actual reviewer brief.
func TestReviewRequesterAmendments(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	const session = "amendment-session"
	ss := s.sessions.Store()
	if err := ss.CreateSessionForAccount(store.SessionSnapshot{ID: session}, actor.UserID, actor.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct{ id, role, text string }{
		{"original", "user", "Original brief"},
		{"request-z", "user", "Also change the parser"},
		{"worker", "assistant", "WORKER-ONLY"},
		{"pipeline-fix-run-head", "user", "REVIEWER-FINDINGS"},
		{"request-a", "user", "Correction: preserve whitespace"},
	} {
		_, err := ss.ApplyV3SessionMutation(store.V3SessionMutationInput{SessionID: session, UserID: actor.UserID, AccountScopeID: actor.AccountScopeID, ClientRequestID: m.id, PayloadHash: m.id, Kind: store.V3SessionMutationAppendMessage, Message: &store.MessageSnapshot{Role: m.role, Content: m.text}})
		if err != nil {
			t.Fatal(err)
		}
	}
	s.signals = store.NewSignalStore(ss.Underlying())
	var brief string
	s.controlMCPRoutesOnce.Do(func() {})
	s.controlMCPRoutes = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v3/workers" {
			_, _ = w.Write([]byte(`{"workers":[{"id":"reviewer"}]}`))
			return
		}
		if r.URL.Path == "/v3/projects" {
			_, _ = w.Write([]byte(`{"projects":[{"id":"project","workspaces":[{"path":"/workspace"}]}]}`))
			return
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if p, ok := body["prompt"].(string); ok {
			brief = p
		}
		http.Error(w, "stop at admission", 503)
	})
	c, err := s.pipelineCall(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.advancePipeline("coder", "run", controlMCPJobRefs{sessionID: session, projectID: "project", run: map[string]any{}}, publish.Rule{}, map[string]any{"head": strings.Repeat("a", 40)})
	if !strings.Contains(brief, "Later instructions from the requester") || !strings.Contains(brief, "amend the original brief") {
		t.Fatalf("missing amendments: %q", brief)
	}
	first, second := strings.Index(brief, "Also change the parser"), strings.Index(brief, "Correction: preserve whitespace")
	if first < 0 || second <= first {
		t.Fatalf("missing or unordered requester instructions: %q", brief)
	}
	_, err = ss.ApplyV3SessionMutation(store.V3SessionMutationInput{SessionID: session, UserID: actor.UserID, AccountScopeID: actor.AccountScopeID, ClientRequestID: "large-request", PayloadHash: "large-request", Kind: store.V3SessionMutationAppendMessage, Message: &store.MessageSnapshot{Role: "user", Content: strings.Repeat("x", 20000)}})
	if err != nil {
		t.Fatal(err)
	}
	bounded, err := c.requesterReviewInstructions(session)
	if err != nil || len(bounded) > 16100 || !strings.Contains(bounded, "size limit reached") {
		t.Fatalf("unbounded amendments: bytes=%d err=%v", len(bounded), err)
	}
	if err := ss.CreateSessionForAccount(store.SessionSnapshot{ID: "other-amendments"}, "other-user", "other-account"); err != nil {
		t.Fatal(err)
	}
	if text, err := c.requesterReviewInstructions("other-amendments"); err == nil || text != "" {
		t.Fatal("cross-account instructions exposed")
	}
	for _, excluded := range []string{"WORKER-ONLY", "REVIEWER-FINDINGS", "Original brief"} {
		if strings.Contains(brief, excluded) {
			t.Fatalf("unexpected %q in brief", excluded)
		}
	}
}
