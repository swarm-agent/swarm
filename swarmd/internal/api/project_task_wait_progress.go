package api

import (
	"log"
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// taskWaitProgressFor is a task's progress as wait_tasks reads it (locally
// through the store's progress hook, and on a linked machine through
// get_task): taskProgressFor, with the workflow "pending" while the task's
// latest end has not been evaluated against the workflows yet.
//
// A task's status moves to needs_review before its job end is marked for the
// workflow runner (signalTaskRunEnded runs after the status write that also
// reconciles waits). In that window the progress alone would say "no
// workflow" (first end) or show the previous round's finished delivery (a
// send-back or rebase round), and a wait would wake with nothing landed.
func (s *Server) taskWaitProgressFor(account string, task pebblestore.ProjectTaskRecord, now int64) *taskProgress {
	deliveries, eval := s.taskProgressRecords(account, task)
	p := computeTaskProgress(task, deliveries, eval, now)
	if s.taskEndAwaitsWorkflows(account, task, deliveries, eval) {
		if p.Workflow == nil {
			p.Workflow = &taskProgressWorkflow{Deliveries: []taskProgressDelivery{}}
		}
		if p.Workflow.State != workflowRunning {
			p.Workflow.State = workflowPending
		}
	}
	return &p
}

// taskEndAwaitsWorkflows reports whether the task ended (its agent finished
// or failed) and that end will be evaluated by the workflow runner but has
// not been yet. Only ends the runner reads count (signalTaskRunEnded and
// markTaskJobEnded): any other task never waits on a workflow.
func (s *Server) taskEndAwaitsWorkflows(account string, task pebblestore.ProjectTaskRecord, deliveries []jobautomation.Delivery, eval *jobautomation.JobEvaluation) bool {
	class := taskStatusClass(task.Status)
	if class != "finished" && class != progressFailed {
		return false
	}
	if s == nil || s.sessions == nil || task.AutomationID != "" || account == "" || account != s.OutboxAccount() || task.SessionID == "" {
		return false
	}
	session, found, err := s.sessions.GetSession(task.SessionID)
	if err != nil || !found || session.AccountScopeID != account || !s.taskRunEndIsJobEnd(account, &task, session.UserID) {
		return false
	}
	// A send-back or rebase that went out for a follow-up whose end is not
	// evaluated yet: the work went back to the agent, and the previous
	// round's finished delivery is not where it ends. The follow-up is named
	// by the run id the reopen started, the id its job end is marked with
	// (jobautomation matches reopen outcomes to job ends the same way). The
	// session's latest run is not used: not every run in a task's session is
	// a job end.
	for _, d := range latestDeliveries(deliveries) {
		for _, step := range d.Steps {
			followup, _ := step.Outcome["followup"].(string)
			if step.Type == jobautomation.StepReopen && step.State == jobautomation.StateSucceeded && followup != "" && (eval == nil || eval.Followup != followup) {
				return true
			}
		}
	}
	// No end of this task was marked yet: one will be evaluated only if a
	// workflow could apply to it.
	return eval == nil && len(deliveries) == 0 && s.taskWorkflowsMayApply(account, task)
}

// taskWorkflowsMayApply reports whether an enabled workflow of the account
// could match the task's job end: one scoped to the task's folder (and, when
// set, its agent or job) that existed when the task last changed. It reads
// the definitions only; the runner's own matching stays authoritative.
func (s *Server) taskWorkflowsMayApply(account string, task pebblestore.ProjectTaskRecord) bool {
	store, err := s.jobAutomationStore()
	if err != nil {
		return false
	}
	folder := strings.TrimSpace(firstNonEmptyString(task.SourceWorkspace.Path, task.WorkspacePath))
	if folder == "" {
		return false
	}
	folder = filepath.Clean(folder)
	list, err := store.List(account)
	if err != nil {
		log.Printf("swarmd task wait: read workflows: %v", err)
		return false
	}
	jobID := taskRunJobID(task.ProjectID, task.ID)
	for _, a := range list {
		if a.Proposed || !a.Enabled || a.On != nil || len(a.Steps) == 0 || a.CreatedAt > task.UpdatedAt {
			continue
		}
		if a.Scope.Folder != folder || a.Scope.Worker != "" || a.ScopeWorkerID != "" || a.Scope.Schedule != "" {
			continue
		}
		if (a.Scope.Job != "" && a.Scope.Job != jobID) || (a.Scope.Role != "" && a.Scope.Role != task.Agent) {
			continue
		}
		return true
	}
	return false
}
