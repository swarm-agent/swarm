package api

import (
	"encoding/json"
	"math"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
	"time"
)

// Purpose: recordTaskMedia is the direct generation path's billing boundary.
// A generated file must produce one durable account receipt despite retries;
// another principal must not charge that task. A real temporary Pebble store
// is the narrowest layer proving both receipt and rollup atomic postconditions.
func TestMediaCostTaskReceipt(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	task := &pebblestore.ProjectTaskRecord{ID: "image-task", ProjectID: "project", AccountID: "account", Title: "image", Agent: "image"}
	if err := store.PutProjectTask("account", task); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"billing":{"status":"verified","lines":[{"kind":"billing_rate","billable":"image_output","unit":"million_tokens","price_usd":30,"variant":"standard","conditions":{"tier":"paid","service_tier":"standard"}}]}}`)
	if err := pebblestore.NewModelCatalogStore(db).SetRecord(pebblestore.ModelCatalogRecord{Provider: "google", Model: "gemini-nano-banana-2.1", Pricing: raw}); err != nil {
		t.Fatal(err)
	}
	server := &Server{sessions: session.NewService(store, nil)}
	p := identity.Principal{AccountScopeID: "account", UserID: "user"}
	opts := pebblestore.MediaCostEstimateOptions{Provider: "google", Model: "gemini-nano-banana-2.1", Kind: "image", Count: 1, OutputTokens: 1120}
	for i := 0; i < 2; i++ {
		estimate, err := server.recordTaskMedia(p, task, 0, opts, "image/png", 100)
		if err != nil || math.Abs(estimate.CostUSD-.0336) > 1e-10 {
			t.Fatalf("estimate %+v error %v", estimate, err)
		}
	}
	receipts, err := store.ListMediaUsage("account", 10)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("receipts %+v err %v", receipts, err)
	}
	acc, found, err := store.GetDailyUsageAccumulator("account", time.Now().UTC().Format("2006-01-02"))
	if err != nil || !found || acc.ImageCount != 1 || math.Abs(acc.MediaCostUSD-.0336) > 1e-10 {
		t.Fatalf("acc %+v found %v err %v", acc, found, err)
	}
	p.AccountScopeID = "other"
	if _, err := server.recordTaskMedia(p, task, 0, opts, "image/png", 100); err == nil {
		t.Fatal("cross-account task accepted")
	}
	receipts, err = store.ListMediaUsage("other", 10)
	if err != nil || len(receipts) != 0 {
		t.Fatalf("unauthorized receipt %+v %v", receipts, err)
	}
}

// Purpose: MCP must expose compact prices even when model settings are absent;
// the view boundary proves the response contract without transport fixtures.
func TestMediaCostModelView(t *testing.T) {
	view := controlMCPMediaModelView(mediaCatalogOption{ID: "image", Pricing: json.RawMessage(`{"billing":{"status":"verified","lines":[{"billable":"image_output","unit":"image","variant":"4K","price_usd":0.113},{"billable":"image_output","unit":"million_tokens","price_usd":30,"conditions":{"service_tier":"standard"}},{"billable":"image_output","unit":"image","variant":"batch_4K","price_usd":0.01}]}}`)})
	pricing := view["pricing"].(map[string]any)
	if pricing["billing_status"] != "verified" || len(pricing["prices"].([]map[string]any)) != 2 {
		t.Fatalf("%+v", view)
	}
	file := controlMCPMediaFile{record: pebblestore.ProjectTaskDeliverable{MediaCostEstimate: pebblestore.MediaCostEstimate{CostUSD: .0336, PriceStatus: "known", PricingSummary: "catalog"}}}
	result := file.view(1)
	if result["cost_usd"] != .0336 || result["price_status"] != "known" || result["pricing_summary"] != "catalog" {
		t.Fatalf("%+v", result)
	}
}
