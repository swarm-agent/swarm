package openrouter

import (
	iface "swarm/packages/swarmd/internal/provider/interfaces"
	"testing"
)

// Purpose: buildChatCompletionRequest must send memory's explicit max_tokens
// without adding a cap to existing callers. No live provider is required.
func TestMemoryOutputLimit(t *testing.T) {
	q := iface.Request{Model: "model", MaxOutputTokens: 321, Input: []map[string]any{{"role": "user", "content": "hello"}}}
	r, err := buildChatCompletionRequest(q)
	if err != nil || r.MaxTokens != 321 {
		t.Fatal(r, err)
	}
	q.MaxOutputTokens = 0
	r, err = buildChatCompletionRequest(q)
	if err != nil || r.MaxTokens != 0 {
		t.Fatal("changed default", err)
	}
}
