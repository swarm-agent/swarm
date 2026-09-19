package pebblestore

import (
	"path/filepath"
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
		TotalTokens:      1300, // occupancy
		BilledTokens:     2400, // cumulative billed tokens (1100 + 1300)
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
	if todayCost != (0.025 + 1.20) {
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
