package run

import (
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func TestResponseContainsTerminalPlanManageCall(t *testing.T) {
	tests := []struct {
		name  string
		calls []provideriface.FunctionCall
		want  bool
	}{
		{name: "final checkpoint", calls: []provideriface.FunctionCall{{Name: "plan_manage", Arguments: `{"action":"complete_checkpoint","handoff_overview":"done"}`}}, want: true},
		{name: "combined subtask completion", calls: []provideriface.FunctionCall{{Name: "plan-manage", Arguments: `{"action":"complete_subtask","complete_checkpoint":true,"handoff_overview":"done"}`}}, want: true},
		{name: "ordinary subtask", calls: []provideriface.FunctionCall{{Name: "plan_manage", Arguments: `{"action":"complete_subtask"}`}}},
		{name: "progress update", calls: []provideriface.FunctionCall{{Name: "plan_manage", Arguments: `{"action":"update_checkpoint"}`}}},
		{name: "unrelated tool", calls: []provideriface.FunctionCall{{Name: "bash", Arguments: `{}`}}},
		{name: "invalid arguments", calls: []provideriface.FunctionCall{{Name: "plan_manage", Arguments: `{`}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := responseContainsTerminalPlanManageCall(test.calls); got != test.want {
				t.Fatalf("responseContainsTerminalPlanManageCall() = %v, want %v", got, test.want)
			}
		})
	}
}
