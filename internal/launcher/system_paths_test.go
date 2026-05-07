package launcher

import (
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/storagecontract"
)

func TestRenderSystemdServiceUnitIncludesStorageDirectives(t *testing.T) {
	root := t.TempDir()
	systemRoot := filepath.Join(root, "system")
	dataRoot := filepath.Join(root, "data")
	cacheRoot := filepath.Join(root, "cache")
	runtimeRoot := filepath.Join(root, "runtime")
	configRoot := filepath.Join(root, "config")
	logsRoot := filepath.Join(root, "logs")
	t.Setenv("SWARM_SYSTEM_BIN_DIR", filepath.Join(systemRoot, "bin"))

	t.Setenv("SUDO_UID", "1234")
	t.Setenv("SUDO_GID", "5678")

	unit := renderSystemdServiceUnit(storagecontract.Roots{
		DataDir:    dataRoot,
		CacheDir:   cacheRoot,
		RuntimeDir: runtimeRoot,
		ConfigDir:  configRoot,
		LogsDir:    logsRoot,
	})
	for _, needle := range []string{
		"StateDirectory=swarmd",
		"CacheDirectory=swarmd",
		"RuntimeDirectory=swarmd",
		"ConfigurationDirectory=swarmd",
		"LogsDirectory=swarmd",
		"User=1234",
		"Group=5678",
		"Environment=SWARM_SYSTEMD_SCOPE=system",
		"Environment=SWARM_SYSTEMD_UNIT=swarm.service",
		"Environment=SWARMD_DATA_DIR=" + dataRoot,
		"Environment=SWARMD_CACHE_DIR=" + cacheRoot,
		"Environment=SWARMD_RUNTIME_DIR=" + runtimeRoot,
		"Environment=SWARMD_CONFIG_DIR=" + configRoot,
		"Environment=SWARMD_LOG_DIR=" + logsRoot,
		"WorkingDirectory=/",
	} {
		if !strings.Contains(unit, needle) {
			t.Fatalf("unit missing %q\n%s", needle, unit)
		}
	}
	for _, forbidden := range []string{"$HOME", "XDG_", "/root", "/home/"} {
		if strings.Contains(unit, forbidden) {
			t.Fatalf("unit contains forbidden path/env %q\n%s", forbidden, unit)
		}
	}
}
