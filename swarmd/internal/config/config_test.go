package config

import (
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

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
