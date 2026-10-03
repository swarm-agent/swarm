package agent

import (
	"strings"
	"testing"
)

// Purpose: the compiled deployment harness must direct cwd-free project chats to
// explicit authorized source inspection and receipt-backed Coder delegation.
// This prompt contract test prevents advice to leave the project; runtime security
// is separately exercised at source resolution and isolated allocation boundaries.
func TestOrchestratorProjectSourceGuidance(t *testing.T) {
	prompt := SwarmOrchestratorAgentPrompt()
	for _, want := range []string{"action=list_sources", "action=inspect_source", "never choose the first project workspace", "Only claim launched after a linked execution/session receipt", "Small coding tasks route to Coder"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("missing deployment guidance %q", want)
		}
	}
}
