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
	// LatestDueAt is the latest NextDueAt that is already due (<= now), or
	// zero. A record that becomes due after a sweep always raises it.
	LatestDueAt int64
	// UpcomingDueAt is the earliest NextDueAt still in the future, or zero.
	UpcomingDueAt int64
	// Pending reports durable unfinished occurrences that need observation.
	Pending bool
	// Overflow reports a catalog larger than the bounded hint scan. Due times
	// are then unknown and the caller must page the catalog as before.
	Overflow bool
}

const automationV2ScheduleHintLimit = 10000

// AutomationV2ScheduleHint mirrors the admission predicate of the V2 tick so
// a record the tick would never admit (disabled, cancelled, archived, expired)
// cannot keep a scheduler awake. Every automation is indexed by automation ID
// and by session ID; admission re-reads the session-ID entry, and the daily
// run cap advances only that entry, so it is the authoritative due time.
func (s *SessionStore) AutomationV2ScheduleHint(now int64) (AutomationV2ScheduleHint, error) {
	var hint AutomationV2ScheduleHint
	if s == nil || s.store == nil || s.store.db == nil {
		return hint, errors.New("store is not open")
	}
	type candidate struct {
		record        AutomationV2Record
		authoritative bool
	}
	byAutomation := map[string]candidate{}
	prefix := "automation/v2/accepted/"
	it, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return hint, err
	}
	for valid := it.First(); valid; valid = it.Next() {
		hint.AcceptedKeys++
		if hint.AcceptedKeys > automationV2ScheduleHintLimit {
			hint.Overflow = true
			break
		}
		var r AutomationV2Record
		if err := json.Unmarshal(it.Value(), &r); err != nil {
			_ = it.Close()
			return hint, err
		}
		key := string(it.Key())
		authoritative := key == automationV2Key("accepted", r.AccountID, r.SessionID)
		id := r.AccountID + "\x00" + r.AutomationID
		if r.AutomationID == "" {
			id = key
		}
		if prior, ok := byAutomation[id]; ok && (prior.authoritative || !authoritative) {
			continue
		}
		byAutomation[id] = candidate{record: r, authoritative: authoritative}
	}
	if err := it.Error(); err != nil {
		_ = it.Close()
		return hint, err
	}
	if err := it.Close(); err != nil {
		return hint, err
	}
	if hint.Overflow {
		hint.AcceptedKeys = automationV2ScheduleHintLimit
	} else {
		for _, c := range byAutomation {
			r := c.record
			if !r.Enabled || r.Cancelled || r.Archived || r.NextDueAt <= 0 ||
				(r.Authorization.Kind == "at" && (now >= r.Authorization.ExpiresAt || r.NextDueAt >= r.Authorization.ExpiresAt)) {
				continue
			}
			if hint.EarliestDueAt == 0 || r.NextDueAt < hint.EarliestDueAt {
				hint.EarliestDueAt = r.NextDueAt
			}
			if r.NextDueAt <= now {
				if r.NextDueAt > hint.LatestDueAt {
					hint.LatestDueAt = r.NextDueAt
				}
			} else if hint.UpcomingDueAt == 0 || r.NextDueAt < hint.UpcomingDueAt {
				hint.UpcomingDueAt = r.NextDueAt
			}
		}
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
