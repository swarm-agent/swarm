package run

import "testing"

func TestManageArtifactPermissionRequirementIsAutomatic(t *testing.T) {
	for _, action := range []string{"create", "create_package", "list", "get", "read", "select", "delete", "materialize", "promote"} {
		requirement, needsApproval := permissionRequirement("auto", "manage_artifact", `{"action":"`+action+`"}`)
		if needsApproval || requirement != "manage_artifact" {
			t.Fatalf("manage_artifact %s requirement = %q/%v, want manage_artifact/false", action, requirement, needsApproval)
		}
	}
	if requirement, needsApproval := permissionRequirement("auto", "manage_artifact", `{"action":"generate_image","prompt":"one image"}`); !needsApproval || requirement != "manage_artifact_generate_image" {
		t.Fatalf("manage_artifact generate_image requirement = %q/%v, want manage_artifact_generate_image/true", requirement, needsApproval)
	}
	if requirement, needsApproval := permissionRequirement("auto+bypass_permissions", "manage_artifact", `{"action":"generate_image","prompt":"one image"}`); needsApproval || requirement != "manage_artifact" {
		t.Fatalf("bypassed manage_artifact generate_image requirement = %q/%v, want manage_artifact/false", requirement, needsApproval)
	}
}

func TestPlanManagePermissionRequirementIsTypedForLifecycleActions(t *testing.T) {
	cases := []struct {
		name            string
		args            string
		wantRequirement string
	}{
		{name: "follow-up", args: `{"action":"request_followup_checkpoint","change_request":"add a review note","checkpoint_title":"Audit note handoff","tasks":["Preserve request"],"acceptance_criteria":["No context lost"],"notes":"handoff context"}`, wantRequirement: "plan_followup_request"},
		{name: "amendment", args: `{"action":"amend_plan","plan_id":"plan_1","base_revision":2,"replace_from_checkpoint_id":"cp-2","document":{"id":"plan_1","title":"Plan","checkpoints":[{"id":"cp-2","status":"pending"}]}}`, wantRequirement: "plan_amendment_request"},
		{name: "new plan", args: `{"action":"request_new_plan","title":"New direction","document":{"title":"New direction","checkpoints":[{"id":"cp-new","title":"New","status":"pending"}]}}`, wantRequirement: "plan_new_request"},
		{name: "new plan alias", args: `{"action":"propose_plan","title":"New direction","document":{"title":"New direction","checkpoints":[{"id":"cp-new","title":"New","status":"pending"}]}}`, wantRequirement: "plan_new_request"},
		{name: "legacy draft update", args: `{"action":"save","plan_id":"plan_1","document":{"info":{"goal":"update"}}}`, wantRequirement: "plan_revision_request"},
		{name: "bulk checkpoint update", args: `{"action":"upsert_checkpoint","plan_id":"plan_1","checkpoint":{"id":"cp-2","title":"Second"}}`, wantRequirement: "plan_revision_request"},
		{name: "bulk checkpoint reorder", args: `{"action":"reorder_checkpoints","checkpoint_order":["cp-2","cp-1"]}`, wantRequirement: "plan_revision_request"},
	}
	if requirement, needsApproval := permissionRequirement("auto", "plan_manage", `{"action":"request_plan_revision","plan_id":"plan_1"}`); needsApproval || requirement != "" {
		t.Fatalf("removed request_plan_revision requirement = %q/%v, want empty/false", requirement, needsApproval)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requirement, needsApproval := permissionRequirement("auto", "plan_manage", tc.args)
			if !needsApproval || requirement != tc.wantRequirement {
				t.Fatalf("permissionRequirement() = %q/%v, want %q/true", requirement, needsApproval, tc.wantRequirement)
			}
		})
	}
}
