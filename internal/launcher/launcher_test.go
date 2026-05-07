package launcher

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCompressedDesktopAssetsCreatesGzipFiles(t *testing.T) {
	dir := t.TempDir()
	assetsDir := filepath.Join(dir, "assets")
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	assetPath := filepath.Join(assetsDir, "index-abc.js")
	original := []byte("console.log('speed');")
	if err := os.WriteFile(assetPath, original, 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	if err := writeCompressedDesktopAssets(dir); err != nil {
		t.Fatalf("writeCompressedDesktopAssets: %v", err)
	}

	compressedPath := assetPath + ".gz"
	file, err := os.Open(compressedPath)
	if err != nil {
		t.Fatalf("open compressed asset: %v", err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gz.Close()
	decoded, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	if string(decoded) != string(original) {
		t.Fatalf("decoded = %q, want %q", string(decoded), string(original))
	}
}

func TestDevFrontendAssetsNeedRebuildDetectsSourceChanges(t *testing.T) {
	webDir := t.TempDir()
	webDistDir := filepath.Join(t.TempDir(), "dist")
	if err := os.MkdirAll(filepath.Join(webDir, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<div id=\"root\"></div>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "package.json"), []byte(`{"scripts":{"build":"vite build"}}`), 0o644); err != nil {
		t.Fatalf("write package: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "src", "main.tsx"), []byte("console.log('one')"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.MkdirAll(webDistDir, 0o755); err != nil {
		t.Fatalf("mkdir dist: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDistDir, "index.html"), []byte("built"), 0o644); err != nil {
		t.Fatalf("write dist index: %v", err)
	}
	fingerprint, err := frontendSourceFingerprint(webDir)
	if err != nil {
		t.Fatalf("frontendSourceFingerprint: %v", err)
	}
	if err := writeFrontendSourceFingerprint(webDistDir, fingerprint); err != nil {
		t.Fatalf("writeFrontendSourceFingerprint: %v", err)
	}

	profile := Profile{WebDir: webDir, WebDistDir: webDistDir}
	needs, err := DevFrontendAssetsNeedRebuild(profile)
	if err != nil {
		t.Fatalf("DevFrontendAssetsNeedRebuild: %v", err)
	}
	if needs {
		t.Fatal("DevFrontendAssetsNeedRebuild = true before source change")
	}

	if err := os.WriteFile(filepath.Join(webDir, "src", "main.tsx"), []byte("console.log('two')"), 0o644); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}
	needs, err = DevFrontendAssetsNeedRebuild(profile)
	if err != nil {
		t.Fatalf("DevFrontendAssetsNeedRebuild after source change: %v", err)
	}
	if !needs {
		t.Fatal("DevFrontendAssetsNeedRebuild = false after source change")
	}
}

func TestEnvMapIncludesInstalledLibDirInLDLibraryPath(t *testing.T) {
	systemRoot := t.TempDir()
	installRoot := filepath.Join(systemRoot, "share", "swarm")
	libRoot := filepath.Join(systemRoot, "lib", "swarm")
	t.Setenv("SWARM_SYSTEM_INSTALL_ROOT", installRoot)
	t.Setenv("SWARM_SYSTEM_BIN_DIR", filepath.Join(systemRoot, "bin"))
	t.Setenv("SWARM_SYSTEM_BINARY_DIR", filepath.Join(installRoot, "bin"))
	t.Setenv("SWARM_SYSTEM_LIBEXEC_DIR", filepath.Join(installRoot, "libexec"))
	t.Setenv("SWARM_SYSTEM_LIB_DIR", libRoot)
	t.Setenv("SWARM_SYSTEM_SHARE_DIR", filepath.Join(installRoot, "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(systemRoot, "config"))
	t.Setenv("HOME", filepath.Join(systemRoot, "home"))
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "state"))
	t.Setenv("CACHE_DIRECTORY", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "runtime"))
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))
	t.Setenv("LOGS_DIRECTORY", filepath.Join(t.TempDir(), "logs"))
	t.Setenv("LD_LIBRARY_PATH", "/opt/existing/lib")

	profile, err := LoadRuntimeProfile("main", nil)
	if err != nil {
		t.Fatalf("LoadRuntimeProfile: %v", err)
	}

	got := profile.EnvMap()["LD_LIBRARY_PATH"]
	want := libRoot + string(os.PathListSeparator) + "/opt/existing/lib"
	if got != want {
		t.Fatalf("LD_LIBRARY_PATH = %q, want %q", got, want)
	}
}

func TestLoadRuntimeProfileUsesStorageContractDaemonRoots(t *testing.T) {
	installRoot := t.TempDir()
	systemRoot := filepath.Join(installRoot, "system")
	systemInstallRoot := filepath.Join(systemRoot, "share", "swarm")
	dataRoot := filepath.Join(t.TempDir(), "data")
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	configRoot := filepath.Join(t.TempDir(), "config")
	logsRoot := filepath.Join(t.TempDir(), "logs")
	t.Setenv("SWARM_SYSTEM_INSTALL_ROOT", systemInstallRoot)
	t.Setenv("SWARM_SYSTEM_BIN_DIR", filepath.Join(systemRoot, "bin"))
	t.Setenv("SWARM_SYSTEM_BINARY_DIR", filepath.Join(systemInstallRoot, "bin"))
	t.Setenv("SWARM_SYSTEM_LIBEXEC_DIR", filepath.Join(systemInstallRoot, "libexec"))
	t.Setenv("SWARM_SYSTEM_LIB_DIR", filepath.Join(systemInstallRoot, "lib"))
	t.Setenv("SWARM_SYSTEM_SHARE_DIR", filepath.Join(systemInstallRoot, "share"))
	t.Setenv("HOME", filepath.Join(installRoot, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(installRoot, "xdg-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(installRoot, "xdg-config"))
	t.Setenv("STATE_DIRECTORY", dataRoot)
	t.Setenv("CACHE_DIRECTORY", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("RUNTIME_DIRECTORY", runtimeRoot)
	t.Setenv("CONFIGURATION_DIRECTORY", configRoot)
	t.Setenv("LOGS_DIRECTORY", logsRoot)

	profile, err := LoadRuntimeProfile("main", nil)
	if err != nil {
		t.Fatalf("LoadRuntimeProfile main: %v", err)
	}
	if profile.Startup.Path != filepath.Join(configRoot, "swarm.conf") {
		t.Fatalf("Startup.Path = %q", profile.Startup.Path)
	}
	if profile.DataDir != dataRoot {
		t.Fatalf("DataDir = %q, want %q", profile.DataDir, dataRoot)
	}
	if profile.StateRoot != runtimeRoot {
		t.Fatalf("StateRoot = %q, want %q", profile.StateRoot, runtimeRoot)
	}
	if profile.LogFile != filepath.Join(logsRoot, "swarmd.log") {
		t.Fatalf("LogFile = %q", profile.LogFile)
	}

	dev, err := LoadRuntimeProfile("dev", nil)
	if err != nil {
		t.Fatalf("LoadRuntimeProfile dev: %v", err)
	}
	if dev.DataDir != filepath.Join(dataRoot, "dev") {
		t.Fatalf("dev DataDir = %q", dev.DataDir)
	}
	if dev.StateRoot != filepath.Join(runtimeRoot, "dev") {
		t.Fatalf("dev StateRoot = %q", dev.StateRoot)
	}
}
