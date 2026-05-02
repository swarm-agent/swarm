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

func TestListKnownRegistersMountedRemoteChildWorkspaces(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	project := filepath.Join(workspaceRoot, "swarm")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir mounted workspace: %v", err)
	}
	withRemoteChildWorkspaceRootPath(t, workspaceRoot)

	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)

	entries, err := svc.ListKnown(200)
	if err != nil {
		t.Fatalf("ListKnown: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1: %#v", len(entries), entries)
	}
	if entries[0].Path != project {
		t.Fatalf("entry path = %q, want %q", entries[0].Path, project)
	}
	if entries[0].WorkspaceName != "swarm" {
		t.Fatalf("entry name = %q, want swarm", entries[0].WorkspaceName)
	}
}
