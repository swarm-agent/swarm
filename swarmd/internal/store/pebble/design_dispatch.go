package pebblestore

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cockroachdb/pebble"
)

// DesignWake is a coalesced hint only. Durable discovery repairs missed hints.
func (s *Store) DesignWake() <-chan struct{} {
	s.designWakeMu.Lock()
	defer s.designWakeMu.Unlock()
	if s.designWake == nil {
		s.designWake = make(chan struct{}, 1)
	}
	return s.designWake
}
func (s *Store) WakeDesign() {
	s.DesignWake()
	s.designWakeMu.Lock()
	defer s.designWakeMu.Unlock()
	select {
	case s.designWake <- struct{}{}:
	default:
	}
}

// ScanDesignPending is daemon-only discovery across owners. Cursor bounds work
// to 256 keys per page; historical content is never decoded during discovery.
func (s *Store) ScanDesignPending(after string) ([]DesignRequest, string, error) {
	const prefix = "design:v1/"
	if after != "" && !strings.HasPrefix(after, prefix) {
		return nil, "", ErrDesignInvalid
	}
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return nil, "", err
	}
	defer it.Close()
	rows := []DesignRequest{}
	cursor := ""
	start := prefix
	if after != "" {
		start = after + "\x00"
	}
	n := 0
	for ok := it.SeekGE([]byte(start)); ok; {
		if n == 256 {
			return rows, cursor, it.Error()
		}
		n++
		cursor = string(it.Key())
		parts := strings.Split(strings.TrimPrefix(cursor, prefix), "/")
		if len(parts) < 3 {
			ok = it.Next()
			continue
		}
		ownerPrefix := prefix + parts[0] + "/" + parts[1] + "/"
		if parts[2] < "pending" {
			ok = it.SeekGE([]byte(ownerPrefix + "pending/"))
			continue
		}
		if parts[2] > "pending" {
			ok = it.SeekGE([]byte(ownerPrefix + "\xff"))
			continue
		}
		if len(parts) != 4 {
			ok = it.Next()
			continue
		}
		r, err := s.GetDesignRequest(DesignPrincipal{AccountID: parts[0], PrincipalID: parts[1]}, parts[3])
		if err != nil {
			return nil, "", err
		}
		rows = append(rows, r)
		ok = it.Next()
	}
	return rows, "", it.Error()
}

// FailQueuedDesign reports stable pre-allocation failures without inventing
// execution provenance. Capacity waits and revision conflicts must not use it.
func (s *Store) FailQueuedDesign(p DesignPrincipal, id string, revision uint64, candidate int, reason string) (DesignRequest, error) {
	var zero DesignRequest
	if err := designOwner(p); err != nil {
		return zero, err
	}
	if !designID(id) || (reason != "model_unavailable" && reason != "allocation_unavailable" && reason != DesignReplacedReason) {
		return zero, ErrDesignInvalid
	}
	s.designMu.Lock()
	defer s.designMu.Unlock()
	r, err := s.GetDesignRequest(p, id)
	if err != nil {
		return zero, err
	}
	if r.Revision != revision {
		return zero, ErrDesignConflict
	}
	if candidate < 0 || candidate >= len(r.Candidates) {
		return zero, ErrDesignInvalid
	}
	c := &r.Candidates[candidate]
	// A retired queued candidate may keep earlier attempts' provenance; it is
	// failed as a whole without touching them.
	if c.State != DesignQueued || (len(c.Attempts) != 0 && reason != DesignReplacedReason) {
		return zero, ErrDesignConflict
	}
	c.State = DesignFailed
	c.FailureReason = reason
	c.RouterAlert = "Designer execution unavailable; no child was allocated."
	switch reason {
	case "model_unavailable":
		c.RouterAlert = "Designer assignment and account default model are unavailable; configure an account model before submitting again."
	case DesignReplacedReason:
		c.RouterAlert = designReplacedAlert
	}
	b := s.db.NewBatch()
	defer b.Close()
	return s.designCommitRequest(p, r, designKey(p, "dispatch-failure", id+"/"+c.Spec.ArtifactID), struct {
		Revision  uint64
		Candidate int
		Reason    string
	}{revision, candidate, reason}, b)
}

// DesignReplacedReason is the failure reason of manage_design requests the
// daemon retired when swarm_design replaced it.
const DesignReplacedReason = "manage_design_replaced"

// designReplacedAlert is the message every retired candidate carries.
const designReplacedAlert = "manage_design was replaced by swarm_design; nothing was generated for this request. Submit the brief to swarm_design instead."

// RetirePendingDesignRequests is the daemon's one-shot startup pass after
// manage_design was replaced by swarm_design: every still-pending request's
// queued candidates fail and its running or cancel-requested attempts end
// (interrupted or cancelled) with DesignReplacedReason. No provider is
// called and no output is published. It returns how many candidates it ended.
func (s *Store) RetirePendingDesignRequests() (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("design store is not configured")
	}
	ended := 0
	cursor := ""
	for page := 0; page < 1<<16; page++ {
		rows, next, err := s.ScanDesignPending(cursor)
		if err != nil {
			return ended, err
		}
		for _, r := range rows {
			n, err := s.retireDesignRequest(r.Owner, r.ID)
			ended += n
			if err != nil {
				return ended, err
			}
		}
		if next == "" {
			return ended, nil
		}
		cursor = next
	}
	return ended, errors.New("design retirement did not finish scanning pending requests")
}

func (s *Store) retireDesignRequest(p DesignPrincipal, id string) (int, error) {
	ended := 0
	r, err := s.GetDesignRequest(p, id)
	if err != nil {
		return 0, err
	}
	for i := range r.Candidates {
		c := r.Candidates[i]
		switch c.State {
		case DesignQueued:
			r, err = s.FailQueuedDesign(p, id, r.Revision, i, DesignReplacedReason)
		case DesignRunning, DesignCancelRequested:
			if len(c.Attempts) == 0 {
				return ended, fmt.Errorf("design request %s candidate %d is %s without an attempt", id, i, c.State)
			}
			a := c.Attempts[len(c.Attempts)-1]
			state := DesignInterrupted
			if c.State == DesignCancelRequested {
				state = DesignCancelled
			}
			r, err = s.RecordDesignAttempt(p, id, DesignAttemptMutation{
				IdempotencyKey: "retire-" + strconv.Itoa(i) + "-" + strconv.FormatUint(r.Revision, 10), ExpectedRevision: r.Revision, Candidate: i,
				State: state, ChildSessionID: a.ChildSessionID, RunID: a.RunID, ReasonCode: DesignReplacedReason,
			})
		default:
			continue
		}
		if err != nil {
			return ended, fmt.Errorf("retire design request %s candidate %d: %w", id, i, err)
		}
		ended++
	}
	return ended, nil
}
