package ui

import "testing"

func TestModelsModalEntryDisplayNameUsesNormalizedModelLabel(t *testing.T) {
	entry := ModelsModalEntry{Provider: "fireworks", Model: "accounts/fireworks/models/gpt-oss-20b"}
	if got := entry.DisplayName(); got != "gpt-oss-20b" {
		t.Fatalf("DisplayName() = %q, want %q", got, "gpt-oss-20b")
	}
}
