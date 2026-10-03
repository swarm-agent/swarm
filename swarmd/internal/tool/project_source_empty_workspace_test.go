package tool

import (
	"strings"
	"testing"
)

// Purpose: openRootedWorkspacePath must reject cwd-free filesystem access without
// implying missing project identity. This boundary test proves no root is opened
// and points callers to authorized source inspection rather than cwd fallback.
func TestProjectSourceEmptyWorkspaceError(t *testing.T) {
	root, err := openRootedWorkspacePath(WorkspaceScope{}, t.TempDir())
	if root != nil || err == nil || !strings.Contains(err.Error(), "inspect_source") {
		t.Fatalf("unexpected cwd-free access: %v %v", root, err)
	}
}
