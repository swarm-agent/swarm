package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunDefaultsToNoService(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	installRoot := filepath.Join(root, "install")
	configDir := filepath.Join(root, "config")
	dataDir := filepath.Join(root, "data")
	cacheDir := filepath.Join(root, "cache")
	runtimeDir := filepath.Join(root, "run")
	logsDir := filepath.Join(root, "logs")
	for _, dir := range []string{binDir, installRoot, configDir, dataDir, cacheDir, runtimeDir, logsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	artifactRoot := filepath.Join(root, "artifact")
	writeSetupArtifact(t, artifactRoot, "v0.0.1")

	t.Setenv("SWARM_SYSTEM_INSTALL_ROOT", installRoot)
	t.Setenv("SWARM_SYSTEM_BIN_DIR", binDir)
	t.Setenv("SWARM_SYSTEM_BINARY_DIR", filepath.Join(installRoot, "bin"))
	t.Setenv("SWARM_SYSTEM_LIBEXEC_DIR", filepath.Join(installRoot, "libexec"))
	t.Setenv("SWARM_SYSTEM_LIB_DIR", filepath.Join(installRoot, "lib"))
	t.Setenv("SWARM_SYSTEM_SHARE_DIR", filepath.Join(installRoot, "share"))
	t.Setenv("CONFIGURATION_DIRECTORY", configDir)
	t.Setenv("STATE_DIRECTORY", dataDir)
	t.Setenv("CACHE_DIRECTORY", cacheDir)
	t.Setenv("RUNTIME_DIRECTORY", runtimeDir)
	t.Setenv("LOGS_DIRECTORY", logsDir)
	t.Setenv("SWARM_SKIP_SYSTEMD_UNIT", "1")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := run([]string{"--artifact-root", artifactRoot}); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(installRoot, "current", "bin", "swarmd")); err != nil {
		t.Fatalf("runtime not installed: %v", err)
	}
}

func writeSetupArtifact(t *testing.T, root, version string) {
	t.Helper()
	platformRoot := filepath.Join(root, "linux-amd64")
	for _, dir := range []string{filepath.Join(platformRoot, "root"), filepath.Join(platformRoot, "swarmd"), filepath.Join(root, "web")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir artifact dir: %v", err)
		}
	}
	for _, name := range []string{"swarm", "swarmdev", "rebuild", "swarmsetup", "swarmtui"} {
		if err := os.WriteFile(filepath.Join(platformRoot, "root", name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write root artifact: %v", err)
		}
	}
	for _, name := range []string{"swarmd", "swarmctl", "swarm-fff-search"} {
		if err := os.WriteFile(filepath.Join(platformRoot, "swarmd", name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write daemon artifact: %v", err)
		}
	}
	for path, content := range map[string]string{
		filepath.Join(platformRoot, "swarmd", "libfff_c.so"): "library",
		filepath.Join(root, "web", "index.html"):             "<html></html>",
		filepath.Join(root, "build-info.txt"):                "version=" + version + "\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write artifact file: %v", err)
		}
	}
}
