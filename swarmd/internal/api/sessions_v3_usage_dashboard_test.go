package api

import (
	"encoding/json"
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

	// 4. Record media artifact variants
	mediaVariants := []pebblestore.SessionArtifactVariant{
		{
			Version:        1,
			ID:             "media-img-1",
			SessionID:      sessionID,
			AccountScopeID: testPrincipal().AccountScopeID,
			Status:         pebblestore.SessionArtifactStatusReady,
			MediaType:      "image/png",
			Filename:       "hero-banner.png",
			Size:           102400,
			CreatedAt:      now - 4000,
			UpdatedAt:      now - 4000,
		},
		{
			Version:        1,
			ID:             "media-vid-1",
			SessionID:      sessionID,
			AccountScopeID: testPrincipal().AccountScopeID,
			Status:         pebblestore.SessionArtifactStatusReady,
			MediaType:      "video/mp4",
			Filename:       "feature-teaser.mp4",
			Size:           2048000,
			CreatedAt:      now - 2000,
			UpdatedAt:      now - 2000,
		},
		{
			Version:        1,
			ID:             "media-aud-1",
			SessionID:      sessionID,
			AccountScopeID: testPrincipal().AccountScopeID,
			Status:         pebblestore.SessionArtifactStatusReady,
			MediaType:      "audio/mp3",
			Filename:       "background-music.mp3",
			Size:           512000,
			CreatedAt:      now - 1000,
			UpdatedAt:      now - 1000,
		},
	}
	for _, v := range mediaVariants {
		if err := sessionSvc.Store().PutArtifactVariant(v); err != nil {
			t.Fatalf("put media variant: %v", err)
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
