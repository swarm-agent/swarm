package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

// The Companion's materialized state is what its durable event log means for
// the conversation now: the current focus, unresolved commitments (a reply
// the person is owed, an approval or confirmation waiting on them, a task
// watch waiting on its tasks), recent topic and decision references, the
// delegated work it follows and recent device/tool and wake results.
//
// It is not a second event history. Its only input is the Companion
// session's own canonical V3 event log (V3SessionEvent, gapless per-session
// sequence, written through ApplyV3SessionMutation by every real call path:
// the person's messages and queued notes, task-watch registrations, outcomes
// and cancellations, tool results, permission and confirmation decisions,
// scheduled and background wakes, run completions). The state records how
// far it has consumed that log (Cursor) and every reference names the exact
// event it came from (CompanionEventSource), so it can always be rebuilt
// from, and checked against, the original sources.
//
// The record lives under the account user's Companion context prefix, so it
// is scoped to its owner and purged with the rest of the Companion's data
// (PurgeCompanionContext). Every commit is one Pebble batch with a revision
// check: state, cursor, focus and topic index mark move together or not at
// all, and a stale writer (another maintainer, a deleted generation) is
// refused rather than merged.
const (
	// CompanionStateVersion is the schema version of CompanionState.
	CompanionStateVersion = 1

	CompanionStateMaxTopics      = 16
	CompanionStateMaxDecisions   = 16
	CompanionStateMaxOpen        = 64
	CompanionStateMaxResolved    = 16
	CompanionStateMaxWork        = 32
	CompanionStateMaxResults     = 16
	CompanionStateMaxTitleBytes  = 200
	CompanionStateMaxDetailBytes = 400

	CompanionCommitmentReply        = "reply"
	CompanionCommitmentApproval     = "approval"
	CompanionCommitmentConfirmation = "confirmation"
	CompanionCommitmentTaskWatch    = "task_watch"

	CompanionCommitmentOpen     = "open"
	CompanionCommitmentResolved = "resolved"

	CompanionWorkTask    = "task"
	CompanionWorkSession = "session"
)

var ErrCompanionStateConflict = errors.New("companion state changed; reload and retry")

// CompanionEventSource is the exact durable event a reference derives from:
// the Companion session, the event's sequence, ID and type, and the message
// it appended (whose sequence is the event's own).
type CompanionEventSource struct {
	SessionID  string `json:"session_id"`
	EventSeq   uint64 `json:"event_seq"`
	EventID    string `json:"event_id,omitempty"`
	EventType  string `json:"event_type"`
	MessageID  string `json:"message_id,omitempty"`
	MessageSeq uint64 `json:"message_seq,omitempty"`
	At         int64  `json:"at,omitempty"`
}

// CompanionStateCursor is the maintenance watermark: the last event of
// SessionID the state has consumed. Events after it are pending.
type CompanionStateCursor struct {
	SessionID string `json:"session_id"`
	EventSeq  uint64 `json:"event_seq"`
	EventID   string `json:"event_id,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
}

// CompanionStateRef points at one context bank record (a topic indexed from
// the person's message, or a decision recorded with manage_companion) and the
// event it came from. The record itself stays the authority for its text.
type CompanionStateRef struct {
	RecordID string               `json:"record_id"`
	Kind     string               `json:"kind"`
	Title    string               `json:"title,omitempty"`
	Source   CompanionEventSource `json:"source"`
}

// CompanionCommitment is something unresolved between the person and the
// Companion, opened and resolved by exact events.
type CompanionCommitment struct {
	ID         string                `json:"id"`
	Kind       string                `json:"kind"`
	Key        string                `json:"key"`
	Title      string                `json:"title,omitempty"`
	State      string                `json:"state"`
	Resolution string                `json:"resolution,omitempty"`
	Opened     CompanionEventSource  `json:"opened"`
	Resolved   *CompanionEventSource `json:"resolved,omitempty"`
}

// CompanionWorkRef is delegated work the Companion follows: a task (machine,
// project, task, the attempt and session last reported) or a work session
// recorded with manage_companion. Status is the last reported status, never
// a live read; live truth stays with the task and session records. Revision
// advances only when the reported status, attempt or watch changes.
type CompanionWorkRef struct {
	ID        string               `json:"id"`
	Kind      string               `json:"kind"`
	Machine   string               `json:"machine,omitempty"`
	ProjectID string               `json:"project_id,omitempty"`
	TaskID    string               `json:"task_id,omitempty"`
	AttemptID string               `json:"attempt_id,omitempty"`
	SessionID string               `json:"session_id,omitempty"`
	RecordID  string               `json:"record_id,omitempty"`
	WatchID   string               `json:"watch_id,omitempty"`
	Objective string               `json:"objective,omitempty"`
	Status    string               `json:"status,omitempty"`
	Untrusted bool                 `json:"untrusted,omitempty"`
	Revision  int64                `json:"revision"`
	Origin    CompanionEventSource `json:"origin"`
	Source    CompanionEventSource `json:"source"`
}

// CompanionResultRef is a recent device/tool result or wake that reached the
// Companion (a machine command, a client tool, a background command or
// watch outcome, a failed run), with its exact event.
type CompanionResultRef struct {
	Kind   string               `json:"kind"`
	Name   string               `json:"name,omitempty"`
	CallID string               `json:"call_id,omitempty"`
	Status string               `json:"status"`
	Detail string               `json:"detail,omitempty"`
	Source CompanionEventSource `json:"source"`
}

// CompanionStateCounters count what the maintainer consumed and what fixed
// bounds evicted, so dropped state is visible rather than silent.
type CompanionStateCounters struct {
	Events              uint64 `json:"events"`
	Applied             uint64 `json:"applied"`
	EvictedTopics       uint64 `json:"evicted_topics,omitempty"`
	EvictedDecisions    uint64 `json:"evicted_decisions,omitempty"`
	EvictedCommitments  uint64 `json:"evicted_commitments,omitempty"`
	EvictedWork         uint64 `json:"evicted_work,omitempty"`
	UnmatchedResolution uint64 `json:"unmatched_resolutions,omitempty"`
}

// CompanionState is the versioned materialized state of an account user's
// current Companion. Lists are ordered oldest first and bounded by the
// CompanionStateMax* limits.
type CompanionState struct {
	Version        int                   `json:"version"`
	AccountScopeID string                `json:"account_scope_id"`
	UserID         string                `json:"user_id"`
	SessionID      string                `json:"session_id"`
	Generation     uint64                `json:"generation"`
	Revision       int64                 `json:"revision"`
	Cursor         CompanionStateCursor  `json:"cursor"`
	Topics         []CompanionStateRef   `json:"topics"`
	Decisions      []CompanionStateRef   `json:"decisions"`
	Commitments    []CompanionCommitment `json:"commitments"`
	Work           []CompanionWorkRef    `json:"work"`
	Results        []CompanionResultRef  `json:"results"`
	// Continuity is the open continuity draft and what was sealed
	// (companion_continuity.go). A state written before continuity existed
	// has none and starts its draft at the next event.
	Continuity CompanionContinuity    `json:"continuity"`
	Counters   CompanionStateCounters `json:"counters"`
	CreatedAt  int64                  `json:"created_at"`
	UpdatedAt  int64                  `json:"updated_at"`
}

// NewCompanionState is the empty state of one Companion generation, before
// any event is consumed.
func NewCompanionState(identity CompanionIdentity, now int64) CompanionState {
	return CompanionState{
		Version: CompanionStateVersion, AccountScopeID: identity.AccountScopeID, UserID: identity.UserID,
		SessionID: identity.SessionID, Generation: identity.Generation,
		Cursor: CompanionStateCursor{SessionID: identity.SessionID},
		Topics: []CompanionStateRef{}, Decisions: []CompanionStateRef{}, Commitments: []CompanionCommitment{},
		Work: []CompanionWorkRef{}, Results: []CompanionResultRef{}, Continuity: CompanionContinuity{Open: []CompanionSegmentItem{}},
		CreatedAt: now, UpdatedAt: now,
	}
}

func companionStateKey(prefix string) []byte { return []byte(prefix + "s") }

// GetCompanionState reads the account user's materialized Companion state.
// found is false before the first commit.
func (s *SessionStore) GetCompanionState(account, user string) (CompanionState, bool, error) {
	var state CompanionState
	account, user = strings.TrimSpace(account), strings.TrimSpace(user)
	if !validCompanionPrincipal(account, user) {
		return state, false, ErrCompanionContextInvalid
	}
	ok, err := getJSONFromReader(s.store.db, string(companionStateKey(companionContextPrefix(account, user))), &state)
	if err != nil {
		return CompanionState{}, false, fmt.Errorf("decode companion state: %w", err)
	}
	if ok && state.Version != CompanionStateVersion {
		return CompanionState{}, false, fmt.Errorf("companion state version %d is not supported (want %d)", state.Version, CompanionStateVersion)
	}
	return state, ok, nil
}

// CompanionStateCommit is one maintenance step: the next state (its revision
// is assigned here), the revision it was derived from (0 when no state was
// stored), and the focus and topic index mark that move with it.
type CompanionStateCommit struct {
	Next             CompanionState
	ExpectedRevision int64
	// Focus, when set, replaces the current focus in the same batch.
	Focus *CompanionFocus
	// IndexMark, when set, moves the topic index mark forward in the same
	// batch (never backward).
	IndexMark *CompanionIndexMark
	// Segments are the continuity segments this step sealed, written in the
	// same batch.
	Segments []CompanionSegment
}

// CommitCompanionState writes one maintenance step atomically. It refuses a
// commit whose expected revision is not the stored one, whose cursor moves
// backward or to another session, or whose session is no longer the account
// user's current Companion (a deleted generation's late maintainer). It
// returns the stored state.
func (s *SessionStore) CommitCompanionState(commit CompanionStateCommit) (CompanionState, error) {
	next := commit.Next
	account, user := strings.TrimSpace(next.AccountScopeID), strings.TrimSpace(next.UserID)
	if !validCompanionPrincipal(account, user) || strings.TrimSpace(next.SessionID) == "" {
		return CompanionState{}, fmt.Errorf("%w: companion state principal and session required", ErrCompanionContextInvalid)
	}
	if next.Version != CompanionStateVersion || next.Cursor.SessionID != next.SessionID {
		return CompanionState{}, fmt.Errorf("%w: companion state version or cursor session", ErrCompanionContextInvalid)
	}
	s.store.companionContextMu.Lock()
	defer s.store.companionContextMu.Unlock()
	identity, found, err := s.GetCompanion(account, user)
	if err != nil {
		return CompanionState{}, err
	}
	if !found || identity.SessionID != next.SessionID || identity.Generation != next.Generation {
		return CompanionState{}, fmt.Errorf("%w: session is not the current Companion", ErrCompanionStateConflict)
	}
	prefix := companionContextPrefix(account, user)
	current, stored, err := s.GetCompanionState(account, user)
	if err != nil {
		return CompanionState{}, err
	}
	currentRevision := int64(0)
	if stored {
		currentRevision = current.Revision
		if current.SessionID == next.SessionID && next.Cursor.EventSeq < current.Cursor.EventSeq {
			return CompanionState{}, fmt.Errorf("%w: cursor would move backward", ErrCompanionStateConflict)
		}
	}
	if commit.ExpectedRevision != currentRevision {
		return CompanionState{}, ErrCompanionStateConflict
	}
	next.AccountScopeID, next.UserID = account, user
	next.Revision = currentRevision + 1
	// The records are the authority for what the state and its segments may
	// say about them: under this lock no forget or correction can land
	// between this check and the commit, so a maintainer that read a record
	// before it was forgotten or corrected cannot write the old text back.
	cache := map[string]companionRecordView{}
	since := uint64(0)
	if stored && current.SessionID == next.SessionID && current.Generation == next.Generation {
		since = current.Cursor.EventSeq
	}
	if err := s.reconcileCompanionStateRecords(prefix, &next, cache, since); err != nil {
		return CompanionState{}, err
	}
	batch := s.store.db.NewBatch()
	defer batch.Close()
	for _, seg := range commit.Segments {
		if err := validateCompanionSegment(seg, next.SessionID, next.Generation); err != nil {
			return CompanionState{}, err
		}
		items, removed, _, err := s.reconcileCompanionItems(prefix, seg.Items, cache)
		if err != nil {
			return CompanionState{}, err
		}
		seg.Items, seg.Removed, seg.Keywords = items, seg.Removed+removed, CompanionSegmentKeywords(items)
		next.Continuity.Removed += removed
		previous, found, err := s.getCompanionSegment(prefix, seg.ID)
		if err != nil {
			return CompanionState{}, err
		}
		var prev *CompanionSegment
		if found {
			prev = &previous
		}
		if err := writeCompanionSegment(batch, prefix, prev, &seg); err != nil {
			return CompanionState{}, err
		}
	}
	payload, err := json.Marshal(next)
	if err != nil {
		return CompanionState{}, err
	}
	if err := batch.Set(companionStateKey(prefix), payload, nil); err != nil {
		return CompanionState{}, err
	}
	if commit.Focus != nil && commit.Focus.RecordID != "" {
		if view, err := s.companionRecordView(prefix, commit.Focus.RecordID, cache); err != nil {
			return CompanionState{}, err
		} else if !view.found || !view.active {
			commit.Focus = nil // a forgotten topic is never the focus
		}
	}
	if commit.Focus != nil {
		focus := *commit.Focus
		focus.Keywords = normalizeCompanionKeywords(focus.Keywords, CompanionContextMaxKeywords)
		raw, err := json.Marshal(focus)
		if err != nil {
			return CompanionState{}, err
		}
		if err := batch.Set(companionFocusKey(prefix), raw, nil); err != nil {
			return CompanionState{}, err
		}
	}
	if mark := commit.IndexMark; mark != nil && strings.TrimSpace(mark.SessionID) != "" {
		var currentMark CompanionIndexMark
		if _, err := getJSONFromReader(s.store.db, string(companionIndexMarkKey(prefix)), &currentMark); err != nil {
			return CompanionState{}, err
		}
		if currentMark.SessionID != mark.SessionID || currentMark.MessageSeq < mark.MessageSeq {
			raw, err := json.Marshal(CompanionIndexMark{SessionID: strings.TrimSpace(mark.SessionID), MessageSeq: mark.MessageSeq})
			if err != nil {
				return CompanionState{}, err
			}
			if err := batch.Set(companionIndexMarkKey(prefix), raw, nil); err != nil {
				return CompanionState{}, err
			}
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return CompanionState{}, err
	}
	return next, nil
}

// SetSessionCommitObserver registers fn to be told, after each durable V3
// session mutation, which session it changed and the last event sequence it
// wrote. It is a nudge for derived maintenance (the Companion state), never
// an authority: the observer must read the durable log itself. fn must not
// block. nil removes it.
func (s *SessionStore) SetSessionCommitObserver(fn func(sessionID string, lastSeq uint64)) {
	if s == nil || s.store == nil {
		return
	}
	if fn == nil {
		s.store.sessionCommitObserver.Store(nil)
		return
	}
	s.store.sessionCommitObserver.Store(&fn)
}

func (s *SessionStore) notifySessionCommit(sessionID string, lastSeq uint64) {
	if s == nil || s.store == nil || lastSeq == 0 {
		return
	}
	if fn := s.store.sessionCommitObserver.Load(); fn != nil {
		(*fn)(sessionID, lastSeq)
	}
}

// GetCompanionLiveTask reads a local project task's current record for a
// Companion turn's work snapshot: one key read, without hydrating attempt
// outcomes or the bound plan, so a turn's live work reads stay bounded by the
// number of tasks it lists.
func (s *SessionStore) GetCompanionLiveTask(account, projectID, taskID string) (*ProjectTaskRecord, bool, error) {
	return s.getProjectTask(account, projectID, taskID, false)
}
