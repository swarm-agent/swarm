package run

import (
	"encoding/json"
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Requirement: ordinary tool history is correlated with the authoritative run,
// not provider-supplied metadata. This emitted-envelope test prevents forged
// metadata from replacing run identity; it does not claim provider execution.
func TestToolHistoryAuthoritativeRunIdentity(t *testing.T) {
	call := tool.Call{CallID: "call", Name: "manage_artifact", Arguments: `{"action":"create"}`}
	result := tool.Result{CallID: "call", Name: "manage_artifact", Output: `{"status":"fixing"}`}
	var record toolHistoryRecord
	if err := json.Unmarshal([]byte(formatToolHistoryForRun(call, map[string]any{"run_id": "forged"}, result, "owned-run")), &record); err != nil {
		t.Fatal(err)
	}
	if record.RunID != "owned-run" || record.CallID != "call" || record.Output != result.Output {
		t.Fatalf("incorrect history: %+v", record)
	}
	if err := json.Unmarshal([]byte(formatToolHistoryForRun(call, map[string]any{"bad": make(chan int)}, result, "owned-run")), &record); err != nil || record.RunID != "owned-run" {
		t.Fatalf("fallback lost identity: %+v %v", record, err)
	}
}
