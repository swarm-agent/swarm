package codex

import "testing"

func TestEffectiveContextWindowSupportsGPT55(t *testing.T) {
	if got := EffectiveContextWindow("gpt-5.5", "", 123); got != 400000 {
		t.Fatalf("EffectiveContextWindow(gpt-5.5) = %d, want 400000", got)
	}
	if got := EffectiveContextWindow("gpt-5.5", "1m", 123); got != 1050000 {
		t.Fatalf("EffectiveContextWindow(gpt-5.5, 1m) = %d, want 1050000", got)
	}
}

func TestEffectiveContextWindowKeepsGPT54OneMOverride(t *testing.T) {
	if got := EffectiveContextWindow("gpt-5.4", "1m", 123); got != 1050000 {
		t.Fatalf("EffectiveContextWindow(gpt-5.4, 1m) = %d, want 1050000", got)
	}
}
