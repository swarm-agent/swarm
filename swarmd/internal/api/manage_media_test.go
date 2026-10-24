package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"swarm/packages/swarmd/internal/imagegen"
	modelruntime "swarm/packages/swarmd/internal/model"
	"sync/atomic"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: ManageMedia must expose only the caller's project files and save
// without overwriting or escaping its workspace. The real temporary store and
// filesystem are the narrowest layer proving bridge authorization and the
// shared mediaInputs/mediaSaveDir/saveMediaFiles postconditions without a provider.
func TestManageMediaFilesAndInputs(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	root := t.TempDir()
	if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "session", WorkspacePath: root, Metadata: map[string]any{"project_id": "project"}}, p.UserID, p.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: p.AccountScopeID, Agent: "image", Title: "sample", Status: "completed", Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "file", Kind: "image", Status: "ready", MediaURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("image bytes"))}}}
	task.Deliverables[0].CostUSD = 0.25
	if err := store.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	server := &Server{sessions: session.NewService(store, nil)}
	scope := tool.WorkspaceScope{SessionID: "session", Principal: p, PrimaryPath: root}
	args := map[string]any{"action": "get", "job_id": "media/project/task"}
	result, err := server.ManageMedia(context.Background(), scope, args)
	if err != nil {
		t.Fatal(err)
	}
	view := result.(map[string]any)
	if view["cost_usd"] != 0.25 || len(view["files"].([]map[string]any)) != 1 {
		t.Fatalf("view %+v", view)
	}
	args["action"], args["folder"] = "save", "assets"
	if _, err := server.ManageMedia(context.Background(), scope, args); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "assets", mediaTaskFiles(task)[0].filename(task.Title, 1))
	if err := os.WriteFile(path, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ManageMedia(context.Background(), scope, args); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep me" {
		t.Fatalf("overwritten %q %v", data, err)
	}
	inputs, err := mediaInputs([]any{map[string]any{"path": path}, map[string]any{"job_id": "media/project/task", "file": 1}}, func() ([]string, error) { return []string{root}, nil }, func(string, string) (*pebblestore.ProjectTaskRecord, error) { return task, nil })
	if err != nil || len(inputs) != 2 || inputs[0]["url"] != "data:image/png;base64,"+base64.StdEncoding.EncodeToString([]byte("keep me")) || inputs[1]["url"] != task.Deliverables[0].MediaURL {
		t.Fatalf("inputs %+v %v", inputs, err)
	}
	args["folder"] = "../escape"
	if _, err := server.ManageMedia(context.Background(), scope, args); err == nil {
		t.Fatal("traversal allowed")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	args["folder"] = "outside"
	if _, err := server.ManageMedia(context.Background(), scope, args); err == nil {
		t.Fatal("symlink escape allowed")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside mutated %v %v", entries, err)
	}
	args["action"], args["job_id"] = "get", "media/other/task"
	if _, err := server.ManageMedia(context.Background(), scope, args); err == nil {
		t.Fatal("cross-project allowed")
	}
	scope.Principal.UserID = "other"
	if _, err := server.ManageMedia(context.Background(), scope, args); err == nil {
		t.Fatal("cross-user allowed")
	}
}

// Purpose: recordTaskMedia must atomically add media counts and estimated costs
// to the originating session exactly once. Real receipt writes prove retry and
// mixed-kind accounting at the narrowest durable boundary without live media.
func TestManageMediaSessionUsage(t *testing.T) {
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
	if err := store.PutUsageSummary(pebblestore.SessionUsageSummary{SessionID: "session", AccountScopeID: p.AccountScopeID, UserID: p.UserID, EstimatedCostUSD: 2}); err != nil {
		t.Fatal(err)
	}
	server := &Server{sessions: session.NewService(store, nil)}
	task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: p.AccountScopeID, OriginSessionID: "session"}
	if err := pebblestore.NewModelCatalogStore(db).SetRecord(pebblestore.ModelCatalogRecord{Provider: "google", Model: "gemini-nano-banana-2.1", Pricing: json.RawMessage(`{"billing":{"status":"verified","lines":[{"kind":"billing_rate","billable":"image_output","unit":"million_tokens","price_usd":30,"conditions":{"tier":"paid","service_tier":"standard"}}]}}`)}); err != nil {
		t.Fatal(err)
	}
	total := 2.0
	for i, kind := range []string{"image", "video", "audio"} {
		opts := pebblestore.MediaCostEstimateOptions{Kind: kind, Count: 1, Provider: "google", Model: "gemini-nano-banana-2.1", OutputTokens: 1120}
		estimate, err := server.recordTaskMedia(p, task, i, opts, kind+"/test", 10)
		if err != nil {
			t.Fatal(err)
		}
		total += estimate.CostUSD
		if _, err := server.recordTaskMedia(p, task, i, opts, kind+"/test", 10); err != nil {
			t.Fatal(err)
		}
	}
	summary, found, err := store.GetUsageSummary("session")
	if err != nil || !found || summary.ImageCount != 1 || summary.VideoCount != 1 || summary.SoundCount != 1 || summary.EstimatedCostUSD != total || math.Abs(summary.MediaCostUSD-(total-2)) > 1e-10 || summary.MediaCostUSD <= 0 {
		t.Fatalf("summary %+v %v", summary, err)
	}
}

// Purpose: ManageMedia generate must use CreateProjectTask with owned origin,
// explicit options and attached inputs. Real task/store boundaries with a blocked
// deterministic provider prove CheckWorkerMediaAdmission rejects unknown pricing
// and excessive estimates without reserving allowance, while background media
// permits model turns in the caller and other workers. Receipts retain attribution.
func TestManageMediaGenerateOrigin(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	root := t.TempDir()
	if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "session", WorkspacePath: root, Metadata: map[string]any{"project_id": "project"}}, p.UserID, p.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	if err := store.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "project", Name: "Media project", AccountID: p.AccountScopeID}); err != nil {
		t.Fatal(err)
	}
	var rec pebblestore.ModelCatalogRecord
	if err := json.Unmarshal([]byte(`{"provider":"google","model":"gemini-nano-banana-2.1","catalog_modalities":{"outputs":["image"]},"media":{"state":"supported","provider_surface":"gemini_generate_content"},"provider_specific":{"google":{"model_api_surface":"generate_content","image_generation":{"api_surface":"generate_content","status":"verified","settings":{"aspect_ratio":{"status":"verified","supported_values":["1:1"]},"image_size":{"status":"verified","supported_values":["1K"]}}}}}}`), &rec); err != nil {
		t.Fatal(err)
	}
	gen := imagegen.NewService(nil, pebblestore.NewAuthStore(db), nil, billingCatalog{rec})
	server := &Server{sessions: session.NewService(store, nil), imageGen: gen}
	scope := tool.WorkspaceScope{SessionID: "session", Principal: p, PrimaryPath: root}
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(root, "input.png")
	if err := os.WriteFile(inputPath, imageData.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	prior := &pebblestore.ProjectTaskRecord{ID: "prior", ProjectID: "project", AccountID: p.AccountScopeID, Agent: "image", Title: "prior", Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "file", Kind: "image", Status: "ready", MediaURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageData.Bytes())}}}
	if err := store.PutProjectTask(p.AccountScopeID, prior); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(pebblestore.KeyWorker("account", "worker"), pebblestore.WorkerRecord{ID: "worker", AccountScopeID: "account"}); err != nil {
		t.Fatal(err)
	}
	// Seed the durable lineage binding normally captured by worker admission.
	if err := db.PutJSON("usage_scope_binding/account/session", []pebblestore.UsageScopeTotal{{Kind: "worker", ID: "worker"}, {Kind: "worker_run", ProjectID: "worker", ID: "run"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutUsageLimit(pebblestore.UsageLimitRecord{AccountScopeID: p.AccountScopeID, Enabled: true, DailyCostLimitUSD: 100}); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"inputs": []any{map[string]any{"path": "input.png"}, map[string]any{"job_id": "media/project/prior", "file": float64(1)}}, "action": "generate", "kind": "image", "brief": "A blue circle", "model": rec.Model, "aspect_ratio": "1:1", "resolution": "1K", "count": float64(25)}
	// Even pending batches require known pricing under a cap.
	if _, err := server.ManageMedia(context.Background(), scope, args); err == nil {
		t.Fatal("unknown price accepted")
	}
	rec.Pricing = json.RawMessage(`{"billing":{"status":"verified","lines":[{"kind":"billing_rate","billable":"image_output","unit":"image","price_usd":0.03,"variant":"1K","conditions":{"tier":"paid","service_tier":"standard"}},{"kind":"billing_rate","billable":"image_output","unit":"million_tokens","price_usd":30,"conditions":{"tier":"paid","service_tier":"standard"}}]}}`)
	if err := pebblestore.NewModelCatalogStore(db).SetRecord(rec); err != nil {
		t.Fatal(err)
	}
	if err := store.PutUsageLimit(pebblestore.UsageLimitRecord{AccountScopeID: p.AccountScopeID, Enabled: true, DailyCostLimitUSD: 0.001}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ManageMedia(context.Background(), scope, args); err == nil || !strings.Contains(err.Error(), "daily account usage limit exceeded") {
		t.Fatalf("estimate limit: %v", err)
	}
	if err := store.PutUsageLimit(pebblestore.UsageLimitRecord{AccountScopeID: p.AccountScopeID, Enabled: true, DailyCostLimitUSD: 100}); err != nil {
		t.Fatal(err)
	}
	policy, err := store.SetWorkerBudget(p.AccountScopeID, "worker", 0, 0.001, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.ManageMedia(context.Background(), scope, args); err == nil || !strings.Contains(err.Error(), "daily worker") {
		t.Fatalf("worker estimate limit: %v", err)
	}
	if _, err := store.SetWorkerBudget(p.AccountScopeID, "worker", policy.Revision, 0, 0); err != nil {
		t.Fatal(err)
	}
	limits, err := store.GetLimits(p.AccountScopeID)
	if err != nil {
		t.Fatal(err)
	}
	limits.JobCostUSD = 0.001
	limits, err = store.SetLimits(p.AccountScopeID, limits.Revision, limits)
	if err != nil {
		t.Fatal(err)
	}
	// Estimated spend must refuse without persisting an actual usage crossing
	// or emitting a warning. Install the real signal store to prove both effects.
	signals := pebblestore.NewSignalStore(db)
	db.SetLimitSignalStore(signals)
	if _, err := server.ManageMedia(context.Background(), scope, args); err == nil || !strings.Contains(err.Error(), "job_cost_usd") {
		t.Fatalf("job estimate limit: %v", err)
	}
	var crossing map[string]any
	if found, err := db.GetJSON("limits/crossed/account/worker%2Frun/job_cost_usd", &crossing); err != nil || found {
		t.Fatalf("estimate wrote crossing %v %v", crossing, err)
	}
	page, err := signals.ListAfter(0, 100, pebblestore.SignalFilter{Account: p.AccountScopeID})
	if err != nil || len(page.Signals) != 0 {
		t.Fatalf("estimate emitted signals %+v %v", page, err)
	}
	limits.JobCostUSD = 0
	if _, err := store.SetLimits(p.AccountScopeID, limits.Revision, limits); err != nil {
		t.Fatal(err)
	}
	result, err := server.ManageMedia(context.Background(), scope, args)
	if err != nil {
		t.Fatal(err)
	}
	assertUnreserved := func() {
		t.Helper()
		var value map[string]any
		for _, key := range []string{"worker_budget_account/account/reservation", "worker_budget/account/worker/reservation"} {
			if found, err := db.GetJSON(key, &value); err != nil || found {
				t.Fatalf("unexpected reservation %s %v", key, err)
			}
		}
	}
	assertUnreserved()
	_, project, id, err := controlMCPParseJobID(result.(map[string]any))
	if err != nil {
		t.Fatal(err)
	}
	task, found, err := store.GetProjectTask(p.AccountScopeID, project, id)
	if err != nil || !found || task.OriginSessionID != "session" || !task.ManageMediaSource || task.Model != rec.Model || task.AspectRatio != "1:1" || task.Resolution != "1K" || task.VariantCount != 25 || task.WorkspacePath != root || task.Status != "pending_approval" || len(task.AttachedMedia) != 2 {
		t.Fatalf("task %+v %v", task, err)
	}
	// Rejected pending tasks cannot start execution or consume allowance.
	for _, status := range []string{"rejected", "cancelled"} {
		task.Status = status
		if err := store.PutProjectTask(p.AccountScopeID, task); err != nil {
			t.Fatal(err)
		}
		assertUnreserved()
	}
	// A pre-provider settings failure must not acquire either reservation.
	task.Resolution = "invalid-resolution"
	server.executeDirectMediaTask(p, &pebblestore.ProjectRecord{ID: project}, task)
	assertUnreserved()
	failed, _, err := store.GetProjectTask(p.AccountScopeID, project, id)
	if err != nil || failed.Status != "failed" {
		t.Fatalf("settings failure %+v %v", failed, err)
	}
	args["resolution"] = "invalid-resolution"
	if _, err := server.ManageMedia(context.Background(), scope, args); err == nil {
		t.Fatal("invalid creation accepted")
	}
	assertUnreserved()
	// Real generation service with a deterministic provider proves background
	// execution without holding creation locks, and cancellable observation.
	auth := pebblestore.NewAuthStore(db)
	if _, err := auth.UpsertCredential(pebblestore.AuthCredentialInput{Provider: "google", AccountScopeID: p.AccountScopeID, ID: "fixture", Type: "api_key", APIKey: "test-only", SetActive: true}); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	client := &blockingMediaClient{png: imageData.Bytes(), release: release, entered: make(chan struct{}, 100)}
	server.imageGen = imagegen.NewService(nil, auth, nil, billingCatalog{rec})
	server.imageGen.SetGeminiImageClient(client)
	args["resolution"], args["count"] = "1K", float64(1)
	delete(args, "inputs")
	priced := rec.Pricing
	rec.Pricing = nil
	if err := pebblestore.NewModelCatalogStore(db).SetRecord(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ManageMedia(context.Background(), scope, args); err == nil {
		t.Fatal("unknown price accepted")
	}
	if client.calls.Load() != 0 {
		t.Fatal("unknown-priced capped generation reached provider")
	}
	assertUnreserved()
	rec.Pricing = priced
	if err := pebblestore.NewModelCatalogStore(db).SetRecord(rec); err != nil {
		t.Fatal(err)
	}
	completed, err := server.ManageMedia(context.Background(), scope, args)
	if err != nil {
		t.Fatal(err)
	}
	_, _, completedID, err := controlMCPParseJobID(completed.(map[string]any))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider not entered")
	}
	assertUnreserved()
	// Model admission must succeed for both the originating worker and another
	// worker in the same capped account while the media provider is blocked.
	if err := db.PutJSON(pebblestore.KeyWorker("account", "other-worker"), pebblestore.WorkerRecord{ID: "other-worker", AccountScopeID: "account"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "other-session"}, p.UserID, p.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON("usage_scope_binding/account/other-session", []pebblestore.UsageScopeTotal{{Kind: "worker", ID: "other-worker"}}); err != nil {
		t.Fatal(err)
	}
	for _, sid := range []string{scope.SessionID, "other-session"} {
		if err := store.CheckWorkerSessionBudgetWithPrice(p.AccountScopeID, sid, "known", "model-turn"); err != nil {
			t.Fatalf("model admission %s: %v", sid, err)
		}
		// Remove only this test's model reservation; media must never own it.
		if err := db.Delete("worker_budget_account/account/reservation"); err != nil {
			t.Fatal(err)
		}
	}
	assertUnreserved()
	// A second creation must return while the first provider remains blocked.
	created := make(chan error, 1)
	go func() {
		_, err := server.CreateProjectTask(context.Background(), p, project, tool.ProjectTaskCreateInput{Title: "second", Agent: "image", Model: rec.Model, VariantCount: 25})
		created <- err
	}()
	select {
	case err := <-created:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("creation blocked by generation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	view, err := server.ManageMedia(ctx, scope, map[string]any{"action": "get", "job_id": completed.(map[string]any)["job_id"], "wait_seconds": float64(30)})
	if err != nil || view.(map[string]any)["message"] != "still generating; wait cancelled" {
		t.Fatalf("cancelled wait %v %v", view, err)
	}
	current, _, err := store.GetProjectTask(p.AccountScopeID, project, completedID)
	if err != nil || current.Status != "in_progress" {
		t.Fatalf("wait changed task %+v %v", current, err)
	}
	close(release)
	waitMediaTask(t, server, scope, completed.(map[string]any)["job_id"].(string))
	saved, _, err := store.GetProjectTask(p.AccountScopeID, project, completedID)
	if err != nil || client.calls.Load() != 1 || len(saved.Deliverables) != 1 || saved.Deliverables[0].Status != "ready" {
		t.Fatalf("generation not complete %+v calls=%d err=%v", saved, client.calls.Load(), err)
	}
	assertUnreserved()
	// Failure after entering a provider, without a receipt, leaves no held state.
	failureGate := make(chan struct{})
	close(failureGate)
	failedClient := &blockingMediaClient{entered: make(chan struct{}, 100), release: failureGate, fail: true}
	server.imageGen.SetGeminiImageClient(failedClient)
	failedJob, err := server.ManageMedia(context.Background(), scope, args)
	if err != nil {
		t.Fatal(err)
	}
	waitMediaTask(t, server, scope, failedJob.(map[string]any)["job_id"].(string))
	assertUnreserved()
	if err := store.CheckWorkerMediaAdmission(p.AccountScopeID, scope.SessionID, pebblestore.MediaCostEstimate{PriceStatus: "known", CostUSD: 0.03}); err != nil {
		t.Fatalf("failure left held state: %v", err)
	}
	// Already-recorded spend must refuse subsequent generation at admission.
	if err := store.PutUsageLimit(pebblestore.UsageLimitRecord{AccountScopeID: p.AccountScopeID, Enabled: true, DailyCostLimitUSD: 0.001}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ManageMedia(context.Background(), scope, args); err == nil || !strings.Contains(err.Error(), "worker budget blocked") {
		t.Fatalf("over-limit generation: %v", err)
	}
	assertUnreserved()
	if err := store.PutUsageLimit(pebblestore.UsageLimitRecord{AccountScopeID: p.AccountScopeID, Enabled: true, DailyCostLimitUSD: 100}); err != nil {
		t.Fatal(err)
	}
	// Unmarked orchestrator-origin tasks and approved batches bypass the new
	// worker gate even with unknown pricing, and approval never waits on providers.
	rec.Pricing = nil
	if err := pebblestore.NewModelCatalogStore(db).SetRecord(rec); err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{1, 25} {
		gate := make(chan struct{})
		legacyClient := &blockingMediaClient{png: imageData.Bytes(), entered: make(chan struct{}, 100), release: gate}
		server.imageGen.SetGeminiImageClient(legacyClient)
		legacy, err := server.CreateProjectTask(context.Background(), p, project, tool.ProjectTaskCreateInput{Title: "orchestrator", Agent: "image", OriginSessionID: scope.SessionID, Model: rec.Model, AspectRatio: "1:1", Resolution: "1K", VariantCount: count, AutoApprove: true})
		if err != nil {
			t.Fatal(err)
		}
		if legacy.ManageMediaSource {
			t.Fatal("ordinary task marked manage_media")
		}
		if count == 25 {
			approved := make(chan error, 1)
			go func() {
				_, err := server.ApproveProjectTask(context.Background(), p, project, legacy.ID)
				approved <- err
			}()
			select {
			case err := <-approved:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("approval blocked")
			}
		}
		select {
		case <-legacyClient.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("legacy task blocked by budget gate")
		}
		assertUnreserved()
		close(gate)
		waitMediaTask(t, server, scope, controlMCPJobID(controlMCPJobMedia, project, legacy.ID))
	}
	if _, err := server.CreateProjectTask(context.Background(), p, project, tool.ProjectTaskCreateInput{ID: "rejected", Title: "bad", Agent: "image", OriginSessionID: "missing"}); err == nil {
		t.Fatal("missing origin accepted")
	}
	if _, found, err := store.GetProjectTask(p.AccountScopeID, project, "rejected"); err != nil || found {
		t.Fatalf("rejected task persisted %v", err)
	}
}

type blockingMediaClient struct {
	fail    bool
	calls   atomic.Int32
	png     []byte
	entered chan struct{}
	release chan struct{}
}

func (c *blockingMediaClient) GenerateImage(ctx context.Context, _ imagegen.GeminiImageGenerationRequest) (imagegen.GeminiImageGenerationResult, error) {
	c.calls.Add(1)
	c.entered <- struct{}{}
	select {
	case <-c.release:
	case <-ctx.Done():
		return imagegen.GeminiImageGenerationResult{}, ctx.Err()
	}
	if c.fail {
		return imagegen.GeminiImageGenerationResult{}, errors.New("provider rejected fixture")
	}
	return imagegen.GeminiImageGenerationResult{Images: []imagegen.GeminiGeneratedImage{{DecodedPNG: c.png, MIMEType: "image/png"}}, Usage: map[string]any{"candidatesTokensDetails": []any{map[string]any{"modality": "IMAGE", "tokenCount": 1120}}}}, nil
}
func waitMediaTask(t *testing.T, s *Server, scope tool.WorkspaceScope, job string) {
	t.Helper()
	result, err := s.ManageMedia(context.Background(), scope, map[string]any{"action": "get", "job_id": job, "wait_seconds": float64(30)})
	if err != nil {
		t.Fatal(err)
	}
	status := result.(map[string]any)["status"]
	if status != "completed" && status != "failed" && status != "needs_review" {
		t.Fatalf("task not terminal %v", result)
	}
}

// Purpose: ManageMedia must refuse projectless sessions before task creation.
// The real session/task store is the narrowest boundary proving both the error
// and absence of tasks, including accidental fallback to an existing project.
func TestManageMediaGenerateWithoutProject(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "projectless"}, p.UserID, p.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	if err := store.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "project", Name: "Existing project", AccountID: p.AccountScopeID}); err != nil {
		t.Fatal(err)
	}
	server := &Server{sessions: session.NewService(store, nil)}
	_, err = server.ManageMedia(context.Background(), tool.WorkspaceScope{SessionID: "projectless", Principal: p}, map[string]any{"action": "generate", "kind": "image", "brief": "a circle"})
	if err == nil || !strings.Contains(err.Error(), "requires a session with a project") {
		t.Fatalf("projectless generation error %v", err)
	}
	for _, project := range []string{"project"} {
		tasks, err := store.ListProjectTasks(p.AccountScopeID, project, 100)
		if err != nil || len(tasks) != 0 {
			t.Fatalf("project %q tasks %+v error %v", project, tasks, err)
		}
	}
}

// Purpose: internal media results must guide agents through manage_media, not
// MCP tools. The session bridge and real catalog/file views are the narrowest
// layer proving caller-selected guidance for models and all job states.
func TestManageMediaGuidance(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "session", Metadata: map[string]any{"project_id": "project"}}, p.UserID, p.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	catalog := pebblestore.NewModelCatalogStore(db)
	server := &Server{sessions: session.NewService(store, nil), imageGen: imagegen.NewService(nil, pebblestore.NewAuthStore(db), nil, billingCatalog{}), model: modelruntime.NewService(pebblestore.NewModelStore(db), nil, modelruntime.NewCatalogService(catalog))}
	scope := tool.WorkspaceScope{SessionID: "session", Principal: p}
	check := func(args map[string]any) {
		t.Helper()
		result, err := server.ManageMedia(context.Background(), scope, args)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "swarm_") || strings.Contains(string(data), "include_media") || !strings.Contains(string(data), "manage_media") {
			t.Fatalf("incorrect guidance %s", data)
		}
	}
	check(map[string]any{"action": "models"})
	for _, status := range []string{"completed", "needs_review", "failed", "blocked", "cancelled", "rejected", "running", "queued"} {
		task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: p.AccountScopeID, Agent: "image", Title: "Guidance fixture", Status: status, Deliverables: []pebblestore.ProjectTaskDeliverable{{Kind: "image", Status: "ready", MediaURL: "data:image/png;base64,aW1hZ2U="}}}
		if err := store.PutProjectTask(p.AccountScopeID, task); err != nil {
			t.Fatal(err)
		}
		check(map[string]any{"action": "get", "job_id": "media/project/task"})
	}
}
