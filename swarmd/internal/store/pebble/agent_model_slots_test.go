package pebblestore

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

// Purpose: UpdateSwarmSlotForAccount serializes read-modify-write under the
// existing store lock, preventing concurrent role editors from erasing siblings.
// Real temporary Pebble storage is the narrowest layer proving concurrency,
// rejected-write postconditions, and close/reopen persistence without providers.
func TestAgentModelSwarmSlotsConcurrentAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()
	settings := NewAgentModelSettingsStore(store)
	base := AgentModelAssignment{Provider: "test-provider", Model: "base", Thinking: "high"}
	_, err = settings.PutForAccount(AgentModelSettingsRecord{
		AccountScopeID: "account", Swarm: SwarmAgentModelAssignments{Action: base, Plan: base},
		SystemAgents: SystemAgentModelAssignments{Compact: base, Finder: base, Coder: base, Designer: base, Router: base},
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, slot := range []string{"action", "plan"} {
		wg.Add(1)
		go func(slot string) {
			defer wg.Done()
			value := base
			value.Model = slot
			_, err := settings.UpdateSwarmSlotForAccount("account", slot, value, 2)
			errs <- err
		}(slot)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	before, _, err := settings.GetForAccount("account")
	if err != nil {
		t.Fatal(err)
	}
	if before.Swarm.Action.Model != "action" || before.Swarm.Plan.Model != "plan" || before.SystemAgents.Coder != base {
		t.Fatalf("siblings lost: %+v", before)
	}
	if _, err := settings.UpdateSwarmSlotForAccount("account", "action", AgentModelAssignment{}, 3); !errors.Is(err, ErrAgentModelSettingsAssignmentInvalid) {
		t.Fatalf("invalid write: %v", err)
	}
	if _, err := settings.UpdateSwarmSlotForAccount("account", "unknown", base, 3); !errors.Is(err, ErrAgentModelSettingsAgentUnknown) {
		t.Fatalf("unknown slot: %v", err)
	}
	after, _, err := settings.GetForAccount("account")
	if err != nil || after != before {
		t.Fatalf("rejected write mutated state: %+v %v", after, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	after, found, err := NewAgentModelSettingsStore(store).GetForAccount("account")
	if err != nil || !found || after != before {
		t.Fatalf("restart lost assignments: %+v %v", after, err)
	}
}
