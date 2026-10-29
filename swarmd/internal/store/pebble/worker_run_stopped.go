package pebblestore

import "strings"

// WorkerRunStoppedPrefix starts the error of a worker job whose own run was
// stopped (a session stop such as swarm_stop_run) rather than cancelled
// (swarm_stop_job). Such a receipt settles "failed" with this error, which
// releases the job's capacity and keeps the job open: its session, branch and
// worktree stay, and a follow-up message continues it on the same branch.
const WorkerRunStoppedPrefix = "stopped: "

// WorkerRunStopped reports whether r is a stopped worker job that a follow-up
// can still continue. A later cancel (CancelRequested) closes it for good.
func WorkerRunStopped(r WorkerRunRecord) bool {
	return r.Status == "failed" && !r.CancelRequested && strings.HasPrefix(r.Error, WorkerRunStoppedPrefix)
}
