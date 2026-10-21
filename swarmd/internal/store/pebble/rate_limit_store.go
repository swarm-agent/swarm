package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/cockroachdb/pebble"
)

// RateLimitBucketPrefix holds token buckets that must survive a daemon restart
// (gateway token and end-user rate limits). A bucket is only state while it is
// below capacity: once ExpiresAtUnixMs passes it has refilled and is deleted.
const RateLimitBucketPrefix = "rate_limit_bucket/"

// RateLimitBucketRecord is one token bucket: Tokens left at LastUnixMs.
type RateLimitBucketRecord struct {
	Key             string  `json:"key"`
	Capacity        int     `json:"capacity"`
	Tokens          float64 `json:"tokens"`
	LastUnixMs      int64   `json:"last_unix_ms"`
	ExpiresAtUnixMs int64   `json:"expires_at_unix_ms"`
}

type RateLimitStore struct {
	store *Store
}

func NewRateLimitStore(store *Store) *RateLimitStore {
	return &RateLimitStore{store: store}
}

// Keys are case-sensitive (callers encode their parts), so they are escaped,
// never lowercased.
func rateLimitBucketKey(key string) string {
	return RateLimitBucketPrefix + url.PathEscape(key)
}

func (s *RateLimitStore) Get(key string) (RateLimitBucketRecord, bool, error) {
	if s == nil || s.store == nil {
		return RateLimitBucketRecord{}, false, errors.New("rate limit store is not configured")
	}
	if strings.TrimSpace(key) == "" {
		return RateLimitBucketRecord{}, false, errors.New("rate limit bucket key is required")
	}
	var record RateLimitBucketRecord
	ok, err := s.store.GetJSON(rateLimitBucketKey(key), &record)
	if err != nil || !ok {
		return RateLimitBucketRecord{}, ok, err
	}
	if record.Key != key {
		return RateLimitBucketRecord{}, false, fmt.Errorf("rate limit bucket %q is corrupt", key)
	}
	return record, true, nil
}

// PutAll writes buckets in one synced batch, so a spend recorded across several
// buckets survives a crash together or not at all.
func (s *RateLimitStore) PutAll(records []RateLimitBucketRecord) error {
	if s == nil || s.store == nil {
		return errors.New("rate limit store is not configured")
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	for _, record := range records {
		if strings.TrimSpace(record.Key) == "" {
			return errors.New("rate limit bucket key is required")
		}
		payload, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := batch.Set([]byte(rateLimitBucketKey(record.Key)), payload, nil); err != nil {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
}

// DeleteExpired removes buckets that have refilled by nowUnixMs and reports how
// many remain.
func (s *RateLimitStore) DeleteExpired(nowUnixMs int64) (int, error) {
	if s == nil || s.store == nil {
		return 0, errors.New("rate limit store is not configured")
	}
	var expired []string
	live := 0
	const iterateAll = int(^uint(0) >> 1)
	err := s.store.IteratePrefix(RateLimitBucketPrefix, iterateAll, func(key string, value []byte) error {
		var record RateLimitBucketRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("rate limit bucket %q is corrupt: %w", key, err)
		}
		if record.ExpiresAtUnixMs <= nowUnixMs {
			expired = append(expired, key)
			return nil
		}
		live++
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(expired) == 0 {
		return live, nil
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	for _, key := range expired {
		if err := batch.Delete([]byte(key), nil); err != nil {
			return 0, err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, err
	}
	return live, nil
}
