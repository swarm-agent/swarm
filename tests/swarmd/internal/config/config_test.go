package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "swarm/packages/swarmd/internal/config"
)

func TestParse_UsesSystemStorageDefaultsAndWritesNoStartupMode(t *testing.T) {
	home := "/test-home/swarmd-config-test-user"
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	configDir, stateDir, runtimeDir := setDaemonTestDirs(t)

	cfg, err := config.Parse(nil)
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}

	if cfg.DataDir != stateDir {
		t.Fatalf("cfg.DataDir = %q, want %q", cfg.DataDir, stateDir)
	}
	if cfg.DBPath != filepath.Join(stateDir, "swarmd.pebble") {
		t.Fatalf("cfg.DBPath = %q", cfg.DBPath)
	}
	if cfg.LockPath != filepath.Join(runtimeDir, "swarmd.lock") {
		t.Fatalf("cfg.LockPath = %q", cfg.LockPath)
	}
	if cfg.ConfigPath != filepath.Join(configDir, "swarm.conf") {
		t.Fatalf("cfg.ConfigPath = %q", cfg.ConfigPath)
	}
	if cfg.ListenAddr != "127.0.0.1:7781" {
		t.Fatalf("cfg.ListenAddr = %q", cfg.ListenAddr)
	}

	data, err := os.ReadFile(cfg.ConfigPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", cfg.ConfigPath, err)
	}
	text := string(data)
	if strings.Contains(text, "startup"+"_mode") {
		t.Fatalf("startup config should not include startup mode: %q", text)
	}
	if !strings.Contains(text, "dev_mode = false") || !strings.Contains(text, "mode = lan") {
		t.Fatalf("startup config missing expected no-mode defaults: %q", text)
	}
	for _, forbidden := range []string{home, filepath.Join(home, ".local"), filepath.Join(home, ".config")} {
		for _, path := range []string{cfg.DataDir, cfg.DBPath, cfg.LockPath, cfg.ConfigPath} {
			if strings.HasPrefix(path, forbidden) {
				t.Fatalf("path %q under forbidden prefix %q", path, forbidden)
			}
		}
	}
}
func TestParse_BypassPermissionsDefaultsFalse(t *testing.T) {
	setDaemonTestDirs(t)
	cfg, err := config.Parse(nil)
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	if cfg.BypassPermissions {
		t.Fatal("BypassPermissions = true, want false by default")
	}
}

func TestParse_BypassPermissionsFromCLI(t *testing.T) {
	setDaemonTestDirs(t)
	cfg, err := config.Parse([]string{"--bypass-permissions"})
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	if !cfg.BypassPermissions {
		t.Fatal("BypassPermissions = false, want true from CLI")
	}
}

func TestParse_RetainToolOutputHistoryDefaultsFalse(t *testing.T) {
	setDaemonTestDirs(t)
	cfg, err := config.Parse(nil)
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	if cfg.RetainToolOutputHistory {
		t.Fatal("RetainToolOutputHistory = true, want false by default")
	}
}

func TestParse_RetainToolOutputHistoryFromStartupConfig(t *testing.T) {
	configDir, _, _ := setDaemonTestDirs(t)

	configPath := filepath.Join(configDir, "swarm.conf")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	content := "mode = lan\nhost = 127.0.0.1\nport = 7781\ndesktop_port = 5555\nretain_tool_output_history = true\n"
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.Parse(nil)
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	if !cfg.RetainToolOutputHistory {
		t.Fatal("RetainToolOutputHistory = false, want true from startup config")
	}
}

func setDaemonTestDirs(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "etc", "swarmd")
	stateDir := filepath.Join(root, "var", "lib", "swarmd")
	runtimeDir := filepath.Join(root, "run", "swarmd")
	t.Setenv("CONFIGURATION_DIRECTORY", configDir)
	t.Setenv("STATE_DIRECTORY", stateDir)
	t.Setenv("RUNTIME_DIRECTORY", runtimeDir)
	return configDir, stateDir, runtimeDir
}
