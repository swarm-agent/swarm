package pebblestore

// ProjectTaskWorkflowBranch is what became of a workflow's fast-forward of a
// task's own branch and worktree to the head one of its agent steps moved
// the task's delivery to (a fix committed in that step's own worktree), so a
// later follow-up of the task starts from the delivered work. Advanced is
// false, with Reason, when the branch was left where it was: the move was
// not a fast-forward (a rebase), the worktree had uncommitted changes, the
// task was running, or the task works on another branch.
type ProjectTaskWorkflowBranch struct {
	DeliveryID string `json:"delivery_id"`
	Step       string `json:"step,omitempty"`
	From       string `json:"from,omitempty"`
	Head       string `json:"head"`
	Advanced   bool   `json:"advanced"`
	Reason     string `json:"reason,omitempty"`
	At         int64  `json:"at"`
}
