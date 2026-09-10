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
	if err != nil { t.Fatal(err) }
	if len(created.Definition.Plans[0].Plan.DocumentSHA256) != 64 { t.Fatal("missing content pin") }
	plans.plan.Document.Title = "Replaced instructions"
	if _, err := s.CheckRun(context.Background(), p, scope, "check", 1); !errors.Is(err, ErrDenied) { t.Fatal("mutable plan accepted", err) }
	if _, _, err := s.SaveDefinition(context.Background(), p, scope, "check", "repin", 1, d); !errors.Is(err, ErrDenied) { t.Fatal("omitted digest repinned plan", err) }
	if r.writes != 1 || a.executions != 1 { t.Fatal("rejected pin reached write/grant") }
	// Revocation must not prevent disabling the original pinned binding.
	d.Enabled = false
	if _, _, err := s.SaveDefinition(context.Background(), p, scope, "check", "disable", 1, d); err != nil { t.Fatal(err) }
	if _, err := s.CheckRun(context.Background(), p, scope, "check", 2); !errors.Is(err, ErrDenied) { t.Fatal("disabled execution", err) }
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
			case "account": q.Scope.AccountID = "foreign"
			case "cursor": q.Cursor = string(make([]byte, 4097))
			case "query": q.Query = string(make([]byte, 257))
			case "negative-limit": q.Limit = -1
			}
			if rows, cursor, err := s.Search(context.Background(), p, q); err == nil || len(rows) != 0 || cursor != "" { t.Fatal("invalid search accepted") }
			if r.reads != 0 || r.writes != 0 { t.Fatal("invalid search reached repository") }
		})
	}
}
