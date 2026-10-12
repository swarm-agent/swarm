package model

import (
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the claudecode provider serves Anthropic models through the local
// Claude Code CLI, so its catalog lookups, listings and onboarding
// recommendations must come from the Anthropic records yet be keyed to
// claudecode. Threats: Claude Code models grouped under (and selected as) the
// Anthropic API provider; onboarding failing for missing recommendations; the
// Anthropic catalog itself being re-keyed. Owners: catalogProviderModelSources,
// CatalogService.Get/List/RecommendedRoleDefaults. The pinned snapshot is the
// real offline catalog, so this is the narrowest layer with real records.
func TestClaudeCodeCatalogReadsAnthropicRecordsKeyedToClaudeCode(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "catalog.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	catalog := NewCatalogService(pebblestore.NewModelCatalogStore(store))
	if err := catalog.EnsureBootDefaults(); err != nil {
		t.Fatalf("EnsureBootDefaults: %v", err)
	}

	anthropicRecords, err := catalog.List("anthropic", 1000)
	if err != nil || len(anthropicRecords) == 0 {
		t.Fatalf("anthropic records: %d, %v", len(anthropicRecords), err)
	}
	claudeCodeRecords, err := catalog.List("claudecode", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(claudeCodeRecords) != len(anthropicRecords) {
		t.Fatalf("claudecode records = %d, anthropic = %d", len(claudeCodeRecords), len(anthropicRecords))
	}
	for i, record := range claudeCodeRecords {
		if record.Provider != "claudecode" || record.Model != anthropicRecords[i].Model {
			t.Fatalf("record %d = %s/%s, want claudecode/%s", i, record.Provider, record.Model, anthropicRecords[i].Model)
		}
	}
	for _, record := range anthropicRecords {
		if record.Provider != "anthropic" {
			t.Fatalf("anthropic listing was re-keyed: %+v", record)
		}
	}

	lookup, err := catalog.Get("claudecode", anthropicRecords[0].Model)
	if err != nil || !lookup.Found || lookup.Record.Provider != "claudecode" || lookup.Record.ContextWindow != anthropicRecords[0].ContextWindow {
		t.Fatalf("claudecode lookup = %+v, %v", lookup, err)
	}
	if direct, err := catalog.Get("anthropic", anthropicRecords[0].Model); err != nil || direct.Record.Provider != "anthropic" {
		t.Fatalf("anthropic lookup = %+v, %v", direct, err)
	}
	if missing, err := catalog.Get("claudecode", "gpt-5.4"); err != nil || missing.Found {
		t.Fatalf("non-Anthropic model must not resolve for claudecode: %+v, %v", missing, err)
	}

	anthropicDefaults, anthropicOK, err := catalog.RecommendedRoleDefaults("anthropic", "auto", "utility")
	if err != nil {
		t.Fatal(err)
	}
	claudeCodeDefaults, claudeCodeOK, err := catalog.RecommendedRoleDefaults("claudecode", "auto", "utility")
	if err != nil {
		t.Fatal(err)
	}
	if !anthropicOK || !claudeCodeOK || len(claudeCodeDefaults) != 2 {
		t.Fatalf("recommendations: anthropic %v, claudecode %v (%d roles)", anthropicOK, claudeCodeOK, len(claudeCodeDefaults))
	}
	for role, record := range claudeCodeDefaults {
		if record.Provider != "claudecode" || record.Model != anthropicDefaults[role].Model {
			t.Fatalf("role %s = %s/%s, anthropic %s", role, record.Provider, record.Model, anthropicDefaults[role].Model)
		}
	}
}
