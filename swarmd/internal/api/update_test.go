package api

import (
	"os"
	"path/filepath"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

func TestConfiguredDevRootUsesStartupConfigDevRoot(t *testing.T) {
	root := makeUpdateDevRoot(t)
	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.DevMode = true
	cfg.DevRoot = root
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("Write startup config: %v", err)
	}
	server := &Server{startupConfigPath: startupPath}
	got, err := server.configuredDevRoot()
	if err != nil {
		t.Fatalf("configuredDevRoot() error = %v", err)
	}
	if got != root {
		t.Fatalf("configuredDevRoot() = %q, want %q", got, root)
	}
}

func TestConfiguredDevRootRequiresConfiguredDevRoot(t *testing.T) {
	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.DevMode = true
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("Write startup config: %v", err)
	}
	server := &Server{startupConfigPath: startupPath}
	if _, err := server.configuredDevRoot(); err == nil {
		t.Fatal("configuredDevRoot() error = nil, want missing dev_root error")
	}
}

func TestDesktopUpdateKindUsesStartupConfigDevMode(t *testing.T) {
	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.DevMode = true
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("Write startup config: %v", err)
	}
	server := &Server{startupConfigPath: startupPath}
	got, err := server.desktopUpdateKind()
	if err != nil {
		t.Fatalf("desktopUpdateKind() error = %v", err)
	}
	if got != updateKindDev {
		t.Fatalf("desktopUpdateKind() = %q, want %q", got, updateKindDev)
	}

	cfg.DevMode = false
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("Write startup config: %v", err)
	}
	got, err = server.desktopUpdateKind()
	if err != nil {
		t.Fatalf("desktopUpdateKind() release error = %v", err)
	}
	if got != updateKindRelease {
		t.Fatalf("desktopUpdateKind() = %q, want %q", got, updateKindRelease)
	}
}

func TestUpdateLaneForKindUsesCurrentLane(t *testing.T) {
	t.Setenv("SWARM_LANE", "dev")
	if got := updateLaneForKind(updateKindDev); got != "dev" {
		t.Fatalf("updateLaneForKind(dev) = %q, want dev", got)
	}
	t.Setenv("SWARM_LANE", "main")
	if got := updateLaneForKind(updateKindDev); got != "main" {
		t.Fatalf("updateLaneForKind(dev on main lane) = %q, want main", got)
	}
	t.Setenv("SWARM_LANE", "")
	if got := updateLaneForKind(updateKindRelease); got != "main" {
		t.Fatalf("updateLaneForKind(release) = %q, want main", got)
	}
}

func makeUpdateDevRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	files := []string{
		filepath.Join(root, "scripts", "rebuild-container.sh"),
		filepath.Join(root, "deploy", "container-mvp", "Containerfile.base"),
		filepath.Join(root, "deploy", "container-mvp", "Containerfile"),
		filepath.Join(root, "deploy", "container-mvp", "entrypoint.sh"),
	}
	for _, path := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("test\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", path, err)
		}
	}
	return filepath.Clean(root)
}
