package api

import (
	"strings"
	"testing"
)

// Purpose: get_job must explain the actual pending tool, requested path and
// recovery, not speculate about sandbox configuration. The formatting boundary
// is the narrowest layer proving these fields and excluding unrelated secrets.
func TestControlMCPJobPermissionWait(t *testing.T) {
	got := controlMCPJobPermissionWait(map[string]any{"tool_name": "search", "requirement": "workspace_scope", "tool_arguments": `{"request":{"requested_path":"sandbox-temp/file"},"secret":"do-not-show"}`})
	for _, want := range []string{"search", "sandbox-temp/file", "use bash inside the sandbox", "deny this pending approval"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "do-not-show") {
		t.Fatal(got)
	}
	got = controlMCPJobPermissionWait(map[string]any{"tool_name": "ask_user"})
	if !strings.Contains(got, "swarm_answer_job") {
		t.Fatal(got)
	}
}
