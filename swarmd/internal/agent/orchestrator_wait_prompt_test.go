package agent

import (
	"strings"
	"testing"
)

// Purpose: the Orchestrator must tell the person before it pends the
// conversation on wait_tasks, and answer them first when it wakes.
// Requirement: the Wait section of SwarmOrchestratorAgentPrompt says to write
// that message in the same turn (tasks, what each does, what done means, what
// happens next, that a message ends the wait) and to answer questions asked
// during the wait first. Threat: the live failure: the turn ends on the tool
// call and the person is left asking why nothing happens. The prompt string is
// the narrowest authority for this instruction.
func TestOrchestratorWaitSectionTellsThePersonFirst(t *testing.T) {
	prompt := SwarmOrchestratorAgentPrompt()
	start := strings.Index(prompt, "\nWait\n")
	end := strings.Index(prompt, "\nRead\n")
	if start < 0 || end < start {
		t.Fatalf("prompt has no Wait section before Read: %q", prompt)
	}
	wait := prompt[start:end]
	for _, want := range []string{
		"Before calling wait_tasks, tell the person in the same turn",
		"which tasks you wait on and what each is doing",
		"what done means (for example pushed to dev)",
		"what happens next",
		"they can message anytime to end the wait",
		"Never end a turn on wait_tasks without that message.",
		"When you wake, first answer anything the person asked during the wait.",
	} {
		if !strings.Contains(wait, want) {
			t.Fatalf("Wait section lacks %q:\n%s", want, wait)
		}
	}
}
