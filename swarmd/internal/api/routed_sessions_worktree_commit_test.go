package api

import (
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func TestApplySessionCreateWorktreeAllocationRecordsBaseCommit(t *testing.T) {
	options := sessionruntime.CreateSessionOptions{Metadata: map[string]any{"source": "test"}}
	applySessionCreateWorktreeAllocation(&options, worktreeruntime.Allocation{
		WorkspacePath: "/managed/worktree",
		BaseBranch:    "dev",
		BaseCommit:    "base-commit-oid",
		BranchName:    "agent/session",
		WorkspaceID:   "workspace-id",
	})

	if options.WorkspacePath != "/managed/worktree" || options.Worktree == nil {
		t.Fatalf("worktree allocation = %+v", options)
	}
	if got := options.Metadata["base_commit"]; got != "base-commit-oid" {
		t.Fatalf("base_commit = %v", got)
	}
	if options.Metadata["source"] != "test" {
		t.Fatalf("existing metadata was not preserved: %+v", options.Metadata)
	}
}
