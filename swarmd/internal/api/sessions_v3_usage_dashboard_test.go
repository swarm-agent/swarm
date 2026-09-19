package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3UsageDashboard(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	now := time.Now().UTC().UnixMilli()

	// Create test session
	sessionID := "sess_usage_dashboard_test_1"
	_, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      sessionID,
		Title:          "Dashboard Feature Build",
		AccountScopeID: testPrincipal().AccountScopeID,
		UserID:         testPrincipal().UserID,
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "test-ws",
		Preference:     &pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.6-sol", Thinking: "medium"},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// 1. Record a Google Gemini 3.8 Flash turn
	_, _, _, err = sessionSvc.RecordTurnUsage(sessionID, pebblestore.SessionTurnUsageSnapshot{
		SessionID:       sessionID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		UserID:          testPrincipal().UserID,
		RunID:           "run-g1",
		Provider:        "google",
		Model:           "gemini-3.8-flash",
		Source:          "google_api_usage",
		InputTokens:     100000,
		OutputTokens:    5000,
		CacheReadTokens: 20000,
		TotalTokens:     105000,
		CreatedAt:       now - 5000,
		UpdatedAt:       now - 5000,
	})
	if err != nil {
		t.Fatalf("record google turn: %v", err)
	}

	// 2. Record a Codex turn (should have $0 billed cost, but nominal cost > 0)
	_, _, _, err = sessionSvc.RecordTurnUsage(sessionID, pebblestore.SessionTurnUsageSnapshot{
		SessionID:       sessionID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		UserID:          testPrincipal().UserID,
		RunID:           "run-c1",
		Provider:        "codex",
		Model:           "gpt-5.6-sol",
		Source:          "codex_api_usage",
		InputTokens:     40000,
		OutputTokens:    2000,
		CacheReadTokens: 10000,
		TotalTokens:     42000,
		CreatedAt:       now - 3000,
		UpdatedAt:       now - 3000,
	})
	if err != nil {
		t.Fatalf("record codex turn: %v", err)
	}

	// 3. Record an Anthropic turn
	_, _, _, err = sessionSvc.RecordTurnUsage(sessionID, pebblestore.SessionTurnUsageSnapshot{
		SessionID:       sessionID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		UserID:          testPrincipal().UserID,
		RunID:           "run-a1",
		Provider:        "anthropic",
		Model:           "claude-sonnet-5",
		Source:          "anthropic_api_usage",
		InputTokens:     50000,
		OutputTokens:    1000,
		CacheReadTokens: 10000,
		TotalTokens:     51000,
		CreatedAt:       now - 1000,
		UpdatedAt:       now - 1000,
	})
	if err != nil {
		t.Fatalf("record anthropic turn: %v", err)
	}

	// 4. Record media usage
	mediaRecords := []pebblestore.SessionMediaUsageRecord{
		{
			ID:             "media-img-1",
			SessionID:      sessionID,
			AccountScopeID: testPrincipal().AccountScopeID,
			MediaType:      "image/png",
			Kind:           "image",
			Provider:       "google",
			Model:          "imagen-3.0",
			Filename:       "hero-banner.png",
			Size:           102400,
			CostUSD:        0.04,
			PriceStatus:    "known",
			PricingSummary: "$0.04 per image (snapshot snap-1)",
			CreatedAt:      now - 4000,
		},
		{
			ID:             "media-vid-1",
			SessionID:      sessionID,
			AccountScopeID: testPrincipal().AccountScopeID,
			MediaType:      "video/mp4",
			Kind:           "video",
			Provider:       "google",
			Model:          "veo-2.0",
			Filename:       "feature-teaser.mp4",
			Size:           2048000,
			CostUSD:        1.20,
			PriceStatus:    "known",
			PricingSummary: "$1.20 per video (snapshot snap-1)",
			CreatedAt:      now - 2000,
		},
		{
			ID:             "media-aud-1",
			SessionID:      sessionID,
			AccountScopeID: testPrincipal().AccountScopeID,
			MediaType:      "audio/mp3",
			Kind:           "audio",
			Provider:       "google",
			Model:          "lyria-3.5",
			Filename:       "background-music.mp3",
			Size:           512000,
			CostUSD:        0.08,
			PriceStatus:    "known",
			PricingSummary: "$0.08 per audio (snapshot snap-1)",
			CreatedAt:      now - 1000,
		},
	}
	for _, m := range mediaRecords {
		if err := sessionSvc.RecordMediaUsage(m); err != nil {
			t.Fatalf("record media usage: %v", err)
		}
	}

	// 5. Test GET /v3/usage
	req := httptest.NewRequest(http.MethodGet, "/v3/usage?time_range=30d", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, withTestPrincipal(req))

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp SessionUsageDashboardResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if !resp.OK {
		t.Fatal("expected ok to be true")
	}

	// Verify Summary
	if resp.Summary.TotalTurns != 3 {
		t.Fatalf("expected 3 total turns, got %d", resp.Summary.TotalTurns)
	}
	if resp.Summary.TotalTokens != (105000 + 42000 + 51000) {
		t.Fatalf("expected %d total tokens, got %d", (105000 + 42000 + 51000), resp.Summary.TotalTokens)
	}
	if resp.Summary.CachedTokens != (20000 + 10000 + 10000) {
		t.Fatalf("expected 40000 cached tokens, got %d", resp.Summary.CachedTokens)
	}
	if resp.Summary.TotalSessions != 1 {
		t.Fatalf("expected 1 session, got %d", resp.Summary.TotalSessions)
	}
	if resp.Summary.TotalMediaCalls != 3 {
		t.Fatalf("expected 3 media calls, got %d", resp.Summary.TotalMediaCalls)
	}
	if resp.Summary.MediaCostUSD <= 0 {
		t.Fatalf("expected positive media cost, got %f", resp.Summary.MediaCostUSD)
	}
	if resp.Summary.TotalCostUSD <= 0 {
		t.Fatalf("expected positive total cost, got %f", resp.Summary.TotalCostUSD)
	}
	if resp.Summary.CodexNominalCostUSD <= 0 {
		t.Fatalf("expected positive Codex nominal cost, got %f", resp.Summary.CodexNominalCostUSD)
	}

	// Verify Daily
	if len(resp.Daily) == 0 {
		t.Fatal("expected daily usage records")
	}
	today := time.Now().UTC().Format("2006-01-02")
	foundToday := false
	for _, d := range resp.Daily {
		if d.Date == today {
			foundToday = true
			if d.Turns != 3 {
				t.Fatalf("expected 3 turns today, got %d", d.Turns)
			}
			if d.MediaCalls != 3 {
				t.Fatalf("expected 3 media calls today, got %d", d.MediaCalls)
			}
			if len(d.ModelsUsed) < 3 {
				t.Fatalf("expected at least 3 models used today, got %d", len(d.ModelsUsed))
			}
		}
	}
	if !foundToday {
		t.Fatalf("expected today (%s) in daily records", today)
	}

	// Verify Providers
	codexFound := false
	googleFound := false
	for _, p := range resp.ByProvider {
		if p.Provider == "codex" {
			codexFound = true
			if !p.IsSubscription {
				t.Fatal("expected Codex is_subscription to be true")
			}
			if p.CostUSD != 0.0 {
				t.Fatalf("expected Codex billed cost to be 0.00, got %f", p.CostUSD)
			}
			if p.CodexNominalCostUSD <= 0 {
				t.Fatalf("expected positive Codex nominal cost, got %f", p.CodexNominalCostUSD)
			}
		}
		if p.Provider == "google" {
			googleFound = true
			if p.CostUSD <= 0 {
				t.Fatalf("expected positive Google cost, got %f", p.CostUSD)
			}
		}
	}
	if !codexFound {
		t.Fatal("expected codex in by_provider")
	}
	if !googleFound {
		t.Fatal("expected google in by_provider")
	}

	// Verify Models
	if len(resp.ByModel) < 3 {
		t.Fatalf("expected at least 3 models, got %d", len(resp.ByModel))
	}

	// Verify Media
	if resp.Media.ImageCount != 1 || resp.Media.VideoCount != 1 || resp.Media.AudioCount != 1 {
		t.Fatalf("media counts mismatch: images=%d videos=%d audio=%d",
			resp.Media.ImageCount, resp.Media.VideoCount, resp.Media.AudioCount)
	}

	// 6. Test Provider Filter
	reqFilter := httptest.NewRequest(http.MethodGet, "/v3/usage?provider=google", nil)
	wFilter := httptest.NewRecorder()
	server.Handler().ServeHTTP(wFilter, withTestPrincipal(reqFilter))

	if wFilter.Code != http.StatusOK {
		t.Fatalf("filter expected 200, got %d", wFilter.Code)
	}
	var respFilter SessionUsageDashboardResponse
	if err := json.Unmarshal(wFilter.Body.Bytes(), &respFilter); err != nil {
		t.Fatalf("unmarshal filter: %v", err)
	}
	if respFilter.Summary.TotalTurns != 1 {
		t.Fatalf("expected 1 turn for Google filter, got %d", respFilter.Summary.TotalTurns)
	}
	if len(respFilter.ByProvider) != 1 || respFilter.ByProvider[0].Provider != "google" {
		t.Fatalf("expected only google in by_provider: %+v", respFilter.ByProvider)
	}

	// 7. Also test alias route /v3/sessions:usage
	reqAlias := httptest.NewRequest(http.MethodGet, "/v3/sessions:usage", nil)
	wAlias := httptest.NewRecorder()
	server.Handler().ServeHTTP(wAlias, withTestPrincipal(reqAlias))
	if wAlias.Code != http.StatusOK {
		t.Fatalf("alias route expected 200, got %d", wAlias.Code)
	}
}

func TestCalculateTurnCostFormulas(t *testing.T) {
	pricing := map[string]catalogPricingLookup{
		"google:gemini-3.8-flash": {
			InputPrice:  0.75,
			OutputPrice: 3.75,
			DisplayName: "Gemini 3.8 Flash",
		},
		"openai:gpt-5.6-sol": {
			InputPrice:  5.0,
			OutputPrice: 30.0,
			DisplayName: "GPT-5.6 Sol",
		},
		"anthropic:claude-sonnet-5": {
			InputPrice:  2.0,
			OutputPrice: 10.0,
			DisplayName: "Claude Sonnet 5",
		},
		"codex_nominal:gpt-5.6-sol": {
			InputPrice:  5.0,
			OutputPrice: 30.0,
			CachedPrice: 2.5,
			HasCached:   true,
			DisplayName: "GPT-5.6 Sol",
		},
	}

	// Test Google Gemini prompt caching (75% discount on cached tokens)
	recGoogle := pebblestore.SessionTurnUsageSnapshot{
		Provider:        "google",
		Model:           "gemini-3.8-flash",
		InputTokens:     100000, // Total prompt tokens
		CacheReadTokens: 20000,  // Cached tokens -> 80,000 uncached
		OutputTokens:    5000,   // Output tokens
	}
	cost, codexNominal := calculateTurnCost(recGoogle, pricing)
	if codexNominal != 0 {
		t.Errorf("expected codexNominal 0 for google, got %f", codexNominal)
	}
	// Expected:
	// Uncached input: 80,000 / 1M * 0.75 = 0.06
	// Cached input: 20,000 / 1M * (0.75 * 0.25) = 0.00375
	// Output: 5,000 / 1M * 3.75 = 0.01875
	// Total: 0.0825
	expectedGoogle := 0.0825
	if cost < expectedGoogle-0.0001 || cost > expectedGoogle+0.0001 {
		t.Errorf("Google cost = %f, want ~%f", cost, expectedGoogle)
	}

	// Test Codex subscription ($0 direct, positive nominal)
	recCodex := pebblestore.SessionTurnUsageSnapshot{
		Provider:        "codex",
		Model:           "gpt-5.6-sol",
		InputTokens:     40000,
		CacheReadTokens: 10000,
		OutputTokens:    2000,
	}
	costCodex, nominalCodex := calculateTurnCost(recCodex, pricing)
	if costCodex != 0.0 {
		t.Errorf("Codex cost should be 0.0, got %f", costCodex)
	}
	if nominalCodex <= 0 {
		t.Errorf("Codex nominal cost should be > 0, got %f", nominalCodex)
	}
}

func TestSessionsV3UsageDashboard_ArchivedSessionsAndOptimization(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	now := time.Now().UTC().UnixMilli()

	// 1. Create active session with usage
	activeID := "sess_active_1"
	_, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      activeID,
		Title:          "Active Project Analysis",
		AccountScopeID: testPrincipal().AccountScopeID,
		UserID:         testPrincipal().UserID,
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "test-ws",
		Preference:     &pebblestore.ModelPreference{Provider: "google", Model: "gemini-3.8-flash", Thinking: "medium"},
	})
	if err != nil {
		t.Fatalf("create active session: %v", err)
	}
	_, _, _, err = sessionSvc.RecordTurnUsage(activeID, pebblestore.SessionTurnUsageSnapshot{
		SessionID:      activeID,
		AccountScopeID: testPrincipal().AccountScopeID,
		UserID:         testPrincipal().UserID,
		RunID:          "run-act-1",
		Provider:       "google",
		Model:          "gemini-3.8-flash",
		Source:         "google_api_usage",
		InputTokens:    20000,
		OutputTokens:   1000,
		TotalTokens:    21000,
		CreatedAt:      now - 2000,
		UpdatedAt:      now - 2000,
	})
	if err != nil {
		t.Fatalf("record active turn: %v", err)
	}

	// 2. Create another session, record usage, then archive it
	archivedID := "sess_archived_1"
	_, _, err = sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      archivedID,
		Title:          "Legacy Archived Investigation",
		AccountScopeID: testPrincipal().AccountScopeID,
		UserID:         testPrincipal().UserID,
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "test-ws",
		Preference:     &pebblestore.ModelPreference{Provider: "anthropic", Model: "claude-sonnet-5", Thinking: "medium"},
	})
	if err != nil {
		t.Fatalf("create archived session: %v", err)
	}
	_, _, _, err = sessionSvc.RecordTurnUsage(archivedID, pebblestore.SessionTurnUsageSnapshot{
		SessionID:      archivedID,
		AccountScopeID: testPrincipal().AccountScopeID,
		UserID:         testPrincipal().UserID,
		RunID:          "run-arch-1",
		Provider:       "anthropic",
		Model:          "claude-sonnet-5",
		Source:         "anthropic_api_usage",
		InputTokens:    30000,
		OutputTokens:   1500,
		TotalTokens:    31500,
		CreatedAt:      now - 1000,
		UpdatedAt:      now - 1000,
	})
	if err != nil {
		t.Fatalf("record archived turn: %v", err)
	}
	if err := sessionSvc.ArchiveSession(archivedID); err != nil {
		t.Fatalf("archive session: %v", err)
	}

	// 3. Create 50 dummy sessions with no usage to ensure large session counts don't slow down or interfere
	for i := 0; i < 50; i++ {
		dummyID := fmt.Sprintf("sess_dummy_%d", i)
		_, _, err = sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
			SessionID:      dummyID,
			Title:          fmt.Sprintf("Dummy Idle Session %d", i),
			AccountScopeID: testPrincipal().AccountScopeID,
			UserID:         testPrincipal().UserID,
			WorkspacePath:  t.TempDir(),
			WorkspaceName:  "test-ws",
			Preference:     &pebblestore.ModelPreference{Provider: "google", Model: "gemini-3.8-flash", Thinking: "medium"},
		})
		if err != nil {
			t.Fatalf("create dummy session %d: %v", i, err)
		}
	}

	// 4. Test default GET /v3/usage (includes both active and archived)
	reqAll := httptest.NewRequest(http.MethodGet, "/v3/usage?time_range=30d", nil)
	wAll := httptest.NewRecorder()
	server.Handler().ServeHTTP(wAll, withTestPrincipal(reqAll))
	if wAll.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wAll.Code, wAll.Body.String())
	}
	var respAll SessionUsageDashboardResponse
	if err := json.Unmarshal(wAll.Body.Bytes(), &respAll); err != nil {
		t.Fatalf("unmarshal respAll: %v", err)
	}

	if respAll.Summary.TotalSessions != 52 {
		t.Fatalf("expected 52 total sessions, got %d", respAll.Summary.TotalSessions)
	}
	if respAll.Summary.ActiveSessions != 51 {
		t.Fatalf("expected 51 active sessions, got %d", respAll.Summary.ActiveSessions)
	}
	if respAll.Summary.ArchivedSessions != 1 {
		t.Fatalf("expected 1 archived session, got %d", respAll.Summary.ArchivedSessions)
	}
	if len(respAll.RecentSessions) == 0 {
		t.Fatalf("expected recent sessions, got 0")
	}

	foundActive := false
	foundArchived := false
	for _, sess := range respAll.RecentSessions {
		if sess.SessionID == activeID {
			foundActive = true
			if sess.Archived {
				t.Fatalf("expected session %s to not be archived", activeID)
			}
			if sess.Title != "Active Project Analysis" {
				t.Fatalf("expected title 'Active Project Analysis', got %q", sess.Title)
			}
		}
		if sess.SessionID == archivedID {
			foundArchived = true
			if !sess.Archived {
				t.Fatalf("expected session %s to be archived", archivedID)
			}
			if sess.Title != "Legacy Archived Investigation" {
				t.Fatalf("expected title 'Legacy Archived Investigation', got %q", sess.Title)
			}
		}
	}
	if !foundActive || !foundArchived {
		t.Fatalf("expected both active and archived session found: active=%v, archived=%v", foundActive, foundArchived)
	}

	// 5. Test GET /v3/usage?archived_mode=active (only active)
	reqActive := httptest.NewRequest(http.MethodGet, "/v3/usage?archived_mode=active", nil)
	wActive := httptest.NewRecorder()
	server.Handler().ServeHTTP(wActive, withTestPrincipal(reqActive))
	if wActive.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", wActive.Code)
	}
	var respActive SessionUsageDashboardResponse
	if err := json.Unmarshal(wActive.Body.Bytes(), &respActive); err != nil {
		t.Fatalf("unmarshal respActive: %v", err)
	}
	if len(respActive.RecentSessions) == 0 {
		t.Fatalf("expected active sessions, got 0")
	}
	foundActiveInActive := false
	for _, s := range respActive.RecentSessions {
		if s.Archived {
			t.Fatalf("expected only active sessions with archived_mode=active, found archived %s", s.SessionID)
		}
		if s.SessionID == activeID {
			foundActiveInActive = true
		}
	}
	if !foundActiveInActive {
		t.Fatalf("expected active session %s in active list", activeID)
	}

	// 6. Test GET /v3/usage?archived_mode=only (only archived)
	reqArchived := httptest.NewRequest(http.MethodGet, "/v3/usage?archived_mode=only", nil)
	wArchived := httptest.NewRecorder()
	server.Handler().ServeHTTP(wArchived, withTestPrincipal(reqArchived))
	if wArchived.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", wArchived.Code)
	}
	var respArchived SessionUsageDashboardResponse
	if err := json.Unmarshal(wArchived.Body.Bytes(), &respArchived); err != nil {
		t.Fatalf("unmarshal respArchived: %v", err)
	}
	if len(respArchived.RecentSessions) != 1 {
		t.Fatalf("expected 1 session with archived_mode=only, got %d", len(respArchived.RecentSessions))
	}
	if respArchived.RecentSessions[0].SessionID != archivedID || !respArchived.RecentSessions[0].Archived {
		t.Fatalf("expected archived session %s, got %+v", archivedID, respArchived.RecentSessions[0])
	}

	// 7. Test archived session with lifetime SessionUsageSummary (no turn records in current window)
	legacyArchivedID := "sess_legacy_archived_2"
	_, _, err = sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      legacyArchivedID,
		Title:          "Historical Deep Analysis",
		AccountScopeID: testPrincipal().AccountScopeID,
		UserID:         testPrincipal().UserID,
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "test-ws",
		Preference:     &pebblestore.ModelPreference{Provider: "google", Model: "gemini-3.8-flash", Thinking: "low"},
	})
	if err != nil {
		t.Fatalf("create legacy archived session: %v", err)
	}
	if err := sessionSvc.Store().PutUsageSummary(pebblestore.SessionUsageSummary{
		SessionID:        legacyArchivedID,
		AccountScopeID:   testPrincipal().AccountScopeID,
		UserID:           testPrincipal().UserID,
		Provider:         "google",
		Model:            "gemini-3.8-flash",
		TotalTokens:      75000,
		InputTokens:      70000,
		OutputTokens:     5000,
		TurnCount:        10,
		EstimatedCostUSD: 0.05,
		UpdatedAt:        now - 50000000,
	}); err != nil {
		t.Fatalf("put legacy usage summary: %v", err)
	}
	if err := sessionSvc.ArchiveSession(legacyArchivedID); err != nil {
		t.Fatalf("archive legacy session: %v", err)
	}

	reqArchived2 := httptest.NewRequest(http.MethodGet, "/v3/usage?archived_mode=only", nil)
	wArchived2 := httptest.NewRecorder()
	server.Handler().ServeHTTP(wArchived2, withTestPrincipal(reqArchived2))
	if wArchived2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", wArchived2.Code)
	}
	var respArchived2 SessionUsageDashboardResponse
	if err := json.Unmarshal(wArchived2.Body.Bytes(), &respArchived2); err != nil {
		t.Fatalf("unmarshal respArchived2: %v", err)
	}
	if len(respArchived2.RecentSessions) != 2 {
		t.Fatalf("expected 2 archived sessions with archived_mode=only, got %d", len(respArchived2.RecentSessions))
	}
	foundLegacy := false
	for _, s := range respArchived2.RecentSessions {
		if s.SessionID == legacyArchivedID {
			foundLegacy = true
			if !s.Archived {
				t.Fatalf("expected session %s to be marked archived", legacyArchivedID)
			}
			if s.Title != "Historical Deep Analysis" {
				t.Fatalf("expected title 'Historical Deep Analysis', got %q", s.Title)
			}
			if s.TotalTokens != 75000 {
				t.Fatalf("expected 75000 total tokens from lifetime summary, got %d", s.TotalTokens)
			}
			if s.TurnCount != 10 {
				t.Fatalf("expected 10 turns from lifetime summary, got %d", s.TurnCount)
			}
		}
	}
	if !foundLegacy {
		t.Fatalf("expected legacy archived session %s in response", legacyArchivedID)
	}
}

// TestAnalyticsReadsPersistedAccountingReadOnly verifies that handleSessionsV3Usage reads
// already-persisted accounting totals and media usage records directly without mutating
// daily usage accumulators, creating phantom records, or re-pricing models on GET.
// Production authority: Server.handleSessionsV3Usage, SessionStore.ListMediaUsage.
func TestAnalyticsReadsPersistedAccountingReadOnly(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	now := time.Now().UTC().UnixMilli()
	today := time.Now().UTC().Format("2006-01-02")
	acctID := testPrincipal().AccountScopeID

	sessionID := "sess_analytics_readonly_test"
	_, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      sessionID,
		Title:          "Readonly Accounting Verification",
		AccountScopeID: acctID,
		UserID:         testPrincipal().UserID,
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "test-ws",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// 1. Record a turn with exact persisted cost
	_, _, _, err = sessionSvc.RecordTurnUsage(sessionID, pebblestore.SessionTurnUsageSnapshot{
		SessionID:        sessionID,
		AccountScopeID:   acctID,
		UserID:           testPrincipal().UserID,
		RunID:            "run-ro-1",
		Provider:         "google",
		Model:            "gemini-3.8-flash",
		Source:           "google_api_usage",
		InputTokens:      50000,
		OutputTokens:     1000,
		TotalTokens:      51000,
		BilledTokens:     51000,
		EstimatedCostUSD: 0.04125,
		CreatedAt:        now - 2000,
		UpdatedAt:        now - 2000,
	})
	if err != nil {
		t.Fatalf("record turn: %v", err)
	}

	// 2. Record media usage via PutMediaUsage
	if err := sessionSvc.RecordMediaUsage(pebblestore.SessionMediaUsageRecord{
		ID:             "media-ro-img-1",
		SessionID:      sessionID,
		AccountScopeID: acctID,
		MediaType:      "image/png",
		Kind:           "image",
		Provider:       "google",
		Model:          "imagen-3.0",
		Filename:       "generated-hero.png",
		Label:          "Generated hero image",
		Size:           54321,
		CostUSD:        0.04,
		CreatedAt:      now - 1000,
	}); err != nil {
		t.Fatalf("record media: %v", err)
	}

	// Capture daily accumulator before GET
	accBefore, ok, err := sessionSvc.Store().GetDailyUsageAccumulator(acctID, today)
	if err != nil || !ok {
		t.Fatalf("get daily accumulator before: ok=%v err=%v", ok, err)
	}

	// 3. Make GET /v3/usage call
	req := httptest.NewRequest(http.MethodGet, "/v3/usage?time_range=today", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, withTestPrincipal(req))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp SessionUsageDashboardResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	// Verify exact persisted cost was read without repricing (turn cost 0.04125 + media cost 0.04 = 0.08125)
	expectedTotalCost := 0.04125 + 0.04
	if resp.Summary.TotalCostUSD < expectedTotalCost-0.00001 || resp.Summary.TotalCostUSD > expectedTotalCost+0.00001 {
		t.Fatalf("expected total cost %f, got %f", expectedTotalCost, resp.Summary.TotalCostUSD)
	}
	if resp.Summary.MediaCostUSD != 0.04 {
		t.Fatalf("expected media cost 0.04, got %f", resp.Summary.MediaCostUSD)
	}
	if resp.Summary.TotalMediaCalls != 1 {
		t.Fatalf("expected 1 media call, got %d", resp.Summary.TotalMediaCalls)
	}

	// Verify daily accumulator was NOT mutated by page visit (read-only guarantee)
	accAfter, ok, err := sessionSvc.Store().GetDailyUsageAccumulator(acctID, today)
	if err != nil || !ok {
		t.Fatalf("get daily accumulator after: ok=%v err=%v", ok, err)
	}
	if accBefore.TotalCostUSD != accAfter.TotalCostUSD || accBefore.TotalTokens != accAfter.TotalTokens || accBefore.TurnCount != accAfter.TurnCount {
		t.Fatalf("daily accumulator was mutated on GET: before=%+v after=%+v", accBefore, accAfter)
	}
}
