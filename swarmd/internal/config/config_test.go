package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseUsesPlatformRootsByDefault(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", configHome)

	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if cfg.DataDir != "/var/lib/swarmd" {
		t.Fatalf("DataDir = %q, want /var/lib/swarmd", cfg.DataDir)
	}
	if cfg.DBPath != filepath.Join("/var/lib/swarmd", "swarmd.pebble") {
		t.Fatalf("DBPath = %q", cfg.DBPath)
	}
	if cfg.LockPath != filepath.Join("/run/swarmd", "swarmd.lock") {
		t.Fatalf("LockPath = %q", cfg.LockPath)
	}
	if cfg.ConfigPath != filepath.Join(configHome, "swarm", "swarm.conf") {
		t.Fatalf("ConfigPath = %q", cfg.ConfigPath)
	}
}

func TestParseUsesSystemdRootOverrides(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "state")
	runtimeRoot := filepath.Join(t.TempDir(), "run")
	t.Setenv("STATE_DIRECTORY", dataRoot)
	t.Setenv("CACHE_DIRECTORY", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("RUNTIME_DIRECTORY", runtimeRoot)
	t.Setenv("LOGS_DIRECTORY", filepath.Join(t.TempDir(), "logs"))
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg-config"))

	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if cfg.DataDir != dataRoot {
		t.Fatalf("DataDir = %q, want %q", cfg.DataDir, dataRoot)
	}
	if cfg.DBPath != filepath.Join(dataRoot, "swarmd.pebble") {
		t.Fatalf("DBPath = %q", cfg.DBPath)
	}
	if cfg.LockPath != filepath.Join(runtimeRoot, "swarmd.lock") {
		t.Fatalf("LockPath = %q", cfg.LockPath)
	}
}

func TestParseRejectsUnsafeSystemdRootOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg-config"))
	t.Setenv("STATE_DIRECTORY", filepath.Join(home, ".local", "share", "swarmd"))

	if _, err := Parse(nil); err == nil {
		t.Fatal("Parse() accepted HOME-derived STATE_DIRECTORY")
	}
}

func TestParsePreservesExplicitRuntimePathFlags(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	originalCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	cwd := t.TempDir()
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalCWD); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	cfg, err := Parse([]string{"--data-dir", "data", "--db-path", "db/swarmd.pebble", "--lock-path", "run/swarmd.lock"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.DataDir != filepath.Join(cwd, "data") {
		t.Fatalf("DataDir = %q", cfg.DataDir)
	}
	if cfg.DBPath != filepath.Join(cwd, "db", "swarmd.pebble") {
		t.Fatalf("DBPath = %q", cfg.DBPath)
	}
	if cfg.LockPath != filepath.Join(cwd, "run", "swarmd.lock") {
		t.Fatalf("LockPath = %q", cfg.LockPath)
	}
}
