package api

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegen"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/store/pebble/pricingtest"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/videogen"
	"testing"
	"time"
)

// Purpose: recordTaskMedia is the direct generation path's billing boundary.
// A generated file must produce one durable account receipt despite retries;
// another principal must not charge that task. A real temporary Pebble store
// is the narrowest layer proving both receipt and rollup atomic postconditions.
// The usage handler and controlMCPGetUsage must expose this account-only charge
// without inventing a conversation in either per-session list.
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
	request := httptest.NewRequest(http.MethodGet, "/v3/usage?time_range=today", nil)
	p.Type = identity.PrincipalTypeUser
	request = request.WithContext(identity.ContextWithPrincipal(request.Context(), p))
	request = requestWithScopedToken(request, &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read"}})
	call := &controlMCPCall{request: request, server: server, next: http.HandlerFunc(server.handleSessionsV3Usage)}
	dashboard, err := call.dispatch(http.MethodGet, "/v3/usage", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := controlMCPGetUsage(call, map[string]any{"detail": true})
	if err != nil {
		t.Fatal(err)
	}
	for _, response := range []map[string]any{dashboard, result.(map[string]any)} {
		media := response["media"].(map[string]any)
		if media["image_count"] != float64(1) || math.Abs(media["image_cost_usd"].(float64)-.0336) > 1e-10 {
			t.Fatalf("media %+v", media)
		}
		if len(controlMCPList(response["recent_sessions"])) != 0 || len(controlMCPList(response["sessions"])) != 0 {
			t.Fatalf("unexpected sessions %+v", response)
		}
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
// account receipt even with a login session or missing origin, without retry
// double billing or regeneration. A deterministic provider
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
				p.SessionID = "sid_0123456789abcdef"
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
			wantSession := ""
			if mode == "origin" {
				wantSession = "session"
			}
			if len(receipts) != 1 || receipts[0].SessionID != wantSession || math.Abs(receipts[0].CostUSD-.0336) > 1e-10 {
				t.Fatalf("receipts %+v", receipts)
			}
			if strings.Contains(saved.Deliverables[0].PricingSummary, "Billing not recorded") {
				t.Fatal("receipt incorrectly reported missing")
			}
			acc, found, err := store.GetDailyUsageAccumulator(p.AccountScopeID, time.Now().UTC().Format("2006-01-02"))
			if err != nil || !found || acc.ImageCount != 1 || math.Abs(acc.MediaCostUSD-.0336) > 1e-10 {
				t.Fatalf("account usage %+v found %v err %v", acc, found, err)
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

// Purpose: generateProjectVideoStory must bill paid clips before assembly can
// reject them. Real receipt persistence and assembly with an injected failing
// probe prove failure retains both charges without an external provider.
func TestMediaUsageStoryAssemblyFailure(t *testing.T) {
	// Inject a failing local probe, not a live encoding dependency.
	bin := t.TempDir()
	for _, binary := range []string{"ffmpeg", "ffprobe"} {
		if err := os.WriteFile(filepath.Join(bin, binary), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	task := &pebblestore.ProjectTaskRecord{ID: "story", ProjectID: "project", AccountID: "account", Title: "story", Agent: "video"}
	if err := store.PutProjectTask("account", task); err != nil {
		t.Fatal(err)
	}
	if err := pebblestore.NewModelCatalogStore(db).SetRecord(pebblestore.ModelCatalogRecord{Provider: "google", Model: "video", Pricing: json.RawMessage(`{"billing":{"status":"verified","lines":[{"kind":"billing_rate","billable":"video_output","unit":"second","price_usd":0.1}]}}`)}); err != nil {
		t.Fatal(err)
	}
	server := &Server{sessions: session.NewService(store, nil)}
	generator := &paidStoryGenerator{}
	_, err = generateProjectVideoStory(context.Background(), generator, videogen.ManagedVideoRequest{}, []pebblestore.ProjectTaskScene{{Prompt: "one"}, {Prompt: "two"}}, func(scene int, clip videogen.ManagedVideoResult) {
		estimate := server.recordGeneratedTaskMedia(identity.Principal{AccountScopeID: "account", UserID: "user"}, task, 0, pebblestore.MediaCostEstimateOptions{Provider: "google", Model: "video", Kind: "video", Count: 1, DurationSeconds: 5}, clip.MediaType, len(clip.Bytes), scene)
		if estimate.PriceStatus != "known" {
			t.Fatalf("estimate %+v", estimate)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "probe scene 1") || generator.calls != 2 {
		t.Fatalf("calls %d error %v", generator.calls, err)
	}
	receipts, err := store.ListMediaUsage("account", 10)
	if err != nil || len(receipts) != 2 {
		t.Fatalf("receipts %+v %v", receipts, err)
	}
	ids := map[string]bool{}
	for _, receipt := range receipts {
		ids[receipt.ID] = true
	}
	if !ids["task-media:project:story:0:scene1"] || !ids["task-media:project:story:0:scene2"] {
		t.Fatal(ids)
	}
	acc, found, err := store.GetDailyUsageAccumulator("account", time.Now().UTC().Format("2006-01-02"))
	if err != nil || !found || acc.VideoCount != 2 || math.Abs(acc.MediaCostUSD-1) > 1e-10 {
		t.Fatalf("acc %+v %v", acc, err)
	}
}

type paidStoryGenerator struct{ calls int }

func (g *paidStoryGenerator) GenerateManagedVideo(context.Context, videogen.ManagedVideoRequest) (videogen.ManagedVideoResult, error) {
	g.calls++
	return videogen.ManagedVideoResult{Bytes: []byte("invalid mp4"), MediaType: "video/mp4", Provider: "google", Model: "video", DurationSeconds: 5}, nil
}

// Purpose: the post-generation receipt boundary must not label a rejected
// charge as known; a principal mismatch exercises the failure without writes.
func TestMediaCostUnrecorded(t *testing.T) {
	estimate := (&Server{}).recordGeneratedTaskMedia(identity.Principal{AccountScopeID: "other"}, &pebblestore.ProjectTaskRecord{AccountID: "account"}, 0, pebblestore.MediaCostEstimateOptions{}, "image/png", 1)
	if estimate.PriceStatus != "unrecorded" {
		t.Fatal(estimate)
	}
}

// Purpose: controlMCPMediaJobFiles must aggregate only ready deliverables;
// unfinished slots cannot erase subscription/free pricing or inflate totals.
// The stored-task view boundary proves the actual MCP response shape.
func TestMediaCostJobReadyPrices(t *testing.T) {
	for _, status := range []string{"known", "subscription", "free", "unknown", "unrecorded", ""} {
		t.Run(status, func(t *testing.T) {
			db, err := pebblestore.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := pebblestore.NewSessionStore(db)
			task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account", Title: "media", Agent: "image", Deliverables: []pebblestore.ProjectTaskDeliverable{
				{ID: "ready", Status: "ready", MediaCostEstimate: pebblestore.MediaCostEstimate{PriceStatus: status, CostUSD: .1}},
				{ID: "pending", Status: "pending", MediaCostEstimate: pebblestore.MediaCostEstimate{CostUSD: 99}},
				{ID: "failed", Status: "failed"},
			}}
			if err := store.PutProjectTask("account", task); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			request = request.WithContext(identity.ContextWithPrincipal(request.Context(), identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}))
			call := &controlMCPCall{request: request, server: &Server{sessions: session.NewService(store, nil)}}
			result, err := call.controlMCPMediaJobFiles(map[string]any{}, "project", "task", map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			want := status
			if want == "" || want == "unrecorded" {
				want = "unknown"
			}
			out := result.(map[string]any)
			if out["price_status"] != want || out["cost_usd"] != .1 {
				t.Fatal(out)
			}
		})
	}
}

// Purpose: recordTaskMedia must propagate measured Omni cost through the existing
// durable receipt, session and daily rollups exactly once. The real store/API
// boundary is the narrowest layer proving these accounting postconditions.
func TestOmniMeasuredReceiptRollups(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	p := identity.Principal{AccountScopeID: "account", UserID: "user"}
	if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "session"}, p.UserID, p.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	pricing, _ := pricingtest.Record(t, "gemini-omni-1.1-flash")
	rec := pebblestore.ModelCatalogRecord{Provider: "google", Model: "gemini-omni-1.1-flash", Pricing: pricing}
	if err := pebblestore.NewModelCatalogStore(db).SetRecord(rec); err != nil {
		t.Fatal(err)
	}
	server := &Server{sessions: session.NewService(store, nil)}
	task := &pebblestore.ProjectTaskRecord{ID: "video-task", ProjectID: "project", AccountID: p.AccountScopeID, OriginSessionID: "session"}
	output := videogen.ManagedVideoResult{Provider: "google", Model: rec.Model, MeasuredDurationSeconds: 4.01, Width: 1280, Height: 720, Resolution: "1080p"}
	for i := 0; i < 2; i++ {
		got := server.recordGeneratedTaskMedia(p, task, 0, output.BillingOptions(), "video/mp4", 100)
		if got.PriceStatus != "known" || math.Abs(got.CostUSD-.401) > 1e-10 {
			t.Fatalf("%+v", got)
		}
	}
	summary, found, err := store.GetUsageSummary("session")
	if err != nil || !found || summary.VideoCount != 1 || math.Abs(summary.MediaCostUSD-.401) > 1e-10 {
		t.Fatalf("%+v %v", summary, err)
	}
	daily, found, err := store.GetDailyUsageAccumulator("account", time.Now().UTC().Format("2006-01-02"))
	if err != nil || !found || daily.VideoCount != 1 || math.Abs(daily.MediaCostUSD-.401) > 1e-10 {
		t.Fatalf("%+v %v", daily, err)
	}
}

// Purpose: the Designer estimateManagedMedia admission path must price a
// duration-less Omni request from the shipped catalog and enforce its worker
// budget before recording usage. Real temporary stores prove the denial and
// absence of receipts, without a provider call.
func TestOmniManagedAdmission(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	p := identity.Principal{AccountScopeID: "account", UserID: "user"}
	if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "session"}, p.UserID, p.AccountScopeID); err != nil {
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
	server := &Server{sessions: session.NewService(store, nil), imageGen: imagegen.NewService(nil, pebblestore.NewAuthStore(db), nil, billingCatalog{}), model: modelruntime.NewService(pebblestore.NewModelStore(db), nil, modelruntime.NewCatalogService(catalog))}
	input := &tool.ProjectTaskCreateInput{Agent: "video", Model: rec.Model}
	estimate, err := server.estimateManagedMedia(context.Background(), p, input)
	if err != nil || estimate.PriceStatus != "known" || estimate.CostUSD != 1 {
		t.Fatalf("%+v %v", estimate, err)
	}
	if input.DurationSeconds != 0 {
		t.Fatal("estimate mutated requested duration")
	}
	if err := store.CheckWorkerMediaAdmission("account", "session", estimate); err == nil {
		t.Fatal("budget admitted oversized create estimate")
	}
	// Extensions reserve only the ten-second generated delta plus 50ms tolerance.
	input.Operation = pebblestore.VideoOperationExtend
	estimate, err = server.estimateManagedMedia(context.Background(), p, input)
	if err != nil || estimate.PriceStatus != "known" || math.Abs(estimate.CostUSD-1.005) > 1e-10 {
		t.Fatalf("extension: %+v %v", estimate, err)
	}
	if _, err := store.SetWorkerBudget("account", "worker", 1, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckWorkerMediaAdmission("account", "session", estimate); err == nil {
		t.Fatal("budget admitted oversized estimate")
	}
	receipts, err := store.ListMediaUsage("account", 10)
	if err != nil || len(receipts) != 0 {
		t.Fatalf("%+v %v", receipts, err)
	}
}
