package model

import "testing"

func TestCodexContextWindowSupportsGPT55(t *testing.T) {
	if got := CodexContextWindow("codex", "gpt-5.5", "", 0); got != 400000 {
		t.Fatalf("CodexContextWindow(gpt-5.5) = %d, want 400000", got)
	}
	if got := CodexContextWindow("codex", "gpt-5.5", "1m", 0); got != 1050000 {
		t.Fatalf("CodexContextWindow(gpt-5.5, 1m) = %d, want 1050000", got)
	}
}

func TestCodexRuntimeSupportIncludesGPT55(t *testing.T) {
	if !Codex1MEnabled("codex", "gpt-5.5", "1m") {
		t.Fatalf("Codex1MEnabled should be true for gpt-5.5")
	}
	if !Codex1MEnabled("codex", "gpt-5.4", "1m") {
		t.Fatalf("Codex1MEnabled should stay true for gpt-5.4")
	}
	if !SupportsCodexFastMode("codex", "gpt-5.5") {
		t.Fatalf("SupportsCodexFastMode should be true for gpt-5.5")
	}
}
