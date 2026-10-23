package api

import (
	"fmt"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// taskReview reads the durable final assistant report, never a truncated card
// summary. Re-reading after restart produces the same verdict and timestamp.
func (s *Server) taskReview(task *pebblestore.ProjectTaskRecord) (*pebblestore.JobReview, error) {
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
	if found && stored.Review != nil {
		return stored.Review, nil
	}
	var after uint64
	report := ""
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
			return nil, fmt.Errorf("review report exceeds transcript traversal bound")
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
	execution, err := s.workerExecutionService()
	if err != nil {
		return nil, err
	}
	review, err = execution.FinishTaskReview(task, review)
	if err != nil {
		return nil, err
	}
	return &review, nil
}
