package pebblestore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenSecuresExistingDatabaseDirectories(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "db")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	for _, candidate := range []string{parent, path} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("mode(%s) = %o, want 700", candidate, got)
		}
	}
}

func TestOpenRejectsSymlinkDatabasePath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); err == nil {
		t.Fatal("Open succeeded for symlink database path")
	}
}
