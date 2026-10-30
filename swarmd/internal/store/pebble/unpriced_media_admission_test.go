package pebblestore

import (
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

// unpricedAdmissionFixture is a real worker-run session in a temporary store.
func unpricedAdmissionFixture(t *testing.T, budget *WorkerBudgetRequest) (*Store, *SessionStore, string, string) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "budget"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, ws := NewSessionStore(db), NewWorkerStore(db)
	w, err := ws.CreateWorker("acct", "user", CreateWorkerRequest{Name: "worker", Role: WorkerRoleCoder, Instructions: "Narrate", InitialLifecycleState: WorkerLifecycleStatePending, Budget: budget}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w, err = ws.AcceptWorker("acct", "user", w.ID, w.Revision, nil); err != nil {
		t.Fatal(err)
	}
	r, err := ws.AdmitWorkerRun("acct", WorkerRunAdmission{WorkerID: w.ID, WorkspaceID: "folder", UserID: "user", RequestSource: "direct", IdempotencyKey: "first", Input: map[string]any{"prompt": "Narrate"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(SessionSnapshot{ID: r.SessionID, AccountScopeID: "acct", UserID: "user", Metadata: map[string]any{"worker_id": w.ID, "worker_run_id": r.ID}}); err != nil {
		t.Fatal(err)
	}
	return db, s, w.ID, r.SessionID
}

// Purpose: under an account daily cost cap, a worker media request whose price
// is unresolved must be admitted and reserved at the unpriced receipt ceiling
// of its kind (per requested output), and refused only when known cost plus
// reservations plus that ceiling would cross the cap. Model turns with unknown
// pricing stay refused. The threat is a cap that either blocks all unpriced
// media (the reported speech outage) or treats it as free. CheckWorkerMediaAdmission
// / checkWorkerSessionBudget own this; real temporary Pebble is the narrowest
// layer that proves the durable reservation records and their absence on refusal.
func TestWorkerUnpricedMediaAdmissionAccountCap(t *testing.T) {
	db, s, _, session := unpricedAdmissionFixture(t, nil)
	if err := s.PutUsageLimit(UsageLimitRecord{AccountScopeID: "acct", Enabled: true, DailyCostLimitUSD: 1.5}); err != nil {
		t.Fatal(err)
	}
	accountKey := accountBudgetReservationKey("acct")
	reserved := func() float64 {
		t.Helper()
		cost, err := s.openWorkerBudgetCost(accountKey)
		if err != nil {
			t.Fatal(err)
		}
		return cost
	}
	if err := s.CheckWorkerMediaAdmission("acct", session, MediaCostEstimate{PriceStatus: "unknown", AdmissionKind: "audio"}, "audio-1"); err != nil {
		t.Fatalf("unpriced audio refused under cap: %v", err)
	}
	if got := reserved(); math.Abs(got-unpricedMediaReceiptCeilingUSD["audio"]) > 1e-12 {
		t.Fatalf("audio reservation = %v, want audio ceiling", got)
	}
	// $1.00 reserved + $10.00 video ceiling crosses the $1.50 cap.
	err := s.CheckWorkerMediaAdmission("acct", session, MediaCostEstimate{PriceStatus: "unknown", AdmissionKind: "video"}, "video-1")
	if !errors.Is(err, ErrWorkerBudget) || !strings.Contains(err.Error(), "daily account usage limit exceeded") || !strings.Contains(err.Error(), "$10.00") {
		t.Fatalf("video ceiling crossing the cap: %v", err)
	}
	// Two images count twice: $1.00 + 2 x $0.50 crosses $1.50.
	if err := s.CheckWorkerMediaAdmission("acct", session, MediaCostEstimate{PriceStatus: "unknown", AdmissionKind: "image", AdmissionCount: 2}, "images-2"); !errors.Is(err, ErrWorkerBudget) {
		t.Fatalf("image batch ceiling crossing the cap: %v", err)
	}
	for _, refused := range []string{"video-1", "images-2"} {
		var reservation workerBudgetReservation
		if found, err := db.GetJSON(budgetOperationKey(accountKey, session, refused), &reservation); err != nil || found {
			t.Fatalf("refused %s persisted a reservation: %v %v", refused, found, err)
		}
	}
	if got := reserved(); math.Abs(got-1) > 1e-12 {
		t.Fatalf("refusals changed reservations: %v", got)
	}
	// One image fits exactly: $1.00 + $0.50 = $1.50 is not over the cap.
	if err := s.CheckWorkerMediaAdmission("acct", session, MediaCostEstimate{PriceStatus: "unknown", AdmissionKind: "image"}, "image-1"); err != nil {
		t.Fatalf("image at the cap refused: %v", err)
	}
	if got := reserved(); math.Abs(got-1.5) > 1e-12 {
		t.Fatalf("image reservation total = %v, want 1.5", got)
	}
	// An unrecognised kind uses the other-kind ceiling, never less.
	if got := unpricedMediaAdmissionUSD(MediaCostEstimate{PriceStatus: "unknown", AdmissionKind: "music"}); got != unpricedMediaOtherKindCeilingUSD {
		t.Fatalf("other-kind ceiling = %v", got)
	}
	if err := s.ReleaseWorkerBudgetReservation("acct", session, "audio-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseWorkerBudgetReservation("acct", session, "image-1"); err != nil {
		t.Fatal(err)
	}
	// Model turns are unchanged: unknown pricing under an account cap refuses.
	if err := s.CheckWorkerSessionBudgetWithPrice("acct", session, "unknown", "turn"); !errors.Is(err, ErrWorkerBudget) || !strings.Contains(err.Error(), "pricing is unknown") {
		t.Fatalf("unknown model turn admitted: %v", err)
	}
	if got := reserved(); got != 0 {
		t.Fatalf("refused model turn reserved %v", got)
	}
}

// Purpose: a worker's own daily USD cap applies the same unpriced media
// ceiling as the account cap (admit at the ceiling, refuse when it would cross),
// so the worker branch of checkWorkerSessionBudget cannot regress to refusing
// all unpriced media or to counting it as free. Real temporary Pebble proves
// the worker reservation record.
func TestWorkerUnpricedMediaAdmissionWorkerCap(t *testing.T) {
	_, s, workerID, session := unpricedAdmissionFixture(t, &WorkerBudgetRequest{DailyUSD: 1})
	if err := s.CheckWorkerMediaAdmission("acct", session, MediaCostEstimate{PriceStatus: "unknown", AdmissionKind: "image", AdmissionCount: 2}, "images"); err != nil {
		t.Fatalf("unpriced images refused under worker cap: %v", err)
	}
	cost, err := s.openWorkerBudgetCost(workerBudgetKey("acct", workerID) + "/reservation")
	if err != nil || math.Abs(cost-1) > 1e-12 {
		t.Fatalf("worker reservation = %v %v, want 1", cost, err)
	}
	if err := s.CheckWorkerMediaAdmission("acct", session, MediaCostEstimate{PriceStatus: "unknown", AdmissionKind: "image"}, "third"); !errors.Is(err, ErrWorkerBudget) || !strings.Contains(err.Error(), "daily worker usage limit exceeded") {
		t.Fatalf("ceiling over the worker cap admitted: %v", err)
	}
	if err := s.CheckWorkerSessionBudgetWithPrice("acct", session, "unknown", "turn"); !errors.Is(err, ErrWorkerBudget) {
		t.Fatalf("unknown model turn admitted under worker cap: %v", err)
	}
}

// Purpose: speech admission bounds must be upper bounds computed from the
// request alone (ceil(bytes/3)+64 prompt tokens over script, style and
// language; ceil(script bytes x 25/10) audio tokens), and must resolve the
// shipped token-priced Gemini TTS catalog rows to a known estimate. Without
// them the estimate is unknown and capped workers cannot speak. Pure functions
// over the checked-in catalog rows are the narrowest layer.
func TestSpeechAdmissionTokenBounds(t *testing.T) {
	prompt, audio := SpeechAdmissionTokenBounds("Hello", "", "")
	if prompt != 2+64 || audio != 13 {
		t.Fatalf("bounds = %d %d", prompt, audio)
	}
	prompt, audio = SpeechAdmissionTokenBounds(strings.Repeat("a", 1200), "Calm", "English")
	if prompt != 404+64 || audio != 3000 {
		t.Fatalf("bounds = %d %d", prompt, audio)
	}
	// Multi-byte scripts count bytes, never fewer tokens than characters / 3.
	prompt, audio = SpeechAdmissionTokenBounds("日本語", "", "")
	if prompt != 3+64 || audio != 23 {
		t.Fatalf("multibyte bounds = %d %d", prompt, audio)
	}
	rec := ModelCatalogRecord{Provider: "google", Model: "gemini-2.5-flash-preview-tts", Pricing: []byte(`{"billing":{"status":"verified","lines":[{"billable":"text_input","unit":"million_tokens","price_usd":0.5,"variant":"standard","conditions":{"tier":"paid","service_tier":"standard"}},{"billable":"audio_output","unit":"million_tokens","price_usd":10,"variant":"standard","conditions":{"tier":"paid","service_tier":"standard"}}]}}`)}
	prompt, audio = SpeechAdmissionTokenBounds(strings.Repeat("a", 1200), "Calm", "English")
	got := EstimateMediaCostFromRecord(rec, MediaCostEstimateOptions{Kind: "audio", Speech: true, InputCharacters: 1200, PromptTokens: prompt, AudioOutputTokens: audio})
	want := (468*0.5 + 3000*10.0) / 1e6
	if got.PriceStatus != "known" || math.Abs(got.CostUSD-want) > 1e-12 {
		t.Fatalf("speech estimate = %+v, want known %v", got, want)
	}
	if got := EstimateMediaCostFromRecord(rec, MediaCostEstimateOptions{Kind: "audio", Speech: true, InputCharacters: 1200}); got.PriceStatus != "unknown" {
		t.Fatalf("estimate without bounds = %+v (regression premise changed)", got)
	}
}
