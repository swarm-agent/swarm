package remote

import (
	"strings"
	"testing"
)

// Purpose: ToolScope permits local write clients to request a human-entered
// key, while filterTools retains the relay exclusion at every level. These
// pure policy boundaries are the narrowest test for accidental relay exposure.
func TestProviderKeyWriteAndRelayExclusion(t *testing.T) {
	if ToolScope("swarm_request_provider_key") != ScopeWrite {
		t.Fatal("key request must be write")
	}
	if !localOnlyTools["swarm_request_provider_key"] {
		t.Fatal("key request exposed to relay")
	}
	for _, scopes := range [][]string{{ScopeRead}, {ScopeRead, ScopeWrite}, {ScopeRead, ScopeWrite, ScopeApprove, ScopeManage}} {
		got := filterTools([]byte(`{"result":{"tools":[{"name":"swarm_request_provider_key"},{"name":"swarm_list_providers"}]}}`), scopes)
		if strings.Contains(string(got), "swarm_request_provider_key") || !strings.Contains(string(got), "swarm_list_providers") {
			t.Fatalf("incorrect relay tools: %s", got)
		}
	}
}
