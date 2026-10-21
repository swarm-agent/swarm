package startupconfig

import (
	"os"
	"path/filepath"

	"testing"
)

func TestWritePreservesIdentityEnforcesPrivateModeAndRejectsSymlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := Default(path)
	if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Write(cfg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(cfg); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("Write replaced the existing startup config instead of preserving its identity")
	}

	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "swarm.conf")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	cfg.Path = link
	if err := Write(cfg); err == nil {
		t.Fatal("Write succeeded for symlink config")
	}
	payload, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "unchanged" {
		t.Fatalf("symlink target changed: %q", payload)
	}
}
