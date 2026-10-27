package api

import (
	"errors"
	"fmt"
	"log"

	"swarm/packages/swarmd/internal/providerlimit"
	"swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// taskReview is read-only; settlement and workflow waiters repair reviews.
func (s *Server) taskReview(task *pebblestore.ProjectTaskRecord) (*pebblestore.JobReview, error) {
	return task.Review, nil
}

// repairTaskReview shares head verification, first-write-wins persistence and
// cleanup with normal settlement. Missing execution authority never permits OK.
func (s *Server) repairTaskReview(task *pebblestore.ProjectTaskRecord) (*pebblestore.JobReview, error) {
	if task.Agent != "reviewer" || task.WorkerID != "" || !controlMCPTaskSettled[task.Status] {
		return nil, nil
	}
	if state, found, err := s.sessions.Store().GetV3SessionRunState(task.SessionID); err != nil {
		return nil, err
	} else if found && state.Active {
		return nil, nil
	}
	stored, found, err := s.sessions.Store().GetProjectTask(task.AccountID, task.ProjectID, task.ID)
	if err != nil {
		return nil, err
	}
	execution, executionErr := s.workerExecutionService()
	if found && stored.Review != nil {
		if executionErr == nil {
			if err := execution.CleanupTaskReview(stored); err != nil {
				return stored.Review, err
			}
		}
		return stored.Review, nil
	}
	// A reviewer whose provider turn ended on a usage, rate or session limit
	// gave no verdict: none is recorded (never a block), its checkout is
	// cleaned up, and the workflow step that started it waits for the reset.
	if (task.Status == "failed" || task.Status == "blocked") && providerlimit.Is(task.LastError) {
		if executionErr == nil {
			if err := execution.CleanupTaskReview(task); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	report := ""
	var after uint64
	for page := 0; page < 100; page++ {
		messages, err := s.sessions.ListMessages(task.SessionID, after, 100)
		if err != nil {
			return nil, err
		}
		for _, message := range messages {
			after = message.GlobalSeq
			if message.Role == "assistant" {
				report = message.Content
			}
		}
		if len(messages) < 100 {
			break
		}
		if page == 99 {
			report = ""
		}
	}
	snapshot, found, err := s.sessions.GetSession(task.SessionID)
	if err != nil {
		return nil, err
	}
	model := ""
	if found {
		model = run.TaskReviewModel(snapshot)
	}
	review := pebblestore.ParseJobReview(report, task.StartCommit, taskRunJobID(task.ProjectID, task.ID), model, task.UpdatedAt)
	switch task.Status {
	case "failed", "blocked", "rejected", "cancelled", "superseded", "needs_input", "pending_approval":
		review.Verdict = "block"
		if review.Reason == "" {
			review.Reason = "reviewer task " + task.Status
		}
	}
	if executionErr == nil {
		checked, err := execution.FinishTaskReview(task, review)
		return &checked, err
	}
	review.Verdict = "block"
	if review.Reason == "" {
		review.Reason = "review head verification unavailable"
	}
	stored, err = s.sessions.Store().UpdateProjectTask(task.AccountID, task.ProjectID, task.ID, func(current *pebblestore.ProjectTaskRecord) error {
		if current.SessionID != task.SessionID || current.StartCommit != task.StartCommit || !controlMCPTaskSettled[current.Status] {
			return fmt.Errorf("review task changed")
		}
		if current.Review == nil {
			current.Review = &review
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stored.Review, nil
}

// settleTaskReview also retries cleanup after an earlier successful persistence.
func (s *Server) settleTaskReview(task *pebblestore.ProjectTaskRecord) (*pebblestore.JobReview, error) {
	review, err := s.repairTaskReview(task)
	if errors.Is(err, run.ErrTaskReviewCleanup) {
		// Preserve the terminal run signal even when cleanup must be retried.
		// Workflow waiters call repairTaskReview and cannot advance until it
		// succeeds; the saved verdict is not proof that cleanup has finished.
		log.Printf("task review cleanup deferred: %v", err)
		return review, nil
	}
	return review, err
}
