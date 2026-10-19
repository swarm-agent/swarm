package pebblestore

import (
	"encoding/json"
	"errors"

	"github.com/cockroachdb/pebble"
)

// AutomationV2ScheduleHint summarizes accepted V2 automations so a scheduler
// can sleep until the earliest admission instead of sweeping every second.
// It is an estimate: admission still re-reads and fences every record.
type AutomationV2ScheduleHint struct {
	// AcceptedKeys counts raw accepted index entries; a sweep pages over them.
	AcceptedKeys int
	// EarliestDueAt is the earliest NextDueAt the sweep would admit, or zero.
	EarliestDueAt int64
	// Pending reports durable unfinished occurrences that need observation.
	Pending bool
}

const automationV2ScheduleHintLimit = 10000

// AutomationV2ScheduleHint mirrors the admission predicate of the V2 tick so
// a record the tick would never admit (disabled, cancelled, archived, expired)
// cannot keep a scheduler awake.
func (s *SessionStore) AutomationV2ScheduleHint(now int64) (AutomationV2ScheduleHint, error) {
	var hint AutomationV2ScheduleHint
	if s == nil || s.store == nil || s.store.db == nil {
		return hint, errors.New("store is not open")
	}
	prefix := "automation/v2/accepted/"
	it, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return hint, err
	}
	for valid := it.First(); valid; valid = it.Next() {
		hint.AcceptedKeys++
		if hint.AcceptedKeys > automationV2ScheduleHintLimit {
			_ = it.Close()
			return hint, errors.New("automation v2 schedule hint exceeds bounded catalog")
		}
		var r AutomationV2Record
		if err := json.Unmarshal(it.Value(), &r); err != nil {
			_ = it.Close()
			return hint, err
		}
		if !r.Enabled || r.Cancelled || r.Archived || r.NextDueAt <= 0 || (r.Authorization.Kind == "at" && now >= r.Authorization.ExpiresAt) {
			continue
		}
		if hint.EarliestDueAt == 0 || r.NextDueAt < hint.EarliestDueAt {
			hint.EarliestDueAt = r.NextDueAt
		}
	}
	if err := it.Error(); err != nil {
		_ = it.Close()
		return hint, err
	}
	if err := it.Close(); err != nil {
		return hint, err
	}
	pendingPrefix := "automation/v2/pending/"
	pending, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(pendingPrefix), UpperBound: []byte(pendingPrefix + "\xff")})
	if err != nil {
		return hint, err
	}
	hint.Pending = pending.First()
	if err := pending.Error(); err != nil {
		_ = pending.Close()
		return hint, err
	}
	return hint, pending.Close()
}
