package run

import "testing"

func TestPlanManagePermissionRequirementIsTypedForLifecycleActions(t *testing.T) {
	cases := []struct {
		name            string
		args            string
		wantRequirement string
	}{
		{name: "follow-up", args: `{"action":"request_followup_checkpoint","change_request":"add a review note"}`, wantRequirement: "plan_followup_request"},
		{name: "revision", args: `{"action":"request_plan_revision","plan_id":"plan_1"}`, wantRequirement: "plan_revision_request"},
		{name: "amendment", args: `{"action":"amend_plan","plan_id":"plan_1","base_revision":2,"replace_from_checkpoint_id":"cp-2","document":{"id":"plan_1","title":"Plan","checkpoints":[{"id":"cp-2","status":"pending"}]}}`, wantRequirement: "plan_revision_request"},
		{name: "new plan", args: `{"action":"request_new_plan","title":"New direction"}`, wantRequirement: "plan_new_request"},
		{name: "legacy draft update", args: `{"action":"save","plan_id":"plan_1","document":{"info":{"goal":"update"}}}`, wantRequirement: "plan_revision_request"},
		{name: "bulk checkpoint update", args: `{"action":"upsert_checkpoint","plan_id":"plan_1","checkpoint":{"id":"cp-2","title":"Second"}}`, wantRequirement: "plan_revision_request"},
		{name: "bulk checkpoint reorder", args: `{"action":"reorder_checkpoints","checkpoint_order":["cp-2","cp-1"]}`, wantRequirement: "plan_revision_request"},
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
