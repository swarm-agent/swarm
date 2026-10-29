package pebblestore

import "github.com/cockroachdb/pebble"

// sessionMutationCompletion separates locked bookkeeping from durable publication.
// A nil completion preserves the existing synchronous, session-only path.
type sessionMutationCompletion struct {
	needsSync bool
	publish   []func() error
	// taskWaitEnded is a task wait the committed mutation ended; its visible
	// end line is recorded after the commit.
	taskWaitEnded *projectTaskWaitEnd
}

func (c *sessionMutationCompletion) afterDurable(publish func() error) error {
	if c == nil || !c.needsSync {
		return publish()
	}
	c.publish = append(c.publish, publish)
	return nil
}

func (s *Store) syncSessionMutationWAL(sessionID string) error {
	if hook := s.sessionMutations.beforePostUnlockSync; hook != nil {
		if err := hook(sessionID); err != nil {
			return err
		}
	}
	// Pebble v1.1.5 encodes a LogData record even for nil data. Unlike an
	// empty batch, this reaches the commit pipeline and syncs the WAL after
	// all previously applied NoSync batches (including across WAL rotation).
	return s.db.LogData(nil, pebble.Sync)
}
