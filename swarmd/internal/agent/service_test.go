package agent

import (
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func newTestService(t *testing.T) (*Service, *pebblestore.AgentStore) {
	t.Helper()

	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agents.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	agents := pebblestore.NewAgentStore(store)
	return NewService(agents, eventLog), agents
}

func TestResolveAgentAllowsEnabledNonPrimaryProfiles(t *testing.T) {
	svc, agents := newTestService(t)
	if err := agents.PutProfile(pebblestore.AgentProfile{
		Name:             "memory",
		Mode:             ModeSubagent,
		Description:      "Memory profile",
		Provider:         "codex",
		Model:            "gpt-5-codex",
		Thinking:         "high",
		Prompt:           "Remember things.",
		ExecutionSetting: pebblestore.AgentExecutionSettingRead,
		Enabled:          true,
	}); err != nil {
		t.Fatalf("put memory profile: %v", err)
	}

	profile, err := svc.ResolveAgent("memory")
	if err != nil {
		t.Fatalf("resolve any agent: %v", err)
	}
	if profile.Name != "memory" || profile.Mode != ModeSubagent {
		t.Fatalf("resolved profile = %+v", profile)
	}

	if _, err := svc.ResolvePrimary("memory"); err == nil {
		t.Fatalf("ResolvePrimary(memory) unexpectedly succeeded")
	}
}

func TestDeleteSwarmRequiresAnotherPrimary(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	if _, _, _, err := svc.Delete("swarm"); err == nil || !strings.Contains(err.Error(), "last primary") {
		t.Fatalf("Delete(swarm) with no other primary error = %v, want last primary", err)
	}

	enabled := true
	if _, _, _, err := svc.Upsert(UpsertInput{
		Name:        "replacement",
		Mode:        ModePrimary,
		Description: "replacement primary",
		Prompt:      "Handle primary tasks.",
		Enabled:     &enabled,
	}); err != nil {
		t.Fatalf("create replacement primary: %v", err)
	}

	result, _, _, err := svc.Delete("swarm")
	if err != nil {
		t.Fatalf("Delete(swarm) with replacement primary error = %v", err)
	}
	if result.Deleted != "swarm" {
		t.Fatalf("deleted = %q, want swarm", result.Deleted)
	}
	if result.ActivePrimary != "replacement" {
		t.Fatalf("active primary after deleting swarm = %q, want replacement", result.ActivePrimary)
	}
	if _, ok, err := svc.GetProfile("swarm"); err != nil || ok {
		t.Fatalf("GetProfile(swarm) after delete ok=%v err=%v, want missing", ok, err)
	}
}

func TestDeletePrimaryRequiresAnotherPrimaryForEveryPrimary(t *testing.T) {
	svc, _ := newTestService(t)
	enabled := true
	if _, _, _, err := svc.Upsert(UpsertInput{
		Name:        "solo",
		Mode:        ModePrimary,
		Description: "only primary",
		Prompt:      "Handle primary tasks.",
		Enabled:     &enabled,
	}); err != nil {
		t.Fatalf("create solo primary: %v", err)
	}
	if _, _, _, err := svc.ActivatePrimary("solo"); err != nil {
		t.Fatalf("activate solo primary: %v", err)
	}

	if _, _, _, err := svc.Delete("solo"); err == nil || !strings.Contains(err.Error(), "last primary") {
		t.Fatalf("Delete(solo) error = %v, want last primary", err)
	}
}

func TestMemoryRemainsProtectedFromDelete(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	if _, _, _, err := svc.Delete("memory"); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("Delete(memory) error = %v, want protected", err)
	}
}
