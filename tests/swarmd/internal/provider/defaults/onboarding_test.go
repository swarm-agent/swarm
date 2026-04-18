package defaults

import "testing"

func TestLookupAnthropicDefaults(t *testing.T) {
	got, ok := Lookup("anthropic")
	if !ok {
		t.Fatalf("Lookup(anthropic) ok = false")
	}
	if got.ProviderID != "anthropic" {
		t.Fatalf("provider = %q, want anthropic", got.ProviderID)
	}
	if got.PrimaryModel != "claude-opus-4-6" {
		t.Fatalf("primary model = %q", got.PrimaryModel)
	}
	if got.PrimaryThinking != "high" {
		t.Fatalf("primary thinking = %q", got.PrimaryThinking)
	}
	if got.UtilityModel != "claude-haiku-4-5" {
		t.Fatalf("utility model = %q", got.UtilityModel)
	}
	if got.UtilityThinking != "high" {
		t.Fatalf("utility thinking = %q", got.UtilityThinking)
	}
}

func TestLookupCodexDefaults(t *testing.T) {
	got, ok := Lookup("codex")
	if !ok {
		t.Fatalf("Lookup(codex) ok = false")
	}
	if got.ProviderID != "codex" {
		t.Fatalf("provider = %q, want codex", got.ProviderID)
	}
	if got.PrimaryModel != "gpt-5.4" {
		t.Fatalf("primary model = %q", got.PrimaryModel)
	}
	if got.PrimaryThinking != "high" {
		t.Fatalf("primary thinking = %q", got.PrimaryThinking)
	}
	if got.UtilityModel != "gpt-5.4-mini" {
		t.Fatalf("utility model = %q", got.UtilityModel)
	}
	if got.UtilityThinking != "medium" {
		t.Fatalf("utility thinking = %q", got.UtilityThinking)
	}
	wantSubagents := []string{"explorer", "memory", "parallel"}
	if len(got.UtilitySubagents) != len(wantSubagents) {
		t.Fatalf("utility subagents = %v", got.UtilitySubagents)
	}
	for i := range wantSubagents {
		if got.UtilitySubagents[i] != wantSubagents[i] {
			t.Fatalf("utility subagents[%d] = %q, want %q", i, got.UtilitySubagents[i], wantSubagents[i])
		}
	}
}

func TestLookupCopilotDefaults(t *testing.T) {
	got, ok := Lookup("copilot")
	if !ok {
		t.Fatalf("Lookup(copilot) ok = false")
	}
	if got.PrimaryModel != "gpt-5.4" {
		t.Fatalf("primary model = %q", got.PrimaryModel)
	}
	if got.PrimaryThinking != "high" {
		t.Fatalf("primary thinking = %q", got.PrimaryThinking)
	}
	if got.UtilityModel != "claude-haiku-4.5" {
		t.Fatalf("utility model = %q", got.UtilityModel)
	}
	if got.UtilityThinking != "high" {
		t.Fatalf("utility thinking = %q", got.UtilityThinking)
	}
}

func TestLookupFireworksDefaults(t *testing.T) {
	got, ok := Lookup("fireworks")
	if !ok {
		t.Fatalf("Lookup(fireworks) ok = false")
	}
	if got.PrimaryModel != "accounts/fireworks/models/kimi-k2p5" {
		t.Fatalf("primary model = %q", got.PrimaryModel)
	}
	if got.UtilityModel != "accounts/fireworks/models/kimi-k2p5" {
		t.Fatalf("utility model = %q", got.UtilityModel)
	}
	if got.PrimaryThinking != "high" || got.UtilityThinking != "high" {
		t.Fatalf("thinking = %q/%q, want high/high", got.PrimaryThinking, got.UtilityThinking)
	}
}

func TestLookupGoogleDefaults(t *testing.T) {
	got, ok := Lookup("google")
	if !ok {
		t.Fatalf("Lookup(google) ok = false")
	}
	if got.PrimaryModel != "gemini-3.1-pro-preview" {
		t.Fatalf("primary model = %q", got.PrimaryModel)
	}
	if got.UtilityModel != "gemini-3-flash-preview" {
		t.Fatalf("utility model = %q", got.UtilityModel)
	}
	if got.PrimaryThinking != "high" || got.UtilityThinking != "high" {
		t.Fatalf("thinking = %q/%q, want high/high", got.PrimaryThinking, got.UtilityThinking)
	}
}

func TestLookupUnknownProvider(t *testing.T) {
	if _, ok := Lookup("unknown"); ok {
		t.Fatalf("Lookup(unknown) ok = true, want false")
	}
}

func TestSupportedProvidersSorted(t *testing.T) {
	got := SupportedProviders()
	want := []string{"anthropic", "codex", "copilot", "fireworks", "google"}
	if len(got) != len(want) {
		t.Fatalf("providers = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("providers[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
