package startupconfig_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

func TestWriteAndLoad_OmitsLegacyModeAndPersistsExplicitState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(path)
	cfg.SwarmName = "my-device"
	cfg.DevMode = true
	cfg.DevRoot = filepath.Clean(filepath.Join(t.TempDir(), "repo"))
	cfg.Child = true
	cfg.TailscaleURL = "https://my-device.example.ts.net"

	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("startup config mode = %#o, want 0o600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	text := string(data)
	if strings.Contains(text, "startup"+"_mode") {
		t.Fatalf("startup config should not include startup mode key: %q", text)
	}
	if strings.Contains(text, "swarm"+"_mode") {
		t.Fatalf("startup config should not include legacy mode key: %q", text)
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

func TestResolvePath_DefaultsToSystemConfigRoot(t *testing.T) {
	home := "/test-home/startupconfig-test-user"
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("CONFIGURATION_DIRECTORY", "")

	path, err := startupconfig.ResolvePath()
	if err != nil {
		t.Fatalf("ResolvePath() error = %v", err)
	}
	if path != "/etc/swarmd/swarm.conf" {
		t.Fatalf("ResolvePath() = %q, want /etc/swarmd/swarm.conf", path)
	}
	forbiddenPrefixes := []string{
		home,
		filepath.Join(home, ".config"),
		filepath.Join(home, ".local", "share"),
		filepath.Join(home, ".cache"),
		filepath.Join(home, ".local", "state"),
	}
	for _, prefix := range forbiddenPrefixes {
		if strings.HasPrefix(path, prefix) {
			t.Fatalf("ResolvePath() = %q under forbidden prefix %q", path, prefix)
		}
	}
}

func TestResolvePath_UsesValidatedSystemdConfigurationDirectory(t *testing.T) {
	home := "/test-home/startupconfig-test-user"
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CONFIGURATION_DIRECTORY", "/etc/swarmd-test")

	path, err := startupconfig.ResolvePath()
	if err != nil {
		t.Fatalf("ResolvePath() error = %v", err)
	}
	if path != "/etc/swarmd-test/swarm.conf" {
		t.Fatalf("ResolvePath() = %q, want /etc/swarmd-test/swarm.conf", path)
	}
}

func TestResolvePath_RejectsForbiddenConfigurationDirectory(t *testing.T) {
	home := "/test-home/startupconfig-test-user"
	t.Setenv("HOME", home)
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(home, ".config", "swarm"))

	_, err := startupconfig.ResolvePath()
	if err == nil {
		t.Fatal("ResolvePath() succeeded with forbidden CONFIGURATION_DIRECTORY")
	}
	if !strings.Contains(err.Error(), "forbidden root") {
		t.Fatalf("ResolvePath() error = %v, want forbidden root", err)
	}
}

func TestLoad_IgnoresLegacyStartupMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swarm.conf")
	text := strings.Join([]string{
		"dev_mode = false",
		"dev_root = ",
		"host = 127.0.0.1",
		"port = 7781",
		"advertise_host = ",
		"advertise_port = 7781",
		"desktop_port = 5555",
		"bypass_permissions = false",
		"retain_tool_output_history = false",
		"swarm_name = child-host",
		"swarm" + "_mode = true",
		"child = true",
		"mode = tailscale",
		"tailscale_url = https://child.example.ts.net",
		"peer_transport_port = 7791",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}

	loaded, err := startupconfig.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !loaded.Child {
		t.Fatal("loaded.Child = false, want true")
	}
	migrated, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	if strings.Contains(string(migrated), "\n"+"startup"+"_mode =") {
		t.Fatalf("legacy startup mode should not be re-emitted: %q", string(migrated))
	}
	if strings.Contains(string(migrated), "\n"+"swarm"+"_mode =") {
		t.Fatalf("legacy swarm mode should not be re-emitted: %q", string(migrated))
	}
}

func TestDefault_DevDefaults(t *testing.T) {
	cfg := startupconfig.Default(filepath.Join(t.TempDir(), "swarm.conf"))
	if cfg.DevMode {
		t.Fatal("Default().DevMode = true, want false")
	}
	if cfg.DevRoot != "" {
		t.Fatalf("Default().DevRoot = %q, want empty", cfg.DevRoot)
	}
}
