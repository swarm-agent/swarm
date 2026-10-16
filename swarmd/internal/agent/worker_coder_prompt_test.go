package agent

import (
	"strings"
	"testing"
)

// Purpose: WorkerCoderAgentPrompt assigns implementation and commits to the
// worker, and builds/tests to landing, preventing duplicate per-job validation.
// This unit-level text contract is the narrowest check of emitted instructions;
// it does not prove provider compliance or successful landing execution.
func TestWorkerCoderPromptDefersValidationToLanding(t *testing.T) {
	prompt := WorkerCoderAgentPrompt()
	for _, want := range []string{
		"Implement the change and commit it.",
		"Do not run builds, test suites or linters: after you hand over, the landing step builds and runs this folder's checks once, and if they fail the output comes back to you in this job.",
		"Use bash only to read, search and inspect.",
		"Your report states the commits, the changed files, and what you did not do or could not verify.",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("worker prompt missing %q", want)
		}
	}
}
