package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsurePrivateDirectoryTightensExistingMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(path); err != nil {
		t.Fatalf("ensurePrivateDirectory: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("mode = %o, want 700", got)
	}
}

func TestEnsurePrivateDirectoryRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "data")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(link); err == nil {
		t.Fatal("ensurePrivateDirectory succeeded for symlink")
	}
}
