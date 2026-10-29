package agent

import (
	"strings"
	"testing"
)

// Purpose: the Orchestrator watches tasks in the background and always
// answers the person; it never pends the conversation.
// Requirement: the Watch section of SwarmOrchestratorAgentPrompt says
// wait_tasks watches and returns at once, that the AI then tells the person
// what it watches and ends its turn, that a new turn brings the outcomes, and
// that a turn never ends without answering the person's latest message. It
// must not say the call pends the conversation or that a message ends it.
// Threat: the live failure of the pending model: a silent turn ended on the
// tool call, and the person's every message cancelling the wait. The prompt
// string is the narrowest authority for this instruction.
func TestOrchestratorWatchSectionAnswersThePerson(t *testing.T) {
	prompt := SwarmOrchestratorAgentPrompt()
	start := strings.Index(prompt, "\nWatch\n")
	end := strings.Index(prompt, "\nRead\n")
	if start < 0 || end < start {
		t.Fatalf("prompt has no Watch section before Read: %q", prompt)
	}
	watch := prompt[start:end]
	for _, want := range []string{
		"Call wait_tasks to watch tasks you need",
		"It returns at once. Then tell the person what you're watching and end your turn.",
		"You'll get a new turn with the outcomes",
		"Never end a turn without answering the person's latest message.",
		"their messages never end a watch",
	} {
		if !strings.Contains(watch, want) {
			t.Fatalf("Watch section lacks %q:\n%s", want, watch)
		}
	}
	for _, retired := range []string{"pends this conversation", "ends the wait", "\nWait\n"} {
		if strings.Contains(prompt, retired) {
			t.Fatalf("prompt still says %q:\n%s", retired, prompt)
		}
	}
}
