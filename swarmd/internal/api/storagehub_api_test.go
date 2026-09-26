package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/notification"
	"swarm/packages/swarmd/internal/storagehub"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestStorageHubAPI_Endpoints(t *testing.T) {
	tempDir := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(tempDir, "pebble"))
	if err != nil {
		t.Fatalf("failed to open pebble store: %v", err)
	}
	defer store.Close()

	notifStore := pebblestore.NewNotificationStore(store)
	notifSvc := notification.NewService(notifStore, nil, nil)
	notifSvc.SetLocalSwarmIDResolver(func() string { return "test-swarm" })

	storageSvc := storagehub.NewService(store, notifSvc)

	server := &Server{
		notifications: notifSvc,
		storageHub:    storageSvc,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/storage/buckets", server.handleStorageBuckets)
	mux.HandleFunc("/v1/storage/buckets/", server.handleStorageBuckets)
	mux.HandleFunc("/v1/storage/workers", server.handleStorageWorkers)
	mux.HandleFunc("/v1/storage/workers/", server.handleStorageWorkers)
	mux.HandleFunc("/v1/storage/deliverables", server.handleStorageDeliverables)
	mux.HandleFunc("/v1/storage/deliverables/", server.handleStorageDeliverables)

	principal := identity.Principal{
		Type:           "user",
		UserID:         "user-1",
		AccountScopeID: "acct-storage-1",
	}

	authReq := func(req *http.Request) *http.Request {
		return req.WithContext(identity.ContextWithPrincipal(req.Context(), principal))
	}

	// 1. GET /v1/storage/buckets -> returns empty
	{
		req := authReq(httptest.NewRequest(http.MethodGet, "/v1/storage/buckets", nil))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp struct {
			Buckets []pebblestore.StorageBucketRecord `json:"buckets"`
			Count   int                               `json:"count"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Count != 0 {
			t.Fatalf("expected 0 buckets, got %d", resp.Count)
		}
	}

	// 2. POST /v1/storage/buckets -> register bucket
	var createdBucket pebblestore.StorageBucketRecord
	{
		body, _ := json.Marshal(map[string]any{
			"name":        "GCP Qualification Bucket",
			"provider":    "mock",
			"bucket_name": "swarm-gcp-qual-workers",
		})
		req := authReq(httptest.NewRequest(http.MethodPost, "/v1/storage/buckets", bytes.NewReader(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var resp struct {
			Bucket pebblestore.StorageBucketRecord `json:"bucket"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Bucket.ID == "" || resp.Bucket.Name != "GCP Qualification Bucket" {
			t.Fatalf("unexpected bucket: %+v", resp.Bucket)
		}
		createdBucket = resp.Bucket
	}

	// Setup mock driver with a worker and a deliverable
	mockDriver := storagehub.NewMockDriver()
	storageSvc.SetDriver(createdBucket.ID, mockDriver)

	ctx := context.Background()
	// Worker manifest & base instructions
	_ = mockDriver.Put(ctx, "workers/cloud-coder/worker.json", []byte(`{"name":"Cloud Coder","version":"2.0.0"}`), "application/json")
	_ = mockDriver.Put(ctx, "workers/cloud-coder/base/instructions.md", []byte("# Coder instructions"), "text/markdown")
	_ = mockDriver.Put(ctx, "workers/cloud-coder/sessions/sess-1/state.json", []byte(`{"status":"running","progress":80,"step":"Executing tests"}`), "application/json")

	// Deliverable
	diffData := []byte("diff --git a/pkg.go b/pkg.go\n+ fixed")
	diffHash := sha256.Sum256(diffData)
	diffHex := hex.EncodeToString(diffHash[:])
	_ = mockDriver.Put(ctx, "deliverables/cloud-coder/deliv-999/files/patch.diff", diffData, "text/x-diff")

	delivManifest := map[string]any{
		"id":         "deliv-999",
		"worker_id":  "cloud-coder",
		"session_id": "sess-1",
		"title":      "Security Patch for Token Expiry",
		"summary":    "Patched token expiration boundary checks.",
		"status":     "pending_review",
		"sha256":     diffHex,
		"files": []map[string]any{
			{
				"name":       "patch.diff",
				"path":       "deliverables/cloud-coder/deliv-999/files/patch.diff",
				"size_bytes": len(diffData),
				"sha256":     diffHex,
			},
		},
	}
	delivManifestBytes, _ := json.Marshal(delivManifest)
	_ = mockDriver.Put(ctx, "deliverables/cloud-coder/deliv-999/manifest.json", delivManifestBytes, "application/json")

	// 3. POST /v1/storage/buckets/{id}/scan -> triggers scan
	{
		scanURL := "/v1/storage/buckets/" + createdBucket.ID + "/scan"
		req := authReq(httptest.NewRequest(http.MethodPost, scanURL, nil))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 on scan, got %d: %s", w.Code, w.Body.String())
		}
		var resp struct {
			Summary storagehub.ScanSummary `json:"scan_summary"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Summary.WorkersFound != 1 || resp.Summary.DeliverablesNew != 1 {
			t.Fatalf("unexpected scan summary: %+v", resp.Summary)
		}
	}

	// 4. GET /v1/storage/workers -> discovered workers
	{
		req := authReq(httptest.NewRequest(http.MethodGet, "/v1/storage/workers", nil))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp struct {
			Workers []pebblestore.StorageDiscoveredWorkerRecord `json:"workers"`
			Count   int                                         `json:"count"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Count != 1 || resp.Workers[0].WorkerID != "cloud-coder" {
			t.Fatalf("unexpected workers: %+v", resp.Workers)
		}
		if resp.Workers[0].LastStatus != "running" || resp.Workers[0].LastProgress != 80 {
			t.Fatalf("unexpected worker progress: %+v", resp.Workers[0])
		}
	}

	// 5. POST /v1/storage/workers/cloud-coder/import -> imports worker
	{
		req := authReq(httptest.NewRequest(http.MethodPost, "/v1/storage/workers/cloud-coder/import", nil))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp struct {
			Worker pebblestore.StorageDiscoveredWorkerRecord `json:"worker"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if !resp.Worker.Imported {
			t.Fatalf("expected worker imported = true")
		}
	}

	// 6. GET /v1/storage/deliverables -> discovered deliverables
	{
		req := authReq(httptest.NewRequest(http.MethodGet, "/v1/storage/deliverables", nil))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp struct {
			Deliverables []pebblestore.StorageDiscoveredDeliverableRecord `json:"deliverables"`
			Count        int                                              `json:"count"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Count != 1 || resp.Deliverables[0].DeliverableID != "deliv-999" {
			t.Fatalf("unexpected deliverables: %+v", resp.Deliverables)
		}
	}

	// 7. POST /v1/storage/deliverables/deliv-999/import -> downloads files and accepts deliverable
	{
		destDir := t.TempDir()
		body, _ := json.Marshal(map[string]any{
			"target_workspace_path": destDir,
		})
		req := authReq(httptest.NewRequest(http.MethodPost, "/v1/storage/deliverables/deliv-999/import", bytes.NewReader(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp struct {
			Deliverable pebblestore.StorageDiscoveredDeliverableRecord `json:"deliverable"`
			TargetDir   string                                         `json:"target_dir"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Deliverable.Status != "accepted" {
			t.Fatalf("expected deliverable status 'accepted', got %s", resp.Deliverable.Status)
		}

		// Verify file written to destDir
		downloadedPath := filepath.Join(destDir, "patch.diff")
		downloadedBytes, err := os.ReadFile(downloadedPath)
		if err != nil {
			t.Fatalf("failed to read downloaded file: %v", err)
		}
		if string(downloadedBytes) != string(diffData) {
			t.Fatalf("downloaded file content mismatch")
		}
	}

	// 8. DELETE /v1/storage/buckets/{id} -> deletes bucket
	{
		req := authReq(httptest.NewRequest(http.MethodDelete, "/v1/storage/buckets/"+createdBucket.ID, nil))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 on delete, got %d", w.Code)
		}
	}
}
