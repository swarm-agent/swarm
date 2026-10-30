package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// A session wake is the one way background work reaches its conversation: a
// background command that ended (or was stopped by a daemon restart), and a
// task watch that settled or reached a milestone. The store decides, under
// the conversation's mutation lock, how it is delivered:
//
//   - a conversation with an active run gets the outcome as a note pinned to
//     that run
//     (feedback_note, the queue a person's message mid-run uses): the run's
//     next provider step reads it. A durable wake note record remembers it, so
//     if that run ends without reading it (no feedback-delivery receipt) the
//     note is re-delivered as a new turn (RedeliverNoteID), after a restart too;
//   - an idle conversation gets the outcome with a new pending run.
//
// Every wake crosses ApplyV3SessionMutation, together with whatever the work
// itself retires in the same batch (a fired task watch). A committed wake that
// started a new turn (NewTurn) is enqueued by the caller.
type V3SessionWakeMutation struct {
	// ID names this delivery (unique per outcome: a background command id, a
	// watch id with its delivery number). Message and run ids derive from it.
	ID string
	// Source is the work: SessionWakeSourceBackgroundCommand or
	// SessionWakeSourceTaskWatch.
	Source string
	// RedeliverNoteID re-delivers a wake note whose run ended unread, as a new
	// turn; the note record is removed in the same batch.
	RedeliverNoteID string
	// TaskUpdateRootRunID is the task-report generation a new turn reads
	// (a task watch's); kept on the note for its re-delivery.
	TaskUpdateRootRunID string
	// DeliveredRunID is set by a committed wake: the run that takes the
	// outcome (the note's run, or the new turn's).
	DeliveredRunID string
	// NewTurn is set by a committed wake that started a new turn.
	NewTurn bool
}

const (
	SessionWakeSourceBackgroundCommand = "background_command"
	SessionWakeSourceTaskWatch         = "task_watch"
)

// V3SessionWakeNote is a delivered note waiting to be read by its run.
type V3SessionWakeNote struct {
	SessionID      string         `json:"session_id"`
	NoteID         string         `json:"note_id"`
	RunID          string         `json:"run_id"`
	WakeID         string         `json:"wake_id"`
	Source         string         `json:"source"`
	UserID         string         `json:"user_id,omitempty"`
	AccountScopeID string         `json:"account_scope_id,omitempty"`
	Content        string         `json:"content"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	// TaskUpdateRootRunID: the task-report generation a re-delivered turn
	// reads (a task watch's).
	TaskUpdateRootRunID string `json:"task_update_root_run_id,omitempty"`
	CreatedAt           int64  `json:"created_at"`
}

const v3WakeNotePrefix = "v3/wake_note/"

func keyV3SessionWakeNotePrefix(sessionID string) string {
	return v3WakeNotePrefix + keyPart(sessionID) + "/"
}

func KeyV3SessionWakeNote(sessionID, noteID string) string {
	return keyV3SessionWakeNotePrefix(sessionID) + keyPart(noteID)
}

func sessionWakeHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:12])
}

// SessionWakeRunID is the deterministic new turn a wake starts.
func SessionWakeRunID(sessionID, wakeID string) string {
	return "wake-" + sessionWakeHash(sessionID, wakeID, "run")
}

// SessionWakeMessageID is the deterministic message id of a delivery: a note
// for runID, or a new turn when runID is empty.
func SessionWakeMessageID(sessionID, wakeID, runID string) string {
	if runID == "" {
		return "wake-msg-" + sessionWakeHash(sessionID, wakeID, "turn")
	}
	return "wake-note-" + sessionWakeHash(sessionID, wakeID, "note", runID)
}

// SessionWakeReceiptID is the delivery receipt the run loop records when a
// step consumed a note (feedback-delivery:<note>:<run>).
func SessionWakeReceiptID(noteID, runID string) string {
	return "feedback-delivery:" + noteID + ":" + runID
}

// ListV3SessionWakeNotes lists a conversation's undelivered wake notes.
func (s *SessionStore) ListV3SessionWakeNotes(sessionID string) ([]V3SessionWakeNote, error) {
	var out []V3SessionWakeNote
	err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: keyV3SessionWakeNotePrefix(strings.TrimSpace(sessionID)), Limit: 256}, func(_ string, raw []byte) (bool, error) {
		var note V3SessionWakeNote
		if err := json.Unmarshal(raw, &note); err != nil {
			return false, err
		}
		out = append(out, note)
		return true, nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, err
}

// ListV3WakeNotesPaged lists every conversation's wake notes (startup).
func (s *SessionStore) ListV3WakeNotesPaged(afterKey string, limit int) ([]V3SessionWakeNote, string, error) {
	if limit <= 0 {
		limit = 64
	}
	start := ""
	if afterKey != "" {
		start = afterKey + "\x00"
	}
	var out []V3SessionWakeNote
	next := ""
	err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: v3WakeNotePrefix, StartKey: start, Limit: limit}, func(key string, raw []byte) (bool, error) {
		next = key
		var note V3SessionWakeNote
		if err := json.Unmarshal(raw, &note); err != nil {
			return false, err
		}
		out = append(out, note)
		return true, nil
	})
	if err != nil {
		return nil, "", err
	}
	if len(out) < limit {
		next = ""
	}
	return out, next, nil
}

// SettleV3SessionWakeNote removes a wake note its run read (its delivery
// receipt exists). It refuses while no receipt exists: an unread note is
// re-delivered, never dropped.
func (s *SessionStore) SettleV3SessionWakeNote(note V3SessionWakeNote) error {
	if _, ok, err := s.GetV3MessageByID(note.SessionID, SessionWakeReceiptID(note.NoteID, note.RunID)); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("wake note %s has no delivery receipt", note.NoteID)
	}
	return s.store.db.Delete([]byte(KeyV3SessionWakeNote(note.SessionID, note.NoteID)), pebble.Sync)
}

// prepareSessionWake turns a wake into a note for the active run or a new
// turn. The message (role user) is the caller's or the work's (a task watch
// fills its outcome first).
func (s *SessionStore) prepareSessionWake(input *V3SessionMutationInput) error {
	op := input.SessionWake
	if op == nil {
		return nil
	}
	if strings.TrimSpace(op.ID) == "" || (op.Source != SessionWakeSourceBackgroundCommand && op.Source != SessionWakeSourceTaskWatch) {
		return errors.New("a session wake needs an id and a known source")
	}
	if tomb, ok, err := s.GetV3SessionTombstone(input.SessionID); err != nil {
		return err
	} else if ok && (tomb.Archived || tomb.Deleted) {
		return ErrProjectTaskWaitStale
	}
	if op.RedeliverNoteID != "" {
		var note V3SessionWakeNote
		found, err := s.store.GetJSON(KeyV3SessionWakeNote(input.SessionID, op.RedeliverNoteID), &note)
		if err != nil {
			return err
		}
		if !found || note.AccountScopeID != input.AccountScopeID || note.UserID != input.UserID {
			return ErrProjectTaskWaitStale
		}
		metadata := map[string]any{}
		for k, v := range note.Metadata {
			metadata[k] = v
		}
		delete(metadata, "feedback_note")
		delete(metadata, "wake_note_run_id")
		metadata["redelivered_note_id"] = note.NoteID
		op.TaskUpdateRootRunID = note.TaskUpdateRootRunID
		input.Message = &MessageSnapshot{Role: "user", Content: note.Content, Metadata: metadata}
		input.wakeNoteDelete = &note
	}
	if input.Message == nil || strings.TrimSpace(input.Message.Content) == "" {
		return errors.New("a session wake needs its outcome message")
	}
	message := *input.Message
	message.Role = "user"
	metadata := map[string]any{}
	for k, v := range message.Metadata {
		metadata[k] = v
	}
	metadata["source"], metadata["wake_id"] = op.Source, op.ID
	now := input.NowUnixMs
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	state, found, err := s.GetV3SessionRunState(input.SessionID)
	if err != nil {
		return err
	}
	if found && state.Active && strings.TrimSpace(state.RunID) != "" {
		// A run is active: the note waits for its next step.
		runID := state.RunID
		message.ID = SessionWakeMessageID(input.SessionID, op.ID, runID)
		metadata["feedback_note"], metadata["wake_note_run_id"] = true, runID
		message.Metadata = metadata
		input.RunIntent = nil
		input.wakeNoteSet = &V3SessionWakeNote{SessionID: input.SessionID, NoteID: message.ID, RunID: runID, WakeID: op.ID, Source: op.Source, UserID: input.UserID, AccountScopeID: input.AccountScopeID, Content: message.Content, Metadata: metadata, CreatedAt: now, TaskUpdateRootRunID: op.TaskUpdateRootRunID}
		op.DeliveredRunID, op.NewTurn = runID, false
	} else {
		message.ID = SessionWakeMessageID(input.SessionID, op.ID, "")
		message.Metadata = metadata
		epochID := ""
		if epoch, ok, err := s.GetActiveExecutionEpoch(input.SessionID); err != nil {
			return err
		} else if ok {
			epochID = epoch.EpochID
		}
		if op.TaskUpdateRootRunID, err = s.currentSessionWakeRoot(input.SessionID, op.TaskUpdateRootRunID, metadata); err != nil {
			return err
		}
		runID := SessionWakeRunID(input.SessionID, op.ID)
		input.RunIntent = &V3SessionRunIntent{SessionID: input.SessionID, UserID: input.UserID, AccountScopeID: input.AccountScopeID, RunID: runID, SourceMessageID: message.ID, Status: V3RunIntentPendingExecutor, EpochID: epochID, TaskUpdateRootRunID: op.TaskUpdateRootRunID, CreatedAt: now, UpdatedAt: now}
		op.DeliveredRunID, op.NewTurn = runID, true
	}
	message.CreatedAt = now
	input.Message = &message
	input.Kind = V3SessionMutationAppendMessage
	if input.EventType == "" {
		input.EventType = "session.wake.delivered"
	}
	return nil
}

// currentSessionWakeRoot is the task-report generation a new wake turn reads:
// the one it was woken for while that is still current. A wake delivered after
// the person started a newer generation (a note re-delivered after their
// message) takes its watch's current generation, or its own ("") when the
// watch already ended, so the turn never fails the owner fence
// (ErrProjectTaskWaitStale) while the outcome it carries is still delivered.
// Reports of a superseded generation stay superseded.
func (s *SessionStore) currentSessionWakeRoot(sessionID, root string, metadata map[string]any) (string, error) {
	if root == "" {
		return "", nil
	}
	err := s.validateTaskUpdateOwnerFence(V3SessionRunIntent{SessionID: sessionID, RunID: root, TaskUpdateRootRunID: root})
	if !errors.Is(err, ErrProjectTaskWaitStale) {
		return root, err
	}
	if id, _ := metadata["task_watch_id"].(string); id != "" {
		watch, ok, err := s.GetV3SessionTaskWatch(sessionID, id)
		if err != nil {
			return "", err
		}
		if ok && watch.UpdateRoot != root {
			return s.currentSessionWakeRoot(sessionID, watch.UpdateRoot, nil)
		}
	}
	return "", nil
}

// setSessionWakeInBatch writes or removes the wake note this mutation made or
// re-delivered, and drops a conversation's wake notes when it is archived or
// deleted.
func (s *SessionStore) setSessionWakeInBatch(batch *pebble.Batch, input V3SessionMutationInput) error {
	if input.Kind == V3SessionMutationArchiveSession || input.Kind == V3SessionMutationDeleteSession {
		notes, err := s.ListV3SessionWakeNotes(input.SessionID)
		if err != nil {
			return err
		}
		for _, note := range notes {
			if err := batch.Delete([]byte(KeyV3SessionWakeNote(note.SessionID, note.NoteID)), nil); err != nil {
				return err
			}
		}
		return nil
	}
	if note := input.wakeNoteDelete; note != nil {
		if err := batch.Delete([]byte(KeyV3SessionWakeNote(note.SessionID, note.NoteID)), nil); err != nil {
			return err
		}
	}
	if note := input.wakeNoteSet; note != nil {
		raw, err := json.Marshal(note)
		if err != nil {
			return err
		}
		if err := batch.Set([]byte(KeyV3SessionWakeNote(note.SessionID, note.NoteID)), raw, nil); err != nil {
			return err
		}
	}
	return nil
}

// V3BackgroundCommandRecord is a background command while it runs: durable,
// so a daemon restart that stopped it is reported to its conversation
// ("stopped by a daemon restart"), never as a bare exit code.
type V3BackgroundCommandRecord struct {
	ID             string `json:"id"`
	SessionID      string `json:"session_id"`
	UserID         string `json:"user_id,omitempty"`
	AccountScopeID string `json:"account_scope_id,omitempty"`
	Command        string `json:"command"`
	StartedAt      int64  `json:"started_at"`
	// StoppedAt is when the daemon stopped it while shutting down (zero when
	// the daemon did not get to record it, e.g. it was killed).
	StoppedAt int64 `json:"stopped_at,omitempty"`
}

const v3BackgroundCommandPrefix = "v3/background_command/"

func KeyV3BackgroundCommand(sessionID, id string) string {
	return v3BackgroundCommandPrefix + keyPart(sessionID) + "/" + keyPart(id)
}

// PutV3BackgroundCommand records a running background command (or that the
// daemon stopped it).
func (s *SessionStore) PutV3BackgroundCommand(record V3BackgroundCommandRecord) error {
	if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.SessionID) == "" {
		return errors.New("background command record needs an id and a session")
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return s.store.db.Set([]byte(KeyV3BackgroundCommand(record.SessionID, record.ID)), raw, pebble.Sync)
}

// DeleteV3BackgroundCommand forgets a background command whose outcome was
// delivered (or that its conversation stopped).
func (s *SessionStore) DeleteV3BackgroundCommand(sessionID, id string) error {
	return s.store.db.Delete([]byte(KeyV3BackgroundCommand(sessionID, id)), pebble.Sync)
}

// ListV3BackgroundCommands lists the recorded background commands (startup).
func (s *SessionStore) ListV3BackgroundCommands() ([]V3BackgroundCommandRecord, error) {
	var out []V3BackgroundCommandRecord
	err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: v3BackgroundCommandPrefix, Limit: 4096}, func(_ string, raw []byte) (bool, error) {
		var record V3BackgroundCommandRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return false, err
		}
		out = append(out, record)
		return true, nil
	})
	return out, err
}

// advancesTaskUpdateFence reports whether a mutation is a person's message: a
// user-role message that is not a queued note and not a task watch's delivery
// (an outcome, milestone or its re-delivery). Only those start a new task
// report generation; a watch's own wake must keep the reports it woke for.
func advancesTaskUpdateFence(input V3SessionMutationInput) bool {
	if input.Message == nil || !strings.EqualFold(strings.TrimSpace(input.Message.Role), "user") || input.Message.Metadata["feedback_note"] == true {
		return false
	}
	return input.SessionWake == nil || input.SessionWake.Source != SessionWakeSourceTaskWatch
}
