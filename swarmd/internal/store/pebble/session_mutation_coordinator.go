package pebblestore

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
)

type keyedSessionLock struct {
	mu   sync.Mutex
	refs int
}

type sessionMutationCoordinator struct {
	locksMu sync.Mutex
	locks   map[string]*keyedSessionLock

	outboxMu          sync.Mutex
	outboxCond        *sync.Cond
	outboxInitialized bool
	outboxInitErr     error
	publishedHead     uint64
	allocated         map[uint64]bool
	committed         map[uint64]bool
	publishing        bool
	publicationErr    error

	// publishOutboxHead is a test seam for failures after the mutation batch is
	// already durable but before the separately durable published head advances.
	publishOutboxHead func(store *Store, target uint64) error

	// libraryRepairMu allows unrelated live mutations to maintain disjoint
	// library rows concurrently while excluding the versioned full rebuild.
	libraryRepairMu sync.RWMutex

	// maintenanceMu serializes bounded retention passes. Each pass still commits
	// its row deletions and durable resume state in one atomic Pebble batch.
	maintenanceMu sync.Mutex

	// beforeSessionMaintenanceCommit is a test seam immediately before the
	// atomic retention batch commit. Returning an error must leave rows and
	// maintenance progress unchanged.
	beforeSessionMaintenanceCommit func() error

	// beforeSearchMigrationCommit injects a failure before a per-session search
	// rebuild and its durable migration cursor are committed atomically.
	beforeSearchMigrationCommit func(sessionID string) error

	// beforeMediaStagingBindCommit injects a failure after staging and session
	// authority have passed preflight but before their one atomic batch commits.
	beforeMediaStagingBindCommit func(sessionID string) error

	// beforeDurableCommit is a test observation seam at the store commit boundary.
	beforeDurableCommit func(sessionID string)
	// beforeArtifactV2Commit injects a failure after V2 preflight and before the
	// one batch containing records, event, projection, idempotency, and outbox.
	beforeArtifactV2Commit func(sessionID string) error
	// beforeExecutionEpochCommit injects a pre-commit failure for the canonical
	// compound epoch transition. Returning an error must leave no durable rows.
	beforeExecutionEpochCommit func(sessionID string) error
}

func newSessionMutationCoordinator() *sessionMutationCoordinator {
	c := &sessionMutationCoordinator{
		locks:     make(map[string]*keyedSessionLock),
		allocated: make(map[uint64]bool),
		committed: make(map[uint64]bool),
	}
	c.outboxCond = sync.NewCond(&c.outboxMu)
	return c
}

func (c *sessionMutationCoordinator) lockSessions(sessionIDs ...string) func() {
	ids := make([]string, 0, len(sessionIDs))
	seen := make(map[string]struct{}, len(sessionIDs))
	for _, id := range sessionIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	entries := make([]*keyedSessionLock, 0, len(ids))
	c.locksMu.Lock()
	for _, id := range ids {
		entry := c.locks[id]
		if entry == nil {
			entry = &keyedSessionLock{}
			c.locks[id] = entry
		}
		entry.refs++
		entries = append(entries, entry)
	}
	c.locksMu.Unlock()

	for _, entry := range entries {
		entry.mu.Lock()
	}
	return func() {
		for i := len(entries) - 1; i >= 0; i-- {
			entries[i].mu.Unlock()
		}
		c.locksMu.Lock()
		for i, id := range ids {
			entries[i].refs--
			if entries[i].refs == 0 {
				delete(c.locks, id)
			}
		}
		c.locksMu.Unlock()
	}
}

func (c *sessionMutationCoordinator) initializeOutboxLocked(store *Store) error {
	if c.outboxInitialized {
		return c.outboxInitErr
	}
	c.outboxInitialized = true
	raw, ok, err := store.GetBytes(KeyV3RealtimeOutboxSequence())
	if err != nil {
		c.outboxInitErr = err
		return err
	}
	if ok {
		c.publishedHead, err = bytesToUint64(raw)
		if err != nil {
			c.outboxInitErr = fmt.Errorf("decode v3 realtime outbox sequence: %w", err)
			return c.outboxInitErr
		}
	}
	err = store.IteratePrefix(V3RealtimeOutboxPrefix(), 0, func(key string, _ []byte) error {
		var seq uint64
		if _, scanErr := fmt.Sscanf(key, V3RealtimeOutboxPrefix()+"%d", &seq); scanErr != nil {
			return scanErr
		}
		if seq > c.publishedHead {
			c.committed[seq] = true
		}
		return nil
	})
	if err != nil {
		c.outboxInitErr = fmt.Errorf("recover v3 realtime outbox coordinator: %w", err)
	}
	return c.outboxInitErr
}

func (c *sessionMutationCoordinator) reserveOutbox(store *Store, count int) ([]uint64, error) {
	if count <= 0 {
		return nil, errors.New("v3 realtime outbox reservation count must be positive")
	}
	c.outboxMu.Lock()
	defer c.outboxMu.Unlock()
	if err := c.initializeOutboxLocked(store); err != nil {
		return nil, err
	}
	reserved := make([]uint64, 0, count)
	for seq := c.publishedHead + 1; len(reserved) < count; seq++ {
		if c.allocated[seq] || c.committed[seq] {
			continue
		}
		c.allocated[seq] = true
		reserved = append(reserved, seq)
	}
	return reserved, nil
}

func (c *sessionMutationCoordinator) abandonOutbox(seqs []uint64) {
	c.outboxMu.Lock()
	for _, seq := range seqs {
		delete(c.allocated, seq)
	}
	c.outboxCond.Broadcast()
	c.outboxMu.Unlock()
}

// commitOutbox marks atomically-written outbox rows durable and advances the
// separately durable published head only across a contiguous set of rows. The
// coordinator mutex is never held while Pebble waits for Sync.
func (c *sessionMutationCoordinator) commitOutbox(store *Store, seqs []uint64) error {
	return c.commitOutboxObserved(store, seqs, nil)
}

func (c *sessionMutationCoordinator) commitOutboxObserved(store *Store, seqs []uint64, observe func(logicalBytes int, duration time.Duration)) error {
	if len(seqs) == 0 {
		return nil
	}
	c.outboxMu.Lock()
	for _, seq := range seqs {
		delete(c.allocated, seq)
		c.committed[seq] = true
	}
	// A prior head publication failure is recoverable. Durable outbox rows stay
	// in committed and every later commit/replay gets another publication try.
	c.publicationErr = nil
	// A publisher already outside the mutex will recheck for newly completed
	// contiguous rows before it relinquishes publication ownership.
	if c.publishing {
		c.outboxMu.Unlock()
		return nil
	}
	c.publishing = true
	for {
		target := c.publishedHead
		for c.committed[target+1] {
			target++
		}
		if target == c.publishedHead {
			// This commit is durable but an earlier reservation is still pending.
			// Do not make an unrelated session wait for that earlier fsync.
			c.publishing = false
			c.outboxCond.Broadcast()
			c.outboxMu.Unlock()
			return nil
		}
		c.outboxMu.Unlock()

		var err error
		logicalBytes := 0
		startedAt := time.Now()
		if publish := c.publishOutboxHead; publish != nil {
			err = publish(store, target)
		} else {
			key := []byte(KeyV3RealtimeOutboxSequence())
			value := uint64ToBytes(target)
			logicalBytes = len(key) + len(value)
			err = store.db.Set(key, value, pebble.Sync)
		}
		if err == nil && observe != nil {
			observe(logicalBytes, time.Since(startedAt))
		}

		c.outboxMu.Lock()
		if err != nil {
			c.publishing = false
			c.publicationErr = fmt.Errorf("publish v3 realtime outbox head %d: %w", target, err)
			c.outboxCond.Broadcast()
			c.outboxMu.Unlock()
			return c.publicationErr
		}
		c.publicationErr = nil
		c.publishedHead = target
		for seq := range c.committed {
			if seq <= target {
				delete(c.committed, seq)
			}
		}
		// Include rows that completed while the head Sync was in progress.
	}
}
