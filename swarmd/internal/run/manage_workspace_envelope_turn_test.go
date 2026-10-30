package run

import (
	"context"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

// Purpose: a manage_workspace call that retargets the conversation's
// session (restart_turn in the action's answer) still restarts the turn now
// that the answer is in the catalog envelope, where restart_turn sits in
// data; an answer without it does not. Threat: the model's next step running
// against the old workspace after a switch (a silent regression of the
// envelope move). Boundary: tool.Runtime.ManageWorkspaceCatalog (the
// envelope the native path returns) read by
// providerManagedToolRequiresTurnRestart; no store is needed for either.
func TestManageWorkspaceEnvelopeKeepsTurnRestart(t *testing.T) {
	answer := func(restart bool) string {
		t.Helper()
		out, err := (*tool.Runtime)(nil).ManageWorkspaceCatalog(context.Background(), tool.WorkspaceScope{}, map[string]any{"action": "update"}, func(map[string]any) (string, error) {
			if restart {
				return `{"action":"update","status":"ok","restart_turn":true}`, nil
			}
			return `{"action":"update","status":"ok","restart_turn":false}`, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tool.EnvelopeData(out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	call := tool.Call{Name: "manage_workspace"}
	if !providerManagedToolRequiresTurnRestart(call, tool.Result{Output: answer(true)}) {
		t.Fatal("a workspace switch in the envelope did not restart the turn")
	}
	if providerManagedToolRequiresTurnRestart(call, tool.Result{Output: answer(false)}) {
		t.Fatal("an answer without restart_turn restarted the turn")
	}
}
