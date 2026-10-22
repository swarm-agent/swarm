package pebblestore

import (
	"container/list"
	"strings"
	"sync"

	"github.com/cockroachdb/pebble"
)

const sessionReadCacheLimit = 1024

// Entries include in-flight misses. Eviction discards their identity too, so a
// reader cannot resurrect an evicted generation. No unbounded generation map.
type sessionReadCacheEntry struct {
	key        string
	generation uint64
	session    *SessionSnapshot
}
type sessionReadCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     list.List
}

func (c *sessionReadCache) begin(key string) (*sessionReadCacheEntry, uint64, *SessionSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*list.Element)
	}
	if e := c.entries[key]; e != nil {
		c.lru.MoveToFront(e)
		v := e.Value.(*sessionReadCacheEntry)
		return v, v.generation, v.session
	}
	v := &sessionReadCacheEntry{key: key}
	c.entries[key] = c.lru.PushFront(v)
	if c.lru.Len() > sessionReadCacheLimit {
		last := c.lru.Back()
		delete(c.entries, last.Value.(*sessionReadCacheEntry).key)
		c.lru.Remove(last)
	}
	return v, v.generation, nil
}

func (c *sessionReadCache) fill(v *sessionReadCacheEntry, generation uint64, session SessionSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[v.key]; e != nil && e.Value == v && v.generation == generation {
		v.session = &session
	}
}

func (c *sessionReadCache) invalidate(keys []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range keys {
		if e := c.entries[key]; e != nil {
			v := e.Value.(*sessionReadCacheEntry)
			v.generation++
			v.session = nil
		}
	}
}

// Inspect the actual batch, not the requested mutation: only session-record
// writes invalidate. Pre/post invalidation fences reads overlapping publication,
// including commits which publish data but subsequently report an error.
func (s *Store) commitSessionBatch(batch *pebble.Batch, options *pebble.WriteOptions) error {
	var keys []string
	reader := batch.Reader()
	for {
		kind, key, _, ok, err := reader.Next()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		if (kind == pebble.InternalKeyKindSet || kind == pebble.InternalKeyKindDelete) && strings.HasPrefix(string(key), SessionPrefix()) {
			keys = append(keys, string(key))
		}
	}
	s.sessionReads.invalidate(keys)
	defer s.sessionReads.invalidate(keys)
	return batch.Commit(options)
}
