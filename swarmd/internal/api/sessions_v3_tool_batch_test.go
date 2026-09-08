package api

import (
	"errors"
	"os"
	"path/filepath"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"testing"
)

// Purpose: the actual V3 executor batch helper must stop after restart, before a
// later write can use stale workspace authority. Assert filesystem postconditions
// and exact executed results, including error interruption and normal batches.
func TestSessionV3ToolBatchRestartBoundary(t *testing.T) {
	for _, mode := range []string{"restart", "error", "normal"} {
		t.Run(mode, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "must-not-write")
			calls := []provideriface.FunctionCall{{CallID: "switch", Name: "manage_workspace"}, {CallID: "write", Name: "write"}}
			seen := 0
			sentinel := errors.New("injected failure")
			results, restart, waited, err := executeSessionV3ToolBatch(calls, func(call provideriface.FunctionCall) (provideriface.ToolExecutionResult, error) {
				seen++
				if call.CallID == "switch" {
					if mode == "error" {
						return provideriface.ToolExecutionResult{}, sentinel
					}
					return provideriface.ToolExecutionResult{CallID: call.CallID, RestartTurn: mode == "restart", PermissionWaitMS: 1}, nil
				}
				return provideriface.ToolExecutionResult{CallID: call.CallID}, os.WriteFile(marker, []byte("executed"), 0600)
			})
			if mode == "normal" {
				if err != nil || restart || !waited || len(results) != 2 || seen != 2 {
					t.Fatalf("normal: %+v %v", results, err)
				}
				return
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatal("later stale call mutated filesystem")
			}
			if seen != 1 {
				t.Fatal("stale suffix invoked")
			}
			if mode == "restart" {
				if err != nil || !restart || !waited || len(results) != 1 || results[0].CallID != "switch" {
					t.Fatal("restart prefix lost")
				}
			}
			if mode == "error" && (!errors.Is(err, sentinel) || restart || len(results) != 0) {
				t.Fatal("error swallowed")
			}
		})
	}
}
