package agent

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestAgentDefaultSessionModeValidation(t *testing.T) {
	svc, _ := newTestService(t)
	_, _, _, err := svc.Upsert(UpsertInput{
		Name:               "invalid-default-mode",
		Mode:               ModeSubagent,
		RuntimeMode:        pebblestore.AgentRuntimeModeRead,
		DefaultSessionMode: "later",
		ToolContract:       &pebblestore.AgentToolContract{Preset: "read_only"},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid default_session_mode") {
		t.Fatalf("Upsert() error = %v, want invalid default_session_mode", err)
	}
}

func TestUpsertPlanAutoClearsExecutionSetting(t *testing.T) {
	svc, _ := newTestService(t)
	profile, _, _, err := svc.Upsert(UpsertInput{
		Name:         "planner",
		Mode:         ModeSubagent,
		Prompt:       "Plan first.",
		RuntimeMode:  pebblestore.AgentRuntimeModePlanAuto,
		ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"},
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if profile.RuntimeMode != pebblestore.AgentRuntimeModePlanAuto {
		t.Fatalf("runtime_mode = %q, want plan_auto", profile.RuntimeMode)
	}
	if profile.ExecutionSetting != "" {
		t.Fatalf("execution_setting = %q, want empty for plan_auto", profile.ExecutionSetting)
	}
	if !pebblestore.AgentExitPlanModeEnabled(profile) {
		t.Fatalf("exit_plan_mode_enabled = false, want true")
	}
}

func TestUpsertRejectsPlanAutoWithExecutionSettingAlias(t *testing.T) {
	svc, _ := newTestService(t)
	_, _, _, err := svc.Upsert(UpsertInput{
		Name:             "contradictory-planner",
		Mode:             ModeSubagent,
		Prompt:           "Plan first.",
		RuntimeMode:      pebblestore.AgentRuntimeModePlanAuto,
		ExecutionSetting: pebblestore.AgentExecutionSettingRead,
		ToolContract:     &pebblestore.AgentToolContract{Preset: "read_only"},
	})
	if err == nil || !strings.Contains(err.Error(), "plan_auto cannot include execution_setting") {
		t.Fatalf("Upsert() error = %v, want plan_auto/execution_setting contradiction", err)
	}
}

func TestUpsertExplicitRuntimeModeOverridesPresetSuggestion(t *testing.T) {
	svc, _ := newTestService(t)
	profile, _, _, err := svc.Upsert(UpsertInput{
		Name:             "read-with-readwrite-preset",
		Mode:             ModeSubagent,
		Prompt:           "Read only.",
		RuntimeMode:      pebblestore.AgentRuntimeModeRead,
		ExecutionSetting: pebblestore.AgentExecutionSettingRead,
		ToolContract:     &pebblestore.AgentToolContract{Preset: "read_write"},
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if profile.RuntimeMode != pebblestore.AgentRuntimeModeRead {
		t.Fatalf("runtime_mode = %q, want read", profile.RuntimeMode)
	}
	if profile.ExecutionSetting != pebblestore.AgentExecutionSettingRead {
		t.Fatalf("execution_setting = %q, want read", profile.ExecutionSetting)
	}
}

func TestUpsertPlanAutoAllowsPresetSuggestions(t *testing.T) {
	svc, _ := newTestService(t)
	profile, _, _, err := svc.Upsert(UpsertInput{
		Name:         "plan-with-readwrite-preset",
		Mode:         ModeSubagent,
		Prompt:       "Plan first.",
		RuntimeMode:  pebblestore.AgentRuntimeModePlanAuto,
		ToolContract: &pebblestore.AgentToolContract{Preset: "read_write"},
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if profile.RuntimeMode != pebblestore.AgentRuntimeModePlanAuto {
		t.Fatalf("runtime_mode = %q, want plan_auto", profile.RuntimeMode)
	}
	if !pebblestore.AgentExitPlanModeEnabled(profile) {
		t.Fatalf("exit_plan_mode_enabled = false, want true")
	}
}

func TestUpsertDerivesReadRuntimeFromReadOnlyContract(t *testing.T) {
	svc, _ := newTestService(t)
	profile, _, _, err := svc.Upsert(UpsertInput{
		Name:         "read-contract",
		Mode:         ModeSubagent,
		Prompt:       "Read only.",
		ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"},
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if profile.RuntimeMode != pebblestore.AgentRuntimeModeRead {
		t.Fatalf("runtime_mode = %q, want read", profile.RuntimeMode)
	}
	if profile.ExecutionSetting != pebblestore.AgentExecutionSettingRead {
		t.Fatalf("execution_setting = %q, want read", profile.ExecutionSetting)
	}
	if pebblestore.AgentExitPlanModeEnabled(profile) {
		t.Fatalf("exit_plan_mode_enabled = true, want false")
	}
}

func TestUpsertDerivesReadWriteRuntimeFromMutatingContract(t *testing.T) {
	svc, _ := newTestService(t)
	profile, _, _, err := svc.Upsert(UpsertInput{
		Name:         "write-contract",
		Mode:         ModeSubagent,
		Prompt:       "Write.",
		ToolContract: &pebblestore.AgentToolContract{Preset: "read_write"},
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if profile.RuntimeMode != pebblestore.AgentRuntimeModeReadWrite {
		t.Fatalf("runtime_mode = %q, want readwrite", profile.RuntimeMode)
	}
	if profile.ExecutionSetting != pebblestore.AgentExecutionSettingReadWrite {
		t.Fatalf("execution_setting = %q, want readwrite", profile.ExecutionSetting)
	}
}
