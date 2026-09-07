package pebblestore

import (
	"errors"
	"path/filepath"
	"testing"
)

// Purpose: RestoreForAccount's locked compare-and-replace must reject stale
// and incomplete replacements before any write, preserving concurrent changes.
// A temporary store is the narrowest layer proving the durable postcondition.
func TestAgentModelRestoreRejectsStaleAndPartialRecords(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "restore"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewAgentModelSettingsStore(db)
	a := AgentModelAssignment{Provider: "codex", Model: "old", Thinking: "high"}
	original := AgentModelSettingsRecord{AccountScopeID: "account-one", Swarm: SwarmAgentModelAssignments{Action: a, Plan: a}, SystemAgents: SystemAgentModelAssignments{Compact: a, Finder: a, Coder: a, Designer: a, Router: a}, UpdatedAt: 1}
	if _, err := store.PutForAccount(original); err != nil {
		t.Fatal(err)
	}
	partial := original
	partial.SystemAgents.Router = AgentModelAssignment{}
	if _, err := store.RestoreForAccount(original, partial); err == nil {
		t.Fatal("partial accepted")
	}
	actual, _, _ := store.GetForAccount(original.AccountScopeID)
	if actual != original {
		t.Fatal("partial write")
	}
	changed := a
	changed.Model = "concurrent"
	current, err := store.UpdateSystemAgentForAccount(original.AccountScopeID, "coder", changed, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RestoreForAccount(original, original); !errors.Is(err, ErrAgentModelSettingsStale) {
		t.Fatalf("stale result %v", err)
	}
	actual, _, _ = store.GetForAccount(original.AccountScopeID)
	if actual != current {
		t.Fatal("concurrent write lost")
	}
}
