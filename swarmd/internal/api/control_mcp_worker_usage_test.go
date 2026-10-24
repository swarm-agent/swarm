package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: both MCP tools expose real account-owned worker receipts without
// double-counting media, disguising rolling windows, or adding write authority.
// Owners: controlMCPListWorkerJobs, controlMCPGetUsage, GetUsageScope/Day and
// CountWorkerRuns. A temporary Pebble store plus real handlers is the narrowest
// layer proving attribution, time boundaries, period-specific ordering and that
// automations-only callers retain listing access without receiving usage data.
func TestControlMCPWorkerUsage(t *testing.T) {
	c, ws := newMCPPresetTestCall(t)
	p := testPrincipal()
	store := c.server.sessions.Store()
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	seed := func(account, name string, tokens int64, cost float64, media bool) string {
		t.Helper()
		worker, err := c.server.sessions.CreateWorker(context.Background(), account, p.UserID, pebblestore.CreateWorkerRequest{Role: "coder", Name: name, IdempotencyKey: name})
		if err != nil {
			t.Fatal(err)
		}
		session := "session-" + name
		run := "run-" + name
		if err := store.CreateSession(pebblestore.SessionSnapshot{ID: session, UserID: p.UserID, AccountScopeID: account, Metadata: map[string]any{"worker_id": worker.ID, "worker_run_id": run}}); err != nil {
			t.Fatal(err)
		}
		if _, err := ws.RecordWorkerRun(account, pebblestore.WorkerRunRecord{ID: run, WorkerID: worker.ID, UserID: p.UserID, SessionID: session, Status: "succeeded", RequestSource: "direct", CreatedAt: today.UnixMilli()}); err != nil {
			t.Fatal(err)
		}
		for i, when := range []time.Time{today, today.AddDate(0, 0, -40)} {
			provenance := "provider"
			if i == 1 {
				provenance = "provider_estimate"
				if name == "low" {
					cost = 10
				}
			}
			if err := store.PutTurnUsage(pebblestore.SessionTurnUsageSnapshot{SessionID: session, RunID: provenance, AccountScopeID: account, UserID: p.UserID, Provider: "fixture", Model: "fixture", TotalTokens: tokens, InputTokens: tokens, EstimatedCostUSD: cost, CostProvenance: provenance, PriceStatus: "known", CreatedAt: when.UnixMilli()}); err != nil {
				t.Fatal(err)
			}
		}
		if media {
			if err := store.PutMediaUsage(pebblestore.SessionMediaUsageRecord{ID: "media-" + name, SessionID: session, AccountScopeID: account, UserID: p.UserID, Provider: "fixture", Model: "fixture", Kind: "image", CostUSD: 3, PriceStatus: "known", CreatedAt: today.UnixMilli()}); err != nil {
				t.Fatal(err)
			}
		}
		return worker.ID
	}
	low := seed(p.AccountScopeID, "low", 10, 1, false)
	high := seed(p.AccountScopeID, "high", 20, 2, true)
	seed("other-account", "foreign", 999, 99, true)
	check := func(row map[string]any, id string) {
		t.Helper()
		tokens, cost, media := int64(10), float64(1), int64(0)
		if id == high {
			tokens, cost, media = 20, 2, 1
		}
		for _, period := range []string{"today", "all_time"} {
			v := row[period].(map[string]any)
			multiplier := int64(1)
			if period == "all_time" {
				multiplier = 2
			}
			expectedCost := cost*float64(multiplier) + 3*float64(media)
			if id == low && period == "all_time" {
				expectedCost = 11
			}
			if v["total_tokens"] != tokens*multiplier || v["cost_usd"] != expectedCost || v["media_cost_usd"] != 3*float64(media) || v["media_count"] != media || v["coverage"] != "observed_receipts_only" || v["history_complete"] != false {
				t.Fatalf("%s %s: %#v", id, period, v)
			}
		}
	}
	c.request = requestWithScopedToken(c.request, &pebblestore.ScopedTokenRecord{Scopes: []string{"automations:read", "sessions:read"}})
	listed, err := controlMCPListWorkerJobs(c, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	rows := listed.(map[string]any)["workers"].([]map[string]any)
	if len(rows) != 2 {
		t.Fatalf("workers: %v", rows)
	}
	for _, row := range rows {
		id := row["id"].(string)
		if id != low && id != high {
			t.Fatalf("foreign worker: %v", row)
		}
		usage := row["usage"].(map[string]any)
		check(usage, id)
		if usage["jobs"] != 1 {
			t.Fatalf("jobs: %v", usage)
		}
	}
	// Detailed usage needs only its existing sessions:read permission.
	c.request = requestWithScopedToken(c.request, &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read"}})
	for _, period := range []string{"", "today", "7d", "30d"} {
		result, err := controlMCPGetUsage(c, map[string]any{"detail": true, "period": period})
		if err != nil {
			t.Fatal(err)
		}
		out := result.(map[string]any)
		rows := out["workers"].([]map[string]any)
		first, second, sortPeriod := low, high, "all_time"
		if period == "" || period == "today" {
			first, second, sortPeriod = high, low, "today"
		}
		if len(rows) != 2 || rows[0]["id"] != first || rows[1]["id"] != second || !strings.Contains(out["workers_note"].(string), "sorted by "+sortPeriod+".cost_usd descending") {
			t.Fatalf("detail: %v", out)
		}
		for _, row := range rows {
			check(row, row["id"].(string))
		}
	}
	result, err := controlMCPGetUsage(c, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := result.(map[string]any)["workers"]; exists {
		t.Fatal("compact usage includes worker detail")
	}
	// Denied calls cannot dispatch or mutate the store.
	before, _, err := store.GetUsageScope(p.AccountScopeID, "worker", "", high)
	if err != nil {
		t.Fatal(err)
	}
	// Worker binding must not be bypassed by direct usage-store reads. Exercise
	// the MCP entry point and both helpers: rejection must return no partial
	// result (including other workers' IDs, names or spend), and leave receipts
	// unchanged. This store-backed test covers the worker collection boundary
	// preserved by workerUsageDetail and workerUsage without a live server.
	c.request = requestWithScopedToken(c.request, &pebblestore.ScopedTokenRecord{WorkerID: low, Scopes: []string{"sessions:read"}})
	if result, err := controlMCPGetUsage(c, map[string]any{"detail": true}); err == nil || result != nil {
		t.Fatalf("worker-bound detail must fail without data: result=%v err=%v", result, err)
	}
	if result, err := c.workerUsageDetail("today"); err == nil || result != nil {
		t.Fatalf("worker-bound enumeration must fail without data: result=%v err=%v", result, err)
	}
	for _, id := range []string{low, high} {
		if result, err := c.workerUsage(id, today.Format("2006-01-02")); err == nil || result != nil {
			t.Fatalf("worker-bound usage must fail without data: result=%v err=%v", result, err)
		}
	}
	afterBound, _, err := store.GetUsageScope(p.AccountScopeID, "worker", "", high)
	if err != nil || before != afterBound {
		t.Fatalf("worker-bound read mutated usage: %v", err)
	}
	c.request = requestWithScopedToken(c.request, &pebblestore.ScopedTokenRecord{Scopes: []string{"automations:read"}})
	listed, err = controlMCPListWorkerJobs(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows = listed.(map[string]any)["workers"].([]map[string]any)
	if len(rows) != 2 {
		t.Fatalf("listing lost workers: %v", rows)
	}
	for _, row := range rows {
		if row["id"] != low && row["id"] != high {
			t.Fatalf("foreign worker: %v", row)
		}
		if _, exists := row["usage"]; exists {
			t.Fatalf("usage leaked without sessions:read: %v", row)
		}
	}
	c.next = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("denied call dispatched") })
	if _, err := controlMCPGetUsage(c, map[string]any{"detail": true}); err == nil {
		t.Fatal("usage accepted missing read scope")
	}
	after, _, err := store.GetUsageScope(p.AccountScopeID, "worker", "", high)
	if err != nil || before != after {
		t.Fatalf("denied read mutated usage: %v", err)
	}
}
