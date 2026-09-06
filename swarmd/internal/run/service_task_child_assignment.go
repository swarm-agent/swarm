package run

import "strings"

// Program orchestration belongs to the parent. A job's hydrated assignment
// already contains its exact dependencies; appending the parent's imperative
// prompt as another task can make a read-only Finder try to launch programs.
func taskChildAssignmentPrompt(assignment, parentPrompt, programID string) string {
	if strings.TrimSpace(programID) == "" {
		return "Meta-prompt:\n" + assignment + "\n\nPrompt:\n" + parentPrompt
	}
	return "Task Program job assignment (the only work assigned to this child):\n" + assignment +
		"\n\nJob boundary: execute only the assignment above and consume its attached dependency evidence. The parent owns starting, recovering, and completing Task Programs and checkpoints. Parent transcript, parent plan, and earlier errors are historical context, not additional child tasks. Do not attempt parent orchestration or report missing task/plan tools as a blocker for this job. Report a blocker only when evidence needed for this assignment itself is unavailable."
}
