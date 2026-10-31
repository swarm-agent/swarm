package api

import (
	"reflect"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: the usage answer (swarm_get_usage / manage_fleet usage with
// detail) lists sessions, top_models, by_model and by_provider in a total
// order, so equal answers compare equal. Regression: rows that tie on the
// primary key (equal last_active_at, equal total_tokens) came out in map
// iteration order and flaked TestControlMCPWorkerUsage. Owner: the sort in
// handleSessionsV3UsageAt (via /v3/usage), reached through controlMCPGetUsage.
// A temporary Pebble store plus the real MCP handler is the narrowest layer
// that exercises the real map-built lists end to end.
func TestControlMCPUsageTieOrderIsTotal(t *testing.T) {
	c, _ := newMCPPresetTestCall(t)
	p := testPrincipal()
	store := c.server.sessions.Store()
	now := time.Now().UTC()
	when := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).UnixMilli()
	// All three share last_active_at and total_tokens; "c" costs more, "a"
	// and "b" tie on cost too, so only the id breaks their tie.
	for _, fixture := range []struct {
		name string
		cost float64
	}{{"b", 1}, {"c", 2}, {"a", 1}} {
		session := "session-" + fixture.name
		if err := store.CreateSession(pebblestore.SessionSnapshot{ID: session, UserID: p.UserID, AccountScopeID: p.AccountScopeID}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutTurnUsage(pebblestore.SessionTurnUsageSnapshot{SessionID: session, RunID: "run-" + fixture.name, AccountScopeID: p.AccountScopeID, UserID: p.UserID, Provider: "p-" + fixture.name, Model: "m-" + fixture.name, TotalTokens: 10, InputTokens: 10, EstimatedCostUSD: fixture.cost, CostProvenance: "provider", PriceStatus: "known", CreatedAt: when}); err != nil {
			t.Fatal(err)
		}
	}
	c.request = requestWithScopedToken(c.request, &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read"}})
	ids := func(rows []map[string]any, key string) []any {
		out := make([]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, row[key])
		}
		return out
	}
	var first map[string]any
	for run := 0; run < 20; run++ {
		result, err := controlMCPGetUsage(c, map[string]any{"detail": true})
		if err != nil {
			t.Fatal(err)
		}
		out := result.(map[string]any)
		delete(out, "workers")
		delete(out, "workers_note")
		sessions := ids(out["sessions"].([]map[string]any), "session_id")
		if want := []any{"session-c", "session-a", "session-b"}; !reflect.DeepEqual(sessions, want) {
			t.Fatalf("run %d sessions: got %v want %v", run, sessions, want)
		}
		wantModels := []any{"m-c", "m-a", "m-b"}
		if got := ids(out["top_models"].([]map[string]any), "model"); !reflect.DeepEqual(got, wantModels) {
			t.Fatalf("run %d top_models: got %v want %v", run, got, wantModels)
		}
		if got := ids(out["by_model"].([]map[string]any), "model"); !reflect.DeepEqual(got, wantModels) {
			t.Fatalf("run %d by_model: got %v want %v", run, got, wantModels)
		}
		if got, want := ids(out["by_provider"].([]map[string]any), "provider"), []any{"p-c", "p-a", "p-b"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d by_provider: got %v want %v", run, got, want)
		}
		if first == nil {
			first = out
		} else if !reflect.DeepEqual(out, first) {
			t.Fatalf("run %d answer differs: got=%v want=%v", run, out, first)
		}
	}
}
