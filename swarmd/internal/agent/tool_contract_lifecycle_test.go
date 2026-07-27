package agent

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestEnsureDefaultsBackfillsMissingBuiltInToolContractsOnly(t *testing.T) {
	svc, agents := newTestService(t)
	legacyBuiltIns := []pebblestore.AgentProfile{
		{Name: "swarm", Mode: ModePrimary, RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), Prompt: "custom swarm", Enabled: true},
		{Name: "finder", Mode: ModeSubagent, RuntimeMode: pebblestore.AgentRuntimeModeRead, ExecutionSetting: pebblestore.AgentExecutionSettingRead, Prompt: "custom finder", Enabled: false},
		{Name: "memory", Mode: ModeSubagent, RuntimeMode: pebblestore.AgentRuntimeModeReadWrite, ExecutionSetting: pebblestore.AgentExecutionSettingReadWrite, Prompt: defaultMemoryPrompt(), Enabled: true},
		{Name: "parallel", Mode: ModeSubagent, RuntimeMode: pebblestore.AgentRuntimeModeReadWrite, ExecutionSetting: pebblestore.AgentExecutionSettingReadWrite, Prompt: "custom parallel", Enabled: true},
		{Name: "custom", Mode: ModeSubagent, RuntimeMode: pebblestore.AgentRuntimeModeRead, ExecutionSetting: pebblestore.AgentExecutionSettingRead, Prompt: "custom agent", Enabled: true},
	}
	for _, profile := range legacyBuiltIns {
		if err := agents.PutProfile(profile); err != nil {
			t.Fatalf("put %s: %v", profile.Name, err)
		}
	}

	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	wantPresets := map[string]string{
		"swarm":    "custom",
		"finder":   "read_only",
		"memory":   "background_commit",
		"parallel": "read_write",
	}
	for name, wantPreset := range wantPresets {
		profile, ok, err := agents.GetProfile(name)
		if err != nil || !ok {
			t.Fatalf("GetProfile(%s) ok=%v err=%v", name, ok, err)
		}
		if profile.ToolContract == nil {
			t.Fatalf("%s missing backfilled tool_contract", name)
		}
		if profile.ToolContract.Preset != wantPreset {
			t.Fatalf("%s preset = %q, want %q", name, profile.ToolContract.Preset, wantPreset)
		}
	}
	custom, ok, err := agents.GetProfile("custom")
	if err != nil || !ok {
		t.Fatalf("GetProfile(custom) ok=%v err=%v", ok, err)
	}
	if custom.ToolContract != nil {
		t.Fatalf("custom tool_contract = %+v, want nil", custom.ToolContract)
	}
}

func TestEnsureDefaultsAddsManageSessionsToSwarmAndPreservesExplicitOptOut(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled *bool
		want    bool
	}{
		{name: "missing", want: true},
		{name: "disabled", enabled: pebblestore.BoolPtr(false), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, agents := newTestService(t)
			tools := map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}
			if tc.enabled != nil {
				tools["manage_sessions"] = pebblestore.AgentToolConfig{Enabled: tc.enabled}
			}
			if err := agents.PutProfile(pebblestore.AgentProfile{Name: "swarm", Mode: ModePrimary, RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: tools}, Enabled: true}); err != nil {
				t.Fatalf("put swarm: %v", err)
			}
			if err := svc.EnsureDefaults(); err != nil {
				t.Fatalf("EnsureDefaults() error = %v", err)
			}
			profile, ok, err := agents.GetProfile("swarm")
			if err != nil || !ok {
				t.Fatalf("GetProfile(swarm) ok=%v err=%v", ok, err)
			}
			cfg, ok := profile.ToolContract.Tools["manage_sessions"]
			if !ok || cfg.Enabled == nil || *cfg.Enabled != tc.want {
				t.Fatalf("manage_sessions = %+v, want enabled=%v", cfg, tc.want)
			}
		})
	}
}

func TestBuiltInManageSessionsDefaultIsSwarmOnly(t *testing.T) {
	if cfg := defaultSwarmToolContract().Tools["manage_sessions"]; cfg.Enabled == nil || !*cfg.Enabled {
		t.Fatalf("swarm manage_sessions = %+v, want enabled", cfg)
	}
	for name, contract := range map[string]*pebblestore.AgentToolContract{
		"finder":     FinderAgentToolContract(),
		"memory":     defaultMemoryToolContract(),
		"read_write": defaultReadWriteSubagentToolContract(),
	} {
		if cfg, ok := contract.Tools["manage_sessions"]; ok && cfg.Enabled != nil && *cfg.Enabled {
			t.Fatalf("%s manage_sessions unexpectedly enabled", name)
		}
	}
}

func TestEnsureDefaultsPreservesExistingBuiltInToolContract(t *testing.T) {
	svc, agents := newTestService(t)
	customContract := &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}
	for _, profile := range []pebblestore.AgentProfile{
		{Name: "swarm", Mode: ModePrimary, RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), Prompt: "custom swarm", ToolContract: customContract, Enabled: true},
		{Name: "memory", Mode: ModeSubagent, RuntimeMode: pebblestore.AgentRuntimeModeReadWrite, ExecutionSetting: pebblestore.AgentExecutionSettingReadWrite, Prompt: defaultMemoryPrompt(), ToolContract: customContract, Enabled: true},
	} {
		if err := agents.PutProfile(profile); err != nil {
			t.Fatalf("put %s: %v", profile.Name, err)
		}
	}

	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	for _, name := range []string{"swarm", "memory"} {
		profile, ok, err := agents.GetProfile(name)
		if err != nil || !ok {
			t.Fatalf("GetProfile(%s) ok=%v err=%v", name, ok, err)
		}
		if profile.ToolContract == nil || len(profile.ToolContract.Tools) != 1 {
			t.Fatalf("%s contract was not preserved: %+v", name, profile.ToolContract)
		}
		if cfg, ok := profile.ToolContract.Tools["read"]; !ok || cfg.Enabled == nil || !*cfg.Enabled {
			t.Fatalf("%s read config = %+v, want preserved enabled", name, cfg)
		}
	}
}

func TestUpsertRequiresToolContractForNewAgentsAndPreservesOnUpdate(t *testing.T) {
	svc, _ := newTestService(t)
	_, _, _, err := svc.Upsert(UpsertInput{
		Name:        "missing-contract",
		Mode:        ModeSubagent,
		Prompt:      "No contract.",
		RuntimeMode: pebblestore.AgentRuntimeModeRead,
		Enabled:     pebblestore.BoolPtr(true),
	})
	if err == nil || !strings.Contains(err.Error(), "tool_contract is required") {
		t.Fatalf("Upsert() error = %v, want tool_contract required", err)
	}

	created, _, _, err := svc.Upsert(UpsertInput{
		Name:         "contracted",
		Mode:         ModeSubagent,
		Prompt:       "Has contract.",
		ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"},
		Enabled:      pebblestore.BoolPtr(true),
	})
	if err != nil {
		t.Fatalf("create contracted: %v", err)
	}
	if created.ToolContract == nil || created.ToolContract.Preset != "read_only" {
		t.Fatalf("created contract = %+v, want read_only", created.ToolContract)
	}

	updated, _, _, err := svc.Upsert(UpsertInput{
		Name:   "contracted",
		Prompt: "Updated prompt.",
	})
	if err != nil {
		t.Fatalf("update contracted: %v", err)
	}
	if updated.ToolContract == nil || updated.ToolContract.Preset != "read_only" {
		t.Fatalf("updated contract = %+v, want preserved read_only", updated.ToolContract)
	}
	if updated.Prompt != "Updated prompt." {
		t.Fatalf("updated prompt = %q", updated.Prompt)
	}
}

func TestPreviewUpsertRequiresToolContractForNewAgents(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.PreviewUpsert(UpsertInput{Name: "preview-missing", Mode: ModeSubagent, Prompt: "No contract."})
	if err == nil || !strings.Contains(err.Error(), "tool_contract is required") {
		t.Fatalf("PreviewUpsert() error = %v, want tool_contract required", err)
	}
}

func TestAssignCustomToolFailsWhenAgentToolContractMissing(t *testing.T) {
	svc, agents := newTestService(t)
	if err := agents.PutProfile(pebblestore.AgentProfile{Name: "legacy-custom", Mode: ModeSubagent, RuntimeMode: pebblestore.AgentRuntimeModeReadWrite, ExecutionSetting: pebblestore.AgentExecutionSettingReadWrite, Prompt: "legacy", Enabled: true}); err != nil {
		t.Fatalf("put legacy profile: %v", err)
	}
	if _, err := svc.PutCustomTool(pebblestore.AgentCustomToolDefinition{Name: "probe", Kind: pebblestore.AgentCustomToolKindFixedBash, Command: "echo ok"}); err != nil {
		t.Fatalf("PutCustomTool() error = %v", err)
	}

	_, _, _, err := svc.AssignCustomTool("legacy-custom", "probe")
	if err == nil || !strings.Contains(err.Error(), "tool_contract is not configured") {
		t.Fatalf("AssignCustomTool() error = %v, want missing tool_contract", err)
	}
}
