package session

import (
	"errors"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Service) GetTaskProgram(parentSessionID, programID string) (pebblestore.TaskProgramRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.TaskProgramRecord{}, false, errors.New("session service is not configured")
	}
	record, ok, err := s.store.GetTaskProgram(strings.TrimSpace(parentSessionID), strings.TrimSpace(programID))
	if err != nil || !ok || (record.State != pebblestore.TaskProgramStateRunning && record.State != pebblestore.TaskProgramStateDeclared) || record.ReservationRunID == "" {
		return record, ok, err
	}
	intent, found, err := s.store.GetV3SessionRunIntent(parentSessionID, record.ReservationRunID)
	if err != nil {
		return record, ok, err
	}
	if !found {
		return record, ok, nil
	}
	switch intent.Status {
	case pebblestore.V3RunIntentCompleted, pebblestore.V3RunIntentFailed, pebblestore.V3RunIntentCancelled, pebblestore.V3RunIntentExpired, pebblestore.V3RunIntentInterrupted:
		// A terminal owning run cannot still execute this program. Preserve
		// committed/handoff-ready children; never resume or replay them here.
		state, next := pebblestore.TaskProgramStateBlocked, "inspect_preserved_children_then_author_new_program_for_unfinished_work"
		blocker := &pebblestore.TaskProgramBlocker{Code: "owner_run_ended", Message: "Task Program owning run ended before program completion", NextAction: next, ProgramID: record.ProgramID}
		updates := []pebblestore.TaskProgramJobTransition{}
		for _, job := range record.Jobs {
			childID := job.CurrentSessionID
			if childID == "" {
				childID = job.ChildSessionID
			}
			if childID != "" {
				preserved := pebblestore.TaskProgramPreservedChild{JobID: job.JobID, State: job.State, ChildSessionID: childID, RunID: job.CurrentRunID, WorkspacePath: job.WorkspacePath, WorktreeBranch: job.WorktreeBranch, ParentBranch: job.ParentBranch, ImmutableStageBase: job.ImmutableStageBase, ChildHead: job.ChildHead, IntegrationState: job.IntegrationState, AttemptNumber: job.AttemptNumber}
				if job.Blocker != nil {
					preserved.Dirty = job.Blocker.Dirty
					preserved.ChangedFiles = append([]string(nil), job.Blocker.ChangedFiles...)
				}
				blocker.PreservedChildren = append(blocker.PreservedChildren, preserved)
			}
			if job.State == pebblestore.TaskProgramJobRunning {
				updates = append(updates, pebblestore.TaskProgramJobTransition{JobID: job.JobID, ExpectedState: job.State, State: pebblestore.TaskProgramJobBlocked, Blocker: blocker})
			}
		}
		updated, _, transitionErr := s.store.TransitionTaskProgram(parentSessionID, programID, pebblestore.TaskProgramTransition{ExpectedRevision: record.Revision, MutationID: "owner-run-ended:" + record.ReservationRunID, State: &state, NextAction: &next, Blocker: blocker, Jobs: updates})
		if transitionErr != nil {
			return record, ok, transitionErr
		}
		return updated, true, nil
	}
	return record, ok, nil
}

func (s *Service) CreateTaskProgram(record pebblestore.TaskProgramRecord) (pebblestore.TaskProgramRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.TaskProgramRecord{}, false, errors.New("session service is not configured")
	}
	return s.store.CreateTaskProgram(record)
}

func (s *Service) TransitionTaskProgram(parentSessionID, programID string, transition pebblestore.TaskProgramTransition) (pebblestore.TaskProgramRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.TaskProgramRecord{}, false, errors.New("session service is not configured")
	}
	return s.store.TransitionTaskProgram(strings.TrimSpace(parentSessionID), strings.TrimSpace(programID), transition)
}

func (s *Service) GetTaskProgramHandoffMessage(sessionID string, globalSeq uint64) (pebblestore.MessageSnapshot, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.MessageSnapshot{}, false, errors.New("session service is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || globalSeq == 0 {
		return pebblestore.MessageSnapshot{}, false, errors.New("session id and message global seq are required")
	}
	messages, err := s.store.ListV3SessionMessages(sessionID, globalSeq-1, 1)
	if err != nil {
		return pebblestore.MessageSnapshot{}, false, err
	}
	if len(messages) != 1 || messages[0].GlobalSeq != globalSeq {
		return pebblestore.MessageSnapshot{}, false, nil
	}
	return messages[0], true, nil
}
