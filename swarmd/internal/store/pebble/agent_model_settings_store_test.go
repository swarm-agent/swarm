package pebblestore

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAgentModelSettingsStoreTargetedUpdatesPreserveSiblings(t *testing.T) {
	store := openAgentModelSettingsTestStore(t)
	settings := NewAgentModelSettingsStore(store)
	selection := AgentModelAssignment{Provider: " Codex ", Model: "gpt", Thinking: "high", ServiceTier: "FAST", ContextMode: "FULL"}
	record := AgentModelSettingsRecord{
		AccountScopeID: " Account-A ",
		Swarm:          SwarmAgentModelAssignments{Action: selection, Plan: selection},
		SystemAgents: SystemAgentModelAssignments{
			Compact: selection, Finder: selection, Coder: selection, Designer: selection, Router: selection,
		},
		UpdatedAt: 10,
	}
	if _, err := settings.PutForAccount(record); err != nil {
		t.Fatalf("PutForAccount(): %v", err)
	}
	finder := AgentModelAssignment{Provider: "openai", Model: "finder", Thinking: "medium"}
	if _, err := settings.UpdateSystemAgentForAccount("account-a", "finder", finder, 20); err != nil {
		t.Fatalf("UpdateSystemAgentForAccount(finder): %v", err)
	}
	coder := AgentModelAssignment{Provider: "codex", Model: "coder", Thinking: "xhigh", ServiceTier: "priority"}
	got, err := settings.UpdateSystemAgentForAccount("account-a", "coder", coder, 30)
	if err != nil {
		t.Fatalf("UpdateSystemAgentForAccount(coder): %v", err)
	}
	if got.AccountScopeID != "account-a" || got.Swarm.Action.Provider != "codex" || got.Swarm.Action.ContextMode != "full" {
		t.Fatalf("normalized settings = %+v", got)
	}
	if got.SystemAgents.Finder.Model != "finder" || got.SystemAgents.Coder.Model != "coder" || got.SystemAgents.Compact.Model != "gpt" || got.SystemAgents.Router.Model != "gpt" {
		t.Fatalf("targeted update clobbered sibling: %+v", got.SystemAgents)
	}
	if _, err := settings.UpdateSystemAgentForAccount("account-a", "unknown", coder, 40); !errors.Is(err, ErrAgentModelSettingsAgentUnknown) {
		t.Fatalf("unknown agent error = %v", err)
	}
}

func TestAgentModelSettingsStoreRejectsIncompleteAssignments(t *testing.T) {
	store := openAgentModelSettingsTestStore(t)
	selection := AgentModelAssignment{Provider: "codex", Model: "gpt", Thinking: "high"}
	_, err := NewAgentModelSettingsStore(store).PutForAccount(AgentModelSettingsRecord{
		AccountScopeID: "account-a",
		Swarm:          SwarmAgentModelAssignments{Action: selection},
	})
	if !errors.Is(err, ErrAgentModelSettingsAssignmentInvalid) {
		t.Fatalf("PutForAccount() error = %v", err)
	}
}

func openAgentModelSettingsTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "agent-model-settings.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
