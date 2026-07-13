package agent

import (
	"reflect"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestBuiltinSystemAgentRegistryIsCompleteAndUnique(t *testing.T) {
	registry, err := BuiltinSystemAgentRegistry()
	if err != nil {
		t.Fatalf("builtin registry: %v", err)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("validate builtin registry: %v", err)
	}
	want := []string{AISidechatAgentID, PlanSidechatAgentID}
	if got := registry.IDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("registry IDs = %v, want %v", got, want)
	}
	for kind, id := range map[string]string{SystemSidechatKindPlan: PlanSidechatAgentID, SystemSidechatKindAI: AISidechatAgentID} {
		definition, ok := registry.DefinitionBySidechatKind(kind)
		if !ok || definition.ID != id {
			t.Fatalf("kind %q resolved to %+v, ok=%v", kind, definition, ok)
		}
	}
}

func TestSystemAgentRegistryRejectsMissingDuplicateAndInvalidDefinitions(t *testing.T) {
	valid := SystemAgentDefinition{
		ID: "system-future", DisplayName: "Future", SidechatKind: "future",
		Materialize: func(parent pebblestore.AgentProfile) pebblestore.AgentProfile {
			return pebblestore.NormalizeAgentProfile(pebblestore.AgentProfile{Name: "system-future", Mode: ModeSubagent, RuntimeMode: pebblestore.AgentRuntimeModeRead, ExitPlanModeEnabled: pebblestore.BoolPtr(false), Enabled: true})
		},
		Reconcile: func(snapshot pebblestore.AgentProfile) pebblestore.AgentProfile { return snapshot },
	}
	if _, err := NewSystemAgentRegistry(nil); err == nil {
		t.Fatal("empty registry was accepted")
	}
	missing := valid
	missing.Materialize = nil
	if _, err := NewSystemAgentRegistry([]SystemAgentDefinition{missing}); err == nil || !strings.Contains(err.Error(), "materialize") {
		t.Fatalf("missing materializer error = %v", err)
	}
	duplicateID := valid
	duplicateID.SidechatKind = "other"
	if _, err := NewSystemAgentRegistry([]SystemAgentDefinition{valid, duplicateID}); err == nil || !strings.Contains(err.Error(), "duplicate system agent id") {
		t.Fatalf("duplicate id error = %v", err)
	}
	duplicateKind := valid
	duplicateKind.ID = "system-other"
	duplicateKind.Materialize = func(parent pebblestore.AgentProfile) pebblestore.AgentProfile {
		return pebblestore.NormalizeAgentProfile(pebblestore.AgentProfile{Name: "system-other", Mode: ModeSubagent, RuntimeMode: pebblestore.AgentRuntimeModeRead, ExitPlanModeEnabled: pebblestore.BoolPtr(false), Enabled: true})
	}
	if _, err := NewSystemAgentRegistry([]SystemAgentDefinition{valid, duplicateKind}); err == nil || !strings.Contains(err.Error(), "duplicate system sidechat kind") {
		t.Fatalf("duplicate kind error = %v", err)
	}
	invalid := valid
	invalid.Materialize = func(parent pebblestore.AgentProfile) pebblestore.AgentProfile {
		return pebblestore.AgentProfile{Name: "wrong", Mode: ModePrimary, Enabled: false}
	}
	if _, err := NewSystemAgentRegistry([]SystemAgentDefinition{invalid}); err == nil || !strings.Contains(err.Error(), "invalid identity") {
		t.Fatalf("invalid materialization error = %v", err)
	}
}

func TestSystemAgentRegistrySupportsFutureCompiledDefinition(t *testing.T) {
	definition := SystemAgentDefinition{
		ID: "system-future", DisplayName: "Future", SidechatKind: "future",
		Materialize: func(parent pebblestore.AgentProfile) pebblestore.AgentProfile {
			return pebblestore.NormalizeAgentProfile(pebblestore.AgentProfile{Name: "system-future", Mode: ModeSubagent, Provider: parent.Provider, Model: parent.Model, Prompt: "future", RuntimeMode: pebblestore.AgentRuntimeModeRead, ExitPlanModeEnabled: pebblestore.BoolPtr(false), Enabled: true})
		},
		Reconcile: func(snapshot pebblestore.AgentProfile) pebblestore.AgentProfile {
			return pebblestore.NormalizeAgentProfile(pebblestore.AgentProfile{Name: "system-future", Mode: ModeSubagent, Provider: snapshot.Provider, Model: snapshot.Model, Prompt: "future", RuntimeMode: pebblestore.AgentRuntimeModeRead, ExitPlanModeEnabled: pebblestore.BoolPtr(false), Enabled: true})
		},
	}
	registry, err := NewSystemAgentRegistry(append(append([]SystemAgentDefinition{}, builtinSystemAgentDefinitions...), definition))
	if err != nil {
		t.Fatalf("register future definition: %v", err)
	}
	profile, err := registry.MaterializeSidechat("future", pebblestore.AgentProfile{Provider: "codex", Model: "future-model"})
	if err != nil {
		t.Fatalf("materialize future definition: %v", err)
	}
	if profile.Name != "system-future" || profile.Provider != "codex" || profile.Model != "future-model" {
		t.Fatalf("unexpected future profile: %+v", profile)
	}
}

func TestSystemAgentSnapshotReconciliationPreservesDynamicContextAndModels(t *testing.T) {
	registry, err := BuiltinSystemAgentRegistry()
	if err != nil {
		t.Fatal(err)
	}
	planPrompt := PlanSidechatAgentPromptWithContext(`{"plan_id":"plan-1","proposal_revision":7}`)
	plan, err := registry.ReconcileSnapshot(PlanSidechatAgentID, pebblestore.AgentProfile{
		Name: PlanSidechatAgentID, Provider: "codex", Model: "plan-model", Thinking: "high", PlanServiceTier: "priority",
		Prompt: planPrompt, RuntimeMode: pebblestore.AgentRuntimeModeReadWrite, Enabled: false,
		ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"bash": {Enabled: pebblestore.BoolPtr(true)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Prompt != planPrompt || plan.Provider != "codex" || plan.Model != "plan-model" || plan.PlanServiceTier != "priority" {
		t.Fatalf("dynamic Plan data was not preserved: %+v", plan)
	}
	if !plan.Enabled || plan.RuntimeMode != pebblestore.AgentRuntimeModeRead || plan.ExitPlanModeEnabled == nil || *plan.ExitPlanModeEnabled {
		t.Fatalf("Plan invariants were not restored: %+v", plan)
	}
	if cfg := plan.ToolContract.Tools["bash"]; cfg.Enabled == nil || *cfg.Enabled {
		t.Fatal("Plan snapshot retained mutable bash permission")
	}

	ai, err := registry.ReconcileSnapshot(AISidechatAgentID, pebblestore.AgentProfile{
		Name: AISidechatAgentID, Provider: "openai", Model: "auto-model", Thinking: "medium", AutoServiceTier: "fast",
		ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"write": {Enabled: pebblestore.BoolPtr(true)}, "plan_manage": {Enabled: pebblestore.BoolPtr(true)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ai.Provider != "openai" || ai.Model != "auto-model" || ai.AutoServiceTier != "fast" || ai.RuntimeMode != pebblestore.AgentRuntimeModeReadWrite {
		t.Fatalf("AI inherited settings were not preserved: %+v", ai)
	}
	if cfg := ai.ToolContract.Tools["write"]; cfg.Enabled == nil || !*cfg.Enabled {
		t.Fatal("AI inherited auto write capability was removed")
	}
	for _, denied := range []string{"plan_manage", "exit_plan_mode", "manage_agent", "ask_user"} {
		if cfg := ai.ToolContract.Tools[denied]; cfg.Enabled == nil || *cfg.Enabled {
			t.Fatalf("AI mandatory denial %q was not restored", denied)
		}
	}
	if _, err := registry.ReconcileSnapshot(PlanSidechatAgentID, pebblestore.AgentProfile{Name: AISidechatAgentID}); err == nil || !strings.Contains(err.Error(), "metadata mismatch") {
		t.Fatalf("metadata mismatch error = %v", err)
	}
}

func TestEnsureSystemAgentRegistryDoesNotPersistOrExposeMutableProfiles(t *testing.T) {
	svc, agents := newTestService(t)
	if err := svc.EnsureSystemAgentRegistry(); err != nil {
		t.Fatalf("ensure registry: %v", err)
	}
	state, err := svc.ListState(100)
	if err != nil {
		t.Fatalf("list agent state: %v", err)
	}
	for _, id := range []string{PlanSidechatAgentID, AISidechatAgentID} {
		if _, ok, err := agents.GetProfile(id); err != nil || ok {
			t.Fatalf("system profile %q persisted ok=%v err=%v", id, ok, err)
		}
		for _, profile := range state.Profiles {
			if normalizeName(profile.Name) == id {
				t.Fatalf("system profile %q was exposed in ordinary agent state", id)
			}
		}
		if _, err := svc.ResolveSystemAgent(id, pebblestore.AgentProfile{}); err != nil {
			t.Fatalf("resolve %q: %v", id, err)
		}
		if _, _, _, err := svc.Upsert(UpsertInput{Name: id, Mode: ModeSubagent, Prompt: "replace"}); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("system profile %q mutation error = %v, want reserved rejection", id, err)
		}
		if _, _, _, err := svc.Delete(id); err == nil || (!strings.Contains(err.Error(), "reserved") && !strings.Contains(err.Error(), "transient")) {
			t.Fatalf("system profile %q delete error = %v, want immutable-system rejection", id, err)
		}
	}
}
