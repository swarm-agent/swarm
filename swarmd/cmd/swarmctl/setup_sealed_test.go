package main

import (
	"bytes"
	"strings"
	"testing"
)

// Purpose: a sealed agent file may only grant the client tools it declares,
// with safe names, so `swarmctl setup sealed-agent` cannot be used to give an
// agent a built-in tool. Validation happens before any daemon request.
func TestSetupSealedAgentRejectsUndeclaredOrUnsafeTools(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"tools":[],"agent":{"name":"evil","prompt":"x","tools":["bash"]}}`, "not one of the client tools"},
		{`{"tools":[{"name":"Bad Name","input_schema":{"type":"object"}}],"agent":{"name":"a","prompt":"x","tools":[]}}`, "lowercase"},
		{`{"tools":[],"agent":{"name":"a","prompt":"","tools":[]}}`, "prompt"},
		{`{"tools":[],"agent":{"name":"a","prompt":"x","tools":[],"tool_contract":{"preset":"read_write"}}}`, "invalid sealed agent definition"},
	} {
		err := runSetupSealedAgent([]string{"sealed-agent", "--stdin", "--socket", "/nonexistent.sock"}, strings.NewReader(tc.body), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err=%v, want %q", tc.body, err, tc.want)
		}
	}
	if err := runSetupSealedAgent([]string{"sealed-agent"}, strings.NewReader("{}"), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--stdin") {
		t.Fatalf("missing --stdin accepted: %v", err)
	}
}
