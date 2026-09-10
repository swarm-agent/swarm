package automation

import (
	"context"
	"errors"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: SaveDefinition and CheckRun must bind canonical document content,
// not trust a revision key that SessionStore can rewrite. Domain fakes are the
// narrowest layer to inject same-version replacement and assert no grant/write.
func TestAutomationPlanContentPin(t *testing.T) {
	s, r, a, plans, p, scope, d := fixture(t)
	d.Enabled = true
	d.Authorization = store.AutomationAuthorizationPolicy{Mode: "approved_policy", ApprovalReference: "approval", ExpiresAt: 100001}
	created, _, err := s.SaveDefinition(context.Background(), p, scope, "check", "create", 0, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Definition.Plans[0].Plan.DocumentSHA256) != 64 {
		t.Fatal("missing content pin")
	}
	plans.plan.Document.Title = "Replaced instructions"
	if _, err := s.CheckRun(context.Background(), p, scope, "check", 1); !errors.Is(err, ErrDenied) {
		t.Fatal("mutable plan accepted", err)
	}
	if _, _, err := s.SaveDefinition(context.Background(), p, scope, "check", "repin", 1, d); !errors.Is(err, ErrDenied) {
		t.Fatal("omitted digest repinned plan", err)
	}
	if r.writes != 1 || a.executions != 1 {
		t.Fatal("rejected pin reached write/grant")
	}
	// Revocation must not prevent disabling the original pinned binding.
	d.Enabled = false
	if _, _, err := s.SaveDefinition(context.Background(), p, scope, "check", "disable", 1, d); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckRun(context.Background(), p, scope, "check", 2); !errors.Is(err, ErrDenied) {
		t.Fatal("disabled execution", err)
	}
}

// Purpose: Search must reject forged account scope before repository reads;
// oversized filters/cursors must not reach persistence. Domain counters expose
// this negative postcondition without depending on a transport implementation.
func TestAutomationSearchRejection(t *testing.T) {
	for _, kind := range []string{"account", "cursor", "query", "negative-limit"} {
		t.Run(kind, func(t *testing.T) {
			s, r, _, _, p, scope, _ := fixture(t)
			q := store.AutomationSearch{Scope: scope, Limit: 20}
			switch kind {
			case "account":
				q.Scope.AccountID = "foreign"
			case "cursor":
				q.Cursor = string(make([]byte, 4097))
			case "query":
				q.Query = string(make([]byte, 257))
			case "negative-limit":
				q.Limit = -1
			}
			if rows, cursor, err := s.Search(context.Background(), p, q); err == nil || len(rows) != 0 || cursor != "" {
				t.Fatal("invalid search accepted")
			}
			if r.reads != 0 || r.writes != 0 {
				t.Fatal("invalid search reached repository")
			}
		})
	}
}

// Purpose: canonicalContext is an optional evidence cache, not permission or
// durable outcome authority. Repeated historical sweeps must converge without
// erasing agent evidence or user locks. This pure boundary isolates ordering,
// capacity and malformed-cache negative cases without a scheduler or provider.
func TestCanonicalContextStableRecency(t *testing.T) {
	locked := map[string]string{"keep": "user instruction"}
	summaries := map[string]string{"agent": "untrusted evidence"}
	r := store.AutomationRecord{ID: "new", Revision: 3, Occurrence: &store.AutomationOccurrence{ScheduledAt: 200}}
	next, _, changed := canonicalContext(locked, summaries, r, "completed")
	if !changed || next["agent"] != summaries["agent"] || len(summaries) != 1 || locked["keep"] != "user instruction" { t.Fatal("cache mutated evidence") }
	for i := 0; i < 100; i++ {
		old := store.AutomationRecord{ID: "old", Revision: 100, Occurrence: &store.AutomationOccurrence{ScheduledAt: 100}}
		if _, _, changed := canonicalContext(locked, next, old, "old"); changed { t.Fatal("historical sweep replaced newest") }
		if _, _, changed := canonicalContext(locked, next, r, "completed"); changed { t.Fatal("replay changed cache") }
	}
	r.Revision++
	if _, _, changed := canonicalContext(locked, next, r, "updated"); !changed { t.Fatal("new revision ignored") }
	next["canonical-latest"] = "agent-owned malformed evidence"
	if _, _, changed := canonicalContext(locked, next, r, "updated"); changed { t.Fatal("malformed evidence overwritten") }
}

// Purpose: V3Runtime.Ensure must reject unsupported delegation restrictions
// before approval, session reads or worktree allocation, also on recovery. Nil
// downstream authorities deliberately make any accidental effect fail the test.
func TestExecutionRejectsUnenforceablePolicyBeforeEffects(t *testing.T) {
	for _, policy := range []store.AutomationAuthorizationPolicy{
		{AllowedTools: []string{"task"}}, {AllowedTools: []string{"manage_sessions"}},
	} {
		def := store.AutomationRecord{Scope: store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}, AutomationID: "automation", Revision: 1, Definition: &store.AutomationDefinition{Authorization: policy}}
		occ := store.AutomationRecord{Scope: def.Scope, AutomationID: def.AutomationID, Occurrence: &store.AutomationOccurrence{DefinitionRevision: 1}}
		if id, err := (&V3Runtime{}).Ensure(context.Background(), Principal{AccountID: "account"}, def, occ); id != "" || !errors.Is(err, ErrDenied) { t.Fatalf("restriction admitted: %q %v", id, err) }
	}
}
