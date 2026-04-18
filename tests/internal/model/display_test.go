package model

import "testing"

func TestDisplayModelNameStripsFireworksPrefix(t *testing.T) {
	got := DisplayModelName("fireworks", "accounts/fireworks/models/gpt-oss-120b")
	if got != "gpt-oss-120b" {
		t.Fatalf("DisplayModelName() = %q, want %q", got, "gpt-oss-120b")
	}
}

func TestDisplayModelLabelPreservesCodexSuffixesAfterNormalization(t *testing.T) {
	got := DisplayModelLabel("codex", "gpt-5.4", "fast", "1m")
	if got != "gpt-5.4" {
		t.Fatalf("DisplayModelLabel() = %q, want %q", got, "gpt-5.4")
	}
}
