package pebblestore

import (
	"path/filepath"
	"testing"
	"time"
)

func TestUsageLimitStore(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test-usage-limit.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	sessionStore := NewSessionStore(db)
	accountID := "test-acct-1"

	// 1. Initial Get should return not found
	limit, found, err := sessionStore.GetUsageLimit(accountID)
	if err != nil {
		t.Fatalf("GetUsageLimit: %v", err)
	}
	if found {
		t.Fatalf("expected not found initially, got %+v", limit)
	}

	// 2. Put and Get
	rec := UsageLimitRecord{
		AccountScopeID:    accountID,
		DailyCostLimitUSD: 25.50,
		DailyTokensLimit:  500000,
		Enabled:           true,
	}
	if err := sessionStore.PutUsageLimit(rec); err != nil {
		t.Fatalf("PutUsageLimit: %v", err)
	}

	saved, found, err := sessionStore.GetUsageLimit(accountID)
	if err != nil {
		t.Fatalf("GetUsageLimit after put: %v", err)
	}
	if !found {
		t.Fatal("expected found after put")
	}
	if saved.DailyCostLimitUSD != 25.50 || !saved.Enabled || saved.DailyTokensLimit != 500000 {
		t.Fatalf("unexpected saved record: %+v", saved)
	}

	// 3. Daily usage accumulator increment
	today := time.Now().UTC().Format("2006-01-02")
	acc, err := sessionStore.IncrementDailyUsage(accountID, today, 0.05, 1000)
	if err != nil {
		t.Fatalf("IncrementDailyUsage: %v", err)
	}
	if acc.TotalCostUSD != 0.05 || acc.TotalTokens != 1000 || acc.TurnCount != 1 {
		t.Fatalf("unexpected accumulator: %+v", acc)
	}

	// Second increment
	acc2, err := sessionStore.IncrementDailyUsage(accountID, today, 0.15, 2500)
	if err != nil {
		t.Fatalf("second IncrementDailyUsage: %v", err)
	}
	if acc2.TotalCostUSD < 0.199 || acc2.TotalCostUSD > 0.201 || acc2.TotalTokens != 3500 || acc2.TurnCount != 2 {
		t.Fatalf("unexpected accumulator after second increment: %+v", acc2)
	}

	// 4. GetTodayUsageTotal
	cost, tokens, err := sessionStore.GetTodayUsageTotal(accountID)
	if err != nil {
		t.Fatalf("GetTodayUsageTotal: %v", err)
	}
	if cost < 0.199 || cost > 0.201 || tokens != 3500 {
		t.Fatalf("unexpected today usage: cost=%v tokens=%v", cost, tokens)
	}
}

func TestCalculateBaselineCost(t *testing.T) {
	// Gemini 3.8 Flash: $0.15 input / $0.60 output per million
	cost := CalculateBaselineCost("google", "gemini-3.8-flash", 1_000_000, 1_000_000, 0, 0)
	if cost < 0.74 || cost > 0.76 {
		t.Errorf("expected Gemini 3.8 Flash cost ~0.75, got %f", cost)
	}

	// Gemini 3.8 Flash with cached input ($0.0375 cached rate)
	costWithCache := CalculateBaselineCost("google", "gemini-3.8-flash", 1_000_000, 0, 500_000, 0)
	// 500k regular = 0.5 * 0.15 = 0.075; 500k cached = 0.5 * 0.0375 = 0.01875 -> 0.09375
	if costWithCache < 0.09 || costWithCache > 0.10 {
		t.Errorf("expected cached cost ~0.09375, got %f", costWithCache)
	}
}

func TestCalculateBaselineMediaCost(t *testing.T) {
	// 1. Veo 3.1 4K 8 seconds -> 8 * 0.60 = $4.80
	veo4k := CalculateBaselineMediaCost("video/mp4", "veo-3.1-generate-preview", "4k", 8)
	if veo4k < 4.79 || veo4k > 4.81 {
		t.Errorf("expected Veo 4K cost $4.80, got %f", veo4k)
	}

	// 2. Veo 3.1 1080p 8 seconds -> 8 * 0.40 = $3.20
	veo1080 := CalculateBaselineMediaCost("video/mp4", "veo-3.1-generate-preview", "1080p", 8)
	if veo1080 < 3.19 || veo1080 > 3.21 {
		t.Errorf("expected Veo 1080p cost $3.20, got %f", veo1080)
	}

	// 3. Lyria 3 Clip -> $0.04
	clipCost := CalculateBaselineMediaCost("audio/mp3", "lyria-3-clip-preview", "", 30)
	if clipCost != 0.04 {
		t.Errorf("expected Lyria clip cost $0.04, got %f", clipCost)
	}

	// 4. Lyria 3.5 full song -> $0.08
	songCost := CalculateBaselineMediaCost("audio/mp3", "lyria-3.5", "", 120)
	if songCost != 0.08 {
		t.Errorf("expected Lyria song cost $0.08, got %f", songCost)
	}

	// 5. Nano Banana 2 1K image -> $0.067
	nb21k := CalculateBaselineMediaCost("image/png", "gemini-3.1-flash-image", "1k", 0)
	if nb21k != 0.067 {
		t.Errorf("expected Nano Banana 2 1K cost $0.067, got %f", nb21k)
	}

	// 6. Nano Banana 2 4K image -> $0.151
	nb24k := CalculateBaselineMediaCost("image/png", "gemini-3.1-flash-image", "4k", 0)
	if nb24k != 0.151 {
		t.Errorf("expected Nano Banana 2 4K cost $0.151, got %f", nb24k)
	}
}

func TestExtractMediaPricingFromCatalog(t *testing.T) {
	veoCatalog := []byte(`{
		"billing": {
			"lines": [
				{"kind":"billing_rate","billable":"video_output","unit":"second","price_usd":0.4,"variant":"1080p"},
				{"kind":"billing_rate","billable":"video_output","unit":"second","price_usd":0.6,"variant":"4K"}
			]
		}
	}`)

	cost4k, ok := ExtractMediaPricingFromCatalog(veoCatalog, "video", "veo-3.1-generate-preview", "4k", 8)
	if !ok || cost4k < 4.79 || cost4k > 4.81 {
		t.Errorf("ExtractMediaPricingFromCatalog 4k: ok=%v, cost=%f, want 4.80", ok, cost4k)
	}

	cost1080, ok := ExtractMediaPricingFromCatalog(veoCatalog, "video", "veo-3.1-generate-preview", "1080p", 8)
	if !ok || cost1080 < 3.19 || cost1080 > 3.21 {
		t.Errorf("ExtractMediaPricingFromCatalog 1080p: ok=%v, cost=%f, want 3.20", ok, cost1080)
	}

	lyriaCatalog := []byte(`{
		"billing": {
			"lines": [
				{"kind":"billing_rate","billable":"song","unit":"song","price_usd":0.04,"variant":"30_second_clip"}
			]
		}
	}`)
	costClip, ok := ExtractMediaPricingFromCatalog(lyriaCatalog, "audio", "lyria-3-clip-preview", "", 30)
	if !ok || costClip != 0.04 {
		t.Errorf("ExtractMediaPricingFromCatalog lyria clip: ok=%v, cost=%f, want 0.04", ok, costClip)
	}
}

func TestGetTodayUsageTotalWithMedia(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test-usage-total-media.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	sessionStore := NewSessionStore(db)
	accountID := "acct-media-total"
	now := time.Now().UTC().UnixMilli()

	// Seed a media variant created today
	v := SessionArtifactVariant{
		Version:          1,
		ID:               "art-vid-1",
		CollectionID:     "col-1",
		SessionID:        "sess-1",
		AccountScopeID:   accountID,
		Status:           SessionArtifactStatusReady,
		MediaType:        "video/mp4",
		ModelID:          "veo-3.1-generate-preview",
		EstimatedCostUSD: 4.80,
		CreatedAt:        now,
	}
	if err := sessionStore.PutArtifactVariant(v); err != nil {
		t.Fatalf("put artifact variant: %v", err)
	}

	// Recomputed usage should include the media artifact ($4.80)
	cost, _, err := sessionStore.GetTodayUsageTotal(accountID)
	if err != nil {
		t.Fatalf("GetTodayUsageTotal: %v", err)
	}
	if cost < 4.79 || cost > 4.81 {
		t.Fatalf("expected today cost to include media $4.80, got %f", cost)
	}

	// Seed local non-billable media: chained master video, keyframe, timeline render
	localChained := SessionArtifactVariant{
		Version:        1,
		ID:             "art-vid-chain",
		CollectionID:   "col-1",
		SessionID:      "sess-1",
		AccountScopeID: accountID,
		Status:         SessionArtifactStatusReady,
		MediaType:      "video/mp4",
		Filename:       "chained-master.mp4",
		Role:           SessionArtifactRoleChainedVideo,
		Presentation:   SessionArtifactPresentation{Kind: "video", Description: "Chained multi-part video (4 parts)"},
		CreatedAt:      now,
	}
	if err := sessionStore.PutArtifactVariant(localChained); err != nil {
		t.Fatalf("put local chained video: %v", err)
	}

	localKeyframe := SessionArtifactVariant{
		Version:        1,
		ID:             "art-img-keyframe",
		CollectionID:   "col-1",
		SessionID:      "sess-1",
		AccountScopeID: accountID,
		Status:         SessionArtifactStatusReady,
		MediaType:      "image/png",
		Filename:       "chained-master-keyframe.png",
		Role:           SessionArtifactRoleKeyframe,
		Presentation:   SessionArtifactPresentation{Kind: "image", Description: "Extracted keyframe (last) from video"},
		CreatedAt:      now,
	}
	if err := sessionStore.PutArtifactVariant(localKeyframe); err != nil {
		t.Fatalf("put local keyframe: %v", err)
	}

	// Total cost must still be $4.80, NOT inflated by the local chained video ($3.20) or keyframe ($0.04)
	costAfterLocal, _, err := sessionStore.GetTodayUsageTotal(accountID)
	if err != nil {
		t.Fatalf("GetTodayUsageTotal after local: %v", err)
	}
	if costAfterLocal < 4.79 || costAfterLocal > 4.81 {
		t.Fatalf("expected cost to remain $4.80 without inflating for local operations, got %f", costAfterLocal)
	}
}

func TestGetTodayUsageTotalReconcilesExistingAccumulatorWithMedia(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test-reconcile-media.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	sessionStore := NewSessionStore(db)
	accountID := "acct-reconcile"
	now := time.Now().UTC().UnixMilli()
	today := time.Now().UTC().Format("2006-01-02")

	// 1. Existing accumulator with $1.50 from earlier turns
	acc := DailyUsageAccumulator{
		AccountScopeID: accountID,
		Date:           today,
		TotalCostUSD:   1.50,
		TotalTokens:    50000,
		TurnCount:      3,
		UpdatedAt:      now - 10000,
	}
	if err := sessionStore.PutDailyUsageAccumulator(acc); err != nil {
		t.Fatalf("put accumulator: %v", err)
	}

	// 2. Add an AI media variant created today ($4.80)
	v := SessionArtifactVariant{
		Version:          1,
		ID:               "art-vid-reconcile",
		CollectionID:     "col-1",
		SessionID:        "sess-1",
		AccountScopeID:   accountID,
		Status:           SessionArtifactStatusReady,
		MediaType:        "video/mp4",
		ModelID:          "veo-3.1-generate-preview",
		EstimatedCostUSD: 4.80,
		CreatedAt:        now,
	}
	if err := sessionStore.PutArtifactVariant(v); err != nil {
		t.Fatalf("put artifact variant: %v", err)
	}

	// 3. GetTodayUsageTotal must combine the existing turn cost ($1.50) with the media ($4.80) = $6.30
	totalCost, tokens, err := sessionStore.GetTodayUsageTotal(accountID)
	if err != nil {
		t.Fatalf("GetTodayUsageTotal: %v", err)
	}
	if totalCost < 6.29 || totalCost > 6.31 {
		t.Fatalf("expected combined today cost $6.30, got %f", totalCost)
	}
	if tokens != 50000 {
		t.Fatalf("expected 50000 tokens, got %d", tokens)
	}

	// 4. Verify the accumulator was updated in Pebble
	updatedAcc, found, err := sessionStore.GetDailyUsageAccumulator(accountID, today)
	if err != nil || !found {
		t.Fatalf("get updated accumulator: found=%v, err=%v", found, err)
	}
	if updatedAcc.TotalCostUSD < 6.29 || updatedAcc.TotalCostUSD > 6.31 {
		t.Fatalf("expected accumulator total $6.30, got %f", updatedAcc.TotalCostUSD)
	}
}

func TestIsAIGeneratedMedia(t *testing.T) {
	cases := []struct {
		name     string
		variant  SessionArtifactVariant
		expected bool
	}{
		{
			name: "AI generated video explicit role",
			variant: SessionArtifactVariant{
				Role:      SessionArtifactRoleAIGenerated,
				MediaType: "video/mp4",
			},
			expected: true,
		},
		{
			name: "AI generated video with model and provider",
			variant: SessionArtifactVariant{
				MediaType:  "video/mp4",
				ProviderID: "google",
				ModelID:    "veo-3.1-generate-preview",
			},
			expected: true,
		},
		{
			name: "Local chained video explicit role",
			variant: SessionArtifactVariant{
				Role:      SessionArtifactRoleChainedVideo,
				MediaType: "video/mp4",
			},
			expected: false,
		},
		{
			name: "Local chained video filename and description heuristic",
			variant: SessionArtifactVariant{
				MediaType:    "video/mp4",
				Filename:     "chained-master.mp4",
				Presentation: SessionArtifactPresentation{Description: "Chained multi-part video (4 parts, audio mode: mix_ducked)"},
			},
			expected: false,
		},
		{
			name: "Local keyframe explicit role",
			variant: SessionArtifactVariant{
				Role:      SessionArtifactRoleKeyframe,
				MediaType: "image/png",
			},
			expected: false,
		},
		{
			name: "Local keyframe filename and description heuristic",
			variant: SessionArtifactVariant{
				MediaType:    "image/png",
				Filename:     "chained-master-keyframe.png",
				Presentation: SessionArtifactPresentation{Description: "Extracted keyframe (last) from video", Label: "Climax Keyframe"},
			},
			expected: false,
		},
		{
			name: "Video Studio project timeline render",
			variant: SessionArtifactVariant{
				MediaType: "video/mp4",
				Lineage:   SessionArtifactLineage{VideoProjectID: "proj-1", VideoRevisionID: "rev-1"},
			},
			expected: false,
		},
		{
			name: "Workspace publication",
			variant: SessionArtifactVariant{
				MediaType:    "image/png",
				Role:         SessionArtifactRoleWorkspacePub,
				CollectionID: "collection-workspace-1",
			},
			expected: false,
		},
		{
			name: "HTML animation render only",
			variant: SessionArtifactVariant{
				MediaType: "video/mp4",
				Role:      SessionArtifactRoleRenderOnly,
			},
			expected: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsAIGeneratedMedia(tc.variant)
			if got != tc.expected {
				t.Errorf("IsAIGeneratedMedia() = %v, want %v", got, tc.expected)
			}
		})
	}
}
