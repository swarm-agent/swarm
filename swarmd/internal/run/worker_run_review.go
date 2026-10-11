package run

import (
	"errors"
	"strings"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// WorkerRunReview is a read-time projection of the project task a worker run
// produced. A run's status reports execution only ("succeeded" means the agent
// finished); every successful run still lands as a project task that awaits
// review and Git integration. This projection states that pending review
// explicitly without changing run status semantics. It is never persisted.
type WorkerRunReview struct {
	// State is "awaiting_review" while the task needs review, "completed" once
	// the task is integrated or accepted, otherwise the task status.
	State      string `json:"state"`
	ProjectID  string `json:"project_id"`
	TaskID     string `json:"task_id"`
	TaskStatus string `json:"task_status"`
	NextAction string `json:"next_action,omitempty"`
}

// WorkerRunReviewStateAwaiting marks a succeeded run whose task needs review.
const WorkerRunReviewStateAwaiting = "awaiting_review"

// RunReview returns the review projection for a succeeded worker run, or nil
// when the run has not succeeded or produced no project task.
func (s *WorkerExecutionService) RunReview(r store.WorkerRunRecord) (*WorkerRunReview, error) {
	if s == nil || s.host == nil || s.host.runs == nil || s.host.runs.sessions == nil {
		return nil, errors.New("sessions service unavailable")
	}
	if r.Status != "succeeded" || strings.TrimSpace(r.SessionID) == "" {
		return nil, nil
	}
	session, found, err := s.host.runs.sessions.GetSession(r.SessionID)
	if err != nil || !found {
		return nil, err
	}
	if session.AccountScopeID != r.AccountScopeID || mapString(session.Metadata, "worker_execution_run_id") != r.ID {
		return nil, nil
	}
	projectID := strings.TrimSpace(mapString(session.Metadata, "project_id"))
	taskID := strings.TrimSpace(mapString(session.Metadata, "task_id"))
	if projectID == "" || taskID == "" {
		return nil, nil
	}
	db := s.host.runs.sessions.Store()
	if db == nil {
		return nil, errors.New("database not available")
	}
	task, found, err := db.GetProjectTask(r.AccountScopeID, projectID, taskID)
	if err != nil || !found || task == nil || task.WorkerRunID != r.ID {
		return nil, err
	}
	review := &WorkerRunReview{State: task.Status, ProjectID: projectID, TaskID: taskID, TaskStatus: task.Status}
	switch task.Status {
	case "needs_review":
		review.State = WorkerRunReviewStateAwaiting
		review.NextAction = "Review the task deliverables, then integrate committed work with POST /v3/projects/" + projectID + "/tasks/" + taskID + "/integrate"
	case "completed":
		review.State = "completed"
	}
	return review, nil
}
