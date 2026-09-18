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
		// Local non-billable operations: chained master, keyframe, and video render
		{
			Version:        1,
			ID:             "local-chain-1",
			SessionID:      sessionID,
			AccountScopeID: testPrincipal().AccountScopeID,
			Status:         pebblestore.SessionArtifactStatusReady,
			MediaType:      "video/mp4",
			Filename:       "chained-master.mp4",
			Role:           pebblestore.SessionArtifactRoleChainedVideo,
			Presentation:   pebblestore.SessionArtifactPresentation{Kind: "video", Description: "Chained multi-part video (4 parts, audio mode: mix_ducked)"},
			Size:           10485760,
			CreatedAt:      now - 500,
			UpdatedAt:      now - 500,
		},
		{
			Version:        1,
			ID:             "local-keyframe-1",
			SessionID:      sessionID,
			AccountScopeID: testPrincipal().AccountScopeID,
			Status:         pebblestore.SessionArtifactStatusReady,
			MediaType:      "image/png",
			Filename:       "chained-master-keyframe.png",
			Role:           pebblestore.SessionArtifactRoleKeyframe,
			Presentation:   pebblestore.SessionArtifactPresentation{Kind: "image", Description: "Extracted keyframe (last) from video"},
			Size:           524288,
			CreatedAt:      now - 400,
			UpdatedAt:      now - 400,
		},
		{
			Version:        1,
			ID:             "local-render-1",
			SessionID:      sessionID,
			AccountScopeID: testPrincipal().AccountScopeID,
			Status:         pebblestore.SessionArtifactStatusReady,
			MediaType:      "video/mp4",
			Filename:       "timeline-render.mp4",
			Role:           pebblestore.SessionArtifactRoleVideoRender,
			Lineage:        pebblestore.SessionArtifactLineage{VideoProjectID: "proj-1", VideoRevisionID: "rev-1"},
			Size:           20971520,
			CreatedAt:      now - 300,
			UpdatedAt:      now - 300,
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
	if resp.Summary.TokenCostUSD <= 0 {
		t.Fatalf("expected positive token cost, got %f", resp.Summary.TokenCostUSD)
	}
	if resp.Summary.TotalCostUSD <= 0 {
		t.Fatalf("expected positive total cost, got %f", resp.Summary.TotalCostUSD)
	}
	expectedTotal := resp.Summary.TokenCostUSD + resp.Summary.MediaCostUSD
	if resp.Summary.TotalCostUSD < expectedTotal-0.001 || resp.Summary.TotalCostUSD > expectedTotal+0.001 {
		t.Fatalf("expected total cost (%f) to equal token cost (%f) + media cost (%f)", resp.Summary.TotalCostUSD, resp.Summary.TokenCostUSD, resp.Summary.MediaCostUSD)
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
			if d.MediaCostUSD <= 0 {
				t.Fatalf("expected positive media cost today, got %f", d.MediaCostUSD)
			}
			if d.CostUSD < d.MediaCostUSD {
				t.Fatalf("expected daily cost (%f) to include media cost (%f)", d.CostUSD, d.MediaCostUSD)
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
			if p.MediaCalls <= 0 {
				t.Fatalf("expected positive Google media calls, got %d", p.MediaCalls)
			}
			if p.MediaCostUSD <= 0 {
				t.Fatalf("expected positive Google media cost, got %f", p.MediaCostUSD)
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

func TestSessionsV3UsageDashboard_MediaAccountingExcludesLocalAndPrices4K(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	principal := testPrincipal()

	sessionID := "sess_media_accounting_4k"
	_, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      sessionID,
		Title:          "Media Accounting 4K Test",
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		WorkspacePath:  t.TempDir(),
		Preference:     &pebblestore.ModelPreference{Provider: "google", Model: "gemini-3.8-flash", Thinking: "high"},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	now := time.Now().UTC().UnixMilli()
	variants := []pebblestore.SessionArtifactVariant{
		// 1. Veo 1080p clip (8 seconds) -> $3.20
		{
			Version:            1,
			ID:                 "art-veo-1080",
			SessionID:          sessionID,
			AccountScopeID:     principal.AccountScopeID,
			Status:             pebblestore.SessionArtifactStatusReady,
			MediaType:          "video/mp4",
			Filename:           "generated-video.mp4",
			ModelID:            "veo-3.1-generate-preview",
			ProviderID:         "google",
			EstimatedCostUSD:   3.20,
			Presentation:       pebblestore.SessionArtifactPresentation{Kind: "video", Label: "Part 1 - 1080p", Width: 1920, Height: 1080},
			OutputRequirements: &pebblestore.SessionArtifactOutputRequirements{PresetID: "1080p", Width: 1920, Height: 1080},
			CreatedAt:          now - 5000,
		},
		// 2. Veo 4K clip (8 seconds) -> $4.80
		{
			Version:            1,
			ID:                 "art-veo-4k",
			SessionID:          sessionID,
			AccountScopeID:     principal.AccountScopeID,
			Status:             pebblestore.SessionArtifactStatusReady,
			MediaType:          "video/mp4",
			Filename:           "generated-video.mp4",
			ModelID:            "veo-3.1-generate-preview",
			ProviderID:         "google",
			EstimatedCostUSD:   4.80,
			Presentation:       pebblestore.SessionArtifactPresentation{Kind: "video", Label: "Part 2 - 4K Master", Width: 3840, Height: 2160},
			OutputRequirements: &pebblestore.SessionArtifactOutputRequirements{PresetID: "4k", Width: 3840, Height: 2160},
			CreatedAt:          now - 4000,
		},
		// 3. Lyria audio soundtrack -> $0.08
		{
			Version:          1,
			ID:               "art-lyria-snd",
			SessionID:        sessionID,
			AccountScopeID:   principal.AccountScopeID,
			Status:           pebblestore.SessionArtifactStatusReady,
			MediaType:        "audio/mp3",
			Filename:         "generated-audio.mp3",
			ModelID:          "lyria-3.5",
			ProviderID:       "google",
			EstimatedCostUSD: 0.08,
			Presentation:     pebblestore.SessionArtifactPresentation{Kind: "audio", Label: "Soundtrack"},
			CreatedAt:        now - 3000,
		},
		// 4. Local FFmpeg chained master -> MUST BE $0.00 and excluded from TotalMediaCalls
		{
			Version:        1,
			ID:             "art-chain-master",
			SessionID:      sessionID,
			AccountScopeID: principal.AccountScopeID,
			Status:         pebblestore.SessionArtifactStatusReady,
			MediaType:      "video/mp4",
			Filename:       "chained-master.mp4",
			Role:           pebblestore.SessionArtifactRoleChainedVideo,
			Presentation:   pebblestore.SessionArtifactPresentation{Kind: "video", Label: "Chained Master", Description: "Chained multi-part video (4 parts, audio mode: mix_ducked)"},
			CreatedAt:      now - 2000,
		},
		// 5. Local FFmpeg keyframe extraction -> MUST BE $0.00 and excluded from TotalMediaCalls
		{
			Version:        1,
			ID:             "art-keyframe",
			SessionID:      sessionID,
			AccountScopeID: principal.AccountScopeID,
			Status:         pebblestore.SessionArtifactStatusReady,
			MediaType:      "image/png",
			Filename:       "chained-master-keyframe.png",
			Role:           pebblestore.SessionArtifactRoleKeyframe,
			Presentation:   pebblestore.SessionArtifactPresentation{Kind: "image", Label: "Climax Keyframe", Description: "Extracted keyframe (last) from video"},
			CreatedAt:      now - 1000,
		},
	}
	for _, v := range variants {
		if err := sessionSvc.Store().PutArtifactVariant(v); err != nil {
			t.Fatalf("put variant %s: %v", v.ID, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v3/usage?session_id="+sessionID+"&time_range=all", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, withTestPrincipal(req))

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp SessionUsageDashboardResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify only genuine AI generations are counted in media calls (2 videos + 1 audio = 3, NOT 5!)
	if resp.Summary.TotalMediaCalls != 3 {
		t.Fatalf("expected exactly 3 AI media calls (excluding local chain & keyframe), got %d", resp.Summary.TotalMediaCalls)
	}
	if resp.Media.VideoCount != 2 {
		t.Fatalf("expected 2 video generations, got %d", resp.Media.VideoCount)
	}
	if resp.Media.AudioCount != 1 {
		t.Fatalf("expected 1 audio generation, got %d", resp.Media.AudioCount)
	}
	if resp.Media.ImageCount != 0 {
		t.Fatalf("expected 0 image generations (keyframe is local), got %d", resp.Media.ImageCount)
	}

	// Expected total media cost: $3.20 (1080p) + $4.80 (4K) + $0.08 (Lyria) = $8.08
	expectedMediaCost := 8.08
	if resp.Summary.MediaCostUSD < expectedMediaCost-0.01 || resp.Summary.MediaCostUSD > expectedMediaCost+0.01 {
		t.Fatalf("expected media cost ~%f, got %f", expectedMediaCost, resp.Summary.MediaCostUSD)
	}
}
