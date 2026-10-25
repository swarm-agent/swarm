package remote

import (
	"strings"
	"testing"
)

// Purpose: relay ToolScope and filterTools must expose definitions to write
// keys, never read keys. The pure scope boundary is the narrowest proof.
func TestCustomAgentWriteScope(t *testing.T) {
	if ToolScope("swarm_define_agent") != ScopeWrite {
		t.Fatal("not write scoped")
	}
	for _, tc := range []struct {
		scopes  []string
		allowed bool
	}{{[]string{ScopeRead}, false}, {[]string{ScopeRead, ScopeWrite}, true}} {
		got := filterTools([]byte(`{"result":{"tools":[{"name":"swarm_define_agent"}]}}`), tc.scopes)
		if strings.Contains(string(got), "swarm_define_agent") != tc.allowed {
			t.Fatalf("unexpected relay tools: %s", got)
		}
	}
}
