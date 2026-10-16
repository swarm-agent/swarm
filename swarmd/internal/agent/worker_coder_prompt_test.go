package agent

import (
	"strings"
	"testing"
)

// Purpose: WorkerCoderAgentPrompt assigns implementation and commits to the
// worker, including commands named by the brief, and self-initiated verification
// builds/tests to landing, preventing blocked jobs and duplicate validation.
// This unit-level text contract is the narrowest check of emitted instructions;
// it does not prove provider compliance or successful landing execution.
func TestWorkerCoderPromptDefersValidationToLanding(t *testing.T) {
	prompt := WorkerCoderAgentPrompt()
	for _, want := range []string{
		"Implement the change and commit it.",
		"Run the commands the brief names as part of the work (setup, data refresh, generation, a check the brief requires on what you commit).",
		"Do not run builds, test suites or linters on your own initiative to check your code change: after you hand over, the landing step builds and runs this folder's checks once, and if they fail the output comes back to you in this job.",
		"Otherwise use bash to read, search and inspect.",
		"Your report states the commits, the changed files, and what you did not do or could not verify.",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("worker prompt missing %q", want)
		}
	}
}
