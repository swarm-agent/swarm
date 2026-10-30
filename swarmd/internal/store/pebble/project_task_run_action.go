package pebblestore

import "strings"

// ProjectTaskFailedRunAction reports a task's ActionNeeded that describes how
// an earlier run ended (blocked, failed, stopped or cancelled). It describes
// only that run: once a newer run of the task succeeds, it is replaced.
func ProjectTaskFailedRunAction(action string) bool {
	if action == ProjectTaskStoppedAction {
		return true
	}
	for _, prefix := range []string{
		"Action Needed: Supply required input and resume.",
		"Action Needed: Worker run failed",
		"Action Needed: Run failed",
		"Action Needed: Run was stopped",
		"Action Needed: Run was cancelled",
	} {
		if strings.HasPrefix(action, prefix) {
			return true
		}
	}
	return false
}
