package google

import (
	iface "swarm/packages/swarmd/internal/provider/interfaces"
	"testing"
)

// Purpose: buildGoogleRequest must preserve an explicit memory ceiling in the
// serialized generation config; omitted caps must preserve conversational defaults.
func TestMemoryOutputLimit(t *testing.T) {
	q := iface.Request{Model: "model", MaxOutputTokens: 321, Input: []map[string]any{{"role": "user", "content": "hello"}}}
	r, err := buildGoogleRequest(q)
	if err != nil || r.GenerationConfig == nil || r.GenerationConfig.MaxOutputTokens != 321 {
		t.Fatal(r, err)
	}
	q.MaxOutputTokens = 0
	r, err = buildGoogleRequest(q)
	if err != nil {
		t.Fatal(err)
	}
	if r.GenerationConfig != nil && r.GenerationConfig.MaxOutputTokens != 0 {
		t.Fatal("changed default")
	}
}
