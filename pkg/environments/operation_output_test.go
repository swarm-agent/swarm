package environments

import (
	"strings"
	"testing"
)

// Purpose: EnvironmentOperation.Validate is the persistence boundary for exec
// diagnostics. It must accept the per-stream cap and reject oversized/invalid
// UTF-8 output without altering valid output; domain unit tests are sufficient.
func TestEnvironmentOperation_OutputBounds(t *testing.T) {
	base := EnvironmentOperation{OperationID: "op_output", AccountScopeID: "account", WorkspaceID: "workspace", Action: "exec", Status: OperationStatusSucceeded, CreatedAt: 1000, Deadline: 2000}
	for _, stream := range []string{"stdout", "stderr"} {
		for _, value := range []string{strings.Repeat("x", MaxOperationOutputBytes), strings.Repeat("x", MaxOperationOutputBytes+1), string([]byte{0xff})} {
			op := base
			if stream == "stdout" {
				op.Result.Stdout = value
			} else {
				op.Result.Stderr = value
			}
			err := op.Validate()
			if len(value) == MaxOperationOutputBytes {
				if err != nil {
					t.Fatalf("cap rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("unsafe output accepted")
			}
			if op.Result.Stdout+op.Result.Stderr != value {
				t.Fatal("validation mutated diagnostics")
			}
		}
	}
}
