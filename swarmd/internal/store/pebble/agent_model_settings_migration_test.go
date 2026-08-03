package pebblestore

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestAgentModelSettingsMigrationJoinsAndCleansLegacyRecords(t *testing.T) {
	store := openAgentModelSettingsTestStore(t)
	selection := ModelProfileSelection{Provider: "codex", Model: "gpt", Thinking: "high", ServiceTier: "fast", ContextMode: "full"}
	if err := store.PutJSON(swarmModeSettingsKeyForAccount("account-a"), legacySwarmModeSettingsRecord{AccountScopeID: "account-a", Action: selection, Plan: selection, UpdatedAt: 10}); err != nil {
		t.Fatalf("put mode settings: %v", err)
	}
	legacyUI := json.RawMessage(`{"theme":{"active_id":"tide"},"chat":{"show_header":true},"agents":{"compact":{"provider":"codex","model":"small","thinking":"low"},"finder":{"provider":"codex","model":"finder","thinking":"medium"},"coder":{"provider":"codex","model":"coder","thinking":"high"},"designer":{"provider":"openai","model":"designer","thinking":"medium"},"router":{"provider":"openai","model":"router","thinking":"medium","service_tier":"priority"}},"updated_at":20}`)
	if err := store.PutBytes(KeyUISettingsForAccount("account-a"), legacyUI); err != nil {
		t.Fatalf("put UI settings: %v", err)
	}

	result, err := RunAgentModelSettingsMigration(store)
	if err != nil {
		t.Fatalf("RunAgentModelSettingsMigration(): %v", err)
	}
	if !result.Applied || result.AccountsMigrated != 1 || result.UIRecordsRewrote != 1 {
		t.Fatalf("migration result = %+v", result)
	}
	got, found, err := NewAgentModelSettingsStore(store).GetForAccount("account-a")
	if err != nil || !found || got.Swarm.Action.ContextMode != "full" || got.SystemAgents.Router.ServiceTier != "priority" {
		t.Fatalf("canonical settings = (%+v, %v, %v)", got, found, err)
	}
	if _, found, err := store.GetBytes(swarmModeSettingsKeyForAccount("account-a")); err != nil || found {
		t.Fatalf("legacy mode key found=%v err=%v", found, err)
	}
	ui, found, err := store.GetBytes(KeyUISettingsForAccount("account-a"))
	if err != nil || !found || bytes.Contains(ui, []byte(`"agents"`)) || !bytes.Contains(ui, []byte(`"theme"`)) {
		t.Fatalf("rewritten UI = %s found=%v err=%v", ui, found, err)
	}
	second, err := RunAgentModelSettingsMigration(store)
	if err != nil || !second.AlreadyApplied {
		t.Fatalf("idempotent migration = (%+v, %v)", second, err)
	}
}

func TestAgentModelSettingsMigrationFailsClosedForUnscopedUI(t *testing.T) {
	store := openAgentModelSettingsTestStore(t)
	if err := store.PutBytes(KeyUISettingsDefault, []byte(`{"agents":{}}`)); err != nil {
		t.Fatalf("put unscoped UI: %v", err)
	}
	if _, err := RunAgentModelSettingsMigration(store); err == nil {
		t.Fatal("migration accepted unscoped UI settings")
	}
	if _, found, err := store.GetBytes(agentModelSettingsMigrationKey); err != nil || found {
		t.Fatalf("marker found=%v err=%v after failure", found, err)
	}
}

func TestAgentModelSettingsMigrationMarksValidCanonicalFreshState(t *testing.T) {
	store := openAgentModelSettingsTestStore(t)
	selection := AgentModelAssignment{Provider: "codex", Model: "gpt", Thinking: "high"}
	if _, err := NewAgentModelSettingsStore(store).PutForAccount(AgentModelSettingsRecord{
		AccountScopeID: "account-a",
		Swarm:          SwarmAgentModelAssignments{Action: selection, Plan: selection},
		SystemAgents: SystemAgentModelAssignments{
			Compact: selection, Finder: selection, Coder: selection, Designer: selection, Router: selection,
		},
	}); err != nil {
		t.Fatalf("put canonical settings: %v", err)
	}
	result, err := RunAgentModelSettingsMigration(store)
	if err != nil || !result.Applied || result.AccountsMigrated != 0 {
		t.Fatalf("canonical fresh-state migration = (%+v, %v)", result, err)
	}
}

func TestAgentModelSettingsMigrationRejectsConflictingCanonicalCollision(t *testing.T) {
	store := openAgentModelSettingsTestStore(t)
	selection := AgentModelAssignment{Provider: "codex", Model: "canonical", Thinking: "high"}
	if _, err := NewAgentModelSettingsStore(store).PutForAccount(AgentModelSettingsRecord{
		AccountScopeID: "account-a",
		Swarm:          SwarmAgentModelAssignments{Action: selection, Plan: selection},
		SystemAgents: SystemAgentModelAssignments{
			Compact: selection, Finder: selection, Coder: selection, Designer: selection, Router: selection,
		},
	}); err != nil {
		t.Fatalf("put canonical settings: %v", err)
	}
	legacySelection := ModelProfileSelection{Provider: "openai", Model: "legacy", Thinking: "medium"}
	if err := store.PutJSON(swarmModeSettingsKeyForAccount("account-a"), legacySwarmModeSettingsRecord{AccountScopeID: "account-a", Action: legacySelection, Plan: legacySelection}); err != nil {
		t.Fatalf("put legacy mode: %v", err)
	}
	if err := store.PutBytes(KeyUISettingsForAccount("account-a"), []byte(`{"agents":{"compact":{"provider":"openai","model":"legacy","thinking":"medium"},"finder":{"provider":"openai","model":"legacy","thinking":"medium"},"coder":{"provider":"openai","model":"legacy","thinking":"medium"},"designer":{"provider":"openai","model":"legacy","thinking":"medium"},"router":{"provider":"openai","model":"legacy","thinking":"medium"}}}`)); err != nil {
		t.Fatalf("put legacy UI: %v", err)
	}
	if _, err := RunAgentModelSettingsMigration(store); err == nil {
		t.Fatal("migration accepted conflicting canonical settings")
	}
}

func TestAgentModelSettingsMigrationRejectsUIAgentsWithoutSwarmPair(t *testing.T) {
	store := openAgentModelSettingsTestStore(t)
	if err := store.PutBytes(KeyUISettingsForAccount("account-a"), []byte(`{"agents":{"router":{"provider":"openai","model":"router","thinking":"medium"}}}`)); err != nil {
		t.Fatalf("put UI settings: %v", err)
	}
	if _, err := RunAgentModelSettingsMigration(store); err == nil {
		t.Fatal("migration accepted UI agent data without Action/Plan")
	}
	if _, found, err := store.GetBytes(KeyAgentModelSettingsForAccount("account-a")); err != nil || found {
		t.Fatalf("canonical key found=%v err=%v after failure", found, err)
	}
}
