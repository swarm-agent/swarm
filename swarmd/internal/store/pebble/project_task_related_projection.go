package pebblestore

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/cockroachdb/pebble"
)

// Versioned derived keys are never mutation inputs. Canonical writers publish
// these in their own batch; deleting/rebuilding them cannot change authority.
func taskRelatedKey(key string) string { return "project_task_related/v1/" + key }

func compactBoardSession(v SessionSnapshot) SessionSnapshot {
	out := SessionSnapshot{ID: v.ID, UserID: v.UserID, AccountScopeID: v.AccountScopeID, MessageCount: v.MessageCount,
		WorktreeEnabled: v.WorktreeEnabled, WorktreeRootPath: v.WorktreeRootPath,
		WorktreeBaseBranch: v.WorktreeBaseBranch, WorktreeBranch: v.WorktreeBranch}
	out.Metadata = make(map[string]any)
	for _, key := range []string{"base_commit", "lifecycle_signal", "lifecycle_signal_run_id", "blocker_reason", "lifecycle_summary", "lifecycle_summary_run_id"} {
		if value, ok := v.Metadata[key].(string); ok {
			if key == "lifecycle_summary" && len(value) > 4000 { continue }
			out.Metadata[key] = value
		}
	}
	return out
}

func compactBoardPlan(v SessionPlanSnapshot) SessionPlanSnapshot {
	out := SessionPlanSnapshot{ID: v.ID, SessionID: v.SessionID, UserID: v.UserID,
		AccountScopeID: v.AccountScopeID, Version: v.Version, Status: v.Status,
		ApprovalState: v.ApprovalState, AcceptedDefinitionReceipt: v.AcceptedDefinitionReceipt}
	if v.Document != nil {
		out.Document = &SessionPlanDocument{ActiveCheckpointID: v.Document.ActiveCheckpointID}
		if v.Document.ExecutionState != nil { state := *v.Document.ExecutionState; out.Document.ExecutionState = &state }
		for _, cp := range v.Document.Checkpoints {
			checkpoint := SessionPlanCheckpoint{ID: cp.ID, Status: cp.Status, AttemptID: cp.AttemptID, RunID: cp.RunID, SessionID: cp.SessionID}
			if cp.Handoff != nil && len(cp.Handoff.Overview) <= 4000 { checkpoint.Handoff = &SessionPlanCheckpointHandoff{Overview: cp.Handoff.Overview} }
			out.Document.Checkpoints = append(out.Document.Checkpoints, checkpoint)
		}
	}
	return out
}

func compactBoardProgram(v TaskProgramRecord) TaskProgramRecord {
	out := TaskProgramRecord{ParentSessionID: v.ParentSessionID, ProgramID: v.ProgramID,
		ReservationRunID: v.ReservationRunID, ReservationCallID: v.ReservationCallID,
		Revision: v.Revision, State: v.State, ActiveStageID: v.ActiveStageID}
	if v.Blocker != nil { out.Blocker = &TaskProgramBlocker{Code: v.Blocker.Code, Message: v.Blocker.Message} }
	for _, j := range v.Jobs {
		job := TaskProgramJobRecord{JobID: j.JobID, State: j.State, ChildSessionID: j.ChildSessionID, CurrentSessionID: j.CurrentSessionID, CurrentRunID: j.CurrentRunID}
		// Only unique excluded session identities are needed by Desktop, not the
		// generations' reports, timestamps, runs or execution history.
		seen := map[string]bool{j.CurrentSessionID: true, j.ChildSessionID: j.CurrentSessionID == ""}
		for _, g := range j.GenerationHistory {
			if g.SessionID != "" && !seen[g.SessionID] { job.GenerationHistory = append(job.GenerationHistory, TaskProgramJobGeneration{SessionID: g.SessionID}); seen[g.SessionID] = true }
		}
		out.Jobs = append(out.Jobs, job)
	}
	return out
}

func setTaskRelatedInBatch(batch *pebble.Batch, key string, value any) error {
	var compact any
	switch v := value.(type) {
	case SessionSnapshot: compact = compactBoardSession(v)
	case SessionPlanSnapshot: compact = compactBoardPlan(v)
	case TaskProgramRecord: compact = compactBoardProgram(v)
	default: return errors.New("unsupported task related projection")
	}
	raw, err := json.Marshal(compact)
	if err != nil { return err }
	return batch.Set([]byte(taskRelatedKey(key)), raw, nil)
}

// Missing projections are discovered against the board snapshot, before the
// consumer runs. A bounded repair commits each row durably and returns not-ready,
// so interruptions/retries resume by skipping already materialized keys. The
// canonical mutation locks prevent stale repair from overwriting a newer row.
func (s *SessionStore) backfillTaskRelated(keys []string, stats *ProjectTaskReadStats) error {
	for i, key := range keys {
		if i >= taskSummaryBackfillRows || (i > 0 && stats.BackfillBytes >= taskSummaryBackfillBytes) { break }
		if err := s.backfillTaskRelatedKey(key, stats); err != nil { return err }
	}
	return ErrProjectTaskSummariesNotReady
}

func (s *SessionStore) backfillTaskRelatedKey(key string, stats *ProjectTaskReadStats) error {
	parts := strings.Split(key, "/")
	if len(parts) < 2 { return ErrProjectTaskSummaryCorrupt }
	session, err := url.PathUnescape(parts[1])
	if err != nil { return err }
	var value any
	var unlock func()
	switch {
	case key == KeySession(session):
		value = &SessionSnapshot{}
		unlock = s.store.sessionMutations.lockSessions(session)
	case len(parts) == 3:
		id, err := url.PathUnescape(parts[2]); if err != nil { return err }
		if key == KeySessionPlan(session, id) {
			value = &SessionPlanSnapshot{}
			unlock = s.store.sessionMutations.lockSessions(session)
		} else if key == KeyTaskProgram(session, id) {
			value = &TaskProgramRecord{}
			lock := taskProgramLock(session, id); lock.Lock(); unlock = lock.Unlock
		} else { return ErrProjectTaskSummaryCorrupt }
	default: return ErrProjectTaskSummaryCorrupt
	}
	defer unlock()
	if _, c, err := s.store.db.Get([]byte(taskRelatedKey(key))); err == nil { c.Close(); return nil } else if !errors.Is(err, pebble.ErrNotFound) { return err }
	raw, closer, err := s.store.db.Get([]byte(key))
	if errors.Is(err, pebble.ErrNotFound) { return nil }
	if err != nil { return err }
	defer closer.Close()
	if len(raw) > 256<<20 { return errors.New("legacy related record exceeds 256 MiB migration limit") }
	stats.BackfillRows++; stats.BackfillBytes += int64(len(raw))
	if err := json.Unmarshal(raw, value); err != nil { return err }
	batch := s.store.db.NewBatch(); defer batch.Close()
	switch v := value.(type) {
	case *SessionSnapshot:
		if key != KeySession(v.ID) { return ErrProjectTaskSummaryCorrupt }; err = setTaskRelatedInBatch(batch, key, *v)
	case *SessionPlanSnapshot:
		if key != KeySessionPlan(v.SessionID, v.ID) { return ErrProjectTaskSummaryCorrupt }; err = setTaskRelatedInBatch(batch, key, *v)
	case *TaskProgramRecord:
		if key != KeyTaskProgram(v.ParentSessionID, v.ProgramID) { return ErrProjectTaskSummaryCorrupt }; err = setTaskRelatedInBatch(batch, key, *v)
	}
	if err != nil { return err }
	return batch.Commit(pebble.Sync)
}

func (r *ProjectTaskBoardReader) prepare(rows []ProjectTaskRecord) {
	for i := range rows {
		t := &rows[i]; r.BindTask(t)
		if t.SessionID == "" { continue }
		r.GetSession(t.SessionID)
		if t.PlanBinding != nil { r.GetPlan(t.SessionID, t.PlanBinding.PlanID) }
		id := t.TaskProgramID
		if id == "" && t.TaskProgram != nil { id = t.TaskProgram.ID }
		if id != "" {
			if p, ok, _ := r.GetTaskProgram(t.SessionID, id); ok {
				for _, j := range p.Jobs { sid := j.CurrentSessionID; if sid == "" { sid = j.ChildSessionID }; if sid != "" { r.GetSession(sid) } }
			}
		}
	}
	r.task = nil
}

func boardRelatedRead[T any](r *ProjectTaskBoardReader, canonical string) (T, bool, error) {
	v, ok, err := boardRead[T](r, taskRelatedKey(canonical))
	if err != nil || ok { return v, ok, err }
	// Check existence only: never decode the full body in a board snapshot.
	_, closer, err := r.reader.Get([]byte(canonical))
	if errors.Is(err, pebble.ErrNotFound) { return v, false, nil }
	if err != nil { r.err = err; return v, false, err }
	closer.Close()
	if !r.missingSet[canonical] { r.missing = append(r.missing, canonical); r.missingSet[canonical] = true }
	return v, false, ErrProjectTaskSummariesNotReady
}

func deleteTaskRelatedSessionInBatch(batch *pebble.Batch, session string, deleted bool) error {
	if err := batch.Delete([]byte(taskRelatedKey(KeySession(session))), nil); err != nil { return err }
	if !deleted { return nil }
	for _, prefix := range []string{SessionPlanPrefix(session), TaskProgramSessionPrefix(session)} {
		prefix = taskRelatedKey(prefix)
		if err := batch.DeleteRange([]byte(prefix), []byte(prefix+"\xff"), nil); err != nil { return err }
	}
	return nil
}
