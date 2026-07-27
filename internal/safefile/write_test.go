package safefile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFilePreservesIdentityAndRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "private.json")
	if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("WriteFile replaced the existing file instead of updating it in place")
	}
	if got := after.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "new\n" {
		t.Fatalf("contents = %q, err = %v", got, err)
	}

	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(link, []byte("changed"), 0o600); err == nil {
		t.Fatal("WriteFile succeeded for symlink")
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "unchanged" {
		t.Fatalf("symlink target contents = %q, err = %v", got, err)
	}
}

func TestWriteFileCreatesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.json")
	if err := WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}
