package run

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestManageImageToolContractCanonicalizationAndOptIn(t *testing.T) {
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	readwrite := pebblestore.AgentExecutionSettingReadWrite
	profile := pebblestore.AgentProfile{
		Name:             "image-worker",
		Mode:             "background",
		ExecutionSetting: readwrite,
		ToolContract:     &pebblestore.AgentToolContract{},
	}
	resolved, _, disabled, err := svc.ResolveAgentToolContract(profile)
	if err != nil {
		t.Fatalf("resolve baseline contract: %v", err)
	}
	if resolved.Tools["manage_image"].Enabled {
		t.Fatalf("manage-image must be opt-in for baseline background contracts: %#v", resolved.Tools["manage_image"])
	}
	if !disabled["manage_image"] {
		t.Fatalf("manage-image should be disabled in compiled policy by default: %#v", disabled)
	}

	enabled := true
	profile.ToolContract = &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{
		"manage-image": {Enabled: &enabled},
	}}
	resolved, _, disabled, err = svc.ResolveAgentToolContract(profile)
	if err != nil {
		t.Fatalf("resolve opt-in contract: %v", err)
	}
	if !resolved.Tools["manage_image"].Enabled {
		t.Fatalf("manage-image dash name did not opt in canonical manage_image entry: %#v", resolved.Tools["manage_image"])
	}
	if disabled["manage_image"] {
		t.Fatalf("manage-image should not remain disabled after explicit opt-in: %#v", disabled)
	}
}

func TestResolveAgentToolContractRejectsFallbackHydration(t *testing.T) {
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	enabled := true

	_, _, _, err := svc.ResolveAgentToolContract(pebblestore.AgentProfile{
		Name:                "planner",
		Mode:                "subagent",
		ExitPlanModeEnabled: pebblestore.BoolPtr(true),
	})
	if err == nil || !strings.Contains(err.Error(), "tool_contract") {
		t.Fatalf("missing tool_contract error = %v, want tool_contract requirement", err)
	}

	_, _, _, err = svc.ResolveAgentToolContract(pebblestore.AgentProfile{
		Name: "legacy-scope",
		Mode: "subagent",
		ToolScope: &pebblestore.AgentToolScope{
			AllowTools: []string{"read"},
		},
		ToolContract: &pebblestore.AgentToolContract{},
	})
	if err == nil || !strings.Contains(err.Error(), "legacy tool_scope") {
		t.Fatalf("legacy tool_scope error = %v, want hard error", err)
	}

	_, _, _, err = svc.ResolveAgentToolContract(pebblestore.AgentProfile{
		Name: "unknown-tool",
		Mode: "subagent",
		ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{
			"definitely_not_a_tool": {Enabled: &enabled},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("unknown tool error = %v, want unknown tool hard error", err)
	}
}

func TestManageImagePermissionRequirement(t *testing.T) {
	requirement, needsApproval := permissionRequirement("auto", "manage-image", `{"action":"inspect"}`)
	if requirement != "manage_image" || needsApproval {
		t.Fatalf("inspect requirement=%q approval=%v, want manage_image false", requirement, needsApproval)
	}
	requirement, needsApproval = permissionRequirement("auto", "manage-image", `{"action":"generate","prompt":"test"}`)
	if requirement != "image_generation" || !needsApproval {
		t.Fatalf("generate requirement=%q approval=%v, want image_generation true", requirement, needsApproval)
	}
}
