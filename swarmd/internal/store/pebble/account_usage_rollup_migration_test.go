package pebblestore

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAccountUsageRollupBackfillMigration(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test-rollup-migration.pebble")
	store, err := Open(dbDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	sessionStore := NewSessionStore(store)
	acctID := "acct-mig-1"
	sess1 := "sess-mig-1"
	sess2 := "sess-mig-2"
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	yesterday := now.Add(-24 * time.Hour).Format("2006-01-02")

	// 1. Populate turn usage directly without creating rollups (simulating pre-migration DB state)
	turn1 := SessionTurnUsageSnapshot{
		SessionID:       sess1,
		AccountScopeID:  acctID,
		RunID:           "run-1",
		Provider:        "google",
		Model:           "gemini-3.8-flash",
		InputTokens:     100000,
		OutputTokens:    10000,
		ThinkingTokens:  2000,
		CacheReadTokens: 50000,
		TotalTokens:     112000,
		CreatedAt:       now.UnixMilli(),
		UpdatedAt:       now.UnixMilli(),
	}
	turnPayload1, _ := jsonMarshal(turn1)
	if err := store.db.Set([]byte(KeySessionTurnUsage(turn1.SessionID, turn1.RunID)), turnPayload1, nil); err != nil {
		t.Fatalf("set turn 1: %v", err)
	}

	// Turn 2: in yesterday's bucket
	yesterdayMs := now.Add(-24 * time.Hour).UnixMilli()
	turn2 := SessionTurnUsageSnapshot{
		SessionID:       sess1,
		AccountScopeID:  acctID,
		RunID:           "run-2",
		Provider:        "google",
		Model:           "gemini-3.8-flash",
		InputTokens:     50000,
		OutputTokens:    5000,
		ThinkingTokens:  1000,
		CacheReadTokens: 20000,
		TotalTokens:     56000,
		CreatedAt:       yesterdayMs,
		UpdatedAt:       yesterdayMs,
	}
	turnPayload2, _ := jsonMarshal(turn2)
	if err := store.db.Set([]byte(KeySessionTurnUsage(turn2.SessionID, turn2.RunID)), turnPayload2, nil); err != nil {
		t.Fatalf("set turn 2: %v", err)
	}

	// Turn 3: codex in sess2
	turn3 := SessionTurnUsageSnapshot{
		SessionID:       sess2,
		AccountScopeID:  acctID,
		RunID:           "run-3",
		Provider:        "codex",
		Model:           "gpt-5.5",
		InputTokens:     40000,
		OutputTokens:    2000,
		ThinkingTokens:  500,
		CacheReadTokens: 10000,
		TotalTokens:     42500,
		CreatedAt:       now.UnixMilli(),
		UpdatedAt:       now.UnixMilli(),
	}
	turnPayload3, _ := jsonMarshal(turn3)
	if err := store.db.Set([]byte(KeySessionTurnUsage(turn3.SessionID, turn3.RunID)), turnPayload3, nil); err != nil {
		t.Fatalf("set turn 3: %v", err)
	}

	// Media 1: image in sess1
	media1 := SessionMediaUsageRecord{
		ID:             "media-img",
		SessionID:      sess1,
		AccountScopeID: acctID,
		Provider:       "google",
		Model:          "imagen-3.0",
		Kind:           "image",
		MediaType:      "image/png",
		CostUSD:        0.04,
		CreatedAt:      now.UnixMilli(),
	}
	mediaPayload1, _ := jsonMarshal(media1)
	if err := store.db.Set([]byte(KeySessionMediaUsage(acctID, media1.ID)), mediaPayload1, nil); err != nil {
		t.Fatalf("set media 1: %v", err)
	}

	// 2. Ensure NO rollups exist before migration
	preRollups, err := sessionStore.ListAccountUsageRollups(acctID)
	if err != nil {
		t.Fatalf("list rollups before: %v", err)
	}
	if len(preRollups) != 0 {
		t.Fatalf("expected 0 rollups before migration, got %d", len(preRollups))
	}

	// 3. Run migration
	res, err := RunAccountUsageRollupBackfillMigration(store)
	if err != nil {
		t.Fatalf("RunAccountUsageRollupBackfillMigration: %v", err)
	}
	if !res.Applied || res.AlreadyApplied {
		t.Fatalf("expected applied=true, alreadyApplied=false, got %+v", res)
	}
	if res.TurnsScanned != 3 || res.MediaScanned != 1 || res.RollupsCommitted != 4 {
		t.Fatalf("unexpected migration counts: %+v", res)
	}

	// 4. Verify rollups
	postRollups, err := sessionStore.ListAccountUsageRollups(acctID)
	if err != nil {
		t.Fatalf("list rollups after: %v", err)
	}
	if len(postRollups) != 4 {
		t.Fatalf("expected 4 rollups after migration, got %d", len(postRollups))
	}

	// Check sess1 today turn rollup
	r1, found, err := sessionStore.GetAccountUsageRollup(acctID, today, sess1, "google", "gemini-3.8-flash")
	if err != nil || !found {
		t.Fatalf("expected rollup for sess1 today: found=%v, err=%v", found, err)
	}
	if r1.Turns != 1 || r1.TotalTokens != 112000 || r1.ThinkingTokens != 2000 {
		t.Fatalf("unexpected r1: %+v", r1)
	}
	// Calculation check:
	// Uncached in: 50,000 / 1e6 * 0.75 = 0.0375
	// Cached in: 50,000 / 1e6 * 0.075 = 0.00375
	// Output: 12,000 / 1e6 * 3.75 = 0.045
	// Total = 0.08625
	expectedCost1 := 0.08625
	if r1.TokenCostUSD < expectedCost1-0.0001 || r1.TokenCostUSD > expectedCost1+0.0001 {
		t.Errorf("r1 TokenCostUSD = %f, want ~%f", r1.TokenCostUSD, expectedCost1)
	}

	// Check sess1 today media rollup
	rMedia, found, err := sessionStore.GetAccountUsageRollup(acctID, today, sess1, "google", "imagen-3.0")
	if err != nil || !found {
		t.Fatalf("expected media rollup for sess1 today: found=%v, err=%v", found, err)
	}
	if rMedia.MediaCalls != 1 || rMedia.ImageCount != 1 || rMedia.ImageCostUSD != 0.04 || rMedia.VideoCostUSD != 0 {
		t.Fatalf("unexpected media rollup: %+v", rMedia)
	}

	// Check sess2 codex rollup
	rCodex, found, err := sessionStore.GetAccountUsageRollup(acctID, today, sess2, "codex", "gpt-5.5")
	if err != nil || !found {
		t.Fatalf("expected codex rollup for sess2 today: found=%v, err=%v", found, err)
	}
	if rCodex.TokenCostUSD != 0.0 || rCodex.CodexNominalCostUSD <= 0 {
		t.Fatalf("expected codex TokenCostUSD=0, CodexNominalCostUSD>0, got %+v", rCodex)
	}

	// Check yesterday rollup
	rYesterday, found, err := sessionStore.GetAccountUsageRollup(acctID, yesterday, sess1, "google", "gemini-3.8-flash")
	if err != nil || !found {
		t.Fatalf("expected rollup for sess1 yesterday: found=%v, err=%v", found, err)
	}
	if rYesterday.Turns != 1 || rYesterday.TotalTokens != 56000 {
		t.Fatalf("unexpected yesterday rollup: %+v", rYesterday)
	}

	// 5. Test idempotency: running again should report already applied
	secondRes, err := RunAccountUsageRollupBackfillMigration(store)
	if err != nil {
		t.Fatalf("second migration run: %v", err)
	}
	if !secondRes.AlreadyApplied {
		t.Fatalf("expected already applied on second run, got %+v", secondRes)
	}
}
