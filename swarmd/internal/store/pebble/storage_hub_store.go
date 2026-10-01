package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *Store) PutStorageBucket(bucket StorageBucketRecord) error {
	if s == nil || s.db == nil {
		return errors.New("pebble store is closed")
	}
	accountScopeID := strings.TrimSpace(bucket.AccountScopeID)
	bucketID := strings.TrimSpace(bucket.ID)
	if accountScopeID == "" || bucketID == "" {
		return errors.New("account_scope_id and bucket id are required")
	}
	if bucket.CreatedAt <= 0 {
		bucket.CreatedAt = time.Now().UnixMilli()
	}
	bucket.UpdatedAt = time.Now().UnixMilli()

	key := KeyStorageBucket(accountScopeID, bucketID)
	return s.PutJSON(key, bucket)
}

func (s *Store) GetStorageBucket(accountScopeID, bucketID string) (StorageBucketRecord, bool, error) {
	if s == nil || s.db == nil {
		return StorageBucketRecord{}, false, errors.New("pebble store is closed")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	bucketID = strings.TrimSpace(bucketID)
	if accountScopeID == "" || bucketID == "" {
		return StorageBucketRecord{}, false, errors.New("account_scope_id and bucket id are required")
	}

	key := KeyStorageBucket(accountScopeID, bucketID)
	var rec StorageBucketRecord
	ok, err := s.GetJSON(key, &rec)
	return rec, ok, err
}

func (s *Store) ListStorageBuckets(accountScopeID string) ([]StorageBucketRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("pebble store is closed")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account_scope_id is required")
	}

	prefix := KeyStorageBucketPrefix(accountScopeID)
	var list []StorageBucketRecord
	err := s.IteratePrefix(prefix, 1000, func(_ string, value []byte) error {
		var rec StorageBucketRecord
		if err := json.Unmarshal(value, &rec); err != nil {
			return fmt.Errorf("unmarshal bucket: %w", err)
		}
		if rec.AccountScopeID == accountScopeID {
			list = append(list, rec)
		}
		return nil
	})
	return list, err
}

func (s *Store) DeleteStorageBucket(accountScopeID, bucketID string) error {
	if s == nil || s.db == nil {
		return errors.New("pebble store is closed")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	bucketID = strings.TrimSpace(bucketID)
	if accountScopeID == "" || bucketID == "" {
		return errors.New("account_scope_id and bucket id are required")
	}

	key := KeyStorageBucket(accountScopeID, bucketID)
	return s.Delete(key)
}

func (s *Store) PutStorageDiscoveredWorker(worker StorageDiscoveredWorkerRecord) error {
	if s == nil || s.db == nil {
		return errors.New("pebble store is closed")
	}
	accountScopeID := strings.TrimSpace(worker.AccountScopeID)
	workerID := strings.TrimSpace(worker.WorkerID)
	if accountScopeID == "" || workerID == "" {
		return errors.New("account_scope_id and worker_id are required")
	}
	if worker.DiscoveredAt <= 0 {
		worker.DiscoveredAt = time.Now().UnixMilli()
	}
	worker.UpdatedAt = time.Now().UnixMilli()

	key := KeyStorageWorker(accountScopeID, workerID)
	return s.PutJSON(key, worker)
}

func (s *Store) GetStorageDiscoveredWorker(accountScopeID, workerID string) (StorageDiscoveredWorkerRecord, bool, error) {
	if s == nil || s.db == nil {
		return StorageDiscoveredWorkerRecord{}, false, errors.New("pebble store is closed")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	workerID = strings.TrimSpace(workerID)
	if accountScopeID == "" || workerID == "" {
		return StorageDiscoveredWorkerRecord{}, false, errors.New("account_scope_id and worker_id are required")
	}

	key := KeyStorageWorker(accountScopeID, workerID)
	var rec StorageDiscoveredWorkerRecord
	ok, err := s.GetJSON(key, &rec)
	return rec, ok, err
}

func (s *Store) ListStorageDiscoveredWorkers(accountScopeID string) ([]StorageDiscoveredWorkerRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("pebble store is closed")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account_scope_id is required")
	}

	prefix := KeyStorageWorkerPrefix(accountScopeID)
	var list []StorageDiscoveredWorkerRecord
	err := s.IteratePrefix(prefix, 1000, func(_ string, value []byte) error {
		var rec StorageDiscoveredWorkerRecord
		if err := json.Unmarshal(value, &rec); err != nil {
			return fmt.Errorf("unmarshal discovered worker: %w", err)
		}
		if rec.AccountScopeID == accountScopeID {
			list = append(list, rec)
		}
		return nil
	})
	return list, err
}

func (s *Store) DeleteStorageDiscoveredWorker(accountScopeID, workerID string) error {
	if s == nil || s.db == nil {
		return errors.New("pebble store is closed")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	workerID = strings.TrimSpace(workerID)
	if accountScopeID == "" || workerID == "" {
		return errors.New("account_scope_id and worker_id are required")
	}

	key := KeyStorageWorker(accountScopeID, workerID)
	return s.Delete(key)
}

func (s *Store) PutStorageDiscoveredDeliverable(deliverable StorageDiscoveredDeliverableRecord) error {
	if s == nil || s.db == nil {
		return errors.New("pebble store is closed")
	}
	accountScopeID := strings.TrimSpace(deliverable.AccountScopeID)
	deliverableID := strings.TrimSpace(deliverable.DeliverableID)
	if accountScopeID == "" || deliverableID == "" {
		return errors.New("account_scope_id and deliverable_id are required")
	}
	if deliverable.CreatedAt <= 0 {
		deliverable.CreatedAt = time.Now().UnixMilli()
	}
	deliverable.UpdatedAt = time.Now().UnixMilli()

	key := KeyStorageDeliverable(accountScopeID, deliverableID)
	return s.PutJSON(key, deliverable)
}

func (s *Store) GetStorageDiscoveredDeliverable(accountScopeID, deliverableID string) (StorageDiscoveredDeliverableRecord, bool, error) {
	if s == nil || s.db == nil {
		return StorageDiscoveredDeliverableRecord{}, false, errors.New("pebble store is closed")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	deliverableID = strings.TrimSpace(deliverableID)
	if accountScopeID == "" || deliverableID == "" {
		return StorageDiscoveredDeliverableRecord{}, false, errors.New("account_scope_id and deliverable_id are required")
	}

	key := KeyStorageDeliverable(accountScopeID, deliverableID)
	var rec StorageDiscoveredDeliverableRecord
	ok, err := s.GetJSON(key, &rec)
	return rec, ok, err
}

func (s *Store) ListStorageDiscoveredDeliverables(accountScopeID string) ([]StorageDiscoveredDeliverableRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("pebble store is closed")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account_scope_id is required")
	}

	prefix := KeyStorageDeliverablePrefix(accountScopeID)
	var list []StorageDiscoveredDeliverableRecord
	err := s.IteratePrefix(prefix, 1000, func(_ string, value []byte) error {
		var rec StorageDiscoveredDeliverableRecord
		if err := json.Unmarshal(value, &rec); err != nil {
			return fmt.Errorf("unmarshal discovered deliverable: %w", err)
		}
		if rec.AccountScopeID == accountScopeID {
			list = append(list, rec)
		}
		return nil
	})
	return list, err
}
