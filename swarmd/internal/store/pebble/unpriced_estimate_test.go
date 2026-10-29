package pebblestore

import (
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// unpricedFixtureEstimate is 1000 input and 1000 output tokens at the highest
// built-in rates (10 and 50 USD per million), used when nothing is known for
// the receipt's provider.
const unpricedFixtureEstimate = (1000*10.0 + 1000*50.0) / 1e6

func openUnpricedBudgetStore(t *testing.T, workerUSD float64) (*Store, *SessionStore, WorkerRecord, WorkerRunRecord) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, ws := NewSessionStore(db), NewWorkerStore(db)
	request := CreateWorkerRequest{Name: "Budget worker", Role: WorkerRoleCoder, Instructions: "Review", InitialLifecycleState: WorkerLifecycleStatePending}
	if workerUSD > 0 {
		request.Budget = &WorkerBudgetRequest{DailyUSD: workerUSD}
	}
	w, err := ws.CreateWorker("acct", "user", request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w, err = ws.AcceptWorker("acct", "user", w.ID, w.Revision, nil); err != nil {
		t.Fatal(err)
	}
	r, err := ws.AdmitWorkerRun("acct", WorkerRunAdmission{WorkerID: w.ID, WorkspaceID: "folder", UserID: "user", RequestSource: "direct", IdempotencyKey: "first", Input: map[string]any{"prompt": "Review"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(SessionSnapshot{ID: r.SessionID, AccountScopeID: "acct", UserID: "user", Metadata: map[string]any{"worker_id": w.ID, "worker_run_id": r.ID}}); err != nil {
		t.Fatal(err)
	}
	return db, s, w, r
}

func setAccountCap(t *testing.T, s *SessionStore, usd float64) {
	t.Helper()
	if err := s.PutUsageLimit(UsageLimitRecord{AccountScopeID: "acct", Enabled: true, DailyCostLimitUSD: usd}); err != nil {
		t.Fatal(err)
	}
}

// Purpose: an account daily cost cap must stay usable after unpriced receipts.
// Live regression: one unpriced transcription receipt made checkWorkerBudget
// refuse all work for the UTC day ("unresolved account pricing"). Requirement:
// PutTurnUsage records a conservative estimate on the unpriced receipt; the
// account checks (checkWorkerBudgetTotalLocked, checkWorkerSessionBudget)
// admit while known cost plus the estimate is below the cap and block at or
// over it, without a sticky day hold; replacing the receipt with a priced one
// removes exactly its estimate. Real temporary Pebble is the narrowest layer
// proving receipt, accumulator and admission together.
func TestUnpricedReceiptsAccountCap(t *testing.T) {
	_, s, w, r := openUnpricedBudgetStore(t, 0)
	setAccountCap(t, s, 100)
	if err := s.PutTurnUsage(SessionTurnUsageSnapshot{SessionID: "spent", AccountScopeID: "acct", UserID: "user", RunID: "spent", Provider: "fixture-provider", Model: "known-model", EstimatedCostUSD: 55.64, PriceStatus: "known", CostProvenance: "provider", InputTokens: 10, TotalTokens: 10}); err != nil {
		t.Fatal(err)
	}
	unpriced := SessionTurnUsageSnapshot{SessionID: "transcribe", AccountScopeID: "acct", UserID: "user", RunID: "turn", Provider: "unpriced-provider", Model: "fixture-model", InputTokens: 1000, OutputTokens: 1000, TotalTokens: 2000}
	// A caller cannot lower the estimate the store records.
	zero := 0.0
	unpriced.UnpricedEstimateUSD = &zero
	if err := s.PutTurnUsage(unpriced); err != nil {
		t.Fatal(err)
	}
	receipt, found, err := s.GetTurnUsage("transcribe", "turn")
	if err != nil || !found || receipt.PriceStatus != "unknown" || receipt.UnpricedEstimateUSD == nil || math.Abs(*receipt.UnpricedEstimateUSD-unpricedFixtureEstimate) > 1e-12 {
		t.Fatalf("receipt estimate: %+v %v", receipt, err)
	}
	count, estimate, err := s.GetTodayUnpricedUsage("acct")
	if err != nil || count != 1 || math.Abs(estimate-unpricedFixtureEstimate) > 1e-12 {
		t.Fatalf("today unpriced: %d %v %v", count, estimate, err)
	}

	// Far below the cap: admitted, and the estimate is reported.
	if err := s.CheckWorkerBudgetAdmission("acct", w.ID); err != nil {
		t.Fatalf("unpriced receipt blocked work far below the cap: %v", err)
	}
	if err := s.CheckWorkerSessionBudget("acct", r.SessionID, "claudecode", "fixture-model", "below"); err != nil {
		t.Fatalf("per-call admission blocked below the cap: %v", err)
	}
	if err := s.ReleaseWorkerBudgetReservation("acct", r.SessionID, "below"); err != nil {
		t.Fatal(err)
	}
	status, err := s.GetWorkerBudgetStatus("acct", w.ID)
	if err != nil || status.Blocked || status.AccountUsage.UnknownReceipts != 1 || math.Abs(status.AccountUnpricedEstimate-unpricedFixtureEstimate) > 1e-12 {
		t.Fatalf("status: %+v %v", status, err)
	}

	// Known cost plus estimate exactly at the cap, then over it: blocked.
	known, fixture := 55.64, unpricedFixtureEstimate
	for _, cap := range []float64{known + fixture, 55.65} {
		setAccountCap(t, s, cap)
		err := s.CheckWorkerBudgetAdmission("acct", w.ID)
		if !errors.Is(err, ErrWorkerBudget) || !strings.Contains(err.Error(), "1 unpriced receipts") {
			t.Fatalf("cap %v: known cost plus estimate admitted: %v", cap, err)
		}
		if err := s.CheckWorkerSessionBudget("acct", r.SessionID, "claudecode", "fixture-model", "at-cap"); !errors.Is(err, ErrWorkerBudget) {
			t.Fatalf("cap %v: per-call admission: %v", cap, err)
		}
	}
	var reservation workerBudgetReservation
	if found, err := s.store.GetJSON(budgetOperationKey(accountBudgetReservationKey("acct"), r.SessionID, "at-cap"), &reservation); err != nil || found {
		t.Fatalf("blocked call reserved budget: %v %v", found, err)
	}
	// The estimate is not a durable exhaustion: no day hold, so raising the
	// cap admits again.
	var hold WorkerBudgetHold
	if found, err := s.store.GetJSON(workerBudgetHoldKey("acct", w.ID, time.Now().UTC().Format("2006-01-02")), &hold); err != nil || found {
		t.Fatalf("estimate created a day hold: %+v %v", hold, err)
	}
	setAccountCap(t, s, 100)
	if err := s.CheckWorkerBudgetAdmission("acct", w.ID); err != nil {
		t.Fatalf("raised cap still blocked: %v", err)
	}

	// Pricing the same receipt later removes exactly its estimate.
	unpriced.UnpricedEstimateUSD = nil
	unpriced.PriceStatus, unpriced.EstimatedCostUSD, unpriced.CostProvenance = "known", 0.01, "provider"
	if err := s.PutTurnUsage(unpriced); err != nil {
		t.Fatal(err)
	}
	acc, _, err := s.GetDailyUsageAccumulator("acct", time.Now().UTC().Format("2006-01-02"))
	if err != nil || acc.UnknownReceipts != 0 || acc.UnpricedEstimateRecordedReceipts != 0 || acc.UnpricedEstimateRecordedUSD != 0 || acc.UnpricedEstimateUSD() != 0 || math.Abs(acc.TotalCostUSD-55.65) > 1e-9 {
		t.Fatalf("accumulator after pricing: %+v %v", acc, err)
	}
	if receipt, _, _ := s.GetTurnUsage("transcribe", "turn"); receipt.UnpricedEstimateUSD != nil {
		t.Fatalf("priced receipt kept an estimate: %+v", receipt)
	}
}

// Purpose: receipts written before estimates existed have no recorded
// estimate. They must still count (at the documented legacy ceiling), never
// as zero, so old unpriced days stay conservative. Accumulator arithmetic is
// the unit that owns this; no store is needed.
func TestUnpricedLegacyReceiptsCountAtCeiling(t *testing.T) {
	acc := DailyUsageAccumulator{UnknownReceipts: 3, UnpricedEstimateRecordedReceipts: 1, UnpricedEstimateRecordedUSD: 0.25}
	if got, want := acc.UnpricedEstimateUSD(), 0.25+2*unpricedLegacyReceiptCeilingUSD; math.Abs(got-want) > 1e-12 {
		t.Fatalf("estimate %v, want %v", got, want)
	}
	media := SessionMediaUsageRecord{Kind: "video", PriceStatus: "unknown"}
	stampMediaUnpricedEstimate(&media)
	if media.UnpricedEstimateUSD == nil || *media.UnpricedEstimateUSD != unpricedMediaReceiptCeilingUSD["video"] {
		t.Fatalf("media estimate: %+v", media)
	}
	media = SessionMediaUsageRecord{Kind: "image", PriceStatus: "known", CostUSD: 0.03}
	stampMediaUnpricedEstimate(&media)
	if media.UnpricedEstimateUSD != nil {
		t.Fatal("priced media got an estimate")
	}
}

// Purpose: the per-worker USD limit applies the same rule. An unpriced worker
// receipt counted through the worker's usage scope (applyScopeReceipt) must
// not block the worker while its estimate is below the worker limit, and must
// block once known cost plus estimate reaches it. Real worker lineage and
// receipts in temporary Pebble are the narrowest proof of scope attribution.
func TestUnpricedReceiptsWorkerCap(t *testing.T) {
	_, s, w, r := openUnpricedBudgetStore(t, 1)
	if err := s.PutTurnUsage(SessionTurnUsageSnapshot{SessionID: r.SessionID, AccountScopeID: "acct", UserID: "user", RunID: "turn", Provider: "unpriced-provider", Model: "fixture-model", InputTokens: 1000, OutputTokens: 1000, TotalTokens: 2000}); err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC().Format("2006-01-02")
	total, found, err := s.GetUsageScopeDay("acct", "worker", "", w.ID, day)
	if err != nil || !found || total.UnknownReceipts != 1 || math.Abs(total.UnpricedEstimateUSD()-unpricedFixtureEstimate) > 1e-12 {
		t.Fatalf("worker day: %+v %v", total, err)
	}
	if err := s.CheckWorkerBudgetAdmission("acct", w.ID); err != nil {
		t.Fatalf("worker blocked below its limit: %v", err)
	}
	if err := s.CheckWorkerSessionBudget("acct", r.SessionID, "claudecode", "fixture-model", "below"); err != nil {
		t.Fatalf("worker per-call blocked below its limit: %v", err)
	}
	if err := s.ReleaseWorkerBudgetReservation("acct", r.SessionID, "below"); err != nil {
		t.Fatal(err)
	}
	status, err := s.GetWorkerBudgetStatus("acct", w.ID)
	if err != nil || status.Blocked || math.Abs(status.UnpricedEstimateUSD-unpricedFixtureEstimate) > 1e-12 {
		t.Fatalf("status: %+v %v", status, err)
	}
	policy, err := s.GetWorkerBudget("acct", w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWorkerBudget("acct", w.ID, policy.Revision, unpricedFixtureEstimate, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerBudgetAdmission("acct", w.ID); !errors.Is(err, ErrWorkerBudget) || !strings.Contains(err.Error(), "unpriced") {
		t.Fatalf("estimate at the worker limit admitted: %v", err)
	}
	if err := s.CheckWorkerSessionBudget("acct", r.SessionID, "claudecode", "fixture-model", "at-cap"); !errors.Is(err, ErrWorkerBudget) {
		t.Fatalf("worker per-call at limit admitted: %v", err)
	}
	if status, err := s.GetWorkerBudgetStatus("acct", w.ID); err != nil || !status.Blocked {
		t.Fatalf("status at limit: %+v %v", status, err)
	}
}

// Purpose: model-family estimates must use the highest catalog rate of the
// family, not a cheaper sibling and not an unrelated family, so the estimate
// stays conservative without stopping work on outlier rates.
func TestUnpricedTurnEstimateUsesFamilyRates(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "rates.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	price := func(in, out float64) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"input_price_per_million_tokens": in, "output_price_per_million_tokens": out})
		return raw
	}
	if err := NewModelCatalogStore(db).SetRecords([]ModelCatalogRecord{
		{Provider: "acme", Model: "swift-1-mini", Pricing: price(1, 2)},
		{Provider: "acme", Model: "swift-2-max", Pricing: price(3, 9)},
		{Provider: "acme", Model: "image-pro", Pricing: price(40, 200)},
	}); err != nil {
		t.Fatal(err)
	}
	got := s.unpricedTurnEstimateUSD(SessionTurnUsageSnapshot{Provider: "acme", Model: "swift-3", InputTokens: 1_000_000, OutputTokens: 1_000_000})
	if math.Abs(got-12) > 1e-9 {
		t.Fatalf("family estimate %v, want 12", got)
	}
	got = s.unpricedTurnEstimateUSD(SessionTurnUsageSnapshot{Provider: "acme", Model: "other", InputTokens: 1_000_000, OutputTokens: 1_000_000})
	if math.Abs(got-240) > 1e-9 {
		t.Fatalf("provider estimate %v, want 240", got)
	}
}

// Purpose: a TTS timing transcription is a generateContent call with audio
// input. When the catalog's speech rows do not resolve (gemini-3.8-flash lists
// only an introductory variant), EstimateMediaCostWithOptions must price it at
// the model's token rates so its receipt is not unknown; audio input uses the
// highest standard-tier audio_input row, never a batch row; missing audio
// usage stays unknown.
func TestTranscriptionReceiptUsesModelTokenPricing(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "speech.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	pricing := json.RawMessage(`{"currency":"USD","input_price_per_million_tokens":0.75,"output_price_per_million_tokens":3.75,"billing":{"status":"verified","lines":[
		{"billable":"text_input","unit":"million_tokens","price_usd":0.75,"variant":"standard_introductory_through_2026_12_31","conditions":{"tier":"paid","service_tier":"standard"}},
		{"billable":"audio_input","unit":"million_tokens","price_usd":1.5,"variant":"standard_introductory_through_2026_12_31","conditions":{"tier":"paid","service_tier":"standard"}},
		{"billable":"audio_input","unit":"million_tokens","price_usd":9,"variant":"priority","conditions":{"tier":"paid","service_tier":"priority"}},
		{"billable":"text_output","unit":"million_tokens","price_usd":3.75,"variant":"standard_introductory_through_2026_12_31","conditions":{"tier":"paid","service_tier":"standard"}}]}}`)
	if err := NewModelCatalogStore(db).SetRecord(ModelCatalogRecord{Provider: "google", Model: "gemini-3.8-flash", Pricing: pricing, SourceSnapshotID: "snap"}); err != nil {
		t.Fatal(err)
	}
	opts := MediaCostEstimateOptions{Provider: "google", Model: "gemini-3.8-flash", Kind: "audio", Count: 1, Transcription: true, PromptTokens: 100, AudioInputTokens: 1000, TextOutputTokens: 1000}
	got := s.EstimateMediaCostWithOptions(opts)
	want := (100*0.75 + 1000*1.5 + 1000*3.75) / 1e6
	if got.PriceStatus != "known" || math.Abs(got.CostUSD-want) > 1e-12 {
		t.Fatalf("transcription estimate: %+v, want %v", got, want)
	}
	opts.AudioInputTokens = 0
	if got := s.EstimateMediaCostWithOptions(opts); got.PriceStatus != "unknown" || got.CostUSD != 0 {
		t.Fatalf("missing audio usage priced: %+v", got)
	}
	// Speech synthesis is not a token call; it keeps its own rules.
	if got := s.EstimateMediaCostWithOptions(MediaCostEstimateOptions{Provider: "google", Model: "gemini-3.8-flash", Kind: "audio", Speech: true, PromptTokens: 100, AudioOutputTokens: 100}); got.PriceStatus != "unknown" {
		t.Fatalf("speech priced from token rates: %+v", got)
	}
}
