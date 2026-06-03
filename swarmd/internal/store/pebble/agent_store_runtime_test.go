package pebblestore

import "testing"

func TestNormalizeAgentProfileCanonicalizesRuntimeAliases(t *testing.T) {
	profile := NormalizeAgentProfile(AgentProfile{
		Name:                "planner",
		RuntimeMode:         AgentRuntimeModePlanAuto,
		ExecutionSetting:    AgentExecutionSettingRead,
		ExitPlanModeEnabled: BoolPtr(true),
	})
	if profile.RuntimeMode != AgentRuntimeModePlanAuto {
		t.Fatalf("runtime_mode = %q, want plan_auto", profile.RuntimeMode)
	}
	if profile.ExecutionSetting != "" {
		t.Fatalf("execution_setting = %q, want empty", profile.ExecutionSetting)
	}
	if !AgentExitPlanModeEnabled(profile) {
		t.Fatalf("exit_plan_mode_enabled = false, want true")
	}
}

func TestNormalizeAgentProfileDerivesReadRuntimeFromReadOnlyContract(t *testing.T) {
	profile := NormalizeAgentProfile(AgentProfile{
		Name:         "reader",
		ToolContract: &AgentToolContract{Preset: "read_only"},
	})
	if profile.RuntimeMode != AgentRuntimeModeRead {
		t.Fatalf("runtime_mode = %q, want read", profile.RuntimeMode)
	}
	if profile.ExecutionSetting != AgentExecutionSettingRead {
		t.Fatalf("execution_setting = %q, want read", profile.ExecutionSetting)
	}
	if AgentExitPlanModeEnabled(profile) {
		t.Fatalf("exit_plan_mode_enabled = true, want false")
	}
}

func TestNormalizeAgentProfileDerivesReadWriteRuntimeFromMutatingContract(t *testing.T) {
	profile := NormalizeAgentProfile(AgentProfile{
		Name:         "writer",
		ToolContract: &AgentToolContract{Preset: "read_write"},
	})
	if profile.RuntimeMode != AgentRuntimeModeReadWrite {
		t.Fatalf("runtime_mode = %q, want readwrite", profile.RuntimeMode)
	}
	if profile.ExecutionSetting != AgentExecutionSettingReadWrite {
		t.Fatalf("execution_setting = %q, want readwrite", profile.ExecutionSetting)
	}
}
