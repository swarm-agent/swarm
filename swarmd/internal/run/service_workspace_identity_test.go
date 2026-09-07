package run

import (
	"fmt"
	"testing"

	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Purpose: exact attachment parsing must preserve omitted versus empty intent,
// reject malformed/oversized input before mutation, and never evict provenance.
// Pure helpers are the narrowest layer for these resource/intent invariants.
func TestWorkspaceAttachmentBoundsAndHistory(t *testing.T) {
	omitted, err := parseManageWorkspaceArguments(`{"action":"set_session","workspace_id":"one"}`)
	if err != nil || omitted.WorkspaceIDs != nil {
		t.Fatal("omitted attachment intent lost")
	}
	empty, err := parseManageWorkspaceArguments(`{"action":"set_session","workspace_id":"one","workspace_ids":[]}`)
	if err != nil || empty.WorkspaceIDs == nil {
		t.Fatal("explicit empty intent lost")
	}
	ids := make([]string, 65)
	for i := range ids {
		ids[i] = fmt.Sprintf("workspace-%d", i)
	}
	if _, err := parseManageWorkspaceArguments(mustJSON(t, map[string]any{"action": "set_session", "workspace_ids": ids})); err == nil {
		t.Fatal("oversized attachments accepted")
	}
	var history []any
	for i := 0; i < 64; i++ {
		history = appendSessionWorktreeHistory(history, SessionWorkspaceCanonicalization{WorkspaceID: ids[i], WorkspaceGeneration: 1}, worktreeruntime.Allocation{WorkspacePath: fmt.Sprintf("/fixture/lane-%d", i), BranchName: fmt.Sprintf("agent/%d", i)}, "owner")
	}
	if len(history) != 64 || mapString(history[0].(map[string]any), "workspace_id") != ids[0] {
		t.Fatal("historical lineage evicted")
	}
}
