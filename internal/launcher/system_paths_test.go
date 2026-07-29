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

func TestEnsureDirsLocalRejectsUnsafeExistingTarget(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	if err := ensureDirsLocal([]systemDirSpec{{Path: link, Mode: 0o700, Owner: true}}); err == nil || !strings.Contains(err.Error(), "unsafe existing directory") {
		t.Fatalf("ensureDirsLocal() error = %v, want unsafe target rejection", err)
	}
}

func TestEnsureDirsLocalAcceptsCanonicalRuntimeDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	versionRoot := filepath.Join(root, "versions", "v1.2.3")
	if err := os.MkdirAll(filepath.Join(versionRoot, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(versionRoot, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	binLink := filepath.Join(root, "bin")
	if err := os.Symlink(filepath.Join(root, "current", "bin"), binLink); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARM_SYSTEM_INSTALL_ROOT", root)

	if err := ensureDirsLocal([]systemDirSpec{{Path: binLink, Mode: 0o755, Owner: true}}); err != nil {
		t.Fatalf("ensureDirsLocal() rejected canonical runtime link: %v", err)
	}
}

func TestEnsureDirsLocalRejectsEscapingRuntimeDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "versions"), 0o755); err != nil {
		t.Fatal(err)
	}
	escapeRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(escapeRoot, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(escapeRoot, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	binLink := filepath.Join(root, "bin")
	if err := os.Symlink(filepath.Join(root, "current", "bin"), binLink); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARM_SYSTEM_INSTALL_ROOT", root)

	if err := ensureDirsLocal([]systemDirSpec{{Path: binLink, Mode: 0o755, Owner: true}}); err == nil || !strings.Contains(err.Error(), "unsafe existing directory") {
		t.Fatalf("ensureDirsLocal() error = %v, want escaping current-runtime rejection", err)
	}
}

func TestInstallTextFileRejectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	realFile := filepath.Join(root, "real")
	if err := os.WriteFile(realFile, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realFile, link); err != nil {
		t.Fatal(err)
	}
	if err := installTextFileIfChanged(link, "replacement", 0o600, "test file"); err == nil || !strings.Contains(err.Error(), "unsafe existing") {
		t.Fatalf("installTextFileIfChanged() error = %v, want unsafe target rejection", err)
	}
	content, err := os.ReadFile(realFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original" {
		t.Fatalf("symlink target changed to %q", content)
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
	for _, forbidden := range []string{
		"$HOME", "XDG_", "/root", "/home/",
		"Delegate=", "PrivateTmp=", "TemporaryFileSystem=",
		"MemoryMax=", "TasksMax=", "CPUQuota=",
		"SWARMD_BASH_CONTAINMENT_POLICY", "SWARMD_COMMAND_TEMP_ROOT",
	} {
		if strings.Contains(unit, forbidden) {
			t.Fatalf("unit contains forbidden path/env %q\n%s", forbidden, unit)
		}
	}
}
