package startupconfig_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

func TestWriteAndLoad_PersistsSwarmMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(path)
	cfg.SwarmName = "my-device"
	cfg.SwarmMode = true
	cfg.DevMode = true
	cfg.DevRoot = filepath.Clean(filepath.Join(t.TempDir(), "repo"))
	cfg.Child = true
	cfg.NetworkMode = startupconfig.NetworkModeTailscale
	cfg.TailscaleURL = "https://my-device.example.ts.net"

	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	text := string(data)
	if !strings.Contains(text, "swarm_mode = true") {
		t.Fatalf("startup config missing swarm_mode=true: %q", text)
	}
	if !strings.Contains(text, "dev_mode = true") {
		t.Fatalf("startup config missing dev_mode=true: %q", text)
	}
	if !strings.Contains(text, "dev_root = "+cfg.DevRoot) {
		t.Fatalf("startup config missing dev_root=%q: %q", cfg.DevRoot, text)
	}

	loaded, err := startupconfig.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !loaded.SwarmMode {
		t.Fatal("loaded.SwarmMode = false, want true")
	}
	if !loaded.DevMode {
		t.Fatal("loaded.DevMode = false, want true")
	}
	if loaded.DevRoot != cfg.DevRoot {
		t.Fatalf("loaded.DevRoot = %q, want %q", loaded.DevRoot, cfg.DevRoot)
	}
	if !loaded.Child {
		t.Fatal("loaded.Child = false, want true")
	}
	if loaded.TailscaleURL != cfg.TailscaleURL {
		t.Fatalf("loaded.TailscaleURL = %q, want %q", loaded.TailscaleURL, cfg.TailscaleURL)
	}
}

func TestDefault_SwarmModeDisabled(t *testing.T) {
	cfg := startupconfig.Default(filepath.Join(t.TempDir(), "swarm.conf"))
	if cfg.SwarmMode {
		t.Fatal("Default().SwarmMode = true, want false")
	}
	if cfg.DevMode {
		t.Fatal("Default().DevMode = true, want false")
	}
	if cfg.DevRoot != "" {
		t.Fatalf("Default().DevRoot = %q, want empty", cfg.DevRoot)
	}
}
