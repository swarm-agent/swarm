package agent

import (
	"strings"
	"testing"
)

// Purpose: WorkerCoderAgentPrompt assigns implementation, the required checks
// and commits to the worker: a job ends when its work is built, tested and
// committed, and no later step validates it. This unit-level text contract is
// the narrowest check of emitted instructions; it does not prove provider
// compliance.
func TestWorkerCoderPromptOwnsValidation(t *testing.T) {
	prompt := WorkerCoderAgentPrompt()
	for _, want := range []string{
		"Implement the change and commit it.",
		"Build and run the checks required by the brief, repository rules and declared job acceptance contract before you hand over",
		"Never claim a check passed without having run it.",
		"Otherwise use bash to read, search and inspect.",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("worker prompt missing %q", want)
		}
	}
	if strings.Contains(strings.ToLower(prompt), "landing") {
		t.Error("worker prompt still defers checks to a landing step")
	}
}
