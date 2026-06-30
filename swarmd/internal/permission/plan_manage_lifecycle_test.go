package permission

import "testing"

func TestPlanManageLifecycleRequirementIsTyped(t *testing.T) {
	cases := []struct {
		name string
		args string
		want string
	}{
		{name: "followup", args: `{"action":"request_followup_checkpoint","change_request":"add a review note"}`, want: "plan_followup_request"},
		{name: "request changes alias", args: `{"action":"request_changes","change_request":"add a review note"}`, want: "plan_followup_request"},
		{name: "revision", args: `{"action":"request_plan_revision","plan_id":"plan_1"}`, want: "plan_revision_request"},
		{name: "new plan", args: `{"action":"request_new_plan","title":"New direction"}`, want: "plan_new_request"},
		{name: "legacy save existing", args: `{"action":"save","plan_id":"plan_1","document":{"info":{"goal":"update"}}}`, want: "plan_revision_request"},
		{name: "draft save no active plan id", args: `{"action":"save","document":{"info":{"goal":"draft"}}}`, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlanManageLifecycleRequirement(tc.args); got != tc.want {
				t.Fatalf("PlanManageLifecycleRequirement() = %q, want %q", got, tc.want)
			}
		})
	}
}
