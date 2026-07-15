package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

func withRemoteChildWorkspaceRootPath(t *testing.T, path string) {
	t.Helper()
	previous := remoteChildWorkspaceRootPath
	remoteChildWorkspaceRootPath = path
	t.Cleanup(func() {
		remoteChildWorkspaceRootPath = previous
	})
}

func explicitChildContainerStartupConfig() startupconfig.FileConfig {
	cfg := startupconfig.Default("")
	cfg.Child = true
	return cfg
}

func plainLaptopStartupConfig() startupconfig.FileConfig {
	cfg := startupconfig.Default("")
	cfg.Child = false
	return cfg
}

func TestRemoteChildWorkspaceRootDetectsWorkspacesWhenPresent(t *testing.T) {
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
}

func TestPlainLaptopBrowseHomeIgnoresWorkspacesDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir workspaces root: %v", err)
	}
	withRemoteChildWorkspaceRootPath(t, root)

	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	svc.SetStartupConfigForTesting(plainLaptopStartupConfig())

	home, err := svc.resolveBrowseHomePath()
	if err != nil {
		t.Fatalf("resolveBrowseHomePath: %v", err)
	}
	if home == root {
		t.Fatalf("browse home = %q, want user home instead of workspaces root", home)
	}
	wantHome, err := os.UserHomeDir()
	if err != nil || wantHome == "" {
		t.Fatalf("os.UserHomeDir: %q %v", wantHome, err)
	}
	wantHome, err = resolvePath(wantHome)
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	if home != wantHome {
		t.Fatalf("browse home = %q, want %q", home, wantHome)
	}
}

func TestExplicitChildContainerBrowseHomeUsesWorkspaces(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir workspaces root: %v", err)
	}
	withRemoteChildWorkspaceRootPath(t, root)

	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	svc.SetStartupConfigForTesting(explicitChildContainerStartupConfig())

	home, err := svc.resolveBrowseHomePath()
	if err != nil {
		t.Fatalf("resolveBrowseHomePath: %v", err)
	}
	if home != root {
		t.Fatalf("browse home = %q, want %q", home, root)
	}
}

func TestPlainLaptopWorkspaceDiscoverRootsIgnoresWorkspacesDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir workspaces root: %v", err)
	}
	withRemoteChildWorkspaceRootPath(t, root)

	roots := workspaceDiscoverRoots(nil)
	for _, got := range roots {
		if got == root {
			t.Fatalf("roots = %#v, should not include plain /workspaces root %q", roots, root)
		}
	}
}

func TestExplicitChildContainerDiscoverIncludesWorkspaces(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	project := filepath.Join(root, "swarm")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir mounted git workspace: %v", err)
	}
	withRemoteChildWorkspaceRootPath(t, root)

	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	svc.SetStartupConfigForTesting(explicitChildContainerStartupConfig())

	entries, err := svc.Discover(nil, 200)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, entry := range entries {
		if entry.Path == project {
			return
		}
	}
	t.Fatalf("Discover entries = %#v, want project %q", entries, project)
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

func TestPlainLaptopListKnownDoesNotRegisterMountedWorkspaces(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	project := filepath.Join(workspaceRoot, "swarm")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir mounted workspace: %v", err)
	}
	withRemoteChildWorkspaceRootPath(t, workspaceRoot)

	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	svc.SetStartupConfigForTesting(plainLaptopStartupConfig())

	entries, err := svc.ListKnownForPrincipal(testPrincipal(), 200)
	if err != nil {
		t.Fatalf("ListKnown: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %d, want 0: %#v", len(entries), entries)
	}
}

func TestExplicitChildContainerListKnownRegistersMountedWorkspaces(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	project := filepath.Join(workspaceRoot, "swarm")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir mounted workspace: %v", err)
	}
	withRemoteChildWorkspaceRootPath(t, workspaceRoot)

	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	svc.SetStartupConfigForTesting(explicitChildContainerStartupConfig())

	entries, err := svc.ListKnownForPrincipal(testPrincipal(), 200)
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
