package run

import (
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
