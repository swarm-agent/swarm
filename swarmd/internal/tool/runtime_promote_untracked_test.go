package tool_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Purpose: manageWorktreePromote must defer overwrite safety to
// PrepareTaskIntegration, not reject unrelated untracked files. The existing
// real-Git lineage fixture proves target ancestry and byte preservation at the
// native tool boundary rather than mocking the integration service.
func TestManageWorktreePromotePreservesUntracked(t *testing.T) {
	f := newCommittedValidationFixture(t)
	path := filepath.Join(f.source, "local-note")
	validationWrite(t, path, "keep\n")
	args, _ := json.Marshal(map[string]any{"action": "promote", "source_session_id": f.request.ChildSessionID, "target_branch": "dev", "target_head": f.base})
	_, err := f.runtime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), f.scope, tool.Call{Name: "manage_worktree", Arguments: string(args)})
	if err != nil {
		t.Fatal(err)
	}
	validationGit(t, f.source, "merge-base", "--is-ancestor", f.head, "HEAD")
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "keep\n" {
		t.Fatalf("untracked changed: %q %v", got, err)
	}
}
