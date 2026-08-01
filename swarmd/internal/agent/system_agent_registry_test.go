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
	want := []string{SwarmAgentID, AISidechatAgentID, AITaskPreparerAgentID, CoderAgentID, CompactAgentID, DesignerAgentID, FinderAgentID, PlanSidechatAgentID, ReviewCommitAgentID, WorkspaceDefinitionAgentID}
	if got := registry.IDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("registry IDs = %v, want %v", got, want)
	}
	for kind, id := range map[string]string{SystemSidechatKindPlan: PlanSidechatAgentID, SystemSidechatKindAI: AISidechatAgentID, SystemSidechatKindCompact: CompactAgentID, SystemSidechatKindFinder: FinderAgentID, SystemSidechatKindCoder: CoderAgentID, SystemSidechatKindDesigner: DesignerAgentID} {
		definition, ok := registry.DefinitionBySidechatKind(kind)
		if !ok || definition.ID != id {
			t.Fatalf("kind %q resolved to %+v, ok=%v", kind, definition, ok)
		}
	}
	for _, id := range []string{PlanSidechatAgentID, AISidechatAgentID} {
		definition, _ := registry.DefinitionByID(id)
		if !definition.RequiresSidechatMetadata || !IsReservedSidechatAgentName(id) {
			t.Fatalf("sidechat-only system agent %q is not protected: %+v", id, definition)
		}
	}
	for _, id := range []string{SwarmAgentID, AITaskPreparerAgentID, CompactAgentID, FinderAgentID, CoderAgentID, DesignerAgentID, ReviewCommitAgentID, WorkspaceDefinitionAgentID} {
		definition, _ := registry.DefinitionByID(id)
		if definition.RequiresSidechatMetadata || IsReservedSidechatAgentName(id) {
			t.Fatalf("ordinary/task system agent %q was classified as sidechat-only: %+v", id, definition)
		}
	}
	visible := registry.UserVisibleIDs()
	for _, id := range []string{SwarmAgentID, CompactAgentID, FinderAgentID, CoderAgentID, DesignerAgentID} {
		if !containsString(visible, id) {
			t.Fatalf("user-visible system agents %v omit %q", visible, id)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestCoderIdentityDoesNotAcceptRetiredCloneNames(t *testing.T) {
	for _, name := range []string{"coder", CoderAgentID} {
		if !IsCoderAgentName(name) {
			t.Fatalf("Coder identity %q was not accepted", name)
		}
	}
	for _, name := range []string{"clone", "system-clone"} {
		if IsCoderAgentName(name) || IsReservedSystemAgentName(name) {
			t.Fatalf("retired Clone identity %q remains launchable", name)
		}
	}
}

func TestBuiltinSystemAgentRegistryUserVisibleIDs(t *testing.T) {
	registry, err := BuiltinSystemAgentRegistry()
	if err != nil {
		t.Fatalf("BuiltinSystemAgentRegistry() error = %v", err)
	}
	want := []string{SwarmAgentID, CoderAgentID, CompactAgentID, DesignerAgentID, FinderAgentID}
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
		Name: PlanSidechatAgentID, Provider: "codex", Model: "plan-model", Thinking: "high", AutoServiceTier: "priority",
		Prompt: planPrompt, RuntimeMode: pebblestore.AgentRuntimeModeReadWrite, Enabled: false,
		ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"bash": {Enabled: pebblestore.BoolPtr(true)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Prompt != planPrompt || plan.Provider != "codex" || plan.Model != "plan-model" || plan.AutoServiceTier != "priority" {
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
	reviewCommit, err := registry.Materialize(ReviewCommitAgentID, pebblestore.AgentProfile{Provider: "codex", Model: "base-model", Thinking: "high", AutoProvider: "openai", AutoModel: "auto-model", AutoThinking: "xhigh", AutoServiceTier: "priority"})
	if err != nil {
		t.Fatal(err)
	}
	if reviewCommit.Provider != "codex" || reviewCommit.Model != "base-model" || reviewCommit.Thinking != "high" || reviewCommit.AutoServiceTier != "priority" || reviewCommit.RuntimeMode != pebblestore.AgentRuntimeModeReadWrite {
		t.Fatalf("Review Commit canonical model inheritance mismatch: %+v", reviewCommit)
	}
	for _, allowed := range []string{"read", "git_status", "git_diff", "git_add", "git_commit"} {
		if cfg := reviewCommit.ToolContract.Tools[allowed]; cfg.Enabled == nil || !*cfg.Enabled {
			t.Fatalf("Review Commit tool %q unavailable: %+v", allowed, reviewCommit.ToolContract)
		}
	}
	if len(reviewCommit.ToolContract.Tools) != 5 {
		t.Fatalf("Review Commit tool contract is not least privilege: %+v", reviewCommit.ToolContract)
	}

	workspaceDefinition, err := registry.ReconcileSnapshot(WorkspaceDefinitionAgentID, pebblestore.AgentProfile{Name: WorkspaceDefinitionAgentID, Provider: "codex", Model: "router-model", Thinking: "high", AutoServiceTier: "priority", Prompt: "mutable", RuntimeMode: pebblestore.AgentRuntimeModeReadWrite, ToolContract: &pebblestore.AgentToolContract{Preset: "read_write"}})
	if err != nil {
		t.Fatal(err)
	}
	if workspaceDefinition.Name != WorkspaceDefinitionAgentID || workspaceDefinition.Provider != "codex" || workspaceDefinition.Model != "router-model" || workspaceDefinition.Thinking != "high" || workspaceDefinition.AutoServiceTier != "priority" {
		t.Fatalf("Workspace Definition model snapshot mismatch: %+v", workspaceDefinition)
	}
	if workspaceDefinition.Prompt != WorkspaceDefinitionAgentPrompt() || workspaceDefinition.RuntimeMode != pebblestore.AgentRuntimeModeRead || workspaceDefinition.ToolContract == nil || workspaceDefinition.ToolContract.Preset != "custom" || len(workspaceDefinition.ToolContract.Tools) != 3 {
		t.Fatalf("Workspace Definition immutable contract was not restored: %+v", workspaceDefinition)
	}
	for _, allowed := range []string{"read", "search", "list"} {
		if cfg := workspaceDefinition.ToolContract.Tools[allowed]; cfg.Enabled == nil || !*cfg.Enabled {
			t.Fatalf("Workspace Definition tool %q unavailable: %+v", allowed, workspaceDefinition.ToolContract)
		}
	}
	for _, instruction := range []string{"First decide whether", "answer directly in one shot without calling tools", "If and only if they are insufficient"} {
		if !strings.Contains(workspaceDefinition.Prompt, instruction) {
			t.Fatalf("Workspace Definition prompt missing decision instruction %q: %s", instruction, workspaceDefinition.Prompt)
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
	finder, err := registry.ReconcileSnapshot(FinderAgentID, pebblestore.AgentProfile{Name: FinderAgentID, Provider: "codex", Model: "utility-model", Thinking: "high", AutoServiceTier: "priority", Prompt: "mutable", RuntimeMode: pebblestore.AgentRuntimeModeReadWrite, ToolContract: &pebblestore.AgentToolContract{Preset: "read_write"}})
	if err != nil {
		t.Fatal(err)
	}
	if finder.AutoServiceTier != "priority" {
		t.Fatalf("Finder service tier was not preserved: %+v", finder)
	}
	if finder.Prompt != FinderAgentPrompt() || finder.RuntimeMode != pebblestore.AgentRuntimeModeRead || finder.ToolContract == nil || finder.ToolContract.Preset != "custom" {
		t.Fatalf("Finder immutable contract was not restored: %+v", finder)
	}
	for _, allowed := range []string{"read", "search", "list", "websearch", "webfetch"} {
		if cfg := finder.ToolContract.Tools[allowed]; cfg.Enabled == nil || !*cfg.Enabled {
			t.Fatalf("Finder locked tool %q unavailable: %+v", allowed, finder.ToolContract)
		}
	}
	designer, err := registry.ReconcileSnapshot(DesignerAgentID, pebblestore.AgentProfile{Name: DesignerAgentID, Provider: "openai", Model: "utility-model", Thinking: "medium", Prompt: "mutable", RuntimeMode: pebblestore.AgentRuntimeModeRead, DefaultSessionMode: pebblestore.AgentDefaultSessionModePlan, ToolContract: &pebblestore.AgentToolContract{Preset: "read_write"}})
	if err != nil {
		t.Fatal(err)
	}
	if designer.Name != DesignerAgentID || designer.Mode != ModeSubagent || designer.Prompt != DesignerAgentPrompt() || designer.RuntimeMode != pebblestore.AgentRuntimeModeReadWrite || designer.DefaultSessionMode != pebblestore.AgentDefaultSessionModeAuto || !designer.Enabled || !designer.Protected || designer.ExitPlanModeEnabled == nil || *designer.ExitPlanModeEnabled {
		t.Fatalf("Designer immutable contract was not restored: %+v", designer)
	}
	for _, allowed := range []string{"read", "search", "find", "list", "write", "edit"} {
		if cfg := designer.ToolContract.Tools[allowed]; cfg.Enabled == nil || !*cfg.Enabled {
			t.Fatalf("Designer locked tool %q unavailable: %+v", allowed, designer.ToolContract)
		}
	}
	for _, denied := range []string{"bash", "git_status", "git_diff", "git_add", "git_commit", "task", "skill_use", "manage_skill", "manage_agent", "manage_theme", "manage_sessions", "manage_worktree", "manage_todos", "plan_manage", "ask_user", "exit_plan_mode"} {
		if cfg := designer.ToolContract.Tools[denied]; cfg.Enabled == nil || *cfg.Enabled {
			t.Fatalf("Designer mandatory denial %q was not restored: %+v", denied, designer.ToolContract)
		}
	}
	if _, exists := designer.ToolContract.Tools["create_file"]; exists || strings.Contains(DesignerAgentPrompt(), "create_file") {
		t.Fatalf("Designer must not register or reference create_file: %+v", designer.ToolContract)
	}

	swarm, err := registry.ReconcileSnapshot(SwarmAgentID, pebblestore.AgentProfile{
		Name: SwarmAgentID, Mode: ModeSubagent, Provider: "codex", Model: "mutable", Prompt: "mutable", RuntimeMode: pebblestore.AgentRuntimeModeRead,
		ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"}, Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if swarm.Name != SwarmAgentID || swarm.Mode != ModePrimary || swarm.Prompt != SwarmAgentPrompt() || swarm.RuntimeMode != pebblestore.AgentRuntimeModePlanAuto || swarm.DefaultSessionMode != pebblestore.AgentDefaultSessionModeAuto || !swarm.Enabled || !swarm.Protected || swarm.ExitPlanModeEnabled == nil || !*swarm.ExitPlanModeEnabled {
		t.Fatalf("Swarm immutable contract was not restored: %+v", swarm)
	}
	if !reflect.DeepEqual(swarm.ToolContract, SwarmAgentToolContract()) {
		t.Fatalf("Swarm exact tool contract mismatch: got %+v want %+v", swarm.ToolContract, SwarmAgentToolContract())
	}
	if swarm.Provider != "" || swarm.Model != "" || swarm.Thinking != "" || swarm.ModelMode != "" || swarm.PlanProvider != "" || swarm.PlanModel != "" || swarm.PlanThinking != "" || swarm.PlanServiceTier != "" || swarm.AutoProvider != "" || swarm.AutoModel != "" || swarm.AutoThinking != "" || swarm.AutoServiceTier != "" {
		t.Fatalf("Swarm system identity retained model-bearing profile fields: %+v", swarm)
	}

	clone, err := registry.ReconcileSnapshot(CoderAgentID, pebblestore.AgentProfile{
		Name: CoderAgentID, Provider: "codex", Model: "parent-model", Thinking: "high", Prompt: "mutable", RuntimeMode: pebblestore.AgentRuntimeModeRead,
		ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"bash": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if clone.Name != CoderAgentID || clone.Prompt != CoderAgentPrompt() || clone.Provider != "codex" || clone.Model != "parent-model" || clone.RuntimeMode != pebblestore.AgentRuntimeModeReadWrite || !clone.Enabled || clone.ExitPlanModeEnabled == nil || *clone.ExitPlanModeEnabled {
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
	if _, err := registry.ReconcileSnapshot(CoderAgentID, pebblestore.AgentProfile{Name: "system-clone"}); err == nil || !strings.Contains(err.Error(), "metadata mismatch") {
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
	for _, id := range []string{PlanSidechatAgentID, AISidechatAgentID, AITaskPreparerAgentID, CompactAgentID, FinderAgentID, CoderAgentID, DesignerAgentID, ReviewCommitAgentID, SwarmAgentID} {
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
