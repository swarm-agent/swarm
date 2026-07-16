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
	want := []string{SwarmAgentID, AISidechatAgentID, CloneAgentID, CompactAgentID, ExplorerAgentID, PlanSidechatAgentID}
	if got := registry.IDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("registry IDs = %v, want %v", got, want)
	}
	for kind, id := range map[string]string{SystemSidechatKindPlan: PlanSidechatAgentID, SystemSidechatKindAI: AISidechatAgentID, SystemSidechatKindCompact: CompactAgentID, SystemSidechatKindExplorer: ExplorerAgentID, SystemSidechatKindClone: CloneAgentID} {
		definition, ok := registry.DefinitionBySidechatKind(kind)
		if !ok || definition.ID != id {
			t.Fatalf("kind %q resolved to %+v, ok=%v", kind, definition, ok)
		}
	}
	for _, id := range []string{PlanSidechatAgentID, AISidechatAgentID, CompactAgentID} {
		definition, _ := registry.DefinitionByID(id)
		if !definition.RequiresSidechatMetadata || !IsReservedSidechatAgentName(id) {
			t.Fatalf("sidechat-only system agent %q is not protected: %+v", id, definition)
		}
	}
	for _, id := range []string{SwarmAgentID, ExplorerAgentID, CloneAgentID} {
		definition, _ := registry.DefinitionByID(id)
		if definition.RequiresSidechatMetadata || IsReservedSidechatAgentName(id) {
			t.Fatalf("ordinary/task system agent %q was classified as sidechat-only: %+v", id, definition)
		}
	}
}

func TestBuiltinSystemAgentRegistryUserVisibleIDs(t *testing.T) {
	registry, err := BuiltinSystemAgentRegistry()
	if err != nil {
		t.Fatalf("BuiltinSystemAgentRegistry() error = %v", err)
	}
	want := []string{SwarmAgentID, CloneAgentID, ExplorerAgentID}
	if got := registry.UserVisibleIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("UserVisibleIDs() = %v, want %v", got, want)
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
	missingSidechatKind := valid
	missingSidechatKind.SidechatKind = ""
	missingSidechatKind.RequiresSidechatMetadata = true
	if _, err := NewSystemAgentRegistry([]SystemAgentDefinition{missingSidechatKind}); err == nil || !strings.Contains(err.Error(), "requires a sidechat kind") {
		t.Fatalf("missing mandatory sidechat kind error = %v", err)
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
	compact, err := registry.Materialize(CompactAgentID, pebblestore.AgentProfile{Provider: "codex", Model: "utility-model", Thinking: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	if compact.Name != CompactAgentID || !compact.Enabled || compact.ExitPlanModeEnabled == nil || *compact.ExitPlanModeEnabled {
		t.Fatalf("Compact identity invariant mismatch: %+v", compact)
	}
	if compact.ToolContract == nil || compact.ToolContract.Preset != "custom" || len(compact.ToolContract.Tools) != 0 {
		t.Fatalf("Compact must have an immutable empty custom tool contract: %+v", compact.ToolContract)
	}
	explorer, err := registry.ReconcileSnapshot(ExplorerAgentID, pebblestore.AgentProfile{Name: ExplorerAgentID, Provider: "codex", Model: "utility-model", Thinking: "high", Prompt: "mutable", RuntimeMode: pebblestore.AgentRuntimeModeReadWrite, ToolContract: &pebblestore.AgentToolContract{Preset: "read_write"}})
	if err != nil {
		t.Fatal(err)
	}
	if explorer.Prompt != ExplorerAgentPrompt() || explorer.RuntimeMode != pebblestore.AgentRuntimeModeRead || explorer.ToolContract == nil || explorer.ToolContract.Preset != "custom" {
		t.Fatalf("Explorer immutable contract was not restored: %+v", explorer)
	}
	for _, allowed := range []string{"read", "search", "list", "websearch", "webfetch"} {
		if cfg := explorer.ToolContract.Tools[allowed]; cfg.Enabled == nil || !*cfg.Enabled {
			t.Fatalf("Explorer locked tool %q unavailable: %+v", allowed, explorer.ToolContract)
		}
	}
	swarm, err := registry.ReconcileSnapshot(SwarmAgentID, pebblestore.AgentProfile{
		Name: SwarmAgentID, Mode: ModeSubagent, Provider: "codex", Model: "mutable", Prompt: "mutable", RuntimeMode: pebblestore.AgentRuntimeModeRead,
		ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"}, Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if swarm.Name != SwarmAgentID || swarm.Mode != ModePrimary || swarm.Prompt != SwarmAgentPrompt() || swarm.RuntimeMode != pebblestore.AgentRuntimeModePlanAuto || !swarm.Enabled || !swarm.Protected || swarm.ExitPlanModeEnabled == nil || !*swarm.ExitPlanModeEnabled {
		t.Fatalf("Swarm immutable contract was not restored: %+v", swarm)
	}
	if !reflect.DeepEqual(swarm.ToolContract, SwarmAgentToolContract()) {
		t.Fatalf("Swarm exact tool contract mismatch: got %+v want %+v", swarm.ToolContract, SwarmAgentToolContract())
	}

	clone, err := registry.ReconcileSnapshot(CloneAgentID, pebblestore.AgentProfile{
		Name: CloneAgentID, Provider: "codex", Model: "parent-model", Thinking: "high", Prompt: "mutable", RuntimeMode: pebblestore.AgentRuntimeModeRead,
		ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"bash": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if clone.Name != CloneAgentID || clone.Prompt != CloneAgentPrompt() || clone.Provider != "codex" || clone.Model != "parent-model" || clone.RuntimeMode != pebblestore.AgentRuntimeModeReadWrite || !clone.Enabled || clone.ExitPlanModeEnabled == nil || *clone.ExitPlanModeEnabled {
		t.Fatalf("Clone immutable contract was not restored: %+v", clone)
	}
	for _, allowed := range []string{"read", "search", "list", "write", "edit", "websearch", "webfetch", "webdownload", "git_status", "git_diff", "git_add", "git_commit"} {
		if cfg := clone.ToolContract.Tools[allowed]; cfg.Enabled == nil || !*cfg.Enabled {
			t.Fatalf("Clone locked tool %q unavailable: %+v", allowed, clone.ToolContract)
		}
	}
	for _, denied := range []string{"bash", "task", "manage_sessions", "manage_agent", "manage_todos", "plan_manage", "ask_user", "exit_plan_mode"} {
		if cfg := clone.ToolContract.Tools[denied]; cfg.Enabled == nil || *cfg.Enabled {
			t.Fatalf("Clone mandatory denial %q was not restored: %+v", denied, clone.ToolContract)
		}
	}
	if _, err := registry.ReconcileSnapshot(CloneAgentID, pebblestore.AgentProfile{Name: "clone"}); err == nil || !strings.Contains(err.Error(), "metadata mismatch") {
		t.Fatalf("Clone alias metadata mismatch error = %v", err)
	}
	if _, err := registry.ReconcileSnapshot(PlanSidechatAgentID, pebblestore.AgentProfile{Name: AISidechatAgentID}); err == nil || !strings.Contains(err.Error(), "metadata mismatch") {
		t.Fatalf("metadata mismatch error = %v", err)
	}
}

func TestEnsureSystemAgentRegistryExposesImmutableProfilesWithoutPersistingThem(t *testing.T) {
	svc, agents := newTestService(t)
	if err := svc.EnsureSystemAgentRegistry(); err != nil {
		t.Fatalf("ensure registry: %v", err)
	}
	for _, id := range []string{PlanSidechatAgentID, AISidechatAgentID, CompactAgentID, ExplorerAgentID, CloneAgentID, SwarmAgentID} {
		if id != SwarmAgentID {
			if _, ok, err := agents.GetProfile(id); err != nil || ok {
				t.Fatalf("system profile %q persisted ok=%v err=%v", id, ok, err)
			}
		}

		if _, err := svc.ResolveSystemAgent(id, pebblestore.AgentProfile{}); err != nil {
			t.Fatalf("resolve %q: %v", id, err)
		}
		if _, _, _, err := svc.Upsert(UpsertInput{Name: id, Mode: ModeSubagent, Prompt: "replace"}); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("system profile %q mutation error = %v, want reserved rejection", id, err)
		}
		if _, _, _, err := svc.Delete(id); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("system profile %q delete error = %v, want immutable-system rejection", id, err)
		}
	}
}
