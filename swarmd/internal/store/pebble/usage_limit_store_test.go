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
