package api

import (
	"context"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/executioncapacity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: an AI asking "how many agents are running" gets the right number
// from one field. swarm_get_usage (which swarm_fleet_status shows as
// usage_today) names the sessions that used a model in the period for what
// they are (sessions_today, sessions_7d), never active_sessions, and carries
// running_now: the runs holding an execution lease, the same number as the
// drain's blocking runs. swarm_status carries the same running_now. With
// sessions used today and nothing running, running_now is 0; with one run
// held, it is 1 on both tools, equal to the drain's blockers, while the
// session count does not move.
// Threat: the live report: active_sessions: 102 on a machine with nothing
// running, read by the person's AI as 102 agents at work.
// Owners: controlMCPGetUsage and controlMCPStatus over the real /v3/usage
// (handleSessionsV3UsageAt) and /v3/box/summary (handleBoxSummary) handlers,
// Server.runningNow and the real permission service's
// executioncapacity.Manager (HeldSessions, DrainStatus): the narrowest layer
// joining the usage records, the MCP answer and the live leases.
func TestRunningNowCountsHeldRunsNotSessionsUsedToday(t *testing.T) {
	c, _ := newMCPPresetTestCall(t)
	p := testPrincipal()
	store := c.server.sessions.Store()
	now := time.Now().UTC()
	for _, id := range []string{"used-1", "used-2"} {
		if err := store.CreateSession(pebblestore.SessionSnapshot{ID: id, UserID: p.UserID, AccountScopeID: p.AccountScopeID}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutTurnUsage(pebblestore.SessionTurnUsageSnapshot{SessionID: id, RunID: "run-" + id, AccountScopeID: p.AccountScopeID, UserID: p.UserID, Provider: "fixture", Model: "fixture", TotalTokens: 10, InputTokens: 10, EstimatedCostUSD: 1, CostProvenance: "provider", PriceStatus: "known", CreatedAt: now.UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}
	usage := func(period string) map[string]any {
		t.Helper()
		result, err := controlMCPGetUsage(c, map[string]any{"period": period})
		if err != nil {
			t.Fatal(err)
		}
		summary, _ := result.(map[string]any)["summary"].(map[string]any)
		if summary == nil {
			t.Fatalf("usage %s has no summary: %v", period, result)
		}
		if _, has := summary["active_sessions"]; has {
			t.Fatalf("usage %s still reports active_sessions: %v", period, summary)
		}
		return summary
	}
	status := func() map[string]any {
		t.Helper()
		call := *c
		call.request = requestWithScopedToken(c.request, &pebblestore.ScopedTokenRecord{Scopes: []string{"signals:read"}})
		result, err := controlMCPStatus(&call, nil)
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]any)
	}

	idle := usage("today")
	if idle["sessions_today"] != float64(2) || idle["running_now"] != float64(0) {
		t.Fatalf("nothing running: summary = %v, want sessions_today 2 and running_now 0", idle)
	}
	if got := status()["running_now"]; got != float64(0) {
		t.Fatalf("nothing running: swarm_status running_now = %v, want 0", got)
	}

	manager := c.server.ExecutionCapacity()
	if manager == nil {
		t.Fatal("fixture has no execution capacity manager")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lease, err := manager.Acquire(ctx, executioncapacity.AcquireRequest{AccountScopeID: p.AccountScopeID, SessionID: "used-1", RunID: "run-held"})
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	blockers := float64(len(manager.DrainStatus().Blockers))
	if blockers != 1 {
		t.Fatalf("drain blockers = %v, want the held run", blockers)
	}
	busy := usage("today")
	if busy["running_now"] != blockers || busy["sessions_today"] != float64(2) {
		t.Fatalf("one run held: summary = %v, want running_now %v (the drain's blockers) and sessions_today 2", busy, blockers)
	}
	if got := status()["running_now"]; got != blockers {
		t.Fatalf("one run held: swarm_status running_now = %v, want %v", got, blockers)
	}
	week := usage("7d")
	if _, has := week["sessions_today"]; has || week["sessions_7d"] != float64(2) || week["running_now"] != blockers {
		t.Fatalf("7d summary = %v, want sessions_7d 2 and running_now %v", week, blockers)
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("release lease: %v", err)
	}
	if got := usage("today")["running_now"]; got != float64(0) {
		t.Fatalf("released: running_now = %v, want 0", got)
	}
}
