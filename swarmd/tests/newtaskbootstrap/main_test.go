package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the offline test helper may initialize only an exclusively new owned
// store with explicit complete assignments. BootstrapFirstIdentity/PutForAccount
// own persistence; this package layer proves no cross-account overwrite, no
// secret/partial-input initialization, and canonical readback after reopening.
// These are hermetic helper tests, not browser/agent qualification.
func TestOwnedBootstrap(t *testing.T) {
	tmp := t.TempDir()
	root, err := os.MkdirTemp(tmp, "new-task-daemon-")
	if err != nil {
		t.Fatal(err)
	}
	owner := "new-task-smoke-00000000-0000-4000-8000-000000000000"
	if err := os.WriteFile(filepath.Join(root, "owner"), []byte(owner), 0600); err != nil {
		t.Fatal(err)
	}
	assignment := pebblestore.AgentModelAssignment{Provider: "fixture-provider", Model: "fixture-model", Thinking: "fixture-thinking"}
	value := input{
		Swarm:        pebblestore.SwarmAgentModelAssignments{Action: assignment, Plan: assignment},
		SystemAgents: pebblestore.SystemAgentModelAssignments{Compact: assignment, Finder: assignment, Coder: assignment, Designer: assignment, Router: assignment},
	}
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap(root, owner, tmp, strings.NewReader(`{"token":"not-an-assignment"}`)); err == nil {
		t.Fatal("accepted secret/partial input")
	}
	if _, err := os.Stat(filepath.Join(root, "db")); !os.IsNotExist(err) {
		t.Fatal("invalid input created state")
	}
	if err := bootstrap(root, "new-task-smoke-11111111-1111-4111-8111-111111111111", tmp, strings.NewReader(string(payload))); err == nil {
		t.Fatal("accepted wrong owner")
	}
	if _, err := os.Stat(filepath.Join(root, "db")); !os.IsNotExist(err) {
		t.Fatal("wrong owner created state")
	}
	if err := bootstrap(root, owner, tmp, strings.NewReader(string(payload))); err != nil {
		t.Fatal(err)
	}
	store, err := pebblestore.Open(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	summary, err := identity.NewService(pebblestore.NewIdentityStore(store)).StateSummary()
	if err != nil || summary.CurrentUser == nil || summary.CurrentUser.Username != owner || summary.AccountScope == nil {
		t.Fatal("missing canonical identity")
	}
	account := summary.AccountScope.ID
	settings, found, err := pebblestore.NewAgentModelSettingsStore(store).GetForAccount(account)
	if err != nil || !found || settings.Swarm != value.Swarm || settings.SystemAgents != value.SystemAgents {
		t.Fatal("canonical assignment readback mismatch")
	}
	if _, found, err := pebblestore.NewAgentModelSettingsStore(store).GetForAccount("unrelated-account"); err != nil || found {
		t.Fatal("cross-account settings written")
	}
	// Startup's maintained migration accepts the new canonical record unchanged.
	if _, err := pebblestore.RunAgentModelSettingsMigration(store); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	value.Swarm.Action.Model = "replacement-must-not-land"
	replacement, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap(root, owner, tmp, strings.NewReader(string(replacement))); err == nil {
		t.Fatal("overwrote existing state")
	}
	store, err = pebblestore.Open(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	after, found, err := pebblestore.NewAgentModelSettingsStore(store).GetForAccount(account)
	if err != nil || !found || after != settings {
		t.Fatal("rejected overwrite changed settings")
	}
}

// Purpose: bootstrap path validation prevents outside/symlink state mutation.
// Filesystem checks precede Pebble open; asserting absent DB proves no partial
// unauthorized state rather than merely a returned error.
func TestBootstrapRejectsPathAndInput(t *testing.T) {
	tmp := t.TempDir()
	root, err := os.MkdirTemp(tmp, "new-task-daemon-")
	if err != nil {
		t.Fatal(err)
	}
	owner := "new-task-smoke-00000000-0000-4000-8000-000000000000"
	if err := os.WriteFile(filepath.Join(root, "owner"), []byte(owner), 0600); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`{}`, `{} {}`, strings.Repeat("x", 8193)} {
		if _, err := decode(strings.NewReader(payload)); err == nil {
			t.Fatal("invalid assignments accepted")
		}
	}
	assignment := pebblestore.AgentModelAssignment{Provider: "fixture-provider", Model: "fixture-model", Thinking: "fixture-thinking"}
	value := input{Swarm: pebblestore.SwarmAgentModelAssignments{Action: assignment, Plan: assignment}, SystemAgents: pebblestore.SystemAgentModelAssignments{Compact: assignment, Finder: assignment, Coder: assignment, Designer: assignment, Router: assignment}}
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(tmp, "new-task-daemon-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap(alias, owner, tmp, strings.NewReader(string(payload))); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := bootstrap(root, owner, root, strings.NewReader(string(payload))); err == nil {
		t.Fatal("scratch root accepted")
	}
	outside := t.TempDir()
	if err := bootstrap(root, owner, outside, strings.NewReader(string(payload))); err == nil {
		t.Fatal("outside root accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "db")); !os.IsNotExist(err) {
		t.Fatal("rejected path created DB")
	}
}
