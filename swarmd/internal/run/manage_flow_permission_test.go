package run

import "testing"

func TestManageFlowPermissionRequirement(t *testing.T) {
	requirement, needsApproval := permissionRequirement("auto", "manage-flow", `{"action":"inspect"}`)
	if needsApproval || requirement != "manage_flow" {
		t.Fatalf("inspect requirement=%q approval=%v", requirement, needsApproval)
	}
	requirement, needsApproval = permissionRequirement("auto", "manage-flow", `{"action":"get","flow_id":"daily"}`)
	if needsApproval || requirement != "manage_flow" {
		t.Fatalf("get requirement=%q approval=%v", requirement, needsApproval)
	}
	requirement, needsApproval = permissionRequirement("auto", "manage-flow", `{"action":"create","content":{"name":"Daily"}}`)
	if !needsApproval || requirement != "flow_change" {
		t.Fatalf("create requirement=%q approval=%v", requirement, needsApproval)
	}
	requirement, needsApproval = permissionRequirement("auto", "manage_flow", `{"action":"delete","flow_id":"daily"}`)
	if !needsApproval || requirement != "flow_change" {
		t.Fatalf("delete requirement=%q approval=%v", requirement, needsApproval)
	}
}
