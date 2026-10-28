package api

import (
	"log"
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/jobautomation"
	sessionruntime "swarm/packages/swarmd/internal/session"
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
// The inference holds only while that window is open (settleTaskRunEnd):
// once the end is marked the runner's evaluation record says pending, and
// an end that is never marked (no signal store, a failed signal write, a
// run that is not a job end, a crash, an end from before marking existed)
// is not inferred to be pending at all, so no wait is held on it for good.
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
	// The window: the task's run end is being settled and its job end has
	// not been marked. Everything after the mark is read from the runner's
	// records (an unevaluated mark is pending in computeTaskProgress).
	unmarked := s.taskRunEndUnmarked(task.SessionID)
	// A send-back or rebase that went out for a follow-up whose end is not
	// marked yet: the work went back to the agent, and the previous round's
	// finished delivery is not where it ends. A reopen keeps the task's
	// attempt and its stored status (needs_review), so this is read from
	// durable records: the delivery's reopen step names the follow-up run
	// (the id its job end is marked with; jobautomation matches reopen
	// outcomes to job ends the same way), and while that run's intent in the
	// task's session is live the work is outstanding. Once the run ended,
	// only the window holds it until the end is marked: a run whose end is
	// never marked (a crash, a failed signal) does not hold a wait for good.
	// The session's latest run is not used: not every run in a task's
	// session is a job end.
	for _, d := range latestDeliveries(deliveries) {
		for _, step := range d.Steps {
			followup, _ := step.Outcome["followup"].(string)
			if step.Type != jobautomation.StepReopen || step.State != jobautomation.StateSucceeded || followup == "" || (eval != nil && eval.Followup == followup) {
				continue
			}
			if s.taskFollowupRunLive(task.SessionID, followup) {
				return true
			}
			if unmarked {
				return s.noteTaskRunEndHeld(task.SessionID)
			}
		}
	}
	if !unmarked {
		return false
	}
	// No end of this task was marked yet: one will be evaluated only if a
	// workflow could apply to it.
	if eval == nil && len(deliveries) == 0 && s.taskWorkflowsMayApply(account, task) {
		return s.noteTaskRunEndHeld(task.SessionID)
	}
	return false
}

// taskFollowupRunLive reports whether the follow-up run a reopen started in
// the task's session has a durable intent that has not ended.
func (s *Server) taskFollowupRunLive(sessionID, runID string) bool {
	intent, found, err := s.sessions.Store().GetV3SessionRunIntent(sessionID, runID)
	if err != nil {
		log.Printf("swarmd task wait: read follow-up run %s: %v", runID, err)
		return false
	}
	if !found {
		return false
	}
	switch intent.Status {
	case pebblestore.V3RunIntentPendingExecutor, pebblestore.V3RunIntentRunning, pebblestore.V3RunIntentWaitingTasks:
		return true
	}
	return false
}

// taskRunEndWindow is one task session's run end being settled: the status
// write (reconcileProjectTaskRunLifecycle) and the job end signal
// (signalTaskRunEnded). In memory only: a restart closes every window, so a
// crash between the two never leaves a task pending.
type taskRunEndWindow struct {
	open   int
	marked bool // the job end was marked for the workflow runner
	held   bool // a progress read inferred pending in this window
}

// settleTaskRunEnd reconciles a run end into its task and signals the job
// end, with the task's run end window open around both. When the window
// closes without the end marked after a read inferred pending, the task's
// project is told it changed (project.updated), which reconciles its waits
// again: the wait wakes by the task's records alone.
func (s *Server) settleTaskRunEnd(job sessionV3ExecutorJob, status, reason string, result sessionruntime.SessionMutationResult) error {
	prior := s.taskStatusBeforeRunEnd(job)
	defer s.openTaskRunEnd(job)()
	err := s.reconcileProjectTaskRunLifecycle(job, status, reason)
	if err == nil {
		s.signalTaskRunEnded(job, status, result, prior)
	}
	return err
}

// openTaskRunEnd opens the run end window of job's session when it is a
// project task's session and returns the function that closes it.
func (s *Server) openTaskRunEnd(job sessionV3ExecutorJob) func() {
	if s == nil || s.sessions == nil || job.SessionID == "" {
		return func() {}
	}
	session, ok, err := s.sessions.GetSession(job.SessionID)
	if err != nil || !ok {
		return func() {}
	}
	projectID := sessionsV3MetadataString(session.Metadata, "project_id")
	taskID := firstNonEmptyString(sessionsV3MetadataString(session.Metadata, "task_id"), sessionsV3MetadataString(session.Metadata, "project_task_id"))
	account := firstNonEmptyString(strings.TrimSpace(job.Principal.AccountScopeID), session.AccountScopeID)
	if projectID == "" || taskID == "" || account == "" {
		return func() {}
	}
	s.taskRunEndMu.Lock()
	if s.taskRunEnds == nil {
		s.taskRunEnds = map[string]*taskRunEndWindow{}
	}
	w := s.taskRunEnds[job.SessionID]
	if w == nil {
		w = &taskRunEndWindow{}
		s.taskRunEnds[job.SessionID] = w
	}
	w.open++
	s.taskRunEndMu.Unlock()
	return func() {
		s.taskRunEndMu.Lock()
		w.open--
		release := w.open == 0
		recheck := release && w.held && !w.marked
		if release {
			delete(s.taskRunEnds, job.SessionID)
		}
		s.taskRunEndMu.Unlock()
		if !recheck {
			return
		}
		if err := s.sessions.Store().NotifyProjectTaskChanged(account, projectID, taskID); err != nil {
			log.Printf("swarmd task wait: re-check waits after an unmarked end of %s: %v", taskID, err)
			s.reconcileProjectTaskWaits(account, projectID)
		}
	}
}

// taskRunEndUnmarked reports whether the session's run end window is open
// and its job end not marked yet.
func (s *Server) taskRunEndUnmarked(sessionID string) bool {
	s.taskRunEndMu.Lock()
	defer s.taskRunEndMu.Unlock()
	w := s.taskRunEnds[sessionID]
	return w != nil && w.open > 0 && !w.marked
}

// noteTaskRunEndHeld records that a read inferred pending in the session's
// open window, so closing it unmarked re-checks the waits. Always true.
func (s *Server) noteTaskRunEndHeld(sessionID string) bool {
	s.taskRunEndMu.Lock()
	defer s.taskRunEndMu.Unlock()
	if w := s.taskRunEnds[sessionID]; w != nil {
		w.held = true
	}
	return true
}

// noteTaskJobEndMarked records that the session's run end was marked for
// the workflow runner: its progress is read from the runner's records now.
func (s *Server) noteTaskJobEndMarked(sessionID string) {
	s.taskRunEndMu.Lock()
	defer s.taskRunEndMu.Unlock()
	if w := s.taskRunEnds[sessionID]; w != nil {
		w.marked = true
	}
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
