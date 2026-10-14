package api

import (
	"log"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// projectTaskDeclaredBlocker projects only an explicit outcome of this execution.
// Provider completion is transport completion, not proof that the task succeeded.
func projectTaskDeclaredBlocker(task *pebblestore.ProjectTaskRecord, sess pebblestore.SessionSnapshot, state pebblestore.V3SessionRunState) bool {
	if task.SessionID != sess.ID || task.AccountID != sess.AccountScopeID || state.AccountScopeID != sess.AccountScopeID ||
		state.RunID == "" || state.Status != pebblestore.V3RunIntentCompleted || state.Active ||
		(task.ExecutionRunID() != "" && task.ExecutionRunID() != state.RunID) ||
		sess.Metadata["lifecycle_signal"] != "blocked" || sess.Metadata["lifecycle_signal_run_id"] != state.RunID {
		return false
	}
	reason, _ := sess.Metadata["blocker_reason"].(string)
	if strings.TrimSpace(reason) == "" {
		return false
	}
	task.Status = "blocked"
	task.LastError = reason
	task.ActionNeeded = "Action Needed: Supply required input and resume. " + reason
	return true
}

// signalJobBlocked records job.blocked (a warning) when a job stopped on a
// blocker it declared: the run itself ended normally (worker.run.succeeded),
// so without this nothing watching the feed would hear that the job did not
// do its work. The reason stays on the machine; the signal names the job.
func (s *Server) signalJobBlocked(account string, task *pebblestore.ProjectTaskRecord) {
	if s == nil || s.signals == nil || task == nil {
		return
	}
	refs := map[string]string{"project_id": task.ProjectID, "task_id": task.ID}
	attrs := map[string]string{}
	if task.WorkerID != "" && task.WorkerRunID != "" {
		refs["worker_id"], refs["run_id"] = task.WorkerID, task.WorkerRunID
		attrs["job_id"] = controlMCPJobID(controlMCPJobWorker, task.WorkerID, task.WorkerRunID)
	}
	name := strings.TrimSpace(task.WorkerName)
	if name == "" {
		name = "A job"
	}
	if _, err := s.signals.Append(pebblestore.Signal{
		Kind: "job.blocked", Severity: pebblestore.SignalSeverityWarning, Source: pebblestore.SignalSourceSwarmd, Account: account,
		Summary: name + " stopped on a blocker: " + controlMCPTruncate(task.LastError, 160), DedupKey: "job.blocked:" + task.ID + ":" + task.ExecutionRunID(),
		Refs: refs, Attrs: attrs,
	}); err != nil {
		log.Printf("swarmd signal job.blocked not recorded: %v", err)
	}
}
