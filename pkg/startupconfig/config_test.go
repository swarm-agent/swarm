package startupconfig

import (
	"path/filepath"
	"testing"
)

func TestResolvePathUsesExplicitStartupConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swarm.conf")
	t.Setenv("SWARM_STARTUP_CONFIG", path)

	got, err := ResolvePath()
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if got != path {
		t.Fatalf("ResolvePath() = %q, want %q", got, path)
	}
}

func TestResolvePathRejectsRelativeStartupConfig(t *testing.T) {
	t.Setenv("SWARM_STARTUP_CONFIG", "relative/swarm.conf")

	if _, err := ResolvePath(); err == nil {
		t.Fatal("ResolvePath accepted relative SWARM_STARTUP_CONFIG")
	}
}

func TestResolvePathRejectsHomeRelativeStartupConfig(t *testing.T) {
	t.Setenv("SWARM_STARTUP_CONFIG", "~/swarm.conf")

	if _, err := ResolvePath(); err == nil {
		t.Fatal("ResolvePath accepted home-relative SWARM_STARTUP_CONFIG")
	}
}
