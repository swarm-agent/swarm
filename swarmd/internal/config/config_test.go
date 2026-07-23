package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

func TestParseDefaultsUseSystemStorageRoots(t *testing.T) {
	configDir := writeTestStartupConfig(t)
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("STATE_DIRECTORY", "")
	t.Setenv("RUNTIME_DIRECTORY", "")
	t.Setenv("CONFIGURATION_DIRECTORY", configDir)

	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.DataDir != "/var/lib/swarmd" {
		t.Fatalf("DataDir = %q, want /var/lib/swarmd", cfg.DataDir)
	}
	if cfg.DBPath != "/var/lib/swarmd/swarmd.pebble" {
		t.Fatalf("DBPath = %q, want /var/lib/swarmd/swarmd.pebble", cfg.DBPath)
	}
	if cfg.LockPath != "/run/swarmd/swarmd.lock" {
		t.Fatalf("LockPath = %q, want /run/swarmd/swarmd.lock", cfg.LockPath)
	}
	for _, path := range []string{cfg.DataDir, cfg.DBPath, cfg.LockPath} {
		if strings.HasPrefix(path, home) {
			t.Fatalf("storage path %q unexpectedly under HOME/XDG", path)
		}
	}
	if cfg.StartupCWD != home {
		t.Fatalf("StartupCWD = %q, want user home %q", cfg.StartupCWD, home)
	}
}

func TestParseRejectsNonLoopbackListen(t *testing.T) {
	for _, listen := range []string{"0.0.0.0:7781", "192.0.2.10:7781", "[::]:7781"} {
		t.Run(listen, func(t *testing.T) {
			configDir := writeTestStartupConfig(t)
			t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
			t.Setenv("CONFIGURATION_DIRECTORY", configDir)
			_, err := Parse([]string{"--listen", listen})
			if err == nil || !strings.Contains(err.Error(), "unsupported non-loopback --listen") {
				t.Fatalf("Parse() error = %v, want unsupported non-loopback", err)
			}
		})
	}
}

func TestParseExplicitCWDOverridesHomeDefault(t *testing.T) {
	configDir := writeTestStartupConfig(t)
	home := filepath.Join(t.TempDir(), "home")
	cwd := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("STATE_DIRECTORY", "")
	t.Setenv("RUNTIME_DIRECTORY", "")
	t.Setenv("CONFIGURATION_DIRECTORY", configDir)

	cfg, err := Parse([]string{"--cwd", cwd})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.StartupCWD != cwd {
		t.Fatalf("StartupCWD = %q, want explicit cwd %q", cfg.StartupCWD, cwd)
	}
}

func TestParseUsesSystemdStorageDirectoryOverrides(t *testing.T) {
	configDir := writeTestStartupConfig(t)
	t.Setenv("HOME", "/test-home/swarmd-config-test")
	t.Setenv("STATE_DIRECTORY", "/var/lib/swarmd-unit")
	t.Setenv("RUNTIME_DIRECTORY", "/run/swarmd-unit")
	t.Setenv("CONFIGURATION_DIRECTORY", configDir)

	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.DataDir != "/var/lib/swarmd-unit" {
		t.Fatalf("DataDir = %q, want /var/lib/swarmd-unit", cfg.DataDir)
	}
	if cfg.DBPath != "/var/lib/swarmd-unit/swarmd.pebble" {
		t.Fatalf("DBPath = %q, want /var/lib/swarmd-unit/swarmd.pebble", cfg.DBPath)
	}
	if cfg.LockPath != "/run/swarmd-unit/swarmd.lock" {
		t.Fatalf("LockPath = %q, want /run/swarmd-unit/swarmd.lock", cfg.LockPath)
	}
}

func TestParseRejectsUnsafeExplicitStorageFlags(t *testing.T) {
	configDir := writeTestStartupConfig(t)
	home := "/test-home/swarmd-config-test"
	t.Setenv("HOME", home)
	t.Setenv("STATE_DIRECTORY", "")
	t.Setenv("RUNTIME_DIRECTORY", "")
	t.Setenv("CONFIGURATION_DIRECTORY", configDir)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "relative data dir", args: []string{"--data-dir", "relative-data"}, want: "relative paths are forbidden"},
		{name: "relative db path", args: []string{"--db-path", "relative.pebble"}, want: "relative paths are forbidden"},
		{name: "relative lock path", args: []string{"--lock-path", "relative.lock"}, want: "relative paths are forbidden"},
		{name: "home data dir", args: []string{"--data-dir", filepath.Join(home, ".local", "share", "swarmd")}, want: "forbidden root"},
		{name: "home db path", args: []string{"--db-path", filepath.Join(home, ".local", "share", "swarmd", "swarmd.pebble")}, want: "forbidden root"},
		{name: "home lock path", args: []string{"--lock-path", filepath.Join(home, ".cache", "swarmd.lock")}, want: "forbidden root"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.args)
			if err == nil {
				t.Fatal("Parse() succeeded, want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse() error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestParseRejectsWorkspaceStorageFlags(t *testing.T) {
	configDir := writeTestStartupConfig(t)
	t.Setenv("STATE_DIRECTORY", "")
	t.Setenv("RUNTIME_DIRECTORY", "")
	t.Setenv("CONFIGURATION_DIRECTORY", configDir)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}

	_, err = Parse([]string{"--data-dir", filepath.Join(cwd, "swarmd-data")})
	if err == nil {
		t.Fatal("Parse() succeeded with workspace data-dir, want error")
	}
	if !strings.Contains(err.Error(), "forbidden root") {
		t.Fatalf("Parse() error = %v, want forbidden root", err)
	}
}

func writeTestStartupConfig(t *testing.T) string {
	t.Helper()
	configDir := filepath.Join(t.TempDir(), "config")
	path := filepath.Join(configDir, "swarm.conf")
	cfg := startupconfig.Default(path)
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	return configDir
}
