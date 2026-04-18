package agent

import (
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestEnsureDefaultsSeedsPrimaryAndSubagents(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-defaults.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	svc := NewService(pebblestore.NewAgentStore(store), events)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	state, err := svc.ListState(100)
	if err != nil {
		t.Fatalf("ListState() error = %v", err)
	}
	if state.ActivePrimary != "swarm" {
		t.Fatalf("active primary = %q, want swarm", state.ActivePrimary)
	}
	if state.Version <= 0 {
		t.Fatalf("version = %d, want > 0", state.Version)
	}
	if len(state.Profiles) < 5 {
		t.Fatalf("profiles = %d, want >= 5", len(state.Profiles))
	}
	if _, ok := state.ActiveSubagent["explorer"]; !ok {
		t.Fatalf("missing default explorer subagent mapping")
	}
}

func TestUpsertAndActivatePrimary(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-activate.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	svc := NewService(pebblestore.NewAgentStore(store), events)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	_, _, _, err = svc.Upsert(UpsertInput{
		Name:    "review",
		Mode:    ModePrimary,
		Prompt:  "You are Review.",
		Enabled: boolPtr(true),
	})
	if err != nil {
		t.Fatalf("Upsert(review) error = %v", err)
	}

	active, _, _, err := svc.ActivatePrimary("review")
	if err != nil {
		t.Fatalf("ActivatePrimary(review) error = %v", err)
	}
	if active != "review" {
		t.Fatalf("active primary = %q, want review", active)
	}

	_, _, _, err = svc.Upsert(UpsertInput{
		Name:    "worker",
		Mode:    ModeSubagent,
		Prompt:  "subagent",
		Enabled: boolPtr(true),
	})
	if err != nil {
		t.Fatalf("Upsert(worker) error = %v", err)
	}
	if _, _, _, err := svc.ActivatePrimary("worker"); err == nil {
		t.Fatalf("ActivatePrimary(worker) expected error, got nil")
	}
}

func TestUpsertAllowsExplicitInheritClearForProviderModelThinking(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-inherit-clear.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	svc := NewService(pebblestore.NewAgentStore(store), events)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	_, _, _, err = svc.Upsert(UpsertInput{
		Name:     "worker",
		Mode:     ModeSubagent,
		Provider: "codex",
		Model:    "gpt-5.3-codex",
		Thinking: "high",
		Prompt:   "worker prompt",
		Enabled:  boolPtr(true),
	})
	if err != nil {
		t.Fatalf("Upsert(worker initial) error = %v", err)
	}

	updated, _, _, err := svc.Upsert(UpsertInput{
		Name:        "worker",
		Provider:    "",
		Model:       "",
		Thinking:    "",
		ProviderSet: true,
		ModelSet:    true,
		ThinkingSet: true,
		Enabled:     boolPtr(true),
	})
	if err != nil {
		t.Fatalf("Upsert(worker clear inherit) error = %v", err)
	}

	if updated.Provider != "" {
		t.Fatalf("provider = %q, want empty inherit", updated.Provider)
	}
	if updated.Model != "" {
		t.Fatalf("model = %q, want empty inherit", updated.Model)
	}
	if updated.Thinking != "" {
		t.Fatalf("thinking = %q, want empty inherit", updated.Thinking)
	}
	if strings.TrimSpace(updated.Prompt) != "worker prompt" {
		t.Fatalf("prompt changed unexpectedly: %q", updated.Prompt)
	}
}

func TestDeletePrimaryFallsBackToSwarm(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-delete-primary.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	svc := NewService(pebblestore.NewAgentStore(store), events)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	_, _, _, err = svc.Upsert(UpsertInput{
		Name:    "review",
		Mode:    ModePrimary,
		Prompt:  "You are Review.",
		Enabled: boolPtr(true),
	})
	if err != nil {
		t.Fatalf("Upsert(review) error = %v", err)
	}
	if _, _, _, err := svc.ActivatePrimary("review"); err != nil {
		t.Fatalf("ActivatePrimary(review) error = %v", err)
	}

	result, _, _, err := svc.Delete("review")
	if err != nil {
		t.Fatalf("Delete(review) error = %v", err)
	}
	if result.Deleted != "review" {
		t.Fatalf("deleted = %q, want review", result.Deleted)
	}
	if result.ActivePrimary != "swarm" {
		t.Fatalf("active primary = %q, want swarm", result.ActivePrimary)
	}

	state, err := svc.ListState(100)
	if err != nil {
		t.Fatalf("ListState() error = %v", err)
	}
	if state.ActivePrimary != "swarm" {
		t.Fatalf("state active primary = %q, want swarm", state.ActivePrimary)
	}
}

func TestDeleteSwarmIsRejected(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-delete-swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	svc := NewService(pebblestore.NewAgentStore(store), events)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	if _, _, _, err := svc.Delete("swarm"); err == nil {
		t.Fatalf("Delete(swarm) expected error, got nil")
	}
}

func TestDeleteMemoryIsRejected(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-delete-memory.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	svc := NewService(pebblestore.NewAgentStore(store), events)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	if _, _, _, err := svc.Delete("memory"); err == nil {
		t.Fatalf("Delete(memory) expected error, got nil")
	}
}

func TestDeleteLastPrimaryIsRejected(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-delete-last-primary.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	agentStore := pebblestore.NewAgentStore(store)
	svc := NewService(agentStore, events)

	// Simulate a corrupted/non-default store where only one primary exists.
	if err := agentStore.PutProfile(pebblestore.AgentProfile{
		Name:      "solo",
		Mode:      ModePrimary,
		Provider:  "codex",
		Prompt:    "solo primary",
		Enabled:   true,
		UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("PutProfile(solo) error = %v", err)
	}
	if err := agentStore.SetActivePrimary("solo"); err != nil {
		t.Fatalf("SetActivePrimary(solo) error = %v", err)
	}
	if err := agentStore.SetVersion(1); err != nil {
		t.Fatalf("SetVersion(1) error = %v", err)
	}

	if _, _, _, err := svc.Delete("solo"); err == nil {
		t.Fatalf("Delete(solo) expected error, got nil")
	}
}

func TestResolveSubagentByPurposeAndName(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-resolve-subagent.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	svc := NewService(pebblestore.NewAgentStore(store), events)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	// Purpose lookup should resolve through active subagent assignments.
	byPurpose, err := svc.ResolveSubagent("explorer")
	if err != nil {
		t.Fatalf("ResolveSubagent(explorer purpose) error = %v", err)
	}
	if byPurpose.Mode != ModeSubagent {
		t.Fatalf("resolved mode = %q, want %q", byPurpose.Mode, ModeSubagent)
	}

	// Direct profile lookup should also succeed.
	byName, err := svc.ResolveSubagent("parallel")
	if err != nil {
		t.Fatalf("ResolveSubagent(parallel profile) error = %v", err)
	}
	if byName.Name != "parallel" {
		t.Fatalf("resolved name = %q, want parallel", byName.Name)
	}
	if byName.ExecutionSetting != pebblestore.AgentExecutionSettingReadWrite {
		t.Fatalf("parallel execution setting = %q, want %q", byName.ExecutionSetting, pebblestore.AgentExecutionSettingReadWrite)
	}

	state, err := svc.ListState(100)
	if err != nil {
		t.Fatalf("ListState() error = %v", err)
	}
	var swarmPrompt string
	for _, profile := range state.Profiles {
		if profile.Name == "swarm" {
			swarmPrompt = profile.Prompt
			break
		}
	}
	if strings.TrimSpace(swarmPrompt) == "" {
		t.Fatalf("expected default swarm prompt to be present")
	}
	if !strings.Contains(swarmPrompt, "Match execution depth to request scope") {
		t.Fatalf("expected swarm prompt to include scope-aware guidance, got: %q", swarmPrompt)
	}
}

func TestEnsureDefaultsReconcilesBuiltInParallelToReadWrite(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-parallel-reconcile.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	svc := NewService(pebblestore.NewAgentStore(store), events)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	parallel, ok, err := svc.GetProfile("parallel")
	if err != nil {
		t.Fatalf("GetProfile(parallel) error = %v", err)
	}
	if !ok {
		t.Fatalf("expected parallel to exist")
	}
	parallel.ExecutionSetting = pebblestore.AgentExecutionSettingRead
	parallel.UpdatedAt = 1
	if err := pebblestore.NewAgentStore(store).PutProfile(parallel); err != nil {
		t.Fatalf("PutProfile(parallel) error = %v", err)
	}

	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() second error = %v", err)
	}
	parallel, ok, err = svc.GetProfile("parallel")
	if err != nil {
		t.Fatalf("GetProfile(parallel) after reconcile error = %v", err)
	}
	if !ok {
		t.Fatalf("expected parallel to still exist")
	}
	if parallel.ExecutionSetting != pebblestore.AgentExecutionSettingReadWrite {
		t.Fatalf("parallel execution setting = %q, want %q", parallel.ExecutionSetting, pebblestore.AgentExecutionSettingReadWrite)
	}
}

func TestEnsureDefaultsDoesNotRecreateDeletedUtilityAgents(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-no-reseed.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	svc := NewService(pebblestore.NewAgentStore(store), events)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}
	if _, _, _, err := svc.Delete("explorer"); err != nil {
		t.Fatalf("Delete(explorer) error = %v", err)
	}
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() second error = %v", err)
	}
	if _, ok, err := svc.GetProfile("explorer"); err != nil {
		t.Fatalf("GetProfile(explorer) error = %v", err)
	} else if ok {
		t.Fatalf("expected explorer to remain deleted after EnsureDefaults")
	}
}

func TestRestoreDefaultsRecreatesUtilityAgents(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-restore-defaults.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	svc := NewService(pebblestore.NewAgentStore(store), events)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}
	if _, _, _, err := svc.Delete("explorer"); err != nil {
		t.Fatalf("Delete(explorer) error = %v", err)
	}
	state, _, _, err := svc.RestoreDefaults()
	if err != nil {
		t.Fatalf("RestoreDefaults() error = %v", err)
	}
	if _, ok, err := svc.GetProfile("explorer"); err != nil {
		t.Fatalf("GetProfile(explorer) error = %v", err)
	} else if !ok {
		t.Fatalf("expected explorer to be restored")
	}
	if got := state.ActiveSubagent["explorer"]; got != "explorer" {
		t.Fatalf("active subagent explorer = %q, want explorer", got)
	}
}

func TestResetDefaultsDeletesCustomAgentsAndTools(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-reset-defaults.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	svc := NewService(pebblestore.NewAgentStore(store), events)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}
	if _, _, _, err := svc.Upsert(UpsertInput{Name: "review", Mode: ModePrimary, Prompt: "review", Enabled: boolPtr(true)}); err != nil {
		t.Fatalf("Upsert(review) error = %v", err)
	}
	tool, err := svc.PutCustomTool(pebblestore.AgentCustomToolDefinition{Name: "hello", Kind: "fixed_bash", Command: "echo hello"})
	if err != nil {
		t.Fatalf("PutCustomTool() error = %v", err)
	}
	if _, _, _, err := svc.AssignCustomTool("review", tool.Name); err != nil {
		t.Fatalf("AssignCustomTool() error = %v", err)
	}
	if _, _, _, err := svc.SetActiveSubagent("helper", "memory"); err != nil {
		t.Fatalf("SetActiveSubagent(helper) error = %v", err)
	}
	state, _, _, err := svc.ResetDefaults()
	if err != nil {
		t.Fatalf("ResetDefaults() error = %v", err)
	}
	if _, ok, err := svc.GetProfile("review"); err != nil {
		t.Fatalf("GetProfile(review) error = %v", err)
	} else if ok {
		t.Fatalf("expected review to be deleted by reset")
	}
	tools, err := svc.ListCustomTools(100)
	if err != nil {
		t.Fatalf("ListCustomTools() error = %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("custom tools = %d, want 0", len(tools))
	}
	if state.ActivePrimary != "swarm" {
		t.Fatalf("active primary = %q, want swarm", state.ActivePrimary)
	}
	if got := state.ActiveSubagent["helper"]; got != "" {
		t.Fatalf("helper active subagent = %q, want empty", got)
	}
	if got := state.ActiveSubagent["explorer"]; got != "explorer" {
		t.Fatalf("active subagent explorer = %q, want explorer", got)
	}
	defaultNames := map[string]bool{"swarm": true, "explorer": true, "memory": true, "commit": true, "parallel": true, "clone": true}
	for _, profile := range state.Profiles {
		if !defaultNames[profile.Name] {
			t.Fatalf("unexpected profile after reset: %s", profile.Name)
		}
		delete(defaultNames, profile.Name)
	}
	if len(defaultNames) != 0 {
		t.Fatalf("missing default profiles after reset: %v", defaultNames)
	}
}

func boolPtr(v bool) *bool {
	return &v
}
