package api

import (
	"log"
	"strings"

	"swarm/packages/swarmd/internal/jobautomation"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// taskRunAgents are the agents whose tasks end as workflow job ends: the ones
// that come with Swarm. A worker job's task ends as worker.run.*, a design
// job as design.job.* and a media task as media.job.*.
var taskRunAgents = map[string]bool{"swarm": true, "coder": true, "finder": true, "designer": true}

// taskRunJobID is the job id workflows use for a task: task/<project>/<task>.
func taskRunJobID(projectID, taskID string) string {
	return "task/" + projectID + "/" + taskID
}

// signalTaskRunEnded records task.run.succeeded, task.run.failed or
// task.run.cancelled when a run in a task's own session ends, so workflows
// (job automations) follow the work of Swarm's own agents the way they follow
// a worker job: the task's first run, and each follow-up sent into its
// session. Only a task the person or their AI started (swarm_run_task) counts;
// a task an agent created inside another run does not. It runs after the
// task's own status was reconciled; prior is the task's status before that
// (taskStatusBeforeRunEnd). A replayed or refused status write raises nothing.
func (s *Server) signalTaskRunEnded(job sessionV3ExecutorJob, status string, result sessionruntime.SessionMutationResult, prior string) {
	if s == nil || s.signals == nil || s.sessions == nil || result.Replayed || result.Conflict != nil || result.Error != nil {
		return
	}
	ended := ""
	switch status {
	case sessionruntime.RunIntentCompleted:
		ended = jobautomation.StatusSucceeded
	case sessionruntime.RunIntentCancelled:
		ended = jobautomation.StatusCancelled
	case sessionruntime.RunIntentFailed, sessionruntime.RunIntentExpired, sessionruntime.RunIntentInterrupted, sessionruntime.RunIntentDispatchBlocked:
		ended = jobautomation.StatusFailed
	default:
		return
	}
	session, ok, err := s.sessions.GetSession(job.SessionID)
	if err != nil || !ok {
		return
	}
	projectID := sessionsV3MetadataString(session.Metadata, "project_id")
	taskID := sessionsV3MetadataString(session.Metadata, "task_id")
	if taskID == "" {
		taskID = sessionsV3MetadataString(session.Metadata, "project_task_id")
	}
	account := strings.TrimSpace(job.Principal.AccountScopeID)
	if account == "" {
		account = session.AccountScopeID
	}
	if projectID == "" || taskID == "" || account == "" {
		return
	}
	db := s.sessions.Store()
	if db == nil {
		return
	}
	task, found, err := db.GetProjectTask(account, projectID, taskID)
	if err != nil || !found || task == nil || task.SessionID != job.SessionID || task.Archived {
		return
	}
	if !taskRunAgents[task.Agent] || task.WorkerRunID != "" || task.Design != nil || task.OriginSessionID != "" {
		return
	}
	if prior == "planning" || prior == "pending_approval" {
		return // a planning run is not the work
	}
	// The task's first run is the task's end; every later run in its session
	// (a follow-up, or a reopened attempt) is named by its run id.
	followup := job.RunID
	if job.RunID == pebblestore.InitialTaskRunID(task.ID) {
		followup = ""
	}
	severity := pebblestore.SignalSeverityInfo
	if ended != jobautomation.StatusSucceeded {
		severity = pebblestore.SignalSeverityWarning
	}
	source := "task"
	if task.AutomationID != "" {
		source = jobautomation.RequestSourceAutomation
	}
	kind := "task.run." + ended
	if _, err := s.signals.AppendOnce(pebblestore.Signal{
		Kind: kind, Severity: severity, Source: pebblestore.SignalSourceSwarmd, Account: account,
		Summary:  "Task " + controlMCPTruncate(task.Title, 120) + " (" + task.Agent + ") run " + ended,
		DedupKey: "task.run:" + task.ID + ":" + followup,
		Refs:     map[string]string{"project_id": projectID, "task_id": taskID, "session_id": job.SessionID, "run_id": job.RunID},
		Attrs:    map[string]string{"job_id": taskRunJobID(projectID, taskID), "role": task.Agent, "source": source},
	}); err != nil {
		log.Printf("swarmd signal %s not recorded: %v", kind, err)
	}
}

// taskStatusBeforeRunEnd is the status of the task whose own session job ran
// in, read before the run's end is reconciled into it ("" for any other
// session): a planning run is told apart from the work by it.
func (s *Server) taskStatusBeforeRunEnd(job sessionV3ExecutorJob) string {
	if s == nil || s.sessions == nil || s.sessions.Store() == nil {
		return ""
	}
	session, ok, err := s.sessions.GetSession(job.SessionID)
	if err != nil || !ok {
		return ""
	}
	projectID := sessionsV3MetadataString(session.Metadata, "project_id")
	taskID := sessionsV3MetadataString(session.Metadata, "task_id")
	if taskID == "" {
		taskID = sessionsV3MetadataString(session.Metadata, "project_task_id")
	}
	if projectID == "" || taskID == "" {
		return ""
	}
	account := firstNonEmptyString(strings.TrimSpace(job.Principal.AccountScopeID), session.AccountScopeID)
	task, found, err := s.sessions.Store().GetProjectTask(account, projectID, taskID)
	if err != nil || !found || task == nil || task.SessionID != job.SessionID {
		return ""
	}
	return task.Status
}
