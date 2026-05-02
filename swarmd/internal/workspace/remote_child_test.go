package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func withRemoteChildWorkspaceRootPath(t *testing.T, path string) {
	t.Helper()
	previous := remoteChildWorkspaceRootPath
	remoteChildWorkspaceRootPath = path
	t.Cleanup(func() {
		remoteChildWorkspaceRootPath = previous
	})
}

func TestRemoteChildWorkspaceRootUsesWorkspacesWhenPresent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir workspaces root: %v", err)
	}
	withRemoteChildWorkspaceRootPath(t, root)

	detected, ok := remoteChildWorkspaceRoot()
	if !ok {
		t.Fatal("expected workspaces root to be detected")
	}
	if detected != root {
		t.Fatalf("root = %q, want %q", detected, root)
	}

	home, err := resolveBrowseHomePath()
	if err != nil {
		t.Fatalf("resolveBrowseHomePath: %v", err)
	}
	if home != root {
		t.Fatalf("browse home = %q, want %q", home, root)
	}
}

func TestWorkspaceDiscoverRootsIncludesWorkspacesWhenPresent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir workspaces root: %v", err)
	}
	withRemoteChildWorkspaceRootPath(t, root)

	roots := workspaceDiscoverRoots(nil)
	if len(roots) == 0 || roots[0] != root {
		t.Fatalf("roots = %#v, want first root %q", roots, root)
	}
}

func TestWorkspaceDiscoverRootsHonorsExplicitRoots(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	if err := os.Mkdir(workspaceRoot, 0o755); err != nil {
		t.Fatalf("mkdir workspaces root: %v", err)
	}
	withRemoteChildWorkspaceRootPath(t, workspaceRoot)

	root := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	roots := workspaceDiscoverRoots([]string{root})
	if len(roots) != 1 || roots[0] != root {
		t.Fatalf("roots = %#v, want only explicit root %q", roots, root)
	}
}
