package tool

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: executeManageEnvironments get_operation must expose complete bounded
// stdout/stderr for success and failure, apply read-time caps with an explicit
// truncation flag, and never mutate durable output or cross workspace authority.
// The authenticated tool plus real store is the narrowest observable API layer.
func TestEnvironmentsTool_OperationOutput(t *testing.T) {
	h := setupEnvironmentsToolHarness(t)
	store := h.depStore.Operations()
	for _, status := range []environments.OperationStatus{environments.OperationStatusSucceeded, environments.OperationStatusFailed} {
		id := "op_output_"+string(status)
		now := time.Now().UnixMilli()
		op, _, err := store.AdmitOperation(environments.EnvironmentOperation{OperationID: id, AccountScopeID: "test-account", WorkspaceID: "ws-test", Action: "exec", Status: environments.OperationStatusQueued, CreatedAt: now, ObservedAt: now, Deadline: now+60000})
		if err != nil { t.Fatal(err) }
		op, err = store.TransitionOperation(pebblestore.OperationTransitionInput{AccountScopeID: "test-account", WorkspaceID: "ws-test", OperationID: id, ExpectedRevision: op.Revision, TargetStatus: environments.OperationStatusRunning, ObservedAt: now})
		if err != nil { t.Fatal(err) }
		code := 0
		if status == environments.OperationStatusFailed { code = 7 }
		stdout := strings.Repeat("x", 2500)+"FINAL ASSERTION\n"
		_, err = store.TransitionOperation(pebblestore.OperationTransitionInput{AccountScopeID: "test-account", WorkspaceID: "ws-test", OperationID: id, ExpectedRevision: op.Revision, TargetStatus: status, ObservedAt: now, Result: &environments.OperationResult{ExitCode: code, Stdout: stdout, Stderr: "error detail\n"}})
		if err != nil { t.Fatal(err) }
		for _, limit := range []int{0, 16, 4096} {
			out, err := execTool(t, h, "manage_environments", map[string]any{"action": "get_operation", "operation_id": id, "max_output": limit})
			if err != nil { t.Fatal(err) }
			var receipt struct { Operation environments.EnvironmentOperation `json:"operation"` }
			if err := json.Unmarshal([]byte(out), &receipt); err != nil { t.Fatal(err) }
			result := receipt.Operation.Result
			if result.ExitCode != code || result.Stderr != "error detail\n" { t.Fatalf("diagnostics missing: %+v", result) }
			if limit == 16 {
				if len(result.Stdout) != 16 || !result.Truncated { t.Fatal("read cap not enforced") }
			} else if result.Stdout != stdout || result.Truncated { t.Fatal("final assertion hidden behind summary") }
		}
		stored, found, err := store.Get("test-account", "ws-test", id)
		if err != nil || !found || stored.Result.Stdout != stdout || stored.Result.Truncated { t.Fatal("receipt read mutated durable output") }
		if _, err := execTool(t, h, "manage_environments", map[string]any{"action": "get_operation", "operation_id": id, "workspace_id": "foreign"}); err == nil { t.Fatal("foreign workspace output disclosed") }
	}
}
