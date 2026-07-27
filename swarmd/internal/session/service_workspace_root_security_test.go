package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeSessionWorkspaceRootRejectsUnsafeRoots(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	symlink := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(directory, symlink); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	for _, tc := range []struct {
		name string
		root string
		want string
	}{
		{name: "relative", root: "relative", want: "must be absolute"},
		{name: "noncanonical", root: filepath.Join(directory, "..", filepath.Base(directory)), want: "must be canonical"},
		{name: "missing", root: filepath.Join(t.TempDir(), "missing"), want: "stat workspace root"},
		{name: "file", root: file, want: "must be a directory"},
		{name: "symlink", root: symlink, want: "must not be a symlink"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeSessionWorkspaceRoot(tc.root)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("normalizeSessionWorkspaceRoot(%q) error = %v, want %q", tc.root, err, tc.want)
			}
		})
	}

	got, err := normalizeSessionWorkspaceRoot(directory)
	if err != nil {
		t.Fatalf("normalize existing directory: %v", err)
	}
	if got != directory {
		t.Fatalf("normalized root = %q, want %q", got, directory)
	}
}
