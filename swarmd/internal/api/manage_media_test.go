package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"swarm/packages/swarmd/internal/imagegen"
	"testing"

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
// explicit options and attached input bytes, rejecting unowned origins before
// reservation. A 25-image batch stays pending under existing admission, so the
// real creation path is tested without provider calls or goroutine races.
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
	args := map[string]any{"inputs": []any{map[string]any{"path": "input.png"}, map[string]any{"job_id": "media/project/prior", "file": float64(1)}}, "action": "generate", "kind": "image", "brief": "A blue circle", "model": rec.Model, "aspect_ratio": "1:1", "resolution": "1K", "count": float64(25)}
	result, err := server.ManageMedia(context.Background(), scope, args)
	if err != nil {
		t.Fatal(err)
	}
	_, project, id, err := controlMCPParseJobID(result.(map[string]any))
	if err != nil {
		t.Fatal(err)
	}
	task, found, err := store.GetProjectTask(p.AccountScopeID, project, id)
	if err != nil || !found || task.OriginSessionID != "session" || task.Model != rec.Model || task.AspectRatio != "1:1" || task.Resolution != "1K" || task.VariantCount != 25 || task.WorkspacePath != root || task.Status != "pending_approval" || len(task.AttachedMedia) != 2 {
		t.Fatalf("task %+v %v", task, err)
	}
	if _, err := server.CreateProjectTask(context.Background(), p, project, tool.ProjectTaskCreateInput{ID: "rejected", Title: "bad", Agent: "image", OriginSessionID: "missing"}); err == nil {
		t.Fatal("missing origin accepted")
	}
	if _, found, err := store.GetProjectTask(p.AccountScopeID, project, "rejected"); err != nil || found {
		t.Fatalf("rejected task persisted %v", err)
	}
}
