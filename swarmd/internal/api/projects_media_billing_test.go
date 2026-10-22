package api

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"math"
	"strings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegen"
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

type billingGeminiClient struct {
	calls int
	png   []byte
}

func (c *billingGeminiClient) GenerateImage(context.Context, imagegen.GeminiImageGenerationRequest) (imagegen.GeminiImageGenerationResult, error) {
	c.calls++
	return imagegen.GeminiImageGenerationResult{Images: []imagegen.GeminiGeneratedImage{{DecodedPNG: c.png, MIMEType: "image/png"}}, Usage: map[string]any{"candidatesTokensDetails": []any{map[string]any{"modality": "IMAGE", "tokenCount": 1120}}}}, nil
}

// Purpose: executeDirectMediaTask must carry Gemini usage into a single durable
// receipt and retain paid output on billing failure. A deterministic provider
// adapter plus real services/store proves orchestration, not live generation.
func TestMediaCostDirectImageTask(t *testing.T) {
	for _, mode := range []string{"origin", "principal", "missing-session"} {
		t.Run(mode, func(t *testing.T) {
			db, err := pebblestore.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := pebblestore.NewSessionStore(db)
			p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
			if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "session", Title: "media"}, p.UserID, p.AccountScopeID); err != nil {
				t.Fatal(err)
			}
			catalog := pebblestore.NewModelCatalogStore(db)
			var rec pebblestore.ModelCatalogRecord
			if err := json.Unmarshal([]byte(`{"provider":"google","model":"gemini-nano-banana-2.1","catalog_modalities":{"outputs":["image"]},"media":{"state":"supported","provider_surface":"gemini_generate_content"},"provider_specific":{"google":{"model_api_surface":"generate_content","image_generation":{"api_surface":"generate_content","status":"verified","settings":{"aspect_ratio":{"status":"verified","supported_values":["1:1"]},"image_size":{"status":"verified","supported_values":["1K"]}}}}},"pricing":{"billing":{"status":"verified","lines":[{"kind":"billing_rate","billable":"image_output","unit":"million_tokens","price_usd":30,"conditions":{"tier":"paid","service_tier":"standard"}}]}}}`), &rec); err != nil {
				t.Fatal(err)
			}
			if err := catalog.SetRecord(rec); err != nil {
				t.Fatal(err)
			}
			auth := pebblestore.NewAuthStore(db)
			if _, err := auth.UpsertCredential(pebblestore.AuthCredentialInput{Provider: "google", AccountScopeID: p.AccountScopeID, ID: "fixture", Type: "api_key", APIKey: "test-only", SetActive: true}); err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
				t.Fatal(err)
			}
			client := &billingGeminiClient{png: buf.Bytes()}
			gen := imagegen.NewService(nil, auth, nil, billingCatalog{rec})
			gen.SetGeminiImageClient(client)
			server := &Server{sessions: session.NewService(store, nil), imageGen: gen}
			task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: p.AccountScopeID, Title: "image", Agent: "image", Model: rec.Model, Resolution: "1K", AspectRatio: "1:1", VariantCount: 1, Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "file", Status: "pending"}}}
			if mode == "principal" {
				p.SessionID = "session"
			} else {
				task.OriginSessionID = "session"
			}
			if mode == "missing-session" {
				task.OriginSessionID = "missing"
			}
			if err := store.PutProjectTask(p.AccountScopeID, task); err != nil {
				t.Fatal(err)
			}
			server.executeDirectMediaTask(p, &pebblestore.ProjectRecord{ID: "project"}, task)
			saved, _, err := store.GetProjectTask(p.AccountScopeID, task.ProjectID, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Deliverables[0].Status != "ready" || saved.Deliverables[0].MediaURL == "" {
				t.Fatalf("output lost: %+v", saved)
			}
			if math.Abs(saved.Deliverables[0].CostUSD-.0336) > 1e-10 {
				t.Fatalf("cost %+v", saved.Deliverables[0])
			}
			server.executeDirectMediaTask(p, &pebblestore.ProjectRecord{ID: "project"}, saved)
			if client.calls != 1 {
				t.Fatalf("regenerated %d times", client.calls)
			}
			receipts, err := store.ListMediaUsage(p.AccountScopeID, 10)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "missing-session" {
				if len(receipts) != 0 || !strings.Contains(saved.Deliverables[0].PricingSummary, "Billing not recorded") {
					t.Fatalf("failure invisible: %+v", saved.Deliverables[0])
				}
			} else if len(receipts) != 1 || receipts[0].SessionID != "session" {
				t.Fatalf("receipts %+v", receipts)
			}
		})
	}
}

type billingCatalog struct {
	record pebblestore.ModelCatalogRecord
}

func (c billingCatalog) ListCatalog(string, int) ([]pebblestore.ModelCatalogRecord, error) {
	return []pebblestore.ModelCatalogRecord{c.record}, nil
}
