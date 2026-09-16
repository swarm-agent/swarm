package codex

import "testing"

// Purpose: buildCodexRequestProperties must serialize memory's explicit output
// ceiling while leaving ordinary requests unchanged. This wire-builder test
// prevents a silent dropped cap without credentials or external network access.
func TestMemoryOutputLimit(t *testing.T) {
	p, err := buildCodexRequestProperties(Request{Model: "model", MaxOutputTokens: 321})
	if err != nil || p["max_output_tokens"] != 321 {
		t.Fatal(p, err)
	}
	p, err = buildCodexRequestProperties(Request{Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p["max_output_tokens"]; ok {
		t.Fatal("changed default")
	}
}
