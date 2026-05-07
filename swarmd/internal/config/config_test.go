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
	home := "/test-home/swarmd-config-test"
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
