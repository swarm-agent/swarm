package pebblestore

import (
	"encoding/json"
	"errors"
	"strings"
)

// Worktree removal states recorded on a finished worker run or project task.
const (
	WorktreeRemovalRemoved = "removed"
	WorktreeRemovalFailed  = "failed"
)

// WorktreeRemoval records Swarm removing a finished job's checkout after its
// work landed: the checkout at Path is gone, its Branch is kept at Head, which
// is contained in TargetBranch. Trigger says what removed it ("landed": right
// after Swarm landed the work; "sweep": the periodic sweep). A failed removal
// keeps the checkout and records the error, the attempts made and when the
// next one may run; failures are never retried in a tight loop.
type WorktreeRemoval struct {
	State         string `json:"state"`
	Path          string `json:"path"`
	Branch        string `json:"branch,omitempty"`
	Head          string `json:"head,omitempty"`
	TargetBranch  string `json:"target_branch,omitempty"`
	Trigger       string `json:"trigger,omitempty"`
	At            int64  `json:"at"`
	Attempts      int    `json:"attempts,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	NextAttemptAt int64  `json:"next_attempt_at,omitempty"`
}

// GetWorktreeOwnership reads the durable ownership projection of one exact
// worktree path for daemon-internal maintenance. It authorizes nothing: Git
// consumers still verify registration and content themselves.
func (s *SessionStore) GetWorktreeOwnership(path string) (WorktreeOwnership, bool, error) {
	if s == nil || s.store == nil || !validWorktreePath(path) {
		return WorktreeOwnership{}, false, nil
	}
	var record WorktreeOwnership
	ok, err := s.store.GetJSON(worktreeOwnershipKey(path), &record)
	if err != nil || !ok {
		return WorktreeOwnership{}, false, err
	}
	return record, true, nil
}

// ListEveryActiveSessionLifecycle returns every active session lifecycle,
// paging through all lifecycle records (active or not) so none is missed
// past a page limit, unlike ListActiveSessionLifecycles. A record that cannot
// be read fails the whole list: callers deciding that a checkout is unused
// must not act on a partial picture.
func (s *SessionStore) ListEveryActiveSessionLifecycle() ([]SessionLifecycleSnapshot, error) {
	if s == nil || s.store == nil || s.store.db == nil || s.store.closed.Load() {
		return nil, errors.New("pebble store is closed")
	}
	const page = 1000
	var out []SessionLifecycleSnapshot
	start := ""
	for {
		seen, last := 0, ""
		err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: SessionLifecyclePrefix(), StartKey: start, Limit: page}, func(key string, value []byte) (bool, error) {
			seen, last = seen+1, key
			var snapshot SessionLifecycleSnapshot
			if err := json.Unmarshal(value, &snapshot); err != nil {
				return false, err
			}
			snapshot.SessionID = strings.TrimSpace(snapshot.SessionID)
			snapshot.AccountScopeID = strings.TrimSpace(snapshot.AccountScopeID)
			if snapshot.Active && snapshot.SessionID != "" {
				out = append(out, snapshot)
			}
			return true, nil
		})
		if err != nil {
			return nil, err
		}
		if seen < page {
			return out, nil
		}
		start = last + "\x00"
	}
}

// AppendSignalOnce records one activity event exactly once per DedupKey.
func (s *SessionStore) AppendSignalOnce(sig Signal) (Signal, error) {
	if strings.TrimSpace(sig.Source) == "" {
		sig.Source = SignalSourceSwarmd
	}
	return NewSignalStore(s.store).AppendOnce(sig)
}
