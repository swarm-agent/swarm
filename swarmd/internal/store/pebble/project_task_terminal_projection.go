package pebblestore

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/cockroachdb/pebble"
)

// A present empty Summary is a durable negative result, not a missing projection.
// The event address and payload provenance remain necessary even for negatives.
type taskTerminalSummary struct {
	SessionID string `json:"session_id"`
	EventSeq uint64 `json:"event_seq"`
	AccountID string `json:"account_id"`
	RunID string `json:"run_id"`
	Summary string `json:"summary"`
}

func setTaskTerminalInBatch(batch *pebble.Batch, event V3SessionEvent) error {
	v := taskTerminalSummary{SessionID: event.SessionID, EventSeq: event.Seq}
	var payload v3SessionEventReplayPayload
	if event.EventType == "session.assistant.completed" {
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
	}
	if run := payload.RunIntent; run != nil {
		v.AccountID, v.RunID = run.AccountScopeID, run.RunID
		state := V3SessionRunState{SessionID: event.SessionID, AccountScopeID: v.AccountID, RunID: v.RunID, Status: run.Status, EventSeq: event.Seq}
		v.Summary, _ = completedTaskEventSummary(state, event)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return batch.Set([]byte(taskRelatedKey(KeyV3SessionEvent(event.SessionID, event.Seq))), raw, nil)
}

func (r *ProjectTaskBoardReader) completedRunSummary(state V3SessionRunState) (string, bool) {
	if state.Active || state.Status != V3RunIntentCompleted || state.RunID == "" || state.EventSeq == 0 || state.AccountScopeID != r.account {
		return "", false
	}
	v, ok, err := boardRelatedRead[taskTerminalSummary](r, KeyV3SessionEvent(state.SessionID, state.EventSeq))
	if err != nil || !ok || v.SessionID != state.SessionID || v.EventSeq != state.EventSeq || v.AccountID != state.AccountScopeID || v.RunID != state.RunID || len(v.Summary) > 4000 {
		return "", false
	}
	return v.Summary, v.Summary != ""
}

func (s *SessionStore) backfillTaskTerminal(key string, stats *ProjectTaskReadStats) error {
	parts := strings.Split(key, "/")
	if len(parts) != 4 {
		return ErrProjectTaskSummaryCorrupt
	}
	session, err := url.PathUnescape(parts[2])
	if err != nil {
		return err
	}
	seq, err := strconv.ParseUint(parts[3], 10, 64)
	if err != nil || key != KeyV3SessionEvent(session, seq) {
		return ErrProjectTaskSummaryCorrupt
	}
	unlock := s.store.sessionMutations.lockSessions(session)
	defer unlock()
	if _, closer, err := s.store.db.Get([]byte(taskRelatedKey(key))); err == nil {
		closer.Close()
		return nil
	} else if !errors.Is(err, pebble.ErrNotFound) {
		return err
	}
	raw, closer, err := s.store.db.Get([]byte(key))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	defer closer.Close()
	if len(raw) > 256<<20 {
		return errors.New("legacy terminal event exceeds 256 MiB migration limit")
	}
	stats.BackfillRows++
	stats.BackfillBytes += int64(len(raw))
	var event V3SessionEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	if event.SessionID != session || event.Seq != seq {
		return ErrProjectTaskSummaryCorrupt
	}
	batch := s.store.db.NewBatch()
	defer batch.Close()
	if err := setTaskTerminalInBatch(batch, event); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
}
