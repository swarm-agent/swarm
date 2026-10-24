package videogen

import (
	"context"
	"math"
	"strings"
	modelruntime "swarm/packages/swarmd/internal/model"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/store/pebble/pricingtest"
	"testing"
)

// Purpose: admission must reserve positive catalog-priced cost for variable
// duration Omni clips without sending a duration to the provider. The pricing
// production PreflightVideoOperation path and real budget store prove denial
// leaves receipts unchanged using the shipped snapshot's actual pricing lines.
func TestOmniAdmissionDuration(t *testing.T) {
	for _, tc := range []struct {
		opts *ParsedVideoOptions
		want int
	}{
		{nil, 10}, {&ParsedVideoOptions{DefaultDur: 5, Durations: []int{8}}, 5}, {&ParsedVideoOptions{Durations: []int{4, 8}}, 8},
	} {
		if got := AdmissionDurationSeconds("gemini-omni-1.1-flash", 0, tc.opts); got != tc.want {
			t.Fatalf("%d != %d", got, tc.want)
		}
	}
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "session"}, "user", "account"); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(pebblestore.KeyWorker("account", "worker"), pebblestore.WorkerRecord{ID: "worker", AccountScopeID: "account"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON("usage_scope_binding/account/session", []pebblestore.UsageScopeTotal{{Kind: "worker", ID: "worker"}, {Kind: "worker_run", ProjectID: "worker", ID: "run"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetWorkerBudget("account", "worker", 0, .5, 0); err != nil {
		t.Fatal(err)
	}
	pricing, specific := pricingtest.Record(t, "gemini-omni-1.1-flash")
	rec := pebblestore.ModelCatalogRecord{Provider: "google", Model: "gemini-omni-1.1-flash", Pricing: pricing, ProviderSpecific: specific}
	rec.CatalogModalities.Outputs = []string{"video"}
	catalog := pebblestore.NewModelCatalogStore(db)
	if err := catalog.SetRecord(rec); err != nil {
		t.Fatal(err)
	}
	models := modelruntime.NewService(pebblestore.NewModelStore(db), nil, modelruntime.NewCatalogService(catalog))
	preflight, err := NewService(nil, nil, models).PreflightVideoOperation(context.Background(), VideoPreflightRequest{AccountScopeID: "account", ExplicitModel: rec.Model})
	if err != nil {
		t.Fatal(err)
	}
	if preflight.DurationSeconds != 0 {
		t.Fatal("admission duration leaked into provider options")
	}
	estimate := pebblestore.MediaCostEstimate{CostUSD: preflight.EstimatedCostUSD, PriceStatus: preflight.PriceStatus}
	if estimate.CostUSD != 1 || estimate.PriceStatus != "known" {
		t.Fatalf("%+v", estimate)
	}
	if err := store.CheckWorkerMediaAdmission("account", "session", estimate); err == nil {
		t.Fatal("budget admitted oversized estimate")
	}
	receipts, err := store.ListMediaUsage("account", 10)
	if err != nil || len(receipts) != 0 {
		t.Fatalf("%+v %v", receipts, err)
	}
}

// Purpose: Omni extension admission and BillingOptions must both exclude the
// source. These pure pricing boundaries prove the reserve equals the maximum
// generated delta including tolerance, preventing combined-timeline charging.
func TestOmniExtensionPricing(t *testing.T) {
	pricing, _ := pricingtest.Record(t, "gemini-omni-1.1-flash")
	rec := pebblestore.ModelCatalogRecord{Pricing: pricing}
	output := ManagedVideoResult{Model: "gemini-omni-1.1-flash", Operation: pebblestore.VideoOperationExtend, MeasuredDurationSeconds: 35, SourceDurationSeconds: 30, Width: 1280, Height: 720}
	actual := pebblestore.EstimateMediaCostFromRecord(rec, output.BillingOptions())
	if actual.PriceStatus != "known" || actual.CostUSD != .5 || !strings.Contains(actual.PricingSummary, "excludes source video") {
		t.Fatalf("%+v", actual)
	}
	for _, requested := range []int{0, 5} {
		duration := AdmissionOperationDurationSeconds(output.Model, output.Operation, requested, &ParsedVideoOptions{DefaultDur: 5})
		estimate := pebblestore.EstimateMediaCostFromRecord(rec, pebblestore.MediaCostEstimateOptions{Kind: "video", Resolution: "720p", MeasuredDurationSeconds: duration})
		if estimate.PriceStatus != "known" || math.Abs(estimate.CostUSD-1.005) > 1e-10 || estimate.CostUSD < actual.CostUSD {
			t.Fatalf("%+v", estimate)
		}
		output.MeasuredDurationSeconds = output.SourceDurationSeconds + duration
		receipt := pebblestore.EstimateMediaCostFromRecord(rec, output.BillingOptions())
		if receipt.PriceStatus != "known" || math.Abs(receipt.CostUSD-estimate.CostUSD) > 1e-10 {
			t.Fatalf("generated-only basis differs: estimate=%+v receipt=%+v", estimate, receipt)
		}
	}
}

// Purpose: BillingOptions must never charge Veo's source timeline. This pricing
// boundary proves the locked eight-second charge even for a 141-second source,
// with admission matching the charge rather than the combined file duration.
func TestVeoExtensionPricing(t *testing.T) {
	pricing, _ := pricingtest.Record(t, "veo-3.1-generate-preview")
	rec := pebblestore.ModelCatalogRecord{Pricing: pricing}
	for _, total := range []float64{148, 149} {
		result := ManagedVideoResult{Model: "veo-3.1-generate-preview", Operation: pebblestore.VideoOperationExtend, SourceDurationSeconds: 141, MeasuredDurationSeconds: total, DurationSeconds: int(total), Width: 1280, Height: 720}
		got := pebblestore.EstimateMediaCostFromRecord(rec, result.BillingOptions())
		if got.PriceStatus != "known" || got.CostUSD != 3.2 || !strings.Contains(got.PricingSummary, "excludes source video") {
			t.Fatalf("%+v", got)
		}
		if duration := AdmissionOperationDurationSeconds(result.Model, result.Operation, 0, nil); duration != 8 {
			t.Fatalf("reserve %g", duration)
		}
	}
}

// Purpose: missing source metadata must not rebill the combined Omni timeline.
// BillingOptions is the narrowest fail-closed boundary before receipt pricing.
func TestOmniExtensionMissingSource(t *testing.T) {
	pricing, _ := pricingtest.Record(t, "gemini-omni-1.1-flash")
	result := ManagedVideoResult{Model: "gemini-omni-1.1-flash", Operation: pebblestore.VideoOperationExtend, MeasuredDurationSeconds: 35, DurationSeconds: 35, Resolution: "720p"}
	got := pebblestore.EstimateMediaCostFromRecord(pebblestore.ModelCatalogRecord{Pricing: pricing}, result.BillingOptions())
	if got.PriceStatus != "unknown" || got.CostUSD != 0 {
		t.Fatalf("%+v", got)
	}
}

// Purpose: BillingOptions must preserve requested Veo seconds despite container
// timing drift. The receipt pricing boundary with the shipped catalog proves
// eight requested seconds cost $3.20, not the measured 8.02-second amount.
func TestVeoRequestedReceiptDuration(t *testing.T) {
	pricing, _ := pricingtest.Record(t, "veo-3.1-generate-preview")
	result := ManagedVideoResult{Model: "veo-3.1-generate-preview", DurationSeconds: 8, MeasuredDurationSeconds: 8.02, Width: 1280, Height: 720}
	got := pebblestore.EstimateMediaCostFromRecord(pebblestore.ModelCatalogRecord{Pricing: pricing}, result.BillingOptions())
	if got.PriceStatus != "known" || got.CostUSD != 3.2 {
		t.Fatalf("%+v", got)
	}
}
