package tool

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGenericFilesystemToolsRejectSymlinkEscapes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on some Windows hosts")
	}
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	scope := normalizeWorkspaceScope(workspace, nil)

	for name, call := range map[string]func() error{
		"read": func() error {
			_, err := executeRead(scope, map[string]any{"path": "escape/secret.txt"})
			return err
		},
		"write": func() error {
			_, err := executeWrite(scope, map[string]any{"path": "escape/new.txt", "content": "written"})
			return err
		},
		"edit": func() error {
			_, err := executeEdit(scope, map[string]any{"path": "escape/secret.txt", "old_string": "secret", "new_string": "changed"})
			return err
		},
		"list": func() error {
			_, err := executeList(scope, map[string]any{"path": "escape"})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatalf("%s unexpectedly followed a symlink outside the workspace", name)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("write escaped workspace: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(outside, "secret.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "secret" {
		t.Fatalf("outside file changed to %q", content)
	}
}

func TestGenericFilesystemWriteSupportsMissingInRootParents(t *testing.T) {
	workspace := t.TempDir()
	scope := normalizeWorkspaceScope(workspace, nil)
	if _, err := executeWrite(scope, map[string]any{
		"path":    filepath.Join("missing", "descendant", "file.txt"),
		"content": "ok",
	}); err != nil {
		t.Fatalf("write missing descendant: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "missing", "descendant", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "ok" {
		t.Fatalf("content = %q, want ok", content)
	}
}

func TestGenericFilesystemMutationRejectsHardlinks(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	outsidePath := filepath.Join(outside, "shared.txt")
	if err := os.WriteFile(outsidePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	insidePath := filepath.Join(workspace, "shared.txt")
	if err := os.Link(outsidePath, insidePath); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	scope := normalizeWorkspaceScope(workspace, nil)

	if _, err := executeWrite(scope, map[string]any{"path": insidePath, "content": "changed"}); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("write error = %v, want hard-link rejection", err)
	}
	if _, err := executeEdit(scope, map[string]any{"path": insidePath, "old_string": "original", "new_string": "changed"}); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("edit error = %v, want hard-link rejection", err)
	}
	content, err := os.ReadFile(outsidePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original" {
		t.Fatalf("outside hardlink changed to %q", content)
	}
}

func TestGenericFilesystemUsesExplicitAdditionalRoot(t *testing.T) {
	workspace := t.TempDir()
	additional := t.TempDir()
	scope := normalizeWorkspaceScope(workspace, []string{additional})
	path := filepath.Join(additional, "allowed.txt")
	if _, err := executeWrite(scope, map[string]any{"path": path, "content": "allowed"}); err != nil {
		t.Fatalf("write additional root: %v", err)
	}
	if _, err := executeRead(scope, map[string]any{"path": path}); err != nil {
		t.Fatalf("read additional root: %v", err)
	}
}
