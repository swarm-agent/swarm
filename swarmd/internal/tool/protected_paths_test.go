package tool

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Purpose: file tools run inside the daemon and are not sandboxed, so they
// must refuse Swarm's storage roots and the daemon user's credential paths
// outright: through symlinks, through a broad workspace grant that contains
// storage, and without ever offering a scope-expansion prompt for them. The
// owning boundary is openRootedWorkspacePath / resolveWorkspacePath /
// scopeExpansionForPath with SetProtectedPaths. Unit level with real files:
// the decision is pure path authority and needs no daemon or permission
// service; refusal must hold regardless of any permission bypass because it
// happens inside the tool after authorization.
func TestFileToolsRefuseProtectedPaths(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	storage := filepath.Join(base, "var", "lib", "swarmd")
	creds := filepath.Join(base, "home", ".ssh")
	workspace := filepath.Join(base, "home", "project")
	for _, dir := range []string{storage, creds, workspace} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(storage, "swarmd-secrets.pebble.key")
	if err := os.WriteFile(secret, []byte("root-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(creds, "id_ed25519"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(workspace, "innocent.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "ok.txt"), []byte("fine"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := protectedPaths.Load()
	t.Cleanup(func() { protectedPaths.Store(previous) })
	SetProtectedPaths([]string{storage}, []string{creds})

	scope := normalizeWorkspaceScope(workspace, []string{workspace})
	if _, err := executeRead(scope, map[string]any{"path": "innocent.txt"}); !errors.Is(err, ErrProtectedPath) {
		t.Fatalf("symlink into storage: want ErrProtectedPath, got %v", err)
	}
	if out, err := executeRead(scope, map[string]any{"path": "ok.txt"}); err != nil {
		t.Fatalf("ordinary read refused: %v %s", err, out)
	}

	// A broad grant that contains storage (as a mistaken workspace or an
	// approved scope expansion would) still cannot reach it.
	broad := normalizeWorkspaceScope(workspace, []string{workspace, filepath.Join(base, "var"), filepath.Join(base, "home")})
	if _, err := executeRead(broad, map[string]any{"path": secret}); !errors.Is(err, ErrProtectedPath) {
		t.Fatalf("read through broad grant: want ErrProtectedPath, got %v", err)
	}
	if _, err := executeWrite(broad, map[string]any{"path": filepath.Join(storage, "planted"), "content": "x"}); !errors.Is(err, ErrProtectedPath) {
		t.Fatalf("write into storage: want ErrProtectedPath, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(storage, "planted")); !os.IsNotExist(err) {
		t.Fatalf("refused write left a file: %v", err)
	}
	if _, err := executeRead(broad, map[string]any{"path": filepath.Join(creds, "id_ed25519")}); !errors.Is(err, ErrProtectedPath) {
		t.Fatalf("read of credentials: want ErrProtectedPath, got %v", err)
	}
	if _, err := executeList(broad, map[string]any{"path": filepath.Join(base, "var")}); !errors.Is(err, ErrProtectedPath) {
		t.Fatalf("listing a directory containing storage: want ErrProtectedPath, got %v", err)
	}
	if _, err := resolveWorkspacePath(broad, filepath.Join(base, "var")); !errors.Is(err, ErrProtectedPath) {
		t.Fatalf("search root containing storage: want ErrProtectedPath, got %v", err)
	}

	// The user is never asked to grant protected paths.
	for _, target := range []string{secret, storage, filepath.Join(base, "var"), filepath.Join(creds, "id_ed25519")} {
		request, ok, err := scopeExpansionForPath(scope, "read", "path", target)
		if ok || !errors.Is(err, ErrWorkspaceScopeExpansionRejected) || !errors.Is(err, ErrProtectedPath) {
			t.Fatalf("scope expansion for %s: request=%+v ok=%v err=%v", target, request, ok, err)
		}
	}
}
