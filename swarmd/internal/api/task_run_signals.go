package api

import (
	"log"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/jobautomation"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// taskRunAgents are the agents whose tasks end as workflow job ends: the ones
// that come with Swarm. A worker job's task ends as worker.run.*, a design
// job as design.job.* and a media task as media.job.*.
var taskRunAgents = map[string]bool{"swarm": true, "coder": true, "finder": true, "designer": true, "reviewer": true}

// taskRunJobID is the job id workflows use for a task: task/<project>/<task>.
func taskRunJobID(projectID, taskID string) string {
	return "task/" + projectID + "/" + taskID
}

// signalTaskRunEnded records task.run.succeeded, task.run.failed or
// task.run.cancelled when a run in a task's own session ends with the task in
// a result state (taskRunLeftResult), so workflows
// (job automations) follow the work of Swarm's own agents the way they follow
// a worker job: the task's first run, and each follow-up sent into its
// session. Tasks started directly (swarm_run_task) or delegated by the primary
// Orchestrator count; tasks created by other agent runs do not. It runs after the
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
	if !taskRunAgents[task.Agent] || task.WorkerRunID != "" || task.Design != nil {
		return
	}
	if task.OriginSessionID != "" {
		origin, found, err := s.sessions.GetSession(task.OriginSessionID)
		if err != nil || !found || origin.AccountScopeID != account || origin.UserID != session.UserID || !taskRunPrimaryOrchestrator(origin) {
			return
		}
	}
	if prior == "planning" || prior == "pending_approval" {
		return // a planning run is not the work
	}
	// A run ending is not the task ending: a run that pends its goal on its
	// delegated tasks (wait_tasks), or any run that leaves the task without a
	// result, records nothing. The run that does finish the task records it.
	if !taskRunLeftResult(task.Status) {
		return
	}
	// The task's first run is the task's end; every later run in its session
	// (a follow-up, or a reopened attempt) is named by its run id.
	followup := job.RunID
	if job.RunID == pebblestore.InitialTaskRunID(task.ID) {
		followup = ""
	}
	attrs := map[string]string{"job_id": taskRunJobID(projectID, taskID), "role": task.Agent}
	// Provider completion does not mean the reconciled task did its work.
	if task.Status == "blocked" {
		ended = jobautomation.StatusFailed
		attrs["blocker_reason"] = task.LastError
	}
	severity := pebblestore.SignalSeverityInfo
	if ended != jobautomation.StatusSucceeded {
		severity = pebblestore.SignalSeverityWarning
	}
	source := "task"
	if task.AutomationID != "" {
		source = jobautomation.RequestSourceAutomation
	}
	attrs["source"] = source
	kind := "task.run." + ended
	sig, err := s.signals.AppendOnce(pebblestore.Signal{
		Kind: kind, Severity: severity, Source: pebblestore.SignalSourceSwarmd, Account: account,
		Summary:  "Task " + controlMCPTruncate(task.Title, 120) + " (" + task.Agent + ") run " + ended,
		DedupKey: "task.run:" + task.ID + ":" + followup,
		Refs:     map[string]string{"project_id": projectID, "task_id": taskID, "session_id": job.SessionID, "run_id": job.RunID},
		Attrs:    attrs,
	})
	if err != nil {
		log.Printf("swarmd signal %s not recorded: %v", kind, err)
		return
	}
	if source != jobautomation.RequestSourceAutomation {
		s.markTaskJobEnded(account, projectID, sig, followup)
	}
}

// taskRunLeftResult reports whether a run left the task, as reconciled, in a
// result state: needs_review, completed, failed, blocked or cancelled. The
// task's job ends only then. A run that leaves it in_progress, queued or
// waiting on its delegated tasks (a pended wait_tasks goal) is not the task's
// end, whatever the run's own status: a failed run of a task that is still
// running records nothing either (the reconciled task is failed when that
// run was its work).
func taskRunLeftResult(taskStatus string) bool {
	switch taskStatus {
	case "needs_review", "completed", "failed", "blocked", "cancelled":
		return true
	}
	return false
}

// markTaskJobEnded records that a task's job end waits for the workflow
// runner, so its progress says workflow "pending" until the runner records
// what it matched ("none" when nothing). Only ends the runner reads (the
// machine owner's account) are marked, so no end stays pending for good.
func (s *Server) markTaskJobEnded(account, projectID string, sig pebblestore.Signal, followup string) {
	store, err := s.jobAutomationStore()
	if err != nil || account != s.OutboxAccount() {
		return
	}
	jobID := sig.Attrs["job_id"]
	if err := store.MarkJobEnded(account, projectID, jobID, followup, sig.Seq, sig.At); err != nil {
		log.Printf("swarmd task progress: mark job end of %s: %v", jobID, err)
	}
}

// taskRunPrimaryOrchestrator uses the origin's persisted runtime identity, never
// task input, to distinguish a person-driven conversation from delegated runs.
func taskRunPrimaryOrchestrator(origin pebblestore.SessionSnapshot) bool {
	profile, err := sessionV3AgentProfileFromMetadata(origin.Metadata)
	return err == nil && profile.Name == agentruntime.SwarmOrchestratorAgentID && profile.Mode == agentruntime.ModePrimary &&
		sessionsV3MetadataString(origin.Metadata, "agent_name") == agentruntime.SwarmOrchestratorAgentID &&
		sessionsV3MetadataString(origin.Metadata, "resolved_agent_name") == agentruntime.SwarmOrchestratorAgentID &&
		sessionsV3MetadataString(origin.Metadata, "agent_mode") == agentruntime.ModePrimary &&
		sessionsV3MetadataString(origin.Metadata, "swarm_v3_execution_class") == "primary" &&
		sessionsV3MetadataString(origin.Metadata, "parent_session_id") == "" &&
		sessionsV3MetadataString(origin.Metadata, "worker_run_id") == ""
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
