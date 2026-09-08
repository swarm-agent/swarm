package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Purpose: providerToolInvoker must reject calls from an invalidated step before
// dispatch, independently of adapter cooperation. A failed tool output cannot
// manufacture a restart. These are the narrow per-step authority boundaries.
func TestProviderWorkspaceRestartRejectsStaleInvoker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "untouched")
	invoker := &providerToolInvoker{service: &Service{}, restartRequired: true}
	_, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{Name: "write", Arguments: mustJSON(t, map[string]any{"path": path, "content": "bad"})})
	if err == nil || !strings.Contains(err.Error(), "requires restart") {
		t.Fatalf("stale invoker: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("stale invocation changed filesystem")
	}
	call := tool.Call{Name: "manage_workspace"}
	if !providerManagedToolRequiresTurnRestart(call, tool.Result{Output: `{"restart_turn":true}`}) {
		t.Fatal("successful restart ignored")
	}
	if providerManagedToolRequiresTurnRestart(call, tool.Result{Output: `{"restart_turn":true}`, Error: "mutation rejected"}) {
		t.Fatal("failed mutation requested restart")
	}
}
