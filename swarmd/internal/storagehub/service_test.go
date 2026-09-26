package storagehub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/notification"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type mockNotificationSubmitter struct {
	submitted []notification.InboxNotificationInput
}

func (m *mockNotificationSubmitter) SubmitInboxNotificationForAccount(accountScopeID string, input notification.InboxNotificationInput) (pebblestore.NotificationRecord, error) {
	m.submitted = append(m.submitted, input)
	return pebblestore.NotificationRecord{
		ID:             "notif_123",
		AccountScopeID: accountScopeID,
		Title:          input.Title,
		Kind:           input.Kind,
		Verified:       input.Verified,
	}, nil
}

func openTestPebble(t *testing.T) (*pebblestore.Store, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "swarm-storagehub-pebble-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	st, err := pebblestore.Open(filepath.Join(dir, "pebble"))
	if err != nil {
		_ = os.RemoveAll(dir)
		t.Fatalf("open pebble: %v", err)
	}
	cleanup := func() {
		_ = st.Close()
		_ = os.RemoveAll(dir)
	}
	return st, cleanup
}

func TestStorageHubService_Lifecycle(t *testing.T) {
	st, cleanup := openTestPebble(t)
	defer cleanup()

	notifMock := &mockNotificationSubmitter{}
	svc := NewService(st, notifMock)
	svc.now = func() time.Time { return time.Unix(1790360000, 0) }

	ctx := context.Background()
	accountID := "acct_test_1"

	// 1. Register a mock bucket
	bkt, err := svc.RegisterBucket(ctx, pebblestore.StorageBucketRecord{
		AccountScopeID: accountID,
		Name:           "Test S3 Hub",
		Provider:       "mock",
		BucketName:     "my-test-workers",
	})
	if err != nil {
		t.Fatalf("RegisterBucket: %v", err)
	}
	if bkt.ID == "" {
		t.Fatalf("expected non-empty bucket ID")
	}

	// Retrieve bucket driver and populate with test worker & deliverable files
	driver := NewMockDriver()
	svc.SetDriver(bkt.ID, driver)

	// A. Create worker.json
	wManifest := map[string]any{
		"name":        "Cloud Benchmark Runner",
		"description": "Runs parallel qualification suites in GCP",
		"version":     "1.2.0",
		"tags":        []string{"ci", "benchmark"},
	}
	wBytes, _ := json.Marshal(wManifest)
	_ = driver.Put(ctx, "workers/bench-worker-1/worker.json", wBytes, "application/json")

	// B. Create base instructions
	_ = driver.Put(ctx, "workers/bench-worker-1/base/instructions.md", []byte("# Benchmark Guidelines"), "text/markdown")

	// C. Create session state
	sState := map[string]any{
		"status":   "running",
		"progress": 65,
		"step":     "Running trial 65/100",
	}
	sBytes, _ := json.Marshal(sState)
	_ = driver.Put(ctx, "workers/bench-worker-1/sessions/sess-alpha/state.json", sBytes, "application/json")

	// D. Create deliverable files & manifest
	patchContent := []byte("diff --git a/main.go b/main.go\n+ func Safe() {}")
	h := sha256.Sum256(patchContent)
	patchSHA := hex.EncodeToString(h[:])

	_ = driver.Put(ctx, "deliverables/bench-worker-1/deliv-001/files/patch.diff", patchContent, "text/x-diff")

	dManifest := map[string]any{
		"id":         "deliv-001",
		"worker_id":  "bench-worker-1",
		"session_id": "sess-alpha",
		"title":      "Qualification Suite Results",
		"summary":    "All 100 benchmark trials succeeded.",
		"status":     "pending_review",
		"sha256":     patchSHA,
		"files": []map[string]any{
			{
				"name":       "patch.diff",
				"path":       "deliverables/bench-worker-1/deliv-001/files/patch.diff",
				"size_bytes": len(patchContent),
				"sha256":     patchSHA,
			},
		},
	}
	dBytes, _ := json.Marshal(dManifest)
	_ = driver.Put(ctx, "deliverables/bench-worker-1/deliv-001/manifest.json", dBytes, "application/json")

	// 2. Scan Bucket
	summary, err := svc.ScanBucket(ctx, accountID, bkt.ID)
	if err != nil {
		t.Fatalf("ScanBucket failed: %v", err)
	}

	if summary.WorkersFound != 1 {
		t.Errorf("expected 1 worker found, got %d", summary.WorkersFound)
	}
	if summary.DeliverablesNew != 1 {
		t.Errorf("expected 1 new deliverable, got %d", summary.DeliverablesNew)
	}

	// 3. Verify Discovered Worker
	workers, err := svc.ListDiscoveredWorkers(accountID)
	if err != nil {
		t.Fatalf("ListDiscoveredWorkers: %v", err)
	}
	if len(workers) != 1 {
		t.Fatalf("expected 1 discovered worker, got %d", len(workers))
	}
	if workers[0].Name != "Cloud Benchmark Runner" {
		t.Errorf("unexpected worker name: %s", workers[0].Name)
	}
	if !workers[0].HasBaseContext {
		t.Errorf("expected HasBaseContext = true")
	}
	if workers[0].LastStatus != "running" || workers[0].LastProgress != 65 {
		t.Errorf("unexpected session progress: %s (%d%%)", workers[0].LastStatus, workers[0].LastProgress)
	}

	// 4. Verify AI Inbox Notification was submitted
	if len(notifMock.submitted) != 1 {
		t.Fatalf("expected 1 inbox notification, got %d", len(notifMock.submitted))
	}
	notif := notifMock.submitted[0]
	if notif.Title != "Qualification Suite Results" {
		t.Errorf("unexpected notif title: %s", notif.Title)
	}
	if !notif.Verified {
		t.Errorf("expected notification to be Verified = true")
	}
	if notif.WorkerID != "bench-worker-1" {
		t.Errorf("unexpected worker ID: %s", notif.WorkerID)
	}
	if len(notif.Actions) == 0 || notif.Actions[0].Endpoint != "/v1/storage/deliverables/deliv-001/import" {
		t.Errorf("unexpected action endpoint: %+v", notif.Actions)
	}

	// 5. Import Deliverable to target directory
	targetDir, err := os.MkdirTemp("", "swarm-deliverable-dest-*")
	if err != nil {
		t.Fatalf("target dir: %v", err)
	}
	defer os.RemoveAll(targetDir)

	imported, err := svc.ImportDeliverable(ctx, accountID, "deliv-001", targetDir)
	if err != nil {
		t.Fatalf("ImportDeliverable: %v", err)
	}
	if imported.Status != "accepted" {
		t.Errorf("expected deliverable status 'accepted', got %s", imported.Status)
	}

	// Check file was actually written to destination
	writtenPath := filepath.Join(targetDir, "patch.diff")
	writtenData, err := os.ReadFile(writtenPath)
	if err != nil {
		t.Fatalf("read written deliverable file: %v", err)
	}
	if string(writtenData) != string(patchContent) {
		t.Errorf("deliverable file content mismatch")
	}

	// Check manifest in driver was updated to accepted
	updatedManifestBytes, err := driver.Get(ctx, "deliverables/bench-worker-1/deliv-001/manifest.json")
	if err != nil {
		t.Fatalf("read updated manifest: %v", err)
	}
	var updatedM map[string]any
	_ = json.Unmarshal(updatedManifestBytes, &updatedM)
	if updatedM["status"] != "accepted" {
		t.Errorf("expected driver manifest status 'accepted', got %v", updatedM["status"])
	}
}
