package pebblestore

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// AutomationV2Occurrence is an immutable accepted instruction snapshot plus a
// durable execution receipt. Authoring sessions are never execution sessions.
type AutomationV2Occurrence struct {
	ID          string             `json:"id"`
	Record      AutomationV2Record `json:"accepted"`
	DueAt       int64              `json:"due_at"`
	AdmittedAt  int64              `json:"admitted_at"`
	SessionID   string             `json:"session_id"`
	RunID       string             `json:"run_id"`
	State       string             `json:"state"`
	Version     uint64             `json:"version"`
	ObservedAt  int64              `json:"observed_at"`
	Detail      string             `json:"detail,omitempty"`
	Preparation *SessionSnapshot   `json:"preparation,omitempty"`
}

type automationV2ExecutionMutation struct {
	action     string
	expected   AutomationV2Record
	occurrence AutomationV2Occurrence
	now        int64
}

// AutomationV2NextDue is strictly after `after`; interval anchors are acceptance
// instants, not process startup times. Cron enumerates UTC instants against the
// requested zone: nonexistent local minutes never fire; repeated minutes are
// distinct slots. The eight-year search bound covers leap-day schedules.
func AutomationV2NextDue(policy AutomationV2Settings, anchor, after int64) (int64, error) {
	if err := ValidateAutomationV2Settings(&policy, 0); err != nil {
		return 0, err
	}
	if after < anchor {
		after = anchor
	}
	if policy.Schedule.Kind == "interval" {
		step := policy.Schedule.IntervalSeconds * 1000
		return anchor + ((after-anchor)/step+1)*step, nil
	}
	loc, err := time.LoadLocation(policy.Schedule.Timezone)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(policy.Schedule.Cron)
	match := func(i, value int) bool {
		f := fields[i]
		if f == "*" {
			return true
		}
		if strings.HasPrefix(f, "*/") {
			n, _ := strconv.Atoi(f[2:])
			base := 0
			if i == 2 || i == 3 {
				base = 1
			}
			return (value-base)%n == 0
		}
		n, _ := strconv.Atoi(f)
		return value == n
	}
	t := time.UnixMilli(after).UTC().Truncate(time.Minute).Add(time.Minute)
	end := t.AddDate(8, 0, 0)
	for t.Before(end) {
		local := t.In(loc)
		if match(3, int(local.Month())) && match(2, local.Day()) && match(4, int(local.Weekday())) && match(1, local.Hour()) && match(0, local.Minute()) {
			return t.UnixMilli(), nil
		}
		t = t.Add(time.Minute)
	}
	return 0, errors.New("cron has no occurrence within eight years")
}

func automationV2OccurrencePrefix(account, session string) string {
	return automationV2Key("occurrence", account, session) + "/"
}
func automationV2OccurrenceKey(o AutomationV2Occurrence) string {
	return automationV2OccurrencePrefix(o.Record.AccountID, o.Record.SessionID) + o.ID
}
func automationV2PendingKey(o AutomationV2Occurrence) string {
	return automationV2Key("pending", o.Record.AccountID, o.Record.SessionID) + "/" + o.ID
}
func AutomationV2Terminal(state string) bool {
	return state == "succeeded" || state == "failed" || state == "cancelled"
}

func (s *SessionStore) GetAutomationV2Occurrence(account, user, workspace, session, id string) (AutomationV2Occurrence, bool, error) {
	if _, err := s.automationV2Owner(account, user, workspace, session); err != nil {
		return AutomationV2Occurrence{}, false, err
	}
	var o AutomationV2Occurrence
	ok, err := s.store.GetJSON(automationV2OccurrencePrefix(account, session)+id, &o)
	if err == nil && ok {
		if o.ID != id || o.SessionID != "av2-execution-"+id || o.RunID != "av2-run:"+id {
			return o, false, ErrAutomationV2Conflict
		}
		err = validateAutomationV2Integrity(o.Record.AutomationV2Proposal, account, user, workspace, session)
	}
	return o, ok, err
}

// ScanAutomationV2Accepted is daemon-only catalog traversal, bounded in rows and
// bytes. Each returned row still requires current owner/authorization checks.
func (s *SessionStore) ScanAutomationV2Accepted(after string) ([]AutomationV2Record, string, error) {
	prefix := "automation/v2/accepted/"
	if after != "" && !strings.HasPrefix(after, prefix) {
		return nil, "", ErrAutomationV2Conflict
	}
	lower := prefix
	if after != "" {
		lower = after + "\x00"
	}
	it, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(lower), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return nil, "", err
	}
	defer it.Close()
	rows := []AutomationV2Record{}
	next := ""
	size := 0
	for valid := it.First(); valid; valid = it.Next() {
		if len(rows) >= 10 || size >= 1024*1024 {
			return rows, next, nil
		}
		var r AutomationV2Record
		if err := json.Unmarshal(it.Value(), &r); err != nil {
			return nil, "", err
		}
		rows = append(rows, r)
		next = string(it.Key())
		size += len(it.Value())
	}
	return rows, "", it.Error()
}

func (s *SessionStore) ListAutomationV2Occurrences(account, user, workspace, session, after string, pending bool, limit int) ([]AutomationV2Occurrence, string, error) {
	if _, err := s.automationV2Owner(account, user, workspace, session); err != nil {
		return nil, "", err
	}
	if limit < 1 || limit > 25 {
		return nil, "", ErrAutomationV2Conflict
	}
	prefix := automationV2OccurrencePrefix(account, session)
	if pending {
		prefix = automationV2Key("pending", account, session) + "/"
	}
	if after != "" && !strings.HasPrefix(after, prefix) {
		return nil, "", ErrAutomationV2Conflict
	}
	lower := prefix
	if after != "" {
		lower = after + "\x00"
	}
	it, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(lower), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return nil, "", err
	}
	defer it.Close()
	rows := []AutomationV2Occurrence{}
	next := ""
	size := 0
	for valid := it.First(); valid; valid = it.Next() {
		if len(rows) >= limit || size >= 1024*1024 {
			return rows, next, nil
		}
		var o AutomationV2Occurrence
		if err := json.Unmarshal(it.Value(), &o); err != nil {
			return nil, "", err
		}
		if err := validateAutomationV2Integrity(o.Record.AutomationV2Proposal, account, user, workspace, session); err != nil {
			return nil, "", err
		}
		rows = append(rows, o)
		next = string(it.Key())
		size += len(it.Value())
	}
	return rows, "", it.Error()
}

func (s *SessionStore) automationV2ExecutionApply(m *automationV2ExecutionMutation) error {
	r := m.expected
	// A changing observation is its own mutation; no caller-controlled event or key.
	body, _ := json.Marshal(m.occurrence)
	hash := fmt.Sprintf("%x", sha256.Sum256(body))
	request := fmt.Sprintf("av2:%s:%d:%d:%s", m.action, r.Generation, m.now, hash)
	_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: r.SessionID, AccountScopeID: r.AccountID, UserID: r.UserID, Kind: V3SessionMutationUpdateMetadata, EventType: "session.automation_v2." + m.action, ClientRequestID: request, PayloadHash: request, NowUnixMs: m.now, automationV2: &automationV2Mutation{execution: m}})
	return err
}

func (s *SessionStore) AdmitAutomationV2(r AutomationV2Record, now int64) (AutomationV2Occurrence, error) {
	s.store.sessionMutations.automationV2Mu.Lock()
	defer s.store.sessionMutations.automationV2Mu.Unlock()
	m := &automationV2ExecutionMutation{action: "admitted", expected: r, now: now}
	err := s.automationV2ExecutionApply(m)
	return m.occurrence, err
}

// Control actions never authorize new instructions. Resume uses the existing
// accepted revision/expiry; cancel_future leaves admitted work alone, cancel_all
// durably fences every already-admitted generation before host cancellation.
func (s *SessionStore) ControlAutomationV2(account, user, workspace, session string, generation uint64, action string, now int64) (AutomationV2Record, error) {
	s.store.sessionMutations.automationV2Mu.Lock()
	defer s.store.sessionMutations.automationV2Mu.Unlock()
	r, ok, err := s.GetAutomationV2Record(account, user, workspace, session)
	if err != nil {
		return r, err
	}
	if !ok || r.Generation != generation {
		return r, ErrAutomationV2Conflict
	}
	switch action {
	case "pause", "resume", "cancel_future", "cancel_all":
	default:
		return r, ErrAutomationV2Conflict
	}
	if err := s.automationV2ExecutionApply(&automationV2ExecutionMutation{action: action, expected: r, now: now}); err != nil {
		return r, err
	}
	r, _, err = s.GetAutomationV2Record(account, user, workspace, session)
	return r, err
}

// WithAutomationV2Dispatch serializes cancellation with preparation/intent
// admission. It never holds a session/worktree lock across the callback.
func (s *SessionStore) WithAutomationV2Dispatch(o AutomationV2Occurrence, fn func() error) error {
	s.store.sessionMutations.automationV2Mu.Lock()
	defer s.store.sessionMutations.automationV2Mu.Unlock()
	current, ok, err := s.GetAutomationV2Occurrence(o.Record.AccountID, o.Record.UserID, o.Record.WorkspaceID, o.Record.SessionID, o.ID)
	if err != nil {
		return err
	}
	r, found, err := s.GetAutomationV2Record(o.Record.AccountID, o.Record.UserID, o.Record.WorkspaceID, o.Record.SessionID)
	if err != nil {
		return err
	}
	if !ok || !found || !reflect.DeepEqual(current, o) || AutomationV2Terminal(current.State) || int64(o.Record.Generation) <= r.CancelThrough {
		return ErrAutomationV2Conflict
	}
	return fn()
}

func (s *SessionStore) ObserveAutomationV2(o AutomationV2Occurrence, state, detail string, now int64) error {
	if len(detail) > 512 {
		detail = detail[:512]
	}
	o.State, o.Detail = state, detail
	return s.automationV2ExecutionApply(&automationV2ExecutionMutation{action: "observed", expected: o.Record, occurrence: o, now: now})
}

func (s *SessionStore) prepareAutomationV2Execution(in *V3SessionMutationInput) error {
	m := in.automationV2.execution
	expected := m.expected
	r, ok, err := s.GetAutomationV2Record(in.AccountScopeID, in.UserID, expected.WorkspaceID, in.SessionID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrAutomationV2Conflict
	}
	if m.action != "observed" && m.action != "prepared" && (r.AutomationV2Review != expected.AutomationV2Review || r.Generation != expected.Generation) {
		return ErrAutomationV2Conflict
	}
	now := m.now
	if now <= 0 {
		return ErrAutomationV2Conflict
	}
	switch m.action {
	case "admitted":
		if !r.Enabled || r.Cancelled || r.NextDueAt <= 0 || r.NextDueAt > now || (r.Authorization.Kind == "at" && (now >= r.Authorization.ExpiresAt || r.NextDueAt >= r.Authorization.ExpiresAt)) {
			return ErrAutomationV2Conflict
		}
		if r.Document.AutomationV2.Overlap == "serialize" {
			rows, _, err := s.ListAutomationV2Occurrences(r.AccountID, r.UserID, r.WorkspaceID, r.SessionID, "", true, 1)
			if err != nil {
				return err
			}
			if len(rows) > 0 {
				return ErrAutomationV2Conflict
			}
		}
		admittedSnapshot := r
		due := r.NextDueAt
		next, err := AutomationV2NextDue(*r.Document.AutomationV2, r.AcceptedAt, due)
		if err != nil {
			return err
		}
		// Late sweeps coalesce to one original durable slot, or skip entirely once
		// another slot is due. Neither policy replays an unbounded backlog.
		skipped := next <= now && r.Document.AutomationV2.Missed == "skip"
		if next <= now {
			next, err = AutomationV2NextDue(*r.Document.AutomationV2, r.AcceptedAt, now)
			if err != nil {
				return err
			}
		}
		r.NextDueAt = next
		if !skipped {
			id := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", r.AutomationID, r.Revision, due))))
			var prior AutomationV2Occurrence
			if found, err := s.store.GetJSON(automationV2OccurrencePrefix(r.AccountID, r.SessionID)+id, &prior); err != nil {
				return err
			} else if found {
				return ErrAutomationV2Conflict
			}
			m.occurrence = AutomationV2Occurrence{ID: id, Record: admittedSnapshot, DueAt: due, AdmittedAt: now, SessionID: "av2-execution-" + id, RunID: "av2-run:" + id, State: "admitted", Version: 1, ObservedAt: now}
		}
	case "pause", "resume", "cancel_future", "cancel_all":
		if r.Cancelled && m.action != "cancel_all" {
			return ErrAutomationV2Conflict
		}
		if m.action == "resume" {
			if r.Authorization.Kind == "at" && now >= r.Authorization.ExpiresAt {
				return ErrAutomationV2Conflict
			}
			r.Enabled = true
			r.NextDueAt, err = AutomationV2NextDue(*r.Document.AutomationV2, r.AcceptedAt, now)
			if err != nil {
				return err
			}
		} else {
			r.Enabled = false
		}
		if m.action == "cancel_future" || m.action == "cancel_all" {
			r.Cancelled = true
		}
		if m.action == "cancel_all" {
			r.CancelThrough = int64(r.Generation)
		}
		r.Generation++
	case "prepared":
		o, found, err := s.GetAutomationV2Occurrence(r.AccountID, r.UserID, r.WorkspaceID, r.SessionID, m.occurrence.ID)
		if err != nil {
			return err
		}
		snapshot := m.occurrence.Preparation
		if !found || o.Version != m.occurrence.Version || o.Preparation != nil || snapshot == nil || snapshot.ID != o.SessionID || snapshot.AccountScopeID != r.AccountID || snapshot.UserID != r.UserID || !snapshot.WorktreeEnabled || int64(o.Record.Generation) <= r.CancelThrough {
			return ErrAutomationV2Conflict
		}
		o.Preparation = snapshot
		o.Version++
		o.ObservedAt = now
		m.occurrence = o
	case "observed":
		o, found, err := s.GetAutomationV2Occurrence(r.AccountID, r.UserID, r.WorkspaceID, r.SessionID, m.occurrence.ID)
		if err != nil {
			return err
		}
		if !found || o.Version != m.occurrence.Version || AutomationV2Terminal(o.State) {
			return ErrAutomationV2Conflict
		}
		switch m.occurrence.State {
		case "admitted", "running", "succeeded", "failed", "cancelled", "unavailable":
		default:
			return ErrAutomationV2Conflict
		}
		o.State, o.Detail, o.ObservedAt = m.occurrence.State, m.occurrence.Detail, now
		o.Version++
		m.occurrence = o
	default:
		return ErrAutomationV2Conflict
	}
	in.automationV2.record = r
	in.EventPayload, err = json.Marshal(map[string]any{"automation_id": r.AutomationID, "revision": r.Revision, "generation": r.Generation, "occurrence_id": m.occurrence.ID, "state": m.occurrence.State})
	return err
}

func (s *SessionStore) setAutomationV2ExecutionInBatch(batch *pebble.Batch, in V3SessionMutationInput) error {
	m := in.automationV2.execution
	r := in.automationV2.record
	if m.action != "observed" && m.action != "prepared" {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if err = batch.Set([]byte(automationV2Key("accepted", r.AccountID, r.SessionID)), b, nil); err != nil {
			return err
		}
	}
	if m.occurrence.ID != "" {
		o := m.occurrence
		b, err := json.Marshal(o)
		if err != nil {
			return err
		}
		if err = batch.Set([]byte(automationV2OccurrenceKey(o)), b, nil); err != nil {
			return err
		}
		if AutomationV2Terminal(o.State) {
			err = batch.Delete([]byte(automationV2PendingKey(o)), nil)
		} else {
			err = batch.Set([]byte(automationV2PendingKey(o)), b, nil)
		}
		if err != nil {
			return err
		}
	}
	if hook := s.store.sessionMutations.beforeAutomationV2Commit; hook != nil {
		return hook(in.SessionID)
	}
	return nil
}

// JournalAutomationV2Preparation is called under WithAutomationV2Dispatch. The
// exact allocated lane/model snapshot survives a session-publication failure.
func (s *SessionStore) JournalAutomationV2Preparation(o AutomationV2Occurrence, snapshot SessionSnapshot) error {
	o.Preparation = &snapshot
	return s.automationV2ExecutionApply(&automationV2ExecutionMutation{action: "prepared", expected: o.Record, occurrence: o, now: time.Now().UnixMilli()})
}
