package pebblestore

import (
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSessionStoreListAllTurnUsageAndSummaries(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test-usage.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	store := NewSessionStore(db)
	now := time.Now().UnixMilli()

	// Add turn usage records
	err = store.PutTurnUsage(SessionTurnUsageSnapshot{
		SessionID:       "sess-1",
		AccountScopeID:  "acct-1",
		RunID:           "run-1",
		Provider:        "google",
		Model:           "gemini-3.8-flash",
		InputTokens:     1000,
		OutputTokens:    100,
		CacheReadTokens: 200,
		TotalTokens:     1100,
		CreatedAt:       now - 2000,
		UpdatedAt:       now - 2000,
	})
	if err != nil {
		t.Fatalf("put turn 1: %v", err)
	}

	err = store.PutTurnUsage(SessionTurnUsageSnapshot{
		SessionID:       "sess-2",
		AccountScopeID:  "acct-1",
		RunID:           "run-2",
		Provider:        "openai",
		Model:           "gpt-5.6-sol",
		InputTokens:     2000,
		OutputTokens:    200,
		CacheReadTokens: 500,
		TotalTokens:     2200,
		CreatedAt:       now - 1000,
		UpdatedAt:       now - 1000,
	})
	if err != nil {
		t.Fatalf("put turn 2: %v", err)
	}

	// Add usage summaries
	err = store.PutUsageSummary(SessionUsageSummary{
		SessionID:      "sess-1",
		AccountScopeID: "acct-1",
		Provider:       "google",
		Model:          "gemini-3.8-flash",
		TotalTokens:    1100,
		UpdatedAt:      now - 2000,
	})
	if err != nil {
		t.Fatalf("put summary 1: %v", err)
	}

	// Add media variants
	err = store.PutArtifactVariant(SessionArtifactVariant{
		Version:        1,
		ID:             "art-1",
		CollectionID:   "default",
		SessionID:      "sess-1",
		AccountScopeID: "acct-1",
		Status:         SessionArtifactStatusReady,
		MediaType:      "image/png",
		Filename:       "test.png",
		CreatedAt:      now - 500,
		UpdatedAt:      now - 500,
	})
	if err != nil {
		t.Fatalf("put variant: %v", err)
	}

	// Test ListAllTurnUsage
	turns, err := store.ListAllTurnUsage("acct-1", 100)
	if err != nil {
		t.Fatalf("list all turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(turns))
	}
	if turns[0].RunID != "run-2" || turns[1].RunID != "run-1" {
		t.Fatalf("turns not sorted by UpdatedAt desc: %+v", turns)
	}

	// Test ListAllUsageSummaries
	summaries, err := store.ListAllUsageSummaries("acct-1", 100)
	if err != nil {
		t.Fatalf("list all summaries: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}

	// Test ListAllMediaArtifactVariants
	media, err := store.ListAllMediaArtifactVariants("acct-1", 100)
	if err != nil {
		t.Fatalf("list all media: %v", err)
	}
	if len(media) != 1 {
		t.Fatalf("expected 1 media item, got %d", len(media))
	}
	if media[0].Filename != "test.png" {
		t.Fatalf("expected test.png, got %s", media[0].Filename)
	}
}

// TestRepeatUpdateDeduplicationVsDistinctRequests verifies that repeated updates or retries
// for the same run ID do not double-count tokens or spending in DailyUsageAccumulator,
// while multi-step distinct requests accurately accumulate delta cost and delta tokens.
// Production authority: SessionStore.PutTurnUsage and IncrementDailyUsage.
func TestRepeatUpdateDeduplicationVsDistinctRequests(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test-dedup.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	store := NewSessionStore(db)
	now := time.Now().UnixMilli()
	today := time.Now().UTC().Format("2006-01-02")
	acctID := "acct-dedup-1"
	sessID := "sess-dedup-1"
	runID := "run-step-1"

	// Step 1: 1000 input, 100 output => BilledTokens: 1100, cost: $0.005
	step1 := SessionTurnUsageSnapshot{
		SessionID:        sessID,
		AccountScopeID:   acctID,
		RunID:            runID,
		Provider:         "google",
		Model:            "gemini-3.8-flash",
		InputTokens:      1000,
		OutputTokens:     100,
		TotalTokens:      1100,
		BilledTokens:     1100,
		EstimatedCostUSD: 0.005,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := store.PutTurnUsage(step1); err != nil {
		t.Fatalf("put step 1: %v", err)
	}

	acc1, ok, err := store.GetDailyUsageAccumulator(acctID, today)
	if err != nil || !ok {
		t.Fatalf("get daily accumulator 1: ok=%v err=%v", ok, err)
	}
	if acc1.TotalCostUSD != 0.005 || acc1.TotalTokens != 1100 {
		t.Fatalf("expected cost $0.005 and 1100 tokens, got cost=%f tokens=%d", acc1.TotalCostUSD, acc1.TotalTokens)
	}

	// Replay Step 1 (identical update / idempotent retry): must NOT increment daily usage
	if err := store.PutTurnUsage(step1); err != nil {
		t.Fatalf("replay step 1: %v", err)
	}
	accReplay, _, err := store.GetDailyUsageAccumulator(acctID, today)
	if err != nil {
		t.Fatalf("get daily accumulator replay: %v", err)
	}
	if accReplay.TotalCostUSD != 0.005 || accReplay.TotalTokens != 1100 {
		t.Fatalf("duplicate counting on replay: cost=%f tokens=%d", accReplay.TotalCostUSD, accReplay.TotalTokens)
	}

	// Step 2: Next step in the same run (cumulative cost: $0.012, cumulative billed tokens: 2400)
	step2 := SessionTurnUsageSnapshot{
		SessionID:        sessID,
		AccountScopeID:   acctID,
		RunID:            runID,
		Provider:         "google",
		Model:            "gemini-3.8-flash",
		InputTokens:      1200,
		OutputTokens:     100,
		TotalTokens:      1300,  // occupancy
		BilledTokens:     2400,  // cumulative billed tokens (1100 + 1300)
		EstimatedCostUSD: 0.012, // cumulative cost ($0.005 + $0.007)
		CreatedAt:        now,
		UpdatedAt:        now + 1000,
	}
	if err := store.PutTurnUsage(step2); err != nil {
		t.Fatalf("put step 2: %v", err)
	}

	acc2, _, err := store.GetDailyUsageAccumulator(acctID, today)
	if err != nil {
		t.Fatalf("get daily accumulator 2: %v", err)
	}
	// Expected: total cost $0.012 ($0.005 + delta $0.007), total tokens 2400 (1100 + delta 1300)
	expectedCost := 0.012
	expectedTokens := int64(2400)
	if acc2.TotalCostUSD != expectedCost || acc2.TotalTokens != expectedTokens {
		t.Fatalf("expected cumulative cost=%f tokens=%d, got cost=%f tokens=%d", expectedCost, expectedTokens, acc2.TotalCostUSD, acc2.TotalTokens)
	}
}

// TestMediaUsagePersistenceAndDailyLimits verifies that media generation usage is
// persisted at execution time into SessionMediaUsageRecord, updates DailyUsageAccumulator
// and SessionUsageSummary, and enforces daily limits without double counting.
// Production authority: SessionStore.PutMediaUsage and SessionStore.ListMediaUsage.
func TestMediaUsagePersistenceAndDailyLimits(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test-media-usage.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	store := NewSessionStore(db)
	now := time.Now().UnixMilli()
	today := time.Now().UTC().Format("2006-01-02")
	acctID := "acct-media-1"
	sessID := "sess-media-1"
	if err := store.CreateSession(SessionSnapshot{ID: sessID, AccountScopeID: acctID, Title: "Test Session"}); err != nil {
		t.Fatalf("set session: %v", err)
	}

	// Put image usage record
	imgRecord := SessionMediaUsageRecord{
		ID:             "media-var-img-1",
		SessionID:      sessID,
		AccountScopeID: acctID,
		MediaType:      "image/png",
		Kind:           "image",
		Provider:       "google",
		Model:          "imagen-3.0",
		Filename:       "generated-image.png",
		Label:          "Generated image",
		Size:           12345,
		CostUSD:        0.04,
		CreatedAt:      now,
	}
	if err := store.PutMediaUsage(imgRecord); err != nil {
		t.Fatalf("put media usage: %v", err)
	}

	// Idempotent retry: putting identical media record must be a no-op
	if err := store.PutMediaUsage(imgRecord); err != nil {
		t.Fatalf("put media usage replay: %v", err)
	}

	// Check ListMediaUsage
	list, err := store.ListMediaUsage(acctID, 10)
	if err != nil {
		t.Fatalf("list media usage: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 media record, got %d", len(list))
	}
	if list[0].CostUSD != 0.04 || list[0].Kind != "image" {
		t.Fatalf("unexpected media record: %+v", list[0])
	}

	// Check DailyUsageAccumulator includes media cost
	acc, ok, err := store.GetDailyUsageAccumulator(acctID, today)
	if err != nil || !ok {
		t.Fatalf("get daily accumulator: ok=%v err=%v", ok, err)
	}
	if acc.TotalCostUSD != 0.04 || acc.MediaCalls != 1 || acc.MediaCostUSD != 0.04 {
		t.Fatalf("expected daily total cost $0.04, media calls 1, got cost=%f calls=%d", acc.TotalCostUSD, acc.MediaCalls)
	}

	// Check SessionUsageSummary includes media cost
	summary, found, err := store.GetUsageSummary(sessID)
	if err != nil || !found {
		t.Fatalf("get usage summary: found=%v err=%v", found, err)
	}
	if summary.EstimatedCostUSD != 0.04 {
		t.Fatalf("expected summary cost $0.04, got %f", summary.EstimatedCostUSD)
	}
}

// TestDurableAccountingSurvivesReopen verifies that stored usage snapshots, media records,
// usage summaries, and daily accumulators survive closing and reopening the Pebble database.
func TestDurableAccountingSurvivesReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test-reopen.pebble")
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	store := NewSessionStore(db)
	now := time.Now().UnixMilli()
	today := time.Now().UTC().Format("2006-01-02")
	acctID := "acct-reopen-1"
	sessID := "sess-reopen-1"
	if err := store.CreateSession(SessionSnapshot{ID: sessID, AccountScopeID: acctID, Title: "Test Session"}); err != nil {
		t.Fatalf("set session: %v", err)
	}

	if err := store.PutTurnUsage(SessionTurnUsageSnapshot{
		SessionID:        sessID,
		AccountScopeID:   acctID,
		RunID:            "run-reopen-1",
		Provider:         "google",
		Model:            "gemini-3.8-flash",
		TotalTokens:      5000,
		BilledTokens:     5000,
		EstimatedCostUSD: 0.025,
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("put turn: %v", err)
	}

	if err := store.PutMediaUsage(SessionMediaUsageRecord{
		ID:             "media-reopen-1",
		SessionID:      sessID,
		AccountScopeID: acctID,
		MediaType:      "video/mp4",
		Kind:           "video",
		CostUSD:        1.20,
		CreatedAt:      now,
	}); err != nil {
		t.Fatalf("put media: %v", err)
	}

	// Close database
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	// Reopen database
	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer db2.Close()

	store2 := NewSessionStore(db2)

	// Verify turn usage
	turn, ok, err := store2.GetTurnUsage(sessID, "run-reopen-1")
	if err != nil || !ok {
		t.Fatalf("get turn after reopen: ok=%v err=%v", ok, err)
	}
	if turn.EstimatedCostUSD != 0.025 {
		t.Fatalf("expected turn cost 0.025, got %f", turn.EstimatedCostUSD)
	}

	// Verify media usage
	media, err := store2.ListMediaUsage(acctID, 10)
	if err != nil || len(media) != 1 {
		t.Fatalf("list media after reopen: count=%d err=%v", len(media), err)
	}
	if media[0].CostUSD != 1.20 {
		t.Fatalf("expected media cost 1.20, got %f", media[0].CostUSD)
	}

	// Verify daily accumulator includes both text and media: $0.025 + $1.20 = $1.225
	todayCost, _, err := store2.GetTodayUsageTotal(acctID)
	if err != nil {
		t.Fatalf("get today total after reopen: %v", err)
	}
	if math.Abs(todayCost-(0.025+1.20)) > 1e-6 {
		t.Fatalf("expected today cost 1.225, got %f", todayCost)
	}

	acc, ok, err := store2.GetDailyUsageAccumulator(acctID, today)
	if err != nil || !ok {
		t.Fatalf("get accumulator after reopen: ok=%v err=%v", ok, err)
	}
	if acc.MediaCalls != 1 || acc.MediaCostUSD != 1.20 {
		t.Fatalf("expected media calls 1 and cost 1.20, got calls=%d cost=%f", acc.MediaCalls, acc.MediaCostUSD)
	}
}

func TestAtomicPutMediaUsageValidationAndRollback(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test-atomic.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	store := NewSessionStore(db)
	acctID := "acct-valid"
	sessID := "sess-valid"
	if err := store.CreateSession(SessionSnapshot{ID: sessID, AccountScopeID: acctID, Title: "Valid"}); err != nil {
		t.Fatalf("set session: %v", err)
	}

	// 1. Rejection on missing session
	err = store.PutMediaUsage(SessionMediaUsageRecord{
		ID:             "media-fail-1",
		SessionID:      "non-existent-sess",
		AccountScopeID: acctID,
		CostUSD:        1.0,
	})
	if err == nil {
		t.Fatal("expected error on non-existent session, got nil")
	}

	// 2. Rejection on account mismatch
	err = store.PutMediaUsage(SessionMediaUsageRecord{
		ID:             "media-fail-2",
		SessionID:      sessID,
		AccountScopeID: "attacker-acct",
		CostUSD:        1.0,
	})
	if err == nil {
		t.Fatal("expected error on account scope mismatch, got nil")
	}

	// Verify no partial records or accumulator increments were created
	acc, found, _ := store.GetDailyUsageAccumulator(acctID, time.Now().UTC().Format("2006-01-02"))
	if found && acc.MediaCalls > 0 {
		t.Fatalf("unexpected media calls in accumulator after failed validation: %d", acc.MediaCalls)
	}
}

func TestMediaCostEstimateSnapshotProvenance(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test-estimate.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	store := NewSessionStore(db)
	catStore := NewModelCatalogStore(db)

	// Add catalog record with snapshot pricing
	err = catStore.SetRecord(ModelCatalogRecord{
		Provider:              "google",
		Model:                 "imagen-3.0",
		SourceSnapshotID:      "snap-2026-09",
		SourceSnapshotVersion: "v1",
		Pricing:               []byte(`{"per_image": 0.035}`),
	})
	if err != nil {
		t.Fatalf("set catalog record: %v", err)
	}

	// 1. Model with snapshot pricing
	est := store.EstimateMediaCost("google", "imagen-3.0", "image", 2, 0, false)
	if est.PriceStatus != "known" {
		t.Fatalf("expected price status known, got %s", est.PriceStatus)
	}
	if est.CostUSD != 0.070 {
		t.Fatalf("expected cost 0.070, got %f", est.CostUSD)
	}
	if est.SnapshotID != "snap-2026-09" {
		t.Fatalf("expected snapshot snap-2026-09, got %s", est.SnapshotID)
	}

	// 2. Unpriced model in catalog: explicit unknown status, no hardcoded invented rates
	err = catStore.SetRecord(ModelCatalogRecord{
		Provider:              "google",
		Model:                 "unpriced-vision",
		SourceSnapshotID:      "snap-2026-09",
		SourceSnapshotVersion: "v1",
	})
	if err != nil {
		t.Fatalf("set unpriced record: %v", err)
	}
	estUnpriced := store.EstimateMediaCost("google", "unpriced-vision", "image", 1, 0, false)
	if estUnpriced.PriceStatus != "unknown" {
		t.Fatalf("expected unknown price status, got %s", estUnpriced.PriceStatus)
	}
	if estUnpriced.CostUSD != 0.0 {
		t.Fatalf("expected 0 cost for unpriced model, got %f", estUnpriced.CostUSD)
	}

	// 3. Codex model: subscription status ($0 billed)
	estCodex := store.EstimateMediaCost("codex", "gpt-image-1", "image", 1, 0, false)
	if estCodex.PriceStatus != "subscription" {
		t.Fatalf("expected subscription status for codex, got %s", estCodex.PriceStatus)
	}
	if estCodex.CostUSD != 0.0 {
		t.Fatalf("expected 0 cost for codex subscription, got %f", estCodex.CostUSD)
	}

	// 4. Video model with billing lines schema: unit second
	err = catStore.SetRecord(ModelCatalogRecord{
		Provider:              "google",
		Model:                 "veo-2.0",
		SourceSnapshotID:      "snap-2026-09",
		SourceSnapshotVersion: "v1",
		Pricing:               []byte(`{"billing":{"status":"verified","lines":[{"billable":"video_output","unit":"second","price_usd":0.05,"conditions":{"resolution":"720p"}}]}}`),
	})
	if err != nil {
		t.Fatalf("set video catalog record: %v", err)
	}
	estVideo := store.EstimateMediaCostWithOptions(MediaCostEstimateOptions{
		Provider:        "google",
		Model:           "veo-2.0",
		Kind:            "video",
		Count:           2,
		DurationSeconds: 6,
		Resolution:      "720p",
	})
	if estVideo.PriceStatus != "known" {
		t.Fatalf("expected video price status known, got %s", estVideo.PriceStatus)
	}
	expectedVideoCost := 2.0 * 6.0 * 0.05
	if estVideo.CostUSD < expectedVideoCost-0.0001 || estVideo.CostUSD > expectedVideoCost+0.0001 {
		t.Fatalf("expected video cost %f, got %f", expectedVideoCost, estVideo.CostUSD)
	}

	// Unknown duration on second-metered video must reject to unknown, not invent 8s
	estVideoZeroDuration := store.EstimateMediaCostWithOptions(MediaCostEstimateOptions{
		Provider:        "google",
		Model:           "veo-2.0",
		Kind:            "video",
		Count:           1,
		DurationSeconds: 0,
		Resolution:      "720p",
	})
	if estVideoZeroDuration.PriceStatus != "unknown" || estVideoZeroDuration.CostUSD != 0.0 {
		t.Fatalf("expected unknown price status on 0 duration second-metered video, got status=%s cost=%f", estVideoZeroDuration.PriceStatus, estVideoZeroDuration.CostUSD)
	}

	// Absent resolution condition must reject to unknown (not match arbitrarily)
	estVideoAbsentResolution := store.EstimateMediaCostWithOptions(MediaCostEstimateOptions{
		Provider:        "google",
		Model:           "veo-2.0",
		Kind:            "video",
		Count:           1,
		DurationSeconds: 6,
		Resolution:      "",
	})
	if estVideoAbsentResolution.PriceStatus != "unknown" || estVideoAbsentResolution.CostUSD != 0.0 {
		t.Fatalf("expected unknown price status on absent resolution, got status=%s cost=%f", estVideoAbsentResolution.PriceStatus, estVideoAbsentResolution.CostUSD)
	}

	// Mismatched resolution condition must reject to unknown
	estVideoMismatchedResolution := store.EstimateMediaCostWithOptions(MediaCostEstimateOptions{
		Provider:        "google",
		Model:           "veo-2.0",
		Kind:            "video",
		Count:           1,
		DurationSeconds: 6,
		Resolution:      "1080p",
	})
	if estVideoMismatchedResolution.PriceStatus != "unknown" || estVideoMismatchedResolution.CostUSD != 0.0 {
		t.Fatalf("expected unknown price status on mismatched resolution, got status=%s cost=%f", estVideoMismatchedResolution.PriceStatus, estVideoMismatchedResolution.CostUSD)
	}

	// 5. Audio model with actual Lyria billing lines schema
	err = catStore.SetRecord(ModelCatalogRecord{
		Provider:              "google",
		Model:                 "lyria-3.5",
		SourceSnapshotID:      "snap-2026-09",
		SourceSnapshotVersion: "v1",
		Pricing: []byte(`{
			"currency": "USD",
			"billing": {
				"status": "verified",
				"lines": [
					{"kind":"billing_rate","billable":"song","unit":"song","price_usd":0.08,"variant":"full_song","conditions":{"tier":"paid","service_tier":"standard"}},
					{"kind":"billing_rate","billable":"song","unit":"song","price_usd":0.04,"variant":"clip","conditions":{"tier":"paid","service_tier":"standard"}}
				]
			}
		}`),
	})
	if err != nil {
		t.Fatalf("set audio catalog: %v", err)
	}
	estSong := store.EstimateMediaCostWithOptions(MediaCostEstimateOptions{
		Provider: "google",
		Model:    "lyria-3.5",
		Kind:     "audio",
		Count:    1,
	})
	if estSong.PriceStatus != "known" || estSong.CostUSD != 0.08 {
		t.Fatalf("expected lyria 3.5 song cost $0.08 known, got status=%s cost=%f", estSong.PriceStatus, estSong.CostUSD)
	}

	estClip := store.EstimateMediaCostWithOptions(MediaCostEstimateOptions{
		Provider: "google",
		Model:    "lyria-clip",
		Kind:     "audio",
		Count:    1,
	})
	if estClip.PriceStatus != "known" || estClip.CostUSD != 0.04 {
		t.Fatalf("expected lyria clip cost $0.04 known, got status=%s cost=%f", estClip.PriceStatus, estClip.CostUSD)
	}

	// 6. Image model with token-metered line: requires actual output token quantities
	err = catStore.SetRecord(ModelCatalogRecord{
		Provider:              "google",
		Model:                 "gemini-image-token",
		SourceSnapshotID:      "snap-2026-09",
		SourceSnapshotVersion: "v1",
		Pricing: []byte(`{
			"currency": "USD",
			"billing": {
				"status": "verified",
				"lines": [
					{"kind":"billing_rate","billable":"image_output","unit":"token","price_usd":0.00003,"variant":"standard"}
				]
			}
		}`),
	})
	if err != nil {
		t.Fatalf("set image token catalog: %v", err)
	}
	estImgTokens := store.EstimateMediaCostWithOptions(MediaCostEstimateOptions{
		Provider:     "google",
		Model:        "gemini-image-token",
		Kind:         "image",
		Count:        1,
		OutputTokens: 1500,
	})
	if estImgTokens.PriceStatus != "known" || estImgTokens.CostUSD != (1500.0*0.00003) {
		t.Fatalf("expected token-metered image cost %f known, got status=%s cost=%f", 1500.0*0.00003, estImgTokens.PriceStatus, estImgTokens.CostUSD)
	}

	estImgTokensZero := store.EstimateMediaCostWithOptions(MediaCostEstimateOptions{
		Provider:     "google",
		Model:        "gemini-image-token",
		Kind:         "image",
		Count:        1,
		OutputTokens: 0,
	})
	if estImgTokensZero.PriceStatus != "unknown" || estImgTokensZero.CostUSD != 0.0 {
		t.Fatalf("expected unknown price status for 0-token image line, got status=%s cost=%f", estImgTokensZero.PriceStatus, estImgTokensZero.CostUSD)
	}

	// 7. Video with includes_audio condition
	err = catStore.SetRecord(ModelCatalogRecord{
		Provider:              "google",
		Model:                 "veo-audio",
		SourceSnapshotID:      "snap-2026-09",
		SourceSnapshotVersion: "v1",
		Pricing:               []byte(`{"currency":"USD","billing":{"status":"verified","lines":[{"billable":"video_output","unit":"second","price_usd":0.05,"conditions":{"resolution":"720p","includes_audio":true}}]}}`),
	})
	if err != nil {
		t.Fatalf("set video with audio catalog: %v", err)
	}
	estVeoAudio := store.EstimateMediaCostWithOptions(MediaCostEstimateOptions{
		Provider:        "google",
		Model:           "veo-audio",
		Kind:            "video",
		Count:           1,
		DurationSeconds: 5,
		Resolution:      "720p",
		IncludesAudio:   true,
	})
	if estVeoAudio.PriceStatus != "known" || estVeoAudio.CostUSD != (5.0*0.05) {
		t.Fatalf("expected veo with audio cost %f known, got status=%s cost=%f", 5.0*0.05, estVeoAudio.PriceStatus, estVeoAudio.CostUSD)
	}
	estVeoNoAudio := store.EstimateMediaCostWithOptions(MediaCostEstimateOptions{
		Provider:        "google",
		Model:           "veo-audio",
		Kind:            "video",
		Count:           1,
		DurationSeconds: 5,
		Resolution:      "720p",
		IncludesAudio:   false,
	})
	if estVeoNoAudio.PriceStatus != "unknown" || estVeoNoAudio.CostUSD != 0.0 {
		t.Fatalf("expected unknown price status when includes_audio=false for audio-conditioned line, got status=%s cost=%f", estVeoNoAudio.PriceStatus, estVeoNoAudio.CostUSD)
	}

	// 8. Image model with unverified status: MUST reject to unknown, no fallback to raw keys
	err = catStore.SetRecord(ModelCatalogRecord{
		Provider:              "google",
		Model:                 "unverified-image",
		SourceSnapshotID:      "snap-2026-09",
		SourceSnapshotVersion: "v1",
		Pricing:               []byte(`{"currency":"USD","per_image":0.03,"billing":{"status":"unverified","lines":[{"billable":"image_output","unit":"image","price_usd":0.03}]}}`),
	})
	if err != nil {
		t.Fatalf("set unverified image catalog: %v", err)
	}
	estUnverifiedImg := store.EstimateMediaCostWithOptions(MediaCostEstimateOptions{
		Provider: "google",
		Model:    "unverified-image",
		Kind:     "image",
		Count:    1,
	})
	if estUnverifiedImg.PriceStatus != "unknown" || estUnverifiedImg.CostUSD != 0.0 {
		t.Fatalf("expected unknown price status for unverified image, got status=%s cost=%f", estUnverifiedImg.PriceStatus, estUnverifiedImg.CostUSD)
	}

	// 9. Image model with resolution condition matching
	err = catStore.SetRecord(ModelCatalogRecord{
		Provider:              "google",
		Model:                 "imagen-res",
		SourceSnapshotID:      "snap-2026-09",
		SourceSnapshotVersion: "v1",
		Pricing: []byte(`{"currency":"USD","billing":{"status":"verified","lines":[
			{"billable":"image_output","unit":"image","price_usd":0.03,"conditions":{"resolution":"1024x1024"}},
			{"billable":"image_output","unit":"image","price_usd":0.06,"conditions":{"resolution":"2048x2048"}}
		]}}`),
	})
	if err != nil {
		t.Fatalf("set imagen-res catalog: %v", err)
	}
	estImg1024 := store.EstimateMediaCostWithOptions(MediaCostEstimateOptions{
		Provider:   "google",
		Model:      "imagen-res",
		Kind:       "image",
		Count:      1,
		Resolution: "1024x1024",
	})
	if estImg1024.PriceStatus != "known" || estImg1024.CostUSD != 0.03 {
		t.Fatalf("expected imagen-res 1024 cost 0.03, got status=%s cost=%f", estImg1024.PriceStatus, estImg1024.CostUSD)
	}
	estImgAbsentRes := store.EstimateMediaCostWithOptions(MediaCostEstimateOptions{
		Provider:   "google",
		Model:      "imagen-res",
		Kind:       "image",
		Count:      1,
		Resolution: "",
	})
	if estImgAbsentRes.PriceStatus != "unknown" || estImgAbsentRes.CostUSD != 0.0 {
		t.Fatalf("expected imagen-res absent resolution to be unknown, got status=%s cost=%f", estImgAbsentRes.PriceStatus, estImgAbsentRes.CostUSD)
	}

	// 10. Image model with image_size condition matching
	err = catStore.SetRecord(ModelCatalogRecord{
		Provider:              "google",
		Model:                 "imagen-size",
		SourceSnapshotID:      "snap-2026-09",
		SourceSnapshotVersion: "v1",
		Pricing: []byte(`{"currency":"USD","billing":{"status":"verified","lines":[
			{"billable":"image_output","unit":"image","price_usd":0.03,"conditions":{"image_size":"1K"}},
			{"billable":"image_output","unit":"image","price_usd":0.06,"conditions":{"image_size":"2K"}}
		]}}`),
	})
	if err != nil {
		t.Fatalf("set imagen-size catalog: %v", err)
	}
	estImg1K := store.EstimateMediaCostWithOptions(MediaCostEstimateOptions{
		Provider:  "google",
		Model:     "imagen-size",
		Kind:      "image",
		Count:     1,
		ImageSize: "1K",
	})
	if estImg1K.PriceStatus != "known" || estImg1K.CostUSD != 0.03 {
		t.Fatalf("expected imagen-size 1K cost 0.03, got status=%s cost=%f", estImg1K.PriceStatus, estImg1K.CostUSD)
	}
}

func TestTurnUsageBilledTokensDeltasAndUnknownPriceStatus(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test-deltas.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	store := NewSessionStore(db)
	catStore := NewModelCatalogStore(db)
	// Add an unpriced model to catalog
	err = catStore.SetRecord(ModelCatalogRecord{
		Provider:              "google",
		Model:                 "unpriced-text",
		SourceSnapshotID:      "snap-1",
		SourceSnapshotVersion: "v1",
		Pricing:               []byte(`{"billing":{"status":"unverified"}}`),
	})
	if err != nil {
		t.Fatalf("set catalog: %v", err)
	}

	acctID := "acct-delta"
	sessID := "sess-delta"
	runID := "run-delta"
	dateStr := time.Now().UTC().Format("2006-01-02")
	if err := store.CreateSession(SessionSnapshot{ID: sessID, AccountScopeID: acctID, UserID: "u1", Title: "Delta Test"}); err != nil {
		t.Fatalf("set session: %v", err)
	}

	// 1. Initial turn: 1000 input tokens from request 1
	turn1 := SessionTurnUsageSnapshot{
		SessionID:          sessID,
		AccountScopeID:     acctID,
		UserID:             "u1",
		RunID:              runID,
		Provider:           "google",
		Model:              "unpriced-text",
		InputTokens:        1000,
		OutputTokens:       100,
		TotalTokens:        1100,
		BilledTokens:       1100,
		BilledInputTokens:  1000,
		BilledOutputTokens: 100,
		CreatedAt:          time.Now().UnixMilli(),
	}
	if err := store.PutTurnUsage(turn1); err != nil {
		t.Fatalf("put turn 1: %v", err)
	}

	// Verify price status is marked unknown and unknown count is incremented
	readTurn1, found, err := store.GetTurnUsage(sessID, runID)
	if err != nil || !found {
		t.Fatalf("get turn 1: found=%v err=%v", found, err)
	}
	if readTurn1.PriceStatus != "unknown" {
		t.Fatalf("expected price status unknown, got %s", readTurn1.PriceStatus)
	}

	rollup1, found, err := store.GetAccountUsageRollup(acctID, dateStr, sessID, "google", "unpriced-text")
	if err != nil || !found {
		t.Fatalf("get rollup 1: found=%v err=%v", found, err)
	}
	if rollup1.UnknownCount != 1 {
		t.Fatalf("expected unknown count 1, got %d", rollup1.UnknownCount)
	}
	if rollup1.InputTokens != 1000 || rollup1.Turns != 1 {
		t.Fatalf("expected input tokens 1000 and turns 1, got tokens=%d turns=%d", rollup1.InputTokens, rollup1.Turns)
	}

	// 2. Repeat update for the same run: distinct second request takes 1500 input tokens, 200 output tokens.
	// Conversational occupancy is 1500 input tokens.
	// Cumulative billed input tokens = 1000 (req 1) + 1500 (req 2) = 2500.
	turn2 := SessionTurnUsageSnapshot{
		SessionID:          sessID,
		AccountScopeID:     acctID,
		UserID:             "u1",
		RunID:              runID,
		Provider:           "google",
		Model:              "unpriced-text",
		InputTokens:        1500, // latest occupancy
		OutputTokens:       200,  // latest response
		TotalTokens:        1700,
		BilledTokens:       2800, // 1100 + 1700
		BilledInputTokens:  2500, // 1000 + 1500
		BilledOutputTokens: 300,  // 100 + 200
		CreatedAt:          time.Now().UnixMilli(),
	}
	if err := store.PutTurnUsage(turn2); err != nil {
		t.Fatalf("put turn 2: %v", err)
	}

	readTurn2, found, err := store.GetTurnUsage(sessID, runID)
	if err != nil || !found {
		t.Fatalf("get turn 2: found=%v err=%v", found, err)
	}
	if readTurn2.InputTokens != 1500 {
		t.Fatalf("expected occupancy input tokens 1500, got %d", readTurn2.InputTokens)
	}
	if readTurn2.BilledInputTokens != 2500 {
		t.Fatalf("expected billed input tokens 2500, got %d", readTurn2.BilledInputTokens)
	}

	rollup2, found, err := store.GetAccountUsageRollup(acctID, dateStr, sessID, "google", "unpriced-text")
	if err != nil || !found {
		t.Fatalf("get rollup 2: found=%v err=%v", found, err)
	}
	// InputTokens in rollup must be cumulative billed input tokens 2500 (1000 req1 + 1500 req2)
	if rollup2.InputTokens != 2500 {
		t.Fatalf("expected cumulative billed input tokens 2500, got %d", rollup2.InputTokens)
	}
	// OutputTokens must be cumulative billed output tokens 300 (100 req1 + 200 req2)
	if rollup2.OutputTokens != 300 {
		t.Fatalf("expected cumulative billed output tokens 300, got %d", rollup2.OutputTokens)
	}
	if rollup2.TotalTokens != 2800 {
		t.Fatalf("expected cumulative billed total tokens 2800, got %d", rollup2.TotalTokens)
	}
	// Turns must stay 1, not 2
	if rollup2.Turns != 1 {
		t.Fatalf("expected turns 1, got %d", rollup2.Turns)
	}

	// DailyAccumulator must also reflect cumulative billed components
	acc, found, err := store.GetDailyUsageAccumulator(acctID, dateStr)
	if err != nil || !found {
		t.Fatalf("get daily accumulator: found=%v err=%v", found, err)
	}
	if acc.InputTokens != 2500 || acc.OutputTokens != 300 || acc.TotalTokens != 2800 || acc.TurnCount != 1 {
		t.Fatalf("daily accumulator tokens mismatch: input=%d output=%d total=%d turns=%d", acc.InputTokens, acc.OutputTokens, acc.TotalTokens, acc.TurnCount)
	}
}

// TestConcurrentDifferentSessionsDailyAccumulator proves that concurrent writes from different
// sessions belonging to the same account serialize correctly on the shared account lock without
// losing spend or corrupted accumulators.
func TestConcurrentDifferentSessionsDailyAccumulator(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test-concurrent.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	store := NewSessionStore(db)
	acctID := "acct-concurrent"
	today := time.Now().UTC().Format("2006-01-02")
	const sessionCount = 20

	for i := 0; i < sessionCount; i++ {
		sessID := fmt.Sprintf("sess-conc-%d", i)
		if err := store.CreateSession(SessionSnapshot{ID: sessID, AccountScopeID: acctID, Title: sessID}); err != nil {
			t.Fatalf("create session %d: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	errCh := make(chan error, sessionCount)

	for i := 0; i < sessionCount; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			sessID := fmt.Sprintf("sess-conc-%d", i)
			rec := SessionMediaUsageRecord{
				ID:             fmt.Sprintf("media-%d", i),
				SessionID:      sessID,
				AccountScopeID: acctID,
				MediaType:      "image/png",
				Kind:           "image",
				Provider:       "google",
				Model:          "imagen-3.0",
				CostUSD:        0.05,
				CreatedAt:      time.Now().UnixMilli(),
			}
			if err := store.PutMediaUsage(rec); err != nil {
				errCh <- err
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent write error: %v", err)
		}
	}

	acc, ok, err := store.GetDailyUsageAccumulator(acctID, today)
	if err != nil || !ok {
		t.Fatalf("get daily accumulator: ok=%v err=%v", ok, err)
	}
	expectedCost := float64(sessionCount) * 0.05
	if acc.MediaCalls != sessionCount {
		t.Fatalf("lost media calls: expected %d, got %d", sessionCount, acc.MediaCalls)
	}
	if acc.TotalCostUSD < expectedCost-0.0001 || acc.TotalCostUSD > expectedCost+0.0001 {
		t.Fatalf("lost spend in concurrent race: expected %f, got %f", expectedCost, acc.TotalCostUSD)
	}
}
