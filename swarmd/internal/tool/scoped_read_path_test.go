package tool

import (
	"os"
	"path/filepath"
	"testing"
)

// Purpose: ResolveExistingReadPath lets the permission policy approve a
// read-only shell command without asking only when the command can read
// nothing the read tool could not. It must follow every symlink and accept
// only existing regular files or directories inside the scope's read roots
// (primary, additional and read-only roots). Threats: in-workspace symlinks
// (file or directory) to outside, ../ escapes, absolute outside paths,
// unresolvable paths, kernel pseudo files and devices even under a broad
// grant, protected Swarm storage/credentials, and an empty scope. Unit level
// with real files: the decision is pure path authority.
func TestResolveExistingReadPathContainment(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(base, "workspace")
	readOnly := filepath.Join(base, "source")
	outside := filepath.Join(base, "outside")
	storage := filepath.Join(base, "storage")
	for _, dir := range []string{filepath.Join(workspace, "src"), readOnly, outside, storage} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(workspace, "src", "a.go"), filepath.Join(readOnly, "b.go"), filepath.Join(outside, "c.txt"), filepath.Join(storage, "key")} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]string{
		"dir-out":    outside,
		"file-out":   filepath.Join(outside, "c.txt"),
		"file-in":    filepath.Join(workspace, "src", "a.go"),
		"storage-in": filepath.Join(storage, "key"),
		"dangling":   filepath.Join(base, "missing"),
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(workspace, name)); err != nil {
			t.Fatal(err)
		}
	}
	previous := protectedPaths.Load()
	t.Cleanup(func() { protectedPaths.Store(previous) })
	SetProtectedPaths([]string{storage}, nil)

	scope := WorkspaceScope{PrimaryPath: workspace, Roots: []string{workspace}, ReadOnlyRoots: []string{readOnly}}
	for requested, want := range map[string]string{
		".":                             workspace,
		"src":                           filepath.Join(workspace, "src"),
		"src/a.go":                      filepath.Join(workspace, "src", "a.go"),
		"file-in":                       filepath.Join(workspace, "src", "a.go"),
		"src/../src/a.go":               filepath.Join(workspace, "src", "a.go"),
		filepath.Join(readOnly, "b.go"): filepath.Join(readOnly, "b.go"),
	} {
		got, err := ResolveExistingReadPath(scope, requested)
		if err != nil || got != want {
			t.Fatalf("%q = %q, %v; want %q", requested, got, err, want)
		}
	}
	for _, requested := range []string{
		"dir-out", "dir-out/c.txt", "file-out", "dangling", "missing.txt", "../outside/c.txt", "..",
		filepath.Join(outside, "c.txt"), "storage-in", filepath.Join(storage, "key"), "/proc/self/environ", "/dev/null", "",
	} {
		if got, err := ResolveExistingReadPath(scope, requested); err == nil {
			t.Fatalf("%q resolved to %q, want refusal", requested, got)
		}
	}
	broad := WorkspaceScope{PrimaryPath: workspace, Roots: []string{workspace, "/"}}
	for _, requested := range []string{"/proc/self/environ", "/proc/1/cmdline", "/dev/zero", "/sys/kernel", filepath.Join(storage, "key")} {
		if got, err := ResolveExistingReadPath(broad, requested); err == nil {
			t.Fatalf("broad grant: %q resolved to %q, want refusal", requested, got)
		}
	}
	if _, err := ResolveExistingReadPath(WorkspaceScope{}, "src"); err == nil {
		t.Fatal("empty scope resolved a path")
	}
}
