package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Purpose: Runtime.executeOne is the real Bash argument dispatch boundary.
// Reviewer workspace overrides must fail before catalog recovery or any process
// runs; even an authorized source is not the reviewer's command lane.
func TestReviewerBashOverrideRefused(t *testing.T) {
	source, checkout := t.TempDir(), t.TempDir()
	args, _ := json.Marshal(map[string]any{"command": "touch forbidden", "workspace_path": source, "category": "write", "explanation": []string{"Attempt outside write."}})
	_, err := (&Runtime{}).executeOne(context.Background(), WorkspaceScope{SessionID: "review", ReviewCheckout: checkout, PrimaryPath: checkout, Roots: []string{checkout}}, Call{Name: "bash", Arguments: string(args)}, nil)
	if err == nil || !strings.Contains(err.Error(), "retargeting is forbidden") {
		t.Fatal(err)
	}
}
