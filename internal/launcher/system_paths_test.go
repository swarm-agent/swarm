package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/storagecontract"
)

func TestEnsureDirsLocalPreservesExistingMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.Chmod(path, 0o750); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	if err := ensureDirsLocal([]systemDirSpec{{Path: path, Mode: 0o700, Owner: true}}); err != nil {
		t.Fatalf("ensureDirsLocal() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Fatalf("mode = %#o, want preserved 0o750", got)
	}
}

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
		"StateDirectoryMode=0700",
		"CacheDirectory=swarmd",
		"CacheDirectoryMode=0700",
		"RuntimeDirectory=swarmd",
		"RuntimeDirectoryMode=0700",
		"ConfigurationDirectory=swarmd",
		"ConfigurationDirectoryMode=0700",
		"LogsDirectory=swarmd",
		"LogsDirectoryMode=0755",
		"User=1234",
		"Group=5678",
		"Environment=SWARM_SYSTEMD_SCOPE=system",
		"Environment=SWARM_SYSTEMD_UNIT=swarm.service",
		"Environment=SWARMD_BASH_CONTAINMENT_POLICY=required",
		"Delegate=cpu memory pids",
		"PrivateTmp=yes",
		"TemporaryFileSystem=/tmp:rw,nosuid,nodev,size=8G,mode=1777",
		"TemporaryFileSystem=/var/tmp:rw,nosuid,nodev,size=8G,mode=1777",
		"MemoryMax=90%",
		"TasksMax=80%",
		"Environment=SWARMD_DATA_DIR=" + dataRoot,
		"Environment=SWARMD_CACHE_DIR=" + cacheRoot,
		"Environment=SWARMD_RUNTIME_DIR=" + runtimeRoot,
		"Environment=SWARMD_CONFIG_DIR=" + configRoot,
		"Environment=SWARMD_LOG_DIR=" + logsRoot,
		"ExecStart=" + filepath.Join(systemRoot, "bin", "swarm") + " main server run",
		"WorkingDirectory=/",
	} {
		if !strings.Contains(unit, needle) {
			t.Fatalf("unit missing %q\n%s", needle, unit)
		}
	}
	for _, forbidden := range []string{"$HOME", "XDG_", "/root", "/home/", "MemoryMax=2G", "TasksMax=512", "CPUQuota="} {
		if strings.Contains(unit, forbidden) {
			t.Fatalf("unit contains forbidden path/env %q\n%s", forbidden, unit)
		}
	}
}
