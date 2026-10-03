package run

import (
	"strings"
	"testing"
)

// Purpose: appendHostRuntimeContext must not inspect the daemon cwd or prescribe
// repository setup for workspace-free project orchestration. This pure formatting
// boundary is the narrowest proof that an empty root cannot become a fake repo.
func TestProjectSourceContextWithoutAmbientCheckout(t *testing.T) {
	got := appendHostRuntimeContext("base", "", nil)
	for _, want := range []string{"base", "No ambient repository checkout", "list_sources", "inspect_source", "Delegate from this project chat", "No first-workspace fallback"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	for _, forbidden := range []string{"workspace_git_state: not_repository", "This directory is not a valid Swarm workspace", "Tools run directly on the host workspace path: ."} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("misleading project guidance: %s", got)
		}
	}
}
