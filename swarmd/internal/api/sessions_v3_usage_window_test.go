package api

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: Today means the UTC calendar day, and every dashboard projection
// must use matching persisted rollups, never lifetime summaries. Media is included
// exactly once in billed totals. Threat: historical/library rows contaminate a
// period response or text/media views disagree. Authority: handleSessionsV3UsageAt
// (the registered handler's clock-injected implementation), RecordTurnUsage and
// RecordMediaUsage. A real temporary store plus fixed-clock HTTP handler is the
// narrowest layer proving the date/filter folds and unchanged persisted records.
func TestSessionsV3UsageDashboardPeriodAccounting(t *testing.T) {
	server, svc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	principal := testPrincipal()
	midnight := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	now := midnight.Add(12 * time.Hour)
	for _, id := range []string{"historical", "mixed", "media-only", "idle"} {
		_, _, err := svc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
			SessionID: id, Title: id, AccountScopeID: principal.AccountScopeID,
			UserID: principal.UserID, WorkspacePath: t.TempDir(), WorkspaceName: "test",
			Preference: &pebblestore.ModelPreference{Provider: "google", Model: "gemini-3.8-flash", Thinking: "low"},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	recordTurn := func(id, run string, at time.Time, tokens int64, cost float64) {
		t.Helper()
		_, _, _, err := svc.RecordTurnUsage(id, pebblestore.SessionTurnUsageSnapshot{
			RunID: run, Provider: "google", Model: "test-text", Source: "test",
			InputTokens: tokens, TotalTokens: tokens, BilledTokens: tokens,
			EstimatedCostUSD: cost, PriceStatus: "known", CreatedAt: at.UnixMilli(),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	recordMedia := func(id, record string, at time.Time, cost float64) {
		t.Helper()
		if err := svc.RecordMediaUsage(pebblestore.SessionMediaUsageRecord{
			ID: record, SessionID: id, AccountScopeID: principal.AccountScopeID,
			Provider: "google", Model: "test-image", Kind: "image", MediaType: "image/png",
			CostUSD: cost, PriceStatus: "known", CreatedAt: at.UnixMilli(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	recordTurn("historical", "old-only", midnight.Add(-time.Millisecond), 900, 9)
	recordTurn("mixed", "old-mixed", midnight.Add(-time.Millisecond), 500, 5)
	recordTurn("mixed", "today-text", midnight, 100, 1.25)
	recordMedia("historical", "old-image", midnight.Add(-time.Millisecond), 8)
	recordMedia("mixed", "today-image", midnight, .5)
	recordMedia("media-only", "image-only", midnight.Add(time.Hour), .25)
	if err := svc.ArchiveSession("media-only"); err != nil {
		t.Fatal(err)
	}
	before, err := svc.Store().ListAccountUsageRollups(principal.AccountScopeID)
	if err != nil {
		t.Fatal(err)
	}
	lifetimeBefore, ok, err := svc.Store().GetUsageSummary("historical")
	if err != nil || !ok || lifetimeBefore.TotalTokens != 900 {
		t.Fatalf("missing lifetime trap fixture: %+v, %v", lifetimeBefore, err)
	}
	for _, tc := range []struct {
		name, query string
		tokens      int64
		cost, media float64
		sessions    int
	}{
		{"today", "time_range=today", 100, 2, .75, 2},
		{"today-session", "time_range=today&session_id=mixed", 100, 1.75, .5, 1},
		{"historical-only", "time_range=today&session_id=historical", 0, 0, 0, 0},
		{"media-model", "time_range=today&model=test-image", 0, .75, .75, 2},
		{"other-provider", "time_range=today&provider=openai", 0, 0, 0, 0},
		{"active", "time_range=today&archived_mode=exclude", 100, 1.75, .5, 1},
		{"archived", "time_range=today&archived_mode=only", 0, .25, .25, 1},
		{"custom-day", "start_date=2026-09-19&end_date=2026-09-19", 1400, 22, 8, 2},
		{"empty-day", "start_date=2026-09-21&end_date=2026-09-21", 0, 0, 0, 0},
		{"all-time", "time_range=all", 1500, 24, 8.75, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/usage?"+tc.query, nil))
			server.handleSessionsV3UsageAt(w, req, now)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			var got SessionUsageDashboardResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			assertCost := func(label string, value, want float64) {
				t.Helper()
				if math.Abs(value-want) > 1e-9 {
					t.Fatalf("%s = %v, want %v", label, value, want)
				}
			}
			if got.Summary.TotalTokens != tc.tokens || got.Summary.TotalSessions != tc.sessions || len(got.RecentSessions) != tc.sessions {
				t.Fatalf("wrong period population: summary=%+v rows=%+v", got.Summary, got.RecentSessions)
			}
			assertCost("total", got.Summary.TotalCostUSD, tc.cost)
			assertCost("media subset", got.Summary.MediaCostUSD, tc.media)
			assertCost("media summary", got.Media.TotalCostUSD, tc.media)
			var rowCost, dayCost, providerCost, modelCost float64
			var rowTokens int64
			for _, row := range got.RecentSessions {
				rowCost += row.CostUSD
				rowTokens += row.TotalTokens
			}
			for _, day := range got.Daily {
				dayCost += day.CostUSD
			}
			for _, provider := range got.ByProvider {
				providerCost += provider.CostUSD
			}
			for _, model := range got.ByModel {
				modelCost += model.CostUSD
			}
			if rowTokens != tc.tokens {
				t.Fatalf("row tokens %d != %d", rowTokens, tc.tokens)
			}
			for label, cost := range map[string]float64{"rows": rowCost, "days": dayCost, "providers": providerCost, "models": modelCost} {
				assertCost(label, cost, tc.cost)
			}
		})
	}
	// Limiting visible rows must not shrink aggregate or per-provider/model counts.
	w := httptest.NewRecorder()
	server.handleSessionsV3UsageAt(w, withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/usage?time_range=today&session_limit=1", nil)), now)
	var limited SessionUsageDashboardResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &limited) != nil {
		t.Fatalf("limited response: %d %s", w.Code, w.Body.String())
	}
	if len(limited.RecentSessions) != 1 || limited.Summary.TotalSessions != 2 || limited.Summary.TotalCostUSD != 2 || len(limited.ByProvider) != 1 || limited.ByProvider[0].Sessions != 2 {
		t.Fatalf("display limit changed accounting population: %+v", limited)
	}
	for _, model := range limited.ByModel {
		want := 1
		if model.Model == "test-image" {
			want = 2
		}
		if model.Sessions != want {
			t.Fatalf("model count: %+v, want %d", model, want)
		}
	}
	// The same instant in a non-UTC zone must select the same UTC calendar day.
	w = httptest.NewRecorder()
	server.handleSessionsV3UsageAt(w, withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/usage?time_range=today", nil)), now.In(time.FixedZone("UTC+14", 14*60*60)))
	var zoned SessionUsageDashboardResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &zoned) != nil || zoned.Summary.TotalCostUSD != 2 {
		t.Fatalf("Today used local rather than UTC day: %d %s", w.Code, w.Body.String())
	}
	after, err := svc.Store().ListAccountUsageRollups(principal.AccountScopeID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("dashboard GET changed rollups: %v", err)
	}
	lifetimeAfter, ok, err := svc.Store().GetUsageSummary("historical")
	if err != nil || !ok || !reflect.DeepEqual(lifetimeBefore, lifetimeAfter) {
		t.Fatalf("dashboard GET changed lifetime usage: %v", err)
	}
}
