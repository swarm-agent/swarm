package agent

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	codexruntime "swarm/packages/swarmd/internal/provider/codex"
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

func TestServicePublishesAgentMutationEvents(t *testing.T) {
	svc, _ := newTestService(t)
	published := make([]pebblestore.EventEnvelope, 0, 1)
	svc.SetEventPublisher(func(event pebblestore.EventEnvelope) {
		published = append(published, event)
	})
	enabled := true
	profile, version, event, err := svc.Upsert(UpsertInput{
		Name:         "publisher-probe",
		Mode:         ModeSubagent,
		Description:  "publisher probe",
		Prompt:       "Probe event publishing.",
		ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"},
		Enabled:      &enabled,
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if event == nil {
		t.Fatalf("Upsert() returned nil event")
	}
	if len(published) != 1 {
		t.Fatalf("published event count = %d, want 1", len(published))
	}
	if published[0].GlobalSeq != event.GlobalSeq {
		t.Fatalf("published seq = %d, returned seq = %d", published[0].GlobalSeq, event.GlobalSeq)
	}
	if published[0].Stream != "system:agent" {
		t.Fatalf("published stream = %q, want system:agent", published[0].Stream)
	}
	if published[0].EventType != "agent.profile.created" {
		t.Fatalf("published event type = %q, want agent.profile.created", published[0].EventType)
	}
	var payload struct {
		Profile pebblestore.AgentProfile `json:"profile"`
		State   State                    `json:"state"`
		Version int64                    `json:"version"`
	}
	if err := json.Unmarshal(published[0].Payload, &payload); err != nil {
		t.Fatalf("decode event payload: %v", err)
	}
	if payload.Profile.Name != profile.Name {
		t.Fatalf("payload profile name = %q, want %q", payload.Profile.Name, profile.Name)
	}
	if payload.Version != version || payload.State.Version != version {
		t.Fatalf("payload version=%d state.version=%d, want %d", payload.Version, payload.State.Version, version)
	}
	found := false
	for _, candidate := range payload.State.Profiles {
		if candidate.Name == profile.Name {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("payload state missing profile %q", profile.Name)
	}
}

func TestServicePublishesCustomToolMutationEvents(t *testing.T) {
	svc, _ := newTestService(t)
	published := make([]pebblestore.EventEnvelope, 0, 2)
	svc.SetEventPublisher(func(event pebblestore.EventEnvelope) {
		published = append(published, event)
	})

	tool, err := svc.PutCustomTool(pebblestore.AgentCustomToolDefinition{
		Name:        "publish_probe",
		Kind:        "fixed_bash",
		Description: "Publish probe",
		Command:     "git status --short",
	})
	if err != nil {
		t.Fatalf("PutCustomTool() error = %v", err)
	}
	if len(published) != 1 {
		t.Fatalf("published event count after put = %d, want 1", len(published))
	}
	if published[0].Stream != "system:agent" || published[0].EventType != "agent.custom_tool.created" {
		t.Fatalf("published put event = %s %s, want system:agent agent.custom_tool.created", published[0].Stream, published[0].EventType)
	}
	var createPayload struct {
		CustomTool pebblestore.AgentCustomToolDefinition `json:"custom_tool"`
		State      State                                 `json:"state"`
	}
	if err := json.Unmarshal(published[0].Payload, &createPayload); err != nil {
		t.Fatalf("decode create payload: %v", err)
	}
	if createPayload.CustomTool.Name != tool.Name || len(createPayload.State.CustomTools) != 1 {
		t.Fatalf("create payload custom tool=%q state tools=%d, want %q and 1", createPayload.CustomTool.Name, len(createPayload.State.CustomTools), tool.Name)
	}

	deleted, err := svc.DeleteCustomTool(tool.Name)
	if err != nil {
		t.Fatalf("DeleteCustomTool() error = %v", err)
	}
	if !deleted {
		t.Fatalf("DeleteCustomTool() deleted = false, want true")
	}
	if len(published) != 2 {
		t.Fatalf("published event count after delete = %d, want 2", len(published))
	}
	if published[1].Stream != "system:agent" || published[1].EventType != "agent.custom_tool.deleted" {
		t.Fatalf("published delete event = %s %s, want system:agent agent.custom_tool.deleted", published[1].Stream, published[1].EventType)
	}
}

func TestEnsureDefaultsExposesCompiledSwarmAndPersistsOnlyModelContext(t *testing.T) {
	svc, agents := newTestService(t)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}
	swarm, ok, err := agents.GetProfile("swarm")
	if err != nil || !ok {
		t.Fatalf("GetProfile(swarm) ok=%v err=%v", ok, err)
	}
	if swarm.ToolContract != nil || swarm.Prompt != "" || swarm.Protected {
		t.Fatalf("persisted swarm row retained mutable system fields: %+v", swarm)
	}
	resolved, ok, err := svc.GetProfile("swarm")
	if err != nil || !ok {
		t.Fatalf("service GetProfile(swarm) ok=%v err=%v", ok, err)
	}
	if resolved.Mode != ModePrimary || resolved.Prompt != SwarmAgentPrompt() || !resolved.Protected || !reflect.DeepEqual(resolved.ToolContract, SwarmAgentToolContract()) {
		t.Fatalf("compiled swarm = %+v", resolved)
	}
	for _, name := range []string{"clone", "system-clone", CoderAgentID, "memory", "finder"} {
		if _, ok, err := agents.GetProfile(name); err != nil || ok {
			t.Fatalf("persisted compiled/retired profile %q ok=%v err=%v, want absent", name, ok, err)
		}
	}
}

func TestCompiledSwarmAllowsConfigurationOnlyUpdates(t *testing.T) {
	svc, agents := newTestService(t)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}
	profile, _, _, err := svc.Upsert(UpsertInput{
		Name: SwarmAgentID, Mode: ModeSubagent, Description: "mutable", Prompt: "mutable",
		ModelMode: "split", PlanProvider: "codex", PlanModel: "plan-model", PlanThinking: "high", PlanProviderSet: true, PlanModelSet: true, PlanThinkingSet: true,
		AutoProvider: "codex", AutoModel: "auto-model", AutoThinking: "medium", AutoProviderSet: true, AutoModelSet: true, AutoThinkingSet: true,
		DefaultSessionMode: pebblestore.AgentDefaultSessionModeAuto, RuntimeMode: pebblestore.AgentRuntimeModeRead,
	})
	if err != nil {
		t.Fatalf("Upsert(swarm) error = %v", err)
	}
	if profile.Mode != ModePrimary || profile.Description != "Compiled primary orchestrator" || profile.Prompt != SwarmAgentPrompt() || profile.RuntimeMode != pebblestore.AgentRuntimeModePlanAuto || !profile.Enabled || !profile.Protected {
		t.Fatalf("compiled Swarm identity/runtime changed: %+v", profile)
	}
	if profile.ModelMode != "split" || profile.PlanModel != "plan-model" || profile.AutoModel != "auto-model" || profile.DefaultSessionMode != pebblestore.AgentDefaultSessionModeAuto {
		t.Fatalf("Swarm configuration was not applied: %+v", profile)
	}
	stored, ok, err := agents.GetProfile(SwarmAgentID)
	if err != nil || !ok {
		t.Fatalf("stored Swarm context ok=%v err=%v", ok, err)
	}
	if stored.Prompt != "" || stored.ToolContract != nil || stored.Mode != ModePrimary || stored.DefaultSessionMode != pebblestore.AgentDefaultSessionModeAuto {
		t.Fatalf("persisted Swarm row escaped configuration-only shape: %+v", stored)
	}
}

func TestRestoreDefaultsDoesNotPersistOrAssignClone(t *testing.T) {
	svc, agents := newTestService(t)
	state, _, _, err := svc.RestoreDefaults()
	if err != nil {
		t.Fatalf("RestoreDefaults() error = %v", err)
	}
	if _, exists := state.ActiveSubagent["clone"]; exists {
		t.Fatalf("RestoreDefaults() returned mutable Clone assignment: %+v", state.ActiveSubagent)
	}
	for _, name := range []string{"clone", "system-clone", CoderAgentID} {
		if _, ok, err := agents.GetProfile(name); err != nil || ok {
			t.Fatalf("GetProfile(%q) ok=%v err=%v, want absent", name, ok, err)
		}
	}
	clone, err := svc.ResolveSystemAgent(CoderAgentID, pebblestore.AgentProfile{Provider: "codex", Model: "parent-model"})
	if err != nil {
		t.Fatalf("ResolveSystemAgent(Clone) error = %v", err)
	}
	if clone.Name != CoderAgentID || clone.Provider != "codex" || clone.Model != "parent-model" || !clone.Enabled {
		t.Fatalf("compiled Clone = %+v", clone)
	}
}

func TestEnsureDefaultsCleansLegacyCloneRowsAndAssignments(t *testing.T) {
	svc, agents := newTestService(t)
	if err := agents.PutProfile(pebblestore.AgentProfile{
		Name: "clone", Mode: ModeSubagent, Prompt: oldDefaultClonePrompt(), RuntimeMode: pebblestore.AgentRuntimeModeReadWrite,
		ToolContract: defaultReadWriteSubagentToolContract(), Enabled: true,
	}); err != nil {
		t.Fatalf("put legacy clone: %v", err)
	}
	if err := agents.SetActiveSubagent("clone", "clone"); err != nil {
		t.Fatalf("set clone assignment: %v", err)
	}
	if err := agents.SetActiveSubagent("helper", "clone"); err != nil {
		t.Fatalf("set helper assignment: %v", err)
	}
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() cleanup error = %v", err)
	}
	if _, ok, err := agents.GetProfile("clone"); err != nil || ok {
		t.Fatalf("legacy Clone row ok=%v err=%v, want removed", ok, err)
	}
	assignments, err := agents.GetActiveSubagents(20)
	if err != nil {
		t.Fatalf("GetActiveSubagents() error = %v", err)
	}
	if _, ok := assignments["clone"]; ok {
		t.Fatalf("legacy Clone purpose survived: %+v", assignments)
	}
	if _, ok := assignments["helper"]; ok {
		t.Fatalf("legacy Clone target survived: %+v", assignments)
	}
}

func TestCloneAliasesRejectUserMutationsAndRemapping(t *testing.T) {
	svc, _ := newTestService(t)
	for _, name := range []string{"clone", "system-clone", CoderAgentID} {
		if _, _, _, err := svc.Upsert(UpsertInput{Name: name, ToolContract: defaultReadWriteSubagentToolContract()}); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("Upsert(%q) error = %v, want reserved", name, err)
		}
		if _, _, _, err := svc.Delete(name); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("Delete(%q) error = %v, want reserved", name, err)
		}
		if _, _, _, err := svc.ActivatePrimary(name); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("ActivatePrimary(%q) error = %v, want reserved", name, err)
		}
	}
	if _, _, _, err := svc.SetActiveSubagent("clone", "helper"); err == nil || !strings.Contains(err.Error(), "cannot be remapped") {
		t.Fatalf("SetActiveSubagent clone purpose error = %v", err)
	}
	if _, _, _, err := svc.SetActiveSubagent("helper", CoderAgentID); err == nil || !strings.Contains(err.Error(), "cannot be remapped") {
		t.Fatalf("SetActiveSubagent Clone target error = %v", err)
	}
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

func TestActivatePrimaryIsIdempotentForCurrentPrimary(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	published := make([]pebblestore.EventEnvelope, 0, 1)
	svc.SetEventPublisher(func(event pebblestore.EventEnvelope) {
		published = append(published, event)
	})

	active, version, event, err := svc.ActivatePrimary("swarm")
	if err != nil {
		t.Fatalf("ActivatePrimary(swarm) error = %v", err)
	}
	if active != "swarm" {
		t.Fatalf("active primary = %q, want swarm", active)
	}
	if version == 0 {
		t.Fatalf("version = 0, want current store version")
	}
	if event != nil {
		t.Fatalf("event = %+v, want nil for idempotent activation", event)
	}
	if len(published) != 0 {
		t.Fatalf("published event count = %d, want 0", len(published))
	}

	state, err := svc.ListState(200)
	if err != nil {
		t.Fatalf("ListState() error = %v", err)
	}
	if state.Version != version {
		t.Fatalf("state version = %d, want %d", state.Version, version)
	}
}

func TestSwarmIsImmutableAndUsersCanActivateReplacementPrimary(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}
	if _, _, _, err := svc.Delete("swarm"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("Delete(swarm) error = %v, want immutable-system rejection", err)
	}
	if _, _, _, err := svc.Upsert(UpsertInput{Name: "swarm", Mode: ModePrimary, Prompt: "mutable"}); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("Upsert(swarm) error = %v, want immutable-system rejection", err)
	}

	enabled := true
	if _, _, _, err := svc.Upsert(UpsertInput{
		Name: "replacement", Mode: ModePrimary, Description: "replacement primary", Prompt: "Handle primary tasks.",
		RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true),
		ToolContract: &pebblestore.AgentToolContract{Preset: "read_write"}, Enabled: &enabled,
	}); err != nil {
		t.Fatalf("create replacement primary: %v", err)
	}
	active, _, _, err := svc.ActivatePrimary("replacement")
	if err != nil || active != "replacement" {
		t.Fatalf("ActivatePrimary(replacement) active=%q err=%v", active, err)
	}
	if _, ok, err := svc.GetProfile("swarm"); err != nil || !ok {
		t.Fatalf("compiled swarm disappeared after override ok=%v err=%v", ok, err)
	}
}

func TestDeletePrimaryRequiresAnotherPrimaryForEveryPrimary(t *testing.T) {
	svc, _ := newTestService(t)
	enabled := true
	if _, _, _, err := svc.Upsert(UpsertInput{
		Name:         "solo",
		Mode:         ModePrimary,
		Description:  "only primary",
		Prompt:       "Handle primary tasks.",
		ToolContract: &pebblestore.AgentToolContract{Preset: "read_write"},
		Enabled:      &enabled,
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

func TestCustomizedMemoryCanBeDeleted(t *testing.T) {
	svc, _ := newTestService(t)
	enabled := true
	if _, _, _, err := svc.Upsert(UpsertInput{
		Name: "memory", Mode: ModeSubagent, Description: "User-owned memory helper", Prompt: "Custom user prompt.",
		RuntimeMode: pebblestore.AgentRuntimeModeRead, ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"}, Enabled: &enabled,
	}); err != nil {
		t.Fatalf("create customized memory: %v", err)
	}

	if _, _, _, err := svc.Delete("memory"); err != nil {
		t.Fatalf("Delete(memory) error = %v", err)
	}
}

func TestNormalizeModelServiceTierKeepsPriorityDistinctFromFast(t *testing.T) {
	if got := pebblestore.NormalizeModelServiceTier("priority"); got != "priority" {
		t.Fatalf("NormalizeModelServiceTier(priority) = %q, want priority", got)
	}
	if got := pebblestore.NormalizeModelServiceTier("fast"); got != "fast" {
		t.Fatalf("NormalizeModelServiceTier(fast) = %q, want fast", got)
	}
	if got := codexruntime.NormalizeServiceTier("priority"); got != "priority" {
		t.Fatalf("codex NormalizeServiceTier(priority) = %q, want priority", got)
	}
	if got := codexruntime.NormalizeServiceTier("fast"); got != "fast" {
		t.Fatalf("codex NormalizeServiceTier(fast) = %q, want fast", got)
	}
}

func TestUpsertPlanCapableAgentPreservesSplitModelFields(t *testing.T) {
	svc, _ := newTestService(t)
	enabled := true
	profile, _, _, err := svc.Upsert(UpsertInput{
		Name:                "planner-model-probe",
		Mode:                ModeSubagent,
		Description:         "planner model probe",
		Provider:            "codex",
		Model:               "base-model",
		Thinking:            "low",
		ModelMode:           "split",
		PlanProvider:        "codex",
		PlanModel:           "gpt-5.4",
		PlanThinking:        "high",
		PlanServiceTier:     "fast",
		AutoProvider:        "fireworks",
		AutoModel:           "glm-5p1",
		AutoThinking:        "medium",
		AutoServiceTier:     "priority",
		Prompt:              "Probe model settings.",
		RuntimeMode:         pebblestore.AgentRuntimeModePlanAuto,
		ExitPlanModeEnabled: pebblestore.BoolPtr(true),
		ToolContract:        &pebblestore.AgentToolContract{Preset: "read_only"},
		Enabled:             &enabled,
		ProviderSet:         true,
		ModelSet:            true,
		ThinkingSet:         true,
		PlanProviderSet:     true,
		PlanModelSet:        true,
		PlanThinkingSet:     true,
		PlanServiceTierSet:  true,
		AutoProviderSet:     true,
		AutoModelSet:        true,
		AutoThinkingSet:     true,
		AutoServiceTierSet:  true,
	})
	if err != nil {
		t.Fatalf("create plan-capable split profile: %v", err)
	}
	if profile.ModelMode != "split" {
		t.Fatalf("model_mode = %q, want split", profile.ModelMode)
	}
	if profile.Provider != "" || profile.Model != "" || profile.Thinking != "" {
		t.Fatalf("single fields were not cleared for split profile: %+v", profile)
	}
	if profile.PlanProvider != "codex" || profile.PlanModel != "gpt-5.4" || profile.PlanThinking != "high" || profile.PlanServiceTier != "fast" {
		t.Fatalf("plan split fields = %+v", profile)
	}
	if profile.AutoProvider != "fireworks" || profile.AutoModel != "glm-5p1" || profile.AutoThinking != "medium" || profile.AutoServiceTier != "priority" {
		t.Fatalf("auto split fields = %+v", profile)
	}
}

func TestUpsertClearsExplicitSplitModelFields(t *testing.T) {
	svc, _ := newTestService(t)
	enabled := true
	created, _, _, err := svc.Upsert(UpsertInput{
		Name:               "model-probe",
		Mode:               ModeSubagent,
		Description:        "model probe",
		ModelMode:          "split",
		PlanProvider:       "codex",
		PlanModel:          "gpt-5.4",
		PlanThinking:       "high",
		PlanServiceTier:    "fast",
		AutoProvider:       "fireworks",
		AutoModel:          "glm-5p1",
		AutoThinking:       "medium",
		AutoServiceTier:    "priority",
		Prompt:             "Probe model settings.",
		RuntimeMode:        pebblestore.AgentRuntimeModeRead,
		ToolContract:       &pebblestore.AgentToolContract{Preset: "read_only"},
		Enabled:            &enabled,
		PlanProviderSet:    true,
		PlanModelSet:       true,
		PlanThinkingSet:    true,
		PlanServiceTierSet: true,
		AutoProviderSet:    true,
		AutoModelSet:       true,
		AutoThinkingSet:    true,
		AutoServiceTierSet: true,
	})
	if err != nil {
		t.Fatalf("create split profile: %v", err)
	}
	if created.ModelMode != "" || created.PlanProvider != "" || created.PlanModel != "" || created.PlanThinking != "" || created.PlanServiceTier != "" || created.AutoProvider != "" || created.AutoModel != "" || created.AutoThinking != "" || created.AutoServiceTier != "" {
		t.Fatalf("non-plan-capable split fields were not cleared on create: %+v", created)
	}

	updated, _, _, err := svc.Upsert(UpsertInput{
		Name:               "model-probe",
		Mode:               ModeSubagent,
		Provider:           "codex",
		Model:              "gpt-5.4",
		Thinking:           "low",
		ModelMode:          "single",
		PlanProvider:       "",
		PlanModel:          "",
		PlanThinking:       "",
		PlanServiceTier:    "",
		AutoProvider:       "",
		AutoModel:          "",
		AutoThinking:       "",
		AutoServiceTier:    "",
		Prompt:             "Probe model settings.",
		RuntimeMode:        pebblestore.AgentRuntimeModeRead,
		ToolContract:       &pebblestore.AgentToolContract{Preset: "read_only"},
		Enabled:            &enabled,
		ProviderSet:        true,
		ModelSet:           true,
		ThinkingSet:        true,
		PlanProviderSet:    true,
		PlanModelSet:       true,
		PlanThinkingSet:    true,
		PlanServiceTierSet: true,
		AutoProviderSet:    true,
		AutoModelSet:       true,
		AutoThinkingSet:    true,
		AutoServiceTierSet: true,
	})
	if err != nil {
		t.Fatalf("update to single profile: %v", err)
	}
	if updated.ModelMode != "" || updated.PlanProvider != "" || updated.PlanModel != "" || updated.PlanThinking != "" || updated.PlanServiceTier != "" || updated.AutoProvider != "" || updated.AutoModel != "" || updated.AutoThinking != "" || updated.AutoServiceTier != "" {
		t.Fatalf("split fields were not cleared: %+v", updated)
	}
	if updated.Provider != "codex" || updated.Model != "gpt-5.4" || updated.Thinking != "low" {
		t.Fatalf("single model fields = provider=%q model=%q thinking=%q, want codex/gpt-5.4/low", updated.Provider, updated.Model, updated.Thinking)
	}
}
