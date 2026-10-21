package tool

import "testing"

func TestManageProjectsDelegation(t *testing.T) {
	cases := []struct {
		args map[string]any
		want bool
	}{
		{map[string]any{"action": "propose_task", "agent": "coder"}, true},
		{map[string]any{"action": "create_task", "agent": "swarm", "auto_approve": true}, true},
		{map[string]any{"action": "propose_task", "intent": "audit"}, true},
		{map[string]any{"action": "propose_task"}, false},
		{map[string]any{"action": "propose_task", "agent": "image"}, false},
		{map[string]any{"action": "propose_task", "agent": "plan"}, false},
		{map[string]any{"action": "propose_task", "agent": "coder", "operation": "create"}, false},
		{map[string]any{"action": "propose_task", "agent": 7}, false},
		{map[string]any{"action": "delete_task", "agent": "coder"}, false},
	}
	for _, c := range cases {
		if got := ManageProjectsDelegation(c.args); got != c.want {
			t.Errorf("ManageProjectsDelegation(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}
