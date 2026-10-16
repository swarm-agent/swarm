package config

import (
	"path/filepath"
	"swarm-refactor/swarmtui/pkg/startupconfig"
	"testing"
)

// Purpose: Parse must carry both values of the startup cache switch to runtime
// configuration. A temporary canonical config tests resolution without a daemon.
func TestCloudBuildCacheResolutionSpeed(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
		dir := writeTestStartupConfig(t)
		cfg := startupconfig.Default(filepath.Join(dir, "swarm.conf"))
		cfg.CloudBuildCache = enabled
		if err := startupconfig.Write(cfg); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CONFIGURATION_DIRECTORY", dir)
		got, err := Parse(nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.CloudBuildCache != enabled {
			t.Fatalf("cache=%v want %v", got.CloudBuildCache, enabled)
		}
	}
}
