package api

import (
	"fmt"
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
			return stored.Review, execution.CleanupTaskReview(stored)
		}
		return stored.Review, nil
	}
	report := ""
	var after uint64
	for page := 0; page < 100; page++ {
		messages, err := s.sessions.ListMessages(task.SessionID, after, 100)
		if err != nil {
			report = ""
			break
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
	review := pebblestore.ParseJobReview(report, task.StartCommit, taskRunJobID(task.ProjectID, task.ID), task.Provider+"/"+task.Model, task.UpdatedAt)
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
	return s.repairTaskReview(task)
}
