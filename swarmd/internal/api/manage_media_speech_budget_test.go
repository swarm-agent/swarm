package api

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/audiogen"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegen"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/store/pebble/pricingtest"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/uisettings"
)

// Purpose: a worker under an account daily cost cap ($100) must be able to
// admit Gemini TTS speech priced per token in the shipped catalog. The
// regression was estimateManagedMedia passing no token counts, so the estimate
// was unknown and checkWorkerSessionBudget refused it before a job existed.
// The real estimateManagedMedia -> CheckWorkerMediaAdmission path over a
// temporary store and the checked-in catalog rows is the narrowest layer that
// proves the estimate is known, admitted, and reserved at exactly the estimate.
func TestSpeechAdmissionUnderAccountCap(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "session"}, p.UserID, p.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(pebblestore.KeyWorker("account", "worker"), pebblestore.WorkerRecord{ID: "worker", AccountScopeID: "account"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON("usage_scope_binding/account/session", []pebblestore.UsageScopeTotal{{Kind: "worker", ID: "worker"}, {Kind: "worker_run", ProjectID: "worker", ID: "run"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutUsageLimit(pebblestore.UsageLimitRecord{AccountScopeID: p.AccountScopeID, Enabled: true, DailyCostLimitUSD: 100}); err != nil {
		t.Fatal(err)
	}
	catalog := pebblestore.NewModelCatalogStore(db)
	rates := map[string][2]float64{"gemini-2.5-flash-preview-tts": {0.5, 10}, "gemini-2.5-pro-preview-tts": {1, 20}}
	var transcription pebblestore.ModelCatalogRecord
	for model := range rates {
		pricing, specific := pricingtest.Record(t, model)
		rec := pebblestore.ModelCatalogRecord{Provider: "google", Model: model, Pricing: pricing, ProviderSpecific: specific}
		rec.CatalogModalities.Outputs = []string{"audio"}
		if err := catalog.SetRecord(rec); err != nil {
			t.Fatal(err)
		}
		transcription = rec
	}
	settings := uisettings.NewService(pebblestore.NewUISettingsStore(db))
	server := &Server{
		sessions: session.NewService(store, nil),
		imageGen: imagegen.NewService(nil, pebblestore.NewAuthStore(db), nil, billingCatalog{}),
		audioGen: audiogen.NewService(nil, settings, billingCatalog{transcription}),
		model:    modelruntime.NewService(pebblestore.NewModelStore(db), nil, modelruntime.NewCatalogService(catalog)),
	}
	text := strings.Repeat("Calm narration. ", 75) // 1200 bytes
	reservedUSD := func() float64 {
		t.Helper()
		total := 0.0
		if err := db.IteratePrefix("worker_budget_account/account/reservation/", 0, func(_ string, value []byte) error {
			var reservation struct {
				CostUSD float64 `json:"cost_usd"`
			}
			if err := json.Unmarshal(value, &reservation); err != nil {
				return err
			}
			total += reservation.CostUSD
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return total
	}
	for model, rate := range rates {
		input := &tool.ProjectTaskCreateInput{Agent: "sound", Operation: "speech", Model: model, Speech: &pebblestore.ProjectSpeechSettings{Text: text, Voice: "Kore", Style: "Calm", Language: "English"}}
		estimate, err := server.estimateManagedMedia(context.Background(), p, input)
		// Upper bounds: ceil((1200+4+7)/3)+64 = 468 prompt tokens and
		// ceil(1200*25/10) = 3000 audio tokens at the standard paid rates.
		want := (468*rate[0] + 3000*rate[1]) / 1e6
		if err != nil || estimate.PriceStatus != "known" || math.Abs(estimate.CostUSD-want) > 1e-12 {
			t.Fatalf("%s estimate = %+v %v, want known %v", model, estimate, err, want)
		}
		before := reservedUSD()
		if err := store.CheckWorkerMediaAdmission(p.AccountScopeID, "session", estimate, "speech-"+model); err != nil {
			t.Fatalf("%s refused under $100 account cap: %v", model, err)
		}
		if got := reservedUSD() - before; math.Abs(got-estimate.CostUSD) > 1e-12 {
			t.Fatalf("%s reservation = %v, want estimate %v", model, got, estimate.CostUSD)
		}
	}
}
