package pebblestore

import (
	"encoding/json"
	"math"
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
	// Gemini 3.8 Flash: $0.75 input / $3.75 output per million
	cost := CalculateBaselineCost("google", "gemini-3.8-flash", 1_000_000, 1_000_000, 0, 0)
	if cost < 4.49 || cost > 4.51 {
		t.Errorf("expected Gemini 3.8 Flash cost ~4.50, got %f", cost)
	}

	// Gemini 3.8 Flash with cached input (90% discount: $0.075 cached rate)
	costWithCache := CalculateBaselineCost("google", "gemini-3.8-flash", 1_000_000, 0, 500_000, 0)
	// 500k regular = 0.5 * 0.75 = 0.375; 500k cached = 0.5 * 0.075 = 0.0375 -> 0.4125
	if costWithCache < 0.41 || costWithCache > 0.42 {
		t.Errorf("expected cached cost ~0.4125, got %f", costWithCache)
	}
}

func TestCalculateCostWithStatusNoBaselineFallback(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test-calc-status.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	store := NewSessionStore(db)
	catStore := NewModelCatalogStore(db)

	// 1. Unpriced model in catalog: returns 0.0 and "unknown", never falls back to baseline table
	err = catStore.SetRecord(ModelCatalogRecord{
		Provider: "google",
		Model:    "gemini-unpriced-experiment",
	})
	if err != nil {
		t.Fatalf("set catalog: %v", err)
	}
	cost, status := store.CalculateCostWithStatus("google", "gemini-unpriced-experiment", 1_000_000, 1_000_000, 0, 0, 0)
	if status != "unknown" || cost != 0.0 {
		t.Fatalf("expected status unknown and cost 0.0 without fallback, got status=%s cost=%f", status, cost)
	}

	// 2. Priced model in catalog: returns exact snapshot cost and "known"
	inp := 1.25
	out := 5.0
	pricingJSON, _ := json.Marshal(map[string]any{
		"input_price_per_million_tokens":  inp,
		"output_price_per_million_tokens": out,
	})
	err = catStore.SetRecord(ModelCatalogRecord{
		Provider: "google",
		Model:    "gemini-priced-test",
		Pricing:  pricingJSON,
	})
	if err != nil {
		t.Fatalf("set catalog: %v", err)
	}
	cost, status = store.CalculateCostWithStatus("google", "gemini-priced-test", 1_000_000, 100_000, 0, 0, 0)
	if status != "known" {
		t.Fatalf("expected status known, got %s", status)
	}
	expectedCost := 1.25 + 0.50 // 1.75
	if cost < expectedCost-0.0001 || cost > expectedCost+0.0001 {
		t.Fatalf("expected cost %f, got %f", expectedCost, cost)
	}

	// 3. Codex model: subscription status and 0.0 billed cost
	cost, status = store.CalculateCostWithStatus("codex", "gpt-5.6-sol", 100_000, 10_000, 0, 0, 0)
	if status != "subscription" || cost != 0.0 {
		t.Fatalf("expected codex subscription with cost 0.0, got status=%s cost=%f", status, cost)
	}
}

// Purpose: catalog cost for Anthropic receipts must follow Anthropic's usage
// semantics. Requirement: Anthropic input_tokens is the uncached remainder only,
// cache reads bill at the cached rate, and cache writes bill at the catalog
// cache_write_5m rate (the TTL the adapter requests). Threat: subtracting cache
// reads from input_tokens and ignoring cache writes under-reports spend, which
// hides real cost from the usage dashboard and budget holds. A receipt with cache
// writes but no catalog write rate must not be reported as fully known.
// Authority: SessionStore.CalculateCostWithStatus over ModelCatalogStore pricing.
// Layer: a temporary Pebble store with a seeded catalog record is the narrowest
// layer that exercises the real catalog lookup.
func TestCalculateCostWithStatusAnthropicCacheSemantics(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test-calc-anthropic.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	store := NewSessionStore(db)
	catStore := NewModelCatalogStore(db)

	pricedJSON, _ := json.Marshal(map[string]any{
		"input_price_per_million_tokens":        4.0,
		"output_price_per_million_tokens":       20.0,
		"cached_input_price_per_million_tokens": 0.2,
		"billing": map[string]any{"lines": []map[string]any{
			{"variant": "standard_input", "price_usd": 4.0},
			{"variant": "cache_write_5m", "price_usd": 5.0},
			{"variant": "cache_write_1h", "price_usd": 8.0},
		}},
	})
	if err := catStore.SetRecord(ModelCatalogRecord{Provider: "anthropic", Model: "claude-priced-test", Pricing: pricedJSON}); err != nil {
		t.Fatalf("set catalog: %v", err)
	}
	// 10k uncached input, 2M cache read, 100k cache write, 50k output.
	cost, status := store.CalculateCostWithStatus("anthropic", "claude-priced-test", 10_000, 50_000, 2_000_000, 100_000, 0)
	if status != "known" {
		t.Fatalf("status = %s, want known", status)
	}
	want := 0.04 + 0.40 + 0.50 + 1.00
	if math.Abs(cost-want) > 1e-9 {
		t.Fatalf("cost = %f, want %f", cost, want)
	}

	// Cache reads larger than input_tokens must not zero out the uncached input.
	cost, _ = store.CalculateCostWithStatus("anthropic", "claude-priced-test", 10_000, 0, 2_000_000, 0, 0)
	if want := 0.04 + 0.40; math.Abs(cost-want) > 1e-9 {
		t.Fatalf("read-heavy cost = %f, want %f", cost, want)
	}

	noWriteRateJSON, _ := json.Marshal(map[string]any{
		"input_price_per_million_tokens":        4.0,
		"output_price_per_million_tokens":       20.0,
		"cached_input_price_per_million_tokens": 0.2,
	})
	if err := catStore.SetRecord(ModelCatalogRecord{Provider: "anthropic", Model: "claude-no-write-rate", Pricing: noWriteRateJSON}); err != nil {
		t.Fatalf("set catalog: %v", err)
	}
	cost, status = store.CalculateCostWithStatus("anthropic", "claude-no-write-rate", 10_000, 0, 0, 100_000, 0)
	if status != "unknown" {
		t.Fatalf("status without write rate = %s, want unknown", status)
	}
	if want := 0.04; math.Abs(cost-want) > 1e-9 {
		t.Fatalf("partial cost without write rate = %f, want %f (writes must not be priced at a guess)", cost, want)
	}
	if _, status := store.CalculateCostWithStatus("anthropic", "claude-no-write-rate", 10_000, 0, 0, 0, 0); status != "known" {
		t.Fatalf("status without cache writes = %s, want known", status)
	}

	// Providers whose prompt tokens include cache reads keep the subtraction.
	googleJSON, _ := json.Marshal(map[string]any{
		"input_price_per_million_tokens":        1.0,
		"output_price_per_million_tokens":       1.0,
		"cached_input_price_per_million_tokens": 0.1,
	})
	if err := catStore.SetRecord(ModelCatalogRecord{Provider: "google", Model: "gemini-cache-test", Pricing: googleJSON}); err != nil {
		t.Fatalf("set catalog: %v", err)
	}
	cost, _ = store.CalculateCostWithStatus("google", "gemini-cache-test", 1_000_000, 0, 500_000, 0, 0)
	if want := 0.5 + 0.05; math.Abs(cost-want) > 1e-9 {
		t.Fatalf("google cost = %f, want %f", cost, want)
	}
}
