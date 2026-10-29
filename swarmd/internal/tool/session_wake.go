package tool

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// The session wake is the one way background work reaches its conversation
// (pebblestore session_wake.go decides, under the conversation's lock): a
// running conversation gets the outcome as a note its run reads at its next
// step; an idle one gets a new turn. Background commands deliver their report
// here; task watches go through the same store mutation from the daemon. A
// note its run did not read is delivered again when that run ends
// (SessionRunEnded) or, after a restart, at startup (RecoverBackgroundWork).

// backgroundWorkStore is the durable state of background work: wake notes
// waiting for their run, and background commands while they run.
type backgroundWorkStore interface {
	PutV3BackgroundCommand(pebblestore.V3BackgroundCommandRecord) error
	DeleteV3BackgroundCommand(sessionID, id string) error
	ListV3BackgroundCommands() ([]pebblestore.V3BackgroundCommandRecord, error)
	ListV3SessionWakeNotes(sessionID string) ([]pebblestore.V3SessionWakeNote, error)
	ListV3WakeNotesPaged(afterKey string, limit int) ([]pebblestore.V3SessionWakeNote, string, error)
	SettleV3SessionWakeNote(pebblestore.V3SessionWakeNote) error
	GetV3SessionRunState(sessionID string) (pebblestore.V3SessionRunState, bool, error)
	GetV3MessageByID(sessionID, messageID string) (pebblestore.MessageSnapshot, bool, error)
}

// SetBackgroundWorkStore installs the durable store for background work. Set
// it before commands run: without it commands are not recorded and notes
// their run did not read are not delivered again.
func (r *Runtime) SetBackgroundWorkStore(store backgroundWorkStore) {
	if r == nil {
		return
	}
	r.backgroundWork = store
	if r.background != nil {
		r.background.record = r.recordBackgroundCommand
		r.background.forget = r.forgetBackgroundCommand
	}
}

// deliverSessionWake delivers one outcome of background work: the store makes
// it a note for the active run or a new turn; a new turn is enqueued here.
// key makes a retry replay the same delivery.
func (r *Runtime) deliverSessionWake(principal identity.Principal, sessionID, key string, message *pebblestore.MessageSnapshot, wake *pebblestore.V3SessionWakeMutation) (pebblestore.V3SessionMutationResult, error) {
	if r == nil || r.sessions == nil {
		return pebblestore.V3SessionMutationResult{}, errors.New("session service is not configured")
	}
	now := time.Now().UnixMilli()
	res, err := r.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key,
		Kind: pebblestore.V3SessionMutationAppendMessage, Message: message, SessionWake: wake, NowUnixMs: now,
	})
	if err != nil {
		return res, fmt.Errorf("deliver %s to its conversation: %w", wake.Source, err)
	}
	if res.Conflict != nil || res.Error != nil {
		return res, fmt.Errorf("%s delivery rejected without mutation: conflict=%v error=%v", wake.Source, res.Conflict, res.Error)
	}
	if r.publishSessionOutbox != nil && res.RealtimeOutbox != nil {
		_ = r.publishSessionOutbox(*res.RealtimeOutbox)
	}
	// A replay reports no new turn; the committed run intent says.
	newTurn := wake.NewTurn || (res.RunIntent != nil && res.RunIntent.Status == pebblestore.V3RunIntentPendingExecutor && res.RunIntent.RunID == pebblestore.SessionWakeRunID(sessionID, wake.ID))
	if newTurn && res.RunIntent != nil {
		if r.sessionController == nil {
			return res, errors.New("session execution controller is not configured")
		}
		if !r.sessionController.EnqueueSessionRun(principal, sessionID, res.RunIntent.RunID, "") {
			return res, fmt.Errorf("failed to enqueue run %s for session %s", res.RunIntent.RunID, sessionID)
		}
	}
	return res, nil
}

// redeliverSessionWakeNotes delivers again the wake notes runID did not read
// (no delivery receipt), now that it ended: to the run active now, or as a
// new turn. Read notes are settled.
func (r *Runtime) redeliverSessionWakeNotes(sessionID, runID string) {
	if r == nil || r.backgroundWork == nil {
		return
	}
	notes, err := r.backgroundWork.ListV3SessionWakeNotes(sessionID)
	if err != nil {
		log.Printf("swarmd wake notes of a conversation: %v", err)
		return
	}
	for _, note := range notes {
		if note.RunID == runID {
			r.redeliverWakeNote(note)
		}
	}
}

func (r *Runtime) redeliverWakeNote(note pebblestore.V3SessionWakeNote) {
	if _, read, err := r.backgroundWork.GetV3MessageByID(note.SessionID, pebblestore.SessionWakeReceiptID(note.NoteID, note.RunID)); err != nil {
		log.Printf("swarmd wake note %s receipt: %v", note.NoteID, err)
		return
	} else if read {
		if err := r.backgroundWork.SettleV3SessionWakeNote(note); err != nil {
			log.Printf("swarmd wake note %s: %v", note.NoteID, err)
		}
		return
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: note.UserID, AccountScopeID: note.AccountScopeID}
	wake := &pebblestore.V3SessionWakeMutation{ID: note.WakeID + ":again:" + note.RunID, Source: note.Source, RedeliverNoteID: note.NoteID}
	if _, err := r.deliverSessionWake(principal, note.SessionID, "session-wake-again:"+note.NoteID, nil, wake); err != nil && !errors.Is(err, pebblestore.ErrProjectTaskWaitStale) {
		log.Printf("swarmd wake note %s delivery: %v", note.NoteID, err)
	}
}

// recordBackgroundCommand keeps a durable record of a running command (and,
// at shutdown, that the daemon stopped it).
func (r *Runtime) recordBackgroundCommand(c *backgroundCommand) {
	if r == nil || r.backgroundWork == nil {
		return
	}
	c.mu.Lock()
	record := pebblestore.V3BackgroundCommandRecord{ID: c.id, SessionID: c.sessionID, UserID: c.principal.UserID, AccountScopeID: c.principal.AccountScopeID, Command: truncateBackgroundText(c.command, 500), StartedAt: c.startedAt.UnixMilli()}
	if c.stoppedByShutdown {
		record.StoppedAt = time.Now().UnixMilli()
	}
	c.mu.Unlock()
	if err := r.backgroundWork.PutV3BackgroundCommand(record); err != nil {
		log.Printf("swarmd background command %s record: %v", c.id, err)
	}
}

// forgetBackgroundCommand drops a command's record: its outcome was
// delivered, or its conversation stopped it.
func (r *Runtime) forgetBackgroundCommand(c *backgroundCommand) {
	if r == nil || r.backgroundWork == nil {
		return
	}
	if err := r.backgroundWork.DeleteV3BackgroundCommand(c.sessionID, c.id); err != nil {
		log.Printf("swarmd background command %s record: %v", c.id, err)
	}
}

// BackgroundCommandRestartText is what a conversation is told about a
// background command a daemon restart stopped. It is not run again: a
// command may have side effects (a push, a deploy, a migration) and depended
// on a process, sandbox or lease that is gone, so repeating it blindly could
// repeat or corrupt work. The AI decides whether to run it again.
func BackgroundCommandRestartText(record pebblestore.V3BackgroundCommandRecord, startedUp time.Time) string {
	when := "at " + time.UnixMilli(record.StoppedAt).UTC().Format(time.RFC3339)
	if record.StoppedAt <= 0 {
		// The daemon did not get to record the stop (it was killed).
		when = "before " + startedUp.UTC().Format(time.RFC3339)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Background command %s was stopped by a daemon restart %s; it did not finish]\n", record.ID, when)
	fmt.Fprintf(&b, "Command: %s\n", record.Command)
	b.WriteString("It was not run again (it may have side effects). Run it again if it is still needed.")
	return b.String()
}

// RecoverBackgroundWork runs once at startup, before new runs start: every
// background command a restart stopped is reported to its conversation as
// such, and every wake note whose run is gone is delivered again.
func (r *Runtime) RecoverBackgroundWork() {
	if r == nil || r.backgroundWork == nil {
		return
	}
	startedUp := time.Now()
	records, err := r.backgroundWork.ListV3BackgroundCommands()
	if err != nil {
		log.Printf("swarmd background command recovery: %v", err)
	}
	for _, record := range records {
		principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: record.UserID, AccountScopeID: record.AccountScopeID}
		message := &pebblestore.MessageSnapshot{Role: "user", Content: BackgroundCommandRestartText(record, startedUp), Metadata: map[string]any{"background_id": record.ID, "stopped_by_restart": true}}
		_, err := r.deliverSessionWake(principal, record.SessionID, "background-command-restart:"+record.ID, message, &pebblestore.V3SessionWakeMutation{ID: record.ID + ":restart", Source: pebblestore.SessionWakeSourceBackgroundCommand})
		if err != nil && !errors.Is(err, pebblestore.ErrProjectTaskWaitStale) {
			log.Printf("swarmd background command %s restart report: %v", record.ID, err)
			continue
		}
		if err := r.backgroundWork.DeleteV3BackgroundCommand(record.SessionID, record.ID); err != nil {
			log.Printf("swarmd background command %s record: %v", record.ID, err)
		}
	}
	after := ""
	for {
		notes, next, err := r.backgroundWork.ListV3WakeNotesPaged(after, 64)
		if err != nil {
			log.Printf("swarmd wake note recovery: %v", err)
			return
		}
		for _, note := range notes {
			state, ok, err := r.backgroundWork.GetV3SessionRunState(note.SessionID)
			if err == nil && ok && state.Active && state.RunID == note.RunID {
				continue // its run is still going (recovered): it reads the note
			}
			r.redeliverWakeNote(note)
		}
		if next == "" {
			return
		}
		after = next
	}
}
