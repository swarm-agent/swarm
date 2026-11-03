package api

import (
	"log"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A final plan handoff (run.appendFinalPlanExecutionSystemMessage) begins a
// fresh execution epoch with SkipRunIntent while the run that completed the
// plan's last checkpoint is still live: it seals that run's epoch without
// handing run ownership to anyone. Every run-owned V3 write is fenced by the
// run's recorded epoch (applySessionV3PrimaryMutation defaults the mutation
// epoch to RunIntent.EpochID), so without this resolution each later write of
// that run (progress, completion, failure, the stop's cancel, the restart's
// interrupt) is refused with "execution epoch ... is not active" and the run
// stays the session's active writer forever.
const (
	sessionV3FinalPlanHandoffReason = "final_plan_handoff"
	// Bounds the successor walk; each final handoff adds one epoch.
	sessionV3FinalHandoffChainLimit = 64
)

// sessionV3FinalHandoffSuccessor returns the session's active epoch when it
// succeeds runEpochID only through final plan handoff boundaries that
// assigned no run, and the plan the boundary next to runEpochID handed off.
// Any other boundary (compaction, context clear, a new run) on the path, a
// missing or still active runEpochID, or no active epoch returns ok=false.
func (s *Server) sessionV3FinalHandoffSuccessor(sessionID, runEpochID string) (successor pebblestore.ExecutionEpoch, planID string, ok bool, err error) {
	sessionID, runEpochID = strings.TrimSpace(sessionID), strings.TrimSpace(runEpochID)
	if s == nil || s.sessions == nil || sessionID == "" || runEpochID == "" {
		return pebblestore.ExecutionEpoch{}, "", false, nil
	}
	recorded, found, err := s.sessions.GetExecutionEpoch(sessionID, runEpochID)
	if err != nil || !found || recorded.Status != pebblestore.ExecutionEpochStatusSealed {
		return pebblestore.ExecutionEpoch{}, "", false, err
	}
	active, found, err := s.sessions.GetActiveExecutionEpoch(sessionID)
	if err != nil || !found || active.Status != pebblestore.ExecutionEpochStatusActive {
		return pebblestore.ExecutionEpoch{}, "", false, err
	}
	cursor := active
	for i := 0; i < sessionV3FinalHandoffChainLimit; i++ {
		if !strings.EqualFold(strings.TrimSpace(cursor.Boundary.Reason), sessionV3FinalPlanHandoffReason) || strings.TrimSpace(cursor.Boundary.RunID) != "" {
			return pebblestore.ExecutionEpoch{}, "", false, nil
		}
		parent := strings.TrimSpace(cursor.ParentEpochID)
		if parent == runEpochID {
			return active, strings.TrimSpace(cursor.Boundary.PlanID), true, nil
		}
		if parent == "" {
			return pebblestore.ExecutionEpoch{}, "", false, nil
		}
		cursor, found, err = s.sessions.GetExecutionEpoch(sessionID, parent)
		if err != nil || !found || cursor.Status != pebblestore.ExecutionEpochStatusSealed {
			return pebblestore.ExecutionEpoch{}, "", false, err
		}
	}
	return pebblestore.ExecutionEpoch{}, "", false, nil
}

// sessionV3RunWriteEpoch is the epoch a run-owned write of runID commits in.
// It is epochID unchanged unless epochID was sealed by final plan handoffs
// only and runID, recorded in epochID, is still the session's active run:
// then that run still owns the session, nothing else may write it, and the
// write commits in the handoff's successor. The run intent keeps its original
// epoch as provenance. Every other case keeps epochID, so the store's fence
// still refuses stale generations, superseded runs and foreign boundaries.
func (s *Server) sessionV3RunWriteEpoch(sessionID, runID, epochID string) (string, error) {
	epochID = strings.TrimSpace(epochID)
	runID = strings.TrimSpace(runID)
	if epochID == "" || runID == "" {
		return epochID, nil
	}
	successor, _, ok, err := s.sessionV3FinalHandoffSuccessor(sessionID, epochID)
	if err != nil || !ok {
		return epochID, err
	}
	owner, active, err := s.sessions.GetSessionActiveRunIntent(sessionID)
	if err != nil {
		return epochID, err
	}
	if !active || strings.TrimSpace(owner.RunID) != runID || strings.TrimSpace(owner.EpochID) != epochID {
		return epochID, nil
	}
	return strings.TrimSpace(successor.EpochID), nil
}

// sessionV3RunHandedOffByFinalPlan reports whether the non-terminal run
// intent's work already ended in a durable final plan handoff: its epoch was
// sealed by that handoff (no other boundary since), the handed-off plan is
// the run's own when it names one, every checkpoint is completed and the
// plan waits for review or is completed. Only then may a run with no live
// executor be finalized as completed instead of stopped or interrupted; the
// plan's review gate itself is untouched.
func (e *sessionV3Executor) sessionV3RunHandedOffByFinalPlan(sessionID string, intent pebblestore.V3SessionRunIntent) (bool, error) {
	if e == nil || e.server == nil || e.server.sessions == nil {
		return false, nil
	}
	switch strings.TrimSpace(intent.Status) {
	case sessionruntime.RunIntentPendingExecutor, sessionruntime.RunIntentRunning:
	default:
		return false, nil
	}
	owner, active, err := e.server.sessions.GetSessionActiveRunIntent(sessionID)
	if err != nil || !active || strings.TrimSpace(owner.RunID) != strings.TrimSpace(intent.RunID) {
		return false, err
	}
	_, planID, ok, err := e.server.sessionV3FinalHandoffSuccessor(sessionID, intent.EpochID)
	if err != nil || !ok || planID == "" {
		return false, err
	}
	if runPlan := strings.TrimSpace(intent.PlanID); runPlan != "" && runPlan != planID {
		return false, nil
	}
	plan, found, err := e.server.sessions.Store().GetPlan(sessionID, planID)
	if err != nil || !found || plan.Document == nil || plan.Document.ExecutionState == nil || len(plan.Document.Checkpoints) == 0 {
		return false, err
	}
	switch strings.TrimSpace(plan.Document.ExecutionState.Status) {
	case sessionruntime.PlanExecutionStateWaitingReview, sessionruntime.PlanExecutionStateCompleted:
	default:
		return false, nil
	}
	for _, checkpoint := range plan.Document.Checkpoints {
		if strings.TrimSpace(checkpoint.Status) != sessionruntime.PlanCheckpointStatusCompleted {
			return false, nil
		}
	}
	return true, nil
}

// finalizeHandedOffRun records a run whose work was proven handed off by
// its final plan as completed: the same terminal write, client request and
// payload as the executor's own terminal-plan completion, so a repeat replays
// and the task settlement and job-end signal run once.
func (e *sessionV3Executor) finalizeHandedOffRun(job sessionV3ExecutorJob, cause string) (sessionruntime.SessionMutationResult, error) {
	log.Printf("v3 executor: finalizing run %q of session %q from its persisted final plan handoff (%s)", job.RunID, job.SessionID, cause)
	return e.recordRunStatus(job, sessionruntime.RunIntentCompleted, "", "session.assistant.completed")
}
