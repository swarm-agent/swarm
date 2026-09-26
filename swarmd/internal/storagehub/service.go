package storagehub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/notification"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type NotificationSubmitter interface {
	SubmitInboxNotificationForAccount(accountScopeID string, input notification.InboxNotificationInput) (pebblestore.NotificationRecord, error)
}

type ScanSummary struct {
	BucketID        string   `json:"bucket_id"`
	WorkersFound    int      `json:"workers_found"`
	WorkersAdded    []string `json:"workers_added,omitempty"`
	WorkersUpdated  []string `json:"workers_updated,omitempty"`
	WorkersRemoved  []string `json:"workers_removed,omitempty"`
	SessionsFound   int      `json:"sessions_found"`
	DeliverablesNew int      `json:"deliverables_new"`
	ScannedAt       int64    `json:"scanned_at"`
}

type Service struct {
	store         *pebblestore.Store
	notifications NotificationSubmitter
	driversMu     sync.RWMutex
	drivers       map[string]Driver
	now           func() time.Time
}

func NewService(store *pebblestore.Store, notifications NotificationSubmitter) *Service {
	return &Service{
		store:         store,
		notifications: notifications,
		drivers:       make(map[string]Driver),
		now:           time.Now,
	}
}

func (s *Service) SetDriver(bucketID string, driver Driver) {
	s.driversMu.Lock()
	defer s.driversMu.Unlock()
	s.drivers[bucketID] = driver
}

func (s *Service) getDriver(bucket pebblestore.StorageBucketRecord) (Driver, error) {
	s.driversMu.RLock()
	d, ok := s.drivers[bucket.ID]
	s.driversMu.RUnlock()
	if ok && d != nil {
		return d, nil
	}

	// Create driver on-demand based on provider
	switch bucket.Provider {
	case "mock":
		s.driversMu.Lock()
		defer s.driversMu.Unlock()
		if existing, ok := s.drivers[bucket.ID]; ok && existing != nil {
			return existing, nil
		}
		mockD := NewMockDriver()
		s.drivers[bucket.ID] = mockD
		return mockD, nil
	case "local":
		localDir := bucket.BucketName
		if localDir == "" {
			return nil, errors.New("local provider requires bucket_name to specify directory path")
		}
		localD, err := NewLocalDirDriver(localDir)
		if err != nil {
			return nil, fmt.Errorf("init local driver: %w", err)
		}
		s.driversMu.Lock()
		s.drivers[bucket.ID] = localD
		s.driversMu.Unlock()
		return localD, nil
	case "s3", "gcs":
		endpoint := bucket.Endpoint
		region := bucket.Region
		if bucket.Provider == "gcs" {
			if endpoint == "" {
				endpoint = "https://storage.googleapis.com"
			}
			if region == "" {
				region = "auto"
			}
		}
		s3D, err := NewS3Driver(S3DriverConfig{
			Bucket:          bucket.BucketName,
			Endpoint:        endpoint,
			Region:          region,
			AccessKeyID:     bucket.AccessKeyID,
			SecretAccessKey: bucket.SecretAccessKey,
			ForcePathStyle:  true,
		})
		if err != nil {
			return nil, fmt.Errorf("init s3 driver: %w", err)
		}
		s.driversMu.Lock()
		s.drivers[bucket.ID] = s3D
		s.driversMu.Unlock()
		return s3D, nil
	default:
		return nil, fmt.Errorf("unsupported storage provider: %s", bucket.Provider)
	}
}

func (s *Service) RegisterBucket(ctx context.Context, bucket pebblestore.StorageBucketRecord) (*pebblestore.StorageBucketRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("storage hub store unconfigured")
	}
	bucket.AccountScopeID = strings.TrimSpace(bucket.AccountScopeID)
	if bucket.AccountScopeID == "" {
		return nil, errors.New("account_scope_id is required")
	}
	bucket.Name = strings.TrimSpace(bucket.Name)
	if bucket.Name == "" {
		return nil, errors.New("bucket name is required")
	}
	if bucket.ID == "" {
		raw := make([]byte, 8)
		_, _ = rand.Read(raw)
		bucket.ID = "bkt_" + hex.EncodeToString(raw)
	}
	if bucket.Provider == "" {
		bucket.Provider = "s3"
	}
	bucket.Enabled = true
	bucket.CreatedAt = s.now().UnixMilli()
	bucket.UpdatedAt = bucket.CreatedAt

	if err := s.store.PutStorageBucket(bucket); err != nil {
		return nil, err
	}
	return &bucket, nil
}

func (s *Service) GetBucket(accountScopeID, bucketID string) (*pebblestore.StorageBucketRecord, bool, error) {
	if s == nil || s.store == nil {
		return nil, false, errors.New("storage hub store unconfigured")
	}
	rec, ok, err := s.store.GetStorageBucket(accountScopeID, bucketID)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &rec, true, nil
}

func (s *Service) ListBuckets(accountScopeID string) ([]pebblestore.StorageBucketRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("storage hub store unconfigured")
	}
	return s.store.ListStorageBuckets(accountScopeID)
}

func (s *Service) DeleteBucket(accountScopeID, bucketID string) error {
	if s == nil || s.store == nil {
		return errors.New("storage hub store unconfigured")
	}
	s.driversMu.Lock()
	delete(s.drivers, bucketID)
	s.driversMu.Unlock()
	return s.store.DeleteStorageBucket(accountScopeID, bucketID)
}

func (s *Service) ScanBucket(ctx context.Context, accountScopeID, bucketID string) (*ScanSummary, error) {
	bucket, ok, err := s.GetBucket(accountScopeID, bucketID)
	if err != nil {
		return nil, err
	}
	if !ok || bucket == nil {
		return nil, errors.New("bucket not found")
	}

	driver, err := s.getDriver(*bucket)
	if err != nil {
		return nil, err
	}

	summary := &ScanSummary{
		BucketID:  bucketID,
		ScannedAt: s.now().UnixMilli(),
	}

	// 1. Scan workers
	prefix := bucket.Prefix
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	workersPrefix := prefix + "workers/"

	workerKeys, err := driver.List(ctx, workersPrefix)
	if err != nil {
		return nil, fmt.Errorf("list workers in bucket: %w", err)
	}

	discoveredWorkersMap := make(map[string]*pebblestore.StorageDiscoveredWorkerRecord)

	for _, k := range workerKeys {
		rel := strings.TrimPrefix(k, workersPrefix)
		parts := strings.Split(rel, "/")
		if len(parts) == 0 || parts[0] == "" {
			continue
		}
		workerID := parts[0]

		rec, exists := discoveredWorkersMap[workerID]
		if !exists {
			// Check if already in Pebble store
			existingRec, ok, _ := s.store.GetStorageDiscoveredWorker(accountScopeID, workerID)
			if ok {
				rec = &existingRec
			} else {
				rec = &pebblestore.StorageDiscoveredWorkerRecord{
					WorkerID:       workerID,
					BucketID:       bucketID,
					AccountScopeID: accountScopeID,
					Name:           workerID,
					DiscoveredAt:   s.now().UnixMilli(),
				}
				summary.WorkersAdded = append(summary.WorkersAdded, workerID)
			}
			discoveredWorkersMap[workerID] = rec
		}

		// Read worker manifest if this key is worker.json
		if len(parts) == 2 && parts[1] == "worker.json" {
			data, err := driver.Get(ctx, k)
			if err == nil {
				var wManifest struct {
					Name        string   `json:"name"`
					Description string   `json:"description"`
					Version     string   `json:"version"`
					Tags        []string `json:"tags"`
				}
				if json.Unmarshal(data, &wManifest) == nil {
					if wManifest.Name != "" {
						rec.Name = wManifest.Name
					}
					rec.Description = wManifest.Description
					rec.Version = wManifest.Version
					rec.Tags = wManifest.Tags
				}
			}
		}

		// Check for base context
		if len(parts) >= 2 && parts[1] == "base" {
			rec.HasBaseContext = true
		}

		// Check for session states
		if len(parts) >= 3 && parts[1] == "sessions" {
			sessionID := parts[2]
			if len(parts) == 4 && parts[3] == "state.json" {
				data, err := driver.Get(ctx, k)
				if err == nil {
					var sState struct {
						Status   string `json:"status"`
						Progress int    `json:"progress"`
						Step     string `json:"step"`
					}
					if json.Unmarshal(data, &sState) == nil {
						rec.LastSessionID = sessionID
						rec.LastStatus = sState.Status
						rec.LastProgress = sState.Progress
						rec.LastStep = sState.Step
						rec.LastActivityAt = s.now().UnixMilli()
					}
				}
			}
		}
	}

	summary.WorkersFound = len(discoveredWorkersMap)

	// Save discovered workers to pebble
	for _, rec := range discoveredWorkersMap {
		rec.UpdatedAt = s.now().UnixMilli()
		_ = s.store.PutStorageDiscoveredWorker(*rec)
	}

	// 2. Scan deliverables
	deliverablesPrefix := prefix + "deliverables/"
	delivKeys, err := driver.List(ctx, deliverablesPrefix)
	if err == nil {
		for _, k := range delivKeys {
			if !strings.HasSuffix(k, "/manifest.json") {
				continue
			}
			data, err := driver.Get(ctx, k)
			if err != nil {
				continue
			}

			var dManifest struct {
				ID        string                                  `json:"id"`
				WorkerID  string                                  `json:"worker_id"`
				SessionID string                                  `json:"session_id"`
				Title     string                                  `json:"title"`
				Summary   string                                  `json:"summary"`
				Kind      string                                  `json:"kind"`
				Status    string                                  `json:"status"`
				SHA256    string                                  `json:"sha256"`
				Files     []pebblestore.StorageDeliverableFileRef `json:"files"`
				Actions   []pebblestore.NotificationAction        `json:"actions"`
				Payload   map[string]any                          `json:"payload"`
				CreatedAt string                                  `json:"created_at"`
			}
			if err := json.Unmarshal(data, &dManifest); err != nil {
				continue
			}

			if dManifest.ID == "" {
				continue
			}

			// Check if already known
			existingDeliv, known, _ := s.store.GetStorageDiscoveredDeliverable(accountScopeID, dManifest.ID)
			if !known {
				delivRec := pebblestore.StorageDiscoveredDeliverableRecord{
					DeliverableID:  dManifest.ID,
					WorkerID:       dManifest.WorkerID,
					SessionID:      dManifest.SessionID,
					BucketID:       bucketID,
					AccountScopeID: accountScopeID,
					Title:          dManifest.Title,
					Summary:        dManifest.Summary,
					Kind:           dManifest.Kind,
					Status:         dManifest.Status,
					SHA256:         dManifest.SHA256,
					Files:          dManifest.Files,
					Actions:        dManifest.Actions,
					Payload:        dManifest.Payload,
					CreatedAt:      s.now().UnixMilli(),
					UpdatedAt:      s.now().UnixMilli(),
				}
				if delivRec.Status == "" {
					delivRec.Status = "pending_review"
				}
				_ = s.store.PutStorageDiscoveredDeliverable(delivRec)
				summary.DeliverablesNew++

				// If pending review and notifications service is present, notify AI Inbox!
				if delivRec.Status == "pending_review" && s.notifications != nil {
					payload := map[string]any{
						"deliverable_id": dManifest.ID,
						"bucket_id":      bucketID,
						"worker_id":      dManifest.WorkerID,
						"files_count":    len(dManifest.Files),
					}
					if dManifest.SHA256 != "" {
						payload["media_sha256"] = dManifest.SHA256
					}

					importEndpoint := fmt.Sprintf("/v1/storage/deliverables/%s/import", dManifest.ID)
					actions := []pebblestore.NotificationAction{
						{
							ID:         "import_to_worktree",
							Label:      "Import to Worktree",
							ActionType: "post",
							Endpoint:   importEndpoint,
							Variant:    "primary",
						},
					}

					_, _ = s.notifications.SubmitInboxNotificationForAccount(accountScopeID, notification.InboxNotificationInput{
						AccountScopeID: accountScopeID,
						Title:          dManifest.Title,
						Body:           dManifest.Summary,
						Category:       pebblestore.NotificationCategoryInbox,
						Kind:           pebblestore.NotificationKindAIDeliverable,
						WorkerID:       dManifest.WorkerID,
						OriginLabel:    fmt.Sprintf("Bucket: %s (%s)", bucket.Name, dManifest.WorkerID),
						Verified:       true,
						Payload:        payload,
						Actions:        actions,
					})
				}
			} else {
				// Update status if changed in manifest
				if dManifest.Status != "" && dManifest.Status != existingDeliv.Status {
					existingDeliv.Status = dManifest.Status
					existingDeliv.UpdatedAt = s.now().UnixMilli()
					_ = s.store.PutStorageDiscoveredDeliverable(existingDeliv)
				}
			}
		}
	}

	return summary, nil
}

func (s *Service) ListDiscoveredWorkers(accountScopeID string) ([]pebblestore.StorageDiscoveredWorkerRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("storage hub store unconfigured")
	}
	return s.store.ListStorageDiscoveredWorkers(accountScopeID)
}

func (s *Service) ImportWorker(ctx context.Context, accountScopeID, workerID string) (*pebblestore.StorageDiscoveredWorkerRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("storage hub store unconfigured")
	}
	rec, ok, err := s.store.GetStorageDiscoveredWorker(accountScopeID, workerID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("discovered worker not found")
	}

	rec.Imported = true
	rec.ImportedAt = s.now().UnixMilli()
	rec.UpdatedAt = rec.ImportedAt

	if err := s.store.PutStorageDiscoveredWorker(rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *Service) ListDiscoveredDeliverables(accountScopeID string) ([]pebblestore.StorageDiscoveredDeliverableRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("storage hub store unconfigured")
	}
	return s.store.ListStorageDiscoveredDeliverables(accountScopeID)
}

func (s *Service) ImportDeliverable(ctx context.Context, accountScopeID, deliverableID string, targetDir string) (*pebblestore.StorageDiscoveredDeliverableRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("storage hub store unconfigured")
	}
	rec, ok, err := s.store.GetStorageDiscoveredDeliverable(accountScopeID, deliverableID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("discovered deliverable not found")
	}

	bucket, ok, err := s.GetBucket(accountScopeID, rec.BucketID)
	if err != nil || !ok || bucket == nil {
		return nil, errors.New("deliverable source bucket not found")
	}

	driver, err := s.getDriver(*bucket)
	if err != nil {
		return nil, err
	}

	// Download each file in the deliverable to targetDir if specified
	if targetDir != "" {
		cleanTarget := filepath.Clean(targetDir)
		for _, f := range rec.Files {
			fileData, err := driver.Get(ctx, f.Path)
			if err != nil {
				return nil, fmt.Errorf("read deliverable file %s from bucket: %w", f.Name, err)
			}

			// Verify SHA-256
			h := sha256.Sum256(fileData)
			actualHex := hex.EncodeToString(h[:])
			if f.SHA256 != "" && actualHex != f.SHA256 {
				return nil, fmt.Errorf("SHA-256 integrity mismatch for file %s: expected %s, got %s", f.Name, f.SHA256, actualHex)
			}

			destPath := filepath.Join(cleanTarget, f.Name)
			if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(destPath, fileData, 0644); err != nil {
				return nil, fmt.Errorf("write deliverable file %s: %w", destPath, err)
			}
		}
	}

	rec.Status = "accepted"
	rec.ImportedAt = s.now().UnixMilli()
	rec.UpdatedAt = rec.ImportedAt
	_ = s.store.PutStorageDiscoveredDeliverable(rec)

	// Also update manifest in bucket if possible
	manifestKey := fmt.Sprintf("deliverables/%s/%s/manifest.json", rec.WorkerID, rec.DeliverableID)
	if bucket.Prefix != "" {
		p := bucket.Prefix
		if !strings.HasSuffix(p, "/") {
			p += "/"
		}
		manifestKey = p + manifestKey
	}
	manifestBytes, err := driver.Get(ctx, manifestKey)
	if err == nil {
		var m map[string]any
		if json.Unmarshal(manifestBytes, &m) == nil {
			m["status"] = "accepted"
			m["updated_at"] = s.now().Format(time.RFC3339)
			if updatedBytes, err := json.MarshalIndent(m, "", "  "); err == nil {
				_ = driver.Put(ctx, manifestKey, updatedBytes, "application/json")
			}
		}
	}

	return &rec, nil
}
